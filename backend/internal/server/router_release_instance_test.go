package server

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReleaseInstanceID(t *testing.T) {
	t.Setenv("SUB2API_INSTANCE_ID", "234-deadbeef-1")
	require.Equal(t, "234-deadbeef-1", releaseInstanceID())
}

func TestReleaseInstanceIDRejectsUnsafeValue(t *testing.T) {
	for _, value := range []string{"contains space", "line\nbreak", strings.Repeat("a", 129)} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("SUB2API_INSTANCE_ID", value)
			require.Empty(t, releaseInstanceID())
		})
	}
}
