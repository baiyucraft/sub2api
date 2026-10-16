package repository

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountQualityCandidateIDsSelectsOnlyIDsWithStableOrderAndScope(t *testing.T) {
	var captured string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &captured}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	repo := newAccountRepositoryWithSQL(client, db, nil)
	mock.ExpectQuery("lean IDs").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(12)).AddRow(int64(11)))
	ids, err := repo.AccountQualityCandidateIDs(context.Background(), service.AccountQualityCandidatesQuery{Scope: service.AccountListScopeUpstream, SortBy: "name", SortOrder: "desc"})
	require.NoError(t, err)
	require.Equal(t, []int64{12, 11}, ids)
	normalized := normalizeSQLWhitespace(captured)
	selectClause, _, _ := strings.Cut(normalized, " FROM ")
	require.NotContains(t, selectClause, "credentials")
	require.NotContains(t, selectClause, "extra")
	require.Contains(t, normalized, `"upstream_key_id" IS NOT NULL`)
	require.Contains(t, normalized, `ORDER BY "accounts"."name" DESC, "accounts"."id" DESC`)
	require.NoError(t, mock.ExpectationsWereMet())
}
