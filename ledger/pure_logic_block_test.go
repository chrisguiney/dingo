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

package ledger

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/stretchr/testify/require"
)

// The header's declared body size must equal the serialized size of the three
// body fields; the maximum is a separate envelope check, so a body that is
// over the limit but matches its header passes here.
func TestValidateBlockBodySize(t *testing.T) {
	t.Parallel()
	header := mustEnvelopeCbor(t, "hdr")
	blockCbor := mustEnvelopeBlockCbor(t, header,
		[]byte{0x80}, []byte{0x80}, []byte{0xa0})
	mk := func(declared uint64) *envelopeTestBlock {
		return &envelopeTestBlock{
			header: &envelopeTestHeader{
				cbor: header, bodySize: declared, slot: 10, number: 42, era: shelley.EraShelley,
			},
			cbor: blockCbor,
		}
	}
	require.NoError(t, ValidateBlockBodySize(mk(3)))
	err := ValidateBlockBodySize(mk(2))
	require.Error(t, err)
	require.Contains(t, err.Error(), "block body size mismatch")
	require.Error(t, ValidateBlockBodySize(mk(4)))
}

// The aggregate execution-unit budget is the sum over the transactions; the
// exported total is that sum, whatever the limit.
func TestBlockDeclaredExUnitsOfAnEmptyBlock(t *testing.T) {
	t.Parallel()
	block := &envelopeTestBlock{header: &envelopeTestHeader{era: shelley.EraShelley}}
	total, err := BlockDeclaredExUnits(block)
	require.NoError(t, err)
	require.Zero(t, total.Memory)
	require.Zero(t, total.Steps)
}
