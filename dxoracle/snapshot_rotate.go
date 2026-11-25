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
	"math/big"
	"sort"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/event"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/dingo/ledger/snapshot"
)

// SnapshotStake is one credential's stake in a snapshot, or, as input, its
// UTxO stake. Pool is empty when the credential is not delegated.
type SnapshotStake struct {
	Tag    uint8
	Key    []byte
	Amount uint64
	Pool   []byte
}

// SnapshotPool is a registered pool.
type SnapshotPool struct {
	Key    []byte
	Pledge uint64
	Cost   uint64
}

// SnapshotRotateInput is the state at an epoch boundary into Epoch (at least
// 4). PriorMark, PriorSet and PriorGo are the Mark captures stored under
// epochs Epoch-1, Epoch-2 and Epoch-3: the snapshots in force before the
// boundary.
type SnapshotRotateInput struct {
	Epoch                        uint64
	PriorMark, PriorSet, PriorGo []SnapshotStake
	Stake                        []SnapshotStake
	Accounts                     []LiveStakeAccount
	Pools                        []SnapshotPool
	// Fees is the fee pot the ended epoch collected.
	Fees uint64
}

// SnapshotRotateResult holds the three snapshots after the boundary, each
// sorted by credential.
type SnapshotRotateResult struct {
	Mark, Set, Go []SnapshotStake
	// FeeSnapshot is the fee pot stored for the new epoch.
	FeeSnapshot uint64
}

const snapshotEpochLength = 432000

// SnapshotRotate seeds in into the scratch database, runs the production SNAP
// capture (snapshot.Manager.ComputeEpochBoundarySnapshot then
// CaptureEpochBoundarySnapshot) for the boundary into in.Epoch, and reads the
// Mark back from the epoch's stored rows. The Set and Go snapshots are read
// from the epochs snapshot.ComputeSnapshotRotationEpochs names, because Dingo
// rotates by reading older Marks back, not by copying rows.
func (o *Oracle) SnapshotRotate(
	ctx context.Context,
	in SnapshotRotateInput,
) (SnapshotRotateResult, error) {
	var out SnapshotRotateResult
	if in.Epoch < 4 {
		return out, fmt.Errorf("dxoracle: snapshot epoch %d is below 4", in.Epoch)
	}
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedSnapshotRotate(ctx, txn, seq, in); err != nil {
			return err
		}
		boundary := in.Epoch * snapshotEpochLength
		evt := event.EpochTransitionEvent{
			PreviousEpoch:   in.Epoch - 1,
			NewEpoch:        in.Epoch,
			BoundarySlot:    boundary,
			EpochNonce:      []byte{0x0a, 0x0b},
			ProtocolVersion: 10,
			SnapshotSlot:    boundary - 1,
		}
		mgr := snapshot.NewManager(adapter, event.NewEventBus(nil, nil), nil)
		if err := mgr.ComputeEpochBoundarySnapshot(ctx, txn, evt); err != nil {
			return fmt.Errorf("snap-point stake: %w", err)
		}
		if err := mgr.CaptureEpochBoundarySnapshot(ctx, txn, evt); err != nil {
			return fmt.Errorf("capture mark: %w", err)
		}
		setEpoch, goEpoch := snapshot.ComputeSnapshotRotationEpochs(in.Epoch)
		fees, err := o.captureFees(txn, in.Epoch)
		if err != nil {
			return err
		}
		out.FeeSnapshot = fees
		if out.Mark, err = o.readSnapshot(txn, in.Epoch); err != nil {
			return err
		}
		if out.Set, err = o.readSnapshot(txn, setEpoch); err != nil {
			return err
		}
		out.Go, err = o.readSnapshot(txn, goEpoch)
		return err
	})
	return out, err
}

func (o *Oracle) readSnapshot(
	txn *database.Txn, epoch uint64,
) ([]SnapshotStake, error) {
	rows, err := o.db.Metadata().GetRewardStakeInputs(epoch, txn.Metadata())
	if err != nil {
		return nil, fmt.Errorf("read epoch %d stake: %w", epoch, err)
	}
	var out []SnapshotStake
	for _, r := range rows {
		out = append(out, SnapshotStake{
			Tag:    r.CredentialTag,
			Key:    append([]byte(nil), r.StakingKey...),
			Amount: uint64(r.Stake),
			Pool:   append([]byte(nil), r.PoolKeyHash...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tag != out[j].Tag {
			return out[i].Tag < out[j].Tag
		}
		return bytes.Compare(out[i].Key, out[j].Key) < 0
	})
	return out, nil
}

func (o *Oracle) seedSnapshotRotate(
	ctx context.Context,
	txn *database.Txn,
	seq *uint64,
	in SnapshotRotateInput,
) error {
	for e := uint64(0); e < in.Epoch; e++ {
		if err := o.db.SetEpoch(
			e*snapshotEpochLength, e, nil, nil, nil, nil,
			eras.ShelleyEraDesc.Id, 1, snapshotEpochLength, txn,
		); err != nil {
			return fmt.Errorf("seed epoch %d: %w", e, err)
		}
	}
	slot := (in.Epoch-1)*snapshotEpochLength + 1
	for _, p := range in.Pools {
		margin := &types.Rat{Rat: big.NewRat(1, 100)}
		pool := &models.Pool{
			PoolKeyHash: p.Key, VrfKeyHash: make([]byte, 32),
			Pledge: types.Uint64(p.Pledge), Cost: types.Uint64(p.Cost),
			Margin: margin, RewardAccount: make([]byte, 28),
		}
		reg := &models.PoolRegistration{
			PoolKeyHash: p.Key, AddedSlot: slot, VrfKeyHash: make([]byte, 32),
			Pledge: types.Uint64(p.Pledge), Cost: types.Uint64(p.Cost),
			Margin: margin, RewardAccount: make([]byte, 28),
		}
		if err := o.db.ImportPool(ctx, txn, pool, reg); err != nil {
			return fmt.Errorf("seed pool: %w", err)
		}
	}
	for _, a := range in.Accounts {
		acct := &models.Account{
			CredentialTag: a.Tag, StakingKey: a.Key,
			Reward: types.Uint64(a.Reward), Active: a.Active, AddedSlot: slot,
		}
		if len(a.Pool) > 0 {
			acct.Pool = a.Pool
		}
		if err := o.db.CreateAccount(ctx, txn, acct); err != nil {
			return fmt.Errorf("seed account: %w", err)
		}
	}
	for _, s := range in.Stake {
		*seq++
		utxo := &models.Utxo{
			TxId: txHash(*seq), OutputIdx: 0, StakingKey: s.Key,
			CredentialTag: s.Tag, Amount: types.Uint64(s.Amount), AddedSlot: slot,
		}
		if err := o.db.CreateUtxo(ctx, txn, utxo); err != nil {
			return fmt.Errorf("seed utxo: %w", err)
		}
	}
	if in.Fees > 0 {
		fees := types.Uint64(in.Fees)
		if err := o.db.Metadata().SaveRewardAdaPots(&models.RewardAdaPots{
			Epoch: in.Epoch - 1, ImportedEpochFees: &fees, CapturedSlot: slot,
		}, txn.Metadata()); err != nil {
			return fmt.Errorf("seed fee pot: %v", err)
		}
	}
	for i, prior := range [][]SnapshotStake{in.PriorMark, in.PriorSet, in.PriorGo} {
		epoch := in.Epoch - 1 - uint64(i)
		var rows []*models.RewardStakeInput
		for _, s := range prior {
			rows = append(rows, &models.RewardStakeInput{
				PoolKeyHash: s.Pool, StakingKey: s.Key, CredentialTag: s.Tag,
				Epoch: epoch, Stake: types.Uint64(s.Amount), Registered: true,
				BoundarySlot: epoch * snapshotEpochLength,
			})
		}
		if len(rows) == 0 {
			continue
		}
		if err := o.db.Metadata().SaveRewardStakeInputs(rows, txn.Metadata()); err != nil {
			return fmt.Errorf("seed epoch %d snapshot: %w", epoch, err)
		}
	}
	return nil
}

// feeHost serves the epoch engine's fee step from the scratch database. Its
// fee reader is the production one the ledger's host calls.
type feeHost struct {
	epochengine.Host
	ldb ledgerdb.LedgerDB
}

func (h feeHost) LedgerDB() ledgerdb.LedgerDB { return h.ldb }

func (h feeHost) RewardEpochFees(
	txn ledgerdb.Txn, epoch models.Epoch,
) (uint64, error) {
	return epochengine.RewardEpochFees(h.ldb, txn, epoch)
}

// captureFees runs the engine step that stores the fee pot of the epoch that
// just ended on the new epoch's ADA pots row, and reads that row back.
func (o *Oracle) captureFees(txn *database.Txn, epoch uint64) (uint64, error) {
	ldb := dbadapter.New(o.db)
	ended := models.Epoch{
		EpochId:       epoch - 1,
		StartSlot:     (epoch - 1) * snapshotEpochLength,
		LengthInSlots: snapshotEpochLength,
	}
	engine := epochengine.New(epochengine.Config{}, feeHost{ldb: ldb})
	if err := engine.SaveRewardAdaPotsForEpoch(
		txn, epoch, ended, epoch*snapshotEpochLength,
	); err != nil {
		return 0, fmt.Errorf("save ada pots: %w", err)
	}
	pots, err := ldb.GetRewardAdaPots(epoch, txn)
	if err != nil || pots == nil {
		return 0, fmt.Errorf("read ada pots: %v", err)
	}
	return uint64(pots.Fees), nil
}
