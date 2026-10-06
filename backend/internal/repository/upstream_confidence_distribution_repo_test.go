package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func newConfidenceDistributionSQLite(t *testing.T) (*upstreamConfigRepository, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", fmt.Sprintf("file:distribution-%s?mode=memory&cache=shared&_pragma=foreign_keys(1)", t.Name()))
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE upstream_keys (id INTEGER PRIMARY KEY, upstream_config_id BIGINT NOT NULL)`,
		`INSERT INTO upstream_keys (id,upstream_config_id) VALUES (1,1)`,
		`CREATE TABLE upstream_confidence_distribution_states (upstream_key_id BIGINT PRIMARY KEY REFERENCES upstream_keys(id) ON DELETE CASCADE, revision BIGINT NOT NULL DEFAULT 1, state_json JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE upstream_events (id INTEGER PRIMARY KEY, upstream_config_id BIGINT NOT NULL, upstream_key_id BIGINT, event_type TEXT NOT NULL, severity TEXT NOT NULL, source TEXT NOT NULL, message TEXT, payload JSONB NOT NULL, occurred_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL)`,
	} {
		_, err := db.Exec(ddl)
		require.NoError(t, err)
	}
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	return &upstreamConfigRepository{client: client}, db
}

func TestConfidenceDistributionRepositoryCrossInstanceClaim(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	testConfidenceDistributionConcurrentClaims(t, repo, db, 1)
}

func TestConfidenceDistributionIdleRetentionUsesCallerTransaction(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx, now := context.Background(), time.Now().UTC()
	old := now.Add(-service.UpstreamHealthObservationRetention - time.Hour)
	attempt, err := repo.ClaimConfidenceDistribution(ctx, 1, "idle", "responses", old)
	require.NoError(t, err)
	_, _, err = repo.FinishConfidenceDistribution(ctx, 1, attempt, service.DistributionSample{Answer: "47", Valid: true}, old.Add(time.Second))
	require.NoError(t, err)
	before, _, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	tx, err := repo.client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, cleanupExpiredConfidenceDistributionStates(ctx, tx.Client(), now))
	require.NoError(t, tx.Rollback())
	after, _, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Len(t, after.Samples, 1, "rollback must retain the uncommitted answer")
	tx, err = repo.client.Tx(ctx)
	require.NoError(t, err)
	require.NoError(t, cleanupExpiredConfidenceDistributionStates(ctx, tx.Client(), now))
	require.NoError(t, tx.Commit())
	after, _, err = readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Empty(t, after.Samples)
	require.Equal(t, before.SeriesID, after.SeriesID)
	require.Equal(t, before.ProbeOrder, after.ProbeOrder)
	require.Equal(t, before.NextSequence, after.NextSequence)
}

type confidenceDistributionCleanupRaceDriver struct {
	dialect.Driver
	beforeUpdate func()
}

func (d *confidenceDistributionCleanupRaceDriver) Exec(ctx context.Context, query string, args, result any) error {
	if d.beforeUpdate != nil {
		hook := d.beforeUpdate
		d.beforeUpdate = nil
		hook()
	}
	return d.Driver.Exec(ctx, query, args, result)
}

func TestConfidenceDistributionIdleRetentionDoesNotOverwriteConcurrentExecution(t *testing.T) {
	for _, operation := range []string{"claim", "finish"} {
		t.Run(operation, func(t *testing.T) {
			repo, db := newConfidenceDistributionSQLite(t)
			ctx, now := context.Background(), time.Now().UTC()
			old := now.Add(-service.UpstreamHealthObservationRetention - time.Hour)
			attempt, err := repo.ClaimConfidenceDistribution(ctx, 1, "race", "responses", old)
			require.NoError(t, err)
			_, _, err = repo.FinishConfidenceDistribution(ctx, 1, attempt, service.DistributionSample{Answer: "47", Valid: true}, old.Add(time.Second))
			require.NoError(t, err)
			if operation == "finish" {
				state, revision, err := readConfidenceDistributionState(ctx, db, 1)
				require.NoError(t, err)
				// Include a current pending attempt alongside an expired observation.
				kept := append([]service.DistributionSample(nil), state.Samples...)
				attempt, err = state.Claim(now)
				require.NoError(t, err)
				state.Samples = append(kept, state.Samples...)
				changed, err := saveConfidenceDistributionState(ctx, db, 1, revision, state, now)
				require.NoError(t, err)
				require.True(t, changed)
			}
			driver := &confidenceDistributionCleanupRaceDriver{Driver: repo.client.Driver(), beforeUpdate: func() {
				if operation == "claim" {
					attempt, err = repo.ClaimConfidenceDistribution(ctx, 1, "race", "responses", now)
				} else {
					_, _, err = repo.FinishConfidenceDistribution(ctx, 1, attempt, service.DistributionSample{Answer: "new answer", Valid: true}, now.Add(time.Second))
				}
				require.NoError(t, err)
			}}
			client := dbent.NewClient(dbent.Driver(driver))
			require.NoError(t, cleanupExpiredConfidenceDistributionStates(ctx, client, now))
			state, _, err := readConfidenceDistributionState(ctx, db, 1)
			require.NoError(t, err)
			require.Len(t, state.Samples, 1)
			require.Equal(t, attempt.Sequence, state.Samples[0].Sequence)
			if operation == "claim" {
				require.NotNil(t, state.Pending)
				require.Equal(t, "pending", state.Samples[0].Reason)
			} else {
				require.Nil(t, state.Pending)
				require.True(t, state.Samples[0].Valid)
				require.Equal(t, "new answer", state.Samples[0].Answer)
			}
		})
	}
}

type confidenceDistributionCleanupDialectDriver struct {
	dialect.Driver
	name     string
	queryErr error
}

func (d confidenceDistributionCleanupDialectDriver) Dialect() string { return d.name }

func (d confidenceDistributionCleanupDialectDriver) Query(ctx context.Context, query string, args, result any) error {
	if d.queryErr != nil {
		return d.queryErr
	}
	return d.Driver.Query(ctx, query, args, result)
}

func TestConfidenceDistributionIdleRetentionMissingMigrationIsNotHidden(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	_, err := db.Exec(`DROP TABLE upstream_confidence_distribution_states`)
	require.NoError(t, err)
	require.NoError(t, cleanupExpiredConfidenceDistributionStates(context.Background(), repo.client, time.Now()))
	missing := fmt.Errorf("relation upstream_confidence_distribution_states does not exist")
	client := dbent.NewClient(dbent.Driver(confidenceDistributionCleanupDialectDriver{Driver: repo.client.Driver(), name: dialect.Postgres, queryErr: missing}))
	require.ErrorIs(t, cleanupExpiredConfidenceDistributionStates(context.Background(), client, time.Now()), missing)
}

func testConfidenceDistributionConcurrentClaims(t *testing.T, repo *upstreamConfigRepository, db *sql.DB, keyID int64) {
	t.Helper()
	now := time.Now().UTC()
	var wg sync.WaitGroup
	claims := make(chan *service.DistributionAttempt, 24)
	errs := make(chan error, 24)
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			otherInstance := &upstreamConfigRepository{client: repo.client}
			claim, err := otherInstance.ClaimConfidenceDistribution(context.Background(), keyID, "same-series", "responses", now)
			claims <- claim
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var winner *service.DistributionAttempt
	for claim := range claims {
		if claim != nil {
			require.Nil(t, winner)
			winner = claim
		}
	}
	require.NotNil(t, winner)
	state, _, err := readConfidenceDistributionState(context.Background(), db, keyID)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.NextSequence)
	require.Len(t, state.Samples, 1)
	_, _, err = repo.FinishConfidenceDistribution(context.Background(), keyID, winner, service.DistributionSample{Valid: false, Reason: "request_failed"}, now.Add(time.Second))
	require.NoError(t, err)
	_, _, err = repo.FinishConfidenceDistribution(context.Background(), keyID, winner, service.DistributionSample{Valid: true, Answer: "42"}, now.Add(2*time.Second))
	require.ErrorIs(t, err, service.ErrConfidenceDistributionLeaseLost)
}

func TestConfidenceDistributionRepositoryRestartAndSeriesIsolation(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := repo.ClaimConfidenceDistribution(ctx, 1, "original", "responses", now)
	require.NoError(t, err)
	restarted := &upstreamConfigRepository{client: repo.client}
	summary, err := restarted.LoadConfidenceDistribution(ctx, 1, now.Add(service.UpstreamConfidenceDistributionLease))
	require.NoError(t, err)
	require.Equal(t, 1, summary.Attempted)
	require.Equal(t, 0, summary.ValidSamples)
	state, _, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Equal(t, "lease_expired", state.Samples[0].Reason)
	require.Nil(t, state.Pending)
	second, err := restarted.ClaimConfidenceDistribution(ctx, 1, "original", "responses", now.Add(3*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Sequence)
	_, _, err = restarted.FinishConfidenceDistribution(ctx, 1, second, service.DistributionSample{Valid: true, Answer: "42"}, now.Add(3*time.Minute+time.Second))
	require.NoError(t, err)
	summary, err = restarted.LoadConfidenceDistributionForSeries(ctx, 1, "changed", now.Add(4*time.Minute))
	require.NoError(t, err)
	require.Nil(t, summary)
	third, err := restarted.ClaimConfidenceDistribution(ctx, 1, "changed", "chat_completions", now.Add(4*time.Minute))
	require.NoError(t, err)
	require.NotEqual(t, first.SeriesID, third.SeriesID)
	require.Equal(t, int64(1), third.Sequence)
	_, _, err = restarted.FinishConfidenceDistribution(ctx, 1, first, service.DistributionSample{}, now.Add(4*time.Minute+time.Second))
	require.ErrorIs(t, err, service.ErrConfidenceDistributionLeaseLost)
	summary, _, err = restarted.FinishConfidenceDistribution(ctx, 1, third, service.DistributionSample{Reason: "empty_answer"}, now.Add(4*time.Minute+time.Second))
	require.NoError(t, err)
	require.Equal(t, "chat_completions", summary.Protocol)
	require.Equal(t, 1, summary.Attempted)
}

func TestConfidenceDistributionRepositoryRetentionAndForeignKey(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx := context.Background()
	now := time.Now().UTC()
	attempt, err := repo.ClaimConfidenceDistribution(ctx, 1, "series", "responses", now)
	require.NoError(t, err)
	_, _, err = repo.FinishConfidenceDistribution(ctx, 1, attempt, service.DistributionSample{Reason: "network_error"}, now.Add(time.Second))
	require.NoError(t, err)
	summary, err := repo.LoadConfidenceDistribution(ctx, 1, now.Add(service.UpstreamHealthObservationRetention+time.Second))
	require.NoError(t, err)
	require.Equal(t, "collecting", summary.Status)
	require.Equal(t, 0, summary.Attempted)
	state, _, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Equal(t, int64(2), state.NextSequence)
	_, err = db.Exec(`DELETE FROM upstream_keys WHERE id=1`)
	require.NoError(t, err)
	state, _, err = readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Nil(t, state)
}

func TestConfidenceDistributionRepositoryRejectsCorruptState(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	_, err := db.Exec(`INSERT INTO upstream_confidence_distribution_states VALUES (1,1,'{}',$1)`, time.Now().UTC())
	require.NoError(t, err)
	_, err = repo.ClaimConfidenceDistribution(context.Background(), 1, "new", "responses", time.Now().UTC())
	require.ErrorContains(t, err, "invalid persistent")
	var raw string
	require.NoError(t, db.QueryRow(`SELECT state_json FROM upstream_confidence_distribution_states WHERE upstream_key_id=1`).Scan(&raw))
	require.JSONEq(t, `{}`, raw)
}

func TestConfidenceDistributionEventPayloadAllowlist(t *testing.T) {
	_, db := newConfidenceDistributionSQLite(t)
	tx, err := db.Begin()
	require.NoError(t, err)
	require.NoError(t, persistConfidenceDistributionEvent(context.Background(), tx, 1, "safe-series", &service.UpstreamConfidenceDistribution{
		Status: "mismatch", ClosestModel: "gpt-6-astra", ValidSamples: 128, BaselineVersion: "baseline",
	}, time.Now().UTC()))
	require.NoError(t, tx.Commit())
	var severity, source, eventType, raw string
	require.NoError(t, db.QueryRow(`SELECT severity,source,event_type,payload FROM upstream_events`).Scan(&severity, &source, &eventType, &raw))
	require.Equal(t, "warning", severity)
	require.Equal(t, "probe", source)
	require.Equal(t, "key_confidence_distribution_changed", eventType)
	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &payload))
	require.Len(t, payload, 5)
	require.Equal(t, "safe-series", payload["series_id"])
	require.Equal(t, float64(128), payload["valid_samples"])
}

func newConfidenceDistributionFixtureState(t *testing.T, index int, now time.Time) (*service.ConfidenceDistributionState, []service.DistributionSample) {
	t.Helper()
	raw, err := os.ReadFile("../service/confidence_baselines/fixtures/responses.json")
	require.NoError(t, err)
	var fixtures []struct {
		Counts map[string]map[string]int `json:"counts"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	state, err := service.NewConfidenceDistributionState("fixture", "responses")
	require.NoError(t, err)
	answers := make(map[string][]string)
	for probeID, counts := range fixtures[index].Counts {
		categories := make([]string, 0, len(counts))
		for answer := range counts {
			categories = append(categories, answer)
		}
		sort.Strings(categories)
		for _, answer := range categories {
			for i := 0; i < counts[answer]; i++ {
				answers[probeID] = append(answers[probeID], answer)
			}
		}
	}
	samples := make([]service.DistributionSample, 0, 128)
	for i, probeID := range state.ProbeOrder {
		answer := answers[probeID][0]
		answers[probeID] = answers[probeID][1:]
		samples = append(samples, service.DistributionSample{
			Sequence: int64(i + 1), ProbeID: probeID, Answer: answer, Valid: true,
			ObservedAt: now.Add(time.Duration(i-127) * time.Minute),
		})
	}
	state.NextSequence, state.Samples = 128, samples[:127]
	return state, samples
}

func TestConfidenceDistributionRepositoryEventAtomicityAndDeduplication(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx := context.Background()
	now := time.Now().UTC()
	state, samples := newConfidenceDistributionFixtureState(t, 1, now)
	check, err := service.ScoreUpstreamConfidenceDistribution("responses", samples)
	require.NoError(t, err)
	require.Equal(t, "mismatch", check.Status, "the upstream oracle fixture must yield a sufficient mismatch")
	changed, err := saveConfidenceDistributionState(ctx, db, 1, 0, state, now)
	require.NoError(t, err)
	require.True(t, changed)
	claim, err := repo.ClaimConfidenceDistribution(ctx, 1, "fixture", "responses", now)
	require.NoError(t, err)
	require.Equal(t, int64(128), claim.Sequence)
	_, err = db.Exec(`CREATE TRIGGER distribution_event_fail BEFORE INSERT ON upstream_events BEGIN SELECT RAISE(ABORT,'event persistence failed'); END`)
	require.NoError(t, err)
	_, _, err = repo.FinishConfidenceDistribution(ctx, 1, claim, samples[127], now.Add(time.Second))
	require.ErrorContains(t, err, "event persistence failed")
	persisted, _, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.NotNil(t, persisted.Pending)
	require.Empty(t, persisted.LastDecisive)
	require.Equal(t, "pending", persisted.Samples[127].Reason)
	_, err = db.Exec(`DROP TRIGGER distribution_event_fail`)
	require.NoError(t, err)
	summary, alert, err := repo.FinishConfidenceDistribution(ctx, 1, claim, samples[127], now.Add(2*time.Second))
	require.NoError(t, err)
	require.True(t, alert)
	require.Equal(t, "mismatch", summary.Status)
	var events int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM upstream_events`).Scan(&events))
	require.Equal(t, 1, events)
	claim, err = repo.ClaimConfidenceDistribution(ctx, 1, "fixture", "responses", now.Add(time.Minute))
	require.NoError(t, err)
	// The cyclic question equals the first expired question; answering identically
	// preserves the oracle counts and must not emit another mismatch event.
	summary, alert, err = repo.FinishConfidenceDistribution(ctx, 1, claim, samples[0], now.Add(time.Minute+time.Second))
	require.NoError(t, err)
	require.False(t, alert)
	require.Equal(t, "mismatch", summary.Status)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM upstream_events`).Scan(&events))
	require.Equal(t, 1, events)
}
