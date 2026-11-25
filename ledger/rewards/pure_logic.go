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

package rewards

import (
	"fmt"
	"math/big"
)

// MonetaryExpansion is the outcome of the per-epoch monetary expansion step
// (createRUpd in cardano-ledger): the efficiency-scaled draw from reserves,
// the treasury cut of the resulting pot, and the pot left for pools.
type MonetaryExpansion struct {
	// Eta is the network reward efficiency.
	Eta *big.Rat
	// DeltaR1 is the amount drawn from reserves.
	DeltaR1 uint64
	// RewardPot is DeltaR1 plus the epoch's fees.
	RewardPot uint64
	// TreasuryCut is the floor of the treasury expansion rate times RewardPot.
	TreasuryCut uint64
	// PoolPot is RewardPot less TreasuryCut.
	PoolPot uint64
	// ExpectedBlocks is the integer block expectation behind Eta.
	ExpectedBlocks *big.Rat
}

// ComputeMonetaryExpansion derives the reward pot for one epoch from reserves,
// fees and the blocks pools made. It reads params.Decentralization,
// ActiveSlotsCoeff, EpochLength, MonetaryExpansion and TreasuryExpansion.
func ComputeMonetaryExpansion(
	params Parameters,
	reserves uint64,
	fees uint64,
	blocksTotal uint64,
) (MonetaryExpansion, error) {
	expected := expectedBlocks(params)
	if expected.Sign() == 0 &&
		params.Decentralization.Cmp(new(big.Rat).SetFrac64(4, 5)) < 0 {
		return MonetaryExpansion{}, fmt.Errorf(
			"%w: expected blocks is zero",
			ErrInvalidParameters,
		)
	}
	efficiency := Efficiency(blocksTotal, params)
	incentives, err := floorMulChecked(
		minRat(oneRat(), efficiency),
		params.MonetaryExpansion,
		uintRat(reserves),
	)
	if err != nil {
		return MonetaryExpansion{}, fmt.Errorf("calculate incentives: %w", err)
	}
	rewardPot, overflow := addUint64(incentives, fees)
	if overflow {
		return MonetaryExpansion{}, fmt.Errorf(
			"%w: reward pot overflow",
			ErrInvalidParameters,
		)
	}
	treasuryCut, err := floorMulChecked(
		params.TreasuryExpansion,
		uintRat(rewardPot),
	)
	if err != nil {
		return MonetaryExpansion{}, fmt.Errorf("calculate treasury tax: %w", err)
	}
	return MonetaryExpansion{
		Eta:            efficiency,
		DeltaR1:        incentives,
		RewardPot:      rewardPot,
		TreasuryCut:    treasuryCut,
		PoolPot:        rewardPot - treasuryCut,
		ExpectedBlocks: expected,
	}, nil
}

// ComputeApparentPerformance implements mkApparentPerformance: a pool's block
// share relative to its stake share, or 1 when decentralization is at least
// 4/5 and 0 for a pool or network with no stake.
func ComputeApparentPerformance(
	d *big.Rat,
	poolStake uint64,
	totalActiveStake uint64,
	blocks uint64,
	blocksTotal uint64,
) *big.Rat {
	return apparentPerformance(
		d, poolStake, totalActiveStake, blocks, blocksTotal,
	)
}

// ComputeMaxPool implements maxPool' for one pool: the most a pool can earn
// from poolPot at the given stake and pledge, floored toward negative
// infinity. It reads params.OptimalPoolCount, PledgeInfluence and, when
// enabled, the CIP-50 pledge leverage.
func ComputeMaxPool(
	params Parameters,
	poolPot uint64,
	totalStake uint64,
	poolStake uint64,
	pledge uint64,
) *big.Int {
	return optimalPoolRewardChecked(
		poolPot,
		params.OptimalPoolCount,
		params.PledgeInfluence,
		poolStake,
		pledge,
		totalStake,
		params.pledgeLeverageCap(),
	)
}
