package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile286OfficialUltrafastPricingMigration(t *testing.T) {
	raw, err := FS.ReadFile("313_channel_pricing_ultrafast_multiplier.sql")
	require.NoError(t, err)
	require.Equal(t, "01eb249a933e5c3bef3a8e2106098abf56d20a5f2890fcc6490b1f83ec05687f", fmt.Sprintf("%x", sha256.Sum256(raw)))

	_, err = FS.ReadFile("252_channel_pricing_ultrafast_multiplier.sql")
	require.ErrorIs(t, err, fs.ErrNotExist)
	_, err = FS.ReadFile("252_channel_monitor_streaming_ttft.sql")
	require.NoError(t, err)
}
