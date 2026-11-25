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

	"github.com/blinklabs-io/dingo/ledger/eras"
	gledger "github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// ValidateBlockBodySize rejects a block whose header declares a body size
// other than the serialized size of the body fields that follow the header.
func ValidateBlockBodySize(block gledger.Block) error {
	actualBodySize, err := serializedBlockBodySize(block)
	if err != nil {
		return err
	}
	declaredBodySize := block.BlockBodySize()
	if declaredBodySize != actualBodySize {
		return fmt.Errorf(
			"block body size mismatch: header declares %d, actual size is %d",
			declaredBodySize,
			actualBodySize,
		)
	}
	return nil
}

// BlockDeclaredExUnits sums the execution units the block's transactions
// declare in their redeemers.
func BlockDeclaredExUnits(block gledger.Block) (lcommon.ExUnits, error) {
	var total lcommon.ExUnits
	for index, tx := range block.Transactions() {
		declared, err := eras.DeclaredExUnits(tx)
		if err != nil {
			return lcommon.ExUnits{}, fmt.Errorf(
				"transaction %d declared execution units: %w", index, err,
			)
		}
		total, err = eras.SafeAddExUnits(total, declared)
		if err != nil {
			return lcommon.ExUnits{}, fmt.Errorf(
				"block declared execution units: %w", err,
			)
		}
	}
	return total, nil
}

// ValidateBlockExUnits rejects a block whose declared execution units exceed
// the protocol's block budget.
func ValidateBlockExUnits(
	block gledger.Block,
	pparams lcommon.ProtocolParameters,
) error {
	return validateBlockExUnits(block, pparams)
}

// BlockReferenceScriptSize is the reference-script size the block's
// transactions consume in aggregate, measured as ValidateBlockReferenceScripts
// measures it. It is zero for eras without the per-block rule.
func BlockReferenceScriptSize(
	block gledger.Block,
	pp lcommon.ProtocolParameters,
	state lcommon.UtxoState,
) (uint64, error) {
	if _, isConway := block.(*conway.ConwayBlock); !isConway {
		return 0, nil
	}
	conwayPParams, ok := pp.(*conway.ConwayProtocolParameters)
	if !ok || conwayPParams == nil {
		return 0, errors.New("pparams are not expected type")
	}
	return lcommon.ConsumedReferenceScriptSizePerBlock(
		block,
		aggregateReferenceScriptState{state: state},
		conwayPParams.ProtocolVersion.Major >= lcommon.ProtocolVersionVanRossem,
		nil,
	)
}

// ValidateBlockReferenceScripts rejects a block whose transactions consume
// more reference-script bytes in aggregate than the era allows.
func ValidateBlockReferenceScripts(
	block gledger.Block,
	pp lcommon.ProtocolParameters,
	state lcommon.UtxoState,
) error {
	return validateBlockReferenceScripts(block, pp, state)
}
