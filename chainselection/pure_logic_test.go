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
	"fmt"
	"slices"
	"testing"

	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/stretchr/testify/assert"
)

// densityPeer builds a candidate that forks from the shared intersection
// (slot 100) with one header per given slot.
func densityPeer(id string, idling bool, slots ...uint64) GenesisDensityPeer {
	points := []ocommon.Point{ocommon.NewPoint(100, []byte("intersection"))}
	for i, s := range slots {
		points = append(points, ocommon.NewPoint(s, fmt.Appendf(nil, "%s-%d", id, i)))
	}
	return GenesisDensityPeer{ID: id, Points: points, Idling: idling}
}

func loserIDs(losers []GenesisDensityLoser) []string {
	var ids []string
	for _, l := range losers {
		ids = append(ids, l.ID)
	}
	slices.Sort(ids)
	return ids
}

// Window is slots 101..110 after the intersection at slot 100.
func TestFindGenesisDensityLosers(t *testing.T) {
	t.Parallel()
	full := []uint64{101, 102, 103, 104, 105, 106, 107, 108, 109, 110}
	for name, tc := range map[string]struct {
		k     uint64
		peers []GenesisDensityPeer
		want  []string
	}{
		// b can still reach 1+5 = 6 headers; a has 10 and offers more than k.
		"denser peer beats a sparser one": {2, []GenesisDensityPeer{densityPeer("a", false, full...), densityPeer("b", false, 105)}, []string{"b"}},
		// b is idle, so it stays at 1 header; a's 10 > k.
		"idling peer is compared at its delivered density": {2, []GenesisDensityPeer{densityPeer("a", false, 101, 102, 103, 104), densityPeer("b", true, 105)}, []string{"b"}},
		// a has 4 headers, not more than k = 20, and b's window is open.
		"a rival with k or fewer headers does not qualify": {20, []GenesisDensityPeer{densityPeer("a", false, 101, 102, 103, 104), densityPeer("b", true, 105)}, nil},
		// b's window is complete (head at 110) and a ties it on 3 headers: a
		// is reported first, then no longer counts as b's rival.
		"equal density in a complete window reports one peer": {2, []GenesisDensityPeer{densityPeer("a", false, 101, 105, 110), densityPeer("b", false, 102, 108, 110)}, []string{"a"}},
		"a peer with no headers is not compared":              {2, []GenesisDensityPeer{densityPeer("a", false, full...), densityPeer("b", false)}, nil},
		"a lone candidate is never disconnected":              {2, []GenesisDensityPeer{densityPeer("a", false, 105)}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := FindGenesisDensityLosers(tc.peers, 10, tc.k, map[string]struct{}{})
			assert.Equal(t, tc.want, loserIDs(got))
		})
	}
}

// A peer reported by an earlier evaluation is not a rival, and is not reported
// again.
func TestFindGenesisDensityLosersHonoursPriorDisconnects(t *testing.T) {
	t.Parallel()
	peers := []GenesisDensityPeer{
		densityPeer("a", false, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110),
		densityPeer("b", false, 105),
	}
	got := FindGenesisDensityLosers(peers, 10, 2, map[string]struct{}{"a": {}})
	assert.Empty(t, got, "a was already disconnected, so b has no rival")
	got = FindGenesisDensityLosers(peers, 10, 2, map[string]struct{}{"b": {}})
	assert.Empty(t, got, "b was already disconnected")
}
