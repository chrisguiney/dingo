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
	"context"
	"fmt"

	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// removeAvvmUtxos ports cardano-ledger's
// Allegra/Translation.hs:returnRedeemAddrsToReserves to the dingo
// ledger. At the Shelley→Allegra boundary every live UTxO at a Byron
// redeem (AVVM) address must leave the active UTxO set; its lovelace
// is returned to reserves by the hard-fork rule. This helper classifies
// and marks the rows and returns the reclaimed total so the caller can
// update NetworkState atomically.
//
// The database layer exposes only era-agnostic primitives: an
// iterator over live UTxO rows (with CBOR populated) and a bulk
// "mark deleted at slot" call. The AVVM classification — decoding
// each output's address and checking for Byron + redeem — lives here
// because that knowledge is ledger-domain.
//
// Divergence from cardano-ledger:
//
// Haskell's Shelley ledger stashes the AVVM UTxO set into a dedicated
// `stashedAVVMAddresses` NewEpochState field at genesis-build time, and
// Allegra translation consumes that stash via
// `shelleyToAllegraAVVMsToDelete = stashedAVVMAddresses`, then discards
// the field. The comment at
// cardano-ledger/eras/allegra/.../Translation.hs:53-54 is explicit about
// the reason: a full UTxO scan at the boundary would be prohibitive on a
// disk-persisted state. Dingo scans the live UTxO instead, which is
// mainnet-equivalent because no AVVM UTxOs were spent before the
// Allegra boundary so the live AVVM set equals the stashed set there.
// On a hypothetical chain where AVVM UTxOs ARE spent pre-Allegra, the
// scan-based approach is still correct — the stash variant gives the
// same answer because spent UTxOs are absent from both views — so the
// divergence is purely an implementation choice driven by dingo's
// live UTxO set model, not a semantic difference.
//
// Returns the count and total lovelace of the outputs removed.
func (ls *LedgerState) removeAvvmUtxos(ctx context.Context,
	txn ledgerdb.Txn,
	boundarySlot uint64,
) (int, uint64, error) {
	var (
		refs          []types.UtxoKey
		totalLovelace uint64
	)
	if err := ls.iterateLiveUtxos(txn, func(u *models.Utxo) error {
		out, err := u.Decode()
		if err != nil {
			return fmt.Errorf(
				"decode utxo %x#%d: %w",
				u.TxId[:8], u.OutputIdx, err,
			)
		}
		isAvvm, amountUint64, err := ClassifyAvvmOutput(
			out.Address(), out.Amount(),
		)
		if err != nil {
			return fmt.Errorf(
				"AVVM UTxO %x#%d has %w",
				u.TxId[:8], u.OutputIdx, err,
			)
		}
		if !isAvvm {
			return nil
		}
		refs = append(refs, types.UtxoKey{
			TxId:      u.TxId,
			OutputIdx: u.OutputIdx,
		})
		totalLovelace, err = addAvvmLovelace(totalLovelace, amountUint64)
		if err != nil {
			return fmt.Errorf(
				"%w at tx %x#%d",
				err, u.TxId[:8], u.OutputIdx,
			)
		}
		return nil
	}); err != nil {
		return 0, 0, fmt.Errorf("scan live utxos: %w", err)
	}
	if len(refs) > 0 {
		removed := make([]models.UtxoId, 0, len(refs))
		for _, r := range refs {
			removed = append(
				removed,
				models.UtxoId{Hash: r.TxId, Idx: r.OutputIdx},
			)
		}
		ls.publishUtxoEventAfterCommit(ctx, txn, UtxoEvent{
			Point:   ocommon.Point{Slot: boundarySlot},
			Cause:   UtxoChangeAvvmRemoval,
			Removed: removed,
		}, nil)
	}
	return len(refs), totalLovelace, nil
}
