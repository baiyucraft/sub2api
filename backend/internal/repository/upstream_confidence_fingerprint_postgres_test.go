//go:build fingerprintpg && !integration

package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This opt-in suite never skips. It requires an explicitly acknowledged,
// disposable loopback test database and isolates all fixtures in a fresh schema.
// SUB2API_FINGERPRINT_TEST_DSN=... SUB2API_FINGERPRINT_TEST_ISOLATED=1
// go test -tags fingerprintpg ./internal/repository -run TestConfidenceFingerprintPostgres
func newConfidenceFingerprintPostgres(t *testing.T) (string, func() (*upstreamConfigRepository, *sql.DB)) {
	t.Helper()
	raw := os.Getenv("SUB2API_FINGERPRINT_TEST_DSN")
	if raw == "" || os.Getenv("SUB2API_FINGERPRINT_TEST_ISOLATED") != "1" {
		t.Fatal("explicit PostgreSQL suite requires SUB2API_FINGERPRINT_TEST_DSN and SUB2API_FINGERPRINT_TEST_ISOLATED=1")
	}
	u, err := url.Parse(raw)
	require.NoError(t, err)
	if (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "::1") ||
		!strings.Contains(strings.ToLower(u.Path), "test") {
		t.Fatal("PostgreSQL test DSN must name a loopback disposable test database")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1024 || port > 65535 || port == 5432 {
		t.Fatal("PostgreSQL suite requires an explicit dedicated non-default test port")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	admin, err := sql.Open("postgres", raw)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, admin.Close()) })
	var database string
	require.NoError(t, admin.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database))
	require.Contains(t, strings.ToLower(database), "test")
	var nonce [8]byte
	_, err = rand.Read(nonce[:])
	require.NoError(t, err)
	schema := "fingerprint_test_" + hex.EncodeToString(nonce[:])
	_, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, dropErr := admin.ExecContext(cleanupCtx, `DROP SCHEMA `+schema+` CASCADE`)
		require.NoError(t, dropErr)
	})
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scopedDSN := u.String()
	open := func() (*upstreamConfigRepository, *sql.DB) {
		t.Helper()
		db, openErr := sql.Open("postgres", scopedDSN)
		require.NoError(t, openErr)
		db.SetMaxOpenConns(4)
		client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
		t.Cleanup(func() { require.NoError(t, client.Close()) })
		return &upstreamConfigRepository{client: client}, db
	}
	return scopedDSN, open
}

func TestConfidenceFingerprintPostgresLeaseCASAndRotation(t *testing.T) {
	_, open := newConfidenceFingerprintPostgres(t)
	firstRepo, firstDB := open()
	secondRepo, _ := open()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, ddl := range []string{
		`CREATE TABLE upstream_keys (id BIGINT PRIMARY KEY)`,
		`INSERT INTO upstream_keys(id) VALUES (1)`,
		`CREATE TABLE upstream_confidence_fingerprint_states (upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE, revision BIGINT NOT NULL DEFAULT 1, state_json JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	} {
		_, err := firstDB.ExecContext(ctx, ddl)
		require.NoError(t, err)
	}
	now := time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)
	identity := service.NewConfidenceFingerprintIdentity("deepseek", "deepseek-chat", "openai_chat_completions", "https://api.example.test/v1")
	changedIdentity := service.NewConfidenceFingerprintIdentity("deepseek", "deepseek-reasoner", "openai_chat_completions", "https://api.example.test/v1")
	type claimResult struct {
		attempt *service.ConfidenceFingerprintAttempt
		err     error
	}
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	results := make(chan claimResult, 2)
	for _, repo := range []*upstreamConfigRepository{firstRepo, secondRepo} {
		go func(repo *upstreamConfigRepository) {
			ready <- struct{}{}
			<-start
			attempt, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now)
			results <- claimResult{attempt, err}
		}(repo)
	}
	<-ready
	<-ready
	close(start)
	var winner *service.ConfidenceFingerprintAttempt
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		if result.attempt != nil {
			require.Nil(t, winner, "separate PostgreSQL connections must not both acquire the lease")
			winner = result.attempt
		}
	}
	require.NotNil(t, winner)
	require.Equal(t, int64(1), winner.Sequence)
	blocked, err := secondRepo.ClaimConfidenceFingerprint(ctx, 1, changedIdentity, now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, blocked, "identity rotation must respect an active lease")

	// Both transactions observe one revision. After the first commits, a stale
	// revision must fail CAS without replacing the committed JSON or timestamp.
	staleTx, err := firstDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer staleTx.Rollback()
	staleState, staleRevision, err := readConfidenceFingerprintState(ctx, staleTx, 1)
	require.NoError(t, err)
	writerDB, err := secondRepo.fingerprintDB()
	require.NoError(t, err)
	writerTx, err := writerDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer writerTx.Rollback()
	writerState, writerRevision, err := readConfidenceFingerprintState(ctx, writerTx, 1)
	require.NoError(t, err)
	require.Equal(t, staleRevision, writerRevision)
	committedAt := now.Add(2 * time.Second)
	updated, err := saveConfidenceFingerprintState(ctx, writerTx, 1, writerRevision, writerState, committedAt)
	require.NoError(t, err)
	require.True(t, updated)
	require.NoError(t, writerTx.Commit())
	var beforeJSON string
	var beforeRevision int64
	var beforeUpdated time.Time
	require.NoError(t, firstDB.QueryRowContext(ctx, `SELECT state_json::text,revision,updated_at FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=1`).Scan(&beforeJSON, &beforeRevision, &beforeUpdated))
	staleState.NextSequence += 100
	updated, err = saveConfidenceFingerprintState(ctx, staleTx, 1, staleRevision, staleState, now.Add(3*time.Second))
	require.NoError(t, err)
	require.False(t, updated)
	require.NoError(t, staleTx.Commit())
	var afterJSON string
	var afterRevision int64
	var afterUpdated time.Time
	require.NoError(t, firstDB.QueryRowContext(ctx, `SELECT state_json::text,revision,updated_at FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=1`).Scan(&afterJSON, &afterRevision, &afterUpdated))
	require.Equal(t, beforeJSON, afterJSON)
	require.Equal(t, beforeRevision, afterRevision)
	require.True(t, beforeUpdated.Equal(afterUpdated))

	// Fixed logical time crosses the persisted lease without sleeping.
	reads, err := firstRepo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.NoError(t, reads[1].Err)
	require.NotNil(t, reads[1].State)
	leaseBoundary := reads[1].State.LeaseExpires
	rotated, err := secondRepo.ClaimConfidenceFingerprint(ctx, 1, changedIdentity, leaseBoundary)
	require.NoError(t, err)
	require.NotNil(t, rotated)
	require.NotEqual(t, winner.SeriesID, rotated.SeriesID)
	require.Equal(t, int64(2), rotated.Sequence, "identity reset must preserve the global claim sequence")
	require.Error(t, firstRepo.FinishConfidenceFingerprint(ctx, 1, winner, identity, service.ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, leaseBoundary.Add(time.Second)))
	require.NoError(t, secondRepo.FinishConfidenceFingerprint(ctx, 1, rotated, changedIdentity, service.ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", Shape: "chat"}, leaseBoundary.Add(2*time.Second)))

	// Close both original pools, then reconstruct the repository to prove state
	// and completed evidence survive connection/process-local memory loss.
	require.NoError(t, firstRepo.client.Close())
	require.NoError(t, secondRepo.client.Close())
	restarted, _ := open()
	reads, err = restarted.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: changedIdentity})
	require.NoError(t, err)
	require.NoError(t, reads[1].Err)
	require.NotNil(t, reads[1].State)
	require.Equal(t, rotated.SeriesID, reads[1].State.SeriesID)
	require.Equal(t, int64(3), reads[1].State.NextSequence)
	require.Nil(t, reads[1].State.Pending)
	require.NotNil(t, reads[1].State.LastProtocolEvidence)
	require.Equal(t, "compatible", reads[1].State.LastProtocolEvidence.Evidence.ProtocolStatus)
	reads, err = restarted.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.Nil(t, reads[1].State, "the old identity must not read the rotated series")
}
