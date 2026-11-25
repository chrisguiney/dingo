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
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/dxoracle"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/dingo/ledger/governance"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
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

// A pending reward round pays the pool and its delegator at the boundary.
// With rho = 3/1000, tau = 1/5, no fees and eta = 1 (21600 blocks, the
// expected count for f = 1/20 over 432000 slots), reserves of 10^12 give
// deltaR1 = 3_000_000_000 and a treasury cut of 600_000_000, leaving a pool
// pot of 2_400_000_000. The pool holds all active stake, sigma = 1/1000 of
// the 4.4999e16 circulation, so maxPool = floor(2.4e9 / 1.3 * 0.001) =
// 1_846_153 and the pool reward is that at performance 1. With margin 1/100
// and no owners the leader gets floor(1_846_153 / 100) = 18_461 and the
// member floor(1_846_153 * 99 / 100) = 1_827_691; the 1 left over returns
// to reserves with the rest of the pool pot.
func TestNewEpochRewardApplication(t *testing.T) {
	t.Parallel()
	const stake = 44_999_000_000_000
	blocks := uint64(21600)
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: conwayPParams(), Reserves: 1_000_000_000_000, Treasury: 1_000_000,
		Accounts: []dxoracle.NewEpochAccount{
			{Key: key(1)},
			{Key: key(2), Pool: key(0xa0)},
		},
		Pools: []dxoracle.NewEpochPool{{Key: key(0xa0), RewardKey: key(1)}},
		Rewards: &dxoracle.NewEpochRewards{
			Snapshot: &models.RewardSnapshot{
				TotalActiveStake: stake, ExcludedActiveStake: new(types.Uint64),
				TotalPoolCount: 1, TotalDelegators: 1,
			},
			PoolInputs: []*models.RewardPoolInput{{
				PoolKeyHash: key(0xa0), RewardAccount: key(1),
				Margin:         &types.Rat{Rat: big.NewRat(1, 100)},
				DelegatedStake: stake, DelegatorCount: 1,
				BlocksProduced: &blocks, TotalBlocksInEpoch: &blocks,
			}},
			StakeInputs: []*models.RewardStakeInput{{
				PoolKeyHash: key(0xa0), StakingKey: key(2), Stake: stake, Registered: true,
			}},
			BlockCounts: map[string]uint64{string(key(0xa0)): blocks},
		},
		PriorGo: []dxoracle.SnapshotStake{snapRow(2, stake, 0xa0)},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(18_461), got.Accounts[0].Reward)
	require.Equal(t, uint64(1_827_691), got.Accounts[1].Reward)
	require.Equal(t, uint64(999_398_153_848), got.Reserves)
	require.Equal(t, uint64(601_000_000), got.Treasury)
}

// A boundary leaves a DRep delegation as it found it, whether to a DRep or to
// a predefined option, and a reward credit does not touch it.
func TestNewEpochKeepsDRepDelegation(t *testing.T) {
	t.Parallel()
	retire := uint64(11)
	delegations := []dxoracle.Delegation{
		{Type: dxoracle.DelegateAlwaysAbstain},
		{Type: dxoracle.DelegateAlwaysNoConfidence},
		{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)},
		{Type: dxoracle.DelegateScriptHash, Key: key(0xd2)},
		{},
	}
	accounts := []dxoracle.NewEpochAccount{{Key: key(1)}}
	for i, d := range delegations {
		accounts = append(accounts, dxoracle.NewEpochAccount{Key: key(byte(10 + i)), Pool: key(0xa0), Drep: d})
	}
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: conwayPParams(), Reserves: 10_000_000_000, Treasury: 1_000_000,
		Accounts: accounts,
		Pools:    []dxoracle.NewEpochPool{{Key: key(0xa0), Deposit: 500, RewardKey: key(1), RetiringEpoch: &retire}},
	})
	require.NoError(t, err)
	for i, d := range delegations {
		require.Equal(t, d, got.Accounts[i+1].Drep, "account %d", i)
	}
}

// The boundary enacts what the boundary before it ratified. One treasury
// withdrawal is accepted by the DRep and committee votes and pays 1_000_000
// to a registered account at the next boundary; a second proposal has expired
// and its deposit goes back to its registered return account, together with
// the proposal it carries as a child.
func TestNewEpochEnactsRatifiedAndDropsExpired(t *testing.T) {
	t.Parallel()
	pp := conwayPParams()
	pp.MinCommitteeSize = 1
	pp.DRepVotingThresholds.TreasuryWithdrawal = unit(1, 2)
	withdrawal := mustHex("8302a1581de0" + hex.EncodeToString(key(1)) + "1a000f4240f6")
	gov := dxoracle.GovState{
		PParams:  pp,
		Treasury: 5_000_000,
		Accounts: []dxoracle.Account{
			{Tag: 0, Key: key(1), Reward: 7, Active: true},
			{Tag: 0, Key: key(2), Reward: 11, Active: true},
		},
		Dreps:     []dxoracle.DRep{{Tag: 0, Key: key(0xd1), ExpiryEpoch: 30, Active: true}},
		DRepStake: []dxoracle.GovDRepStake{{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 100}},
		Committee: []dxoracle.CommitteeMember{{Tag: 0, Key: key(0xc1), Expiry: 40, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xc2)}}},
		Quorum:    big.NewRat(1, 1),
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xa0), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{
			proposal(1, withdrawal, 2, nil, 15, 1000, 0xa2),
			proposal(2, mustHex(infoAction), 6, nil, 5, 300, 0x02),
		},
		Votes: []dxoracle.GovVote{
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1},
			{Proposal: 0, Role: 0, Tag: 0, Key: key(0xc2), Vote: 1},
		},
	}
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: pp, Reserves: 10_000_000_000, Treasury: 5_000_000,
		Accounts: []dxoracle.NewEpochAccount{{Key: key(1), Reward: 7}, {Key: key(2), Reward: 11}},
		Gov:      &gov,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Gov)
	require.Equal(t, []dxoracle.GovAction{action(1)}, got.Gov.Enacted)
	require.Equal(t, []dxoracle.GovAction{action(2)}, got.Gov.Expired)
	require.Equal(t, []dxoracle.GovAction{action(1), action(2)}, got.Gov.Removed)
	// 5_000_000 less the withdrawal, plus the 1000 deposit whose return
	// account is not registered.
	require.Equal(t, uint64(4_001_000), got.Treasury)
	require.Equal(t, uint64(7+1_000_000), got.Accounts[0].Reward)
	require.Equal(t, uint64(11+300), got.Accounts[1].Reward)
}

// Without the committee's vote the withdrawal is not ratified, so nothing is
// enacted and only the expired proposal leaves.
func TestNewEpochGovUnratifiedWithdrawalStays(t *testing.T) {
	t.Parallel()
	pp := conwayPParams()
	pp.MinCommitteeSize = 1
	pp.DRepVotingThresholds.TreasuryWithdrawal = unit(1, 2)
	withdrawal := mustHex("8302a1581de0" + hex.EncodeToString(key(1)) + "1a000f4240f6")
	gov := dxoracle.GovState{
		PParams:   pp,
		Treasury:  5_000_000,
		Accounts:  []dxoracle.Account{{Tag: 0, Key: key(1), Reward: 7, Active: true}},
		Dreps:     []dxoracle.DRep{{Tag: 0, Key: key(0xd1), ExpiryEpoch: 30, Active: true}},
		DRepStake: []dxoracle.GovDRepStake{{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 100}},
		Committee: []dxoracle.CommitteeMember{{Tag: 0, Key: key(0xc1), Expiry: 40, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xc2)}}},
		Quorum:    big.NewRat(1, 1),
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xa0), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{proposal(1, withdrawal, 2, nil, 15, 1000, 0xa2)},
		Votes:     []dxoracle.GovVote{{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1}},
	}
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: pp, Reserves: 10_000_000_000, Treasury: 5_000_000,
		Accounts: []dxoracle.NewEpochAccount{{Key: key(1), Reward: 7}},
		Gov:      &gov,
	})
	require.NoError(t, err)
	require.Empty(t, got.Gov.Enacted)
	require.Empty(t, got.Gov.Removed)
	require.Equal(t, uint64(5_000_000), got.Treasury)
	require.Equal(t, uint64(7), got.Accounts[0].Reward)
}

// A ParameterChange the boundary before accepted is enacted at the boundary:
// max_block_body_size (key 2) goes from 65_536 to 0x16000 = 90_112 and the
// result holds every other parameter as it was. The change is in the
// security group, so the DRep, committee and pool votes are all needed.
func TestNewEpochEnactsParameterChange(t *testing.T) {
	t.Parallel()
	pp := conwayPParams()
	pp.MaxBlockBodySize = 65_536
	pp.MinCommitteeSize = 1
	gov := dxoracle.GovState{
		PParams:   pp,
		Treasury:  5_000_000,
		Accounts:  []dxoracle.Account{{Tag: 0, Key: key(1), Active: true}},
		Dreps:     []dxoracle.DRep{{Tag: 0, Key: key(0xd1), ExpiryEpoch: 30, Active: true}},
		DRepStake: []dxoracle.GovDRepStake{{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 100}},
		Committee: []dxoracle.CommitteeMember{{Tag: 0, Key: key(0xc1), Expiry: 40, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xc2)}}},
		Quorum:    big.NewRat(1, 1),
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xa0), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{proposal(1, pcAction(0), 0, nil, 15, 0, 0xa2)},
		Votes: []dxoracle.GovVote{
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1},
			{Proposal: 0, Role: 0, Tag: 0, Key: key(0xc2), Vote: 1},
			{Proposal: 0, Role: 2, Key: key(0xa0), Vote: 1},
		},
	}
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: pp, Reserves: 10_000_000_000, Treasury: 5_000_000,
		Accounts: []dxoracle.NewEpochAccount{{Key: key(1)}},
		Gov:      &gov,
	})
	require.NoError(t, err)
	require.Equal(t, []dxoracle.GovAction{action(1)}, got.Gov.Enacted)
	after, ok := got.PParams.(*conway.ConwayProtocolParameters)
	require.True(t, ok, "pparams %T", got.PParams)
	require.Equal(t, uint(90_112), after.MaxBlockBodySize)
	want := *pp
	want.MaxBlockBodySize = 90_112
	require.Equal(t, want.MinFeeA, after.MinFeeA)
	require.Equal(t, want.ProtocolVersion, after.ProtocolVersion)
}

// Without an accepted ParameterChange the parameters come back as they went in.
func TestNewEpochKeepsParameters(t *testing.T) {
	t.Parallel()
	pp := conwayPParams()
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: pp, Reserves: 10_000_000_000, Treasury: 1_000_000,
		Accounts: []dxoracle.NewEpochAccount{{Key: key(1)}},
	})
	require.NoError(t, err)
	after, ok := got.PParams.(*conway.ConwayProtocolParameters)
	require.True(t, ok, "pparams %T", got.PParams)
	require.Equal(t, pp.MaxBlockBodySize, after.MaxBlockBodySize)
	require.Equal(t, pp.ProtocolVersion, after.ProtocolVersion)
}

// hardForkBoundary enacts a ratified hard fork to to (major, minor) at a
// ledger on protocol from, with account 0x72 delegating to a DRep nobody
// registered and 0x71 to the registered DRep.
func hardForkBoundary(t *testing.T, from uint, hf string) dxoracle.NewEpochOutput {
	t.Helper()
	pp := conwayPParams()
	pp.ProtocolVersion.Major = from
	pp.MinCommitteeSize = 1
	gov := dxoracle.GovState{
		PParams:   pp,
		Treasury:  5_000_000,
		Accounts:  []dxoracle.Account{{Tag: 0, Key: key(1), Active: true}},
		Dreps:     []dxoracle.DRep{{Tag: 0, Key: key(0xd1), ExpiryEpoch: 30, Active: true}},
		DRepStake: []dxoracle.GovDRepStake{{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 100}},
		Committee: []dxoracle.CommitteeMember{{Tag: 0, Key: key(0xc1), Expiry: 40, Hot: &dxoracle.Credential{Tag: 0, Key: key(0xc2)}}},
		Quorum:    big.NewRat(1, 1),
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xa0), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{proposal(1, mustHex(hf), 1, nil, 15, 0, 0xa2)},
		Votes: []dxoracle.GovVote{
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1},
			{Proposal: 0, Role: 0, Tag: 0, Key: key(0xc2), Vote: 1},
			{Proposal: 0, Role: 2, Key: key(0xa0), Vote: 1},
		},
	}
	got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
		Epoch: 10, PParams: pp, Reserves: 10_000_000_000, Treasury: 5_000_000,
		Accounts: []dxoracle.NewEpochAccount{
			{Key: key(1)},
			{Key: key(0x71), Drep: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}},
			{Key: key(0x72), Drep: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xdd)}},
		},
		Gov: &gov,
	})
	require.NoError(t, err)
	require.Equal(t, []dxoracle.GovAction{action(1)}, got.Gov.Enacted)
	return got
}

// Enacting a hard fork to 10 runs the protocol 10 HARDFORK step: the version
// changes and the delegation to the unregistered DRep is cleared while the
// delegation to the registered DRep stays.
func TestNewEpochHardForkTo10ClearsDanglingDelegation(t *testing.T) {
	t.Parallel()
	got := hardForkBoundary(t, 9, "8301f6820a00")
	after := got.PParams.(*conway.ConwayProtocolParameters)
	require.Equal(t, uint(10), after.ProtocolVersion.Major)
	require.Equal(t, uint(0), after.ProtocolVersion.Minor)
	require.Equal(t, dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, got.Accounts[1].Drep)
	require.Equal(t, dxoracle.Delegation{}, got.Accounts[2].Drep)
}

// A hard fork from 10 to 11 changes only the version: the protocol 11 step
// has no effect on delegations.
func TestNewEpochHardForkTo11KeepsDelegations(t *testing.T) {
	t.Parallel()
	got := hardForkBoundary(t, 10, "8301f6820b00")
	after := got.PParams.(*conway.ConwayProtocolParameters)
	require.Equal(t, uint(11), after.ProtocolVersion.Major)
	require.Equal(t, dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xdd)}, got.Accounts[2].Drep)
}

// A pre-Conway boundary runs the same pool retirement, refund and snapshot
// rotation as Conway's: no governance state exists. Shelley, Allegra and Mary
// share the Shelley parameters; Alonzo's and Babbage's carry their own.
func TestNewEpochPreConwayPoolRetirement(t *testing.T) {
	t.Parallel()
	shelleyPP := func(major uint) lcommon.ProtocolParameters {
		pp := &shelley.ShelleyProtocolParameters{}
		pp.ProtocolMajor = major
		return pp
	}
	alonzoPP := &alonzo.AlonzoProtocolParameters{}
	alonzoPP.ProtocolMajor = 5
	babbagePP := &babbage.BabbageProtocolParameters{}
	babbagePP.ProtocolMajor = 8
	for name, tc := range map[string]struct {
		era eras.EraDesc
		pp  lcommon.ProtocolParameters
	}{
		"shelley": {eras.ShelleyEraDesc, shelleyPP(2)},
		"allegra": {eras.AllegraEraDesc, shelleyPP(3)},
		"mary":    {eras.MaryEraDesc, shelleyPP(4)},
		"alonzo":  {eras.AlonzoEraDesc, alonzoPP},
		"babbage": {eras.BabbageEraDesc, babbagePP},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			retire := uint64(11)
			era := tc.era
			got, err := dxoracle.NewEpoch(context.Background(), dxoracle.NewEpochInput{
				Era: &era, Epoch: 10, PParams: tc.pp, Reserves: 10_000_000_000, Treasury: 1_000_000,
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
			require.Equal(t, [][]byte{key(0xa0)}, got.PoolsRemoved)
			require.Equal(t, uint64(500), got.Accounts[0].Reward)
			require.Empty(t, got.Accounts[1].Pool)
			require.Equal(t, []dxoracle.SnapshotStake{snapRow(2, 45, 0xa0)}, got.Mark)
			require.Equal(t, []dxoracle.SnapshotStake{snapRow(3, 100, 0xa0)}, got.Set)
		})
	}
}

func preConwayBoundary(in dxoracle.NewEpochInput) dxoracle.NewEpochInput {
	era := eras.BabbageEraDesc
	pp := &babbage.BabbageProtocolParameters{}
	pp.ProtocolMajor = 8
	pp.MinFeeA, pp.MaxBlockBodySize, pp.MaxTxSize, pp.MaxBlockHeaderSize = 44, 65_536, 16_384, 1_100
	in.Era, in.PParams, in.Epoch = &era, pp, 10
	in.Reserves, in.Treasury = 10_000_000_000, 1_000_000
	return in
}

// Two of the three required genesis keys vote the same update for the
// ended epoch, so it is adopted at the boundary: min_fee_a (key 0) goes from
// 44 to 100. A quorum of 3 would not be met.
func TestNewEpochAdoptsParameterUpdate(t *testing.T) {
	t.Parallel()
	update := []byte{0xa1, 0x00, 0x18, 0x64}
	prop := func(k byte) dxoracle.NewEpochPParamUpdate {
		return dxoracle.NewEpochPParamUpdate{GenesisHash: key(k), Cbor: update, AddedSlot: 10*432_000 + 10, Epoch: 10}
	}
	for quorum, want := range map[int]uint64{2: 100, 3: 44} {
		got, err := dxoracle.NewEpoch(context.Background(), preConwayBoundary(dxoracle.NewEpochInput{
			Accounts:      []dxoracle.NewEpochAccount{{Key: key(1)}},
			UpdateQuorum:  quorum,
			PParamUpdates: []dxoracle.NewEpochPParamUpdate{prop(1), prop(2)},
		}))
		require.NoError(t, err)
		require.Equal(t, want, uint64(got.PParams.(*babbage.BabbageProtocolParameters).MinFeeA), "quorum %d", quorum)
	}
}

// A pending MIR credit of 150 from reserves to a registered account is paid at
// the boundary; one to an unregistered account is dropped with the rest.
func TestNewEpochAppliesMIR(t *testing.T) {
	t.Parallel()
	got, err := dxoracle.NewEpoch(context.Background(), preConwayBoundary(dxoracle.NewEpochInput{
		Accounts: []dxoracle.NewEpochAccount{{Key: key(1)}},
		MIRs:     []dxoracle.NewEpochMIR{{Key: key(1), Amount: 150}},
	}))
	require.NoError(t, err)
	require.Equal(t, uint64(150), got.Accounts[0].Reward)
	require.Equal(t, uint64(10_000_000_000-150), got.Reserves)
	require.Equal(t, uint64(1_000_000), got.Treasury)
}
