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

package chainselection

import (
	"sort"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// GenesisDensityPeer is one live candidate's retained Genesis frontier.
type GenesisDensityPeer struct {
	// ID identifies the peer and orders the candidates.
	ID string
	// Points are the retained headers, ascending.
	Points []ocommon.Point
	// BaseBlock is the block number of Points[0].
	BaseBlock uint64
	// Idling is true once the peer sent MsgAwaitReply.
	Idling bool
}

// GenesisDensityLoser is a peer the density comparison found provably sparser.
type GenesisDensityLoser struct {
	ID                string
	DominatingID      string
	Intersection      ocommon.Point
	WindowSlots       uint64
	DominatingDensity uint64
	MaxDensity        uint64
}

// FindGenesisDensityLosers is the pairwise Genesis Density Disconnector
// comparison. For each pair of peers that fork from a common intersection I,
// peer B is provably sparser than peer A when A meets B's density bound and
// either B's window is complete or A offers more than securityParam headers
// after I. An idling B is bounded by its delivered density; otherwise its
// bound includes every still-possible slot through the window end. Pairs with
// no shared point, and pairs where either peer has no header after I, are not
// compared.
//
// A peer in disconnected is neither compared nor a rival. Each loser is added
// to it as soon as it is found: density is compared at each pair's own
// intersection and is not transitive, so a peer reported in this pass must not
// serve as a rival for a later one, or a cycle could report every candidate.
// peers must be in a stable order for the same reason.
func FindGenesisDensityLosers(
	peers []GenesisDensityPeer,
	window uint64,
	securityParam uint64,
	disconnected map[string]struct{},
) []GenesisDensityLoser {
	var out []GenesisDensityLoser
	for i := range peers {
		b := peers[i]
		if _, done := disconnected[b.ID]; done {
			continue
		}
		for j := range peers {
			if i == j {
				continue
			}
			a := peers[j]
			if _, done := disconnected[a.ID]; done {
				continue
			}
			intersection, ok := intersectPointFrontiers(
				a.Points,
				a.BaseBlock,
				b.Points,
				b.BaseBlock,
			)
			if !ok ||
				intersection.Slot == a.Points[len(a.Points)-1].Slot ||
				intersection.Slot == b.Points[len(b.Points)-1].Slot {
				continue
			}
			windowEnd := safeAddUint64(intersection.Slot, window)
			dominating, _ := fragmentWindowBounds(
				a.Points, intersection.Slot, windowEnd,
			)
			minDensity, maxDensity := fragmentWindowBounds(
				b.Points, intersection.Slot, windowEnd,
			)
			rivalStart := sort.Search(len(a.Points), func(i int) bool {
				return a.Points[i].Slot > intersection.Slot
			})
			// #nosec G115 -- rivalStart is an index within a.Points.
			rivalBlocks := uint64(len(a.Points) - rivalStart)
			offersMoreThanK := rivalBlocks > securityParam
			if !offersMoreThanK && minDensity != maxDensity {
				continue
			}
			comparisonBound := maxDensity
			if b.Idling {
				comparisonBound = minDensity
			}
			if dominating < comparisonBound {
				continue
			}
			disconnected[b.ID] = struct{}{}
			out = append(out, GenesisDensityLoser{
				ID:                b.ID,
				DominatingID:      a.ID,
				Intersection:      intersection,
				WindowSlots:       window,
				DominatingDensity: dominating,
				MaxDensity:        comparisonBound,
			})
			break
		}
	}
	return out
}
