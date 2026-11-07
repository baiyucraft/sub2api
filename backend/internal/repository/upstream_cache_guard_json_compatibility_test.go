package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

func TestCacheGuardAdmissionRepositoryReadsCaptureOnlyAndLegacyJSON(t *testing.T) {
	for _, tc := range []struct {
		name       string
		raw        string
		capture    bool
		generation uint64
		route      uint64
	}{
		{"legacy", `{"request_id":"r","sample":true,"warm_at_start":true}`, false, 0, 0},
		{"capture", `{"request_id":"r","sample":true,"warm_at_start":true,"capture_only":true}`, true, 0, 0},
		{"affinity_reset", `{"request_id":"r","sample":true,"warm_at_start":true,"affinity_generation":4}`, false, 4, 0},
		{"route_fence", `{"request_id":"r","sample":true,"warm_at_start":true,"affinity_generation":4,"route_generation":9}`, false, 4, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock\(299,hashtext`).WithArgs(int64(9), "session").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT admission_json FROM upstream_cache_guard_admissions`).WithArgs(int64(9), "session", "r").WillReturnRows(sqlmock.NewRows([]string{"admission_json"}).AddRow(tc.raw))
			mock.ExpectCommit()
			err = NewUpstreamCacheGuardRepository(db).Transact(context.Background(), 9, func(tx upstreamcacheguard.Tx) error {
				a, exists, err := tx.(upstreamcacheguard.AdmissionTx).ReadAdmission("session", "r")
				require.NoError(t, err)
				require.True(t, exists)
				require.True(t, a.Sample)
				require.True(t, a.WarmAtStart)
				require.Equal(t, tc.capture, a.CaptureOnly)
				require.Equal(t, tc.generation, a.AffinityGeneration)
				require.Equal(t, tc.route, a.RouteGeneration)
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCacheGuardSessionRepositoryMarshalRetainsTrialSuppressionAndLegacyDefaults(t *testing.T) {
	for _, raw := range []string{
		`{"key":"session","natural_trial_ordinal":7}`,
		`{"key":"session","natural_trial_ordinal":7,"natural_trial_suppressed":true}`,
		`{"key":"session","natural_trial_ordinal":7,"affinity_generation":4}`,
		`{"key":"session","natural_trial_ordinal":7,"routes":{"route":{"account_id":2,"upstream_key_id":5}}}`,
		`{"key":"session","natural_trial_ordinal":7,"affinity_generation":4,"route_generation":9,"owner_route_generation":8,"accepted_route_key":"route","routes":{"route":{"account_id":2,"upstream_key_id":5,"model":"gpt-test","protocol":"openai"}}}`,
	} {
		var session upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal([]byte(raw), &session))
		suppressed := session.NaturalTrialSuppressed
		generation := session.AffinityGeneration
		routeGeneration, ownerGeneration := session.RouteGeneration, session.OwnerRouteGeneration
		acceptedRoute, routes := session.AcceptedRouteKey, session.Routes
		session.Admissions = map[string]upstreamcacheguard.Admission{"r": {CaptureOnly: true}}
		encoded, err := marshalCacheGuardRuntime(upstreamcacheguard.SessionKind, &session)
		require.NoError(t, err)
		var decoded upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Equal(t, suppressed, decoded.NaturalTrialSuppressed)
		require.Equal(t, int64(7), decoded.NaturalTrialOrdinal)
		require.Equal(t, generation, decoded.AffinityGeneration)
		require.Equal(t, routeGeneration, decoded.RouteGeneration)
		require.Equal(t, ownerGeneration, decoded.OwnerRouteGeneration)
		require.Equal(t, acceptedRoute, decoded.AcceptedRouteKey)
		require.Equal(t, routes, decoded.Routes)
		if routeGeneration == 0 {
			require.Zero(t, decoded.OwnerRouteGeneration, "legacy session JSON has no accepted-owner fence")
			require.Empty(t, decoded.AcceptedRouteKey)
			require.Empty(t, decoded.Routes["route"].Model)
			require.Empty(t, decoded.Routes["route"].Protocol)
		} else {
			require.Equal(t, "gpt-test", decoded.Routes["route"].Model)
			require.Equal(t, "openai", decoded.Routes["route"].Protocol)
		}
		require.Nil(t, decoded.Admissions)
		require.True(t, session.Admissions["r"].CaptureOnly, "marshaling must not mutate caller admissions")
	}
}

func TestCacheGuardOutboxRepositoryPreservesCompletionReasonAndLegacyJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	at := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	evidence := upstreamcacheguard.Evidence{ID: "new", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5, Model: "gpt-test", Protocol: "openai"}, Admission: upstreamcacheguard.Admission{CaptureOnly: true, Sample: true, AffinityGeneration: 4, RouteGeneration: 9, RouteKey: "route"}, CompletionReason: "usage_absent", CompletedAt: at}
	raw, err := json.Marshal(evidence)
	require.NoError(t, err)
	mock.ExpectExec(`INSERT INTO upstream_cache_guard_facts.*FALSE.*DO NOTHING`).WithArgs(int64(9), "new", int64(2), int64(5), at, false, string(raw)).WillReturnResult(sqlmock.NewResult(0, 1))
	repo := NewUpstreamCacheGuardRepository(db)
	require.NoError(t, repo.Enqueue(context.Background(), evidence))
	mock.ExpectQuery(`WITH pending AS .*RETURNING f.evidence_json`).WithArgs(2, "owner", int64(10000)).WillReturnRows(sqlmock.NewRows([]string{"evidence_json"}).AddRow(raw).AddRow(`{"id":"old","admission":{"sample":true},"success":false}`))
	got, err := repo.ClaimPending(context.Background(), "owner", 2, 10*time.Second)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "usage_absent", got[0].CompletionReason)
	require.True(t, got[0].Admission.CaptureOnly)
	require.EqualValues(t, 4, got[0].Admission.AffinityGeneration)
	require.EqualValues(t, 9, got[0].Admission.RouteGeneration)
	require.Equal(t, "route", got[0].Admission.RouteKey)
	require.Equal(t, "gpt-test", got[0].Request.Model)
	require.Equal(t, "openai", got[0].Request.Protocol)
	require.Empty(t, got[1].CompletionReason)
	require.False(t, got[1].Admission.CaptureOnly)
	require.Zero(t, got[1].Admission.AffinityGeneration)
	require.Zero(t, got[1].Admission.RouteGeneration)
	require.Empty(t, got[1].Admission.RouteKey)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCacheGuardSessionRepositoryRoundTripPreservesAcceptedRouteFence(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"legacy", `{"key":"session","natural_trial_ordinal":7,"routes":{"route":{"account_id":2,"upstream_key_id":5}}}`},
		{"accepted_route", `{"key":"session","natural_trial_ordinal":7,"affinity_generation":4,"route_generation":9,"owner_route_generation":8,"accepted_route_key":"route","last_account_id":1,"last_route_key":"old-route","routes":{"route":{"account_id":2,"upstream_key_id":5,"model":"gpt-test","protocol":"openai"}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			var expected upstreamcacheguard.SessionState
			require.NoError(t, json.Unmarshal([]byte(tc.raw), &expected))
			encoded, err := marshalCacheGuardRuntime(upstreamcacheguard.SessionKind, expected)
			require.NoError(t, err)
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock\(299,hashtext`).WithArgs(int64(9), "session").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT state_json, version FROM upstream_cache_guard_runtime`).WithArgs(int64(9), upstreamcacheguard.SessionKind, "session").WillReturnRows(sqlmock.NewRows([]string{"state_json", "version"}).AddRow(tc.raw, 4))
			mock.ExpectQuery(`INSERT INTO upstream_cache_guard_runtime`).WithArgs(int64(9), upstreamcacheguard.SessionKind, "session", string(encoded)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(5))
			mock.ExpectCommit()
			err = NewUpstreamCacheGuardRepository(db).Transact(context.Background(), 9, func(tx upstreamcacheguard.Tx) error {
				var actual upstreamcacheguard.SessionState
				version, err := tx.Read(upstreamcacheguard.SessionKind, "session", &actual)
				require.NoError(t, err)
				require.EqualValues(t, 4, version)
				require.Equal(t, expected, actual)
				if tc.name == "legacy" {
					require.Zero(t, actual.RouteGeneration)
					require.Zero(t, actual.OwnerRouteGeneration)
					require.Empty(t, actual.AcceptedRouteKey)
					require.Empty(t, actual.Routes["route"].Model)
					require.Empty(t, actual.Routes["route"].Protocol)
				} else {
					require.EqualValues(t, 9, actual.RouteGeneration)
					require.EqualValues(t, 8, actual.OwnerRouteGeneration, "accepted-but-in-flight and completed owner generations are distinct")
					require.Equal(t, "route", actual.AcceptedRouteKey)
					require.Equal(t, "gpt-test", actual.Routes["route"].Model)
					require.Equal(t, "openai", actual.Routes["route"].Protocol)
					require.Equal(t, "old-route", actual.LastRouteKey, "repository decode must not promote an accepted in-flight route to completed owner")
				}
				version, err = tx.Write(upstreamcacheguard.SessionKind, "session", &actual)
				require.NoError(t, err)
				require.EqualValues(t, 5, version)
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
