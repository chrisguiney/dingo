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

package database

import (
	"github.com/blinklabs-io/dingo/database/models"
	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
)

// ClassicPParamSelection is the update the Shelley update system enacts at an
// epoch boundary, before it is applied to any parameters.
type ClassicPParamSelection struct {
	// Update is the decoded update value chosen by decodeFunc.
	Update any
	// Votes is the number of genesis keys that proposed it.
	Votes int
}

// SelectClassicPParamUpdate returns the proposed update the Shelley update
// system enacts at the boundary into enactEpoch, or nil when none is enacted.
// rows are the stored proposals; IDs must increase in storage order.
// submissionEpochStart is the first slot of epoch enactEpoch-1 and
// currentPParams the parameters in force during it. quorum is the genesis
// update quorum. See selectClassicPParamUpdate for the reference rules.
func SelectClassicPParamUpdate(
	rows []models.PParamUpdate,
	enactEpoch uint64,
	submissionEpochStart uint64,
	quorum int,
	currentPParams lcommon.ProtocolParameters,
	decodeFunc func([]byte) (any, error),
) (*ClassicPParamSelection, error) {
	selected, err := selectClassicPParamUpdate(
		rows, enactEpoch, submissionEpochStart, quorum,
		currentPParams, decodeFunc,
	)
	if err != nil || selected == nil {
		return nil, err
	}
	return &ClassicPParamSelection{
		Update: selected.update,
		Votes:  selected.votes,
	}, nil
}

// ClassicPParamQuorumPossible counts the distinct genesis keys with a stored
// proposal for submissionEpoch and reports whether that count can reach
// quorum. Fewer than quorum proposers proves nothing is enacted, so no
// proposal needs decoding.
func ClassicPParamQuorumPossible(
	rows []models.PParamUpdate,
	submissionEpoch uint64,
	quorum int,
) (proposers int, possible bool) {
	proposers = classicPParamProposers(rows, submissionEpoch)
	return proposers, proposers > 0 && proposers >= quorum
}
