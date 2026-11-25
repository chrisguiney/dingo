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
	"github.com/stretchr/testify/require"
)

func snapRow(k byte, amount uint64, pool byte) dxoracle.SnapshotStake {
	row := dxoracle.SnapshotStake{Key: key(k), Amount: amount}
	if pool != 0 {
		row.Pool = key(pool)
	}
	return row
}

// The boundary into epoch N stores a new Mark, and the Set and Go snapshots
// are the Marks stored under N-1 and N-2.
func TestSnapshotRotateReadsPriorMarksBack(t *testing.T) {
	t.Parallel()
	got, err := shared.SnapshotRotate(context.Background(), dxoracle.SnapshotRotateInput{
		Epoch:     10,
		PriorMark: []dxoracle.SnapshotStake{snapRow(1, 100, 0xa0)},
		PriorSet:  []dxoracle.SnapshotStake{snapRow(2, 200, 0xa0)},
		PriorGo:   []dxoracle.SnapshotStake{snapRow(3, 300, 0xa0)},
	})
	require.NoError(t, err)
	require.Equal(t, []dxoracle.SnapshotStake{snapRow(1, 100, 0xa0)}, got.Set)
	require.Equal(t, []dxoracle.SnapshotStake{snapRow(2, 200, 0xa0)}, got.Go)
	require.Empty(t, got.Mark)
}

// The new Mark is the production capture over live UTxO stake plus reward
// balances of registered, delegated accounts.
func TestSnapshotRotateCapturesMark(t *testing.T) {
	t.Parallel()
	got, err := shared.SnapshotRotate(context.Background(), dxoracle.SnapshotRotateInput{
		Epoch: 10,
		Pools: []dxoracle.SnapshotPool{{Key: key(0xa0)}},
		Accounts: []dxoracle.LiveStakeAccount{
			{Key: key(1), Reward: 5, Active: true, Pool: key(0xa0)},
		},
		Stake: []dxoracle.SnapshotStake{snapRow(1, 100, 0)},
	})
	require.NoError(t, err)
	require.Equal(t, []dxoracle.SnapshotStake{snapRow(1, 105, 0xa0)}, got.Mark)
}

// The fee snapshot is the fee pot row the boundary writes for the new epoch,
// summed over the ended epoch by the production epoch-fee reader.
func TestSnapshotRotateFeeSnapshot(t *testing.T) {
	t.Parallel()
	got, err := shared.SnapshotRotate(context.Background(), dxoracle.SnapshotRotateInput{
		Epoch: 10,
		Fees:  1234,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(1234), got.FeeSnapshot)
}
