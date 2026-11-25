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

package ledger

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/ledger/rewards"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/require"
)

func nonMyopicKey(b byte) lcommon.PoolKeyHash {
	return lcommon.PoolKeyHash(lcommon.NewBlake2b224(bytes.Repeat([]byte{b}, 28)))
}

// nOpt 2, a0 3/10, reward pot 1e9, circulation 1000, member stake 50.
//
// Desirability (cost-adjusted, float64): A 347330769.2, B 152707692.3, C 0
// (its cost exceeds f-tilde). The top two pools are A and B.
//
//	A: nonMyopicStake = max(100/1000 + 50/1000, 1/2) = 1/2; maxPool' =
//	   floor(1e9 / 1.3 * (1/2 + (1/100) * 0.3 * (1/2) / (1/2))) = 386923076;
//	   fHat = 386923076; member = floor((fHat - 1e6) * 0.9 * 0.05 / 0.5)
//	   = 34733076.
//	B: nonMyopicStake = 1/2; maxPool' = 385769230; fHat = floor(0.5 * that) =
//	   192884615; member = floor((fHat - 2e6) * 0.8 * 0.05 / 0.5) = 15270769.
//	C: outside the top pools; its pledge-plus-member stake gives an fHat
//	   below its cost of 4e8: 0.
func TestNonMyopicMemberRewards(t *testing.T) {
	t.Parallel()
	params := rewards.Parameters{
		OptimalPoolCount: 2,
		PledgeInfluence:  big.NewRat(3, 10),
	}
	pools := []NonMyopicPool{
		{Key: nonMyopicKey(0xa1), Stake: 100, OwnerStake: 20, Pledge: 10, Cost: 1_000_000, Margin: big.NewRat(1, 10), Performance: 1.0, HasLikelihood: true},
		{Key: nonMyopicKey(0xb2), Stake: 50, OwnerStake: 5, Pledge: 5, Cost: 2_000_000, Margin: big.NewRat(1, 5), Performance: 0.5, HasLikelihood: true},
		{Key: nonMyopicKey(0xc3), Stake: 10, OwnerStake: 1, Pledge: 1, Cost: 400_000_000, Margin: new(big.Rat), Performance: 1.0, HasLikelihood: true},
	}
	got, err := NonMyopicMemberRewards(params, 1_000_000_000, 1000, 0, pools, 50)
	require.NoError(t, err)
	require.Equal(t, map[lcommon.PoolKeyHash]uint64{
		nonMyopicKey(0xa1): 34_733_076,
		nonMyopicKey(0xb2): 15_270_769,
		nonMyopicKey(0xc3): 0,
	}, got)
}

// A pool whose pledge exceeds its owners' stake pays nothing.
func TestNonMyopicMemberRewardsPledgeNotMet(t *testing.T) {
	t.Parallel()
	params := rewards.Parameters{OptimalPoolCount: 2, PledgeInfluence: big.NewRat(3, 10)}
	pools := []NonMyopicPool{
		{Key: nonMyopicKey(0xa1), Stake: 100, OwnerStake: 9, Pledge: 10, Cost: 1_000_000, Margin: big.NewRat(1, 10), Performance: 1.0, HasLikelihood: true},
	}
	got, err := NonMyopicMemberRewards(params, 1_000_000_000, 1000, 0, pools, 50)
	require.NoError(t, err)
	require.Equal(t, uint64(0), got[nonMyopicKey(0xa1)])
}

// A pool without a likelihood history is never a top pool, so it earns on
// the pledge-plus-member stake: A drops from 34733076 to the smaller
// reward of its non-top stake.
func TestNonMyopicMemberRewardsWithoutLikelihoodIsNotTop(t *testing.T) {
	t.Parallel()
	params := rewards.Parameters{OptimalPoolCount: 2, PledgeInfluence: big.NewRat(3, 10)}
	pool := NonMyopicPool{Key: nonMyopicKey(0xa1), Stake: 100, OwnerStake: 20, Pledge: 10, Cost: 1_000_000, Margin: big.NewRat(1, 10), Performance: 1.0}
	top, err := NonMyopicMemberRewards(params, 1_000_000_000, 1000, 0, []NonMyopicPool{withLikelihood(pool)}, 50)
	require.NoError(t, err)
	require.Equal(t, uint64(34_733_076), top[nonMyopicKey(0xa1)])
	notTop, err := NonMyopicMemberRewards(params, 1_000_000_000, 1000, 0, []NonMyopicPool{pool}, 50)
	require.NoError(t, err)
	require.Less(t, notTop[nonMyopicKey(0xa1)], top[nonMyopicKey(0xa1)])
}

func withLikelihood(p NonMyopicPool) NonMyopicPool {
	p.HasLikelihood = true
	return p
}
