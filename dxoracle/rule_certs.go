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
	"math"
	"math/big"
	"reflect"
	"sync"

	"github.com/blinklabs-io/dingo/database/ledgerdb"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/database/types"
	"github.com/blinklabs-io/dingo/internal/test/conformance"
	dledger "github.com/blinklabs-io/dingo/ledger"
	"github.com/blinklabs-io/dingo/ledger/eras"
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	ocon "github.com/blinklabs-io/ouroboros-mock/conformance"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"
)

// CertAccount is a stake account row. Pool is nil when undelegated; Drep is
// nil when there is no vote delegation.
type CertAccount struct {
	Credential lcommon.Credential
	Deposit    uint64
	Balance    uint64
	Pool       []byte
	Drep       *lcommon.Drep
}

// CertPool is a pool row. Future marks re-registration parameters that take
// effect at the next epoch boundary, written beside the parameters in force.
type CertPool struct {
	PoolKeyHash   []byte
	VrfKeyHash    []byte
	Pledge        uint64
	Cost          uint64
	Deposit       uint64
	Margin        *big.Rat
	RewardAccount lcommon.Credential
	Owners        [][]byte
	Future        bool
	RetiringEpoch *uint64
}

// CertDRep is a registered DRep row.
type CertDRep struct {
	Credential lcommon.Credential
	Deposit    uint64
	Expiry     uint64
	AnchorURL  string
	AnchorHash []byte
}

// CertCommitteeMember is a committee member and its hot-key authorization.
type CertCommitteeMember struct {
	Cold     lcommon.Credential
	Hot      *lcommon.Credential
	Expiry   uint64
	Resigned bool
}

// CertTables are the tables a certificate transition reads and writes.
type CertTables struct {
	Accounts []CertAccount
	Pools    []CertPool
	DReps    []CertDRep
	// Committee is nil when there is no committee.
	Committee          []CertCommitteeMember
	HasCommittee       bool
	CommitteeThreshold *big.Rat
}

// CertRunInput is one certificate-rule run. Withdrawals, when set, are
// validated and applied first, then each of Certs in order.
type CertRunInput struct {
	Slot, Epoch uint64
	// NetworkID is the ledger's network: 0 testnet, 1 mainnet.
	NetworkID   uint
	Params      lcommon.ProtocolParameters
	State       CertTables
	Tx          lcommon.Transaction
	Certs       []lcommon.Certificate
	Withdrawals bool
	Families    []lcommon.UtxoValidationFamily
	// RuleFailures lists the failures of an era's rule list for the selected
	// families. Nil runs the Conway list (eras.ConwayRuleFailures); a Shelley
	// to Babbage run passes eras.LegacyRuleFailures for its era.
	RuleFailures func(
		tx lcommon.Transaction, slot uint64, ls lcommon.LedgerState,
		pp lcommon.ProtocolParameters, families ...lcommon.UtxoValidationFamily,
	) []lcommon.UtxoValidationFailure
	// Ignore, when set, drops the failures it reports true for: those the
	// caller's rule does not own. A run whose remaining failures are empty is
	// applied.
	Ignore func(lcommon.UtxoValidationFailure) bool
}

// CertRunResult holds every failure the steps reported, or, when there are
// none, the rows left by applying them.
type CertRunResult struct {
	Failures []lcommon.UtxoValidationFailure
	After    *CertTables
}

// CertOracle runs certificate rules through Dingo's ledger database: rows
// are imported the way a ledger-state import writes them, validation runs
// the production Conway rule list against the database-backed state, and the
// transaction is applied through the production transaction writer.
type CertOracle struct {
	mu sync.Mutex
	m  *conformance.DingoStateManager
	// aux is a second database, opened by RunBlock for the certificate steps.
	aux *CertOracle
}

// OpenCertOracle opens a scratch ledger database.
func OpenCertOracle() (*CertOracle, error) {
	m, err := conformance.NewDingoStateManager()
	if err != nil {
		return nil, fmt.Errorf("dxoracle: %w", err)
	}
	return &CertOracle{m: m}, nil
}

// Close releases the scratch database.
func (o *CertOracle) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	var auxErr error
	if o.aux != nil {
		auxErr = o.aux.Close()
	}
	return errors.Join(o.m.Close(), auxErr)
}

// certSubsetTx narrows a transaction to a prefix of its certificates, and
// optionally drops its withdrawals, so each rule sees only what it owns.
type certSubsetTx struct {
	lcommon.Transaction
	certs       []lcommon.Certificate
	withdrawals bool
	// salt makes the hash of each step distinct, so the steps of one
	// transaction are stored as separate transactions.
	salt byte
}

func (t certSubsetTx) Hash() lcommon.Blake2b256 {
	h := t.Transaction.Hash()
	h[0] ^= t.salt
	return h
}

func (t certSubsetTx) Certificates() []lcommon.Certificate { return t.certs }

// Proposals and votes belong to the GOV rule, not to a certificate step, and
// the database a step runs against does not hold the proposals a vote names.
func (certSubsetTx) ProposalProcedures() []lcommon.ProposalProcedure { return nil }

func (certSubsetTx) VotingProcedures() lcommon.VotingProcedures { return nil }

func (t certSubsetTx) Withdrawals() map[*lcommon.Address]*big.Int {
	if !t.withdrawals {
		return nil
	}
	return t.Transaction.Withdrawals()
}

// Run seeds in.State, validates the certificates and, when none fails,
// applies the transaction and reads the touched rows back.
func (o *CertOracle) Run(
	ctx context.Context,
	in CertRunInput,
) (*CertRunResult, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.m.Reset(); err != nil {
		return nil, fmt.Errorf("dxoracle: reset: %w", err)
	}
	o.m.SetSeedHook(seedHook(in.State))
	defer o.m.SetSeedHook(nil)
	in.Params = withRationals(in.Params)
	o.m.SetNetworkID(in.NetworkID)
	if err := o.m.LoadInitialState(parsedState(in), in.Params); err != nil {
		return nil, fmt.Errorf("dxoracle: load state: %w", err)
	}
	provider := conformance.NewDingoStateProvider(o.m)
	// The reference runs the withdrawals first and then each certificate in
	// order, keeps going after a failing one and reports every failure. Each
	// step is validated against the database as the steps before it left
	// it; a step that passes is applied through the production transaction
	// writer, a failing one is not.
	var failures []lcommon.UtxoValidationFailure
	step := func(sub certSubsetTx) error {
		ruleFailures := in.RuleFailures
		if ruleFailures == nil {
			ruleFailures = eras.ConwayRuleFailures
		}
		found := ruleFailures(
			sub, in.Slot, provider, in.Params, in.Families...,
		)
		kept := 0
		for _, f := range found {
			if in.Ignore == nil || !in.Ignore(f) {
				failures = append(failures, f)
				kept++
			}
		}
		if kept > 0 {
			return nil
		}
		return o.m.ApplyTransaction(sub, in.Slot)
	}
	if in.Withdrawals {
		if err := step(certSubsetTx{Transaction: in.Tx, withdrawals: true, salt: 0}); err != nil {
			return nil, fmt.Errorf("dxoracle: withdrawals: %w", err)
		}
	}
	for i, c := range in.Certs {
		sub := certSubsetTx{Transaction: in.Tx, certs: []lcommon.Certificate{c}, salt: byte(i + 1)}
		if err := step(sub); err != nil {
			return nil, fmt.Errorf("dxoracle: certificate %d: %w", i, err)
		}
	}
	if len(failures) > 0 {
		return &CertRunResult{Failures: failures}, nil
	}
	after, err := o.readBack(ctx, provider, in)
	if err != nil {
		return nil, err
	}
	return &CertRunResult{After: after}, nil
}

func parsedState(in CertRunInput) *ocon.ParsedInitialState {
	st := &ocon.ParsedInitialState{
		CurrentEpoch:                     in.Epoch,
		Utxos:                            map[string]ocon.ParsedUtxo{},
		StakeRegistrationsByCredential:   map[mockledger.RewardAccountKey]bool{},
		RewardAccountBalances:            map[mockledger.RewardAccountKey]uint64{},
		StakeCredentialDeposits:          map[mockledger.RewardAccountKey]uint64{},
		PoolDelegationsByCredential:      map[mockledger.RewardAccountKey]lcommon.PoolKeyHash{},
		DRepDelegationsByCredential:      map[mockledger.RewardAccountKey]lcommon.Drep{},
		CommitteeMembersByCredential:     map[mockledger.RewardAccountKey]uint64{},
		HotKeyAuthorizationsByCredential: map[mockledger.RewardAccountKey]lcommon.Credential{},
		CommitteeResignations:            map[mockledger.RewardAccountKey]bool{},
		Proposals:                        map[string]ocon.GovActionInfo{},
	}
	for _, a := range in.State.Accounts {
		k := mockledger.NewRewardAccountKey(a.Credential)
		st.StakeRegistrationsByCredential[k] = true
		st.RewardAccountBalances[k] = a.Balance
		st.StakeCredentialDeposits[k] = a.Deposit
		if a.Pool != nil {
			var h lcommon.PoolKeyHash
			copy(h[:], a.Pool)
			st.PoolDelegationsByCredential[k] = h
		}
		if a.Drep != nil {
			st.DRepDelegationsByCredential[k] = *a.Drep
		}
	}
	if in.State.HasCommittee {
		st.CommitteeThreshold = in.State.CommitteeThreshold
		for _, c := range in.State.Committee {
			k := mockledger.NewRewardAccountKey(c.Cold)
			st.CommitteeMembersByCredential[k] = c.Expiry
			switch {
			case c.Resigned:
				st.CommitteeResignations[k] = true
			case c.Hot != nil:
				st.HotKeyAuthorizationsByCredential[k] = *c.Hot
			}
		}
	}
	return st
}

// seedHook writes the pool and DRep rows, which a parsed state carries
// without parameters or anchors.
func seedHook(t CertTables) func(*dledger.ImportLedgerDB, ledgerdb.Txn) error {
	return func(ldb *dledger.ImportLedgerDB, txn ledgerdb.Txn) error {
		for _, future := range []bool{false, true} {
			for _, p := range t.Pools {
				if p.Future != future {
					continue
				}
				if err := ldb.ImportPool(poolRegistration(p), future, txn); err != nil {
					return fmt.Errorf("seed pool: %w", err)
				}
			}
		}
		for _, p := range t.Pools {
			if p.Future || p.RetiringEpoch == nil {
				continue
			}
			if err := ldb.RetirePools([][]byte{p.PoolKeyHash}, *p.RetiringEpoch, 0, txn); err != nil {
				return fmt.Errorf("seed retirement: %w", err)
			}
		}
		for _, d := range t.DReps {
			tag, err := models.CredentialTagFromUint(d.Credential.CredType)
			if err != nil {
				return err
			}
			row := &models.Drep{
				Credential:    d.Credential.Credential[:],
				CredentialTag: tag,
				Active:        true,
				ExpiryEpoch:   d.Expiry,
				AnchorURL:     d.AnchorURL,
				AnchorHash:    d.AnchorHash,
			}
			if err := ldb.ImportDrep(row, d.Deposit, txn); err != nil {
				return fmt.Errorf("seed drep: %w", err)
			}
		}
		return nil
	}
}

func poolRegistration(p CertPool) *models.PoolRegistration {
	tag, _ := models.CredentialTagFromUint(p.RewardAccount.CredType)
	reg := &models.PoolRegistration{
		PoolKeyHash:                p.PoolKeyHash,
		VrfKeyHash:                 p.VrfKeyHash,
		RewardAccount:              p.RewardAccount.Credential[:],
		RewardAccountCredentialTag: tag,
		Pledge:                     types.Uint64(p.Pledge),
		Cost:                       types.Uint64(p.Cost),
		Margin:                     &types.Rat{Rat: p.Margin},
		DepositAmount:              types.Uint64(p.Deposit),
	}
	for _, o := range p.Owners {
		reg.Owners = append(reg.Owners, models.PoolRegistrationOwner{KeyHash: o})
	}
	return reg
}

// touched returns the credentials, pools and DReps the run can have changed.
type touchedKeys struct {
	stake     credSet
	pools     map[lcommon.PoolKeyHash]struct{}
	dreps     credSet
	committee credSet
}

// credSet is a set of credentials; lcommon.Credential is not comparable.
type credSet map[credID]lcommon.Credential

type credID struct {
	credType uint
	hash     lcommon.Blake2b224
}

func (s credSet) add(c lcommon.Credential) {
	s[credID{c.CredType, c.Credential}] = c
}

func touched(in CertRunInput) touchedKeys {
	k := touchedKeys{
		stake:     credSet{},
		pools:     map[lcommon.PoolKeyHash]struct{}{},
		dreps:     credSet{},
		committee: credSet{},
	}
	for _, a := range in.State.Accounts {
		k.stake.add(a.Credential)
	}
	for _, p := range in.State.Pools {
		var h lcommon.PoolKeyHash
		copy(h[:], p.PoolKeyHash)
		k.pools[h] = struct{}{}
	}
	for _, d := range in.State.DReps {
		k.dreps.add(d.Credential)
	}
	for _, c := range in.State.Committee {
		k.committee.add(c.Cold)
	}
	for _, cert := range in.Certs {
		switch c := cert.(type) {
		case *lcommon.StakeRegistrationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.StakeDeregistrationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.StakeDelegationCertificate:
			if c.StakeCredential != nil {
				k.stake.add(*c.StakeCredential)
			}
		case *lcommon.RegistrationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.DeregistrationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.VoteDelegationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.StakeVoteDelegationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.StakeRegistrationDelegationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.VoteRegistrationDelegationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.StakeVoteRegistrationDelegationCertificate:
			k.stake.add(c.StakeCredential)
		case *lcommon.PoolRegistrationCertificate:
			k.pools[c.Operator] = struct{}{}
		case *lcommon.PoolRetirementCertificate:
			k.pools[c.PoolKeyHash] = struct{}{}
		case *lcommon.RegistrationDrepCertificate:
			k.dreps.add(c.DrepCredential)
		case *lcommon.DeregistrationDrepCertificate:
			k.dreps.add(c.DrepCredential)
		case *lcommon.UpdateDrepCertificate:
			k.dreps.add(c.DrepCredential)
		case *lcommon.AuthCommitteeHotCertificate:
			k.committee.add(c.ColdCredential)
		case *lcommon.ResignCommitteeColdCertificate:
			k.committee.add(c.ColdCredential)
		}
	}
	for addr := range in.Tx.Withdrawals() {
		if cred, err := addr.RewardAccountCredential(); err == nil {
			k.stake.add(cred)
		}
	}
	return k
}

func (o *CertOracle) readBack(
	ctx context.Context,
	provider *conformance.DingoStateProvider,
	in CertRunInput,
) (*CertTables, error) {
	keys := touched(in)
	ldb := o.m.LedgerDB()
	out := &CertTables{HasCommittee: in.State.HasCommittee, CommitteeThreshold: in.State.CommitteeThreshold}
	for _, cred := range keys.stake {
		tag, err := models.CredentialTagFromUint(cred.CredType)
		if err != nil {
			return nil, err
		}
		acct, err := ldb.GetAccountByCredential(ctx, tag, cred.Credential[:], false, nil)
		if errors.Is(err, models.ErrAccountNotFound) || (err == nil && (acct == nil || !acct.Active)) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("dxoracle: read account: %w", err)
		}
		deposit, err := provider.StakeCredentialDeposit(cred)
		if err != nil {
			return nil, err
		}
		row := CertAccount{Credential: cred, Balance: uint64(acct.Reward)}
		if deposit != nil {
			row.Deposit = *deposit
		}
		if len(acct.Pool) > 0 {
			row.Pool = append([]byte(nil), acct.Pool...)
		}
		drep, err := provider.DRepDelegation(cred)
		if err != nil {
			return nil, err
		}
		row.Drep = drep
		out.Accounts = append(out.Accounts, row)
	}
	for h := range keys.pools {
		rows, err := o.poolRows(ctx, h, in.Epoch)
		if err != nil {
			return nil, err
		}
		out.Pools = append(out.Pools, rows...)
	}
	for _, cred := range keys.dreps {
		tag, err := models.CredentialTagFromUint(cred.CredType)
		if err != nil {
			return nil, err
		}
		drep, err := ldb.GetDrepByCredential(ctx, tag, cred.Credential[:], false, nil)
		if errors.Is(err, models.ErrDrepNotFound) || (err == nil && (drep == nil || !drep.Active)) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("dxoracle: read drep: %w", err)
		}
		depositPtr, err := ldb.GetDrepLastRegistrationDeposit(ctx, tag, cred.Credential[:], nil)
		if err != nil {
			return nil, err
		}
		var deposit uint64
		if depositPtr != nil {
			deposit = *depositPtr
		}
		out.DReps = append(out.DReps, CertDRep{
			Credential: cred, Deposit: deposit, Expiry: drep.ExpiryEpoch,
			AnchorURL: drep.AnchorURL, AnchorHash: drep.AnchorHash,
		})
	}
	for _, cold := range keys.committee {
		member, err := provider.CommitteeCredentialMember(cold)
		if err != nil {
			return nil, err
		}
		if member == nil {
			continue
		}
		row := CertCommitteeMember{Cold: cold, Expiry: member.ExpiryEpoch, Resigned: member.Resigned}
		if member.HotKey != nil {
			row.Hot = &lcommon.Credential{CredType: hotType(in, *member.HotKey), Credential: *member.HotKey}
		}
		out.Committee = append(out.Committee, row)
	}
	return out, nil
}

// hotType recovers the credential type of a hot key from the request: the
// database keeps the hot key hash without its type.
func hotType(in CertRunInput, hot lcommon.Blake2b224) uint {
	for _, c := range in.State.Committee {
		if c.Hot != nil && c.Hot.Credential == hot {
			return c.Hot.CredType
		}
	}
	for _, cert := range in.Certs {
		if c, ok := cert.(*lcommon.AuthCommitteeHotCertificate); ok && c.HotCredential.Credential == hot {
			return c.HotCredential.CredType
		}
	}
	return lcommon.CredentialTypeAddrKeyHash
}

// poolRows reads a pool the way the ledger does: the parameters in force are
// the registration effective at the start of the epoch, the latest
// registration is future parameters when it is a different one, and the
// pending retirement comes from the pool's status. A pool registered during
// the epoch has no effective registration yet, so its latest one is in force.
func (o *CertOracle) poolRows(ctx context.Context, h lcommon.PoolKeyHash, epoch uint64) ([]CertPool, error) {
	ldb := o.m.LedgerDB()
	pool, err := ldb.GetPool(ctx, h, true, nil)
	if errors.Is(err, models.ErrPoolNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read pool: %w", err)
	}
	status, err := ldb.GetPoolStatus(h, nil)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read pool status: %w", err)
	}
	if !status.Active || len(pool.Registration) == 0 {
		return nil, nil
	}
	latest := &pool.Registration[0]
	for i := range pool.Registration {
		if dledger.PoolRegistrationSupersedes(&pool.Registration[i], latest) {
			latest = &pool.Registration[i]
		}
	}
	effective, err := ldb.GetPoolRegistrationsEffectiveForEpoch(
		[]lcommon.PoolKeyHash{h}, 0, epoch, math.MaxInt64, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read effective pool registration: %w", err)
	}
	current := latest
	if len(effective) > 0 {
		current = &effective[0]
	}
	held, err := ldb.GetPoolDepositsHeld([]lcommon.PoolKeyHash{h}, nil)
	if err != nil {
		return nil, fmt.Errorf("dxoracle: read pool deposit: %w", err)
	}
	row := func(r *models.PoolRegistration, future bool) CertPool {
		cp := CertPool{
			PoolKeyHash: append([]byte(nil), h[:]...),
			VrfKeyHash:  append([]byte(nil), r.VrfKeyHash...),
			Pledge:      uint64(r.Pledge),
			Cost:        uint64(r.Cost),
			Deposit:     held[string(h[:])],
			Future:      future,
		}
		if r.Margin != nil && r.Margin.Rat != nil {
			cp.Margin = new(big.Rat).Set(r.Margin.Rat)
		}
		credType := uint(lcommon.CredentialTypeAddrKeyHash)
		if r.RewardAccountCredentialTag == 1 {
			credType = lcommon.CredentialTypeScriptHash
		}
		cp.RewardAccount = lcommon.Credential{CredType: credType, Credential: lcommon.NewBlake2b224(r.RewardAccount)}
		for _, o := range r.Owners {
			cp.Owners = append(cp.Owners, append([]byte(nil), o.KeyHash...))
		}
		return cp
	}
	out := []CertPool{row(current, false)}
	out[0].RetiringEpoch = status.RetirementEpoch
	if latest.AddedSlot > current.AddedSlot {
		out = append(out, row(latest, true))
	}
	return out, nil
}

// withRationals returns a copy of params whose unset rational fields are
// zero. The ledger database persists protocol parameters as CBOR and a nil
// rational does not encode; the certificate rules read none of them.
func withRationals(pp lcommon.ProtocolParameters) lcommon.ProtocolParameters {
	v := reflect.ValueOf(pp)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return pp
	}
	cp := reflect.New(v.Elem().Type())
	cp.Elem().Set(v.Elem())
	fillRationals(cp.Elem())
	return cp.Interface().(lcommon.ProtocolParameters)
}

func fillRationals(v reflect.Value) {
	ratPtr := reflect.TypeOf((*cbor.Rat)(nil))
	ratVal := reflect.TypeOf(cbor.Rat{})
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		switch {
		case f.Type() == ratPtr:
			if f.IsNil() && f.CanSet() {
				f.Set(reflect.ValueOf(&cbor.Rat{Rat: new(big.Rat)}))
			}
		case f.Type() == ratVal:
			if r := f.Addr().Interface().(*cbor.Rat); r.Rat == nil {
				r.Rat = new(big.Rat)
			}
		case f.Kind() == reflect.Struct:
			fillRationals(f)
		}
	}
}
