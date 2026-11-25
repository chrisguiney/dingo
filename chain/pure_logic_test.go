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

package chain

import (
	"bytes"
	"testing"
)

func TestFitsTip(t *testing.T) {
	t.Parallel()
	h := bytes.Repeat([]byte{0xab}, 32)
	other := append(bytes.Repeat([]byte{0xab}, 31), 0xac)
	for name, tc := range map[string]struct {
		prev, tip []byte
		want      bool
	}{
		"equal hashes":        {h, h, true},
		"last byte differs":   {h, other, false},
		"prefix of the tip":   {h[:31], h, false},
		"tip a prefix":        {h, h[:31], false},
		"both empty":          {nil, nil, true},
		"empty prev, set tip": {nil, h, false},
		"set prev, empty tip": {h, nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := FitsTip(tc.prev, tc.tip); got != tc.want {
				t.Fatalf("FitsTip = %v, want %v", got, tc.want)
			}
		})
	}
}
