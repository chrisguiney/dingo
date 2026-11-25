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

package ledgerstate

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMempackTxOutDatumHash(t *testing.T) {
	t.Parallel()
	data, err := hex.DecodeString(previewCompactDatumHashHex)
	require.NoError(t, err)
	row, err := ParseMempackTxOut(data)
	require.NoError(t, err)
	require.Len(t, row.DatumHash, 32)
	require.NotEmpty(t, row.Address)
	require.NotEmpty(t, row.Cbor)
}

func TestParseMempackTxOutRejectsTrailingBytes(t *testing.T) {
	t.Parallel()
	data, err := hex.DecodeString(previewCompactDatumHashHex)
	require.NoError(t, err)
	_, err = ParseMempackTxOut(append(data, 0x00))
	require.Error(t, err)
}

func TestDecodePParamsRejectsUnknownEra(t *testing.T) {
	t.Parallel()
	_, err := DecodePParams(99, []byte{0x80})
	require.Error(t, err)
}
