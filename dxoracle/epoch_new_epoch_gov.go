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
	"os"
	"sort"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger"
	"github.com/blinklabs-io/dingo/ledger/governance"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// ProposalMarks is the lifecycle a RATIFY decision left on one proposal.
type ProposalMarks struct {
	RatifiedEpoch, RatifiedSlot *uint64
	ExpiredEpoch, ExpiredSlot   *uint64
}

// PriorBoundary is the RATIFY decision of the boundary before the one under
// test: the marks the node holds on each proposal when the next boundary
// enacts the ratified ones and drops the expired ones.
type PriorBoundary struct {
	// Marks is parallel to GovState.Proposals.
	Marks []ProposalMarks
	// Ratified are the accepted actions in acceptance order, Expired the
	// actions the decision classed as expired, not their descendants.
	Ratified []GovAction
	Expired  []GovAction
}

// PriorBoundary seeds st and runs the production boundary into epoch with its
// RATIFY decided and applied, then reads the marks that decision left on
// st.Proposals. It is the deferred RATIFY a node runs at one boundary and
// consumes at the next.
func (o *Oracle) PriorBoundary(ctx context.Context, st GovState, epoch uint64) (*PriorBoundary, error) {
	if epoch == 0 {
		return nil, errors.New("dxoracle: no boundary into epoch 0")
	}
	var out *PriorBoundary
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
		if _, err := res.Ratification.Apply(ctx, decision, dbadapter.Txn(txn)); err != nil {
			return err
		}
		out = &PriorBoundary{}
		for _, p := range decision.Ratified {
			out.Ratified = append(out.Ratified, GovAction{p.TxHash, p.ActionIndex})
		}
		for _, p := range decision.Expired {
			out.Expired = append(out.Expired, GovAction{p.TxHash, p.ActionIndex})
		}
		all, err := adapter.GetAllGovernanceProposals(ctx, dbadapter.Txn(txn))
		if err != nil {
			return err
		}
		byID := map[string]*models.GovernanceProposal{}
		for _, p := range all {
			byID[proposalKey(p.TxHash, p.ActionIndex)] = p
		}
		for _, p := range st.Proposals {
			row := byID[proposalKey(p.ID.TxHash, p.ID.Index)]
			if row == nil {
				return fmt.Errorf("dxoracle: proposal %x#%d not stored", p.ID.TxHash, p.ID.Index)
			}
			out.Marks = append(out.Marks, ProposalMarks{
				RatifiedEpoch: row.RatifiedEpoch, RatifiedSlot: row.RatifiedSlot,
				ExpiredEpoch: row.ExpiredEpoch, ExpiredSlot: row.ExpiredSlot,
			})
		}
		return nil
	})
	return out, err
}

func proposalKey(tx []byte, idx uint32) string { return fmt.Sprintf("%x#%d", tx, idx) }

// NewEpochGovOutput is what the governance side of the boundary left behind.
type NewEpochGovOutput struct {
	// Enacted are the actions the boundary enacted, in the order RATIFY
	// accepted them; Expired the actions it dropped as expired.
	Enacted, Expired []GovAction
	// Removed are the proposals that left the set at the boundary, with the
	// subtrees that left with them, in canonical order.
	Removed []GovAction
	// Dreps are the input DReps with the expiry they hold after.
	Dreps []DRep
	// Committee is the enacted committee after, with its quorum; Quorum is
	// nil for none. Constitution is the enacted constitution, nil for none.
	Committee    []*models.CommitteeMember
	Quorum       *big.Rat
	Constitution *models.Constitution
	// Roots are the last enacted actions after, by action type.
	Roots map[uint8]GovAction
}

// priorBoundary runs PriorBoundary on a database of its own.
func priorBoundary(ctx context.Context, st GovState, epoch uint64) (*PriorBoundary, error) {
	dir, err := os.MkdirTemp("", "dxoracle-prior-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	o, err := Open(dir) //nolint:contextcheck // Open takes no context
	if err != nil {
		return nil, err
	}
	defer func() { _ = o.Close() }() //nolint:contextcheck // Close takes no context
	return o.PriorBoundary(ctx, st, epoch)
}

// govChanges stages the governance rows of g as a node that ran the boundary
// before the one under test holds them: the same rows seedGov writes, with the marks of
// the prior decision on the proposals.
func govChanges(changes *ledger.LedgerChanges, g GovState, prior *PriorBoundary) {
	for i, r := range g.Roots {
		row := proposalRow(r, uint32(i)) //nolint:gosec // test-sized index
		e, s := uint64(0), uint64(seedSlot)
		row.RatifiedEpoch, row.RatifiedSlot = &e, &s
		row.EnactedEpoch, row.EnactedSlot = &e, &s
		changes.Proposals = append(changes.Proposals, ledger.ProposalChange{Slot: seedSlot, Proposal: *row})
	}
	for i, p := range g.Proposals {
		row := proposalRow(p, uint32(len(g.Roots)+i)) //nolint:gosec // test-sized index
		m := prior.Marks[i]
		row.RatifiedEpoch, row.RatifiedSlot = m.RatifiedEpoch, m.RatifiedSlot
		row.ExpiredEpoch, row.ExpiredSlot = m.ExpiredEpoch, m.ExpiredSlot
		changes.Proposals = append(changes.Proposals, ledger.ProposalChange{Slot: seedSlot, Proposal: *row})
	}
	for _, v := range g.Votes {
		p := g.Proposals[v.Proposal]
		changes.Votes = append(changes.Votes, ledger.VoteChange{
			Slot: seedSlot, TxHash: p.ID.TxHash, ActionIndex: p.ID.Index, Vote: *voteRow(v, 0),
		})
	}
	for _, d := range g.Dreps {
		row := *drepRow(d)
		row.AddedSlot = seedSlot
		changes.DReps = append(changes.DReps, ledger.DRepChange{Slot: seedSlot, Drep: row})
	}
	for _, m := range g.Committee {
		changes.Committee = append(changes.Committee, ledger.CommitteeChange{Slot: 0, Member: &models.CommitteeMember{
			ColdCredentialTag: m.Tag, ColdCredHash: m.Key, ExpiresEpoch: m.Expiry, TermStartSlotSet: true,
		}})
		if m.Hot != nil {
			changes.Committee = append(changes.Committee, ledger.CommitteeChange{Slot: committeeAuthSlot, Auth: &models.AuthCommitteeHot{
				ColdCredentialTag: m.Tag, ColdCredential: m.Key,
				HotCredentialTag: m.Hot.Tag, HotCredential: m.Hot.Key, AddedSlot: committeeAuthSlot,
			}})
		}
		if m.Resigned {
			changes.Committee = append(changes.Committee, ledger.CommitteeChange{Slot: committeeResignSlot, Resign: &models.ResignCommitteeCold{
				ColdCredentialTag: m.Tag, ColdCredential: m.Key, AddedSlot: committeeResignSlot,
			}})
		}
	}
	if g.Quorum != nil {
		changes.Committee = append(changes.Committee, ledger.CommitteeChange{
			Slot: seedSlot, Quorum: &ledger.CommitteeQuorumRow{Slot: seedSlot, Quorum: g.Quorum},
		})
	}
	if g.Constitution != nil {
		c := *g.Constitution
		c.AddedSlot = seedSlot
		changes.Committee = append(changes.Committee, ledger.CommitteeChange{Slot: seedSlot, Constitution: &c})
	}
}

// readNewEpochGov reads what the boundary into epoch did to the governance
// rows seeded by govChanges.
func readNewEpochGov(
	ctx context.Context, ldb ledgerdb.LedgerDB, g GovState, prior *PriorBoundary, epoch uint64,
) (*NewEpochGovOutput, error) {
	out := &NewEpochGovOutput{Roots: map[uint8]GovAction{}}
	all, err := ldb.GetAllGovernanceProposals(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read proposals: %w", err)
	}
	seeded := map[string]bool{}
	for _, p := range g.Proposals {
		seeded[proposalKey(p.ID.TxHash, p.ID.Index)] = true
	}
	dropped := map[string]bool{}
	enacted := map[string]bool{}
	for _, p := range all {
		k := proposalKey(p.TxHash, p.ActionIndex)
		if !seeded[k] {
			continue
		}
		if p.EnactedEpoch != nil && *p.EnactedEpoch == epoch {
			enacted[k] = true
		}
		if p.DroppedEpoch != nil && *p.DroppedEpoch == epoch {
			dropped[k] = true
		}
		if enacted[k] || dropped[k] || p.DeletedSlot != nil {
			out.Removed = append(out.Removed, GovAction{p.TxHash, p.ActionIndex})
		}
	}
	for _, a := range prior.Ratified {
		if enacted[proposalKey(a.TxHash, a.Index)] {
			out.Enacted = append(out.Enacted, a)
		}
	}
	for _, a := range prior.Expired {
		if dropped[proposalKey(a.TxHash, a.Index)] {
			out.Expired = append(out.Expired, a)
		}
	}
	sortActions(out.Removed)
	sortActions(out.Expired)
	for _, d := range g.Dreps {
		row, err := ldb.GetDrepByCredential(ctx, d.Tag, d.Key, true, nil)
		if err != nil {
			return nil, fmt.Errorf("dxoracle: read drep: %w", err)
		}
		if row == nil {
			return nil, fmt.Errorf("dxoracle: drep %x not stored", d.Key)
		}
		d.ExpiryEpoch = row.ExpiryEpoch
		out.Dreps = append(out.Dreps, d)
	}
	if out.Committee, err = ldb.GetCommitteeMembers(ctx, nil); err != nil {
		return nil, err
	}
	if out.Quorum, err = ldb.GetCommitteeQuorum(ctx, nil); err != nil {
		return nil, err
	}
	if out.Constitution, err = ldb.GetConstitution(nil); err != nil {
		return nil, err
	}
	for _, purpose := range [][]uint8{
		{uint8(lcommon.GovActionTypeParameterChange)},
		{uint8(lcommon.GovActionTypeHardForkInitiation)},
		{uint8(lcommon.GovActionTypeNoConfidence), uint8(lcommon.GovActionTypeUpdateCommittee)},
		{uint8(lcommon.GovActionTypeNewConstitution)},
	} {
		root, err := ldb.GetLastEnactedGovernanceProposal(ctx, purpose, nil)
		if err != nil {
			return nil, err
		}
		if root != nil {
			out.Roots[purpose[0]] = GovAction{root.TxHash, root.ActionIndex}
		}
	}
	return out, nil
}

func sortActions(as []GovAction) {
	sort.Slice(as, func(i, j int) bool {
		if c := bytes.Compare(as[i].TxHash, as[j].TxHash); c != 0 {
			return c < 0
		}
		return as[i].Index < as[j].Index
	})
}
