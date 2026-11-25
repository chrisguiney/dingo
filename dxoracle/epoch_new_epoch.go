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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"sort"
	"strings"

	"github.com/blinklabs-io/dingo/config/cardano"
	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/event"
	"github.com/blinklabs-io/dingo/ledger"
	"github.com/blinklabs-io/dingo/ledger/epochengine"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/dingo/ledger/snapshot"
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	ochainsync "github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// NewEpochAccount is a registered stake account.
type NewEpochAccount struct {
	Tag    uint8
	Key    []byte
	Reward uint64
	Pool   []byte
	// Drep is the account's DRep delegation.
	Drep Delegation
}

// NewEpochPool is a registered pool. RetiringEpoch is nil when no retirement
// is scheduled.
type NewEpochPool struct {
	Key           []byte
	Pledge, Cost  uint64
	Deposit       uint64
	RewardTag     uint8
	RewardKey     []byte
	RetiringEpoch *uint64
}

const rewardBlockBatch = 500

// NewEpochRewards is a reward update pending at the boundary: the rows the
// delayed reward round reads. StakeInputs are the snapshot in force as GO,
// PoolInputs and Snapshot describe it, BlockCounts are the blocks each pool
// made in the epoch before Epoch, and Fees is the fee pot the round pays out.
// The round runs at the boundary, as it does when a block after the stability
// window has been applied; without NewEpochRewards no round runs.
type NewEpochRewards struct {
	Snapshot    *models.RewardSnapshot
	PoolInputs  []*models.RewardPoolInput
	StakeInputs []*models.RewardStakeInput
	BlockCounts map[string]uint64
	Fees        uint64
}

// NewEpochInput is the ledger at the end of epoch Epoch. The boundary enters
// Epoch+1. PriorMark, PriorSet and PriorGo are the snapshots in force.
type NewEpochInput struct {
	Epoch                        uint64
	PParams                      lcommon.ProtocolParameters
	Reserves, Treasury           uint64
	Donations                    uint64
	Accounts                     []NewEpochAccount
	Pools                        []NewEpochPool
	Stake                        []SnapshotStake
	PriorMark, PriorSet, PriorGo []SnapshotStake
	// Rewards is the pending reward update, nil when there is none. When set
	// its stake rows replace PriorGo.
	Rewards *NewEpochRewards
	// Gov is the governance state the boundary reads, nil for none. It is
	// the state RATIFY decided over at the boundary before: that decision is
	// made by the production RATIFY on a database of its own, and the
	// boundary under test enacts and drops what it ratified and expired.
	Gov *GovState
}

// NewEpochOutput is what the boundary left behind.
type NewEpochOutput struct {
	Epoch              uint64
	Accounts           []NewEpochAccount
	PoolsRemoved       [][]byte
	Reserves, Treasury uint64
	Mark, Set, Go      []SnapshotStake
	// Gov is set when the input held governance state.
	Gov *NewEpochGovOutput
}

// NewEpoch builds a scratch ledger at the end of in.Epoch, runs the
// production epoch rollover into in.Epoch+1 with the node's snapshot hooks
// wired, and reads the resulting rows. Every call uses its own database.
func NewEpoch(ctx context.Context, in NewEpochInput) (out NewEpochOutput, err error) {
	if in.Epoch < 4 {
		return out, fmt.Errorf("dxoracle: epoch %d is below 4", in.Epoch)
	}
	store, err := openScratchLedgerStore(ctx)
	if err != nil {
		return out, fmt.Errorf("dxoracle: open database: %w", err)
	}
	defer store.close()
	db, set := store.db, store.set

	nodeCfg := &cardano.CardanoNodeConfig{ShelleyGenesisHash: strings.Repeat("5a", 32)}
	if err := scratchShelleyGenesis(nodeCfg, strings.NewReader(`{
		"activeSlotsCoeff": 0.05,
		"epochLength": 432000,
		"maxLovelaceSupply": 45000000000000000,
		"securityParam": 2160,
		"slotLength": 1,
		"updateQuorum": 5,
		"systemStart": "2017-09-23T21:44:51Z"
	}`)); err != nil {
		return out, fmt.Errorf("dxoracle: genesis: %w", err)
	}
	epoch := models.Epoch{
		EpochId: in.Epoch, StartSlot: in.Epoch * snapshotEpochLength,
		SlotLength: 1000, LengthInSlots: snapshotEpochLength,
		EraId: eras.ConwayEraDesc.Id,
	}
	sl, err := ledger.NewScratchLedger(ledger.ScratchLedgerConfig{
		Database: db, UtxoSet: set, NodeConfig: nodeCfg,
		Era: eras.ConwayEraDesc, Epoch: epoch, PParams: in.PParams,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	if err != nil {
		return out, fmt.Errorf("dxoracle: %w", err)
	}
	var prior *PriorBoundary
	if in.Gov != nil {
		if prior, err = priorBoundary(ctx, *in.Gov, in.Epoch); err != nil {
			return out, fmt.Errorf("dxoracle: prior boundary: %w", err)
		}
	}
	if err := seedNewEpoch(sl, set, epoch, in, prior); err != nil {
		return out, err
	}
	mgr := snapshot.NewManager(sl.LedgerState().LedgerDB(), event.NewEventBus(nil, nil), nil)
	ls := sl.LedgerState()
	ls.SetEpochBoundarySnapshotStakeHook(
		func(txn ledgerdb.Txn, evt event.EpochTransitionEvent) error {
			return mgr.ComputeEpochBoundarySnapshot(context.Background(), txn, evt)
		},
	)
	ls.SetEpochBoundarySnapshotHook(
		func(txn ledgerdb.Txn, evt event.EpochTransitionEvent) error {
			return mgr.CaptureEpochBoundarySnapshot(context.Background(), txn, evt)
		},
	)
	ls.SetCurrentBoundarySPOStakeHook(
		func(txn ledgerdb.Txn, evt event.EpochTransitionEvent, incoming uint) ([]*models.PoolStakeSnapshot, error) {
			return mgr.CurrentBoundarySPOStakeRows(context.Background(), txn, evt, incoming)
		},
	)
	before, err := ls.LedgerDB().GetActivePoolKeyHashes(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("dxoracle: pools before: %w", err)
	}
	if in.Rewards != nil {
		// The update starts once a block past start + 4k/f is applied, and the
		// boundary reads it only if the ended epoch had such a block. The tip
		// is set after the pool read above, which a tip makes the set refuse.
		tip := ochainsync.Tip{Point: ocommon.Point{
			Slot: epoch.StartSlot + snapshotEpochLength*3/4, Hash: txHash(0xff),
		}}
		if err := sl.Seed().SetTip(tip, nil); err != nil {
			return out, fmt.Errorf("dxoracle: tip: %w", err)
		}
	}
	result, err := sl.RunRollover(ctx)
	if err != nil {
		return out, fmt.Errorf("dxoracle: rollover: %w", err)
	}
	out.Epoch = result.NewCurrentEpoch.EpochId
	out, err = readNewEpoch(ctx, ls, in, before, out)
	if err != nil || in.Gov == nil {
		return out, err
	}
	out.Gov, err = readNewEpochGov(ctx, ls.LedgerDB(), *in.Gov, prior, out.Epoch)
	return out, err
}

func seedNewEpoch(
	sl *ledger.ScratchLedger, set ledger.UtxoSetWriter, epoch models.Epoch, in NewEpochInput,
	prior *PriorBoundary,
) error {
	db := sl.Seed()
	if err := db.SetNetworkState(in.Treasury, in.Reserves, epoch.StartSlot, nil); err != nil {
		return fmt.Errorf("dxoracle: network state: %w", err)
	}
	if in.Donations > 0 {
		if err := db.AddNetworkDonation(epoch.StartSlot+5, epoch.EpochId, in.Donations, nil); err != nil {
			return fmt.Errorf("dxoracle: donations: %w", err)
		}
	}
	var changes ledger.LedgerChanges
	for _, p := range in.Pools {
		margin := &types.Rat{Rat: big.NewRat(1, 100)}
		reg := &models.PoolRegistration{
			PoolKeyHash: p.Key, VrfKeyHash: make([]byte, 32),
			Pledge: types.Uint64(p.Pledge), Cost: types.Uint64(p.Cost),
			Margin: margin, RewardAccount: p.RewardKey,
			RewardAccountCredentialTag: p.RewardTag,
			AddedSlot:                  epoch.StartSlot - 10,
			DepositAmount:              types.Uint64(p.Deposit),
		}
		deposit := p.Deposit
		stateSlot := reg.AddedSlot
		changes.PoolEvents = append(changes.PoolEvents, ledger.PoolEventChange{
			Pool:  p.Key,
			Event: ledger.PoolEvent{Slot: reg.AddedSlot, Registration: ledger.CleanPoolRegistration(reg)},
		})
		if p.RetiringEpoch != nil {
			stateSlot = epoch.StartSlot + 1
			changes.PoolEvents = append(changes.PoolEvents, ledger.PoolEventChange{
				Pool:  p.Key,
				Event: ledger.PoolEvent{Slot: stateSlot, RetiringEpoch: *p.RetiringEpoch},
			})
		}
		changes.Pools = append(changes.Pools, ledger.PoolChange{
			Slot: stateSlot,
			State: ledger.PoolState{
				Pool: p.Key, Params: ledger.CleanPoolRegistration(reg),
				RetiringEpoch: p.RetiringEpoch, DepositHeld: &deposit,
			},
		})
	}
	for _, a := range in.Accounts {
		acct := models.Account{
			CredentialTag: a.Tag, StakingKey: a.Key, Reward: types.Uint64(a.Reward),
			Active: true, AddedSlot: epoch.StartSlot - 10,
		}
		if len(a.Pool) > 0 {
			acct.Pool = a.Pool
		}
		if err := setDelegation(&acct, a.Drep); err != nil {
			return err
		}
		changes.Accounts = append(changes.Accounts, ledger.AccountChange{Slot: acct.AddedSlot, Account: acct})
	}
	for i, prior := range [][]SnapshotStake{in.PriorMark, in.PriorSet, in.PriorGo} {
		ep := epoch.EpochId - uint64(i)
		var rows []*models.RewardStakeInput
		if i == 2 && in.Rewards != nil {
			for _, r := range in.Rewards.StakeInputs {
				row := *r
				row.Epoch, row.BoundarySlot = ep, ep*snapshotEpochLength
				rows = append(rows, &row)
			}
			prior = nil
		}
		for _, s := range prior {
			rows = append(rows, &models.RewardStakeInput{
				PoolKeyHash: s.Pool, StakingKey: s.Key, CredentialTag: s.Tag,
				Epoch: ep, Stake: types.Uint64(s.Amount), Registered: true,
				BoundarySlot: ep * snapshotEpochLength,
			})
		}
		if len(rows) > 0 {
			changes.RewardInputs = append(changes.RewardInputs, ledger.RewardInputChange{
				Kind: ledger.RewardInputStakes, Epoch: ep, Stakes: rows,
			})
		}
	}
	if in.Rewards != nil {
		if err := seedPendingRewards(sl, db, epoch, in); err != nil {
			return err
		}
	}
	if in.Gov != nil {
		govChanges(&changes, *in.Gov, prior)
	}
	sl.SeedChanges(changes)
	for i, s := range in.Stake {
		addr, err := lcommon.NewAddressFromParts(
			lcommon.AddressTypeKeyKey, lcommon.AddressNetworkTestnet,
			bytes.Repeat([]byte{0x37}, lcommon.AddressHashSize), s.Key,
		)
		if err != nil {
			return fmt.Errorf("dxoracle: stake address: %w", err)
		}
		encoded, err := cbor.Encode(&shelley.ShelleyTransactionOutput{OutputAddress: addr, OutputAmount: s.Amount})
		if err != nil {
			return err
		}
		if err := set.ApplyUtxoChanges(nil, []ledger.UtxoEvent{{Added: []ledger.UtxoOutput{{
			Ref:  models.UtxoId{Hash: txHash(uint64(i + 1)), Idx: 0},
			Cbor: encoded,
		}}}}); err != nil {
			return fmt.Errorf("dxoracle: seed stake: %w", err)
		}
	}
	return nil
}

// seedPendingRewards seeds what the reward round applied at the boundary into
// Epoch+1 reads, as a node that ran the epoch holds it: the GO snapshot's
// header and pool rows (its stake rows are seeded with the other snapshots),
// the blocks each pool made in the epoch before Epoch and the pots row with
// the fee pot.
func seedPendingRewards(
	sl *ledger.ScratchLedger, db ledgerdb.LedgerDB, epoch models.Epoch,
	in NewEpochInput,
) error {
	r := in.Rewards
	epochs, ok := epochengine.StakeRewardEpochsForApplication(epoch.EpochId + 1)
	if !ok || epochs.Bootstrap {
		return fmt.Errorf("dxoracle: epoch %d has no reward round", epoch.EpochId+1)
	}
	if r.Snapshot == nil {
		return errors.New("dxoracle: pending rewards need a snapshot header")
	}
	if err := db.SaveRewardAdaPots(&models.RewardAdaPots{
		Epoch: epochs.Pots, Treasury: types.Uint64(in.Treasury),
		Reserves: types.Uint64(in.Reserves), Fees: types.Uint64(r.Fees),
		CapturedSlot: epoch.StartSlot,
	}, nil); err != nil {
		return fmt.Errorf("dxoracle: reward pots: %w", err)
	}
	header := *r.Snapshot
	header.Epoch, header.SnapshotType = epochs.Snapshot, "mark"
	header.CapturedSlot = epochs.Snapshot * snapshotEpochLength
	header.BoundarySlot = header.CapturedSlot
	if err := db.SaveRewardSnapshot(&header, nil); err != nil {
		return fmt.Errorf("dxoracle: reward snapshot: %w", err)
	}
	var pools []*models.RewardPoolInput
	for _, p := range r.PoolInputs {
		row := *p
		row.Epoch = epochs.Snapshot
		row.CapturedSlot, row.BoundarySlot = header.CapturedSlot, header.BoundarySlot
		pools = append(pools, &row)
	}
	if err := db.SaveRewardPoolInputs(pools, nil); err != nil {
		return fmt.Errorf("dxoracle: reward pool inputs: %w", err)
	}
	perfStart := epochs.Performance * snapshotEpochLength
	blocks := []ledger.RewardBlockChange{{Slot: perfStart, Epoch: epochs.Performance, Open: true}}
	pks := make([]string, 0, len(r.BlockCounts))
	for k := range r.BlockCounts {
		pks = append(pks, k)
	}
	sort.Strings(pks)
	slot := perfStart
	for _, k := range pks {
		for range r.BlockCounts[k] {
			blocks = append(blocks, ledger.RewardBlockChange{
				Slot: slot, Epoch: epochs.Performance, Pool: []byte(k),
			})
			slot++
		}
	}
	// The set takes a bounded number of block changes in one batch.
	for len(blocks) > 0 {
		n := min(len(blocks), rewardBlockBatch)
		sl.SeedChanges(ledger.LedgerChanges{RewardBlocks: blocks[:n]})
		blocks = blocks[n:]
	}
	return nil
}

func readNewEpoch(
	ctx context.Context, ls *ledger.LedgerState, in NewEpochInput,
	before [][]byte, out NewEpochOutput,
) (NewEpochOutput, error) {
	ldb := ls.LedgerDB()
	for _, a := range in.Accounts {
		row, err := ldb.GetAccountByCredential(ctx, a.Tag, a.Key, true, nil)
		if err != nil || row == nil {
			return out, fmt.Errorf("dxoracle: read account: %v", err)
		}
		// A reward round credits through a pending round the account row has
		// not absorbed yet; the balance is the row plus that credit.
		pending, err := ls.PendingRewardCredit(nil, a.Tag, a.Key)
		if err != nil {
			return out, fmt.Errorf("dxoracle: pending reward credit: %w", err)
		}
		acct := NewEpochAccount{Tag: a.Tag, Key: a.Key, Reward: uint64(row.Reward) + pending}
		if row.Active {
			acct.Pool = row.Pool
		}
		if acct.Drep, err = delegationOf(row); err != nil {
			return out, err
		}
		out.Accounts = append(out.Accounts, acct)
	}
	after, err := ldb.GetActivePoolKeyHashes(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("dxoracle: pools after: %w", err)
	}
	live := map[string]bool{}
	for _, p := range after {
		live[string(p)] = true
	}
	for _, p := range before {
		if !live[string(p)] {
			out.PoolsRemoved = append(out.PoolsRemoved, p)
		}
	}
	sort.Slice(out.PoolsRemoved, func(i, j int) bool { return bytes.Compare(out.PoolsRemoved[i], out.PoolsRemoved[j]) < 0 })
	state, err := ldb.GetNetworkState(nil)
	if err != nil || state == nil {
		return out, fmt.Errorf("dxoracle: network state: %v", err)
	}
	out.Treasury, out.Reserves = uint64(state.Treasury), uint64(state.Reserves)
	setEpoch, goEpoch := snapshot.ComputeSnapshotRotationEpochs(out.Epoch)
	for _, part := range []struct {
		epoch uint64
		dst   *[]SnapshotStake
	}{{out.Epoch, &out.Mark}, {setEpoch, &out.Set}, {goEpoch, &out.Go}} {
		rows, err := ldb.GetRewardStakeInputs(part.epoch, nil)
		if err != nil {
			return out, fmt.Errorf("dxoracle: read epoch %d stake: %w", part.epoch, err)
		}
		for _, r := range rows {
			*part.dst = append(*part.dst, SnapshotStake{
				Tag: r.CredentialTag, Key: append([]byte(nil), r.StakingKey...),
				Amount: uint64(r.Stake), Pool: append([]byte(nil), r.PoolKeyHash...),
			})
		}
		sort.Slice(*part.dst, func(i, j int) bool {
			a, b := (*part.dst)[i], (*part.dst)[j]
			if a.Tag != b.Tag {
				return a.Tag < b.Tag
			}
			return bytes.Compare(a.Key, b.Key) < 0
		})
	}
	return out, nil
}
