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

package dxoracle_test

import (
	"context"
	"testing"

	"github.com/blinklabs-io/dingo/dxoracle"
	"github.com/blinklabs-io/dingo/ledger/governance"
	"github.com/stretchr/testify/require"
)

func hotKey(tag uint8, b byte) string {
	return string([]byte{tag}) + string(key(b))
}

func TestCommitteeVotingStateKeepsAuthorizedSeatedMembers(t *testing.T) {
	t.Parallel()
	got, err := shared.CommitteeVotingState(context.Background(), dxoracle.CommitteeVotingInput{
		Epoch: 5,
		Members: []dxoracle.CommitteeMember{
			{Tag: 0, Key: key(0x01), Expiry: 9, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xa1)}},
			// Authorized but not seated through the epoch: expired.
			{Tag: 0, Key: key(0x02), Expiry: 4, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xa2)}},
			// Seated with no hot key.
			{Tag: 0, Key: key(0x03), Expiry: 9},
			// Authorized then resigned.
			{Tag: 1, Key: key(0x04), Expiry: 9, Hot: &dxoracle.Credential{Tag: 1, Key: key(0xa4)}, Resigned: true},
			// Seated, resigned without ever authorizing.
			{Tag: 0, Key: key(0x05), Expiry: 9, Resigned: true},
			// Expiry equal to the epoch is still seated.
			{Tag: 0, Key: key(0x06), Expiry: 5, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xa6)}},
		},
	})
	require.NoError(t, err)
	require.True(t, got.CommitteePresent)
	require.Equal(t, 2, got.ActiveMemberCount)
	require.Contains(t, got.HotCredentialPresence, hotKey(0, 0xa1))
	require.Contains(t, got.HotCredentialPresence, hotKey(0, 0xa6))
	require.NotContains(t, got.HotCredentialPresence, hotKey(0, 0xa2))
	require.NotContains(t, got.HotCredentialPresence, hotKey(1, 0xa4))
}

func TestCommitteeVotingStateWithoutCommittee(t *testing.T) {
	t.Parallel()
	var got *governance.CommitteeVotingState
	got, err := shared.CommitteeVotingState(context.Background(), dxoracle.CommitteeVotingInput{Epoch: 1})
	require.NoError(t, err)
	require.False(t, got.CommitteePresent)
	require.Zero(t, got.ActiveMemberCount)
}
