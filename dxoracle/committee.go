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
	"fmt"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/models"
	"github.com/blinklabs-io/dingo/ledger/governance"
	"github.com/blinklabs-io/gouroboros/cbor"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// CommitteeMember is a seated committee member. Hot is the hot credential its
// latest authorization names; Resigned records a later resignation.
type CommitteeMember struct {
	Tag      uint8
	Key      []byte
	Expiry   uint64
	Hot      *Credential
	Resigned bool
}

// CommitteeVotingInput seeds a committee and names the epoch it is read at.
type CommitteeVotingInput struct {
	Members []CommitteeMember
	Epoch   uint64
}

// CommitteeVotingState seeds the members and their certificates, then runs
// the production loader of the committee's voting view.
func (o *Oracle) CommitteeVotingState(
	ctx context.Context,
	in CommitteeVotingInput,
) (*governance.CommitteeVotingState, error) {
	var out *governance.CommitteeVotingState
	err := o.withScratch(ctx, func(
		adapter dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedCommittee(ctx, txn, seq, in.Members); err != nil {
			return err
		}
		var err error
		out, err = governance.LoadCommitteeVotingState(
			ctx, adapter, dbadapter.Txn(txn), in.Epoch,
		)
		return err
	})
	return out, err
}

// Slots of the seeded committee rows. Members are seated at slot 0, so the
// authorization at slot 1 and the resignation at slot 2 both fall inside the
// term, and a resignation outranks the authorization it follows.
const (
	committeeAuthSlot   = 1
	committeeResignSlot = 2
)

// seedCommittee writes the members through database.SetCommitteeMembers and
// their hot key authorizations and resignations through the metadata store's
// transaction path, which is how certificates reach the committee tables.
func (o *Oracle) seedCommittee(
	ctx context.Context,
	txn *database.Txn,
	seq *uint64,
	members []CommitteeMember,
) error {
	rows := make([]*models.CommitteeMember, 0, len(members))
	for _, m := range members {
		rows = append(rows, &models.CommitteeMember{
			ColdCredentialTag: m.Tag,
			ColdCredHash:      m.Key,
			ExpiresEpoch:      m.Expiry,
			TermStartSlotSet:  true,
		})
	}
	if len(rows) > 0 {
		if err := o.db.SetCommitteeMembers(ctx, rows, txn); err != nil {
			return fmt.Errorf("seed committee: %w", err)
		}
	}
	for _, m := range members {
		cold := lcommon.Credential{CredType: uint(m.Tag), Credential: lcommon.NewBlake2b224(m.Key)}
		if m.Hot != nil {
			hot := lcommon.Credential{CredType: uint(m.Hot.Tag), Credential: lcommon.NewBlake2b224(m.Hot.Key)}
			if err := o.storeCertificate(txn, seq, committeeAuthSlot, &lcommon.AuthCommitteeHotCertificate{
				CertType:       uint(lcommon.CertificateTypeAuthCommitteeHot),
				ColdCredential: cold,
				HotCredential:  hot,
			}); err != nil {
				return fmt.Errorf("seed authorization: %w", err)
			}
		}
		if m.Resigned {
			if err := o.storeCertificate(txn, seq, committeeResignSlot, &lcommon.ResignCommitteeColdCertificate{
				CertType:       uint(lcommon.CertificateTypeResignCommitteeCold),
				ColdCredential: cold,
			}); err != nil {
				return fmt.Errorf("seed resignation: %w", err)
			}
		}
	}
	return nil
}

// storeCertificate stores cert in a synthetic Conway transaction at slot.
// The transaction fee carries the sequence number so every transaction hashes
// differently.
func (o *Oracle) storeCertificate(
	txn *database.Txn,
	seq *uint64,
	slot uint64,
	cert lcommon.Certificate,
) error {
	*seq++
	tx := &conway.ConwayTransaction{
		Body: conway.ConwayTransactionBody{
			TxFee: *seq,
			TxCertificates: []lcommon.CertificateWrapper{
				{Type: cert.Type(), Certificate: cert},
			},
		},
		TxIsValid: true,
	}
	body, err := cbor.Encode(&tx.Body)
	if err != nil {
		return err
	}
	tx.Body.SetCbor(body)
	point := ocommon.Point{Slot: slot, Hash: txHash(*seq)}
	return o.db.Metadata().SetTransaction(
		tx, point, 0, nil, false, txn.Metadata(), 10,
	)
}
