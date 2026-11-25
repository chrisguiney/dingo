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

// ClassicPParamUpdateWindow returns the epoch that holds a slot and the first
// slot of that epoch whose Shelley-family protocol parameter proposals target
// the following epoch. The point of no return is the next epoch's first slot
// less twice the stability window (Cardano.Ledger.Slot.getTheSlotOfNoReturn).
// When the window covers the whole epoch every slot targets the next epoch,
// which the epoch's first slot expresses.
func ClassicPParamUpdateWindow(
	epochID, epochStartSlot, epochLength, stabilityWindow uint64,
) (uint64, uint64) {
	if stabilityWindow > (^uint64(0))/2 || 2*stabilityWindow >= epochLength {
		return epochID, epochStartSlot
	}
	return epochID, epochStartSlot + epochLength - 2*stabilityWindow
}
