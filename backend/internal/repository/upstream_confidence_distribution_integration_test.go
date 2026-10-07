//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func newConfidenceDistributionPostgresFixture(t *testing.T) (*upstreamConfigRepository, *sql.DB, int64) {
	t.Helper()
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
	return repo, integrationDB, key.ID
}

func TestConfidenceDistributionPostgresCrossInstanceClaim(t *testing.T) {
	repo, db, keyID := newConfidenceDistributionPostgresFixture(t)
	testConfidenceDistributionConcurrentClaims(t, repo, db, keyID)
}

func TestConfidenceDistributionPostgresIdentityLifecycle(t *testing.T) {
	repo, db, keyID := newConfidenceDistributionPostgresFixture(t)
	testConfidenceDistributionIdentityLifecycle(t, repo, db, keyID)
}

func TestConfidenceDistributionPostgresStableWindow(t *testing.T) {
	repo, db, keyID := newConfidenceDistributionPostgresFixture(t)
	testConfidenceDistributionStableWindow(t, repo, db, keyID)
}
