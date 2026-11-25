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

package epochengine_test

import (
	"math"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cred(b byte) epochengine.StakeCredential {
	h := make([]byte, 28)
	h[0] = b
	return epochengine.StakeCredential{Tag: 0, Hash: h}
}

func poolHash(b byte) []byte {
	h := make([]byte, 28)
	h[27] = b
	return h
}

func TestSplitPoolReapDeposit(t *testing.T) {
	t.Parallel()
	refund, unclaimed := epochengine.SplitPoolReapDeposit(500, true)
	assert.Equal(t, uint64(500), refund)
	assert.Equal(t, uint64(0), unclaimed)
	refund, unclaimed = epochengine.SplitPoolReapDeposit(500, false)
	assert.Equal(t, uint64(0), refund)
	assert.Equal(t, uint64(500), unclaimed)
}

func TestComputePoolReapRefunds(t *testing.T) {
	t.Parallel()
	pools := []epochengine.PoolReapPool{
		{PoolKeyHash: poolHash(1), RewardAccount: cred(1), Deposit: 500},
		{PoolKeyHash: poolHash(2), RewardAccount: cred(2), Deposit: 300},
		{PoolKeyHash: poolHash(3), RewardAccount: cred(1), Deposit: 200},
	}
	delegations := []epochengine.PoolReapDelegation{
		{Credential: cred(10), PoolKeyHash: poolHash(2)},
		{Credential: cred(11), PoolKeyHash: poolHash(9)},
		{Credential: cred(12), PoolKeyHash: poolHash(1)},
	}
	got, err := epochengine.ComputePoolReapRefunds(
		pools, delegations, []epochengine.StakeCredential{cred(1)},
	)
	require.NoError(t, err)
	// Pools 1 and 3 share a registered account: two separate refunds.
	require.Len(t, got.Refunds, 2)
	assert.Equal(t, poolHash(1), got.Refunds[0].PoolKeyHash)
	assert.Equal(t, uint64(500), got.Refunds[0].Amount)
	assert.Equal(t, cred(1), got.Refunds[0].Credential)
	assert.Equal(t, poolHash(3), got.Refunds[1].PoolKeyHash)
	assert.Equal(t, uint64(200), got.Refunds[1].Amount)
	// Pool 2's account is unregistered: its deposit goes to the treasury.
	assert.Equal(t, uint64(300), got.UnclaimedToTreasury)
	// Only delegations to retiring pools are cleared.
	assert.Equal(t,
		[]epochengine.StakeCredential{cred(10), cred(12)},
		got.Undelegated,
	)
}

func TestComputePoolReapRefundsKindDistinguishesAccount(t *testing.T) {
	t.Parallel()
	script := cred(1)
	script.Tag = 1
	got, err := epochengine.ComputePoolReapRefunds(
		[]epochengine.PoolReapPool{
			{PoolKeyHash: poolHash(1), RewardAccount: script, Deposit: 7},
		},
		nil,
		[]epochengine.StakeCredential{cred(1)},
	)
	require.NoError(t, err)
	assert.Empty(t, got.Refunds)
	assert.Equal(t, uint64(7), got.UnclaimedToTreasury)
}

func TestComputePoolReapRefundsUnclaimedOverflow(t *testing.T) {
	t.Parallel()
	_, err := epochengine.ComputePoolReapRefunds(
		[]epochengine.PoolReapPool{
			{PoolKeyHash: poolHash(1), RewardAccount: cred(1), Deposit: math.MaxUint64},
			{PoolKeyHash: poolHash(2), RewardAccount: cred(2), Deposit: 1},
		},
		nil,
		nil,
	)
	require.Error(t, err)
}

func TestComputeEpochDonations(t *testing.T) {
	t.Parallel()
	got, err := epochengine.ComputeEpochDonations(1000, 25)
	require.NoError(t, err)
	assert.Equal(t, uint64(1025), got)
	_, err = epochengine.ComputeEpochDonations(math.MaxUint64, 1)
	require.Error(t, err)
}

func mirReward(c epochengine.StakeCredential, amount int64) models.MIRReward {
	return models.MIRReward{
		Credential:    c.Hash,
		CredentialTag: c.Tag,
		Amount:        big.NewInt(amount),
	}
}

func registeredSet(cs ...epochengine.StakeCredential) map[string]bool {
	out := make(map[string]bool, len(cs))
	for _, c := range cs {
		out[models.NewStakeCredentialRef(c.Tag, c.Hash).MapKey()] = true
	}
	return out
}

func TestComputeMIRBoundaryEraFold(t *testing.T) {
	t.Parallel()
	effects := []models.MIREffect{
		{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(1), 100)}},
		{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(1), 40)}},
	}
	reg := registeredSet(cred(1))

	// Alonzo onward sums repeated entries.
	plan, err := epochengine.ComputeMIRBoundary(
		effects, reg, alonzo.EraIdAlonzo, 1000, 0,
	)
	require.NoError(t, err)
	require.Equal(t, epochengine.MIROutcomeApplied, plan.Outcome)
	require.Len(t, plan.Credits, 1)
	assert.Equal(t, uint64(140), plan.Credits[0].Amount)
	assert.Equal(t, uint64(860), plan.Reserves)

	// Before Alonzo the later certificate replaces the earlier one.
	plan, err = epochengine.ComputeMIRBoundary(
		effects, reg, alonzo.EraIdAlonzo-1, 1000, 0,
	)
	require.NoError(t, err)
	require.Len(t, plan.Credits, 1)
	assert.Equal(t, uint64(40), plan.Credits[0].Amount)
	assert.Equal(t, uint64(960), plan.Reserves)
}

func TestComputeMIRBoundaryNegativeNet(t *testing.T) {
	t.Parallel()
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{
			{Pot: epochengine.MIRPotTreasury, Rewards: []models.MIRReward{mirReward(cred(1), 10)}},
			{Pot: epochengine.MIRPotTreasury, Rewards: []models.MIRReward{mirReward(cred(1), -30)}},
		},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 5, 1000,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeDiscarded, plan.Outcome)
	assert.NotEmpty(t, plan.DiscardReason)
	assert.Equal(t, uint64(5), plan.Reserves)
	assert.Equal(t, uint64(1000), plan.Treasury)
}

func TestComputeMIRBoundaryOverBudgetAndTransfer(t *testing.T) {
	t.Parallel()
	dist := models.MIREffect{
		Pot:     epochengine.MIRPotReserves,
		Rewards: []models.MIRReward{mirReward(cred(1), 150)},
	}
	reg := registeredSet(cred(1))
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{dist}, reg, alonzo.EraIdAlonzo, 100, 0,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeOverBudget, plan.Outcome)
	assert.Empty(t, plan.Credits)
	assert.Equal(t, uint64(100), plan.Reserves)

	// A treasury-to-reserves transfer in the same boundary funds it.
	transfer := models.MIREffect{Pot: epochengine.MIRPotTreasury, OtherPot: 60}
	plan, err = epochengine.ComputeMIRBoundary(
		[]models.MIREffect{transfer, dist}, reg, alonzo.EraIdAlonzo, 100, 80,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeApplied, plan.Outcome)
	assert.Equal(t, uint64(10), plan.Reserves)
	assert.Equal(t, uint64(20), plan.Treasury)
}

func TestComputeMIRBoundarySkipsUnregisteredAndEmpty(t *testing.T) {
	t.Parallel()
	plan, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{
			{Pot: epochengine.MIRPotReserves, Rewards: []models.MIRReward{mirReward(cred(2), 10)}},
		},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 100, 100,
	)
	require.NoError(t, err)
	assert.Equal(t, epochengine.MIROutcomeEmpty, plan.Outcome)
	assert.Equal(t, uint64(100), plan.Reserves)
	assert.Equal(t, uint64(100), plan.Treasury)
}

func TestComputeMIRBoundaryNilAmount(t *testing.T) {
	t.Parallel()
	_, err := epochengine.ComputeMIRBoundary(
		[]models.MIREffect{{
			Pot: epochengine.MIRPotReserves,
			Rewards: []models.MIRReward{{
				Credential: cred(1).Hash, CredentialTag: 0,
			}},
		}},
		registeredSet(cred(1)), alonzo.EraIdAlonzo, 100, 100,
	)
	require.Error(t, err)
}
