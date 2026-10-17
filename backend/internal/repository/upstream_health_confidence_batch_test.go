package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamHealthConfidencesBatchReadsFullWindowOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &upstreamConfigRepository{client: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	now := time.Now().UTC()
	rows := sqlmock.NewRows([]string{"key_id", "counts24", "counts7", "facts_valid", "observed_at", "checks", "evidence", "version", "effort", "tokens", "score"}).
		AddRow(1, `[60,30,0,0,0,30,0,0]`, `[60,30,0,0,0,30,0,0]`, true, now, `{"valid_completed":2,"current_success":1}`, nil, service.UpstreamConfidencePromptVersion, nil, nil, 30).
		AddRow(2, `[0,0,0,0,0,0,0,0]`, `[1,1,0,0,0,1,0,0]`, true, now.Add(-48*time.Hour), `{"valid_completed":1,"current_success":1}`, nil, service.UpstreamConfidencePromptVersion, nil, nil, 90).
		AddRow(3, nil, nil, true, nil, nil, nil, nil, nil, nil, nil)
	mock.ExpectQuery(`WITH facts AS MATERIALIZED .*LEFT JOIN LATERAL.*ORDER BY requested.key_id`).
		WithArgs("{1,2,3}", service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(rows)
	got, err := repo.GetUpstreamHealthConfidences(context.Background(), []int64{2, 1, 1, 3, 0, -1})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, 60, got[1].SampleCount7d)
	require.Equal(t, 60, got[1].SampleCount24h)
	require.Equal(t, 30, *got[1].LastScore, "newest row wins; no last-24-row truncation")
	require.Equal(t, 50.0, *got[1].Score7d)
	require.Equal(t, 1, got[2].SampleCount7d)
	require.Zero(t, got[2].SampleCount24h)
	require.Nil(t, got[3].Score7d)
	require.Equal(t, "data_insufficient", got[3].Status)
	require.NoError(t, mock.ExpectationsWereMet(), "a page uses one observation query")
	got, err = repo.GetUpstreamHealthConfidences(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestUpstreamHealthConfidencesRejectsMalformedHistoricalJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &upstreamConfigRepository{client: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	rows := sqlmock.NewRows([]string{"key_id", "counts24", "counts7", "facts_valid", "observed_at", "checks", "evidence", "version", "effort", "tokens", "score"}).
		AddRow(1, nil, nil, false, nil, nil, nil, nil, nil, nil, nil)
	mock.ExpectQuery(`WITH facts AS MATERIALIZED .*LEFT JOIN LATERAL.*ORDER BY requested.key_id`).
		WithArgs("{1}", service.PlatformOpenAI, service.UpstreamConfidencePromptVersion, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).WillReturnRows(rows)
	got, err := repo.GetUpstreamHealthConfidences(context.Background(), []int64{1})
	require.ErrorContains(t, err, "invalid confidence observation JSON")
	require.Nil(t, got, "invalid older facts must not silently produce a healthy summary")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUpstreamAccountBindingsBatchPreservesDuplicateBindingsAndHydratesOnce(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &accountRepository{client: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	mock.ExpectQuery(`SELECT .* FROM "accounts" WHERE .*upstream_key_id.* IN .* ORDER BY .*id.*`).
		WithArgs(int64(1), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "upstream_key_id", "credentials", "extra", "platform", "type"}).
		AddRow(101, 1, `{}`, `{}`, service.PlatformOpenAI, service.AccountTypeAPIKey).
		AddRow(102, 1, `{}`, `{}`, service.PlatformOpenAI, service.AccountTypeAPIKey).
		AddRow(103, 2, `{}`, `{}`, service.PlatformOpenAI, service.AccountTypeAPIKey))
	mock.ExpectQuery(`SELECT .* FROM "upstream_keys" WHERE .*id.* IN .*`).
		WithArgs(int64(1), int64(2)).WillReturnRows(sqlmock.NewRows([]string{"id", "upstream_config_id", "key", "extra", "name"}).AddRow(1, 11, "synthetic-key-one", `{}`, "first").AddRow(2, 11, "synthetic-key-two", `{}`, "second"))
	mock.ExpectQuery(`SELECT .* FROM "account_groups" WHERE .*account_id.* IN .*`).
		WithArgs(int64(101), int64(102), int64(103)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}))
	got, err := repo.ListByUpstreamKeyIDs(context.Background(), []int64{2, 1, 1, 0})
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, int64(1), *got[0].UpstreamKeyID)
	require.Equal(t, int64(1), *got[1].UpstreamKeyID)
	require.Equal(t, "synthetic-key-one", got[0].Credentials["api_key"])
	require.Equal(t, "synthetic-key-one", got[1].Credentials["api_key"])
	require.Equal(t, "synthetic-key-two", got[2].Credentials["api_key"])
	require.NoError(t, mock.ExpectationsWereMet(), "binding, upstream keys, and groups each use one query")
}

func TestConfidenceDistributionBatchStableReadsOnceAndIsolatesInvalidRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	repo := &upstreamConfigRepository{client: dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))}
	now := time.Now().UTC()
	identity := testConfidenceDistributionIdentity("current", "responses")
	state, err := service.NewConfidenceDistributionStateForIdentity(identity)
	require.NoError(t, err)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	changed := testConfidenceDistributionIdentity("changed", "responses")
	identities := map[int64]service.ConfidenceDistributionIdentity{1: identity, 2: changed, 3: identity, 4: identity}
	mock.ExpectQuery(`SELECT upstream_key_id, state_json FROM upstream_confidence_distribution_states WHERE upstream_key_id IN \(\$1,\$2,\$3,\$4\)`).
		WithArgs(int64(1), int64(2), int64(3), int64(4)).WillReturnRows(sqlmock.NewRows([]string{"upstream_key_id", "state_json"}).AddRow(1, raw).AddRow(2, raw).AddRow(3, "invalid-json"))
	got, err := repo.LoadConfidenceDistributionsForSeries(context.Background(), identities, now)
	require.NoError(t, err)
	want, err := state.Summary()
	require.NoError(t, err)
	require.Equal(t, want, got[1].Distribution)
	require.NoError(t, got[1].Err)
	reasons, upgrade := state.IdentityChange(changed)
	require.False(t, upgrade)
	wantReset, err := state.PendingResetSummary(changed, reasons)
	require.NoError(t, err)
	require.Equal(t, wantReset, got[2].Distribution)
	require.Error(t, got[3].Err)
	require.Nil(t, got[3].Distribution)
	require.Nil(t, got[4].Distribution)
	require.NoError(t, got[4].Err)
	require.NoError(t, mock.ExpectationsWereMet(), "stable reads do not write or issue one query per key")
}

func TestConfidenceDistributionBatchMatchesSingleReadAndPreservesLeaseRecovery(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx, now := context.Background(), time.Now().UTC()
	_, err := db.Exec(`INSERT INTO upstream_keys(id,upstream_config_id) VALUES (2,1),(3,1)`)
	require.NoError(t, err)
	identity := testConfidenceDistributionIdentity("current", "responses")
	state, err := service.NewConfidenceDistributionStateForIdentity(identity)
	require.NoError(t, err)
	_, err = state.Claim(now.Add(-2 * service.UpstreamConfidenceDistributionLease))
	require.NoError(t, err)
	saved, err := saveConfidenceDistributionState(ctx, db, 1, 0, state, now)
	require.NoError(t, err)
	require.True(t, saved)
	stable, err := service.NewConfidenceDistributionStateForIdentity(identity)
	require.NoError(t, err)
	saved, err = saveConfidenceDistributionState(ctx, db, 2, 0, stable, now)
	require.NoError(t, err)
	require.True(t, saved)
	identities := map[int64]service.ConfidenceDistributionIdentity{1: identity, 2: identity, 3: identity}
	got, err := repo.LoadConfidenceDistributionsForSeries(ctx, identities, now)
	require.NoError(t, err)
	for id := int64(1); id <= 3; id++ {
		want, err := repo.LoadConfidenceDistributionForSeries(ctx, id, identity, now)
		require.NoError(t, err)
		require.NoError(t, got[id].Err)
		require.Equal(t, want, got[id].Distribution)
	}
	recovered, revision, err := readConfidenceDistributionState(ctx, db, 1)
	require.NoError(t, err)
	require.Nil(t, recovered.Pending)
	require.Empty(t, recovered.Samples)
	require.Equal(t, 1, recovered.FailedAttempts)
	require.Equal(t, int64(1), recovered.NextValidSequence)
	require.Equal(t, int64(2), revision)
	_, stableRevision, err := readConfidenceDistributionState(ctx, db, 2)
	require.NoError(t, err)
	require.Equal(t, int64(1), stableRevision)
	var missing int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM upstream_confidence_distribution_states WHERE upstream_key_id=3`).Scan(&missing))
	require.Zero(t, missing, "a missing series remains missing")
}

func TestConfidenceDistributionBatchPolicyResetMatchesSingleReadWithoutMutation(t *testing.T) {
	repo, db := newConfidenceDistributionSQLite(t)
	ctx, now := context.Background(), time.Now().UTC()
	_, err := db.Exec(`INSERT INTO upstream_keys(id,upstream_config_id) VALUES (2,1)`)
	require.NoError(t, err)
	identity := testConfidenceDistributionIdentity("current", "responses")
	identities := map[int64]service.ConfidenceDistributionIdentity{1: identity, 2: identity}
	for id := int64(1); id <= 2; id++ {
		legacy, err := service.NewConfidenceDistributionStateForIdentity(identity)
		require.NoError(t, err)
		legacy.SamplingPolicyVersion, legacy.NextValidSequence = 0, 0
		legacy.NextSequence = 2
		legacy.Samples = []service.DistributionSample{{Sequence: 1, ProbeID: legacy.ProbeOrder[0], Valid: true, Answer: "47", ObservedAt: now}}
		if id == 2 {
			legacy.Pending = &service.DistributionAttempt{SeriesID: legacy.SeriesID, LeaseToken: "old-lease", Sequence: 2, ProbeID: legacy.ProbeOrder[1]}
			legacy.LeaseExpiresAt = now.Add(service.UpstreamConfidenceDistributionLease)
			legacy.NextSequence = 3
			legacy.Samples = append(legacy.Samples, service.DistributionSample{Sequence: 2, ProbeID: legacy.Pending.ProbeID, Reason: "pending", ObservedAt: now})
		}
		saved, err := saveConfidenceDistributionState(ctx, db, id, 0, legacy, now)
		require.NoError(t, err)
		require.True(t, saved)
	}
	got, err := repo.LoadConfidenceDistributionsForSeries(ctx, identities, now.Add(time.Second))
	require.NoError(t, err)
	for id := int64(1); id <= 2; id++ {
		want, err := repo.LoadConfidenceDistributionForSeries(ctx, id, identity, now.Add(time.Second))
		require.NoError(t, err)
		require.NoError(t, got[id].Err)
		require.Equal(t, want, got[id].Distribution)
		require.True(t, want.SeriesReset.Pending)
		require.Equal(t, []string{"sampling_policy_changed"}, want.SeriesReset.Reasons)
		require.Equal(t, 0, want.SamplingPolicyVersion)
		require.Empty(t, want.Matches)
		state, revision, err := readConfidenceDistributionState(ctx, db, id)
		require.NoError(t, err)
		require.Equal(t, int64(1), revision, "policy reset listing never acquires a new series")
		require.Equal(t, 0, state.SamplingPolicyVersion)
	}
}

func TestConfidenceCountBoundaryPreservesGoNanosecondComparison(t *testing.T) {
	base := time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)
	for _, offset := range []time.Duration{-900, -100, 0, 100, 900, 1000} {
		cutoff := base.Add(offset)
		for _, fact := range []time.Time{base.Add(-time.Microsecond), base, base.Add(time.Microsecond)} {
			require.Equal(t, !fact.Before(cutoff), !fact.Before(confidenceCountBoundary(cutoff)), "offset %s fact %s", offset, fact)
		}
	}
}
