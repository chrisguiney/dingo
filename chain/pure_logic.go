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

import "bytes"

// FitsTip reports whether a block or header whose previous-block hash is
// prevHash extends a chain tip with hash tipHash. The hashes are compared as
// bytes; a tip hash of a different length never matches.
func FitsTip(prevHash, tipHash []byte) bool {
	return bytes.Equal(prevHash, tipHash)
}
