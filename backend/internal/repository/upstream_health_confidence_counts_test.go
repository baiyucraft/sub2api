package repository

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeConfidenceCountsPreservesLegacyOverflow(t *testing.T) {
	for _, values := range [][]int64{
		{0, 1, -1}, {math.MaxInt64}, {math.MinInt64},
		{math.MaxInt64, 1}, {math.MinInt64, -1},
		{math.MaxInt64, math.MaxInt64, math.MinInt64, -7, 23},
		{math.MinInt64, math.MinInt64, math.MinInt64},
	} {
		var legacy int
		exact := new(big.Int)
		for _, value := range values {
			legacy += int(value)
			exact.Add(exact, big.NewInt(value))
		}
		actual, err := decodeConfidenceCounts([]byte("[" + exact.String() + "]"))
		require.NoError(t, err)
		require.Equal(t, []int{legacy}, actual, "same sequence %v", values)
	}
	for _, raw := range []string{"[0,-1,1,null]", "[]", "null"} {
		var legacy []int
		require.NoError(t, json.Unmarshal([]byte(raw), &legacy))
		actual, err := decodeConfidenceCounts([]byte(raw))
		require.NoError(t, err)
		if legacy == nil {
			require.Empty(t, actual)
		} else {
			require.Equal(t, legacy, actual)
		}
	}
	for _, raw := range []string{`[1.0]`, `["1"]`, `[true]`, `[{}]`, `not-json`} {
		_, err := decodeConfidenceCounts([]byte(raw))
		require.Error(t, err, raw)
	}
}
