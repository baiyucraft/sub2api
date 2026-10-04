package migrations

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfile261MigrationRawByteContracts(t *testing.T) {
	for filename, checksum := range map[string]string{
		"286_add_payment_order_bonus_amount.sql": "ddd45f14154ea6d6b5b612bee7eb49436e1f2a1665f9a71eb8304e48475ed12d",
		"287_add_typesafe_platform.sql":          "419fceaa3e6f1bd089a454fda30b9d343a83fff75ad3b6dffdc44287bf99d171",
	} {
		t.Run(filename, func(t *testing.T) {
			content, err := FS.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, checksum, fmt.Sprintf("%x", sha256.Sum256(content)))
		})
	}
	for _, filename := range []string{"241_add_payment_order_bonus_amount.sql", "241_add_typesafe_platform.sql"} {
		_, err := FS.ReadFile(filename)
		require.ErrorIs(t, err, fs.ErrNotExist)
	}
	_, err := FS.ReadFile("241_precise_upstream_effective_rate.sql")
	require.NoError(t, err)
}

func TestProfile261PaymentBonusMigrationContract(t *testing.T) {
	content, err := FS.ReadFile("286_add_payment_order_bonus_amount.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")
	require.Contains(t, sql, "ALTER TABLE payment_orders ADD COLUMN IF NOT EXISTS bonus_amount DECIMAL(20,2) NOT NULL DEFAULT 0;")
	require.NotContains(t, strings.ToUpper(sql), "UPDATE PAYMENT_ORDERS")
}
