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

package byronupdate

import (
	"bytes"
	"maps"
	"slices"

	"github.com/blinklabs-io/dingo/ledger/eras"
)

// Endorsement is a genesis key's endorsement of a protocol version
// (Endorsement in Cardano.Chain.Update.Validation.Endorsement).
type Endorsement struct {
	Version ProtocolVersion
	KeyHash KeyHash
}

// ApplicationVersion is a registered software version and the slot that
// confirmed it.
type ApplicationVersion struct {
	Version uint32
	Slot    uint64
}

// ProtocolProposal is a registered protocol update proposal
// (ProtocolUpdateProposal): the version it proposes and the parameters that
// version would adopt.
type ProtocolProposal struct {
	Version ProtocolVersion
	Params  *eras.ByronProtocolParameters
}

// SoftwareProposal is a registered software update proposal.
type SoftwareProposal struct {
	AppName string
	Version uint32
}

// StateParts is the update interface state (UPI.State) as plain data. It is
// the exchange form for callers that need to build a State at an arbitrary
// point, or read one back, without replaying the chain.
type StateParts struct {
	AdoptedVersion ProtocolVersion
	AdoptedParams  *eras.ByronProtocolParameters
	// Candidates is ordered newest first.
	Candidates        []Candidate
	AppVersions       map[string]ApplicationVersion
	ProtocolProposals map[UpId]ProtocolProposal
	SoftwareProposals map[UpId]SoftwareProposal
	Confirmed         map[UpId]uint64
	// Votes lists, per proposal, the genesis key hashes that voted for it,
	// sorted.
	Votes map[UpId][]KeyHash
	// Endorsements is sorted by version, then key hash.
	Endorsements     []Endorsement
	RegistrationSlot map[UpId]uint64
	// LastSlot is the slot of the last applied block.
	LastSlot uint64
}

// NewStateFromParts returns the state described by parts. Nothing in the
// result aliases parts. Block-number tracking starts empty, as in a state
// that has applied no block.
func NewStateFromParts(parts StateParts) State {
	ret := State{
		adoptedVersion:    parts.AdoptedVersion,
		adoptedParams:     parts.AdoptedParams.Clone(),
		appVersions:       make(map[string]applicationVersion),
		protocolProposals: make(map[UpId]protocolUpdateProposal),
		softwareProposals: make(map[UpId]softwareUpdateProposal),
		confirmed:         maps.Clone(parts.Confirmed),
		votes:             make(map[UpId]map[KeyHash]struct{}),
		endorsements:      make(map[endorsement]struct{}),
		registrationSlot:  maps.Clone(parts.RegistrationSlot),
		candidateBlockNo:  make(map[ProtocolVersion]uint64),
		lastSlot:          parts.LastSlot,
		initialized:       true,
	}
	for _, c := range parts.Candidates {
		ret.candidates = append(ret.candidates, Candidate{
			Slot: c.Slot, Version: c.Version, Params: c.Params.Clone(),
		})
	}
	for name, v := range parts.AppVersions {
		ret.appVersions[name] = applicationVersion{version: v.Version, slot: v.Slot}
	}
	for id, p := range parts.ProtocolProposals {
		ret.protocolProposals[id] = protocolUpdateProposal{
			version: p.Version, params: p.Params.Clone(),
		}
	}
	for id, p := range parts.SoftwareProposals {
		ret.softwareProposals[id] = softwareUpdateProposal{
			appName: p.AppName, version: p.Version,
		}
	}
	for id, voters := range parts.Votes {
		set := make(map[KeyHash]struct{}, len(voters))
		for _, v := range voters {
			set[v] = struct{}{}
		}
		ret.votes[id] = set
	}
	for _, e := range parts.Endorsements {
		ret.endorsements[endorsement{version: e.Version, keyHash: e.KeyHash}] = struct{}{}
	}
	return ret.clone()
}

// Parts returns the state as plain data. The result shares nothing mutable
// with s.
func (s State) Parts() StateParts {
	parts := StateParts{
		AdoptedVersion:    s.adoptedVersion,
		AdoptedParams:     s.adoptedParams.Clone(),
		Candidates:        s.Candidates(),
		AppVersions:       make(map[string]ApplicationVersion, len(s.appVersions)),
		ProtocolProposals: make(map[UpId]ProtocolProposal, len(s.protocolProposals)),
		SoftwareProposals: make(map[UpId]SoftwareProposal, len(s.softwareProposals)),
		Confirmed:         make(map[UpId]uint64, len(s.confirmed)),
		Votes:             make(map[UpId][]KeyHash, len(s.votes)),
		RegistrationSlot:  make(map[UpId]uint64, len(s.registrationSlot)),
		LastSlot:          s.lastSlot,
	}
	for name, v := range s.appVersions {
		parts.AppVersions[name] = ApplicationVersion{Version: v.version, Slot: v.slot}
	}
	for id, p := range s.protocolProposals {
		parts.ProtocolProposals[id] = ProtocolProposal{
			Version: p.version, Params: p.params.Clone(),
		}
	}
	for id, p := range s.softwareProposals {
		parts.SoftwareProposals[id] = SoftwareProposal{
			AppName: p.appName, Version: p.version,
		}
	}
	maps.Copy(parts.Confirmed, s.confirmed)
	maps.Copy(parts.RegistrationSlot, s.registrationSlot)
	for id, voters := range s.votes {
		list := make([]KeyHash, 0, len(voters))
		for v := range voters {
			list = append(list, v)
		}
		slices.SortFunc(list, func(a, b KeyHash) int {
			return bytes.Compare(a[:], b[:])
		})
		parts.Votes[id] = list
	}
	for e := range s.endorsements {
		parts.Endorsements = append(parts.Endorsements, Endorsement{
			Version: e.version, KeyHash: e.keyHash,
		})
	}
	slices.SortFunc(parts.Endorsements, func(a, b Endorsement) int {
		if a.Version != b.Version {
			if a.Version.Less(b.Version) {
				return -1
			}
			return 1
		}
		return bytes.Compare(a.KeyHash[:], b.KeyHash[:])
	})
	return parts
}
