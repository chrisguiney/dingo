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

package conformance

import (
	"github.com/blinklabs-io/dingo/database/ledgerdb"
	dledger "github.com/blinklabs-io/dingo/ledger"
)

// SetSeedHook registers fn to run inside the import transaction of the next
// LoadInitialState calls, after accounts and before pools. It lets a caller
// write pool and DRep rows with the parameters and anchors a
// ParsedInitialState cannot carry, through the same ImportLedgerDB writers
// the initial state uses.
func (m *DingoStateManager) SetSeedHook(
	fn func(*dledger.ImportLedgerDB, ledgerdb.Txn) error,
) {
	m.seedHook = fn
}

// LedgerDB returns the ledger database the manager reads and writes, for
// callers that read back the rows a transaction left.
func (m *DingoStateManager) LedgerDB() *dledger.ImportLedgerDB {
	return m.ldb
}

// SetNetworkID sets the network the provider reports to the rules: 0 for
// testnet, 1 for mainnet. Conformance vectors run on testnet, the default.
func (m *DingoStateManager) SetNetworkID(id uint) {
	m.networkID = id
}
