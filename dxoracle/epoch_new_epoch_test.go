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
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/dxoracle"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/stretchr/testify/require"
)

func conwayPParams() *conway.ConwayProtocolParameters {
	rat := func(n, d int64) cbor.Rat { return cbor.Rat{Rat: big.NewRat(n, d)} }
	p := &conway.ConwayProtocolParameters{}
	p.ProtocolVersion.Major = 10
	p.NOpt = 500
	p.A0 = &cbor.Rat{Rat: big.NewRat(3, 10)}
	p.Rho = &cbor.Rat{Rat: big.NewRat(3, 1000)}
	p.Tau = &cbor.Rat{Rat: big.NewRat(1, 5)}
	p.MinCommitteeSize = 3
	p.MinFeeA, p.MinFeeB = 44, 155_381
	p.MaxBlockBodySize, p.MaxTxSize, p.MaxBlockHeaderSize = 90_112, 16_384, 1_100
	p.KeyDeposit, p.PoolDeposit, p.MaxEpoch = 2_000_000, 500_000_000, 18
	p.MinPoolCost, p.AdaPerUtxoByte = 170_000_000, 4_310
	p.GovActionValidityPeriod, p.GovActionDeposit, p.DRepDeposit = 6, 100_000_000_000, 500_000_000
	p.DRepInactivityPeriod, p.CommitteeTermLimit = 20, 146
	p.MinFeeRefScriptCostPerByte = &cbor.Rat{Rat: big.NewRat(15, 1)}
	p.DRepVotingThresholds.MotionNoConfidence = rat(67, 100)
	p.DRepVotingThresholds.CommitteeNormal = rat(67, 100)
	p.DRepVotingThresholds.CommitteeNoConfidence = rat(60, 100)
	p.DRepVotingThresholds.UpdateToConstitution = rat(75, 100)
	p.DRepVotingThresholds.HardForkInitiation = rat(60, 100)
	p.DRepVotingThresholds.PpNetworkGroup = rat(67, 100)
	p.DRepVotingThresholds.PpEconomicGroup = rat(67, 100)
	p.DRepVotingThresholds.PpTechnicalGroup = rat(67, 100)
	p.DRepVotingThresholds.PpGovGroup = rat(75, 100)
	p.DRepVotingThresholds.TreasuryWithdrawal = rat(67, 100)
	p.PoolVotingThresholds.MotionNoConfidence = rat(51, 100)
	p.PoolVotingThresholds.CommitteeNormal = rat(51, 100)
	p.PoolVotingThresholds.CommitteeNoConfidence = rat(51, 100)
	p.PoolVotingThresholds.HardForkInitiation = rat(51, 100)
	p.PoolVotingThresholds.PpSecurityGroup = rat(51, 100)
	return p
}

// A pool retiring at the boundary is removed and its deposit refunded to its
// registered reward account; the mark is read before POOLREAP, so the
// retiring pool's delegator is still in it, and the old mark becomes the set.
func TestNewEpochPoolRetirement(t *testing.T) {
	t.Parallel()
	retire := uint64(11)
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: conwayPParams(), Reserves: 10_000_000_000, Treasury: 1_000_000,
		Accounts: []dxoracle.NewEpochAccount{
			{Key: key(1)},
			{Key: key(2), Reward: 5, Pool: key(0xa0)},
		},
		Pools: []dxoracle.NewEpochPool{{
			Key: key(0xa0), Deposit: 500, RewardKey: key(1), RetiringEpoch: &retire,
		}},
		Stake:     []dxoracle.SnapshotStake{{Key: key(2), Amount: 40}},
		PriorMark: []dxoracle.SnapshotStake{snapRow(3, 100, 0xa0)},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(11), got.Epoch)
	require.Equal(t, [][]byte{key(0xa0)}, got.PoolsRemoved)
	require.Equal(t, uint64(500), got.Accounts[0].Reward)
	require.Empty(t, got.Accounts[1].Pool)
	require.Equal(t, []dxoracle.SnapshotStake{snapRow(2, 45, 0xa0)}, got.Mark)
	require.Equal(t, []dxoracle.SnapshotStake{snapRow(3, 100, 0xa0)}, got.Set)
	t.Logf("reserves=%d treasury=%d", got.Reserves, got.Treasury)
}
