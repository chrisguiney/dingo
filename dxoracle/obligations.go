// Copyright 2026 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dxoracle

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// Obligations is the lovelace dingo's deposit rows hold: stake key, pool,
// DRep and governance proposal deposits.
type Obligations struct {
	Stake, Pool, DRep, Proposal, Total uint64
}

// ComputeObligations sums the stored deposit rows. A pool row marked Future is
// a re-registration awaiting the next boundary; its deposit is the one the
// current registration already holds, so it is not counted again.
func ComputeObligations(
	accounts []CertAccount,
	pools []CertPool,
	dreps []CertDRep,
	proposals []*models.GovernanceProposal,
) (Obligations, error) {
	var o Obligations
	var err error
	for _, a := range accounts {
		o.Stake, err = addDeposit(o.Stake, a.Deposit)
		if err != nil {
			return Obligations{}, err
		}
	}
	for _, p := range pools {
		if p.Future {
			continue
		}
		o.Pool, err = addDeposit(o.Pool, p.Deposit)
		if err != nil {
			return Obligations{}, err
		}
	}
	for _, d := range dreps {
		o.DRep, err = addDeposit(o.DRep, d.Deposit)
		if err != nil {
			return Obligations{}, err
		}
	}
	for _, p := range proposals {
		o.Proposal, err = addDeposit(o.Proposal, p.Deposit)
		if err != nil {
			return Obligations{}, err
		}
	}
	for _, part := range []uint64{o.Stake, o.Pool, o.DRep, o.Proposal} {
		o.Total, err = addDeposit(o.Total, part)
		if err != nil {
			return Obligations{}, err
		}
	}
	return o, nil
}

// ErrDepositOverflow reports deposits whose sum does not fit a uint64.
var ErrDepositOverflow = errors.New("dxoracle: deposits exceed Word64")

func addDeposit(sum, n uint64) (uint64, error) {
	if sum > math.MaxUint64-n {
		return 0, ErrDepositOverflow
	}
	return sum + n, nil
}

// ObligationsInput is the deposit-bearing state to store.
type ObligationsInput struct {
	Accounts  []CertAccount
	Pools     []CertPool
	DReps     []CertDRep
	Proposals []Proposal
}

// StoredObligations stores in through dingo's own write paths and returns
// ComputeObligations over the rows read back.
func StoredObligations(
	ctx context.Context, certs *CertOracle, props *Oracle, in ObligationsInput,
) (Obligations, error) {
	// Seeding with no certificate stores the rows through the ledger-state
	// import writers and reads them back through the production readers.
	pp := &conway.ConwayProtocolParameters{}
	pp.ProtocolVersion.Major = 10
	res, err := certs.Run(ctx, CertRunInput{
		Slot: 100, Epoch: 5, Params: pp, Tx: &conway.ConwayTransaction{},
		State: CertTables{Accounts: in.Accounts, Pools: in.Pools, DReps: in.DReps},
	})
	if err != nil {
		return Obligations{}, err
	}
	if len(res.Failures) > 0 || res.After == nil {
		return Obligations{}, fmt.Errorf("dxoracle: storing deposits failed: %v", res.Failures)
	}
	proposals, err := props.storedProposals(ctx, in.Proposals)
	if err != nil {
		return Obligations{}, err
	}
	return ComputeObligations(res.After.Accounts, res.After.Pools, res.After.DReps, proposals)
}

// storedProposals stores proposals and reads back the proposals set, the rows
// whose deposits the ledger holds.
func (o *Oracle) storedProposals(
	ctx context.Context, proposals []Proposal,
) ([]*models.GovernanceProposal, error) {
	var out []*models.GovernanceProposal
	err := o.withScratch(ctx, func(
		_ dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedDRepStake(ctx, txn, seq, DRepStakeDistributionInput{
			Proposals: proposals,
		}); err != nil {
			return err
		}
		var err error
		out, err = o.db.GetGovernanceProposalSet(ctx, txn)
		return err
	})
	return out, err
}
