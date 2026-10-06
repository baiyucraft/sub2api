//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestConfidenceDistributionPostgresCrossInstanceClaim(t *testing.T) {
	ctx := context.Background()
	config, err := integrationEntClient.UpstreamConfig.Create().
		SetName(fmt.Sprintf("distribution-%d", time.Now().UnixNano())).
		SetProvider(service.UpstreamProviderSub2API).SetSiteURL("https://example.com").
		SetAuthMode(service.UpstreamAuthModeManualJWT).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = integrationDB.Exec(`DELETE FROM upstream_configs WHERE id=$1`, config.ID) })
	key, err := integrationEntClient.UpstreamKey.Create().
		SetUpstreamConfigID(config.ID).SetName("distribution-test").
		SetKey("fixture-key").SetKeyHash(service.HashUpstreamKey("fixture-key")).Save(ctx)
	require.NoError(t, err)
	repo := &upstreamConfigRepository{client: integrationEntClient}
	testConfidenceDistributionConcurrentClaims(t, repo, integrationDB, key.ID)
}
