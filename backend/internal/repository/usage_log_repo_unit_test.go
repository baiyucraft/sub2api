//go:build unit

package repository

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSafeDateFormat(t *testing.T) {
	tests := []struct {
		name        string
		granularity string
		expected    string
	}{
		// 合法值
		{"hour", "hour", "YYYY-MM-DD HH24:00"},
		{"day", "day", "YYYY-MM-DD"},
		{"week", "week", "IYYY-IW"},
		{"month", "month", "YYYY-MM"},

		// 非法值回退到默认
		{"空字符串", "", "YYYY-MM-DD"},
		{"未知粒度 year", "year", "YYYY-MM-DD"},
		{"未知粒度 minute", "minute", "YYYY-MM-DD"},

		// 恶意字符串
		{"SQL 注入尝试", "'; DROP TABLE users; --", "YYYY-MM-DD"},
		{"带引号", "day'", "YYYY-MM-DD"},
		{"带括号", "day)", "YYYY-MM-DD"},
		{"Unicode", "日", "YYYY-MM-DD"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeDateFormat(tc.granularity)
			require.Equal(t, tc.expected, got, "safeDateFormat(%q)", tc.granularity)
		})
	}
}

func TestBuildUsageLogBatchInsertQuery_UsesConflictDoNothing(t *testing.T) {
	upstreamConfigID := int64(41)
	upstreamKeyID := int64(42)
	currency := "CNY"
	toCNYRate := 1.0
	log := &service.UsageLog{
		UserID:                1,
		APIKeyID:              2,
		AccountID:             3,
		UpstreamConfigID:      &upstreamConfigID,
		UpstreamKeyID:         &upstreamKeyID,
		UpstreamCostCurrency:  &currency,
		UpstreamCostToCNYRate: &toCNYRate,
		RequestID:             "req-batch-no-update",
		Model:                 "gpt-5",
		InputTokens:           10,
		OutputTokens:          5,
		TotalCost:             1.2,
		ActualCost:            1.2,
		CreatedAt:             time.Now().UTC(),
	}
	prepared := prepareUsageLogInsert(log)

	query, args := buildUsageLogBatchInsertQuery([]string{usageLogBatchKey(log.RequestID, log.APIKeyID)}, map[string]usageLogInsertPrepared{
		usageLogBatchKey(log.RequestID, log.APIKeyID): prepared,
	})

	require.Contains(t, query, "ON CONFLICT (request_id, api_key_id) DO NOTHING")
	require.NotContains(t, strings.ToUpper(query), "DO UPDATE")
	require.Len(t, usageLogInsertArgTypes, 60)
	require.Len(t, prepared.args, 60)
	require.Len(t, args, 61)
	require.Equal(t, sql.NullInt64{Int64: upstreamConfigID, Valid: true}, prepared.args[3])
	require.Equal(t, sql.NullInt64{Int64: upstreamKeyID, Valid: true}, prepared.args[4])
	require.Equal(t, sql.NullString{String: currency, Valid: true}, prepared.args[29])
	require.Equal(t, &toCNYRate, prepared.args[30])
	require.Equal(t, 3, strings.Count(query, "upstream_config_id"))
	require.Equal(t, 3, strings.Count(query, "upstream_key_id"))
	require.Equal(t, 3, strings.Count(query, "upstream_cost_currency"))
	require.Equal(t, 3, strings.Count(query, "upstream_cost_to_cny_rate"))
	require.Contains(t, query, "$61::timestamptz")
}
