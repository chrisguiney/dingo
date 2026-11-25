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
	"fmt"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// ParseMempackTxOut decodes one MemPack-encoded Babbage or Conway TxOut, the
// value format of UTxO-HD tables, into the row the importer builds for it.
// The row has no transaction hash or output index.
func ParseMempackTxOut(data []byte) (*ParsedUTxO, error) {
	return parseMempackTxOut(nil, 0, data)
}

// DecodePParams decodes the current protocol parameters of an era from the
// GovState (or, before Conway, the PParams) encoding and validates their
// domains, as the importer does.
func DecodePParams(
	eraIndex int,
	data []byte,
) (lcommon.ProtocolParameters, error) {
	params, err := decodePParamsData(eraIndex, data)
	if err != nil {
		return nil, fmt.Errorf("protocol parameters: %w", err)
	}
	return params, nil
}
