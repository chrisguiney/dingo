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

package epochengine_test

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeRewardPrefilterSlot(t *testing.T) {
	t.Parallel()
	const max = ^uint64(0)
	tests := []struct {
		name                  string
		start, length, window uint64
		want                  uint64
	}{
		{"one past the window", 1000, 432000, 100, 1101},
		{"no window uses start", 1000, 432000, 0, 1000},
		{"empty epoch uses start", 1000, 0, 100, 1000},
		{"window past end clamps to end", 1000, 50, 100, 1049},
		{"window landing on end", 1000, 102, 100, 1101},
		{"window one past end clamps", 1000, 101, 100, 1100},
		{"overflowing window clamps to end", 1000, 432000, max, 432999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(
				t,
				tt.want,
				epochengine.ComputeRewardPrefilterSlot(
					tt.start, tt.length, tt.window,
				),
			)
		})
	}
}

func TestComputeUpdatedPots(t *testing.T) {
	t.Parallel()
	base := epochengine.UpdatedPotsInput{
		TotalRewardPot:     600,
		Fees:               100,
		PotsReserves:       10000,
		BaseReserves:       10000,
		BaseTreasury:       1000,
		Undistributed:      30,
		Unspendable:        7,
		UnspendableDeficit: 2,
		TreasuryExpansion:  big.NewRat(1, 5),
	}
	t.Run("applies deltas", func(t *testing.T) {
		t.Parallel()
		reserves, treasury, err := epochengine.ComputeUpdatedPots(base)
		require.NoError(t, err)
		assert.Equal(t, uint64(9530), reserves)
		assert.Equal(t, uint64(1125), treasury)
	})
	t.Run("replay baseline replaces the base pots", func(t *testing.T) {
		t.Parallel()
		in := base
		in.BaseReserves = 5000
		in.BaseTreasury = 200
		reserves, treasury, err := epochengine.ComputeUpdatedPots(in)
		require.NoError(t, err)
		assert.Equal(t, uint64(4530), reserves)
		assert.Equal(t, uint64(325), treasury)
	})
	t.Run("incentives above round reserves", func(t *testing.T) {
		t.Parallel()
		in := base
		in.PotsReserves = 499
		_, _, err := epochengine.ComputeUpdatedPots(in)
		require.ErrorContains(t, err, "exceed reserves")
	})
	t.Run("incentives above base reserves", func(t *testing.T) {
		t.Parallel()
		in := base
		in.BaseReserves = 499
		_, _, err := epochengine.ComputeUpdatedPots(in)
		require.ErrorContains(t, err, "exceed reserves")
	})
}

func TestComputeRewardTotals(t *testing.T) {
	t.Parallel()
	base := epochengine.RewardTotalsInput{
		TotalRewardPot:    600,
		Fees:              100,
		TreasuryExpansion: big.NewRat(1, 5),
		Outputs: []epochengine.RewardOutputAmount{
			{Amount: 200, Spendable: true},
			{Amount: 50, Spendable: false},
			{Amount: 30, Spendable: true, Guarded: true},
		},
	}
	t.Run("splits outputs and skips guarded", func(t *testing.T) {
		t.Parallel()
		got, err := epochengine.ComputeRewardTotals(base)
		require.NoError(t, err)
		assert.Equal(t, uint64(480), got.AvailableRewards)
		assert.Equal(t, uint64(200), got.EffectiveRewards)
		assert.Equal(t, uint64(50), got.Unspendable)
		assert.Equal(t, uint64(230), got.Undistributed)
	})
	t.Run("pot below fees", func(t *testing.T) {
		t.Parallel()
		in := base
		in.Fees = 601
		_, err := epochengine.ComputeRewardTotals(in)
		require.ErrorContains(t, err, "below fees")
	})
	t.Run("outputs exceed pot", func(t *testing.T) {
		t.Parallel()
		in := base
		in.Outputs = []epochengine.RewardOutputAmount{
			{Amount: 481, Spendable: true},
		}
		_, err := epochengine.ComputeRewardTotals(in)
		require.ErrorContains(t, err, "exceed available pot")
	})
	t.Run("output total overflow", func(t *testing.T) {
		t.Parallel()
		in := base
		in.Outputs = []epochengine.RewardOutputAmount{
			{Amount: ^uint64(0), Spendable: true},
			{Amount: 1, Spendable: true},
		}
		_, err := epochengine.ComputeRewardTotals(in)
		require.ErrorContains(t, err, "overflow")
	})
}

func rewardKey(tag uint8, b byte) []byte {
	h := make([]byte, 28)
	for i := range h {
		h[i] = b
	}
	_ = tag
	return h
}

func rewardSetKey(tag uint8, b byte) string {
	return models.NewStakeCredentialRef(tag, rewardKey(tag, b)).MapKey()
}

func rewardSnapshotFixture() epochengine.RewardSnapshotInput {
	const captured, boundary = uint64(10), uint64(20)
	pool := rewardKey(0, 0x01)
	return epochengine.RewardSnapshotInput{
		Snapshot: &models.RewardSnapshot{
			TotalActiveStake: 100,
			TotalPoolCount:   1,
			TotalDelegators:  2,
			CapturedSlot:     captured,
			BoundarySlot:     boundary,
		},
		PoolInputs: []*models.RewardPoolInput{{
			PoolKeyHash:    pool,
			RewardAccount:  rewardKey(0, 0x02),
			Margin:         &types.Rat{Rat: big.NewRat(1, 10)},
			Pledge:         60,
			Cost:           5,
			DelegatedStake: 100,
			OwnerStake:     60,
			DelegatorCount: 2,
			CapturedSlot:   captured,
			BoundarySlot:   boundary,
		}},
		StakeInputs: []*models.RewardStakeInput{
			{
				PoolKeyHash: pool, StakingKey: rewardKey(0, 0x03),
				Stake: 60, Owner: true,
				CapturedSlot: captured, BoundarySlot: boundary,
			},
			{
				PoolKeyHash: pool, StakingKey: rewardKey(0, 0x04),
				Stake:        40,
				CapturedSlot: captured, BoundarySlot: boundary,
			},
		},
		BlockCounts: map[string]uint64{string(pool): 7},
		TotalBlocks: 70,
		ActiveAccounts: map[string]struct{}{
			rewardSetKey(0, 0x02): {},
			rewardSetKey(0, 0x04): {},
		},
		PrefilterAccounts: map[string]struct{}{
			rewardSetKey(0, 0x02): {},
		},
	}
}

func TestBuildRewardSnapshot(t *testing.T) {
	t.Parallel()
	t.Run("maps inputs and account sets", func(t *testing.T) {
		t.Parallel()
		got, err := epochengine.BuildRewardSnapshot(rewardSnapshotFixture())
		require.NoError(t, err)
		assert.Equal(t, uint64(100), got.TotalActiveStake)
		require.Len(t, got.Pools, 1)
		pool := got.Pools[0]
		assert.Equal(t, uint64(100), pool.DelegatedStake)
		assert.Equal(t, uint64(60), pool.OwnerStake)
		assert.Equal(t, uint64(7), pool.BlocksProduced)
		assert.Equal(t, uint64(70), pool.TotalBlocks)
		assert.True(t, pool.RewardAccountRegistered)
		assert.True(t, pool.RewardAccountEligible)
		assert.Len(t, pool.Owners, 1)
		require.Len(t, pool.Delegators, 2)
		byStake := map[uint64]bool{}
		for _, d := range pool.Delegators {
			byStake[d.Stake] = d.Eligible
			assert.False(t, d.Registered)
		}
		assert.False(t, byStake[60])
		assert.True(t, byStake[40])
	})
	t.Run("rejects inconsistent pool count", func(t *testing.T) {
		t.Parallel()
		in := rewardSnapshotFixture()
		in.Snapshot.TotalPoolCount = 2
		_, err := epochengine.BuildRewardSnapshot(in)
		require.ErrorContains(t, err, "pool count")
	})
	t.Run("missing snapshot", func(t *testing.T) {
		t.Parallel()
		in := rewardSnapshotFixture()
		in.Snapshot = nil
		_, err := epochengine.BuildRewardSnapshot(in)
		require.ErrorContains(t, err, "missing reward snapshot")
	})
}
