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
	"testing"

	"github.com/blinklabs-io/dingo/ledger/eras"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"
	"github.com/stretchr/testify/require"
)

func certTestParams() *conway.ConwayProtocolParameters {
	pp := &conway.ConwayProtocolParameters{}
	pp.ProtocolVersion.Major = 10
	pp.KeyDeposit = 2_000_000
	pp.PoolDeposit = 500_000_000
	pp.DRepDeposit = 500_000_000
	pp.MaxEpoch = 18
	pp.MinPoolCost = 170_000_000
	pp.MaxTxSize = 16384
	pp.GovActionValidityPeriod = 6
	pp.DRepInactivityPeriod = 20
	return pp
}

func certTestCred(b byte) lcommon.Credential {
	return lcommon.Credential{
		CredType:   lcommon.CredentialTypeAddrKeyHash,
		Credential: lcommon.NewBlake2b224(bytes.Repeat([]byte{b}, 28)),
	}
}

func certTestTx(t *testing.T, certs ...lcommon.Certificate) lcommon.Transaction {
	t.Helper()
	out, err := mockledger.NewTransactionOutputBuilder().
		WithAddress("addr1qytna5k2fq9ler0fuk45j7zfwv7t2zwhp777nvdjqqfr5tz8ztpwnk8zq5ngetcz5k5mckgkajnygtsra9aej2h3ek5seupmvd").
		WithLovelace(1_000_000).Build()
	require.NoError(t, err)
	in, err := mockledger.NewSimpleTransactionInput(bytes.Repeat([]byte{9}, 32), 0)
	require.NoError(t, err)
	builder := mockledger.NewTransactionBuilder()
	builder.WithId(bytes.Repeat([]byte{7}, 32))
	builder.WithType(int(conway.EraIdConway))
	builder.WithValid(true)
	builder.WithInputs(in)
	builder.WithOutputs(out)
	builder.WithCertificates(certs...)
	tx, err := builder.Build()
	require.NoError(t, err)
	return tx
}

func TestCertOracleRegistersStakeCredential(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	cred := certTestCred(1)
	cert := &lcommon.RegistrationCertificate{
		CertType: uint(lcommon.CertificateTypeRegistration), StakeCredential: cred, Amount: 2_000_000,
	}
	res, err := o.Run(context.Background(), CertRunInput{
		Slot: 100, Epoch: 5, Params: certTestParams(), Tx: certTestTx(t, cert),
		Certs:    []lcommon.Certificate{cert},
		Families: []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyDeleg},
	})
	require.NoError(t, err)
	require.Empty(t, res.Failures)
	require.Len(t, res.After.Accounts, 1)
	require.Equal(t, uint64(2_000_000), res.After.Accounts[0].Deposit)
	require.Equal(t, uint64(0), res.After.Accounts[0].Balance)
}

func TestCertOracleRejectsRegisteredCredential(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	cred := certTestCred(1)
	cert := &lcommon.RegistrationCertificate{
		CertType: uint(lcommon.CertificateTypeRegistration), StakeCredential: cred, Amount: 2_000_000,
	}
	res, err := o.Run(context.Background(), CertRunInput{
		Slot: 100, Epoch: 5, Params: certTestParams(), Tx: certTestTx(t, cert),
		State:    CertTables{Accounts: []CertAccount{{Credential: cred, Deposit: 2_000_000}}},
		Certs:    []lcommon.Certificate{cert},
		Families: []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyDeleg},
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Failures)
	require.Equal(t, lcommon.UtxoFamilyDeleg, res.Failures[0].Family)
	require.ErrorAs(t, res.Failures[0].Err, new(conway.StakeCredentialAlreadyRegisteredError))
}

func legacyTestParams() *shelley.ShelleyProtocolParameters {
	return &shelley.ShelleyProtocolParameters{
		KeyDeposit: 2_000_000, PoolDeposit: 500_000_000, MaxEpoch: 18, MinPoolCost: 170_000_000, MaxTxSize: 16384,
		ProtocolMajor: 2,
	}
}

func legacyTestTx(t *testing.T, certs ...lcommon.Certificate) lcommon.Transaction {
	t.Helper()
	out, err := mockledger.NewTransactionOutputBuilder().
		WithAddress("addr1qytna5k2fq9ler0fuk45j7zfwv7t2zwhp777nvdjqqfr5tz8ztpwnk8zq5ngetcz5k5mckgkajnygtsra9aej2h3ek5seupmvd").
		WithLovelace(1_000_000).Build()
	require.NoError(t, err)
	in, err := mockledger.NewSimpleTransactionInput(bytes.Repeat([]byte{9}, 32), 0)
	require.NoError(t, err)
	builder := mockledger.NewTransactionBuilder()
	builder.WithId(bytes.Repeat([]byte{7}, 32))
	builder.WithType(int(shelley.EraIdShelley))
	builder.WithValid(true)
	builder.WithInputs(in)
	builder.WithOutputs(out)
	builder.WithCertificates(certs...)
	tx, err := builder.Build()
	require.NoError(t, err)
	return tx
}

// A Shelley run validates through the Shelley rule list, not Conway's: the
// delegation rule refuses a deregistration of a credential nobody registered
// with the Shelley error, and a registration is applied to the database.
func TestCertOracleRunsTheShelleyRuleList(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	cred := certTestCred(1)
	families := []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyDeleg}
	rules := func(tx lcommon.Transaction, slot uint64, ls lcommon.LedgerState, pp lcommon.ProtocolParameters, f ...lcommon.UtxoValidationFamily) []lcommon.UtxoValidationFailure {
		out, ok := eras.LegacyRuleFailures(shelley.EraIdShelley, tx, slot, ls, pp, f...)
		require.True(t, ok)
		return out
	}

	dereg := &lcommon.StakeDeregistrationCertificate{
		CertType: uint(lcommon.CertificateTypeStakeDeregistration), StakeCredential: cred,
	}
	res, err := o.Run(context.Background(), CertRunInput{
		Slot: 100, Epoch: 5, Params: legacyTestParams(), Tx: legacyTestTx(t, dereg),
		Certs: []lcommon.Certificate{dereg}, Families: families, RuleFailures: rules,
	})
	require.NoError(t, err)
	require.Len(t, res.Failures, 1)
	require.ErrorAs(t, res.Failures[0].Err, new(shelley.StakeKeyNotRegisteredError))

	reg := &lcommon.StakeRegistrationCertificate{
		CertType: uint(lcommon.CertificateTypeStakeRegistration), StakeCredential: cred,
	}
	res, err = o.Run(context.Background(), CertRunInput{
		Slot: 100, Epoch: 5, Params: legacyTestParams(), Tx: legacyTestTx(t, reg),
		Certs: []lcommon.Certificate{reg}, Families: families, RuleFailures: rules,
	})
	require.NoError(t, err)
	require.Empty(t, res.Failures)
	require.Len(t, res.After.Accounts, 1)
	require.Equal(t, uint64(2_000_000), res.After.Accounts[0].Deposit)
}
