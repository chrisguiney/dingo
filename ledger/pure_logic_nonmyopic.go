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
	"math/big"

	"github.com/blinklabs-io/dingo/ledger/rewards"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// NonMyopicPool is one pool's input to the non-myopic ranking.
type NonMyopicPool struct {
	Key lcommon.PoolKeyHash
	// Stake is the stake delegated to the pool; OwnerStake the part the owners
	// delegate.
	Stake      uint64
	OwnerStake uint64
	Pledge     uint64
	Cost       uint64
	Margin     *big.Rat
	// Performance is the pool's performance estimate.
	Performance float64
	// HasLikelihood is false for a pool without a block history, which is
	// never a top pool.
	HasLikelihood bool
}

// NonMyopicMemberRewards returns each pool's non-myopic member reward for a
// delegator holding memberStake: pools are ranked by desirability, the top
// params.OptimalPoolCount of those with a likelihood history are the top pools,
// and the reward follows the ledger's calcNonMyopicMemberReward.
func NonMyopicMemberRewards(
	params rewards.Parameters,
	rewardPot uint64,
	totalStake uint64,
	minPoolCost uint64,
	pools []NonMyopicPool,
	memberStake uint64,
) (map[lcommon.PoolKeyHash]uint64, error) {
	state := &nonMyopicQueryState{
		pools:       make([]nonMyopicPool, 0, len(pools)),
		params:      params,
		rewardPot:   rewardPot,
		totalStake:  totalStake,
		minPoolCost: minPoolCost,
	}
	for _, p := range pools {
		state.pools = append(state.pools, nonMyopicPool{
			key:           p.Key,
			stake:         p.Stake,
			ownerStake:    p.OwnerStake,
			pledge:        p.Pledge,
			cost:          p.Cost,
			margin:        p.Margin,
			performance:   p.Performance,
			hasLikelihood: p.HasLikelihood,
		})
	}
	if err := rankNonMyopicPools(state); err != nil {
		return nil, err
	}
	out := make(map[lcommon.PoolKeyHash]uint64, len(state.pools))
	for _, pool := range state.pools {
		reward, err := nonMyopicMemberReward(pool, memberStake, state)
		if err != nil {
			return nil, err
		}
		out[pool.key] = reward
	}
	return out, nil
}
