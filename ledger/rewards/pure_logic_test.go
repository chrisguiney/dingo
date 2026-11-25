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
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeApparentPerformance(t *testing.T) {
	t.Parallel()
	zero := new(big.Rat)
	tests := []struct {
		name  string
		d     *big.Rat
		stake uint64
		total uint64
		made  uint64
		all   uint64
		want  *big.Rat
	}{
		{"proportional is one", zero, 10, 100, 5, 50, big.NewRat(1, 1)},
		{"double share", zero, 10, 100, 10, 50, big.NewRat(2, 1)},
		{"no blocks", zero, 10, 100, 0, 50, new(big.Rat)},
		{"d at 4/5 forces one", big.NewRat(4, 5), 10, 100, 0, 50, big.NewRat(1, 1)},
		{"d below 4/5 does not", big.NewRat(79, 100), 10, 100, 0, 50, new(big.Rat)},
		{"zero pool stake", zero, 0, 100, 5, 50, new(big.Rat)},
		{"zero active stake", zero, 10, 0, 5, 50, new(big.Rat)},
		{"zero total blocks divides by one", zero, 10, 100, 1, 0, big.NewRat(10, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ComputeApparentPerformance(tt.d, tt.stake, tt.total, tt.made, tt.all)
			require.NotNil(t, got)
			assert.Zero(t, tt.want.Cmp(got), "want %s got %s", tt.want, got)
		})
	}
}

func TestComputeMaxPool(t *testing.T) {
	t.Parallel()
	base := Parameters{
		OptimalPoolCount: 2,
		PledgeInfluence:  new(big.Rat),
	}
	t.Run("a0 zero is stake share of pot", func(t *testing.T) {
		t.Parallel()
		got := ComputeMaxPool(base, 1000, 1000, 250, 0)
		require.NotNil(t, got)
		assert.Equal(t, int64(250), got.Int64())
	})
	t.Run("saturated pool is capped at 1/nOpt", func(t *testing.T) {
		t.Parallel()
		got := ComputeMaxPool(base, 1000, 1000, 900, 0)
		require.NotNil(t, got)
		assert.Equal(t, int64(500), got.Int64())
	})
	t.Run("pledge raises reward when a0 positive", func(t *testing.T) {
		t.Parallel()
		withA0 := base
		withA0.PledgeInfluence = big.NewRat(1, 2)
		low := ComputeMaxPool(withA0, 1000, 1000, 250, 0)
		high := ComputeMaxPool(withA0, 1000, 1000, 250, 100)
		require.NotNil(t, low)
		require.NotNil(t, high)
		assert.Equal(t, 1, high.Cmp(low))
	})
	t.Run("zero total stake is zero", func(t *testing.T) {
		t.Parallel()
		got := ComputeMaxPool(base, 1000, 0, 0, 0)
		require.NotNil(t, got)
		assert.Zero(t, got.Sign())
	})
	t.Run("leverage cap zeroes a zero-pledge pool", func(t *testing.T) {
		t.Parallel()
		capped := base
		capped.PledgeLeverageEnabled = true
		capped.PledgeLeverage = big.NewRat(10, 1)
		got := ComputeMaxPool(capped, 1000, 1000, 250, 0)
		require.NotNil(t, got)
		assert.Zero(t, got.Sign())
	})
}

func TestComputeMonetaryExpansion(t *testing.T) {
	t.Parallel()
	params := Parameters{
		Decentralization:  new(big.Rat),
		ActiveSlotsCoeff:  big.NewRat(1, 2),
		EpochLength:       100,
		MonetaryExpansion: big.NewRat(1, 10),
		TreasuryExpansion: big.NewRat(1, 5),
	}
	t.Run("half efficiency", func(t *testing.T) {
		t.Parallel()
		got, err := ComputeMonetaryExpansion(params, 1000, 10, 25)
		require.NoError(t, err)
		require.NotNil(t, got.Eta)
		assert.Zero(t, big.NewRat(1, 2).Cmp(got.Eta))
		assert.Equal(t, uint64(50), got.DeltaR1)
		assert.Equal(t, uint64(60), got.RewardPot)
		assert.Equal(t, uint64(12), got.TreasuryCut)
		assert.Equal(t, uint64(48), got.PoolPot)
	})
	t.Run("efficiency above one is capped for the draw", func(t *testing.T) {
		t.Parallel()
		got, err := ComputeMonetaryExpansion(params, 1000, 0, 100)
		require.NoError(t, err)
		assert.Equal(t, uint64(100), got.DeltaR1)
	})
	t.Run("high decentralization forces eta one", func(t *testing.T) {
		t.Parallel()
		high := params
		high.Decentralization = big.NewRat(4, 5)
		got, err := ComputeMonetaryExpansion(high, 1000, 0, 0)
		require.NoError(t, err)
		require.NotNil(t, got.Eta)
		assert.Zero(t, big.NewRat(1, 1).Cmp(got.Eta))
		assert.Equal(t, uint64(100), got.DeltaR1)
	})
	t.Run("zero expected blocks is rejected", func(t *testing.T) {
		t.Parallel()
		zero := params
		zero.EpochLength = 0
		_, err := ComputeMonetaryExpansion(zero, 1000, 0, 0)
		require.ErrorIs(t, err, ErrInvalidParameters)
	})
}
