//go:build distribution_postgres && !integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This explicit opt-in suite never skips: it creates a disposable database on
// a loopback PostgreSQL test cluster and does not need Docker or the app server.
// The DSN must address the cluster's postgres database, not application data.
func TestConfidenceDistributionNativePostgres(t *testing.T) {
	ctx := context.Background()
	raw := os.Getenv("SUB2API_DISTRIBUTION_TEST_POSTGRES_DSN")
	if raw == "" {
		t.Fatal("SUB2API_DISTRIBUTION_TEST_POSTGRES_DSN is required for the explicit PostgreSQL suite")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") || u.Path != "/postgres" || u.Port() == "" {
		t.Fatal("PostgreSQL test DSN must use a literal loopback host, explicit port and /postgres database")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port == 5432 {
		t.Fatal("PostgreSQL suite requires a dedicated non-default test port")
	}
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal("could not initialize the isolated PostgreSQL connection")
	}
	t.Cleanup(func() { _ = admin.Close() })
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if admin.PingContext(pingCtx) != nil {
		t.Fatal("isolated PostgreSQL test cluster is unavailable")
	}
	name := fmt.Sprintf("distribution_identity_%d_%d", os.Getpid(), time.Now().UnixNano())
	_, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`"`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, dropErr := admin.ExecContext(context.Background(), `DROP DATABASE "`+name+`" WITH (FORCE)`)
		require.NoError(t, dropErr)
	})
	u.Path = "/" + name
	db, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(12)
	for _, ddl := range []string{
		`CREATE TABLE upstream_keys (id BIGINT PRIMARY KEY, upstream_config_id BIGINT NOT NULL)`,
		`INSERT INTO upstream_keys VALUES (1,1),(2,1),(3,1)`,
		`CREATE TABLE upstream_confidence_distribution_states (upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE, revision BIGINT NOT NULL DEFAULT 1, state_json JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE upstream_events (id BIGSERIAL PRIMARY KEY, upstream_config_id BIGINT NOT NULL, upstream_key_id BIGINT, event_type TEXT NOT NULL, severity TEXT NOT NULL, source TEXT NOT NULL, message TEXT, payload JSONB NOT NULL, occurred_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL)`,
	} {
		_, err = db.ExecContext(ctx, ddl)
		require.NoError(t, err)
	}
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := &upstreamConfigRepository{client: client}
	t.Run("cross_instance_claim", func(t *testing.T) {
		testConfidenceDistributionConcurrentClaims(t, repo, db, 1)
	})
	t.Run("identity_upgrade_reset_transaction", func(t *testing.T) {
		testConfidenceDistributionIdentityLifecycle(t, repo, db, 2)
	})
	t.Run("stable_sliding_window", func(t *testing.T) {
		testConfidenceDistributionStableWindow(t, repo, db, 3)
	})
}
