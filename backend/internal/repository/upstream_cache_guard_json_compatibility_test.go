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
		name    string
		raw     string
		capture bool
	}{{"legacy", `{"request_id":"r","sample":true,"warm_at_start":true}`, false}, {"capture", `{"request_id":"r","sample":true,"warm_at_start":true,"capture_only":true}`, true}} {
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
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestCacheGuardSessionRepositoryMarshalRetainsTrialSuppressionAndLegacyDefaults(t *testing.T) {
	for _, raw := range []string{`{"key":"session","natural_trial_ordinal":7}`, `{"key":"session","natural_trial_ordinal":7,"natural_trial_suppressed":true}`} {
		var session upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal([]byte(raw), &session))
		suppressed := session.NaturalTrialSuppressed
		session.Admissions = map[string]upstreamcacheguard.Admission{"r": {CaptureOnly: true}}
		encoded, err := marshalCacheGuardRuntime(upstreamcacheguard.SessionKind, &session)
		require.NoError(t, err)
		var decoded upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Equal(t, suppressed, decoded.NaturalTrialSuppressed)
		require.Equal(t, int64(7), decoded.NaturalTrialOrdinal)
		require.Nil(t, decoded.Admissions)
		require.True(t, session.Admissions["r"].CaptureOnly, "marshaling must not mutate caller admissions")
	}
}

func TestCacheGuardOutboxRepositoryPreservesCompletionReasonAndLegacyJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	at := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	evidence := upstreamcacheguard.Evidence{ID: "new", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5}, Admission: upstreamcacheguard.Admission{CaptureOnly: true, Sample: true}, CompletionReason: "usage_absent", CompletedAt: at}
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
	require.Empty(t, got[1].CompletionReason)
	require.False(t, got[1].Admission.CaptureOnly)
	require.NoError(t, mock.ExpectationsWereMet())
}
