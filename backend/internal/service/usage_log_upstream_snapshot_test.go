package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyUpstreamUsageSnapshot(t *testing.T) {
	t.Run("complete binding", func(t *testing.T) {
		configID := int64(11)
		keyID := int64(22)
		log := &UsageLog{}
		account := &Account{UpstreamConfigID: &configID, UpstreamKeyID: &keyID}

		ApplyUpstreamUsageSnapshot(log, account)
		configID = 111
		keyID = 222

		require.Equal(t, int64(11), *log.UpstreamConfigID)
		require.Equal(t, int64(22), *log.UpstreamKeyID)
		require.Equal(t, "CNY", *log.UpstreamCostCurrency)
		require.Equal(t, 1.0, *log.UpstreamCostToCNYRate)
	})

	for _, tt := range []struct {
		name     string
		configID *int64
		keyID    *int64
	}{
		{name: "unbound"},
		{name: "config only", configID: snapshotInt64Ptr(11)},
		{name: "key only", keyID: snapshotInt64Ptr(22)},
		{name: "zero config", configID: snapshotInt64Ptr(0), keyID: snapshotInt64Ptr(22)},
		{name: "zero key", configID: snapshotInt64Ptr(11), keyID: snapshotInt64Ptr(0)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			log := &UsageLog{}
			ApplyUpstreamUsageSnapshot(log, &Account{UpstreamConfigID: tt.configID, UpstreamKeyID: tt.keyID})
			require.Nil(t, log.UpstreamConfigID)
			require.Nil(t, log.UpstreamKeyID)
			require.Nil(t, log.UpstreamCostCurrency)
			require.Nil(t, log.UpstreamCostToCNYRate)
		})
	}

	t.Run("nil inputs", func(t *testing.T) {
		require.NotPanics(t, func() {
			ApplyUpstreamUsageSnapshot(nil, &Account{})
			ApplyUpstreamUsageSnapshot(&UsageLog{}, nil)
		})
	})
}

func snapshotInt64Ptr(value int64) *int64 { return &value }
