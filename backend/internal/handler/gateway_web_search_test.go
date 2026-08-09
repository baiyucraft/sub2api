package handler

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeGrokWebSearchQuery(t *testing.T) {
	query, err := normalizeGrokWebSearchQuery("  hello  ")
	require.NoError(t, err)
	require.Equal(t, "hello", query)

	_, err = normalizeGrokWebSearchQuery(" \t\n ")
	require.ErrorIs(t, err, errGrokWebSearchQueryRequired)

	_, err = normalizeGrokWebSearchQuery(strings.Repeat("x", maxGrokWebSearchQueryBytes+1))
	require.True(t, errors.Is(err, errGrokWebSearchQueryTooLarge))
}
