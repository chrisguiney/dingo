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
	"context"
	"testing"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/internal/test/dbtest"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/stretchr/testify/require"
)

// The scratch ledger runs the production Conway rollover for one boundary
// built from parameters alone.
func TestScratchLedgerRunsConwayRollover(t *testing.T) {
	t.Parallel()
	db, err := dbtest.NewDatabase(t, &database.Config{DataDir: t.TempDir()})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dbtest.CloseDatabase(db)) })
	set := OpenLiveSetForTest(t, db)

	const epochID = 741
	sl, err := NewScratchLedger(ScratchLedgerConfig{
		Database:   db,
		UtxoSet:    set,
		NodeConfig: epochBoundaryBenchNodeConfig(t),
		Era:        eras.ConwayEraDesc,
		Epoch: newTestEpoch(
			epochID, epochID*govDiffEpochLength, govDiffEpochLength,
			eras.ConwayEraDesc.Id,
		),
		PParams: donationTestConwayPParams(10),
	})
	require.NoError(t, err)

	result, err := sl.RunRollover(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, uint64(epochID+1), result.NewCurrentEpoch.EpochId)
	require.Equal(t, uint64((epochID+1)*govDiffEpochLength), result.NewCurrentEpoch.StartSlot)
}
