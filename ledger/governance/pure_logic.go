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
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"

	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/models"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	gdijkstra "github.com/blinklabs-io/gouroboros/ledger/dijkstra"
)

// This file holds the governance rules that depend only on their arguments:
// no database, transaction, clock, logging or goroutine. The loaders and
// boundary code elsewhere in the package read state and call these, so each
// rule has one implementation.

// TallyDRepVotes adds the DRep stake of every regular DRep in state, and the
// predefined AlwaysAbstain and AlwaysNoConfidence powers, into the DRep fields
// of tally. tally.ActionType selects whether AlwaysNoConfidence counts Yes
// (NoConfidence) or No. Regular DReps that did not vote count only toward the
// total. Sums that would overflow uint64 return an error.
func TallyDRepVotes(
	state *DRepVotingState,
	votes []*models.GovernanceVote,
	tally *ProposalTally,
) error {
	if state == nil {
		return errors.New("nil DRep voting state")
	}
	if len(state.Dreps) > 0 {
		voteByCred := make(map[string]uint8, len(votes))
		for _, v := range votes {
			ref := models.StakeCredentialRef{
				Tag: v.VoterCredentialTag,
				Key: v.VoterCredential,
			}
			voteByCred[ref.MapKey()] = v.Vote
		}

		for _, drep := range state.Dreps {
			ref := models.StakeCredentialRef{
				Tag: drep.CredentialTag,
				Key: drep.Credential,
			}
			power := state.Powers[ref.MapKey()]
			var err error
			tally.DRepTotalStake, err = addUint64(tally.DRepTotalStake, power)
			if err != nil {
				return fmt.Errorf("drep total stake: %w", err)
			}

			vote, voted := voteByCred[ref.MapKey()]
			if !voted {
				continue
			}
			switch vote {
			case models.VoteYes:
				tally.DRepYesStake, err = addUint64(tally.DRepYesStake, power)
				if err != nil {
					return fmt.Errorf("drep yes stake: %w", err)
				}
			case models.VoteNo:
				tally.DRepNoStake, err = addUint64(tally.DRepNoStake, power)
				if err != nil {
					return fmt.Errorf("drep no stake: %w", err)
				}
			case models.VoteAbstain:
				tally.DRepAbstainStake, err = addUint64(
					tally.DRepAbstainStake, power,
				)
				if err != nil {
					return fmt.Errorf("drep abstain stake: %w", err)
				}
			}
		}
	}

	abstainPower := state.AbstainPower
	noConfidencePower := state.NoConfidencePower
	virtualPower, err := addUint64(abstainPower, noConfidencePower)
	if err != nil {
		return fmt.Errorf("drep virtual power: %w", err)
	}
	tally.DRepTotalStake, err = addUint64(tally.DRepTotalStake, virtualPower)
	if err != nil {
		return fmt.Errorf("drep total stake: %w", err)
	}
	tally.DRepAbstainStake, err = addUint64(
		tally.DRepAbstainStake,
		abstainPower,
	)
	if err != nil {
		return fmt.Errorf("drep abstain stake: %w", err)
	}
	if noConfidencePower > 0 {
		if lcommon.GovActionType(tally.ActionType) ==
			lcommon.GovActionTypeNoConfidence {
			tally.DRepYesStake, err = addUint64(
				tally.DRepYesStake, noConfidencePower,
			)
			if err != nil {
				return fmt.Errorf("drep yes stake: %w", err)
			}
		} else {
			tally.DRepNoStake, err = addUint64(
				tally.DRepNoStake, noConfidencePower,
			)
			if err != nil {
				return fmt.Errorf("drep no stake: %w", err)
			}
		}
	}
	return nil
}

// TallySPOVotes adds the stake of every pool in state.Dist into the SPO fields
// of tally and sets tally.SPOTotalStake. An explicit vote wins. A silent pool
// is implicit No on HardForkInitiation, Abstain while majorVersion is the
// Conway bootstrap version, and otherwise follows its resolved reward-account
// auto-vote.
func TallySPOVotes(
	state *SPOVotingState,
	majorVersion uint,
	votes []*models.GovernanceVote,
	tally *ProposalTally,
) error {
	if state == nil {
		return errors.New("nil SPO voting state")
	}
	dist := state.Dist
	tally.SPOTotalStake = state.TotalStake
	if len(dist) == 0 {
		return nil
	}

	voteByPool := make(map[string]uint8, len(votes))
	for _, v := range votes {
		voteByPool[string(v.VoterCredential)] = v.Vote
	}

	actionType := lcommon.GovActionType(tally.ActionType)
	isHardForkInitiation := actionType ==
		lcommon.GovActionTypeHardForkInitiation
	inBootstrap := majorVersion == bootstrapProtocolVersion
	isNoConfidenceAction := actionType ==
		lcommon.GovActionTypeNoConfidence

	for _, s := range dist {
		stake := uint64(s.TotalStake)
		var err error

		if v, voted := voteByPool[string(s.PoolKeyHash)]; voted {
			switch v {
			case models.VoteYes:
				tally.SPOYesStake, err = addUint64(tally.SPOYesStake, stake)
				if err != nil {
					return fmt.Errorf("spo yes stake: %w", err)
				}
			case models.VoteNo:
				tally.SPONoStake, err = addUint64(tally.SPONoStake, stake)
				if err != nil {
					return fmt.Errorf("spo no stake: %w", err)
				}
			case models.VoteAbstain:
				tally.SPOAbstainStake, err = addUint64(
					tally.SPOAbstainStake, stake,
				)
				if err != nil {
					return fmt.Errorf("spo abstain stake: %w", err)
				}
			}
			continue
		}

		// The reference RATIFY rule applies non-voter semantics before
		// consulting reward-account defaults. HardForkInitiation always
		// keeps silent-pool stake in the active denominator as implicit No.
		if isHardForkInitiation {
			continue
		}
		if inBootstrap {
			tally.SPOAbstainStake, err = addUint64(
				tally.SPOAbstainStake, stake,
			)
			if err != nil {
				return fmt.Errorf("spo abstain stake: %w", err)
			}
			continue
		}

		// An unresolved auto-vote (imported or pre-CIP-1694 rows) is treated
		// as None so a stale value never buckets stake into Abstain or
		// NoConfidence.
		if !s.RewardAccountAutoVoteResolved {
			continue
		}
		switch s.RewardAccountAutoVote {
		case models.PoolRewardAccountAutoVoteAbstain:
			tally.SPOAbstainStake, err = addUint64(tally.SPOAbstainStake, stake)
			if err != nil {
				return fmt.Errorf("spo abstain stake: %w", err)
			}
		case models.PoolRewardAccountAutoVoteNoConfidence:
			if isNoConfidenceAction {
				tally.SPOYesStake, err = addUint64(tally.SPOYesStake, stake)
				if err != nil {
					return fmt.Errorf("spo yes stake: %w", err)
				}
			} else {
				tally.SPONoStake, err = addUint64(tally.SPONoStake, stake)
				if err != nil {
					return fmt.Errorf("spo no stake: %w", err)
				}
			}
		case models.PoolRewardAccountAutoVoteNone:
			// Contributes only to SPOTotalStake (implicit No).
		}
	}
	return nil
}

// TallyCCVotes counts the votes cast by the hot credentials in state, which
// holds only seated, unexpired, unresigned, authorized members, and sets
// tally.CCTotalCount to the active member count. A member that did not vote is
// in no bucket.
func TallyCCVotes(
	state *CommitteeVotingState,
	votes []*models.GovernanceVote,
	tally *ProposalTally,
) error {
	if state == nil {
		return errors.New("nil committee voting state")
	}
	tally.CCTotalCount = state.ActiveMemberCount

	votesByHotCredential := make(map[string]uint8, len(votes))
	for _, vote := range votes {
		credentialKey := models.CommitteeCredential{
			CredentialTag: vote.VoterCredentialTag,
			Credential:    vote.VoterCredential,
		}.Key()
		if _, ok := state.HotCredentialPresence[credentialKey]; ok {
			votesByHotCredential[credentialKey] = vote.Vote
		}
	}

	for _, hotCredential := range state.MemberHotCredentials {
		// models.VoteNo is the zero value, so "voted" must be tracked
		// separately or every silent member would count as No.
		vote, voted := votesByHotCredential[hotCredential]
		if !voted {
			continue
		}
		switch vote {
		case models.VoteYes:
			tally.CCYesCount++
		case models.VoteNo:
			tally.CCNoCount++
		case models.VoteAbstain:
			tally.CCAbstainCount++
		}
	}
	return nil
}

// DRepThreshold returns the DRep yes-ratio threshold for actionType, or nil
// when DReps do not vote on it. parameterChange selects the parameter groups
// for ParameterChange (nil means the most restrictive group); committeePresent
// selects CommitteeNormal over CommitteeNoConfidence for UpdateCommittee.
func DRepThreshold(
	actionType lcommon.GovActionType,
	pparams *conway.ConwayProtocolParameters,
	parameterChange lcommon.ParameterChangeGovAction,
	committeePresent bool,
) *big.Rat {
	return getDRepThreshold(actionType, pparams, parameterChange, committeePresent)
}

// SPOThreshold returns the stake-pool yes-ratio threshold for actionType, or
// nil when pools do not vote on it. For ParameterChange it is non-nil only when
// parameterChange touches the security group.
func SPOThreshold(
	actionType lcommon.GovActionType,
	pparams *conway.ConwayProtocolParameters,
	parameterChange lcommon.ParameterChangeGovAction,
	committeePresent bool,
) *big.Rat {
	return getSPOThreshold(actionType, pparams, parameterChange, committeePresent)
}

// RequiresCCApproval reports whether actionType needs constitutional
// committee approval. Unknown action types require it.
func RequiresCCApproval(actionType lcommon.GovActionType) bool {
	return needsCCApproval(actionType)
}

// DRepThresholdInForce is DRepThreshold under protocol major version
// majorVersion. During the Conway bootstrap phase every DRep threshold except
// Info's is zero, so DReps cannot block an action; Info stays unvotable (nil).
func DRepThresholdInForce(
	majorVersion uint,
	actionType lcommon.GovActionType,
	pparams *conway.ConwayProtocolParameters,
	parameterChange lcommon.ParameterChangeGovAction,
	committeePresent bool,
) *big.Rat {
	if majorVersion == bootstrapProtocolVersion &&
		actionType != lcommon.GovActionTypeInfo {
		return new(big.Rat)
	}
	return getDRepThreshold(
		actionType, pparams, parameterChange, committeePresent,
	)
}

// CommitteeApproval is what the constitutional committee must satisfy for an
// action.
type CommitteeApproval struct {
	// Voting is false when the committee does not vote on the action.
	Voting bool
	// Reason is non-empty when the committee cannot approve the action.
	Reason string
	// Quorum is the yes ratio the committee must reach. It is set only when
	// Voting is true and Reason is empty.
	Quorum *big.Rat
}

// CommitteeRequirement evaluates the committee gate of an action: who must
// approve and at what ratio. committeeAbsent and noConfidence describe the
// enacted committee, activeMembers counts seated, unexpired, unresigned,
// authorized members, and minSize applies outside the Conway bootstrap phase.
func CommitteeRequirement(
	actionType lcommon.GovActionType,
	majorVersion uint,
	committeeAbsent bool,
	noConfidence bool,
	activeMembers int,
	minSize uint,
	quorum *big.Rat,
) CommitteeApproval {
	inBootstrap := majorVersion == bootstrapProtocolVersion
	switch {
	case !needsCCApproval(actionType):
		return CommitteeApproval{}
	case noConfidence:
		return CommitteeApproval{Voting: true, Reason: "cc in no-confidence state"}
	case committeeAbsent:
		return CommitteeApproval{Voting: true, Reason: "committee absent"}
	case !inBootstrap && activeMembers < int(minSize): //nolint:gosec
		return CommitteeApproval{Voting: true, Reason: "cc below minimum committee size"}
	case quorum == nil:
		return CommitteeApproval{Voting: true, Reason: "cc quorum missing"}
	}
	return CommitteeApproval{Voting: true, Quorum: quorum}
}

// SelectCommitteeQuorum returns the committee quorum in force: enacted when
// non-nil, else the Conway genesis threshold, else the 2/3 default. usedDefault
// is true only for the last case. The result may alias enacted or genesis, so
// callers must not modify it.
func SelectCommitteeQuorum(
	enacted *big.Rat,
	genesis *conway.ConwayGenesis,
) (quorum *big.Rat, usedDefault bool) {
	if enacted != nil {
		return enacted, false
	}
	if genesis != nil && genesis.Committee.Threshold != nil &&
		genesis.Committee.Threshold.Rat != nil {
		return genesis.Committee.Threshold.Rat, false
	}
	return new(big.Rat).Set(defaultCCQuorum), true
}

// OrphanRemoval is one proposal that PlanOrphanRemovals removes from the tree.
type OrphanRemoval struct {
	Proposal *models.GovernanceProposal
	// RefundNow is true when the deposit returns in the same epoch tick:
	// enactment-driven and drop-driven removals. Expiry-driven removals defer
	// the refund.
	RefundNow bool
}

// PlanOrphanRemovals lists, in removal order, the proposals pruned from the
// governance tree. An enacted proposal removes its active same-purpose
// siblings with their subtrees; an expired or dropped proposal removes its
// descendant subtrees. The enactment sweep runs first, then expiry, then drop,
// so a proposal reachable several ways follows the earliest sweep. Each
// proposal appears once.
func PlanOrphanRemovals(
	enacted []*models.GovernanceProposal,
	expired []*models.GovernanceProposal,
	dropped []*models.GovernanceProposal,
	active []*models.GovernanceProposal,
) []OrphanRemoval {
	children := make(map[string][]*models.GovernanceProposal)
	for _, proposal := range active {
		children[proposalParentKey(proposal)] = append(
			children[proposalParentKey(proposal)],
			proposal,
		)
	}
	enactmentSeeds := make([]*models.GovernanceProposal, 0)
	for _, winner := range enacted {
		winnerPurpose := govActionPurposeOf(
			lcommon.GovActionType(winner.ActionType),
		)
		if winnerPurpose == purposeNone {
			continue
		}
		for _, sibling := range children[proposalParentKey(winner)] {
			if govActionPurposeOf(
				lcommon.GovActionType(sibling.ActionType),
			) == winnerPurpose {
				enactmentSeeds = append(enactmentSeeds, sibling)
			}
		}
	}
	expirySeeds := make([]*models.GovernanceProposal, 0, len(expired))
	for _, proposal := range expired {
		expirySeeds = append(
			expirySeeds, children[proposalIdentityKey(proposal)]...,
		)
	}
	dropSeeds := make([]*models.GovernanceProposal, 0, len(dropped))
	for _, proposal := range dropped {
		dropSeeds = append(
			dropSeeds, children[proposalIdentityKey(proposal)]...,
		)
	}

	removed := make(map[string]struct{})
	var plan []OrphanRemoval
	sweep := func(seeds []*models.GovernanceProposal, refundNow bool) {
		queue := append(
			make([]*models.GovernanceProposal, 0, len(seeds)), seeds...,
		)
		for len(queue) > 0 {
			proposal := queue[0]
			queue = queue[1:]
			identity := proposalIdentityKey(proposal)
			if _, ok := removed[identity]; ok {
				continue
			}
			removed[identity] = struct{}{}
			plan = append(plan, OrphanRemoval{
				Proposal: proposal, RefundNow: refundNow,
			})
			queue = append(queue, children[identity]...)
		}
	}
	sweep(enactmentSeeds, true)
	sweep(expirySeeds, false)
	sweep(dropSeeds, true)
	return plan
}

// OrderParameterChangeChains returns proposals in candidate order, except that
// a parameter change listed before its own parent moves to immediately after
// that parent.
func OrderParameterChangeChains(
	proposals []*models.GovernanceProposal,
) []*models.GovernanceProposal {
	return orderParameterChangeChains(proposals)
}

// ValidateParentChain reports whether proposal's parent matches currentRoot,
// the enacted root for its purpose (nil when there is none). Info and
// TreasuryWithdrawal have no chain and always pass; unknown action types fail.
func ValidateParentChain(
	proposal *models.GovernanceProposal,
	currentRoot *models.GovernanceProposal,
) bool {
	return validateParentChain(proposal, currentRoot)
}

// ListBoundaryExpiredRefs returns the references of decided followed by stored
// expirations, each proposal once and nil entries skipped.
func ListBoundaryExpiredRefs(
	decided []*models.GovernanceProposal,
	stored []*models.GovernanceProposal,
) []models.GovernanceActionRef {
	return boundaryExpiredRefs(decided, stored)
}

// DRepActiveAtEpoch reports whether drep is eligible to vote in epoch: a zero
// expiry means no activity bound was ever recorded.
func DRepActiveAtEpoch(drep *models.Drep, epoch uint64) bool {
	return drepActiveAtEpoch(drep, epoch)
}

// ShouldBumpDormantDRepExpiry reports whether the epoch boundary extends
// DRep expiries: only when no governance proposal is active in the new epoch.
func ShouldBumpDormantDRepExpiry(activeProposalCount int) bool {
	return activeProposalCount == 0
}

// BumpDormantDRepExpiries returns copies of the active DReps with a nonzero
// expiry, each extended by one epoch, or nil when proposalsActive. The input is
// not modified. An expiry that would leave the int64 range errors, since the
// ledger store cannot hold it.
func BumpDormantDRepExpiries(
	dreps []*models.Drep,
	proposalsActive bool,
) ([]*models.Drep, error) {
	if proposalsActive {
		return nil, nil
	}
	var changed []*models.Drep
	for _, drep := range dreps {
		if drep == nil || !drep.Active || drep.ExpiryEpoch == 0 {
			continue
		}
		if drep.ExpiryEpoch >= math.MaxInt64 {
			return nil, fmt.Errorf(
				"dormant DRep expiry exceeds storage range: %d",
				drep.ExpiryEpoch,
			)
		}
		bumped := *drep
		bumped.ExpiryEpoch++
		changed = append(changed, &bumped)
	}
	return changed, nil
}

// DRepDepositPower is the voting power that active proposal deposits add to
// DRep delegation targets, keyed by StakeCredentialRef.MapKey for regular
// DReps.
type DRepDepositPower struct {
	Credential   map[string]uint64
	Abstain      uint64
	NoConfidence uint64
}

// BuildDRepStakeDistribution assembles the complete boundary DRep stake
// distribution: each DRep's power plus its deposit power, in ascending
// (type, credential) order with zero-stake DReps omitted, followed by the
// AlwaysAbstain and AlwaysNoConfidence entries when nonzero. virtual is keyed
// by models.DrepTypeAlwaysAbstain and models.DrepTypeAlwaysNoConfidence. No
// inactivity filter is applied. Inputs are not modified; overflow errors.
func BuildDRepStakeDistribution(
	dreps []*models.Drep,
	powers map[string]uint64,
	virtual map[uint64]uint64,
	deposits DRepDepositPower,
) ([]models.DRepStakeDistributionEntry, error) {
	merged := make(map[string]uint64, len(powers)+len(deposits.Credential))
	for key, power := range powers {
		merged[key] = power
	}
	for key, amount := range deposits.Credential {
		sum, err := addUint64(merged[key], amount)
		if err != nil {
			return nil, fmt.Errorf("drep stake distribution deposits: %w", err)
		}
		merged[key] = sum
	}
	abstain, err := addUint64(
		virtual[models.DrepTypeAlwaysAbstain], deposits.Abstain,
	)
	if err != nil {
		return nil, fmt.Errorf("abstain stake distribution deposits: %w", err)
	}
	noConfidence, err := addUint64(
		virtual[models.DrepTypeAlwaysNoConfidence], deposits.NoConfidence,
	)
	if err != nil {
		return nil, fmt.Errorf("no-confidence stake distribution deposits: %w", err)
	}

	ret := make([]models.DRepStakeDistributionEntry, 0, len(dreps)+2)
	for _, drep := range dreps {
		ref := models.NewStakeCredentialRef(
			drep.CredentialTag,
			drep.Credential,
		)
		stake := merged[ref.MapKey()]
		if stake == 0 {
			continue
		}
		ret = append(ret, models.DRepStakeDistributionEntry{
			Credential:    append([]byte(nil), drep.Credential...),
			Stake:         stake,
			DRepType:      drep.CredentialTag,
			CredentialTag: drep.CredentialTag,
		})
	}
	sort.Slice(ret, func(i, j int) bool {
		if ret[i].DRepType != ret[j].DRepType {
			return ret[i].DRepType < ret[j].DRepType
		}
		return bytes.Compare(ret[i].Credential, ret[j].Credential) < 0
	})
	if abstain > 0 {
		ret = append(ret, models.DRepStakeDistributionEntry{
			DRepType: uint8(models.DrepTypeAlwaysAbstain), //nolint:gosec // protocol enum is 2
			Stake:    abstain,
		})
	}
	if noConfidence > 0 {
		ret = append(ret, models.DRepStakeDistributionEntry{
			DRepType: uint8(models.DrepTypeAlwaysNoConfidence), //nolint:gosec // protocol enum is 3
			Stake:    noConfidence,
		})
	}
	return ret, nil
}

// ApplyPParamsEnactment applies the protocol-parameter effect of a ratified
// action: ParameterChange through updateFn, or HardForkInitiation by setting
// the protocol version. handled is false for every other action, which leaves
// pparams unchanged. pparams is not modified.
func ApplyPParamsEnactment(
	pparams lcommon.ProtocolParameters,
	action lcommon.GovAction,
	updateFn func(lcommon.ProtocolParameters, any) (lcommon.ProtocolParameters, error),
) (result *EnactmentResult, handled bool, err error) {
	result = &EnactmentResult{UpdatedPParams: pparams}
	switch a := action.(type) {
	case *conway.ConwayParameterChangeGovAction:
		updated, err := updateFn(pparams, a.ParamUpdate)
		if err != nil {
			return nil, true, fmt.Errorf("apply param update: %w", err)
		}
		result.UpdatedPParams = updated
		result.PParamsChanged = true
		if _, ok := a.ParamUpdate.CostModels[1]; ok {
			result.PlutusV2CostModelWritten = true
		}
		return result, true, nil
	case *gdijkstra.DijkstraParameterChangeGovAction:
		updated, err := updateFn(pparams, a.ParamUpdate)
		if err != nil {
			return nil, true, fmt.Errorf("apply param update: %w", err)
		}
		result.UpdatedPParams = updated
		result.PParamsChanged = true
		if _, ok := a.ParamUpdate.CostModels[1]; ok {
			result.PlutusV2CostModelWritten = true
		}
		return result, true, nil
	case *lcommon.HardForkInitiationGovAction:
		updated, err := setProtocolVersion(
			pparams,
			a.ProtocolVersion.Major,
			a.ProtocolVersion.Minor,
		)
		if err != nil {
			return nil, true, fmt.Errorf("schedule hard fork: %w", err)
		}
		result.UpdatedPParams = updated
		result.PParamsChanged = true
		return result, true, nil
	}
	return result, false, nil
}

// CommitteeUpdatePlan is the committee change an UpdateCommittee action makes.
type CommitteeUpdatePlan struct {
	// Remove lists the cold credentials removed from the committee.
	Remove []models.CommitteeCredential
	// Quorum is the new committee threshold.
	Quorum *big.Rat
	// Add lists the added or renewed members, ordered by credential hash then
	// tag so database ids are stable across nodes.
	Add []*models.CommitteeMember
}

// PlanCommitteeUpdate computes the removals, new quorum and added members of
// an UpdateCommittee action enacted at slot. existingTermStart maps the key of
// each currently seated cold credential (models.CommitteeCredential.Key) to its
// term start; a continuing member keeps it. A new member starts at the later of
// termStartSlot, the proposal's slot, and prevEpochStartSlot, because
// certificates recorded before the closing epoch were dropped at its boundary.
func PlanCommitteeUpdate(
	action *lcommon.UpdateCommitteeGovAction,
	existingTermStart map[string]uint64,
	termStartSlot uint64,
	prevEpochStartSlot uint64,
	slot uint64,
) (*CommitteeUpdatePlan, error) {
	if action == nil {
		return nil, errors.New("nil update committee action")
	}
	plan := &CommitteeUpdatePlan{
		Remove: make([]models.CommitteeCredential, 0, len(action.Credentials)),
		Quorum: action.Quorum.Rat,
	}
	for _, c := range action.Credentials {
		credentialTag, err := models.CredentialTagFromUint(c.CredType)
		if err != nil {
			return nil, fmt.Errorf("remove member credential: %w", err)
		}
		hash := c.Credential
		plan.Remove = append(plan.Remove, models.CommitteeCredential{
			CredentialTag: credentialTag,
			Credential:    hash[:],
		})
	}
	members := make([]*models.CommitteeMember, 0, len(action.CredEpochs))
	for cred, expiry := range action.CredEpochs {
		if cred == nil {
			continue
		}
		hash := cred.Credential
		credentialTag, err := models.CredentialTagFromUint(cred.CredType)
		if err != nil {
			return nil, fmt.Errorf("add member credential: %w", err)
		}
		memberTermStart := max(termStartSlot, prevEpochStartSlot)
		key := models.CommitteeCredential{
			CredentialTag: credentialTag,
			Credential:    hash[:],
		}.Key()
		if priorTermStart, ok := existingTermStart[key]; ok {
			memberTermStart = priorTermStart
		}
		members = append(members, &models.CommitteeMember{
			ColdCredentialTag: credentialTag,
			ColdCredHash:      hash[:],
			ExpiresEpoch:      uint64(expiry),
			TermStartSlot:     memberTermStart,
			TermStartSlotSet:  true,
			AddedSlot:         slot,
		})
	}
	sort.Slice(members, func(i, j int) bool {
		if cmp := bytes.Compare(
			members[i].ColdCredHash,
			members[j].ColdCredHash,
		); cmp != 0 {
			return cmp < 0
		}
		return members[i].ColdCredentialTag < members[j].ColdCredentialTag
	})
	plan.Add = members
	return plan, nil
}

// LoadDRepStakeDistribution reads the complete boundary DRep stake
// distribution for activeProposalEpoch through db and txn, exactly as the epoch
// boundary does.
func LoadDRepStakeDistribution(
	ctx context.Context,
	db ledgerdb.LedgerDB,
	txn ledgerdb.Txn,
	activeProposalEpoch uint64,
) ([]models.DRepStakeDistributionEntry, error) {
	return loadDRepStakeDistribution(ctx, db, txn, activeProposalEpoch)
}
