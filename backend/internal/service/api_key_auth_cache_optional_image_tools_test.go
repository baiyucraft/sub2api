package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthSnapshotOptionalImageToolsRoundtrip(t *testing.T) {
	groupID := int64(50)
	apiKey := &APIKey{
		ID: 82, UserID: 40, GroupID: &groupID, Key: "sk-image-tool-roundtrip", Status: StatusActive,
		User: &User{ID: 40, Status: StatusActive},
		Group: &Group{
			ID: groupID, Name: "image-tool-policy", Platform: PlatformOpenAI, Status: StatusActive,
			Hydrated: true, AllowImageGeneration: true, StripOptionalImageTools: true, ImageCountBilling: true,
		},
	}
	svc := &APIKeyService{}
	payload, err := json.Marshal(&APIKeyAuthCacheEntry{Snapshot: svc.snapshotFromAPIKey(context.Background(), apiKey)})
	require.NoError(t, err)
	var cached APIKeyAuthCacheEntry
	require.NoError(t, json.Unmarshal(payload, &cached))
	require.Equal(t, 26, cached.Snapshot.Version)
	materialized, used, err := svc.applyAuthCacheEntry(apiKey.Key, &cached)
	require.NoError(t, err)
	require.True(t, used)
	require.True(t, materialized.Group.StripOptionalImageTools)
	require.True(t, materialized.Group.AllowImageGeneration)
	require.True(t, materialized.Group.ImageCountBilling)

	for _, version := range []int{24, 25} {
		cached.Snapshot.Version = version
		materialized, used, err = svc.applyAuthCacheEntry(apiKey.Key, &cached)
		require.NoError(t, err)
		require.False(t, used, "snapshots without both image policies must be reloaded")
		require.Nil(t, materialized)
	}
}
