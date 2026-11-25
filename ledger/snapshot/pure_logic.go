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
