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

// Package dxoracle runs dingo's SQL-backed ledger queries against a scratch
// database so a differential-testing harness can call the code production
// runs. Each call seeds its input rows through dingo's own write paths, runs
// the production loader, reads the result and discards the seeded state.
package dxoracle

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/blinklabs-io/dingo/database"
	"github.com/blinklabs-io/dingo/database/ledgerdb/dbadapter"
	"github.com/blinklabs-io/dingo/database/plugin/blob"
	"github.com/blinklabs-io/dingo/database/plugin/blob/badger"
	immplugin "github.com/blinklabs-io/dingo/database/plugin/immutable"
	"github.com/blinklabs-io/dingo/database/plugin/immutable/chunkfile"
	"github.com/blinklabs-io/dingo/database/plugin/metadata"
	"github.com/blinklabs-io/dingo/database/plugin/metadata/sqlite"
	"github.com/blinklabs-io/dingo/internal/test/testutil"
	"github.com/blinklabs-io/dingo/plugin"
	_ "modernc.org/sqlite"
)

// Oracle is a scratch database that serves one call at a time. It is safe for
// concurrent use; calls are serialised because each holds the metadata
// store's single write transaction.
type Oracle struct {
	mu   sync.Mutex
	db   *database.Database
	host *plugin.Host
	// seq numbers the synthetic transaction hashes of seeded UTxOs within
	// the process, so two rows never share an output reference.
	seq uint64
}

// Open creates a scratch database under dataDir, which must be empty or absent
// and is owned by the Oracle until Close. The database holds the metadata
// store with the account, utxo, drep and governance tables (database.RoleReleased
// over SQLite metadata and Badger blob storage).
func Open(dataDir string) (*Oracle, error) {
	if dataDir == "" {
		return nil, errors.New("dxoracle: data directory is required")
	}
	ctx := context.Background()
	config := database.Config{
		DataDir:     dataDir,
		Role:        database.RoleReleased,
		StorageMode: database.DefaultConfig.StorageMode,
		Logger:      database.DefaultConfig.Logger,
	}
	host := plugin.NewHost()
	for _, register := range []func(*plugin.Host) error{
		badger.RegisterProvider,
		sqlite.RegisterProvider,
		chunkfile.RegisterProvider,
	} {
		if err := register(host); err != nil {
			_ = host.Stop(ctx)
			return nil, fmt.Errorf("dxoracle: register provider: %w", err)
		}
	}
	fail := func(what string, err error) (*Oracle, error) {
		_ = host.Stop(ctx)
		return nil, fmt.Errorf("dxoracle: %s: %w", what, err)
	}
	blobStore, err := plugin.Resolve[blob.BlobStore](
		ctx, host, plugin.CapabilityStorageBlob, "badger",
		testutil.BadgerBlobConfig(),
		blob.ProviderDependencies{
			DataDir: dataDir, StorageMode: config.StorageMode,
			Logger: config.Logger,
		},
	)
	if err != nil {
		return fail("resolve blob store", err)
	}
	metadataStore, err := plugin.Resolve[metadata.MetadataStore](
		ctx, host, plugin.CapabilityStorageMetadata, "sqlite", nil,
		metadata.ProviderDependencies{
			DataDir: dataDir, StorageMode: config.StorageMode,
			Logger: config.Logger,
		},
	)
	if err != nil {
		return fail("resolve metadata store", err)
	}
	immutableStore, err := plugin.Resolve[immplugin.ImmutableDB](
		ctx, host, plugin.CapabilityStorageImmutable, chunkfile.ProviderName,
		nil,
		immplugin.ProviderDependencies{DataDir: dataDir, Logger: config.Logger},
	)
	if err != nil {
		return fail("resolve immutable store", err)
	}
	db, err := database.New(ctx, &config, database.Stores{
		Blob: blobStore, Metadata: metadataStore, Immutable: immutableStore,
	})
	if db == nil {
		return fail("open database", err)
	}
	if err != nil {
		_ = db.Close()
		return fail("open database", err)
	}
	return &Oracle{db: db, host: host}, nil
}

// Close closes the database and its provider host.
func (o *Oracle) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.db == nil {
		return nil
	}
	err := o.db.Close()
	err = errors.Join(err, o.host.Stop(context.Background()))
	o.db = nil
	return err
}

// withScratch runs fn in a read-write transaction and always rolls it back, so
// nothing seeded by fn outlives the call.
func (o *Oracle) withScratch(
	ctx context.Context,
	fn func(adapter dbadapter.Adapter, txn *database.Txn, seq *uint64) error,
) (err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.db == nil {
		return errors.New("dxoracle: closed")
	}
	txn := o.db.Transaction(ctx, true)
	defer func() {
		if rbErr := txn.Rollback(); rbErr != nil {
			err = errors.Join(err, fmt.Errorf("dxoracle: rollback: %w", rbErr))
		}
	}()
	return fn(dbadapter.New(o.db), txn, &o.seq)
}

// txHash returns a unique synthetic 32-byte transaction hash.
func txHash(n uint64) []byte {
	h := make([]byte, 32)
	binary.BigEndian.PutUint64(h[24:], n)
	return h
}
