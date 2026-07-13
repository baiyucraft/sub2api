package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNewAPIModelLimitsPlatformEvidenceIsStrict(t *testing.T) {
	detectedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name       string
		enabled    bool
		raw        string
		used       bool
		status     string
		candidates []string
	}{
		{name: "disabled", raw: `"claude-3-7-sonnet"`},
		{name: "unique", enabled: true, raw: `"claude-3-7-sonnet"`, used: true, status: newAPIPlatformEvidenceUnique, candidates: []string{PlatformAnthropic}},
		{name: "multiple", enabled: true, raw: `"claude-3-7-sonnet,gpt-4o"`, used: true, status: newAPIPlatformEvidenceMultiple, candidates: []string{PlatformAnthropic, PlatformOpenAI}},
		{name: "unknown model does not dilute unique evidence", enabled: true, raw: `"claude-3-7-sonnet,custom-model"`, used: true, status: newAPIPlatformEvidenceUnique, candidates: []string{PlatformAnthropic}},
		{name: "unknown only falls through to pricing", enabled: true, raw: `"custom-model"`, used: false},
		{name: "missing value is partial", enabled: true, used: true, status: newAPIPlatformEvidencePartial},
		{name: "object is rejected", enabled: true, raw: `{"model":"claude-3-7-sonnet"}`, used: true, status: newAPIPlatformEvidencePartial},
		{name: "non string array is rejected", enabled: true, raw: `["claude-3-7-sonnet",1]`, used: true, status: newAPIPlatformEvidencePartial},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence, used := newAPIModelLimitsPlatformEvidence(newAPIKeyRow{
				ModelLimitsEnabled: tt.enabled,
				ModelLimits:        json.RawMessage(tt.raw),
			}, detectedAt)

			require.Equal(t, tt.used, used)
			if !used {
				return
			}
			require.Equal(t, tt.status, evidence.Status)
			if len(tt.candidates) == 0 {
				require.Empty(t, evidence.Candidates)
			} else {
				require.Equal(t, tt.candidates, evidence.Candidates)
			}
			require.Equal(t, newAPIPlatformSourceModelLimits, evidence.Source)
			require.Equal(t, detectedAt, evidence.DetectedAt)
		})
	}
}

func TestNewAPIPricingPlatformEvidenceHonorsOwnerAndModelBoundaries(t *testing.T) {
	detectedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name       string
		record     map[string]any
		status     string
		candidates []string
		source     string
	}{
		{
			name:       "recognized owner wins over model name",
			record:     map[string]any{"owner_by": "OpenAI", "model_name": "claude-3-7-sonnet"},
			status:     newAPIPlatformEvidenceUnique,
			candidates: []string{PlatformOpenAI},
			source:     newAPIPlatformSourcePricingOwner,
		},
		{
			name:   "unknown nonempty owner blocks model fallback",
			record: map[string]any{"owner_by": "private vendor", "model_name": "claude-3-7-sonnet"},
			status: newAPIPlatformEvidenceUnknown,
			source: newAPIPlatformSourcePricingOwner,
		},
		{
			name:       "conflicting explicit fields are ambiguous",
			record:     map[string]any{"owner_by": "OpenAI", "platform": "Anthropic", "model_name": "gpt-4o"},
			status:     newAPIPlatformEvidenceMultiple,
			candidates: []string{PlatformAnthropic, PlatformOpenAI},
			source:     newAPIPlatformSourcePricingOwner,
		},
		{
			name:       "model fallback requires every owner field empty",
			record:     map[string]any{"owner_by": "", "ownerBy": "", "platform": "", "vendor": "", "model_name": "vendor/claude-3-7-sonnet"},
			status:     newAPIPlatformEvidenceUnique,
			candidates: []string{PlatformAnthropic},
			source:     newAPIPlatformSourcePricingModelName,
		},
		{
			name:   "model fallback uses token boundaries",
			record: map[string]any{"model_name": "myclaudex-model"},
			status: newAPIPlatformEvidenceUnknown,
			source: newAPIPlatformSourcePricingModelName,
		},
		{
			name:   "supported endpoint types are ignored",
			record: map[string]any{"supported_endpoint_types": []string{"anthropic", "openai"}},
			status: newAPIPlatformEvidenceUnknown,
			source: newAPIPlatformSourcePricingModelName,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := newAPIPricingRecordPlatformEvidence(tt.record, detectedAt)
			require.Equal(t, tt.status, evidence.Status)
			if len(tt.candidates) == 0 {
				require.Empty(t, evidence.Candidates)
			} else {
				require.Equal(t, tt.candidates, evidence.Candidates)
			}
			require.Equal(t, tt.source, evidence.Source)
		})
	}
}

func TestNewAPIKnownUpstreamPricingFixtures(t *testing.T) {
	detectedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	tests := []struct {
		name     string
		model    string
		platform string
	}{
		{name: "sunai ccmax", model: "claude-sonnet-4-6", platform: PlatformAnthropic},
		{name: "sunai kiro", model: "claude-3-7-sonnet", platform: PlatformAnthropic},
		{name: "sunai plus", model: "codex-mini-latest", platform: PlatformOpenAI},
		{name: "sunai pro", model: "gpt-5.4", platform: PlatformOpenAI},
		{name: "sunai gemini", model: "gemini-2.5-pro", platform: PlatformGemini},
		{name: "sunai grok", model: "grok-4", platform: PlatformGrok},
		{name: "knife pro", model: "gpt5.4", platform: PlatformOpenAI},
		{name: "knife plus", model: "codex5", platform: PlatformOpenAI},
		{name: "knife kiro", model: "claude3.7-sonnet", platform: PlatformAnthropic},
		{name: "knife ccmax", model: "claude-opus-4-6", platform: PlatformAnthropic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evidence := newAPIPricingRecordPlatformEvidence(map[string]any{
				"owner_by": "", "ownerBy": "", "platform": "", "vendor": "", "model_name": tt.model,
			}, detectedAt)
			require.Equal(t, newAPIPlatformEvidenceUnique, evidence.Status)
			require.Equal(t, []string{tt.platform}, evidence.Candidates)
		})
	}
}

func TestNewAPIPricingFailureProducesPartialEvidence(t *testing.T) {
	detectedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	groups := map[string]newAPIGroupInfo{"default": {Ratio: 1}}
	(newAPIUpstreamProviderAdapter{}).enrichGroupsFromPricing(context.Background(), &newAPISession{
		rootURL: server.URL,
		userID:  4798,
		client:  server.Client(),
	}, groups, detectedAt)

	evidence := groups["default"].PlatformEvidence
	require.Equal(t, newAPIPlatformEvidencePartial, evidence.Status)
	require.Equal(t, newAPIPlatformSourcePricing, evidence.Source)
	require.Empty(t, evidence.Candidates)
}

func TestNewAPIPlatformEvidenceAccumulatorDoesNotHideUnknownRecords(t *testing.T) {
	detectedAt := time.Date(2026, 7, 14, 1, 2, 3, 0, time.UTC)
	accumulator := newAPIPlatformAccumulator()
	accumulator.Add(newAPIPlatformEvidenceFromCandidates([]string{PlatformOpenAI}, false, newAPIPlatformSourcePricingOwner, detectedAt))
	accumulator.Add(newAPIUnknownPlatformEvidence(newAPIPlatformSourcePricingOwner, detectedAt))

	evidence := accumulator.Evidence(detectedAt)
	require.Equal(t, newAPIPlatformEvidencePartial, evidence.Status)
	require.Equal(t, []string{PlatformOpenAI}, evidence.Candidates)
	require.Nil(t, evidence.DetectedPlatform())
}

func TestNewAPIPlatformEvidenceFlowsToUpstreamKeysWithoutOpenAIDefault(t *testing.T) {
	groups := map[string]any{
		"anthropic":      map[string]any{"ratio": 1},
		"openai":         map[string]any{"ratio": 1},
		"multi":          map[string]any{"ratio": 1},
		"model-limits":   map[string]any{"ratio": 1},
		"model-partial":  map[string]any{"ratio": 1},
		"model-fallback": map[string]any{"ratio": 1},
		"owner-unknown":  map[string]any{"ratio": 1},
		"endpoint-only":  map[string]any{"ratio": 1},
		"no-pricing":     map[string]any{"ratio": 1},
	}
	pricing := []map[string]any{
		{"owner_by": "Anthropic", "model_name": "gpt-4o", "enable_groups": []string{"anthropic", "multi", "model-limits"}},
		{"owner_by": "OpenAI", "model_name": "claude-3-7-sonnet", "enable_groups": []string{"openai", "multi", "model-limits", "model-partial"}},
		{"model_name": "vendor/claude-3-7-sonnet", "enable_group": "model-fallback"},
		{"owner_by": "private vendor", "model_name": "claude-3-7-sonnet", "enable_group": "owner-unknown"},
		{"supported_endpoint_types": []string{"anthropic"}, "enable_group": "endpoint-only"},
	}
	items := []map[string]any{
		{"id": 1, "key": "sk-anthropic-visible", "status": 1, "name": "anthropic", "group": "anthropic"},
		{"id": 2, "key": "sk-openai-visible", "status": 1, "name": "openai", "group": "openai"},
		{"id": 3, "key": "sk-multi-visible", "status": 1, "name": "multi", "group": "multi"},
		{"id": 4, "key": "sk-limits-visible", "status": 1, "name": "limits", "group": "model-limits", "model_limits_enabled": true, "model_limits": "claude-3-7-sonnet"},
		{"id": 5, "key": "sk-partial-visible", "status": 1, "name": "model-partial", "group": "model-partial", "model_limits_enabled": true, "model_limits": "claude-3-7-sonnet,private-model"},
		{"id": 6, "key": "sk-fallback-visible", "status": 1, "name": "fallback", "group": "model-fallback"},
		{"id": 7, "key": "sk-owner-unknown-visible", "status": 1, "name": "owner-unknown", "group": "owner-unknown"},
		{"id": 8, "key": "sk-endpoint-visible", "status": 1, "name": "endpoint-only", "group": "endpoint-only"},
		{"id": 9, "key": "sk-no-pricing-visible", "status": 1, "name": "no-pricing", "group": "no-pricing"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/self/groups":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": groups})
		case "/api/pricing":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": pricing})
		case "/api/token/":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"page": 0, "page_size": 100, "total": len(items), "items": items}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snapshot, err := (newAPIUpstreamProviderAdapter{}).SyncSnapshot(context.Background(), &UpstreamConfig{
		ID:       99,
		Name:     "NewAPI",
		Provider: UpstreamProviderNewAPI,
		SiteURL:  server.URL,
		AuthMode: UpstreamAuthModeCookie,
		Credentials: map[string]any{
			AccountCredentialNewAPICookie: "session=secret",
			AccountCredentialNewAPIUserID: "4798",
		},
	}, "", false)

	require.NoError(t, err)
	require.Len(t, snapshot.Keys, len(items))
	keys := make(map[string]UpstreamKey, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		keys[key.Name] = key
	}

	for _, key := range keys {
		require.Nil(t, key.Platform, "the adapter must emit evidence instead of assigning the final platform")
	}
	assertNewAPIDetectedPlatform(t, keys["anthropic"], PlatformAnthropic, UpstreamKeyPlatformDetectionDetected)
	assertNewAPIDetectedPlatform(t, keys["openai"], PlatformOpenAI, UpstreamKeyPlatformDetectionDetected)
	assertNewAPIDetectedPlatform(t, keys["multi"], "", UpstreamKeyPlatformDetectionAmbiguous)
	assertNewAPIDetectedPlatform(t, keys["limits"], PlatformAnthropic, UpstreamKeyPlatformDetectionDetected)
	assertNewAPIDetectedPlatform(t, keys["model-partial"], PlatformAnthropic, UpstreamKeyPlatformDetectionDetected)
	assertNewAPIDetectedPlatform(t, keys["fallback"], PlatformAnthropic, UpstreamKeyPlatformDetectionDetected)
	assertNewAPIDetectedPlatform(t, keys["owner-unknown"], "", UpstreamKeyPlatformDetectionUnresolved)
	assertNewAPIDetectedPlatform(t, keys["endpoint-only"], "", UpstreamKeyPlatformDetectionUnresolved)
	assertNewAPIDetectedPlatform(t, keys["no-pricing"], "", UpstreamKeyPlatformDetectionUnresolved)

	assertNewAPIPlatformEvidence(t, keys["multi"], newAPIPlatformEvidenceMultiple, newAPIPlatformSourcePricingOwner, []string{PlatformAnthropic, PlatformOpenAI})
	assertNewAPIPlatformEvidence(t, keys["limits"], newAPIPlatformEvidenceUnique, newAPIPlatformSourceModelLimits, []string{PlatformAnthropic})
	assertNewAPIPlatformEvidence(t, keys["model-partial"], newAPIPlatformEvidenceUnique, newAPIPlatformSourceModelLimits, []string{PlatformAnthropic})
	assertNewAPIPlatformEvidence(t, keys["owner-unknown"], newAPIPlatformEvidenceUnknown, newAPIPlatformSourcePricingOwner, nil)
	assertNewAPIPlatformEvidence(t, keys["endpoint-only"], newAPIPlatformEvidenceUnknown, newAPIPlatformSourcePricingModelName, nil)
	assertNewAPIPlatformEvidence(t, keys["no-pricing"], newAPIPlatformEvidenceUnknown, newAPIPlatformSourcePricing, nil)
	require.False(t, snapshot.Partial)
}

func assertNewAPIDetectedPlatform(t *testing.T, key UpstreamKey, platform, status string) {
	t.Helper()
	require.Equal(t, status, key.PlatformDetectionStatus)
	if platform == "" {
		require.Nil(t, key.DetectedPlatform)
	} else {
		require.NotNil(t, key.DetectedPlatform)
		require.Equal(t, platform, *key.DetectedPlatform)
	}
	require.NotNil(t, key.PlatformDetectedAt)
}

func assertNewAPIPlatformEvidence(t *testing.T, key UpstreamKey, status, source string, candidates []string) {
	t.Helper()
	evidence, ok := key.Extra["newapi_platform_evidence"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, status, evidence["status"])
	require.Equal(t, source, evidence["source"])
	if len(candidates) == 0 {
		require.Empty(t, evidence["candidates"])
	} else {
		require.Equal(t, candidates, evidence["candidates"])
	}
	_, err := time.Parse(time.RFC3339Nano, evidence["detected_at"].(string))
	require.NoError(t, err)
}
