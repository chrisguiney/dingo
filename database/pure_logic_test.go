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

package database

import (
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pureLogicRows(t *testing.T, fees map[byte]uint) []models.PParamUpdate {
	t.Helper()
	var rows []models.PParamUpdate
	id := uint(1)
	for genesis := byte(1); genesis <= 3; genesis++ {
		fee, ok := fees[genesis]
		if !ok {
			continue
		}
		rows = append(rows, models.PParamUpdate{
			ID:          id,
			GenesisHash: []byte{genesis},
			Cbor:        classicUpdate(t, map[uint64]any{0: fee}),
			AddedSlot:   classicTestEpochStart + uint64(id),
			Epoch:       classicTestSubmissionEpoch,
		})
		id++
	}
	return rows
}

func TestSelectClassicPParamUpdate(t *testing.T) {
	t.Parallel()
	current := classicTestPParams(2, 0)
	rows := pureLogicRows(t, map[byte]uint{1: 77, 2: 77, 3: 90})

	got, err := SelectClassicPParamUpdate(
		rows, classicTestEnactEpoch, classicTestEpochStart, 2,
		current, classicDecode,
	)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, 2, got.Votes)

	// Two of three keys agree, which is below a quorum of three.
	got, err = SelectClassicPParamUpdate(
		rows, classicTestEnactEpoch, classicTestEpochStart, 3,
		current, classicDecode,
	)
	require.NoError(t, err)
	assert.Nil(t, got)

	// Proposals target the epoch before the one being enacted.
	got, err = SelectClassicPParamUpdate(
		rows, classicTestEnactEpoch+1, classicTestEpochStart, 2,
		current, classicDecode,
	)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestClassicPParamQuorumPossible(t *testing.T) {
	t.Parallel()
	rows := pureLogicRows(t, map[byte]uint{1: 77, 2: 77})
	proposers, possible := ClassicPParamQuorumPossible(
		rows, classicTestSubmissionEpoch, 2,
	)
	assert.Equal(t, 2, proposers)
	assert.True(t, possible)
	_, possible = ClassicPParamQuorumPossible(
		rows, classicTestSubmissionEpoch, 3,
	)
	assert.False(t, possible)
	proposers, possible = ClassicPParamQuorumPossible(
		rows, classicTestSubmissionEpoch+1, 0,
	)
	assert.Equal(t, 0, proposers)
	assert.False(t, possible, "no proposers is never possible, even at quorum 0")
}
