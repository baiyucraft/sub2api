package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

func TestCacheGuardSessionBodyOmitsLedgerWithoutMutatingCaller(t *testing.T) {
	session := upstreamcacheguard.SessionState{Key: "session", Admissions: map[string]upstreamcacheguard.Admission{"request": {RequestID: "request"}}, NaturalTrialOrdinal: 7}
	for _, value := range []any{session, &session} {
		raw, err := marshalCacheGuardRuntime(upstreamcacheguard.SessionKind, value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if _, exists := decoded["admissions"]; exists || len(session.Admissions) != 1 || string(decoded["natural_trial_ordinal"]) != "7" {
			t.Fatalf("session identity or caller changed: raw=%s caller=%+v", raw, session)
		}
	}
}

func TestCacheGuardSplitAdmissionQueriesKeepSessionLock(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db)
	at := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	a := upstreamcacheguard.Admission{RequestID: "request", GroupID: 9, SessionKey: "session", AccountID: 2, UpstreamKeyID: 5, RouteKey: "route", StartedAt: at, Sample: true}
	raw, _ := json.Marshal(a)
	mock.ExpectBegin()
	// All ledger operations share exactly one session advisory lock.
	mock.ExpectExec(`pg_advisory_xact_lock\(299,hashtext`).WithArgs(int64(9), "session").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT admission_json FROM upstream_cache_guard_admissions WHERE group_id=\$1 AND session_key=\$2 AND admission_key=\$3`).WithArgs(int64(9), "session", "missing").WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(`(?s)INSERT INTO upstream_cache_guard_admissions.*ON CONFLICT.*EXCLUDED.completed AND NOT upstream_cache_guard_admissions.completed`).WithArgs(int64(9), "session", "request-key", int64(2), int64(5), "route", at, false, string(raw)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT admission_json FROM upstream_cache_guard_admissions WHERE group_id=\$1 AND session_key=\$2 AND admission_key=\$3`).WithArgs(int64(9), "session", "request-key").WillReturnRows(sqlmock.NewRows([]string{"admission_json"}).AddRow(raw))
	mock.ExpectQuery(`(?s)SELECT admission_json FROM upstream_cache_guard_admissions.*completed=FALSE AND \(route_key=\$4 OR route_key=''\)`).WithArgs(int64(9), "session", int64(2), "route", upstreamcacheguard.MaxFacts+1).WillReturnRows(sqlmock.NewRows([]string{"admission_json"}).AddRow(raw))
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\) FROM .*completed=FALSE.*UNION ALL.*completed=TRUE AND started_at>=\$3.*LIMIT 100000`).WithArgs(int64(9), "session", at.Add(-48*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1650))
	mock.ExpectCommit()
	err = repo.Transact(context.Background(), 9, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AdmissionTx)
		if _, exists, err := tx.ReadAdmission("session", "missing"); err != nil || exists {
			t.Fatalf("missing exists=%v err=%v", exists, err)
		}
		if err := tx.WriteAdmission("session", "request-key", a); err != nil {
			return err
		}
		if got, exists, err := tx.ReadAdmission("session", "request-key"); err != nil || !exists || got.RequestID != a.RequestID || !got.Sample {
			t.Fatalf("admission=%+v exists=%v err=%v", got, exists, err)
		}
		pending, err := tx.PendingAdmissions("session", 2, "route")
		if err != nil || len(pending) != 1 || pending[0].Completed {
			t.Fatalf("pending=%+v err=%v", pending, err)
		}
		count, err := tx.RetainedAdmissionCount("session", at.Add(-48*time.Hour))
		if err != nil || count != 1650 {
			t.Fatalf("count=%d err=%v", count, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGuardAdmissionHealthReadsOnlyUnprocessedFactsAndExplicitFailures(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AdmissionStore)
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	cutoff := now.Add(-upstreamcacheguard.RefreshLifetime)
	mock.ExpectQuery(`(?s)SELECT account_id,upstream_key_id,MIN\(completed_at\).*FROM upstream_cache_guard_facts.*WHERE processed=FALSE AND completed_at<\$1.*GROUP BY account_id,upstream_key_id.*ORDER BY account_id,upstream_key_id LIMIT \$2`).WithArgs(cutoff, upstreamcacheguard.MaxRuntimeRows+1).WillReturnRows(sqlmock.NewRows([]string{"account_id", "upstream_key_id", "completed_at"}).AddRow(2, 5, now.Add(-10*time.Minute)))
	mock.ExpectQuery(`(?s)FROM unnest.*WHERE EXISTS.*a.admission_key=request.admission_key AND a.completed=TRUE`).WithArgs("{2}", "{5}", `{"opaque-key"}`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "key_id", "admission_key"}).AddRow(2, 5, "opaque-key"))
	health, err := repo.AdmissionHealth(context.Background(), cutoff, []upstreamcacheguard.AdmissionFailure{{AccountID: 2, UpstreamKeyID: 5, Key: "opaque-key"}})
	if err != nil || len(health.PendingFacts) != 1 || health.PendingFacts[0].AccountID != 2 || health.PendingFacts[0].UpstreamKeyID != 5 || !health.PendingFacts[0].CompletedAt.Equal(now.Add(-10*time.Minute)) || len(health.Acknowledged) != 1 || health.Acknowledged[0].Key != "opaque-key" {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGuardAdmissionHealthFailureIsExplicit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := errors.New("database unavailable")
	mock.ExpectQuery(`SELECT account_id,upstream_key_id,MIN`).WillReturnError(want)
	_, err = NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AdmissionStore).AdmissionHealth(context.Background(), time.Now(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("health failure=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGuardAdmissionHealthStreamFailureIsExplicit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := errors.New("stream interrupted")
	rows := sqlmock.NewRows([]string{"account_id", "upstream_key_id", "completed_at"}).AddRow(2, 5, time.Now()).RowError(0, want)
	mock.ExpectQuery(`SELECT account_id,upstream_key_id,MIN`).WillReturnRows(rows)
	_, err = NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AdmissionStore).AdmissionHealth(context.Background(), time.Now(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("health stream failure=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheGuardAdmissionHealthAcknowledgementFailureIsExplicit(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := errors.New("acknowledgement lookup unavailable")
	mock.ExpectQuery(`SELECT account_id,upstream_key_id,MIN`).WillReturnRows(sqlmock.NewRows([]string{"account_id", "upstream_key_id", "completed_at"}))
	mock.ExpectQuery(`(?s)FROM unnest.*WHERE EXISTS`).WillReturnError(want)
	_, err = NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AdmissionStore).AdmissionHealth(context.Background(), time.Now(), []upstreamcacheguard.AdmissionFailure{{AccountID: 2, UpstreamKeyID: 5, Key: "opaque-key"}})
	if !errors.Is(err, want) {
		t.Fatalf("acknowledgement failure=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
