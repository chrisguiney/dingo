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
	"math"
	"math/big"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/rewards"
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
