package repository

import (
	"context"
	"encoding/json"
	"errors"
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
	mock.ExpectExec(regexp.QuoteMeta(`SELECT pg_advisory_xact_lock(296, ($1 % 2147483647)::integer)`)).WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
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
	e := upstreamcacheguard.Evidence{ID: "test", Request: upstreamcacheguard.Request{GroupID: 9, AccountID: 2}, CompletedAt: at}
	raw, _ := json.Marshal(e)
	mock.ExpectExec(`INSERT INTO upstream_cache_guard_facts.*FALSE.*DO NOTHING`).WithArgs(int64(9), "test", int64(2), at, false, string(raw)).WillReturnResult(sqlmock.NewResult(0, 1))
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
	mock.ExpectExec(`DELETE FROM upstream_cache_guard_facts.*processed=TRUE AND completed_at`).WithArgs(at).WillReturnResult(sqlmock.NewResult(0, 1))
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
			e := upstreamcacheguard.Evidence{ID: "id", Request: upstreamcacheguard.Request{AccountID: 2}, CompletedAt: time.Now().UTC()}
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(int64(9)).WillReturnResult(sqlmock.NewResult(0, 1))
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
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WillReturnResult(sqlmock.NewResult(0, 1))
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
