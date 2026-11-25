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
	"errors"
	"fmt"
	"math"
	"math/big"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// AvvmOutput is one live output considered by the Allegra HARDFORK rule that
// returns Byron redeem (AVVM) outputs to reserves.
type AvvmOutput struct {
	Address lcommon.Address
	Amount  *big.Int
}

// ClassifyAvvmOutput reports whether an output sits at a Byron redeem address
// and so is removed by the Allegra HARDFORK rule, and the lovelace it carries.
// An output elsewhere is ignored whatever its amount. A redeem output whose
// amount is missing, negative or beyond uint64 is an error.
func ClassifyAvvmOutput(
	addr lcommon.Address,
	amount *big.Int,
) (isAvvm bool, lovelace uint64, err error) {
	if addr.Type() != lcommon.AddressTypeByron ||
		addr.ByronType() != lcommon.ByronAddressTypeRedeem {
		return false, 0, nil
	}
	if amount == nil || amount.Sign() < 0 || !amount.IsUint64() {
		return false, 0, fmt.Errorf("invalid amount %v", amount)
	}
	return true, amount.Uint64(), nil
}

// addAvvmLovelace adds one AVVM output's lovelace to a running total.
func addAvvmLovelace(total, lovelace uint64) (uint64, error) {
	if total > math.MaxUint64-lovelace {
		return 0, errors.New("AVVM lovelace total overflows uint64")
	}
	return total + lovelace, nil
}

// creditAvvmReserves returns reserves after the AVVM total is returned to
// them.
func creditAvvmReserves(reserves, total uint64) (uint64, error) {
	if total > math.MaxUint64-reserves {
		return 0, fmt.Errorf(
			"AVVM reserve credit overflows uint64: reserves %d, credit %d",
			reserves,
			total,
		)
	}
	return reserves + total, nil
}

// ComputeAvvmReturn computes the Allegra HARDFORK rule over the live outputs:
// the indices of the outputs removed, their lovelace total, and the reserves
// after the total is returned to them. Reserves change only when at least one
// output is removed.
func ComputeAvvmReturn(
	outputs []AvvmOutput,
	reserves uint64,
) (removed []int, total, newReserves uint64, err error) {
	for i, out := range outputs {
		isAvvm, lovelace, err := ClassifyAvvmOutput(out.Address, out.Amount)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("AVVM output %d has %w", i, err)
		}
		if !isAvvm {
			continue
		}
		removed = append(removed, i)
		if total, err = addAvvmLovelace(total, lovelace); err != nil {
			return nil, 0, 0, fmt.Errorf("%w at output %d", err, i)
		}
	}
	newReserves = reserves
	if len(removed) > 0 {
		if newReserves, err = creditAvvmReserves(reserves, total); err != nil {
			return nil, 0, 0, err
		}
	}
	return removed, total, newReserves, nil
}
