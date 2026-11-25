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
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/config/cardano"
	"github.com/blinklabs-io/dingo/ledger/byronupdate"
	"github.com/blinklabs-io/dingo/ledger/eras"
	byronconsensus "github.com/blinklabs-io/gouroboros/consensus/byron"
	gledger "github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/byron"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/require"
)

// preprodByronApplyFixture is the preprod Byron chain with the configuration
// and genesis state ApplyByronBlock starts from.
func preprodByronApplyFixture(t *testing.T) (
	[]gledger.Block,
	byronconsensus.ByronConfig,
	ByronChainState,
) {
	t.Helper()
	blocks := loadPreprodByronChain(t)
	nodeConfig, err := cardano.NewCardanoNodeConfigFromEmbedFS(
		cardano.EmbeddedConfigFS,
		"preprod/config.json",
	)
	require.NoError(t, err)
	cache, err := newByronPBFTCache(LedgerStateConfig{CardanoNodeConfig: nodeConfig})
	require.NoError(t, err)
	require.NotNil(t, cache.config)
	params, err := eras.NewByronProtocolParametersFromGenesis(nodeConfig.ByronGenesis())
	require.NoError(t, err)
	pbft, err := NewByronPBFTState(*cache.config, params)
	require.NoError(t, err)
	return blocks, *cache.config, ByronChainState{PBFT: pbft}
}

func withByronAdoptedParams(
	t *testing.T,
	state ByronPBFTState,
	params *eras.ByronProtocolParameters,
) ByronPBFTState {
	t.Helper()
	parts := state.UpdateState().Parts()
	parts.AdoptedParams = params
	state.update = byronupdate.NewStateFromParts(parts)
	return state
}

func byronOutput(
	t *testing.T,
	address lcommon.Address,
	amount uint64,
) lcommon.TransactionOutput {
	t.Helper()
	return &byron.ByronTransactionOutput{OutputAddress: address, OutputAmount: amount}
}

// TestApplyByronBlockPreprodChain replays the whole preprod Byron chain
// through the composed block application, which cardano-node accepted, both
// with and without the PBFT header checks.
func TestApplyByronBlockPreprodChain(t *testing.T) {
	t.Parallel()
	for _, validateHeader := range []bool{false, true} {
		blocks, config, state := preprodByronApplyFixture(t)
		for _, block := range blocks {
			next, err := ApplyByronBlock(config, state, block, validateHeader)
			require.NoError(t, err, "slot %d, validateHeader %v", block.SlotNumber(), validateHeader)
			require.Equal(t, block.Hash().Bytes(), next.PrevHash)
			state = next
		}
		require.Equal(t, blocks[len(blocks)-1].Hash().Bytes(), state.PrevHash)
	}
}

// TestApplyByronBlockLinkage pins the previous-hash rule: a block must extend
// the recorded tip, and with no tip only a genesis epoch boundary block may
// open the chain.
func TestApplyByronBlockLinkage(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	state.PrevHash = blocks[0].Hash().Bytes()
	_, err := ApplyByronBlock(config, state, blocks[2], false)
	require.ErrorIs(t, err, errStaleChainIterator)

	state.PrevHash = nil
	_, err = ApplyByronBlock(config, state, blocks[0], false)
	require.NoError(t, err)
}

// TestApplyByronBlockProtocolMagic pins that a block carrying another
// network's protocol magic is rejected before anything else is read.
func TestApplyByronBlockProtocolMagic(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	state.PrevHash = blocks[0].Hash().Bytes()
	config.ProtocolMagic++
	_, err := ApplyByronBlock(config, state, blocks[1], false)
	var mismatch ByronProtocolMagicMismatchError
	require.ErrorAs(t, err, &mismatch)
	require.Equal(t, config.ProtocolMagic-1, mismatch.Block)
	require.Equal(t, config.ProtocolMagic, mismatch.Expected)
}

// TestApplyByronBlockSizeLimits pins the header and block size limits of the
// adopted parameters, which a ledger-only application still enforces.
func TestApplyByronBlockSizeLimits(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	state.PrevHash = blocks[0].Hash().Bytes()
	main := blocks[1]

	set := func(maxBlock, maxHeader int64) ByronChainState {
		params := state.PBFT.UpdateState().AdoptedParams()
		params.MaxBlockSize = big.NewInt(maxBlock)
		params.MaxHeaderSize = big.NewInt(maxHeader)
		next := state
		next.PBFT = withByronAdoptedParams(t, state.PBFT, params)
		return next
	}
	blockLen := int64(len(main.Cbor()))
	headerLen := int64(len(main.Header().Cbor()))

	_, err := ApplyByronBlock(config, set(blockLen, headerLen), main, false)
	require.NoError(t, err, "limits equal to the sizes are accepted")

	_, err = ApplyByronBlock(config, set(blockLen-1, headerLen), main, false)
	var tooLarge ByronBlockSizeError
	require.ErrorAs(t, err, &tooLarge)
	require.Equal(t, "block", tooLarge.What)

	_, err = ApplyByronBlock(config, set(blockLen, headerLen-1), main, false)
	require.ErrorAs(t, err, &tooLarge)
	require.Equal(t, "header", tooLarge.What)
}

// TestApplyByronBlockBodyProof pins that a body must match the proofs its
// header carries.
func TestApplyByronBlockBodyProof(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	state.PrevHash = blocks[1].Hash().Bytes()
	// Block 2 carries an update proposal and votes. Giving it the body of
	// block 4, which has none, keeps the wire bytes but leaves the header's
	// delegation, transaction and update proofs describing another body. Such
	// a block cannot decode from the wire, so it only arises when a body is
	// replaced after decoding.
	mainA, ok := blocks[2].(*byron.ByronMainBlock)
	require.True(t, ok)
	mainB, ok := blocks[4].(*byron.ByronMainBlock)
	require.True(t, ok)
	swapped := *mainA
	swapped.Body = mainB.Body

	_, err := ApplyByronBlock(config, state, blocks[2], false)
	require.NoError(t, err)
	_, err = ApplyByronBlock(config, state, &swapped, false)
	require.ErrorContains(t, err, "body proof")
}

// TestApplyByronBlockUtxo pins the transaction step: transactions apply in
// order against the UTxO as earlier ones in the block left it, and a failure
// names the transaction.
func TestApplyByronBlockUtxo(t *testing.T) {
	t.Parallel()
	const magic = 764824073
	keyA := newByronBlockTestKey(t, 0x71)
	keyB := newByronBlockTestKey(t, 0x72)
	keyC := newByronBlockTestKey(t, 0x73)
	seedId := make([]byte, 32)
	seedId[0] = 1

	txA := buildByronBlockTestTx(t, magic,
		[]byronBlockTestInput{{seedId, 0}},
		[]byronBlockTestOutput{{keyB.address, 900}},
		nil, []byronBlockTestKey{keyA})
	txB := buildByronBlockTestTx(t, magic,
		[]byronBlockTestInput{{txA.Hash().Bytes(), 0}},
		[]byronBlockTestOutput{{keyC.address, 800}},
		nil, []byronBlockTestKey{keyB})

	params := &eras.ByronProtocolParameters{
		MaxTxSize:           big.NewInt(4096),
		TxFeeMultiplierNano: big.NewInt(0),
	}
	utxo := ByronUtxo{}
	utxo.Add(lcommon.NewBlake2b256(seedId), 0, byronOutput(t, keyA.address, 1_000))

	next, err := applyByronTransactions(
		magic, params, utxo, 0, []lcommon.Transaction{txA, txB},
	)
	require.NoError(t, err)
	require.Len(t, next, 1, "the intermediate output was created and spent in the block")
	require.Contains(t, next, ByronUtxoKey{TxId: txB.Hash(), Index: 0})
	require.Len(t, utxo, 1, "the input UTxO is not modified")

	// Spending the same output twice fails the second transaction.
	_, err = applyByronTransactions(
		magic, params, utxo, 0, []lcommon.Transaction{txA, txA},
	)
	var txErr *ByronBlockTxError
	require.ErrorAs(t, err, &txErr)
	require.Equal(t, 1, txErr.Index)
}

// TestByronPBFTStateWithUpdateState pins that replacing the update state
// leaves the issuer window and delegation state alone.
func TestByronPBFTStateWithUpdateState(t *testing.T) {
	t.Parallel()
	_, _, state := preprodByronApplyFixture(t)
	parts := state.PBFT.UpdateState().Parts()
	parts.LastSlot = 77
	next := state.PBFT.WithUpdateState(byronupdate.NewStateFromParts(parts))
	require.Equal(t, uint64(77), next.UpdateState().Parts().LastSlot)
	require.Equal(t, state.PBFT.IssuerState(), next.IssuerState())
	before, after := state.PBFT.DelegationState(), next.DelegationState()
	require.Equal(t, before.ActiveDelegations(), after.ActiveDelegations())
	require.Equal(t, uint64(0), state.PBFT.UpdateState().Parts().LastSlot, "the receiver is unchanged")
}

// TestAdvanceByronPBFTStateCarriesSlots pins that advancing over a block
// records the block's flat slot with its signer, so the window can enforce the
// reference's slot rule, whether the block is validated or only observed.
func TestAdvanceByronPBFTStateCarriesSlots(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	for _, validate := range []bool{true, false} {
		pbft := state.PBFT
		for _, block := range blocks[:4] {
			next, _, err := AdvanceByronPBFTState(config, pbft, block, validate)
			require.NoError(t, err)
			pbft = next
		}
		signers := pbft.IssuerState().Signers()
		require.Len(t, signers, 3, "the boundary block signs nothing")
		for i, block := range blocks[1:4] {
			require.True(t, signers[i].SlotKnown)
			require.Equal(t, block.SlotNumber(), signers[i].Slot)
		}
	}
}

// TestAdvanceByronPBFTStateEnforcesSlotOrder pins that a validated block must
// not precede the last signer's slot, and that a trusted one is only observed.
func TestAdvanceByronPBFTStateEnforcesSlotOrder(t *testing.T) {
	t.Parallel()
	blocks, config, state := preprodByronApplyFixture(t)
	later, err := byronconsensus.NewPBFTStateWithSlots(
		[]byronconsensus.PBFTSigner{{Issuer: lcommon.Blake2b224{1}, Slot: 1_000_000, SlotKnown: true}},
		config.SecurityParam, 1, 1,
	)
	require.NoError(t, err)
	pbft := state.PBFT.WithIssuerState(later)

	_, _, err = AdvanceByronPBFTState(config, pbft, blocks[1], true)
	require.ErrorIs(t, err, byronconsensus.ErrPBFTInvalidSlot)
	_, _, err = AdvanceByronPBFTState(config, pbft, blocks[1], false)
	require.NoError(t, err)
}
