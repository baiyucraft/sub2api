package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

func TestUpstreamCacheGuardTransactionLockRollback(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db)
	mock.ExpectBegin()
	mock.ExpectRollback()
	want := errors.New("write failed")
	if got := repo.Transact(context.Background(), 9, func(upstreamcacheguard.Tx) error { return want }); !errors.Is(got, want) {
		t.Fatalf("got=%v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardOutboxLeaseAndRetention(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db)
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	e := upstreamcacheguard.Evidence{ID: "test", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: at}
	raw, _ := json.Marshal(e)
	mock.ExpectExec(`INSERT INTO upstream_cache_guard_facts.*FALSE.*DO NOTHING`).WithArgs(int64(9), "test", int64(2), int64(5), at, false, string(raw)).WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.Enqueue(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`WITH pending AS .*processed=FALSE.*claim_until.*FOR UPDATE SKIP LOCKED.*RETURNING f.evidence_json`).WithArgs(32, "owner", int64(10000)).WillReturnRows(sqlmock.NewRows([]string{"evidence_json"}).AddRow(raw))
	pending, err := repo.ClaimPending(context.Background(), "owner", 32, 10*time.Second)
	if err != nil || len(pending) != 1 || pending[0].ID != e.ID {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	mock.ExpectQuery(`SELECT EXISTS.*processed=FALSE`).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	if p, err := repo.HasPending(context.Background()); err != nil || !p {
		t.Fatalf("pending detection %v %v", p, err)
	}
	mock.ExpectQuery(`(?s)WITH expired_admissions AS .*completed=TRUE AND started_at.*deleted_admissions.*expired_facts AS .*processed=TRUE AND completed_at.*deleted_facts`).WithArgs(at, 1000).WillReturnRows(sqlmock.NewRows([]string{"deleted"}).AddRow(1))
	if err := repo.Cleanup(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardProcessedAckAtomicAndDuplicate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		affected int64
		fail     bool
	}{{"ack", 1, false}, {"duplicate", 0, false}, {"rollback", 1, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := NewUpstreamCacheGuardRepository(db)
			e := upstreamcacheguard.Evidence{ID: "id", Request: upstreamcacheguard.Request{AccountID: 2, UpstreamKeyID: 5}, CompletedAt: time.Now().UTC()}
			mock.ExpectBegin()
			mock.ExpectExec(`INSERT INTO upstream_cache_guard_facts.*processed.*TRUE.*DO UPDATE SET processed=TRUE.*processed=FALSE`).WillReturnResult(sqlmock.NewResult(0, tc.affected))
			if tc.fail {
				mock.ExpectRollback()
			} else {
				mock.ExpectCommit()
			}
			err = repo.Transact(context.Background(), 9, func(tx upstreamcacheguard.Tx) error {
				inserted, err := tx.InsertFact(e)
				if err != nil {
					return err
				}
				if inserted != (tc.affected == 1) {
					t.Fatalf("inserted=%v", inserted)
				}
				if tc.fail {
					return errors.New("runtime write failed")
				}
				return nil
			})
			if (err != nil) != tc.fail {
				t.Fatalf("err=%v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamCacheGuardHistoryIncludesFailureBarriers(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db)
	at := time.Now().UTC()
	failed := upstreamcacheguard.Evidence{ID: "failed", Success: false}
	raw, _ := json.Marshal(failed)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT evidence_json FROM upstream_cache_guard_facts WHERE group_id=$1 AND account_id=$2 AND processed=TRUE AND completed_at>=$3 ORDER BY completed_at,evidence_id LIMIT $4`)).WithArgs(int64(9), int64(2), at, upstreamcacheguard.MaxFacts+1).WillReturnRows(sqlmock.NewRows([]string{"evidence_json"}).AddRow(raw))
	mock.ExpectCommit()
	if err := repo.Transact(context.Background(), 9, func(tx upstreamcacheguard.Tx) error {
		facts, err := tx.Facts(2, at)
		if len(facts) != 1 || facts[0].Success {
			t.Fatalf("failure barrier missing: %v", facts)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardPendingAgeQuery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending bool
		fail    bool
	}{{"old pending", true, false}, {"within budget or processed", false, false}, {"database unavailable", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := NewUpstreamCacheGuardRepository(db)
			cutoff := time.Now().UTC().Add(-upstreamcacheguard.RefreshLifetime)
			expected := mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM upstream_cache_guard_facts WHERE processed=FALSE AND completed_at<$1)`)).WithArgs(cutoff)
			if tc.fail {
				expected.WillReturnError(errors.New("database unavailable"))
			} else {
				expected.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.pending))
			}
			pending, err := repo.HasPendingBefore(context.Background(), cutoff)
			if pending != tc.pending || (err != nil) != tc.fail {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamCacheGuardAccountTransactionLockOrder(t *testing.T) {
	for _, quota := range []bool{false, true} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AccountStore)
			mock.ExpectBegin()
			mock.ExpectExec(`pg_advisory_xact_lock\(298`).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`pg_advisory_xact_lock\(299,hashtext`).WithArgs(int64(9), "session").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery(`SELECT state_json, version FROM upstream_cache_guard_runtime`).WithArgs(int64(9), upstreamcacheguard.SessionKind, "session").WillReturnRows(sqlmock.NewRows([]string{"state_json", "version"}).AddRow(`{}`, 1))
			if quota {
				mock.ExpectExec(`pg_advisory_xact_lock\(296`).WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery(`SELECT state_json, version FROM upstream_cache_guard_runtime`).WithArgs(int64(9), upstreamcacheguard.GroupKind, "quota").WillReturnRows(sqlmock.NewRows([]string{"state_json", "version"}).AddRow(`{}`, 1))
			}
			mock.ExpectCommit()
			err = repo.TransactAccount(context.Background(), 9, 2, 5, func(tx upstreamcacheguard.Tx) error {
				var session upstreamcacheguard.SessionState
				if _, err := tx.Read(upstreamcacheguard.SessionKind, "session", &session); err != nil {
					return err
				}
				if quota {
					var group upstreamcacheguard.GroupState
					_, err := tx.Read(upstreamcacheguard.GroupKind, "quota", &group)
					return err
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestUpstreamCacheGuardAccountStateAndCrossGroupFacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AccountStore)
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	state := upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 5, Phase: upstreamcacheguard.Degraded}
	rawState, _ := json.Marshal(state)
	first := upstreamcacheguard.Evidence{ID: "first", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: at}
	second := upstreamcacheguard.Evidence{ID: "second", Request: upstreamcacheguard.Request{GroupID: 10, AccountID: 2, UpstreamKeyID: 5}, CompletedAt: at.Add(time.Second)}
	rawFirst, _ := json.Marshal(first)
	rawSecond, _ := json.Marshal(second)
	mock.ExpectBegin()
	mock.ExpectExec(`pg_advisory_xact_lock\(298`).WithArgs(int64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT state_json,version FROM upstream_cache_guard_accounts`).WithArgs(int64(2), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"state_json", "version"}).AddRow(rawState, 4))
	mock.ExpectQuery(`INSERT INTO upstream_cache_guard_accounts.*RETURNING version`).WithArgs(int64(2), int64(5), string(rawState)).WillReturnRows(sqlmock.NewRows([]string{"version"}).AddRow(5))
	mock.ExpectQuery(`SELECT evidence_json FROM upstream_cache_guard_facts WHERE account_id=.*upstream_key_id=.*processed=TRUE.*ORDER BY completed_at,group_id,evidence_id`).WithArgs(int64(2), int64(5), at, upstreamcacheguard.MaxFacts+1).WillReturnRows(sqlmock.NewRows([]string{"evidence_json"}).AddRow(rawFirst).AddRow(rawSecond))
	mock.ExpectQuery(`(?s)SELECT legacy.group_id,legacy.state_json FROM upstream_cache_guard_runtime AS legacy.*JOIN accounts AS account.*account.upstream_key_id=\$2.*WHERE legacy.state_kind='account'`).WithArgs(int64(2), int64(5), upstreamcacheguard.MaxRuntimeRows+1).WillReturnRows(sqlmock.NewRows([]string{"group_id", "state_json"}).AddRow(9, rawState).AddRow(10, rawState))
	mock.ExpectCommit()
	err = repo.TransactAccount(context.Background(), 9, 2, 5, func(rawTx upstreamcacheguard.Tx) error {
		tx := rawTx.(upstreamcacheguard.AccountTx)
		if _, err := tx.ReadAccount(2, 6, &state); err == nil {
			t.Fatal("cross-key read should fail")
		}
		var read upstreamcacheguard.AccountState
		if v, err := tx.ReadAccount(2, 5, &read); err != nil || v != 4 || read.Phase != upstreamcacheguard.Degraded {
			t.Fatalf("read v=%d state=%+v err=%v", v, read, err)
		}
		if v, err := tx.WriteAccount(2, 5, state); err != nil || v != 5 {
			t.Fatalf("write v=%d err=%v", v, err)
		}
		facts, err := tx.AccountFacts(2, 5, at)
		if err != nil || len(facts) != 2 || facts[0].Request.GroupID != 9 || facts[1].Request.GroupID != 10 {
			t.Fatalf("cross-group facts=%+v err=%v", facts, err)
		}
		legacy, err := tx.LegacyAccounts(2, 5)
		if err != nil || len(legacy) != 2 || legacy[0].GroupID != 9 || legacy[1].GroupID != 10 {
			t.Fatalf("legacy states=%+v err=%v", legacy, err)
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

func TestUpstreamCacheGuardAccountRowsIncludeCurrentKeyLegacyOnly(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AccountStore)
	at := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	state := []byte(`{"account_id":2,"upstream_key_id":5}`)
	mock.ExpectQuery(`(?s)FROM upstream_cache_guard_accounts AS state.*JOIN accounts AS account.*UNION ALL.*JOIN accounts AS a.*a.upstream_key_id.*NOT EXISTS.*LIMIT`).WithArgs(upstreamcacheguard.MaxRuntimeRows+1, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows([]string{"account_id", "upstream_key_id", "version", "state_json", "updated_at"}).AddRow(2, 5, 3, state, at).AddRow(3, 6, 0, nil, at))
	rows, err := repo.AccountRows(context.Background())
	if err != nil || len(rows) != 2 || rows[0].Version != 3 || rows[1].Version != 0 || rows[1].AccountID != 3 || rows[1].UpstreamKeyID != 6 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardAccountWindowsAggregatesCurrentKey(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AccountStore)
	since := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	until := since.Add(time.Hour)
	columns := []string{"account_id", "upstream_key_id", "version", "state_json", "valid_requests", "input_tokens", "read_tokens", "trial_valid_requests", "trial_input_tokens", "trial_read_tokens", "trial_sessions", "last_evidence_at", "oldest", "future", "group_ids", "trial_group_ids", "policy_windows"}
	firstState, _ := json.Marshal(upstreamcacheguard.AccountState{AccountID: 2, UpstreamKeyID: 5})
	secondState, _ := json.Marshal(upstreamcacheguard.AccountState{AccountID: 3, UpstreamKeyID: 6})
	mock.ExpectQuery(`(?s)WITH states AS MATERIALIZED .*fact.processed=TRUE AND fact.valid_sample=TRUE.*fact.completed_at <= \$2.*legacy_quality_since.*COUNT\(DISTINCT \(fact.group_id,fact.session_key\)\).*LIMIT \$3`).WithArgs(since, until, upstreamcacheguard.MaxRuntimeRows+1, sqlmock.AnyArg()).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(2, 5, 4, firstState, 2, 110, 60, 1, 10, 0, 1, since.Add(time.Second), since.Add(time.Second), nil, "{9,10}", "{10}", "{}").
		AddRow(3, 6, 1, secondState, 0, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, nil, "{}"))
	windows, err := repo.AccountWindows(context.Background(), since, until)
	if err != nil || len(windows) != 2 {
		t.Fatalf("windows=%+v err=%v", windows, err)
	}
	w := windows["2:5"]
	if w.Version != 4 || w.ValidRequests != 2 || w.TotalInputTokens != 110 || w.CacheReadTokens != 60 ||
		w.TrialSessions != 1 || len(w.GroupIDs) != 2 || w.GroupIDs[1] != 10 || len(w.TrialGroupIDs) != 1 || w.TrialGroupIDs[0] != 10 ||
		w.LastEvidenceAt != since.Add(time.Second) {
		t.Fatalf("window=%+v", w)
	}
	if windows["3:6"].ValidRequests != 0 || !windows["3:6"].LastEvidenceAt.IsZero() {
		t.Fatalf("empty current-key window=%+v", windows["3:6"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardAccountWindowsForFiltersOuterAccountRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.IncrementalStore)
	now := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	// Filtering only eligible facts still returns every LEFT JOIN account with
	// zero metrics. The account relation itself must have the same restriction.
	mock.ExpectQuery(`(?s)WITH states AS MATERIALIZED \(.*FROM upstream_cache_guard_accounts AS state.*WHERE \(\$4::bigint\[\] IS NULL OR state.account_id=ANY\(\$4\)\).*FROM states AS state.*LEFT JOIN account_metrics AS fact ON`).
		WithArgs(now.Add(-time.Hour), now, upstreamcacheguard.MaxRuntimeRows+1, "{2}").
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "upstream_key_id", "version", "state_json", "valid_requests", "input_tokens", "read_tokens", "trial_valid_requests", "trial_input_tokens", "trial_read_tokens", "trial_sessions", "last_evidence_at", "oldest", "future", "group_ids", "trial_group_ids", "policy_windows"}).
			AddRow(2, 5, 4, `{"account_id":2,"upstream_key_id":5}`, 2, 110, 60, 0, 0, 0, 0, now, now, nil, "{1}", nil, "{}"))
	windows, err := repo.AccountWindowsFor(context.Background(), []int64{2}, now.Add(-time.Hour), now)
	if err != nil || len(windows) != 1 || windows["2:5"].CacheReadTokens != 60 {
		t.Fatalf("targeted windows=%+v err=%v", windows, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamCacheGuardAccountWindowDecodeFailureIsIsolated(t *testing.T) {
	for _, invalid := range []struct{ name, state, policy string }{
		{"state", `{"account_id":"invalid"}`, `{}`},
		{"policy", `{"account_id":2,"upstream_key_id":5}`, `{"bad":"invalid"}`},
		{"identity", `{"account_id":3,"upstream_key_id":5}`, `{}`},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := NewUpstreamCacheGuardRepository(db).(upstreamcacheguard.AccountStore)
			now := time.Now().UTC()
			columns := []string{"account_id", "upstream_key_id", "version", "state_json", "valid_requests", "input_tokens", "read_tokens", "trial_valid_requests", "trial_input_tokens", "trial_read_tokens", "trial_sessions", "last_evidence_at", "oldest", "future", "group_ids", "trial_group_ids", "policy_windows"}
			mock.ExpectQuery(`(?s)WITH states AS MATERIALIZED`).WillReturnRows(sqlmock.NewRows(columns).
				AddRow(2, 5, 1, invalid.state, 0, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, nil, invalid.policy).
				AddRow(3, 6, 1, `{"account_id":3,"upstream_key_id":6}`, 21, 1000, 900, 0, 0, 0, 0, nil, nil, nil, nil, nil, `{}`))
			windows, err := repo.AccountWindows(context.Background(), now.Add(-time.Hour), now)
			if err != nil || len(windows) != 2 || windows["2:5"].Err == nil || windows["3:6"].Err != nil || windows["3:6"].CacheReadTokens != 900 {
				t.Fatalf("bad account poisoned batch: windows=%+v err=%v", windows, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
