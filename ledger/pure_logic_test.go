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
	"bytes"
	"math"
	"math/big"
	"testing"

	lcommon "github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pureLogicByronAddrs(t *testing.T) (redeem, pubkey lcommon.Address) {
	t.Helper()
	redeem, err := lcommon.NewByronAddressRedeem(
		bytes.Repeat([]byte{0x42}, 32),
		lcommon.ByronAddressAttributes{},
	)
	require.NoError(t, err)
	pubkey, err = lcommon.NewByronAddressFromParts(
		lcommon.ByronAddressTypePubkey,
		bytes.Repeat([]byte{0x55}, lcommon.AddressHashSize),
		lcommon.ByronAddressAttributes{},
	)
	require.NoError(t, err)
	return redeem, pubkey
}

func TestClassifyAvvmOutput(t *testing.T) {
	t.Parallel()
	redeem, pubkey := pureLogicByronAddrs(t)

	isAvvm, lovelace, err := ClassifyAvvmOutput(redeem, big.NewInt(25))
	require.NoError(t, err)
	assert.True(t, isAvvm)
	assert.Equal(t, uint64(25), lovelace)

	isAvvm, lovelace, err = ClassifyAvvmOutput(pubkey, big.NewInt(25))
	require.NoError(t, err)
	assert.False(t, isAvvm)
	assert.Equal(t, uint64(0), lovelace)

	_, _, err = ClassifyAvvmOutput(redeem, big.NewInt(-1))
	require.Error(t, err)
	_, _, err = ClassifyAvvmOutput(redeem, nil)
	require.Error(t, err)
	// A non-AVVM output is ignored whatever its amount.
	isAvvm, _, err = ClassifyAvvmOutput(pubkey, big.NewInt(-1))
	require.NoError(t, err)
	assert.False(t, isAvvm)
}

func TestComputeAvvmReturn(t *testing.T) {
	t.Parallel()
	redeem, pubkey := pureLogicByronAddrs(t)
	removed, total, reserves, err := ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: big.NewInt(10)},
		{Address: pubkey, Amount: big.NewInt(99)},
		{Address: redeem, Amount: big.NewInt(5)},
	}, 1000)
	require.NoError(t, err)
	assert.Equal(t, []int{0, 2}, removed)
	assert.Equal(t, uint64(15), total)
	assert.Equal(t, uint64(1015), reserves)

	// Nothing to return leaves reserves alone.
	removed, total, reserves, err = ComputeAvvmReturn(
		[]AvvmOutput{{Address: pubkey, Amount: big.NewInt(1)}}, 7,
	)
	require.NoError(t, err)
	assert.Empty(t, removed)
	assert.Equal(t, uint64(0), total)
	assert.Equal(t, uint64(7), reserves)
}

func TestComputeAvvmReturnOverflow(t *testing.T) {
	t.Parallel()
	redeem, _ := pureLogicByronAddrs(t)
	_, _, _, err := ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: new(big.Int).SetUint64(math.MaxUint64)},
		{Address: redeem, Amount: big.NewInt(1)},
	}, 0)
	require.ErrorContains(t, err, "AVVM lovelace total overflows uint64")

	_, _, _, err = ComputeAvvmReturn([]AvvmOutput{
		{Address: redeem, Amount: big.NewInt(2)},
	}, math.MaxUint64-1)
	require.ErrorContains(t, err, "AVVM reserve credit overflows uint64")
}
