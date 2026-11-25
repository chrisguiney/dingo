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
	"errors"
	"fmt"
	"math/big"
	"sort"

	"github.com/blinklabs-io/dingo/internal/test/conformance"
	dledger "github.com/blinklabs-io/dingo/ledger"
	"github.com/blinklabs-io/gouroboros/ledger"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// BlockFailureKind names a block-level check that failed.
type BlockFailureKind int

const (
	// BlockBodySize: the header declares a body size other than the body's.
	BlockBodySize BlockFailureKind = iota + 1
	// BlockBodyHash: the header's body hash is not the hash of the body.
	BlockBodyHash
	// BlockExUnits: the transactions declare more than the block budget.
	BlockExUnits
	// BlockRefScripts: the transactions consume too many reference-script bytes.
	BlockRefScripts
	// BlockHeaderProtocolVersion: the header is more than one major version ahead.
	BlockHeaderProtocolVersion
)

// BlockFailure is one failed block-level check.
type BlockFailure struct {
	Kind BlockFailureKind
	Err  error
}

// BlockRunInput is one block applied to the state. The block is decoded from
// BlockCbor as Dingo decodes an inbound block, except that a body hash the
// header does not commit to is reported as a failure instead of a decode
// error. Families, CertFamilies and Ignore are as in LedgerRunInput and apply
// to every transaction.
type BlockRunInput struct {
	Slot, Epoch  uint64
	NetworkID    uint
	Params       lcommon.ProtocolParameters
	State        LedgerTables
	BlockCbor    []byte
	BlockType    uint
	Families     []lcommon.UtxoValidationFamily
	CertFamilies []lcommon.UtxoValidationFamily
	// Withdrawals runs each transaction's withdrawals as its first certificate step.
	Withdrawals bool
	Ignore      func(lcommon.UtxoValidationFailure) bool
	// LegacyEraID is as in LedgerRunInput.
	LegacyEraID uint
}

// BlockRunResult holds what the block-level checks and the transactions
// reported and, when nothing failed, the net change the block made.
type BlockRunResult struct {
	// LedgerRunResult carries the failures of the first failing transaction
	// or, when nothing failed, the rows the block left: an output created and
	// spent inside the block is in neither output list.
	LedgerRunResult
	BlockFailures []BlockFailure
	// FailedTx is the index of the first failing transaction, or -1, and
	// FailedTransaction that transaction.
	FailedTx          int
	FailedTransaction lcommon.Transaction
	// ExUnits is the sum of the redeemer budgets the transactions declare.
	ExUnits lcommon.ExUnits
	// RefScriptBytes is the reference-script size the block consumes.
	RefScriptBytes uint64
	// Transactions is the number of transactions in the block.
	Transactions int
}

// mergedWithdrawals gives touched the withdrawals of every transaction of a
// block; touched reads nothing else from its transaction.
type mergedWithdrawals struct {
	lcommon.Transaction
	all map[*lcommon.Address]*big.Int
}

func (m mergedWithdrawals) Withdrawals() map[*lcommon.Address]*big.Int { return m.all }

// scratch returns the second database the certificate steps of a transaction
// run on while the first holds the block's state.
func (o *CertOracle) scratch() (*CertOracle, error) {
	if o.aux == nil {
		aux, err := OpenCertOracle()
		if err != nil {
			return nil, err
		}
		o.aux = aux
	}
	return o.aux, nil
}

// decodeBlock decodes the block as Dingo does, with the body hash check of the
// decoder lifted into a failure.
func decodeBlock(blockType uint, data []byte) (ledger.Block, *BlockFailure, error) {
	block, err := ledger.NewBlockFromCbor(blockType, data)
	if err == nil {
		return block, nil, nil
	}
	var ve *lcommon.ValidationError
	if !errors.As(err, &ve) || ve.Type != lcommon.ValidationErrorTypeBodyHash {
		return nil, nil, err
	}
	block, err = ledger.NewBlockFromCbor(blockType, data, lcommon.VerifyConfig{SkipBodyHashValidation: true})
	if err != nil {
		return nil, nil, err
	}
	return block, &BlockFailure{Kind: BlockBodyHash, Err: ve}, nil
}

// RunBlock applies a block the way Dingo does while syncing: the block-level
// checks run against the state before the block, then each transaction is
// validated against the database the transactions before it left and applied
// to it through the production transaction writer. The first failing
// transaction ends the run, and a block with any failure leaves no change.
func (o *CertOracle) RunBlock(ctx context.Context, in BlockRunInput) (*BlockRunResult, error) {
	block, hashFailure, err := decodeBlock(in.BlockType, in.BlockCbor)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: decode block: %w", err)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.seed(LedgerRunInput{
		Slot: in.Slot, Epoch: in.Epoch, NetworkID: in.NetworkID, Params: in.Params, State: in.State,
	}); err != nil {
		return nil, err
	}
	provider := conformanceProvider(o)
	params := withRationals(in.Params)
	res := &BlockRunResult{FailedTx: -1, Transactions: len(block.Transactions())}
	add := func(kind BlockFailureKind, err error) {
		if err != nil {
			res.BlockFailures = append(res.BlockFailures, BlockFailure{Kind: kind, Err: err})
		}
	}
	if hashFailure != nil {
		res.BlockFailures = append(res.BlockFailures, *hashFailure)
	}
	add(BlockBodySize, dledger.ValidateBlockBodySize(block))
	if pv, perr := dledger.GetProtocolVersion(params); perr == nil {
		mainnet := in.NetworkID != 0
		add(BlockHeaderProtocolVersion, dledger.ValidateHeaderProtocolVersion(block.Header(), pv.Major, mainnet))
	}
	if res.ExUnits, err = dledger.BlockDeclaredExUnits(block); err != nil {
		return nil, fmt.Errorf("dxoracle: %w", err)
	}
	add(BlockExUnits, dledger.ValidateBlockExUnits(block, params))
	if res.RefScriptBytes, err = dledger.BlockReferenceScriptSize(block, params, provider); err != nil {
		return nil, fmt.Errorf("dxoracle: %w", err)
	}
	add(BlockRefScripts, dledger.ValidateBlockReferenceScripts(block, params, provider))
	sort.SliceStable(res.BlockFailures, func(i, j int) bool { return res.BlockFailures[i].Kind < res.BlockFailures[j].Kind })

	keep := func(found []lcommon.UtxoValidationFailure) []lcommon.UtxoValidationFailure {
		var out []lcommon.UtxoValidationFailure
		for _, f := range found {
			if in.Ignore == nil || !in.Ignore(f) {
				out = append(out, f)
			}
		}
		return out
	}
	before := o.utxoIDs()
	var certs []lcommon.Certificate
	withdrawals := map[*lcommon.Address]*big.Int{}
	for i, tx := range block.Transactions() {
		certs = append(certs, tx.Certificates()...)
		for a, v := range tx.Withdrawals() {
			withdrawals[a] = v
		}
		var failures []lcommon.UtxoValidationFailure
		if len(in.CertFamilies) > 0 {
			certFailures, err := o.certStepFailures(ctx, provider, in, tx, certs, withdrawals)
			if err != nil {
				return nil, err
			}
			failures = append(failures, certFailures...)
		}
		failures = append(failures, ledgerTxFailures(provider, in.Slot, in.Params, in.LegacyEraID, in.Families, tx, len(failures), keep)...)
		if len(failures) > 0 {
			res.Failures, res.FailedTx, res.FailedTransaction = failures, i, tx
			return res, nil
		}
		if err := o.m.ApplyTransaction(tx, in.Slot); err != nil {
			return nil, fmt.Errorf("dxoracle: apply transaction %d: %w", i, err)
		}
	}
	if len(res.BlockFailures) > 0 {
		return res, nil
	}
	certIn := CertRunInput{
		Slot: in.Slot, Epoch: in.Epoch, NetworkID: in.NetworkID, Params: in.Params,
		State: in.State.CertTables, Tx: mergedWithdrawals{all: withdrawals}, Certs: certs,
	}
	var produced []lcommon.Utxo
	for _, tx := range block.Transactions() {
		produced = append(produced, tx.Produced()...)
	}
	diff, err := o.ledgerResult(ctx, provider, certIn, produced, before)
	if err != nil {
		return nil, err
	}
	res.LedgerRunResult = *diff
	return res, nil
}

// certStepFailures runs the withdrawals and certificates of tx one step at a
// time on the scratch database, seeded with the certificate tables the block
// has left so far. certs and withdrawals are those of every transaction up to
// and including tx, so that the tables read back cover each credential the
// block has touched.
func (o *CertOracle) certStepFailures(
	ctx context.Context,
	provider *conformance.DingoStateProvider,
	in BlockRunInput,
	tx lcommon.Transaction,
	certs []lcommon.Certificate,
	withdrawals map[*lcommon.Address]*big.Int,
) ([]lcommon.UtxoValidationFailure, error) {
	aux, err := o.scratch()
	if err != nil {
		return nil, err
	}
	current, err := o.readBack(ctx, provider, CertRunInput{
		Epoch: in.Epoch, State: in.State.CertTables, Tx: mergedWithdrawals{all: withdrawals}, Certs: certs,
	})
	if err != nil {
		return nil, err
	}
	run, err := aux.Run(ctx, CertRunInput{
		Slot: in.Slot, Epoch: in.Epoch, NetworkID: in.NetworkID, Params: in.Params, State: *current,
		Tx: tx, Certs: tx.Certificates(), Withdrawals: in.Withdrawals, Families: in.CertFamilies, Ignore: in.Ignore,
		RuleFailures: legacyRuleFailures(in.LegacyEraID),
	})
	if err != nil {
		return nil, err
	}
	return run.Failures, nil
}
