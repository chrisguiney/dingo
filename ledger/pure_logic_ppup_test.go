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

package ledger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassicPParamUpdateWindow(t *testing.T) {
	t.Parallel()
	// Epoch 5 of 100 slots starting at 500. A stability window of 10 puts the
	// point of no return at 500+100-2*10 = 580.
	epoch, noReturn := ClassicPParamUpdateWindow(5, 500, 100, 10)
	require.Equal(t, uint64(5), epoch)
	require.Equal(t, uint64(580), noReturn)

	// A window covering the whole epoch makes every slot target the next epoch.
	_, noReturn = ClassicPParamUpdateWindow(5, 500, 100, 50)
	require.Equal(t, uint64(500), noReturn)
	_, noReturn = ClassicPParamUpdateWindow(5, 500, 100, ^uint64(0))
	require.Equal(t, uint64(500), noReturn)
}
