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
	"fmt"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/ledger/governance"
)

// Credential is a tagged credential: Tag is 0 for a key hash and 1 for a
// script hash. Key is the 28-byte hash.
type Credential struct {
	Tag uint8
	Key []byte
}

// DelegationType selects an account's DRep delegation.
type DelegationType uint8

// Delegation types. DelegateNone is the zero value.
const (
	DelegateNone DelegationType = iota
	DelegateKeyHash
	DelegateScriptHash
	DelegateAlwaysAbstain
	DelegateAlwaysNoConfidence
)

// Delegation is an account's DRep delegation; Key is used for the key-hash and
// script-hash types.
type Delegation struct {
	Type DelegationType
	Key  []byte
}

// setDelegation stores d as acct's DRep delegation.
func setDelegation(acct *models.Account, d Delegation) error {
	switch d.Type {
	case DelegateNone:
	case DelegateKeyHash:
		acct.DrepType, acct.Drep = models.DrepTypeAddrKeyHash, d.Key
	case DelegateScriptHash:
		acct.DrepType, acct.Drep = models.DrepTypeScriptHash, d.Key
	case DelegateAlwaysAbstain:
		acct.DrepType = models.DrepTypeAlwaysAbstain
	case DelegateAlwaysNoConfidence:
		acct.DrepType = models.DrepTypeAlwaysNoConfidence
	default:
		return fmt.Errorf("dxoracle: unknown delegation type %d", d.Type)
	}
	return nil
}

// delegationOf is the delegation an account row holds.
func delegationOf(acct *models.Account) (Delegation, error) {
	switch acct.DrepType {
	case models.DrepTypeAddrKeyHash:
		if len(acct.Drep) > 0 {
			return Delegation{Type: DelegateKeyHash, Key: append([]byte(nil), acct.Drep...)}, nil
		}
		return Delegation{}, nil
	case models.DrepTypeScriptHash:
		return Delegation{Type: DelegateScriptHash, Key: append([]byte(nil), acct.Drep...)}, nil
	case models.DrepTypeAlwaysAbstain:
		return Delegation{Type: DelegateAlwaysAbstain}, nil
	case models.DrepTypeAlwaysNoConfidence:
		return Delegation{Type: DelegateAlwaysNoConfidence}, nil
	}
	return Delegation{}, fmt.Errorf("dxoracle: unknown stored DRep type %d", acct.DrepType)
}

// Account is a stake (reward) account row.
type Account struct {
	Tag             uint8
	Key             []byte
	Reward          uint64
	Active          bool
	Delegation      Delegation
	ExpirationEpoch uint64
}

// Utxo is a UTxO attributed to a stake credential. Deleted marks it spent.
type Utxo struct {
	Tag     uint8
	Key     []byte
	Amount  uint64
	Deleted bool
}

// DRep is a registered DRep row.
type DRep struct {
	Tag         uint8
	Key         []byte
	ExpiryEpoch uint64
	Active      bool
}

// Proposal is a governance proposal whose deposit is returned to ReturnAccount.
type Proposal struct {
	Deposit       uint64
	ReturnAccount Credential
	ProposedEpoch uint64
	ExpiresEpoch  uint64
}

// DRepStakeDistributionInput is every row DRepStakeDistribution reads.
// ActiveProposalEpoch selects which proposals count as active.
type DRepStakeDistributionInput struct {
	Accounts            []Account
	Utxos               []Utxo
	Dreps               []DRep
	Proposals           []Proposal
	ActiveProposalEpoch uint64
}

// DRepStakeDistribution seeds in into the scratch database, runs the
// production boundary DRep stake distribution loader and discards the seeded
// rows. Nothing seeded by one call is visible to another.
func (o *Oracle) DRepStakeDistribution(
	ctx context.Context,
	in DRepStakeDistributionInput,
) ([]models.DRepStakeDistributionEntry, error) {
	var out []models.DRepStakeDistributionEntry
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedDRepStake(ctx, txn, seq, in); err != nil {
			return err
		}
		var err error
		out, err = governance.LoadDRepStakeDistribution(
			ctx, adapter, dbadapter.Txn(txn), in.ActiveProposalEpoch,
		)
		return err
	})
	return out, err
}

// seedDRepStake writes in through database.Database.CreateAccount,
// CreateDrep and SetGovernanceProposal, and the metadata store's CreateUtxo,
// for which Database has no wrapper.
func (o *Oracle) seedDRepStake(
	ctx context.Context,
	txn *database.Txn,
	seq *uint64,
	in DRepStakeDistributionInput,
) error {
	for _, a := range in.Accounts {
		acct := &models.Account{
			CredentialTag:   a.Tag,
			StakingKey:      a.Key,
			Reward:          types.Uint64(a.Reward),
			Active:          a.Active,
			ExpirationEpoch: a.ExpirationEpoch,
		}
		if err := setDelegation(acct, a.Delegation); err != nil {
			return err
		}
		if err := o.db.CreateAccount(ctx, txn, acct); err != nil {
			return fmt.Errorf("seed account: %w", err)
		}
	}
	for _, u := range in.Utxos {
		*seq++
		row := &models.Utxo{
			TxId:          txHash(*seq),
			OutputIdx:     0,
			StakingKey:    u.Key,
			CredentialTag: u.Tag,
			Amount:        types.Uint64(u.Amount),
		}
		if u.Deleted {
			row.DeletedSlot = 1
		}
		if err := o.db.Metadata().CreateUtxo(txn.Metadata(), row); err != nil {
			return fmt.Errorf("seed utxo: %w", err)
		}
	}
	for _, d := range in.Dreps {
		drep := &models.Drep{
			CredentialTag: d.Tag,
			Credential:    d.Key,
			ExpiryEpoch:   d.ExpiryEpoch,
			Active:        d.Active,
		}
		if err := o.db.CreateDrep(ctx, txn, drep); err != nil {
			return fmt.Errorf("seed drep: %w", err)
		}
	}
	for i, p := range in.Proposals {
		header := byte(0xe0)
		if p.ReturnAccount.Tag == 1 {
			header = 0xf0
		}
		*seq++
		prop := &models.GovernanceProposal{
			TxHash:        txHash(*seq),
			ActionIndex:   uint32(i), //nolint:gosec // test-sized index
			ProposedEpoch: p.ProposedEpoch,
			ExpiresEpoch:  p.ExpiresEpoch,
			Deposit:       p.Deposit,
			ReturnAddress: append([]byte{header}, p.ReturnAccount.Key...),
		}
		if err := o.db.SetGovernanceProposal(ctx, prop, txn); err != nil {
			return fmt.Errorf("seed proposal: %w", err)
		}
	}
	return nil
}
