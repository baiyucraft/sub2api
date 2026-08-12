package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateUpstreamAccountEditableUpdate(t *testing.T) {
	t.Run("accepts account-local behavior controls", func(t *testing.T) {
		err := validateUpstreamAccountEditableUpdate(&UpdateAccountInput{
			Credentials: map[string]any{
				"model_mapping":                map[string]any{"gpt": "gpt-upstream"},
				"pool_mode":                    true,
				"pool_mode_retry_count":        float64(4),
				"pool_mode_retry_status_codes": []any{float64(401), float64(429), float64(503)},
			},
			Extra: map[string]any{
				"openai_passthrough": true,
				"quota_limit":        float64(100),
			},
		})
		require.NoError(t, err)
	})

	for name, input := range map[string]*UpdateAccountInput{
		"credential secret": {Credentials: map[string]any{"api_key": "secret"}},
		"runtime extra":     {Extra: map[string]any{UpstreamBillingProbeExtraKey: map[string]any{}}},
		"pool mode type":    {Credentials: map[string]any{"pool_mode": "true"}},
		"retry count range": {Credentials: map[string]any{"pool_mode_retry_count": float64(11)}},
		"status code range": {Credentials: map[string]any{"pool_mode_retry_status_codes": []any{float64(99)}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, validateUpstreamAccountEditableUpdate(input))
		})
	}
}

func TestMergeUpstreamAccountEditableExtraPreservesRuntimeState(t *testing.T) {
	merged := mergeUpstreamAccountEditableExtra(
		map[string]any{
			"openai_passthrough":         true,
			UpstreamBillingProbeExtraKey: map[string]any{"status": "ok"},
			"quota_used":                 float64(12),
		},
		map[string]any{"quota_limit": float64(100)},
	)

	require.NotContains(t, merged, "openai_passthrough")
	require.Equal(t, float64(100), merged["quota_limit"])
	require.Contains(t, merged, UpstreamBillingProbeExtraKey)
	require.Equal(t, float64(12), merged["quota_used"])
}

func TestMergeUpstreamAccountEditableCredentialsPreservesDerivedState(t *testing.T) {
	merged := mergeUpstreamAccountEditableCredentials(
		map[string]any{
			"pool_mode":       true,
			"provider_marker": "derived",
		},
		map[string]any{
			"model_mapping": map[string]any{"gpt": "gpt-upstream"},
			"pool_mode":     true,
		},
	)

	require.Equal(t, true, merged["pool_mode"])
	require.Equal(t, map[string]any{"gpt": "gpt-upstream"}, merged["model_mapping"])
	require.Equal(t, "derived", merged["provider_marker"])
}

type upstreamAccountDefaultRepo struct {
	AccountRepository
	created []*Account
}

func (r *upstreamAccountDefaultRepo) ListByUpstreamKeyID(context.Context, int64) ([]Account, error) {
	return nil, nil
}

func (r *upstreamAccountDefaultRepo) Create(_ context.Context, account *Account) error {
	copyAccount := *account
	copyAccount.Credentials = make(map[string]any, len(account.Credentials))
	for key, value := range account.Credentials {
		copyAccount.Credentials[key] = value
	}
	r.created = append(r.created, &copyAccount)
	return nil
}

func TestReconcileUpstreamAccountsUsesOperationalDefaults(t *testing.T) {
	rate := 0.12
	platform := PlatformOpenAI
	repo := &upstreamAccountDefaultRepo{}
	svc := NewUpstreamConfigService(nil, nil, repo)

	created, err := svc.reconcileUpstreamAccounts(context.Background(), &UpstreamConfig{
		ID: 7, Name: "Transit", Provider: UpstreamProviderNewAPI,
	}, []UpstreamKey{{
		ID: 8, UpstreamConfigID: 7, Name: "Key A", Platform: &platform,
		RateMultiplier: &rate, Status: StatusActive,
	}})

	require.NoError(t, err)
	require.Equal(t, 1, created)
	require.Len(t, repo.created, 1)
	account := repo.created[0]
	require.Equal(t, 100, account.Concurrency)
	require.Equal(t, true, account.Credentials["pool_mode"])
	require.False(t, account.Schedulable)
	require.Nil(t, account.LoadFactor)
	require.NotContains(t, account.Credentials, "pool_mode_retry_count")
	require.NotContains(t, account.Credentials, "pool_mode_retry_status_codes")
}
