package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func distributionTestClientResolver(t *testing.T, resolver func() string) {
	t.Helper()
	codexCanonicalUAMu.RLock()
	original := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	t.Cleanup(func() { SetCodexCanonicalUserAgentResolver(original) })
	SetCodexCanonicalUserAgentResolver(resolver)
}

func TestDistributionClientAutomaticVersionKeepsIdentityAndWirePin(t *testing.T) {
	version := "0.160.0"
	distributionTestClientResolver(t, func() string { return buildCodexCLIUserAgent(version) })
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	state, err := NewConfidenceDistributionStateForIdentity(initial)
	require.NoError(t, err)
	version = "0.162.0"
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Equal(t, initial.Fingerprint, current.Fingerprint)
	require.NotEqual(t, initial.V2Fingerprint, current.V2Fingerprint)
	require.Equal(t, "0.162.0", current.ClientVersion)
	reasons, upgrade := state.IdentityChange(current)
	require.Empty(t, reasons)
	require.False(t, upgrade)
	upstream := &upstreamHealthProbeHTTPStub{stream: `{"status":"completed","output_text":"47"}`}
	svc.httpUpstream = upstream
	ctx := context.WithValue(context.Background(), distributionProbeContextKey{}, distributionProbeClaim(func(_ context.Context, identity ConfidenceDistributionIdentity) (*DistributionAttempt, error) {
		require.Equal(t, initial.Fingerprint, identity.Fingerprint)
		version = "0.164.0" // A live update during Claim cannot change this request.
		attempt, err := state.Claim(time.Now().UTC())
		require.NoError(t, err)
		attempt.ProbeID = DistributionProbeInteger
		return attempt, nil
	}))
	result, err := svc.runOpenAIDistributionHealthProbe(ctx, account)
	require.NoError(t, err)
	require.True(t, result.distributionSample.Valid)
	require.Equal(t, buildCodexCLIUserAgent("0.160.0"), distributionRequestHeader(upstream.req.Header, "User-Agent"))
	require.Equal(t, "0.160.0", distributionRequestHeader(upstream.req.Header, "version"))
	for _, values := range upstream.req.Header {
		for _, value := range values {
			require.NotContains(t, value, distributionClientVersionMarker, "normalization marker must never be sent")
		}
	}
	for _, public := range []any{current, result.distributionAttempt, result.ConfidenceEvidence} {
		raw, err := json.Marshal(public)
		require.NoError(t, err)
		require.NotContains(t, string(raw), "client_version")
		require.NotContains(t, string(raw), "0.160.0")
	}
}

func TestDistributionClientV2FingerprintMatchesPreviousWireContract(t *testing.T) {
	distributionTestClientResolver(t, func() string { return buildCodexCLIUserAgent("0.160.0") })
	for _, protocol := range []string{"responses", "chat_completions"} {
		t.Run(protocol, func(t *testing.T) {
			svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
			if protocol == "chat_completions" {
				account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
			}
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{"x-synthetic": "old-wire-contract"}
			spec, err := svc.resolveDistributionRequest(account)
			require.NoError(t, err)
			// Independently encode the v2 contract rather than using the new
			// identity components, whose generated header values are normalized.
			digest := func(value any) string {
				raw, err := json.Marshal(value)
				require.NoError(t, err)
				hash := sha256.Sum256(raw)
				return hex.EncodeToString(hash[:])
			}
			endpoint := "https://probe.example/v1/responses"
			headers := map[string][]string{
				"accept": {"text/event-stream"}, "content-type": {"application/json"}, "x-synthetic": {"old-wire-contract"},
			}
			if protocol == "responses" {
				headers["user-agent"] = []string{buildCodexCLIUserAgent("0.160.0")}
				headers["version"] = []string{"0.160.0"}
				headers["originator"] = []string{openai.CodexDefaultOriginator}
				headers["openai-beta"] = []string{"responses=experimental"}
			} else {
				endpoint = "https://probe.example/v1/chat/completions"
			}
			components := map[string]string{
				"binding": digest([]any{int64(401), int64Ptr(501)}), "protocol": digest(protocol),
				"endpoint": digest(endpoint), "credential": digest("synthetic"),
				"model": digest(UpstreamConfidenceDistributionClaimedModel),
				"proxy": digest([]any{(*int64)(nil), (*int64)(nil), ""}), "headers": digest(headers),
				"contract": digest(UpstreamConfidenceDistributionPromptVersion), "baseline": digest(DistributionBaselineVersion(protocol)),
			}
			require.Equal(t, digest([]any{2, components}), spec.identity.V2Fingerprint)
		})
	}
}

func TestDistributionClientOverridesAndManualPolicy(t *testing.T) {
	for _, overrides := range []map[string]any{
		{}, {"User-Agent": "fixed-agent"}, {"version": "1.0.0"},
		{"User-Agent": "fixed-agent", "version": "1.0.0"},
	} {
		raw, _ := json.Marshal(overrides)
		t.Run(string(raw), func(t *testing.T) {
			// Simulate a stale resolver cache while the strict policy is changed.
			distributionTestClientResolver(t, func() string { return buildCodexCLIUserAgent("0.159.0") })
			repo := newCodexVersionSyncSettingRepoStub(map[string]string{
				SettingKeyOpenAICodexClientVersion: "0.160.0", SettingKeyOpenAICodexClientVersionSynced: "0.162.0",
			})
			svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
			svc.SetSettingService(NewSettingService(repo, nil))
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = overrides
			initial, err := svc.resolveDistributionRequest(account)
			require.NoError(t, err)
			require.NoError(t, initial.pinClientVersion("0.160.0"))
			for _, name := range []string{"User-Agent", "version"} {
				if value, ok := overrides[name]; ok {
					require.Equal(t, value, distributionRequestHeader(initial.headers, name))
				} else if name == "version" {
					require.Equal(t, "0.160.0", distributionRequestHeader(initial.headers, name))
				} else {
					require.Equal(t, buildCodexCLIUserAgent("0.160.0"), distributionRequestHeader(initial.headers, name))
				}
			}
			require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersion, "0.161.0"))
			changed, err := svc.resolveDistributionRequest(account)
			require.NoError(t, err)
			bothExplicit := len(overrides) == 2
			if bothExplicit {
				require.Equal(t, initial.identity.Fingerprint, changed.identity.Fingerprint)
			} else {
				require.NotEqual(t, initial.identity.Components["headers"], changed.identity.Components["headers"])
				require.Equal(t, "0.161.0", changed.identity.ClientVersion)
			}
			require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersion, ""))
			automatic, err := svc.resolveDistributionRequest(account)
			require.NoError(t, err)
			if !bothExplicit {
				require.Equal(t, "0.162.0", automatic.identity.ClientVersion, "removed fixed policy must not use the old cached version")
			}
			require.NoError(t, repo.Set(context.Background(), SettingKeyOpenAICodexClientVersion, "invalid-policy"))
			invalid, err := svc.resolveDistributionRequest(account)
			require.NoError(t, err)
			require.Equal(t, automatic.identity.Fingerprint, invalid.identity.Fingerprint)
		})
	}
}

func TestDistributionClientHeaderOriginCannotCollide(t *testing.T) {
	distributionTestClientResolver(t, func() string { return buildCodexCLIUserAgent("0.162.0") })
	for _, header := range []string{"User-Agent", "version"} {
		t.Run(header, func(t *testing.T) {
			svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
			initial, err := svc.ResolveConfidenceDistributionIdentity(account)
			require.NoError(t, err)
			value := distributionClientVersionMarker
			if header == "User-Agent" {
				value = openai.SetCodexUserAgentVersion(buildCodexCLIUserAgent("0.162.0"), distributionClientVersionMarker)
			}
			account.Credentials["header_override_enabled"] = true
			account.Credentials["header_overrides"] = map[string]any{header: value}
			explicit, err := svc.ResolveConfidenceDistributionIdentity(account)
			require.NoError(t, err)
			require.NotEqual(t, initial.Fingerprint, explicit.Fingerprint)
		})
	}
}

func TestDistributionClientTemplateChangesAndChatIsolation(t *testing.T) {
	ua := buildCodexCLIUserAgent("0.160.0")
	distributionTestClientResolver(t, func() string { return ua })
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	initial, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	ua = "codex_cli_rs/0.162.0 (Windows 11; x86_64) terminal"
	changed, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.NotEqual(t, initial.Components["headers"], changed.Components["headers"])
	account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
	chat, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	ua = buildCodexCLIUserAgent("0.164.0")
	current, err := svc.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	require.Empty(t, current.ClientVersion)
	require.Equal(t, chat.Fingerprint, current.Fingerprint)
}

func TestDistributionClientPolicyFailureDoesNotClaim(t *testing.T) {
	svc, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	repo := newCodexVersionSyncSettingRepoStub(nil)
	repo.getErr = errors.New("synthetic settings unavailable")
	svc.SetSettingService(NewSettingService(repo, nil))
	upstream := &upstreamHealthProbeHTTPStub{}
	svc.httpUpstream = upstream
	claims := 0
	result, err := svc.runOpenAIDistributionHealthProbe(distributionProbeTestContext(&claims, DistributionProbeInteger), account)
	require.Error(t, err)
	require.Equal(t, "probe_client_identity_unavailable", result.Reason)
	require.Zero(t, claims)
	require.Empty(t, upstream.requests)
}
