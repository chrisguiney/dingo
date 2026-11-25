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
)

// DedupeStakeInputs collapses rows that share a credential to the greatest
// row by pool, then stake, then registration state. The result does not
// depend on the input order, and the input is not modified.
func DedupeStakeInputs(inputs []StakeInput) []StakeInput {
	return dedupeStakeInputs(inputs)
}

// MergePointerStakeInputs adds pointer-derived stake onto the live rows of the
// same credential, appending a row for a credential with no live row. It
// returns an error if an addition overflows uint64. The inputs are not
// modified.
func MergePointerStakeInputs(
	liveInputs []*models.RewardStakeInput,
	pointerInputs []*models.RewardStakeInput,
) ([]*models.RewardStakeInput, error) {
	// mergePointerStakeInputs adds in place to the surviving live row, which
	// production owns exclusively; a pure caller's rows must stay untouched.
	cloned := make([]*models.RewardStakeInput, len(liveInputs))
	for i, in := range liveInputs {
		if in != nil {
			c := *in
			cloned[i] = &c
		}
	}
	return mergePointerStakeInputs(cloned, pointerInputs)
}

// AggregateRewardStakeInputs derives per-pool stake totals and delegator
// counts from a distribution's per-credential inputs, after deduplicating
// them. Zero-stake rows are not counted. It rejects malformed credentials and
// stake overflow.
func AggregateRewardStakeInputs(
	dist *StakeDistribution,
) (*StakeDistribution, error) {
	return rewardStakeDistribution(dist)
}

// ComputeSnapshotRotationEpochs returns the epochs whose stored snapshots
// serve as the Set and Go snapshots once the boundary into newEpoch is
// crossed. Dingo does not copy rows between snapshot kinds: the Mark snapshot
// captured at the boundary is stored under newEpoch, the Set snapshot is the
// one stored under newEpoch-1 and the Go snapshot the one under newEpoch-2,
// each clamped at epoch 0.
func ComputeSnapshotRotationEpochs(
	newEpoch uint64,
) (setEpoch, goEpoch uint64) {
	if newEpoch > 0 {
		setEpoch = newEpoch - 1
	}
	if newEpoch > 1 {
		goEpoch = newEpoch - 2
	}
	return setEpoch, goEpoch
}
