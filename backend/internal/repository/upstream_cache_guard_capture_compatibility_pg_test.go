//go:build cacheguardpg

package repository

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardCaptureOnlyJSONAndDuplicateCompletionPostgres(t *testing.T) {
	db, ctx := cacheGuardAdmissionPostgres(t)
	migration, err := migrations.FS.ReadFile("301_upstream_cache_guard_admissions.sql")
	require.NoError(t, err)
	_, err = db.ExecContext(ctx, string(migration))
	require.NoError(t, err)
	store := NewUpstreamCacheGuardRepository(db).(*upstreamCacheGuardRepository)
	engine := upstreamcacheguard.NewEngine(store)
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode, settings.GroupIDs, settings.TrialPercent = upstreamcacheguard.Protect, []int64{9}, 100
	require.NoError(t, engine.ApplySettings(settings))
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	request := upstreamcacheguard.Request{ID: "capture-cold", APIKeyID: 7, GroupID: 9, AccountID: 2, UpstreamKeyID: 5,
		LogicalSessionDigest: strings.Repeat("a", 64), Model: "gpt-test", Protocol: "openai", StartedAt: at}
	admission, err := engine.CaptureOnly(ctx, request)
	require.NoError(t, err)
	require.True(t, admission.CaptureOnly)
	require.False(t, admission.WarmAtStart)
	fact := upstreamcacheguard.Evidence{ID: "capture-fact", Request: request, Admission: admission, Success: true,
		CompletionReason: "success", CompletedAt: at.Add(time.Second),
		RawUsage: upstreamcacheguard.RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 0}}
	inserted, err := engine.CompleteWithResult(ctx, fact)
	require.NoError(t, err)
	require.True(t, inserted)
	inserted, err = engine.CompleteWithResult(ctx, fact)
	require.NoError(t, err)
	require.False(t, inserted)
	restarted := upstreamcacheguard.NewEngine(store)
	require.NoError(t, restarted.ApplySettings(settings))
	inserted, err = restarted.CompleteWithResult(ctx, fact)
	require.NoError(t, err)
	require.False(t, inserted)
	var count int
	var raw []byte
	var valid bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM upstream_cache_guard_facts WHERE evidence_id='capture-fact'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT evidence_json,valid_sample FROM upstream_cache_guard_facts WHERE evidence_id='capture-fact'`).Scan(&raw, &valid))
	var persisted upstreamcacheguard.Evidence
	require.NoError(t, json.Unmarshal(raw, &persisted))
	require.True(t, persisted.Admission.CaptureOnly)
	require.Equal(t, "success", persisted.CompletionReason)
	require.False(t, valid, "cold completion warms the route without entering the valid sample window")
	next := request
	next.ID, next.StartedAt = "capture-warm", at.Add(2*time.Second)
	warm, err := restarted.CaptureOnly(ctx, next)
	require.NoError(t, err)
	require.True(t, warm.WarmAtStart)
	warmFact := fact
	warmFact.ID, warmFact.Request, warmFact.Admission, warmFact.CompletedAt = "capture-warm-fact", next, warm, next.StartedAt.Add(time.Second)
	inserted, err = restarted.CompleteWithResult(ctx, warmFact)
	require.NoError(t, err)
	require.True(t, inserted)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT valid_sample FROM upstream_cache_guard_facts WHERE evidence_id='capture-warm-fact'`).Scan(&valid))
	require.True(t, valid, "explicit cached_tokens=0 is a known valid warm sample")
	loaded, _, err := store.LoadSession(ctx, 9, upstreamcacheguard.SessionKey(request))
	require.NoError(t, err)
	require.True(t, loaded.NaturalTrialSuppressed)
	require.Empty(t, loaded.LastRouteKey)
	require.Zero(t, loaded.TrialAccountID)

	legacyKey := strings.Repeat("b", 64)
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_runtime(group_id,state_kind,state_key,version,state_json,updated_at) VALUES(9,'session',$1,1,$2,$3)`, legacyKey,
		`{"key":"legacy","natural_trial_ordinal":7,"first_seen_at":"2026-10-20T00:00:00Z"}`, at)
	require.NoError(t, err)
	legacy, _, err := store.LoadSession(ctx, 9, legacyKey)
	require.NoError(t, err)
	require.False(t, legacy.NaturalTrialSuppressed)
	require.Equal(t, int64(7), legacy.NaturalTrialOrdinal)
	_, err = db.ExecContext(ctx, `INSERT INTO upstream_cache_guard_admissions(group_id,session_key,admission_key,account_id,upstream_key_id,route_key,started_at,completed,admission_json) VALUES(9,$1,'legacy',2,5,'legacy',$2,FALSE,$3)`, legacyKey, at,
		`{"request_id":"legacy","sample":true,"warm_at_start":true}`)
	require.NoError(t, err)
	require.NoError(t, store.TransactAccount(ctx, 9, 2, 5, func(tx upstreamcacheguard.Tx) error {
		a, exists, err := tx.(upstreamcacheguard.AdmissionTx).ReadAdmission(legacyKey, "legacy")
		require.NoError(t, err)
		require.True(t, exists)
		require.False(t, a.CaptureOnly)
		require.True(t, a.Sample)
		return nil
	}))
}
