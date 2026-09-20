package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

var _ service.PluginRequestGuardRepository = (*pluginRepository)(nil)

const pluginRequestAdmissionSQL = `WITH ready AS (
	SELECT id FROM sub2api_plugin_installations p
	WHERE id=$1 AND binary_sha256=$2 AND config_revision=$3 AND state='enabled'
	AND EXISTS (SELECT 1 FROM sub2api_plugin_bindings b WHERE b.plugin_id=p.id AND b.enabled)
	FOR SHARE
) INSERT INTO sub2api_plugin_runtime_requests(plugin_id,request_id)
SELECT id,$4 FROM ready`

func newPluginRequestGuardTestRepo(t *testing.T) (*pluginRepository, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	})
	return &pluginRepository{db: db}, mock
}

func TestPluginRequestGuardsBeginAtomicAdmission(t *testing.T) {
	repo, mock := newPluginRequestGuardTestRepo(t)
	// One statement must lock the exact enabled installation, check its binary,
	// configuration and binding, and insert before releasing the SHARE lock.
	// Splitting the read and insert would permit an upgrade to observe zero work.
	mock.ExpectExec(regexp.QuoteMeta(pluginRequestAdmissionSQL)).
		WithArgs(int64(42), "binary-sha", uint64(7), "request-a").
		WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, repo.BeginPluginRequest(context.Background(), 42, "binary-sha", 7, "request-a"))
}

func TestPluginRequestGuardsBeginRejectsNoOrUnexpectedAdmission(t *testing.T) {
	for _, rows := range []int64{0, 2} {
		t.Run(map[int64]string{0: "not_admitted", 2: "unexpected_row_count"}[rows], func(t *testing.T) {
			repo, mock := newPluginRequestGuardTestRepo(t)
			mock.ExpectExec(regexp.QuoteMeta(pluginRequestAdmissionSQL)).
				WithArgs(int64(42), "sha", uint64(7), "request-a").WillReturnResult(sqlmock.NewResult(0, rows))
			err := repo.BeginPluginRequest(context.Background(), 42, "sha", 7, "request-a")
			require.ErrorIs(t, err, service.ErrPluginStateChanged)
		})
	}
}

func TestPluginRequestGuardsBeginPropagatesDatabaseErrors(t *testing.T) {
	for _, phase := range []string{"execution", "rows_affected"} {
		t.Run(phase, func(t *testing.T) {
			repo, mock := newPluginRequestGuardTestRepo(t)
			failure := errors.New("database failure")
			expect := mock.ExpectExec(regexp.QuoteMeta(pluginRequestAdmissionSQL)).WithArgs(int64(42), "sha", uint64(7), "request-a")
			if phase == "execution" {
				expect.WillReturnError(failure)
			} else {
				expect.WillReturnResult(sqlmock.NewErrorResult(failure))
			}
			err := repo.BeginPluginRequest(context.Background(), 42, "sha", 7, "request-a")
			require.ErrorIs(t, err, failure)
			require.NotErrorIs(t, err, service.ErrPluginStateChanged)
		})
	}
}

func TestPluginRequestGuardsEndIsScopedAndIdempotent(t *testing.T) {
	for _, rows := range []int64{0, 1} {
		t.Run(map[int64]string{0: "already_ended", 1: "registered"}[rows], func(t *testing.T) {
			repo, mock := newPluginRequestGuardTestRepo(t)
			mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1 AND request_id=$2`)).
				WithArgs(int64(42), "request-a").WillReturnResult(sqlmock.NewResult(0, rows))
			require.NoError(t, repo.EndPluginRequest(context.Background(), 42, "request-a"))
		})
	}
}

func TestPluginRequestGuardsEndPropagatesDatabaseError(t *testing.T) {
	repo, mock := newPluginRequestGuardTestRepo(t)
	failure := errors.New("delete failed")
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1 AND request_id=$2`)).
		WithArgs(int64(42), "request-a").WillReturnError(failure)
	require.ErrorIs(t, repo.EndPluginRequest(context.Background(), 42, "request-a"), failure)
}

func TestPluginRequestGuardsCountIncludesAllRegisteredRequests(t *testing.T) {
	for _, count := range []int64{0, 3} {
		t.Run(map[int64]string{0: "drained", 3: "still_running"}[count], func(t *testing.T) {
			repo, mock := newPluginRequestGuardTestRepo(t)
			// No age/expiry filter: a crashed or old registration still blocks drain.
			mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1`)).
				WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(count))
			actual, err := repo.PluginRequestsInFlight(context.Background(), 42)
			require.NoError(t, err)
			require.Equal(t, count, actual)
		})
	}
}

func TestPluginRequestGuardsCountPropagatesErrors(t *testing.T) {
	for _, phase := range []string{"execution", "scan"} {
		t.Run(phase, func(t *testing.T) {
			repo, mock := newPluginRequestGuardTestRepo(t)
			failure := errors.New("count failed")
			query := mock.ExpectQuery(regexp.QuoteMeta(`SELECT COUNT(*) FROM sub2api_plugin_runtime_requests WHERE plugin_id=$1`)).WithArgs(int64(42))
			if phase == "execution" {
				query.WillReturnError(failure)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow("invalid count"))
			}
			_, err := repo.PluginRequestsInFlight(context.Background(), 42)
			require.Error(t, err, "failure must not be mistaken for a drained plugin")
			if phase == "execution" {
				require.ErrorIs(t, err, failure)
			}
		})
	}
}
