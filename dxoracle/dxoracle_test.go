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
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/dxoracle"
	"github.com/stretchr/testify/require"
)

var shared *dxoracle.Oracle

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "dxoracle-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)
	shared, err = dxoracle.Open(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	code := m.Run()
	if err := shared.Close(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return code
}

func key(b byte) []byte {
	return slices.Repeat([]byte{b}, 28)
}

func stakeOf(
	t *testing.T,
	dist []models.DRepStakeDistributionEntry,
	drepType uint8,
	credential []byte,
) uint64 {
	t.Helper()
	for _, e := range dist {
		if e.DRepType == drepType && string(e.Credential) == string(credential) {
			return e.Stake
		}
	}
	return 0
}

func keyDRep(k byte) dxoracle.DRep {
	return dxoracle.DRep{Key: key(k), Active: true, ExpiryEpoch: 100}
}

func delegated(
	acct, drep byte, reward uint64,
) dxoracle.Account {
	return dxoracle.Account{
		Key: key(acct), Reward: reward, Active: true,
		Delegation: dxoracle.Delegation{
			Type: dxoracle.DelegateKeyHash, Key: key(drep),
		},
	}
}

func TestDRepStakeSumsUtxoAndReward(t *testing.T) {
	t.Parallel()
	in := dxoracle.DRepStakeDistributionInput{
		Dreps: []dxoracle.DRep{keyDRep(0xd1)},
		Accounts: []dxoracle.Account{
			delegated(0xa1, 0xd1, 5),
			delegated(0xa2, 0xd1, 7),
		},
		Utxos: []dxoracle.Utxo{
			{Key: key(0xa1), Amount: 100},
			{Key: key(0xa1), Amount: 200},
			{Key: key(0xa2), Amount: 1000},
			{Key: key(0xa9), Amount: 77777}, // no account
		},
	}
	dist, err := shared.DRepStakeDistribution(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, uint64(100+200+5+1000+7),
		stakeOf(t, dist, uint8(models.DrepTypeAddrKeyHash), key(0xd1)))
}

func TestDRepStakePredefined(t *testing.T) {
	t.Parallel()
	in := dxoracle.DRepStakeDistributionInput{
		Accounts: []dxoracle.Account{
			{Key: key(0xa1), Reward: 1, Active: true, Delegation: dxoracle.Delegation{Type: dxoracle.DelegateAlwaysAbstain}},
			{Key: key(0xa2), Reward: 2, Active: true, Delegation: dxoracle.Delegation{Type: dxoracle.DelegateAlwaysNoConfidence}},
		},
		Utxos: []dxoracle.Utxo{
			{Key: key(0xa1), Amount: 10},
			{Key: key(0xa2), Amount: 20},
		},
	}
	dist, err := shared.DRepStakeDistribution(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, uint64(11),
		stakeOf(t, dist, uint8(models.DrepTypeAlwaysAbstain), nil))
	require.Equal(t, uint64(22),
		stakeOf(t, dist, uint8(models.DrepTypeAlwaysNoConfidence), nil))
}

func TestDRepStakeExcludesInactiveAccountAndDeletedUtxo(t *testing.T) {
	t.Parallel()
	inactive := delegated(0xa2, 0xd1, 50)
	inactive.Active = false
	in := dxoracle.DRepStakeDistributionInput{
		Dreps:    []dxoracle.DRep{keyDRep(0xd1)},
		Accounts: []dxoracle.Account{delegated(0xa1, 0xd1, 3), inactive},
		Utxos: []dxoracle.Utxo{
			{Key: key(0xa1), Amount: 100},
			{Key: key(0xa1), Amount: 900, Deleted: true},
			{Key: key(0xa2), Amount: 400},
		},
	}
	dist, err := shared.DRepStakeDistribution(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, uint64(103),
		stakeOf(t, dist, uint8(models.DrepTypeAddrKeyHash), key(0xd1)))
}

func TestDRepStakeAddsProposalDepositViaReturnAccount(t *testing.T) {
	t.Parallel()
	in := dxoracle.DRepStakeDistributionInput{
		Dreps:               []dxoracle.DRep{keyDRep(0xd1)},
		Accounts:            []dxoracle.Account{delegated(0xa1, 0xd1, 4)},
		Utxos:               []dxoracle.Utxo{{Key: key(0xa1), Amount: 10}},
		ActiveProposalEpoch: 5,
		Proposals: []dxoracle.Proposal{
			{Deposit: 500, ReturnAccount: dxoracle.Credential{Key: key(0xa1)}, ProposedEpoch: 3, ExpiresEpoch: 9},
			{Deposit: 9999, ReturnAccount: dxoracle.Credential{Key: key(0xa1)}, ProposedEpoch: 1, ExpiresEpoch: 4}, // expired
		},
	}
	dist, err := shared.DRepStakeDistribution(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, uint64(4+10+500),
		stakeOf(t, dist, uint8(models.DrepTypeAddrKeyHash), key(0xd1)))
}

func TestCallsAreIsolated(t *testing.T) {
	t.Parallel()
	// Distinct keys would hide leakage into a sum, so both calls reuse the
	// same credentials and the second must see only its own amounts.
	mk := func(amount uint64) dxoracle.DRepStakeDistributionInput {
		return dxoracle.DRepStakeDistributionInput{
			Dreps:    []dxoracle.DRep{keyDRep(0xe1)},
			Accounts: []dxoracle.Account{delegated(0xe2, 0xe1, 0)},
			Utxos:    []dxoracle.Utxo{{Key: key(0xe2), Amount: amount}},
		}
	}
	d1, err := shared.DRepStakeDistribution(context.Background(), mk(111))
	require.NoError(t, err)
	require.Equal(t, uint64(111),
		stakeOf(t, d1, uint8(models.DrepTypeAddrKeyHash), key(0xe1)))
	d2, err := shared.DRepStakeDistribution(context.Background(), mk(7))
	require.NoError(t, err)
	require.Equal(t, uint64(7),
		stakeOf(t, d2, uint8(models.DrepTypeAddrKeyHash), key(0xe1)))
	// A call that seeds nothing sees nothing.
	d3, err := shared.DRepStakeDistribution(context.Background(),
		dxoracle.DRepStakeDistributionInput{})
	require.NoError(t, err)
	require.Empty(t, d3)
}

func BenchmarkDRepStakeDistribution(b *testing.B) {
	in := dxoracle.DRepStakeDistributionInput{
		Dreps:    []dxoracle.DRep{keyDRep(0xd1)},
		Accounts: []dxoracle.Account{delegated(0xa1, 0xd1, 4), delegated(0xa2, 0xd1, 5)},
		Utxos:    []dxoracle.Utxo{{Key: key(0xa1), Amount: 10}, {Key: key(0xa2), Amount: 20}},
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := shared.DRepStakeDistribution(context.Background(), in); err != nil {
			b.Fatal(err)
		}
	}
}
