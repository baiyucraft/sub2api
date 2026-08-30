package repository

import (
	"context"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbupstreamconfig "github.com/Wei-Shaw/sub2api/ent/upstreamconfig"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUpstreamConfigListOrderDefaultsToIDAscending(t *testing.T) {
	for _, params := range []pagination.PaginationParams{
		{},
		{SortBy: "unknown", SortOrder: "desc"},
	} {
		query := upstreamConfigOrderSQL(params, true)
		require.Contains(t, query, "ORDER BY `upstream_configs`.`id` ASC")
	}
}

func TestUpstreamConfigListOrderSupportsAllowlistedFields(t *testing.T) {
	tests := []struct {
		name     string
		sortBy   string
		order    string
		contains []string
	}{
		{name: "id desc", sortBy: "id", order: "desc", contains: []string{"ORDER BY `upstream_configs`.`id` DESC"}},
		{name: "name", sortBy: "name", order: "asc", contains: []string{"LOWER(`upstream_configs`.`name`) ASC", "`upstream_configs`.`id` ASC"}},
		{name: "provider", sortBy: "provider", order: "desc", contains: []string{"LOWER(`upstream_configs`.`provider`) DESC", "`upstream_configs`.`id` ASC"}},
		{name: "auth mode", sortBy: "auth_mode", order: "asc", contains: []string{"LOWER(`upstream_configs`.`auth_mode`) ASC", "`upstream_configs`.`id` ASC"}},
		{name: "scheduling", sortBy: "scheduling_enabled", order: "desc", contains: []string{"`upstream_configs`.`scheduling_enabled` DESC", "`upstream_configs`.`id` ASC"}},
		{name: "last success", sortBy: "last_success_at", order: "asc", contains: []string{"`upstream_configs`.`last_success_at` NULLS LAST", "`upstream_configs`.`id` ASC"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := upstreamConfigOrderSQL(pagination.PaginationParams{SortBy: tt.sortBy, SortOrder: tt.order}, true)
			for _, fragment := range tt.contains {
				require.Contains(t, query, fragment)
			}
		})
	}
}

func TestUpstreamConfigBalanceSortUsesFreshLatestSnapshot(t *testing.T) {
	query := upstreamConfigOrderSQL(pagination.PaginationParams{SortBy: "balance_cny", SortOrder: "desc"}, true)
	require.Contains(t, query, "FROM upstream_balance_snapshots AS snapshot")
	require.Contains(t, query, "snapshot.observed_at >= CURRENT_TIMESTAMP - INTERVAL '24 hours'")
	require.Contains(t, query, "ORDER BY snapshot.observed_at DESC, snapshot.id DESC")
	require.Contains(t, query, "DESC NULLS LAST")
	require.Contains(t, query, "`upstream_configs`.`id` ASC")
}

func TestUpstreamConfigBalanceSortWithoutThresholdFallsBackToID(t *testing.T) {
	query := upstreamConfigOrderSQL(pagination.PaginationParams{SortBy: "balance_cny", SortOrder: "desc"}, false)
	require.NotContains(t, query, "upstream_balance_snapshots")
	require.Contains(t, query, "ORDER BY `upstream_configs`.`id` ASC")
}

func TestUpstreamConfigConcurrencySortMatchesResolvedPriority(t *testing.T) {
	query := upstreamConfigOrderSQL(pagination.PaginationParams{SortBy: "upstream_concurrency", SortOrder: "desc"}, true)
	for _, fragment := range []string{
		"scheduler_concurrency_override",
		"upstream_concurrency_snapshot,status",
		"provider_defined",
		"unlimited",
		"THEN 1000001",
		"ELSE 100",
		"`upstream_configs`.`id` ASC",
	} {
		require.Contains(t, query, fragment)
	}
}

func TestUpstreamConfigListAppliesOrderBeforePagination(t *testing.T) {
	var capturedSQL string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(captureEntQueryMatcher{actual: &capturedSQL}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	driver := entsql.OpenDB(dialect.Postgres, db)
	client := dbent.NewClient(dbent.Driver(driver))
	t.Cleanup(func() { _ = client.Close() })
	repo := &upstreamConfigRepository{client: client}

	mock.ExpectQuery("count upstream configs").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("list upstream configs").
		WillReturnRows(sqlmock.NewRows(dbupstreamconfig.Columns))

	_, _, err = repo.List(context.Background(), pagination.PaginationParams{
		Page: 2, PageSize: 10, SortBy: "name", SortOrder: "desc",
	}, service.UpstreamConfigListFilter{})
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())

	query := strings.Join(strings.Fields(capturedSQL), " ")
	orderIndex := strings.Index(query, "ORDER BY LOWER")
	limitIndex := strings.Index(query, "LIMIT")
	require.GreaterOrEqual(t, orderIndex, 0, query)
	require.Greater(t, limitIndex, orderIndex, query)
}

func upstreamConfigOrderSQL(params pagination.PaginationParams, balanceSortAvailable bool) string {
	table := entsql.Table("upstream_configs")
	selector := entsql.Select(table.C("id")).From(table)
	for _, order := range upstreamConfigListOrder(params, balanceSortAvailable) {
		order(selector)
	}
	query, _ := selector.Query()
	return strings.Join(strings.Fields(query), " ")
}
