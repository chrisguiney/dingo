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
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	gledger "github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
)

// ErrDecodeOutput is returned, wrapped, when an output is not a transaction
// output.
var ErrDecodeOutput = errors.New("dxoracle: output does not decode")

// LiveStakeAccount is a stake account row. Pool is the delegated pool key
// hash; empty means not delegated.
type LiveStakeAccount struct {
	Tag    uint8
	Key    []byte
	Reward uint64
	Active bool
	Pool   []byte
}

// LiveStakeInput is every row the reward live stake rebuild reads. Outputs
// are transaction output CBOR; each becomes a live UTxO classified by its
// address.
type LiveStakeInput struct {
	Accounts []LiveStakeAccount
	Outputs  [][]byte
	Slot     uint64
}

// LiveStakeRow is one credential's row of the reward live stake aggregate.
type LiveStakeRow struct {
	Tag         uint8
	Key         []byte
	Pool        []byte
	UtxoStake   uint64
	RewardStake uint64
	TotalStake  uint64
	Registered  bool
}

// LiveStake seeds in into the scratch database, runs the production reward
// live stake rebuild and reads its rows.
func (o *Oracle) LiveStake(
	ctx context.Context,
	in LiveStakeInput,
) ([]LiveStakeRow, error) {
	var rows []LiveStakeRow
	err := o.withScratch(ctx, func(
		_ dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedLiveStake(ctx, txn, seq, in); err != nil {
			return err
		}
		if err := o.db.Metadata().RebuildRewardLiveStake(
			in.Slot, txn.Metadata(),
		); err != nil {
			return fmt.Errorf("rebuild reward live stake: %w", err)
		}
		return o.db.EachRewardLiveStake(
			txn, func(r *models.RewardLiveStake) error {
				rows = append(rows, LiveStakeRow{
					Tag:         r.CredentialTag,
					Key:         append([]byte(nil), r.StakingKey...),
					Pool:        append([]byte(nil), r.PoolKeyHash...),
					UtxoStake:   uint64(r.UtxoStake),
					RewardStake: uint64(r.RewardStake),
					TotalStake:  uint64(r.TotalStake),
					Registered:  r.Registered,
				})
				return nil
			},
		)
	})
	return rows, err
}

// seedLiveStake writes in through database.Database.CreateAccount and the
// metadata store's CreateUtxo, with the rows models.UtxoLedgerToModel builds
// from each output.
func (o *Oracle) seedLiveStake(
	ctx context.Context,
	txn *database.Txn,
	seq *uint64,
	in LiveStakeInput,
) error {
	for _, a := range in.Accounts {
		acct := &models.Account{
			CredentialTag: a.Tag,
			StakingKey:    a.Key,
			Reward:        types.Uint64(a.Reward),
			Active:        a.Active,
		}
		if len(a.Pool) > 0 {
			acct.Pool = a.Pool
		}
		if err := o.db.CreateAccount(ctx, txn, acct); err != nil {
			return fmt.Errorf("seed account: %w", err)
		}
	}
	for _, cbor := range in.Outputs {
		out, err := gledger.NewTransactionOutputFromCbor(cbor)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrDecodeOutput, err)
		}
		*seq++
		id, err := shelley.NewShelleyTransactionInput(
			hex.EncodeToString(txHash(*seq)), 0,
		)
		if err != nil {
			return fmt.Errorf("seed utxo id: %w", err)
		}
		row, err := models.UtxoLedgerToModel(
			lcommon.Utxo{Id: id, Output: out}, in.Slot,
		)
		if err != nil {
			return fmt.Errorf("seed utxo: %w", err)
		}
		if err := o.db.Metadata().CreateUtxo(txn.Metadata(), &row); err != nil {
			return fmt.Errorf("seed utxo: %w", err)
		}
	}
	return nil
}
