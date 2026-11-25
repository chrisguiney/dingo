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

package eras

import (
	"fmt"
	"math/big"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// MinFeeTx returns the minimum fee for tx: the size-based fee, the price of
// its declared execution units, and the tiered reference-script fee for
// refScriptSize bytes of reference scripts. A zero refScriptSize adds nothing,
// which is the pre-Conway case. The result saturates at the largest uint64.
func MinFeeTx(
	tx lcommon.Transaction,
	minFeeA uint,
	minFeeB uint,
	pricesMem *big.Rat,
	pricesSteps *big.Rat,
	refScriptSize uint64,
	refScriptCostPerByte *big.Rat,
) (uint64, error) {
	declaredEU, err := DeclaredExUnits(tx)
	if err != nil {
		return 0, fmt.Errorf(
			"calculating declared execution units: %w",
			err,
		)
	}
	return CalculateConwayMinFee(
		TxSizeForFee(tx),
		declaredEU,
		minFeeA,
		minFeeB,
		pricesMem,
		pricesSteps,
		refScriptSize,
		refScriptCostPerByte,
	), nil
}
