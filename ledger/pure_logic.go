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
	"fmt"
	"math/big"
	"time"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/byronupdate"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/dingo/ledger/hardfork"
	byronconsensus "github.com/blinklabs-io/gouroboros/consensus/byron"
	"github.com/blinklabs-io/gouroboros/ledger/byron"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// Pure praos, tpraos, time and byron entry points. Everything below takes
// values and returns values: no database, transaction, clock, logger or
// global state.

// NonceFoldBlock is one block's contribution to the evolving nonce.
type NonceFoldBlock struct {
	// Slot is the block's absolute slot.
	Slot uint64
	// VrfNonce is the value mixed into the evolving nonce: the raw VRF
	// output for TPraos eras, and the double-hashed VRF nonce value for
	// Praos eras.
	VrfNonce []byte
}

// AdvanceCandidateNonce applies the candidate-freeze rule after a block has
// been folded into the evolving nonce. The candidate follows the evolving
// nonce for a block below the cutoff slot and is carried unchanged at or
// beyond it. The result never aliases its inputs.
func AdvanceCandidateNonce(
	evolving, candidate []byte,
	slot, cutoffSlot uint64,
) []byte {
	if slot < cutoffSlot {
		return bytes.Clone(evolving)
	}
	return bytes.Clone(candidate)
}

// FoldCandidateNonce folds blocks, in slot order, into the evolving nonce and
// tracks the candidate nonce, which freezes at cutoffSlot. It returns the new
// evolving and candidate nonces. Both must be 32 bytes whenever blocks is
// non-empty.
func FoldCandidateNonce(
	evolving, candidate []byte,
	cutoffSlot uint64,
	blocks []NonceFoldBlock,
) ([]byte, []byte, error) {
	evolving = bytes.Clone(evolving)
	candidate = bytes.Clone(candidate)
	for _, block := range blocks {
		next, err := lcommon.CalculateRollingNonce(evolving, block.VrfNonce)
		if err != nil {
			return nil, nil, fmt.Errorf(
				"fold nonce at slot %d: %w", block.Slot, err,
			)
		}
		evolving = next.Bytes()
		candidate = AdvanceCandidateNonce(
			evolving, candidate, block.Slot, cutoffSlot,
		)
	}
	return evolving, candidate, nil
}

// ComputeNonceStabilityWindow returns the randomness stabilisation window in
// slots, ceiling(m*k/f), for an era. The multiplier m is 3 from Shelley
// through Babbage and 4 from Conway; Babbage keeps 3 for compatibility with
// the TPraos eras. It returns 0 for Byron, an unknown era, a non-positive k,
// or a missing or non-positive active slot coefficient.
func ComputeNonceStabilityWindow(
	eraId uint,
	securityParam uint64,
	activeSlotsCoeff *big.Rat,
) uint64 {
	if securityParam == 0 ||
		activeSlotsCoeff == nil ||
		activeSlotsCoeff.Num().Sign() <= 0 {
		return 0
	}
	multiplier, ok := nonceStabilityWindowKMultiplier(eraId)
	if !ok {
		return 0
	}
	numerator := new(big.Int).SetUint64(securityParam)
	numerator.Mul(numerator, big.NewInt(multiplier))
	numerator.Mul(numerator, activeSlotsCoeff.Denom())
	denominator := new(big.Int).Set(activeSlotsCoeff.Num())
	result, remainder := new(big.Int).QuoRem(
		numerator, denominator, new(big.Int),
	)
	if remainder.Sign() != 0 {
		result.Add(result, big.NewInt(1))
	}
	if !result.IsUint64() {
		return 0
	}
	return result.Uint64()
}

// OpCertCounterResult is the outcome of CheckOpCertCounter.
type OpCertCounterResult struct {
	// Ok reports whether the header counter is acceptable.
	Ok bool
	// NewCounter is the counter to record for the issuer: the header counter
	// when Ok, otherwise the baseline that was compared against.
	NewCounter uint64
	// Err describes the rejection when Ok is false.
	Err error
}

// CheckOpCertCounter applies the operational-certificate counter rules for an
// issuer. lastCounter is the issuer's last recorded counter and found reports
// whether one is recorded; when it is not, zero is the baseline. A counter
// below the baseline is rejected in every era; one that skips ahead by more
// than one is rejected only from Babbage onward (the Praos no-gap rule).
func CheckOpCertCounter(
	lastCounter uint64,
	found bool,
	headerCounter uint64,
	eraId uint8,
) OpCertCounterResult {
	baseline := lastCounter
	if !found {
		baseline = 0
	}
	err := validateOpCertCounter(
		lastCounter,
		found,
		headerCounter,
		opCertNoGapRuleApplies(eraId),
	)
	if err != nil {
		return OpCertCounterResult{NewCounter: baseline, Err: err}
	}
	return OpCertCounterResult{Ok: true, NewCounter: headerCounter}
}

// CheckHeaderSizeLimit rejects a block header whose serialized size exceeds
// maxBlockHeaderSize.
func CheckHeaderSizeLimit(size, maxHeaderSize uint64) error {
	if size > maxHeaderSize {
		return fmt.Errorf(
			"block header size %d exceeds maxBlockHeaderSize %d",
			size,
			maxHeaderSize,
		)
	}
	return nil
}

// CheckBodySizeLimit rejects a block body whose size exceeds
// maxBlockBodySize.
func CheckBodySizeLimit(size, maxBodySize uint64) error {
	if size > maxBodySize {
		return fmt.Errorf(
			"block body size %d exceeds maxBlockBodySize %d",
			size,
			maxBodySize,
		)
	}
	return nil
}

// CheckBlockSizeLimits applies the header and body size envelope checks,
// header first.
func CheckBlockSizeLimits(
	maxHeaderSize, maxBodySize, headerSize, bodySize uint64,
) error {
	if err := CheckHeaderSizeLimit(headerSize, maxHeaderSize); err != nil {
		return err
	}
	return CheckBodySizeLimit(bodySize, maxBodySize)
}

// CheckRegisteredVrfKey rejects a block whose header VRF key does not hash to
// the VRF key hash registered for the producing pool.
func CheckRegisteredVrfKey(
	slot uint64,
	poolKeyHash []byte,
	headerVrfKey []byte,
	registeredVrfKeyHash []byte,
) error {
	headerVrfKeyHash := lcommon.Blake2b256Hash(headerVrfKey)
	if !bytes.Equal(registeredVrfKeyHash, headerVrfKeyHash.Bytes()) {
		return fmt.Errorf(
			"block header verification rejected at slot %d: "+
				"producer pool %x VRF key does not match registered VRF key "+
				"(header %x, registered %x)",
			slot,
			poolKeyHash,
			headerVrfKeyHash.Bytes(),
			registeredVrfKeyHash,
		)
	}
	return nil
}

// GenesisOverlaySlotStatus classifies a slot under the TPraos overlay
// schedule.
type GenesisOverlaySlotStatus = genesisOverlaySlotStatus

// Overlay slot classifications.
const (
	// GenesisOverlayNone marks a slot outside the overlay schedule, or any
	// slot when the schedule is disabled.
	GenesisOverlayNone = genesisOverlayNone
	// GenesisOverlayNonActive marks an overlay slot nobody may produce.
	GenesisOverlayNonActive = genesisOverlayNonActive
	// GenesisOverlayActive marks an overlay slot a genesis delegate produces.
	GenesisOverlayActive = genesisOverlayActive
)

// ClassifyGenesisOverlaySlot classifies a slot, counted from the first slot
// of its epoch, under the TPraos overlay schedule. For an active slot it also
// returns the index of the producing genesis key. The schedule is disabled
// (GenesisOverlayNone) when decentralization or activeSlotsCoeff is outside
// (0, 1] or there are no genesis keys.
func ClassifyGenesisOverlaySlot(
	relativeSlot uint64,
	decentralization *big.Rat,
	activeSlotsCoeff *big.Rat,
	genesisKeyCount uint64,
) (uint64, GenesisOverlaySlotStatus) {
	if decentralization == nil ||
		decentralization.Sign() <= 0 ||
		decentralization.Cmp(big.NewRat(1, 1)) > 0 ||
		activeSlotsCoeff == nil ||
		activeSlotsCoeff.Sign() <= 0 ||
		activeSlotsCoeff.Cmp(big.NewRat(1, 1)) > 0 ||
		genesisKeyCount == 0 {
		return 0, genesisOverlayNone
	}
	position := ceilUint64Rat(relativeSlot, decentralization)
	nextPosition := ceilUint64Rat(relativeSlot+1, decentralization)
	if position >= nextPosition {
		return 0, genesisOverlayNone
	}
	activeSlotCoeffInv := activeSlotCoeffInverse(activeSlotsCoeff)
	if activeSlotCoeffInv == 0 || position%activeSlotCoeffInv != 0 {
		return 0, genesisOverlayNonActive
	}
	return (position / activeSlotCoeffInv) % genesisKeyCount,
		genesisOverlayActive
}

// HardForkSummaryInput is the state BuildHardForkSummaryFromEpochRows derives
// a hardfork.Summary from.
type HardForkSummaryInput struct {
	// SystemStart is the chain's genesis time. Shape.SystemStart overrides it
	// when non-zero and the current era is in the shape.
	SystemStart time.Time
	// EpochRows are the cached epochs in ascending epoch order. Rows are
	// sparse across an empty epoch.
	EpochRows []models.Epoch
	// Shape is the static era table; ShapeErr is set when it could not be
	// built, which makes the forecast unavailable.
	Shape    hardfork.Shape
	ShapeErr error
	// CurrentEraName names the current era in the unavailable-shape error.
	CurrentEraName string
	// Transition is the ledger's pending hard-fork transition state.
	Transition hardfork.TransitionInfo
	// TipSlot anchors the forecast horizon.
	TipSlot uint64
}

// BuildHardForkSummaryFromEpochRows groups the epoch rows by era into closed
// past eras and an open current era, takes the current era's safe zone from
// the shape, and hands both to hardfork.BuildSummary. When a transition is
// known it appends the successor era so the horizon covers the first epoch
// after the boundary.
func BuildHardForkSummaryFromEpochRows(
	in HardForkSummaryInput,
) (*hardfork.Summary, error) {
	systemStart := in.SystemStart
	cache := in.EpochRows
	transitionInfo := in.Transition
	shape, shapeErr := in.Shape, in.ShapeErr
	tipSlot := in.TipSlot

	if len(cache) == 0 {
		return nil, errors.New("ledger: no epochs in cache")
	}

	// Walk the epoch cache grouping contiguous epochs by EraId. Each group
	// becomes one EraSummary. A group's Start is the bound of its era's first
	// epoch, which is the first cached row only when no empty epoch precedes
	// it: rows are sparse across an empty epoch, so a boundary between eras
	// is resolved by eraTransitionEpoch and the epochs between rows are
	// counted at their era's geometry, keeping every End equal to the next
	// era's Start as the hard-fork combinator's summary does.
	eraSummaries := make([]hardfork.EraSummary, 0, len(cache))
	relTime := time.Duration(0)

	i := 0
	for i < len(cache) {
		first := cache[i]
		eraID := first.EraId
		// Per-epoch params within an era are expected to be constant; we use
		// the first epoch's values as the era-level params.
		eraParams, err := hardForkCachedEraParams(
			first.LengthInSlots,
			first.SlotLength,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"ledger: cached epoch %d (era %d) params invalid: %w",
				first.EpochId,
				eraID,
				err,
			)
		}
		slotLen := eraParams.SlotLength
		epochSize := eraParams.EpochSize

		start := hardfork.Bound{
			RelativeTime: relTime,
			Slot:         first.StartSlot,
			Epoch:        first.EpochId,
		}
		// nextEpoch is the epoch the next row would occupy if no epoch were
		// empty.
		nextEpoch := first.EpochId
		if i > 0 {
			prev := cache[i-1]
			transition, err := eraTransitionEpoch(
				shape, shapeErr, cache[:i], first,
			)
			if err != nil {
				return nil, err
			}
			// Old-era epochs before the transition, from the previous row.
			oldEpochs := transition - prev.EpochId - 1
			oldDuration, err := hardForkCachedEpochDuration(
				prev.LengthInSlots, prev.SlotLength,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"ledger: cached epoch %d (era %d) params invalid: %w",
					prev.EpochId, prev.EraId, err,
				)
			}
			relTime, err = addEpochDurations(
				relTime, oldDuration, oldEpochs, prev.EpochId,
			)
			if err != nil {
				return nil, err
			}
			start = hardfork.Bound{
				RelativeTime: relTime,
				Slot: prev.StartSlot +
					(transition-prev.EpochId)*uint64(prev.LengthInSlots),
				Epoch: transition,
			}
			nextEpoch = transition
			// The previous group, already appended, ends at this start.
			end := start
			eraSummaries[len(eraSummaries)-1].End = &end
		}

		// Advance through all contiguous epochs with the same EraId,
		// accumulating relTime and counting epochs absent between rows.
		j := i
		for j < len(cache) && cache[j].EraId == eraID {
			ep := cache[j]
			epochDuration, err := hardForkCachedEpochDuration(
				ep.LengthInSlots,
				ep.SlotLength,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"ledger: cached epoch %d (era %d) params invalid: %w",
					ep.EpochId,
					eraID,
					err,
				)
			}
			if j > 0 && ep.EpochId > nextEpoch {
				relTime, err = addEpochDurations(
					relTime, epochDuration, ep.EpochId-nextEpoch, ep.EpochId,
				)
				if err != nil {
					return nil, err
				}
			}
			if epochDuration > maxHardForkDuration-relTime {
				return nil, fmt.Errorf(
					"ledger: cumulative cached epoch duration overflows time.Duration at epoch %d (era %d)",
					ep.EpochId,
					eraID,
				)
			}
			relTime += epochDuration
			nextEpoch = ep.EpochId + 1
			j++
		}

		eraSummaries = append(eraSummaries, hardfork.EraSummary{
			EraID: eraID,
			Start: start,
			Params: hardfork.EraParams{
				EpochSize:     epochSize,
				SlotLength:    slotLen,
				SafeZoneSlots: 0,
				GenesisWindow: 0,
			},
		})
		i = j
	}

	current := eraSummaries[len(eraSummaries)-1]
	past := eraSummaries[:len(eraSummaries)-1]

	// Use the same configured safe-zone source as the NtC era-history query.
	// A missing shape or current-era entry cannot safely supply a forecast:
	// the cache-derived current era has a zero safe zone and no end bound.
	if shapeErr != nil {
		return nil, fmt.Errorf(
			"ledger: hard-fork forecast unavailable: %w",
			shapeErr,
		)
	}
	shapeEntry, shapeAvailable := shape.EraForID(current.EraID)
	if shapeAvailable {
		current.Params.SafeZoneSlots = shapeEntry.Params.SafeZoneSlots
		current.Params.GenesisWindow = shapeEntry.Params.GenesisWindow
		if !shape.SystemStart.IsZero() {
			systemStart = shape.SystemStart
		}
	}
	if !shapeAvailable {
		eraName := in.CurrentEraName
		if eraName == "" {
			eraName = fmt.Sprintf("ID %d", current.EraID)
		}
		return nil, fmt.Errorf(
			"ledger: hard-fork forecast unavailable: %s era is unavailable "+
				"in the hard-fork shape",
			eraName,
		)
	}

	summary, err := hardfork.BuildSummary(
		hardfork.Shape{SystemStart: systemStart},
		past,
		current,
		tipSlot,
		transitionInfo,
	)
	if err != nil {
		return nil, err
	}
	summary.Transition = transitionInfo

	// When a known transition is armed, BuildSummary bounds the current era at
	// the announced epoch boundary (mkUpperBound) and appends no successor era.
	// That leaves the summary's last era bounded, so eraForSlot / SlotToEpoch
	// return ErrPastHorizon for every slot at or past the boundary. The header
	// forecast-horizon gate in verify_header.go then hard-rejects the first
	// header of the post-boundary epoch, and the node can never apply the block
	// that would consume the transition and extend era history — a liveness
	// deadlock at the boundary. Unlike the NtC era-history query, which serves a
	// point-in-time answer and is intentionally bounded, live header
	// verification must see the horizon extend at least one epoch past a known
	// transition, because the rollover is deterministic within the stability
	// window. Append the successor era starting at the announced boundary so the
	// horizon covers the first post-boundary epoch. hardfork.SuccessorEra bounds
	// it by the successor's own safe zone measured from max(tipSlot+1,
	// boundary) -- the same tipSlot BuildSummary used for the bounded era
	// above -- which always snaps up to at least the next epoch boundary; the
	// successor stays open only when the resolved safe zone is zero
	// (UnsafeIndefiniteSafeZone), the same rule BuildSummary applies to the
	// current era. Passing tipSlot here, rather than measuring only from the
	// boundary, is what keeps the successor's horizon rolling forward with the
	// live tip once the boundary is behind it instead of freezing at
	// boundary+safeZone. Take the successor by shape order rather than EraID+1 so
	// non-contiguous EraID values still resolve; when the current era is the
	// last modeled era (the transition re-arms an era the ledger already
	// occupies), reuse the current era's params — epoch length and slot length
	// are constant across post-Byron eras, which is all SlotToEpoch needs.
	// Mirrors the successor-era recursion in Haskell HFC reconstructSummary.
	if transitionInfo.State == hardfork.TransitionKnown &&
		len(summary.Eras) > 0 {
		bounded := summary.Eras[len(summary.Eras)-1]
		if bounded.End != nil {
			succEraID := bounded.EraID
			succParams := bounded.Params
			if idx, ok := shape.EraIndex(bounded.EraID); ok &&
				idx+1 < len(shape.Eras) {
				next := shape.Eras[idx+1]
				succEraID = next.EraID
				succParams = next.Params
			}
			summary.Eras = append(summary.Eras, hardfork.SuccessorEra(
				*bounded.End,
				succEraID,
				hardfork.EraParams{
					EpochSize:     succParams.EpochSize,
					SlotLength:    succParams.SlotLength,
					SafeZoneSlots: succParams.SafeZoneSlots,
					GenesisWindow: succParams.GenesisWindow,
				},
				tipSlot,
			))
		}
	}
	return &summary, nil
}

// ByronPBFTState is the Byron chain state advanced block by block: the PBFT
// issuer signature window, the genesis delegation map and, when genesis
// protocol parameters were supplied, the update-system state.
type ByronPBFTState = byronPBFTState

// NewByronPBFTState returns the Byron chain state at genesis. genesisParams
// may be nil, in which case update payloads are not processed.
func NewByronPBFTState(
	config byronconsensus.ByronConfig,
	genesisParams *eras.ByronProtocolParameters,
) (ByronPBFTState, error) {
	return newByronPBFTState(config, genesisParams)
}

// IssuerState returns the PBFT issuer signature window.
func (s byronPBFTState) IssuerState() byronconsensus.PBFTState {
	return s.issuerState
}

// DelegationState returns the genesis delegation state.
func (s byronPBFTState) DelegationState() byronconsensus.PBFTDelegationState {
	return s.delegationState
}

// UpdateState returns the Byron update-system state.
func (s byronPBFTState) UpdateState() byronupdate.State {
	return s.update
}

// ByronPBFTAdvanceOutcome reports what AdvanceByronPBFTState did beyond
// returning the new state.
type ByronPBFTAdvanceOutcome struct {
	// SkippedUpdateErr is set when a trusted block's update payload could not
	// be applied and the update state was only advanced past the block.
	SkippedUpdateErr error
}

// ValidateByronPBFTIssuer checks a Byron main block header against the PBFT
// rules under the given delegation state and returns its issuer. An epoch
// boundary block has no issuer and yields the zero value. Signature and
// ledger-position checks are not included.
func ValidateByronPBFTIssuer(
	block lcommon.Block,
	config byronconsensus.ByronConfig,
	delegationState byronconsensus.PBFTDelegationState,
) (byronconsensus.PBFTIssuer, error) {
	if block.Type() == byron.BlockTypeByronEbb {
		return byronconsensus.PBFTIssuer{}, nil
	}
	header, ok := block.Header().(*byron.ByronMainBlockHeader)
	if !ok || header == nil {
		return byronconsensus.PBFTIssuer{}, fmt.Errorf(
			"byron main block at slot %d has unexpected header type %T",
			block.SlotNumber(),
			block.Header(),
		)
	}
	config.GenesisDelegations = delegationState.ActiveDelegations()
	issuer, err := byronconsensus.ValidatePBFTHeader(header, config)
	if err != nil {
		return byronconsensus.PBFTIssuer{}, fmt.Errorf(
			"byron PBFT header verification failed at slot %d: %w",
			block.SlotNumber(),
			err,
		)
	}
	return issuer, nil
}

// AdvanceByronPBFTState applies one Byron block to the chain state: it ticks
// delegation and update state to the block's slot, optionally validates the
// header and enforces the PBFT signature window (shouldValidate), then applies
// the block's delegation and update payloads. A trusted block
// (shouldValidate false) is observed without validation, and an update payload
// the partial update state cannot judge is reported in the outcome instead of
// failing.
func AdvanceByronPBFTState(
	config byronconsensus.ByronConfig,
	state ByronPBFTState,
	block lcommon.Block,
	shouldValidate bool,
) (ByronPBFTState, ByronPBFTAdvanceOutcome, error) {
	return advanceByronPBFTStateCore(config, state, block, shouldValidate, nil)
}
