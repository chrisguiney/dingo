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
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/dingo/ledger/governance"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// GovAction names an action by transaction hash and index.
type GovAction struct {
	TxHash []byte
	Index  uint32
}

// GovProposal is a governance proposal row. Cbor is the GovAction CBOR. Parent
// is the previous action of the same purpose, nil for none.
type GovProposal struct {
	ID            GovAction
	ActionType    uint8
	Cbor          []byte
	Parent        *GovAction
	ProposedEpoch uint64
	ExpiresEpoch  uint64
	Deposit       uint64
	Return        Credential
	// Ratified marks a proposal an earlier boundary ratified, which the next
	// boundary enacts.
	Ratified bool
}

// GovVote is one vote on the proposal at index Proposal. Role is 0 for the
// constitutional committee, 1 for a DRep and 2 for a stake pool, whose Key is
// the pool id.
type GovVote struct {
	Proposal int
	Role     uint8
	Tag      uint8
	Key      []byte
	Vote     uint8
}

// GovDRepStake is the stake delegated to one DRep.
type GovDRepStake struct {
	Delegation Delegation
	Stake      uint64
}

// GovState is every row a governance boundary reads.
type GovState struct {
	Proposals []GovProposal
	Votes     []GovVote
	// Roots are the last enacted action of each purpose, written as enacted
	// proposals of the given action type.
	Roots []GovProposal
	// Dreps are the registered DReps. DRepStake is their delegated stake,
	// seeded as delegating accounts that hold no other stake.
	Dreps     []DRep
	DRepStake []GovDRepStake
	// Accounts are the registered reward accounts. They are stored without a
	// delegation, so they add no DRep stake.
	Accounts  []Account
	Committee []CommitteeMember
	// Quorum is the enacted committee threshold, nil for none.
	Quorum   *big.Rat
	Treasury uint64
	// Constitution is the enacted constitution, nil for none.
	Constitution *models.Constitution
	// SPO is the pool stake of the boundary epoch.
	SPO     *governance.SPOVotingState
	PParams *conway.ConwayProtocolParameters
}

// boundarySlot is the slot the boundary under test records its effects at.
// The seeded rows sit at slot 1, before it.
const (
	seedSlot     = 1
	boundarySlot = 1000
)

func (o *Oracle) seedGov(
	ctx context.Context, txn *database.Txn, seq *uint64, st GovState,
) error {
	if err := o.seedDRepStake(ctx, txn, seq, DRepStakeDistributionInput{
		Accounts: st.Accounts, Dreps: st.Dreps,
	}); err != nil {
		return err
	}
	for i, s := range st.DRepStake {
		*seq++
		key := txHash(*seq)[4:]
		if err := o.seedDRepStake(ctx, txn, seq, DRepStakeDistributionInput{
			Accounts: []Account{{Tag: 0, Key: key, Reward: s.Stake, Active: true, Delegation: s.Delegation}},
		}); err != nil {
			return fmt.Errorf("seed drep stake %d: %w", i, err)
		}
	}
	if err := o.seedCommittee(ctx, txn, seq, st.Committee); err != nil {
		return err
	}
	if st.Quorum != nil {
		if err := o.db.SetCommitteeQuorum(ctx, st.Quorum, seedSlot, txn); err != nil {
			return fmt.Errorf("seed quorum: %w", err)
		}
	}
	if err := o.db.Metadata().SetNetworkState(st.Treasury, 0, seedSlot, txn.Metadata()); err != nil {
		return fmt.Errorf("seed treasury: %w", err)
	}
	if st.Constitution != nil {
		c := *st.Constitution
		c.AddedSlot = seedSlot
		if err := o.db.SetConstitution(&c, txn); err != nil {
			return fmt.Errorf("seed constitution: %w", err)
		}
	}
	for i, r := range st.Roots {
		row := proposalRow(r, uint32(i)) //nolint:gosec // test-sized index
		epoch, slot := uint64(0), uint64(seedSlot)
		row.RatifiedEpoch, row.RatifiedSlot = &epoch, &slot
		row.EnactedEpoch, row.EnactedSlot = &epoch, &slot
		if err := o.db.SetGovernanceProposal(ctx, row, txn); err != nil {
			return fmt.Errorf("seed root: %w", err)
		}
	}
	rows := make([]*models.GovernanceProposal, len(st.Proposals))
	for i, p := range st.Proposals {
		row := proposalRow(p, uint32(len(st.Roots)+i)) //nolint:gosec // test-sized index
		if p.Ratified {
			epoch, slot := p.ProposedEpoch, uint64(seedSlot)
			row.RatifiedEpoch, row.RatifiedSlot = &epoch, &slot
		}
		if err := o.db.SetGovernanceProposal(ctx, row, txn); err != nil {
			return fmt.Errorf("seed proposal: %w", err)
		}
		rows[i] = row
	}
	for _, v := range st.Votes {
		if v.Proposal < 0 || v.Proposal >= len(rows) {
			return fmt.Errorf("vote on proposal %d of %d", v.Proposal, len(rows))
		}
		vote := voteRow(v, rows[v.Proposal].ID)
		if err := o.db.SetGovernanceVote(ctx, vote, txn); err != nil {
			return fmt.Errorf("seed vote: %w", err)
		}
	}
	return nil
}

// voteRow is the row for v on the proposal row with id proposalID.
func voteRow(v GovVote, proposalID uint) *models.GovernanceVote {
	return &models.GovernanceVote{
		ProposalID:         proposalID,
		VoterType:          v.Role,
		VoterCredentialTag: v.Tag,
		VoterCredential:    v.Key,
		Vote:               v.Vote,
		AddedSlot:          seedSlot,
	}
}

// drepRow is the row for a registered DRep.
func drepRow(d DRep) *models.Drep {
	return &models.Drep{
		CredentialTag: d.Tag,
		Credential:    d.Key,
		ExpiryEpoch:   d.ExpiryEpoch,
		Active:        d.Active,
	}
}

func proposalRow(p GovProposal, order uint32) *models.GovernanceProposal {
	header := byte(0xe0)
	if p.Return.Tag == 1 {
		header = 0xf0
	}
	row := &models.GovernanceProposal{
		TxHash:        p.ID.TxHash,
		ActionIndex:   p.ID.Index,
		ActionType:    p.ActionType,
		GovActionCbor: p.Cbor,
		ProposedEpoch: p.ProposedEpoch,
		ExpiresEpoch:  p.ExpiresEpoch,
		Deposit:       p.Deposit,
		ReturnAddress: append([]byte{header}, p.Return.Key...),
		AddedSlot:     seedSlot,
		TxIndex:       &order,
	}
	if p.Parent != nil {
		idx := p.Parent.Index
		row.ParentTxHash, row.ParentActionIdx = p.Parent.TxHash, &idx
	}
	return row
}

// epochInput is the production boundary input for the boundary into epoch.
func (o *Oracle) epochInput(
	adapter dbadapter.Adapter, txn *database.Txn, st GovState, epoch uint64,
) *governance.EpochInput {
	return &governance.EpochInput{
		DB:                      adapter,
		Txn:                     dbadapter.Txn(txn),
		PrevEpoch:               epoch - 1,
		NewEpoch:                epoch,
		BoundarySlot:            boundarySlot + epoch,
		PrevEpochStartSlot:      seedSlot,
		PParams:                 st.PParams,
		UpdateFn:                eras.PParamsUpdateConway,
		CurrentBoundarySPOState: st.SPO,
	}
}

// RatifyOutput is RATIFY's verdict on the active proposals: the accepted
// actions in the order they were accepted, the expired ones, and whether a
// delaying action was accepted.
type RatifyOutput struct {
	Ratified []GovAction
	Expired  []GovAction
	Delayed  bool
}

// Ratify seeds st and runs the production boundary into epoch up to RATIFY,
// then reads the verdict without applying it.
func (o *Oracle) Ratify(ctx context.Context, st GovState, epoch uint64) (*RatifyOutput, error) {
	if epoch == 0 {
		return nil, errors.New("dxoracle: no boundary into epoch 0")
	}
	var out *RatifyOutput
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedGov(ctx, txn, seq, st); err != nil {
			return err
		}
		in := o.epochInput(adapter, txn, st, epoch)
		in.DeferRatification = true
		res, err := governance.ProcessEpoch(ctx, in)
		if err != nil {
			return err
		}
		if res.Ratification == nil {
			return errors.New("dxoracle: boundary deferred no ratification")
		}
		decision, err := res.Ratification.Decide(ctx, dbadapter.Txn(txn))
		if err != nil {
			return err
		}
		out = &RatifyOutput{Delayed: decision.BoundarySnapshot.Delayed}
		for _, p := range decision.Ratified {
			out.Ratified = append(out.Ratified, GovAction{p.TxHash, p.ActionIndex})
		}
		for _, p := range decision.Expired {
			out.Expired = append(out.Expired, GovAction{p.TxHash, p.ActionIndex})
		}
		return nil
	})
	return out, err
}

// AccountReward is a reward account's balance.
type AccountReward struct {
	Credential Credential
	Reward     uint64
}

// EnactOutput is what enacting one ratified action changed.
type EnactOutput struct {
	// Accounts are the registered accounts whose balance changed, with the new
	// balance.
	Accounts []AccountReward
	// Treasury is the treasury after.
	Treasury uint64
	// PParams is the protocol parameters after.
	PParams lcommon.ProtocolParameters
	// Committee is the enacted committee after, with its quorum; Members is
	// nil when there is none.
	Committee    []*models.CommitteeMember
	Quorum       *big.Rat
	Constitution *models.Constitution
	// Roots are the last enacted actions after, by action type.
	Roots map[uint8]GovAction
}

// Enact seeds st, whose proposal at index 0 is ratified, and runs the
// production boundary into epoch, which enacts it, then reads the rows
// enactment wrote.
func (o *Oracle) Enact(ctx context.Context, st GovState, epoch uint64) (*EnactOutput, error) {
	if epoch == 0 {
		return nil, errors.New("dxoracle: no boundary into epoch 0")
	}
	var out *EnactOutput
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedGov(ctx, txn, seq, st); err != nil {
			return err
		}
		res, err := governance.ProcessEpoch(ctx, o.epochInput(adapter, txn, st, epoch))
		if err != nil {
			return err
		}
		out = &EnactOutput{PParams: res.UpdatedPParams}
		if out.Accounts, err = o.changedAccounts(ctx, adapter, txn, st.Accounts); err != nil {
			return err
		}
		if out.Treasury, err = treasuryOf(adapter, txn); err != nil {
			return err
		}
		if out.Committee, err = o.db.GetCommitteeMembers(ctx, txn); err != nil {
			return err
		}
		if out.Quorum, err = o.db.GetCommitteeQuorum(ctx, txn); err != nil {
			return err
		}
		if out.Constitution, err = o.db.GetConstitution(txn); err != nil {
			return err
		}
		out.Roots = map[uint8]GovAction{}
		for _, purpose := range [][]uint8{
			{uint8(lcommon.GovActionTypeParameterChange)},
			{uint8(lcommon.GovActionTypeHardForkInitiation)},
			{uint8(lcommon.GovActionTypeNoConfidence), uint8(lcommon.GovActionTypeUpdateCommittee)},
			{uint8(lcommon.GovActionTypeNewConstitution)},
		} {
			root, err := adapter.GetLastEnactedGovernanceProposal(ctx, purpose, dbadapter.Txn(txn))
			if err != nil {
				return err
			}
			if root != nil {
				out.Roots[purpose[0]] = GovAction{root.TxHash, root.ActionIndex}
			}
		}
		return nil
	})
	return out, err
}

// ExpiryOutput is what the boundary that expires proposals removed and paid
// back.
type ExpiryOutput struct {
	// Expired are the proposals removed as expired, with the subtrees that
	// leave with them.
	Expired []GovAction
	// Accounts are the registered accounts whose balance changed (the
	// refunds), with the new balance.
	Accounts []AccountReward
	// Unclaimed is the deposit total whose return account is not registered,
	// which goes to the treasury.
	Unclaimed uint64
}

// ProposalExpiry seeds st and runs the production boundaries that expire and
// then drop the proposals whose expiry is below epoch, reading the removed
// proposals, the refunds and the treasury. Dingo marks a proposal expired at
// the boundary into epoch and refunds it at the next one.
func (o *Oracle) ProposalExpiry(ctx context.Context, st GovState, epoch uint64) (*ExpiryOutput, error) {
	if epoch == 0 {
		return nil, errors.New("dxoracle: no boundary into epoch 0")
	}
	var out *ExpiryOutput
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedGov(ctx, txn, seq, st); err != nil {
			return err
		}
		// The expired set is what the first boundary removes; the second
		// boundary also expires proposals whose expiry equals epoch, which
		// the reference's boundary does not.
		if _, err := governance.ProcessEpoch(ctx, o.epochInput(adapter, txn, st, epoch)); err != nil {
			return err
		}
		out = &ExpiryOutput{}
		all, err := adapter.GetAllGovernanceProposals(ctx, dbadapter.Txn(txn))
		if err != nil {
			return err
		}
		for _, p := range all {
			if p.ExpiredEpoch != nil || p.DroppedEpoch != nil || p.DeletedSlot != nil {
				out.Expired = append(out.Expired, GovAction{p.TxHash, p.ActionIndex})
			}
		}
		if _, err := governance.ProcessEpoch(ctx, o.epochInput(adapter, txn, st, epoch+1)); err != nil {
			return err
		}
		sort.Slice(out.Expired, func(i, j int) bool {
			a, b := out.Expired[i], out.Expired[j]
			if c := bytes.Compare(a.TxHash, b.TxHash); c != 0 {
				return c < 0
			}
			return a.Index < b.Index
		})
		if out.Accounts, err = o.changedAccounts(ctx, adapter, txn, st.Accounts); err != nil {
			return err
		}
		treasury, err := treasuryOf(adapter, txn)
		if err != nil {
			return err
		}
		out.Unclaimed = treasury - st.Treasury
		return nil
	})
	return out, err
}

func treasuryOf(adapter dbadapter.Adapter, txn *database.Txn) (uint64, error) {
	state, err := adapter.GetNetworkState(dbadapter.Txn(txn))
	if err != nil {
		return 0, err
	}
	if state == nil {
		return 0, nil
	}
	return uint64(state.Treasury), nil
}

// changedAccounts lists the seeded accounts whose reward balance differs from
// the seeded one.
func (o *Oracle) changedAccounts(
	ctx context.Context, adapter dbadapter.Adapter, txn *database.Txn, seeded []Account,
) ([]AccountReward, error) {
	var out []AccountReward
	for _, a := range seeded {
		row, err := adapter.GetAccountByCredential(ctx, a.Tag, a.Key, true, dbadapter.Txn(txn))
		if err != nil {
			return nil, fmt.Errorf("read account: %w", err)
		}
		if row == nil || uint64(row.Reward) == a.Reward {
			continue
		}
		out = append(out, AccountReward{Credential: Credential{Tag: a.Tag, Key: a.Key}, Reward: uint64(row.Reward)})
	}
	return out, nil
}
