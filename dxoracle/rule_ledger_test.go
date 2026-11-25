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
	"fmt"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/require"
)

// ledgerTestTx is a Conway transaction spending one input to one output with
// the given fee, with no witnesses.
func ledgerTestTx(t *testing.T, fee uint64) (lcommon.Transaction, lcommon.TransactionOutput) {
	t.Helper()
	addr := append([]byte{0x61}, bytes.Repeat([]byte{0x77}, 28)...)
	body := map[uint]any{
		0: []any{[]any{bytes.Repeat([]byte{9}, 32), uint(0)}},
		1: []any{[]any{addr, uint(5_000_000)}},
		2: fee,
	}
	raw, err := cbor.Encode([]any{body, map[uint]any{}, true, nil})
	require.NoError(t, err)
	tx, err := ledger.NewTransactionFromCbor(ledger.TxTypeConway, raw)
	require.NoError(t, err)
	outRaw, err := cbor.Encode([]any{addr, uint(5_000_000 + fee)})
	require.NoError(t, err)
	out, err := ledger.NewTransactionOutputFromCbor(outRaw)
	require.NoError(t, err)
	return tx, out
}

// A spend whose input is in the seeded UTxO is accepted, and the run reports
// the input as removed and the transaction's output as added.
func TestRunLedgerAppliesASpend(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	tx, out := ledgerTestTx(t, 200_000)
	pp := certTestParams()
	in := tx.Inputs()[0]
	res, err := o.RunLedger(context.Background(), LedgerRunInput{
		Slot: 100, Epoch: 5, Params: pp, Tx: tx,
		State: LedgerTables{Utxos: []LedgerUtxo{{TxHash: in.Id().Bytes(), Index: in.Index(), Output: out}}},
	})
	require.NoError(t, err)
	require.Empty(t, res.Failures)
	require.Equal(t, []string{fmt.Sprintf("%x#%d", in.Id().Bytes(), in.Index())}, res.UtxoRemoved)
	require.Len(t, res.UtxoAdded, 1)
	require.Equal(t, tx.Hash().Bytes(), res.UtxoAdded[0].TxHash)
}

// With the minimum fee in force a transaction with no fee is refused with the
// fee rule's failure, which the rule list alone does not report.
func TestRunLedgerReportsTheMinimumFee(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	tx, out := ledgerTestTx(t, 1000)
	pp := certTestParams()
	pp.MinFeeA, pp.MinFeeB = 44, 155381
	in := tx.Inputs()[0]
	res, err := o.RunLedger(context.Background(), LedgerRunInput{
		Slot: 100, Epoch: 5, Params: pp, Tx: tx,
		Families: []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyUtxo},
		State:    LedgerTables{Utxos: []LedgerUtxo{{TxHash: bytes.Clone(in.Id().Bytes()), Index: in.Index(), Output: out}}},
	})
	require.NoError(t, err)
	var fee bool
	for _, f := range res.Failures {
		fee = fee || f.RuleId == lcommon.UtxoValidationRuleFeeTooSmall
	}
	require.True(t, fee, "failures: %v", res.Failures)
}
