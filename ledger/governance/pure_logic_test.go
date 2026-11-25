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

package governance

import (
	"math"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pureDRep(tag uint8, cred byte) *models.Drep {
	return &models.Drep{
		CredentialTag: tag, Credential: []byte{cred}, Active: true,
	}
}

func pureDRepKey(d *models.Drep) string {
	return models.StakeCredentialRef{Tag: d.CredentialTag, Key: d.Credential}.MapKey()
}

func TestTallyDRepVotesPure(t *testing.T) {
	t.Parallel()
	d1, d2 := pureDRep(0, 1), pureDRep(1, 2)
	state := &DRepVotingState{
		Dreps: []*models.Drep{d1, d2},
		Powers: map[string]uint64{
			pureDRepKey(d1): 10,
			pureDRepKey(d2): 20,
		},
		AbstainPower:      5,
		NoConfidencePower: 7,
	}
	votes := []*models.GovernanceVote{{
		VoterCredentialTag: 0, VoterCredential: []byte{1}, Vote: models.VoteYes,
	}}

	tally := &ProposalTally{ActionType: uint8(lcommon.GovActionTypeParameterChange)}
	require.NoError(t, TallyDRepVotes(state, votes, tally))
	assert.Equal(t, uint64(42), tally.DRepTotalStake)
	assert.Equal(t, uint64(10), tally.DRepYesStake)
	assert.Equal(t, uint64(7), tally.DRepNoStake, "AlwaysNoConfidence counts No")
	assert.Equal(t, uint64(5), tally.DRepAbstainStake)

	tally = &ProposalTally{ActionType: uint8(lcommon.GovActionTypeNoConfidence)}
	require.NoError(t, TallyDRepVotes(state, votes, tally))
	assert.Equal(t, uint64(17), tally.DRepYesStake, "AlwaysNoConfidence counts Yes")
	assert.Equal(t, uint64(0), tally.DRepNoStake)

	overflow := &DRepVotingState{AbstainPower: math.MaxUint64, NoConfidencePower: 1}
	require.Error(t, TallyDRepVotes(overflow, nil, &ProposalTally{}))
	require.Error(t, TallyDRepVotes(nil, nil, &ProposalTally{}))
}

func TestTallySPOVotesPure(t *testing.T) {
	t.Parallel()
	dist := []*models.PoolStakeSnapshot{
		{PoolKeyHash: []byte{1}, TotalStake: types.Uint64(10)},
		{PoolKeyHash: []byte{2}, TotalStake: types.Uint64(20)},
		{
			PoolKeyHash: []byte{3}, TotalStake: types.Uint64(30),
			RewardAccountAutoVoteResolved: true,
			RewardAccountAutoVote:         models.PoolRewardAccountAutoVoteNoConfidence,
		},
	}
	state := &SPOVotingState{Dist: dist, TotalStake: 60}
	votes := []*models.GovernanceVote{{VoterCredential: []byte{1}, Vote: models.VoteYes}}

	tally := &ProposalTally{ActionType: uint8(lcommon.GovActionTypeParameterChange)}
	require.NoError(t, TallySPOVotes(state, 10, votes, tally))
	assert.Equal(t, uint64(60), tally.SPOTotalStake)
	assert.Equal(t, uint64(10), tally.SPOYesStake)
	assert.Equal(t, uint64(30), tally.SPONoStake, "auto NoConfidence is No here")
	assert.Equal(t, uint64(0), tally.SPOAbstainStake)

	tally = &ProposalTally{ActionType: uint8(lcommon.GovActionTypeNoConfidence)}
	require.NoError(t, TallySPOVotes(state, 10, votes, tally))
	assert.Equal(t, uint64(40), tally.SPOYesStake)

	tally = &ProposalTally{ActionType: uint8(lcommon.GovActionTypeParameterChange)}
	require.NoError(t, TallySPOVotes(state, bootstrapProtocolVersion, votes, tally))
	assert.Equal(t, uint64(50), tally.SPOAbstainStake, "silent pools abstain in bootstrap")

	tally = &ProposalTally{ActionType: uint8(lcommon.GovActionTypeHardForkInitiation)}
	require.NoError(t, TallySPOVotes(state, bootstrapProtocolVersion, votes, tally))
	assert.Equal(t, uint64(0), tally.SPOAbstainStake, "HardFork silent pools are implicit No")
	require.Error(t, TallySPOVotes(nil, 10, nil, &ProposalTally{}))
}

func TestTallyCCVotesPure(t *testing.T) {
	t.Parallel()
	hot := func(tag uint8, c byte) string {
		return models.CommitteeCredential{CredentialTag: tag, Credential: []byte{c}}.Key()
	}
	state := &CommitteeVotingState{
		CommitteePresent:     true,
		ActiveMemberCount:    3,
		MemberHotCredentials: []string{hot(0, 1), hot(0, 2), hot(0, 3)},
		HotCredentialPresence: map[string]struct{}{
			hot(0, 1): {}, hot(0, 2): {}, hot(0, 3): {},
		},
	}
	votes := []*models.GovernanceVote{
		{VoterCredentialTag: 0, VoterCredential: []byte{1}, Vote: models.VoteYes},
		{VoterCredentialTag: 0, VoterCredential: []byte{2}, Vote: models.VoteNo},
		{VoterCredentialTag: 0, VoterCredential: []byte{9}, Vote: models.VoteYes},
	}
	tally := &ProposalTally{}
	require.NoError(t, TallyCCVotes(state, votes, tally))
	assert.Equal(t, 3, tally.CCTotalCount)
	assert.Equal(t, 1, tally.CCYesCount)
	assert.Equal(t, 1, tally.CCNoCount, "non-voter is not counted No")
	assert.Equal(t, 0, tally.CCAbstainCount)
	require.Error(t, TallyCCVotes(nil, nil, &ProposalTally{}))
}

func TestThresholdEntryPoints(t *testing.T) {
	t.Parallel()
	pp := &conway.ConwayProtocolParameters{}
	pp.DRepVotingThresholds.MotionNoConfidence = cbor.Rat{Rat: big.NewRat(2, 3)}
	pp.PoolVotingThresholds.MotionNoConfidence = cbor.Rat{Rat: big.NewRat(1, 2)}

	assert.Equal(t, 0, DRepThreshold(lcommon.GovActionTypeNoConfidence, pp, nil, true).Cmp(big.NewRat(2, 3)))
	assert.Equal(t, 0, SPOThreshold(lcommon.GovActionTypeNoConfidence, pp, nil, true).Cmp(big.NewRat(1, 2)))
	assert.Nil(t, DRepThreshold(lcommon.GovActionTypeInfo, pp, nil, true))
	assert.Nil(t, SPOThreshold(lcommon.GovActionTypeNewConstitution, pp, nil, true))
	assert.True(t, RequiresCCApproval(lcommon.GovActionTypeTreasuryWithdrawal))
	assert.False(t, RequiresCCApproval(lcommon.GovActionTypeNoConfidence))
}

func TestSelectCommitteeQuorumPure(t *testing.T) {
	t.Parallel()
	genesis := &conway.ConwayGenesis{}
	genesis.Committee.Threshold = &cbor.Rat{Rat: big.NewRat(3, 5)}

	q, fallback := SelectCommitteeQuorum(big.NewRat(1, 4), genesis)
	assert.Equal(t, 0, q.Cmp(big.NewRat(1, 4)), "enacted wins")
	assert.False(t, fallback)
	q, fallback = SelectCommitteeQuorum(nil, genesis)
	assert.Equal(t, 0, q.Cmp(big.NewRat(3, 5)), "genesis next")
	assert.False(t, fallback)
	q, fallback = SelectCommitteeQuorum(nil, nil)
	assert.Equal(t, 0, q.Cmp(big.NewRat(2, 3)), "2/3 default")
	assert.True(t, fallback)
	q.SetInt64(9)
	again, _ := SelectCommitteeQuorum(nil, nil)
	assert.Equal(t, 0, again.Cmp(big.NewRat(2, 3)), "default is not aliased")
}

func pureProposal(tx byte, typ lcommon.GovActionType, parent *byte) *models.GovernanceProposal {
	p := &models.GovernanceProposal{TxHash: []byte{tx}, ActionType: uint8(typ)}
	if parent != nil {
		p.ParentTxHash = []byte{*parent}
		idx := uint32(0)
		p.ParentActionIdx = &idx
	}
	return p
}

func TestPlanOrphanRemovals(t *testing.T) {
	t.Parallel()
	root := byte(1)
	winner := pureProposal(2, lcommon.GovActionTypeParameterChange, &root)
	sibling := pureProposal(3, lcommon.GovActionTypeParameterChange, &root)
	w2 := byte(2)
	child := pureProposal(4, lcommon.GovActionTypeParameterChange, &w2)
	s3 := byte(3)
	siblingChild := pureProposal(5, lcommon.GovActionTypeParameterChange, &s3)
	expired := pureProposal(6, lcommon.GovActionTypeNoConfidence, nil)
	e6 := byte(6)
	expiredChild := pureProposal(7, lcommon.GovActionTypeUpdateCommittee, &e6)
	dropped := pureProposal(8, lcommon.GovActionTypeNewConstitution, nil)
	d8 := byte(8)
	droppedChild := pureProposal(9, lcommon.GovActionTypeNewConstitution, &d8)

	plan := PlanOrphanRemovals(
		[]*models.GovernanceProposal{winner},
		[]*models.GovernanceProposal{expired},
		[]*models.GovernanceProposal{dropped},
		[]*models.GovernanceProposal{sibling, child, siblingChild, expiredChild, droppedChild},
	)
	require.Len(t, plan, 4)
	assert.Same(t, sibling, plan[0].Proposal)
	assert.True(t, plan[0].RefundNow)
	assert.Same(t, siblingChild, plan[1].Proposal, "subtree follows its seed")
	assert.True(t, plan[1].RefundNow)
	assert.Same(t, expiredChild, plan[2].Proposal)
	assert.False(t, plan[2].RefundNow, "expiry sweep defers the refund")
	assert.Same(t, droppedChild, plan[3].Proposal)
	assert.True(t, plan[3].RefundNow)
}

func TestProposalTreeEntryPoints(t *testing.T) {
	t.Parallel()
	a := byte(1)
	p1 := pureProposal(1, lcommon.GovActionTypeParameterChange, nil)
	p2 := pureProposal(2, lcommon.GovActionTypeParameterChange, &a)
	p3 := pureProposal(3, lcommon.GovActionTypeParameterChange, nil)
	ordered := OrderParameterChangeChains([]*models.GovernanceProposal{p2, p1, p3})
	require.Len(t, ordered, 3)
	assert.Same(t, p1, ordered[0])
	assert.Same(t, p2, ordered[1], "child moves right after its parent")

	assert.True(t, ValidateParentChain(p1, nil))
	assert.False(t, ValidateParentChain(p2, nil))
	assert.True(t, ValidateParentChain(p2, p1))
	assert.True(t, ValidateParentChain(pureProposal(9, lcommon.GovActionTypeInfo, &a), p1))
}

func TestListBoundaryExpiredRefs(t *testing.T) {
	t.Parallel()
	p1 := pureProposal(1, lcommon.GovActionTypeInfo, nil)
	p2 := pureProposal(2, lcommon.GovActionTypeInfo, nil)
	refs := ListBoundaryExpiredRefs(
		[]*models.GovernanceProposal{p2, p1},
		[]*models.GovernanceProposal{p1, nil},
	)
	require.Len(t, refs, 2)
	assert.Equal(t, []byte{2}, refs[0].TxHash)
	assert.Equal(t, []byte{1}, refs[1].TxHash)
}

func TestBuildDRepStakeDistribution(t *testing.T) {
	t.Parallel()
	d1, d2, d3 := pureDRep(1, 9), pureDRep(0, 5), pureDRep(0, 4)
	dreps := []*models.Drep{d1, d2, d3}
	powers := map[string]uint64{pureDRepKey(d1): 1, pureDRepKey(d2): 2}
	virtual := map[uint64]uint64{models.DrepTypeAlwaysAbstain: 3}
	deposits := DRepDepositPower{
		Credential:   map[string]uint64{pureDRepKey(d3): 4, pureDRepKey(d2): 10},
		NoConfidence: 6,
	}
	got, err := BuildDRepStakeDistribution(dreps, powers, virtual, deposits)
	require.NoError(t, err)
	require.Len(t, got, 5)
	assert.Equal(t, models.DRepStakeDistributionEntry{
		Credential: []byte{4}, Stake: 4, DRepType: 0, CredentialTag: 0}, got[0])
	assert.Equal(t, uint64(12), got[1].Stake, "deposit folds into delegated power")
	assert.Equal(t, uint8(1), got[2].DRepType)
	assert.Equal(t, uint8(models.DrepTypeAlwaysAbstain), got[3].DRepType)
	assert.Equal(t, uint8(models.DrepTypeAlwaysNoConfidence), got[4].DRepType)
	assert.Equal(t, uint64(6), got[4].Stake)
	assert.Equal(t, uint64(1), powers[pureDRepKey(d1)], "input untouched")
	assert.Equal(t, uint64(3), virtual[models.DrepTypeAlwaysAbstain], "input untouched")

	_, err = BuildDRepStakeDistribution(dreps, map[string]uint64{pureDRepKey(d1): math.MaxUint64},
		nil, DRepDepositPower{Credential: map[string]uint64{pureDRepKey(d1): 1}})
	require.Error(t, err)
}

func TestDRepExpiryEntryPoints(t *testing.T) {
	t.Parallel()
	assert.True(t, DRepActiveAtEpoch(&models.Drep{ExpiryEpoch: 0}, 5))
	assert.True(t, DRepActiveAtEpoch(&models.Drep{ExpiryEpoch: 5}, 5))
	assert.False(t, DRepActiveAtEpoch(&models.Drep{ExpiryEpoch: 4}, 5))
	assert.False(t, DRepActiveAtEpoch(nil, 5))

	assert.True(t, ShouldBumpDormantDRepExpiry(0))
	assert.False(t, ShouldBumpDormantDRepExpiry(1))

	dreps := []*models.Drep{
		{Credential: []byte{1}, Active: true, ExpiryEpoch: 10},
		{Credential: []byte{2}, Active: true, ExpiryEpoch: 0},
		{Credential: []byte{3}, Active: false, ExpiryEpoch: 10},
	}
	got, err := BumpDormantDRepExpiries(dreps, false)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, uint64(11), got[0].ExpiryEpoch)
	assert.Equal(t, uint64(10), dreps[0].ExpiryEpoch, "input untouched")
	got, err = BumpDormantDRepExpiries(dreps, true)
	require.NoError(t, err)
	assert.Empty(t, got, "active proposals suppress the bump")
	_, err = BumpDormantDRepExpiries(
		[]*models.Drep{{Active: true, ExpiryEpoch: math.MaxInt64}}, false)
	require.Error(t, err)
}

func TestApplyPParamsEnactment(t *testing.T) {
	t.Parallel()
	pp := &conway.ConwayProtocolParameters{}
	hf := &lcommon.HardForkInitiationGovAction{}
	hf.ProtocolVersion.Major = 10
	res, handled, err := ApplyPParamsEnactment(pp, hf, nil)
	require.NoError(t, err)
	require.True(t, handled)
	assert.True(t, res.PParamsChanged)
	assert.Equal(t, uint(10), res.UpdatedPParams.(*conway.ConwayProtocolParameters).ProtocolVersion.Major)
	assert.Equal(t, uint(0), pp.ProtocolVersion.Major, "input pparams untouched")

	_, handled, err = ApplyPParamsEnactment(pp, &lcommon.InfoGovAction{}, nil)
	require.NoError(t, err)
	assert.False(t, handled)
}

func TestPlanCommitteeUpdate(t *testing.T) {
	t.Parallel()
	keep := &lcommon.Credential{CredType: 0}
	copy(keep.Credential[:], []byte{1})
	add := &lcommon.Credential{CredType: 1}
	copy(add.Credential[:], []byte{2})
	action := &lcommon.UpdateCommitteeGovAction{
		CredEpochs: map[*lcommon.Credential]uint64{keep: 50, add: 60},
	}
	existing := map[string]uint64{
		models.CommitteeCredential{CredentialTag: 0, Credential: keep.Credential[:]}.Key(): 7,
	}
	plan, err := PlanCommitteeUpdate(action, existing, 100, 200, 300)
	require.NoError(t, err)
	require.Len(t, plan.Add, 2)
	assert.Equal(t, uint8(0), plan.Add[0].ColdCredentialTag)
	assert.Equal(t, uint64(7), plan.Add[0].TermStartSlot, "continuing member keeps term start")
	assert.Equal(t, uint64(200), plan.Add[1].TermStartSlot, "new member starts at the later slot")
	assert.Equal(t, uint64(300), plan.Add[1].AddedSlot)
	assert.Equal(t, uint64(60), plan.Add[1].ExpiresEpoch)
}
