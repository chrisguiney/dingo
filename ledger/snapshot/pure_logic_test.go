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

package snapshot

import (
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func pureHash(b byte) []byte {
	h := make([]byte, 28)
	for i := range h {
		h[i] = b
	}
	return h
}

func TestDedupeStakeInputsExported(t *testing.T) {
	t.Parallel()
	rows := []StakeInput{
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(9), Stake: 5},
		{PoolKeyHash: pureHash(2), StakingKey: pureHash(9), Stake: 1},
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(8), Stake: 3},
	}
	got := DedupeStakeInputs(rows)
	require.Len(t, got, 2)
	// Ordered by credential; the credential-9 row under the greater pool wins.
	assert.Equal(t, pureHash(8), got[0].StakingKey)
	assert.Equal(t, pureHash(9), got[1].StakingKey)
	assert.Equal(t, pureHash(2), got[1].PoolKeyHash)
	assert.Len(t, rows, 3)
	reversed := DedupeStakeInputs([]StakeInput{rows[2], rows[1], rows[0]})
	assert.Equal(t, got, reversed)
}

func TestMergePointerStakeInputsExported(t *testing.T) {
	t.Parallel()
	live := []*models.RewardStakeInput{
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(9), Stake: 10},
	}
	pointers := []*models.RewardStakeInput{
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(9), Stake: 4},
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(7), Stake: 2},
	}
	got, err := MergePointerStakeInputs(live, pointers)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, types.Uint64(14), got[0].Stake)
	assert.Equal(t, types.Uint64(2), got[1].Stake)
	assert.Equal(t, types.Uint64(10), live[0].Stake, "input must not change")

	_, err = MergePointerStakeInputs(live, []*models.RewardStakeInput{
		{PoolKeyHash: pureHash(1), StakingKey: pureHash(9), Stake: ^types.Uint64(0)},
	})
	require.ErrorContains(t, err, "overflow")
}

func TestAggregateRewardStakeInputs(t *testing.T) {
	t.Parallel()
	poolA := pureHash(1)
	poolB := pureHash(2)
	dist := &StakeDistribution{
		Slot:             42,
		TotalActiveStake: 99,
		StakeInputs: []StakeInput{
			{PoolKeyHash: poolA, StakingKey: pureHash(5), Stake: 10},
			{PoolKeyHash: poolA, StakingKey: pureHash(6), Stake: 20},
			{PoolKeyHash: poolB, StakingKey: pureHash(7), Stake: 7},
			{PoolKeyHash: poolB, StakingKey: pureHash(8), Stake: 0},
		},
	}
	got, err := AggregateRewardStakeInputs(dist)
	require.NoError(t, err)
	require.NotNil(t, got)
	var a, b lcommon.PoolKeyHash
	copy(a[:], poolA)
	copy(b[:], poolB)
	assert.Equal(t, uint64(30), got.PoolStakes[a])
	assert.Equal(t, uint64(7), got.PoolStakes[b])
	assert.Equal(t, uint64(2), got.DelegatorCount[a])
	assert.Equal(t, uint64(1), got.DelegatorCount[b])
	assert.Equal(t, uint64(37), got.TotalStake)
	assert.Equal(t, uint64(2), got.TotalPools)
	assert.Equal(t, uint64(99), got.TotalActiveStake)

	t.Run("rejects bad credential tag", func(t *testing.T) {
		t.Parallel()
		_, err := AggregateRewardStakeInputs(&StakeDistribution{
			StakeInputs: []StakeInput{
				{PoolKeyHash: poolA, StakingKey: pureHash(5), CredentialTag: 2, Stake: 1},
			},
		})
		require.ErrorContains(t, err, "credential tag")
	})
	t.Run("rejects pool stake overflow", func(t *testing.T) {
		t.Parallel()
		_, err := AggregateRewardStakeInputs(&StakeDistribution{
			StakeInputs: []StakeInput{
				{PoolKeyHash: poolA, StakingKey: pureHash(5), Stake: ^uint64(0)},
				{PoolKeyHash: poolA, StakingKey: pureHash(6), Stake: 1},
			},
		})
		require.ErrorContains(t, err, "overflow")
	})
	t.Run("nil distribution", func(t *testing.T) {
		t.Parallel()
		_, err := AggregateRewardStakeInputs(nil)
		require.Error(t, err)
	})
}

func TestComputeSnapshotRotationEpochs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		newEpoch, wantSet, wantGo uint64
	}{
		{0, 0, 0},
		{1, 0, 0},
		{2, 1, 0},
		{10, 9, 8},
	} {
		setEpoch, goEpoch := ComputeSnapshotRotationEpochs(tc.newEpoch)
		assert.Equal(t, tc.wantSet, setEpoch, "set epoch for %d", tc.newEpoch)
		assert.Equal(t, tc.wantGo, goEpoch, "go epoch for %d", tc.newEpoch)
	}
}
