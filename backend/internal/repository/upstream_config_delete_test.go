package repository

import (
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSummarizeUpstreamConfigBindingsProtectsManualUnknownAndBadKeys(t *testing.T) {
	configID := int64(10)
	otherConfigID := int64(11)
	validKeyID := int64(20)
	deletedKeyID := int64(21)
	otherKeyID := int64(22)
	keys := []*dbent.UpstreamKey{
		{ID: validKeyID, UpstreamConfigID: configID},
		{ID: deletedKeyID, UpstreamConfigID: configID, DeletedAt: func() *time.Time { value := time.Unix(1, 0); return &value }()},
		{ID: otherKeyID, UpstreamConfigID: otherConfigID},
	}
	accounts := map[int64]*dbent.Account{
		1: {ID: 1, UpstreamConfigID: &configID, UpstreamKeyID: &validKeyID, UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
		2: {ID: 2, UpstreamConfigID: &configID, UpstreamKeyID: &validKeyID, UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerManual},
		3: {ID: 3, UpstreamConfigID: &configID, UpstreamKeyID: &deletedKeyID, UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
		4: {ID: 4, UpstreamConfigID: &configID, UpstreamKeyID: &otherKeyID, UpstreamLifecycleOwner: "legacy"},
		5: {ID: 5, UpstreamConfigID: &configID, UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
		6: {ID: 6, UpstreamConfigID: &otherConfigID, UpstreamKeyID: &validKeyID, UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
	}

	summary := summarizeUpstreamConfigBindings(configID, keys, accounts)
	require.Equal(t, service.UpstreamConfigBindingSummary{
		BoundAccountCount:       6,
		ManualAccountCount:      2,
		MissingKeyAccountCount:  4,
		SyncManagedAccountCount: 4,
	}, summary)
}
