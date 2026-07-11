package admin

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeySuffixNeverReturnsShortSecret(t *testing.T) {
	require.Equal(t, "***", keySuffix("abc123"))
	require.Equal(t, "456789", keySuffix("sk-123456789"))
}
