//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUsageLogRepositoryQualityActivityIncludesUntimedStream(t *testing.T) {
	ctx := context.Background()
	tx := testTx(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	suffix := now.UnixNano()

	var userID, accountID, apiKeyID int64
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO users (email, password_hash) VALUES ($1, 'test') RETURNING id
	`, fmt.Sprintf("quality-activity-%d@example.com", suffix)).Scan(&userID))
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type) VALUES ($1, 'openai', 'apikey') RETURNING id
	`, fmt.Sprintf("quality-activity-%d", suffix)).Scan(&accountID))
	require.NoError(t, tx.QueryRowContext(ctx, `
		INSERT INTO api_keys (user_id, key, name) VALUES ($1, $2, 'quality activity') RETURNING id
	`, userID, fmt.Sprintf("sk-quality-activity-%d", suffix)).Scan(&apiKeyID))

	_, err := tx.ExecContext(ctx, `
		INSERT INTO usage_logs (
			user_id, api_key_id, account_id, request_id, model,
			actual_cost, stream, duration_ms, created_at
		) VALUES ($1, $2, $3, $4, 'gpt-test', 0.01, TRUE, NULL, $5)
	`, userID, apiKeyID, accountID, fmt.Sprintf("quality-activity-%d", suffix), now)
	require.NoError(t, err)

	repo := newUsageLogRepositoryWithSQL(nil, tx)
	stats, err := repo.GetAccountQualityStatsBatch(ctx, []int64{accountID}, now.Add(-24*time.Hour), now.Add(-time.Hour), now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(1), stats[accountID].SuccessfulRequests1h)
	require.Zero(t, stats[accountID].Recent1h.Last10.SampleCount)
	require.Zero(t, stats[accountID].Last24h.Last10.SampleCount)
	require.WithinDuration(t, now, *stats[accountID].LastSuccessAt, time.Millisecond)
}
