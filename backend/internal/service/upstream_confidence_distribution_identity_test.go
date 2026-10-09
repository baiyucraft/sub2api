package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func distributionIdentityTestService() *AccountTestService {
	return &AccountTestService{cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
}

func distributionIdentityTestAccount() *Account {
	return &Account{ID: 401, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		UpstreamKeyID: int64Ptr(501), UpstreamConfigID: int64Ptr(601),
		Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://probe.example"}}
}

func TestDistributionIdentityIgnoresUnrelatedConfiguration(t *testing.T) {
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	account.Credentials["model_mapping"] = map[string]any{UpstreamConfidenceDistributionClaimedModel: UpstreamConfidenceDistributionClaimedModel, "other": "changed"}
	account.Credentials["pool_mode"] = "random"
	account.Credentials["pool_mode_retry_count"] = 12
	account.Extra = map[string]any{"upstream_model_sync": map[string]any{"checked_at": "now"}, "unrelated": "metadata"}
	account.Concurrency = 99
	rate := 10.0
	account.RateMultiplier = &rate
	account.ProbeMinInputTokens = 1000
	account.UpstreamConfigID = int64Ptr(602)
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Equal(t, initial.Fingerprint, current.Fingerprint)
	require.Equal(t, initial.Components, current.Components)
	require.NotEqual(t, initial.LegacyFingerprint, current.LegacyFingerprint)
	raw, err := json.Marshal(current)
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(raw), "internal identity must never enter an API response")
}

func TestDistributionIdentityEquivalentEndpointAndHeaders(t *testing.T) {
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	account.Credentials["header_override_enabled"] = true
	account.Credentials["header_overrides"] = map[string]any{"X-Synthetic": "stable"}
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	account.Credentials["base_url"] = " HTTPS://PROBE.EXAMPLE:443/v1/responses/ "
	account.Credentials["api_key"] = " synthetic "
	account.Credentials["header_overrides"] = map[string]any{"x-synthetic": " stable "}
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Equal(t, initial.Fingerprint, current.Fingerprint)
	account.Credentials["header_override_enabled"] = false
	noOverrides, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	account.Credentials["header_overrides"] = map[string]any{"x-synthetic": "unused-different"}
	disabled, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Equal(t, noOverrides.Fingerprint, disabled.Fingerprint)
}

func TestDistributionIdentityParentMetadataPreservesLegacyUpgrade(t *testing.T) {
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	account.UpstreamConfigID = int64Ptr(602)
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Equal(t, initial.Fingerprint, current.Fingerprint)
	require.Equal(t, initial.LegacyFingerprint, current.LegacyFingerprint)
	require.Equal(t, initial.Components["binding"], current.Components["binding"], "binding is the Key and account, without new unprovable parent metadata")
}

func TestDistributionIdentityChangesOnlyEffectiveRequestDimensions(t *testing.T) {
	for _, tc := range []struct {
		name, component string
		mutate          func(*Account)
	}{
		{"account binding", "binding", func(a *Account) { a.ID++ }},
		{"key binding", "binding", func(a *Account) { a.UpstreamKeyID = int64Ptr(502) }},
		{"endpoint", "endpoint", func(a *Account) { a.Credentials["base_url"] = "https://other.example" }},
		{"credential", "credential", func(a *Account) { a.Credentials["api_key"] = "rotated-synthetic" }},
		{"model", "model", func(a *Account) {
			a.Credentials["model_mapping"] = map[string]any{UpstreamConfidenceDistributionClaimedModel: "gpt-6.1-sol-alias"}
		}},
		{"protocol", "protocol", func(a *Account) { a.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"} }},
		{"proxy binding", "proxy", func(a *Account) {
			a.ProxyID = int64Ptr(12)
			a.Proxy = &Proxy{ID: 12, Protocol: "http", Host: "proxy.example", Port: 1080}
		}},
		{"headers", "headers", func(a *Account) {
			a.Credentials["header_override_enabled"] = true
			a.Credentials["header_overrides"] = map[string]any{"X-Synthetic": "changed"}
		}},
		{"explicit window id", "headers", func(a *Account) {
			a.Credentials["header_override_enabled"] = true
			a.Credentials["header_overrides"] = map[string]any{"X-Codex-Window-ID": "operator-fixed"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
			initial, err := svc.ResolveConfidenceDistributionIdentity(account)
			require.NoError(t, err)
			tc.mutate(account)
			current, err := svc.ResolveConfidenceDistributionIdentity(account)
			require.NoError(t, err)
			require.NotEqual(t, initial.Fingerprint, current.Fingerprint)
			require.NotEqual(t, initial.Components[tc.component], current.Components[tc.component])
		})
	}
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	account.ProxyID = int64Ptr(12)
	account.Proxy = &Proxy{ID: 12, Protocol: "http", Host: "proxy.example", Port: 1080, Username: "synthetic", Password: "first"}
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	account.Proxy.Password = "rotated"
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.NotEqual(t, initial.Components["proxy"], current.Components["proxy"])
	require.Equal(t, initial.LegacyFingerprint, current.LegacyFingerprint, "legacy identity did not capture proxy contents")
	require.False(t, current.LegacyCompatible)
}

func TestDistributionIdentityLegacyCompatibility(t *testing.T) {
	// Restore the global resolver so this test does not affect other probes.
	codexCanonicalUAMu.RLock()
	original := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(original) })
	SetCodexCanonicalUserAgentResolver(nil)
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	identity, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.True(t, identity.LegacyCompatible)
	account.Credentials["header_override_enabled"] = true
	account.Credentials["header_overrides"] = map[string]any{"X-Synthetic": "stable"}
	identity, err = svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.True(t, identity.LegacyCompatible, "effective overrides were captured by the old credentials hash")
	SetCodexCanonicalUserAgentResolver(func() string { return codexCLIUserAgent })
	identity, err = svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.False(t, identity.LegacyCompatible, "mutable runtime defaults have no historical proof")
	account.Credentials["header_overrides"] = map[string]any{"User-Agent": "fixed-agent", "originator": "fixed-origin", "version": "1.0.0"}
	identity, err = svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.True(t, identity.LegacyCompatible)
	account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
	account.Credentials["header_overrides"] = nil
	identity, err = svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.True(t, identity.LegacyCompatible, "chat has no Codex runtime headers")
}

type distributionSnapshotHTTPStub struct {
	upstreamHealthProbeHTTPStub
	proxyURL string
}

// Account overrides preserve wire casing rather than Go's canonical map keys.
// Check the actual transmitted header without relying on Header.Get casing.
func distributionRequestHeader(headers http.Header, name string) string {
	for key, values := range headers {
		if strings.EqualFold(key, name) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func (s *distributionSnapshotHTTPStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	s.proxyURL = proxyURL
	return s.Do(req, proxyURL, accountID, concurrency)
}

func TestDistributionProbeUsesClaimedSnapshot(t *testing.T) {
	account := distributionIdentityTestAccount()
	account.ProxyID = int64Ptr(12)
	account.Proxy = &Proxy{ID: 12, Protocol: "http", Host: "first.example", Port: 1080}
	account.Credentials["header_override_enabled"] = true
	account.Credentials["header_overrides"] = map[string]any{"X-Synthetic": "before"}
	upstream := &distributionSnapshotHTTPStub{upstreamHealthProbeHTTPStub: upstreamHealthProbeHTTPStub{stream: `{"status":"completed","output_text":"47"}`}}
	svc := distributionIdentityTestService()
	svc.httpUpstream = upstream
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), distributionProbeContextKey{}, distributionProbeClaim(func(_ context.Context, identity ConfidenceDistributionIdentity) (*DistributionAttempt, error) {
		require.Equal(t, initial.Fingerprint, identity.Fingerprint)
		account.Credentials["api_key"] = "after"
		account.Credentials["base_url"] = "https://after.example"
		account.Credentials["header_overrides"].(map[string]any)["X-Synthetic"] = "after"
		account.Proxy.Host = "after.example"
		return &DistributionAttempt{SeriesID: "snapshot", LeaseToken: "lease", Sequence: 1, ProbeID: DistributionProbeInteger, ClientVersion: identity.ClientVersion}, nil
	}))
	result, err := svc.runOpenAIDistributionHealthProbe(ctx, account)
	require.NoError(t, err)
	require.True(t, result.distributionSample.Valid)
	require.Equal(t, "https://probe.example/v1/responses", upstream.req.URL.String())
	require.Equal(t, "Bearer synthetic", upstream.req.Header.Get("Authorization"))
	require.Equal(t, "before", distributionRequestHeader(upstream.req.Header, "X-Synthetic"))
	require.Equal(t, "http://first.example:1080", upstream.proxyURL)
}

func TestDistributionProbeRandomWindowIDDoesNotChangeIdentity(t *testing.T) {
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	upstream := &upstreamHealthProbeHTTPStub{stream: `{"status":"completed","output_text":"47"}`}
	svc.httpUpstream = upstream
	var fingerprints []string
	ctx := context.WithValue(context.Background(), distributionProbeContextKey{}, distributionProbeClaim(func(_ context.Context, identity ConfidenceDistributionIdentity) (*DistributionAttempt, error) {
		fingerprints = append(fingerprints, identity.Fingerprint)
		return &DistributionAttempt{Sequence: 1, ProbeID: DistributionProbeInteger, ClientVersion: identity.ClientVersion}, nil
	}))
	for i := 0; i < 2; i++ {
		_, err := svc.runOpenAIDistributionHealthProbe(ctx, account)
		require.NoError(t, err)
	}
	require.Equal(t, fingerprints[0], fingerprints[1])
	require.NotEmpty(t, upstream.requests[0].Header.Get("X-Codex-Window-ID"))
	require.NotEqual(t, upstream.requests[0].Header.Get("X-Codex-Window-ID"), upstream.requests[1].Header.Get("X-Codex-Window-ID"))
	account.Credentials["header_override_enabled"] = true
	account.Credentials["header_overrides"] = map[string]any{"X-Codex-Window-ID": "operator-fixed"}
	_, err := svc.runOpenAIDistributionHealthProbe(ctx, account)
	require.NoError(t, err)
	require.Equal(t, "operator-fixed", distributionRequestHeader(upstream.req.Header, "X-Codex-Window-ID"))
	require.NotEqual(t, fingerprints[1], fingerprints[2])
}
