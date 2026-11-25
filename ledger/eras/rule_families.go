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
	"github.com/blinklabs-io/gouroboros/ledger/allegra"
	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
)

// ConwayRuleFailures runs the phase-1 rules ValidateTxConway runs, with
// Dingo's replacements for the committee, delegation and size rules, and
// reports every failure instead of joining them. When families is non-empty
// only rules of those STS families run.
func ConwayRuleFailures(
	tx lcommon.Transaction,
	slot uint64,
	ls lcommon.LedgerState,
	pp lcommon.ProtocolParameters,
	families ...lcommon.UtxoValidationFamily,
) []lcommon.UtxoValidationFailure {
	ls = lcommon.NewTransactionScopedLedgerState(ls)
	descriptors := conway.UtxoValidationRuleDescriptors()
	var failures []lcommon.UtxoValidationFailure
	for _, rule := range conwayValidationRules(ls) {
		descriptor := descriptors[rule.index]
		family, _ := lcommon.UtxoValidationRuleFamily(descriptor.Id)
		if len(families) > 0 && !familyIn(family, families) {
			continue
		}
		if err := rule.validationFunc(tx, slot, ls, pp); err != nil {
			failures = append(failures, lcommon.UtxoValidationFailure{
				RuleIndex: rule.index,
				RuleId:    descriptor.Id,
				Family:    family,
				Err:       err,
			})
		}
	}
	return failures
}

// LegacyRuleFailures runs the rule list the production validator of a
// Shelley-family era (Shelley, Allegra, Mary, Alonzo or Babbage) runs and
// reports every failure instead of joining them. When families is non-empty
// only rules of those STS families run. The second result is false when
// eraID has no such rule list.
func LegacyRuleFailures(
	eraID uint,
	tx lcommon.Transaction,
	slot uint64,
	ls lcommon.LedgerState,
	pp lcommon.ProtocolParameters,
	families ...lcommon.UtxoValidationFamily,
) ([]lcommon.UtxoValidationFailure, bool) {
	var (
		rules       []indexedUtxoValidationRule
		descriptors []lcommon.UtxoValidationRuleDescriptor
	)
	switch eraID {
	case shelley.EraIdShelley:
		rules, descriptors = shelleyUtxoValidationRules, shelley.UtxoValidationRuleDescriptors()
	case allegra.EraIdAllegra:
		rules, descriptors = allegraUtxoValidationRules, allegra.UtxoValidationRuleDescriptors()
	case mary.EraIdMary:
		// ValidateTxMary runs the upstream list as it is.
		for i, f := range mary.UtxoValidationRules {
			rules = append(rules, indexedUtxoValidationRule{index: i, validationFunc: f})
		}
		descriptors = mary.UtxoValidationRuleDescriptors()
	case alonzo.EraIdAlonzo:
		rules, descriptors = alonzoUtxoValidationRules, alonzo.UtxoValidationRuleDescriptors()
	case babbage.EraIdBabbage:
		rules, descriptors = babbageUtxoValidationRules, babbage.UtxoValidationRuleDescriptors()
	default:
		return nil, false
	}
	ls = lcommon.NewTransactionScopedLedgerState(ls)
	var failures []lcommon.UtxoValidationFailure
	for _, rule := range rules {
		descriptor := descriptors[rule.index]
		family, _ := lcommon.UtxoValidationRuleFamily(descriptor.Id)
		if len(families) > 0 && !familyIn(family, families) {
			continue
		}
		if err := rule.validationFunc(tx, slot, ls, pp); err != nil {
			failures = append(failures, lcommon.UtxoValidationFailure{
				RuleIndex: rule.index,
				RuleId:    descriptor.Id,
				Family:    family,
				Err:       err,
			})
		}
	}
	return failures, true
}

func familyIn(
	family lcommon.UtxoValidationFamily,
	families []lcommon.UtxoValidationFamily,
) bool {
	for _, f := range families {
		if f == family {
			return true
		}
	}
	return false
}
