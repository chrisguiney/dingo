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
	"fmt"

	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/gouroboros/ledger/allegra"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
)

// legacyRuleFailures is the rule list of a Shelley-family era as a
// CertRunInput.RuleFailures, or nil for Conway (eraID 0).
func legacyRuleFailures(eraID uint) func(
	lcommon.Transaction, uint64, lcommon.LedgerState, lcommon.ProtocolParameters, ...lcommon.UtxoValidationFamily,
) []lcommon.UtxoValidationFailure {
	if eraID == 0 {
		return nil
	}
	return func(
		tx lcommon.Transaction, slot uint64, ls lcommon.LedgerState,
		pp lcommon.ProtocolParameters, families ...lcommon.UtxoValidationFamily,
	) []lcommon.UtxoValidationFailure {
		out, _ := eras.LegacyRuleFailures(eraID, tx, slot, ls, pp, families...)
		return out
	}
}

// legacySizeFeeFailures runs the maximum-size and minimum-fee checks that the
// production validator of Shelley and Allegra performs beside its rule list;
// the later eras carry both inside the list.
func legacySizeFeeFailures(eraID uint, tx lcommon.Transaction, params lcommon.ProtocolParameters) ([]lcommon.UtxoValidationFailure, error) {
	switch eraID {
	case shelley.EraIdShelley, allegra.EraIdAllegra:
		p, ok := params.(*shelley.ShelleyProtocolParameters)
		if !ok || p == nil {
			return nil, fmt.Errorf("dxoracle: era %d wants Shelley parameters, got %T", eraID, params)
		}
		var out []lcommon.UtxoValidationFailure
		if err := eras.ValidateTxSize(tx, p.MaxTxSize); err != nil {
			out = append(out, lcommon.UtxoValidationFailure{RuleId: lcommon.UtxoValidationRuleMaxTxSize, Family: lcommon.UtxoFamilyUtxo, Err: err})
		}
		if err := eras.ValidateTxFee(tx, p.MinFeeA, p.MinFeeB, nil, nil); err != nil {
			out = append(out, lcommon.UtxoValidationFailure{RuleId: lcommon.UtxoValidationRuleFeeTooSmall, Family: lcommon.UtxoFamilyUtxo, Err: err})
		}
		return out, nil
	}
	return nil, nil
}
