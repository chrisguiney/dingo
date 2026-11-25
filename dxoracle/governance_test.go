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

package dxoracle_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/dxoracle"
	"github.com/blinklabs-io/dingo/ledger/governance"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/stretchr/testify/require"
)

func unit(n, d int64) cbor.Rat { return cbor.Rat{Rat: big.NewRat(n, d)} }

// govParams has every voting threshold at 1, so nothing is accepted without
// stake behind it.
func govParams(major uint) *conway.ConwayProtocolParameters {
	pp := &conway.ConwayProtocolParameters{}
	pp.ProtocolVersion.Major = major
	p := &pp.PoolVotingThresholds
	p.MotionNoConfidence, p.CommitteeNormal, p.CommitteeNoConfidence = unit(1, 1), unit(1, 1), unit(1, 1)
	p.HardForkInitiation, p.PpSecurityGroup = unit(1, 1), unit(1, 1)
	d := &pp.DRepVotingThresholds
	d.MotionNoConfidence, d.CommitteeNormal, d.CommitteeNoConfidence = unit(1, 1), unit(1, 1), unit(1, 1)
	d.UpdateToConstitution, d.HardForkInitiation, d.PpNetworkGroup = unit(1, 1), unit(1, 1), unit(1, 1)
	d.PpEconomicGroup, d.PpTechnicalGroup, d.PpGovGroup, d.TreasuryWithdrawal = unit(1, 1), unit(1, 1), unit(1, 1), unit(1, 1)
	return pp
}

func txid(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func action(b byte) dxoracle.GovAction { return dxoracle.GovAction{TxHash: txid(b)} }

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

const (
	infoAction = "8106"
	pcUpdate   = "a1021a00016000f6"
)

// pcAction is a ParameterChange naming parent (zero for none).
func pcAction(parent byte) []byte {
	if parent == 0 {
		return mustHex("8400f6" + pcUpdate)
	}
	return mustHex("840082" + "5820" + hex.EncodeToString(txid(parent)) + "00" + pcUpdate)
}

func proposal(b byte, cbor []byte, typ uint8, parent *dxoracle.GovAction, expires, deposit uint64, ret byte) dxoracle.GovProposal {
	return dxoracle.GovProposal{
		ID: action(b), ActionType: typ, Cbor: cbor, Parent: parent,
		ProposedEpoch: 1, ExpiresEpoch: expires, Deposit: deposit,
		Return: dxoracle.Credential{Tag: 0, Key: key(ret)},
	}
}

func ids(as []dxoracle.GovAction) []byte {
	var out []byte
	for _, a := range as {
		out = append(out, a.TxHash[0])
	}
	return out
}

func TestProposalExpiryRefundsRegisteredAndKeepsUnclaimed(t *testing.T) {
	t.Parallel()
	parent := action(1)
	st := dxoracle.GovState{
		PParams:  govParams(10),
		Treasury: 7,
		Accounts: []dxoracle.Account{{Tag: 0, Key: key(0xa1), Reward: 10, Active: true}},
		Proposals: []dxoracle.GovProposal{
			// Expired, refunded to a registered account.
			proposal(1, pcAction(0), 0, nil, 3, 1000, 0xa1),
			// A child of the expired action leaves with it, refunded to an
			// account that is not registered.
			proposal(2, pcAction(1), 0, &parent, 20, 500, 0xa2),
			// Expired on its own, return account unregistered.
			proposal(3, mustHex(infoAction), 6, nil, 4, 300, 0xa3),
			// Still alive.
			proposal(4, mustHex(infoAction), 6, nil, 5, 200, 0xa1),
		},
	}
	got, err := shared.ProposalExpiry(context.Background(), st, 5)
	require.NoError(t, err)
	require.Equal(t, []byte{1, 2, 3}, ids(got.Expired))
	require.Equal(t, []dxoracle.AccountReward{{Credential: dxoracle.Credential{Tag: 0, Key: key(0xa1)}, Reward: 1010}}, got.Accounts)
	require.Equal(t, uint64(800), got.Unclaimed)
}

func TestProposalExpiryBoundary(t *testing.T) {
	t.Parallel()
	// Expiry equal to the epoch is still alive; one below it is expired.
	st := dxoracle.GovState{
		PParams: govParams(10),
		Proposals: []dxoracle.GovProposal{
			proposal(1, mustHex(infoAction), 6, nil, 5, 0, 0xa1),
			proposal(2, mustHex(infoAction), 6, nil, 4, 0, 0xa1),
		},
	}
	got, err := shared.ProposalExpiry(context.Background(), st, 5)
	require.NoError(t, err)
	require.Equal(t, []byte{2}, ids(got.Expired))
}

func TestRatifyAcceptsOnStake(t *testing.T) {
	t.Parallel()
	d1 := dxoracle.DRep{Tag: 0, Key: key(0xd1), ExpiryEpoch: 10, Active: true}
	pp := govParams(10)
	pp.DRepVotingThresholds.MotionNoConfidence = unit(1, 2)
	pp.PoolVotingThresholds.MotionNoConfidence = unit(1, 2)
	noConfidence := mustHex("8203f6")
	st := dxoracle.GovState{
		PParams:   pp,
		Treasury:  1,
		Dreps:     []dxoracle.DRep{d1},
		DRepStake: []dxoracle.GovDRepStake{{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 100}},
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xb1), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{proposal(1, noConfidence, 3, nil, 9, 0, 0xa1)},
		Votes: []dxoracle.GovVote{
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1},
			{Proposal: 0, Role: 2, Key: key(0xb1), Vote: 1},
		},
	}
	got, err := shared.Ratify(context.Background(), st, 5)
	require.NoError(t, err)
	require.Equal(t, []byte{1}, ids(got.Ratified))
	require.True(t, got.Delayed)
	require.Empty(t, got.Expired)

	// Without the pool vote the SPO share is zero and the action fails.
	st.Votes = st.Votes[:1]
	got, err = shared.Ratify(context.Background(), st, 5)
	require.NoError(t, err)
	require.Empty(t, got.Ratified)
	require.False(t, got.Delayed)
}

func TestEnactParameterChangeAndWithdrawal(t *testing.T) {
	t.Parallel()
	pp := govParams(10)
	ratified := proposal(1, pcAction(0), 0, nil, 9, 0, 0xa1)
	ratified.Ratified = true
	st := dxoracle.GovState{PParams: pp, Proposals: []dxoracle.GovProposal{ratified}}
	got, err := shared.Enact(context.Background(), st, 5)
	require.NoError(t, err)
	conwayPP, ok := got.PParams.(*conway.ConwayProtocolParameters)
	require.True(t, ok)
	// The update sets key 2, the maximum block body size.
	require.Equal(t, uint(0x16000), conwayPP.MaxBlockBodySize)
	require.Equal(t, action(1), got.Roots[0])
}

func TestRatifyCountsEveryDRepStake(t *testing.T) {
	t.Parallel()
	// Two DReps each hold stake; each needs its own delegating account.
	pp := govParams(10)
	pp.DRepVotingThresholds.MotionNoConfidence = unit(61, 100)
	pp.PoolVotingThresholds.MotionNoConfidence = unit(1, 2)
	st := dxoracle.GovState{
		PParams: pp,
		Dreps: []dxoracle.DRep{
			{Tag: 0, Key: key(0xd1), ExpiryEpoch: 10, Active: true},
			{Tag: 0, Key: key(0xd2), ExpiryEpoch: 10, Active: true},
		},
		DRepStake: []dxoracle.GovDRepStake{
			{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd1)}, Stake: 61},
			{Delegation: dxoracle.Delegation{Type: dxoracle.DelegateKeyHash, Key: key(0xd2)}, Stake: 39},
		},
		SPO:       &governance.SPOVotingState{Dist: []*models.PoolStakeSnapshot{{PoolKeyHash: key(0xb1), TotalStake: 100}}, TotalStake: 100},
		Proposals: []dxoracle.GovProposal{proposal(1, mustHex("8203f6"), 3, nil, 9, 0, 0xa1)},
		Votes: []dxoracle.GovVote{
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd1), Vote: 1},
			{Proposal: 0, Role: 1, Tag: 0, Key: key(0xd2), Vote: 0},
			{Proposal: 0, Role: 2, Key: key(0xb1), Vote: 1},
		},
	}
	got, err := shared.Ratify(context.Background(), st, 5)
	require.NoError(t, err)
	// 61 of 100 is exactly the threshold.
	require.Equal(t, []byte{1}, ids(got.Ratified))
}
