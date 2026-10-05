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
				"api_protocol":                 APIProtocolChatCompletions,
				"model_mapping":                map[string]any{"gpt": "gpt-upstream"},
				"pool_mode":                    true,
				"pool_mode_retry_count":        float64(4),
				"pool_mode_retry_status_codes": []any{float64(401), float64(429), float64(503)},
			},
			Extra: map[string]any{
				"images_url_to_b64_json":              true,
				"upstream_request_id_header":          "X-Upstream-Request-ID",
				"openai_passthrough":                  true,
				"openai_long_context_billing_enabled": true,
				"quota_limit":                         float64(100),
			},
		})
		require.NoError(t, err)
	})

	for name, input := range map[string]*UpdateAccountInput{
		"credential secret": {Credentials: map[string]any{"api_key": "secret"}},
		"runtime extra":     {Extra: map[string]any{UpstreamBillingProbeExtraKey: map[string]any{}}},
		"unlisted extra":    {Extra: map[string]any{"provider_runtime_marker": true}},
		"pool mode type":    {Credentials: map[string]any{"pool_mode": "true"}},
		"protocol type":     {Credentials: map[string]any{"api_protocol": true}},
		"protocol value":    {Credentials: map[string]any{"api_protocol": "invalid"}},
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
			"images_url_to_b64_json":     true,
			"upstream_request_id_header": "X-Upstream-Request-ID",
			"openai_passthrough":         true,
			UpstreamBillingProbeExtraKey: map[string]any{"status": "ok"},
			"quota_used":                 float64(12),
		},
		map[string]any{"quota_limit": float64(100)},
	)

	require.NotContains(t, merged, "images_url_to_b64_json")
	require.NotContains(t, merged, "upstream_request_id_header")
	require.NotContains(t, merged, "openai_passthrough")
	require.Equal(t, float64(100), merged["quota_limit"])
	require.Contains(t, merged, UpstreamBillingProbeExtraKey)
	require.Equal(t, float64(12), merged["quota_used"])
}

func TestMergeUpstreamAccountEditableExtraCanonicalImagePolicyClearsLegacy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		incoming map[string]any
		bridge   *bool
		policy   string
	}{
		{name: "auto", incoming: map[string]any{}},
		{name: "manual inherit", incoming: map[string]any{"codex_image_generation_explicit_tool_policy": "allow"}, policy: "allow"},
		{name: "manual enabled", incoming: map[string]any{"codex_image_generation_bridge": true, "codex_image_generation_explicit_tool_policy": "allow"}, bridge: boolOverridePtr(true), policy: "allow"},
		{name: "manual disabled", incoming: map[string]any{"codex_image_generation_bridge": false, "codex_image_generation_explicit_tool_policy": "allow"}, bridge: boolOverridePtr(false), policy: "allow"},
		{name: "manual block", incoming: map[string]any{"codex_image_generation_explicit_tool_policy": "strip"}, policy: "strip"},
		{name: "bridge only", incoming: map[string]any{"codex_image_generation_bridge": false}, bridge: boolOverridePtr(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := map[string]any{"status": "disabled", "allow_image_generation": false}
			nested := map[string]any{
				"codex_image_generation_bridge":               true,
				"codex_image_generation_bridge_enabled":       true,
				"codex_image_generation_explicit_tool_policy": "strip",
				"unrelated_setting":                           "preserved",
				"snapshot":                                    snapshot,
			}
			existing := map[string]any{
				"codex_image_generation_bridge":               true,
				"codex_image_generation_bridge_enabled":       true,
				"codex_image_generation_explicit_tool_policy": "strip",
				PlatformOpenAI:                   nested,
				"sub2api_image_pricing_snapshot": snapshot,
				"lcodex_image_pricing_snapshot":  snapshot,
				UpstreamBillingProbeExtraKey:     snapshot,
				"quota_used":                     float64(12),
			}
			require.NoError(t, validateUpstreamAccountEditableUpdate(&UpdateAccountInput{Extra: tc.incoming}))
			merged := mergeUpstreamAccountEditableExtra(existing, tc.incoming)
			require.NotContains(t, merged, "codex_image_generation_bridge_enabled")
			for _, key := range []string{"codex_image_generation_bridge", "codex_image_generation_explicit_tool_policy"} {
				value, present := tc.incoming[key]
				if present {
					require.Equal(t, value, merged[key])
				} else {
					require.NotContains(t, merged, key)
				}
			}
			require.Equal(t, map[string]any{"unrelated_setting": "preserved", "snapshot": snapshot}, merged[PlatformOpenAI])
			for _, key := range []string{"sub2api_image_pricing_snapshot", "lcodex_image_pricing_snapshot", UpstreamBillingProbeExtraKey} {
				require.Equal(t, snapshot, merged[key])
			}
			require.Equal(t, float64(12), merged["quota_used"])
			account := &Account{Platform: PlatformOpenAI, Extra: merged}
			require.Equal(t, tc.bridge, account.CodexImageGenerationBridgeOverride())
			policy, overridden := account.CodexImageGenerationExplicitToolPolicyOverride()
			require.Equal(t, tc.policy != "", overridden)
			if overridden {
				require.Equal(t, tc.policy, policy)
			}
			require.Equal(t, true, existing["codex_image_generation_bridge_enabled"])
			require.Contains(t, nested, "codex_image_generation_bridge")
			require.Contains(t, nested, "codex_image_generation_bridge_enabled")
			require.Contains(t, nested, "codex_image_generation_explicit_tool_policy")
		})
	}
}

func TestMergeUpstreamAccountEditableExtraImagePolicyNestedBoundaries(t *testing.T) {
	for _, nested := range []any{nil, map[string]any(nil), "opaque", []any{"opaque"}, map[string]any{}, map[string]any{"other": true}} {
		merged := mergeUpstreamAccountEditableExtra(map[string]any{PlatformOpenAI: nested}, map[string]any{})
		require.Equal(t, nested, merged[PlatformOpenAI])
	}
	for _, nestedKey := range []string{"codex_image_generation_bridge", "codex_image_generation_bridge_enabled", "codex_image_generation_explicit_tool_policy"} {
		var value any = true
		if nestedKey == featureKeyCodexImageGenerationExplicitToolPolicy {
			value = "strip"
		}
		merged := mergeUpstreamAccountEditableExtra(map[string]any{PlatformOpenAI: map[string]any{nestedKey: value}}, map[string]any{})
		require.Empty(t, merged[PlatformOpenAI])
		account := &Account{Platform: PlatformOpenAI, Extra: merged}
		require.Nil(t, account.CodexImageGenerationBridgeOverride())
		_, overridden := account.CodexImageGenerationExplicitToolPolicyOverride()
		require.False(t, overridden)
	}
	mergedFlat := mergeUpstreamAccountEditableExtra(map[string]any{"codex_image_generation_bridge_enabled": true}, map[string]any{})
	require.Empty(t, mergedFlat)
	merged := mergeUpstreamAccountEditableExtra(nil, map[string]any{})
	require.Empty(t, merged)
}

func TestValidateUpstreamAccountEditableUpdateRejectsImagePolicyLegacyWrites(t *testing.T) {
	for _, extra := range []map[string]any{
		{"codex_image_generation_bridge_enabled": true},
		{PlatformOpenAI: map[string]any{"codex_image_generation_explicit_tool_policy": "strip"}},
		{"codex_image_generation_policy_mode": "auto"},
		{"sub2api_image_pricing_snapshot": map[string]any{"status": "available"}},
		{"lcodex_image_pricing_snapshot": map[string]any{"status": "available"}},
	} {
		require.Error(t, validateUpstreamAccountEditableUpdate(&UpdateAccountInput{Extra: extra}))
	}
}

func TestMergeUpstreamAccountEditableCredentialsPreservesDerivedState(t *testing.T) {
	merged := mergeUpstreamAccountEditableCredentials(
		map[string]any{
			"pool_mode":       true,
			"provider_marker": "derived",
		},
		map[string]any{
			"api_protocol":  APIProtocolChatCompletions,
			"model_mapping": map[string]any{"gpt": "gpt-upstream"},
			"pool_mode":     true,
		},
	)

	require.Equal(t, true, merged["pool_mode"])
	require.Equal(t, APIProtocolChatCompletions, merged["api_protocol"])
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

func TestReconcileUpstreamAccountsDefaultsCNProviderToAdaptiveProtocol(t *testing.T) {
	rate := 0.12
	platform := PlatformZhipu
	baseURL := "https://relay.example.com/v1"
	repo := &upstreamAccountDefaultRepo{}
	svc := NewUpstreamConfigService(nil, nil, repo)

	created, err := svc.reconcileUpstreamAccounts(context.Background(), &UpstreamConfig{
		ID: 7, Name: "Transit", Provider: UpstreamProviderNewAPI,
		SiteURL: "https://upstream.example.com",
	}, []UpstreamKey{{
		ID: 8, UpstreamConfigID: 7, Name: "GLM", Platform: &platform,
		BaseURL: &baseURL, RateMultiplier: &rate, Status: StatusActive,
	}})

	require.NoError(t, err)
	require.Equal(t, 1, created)
	require.Len(t, repo.created, 1)
	account := repo.created[0]
	require.Equal(t, APIProtocolAdaptive, account.Credentials["api_protocol"])
	require.Equal(t, baseURL, account.Credentials["base_url"])
}
