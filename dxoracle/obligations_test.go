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

package dxoracle

import (
	"bytes"
	"context"
	"math"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/stretchr/testify/require"
)

func TestComputeObligationsSumsEveryDepositKind(t *testing.T) {
	t.Parallel()
	got, err := ComputeObligations(
		[]CertAccount{{Deposit: 2_000_000}, {Deposit: 2_000_000}},
		[]CertPool{{Deposit: 500_000_000}},
		[]CertDRep{{Deposit: 500_000_000}, {Deposit: 500_000_000}},
		[]*models.GovernanceProposal{{Deposit: 100_000_000_000}},
	)
	require.NoError(t, err)
	require.Equal(t, Obligations{
		Stake: 4_000_000, Pool: 500_000_000, DRep: 1_000_000_000,
		Proposal: 100_000_000_000, Total: 101_504_000_000,
	}, got)
}

func TestComputeObligationsSkipsFuturePoolParameters(t *testing.T) {
	t.Parallel()
	got, err := ComputeObligations(nil,
		[]CertPool{{Deposit: 500_000_000}, {Deposit: 500_000_000, Future: true}}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(500_000_000), got.Pool)
	require.Equal(t, uint64(500_000_000), got.Total)
}

func TestComputeObligationsRejectsOverflow(t *testing.T) {
	t.Parallel()
	_, err := ComputeObligations(
		[]CertAccount{{Deposit: math.MaxUint64}, {Deposit: 1}}, nil, nil, nil)
	require.Error(t, err)
	_, err = ComputeObligations(
		[]CertAccount{{Deposit: math.MaxUint64}}, nil, []CertDRep{{Deposit: 1}}, nil)
	require.Error(t, err, "the total overflows although no kind does")
}

func TestStoredObligationsReadsSeededDeposits(t *testing.T) {
	t.Parallel()
	certs, err := OpenCertOracle()
	require.NoError(t, err)
	defer certs.Close()
	props, err := Open(t.TempDir())
	require.NoError(t, err)
	defer props.Close()
	got, err := StoredObligations(context.Background(), certs, props, ObligationsInput{
		// A deposit that differs from the parameter's KeyDeposit shows the
		// stored amount is read, not the current parameter.
		Accounts: []CertAccount{
			{Credential: certTestCred(1), Deposit: 2_000_000},
			{Credential: certTestCred(2), Deposit: 1_500_000},
		},
		Pools: []CertPool{{
			PoolKeyHash: bytes.Repeat([]byte{3}, 28), VrfKeyHash: bytes.Repeat([]byte{4}, 32),
			Deposit: 500_000_000, Margin: big.NewRat(1, 100), RewardAccount: certTestCred(5),
		}},
		DReps: []CertDRep{{Credential: certTestCred(6), Deposit: 400_000_000}},
		Proposals: []Proposal{
			{Deposit: 100_000_000_000, ReturnAccount: Credential{Key: bytes.Repeat([]byte{7}, 28)}, ProposedEpoch: 4, ExpiresEpoch: 10},
		},
	})
	require.NoError(t, err)
	require.Equal(t, Obligations{
		Stake: 3_500_000, Pool: 500_000_000, DRep: 400_000_000,
		Proposal: 100_000_000_000, Total: 100_903_500_000,
	}, got)
}
