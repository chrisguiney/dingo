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

package eras

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/allegra"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/stretchr/testify/require"
)

// Each pre-Conway era's rule list reports a deregistration of an unregistered
// credential from its delegation rule alone: selecting the pool family runs
// nothing, selecting the delegation family reports the failure with its
// family, and no selection runs every rule of the era.
func TestLegacyRuleFailuresSelectByFamily(t *testing.T) {
	t.Parallel()
	for _, era := range []struct {
		name string
		id   uint
		pp   lcommon.ProtocolParameters
	}{
		{"Shelley", shelley.EraIdShelley, majorPParams{major: lcommon.ProtocolVersionShelley}},
		{"Allegra", allegra.EraIdAllegra, majorPParams{major: lcommon.ProtocolVersionAllegra}},
		{"Mary", mary.EraIdMary, majorPParams{major: lcommon.ProtocolVersionMary}},
		{"Alonzo", alonzo.EraIdAlonzo, majorPParams{major: lcommon.ProtocolVersionAlonzo}},
		{"Babbage", babbage.EraIdBabbage, majorPParams{major: lcommon.ProtocolVersionBabbage}},
	} {
		t.Run(era.name, func(t *testing.T) {
			t.Parallel()
			tx := &mirTx{certs: []lcommon.Certificate{stakeDereg(mirCredential(0x31))}}
			ls := newMockLedgerState()

			got, ok := LegacyRuleFailures(era.id, tx, 100, ls, era.pp, lcommon.UtxoFamilyPool)
			require.True(t, ok)
			require.Empty(t, got)

			got, ok = LegacyRuleFailures(era.id, tx, 100, ls, era.pp, lcommon.UtxoFamilyDeleg)
			require.True(t, ok)
			require.Len(t, got, 1)
			require.Equal(t, lcommon.UtxoFamilyDeleg, got[0].Family)
			require.Equal(t, lcommon.UtxoValidationRuleDelegation, got[0].RuleId)
			require.ErrorAs(t, got[0].Err, new(shelley.StakeKeyNotRegisteredError))
		})
	}
}

// An era with no pre-Conway rule list is reported rather than run.
func TestLegacyRuleFailuresReportsOtherEras(t *testing.T) {
	t.Parallel()
	_, ok := LegacyRuleFailures(99, &mirTx{}, 100, newMockLedgerState(), majorPParams{}, lcommon.UtxoFamilyDeleg)
	require.False(t, ok)
}
