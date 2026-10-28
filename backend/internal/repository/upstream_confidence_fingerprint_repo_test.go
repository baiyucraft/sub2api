package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func confidenceFingerprintTestInt64Ptr(value int64) *int64 { return &value }

func newConfidenceFingerprintSQLite(t *testing.T) (*upstreamConfigRepository, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:fingerprint-%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", t.Name()))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE upstream_keys (id INTEGER PRIMARY KEY)`,
		`INSERT INTO upstream_keys (id) VALUES (1),(2)`,
		`CREATE TABLE upstream_confidence_fingerprint_states (upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE, revision BIGINT NOT NULL DEFAULT 1, state_json JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	return &upstreamConfigRepository{client: client}, db
}

func TestConfidenceFingerprintRepositoryRestartLeaseAndIdentity(t *testing.T) {
	repo, _ := newConfidenceFingerprintSQLite(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	identity := service.NewConfidenceFingerprintIdentity("deepseek", "deepseek-chat", "openai_chat_completions", "https://api.example.test/v1")
	identity.Components["credential"] = "credential-digest"
	attempt, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.Equal(t, "protocol", attempt.Method)
	blocked, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now.Add(time.Second))
	require.NoError(t, err)
	require.Nil(t, blocked)
	require.NoError(t, repo.FinishConfidenceFingerprint(ctx, 1, attempt, identity, service.ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", Shape: "chat"}, now.Add(2*time.Second)))
	reads, err := repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.Empty(t, reads[1].State.Samples, "protocol-only evidence does not consume attribution sample capacity")
	require.NotNil(t, reads[1].State.LastProtocolEvidence)
	require.Equal(t, "compatible", reads[1].State.LastProtocolEvidence.Evidence.ProtocolStatus)

	changed := service.NewConfidenceFingerprintIdentity("deepseek", "deepseek-reasoner", "openai_chat_completions", "https://api.example.test/v1")
	changed.Components["credential"] = "credential-digest"
	reads, err = repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: changed})
	require.NoError(t, err)
	require.Nil(t, reads[1].State, "a changed mapped model must not reuse the previous series")
	next, err := repo.ClaimConfidenceFingerprint(ctx, 1, changed, now.Add(3*time.Second))
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, attempt.SeriesID, next.SeriesID)
	require.Equal(t, int64(2), next.Sequence, "rotation sequence survives identity changes")

	stale := *next
	stale.Token = "stale-token"
	require.Error(t, repo.FinishConfidenceFingerprint(ctx, 1, &stale, changed, service.ConfidenceFingerprintEvidence{}, now.Add(4*time.Second)))
}

func TestConfidenceFingerprintRepositoryBatchIsolationAndCascade(t *testing.T) {
	repo, db := newConfidenceFingerprintSQLite(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first := service.NewConfidenceFingerprintIdentity("anthropic", "claude-sonnet", "anthropic_messages", "https://api.example.test")
	second := service.NewConfidenceFingerprintIdentity("gemini", "gemini-flash", "gemini_stream_generate_content", "https://generativelanguage.googleapis.com")
	for id, identity := range map[int64]service.ConfidenceFingerprintIdentity{1: first, 2: second} {
		attempt, err := repo.ClaimConfidenceFingerprint(ctx, id, identity, now)
		require.NoError(t, err)
		require.NoError(t, repo.FinishConfidenceFingerprint(ctx, id, attempt, identity, service.ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, now.Add(time.Second)))
	}
	reads, err := repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: first, 2: second})
	require.NoError(t, err)
	require.Empty(t, reads[1].State.Samples)
	require.Empty(t, reads[2].State.Samples)
	require.NotNil(t, reads[1].State.LastProtocolEvidence)
	require.NotNil(t, reads[2].State.LastProtocolEvidence)
	_, err = db.Exec(`DELETE FROM upstream_keys WHERE id=1`)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=1`).Scan(&count))
	require.Zero(t, count)
}

func TestConfidenceFingerprintRepositoryBatchDecodeFailureIsolation(t *testing.T) {
	identity := service.NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://api.example.test")
	healthy, err := service.NewConfidenceFingerprintState(identity)
	require.NoError(t, err)
	healthyRaw, err := json.Marshal(healthy)
	require.NoError(t, err)
	invalid := *healthy
	invalid.NextSequence = 0
	invalidRaw, err := json.Marshal(invalid)
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed_json", `{"policy_version":1,`},
		{"invalid_state", string(invalidRaw)},
	} {
		t.Run(tc.name+"/sqlite", func(t *testing.T) {
			repo, db := newConfidenceFingerprintSQLite(t)
			now := time.Date(2026, 10, 28, 5, 0, 0, 0, time.UTC)
			for id, raw := range map[int64]string{1: tc.raw, 2: string(healthyRaw)} {
				_, err := db.Exec(`INSERT INTO upstream_confidence_fingerprint_states (upstream_key_id,revision,state_json,updated_at) VALUES (?,7,?,?)`, id, raw, now)
				require.NoError(t, err)
			}
			reads, err := repo.LoadConfidenceFingerprints(context.Background(), map[int64]service.ConfidenceFingerprintIdentity{1: identity, 2: identity})
			require.NoError(t, err)
			require.Error(t, reads[1].Err)
			require.Nil(t, reads[1].State)
			require.NoError(t, reads[2].Err)
			require.Equal(t, healthy, reads[2].State)
			for id, want := range map[int64]string{1: tc.raw, 2: string(healthyRaw)} {
				var raw string
				var revision int64
				require.NoError(t, db.QueryRow(`SELECT state_json,revision FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=?`, id).Scan(&raw, &revision))
				require.Equal(t, want, raw, "batch reads must not repair stored state")
				require.Equal(t, int64(7), revision)
			}
		})
		t.Run(tc.name+"/postgres_reader", func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			repo := &upstreamConfigRepository{client: client}
			mock.ExpectQuery(`SELECT upstream_key_id,state_json FROM upstream_confidence_fingerprint_states WHERE upstream_key_id=ANY\(\$1\)`).
				WithArgs(sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"upstream_key_id", "state_json"}).AddRow(1, tc.raw).AddRow(2, string(healthyRaw)))
			reads, err := repo.LoadConfidenceFingerprints(context.Background(), map[int64]service.ConfidenceFingerprintIdentity{1: identity, 2: identity})
			require.NoError(t, err)
			require.Error(t, reads[1].Err)
			require.Nil(t, reads[1].State)
			require.NoError(t, reads[2].Err)
			require.Equal(t, healthy, reads[2].State)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestConfidenceFingerprintAdaptiveRouteKeepsReadableSeries(t *testing.T) {
	repo, _ := newConfidenceFingerprintSQLite(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 3, 0, 0, 0, time.UTC)
	account := &service.Account{
		ID: 12, UpstreamKeyID: confidenceFingerprintTestInt64Ptr(1), Type: service.AccountTypeAPIKey, Platform: service.PlatformDeepseek,
		Credentials: map[string]any{
			"api_key": "deepseek-key", "api_protocol": service.APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				service.APIProtocolChatCompletions: "https://chat.example/v1",
				service.APIProtocolResponses:       "https://responses.example/v1",
				service.APIProtocolAnthropic:       "https://anthropic.example",
			},
		},
	}
	identity, ok := service.ConfidenceFingerprintIdentityForAccount(account, "deepseek-chat")
	require.True(t, ok)
	attempt, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now)
	require.NoError(t, err)
	require.NotNil(t, attempt)
	require.NoError(t, repo.FinishConfidenceFingerprint(ctx, 1, attempt, identity, service.ConfidenceFingerprintEvidence{
		ProtocolStatus: "compatible", Shape: "openai_chat_completions",
	}, now.Add(time.Second)))
	reads, err := repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.NotNil(t, reads[1].State)
	require.Empty(t, reads[1].State.Samples)
	require.Equal(t, "openai_chat_completions", reads[1].State.LastProtocolEvidence.Evidence.Shape)

	next, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now.Add(2*time.Second))
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, int64(2), next.Sequence)
}

func TestConfidenceFingerprintRepositoryLegacyJSONKeepsSamplesSequenceAndPending(t *testing.T) {
	repo, db := newConfidenceFingerprintSQLite(t)
	ctx := context.Background()
	identity := service.NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://api.example.test")
	identity.Components["credential"] = "credential-digest"
	now := time.Date(2026, 10, 28, 5, 0, 0, 0, time.UTC)
	legacy := `{"policy_version":1,"series_id":"legacy-series","fingerprint":"` + identity.Fingerprint + `","components":{"platform":"openai","baseline":"","challenge":"health-structure-v1","normalization":"response-shape-v1","protocol":"responses","endpoint":"endpoint-digest","credential":"credential-digest"},"next_sequence":9,"pending":{"series_id":"legacy-series","token":"legacy-token","method":"behavioral","observed_at":"2026-10-28T04:58:00Z","sequence":8},"lease_expires":"2026-10-28T05:01:00Z","samples":[{"observed_at":"2026-10-28T04:00:00Z","method":"behavioral","evidence":{"protocol_status":"compatible"}}]}`
	_, err := db.Exec(`INSERT INTO upstream_confidence_fingerprint_states (upstream_key_id, revision, state_json, updated_at) VALUES (1, 1, ?, ?)`, legacy, now)
	require.NoError(t, err)

	blocked, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now)
	require.NoError(t, err)
	require.Nil(t, blocked)
	reads, err := repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.Equal(t, int64(9), reads[1].State.NextSequence)
	require.Len(t, reads[1].State.Samples, 1)
	require.Equal(t, "behavioral", reads[1].State.Pending.Method)
	require.Zero(t, reads[1].State.SamplingPolicyVersion, "read-only loading must not rewrite legacy state")
	require.Empty(t, reads[1].State.Components["model"], "blocked claims and reads must not upgrade legacy components")
	require.Equal(t, "legacy-token", reads[1].State.Pending.Token)
	legacySamples := reads[1].State.Samples

	next, err := repo.ClaimConfidenceFingerprint(ctx, 1, identity, now.Add(2*time.Minute))
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, int64(9), next.Sequence)
	require.Equal(t, "distribution", next.Method)
	reads, err = repo.LoadConfidenceFingerprints(ctx, map[int64]service.ConfidenceFingerprintIdentity{1: identity})
	require.NoError(t, err)
	require.Len(t, reads[1].State.Samples, 1)
	require.Equal(t, legacySamples, reads[1].State.Samples)
	require.Equal(t, "legacy-series", reads[1].State.SeriesID)
	require.Equal(t, identity.Fingerprint, reads[1].State.Fingerprint)
	require.Equal(t, identity.Model, reads[1].State.Components["model"])
	require.Equal(t, service.ConfidenceFingerprintSamplingPolicyVersion, reads[1].State.SamplingPolicyVersion)
}
