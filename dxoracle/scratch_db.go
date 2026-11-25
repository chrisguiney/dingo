// Copyright 2026 Blink Labs Software
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package dxoracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"

	"github.com/blinklabs-io/dingo/config/cardano"
	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/plugin/blob"
	"github.com/blinklabs-io/dingo/database/plugin/blob/badger"
	immplugin "github.com/blinklabs-io/dingo/database/plugin/immutable"
	"github.com/blinklabs-io/dingo/database/plugin/immutable/chunkfile"
	"github.com/blinklabs-io/dingo/internal/nodestorage"
	"github.com/blinklabs-io/dingo/internal/utxoset"
	"github.com/blinklabs-io/dingo/plugin"
)

// badgerBlobConfig bounds the blob store's file and memtable sizes so a
// scratch database does not reserve the production sizes.
func badgerBlobConfig() map[string]any {
	return map[string]any{
		"valueLogFileSize": 16 * 1024 * 1024,
		"memTableSize":     16 * 1024 * 1024,
	}
}

// scratchLedgerStore is a ledger-role database with its live UTxO set, under
// a temporary directory that close removes.
type scratchLedgerStore struct {
	dir  string
	db   *database.Database
	set  *utxoset.Set
	host *plugin.Host
}

// openScratchLedgerStore opens a database without a metadata store, the shape
// the node's ledger database has, and the live set reconciled over it.
func openScratchLedgerStore(ctx context.Context) (s *scratchLedgerStore, err error) {
	dir, err := os.MkdirTemp("", "dxoracle-epoch-")
	if err != nil {
		return nil, err
	}
	s = &scratchLedgerStore{dir: dir}
	defer func() {
		if err != nil {
			s.close()
			s = nil
		}
	}()
	s.host = plugin.NewHost()
	if err = badger.RegisterProvider(s.host); err != nil {
		return s, err
	}
	if err = chunkfile.RegisterProvider(s.host); err != nil {
		return s, err
	}
	config := &database.Config{DataDir: dir}
	blobStore, err := plugin.Resolve[blob.BlobStore](
		ctx, s.host, plugin.CapabilityStorageBlob, "badger", badgerBlobConfig(),
		blob.ProviderDependencies{
			DataDir: dir, StorageMode: config.StorageMode, Logger: config.Logger,
		},
	)
	if err != nil {
		return s, err
	}
	immutableStore, err := plugin.Resolve[immplugin.ImmutableDB](
		ctx, s.host, plugin.CapabilityStorageImmutable, chunkfile.ProviderName, nil,
		immplugin.ProviderDependencies{DataDir: dir, Logger: config.Logger},
	)
	if err != nil {
		return s, err
	}
	s.db, err = database.New(ctx, config, database.Stores{
		Blob: blobStore, Immutable: immutableStore,
	})
	if err != nil {
		return s, err
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	s.set, err = utxoset.Open(utxoset.Config{
		Dir:           utxoset.DirFor(dir),
		Logger:        logger,
		Only:          true,
		Authoritative: true,
		MemTableSize:  8 << 20,
	})
	if err != nil {
		return s, err
	}
	err = nodestorage.Reconcile{DB: s.db, Set: s.set, Logger: logger}.Run(ctx)
	return s, err
}

// close releases the set, the database and its provider host, then removes the
// directory.
func (s *scratchLedgerStore) close() {
	var err error
	if s.set != nil {
		err = errors.Join(err, s.set.Close())
	}
	if s.db != nil {
		err = errors.Join(err, s.db.Close())
	}
	if s.host != nil {
		err = errors.Join(err, s.host.Stop(context.Background()))
	}
	_ = err
	_ = os.RemoveAll(s.dir)
}

// scratchShelleyGenesis loads the embedded complete Shelley genesis with the
// given top-level overrides into cfg.
func scratchShelleyGenesis(cfg *cardano.CardanoNodeConfig, overrides io.Reader) error {
	base, err := cardano.EmbeddedConfigFS.ReadFile("preview/shelley-genesis.json")
	if err != nil {
		return fmt.Errorf("read Shelley genesis: %w", err)
	}
	var genesis map[string]json.RawMessage
	if err := json.Unmarshal(base, &genesis); err != nil {
		return fmt.Errorf("decode Shelley genesis: %w", err)
	}
	genesis["genDelegs"] = json.RawMessage(`{}`)
	genesis["updateQuorum"] = json.RawMessage(`0`)
	raw, err := io.ReadAll(overrides)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("decode Shelley genesis overrides: %w", err)
	}
	maps.Copy(genesis, fields)
	merged, err := json.Marshal(genesis)
	if err != nil {
		return err
	}
	return cfg.LoadShelleyGenesisFromReader(bytes.NewReader(merged))
}
