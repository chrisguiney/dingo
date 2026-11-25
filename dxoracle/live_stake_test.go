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
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/stretchr/testify/require"
)

// baseOutput is the CBOR of a Shelley output to a base address staking with
// the given key hash.
func baseOutput(t *testing.T, stake byte, amount uint64) []byte {
	t.Helper()
	addr, err := lcommon.NewAddressFromParts(
		lcommon.AddressTypeKeyKey, 0, key(0x70), key(stake),
	)
	require.NoError(t, err)
	out := shelley.ShelleyTransactionOutput{
		OutputAddress: addr, OutputAmount: amount,
	}
	data, err := cbor.Encode(&out)
	require.NoError(t, err)
	return data
}

// K1 holds 10 + 5 in outputs and 3 in rewards and delegates to pool P; K2
// holds 7 and delegates nowhere; K3 holds only 2 in rewards and delegates to
// P; K4 is not registered and holds 4.
func TestLiveStakeSumsOutputsAndRewardsPerCredential(t *testing.T) {
	t.Parallel()
	pool := key(0xaa)
	rows, err := shared.LiveStake(context.Background(), dxoracle.LiveStakeInput{
		Slot: 100,
		Accounts: []dxoracle.LiveStakeAccount{
			{Key: key(1), Reward: 3, Active: true, Pool: pool},
			{Key: key(2), Active: true},
			{Key: key(3), Reward: 2, Active: true, Pool: pool},
		},
		Outputs: [][]byte{
			baseOutput(t, 1, 10), baseOutput(t, 1, 5), baseOutput(t, 2, 7), baseOutput(t, 4, 4),
		},
	})
	require.NoError(t, err)
	got := map[byte]dxoracle.LiveStakeRow{}
	for _, r := range rows {
		got[r.Key[0]] = r
	}
	require.Len(t, got, 4)
	require.Equal(t, uint64(15), got[1].UtxoStake)
	require.Equal(t, uint64(3), got[1].RewardStake)
	require.Equal(t, uint64(18), got[1].TotalStake)
	require.Equal(t, pool, got[1].Pool)
	require.True(t, got[1].Registered)
	require.Equal(t, uint64(7), got[2].TotalStake)
	require.Empty(t, got[2].Pool)
	require.Equal(t, uint64(2), got[3].TotalStake)
	require.Equal(t, pool, got[3].Pool)
	require.Equal(t, uint64(4), got[4].TotalStake)
	require.False(t, got[4].Registered)
}

// A second call must not see the first call's rows.
func TestLiveStakeCallsAreIsolated(t *testing.T) {
	t.Parallel()
	_, err := shared.LiveStake(context.Background(), dxoracle.LiveStakeInput{
		Slot:     100,
		Accounts: []dxoracle.LiveStakeAccount{{Key: key(9), Active: true, Reward: 1}},
		Outputs:  [][]byte{baseOutput(t, 9, 50)},
	})
	require.NoError(t, err)
	rows, err := shared.LiveStake(context.Background(), dxoracle.LiveStakeInput{Slot: 100})
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestLiveStakeRejectsUndecodableOutput(t *testing.T) {
	t.Parallel()
	_, err := shared.LiveStake(context.Background(), dxoracle.LiveStakeInput{
		Slot: 100, Outputs: [][]byte{{0xff}},
	})
	require.ErrorIs(t, err, dxoracle.ErrDecodeOutput)
}
