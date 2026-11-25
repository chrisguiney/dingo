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
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math"
	"math/big"
	"testing"
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

func cred(b byte) epochengine.StakeCredential {
	h := make([]byte, 28)
	h[0] = b
	return epochengine.StakeCredential{Tag: 0, Hash: h}
}

func poolHash(b byte) []byte {
	h := make([]byte, 28)
	h[27] = b
	return h
}

func TestSplitPoolReapDeposit(t *testing.T) {
	t.Parallel()
	refund, unclaimed := epochengine.SplitPoolReapDeposit(500, true)
	assert.Equal(t, uint64(500), refund)
	assert.Equal(t, uint64(0), unclaimed)
	refund, unclaimed = epochengine.SplitPoolReapDeposit(500, false)
	assert.Equal(t, uint64(0), refund)
	assert.Equal(t, uint64(500), unclaimed)
}

func TestComputePoolReapRefunds(t *testing.T) {
	t.Parallel()
	pools := []epochengine.PoolReapPool{
		{PoolKeyHash: poolHash(1), RewardAccount: cred(1), Deposit: 500},
		{PoolKeyHash: poolHash(2), RewardAccount: cred(2), Deposit: 300},
		{PoolKeyHash: poolHash(3), RewardAccount: cred(1), Deposit: 200},
	}
	delegations := []epochengine.PoolReapDelegation{
		{Credential: cred(10), PoolKeyHash: poolHash(2)},
		{Credential: cred(11), PoolKeyHash: poolHash(9)},
		{Credential: cred(12), PoolKeyHash: poolHash(1)},
	}
	got, err := epochengine.ComputePoolReapRefunds(
		pools, delegations, []epochengine.StakeCredential{cred(1)},
	)
	require.NoError(t, err)
	// Pools 1 and 3 share a registered account: two separate refunds.
	require.Len(t, got.Refunds, 2)
	assert.Equal(t, poolHash(1), got.Refunds[0].PoolKeyHash)
	assert.Equal(t, uint64(500), got.Refunds[0].Amount)
	assert.Equal(t, cred(1), got.Refunds[0].Credential)
	assert.Equal(t, poolHash(3), got.Refunds[1].PoolKeyHash)
	assert.Equal(t, uint64(200), got.Refunds[1].Amount)
	// Pool 2's account is unregistered: its deposit goes to the treasury.
	assert.Equal(t, uint64(300), got.UnclaimedToTreasury)
	// Only delegations to retiring pools are cleared.
	assert.Equal(t,
		[]epochengine.StakeCredential{cred(10), cred(12)},
		got.Undelegated,
	)
}

func TestComputePoolReapRefundsKindDistinguishesAccount(t *testing.T) {
	t.Parallel()
	script := cred(1)
	script.Tag = 1
	got, err := epochengine.ComputePoolReapRefunds(
		[]epochengine.PoolReapPool{
			{PoolKeyHash: poolHash(1), RewardAccount: script, Deposit: 7},
		},
		nil,
		[]epochengine.StakeCredential{cred(1)},
	)
	require.NoError(t, err)
	assert.Empty(t, got.Refunds)
	assert.Equal(t, uint64(7), got.UnclaimedToTreasury)
}

func TestComputePoolReapRefundsUnclaimedOverflow(t *testing.T) {
	t.Parallel()
	_, err := epochengine.ComputePoolReapRefunds(
		[]epochengine.PoolReapPool{
			{PoolKeyHash: poolHash(1), RewardAccount: cred(1), Deposit: math.MaxUint64},
			{PoolKeyHash: poolHash(2), RewardAccount: cred(2), Deposit: 1},
		},
		nil,
		nil,
	)
	require.Error(t, err)
}

func TestComputeEpochDonations(t *testing.T) {
	t.Parallel()
	got, err := epochengine.ComputeEpochDonations(1000, 25)
	require.NoError(t, err)
	assert.Equal(t, uint64(1025), got)
	_, err = epochengine.ComputeEpochDonations(math.MaxUint64, 1)
	require.Error(t, err)
}

func mirReward(c epochengine.StakeCredential, amount int64) models.MIRReward {
	return models.MIRReward{
		Credential:    c.Hash,
		CredentialTag: c.Tag,
		Amount:        big.NewInt(amount),
	}
}

func registeredSet(cs ...epochengine.StakeCredential) map[string]bool {
	out := make(map[string]bool, len(cs))
	for _, c := range cs {
		out[models.NewStakeCredentialRef(c.Tag, c.Hash).MapKey()] = true
	}
	return out
}

func TestComputeMIRBoundaryEraFold(t *testing.T) {
	t.Parallel()
	effects := []models.MIREffect{
		{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(1), 100)}},
		{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(1), 40)}},
	}
	reg := registeredSet(cred(1))

	// Alonzo onward sums repeated entries.
	plan, err := epochengine.ComputeMIRBoundary(
		effects, reg, alonzo.EraIdAlonzo, 1000, 0,
	)
	require.NoError(t, err)
	require.Equal(t, epochengine.MIROutcomeApplied, plan.Outcome)
	require.Len(t, plan.Credits, 1)
	assert.Equal(t, uint64(140), plan.Credits[0].Amount)
	assert.Equal(t, uint64(860), plan.Reserves)

	// Before Alonzo the later certificate replaces the earlier one.
	plan, err = epochengine.ComputeMIRBoundary(
		effects, reg, alonzo.EraIdAlonzo-1, 1000, 0,
	)
	require.NoError(t, err)
	require.Len(t, plan.Credits, 1)
	assert.Equal(t, uint64(40), plan.Credits[0].Amount)
	assert.Equal(t, uint64(960), plan.Reserves)
}

func TestComputeMIRBoundaryNegativeNet(t *testing.T) {
	t.Parallel()
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{
			{Pot: epochengine.MIRPotTreasury, Rewards: []models.MIRReward{mirReward(cred(1), 10)}},
			{Pot: epochengine.MIRPotTreasury, Rewards: []models.MIRReward{mirReward(cred(1), -30)}},
		},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 5, 1000,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeDiscarded, plan.Outcome)
	assert.NotEmpty(t, plan.DiscardReason)
	assert.Equal(t, uint64(5), plan.Reserves)
	assert.Equal(t, uint64(1000), plan.Treasury)
}

func TestComputeMIRBoundaryOverBudgetAndTransfer(t *testing.T) {
	t.Parallel()
	dist := models.MIREffect{
		Pot:     epochengine.MIRPotReserves,
		Rewards: []models.MIRReward{mirReward(cred(1), 150)},
	}
	reg := registeredSet(cred(1))
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{dist}, reg, alonzo.EraIdAlonzo, 100, 0,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeOverBudget, plan.Outcome)
	assert.Empty(t, plan.Credits)
	assert.Equal(t, uint64(100), plan.Reserves)

	// A treasury-to-reserves transfer in the same boundary funds it.
	transfer := models.MIREffect{Pot: epochengine.MIRPotTreasury, OtherPot: 60}
	plan, err = epochengine.ComputeMIRBoundary(
		[]models.MIREffect{transfer, dist}, reg, alonzo.EraIdAlonzo, 100, 80,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeApplied, plan.Outcome)
	assert.Equal(t, uint64(10), plan.Reserves)
	assert.Equal(t, uint64(20), plan.Treasury)
}

func TestComputeMIRBoundarySkipsUnregisteredAndEmpty(t *testing.T) {
	t.Parallel()
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{
			{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(2), 10)}},
		},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 100, 100,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeEmpty, plan.Outcome)
	assert.Equal(t, uint64(100), plan.Reserves)
	assert.Equal(t, uint64(100), plan.Treasury)
}

func TestComputeMIRBoundaryNilAmount(t *testing.T) {
	t.Parallel()
	_, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{{
			Pot: epochengine.MIRPotReserves,
			Rewards: []models.MIRReward{{
				Credential: cred(1).Hash, CredentialTag: 0,
			}},
		}},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 100, 100,
	)
	require.Error(t, err)
}
