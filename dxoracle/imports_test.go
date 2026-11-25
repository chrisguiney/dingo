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
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScratchPathHasNoTestImports pins that the non-test files do not link
// the standard testing package or the database and live set test fixtures.
// internal/test/conformance is not listed: the rule oracles read its state
// manager.
func TestScratchPathHasNoTestImports(t *testing.T) {
	t.Parallel()
	forbidden := []string{
		"testing",
		"github.com/stretchr/testify",
		"github.com/blinklabs-io/dingo/internal/test/dbtest",
		"github.com/blinklabs-io/dingo/internal/test/livesettest",
		"github.com/blinklabs-io/dingo/internal/test/testutil",
	}
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		require.NoError(t, err)
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			require.NoError(t, err)
			for _, bad := range forbidden {
				require.Falsef(t, path == bad || strings.HasPrefix(path, bad+"/"),
					"%s imports %s", name, path)
			}
		}
	}
}
