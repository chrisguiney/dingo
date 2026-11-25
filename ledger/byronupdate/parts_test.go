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

package byronupdate

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStateFromPartsRoundTrip pins that every field of the update interface
// state survives NewStateFromParts and Parts, and that the rebuilt state
// behaves like one reached by Apply.
func TestStateFromPartsRoundTrip(t *testing.T) {
	t.Parallel()
	delegates := newDelegates(t, 7)
	env := testEnv(delegates)
	proposal := proposalSpec{version: v1, appVer: 1}.build(t, delegates[0], false)
	upId := upIdOf(proposal)

	applied, err := apply(t, NewState(testGenesisParams()), env, 5, delegates[0], v0, proposal)
	require.NoError(t, err)

	parts := applied.Parts()
	require.Equal(t, uint64(5), parts.LastSlot)
	require.Contains(t, parts.ProtocolProposals, upId)
	require.Contains(t, parts.SoftwareProposals, upId)
	require.Equal(t, uint64(5), parts.RegistrationSlot[upId])

	rebuilt := NewStateFromParts(parts)
	require.Equal(t, parts, rebuilt.Parts())

	// A vote against the rebuilt state is registered exactly as against the
	// state Apply produced.
	vote := newVote(t, delegates[1], upId, false)
	want, err := apply(t, applied, env, 6, delegates[1], v0, nil, vote)
	require.NoError(t, err)
	got, err := apply(t, rebuilt, env, 6, delegates[1], v0, nil, vote)
	require.NoError(t, err)
	require.Equal(t, want.Parts(), got.Parts())
	require.Equal(t,
		[]KeyHash{delegates[1].genesis},
		got.Parts().Votes[upId],
	)
}

// TestPartsAreCopies pins that mutating the returned parts leaves the state
// untouched, since a State is a value.
func TestPartsAreCopies(t *testing.T) {
	t.Parallel()
	delegates := newDelegates(t, 7)
	env := testEnv(delegates)
	proposal := proposalSpec{version: v1, appVer: 1}.build(t, delegates[0], false)
	applied, err := apply(t, NewState(testGenesisParams()), env, 5, delegates[0], v0, proposal)
	require.NoError(t, err)

	parts := applied.Parts()
	for id := range parts.RegistrationSlot {
		delete(parts.RegistrationSlot, id)
	}
	require.NotEmpty(t, applied.Parts().RegistrationSlot)
}
