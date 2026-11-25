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
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/require"
)

// blockTx is a Conway transaction spending input to one output, with no
// witnesses, and its body as the block body carries it.
func blockTx(t *testing.T, input []byte, coin, fee uint64) (lcommon.Transaction, cbor.RawMessage) {
	t.Helper()
	addr := append([]byte{0x61}, bytes.Repeat([]byte{0x77}, 28)...)
	body, err := cbor.Encode(map[uint]any{
		0: []any{[]any{input, uint(0)}},
		1: []any{[]any{addr, coin}},
		2: fee,
	})
	require.NoError(t, err)
	raw, err := cbor.Encode([]any{cbor.RawMessage(body), map[uint]any{}, true, nil})
	require.NoError(t, err)
	tx, err := ledger.NewTransactionFromCbor(ledger.TxTypeConway, raw)
	require.NoError(t, err)
	return tx, body
}

// blockOf wraps the transaction bodies in a Conway block whose header declares
// the body's real size and hash.
func blockOf(t *testing.T, bodies ...cbor.RawMessage) []byte {
	t.Helper()
	enc := func(v any) []byte {
		b, err := cbor.Encode(v)
		require.NoError(t, err)
		return b
	}
	wits := make([]any, len(bodies))
	for i := range wits {
		wits[i] = map[uint]any{}
	}
	parts := [][]byte{enc(bodies), enc(wits), enc(map[uint]any{}), enc([]uint{})}
	var hashes []byte
	var size uint64
	for _, p := range parts {
		h := lcommon.Blake2b256Hash(p)
		hashes = append(hashes, h.Bytes()...)
		size += uint64(len(p))
	}
	return assembleBlock(t, size, lcommon.Blake2b256Hash(hashes), parts)
}

func assembleBlock(t *testing.T, size uint64, hash lcommon.Blake2b256, parts [][]byte) []byte {
	t.Helper()
	header, err := cbor.Encode(&babbage.BabbageBlockHeader{
		Body: babbage.BabbageBlockHeaderBody{
			BlockNumber: 7, Slot: 100, BlockBodySize: size, BlockBodyHash: hash,
			VrfKey:       make([]byte, 32),
			VrfResult:    lcommon.VrfResult{Output: make([]byte, 64), Proof: make([]byte, 80)},
			OpCert:       babbage.BabbageOpCert{HotVkey: make([]byte, 32), Signature: make([]byte, 64)},
			ProtoVersion: babbage.BabbageProtoVersion{Major: 10},
		},
		Signature: make([]byte, 448),
	})
	require.NoError(t, err)
	raw := append([]byte{0x85}, header...)
	for _, p := range parts {
		raw = append(raw, p...)
	}
	return raw
}

func blockSpend(t *testing.T, o *CertOracle, mutate func(*BlockRunInput)) *BlockRunResult {
	t.Helper()
	first, firstBody := blockTx(t, bytes.Repeat([]byte{9}, 32), 5_000_000, 200_000)
	// The second transaction spends the first one's only output: it resolves
	// only if the first has been applied to the database before it is
	// validated.
	second, secondBody := blockTx(t, first.Hash().Bytes(), 4_800_000, 200_000)
	_ = second
	addr := append([]byte{0x61}, bytes.Repeat([]byte{0x77}, 28)...)
	outRaw, err := cbor.Encode([]any{addr, uint(5_200_000)})
	require.NoError(t, err)
	out, err := ledger.NewTransactionOutputFromCbor(outRaw)
	require.NoError(t, err)
	pp := certTestParams()
	pp.MaxValueSize = 5000
	in := BlockRunInput{
		Slot: 100, Epoch: 5, NetworkID: 1, Params: pp,
		State:     LedgerTables{Utxos: []LedgerUtxo{{TxHash: bytes.Repeat([]byte{9}, 32), Index: 0, Output: out}}},
		BlockCbor: blockOf(t, firstBody, secondBody),
		BlockType: ledger.BlockTypeConway,
		Families:  []lcommon.UtxoValidationFamily{},
	}
	if mutate != nil {
		mutate(&in)
	}
	res, err := o.RunBlock(context.Background(), in)
	require.NoError(t, err)
	return res
}

// The second transaction spends an output the first creates, so it is valid
// only against the state the first leaves; the block's net change is the one
// output consumed and the last one produced.
func TestRunBlockChainsTransactions(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	res := blockSpend(t, o, func(in *BlockRunInput) {
		in.Families = []lcommon.UtxoValidationFamily{lcommon.UtxoFamilyUtxo}
	})
	require.Empty(t, res.Failures, "%v", res.Failures)
	require.Equal(t, []string{fmt.Sprintf("%x#0", bytes.Repeat([]byte{9}, 32))}, res.UtxoRemoved)
	require.Len(t, res.UtxoAdded, 1)
}

// A body that disagrees with the header's declared size is a block failure of
// its own kind; the transactions still run.
func TestRunBlockReportsABodySizeMismatch(t *testing.T) {
	t.Parallel()
	o, err := OpenCertOracle()
	require.NoError(t, err)
	defer o.Close()
	res := blockSpend(t, o, func(in *BlockRunInput) {
		_, body := blockTx(t, bytes.Repeat([]byte{9}, 32), 5_000_000, 200_000)
		parts := [][]byte{mustEnc(t, []cbor.RawMessage{body}), mustEnc(t, []any{map[uint]any{}}), mustEnc(t, map[uint]any{}), mustEnc(t, []uint{})}
		var hashes []byte
		for _, p := range parts {
			h := lcommon.Blake2b256Hash(p)
			hashes = append(hashes, h.Bytes()...)
		}
		in.BlockCbor = assembleBlock(t, 1, lcommon.Blake2b256Hash(hashes), parts)
	})
	var kinds []BlockFailureKind
	for _, f := range res.BlockFailures {
		kinds = append(kinds, f.Kind)
	}
	require.Equal(t, []BlockFailureKind{BlockBodySize}, kinds)
}

func mustEnc(t *testing.T, v any) []byte {
	t.Helper()
	b, err := cbor.Encode(v)
	require.NoError(t, err)
	return b
}
