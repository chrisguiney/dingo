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

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/stretchr/testify/require"
)

// shelleySpendTx is a Shelley transaction spending one input to one output with
// the given fee and no witnesses, and the output it spends.
func shelleySpendTx(t *testing.T, fee uint64) (lcommon.Transaction, lcommon.TransactionOutput) {
	t.Helper()
	addr := append([]byte{0x61}, bytes.Repeat([]byte{0x77}, 28)...)
	body := map[uint]any{
		0: []any{[]any{bytes.Repeat([]byte{9}, 32), uint(0)}},
		1: []any{[]any{addr, uint(5_000_000)}},
		2: fee,
		3: uint(1) << 40,
	}
	raw, err := cbor.Encode([]any{body, map[uint]any{}, nil})
	require.NoError(t, err)
	tx, err := ledger.NewTransactionFromCbor(ledger.TxTypeShelley, raw)
	require.NoError(t, err)
	outRaw, err := cbor.Encode([]any{addr, uint(5_000_000 + fee)})
	require.NoError(t, err)
	out, err := ledger.NewTransactionOutputFromCbor(outRaw)
	require.NoError(t, err)
	return tx, out
}

func shelleySpendParams() *shelley.ShelleyProtocolParameters {
	return &shelley.ShelleyProtocolParameters{MinFeeA: 44, MinFeeB: 155381, MaxTxSize: 16384, ProtocolMajor: 2}
}

func failedRuleIds(res *LedgerRunResult) map[lcommon.UtxoValidationRuleId]bool {
	out := map[lcommon.UtxoValidationRuleId]bool{}
	for _, f := range res.Failures {
		out[f.RuleId] = true
	}
	return out
}

// Shelley's production validator runs the maximum-size and minimum-fee checks
// beside its rule list, so the list alone reports neither: a legacy run must
// add them.
func TestRunLedgerLegacyReportsShelleySizeAndFee(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	tx, out := shelleySpendTx(t, 1000)
	in := tx.Inputs()[0]
	pp := shelleySpendParams()
	pp.MaxTxSize = 10
	res, err := o.RunLedger(context.Background(), LedgerRunInput{
		Slot: 100, Epoch: 5, NetworkID: 1, Params: pp, Tx: tx, LegacyEraID: shelley.EraIdShelley,
		Families: []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyUtxo},
		State:    LedgerTables{Utxos: []LedgerUtxo{{TxHash: bytes.Clone(in.Id().Bytes()), Index: in.Index(), Output: out}}},
	})
	require.NoError(t, err)
	rules := failedRuleIds(res)
	require.True(t, rules[lcommon.UtxoValidationRuleFeeTooSmall], "failures: %v", res.Failures)
	require.True(t, rules[lcommon.UtxoValidationRuleMaxTxSize], "failures: %v", res.Failures)
}

// A Shelley transaction that pays its fee and fits is accepted and applied
// through the production writer.
func TestRunLedgerLegacyAppliesAShelleySpend(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	tx, out := shelleySpendTx(t, 200_000)
	in := tx.Inputs()[0]
	res, err := o.RunLedger(context.Background(), LedgerRunInput{
		Slot: 100, Epoch: 5, NetworkID: 1, Params: shelleySpendParams(), Tx: tx, LegacyEraID: shelley.EraIdShelley,
		Families: []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyUtxo},
		State:    LedgerTables{Utxos: []LedgerUtxo{{TxHash: bytes.Clone(in.Id().Bytes()), Index: in.Index(), Output: out}}},
	})
	require.NoError(t, err)
	require.Empty(t, res.Failures)
	require.Len(t, res.UtxoAdded, 1)
	require.Len(t, res.UtxoRemoved, 1)
}
