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
	"sort"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/internal/test/conformance"
	"github.com/blinklabs-io/dingo/ledger/eras"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	conwaypp "github.com/blinklabs-io/gouroboros/ledger/conway"
	ocon "github.com/blinklabs-io/ouroboros-mock/conformance"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"
)

// LedgerUtxo is a live output row.
type LedgerUtxo struct {
	TxHash []byte
	Index  uint32
	Output lcommon.TransactionOutput
}

// LedgerProposal is an active governance proposal row; ID is "txhash#index"
// and Parent, when set, is the parent action in the same form. Votes maps
// "votertype:hash" to the vote (0 no, 1 yes, 2 abstain).
type LedgerProposal struct {
	ID             string
	ActionType     lcommon.GovActionType
	ExpiresAfter   uint64
	SubmittedEpoch uint64
	Deposit        uint64
	ReturnAccount  *lcommon.Credential
	Parent         *string
	Votes          map[string]uint8
}

// LedgerTables are the tables a whole-transaction transition reads and writes:
// the certificate tables plus the UTxO set, the proposals and the treasury.
type LedgerTables struct {
	CertTables
	Utxos     []LedgerUtxo
	Proposals []LedgerProposal
	Treasury  uint64
	// Constitution is the enacted constitution; guardrail lookups fail without one.
	Constitution *LedgerConstitution
}

// LedgerConstitution is the enacted constitution row. PolicyHash is nil when
// there is no guardrails script.
type LedgerConstitution struct {
	AnchorURL  string
	AnchorHash []byte
	PolicyHash []byte
}

// LedgerRunInput is one run of the composed ledger rule. Families selects the
// rules that validate the transaction against the state it starts from;
// CertFamilies, when set, validate the withdrawals and certificates one step at
// a time, each against the state the steps before it left.
type LedgerRunInput struct {
	Slot, Epoch  uint64
	NetworkID    uint
	Params       lcommon.ProtocolParameters
	State        LedgerTables
	Tx           lcommon.Transaction
	Families     []lcommon.UtxoValidationFamily
	CertFamilies []lcommon.UtxoValidationFamily
	// Withdrawals runs the withdrawals as the first certificate step.
	Withdrawals bool
	Ignore      func(lcommon.UtxoValidationFailure) bool
	// LegacyEraID, when nonzero, is the gouroboros era id of a Shelley-to-Babbage
	// run, which uses that era's rule list in place of Conway's.
	LegacyEraID uint
}

// LedgerVote is a vote row read back after a run.
type LedgerVote struct {
	ActionID   string
	VoterType  uint8
	Credential []byte
	CredTag    uint8
	Vote       uint8
}

// LedgerRunResult holds the failures of the validation phases or, when there
// are none, the rows the production writer left.
type LedgerRunResult struct {
	Failures []lcommon.UtxoValidationFailure
	After    *CertTables
	// UtxoAdded and UtxoRemoved are the output references that appeared in and
	// vanished from the UTxO set, with the output of each added one.
	UtxoAdded   []LedgerUtxo
	UtxoRemoved []string
	// ProposalsAfter and VotesAfter are the proposal and vote rows after.
	ProposalsAfter []models.GovernanceProposal
	VotesAfter     []LedgerVote
	// Donations is the Conway treasury donation the transaction recorded.
	Donations uint64
}

func parsedLedgerState(in LedgerRunInput) *ocon.ParsedInitialState {
	st := parsedState(CertRunInput{Epoch: in.Epoch, State: in.State.CertTables})
	st.Treasury = in.State.Treasury
	if c := in.State.Constitution; c != nil {
		st.Constitution = &ocon.ConstitutionInfo{AnchorURL: c.AnchorURL, AnchorHash: c.AnchorHash, PolicyHash: c.PolicyHash}
	}
	for _, u := range in.State.Utxos {
		st.Utxos[fmt.Sprintf("%s#%d", hex.EncodeToString(u.TxHash), u.Index)] = ocon.ParsedUtxo{
			TxHash: u.TxHash, Index: u.Index, Output: u.Output,
		}
	}
	for _, p := range in.State.Proposals {
		info := ocon.GovActionInfo{
			ActionType: p.ActionType, ExpiresAfter: p.ExpiresAfter, SubmittedEpoch: p.SubmittedEpoch,
			Deposit: p.Deposit, ParentActionId: p.Parent, Votes: p.Votes,
			ProposedMembers: map[lcommon.Blake2b224]uint64{},
		}
		if p.ReturnAccount != nil {
			k := mockledger.NewRewardAccountKey(*p.ReturnAccount)
			info.ReturnAccount = &k
		}
		st.Proposals[p.ID] = info
	}
	return st
}

// RunLedger validates the transaction and, when it is accepted, applies it
// through the production transaction writer and reads the touched rows back.
func (o *CertOracle) RunLedger(ctx context.Context, in LedgerRunInput) (*LedgerRunResult, error) {
	kept := func(found []lcommon.UtxoValidationFailure) []lcommon.UtxoValidationFailure {
		var out []lcommon.UtxoValidationFailure
		for _, f := range found {
			if in.Ignore == nil || !in.Ignore(f) {
				out = append(out, f)
			}
		}
		return out
	}
	var failures []lcommon.UtxoValidationFailure
	if len(in.CertFamilies) > 0 {
		cert, err := o.Run(ctx, CertRunInput{
			Slot: in.Slot, Epoch: in.Epoch, NetworkID: in.NetworkID, Params: in.Params,
			State: in.State.CertTables, Tx: in.Tx, Certs: in.Tx.Certificates(),
			Withdrawals: in.Withdrawals, Families: in.CertFamilies, Ignore: in.Ignore,
			RuleFailures: legacyRuleFailures(in.LegacyEraID),
		})
		if err != nil {
			return nil, err
		}
		failures = append(failures, cert.Failures...)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.seed(in); err != nil {
		return nil, err
	}
	provider := conformanceProvider(o)
	failures = append(failures, ledgerTxFailures(provider, in.Slot, in.Params, in.LegacyEraID, in.Families, in.Tx, len(failures), kept)...)
	if len(failures) > 0 {
		return &LedgerRunResult{Failures: failures}, nil
	}
	before := o.utxoIDs()
	if err := o.m.ApplyTransaction(in.Tx, in.Slot); err != nil {
		return nil, fmt.Errorf("dxoracle: apply: %w", err)
	}
	certIn := CertRunInput{Slot: in.Slot, Epoch: in.Epoch, NetworkID: in.NetworkID, Params: in.Params, State: in.State.CertTables, Tx: in.Tx, Certs: in.Tx.Certificates()}
	return o.ledgerResult(ctx, provider, certIn, in.Tx.Produced(), before)
}

// ledgerResult reads the rows the applied transactions left and diffs the
// output set against before. produced lists every output the transactions
// create; an output that appears must be one of them. The caller holds o.mu.
func (o *CertOracle) ledgerResult(
	ctx context.Context,
	provider *conformance.DingoStateProvider,
	certIn CertRunInput,
	producedUtxos []lcommon.Utxo,
	before map[string]bool,
) (*LedgerRunResult, error) {
	in := certIn
	res := &LedgerRunResult{}
	after, err := o.readBack(ctx, provider, certIn)
	if err != nil {
		return nil, err
	}
	res.After = after
	afterIDs := o.utxoIDs()
	produced := map[string]lcommon.Utxo{}
	for _, u := range producedUtxos {
		produced[fmt.Sprintf("%x#%d", u.Id.Id().Bytes(), u.Id.Index())] = u
	}
	for id := range afterIDs {
		if !before[id] {
			u, ok := produced[id]
			if !ok {
				return nil, fmt.Errorf("dxoracle: output %s appeared that the transaction does not produce", id)
			}
			res.UtxoAdded = append(res.UtxoAdded, LedgerUtxo{TxHash: u.Id.Id().Bytes(), Index: u.Id.Index(), Output: u.Output})
		}
	}
	for id := range before {
		if !afterIDs[id] {
			res.UtxoRemoved = append(res.UtxoRemoved, id)
		}
	}
	sort.Strings(res.UtxoRemoved)
	sort.Slice(res.UtxoAdded, func(i, j int) bool {
		if c := compareBytes(res.UtxoAdded[i].TxHash, res.UtxoAdded[j].TxHash); c != 0 {
			return c < 0
		}
		return res.UtxoAdded[i].Index < res.UtxoAdded[j].Index
	})
	ldb := o.m.LedgerDB()
	props, err := ldb.GetActiveGovernanceProposals(ctx, in.Epoch, nil)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read proposals: %w", err)
	}
	for _, p := range props {
		res.ProposalsAfter = append(res.ProposalsAfter, *p)
		votes, err := ldb.GetGovernanceVotes(ctx, p.ID, nil)
		if err != nil {
			return nil, fmt.Errorf("dxoracle: read votes: %w", err)
		}
		for _, v := range votes {
			res.VotesAfter = append(res.VotesAfter, LedgerVote{
				ActionID:  fmt.Sprintf("%x#%d", p.TxHash, p.ActionIndex),
				VoterType: v.VoterType, Credential: v.VoterCredential, CredTag: v.VoterCredentialTag, Vote: v.Vote,
			})
		}
	}
	res.Donations, err = ldb.SumNetworkDonationsForEpoch(in.Epoch, nil)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read donations: %w", err)
	}
	return res, nil
}

// ledgerTxFailures validates tx against the database behind provider with the
// rule families listed, and keeps the failures keep lets through. An
// unresolved input fails the fee check only when no failure came before it,
// priorFailures counting those of earlier phases.
func ledgerTxFailures(
	provider *conformance.DingoStateProvider,
	slot uint64,
	params lcommon.ProtocolParameters,
	legacyEraID uint,
	families []lcommon.UtxoValidationFamily,
	tx lcommon.Transaction,
	priorFailures int,
	keep func([]lcommon.UtxoValidationFailure) []lcommon.UtxoValidationFailure,
) []lcommon.UtxoValidationFailure {
	var failures []lcommon.UtxoValidationFailure
	if len(families) > 0 {
		if legacyEraID != 0 {
			found, ok := eras.LegacyRuleFailures(legacyEraID, tx, slot, provider, withRationals(params), families...)
			if !ok {
				return nil
			}
			failures = append(failures, keep(found)...)
		} else {
			failures = append(failures, keep(eras.ConwayRuleFailures(tx, slot, provider, withRationals(params), families...))...)
		}
	}
	if legacyEraID != 0 {
		if familyListed(families, lcommon.UtxoFamilyUtxo) {
			found, err := legacySizeFeeFailures(legacyEraID, tx, withRationals(params))
			if err == nil {
				failures = append(failures, keep(found)...)
			}
		}
		return failures
	}
	// ConwayRuleFailures leaves the minimum fee to ValidateTxFeeConway, which
	// the production validator runs beside the rule list.
	if conway, ok := params.(*conwaypp.ConwayProtocolParameters); ok && familyListed(families, lcommon.UtxoFamilyUtxo) {
		err := eras.ValidateTxFeeConway(tx, lcommon.NewTransactionScopedLedgerState(provider), conway)
		// An unresolved input is reported by the input rule alone, as in the
		// production validator.
		unresolved := errors.Is(err, lcommon.ErrInputResolution) || errors.Is(err, lcommon.ErrReferenceInputResolution)
		if err != nil && (!unresolved || priorFailures+len(failures) == 0) {
			failures = append(failures, keep([]lcommon.UtxoValidationFailure{{
				RuleId: lcommon.UtxoValidationRuleFeeTooSmall, Family: lcommon.UtxoFamilyUtxo, Err: err,
			}})...)
		}
	}
	return failures
}

func familyListed(list []lcommon.UtxoValidationFamily, f lcommon.UtxoValidationFamily) bool {
	for _, x := range list {
		if x == f {
			return true
		}
	}
	return false
}

func compareBytes(a, b []byte) int {
	switch {
	case string(a) < string(b):
		return -1
	case string(a) > string(b):
		return 1
	}
	return 0
}

// seed resets the scratch database and imports the input tables. The caller
// holds o.mu.
func (o *CertOracle) seed(in LedgerRunInput) error {
	if err := o.m.Reset(); err != nil {
		return fmt.Errorf("dxoracle: reset: %w", err)
	}
	o.m.SetSeedHook(seedHook(in.State.CertTables))
	defer o.m.SetSeedHook(nil)
	o.m.SetNetworkID(in.NetworkID)
	if err := o.m.LoadInitialState(parsedLedgerState(in), withRationals(in.Params)); err != nil {
		return fmt.Errorf("dxoracle: load state: %w", err)
	}
	return nil
}

func conformanceProvider(o *CertOracle) *conformance.DingoStateProvider {
	return conformance.NewDingoStateProvider(o.m)
}

// utxoIDs lists the live output references as "txhash#index".
func (o *CertOracle) utxoIDs() map[string]bool {
	out := map[string]bool{}
	for _, id := range o.m.GetStateSnapshot().UtxoIDs {
		out[id] = true
	}
	return out
}
