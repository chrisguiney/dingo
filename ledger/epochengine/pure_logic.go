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

package epochengine

import (
	"errors"
	"fmt"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/rewards"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"math"
	"math/big"
)

// ComputeRewardPrefilterSlot returns the slot at which the reward prefilter
// snapshot of registered accounts is taken: one slot past the RUPD
// stabilisation window from the start of the pots epoch, clamped to the
// epoch's last slot. A zero-length epoch yields its start slot.
func ComputeRewardPrefilterSlot(
	startSlot uint64,
	lengthInSlots uint64,
	stabilityWindow uint64,
) uint64 {
	if lengthInSlots == 0 {
		return startSlot
	}
	endSlot := startSlot + lengthInSlots - 1
	prefilterSlot := startSlot
	if stabilityWindow > 0 {
		if stabilityWindow >= math.MaxUint64-startSlot {
			prefilterSlot = endSlot
		} else {
			prefilterSlot = startSlot + stabilityWindow + 1
		}
	}
	if prefilterSlot > endSlot {
		prefilterSlot = endSlot
	}
	return prefilterSlot
}

// UpdatedPotsInput carries the values StakeRewardUpdatedPots reads from a
// reward application.
type UpdatedPotsInput struct {
	// TotalRewardPot is incentives plus fees.
	TotalRewardPot uint64
	// Fees is the fee pot the reward round consumed.
	Fees uint64
	// PotsReserves is the reserves of the round's own pots row, which bounds
	// the incentives.
	PotsReserves uint64
	// BaseReserves and BaseTreasury are the pots the deltas apply to: the
	// round's own pots, or the replay boundary's pots for a replayed round.
	BaseReserves uint64
	BaseTreasury uint64
	// Undistributed is refunded to reserves.
	Undistributed uint64
	// Unspendable is sent to the treasury.
	Unspendable uint64
	// UnspendableDeficit is the magnitude of negative leader rewards owed to
	// unregistered accounts, taken from the treasury.
	UnspendableDeficit uint64
	// TreasuryExpansion is tau.
	TreasuryExpansion *big.Rat
}

// ComputeUpdatedPots returns the reserves and treasury after a reward round is
// applied: reserves lose the incentives and regain the undistributed rewards;
// the treasury gains the tax and the unspendable rewards less the deficit.
func ComputeUpdatedPots(in UpdatedPotsInput) (uint64, uint64, error) {
	incentives := in.TotalRewardPot - in.Fees
	if in.PotsReserves < incentives {
		return 0, 0, fmt.Errorf(
			"stake reward incentives %d exceed reserves %d",
			incentives,
			in.PotsReserves,
		)
	}
	if in.BaseReserves < incentives {
		return 0, 0, fmt.Errorf(
			"stake reward incentives %d exceed reserves %d",
			incentives,
			in.BaseReserves,
		)
	}
	reserves := in.BaseReserves - incentives
	var overflow bool
	reserves, overflow = AddRewardUint64(reserves, in.Undistributed)
	if overflow {
		return 0, 0, errors.New("stake reward reserves overflow")
	}
	treasuryTax, err := rewardFloorMul(in.TreasuryExpansion, in.TotalRewardPot)
	if err != nil {
		return 0, 0, fmt.Errorf("stake reward treasury tax: %w", err)
	}
	treasury, overflow := AddRewardUint64(in.BaseTreasury, treasuryTax)
	if overflow {
		return 0, 0, errors.New("stake reward treasury tax overflow")
	}
	treasury, overflow = AddRewardUint64(treasury, in.Unspendable)
	if overflow {
		return 0, 0, errors.New("stake reward treasury overflow")
	}
	treasury, err = rewards.SubtractUnspendableDeficit(
		treasury,
		in.UnspendableDeficit,
	)
	if err != nil {
		return 0, 0, err
	}
	return reserves, treasury, nil
}

// RewardOutputAmount is one account reward output for ComputeRewardTotals.
type RewardOutputAmount struct {
	Amount    uint64
	Spendable bool
	// Guarded marks an output the inactivity guard skips: it is neither
	// credited nor counted, so its amount returns to reserves.
	Guarded bool
}

// RewardTotalsInput carries the values deriveStakeRewardApplicationTotals
// reads from a reward application.
type RewardTotalsInput struct {
	TotalRewardPot     uint64
	Fees               uint64
	TreasuryExpansion  *big.Rat
	UnspendableDeficit uint64
	Outputs            []RewardOutputAmount
}

// RewardTotals is the reward pot split derived from a round's outputs.
type RewardTotals struct {
	// AvailableRewards is the pot after the treasury cut.
	AvailableRewards uint64
	// EffectiveRewards sums spendable (credited) outputs.
	EffectiveRewards uint64
	// Unspendable sums outputs owed to unregistered accounts.
	Unspendable uint64
	// Undistributed is what returns to reserves.
	Undistributed uint64
}

// ComputeRewardTotals splits a reward round's pot into credited, unspendable
// and undistributed amounts, rejecting a round whose outputs exceed the pot.
func ComputeRewardTotals(in RewardTotalsInput) (RewardTotals, error) {
	if in.TotalRewardPot < in.Fees {
		return RewardTotals{}, fmt.Errorf(
			"invalid precomputed reward pot %d below fees %d",
			in.TotalRewardPot,
			in.Fees,
		)
	}
	treasuryTax, err := rewardFloorMul(in.TreasuryExpansion, in.TotalRewardPot)
	if err != nil {
		return RewardTotals{}, fmt.Errorf("calculate treasury tax: %w", err)
	}
	if treasuryTax > in.TotalRewardPot {
		return RewardTotals{}, fmt.Errorf(
			"invalid treasury tax %d above reward pot %d",
			treasuryTax,
			in.TotalRewardPot,
		)
	}
	totals := RewardTotals{AvailableRewards: in.TotalRewardPot - treasuryTax}
	for _, output := range in.Outputs {
		// A guarded output is neither credited nor counted here, so its
		// amount falls through to Undistributed; this must match the
		// crediting loop's skip exactly.
		if output.Guarded {
			continue
		}
		var overflow bool
		if output.Spendable {
			totals.EffectiveRewards, overflow = AddRewardUint64(
				totals.EffectiveRewards,
				output.Amount,
			)
		} else {
			totals.Unspendable, overflow = AddRewardUint64(
				totals.Unspendable,
				output.Amount,
			)
		}
		if overflow {
			return RewardTotals{}, errors.New(
				"stake reward output total overflow",
			)
		}
	}
	credited, overflow := AddRewardUint64(
		totals.EffectiveRewards,
		totals.Unspendable,
	)
	var undistributed uint64
	if !overflow {
		undistributed, err = rewards.UndistributedRewards(
			totals.AvailableRewards,
			credited,
			in.UnspendableDeficit,
		)
	}
	if overflow || err != nil {
		return RewardTotals{}, fmt.Errorf(
			"precomputed rewards exceed available pot: effective=%d unspendable=%d deficit=%d available=%d",
			totals.EffectiveRewards,
			totals.Unspendable,
			in.UnspendableDeficit,
			totals.AvailableRewards,
		)
	}
	totals.Undistributed = undistributed
	return totals, nil
}

// RewardSnapshotInput carries every value BuildRewardSnapshot reads. The
// account sets are keyed by models.StakeCredentialRef.MapKey.
type RewardSnapshotInput struct {
	Snapshot    *models.RewardSnapshot
	PoolInputs  []*models.RewardPoolInput
	StakeInputs []*models.RewardStakeInput
	// BlockCounts maps a pool key hash (as a string) to its blocks made.
	BlockCounts map[string]uint64
	TotalBlocks uint64
	// ActiveAccounts holds the credentials registered now.
	ActiveAccounts map[string]struct{}
	// PrefilterAccounts holds the credentials registered at the prefilter
	// slot; it equals ActiveAccounts when no prefilter applies.
	PrefilterAccounts map[string]struct{}
}

// BuildRewardSnapshot validates the stored reward inputs and builds the
// rewards.Snapshot the calculator consumes.
func BuildRewardSnapshot(in RewardSnapshotInput) (rewards.Snapshot, error) {
	if err := ValidateRewardCalculatorInputs(
		in.Snapshot,
		in.PoolInputs,
		in.StakeInputs,
	); err != nil {
		return rewards.Snapshot{}, err
	}
	return assembleRewardSnapshot(in)
}

// assembleRewardSnapshot builds the snapshot from inputs that have already
// passed ValidateRewardCalculatorInputs.
func assembleRewardSnapshot(
	in RewardSnapshotInput,
) (rewards.Snapshot, error) {
	stakeByPool := make(map[string][]*models.RewardStakeInput)
	for _, input := range in.StakeInputs {
		if input == nil {
			continue
		}
		stakeByPool[string(input.PoolKeyHash)] = append(
			stakeByPool[string(input.PoolKeyHash)],
			input,
		)
	}
	ret := rewards.Snapshot{
		TotalActiveStake:    uint64(in.Snapshot.TotalActiveStake),
		ExcludedActiveStake: (*uint64)(in.Snapshot.ExcludedActiveStake),
		Pools:               make([]rewards.Pool, 0, len(in.PoolInputs)),
	}
	for _, input := range in.PoolInputs {
		if input == nil {
			continue
		}
		pool, err := rewardPoolFromInput(
			input,
			stakeByPool[string(input.PoolKeyHash)],
			in.ActiveAccounts,
			in.PrefilterAccounts,
			in.BlockCounts[string(input.PoolKeyHash)],
			in.TotalBlocks,
		)
		if err != nil {
			return rewards.Snapshot{}, err
		}
		ret.Pools = append(ret.Pools, pool)
	}
	return ret, nil
}

// StakeCredential identifies a stake credential: Tag is the credential kind
// (key or script) and Hash the 28-byte credential hash.
type StakeCredential struct {
	Tag  uint8
	Hash []byte
}

func (c StakeCredential) mapKey() string {
	return models.NewStakeCredentialRef(c.Tag, c.Hash).MapKey()
}

// PoolReapPool is one pool retiring at the boundary, with the deposit its
// effective registration holds and the reward account that receives it.
type PoolReapPool struct {
	PoolKeyHash   []byte
	RewardAccount StakeCredential
	Deposit       uint64
}

// PoolReapDelegation is one account's delegation to a pool.
type PoolReapDelegation struct {
	Credential  StakeCredential
	PoolKeyHash []byte
}

// PoolReapRefund is the deposit refund credited to one retiring pool's reward
// account. Refunds are per pool: two pools sharing a reward account yield two
// entries.
type PoolReapRefund struct {
	PoolKeyHash []byte
	Credential  StakeCredential
	Amount      uint64
}

// PoolReapResult is the outcome of the POOLREAP transition.
type PoolReapResult struct {
	// Refunds are the credits to registered reward accounts, in input order.
	Refunds []PoolReapRefund
	// UnclaimedToTreasury is the sum of the deposits whose reward account is
	// not registered.
	UnclaimedToTreasury uint64
	// Undelegated are the credentials delegated to a retiring pool, in
	// delegation input order.
	Undelegated []StakeCredential
}

// SplitPoolReapDeposit routes one retiring pool's deposit: the refund when its
// reward account is registered, the unclaimed amount otherwise. Exactly one of
// the two results is non-zero for a non-zero deposit.
func SplitPoolReapDeposit(
	deposit uint64,
	accountRegistered bool,
) (refund, unclaimed uint64) {
	if accountRegistered {
		return deposit, 0
	}
	return 0, deposit
}

// ComputePoolReapRefunds computes the POOLREAP transition over the pools
// retiring at the boundary. registered is the set of registered reward
// accounts. It errors when the unclaimed total overflows uint64.
func ComputePoolReapRefunds(
	pools []PoolReapPool,
	delegations []PoolReapDelegation,
	registered []StakeCredential,
) (PoolReapResult, error) {
	registeredSet := make(map[string]struct{}, len(registered))
	for _, c := range registered {
		registeredSet[c.mapKey()] = struct{}{}
	}
	retiring := make(map[string]struct{}, len(pools))
	var result PoolReapResult
	for _, pool := range pools {
		retiring[string(pool.PoolKeyHash)] = struct{}{}
		_, ok := registeredSet[pool.RewardAccount.mapKey()]
		refund, unclaimed := SplitPoolReapDeposit(pool.Deposit, ok)
		if ok {
			result.Refunds = append(result.Refunds, PoolReapRefund{
				PoolKeyHash: pool.PoolKeyHash,
				Credential:  pool.RewardAccount,
				Amount:      refund,
			})
		}
		if unclaimed > math.MaxUint64-result.UnclaimedToTreasury {
			return PoolReapResult{}, errors.New(
				"unclaimed pool deposits overflow uint64",
			)
		}
		result.UnclaimedToTreasury += unclaimed
	}
	for _, d := range delegations {
		if _, ok := retiring[string(d.PoolKeyHash)]; ok {
			result.Undelegated = append(result.Undelegated, d.Credential)
		}
	}
	return result, nil
}

// ComputeEpochDonations returns the treasury after moving the donations
// accumulated during the ended epoch into it. It errors on uint64 overflow.
func ComputeEpochDonations(treasury, donations uint64) (uint64, error) {
	if donations > math.MaxUint64-treasury {
		return 0, fmt.Errorf(
			"treasury overflow adding donations %d to %d",
			donations, treasury,
		)
	}
	return treasury + donations, nil
}

// MIROutcome is the decision the MIR rule takes for one boundary.
type MIROutcome int

const (
	// MIROutcomeEmpty means the boundary carries no pot movement.
	MIROutcomeEmpty MIROutcome = iota
	// MIROutcomeDiscarded means the folded boundary cannot be credited at
	// all; MIRPlan.DiscardReason names why.
	MIROutcomeDiscarded
	// MIROutcomeOverBudget means a pot cannot cover its total, so the whole
	// boundary is a no-op.
	MIROutcomeOverBudget
	// MIROutcomeApplied means the credits and pot transfers are applied.
	MIROutcomeApplied
)

// MIRCreditPlan is one registered-account credit of an applied MIR boundary.
type MIRCreditPlan struct {
	// Pot is MIRPotReserves or MIRPotTreasury.
	Pot        uint
	Credential StakeCredential
	Amount     uint64
}

// MIRPlan is the decision for one MIR boundary. Reserves and Treasury are the
// pots after the boundary; they equal the inputs unless Outcome is
// MIROutcomeApplied.
type MIRPlan struct {
	Outcome       MIROutcome
	DiscardReason string
	Credits       []MIRCreditPlan
	Reserves      uint64
	Treasury      uint64
}

// ComputeMIRBoundary folds every MIR effect of the ended epoch and decides
// whether it applies against the pots. registered is keyed by
// models.StakeCredentialRef.MapKey and restricts distributions to registered
// reward accounts. epochEraID selects the pre- or post-Alonzo accumulation of
// repeated entries. A delta that carries no amount is an error.
func ComputeMIRBoundary(
	effects []models.MIREffect,
	registered map[string]bool,
	epochEraID uint,
	reserves, treasury uint64,
) (MIRPlan, error) {
	plan := MIRPlan{Reserves: reserves, Treasury: treasury}
	boundary, err := foldMIRBoundary(effects, registered, epochEraID)
	if err != nil {
		return plan, err
	}
	if boundary.discard != "" {
		plan.Outcome = MIROutcomeDiscarded
		plan.DiscardReason = boundary.discard
		return plan, nil
	}
	if boundary.isEmpty() {
		plan.Outcome = MIROutcomeEmpty
		return plan, nil
	}
	availableReserves, availableTreasury, fits := boundary.capacity(
		reserves, treasury,
	)
	if boundary.discard != "" {
		plan.Outcome = MIROutcomeDiscarded
		plan.DiscardReason = boundary.discard
		return plan, nil
	}
	if !fits {
		plan.Outcome = MIROutcomeOverBudget
		return plan, nil
	}
	plan.Outcome = MIROutcomeApplied
	plan.Reserves = availableReserves
	plan.Treasury = availableTreasury
	for _, credit := range boundary.credits {
		plan.Credits = append(plan.Credits, MIRCreditPlan{
			Pot: credit.pot,
			Credential: StakeCredential{
				Tag:  credit.credentialTag,
				Hash: credit.credential,
			},
			Amount: credit.amount,
		})
		if credit.pot == MIRPotTreasury {
			plan.Treasury -= credit.amount
		} else {
			plan.Reserves -= credit.amount
		}
	}
	return plan, nil
}

// foldMIRBoundary folds every effect for the ended epoch into a single
// mirBoundary. Distribution deltas are accumulated per (pot, credential) as
// signed values first, then resolved into credits, so a negative delta
// reduces an earlier positive one instead of being applied as a debit the
// reward-account path cannot represent. Credits are restricted to registered
// accounts.
//
// The accumulation preserves first-appearance order, so the credit order is
// deterministic for a given effect order.
//
// epochEraID gates how a repeated (pot, credential) entry combines with its
// running total: additively for era >= Alonzo, matching `Map.unionWith (<>)`,
// or replaced by the latest certificate's amount below Alonzo, matching
// `Map.union`'s left bias on the newer entry. See the era discussion on
// ApplyMIRCerts.
func foldMIRBoundary(
	effects []models.MIREffect,
	registered map[string]bool,
	epochEraID uint,
) (*mirBoundary, error) {
	additive := epochEraID >= alonzo.EraIdAlonzo
	boundary := &mirBoundary{}
	pending := make(map[MIRPendingKey]*big.Int)
	order := make([]MIRPendingKey, 0, len(effects))
	for _, effect := range effects {
		if effect.OtherPot > 0 {
			boundary.addTransfer(effect.Pot, effect.OtherPot)
			if boundary.discard != "" {
				return boundary, nil
			}
			continue
		}
		if effect.Pot != MIRPotReserves && effect.Pot != MIRPotTreasury {
			boundary.discard = fmt.Sprintf(
				"unknown MIR source pot %d", effect.Pot,
			)
			return boundary, nil
		}
		for _, reward := range effect.Rewards {
			ref := models.NewStakeCredentialRef(
				reward.CredentialTag, reward.Credential,
			)
			if !registered[ref.MapKey()] {
				continue
			}
			if reward.Amount == nil {
				return nil, fmt.Errorf(
					"MIR %d carries no delta for credential %x",
					effect.ID,
					reward.Credential,
				)
			}
			key := MIRPendingKey{
				Pot:        effect.Pot,
				Tag:        reward.CredentialTag,
				Credential: string(reward.Credential),
			}
			total, ok := pending[key]
			if !ok {
				total = new(big.Int)
				pending[key] = total
				order = append(order, key)
			}
			if additive {
				total.Add(total, reward.Amount)
			} else {
				total.Set(reward.Amount)
			}
		}
	}
	for _, key := range order {
		net := pending[key]
		if net.Sign() == 0 {
			continue
		}
		if net.Sign() < 0 {
			boundary.discard = fmt.Sprintf(
				"MIR deltas for pot %d credential %x net to %s, "+
					"which no reward account can carry",
				key.Pot,
				key.Credential,
				net,
			)
			return boundary, nil
		}
		if !net.IsUint64() {
			boundary.discard = fmt.Sprintf(
				"MIR deltas for pot %d credential %x net to %s, which exceeds any Ada pot",
				key.Pot,
				key.Credential,
				net,
			)
			return boundary, nil
		}
		boundary.addCredit(mirCredit{
			pot:           key.Pot,
			credentialTag: key.Tag,
			credential:    []byte(key.Credential),
			amount:        net.Uint64(),
		})
		if boundary.discard != "" {
			return boundary, nil
		}
	}
	return boundary, nil
}

// capacity resolves both pots after the boundary's pot-to-pot transfers and
// reports whether each pot covers its distribution total. A discard set while
// resolving (unknown pot, overflow) takes precedence over fits.
func (b *mirBoundary) capacity(
	reserves, treasury uint64,
) (availableReserves, availableTreasury uint64, fits bool) {
	availableReserves, reservesOk := b.availablePot(MIRPotReserves, reserves)
	availableTreasury, treasuryOk := b.availablePot(MIRPotTreasury, treasury)
	fits = reservesOk && treasuryOk &&
		b.totalReserves <= availableReserves &&
		b.totalTreasury <= availableTreasury
	return availableReserves, availableTreasury, fits
}
