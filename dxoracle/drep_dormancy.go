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
)

// DRepDormancyInput seeds DReps and the number of dormant epoch boundaries
// to run.
type DRepDormancyInput struct {
	Dreps         []DRep
	DormantEpochs uint64
}

// DRepDormancy seeds the DReps, then runs the epoch-boundary dormancy bump of
// dingo's metadata store once per dormant epoch (each at its own slot) and
// returns the stored DRep expiries, in input order.
func (o *Oracle) DRepDormancy(ctx context.Context, in DRepDormancyInput) ([]DRep, error) {
	out := make([]DRep, len(in.Dreps))
	err := o.withScratch(ctx, func(
		_ dbadapter.Adapter, txn *database.Txn, seq *uint64,
	) error {
		if err := o.seedDRepStake(ctx, txn, seq, DRepStakeDistributionInput{Dreps: in.Dreps}); err != nil {
			return err
		}
		for slot := uint64(1); slot <= in.DormantEpochs; slot++ {
			if _, err := o.db.BumpDormantDRepExpiries(ctx, slot, txn); err != nil {
				return fmt.Errorf("dormancy bump at slot %d: %w", slot, err)
			}
		}
		for i, d := range in.Dreps {
			row, err := o.db.GetDrep(ctx, d.Key, true, txn)
			if err != nil {
				return fmt.Errorf("read drep: %w", err)
			}
			out[i] = d
			out[i].ExpiryEpoch = row.ExpiryEpoch
		}
		return nil
	})
	return out, err
}
