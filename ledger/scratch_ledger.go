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
	"errors"
	"io"
	"log/slog"

	"github.com/blinklabs-io/dingo/config/cardano"
	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// ScratchLedgerConfig describes the one epoch boundary a ScratchLedger runs.
type ScratchLedgerConfig struct {
	// Database must already have its LedgerDB set open and is not closed by
	// the ScratchLedger.
	Database *database.Database
	// UtxoSet is the live set opened over Database.
	UtxoSet UtxoSetWriter
	// NodeConfig carries the genesis the boundary reads.
	NodeConfig *cardano.CardanoNodeConfig
	// Era, Epoch and PParams are the ledger state before the boundary.
	Era     eras.EraDesc
	Epoch   models.Epoch
	PParams lcommon.ProtocolParameters
	Logger  *slog.Logger
}

// ScratchLedger is a LedgerState over a scratch database, positioned at the
// end of one epoch, that runs the production epoch rollover for that
// boundary. It has no chain manager, so it can run no other ledger operation.
type ScratchLedger struct {
	ls  *LedgerState
	cfg ScratchLedgerConfig
}

// NewScratchLedger builds a ScratchLedger and seeds the rows a boundary needs
// from a node that has run the epoch: the current epoch row, the parameters
// and ADA pots the reward application reads, and an empty mark snapshot.
func NewScratchLedger(cfg ScratchLedgerConfig) (*ScratchLedger, error) {
	if cfg.Database == nil || cfg.UtxoSet == nil || cfg.NodeConfig == nil ||
		cfg.PParams == nil {
		return nil, errors.New(
			"scratch ledger: Database, UtxoSet, NodeConfig and PParams are required",
		)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	ls := &LedgerState{
		db:             cfg.Database,
		currentEra:     cfg.Era,
		currentEpoch:   cfg.Epoch,
		currentPParams: cfg.PParams,
		config: LedgerStateConfig{
			CardanoNodeConfig: cfg.NodeConfig,
			Logger:            cfg.Logger,
			UtxoSet:           cfg.UtxoSet,
		},
	}
	s := &ScratchLedger{ls: ls, cfg: cfg}
	if err := cfg.Database.SetLiveUtxoSource(ls); err != nil {
		return nil, err
	}
	ls.installNonceSource()
	ls.installEpochSource()
	ls.installPParamsSource()
	ls.installPoolSource()
	if err := s.seedBoundaryBasis(); err != nil {
		return nil, err
	}
	ls.publishSnapshotsLocked()
	return s, nil
}

// LedgerState returns the underlying ledger, for wiring the epoch-boundary
// hooks the way the node does.
func (s *ScratchLedger) LedgerState() *LedgerState { return s.ls }

// Seed returns the writer a caller seeds ledger state through, outside any
// block, as an import does.
func (s *ScratchLedger) Seed() ledgerdb.LedgerDB {
	return stagingLedgerDB{LedgerDB: s.ls.ledgerDB(), ls: s.ls}
}

// SeedChanges stages changes to the ledger's set outside any block.
func (s *ScratchLedger) SeedChanges(changes LedgerChanges) {
	s.ls.stageLedgerChanges(nil, changes)
}

// RunRollover runs the production processEpochRollover for the configured
// boundary in one committed write transaction.
func (s *ScratchLedger) RunRollover(
	ctx context.Context,
) (*EpochRolloverResult, error) {
	var result *EpochRolloverResult
	txn := s.cfg.Database.Transaction(ctx, true)
	err := txn.Do(func(txn *database.Txn) error {
		var err error
		result, err = s.ls.processEpochRollover(
			ctx, txn, s.ls.currentEpoch, s.ls.currentEra, s.ls.currentPParams,
		)
		return err
	})
	// The boundary hands its ratification to a job that runs after the commit;
	// let it finish before the caller reads or closes the database.
	s.ls.ratificationWG.Wait()
	return result, err
}

// seedBoundaryBasis writes what the reward application of the boundary into
// Epoch.EpochId+1 reads from the epochs before it: their epoch rows, the
// parameters of the performance epoch, the ADA pots row and an empty mark
// snapshot.
func (s *ScratchLedger) seedBoundaryBasis() error {
	db := s.Seed()
	cur := s.cfg.Epoch
	if err := db.SetEpoch(
		cur.StartSlot, cur.EpochId, cur.Nonce, cur.EvolvingNonce,
		cur.CandidateNonce, cur.LastEpochBlockNonce, cur.EraId,
		cur.SlotLength, cur.LengthInSlots, nil,
	); err != nil {
		return err
	}
	rewardEpochs, ok := epochengine.StakeRewardEpochsForApplication(cur.EpochId + 1)
	if !ok {
		return errors.New("scratch ledger: epoch too early for a reward round")
	}
	for _, id := range []uint64{
		rewardEpochs.Snapshot, rewardEpochs.Performance, rewardEpochs.Pots,
	} {
		epoch, err := db.GetEpoch(id, nil)
		if err != nil {
			return err
		}
		if epoch != nil {
			continue
		}
		start := cur.StartSlot - (cur.EpochId-id)*uint64(cur.LengthInSlots)
		if err := db.SetEpoch(
			start, id, nil, nil, nil, nil, cur.EraId, cur.SlotLength,
			cur.LengthInSlots, nil,
		); err != nil {
			return err
		}
	}
	encoded, err := cbor.Encode(s.cfg.PParams)
	if err != nil {
		return err
	}
	performance, err := db.GetEpoch(rewardEpochs.Performance, nil)
	if err != nil || performance == nil {
		return errors.Join(err, errors.New("scratch ledger: no performance epoch"))
	}
	if err := db.SetPParams(
		encoded, performance.StartSlot, rewardEpochs.Performance, cur.EraId, nil,
	); err != nil {
		return err
	}
	state, err := db.GetNetworkState(nil)
	if err != nil {
		return err
	}
	var treasury, reserves types.Uint64
	if state != nil {
		treasury, reserves = state.Treasury, state.Reserves
	}
	if err := db.SaveRewardAdaPots(&models.RewardAdaPots{
		Epoch: rewardEpochs.Pots, Treasury: treasury, Reserves: reserves,
		CapturedSlot: cur.StartSlot,
	}, nil); err != nil {
		return err
	}
	if err := db.DeleteRewardInputsForEpoch(rewardEpochs.Snapshot, nil); err != nil {
		return err
	}
	if err := db.DeleteRewardOutputsForEpoch(rewardEpochs.Snapshot, nil); err != nil {
		return err
	}
	return db.SaveRewardSnapshot(&models.RewardSnapshot{
		Epoch: rewardEpochs.Snapshot, SnapshotType: "mark",
		CapturedSlot: cur.StartSlot, BoundarySlot: cur.StartSlot,
	}, nil)
}
