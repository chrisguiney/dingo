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
	"bytes"
	"errors"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/hardfork"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"math"
	"math/big"
	"testing"
	"time"
)

func pureLogicByronAddrs(t *testing.T) (redeem, pubkey lcommon.Address) {
	t.Helper()
	redeem, err := lcommon.NewByronAddressRedeem(
		bytes.Repeat([]byte{0x42}, 32),
		lcommon.ByronAddressAttributes{},
	)
	require.NoError(t, err)
	pubkey, err = lcommon.NewByronAddressFromParts(
		lcommon.ByronAddressTypePubkey,
		bytes.Repeat([]byte{0x55}, lcommon.AddressHashSize),
		lcommon.ByronAddressAttributes{},
	)
	require.NoError(t, err)
	return redeem, pubkey
}

func TestClassifyAvvmOutput(t *testing.T) {
	t.Parallel()
	redeem, pubkey := pureLogicByronAddrs(t)

	isAvvm, lovelace, err := ClassifyAvvmOutput(redeem, big.NewInt(25))
	require.NoError(t, err)
	assert.True(t, isAvvm)
	assert.Equal(t, uint64(25), lovelace)

	isAvvm, lovelace, err = ClassifyAvvmOutput(pubkey, big.NewInt(25))
	require.NoError(t, err)
	assert.False(t, isAvvm)
	assert.Equal(t, uint64(0), lovelace)

	_, _, err = ClassifyAvvmOutput(redeem, big.NewInt(-1))
	require.Error(t, err)
	_, _, err = ClassifyAvvmOutput(redeem, nil)
	require.Error(t, err)
	// A non-AVVM output is ignored whatever its amount.
	isAvvm, _, err = ClassifyAvvmOutput(pubkey, big.NewInt(-1))
	require.NoError(t, err)
	assert.False(t, isAvvm)
}

func TestComputeAvvmReturn(t *testing.T) {
	t.Parallel()
	redeem, pubkey := pureLogicByronAddrs(t)
	removed, total, reserves, err := ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: big.NewInt(10)},
		{Address: pubkey, Amount: big.NewInt(99)},
		{Address: redeem, Amount: big.NewInt(5)},
	}, 1000)
	require.NoError(t, err)
	assert.Equal(t, []int{0, 2}, removed)
	assert.Equal(t, uint64(15), total)
	assert.Equal(t, uint64(1015), reserves)

	// Nothing to return leaves reserves alone.
	removed, total, reserves, err = ComputeAvvmReturn(
		[]AvvmOutput{{Address: pubkey, Amount: big.NewInt(1)}}, 7,
	)
	require.NoError(t, err)
	assert.Empty(t, removed)
	assert.Equal(t, uint64(0), total)
	assert.Equal(t, uint64(7), reserves)
}

func TestComputeAvvmReturnOverflow(t *testing.T) {
	t.Parallel()
	redeem, _ := pureLogicByronAddrs(t)
	_, _, _, err := ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: new(big.Int).SetUint64(math.MaxUint64)},
		{Address: redeem, Amount: big.NewInt(1)},
	}, 0)
	require.ErrorContains(t, err, "AVVM lovelace total overflows uint64")

	_, _, _, err = ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: big.NewInt(2)},
	}, math.MaxUint64-1)
	require.ErrorContains(t, err, "AVVM reserve credit overflows uint64")
}

var errTestShape = errors.New("shape unavailable")

// --- praos / tpraos / time / byron pure entry points ---

func praosTestNonce(b byte) []byte {
	return bytes.Repeat([]byte{b}, 32)
}

func TestFoldCandidateNonce(t *testing.T) {
	t.Parallel()
	evolving0 := praosTestNonce(0x11)
	candidate0 := praosTestNonce(0x22)
	blocks := []NonceFoldBlock{
		{Slot: 10, VrfNonce: praosTestNonce(0xa1)},
		{Slot: 20, VrfNonce: praosTestNonce(0xa2)},
		{Slot: 30, VrfNonce: praosTestNonce(0xa3)},
	}
	// Oracle: the gouroboros rolling-nonce operator applied block by block.
	e1, err := lcommon.CalculateRollingNonce(evolving0, blocks[0].VrfNonce)
	require.NoError(t, err)
	e2, err := lcommon.CalculateRollingNonce(e1.Bytes(), blocks[1].VrfNonce)
	require.NoError(t, err)
	e3, err := lcommon.CalculateRollingNonce(e2.Bytes(), blocks[2].VrfNonce)
	require.NoError(t, err)

	// A block at the cutoff slot does not move the candidate.
	evolving, candidate, err := FoldCandidateNonce(
		evolving0, candidate0, 30, blocks,
	)
	require.NoError(t, err)
	require.Equal(t, e3.Bytes(), evolving)
	require.Equal(t, e2.Bytes(), candidate)

	// Cutoff beyond every block: candidate tracks evolving.
	evolving, candidate, err = FoldCandidateNonce(
		evolving0, candidate0, 31, blocks,
	)
	require.NoError(t, err)
	require.Equal(t, e3.Bytes(), evolving)
	require.Equal(t, e3.Bytes(), candidate)

	// Cutoff before every block: candidate stays at the carried value.
	evolving, candidate, err = FoldCandidateNonce(
		evolving0, candidate0, 10, blocks,
	)
	require.NoError(t, err)
	require.Equal(t, e3.Bytes(), evolving)
	require.Equal(t, candidate0, candidate)

	// No blocks: both carried through unchanged.
	evolving, candidate, err = FoldCandidateNonce(
		evolving0, candidate0, 100, nil,
	)
	require.NoError(t, err)
	require.Equal(t, evolving0, evolving)
	require.Equal(t, candidate0, candidate)

	_, _, err = FoldCandidateNonce(
		evolving0, candidate0, 100,
		[]NonceFoldBlock{{Slot: 1, VrfNonce: []byte{1}}},
	)
	require.Error(t, err)
}

func TestAdvanceCandidateNonce(t *testing.T) {
	t.Parallel()
	evolving := praosTestNonce(0x01)
	candidate := praosTestNonce(0x02)
	require.Equal(t, evolving, AdvanceCandidateNonce(evolving, candidate, 9, 10))
	require.Equal(t, candidate, AdvanceCandidateNonce(evolving, candidate, 10, 10))
	require.Equal(t, candidate, AdvanceCandidateNonce(evolving, candidate, 11, 10))
}

func TestComputeNonceStabilityWindow(t *testing.T) {
	t.Parallel()
	f := big.NewRat(1, 20)
	// Shelley..Babbage use 3k/f; Conway and later use 4k/f.
	require.Equal(t, uint64(129600), ComputeNonceStabilityWindow(1, 2160, f))
	require.Equal(t, uint64(129600), ComputeNonceStabilityWindow(5, 2160, f))
	require.Equal(t, uint64(172800), ComputeNonceStabilityWindow(6, 2160, f))
	// Ceiling: 3*1/(2/5) = 7.5 -> 8.
	require.Equal(t, uint64(8), ComputeNonceStabilityWindow(1, 1, big.NewRat(2, 5)))
	// Byron, zero k, and missing coefficient have no window.
	require.Zero(t, ComputeNonceStabilityWindow(0, 2160, f))
	require.Zero(t, ComputeNonceStabilityWindow(6, 0, f))
	require.Zero(t, ComputeNonceStabilityWindow(6, 2160, nil))
	require.Zero(t, ComputeNonceStabilityWindow(6, 2160, big.NewRat(0, 1)))
}

func TestCheckOpCertCounter(t *testing.T) {
	t.Parallel()
	const shelley, babbage = 1, 5
	// Same counter is accepted and stays.
	res := CheckOpCertCounter(7, true, 7, babbage)
	require.True(t, res.Ok)
	require.Equal(t, uint64(7), res.NewCounter)
	require.NoError(t, res.Err)
	// Increment by one.
	res = CheckOpCertCounter(7, true, 8, babbage)
	require.True(t, res.Ok)
	require.Equal(t, uint64(8), res.NewCounter)
	// Stale counter rejected in every era.
	for _, era := range []uint8{shelley, babbage} {
		res = CheckOpCertCounter(7, true, 6, era)
		require.False(t, res.Ok)
		require.ErrorContains(t, res.Err, "below last seen")
		require.Equal(t, uint64(7), res.NewCounter)
	}
	// Gap rejected under Praos only.
	res = CheckOpCertCounter(7, true, 9, babbage)
	require.False(t, res.Ok)
	require.ErrorContains(t, res.Err, "skips ahead")
	res = CheckOpCertCounter(7, true, 9, shelley)
	require.True(t, res.Ok)
	require.Equal(t, uint64(9), res.NewCounter)
	// No recorded counter: zero baseline.
	res = CheckOpCertCounter(99, false, 1, babbage)
	require.True(t, res.Ok)
	res = CheckOpCertCounter(99, false, 2, babbage)
	require.False(t, res.Ok)
}

func TestCheckBlockSizeLimits(t *testing.T) {
	t.Parallel()
	require.NoError(t, CheckBlockSizeLimits(100, 200, 100, 200))
	require.ErrorContains(
		t, CheckBlockSizeLimits(100, 200, 101, 10),
		"block header size 101 exceeds maxBlockHeaderSize 100",
	)
	require.ErrorContains(
		t, CheckBlockSizeLimits(100, 200, 10, 201),
		"block body size 201 exceeds maxBlockBodySize 200",
	)
	// Header is checked first.
	require.ErrorContains(
		t, CheckBlockSizeLimits(100, 200, 101, 201), "header size",
	)
}

func TestCheckRegisteredVrfKey(t *testing.T) {
	t.Parallel()
	vrfKey := praosTestNonce(0x55)
	registered := lcommon.Blake2b256Hash(vrfKey)
	pool := bytes.Repeat([]byte{0x09}, 28)
	require.NoError(t, CheckRegisteredVrfKey(5, pool, vrfKey, registered.Bytes()))
	other := lcommon.Blake2b256Hash(praosTestNonce(0x56))
	err := CheckRegisteredVrfKey(5, pool, vrfKey, other.Bytes())
	require.ErrorContains(t, err, "does not match registered VRF key")
}

func TestClassifyGenesisOverlaySlotExported(t *testing.T) {
	t.Parallel()
	f := big.NewRat(1, 20)
	// d = 1: every slot is overlay; every 20th is active, rotating over keys.
	idx, status := ClassifyGenesisOverlaySlot(0, big.NewRat(1, 1), f, 3)
	require.Equal(t, GenesisOverlayActive, status)
	require.Zero(t, idx)
	_, status = ClassifyGenesisOverlaySlot(1, big.NewRat(1, 1), f, 3)
	require.Equal(t, GenesisOverlayNonActive, status)
	idx, status = ClassifyGenesisOverlaySlot(40, big.NewRat(1, 1), f, 3)
	require.Equal(t, GenesisOverlayActive, status)
	require.Equal(t, uint64(2), idx)
	idx, status = ClassifyGenesisOverlaySlot(60, big.NewRat(1, 1), f, 3)
	require.Equal(t, GenesisOverlayActive, status)
	require.Zero(t, idx)
	// d = 1/2: slot 0 is overlay, slot 1 is not.
	_, status = ClassifyGenesisOverlaySlot(0, big.NewRat(1, 2), f, 3)
	require.Equal(t, GenesisOverlayActive, status)
	_, status = ClassifyGenesisOverlaySlot(1, big.NewRat(1, 2), f, 3)
	require.Equal(t, GenesisOverlayNone, status)
	// d = 0, nil inputs, and no keys disable the overlay.
	_, status = ClassifyGenesisOverlaySlot(0, big.NewRat(0, 1), f, 3)
	require.Equal(t, GenesisOverlayNone, status)
	_, status = ClassifyGenesisOverlaySlot(0, nil, f, 3)
	require.Equal(t, GenesisOverlayNone, status)
	_, status = ClassifyGenesisOverlaySlot(0, big.NewRat(1, 1), f, 0)
	require.Equal(t, GenesisOverlayNone, status)
}

func TestBuildHardForkSummaryFromEpochRows(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_000_000, 0).UTC()
	params := hardfork.EraParams{
		EpochSize:     100,
		SlotLength:    time.Second,
		SafeZoneSlots: 40,
	}
	shape := hardfork.Shape{
		SystemStart: start,
		Eras: []hardfork.ShapeEntry{{
			EraID:           1,
			EraName:         "Shelley",
			MinMajorVersion: 2,
			MaxMajorVersion: 2,
			Params:          params,
			NextEraTrigger:  hardfork.TriggerHardFork{Kind: hardfork.TriggerNotDuringThisExecution},
		}},
	}
	rows := []models.Epoch{
		{EpochId: 0, EraId: 1, StartSlot: 0, LengthInSlots: 100, SlotLength: 1000},
		{EpochId: 1, EraId: 1, StartSlot: 100, LengthInSlots: 100, SlotLength: 1000},
	}
	summary, err := BuildHardForkSummaryFromEpochRows(HardForkSummaryInput{
		SystemStart: start,
		EpochRows:   rows,
		Shape:       shape,
		TipSlot:     150,
	})
	require.NoError(t, err)
	require.Len(t, summary.Eras, 1)
	require.Equal(t, uint(1), summary.Eras[0].EraID)
	require.Equal(t, uint64(40), summary.Eras[0].Params.SafeZoneSlots)
	epoch, err := summary.SlotToEpoch(150)
	require.NoError(t, err)
	require.Equal(t, uint64(1), epoch.Epoch)

	_, err = BuildHardForkSummaryFromEpochRows(HardForkSummaryInput{
		Shape: shape,
	})
	require.ErrorContains(t, err, "no epochs in cache")

	_, err = BuildHardForkSummaryFromEpochRows(HardForkSummaryInput{
		EpochRows: rows,
		ShapeErr:  errTestShape,
	})
	require.ErrorContains(t, err, "hard-fork forecast unavailable")
}

func TestAdvanceByronPBFTStateExported(t *testing.T) {
	t.Parallel()
	stored := loadRealByronMainBlock(t)
	block, err := stored.Decode()
	require.NoError(t, err)
	const securityParam = 10
	ls := &LedgerState{
		config: LedgerStateConfig{
			CardanoNodeConfig: newByronPBFTTestNodeConfig(
				t, block, securityParam,
			),
		},
	}
	config, err := ls.byronPBFTConfig()
	require.NoError(t, err)
	state, err := NewByronPBFTState(config, nil)
	require.NoError(t, err)

	// The signature-window rule fires on the third block by one issuer.
	for range 2 {
		var outcome ByronPBFTAdvanceOutcome
		state, outcome, err = AdvanceByronPBFTState(config, state, block, true)
		require.NoError(t, err)
		require.NoError(t, outcome.SkippedUpdateErr)
	}
	require.Len(t, state.IssuerState().SignatureHistory(), 2)
	_, _, err = AdvanceByronPBFTState(config, state, block, true)
	require.ErrorContains(t, err, "signature threshold")

	// A trusted advance observes the issuer without enforcing the window.
	trusted, _, err := AdvanceByronPBFTState(config, state, block, false)
	require.NoError(t, err)
	require.Len(t, trusted.IssuerState().SignatureHistory(), 3)
}

func TestCandidateFreezeCutoff(t *testing.T) {
	t.Parallel()
	// Blocks below the cutoff move the candidate; the cutoff is the epoch end
	// less the window, or the epoch start once the window covers the epoch.
	require.Equal(t, uint64(1_000), CandidateFreezeCutoff(0, 2_000, 1_000))
	require.Equal(t, uint64(500_300), CandidateFreezeCutoff(500_000, 501_000, 700))
	require.Equal(t, uint64(500_000), CandidateFreezeCutoff(500_000, 501_000, 1_000))
	require.Equal(t, uint64(500_000), CandidateFreezeCutoff(500_000, 501_000, 5_000))
	require.Equal(t, uint64(2_000), CandidateFreezeCutoff(0, 2_000, 0))
}
