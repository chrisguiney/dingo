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

func TestDRepDormancyBumpsEachDormantEpoch(t *testing.T) {
	t.Parallel()
	got, err := shared.DRepDormancy(context.Background(), dxoracle.DRepDormancyInput{
		Dreps: []dxoracle.DRep{
			{Tag: 0, Key: key(0xd1), ExpiryEpoch: 5, Active: true},
			{Tag: 0, Key: key(0xd2), ExpiryEpoch: 0, Active: true},
		},
		DormantEpochs: 3,
	})
	require.NoError(t, err)
	require.Equal(t, uint64(8), got[0].ExpiryEpoch)
	// A zero expiry records no activity bound and is never bumped.
	require.Equal(t, uint64(0), got[1].ExpiryEpoch)
}

func TestDRepDormancyWithoutDormantEpochsChangesNothing(t *testing.T) {
	t.Parallel()
	got, err := shared.DRepDormancy(context.Background(), dxoracle.DRepDormancyInput{
		Dreps: []dxoracle.DRep{{Tag: 0, Key: key(0xd3), ExpiryEpoch: 5, Active: true}},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(5), got[0].ExpiryEpoch)
}
