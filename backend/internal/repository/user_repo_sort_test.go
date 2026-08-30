package repository

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserUsageSortExpression_AllowsSupportedKeys(t *testing.T) {
	t.Parallel()

	keys := []string{
		"usage_today_tokens",
		"usage_today_spend",
		"usage_today_cost",
		"usage_last_30d_tokens",
		"usage_last_30d_spend",
		"usage_last_30d_cost",
		"usage_lifetime_tokens",
		"usage_lifetime_spend",
		"usage_lifetime_cost",
		"usage_anthropic_today_spend",
		"usage_anthropic_last_30d_spend",
		"usage_openai_today_spend",
		"usage_openai_last_30d_spend",
		"usage_gemini_today_spend",
		"usage_gemini_last_30d_spend",
		"usage_antigravity_today_spend",
		"usage_antigravity_last_30d_spend",
	}
	for _, key := range keys {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			expr, ok := userUsageSortExpression(key)
			require.True(t, ok)
			require.NotNil(t, expr)
		})
	}
}

func TestUserUsageSortExpression_RejectsUnknownKeys(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"usage_unknown", "usage_openai_lifetime_spend", "sort_by_users"} {
		expr, ok := userUsageSortExpression(key)
		require.False(t, ok, key)
		require.Nil(t, expr, key)
	}
}
