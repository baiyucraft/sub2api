package service

import (
	"testing"
	"time"
)

func TestBuildAutoUpstreamKeyModelRoutesUsesRegisteredDetectionAndFailsClosed(t *testing.T) {
	observedAt := time.Date(2026, 9, 14, 8, 30, 0, 0, time.FixedZone("CST", 8*60*60))
	routes := BuildAutoUpstreamKeyModelRoutes(42, []string{
		"MiniMax-M2", "glm-5.3", "kimi-k2", "deepseek-chat", "gpt-5.5", "private-model", "GLM-5.3",
	}, observedAt)
	if len(routes) != 6 {
		t.Fatalf("unexpected route count: %d (%#v)", len(routes), routes)
	}
	byModel := map[string]UpstreamKeyModelRoute{}
	for _, route := range routes {
		byModel[route.PublicModel] = route
	}
	for model, platform := range map[string]string{
		"MiniMax-M2":    PlatformMiniMax,
		"glm-5.3":       PlatformZhipu,
		"kimi-k2":       PlatformKimi,
		"deepseek-chat": PlatformDeepseek,
		"gpt-5.5":       PlatformOpenAI,
	} {
		route := byModel[model]
		if route.TargetPlatform != platform || !route.Enabled || route.Status != UpstreamKeyModelRouteStatusAvailable {
			t.Fatalf("unexpected route for %s: %#v", model, route)
		}
		if route.LastSeenAt == nil || !route.LastSeenAt.Equal(observedAt.UTC()) {
			t.Fatalf("unexpected last_seen_at for %s: %#v", model, route.LastSeenAt)
		}
	}
	unknown := byModel["private-model"]
	if unknown.Enabled || unknown.TargetPlatform != "" || unknown.Status != UpstreamKeyModelRouteStatusUnknown || unknown.LastError == nil {
		t.Fatalf("unknown model must fail closed: %#v", unknown)
	}
}

func TestBuildAutoUpstreamKeyModelRoutesExcludesComposite(t *testing.T) {
	if IsConcreteRequestPlatform(PlatformComposite) {
		t.Fatal("composite must not be a concrete route target")
	}
}

func TestDetectModelPlatformUsesRegisteredCatalogAndRequiresMetadataForAmbiguousModels(t *testing.T) {
	if platform, ok := DetectModelPlatform("gemini-2.5-flash"); ok || platform != "" {
		t.Fatalf("overlapping Gemini/Antigravity model must be ambiguous: platform=%q ok=%v", platform, ok)
	}
	if platform, ok := DetectModelPlatform("google/gemini-2.5-flash"); !ok || platform != PlatformGemini {
		t.Fatalf("provider metadata must resolve Gemini: platform=%q ok=%v", platform, ok)
	}
	if platform, ok := DetectModelPlatform("antigravity/gemini-2.5-flash"); !ok || platform != PlatformAntigravity {
		t.Fatalf("provider metadata must resolve Antigravity: platform=%q ok=%v", platform, ok)
	}
	if platform, ok := DetectModelPlatform("o2-pro"); !ok || platform != PlatformOpenAI {
		t.Fatalf("OpenAI o2 series must be recognized: platform=%q ok=%v", platform, ok)
	}

	routes := BuildAutoUpstreamKeyModelRoutes(42, []string{"gemini-2.5-flash"}, time.Now())
	if len(routes) != 1 || routes[0].Status != UpstreamKeyModelRouteStatusAmbiguous || routes[0].Enabled || routes[0].LastError == nil {
		t.Fatalf("ambiguous model must be retained but unschedulable: %#v", routes)
	}
}

func TestAccountEffectiveUpstreamTargetKeepsPhysicalAccountIdentity(t *testing.T) {
	keyID := int64(42)
	account := &Account{
		ID: 7, Platform: PlatformOpenAI, UpstreamKeyID: &keyID,
		Credentials: map[string]any{"base_url": "https://mixed.example/v1"},
		UpstreamModelRoutes: []UpstreamKeyModelRoute{{
			ID: 9, UpstreamKeyID: keyID, PublicModel: "kimi-k2", UpstreamModel: "moonshot-v1-128k",
			TargetPlatform: PlatformKimi, APIProtocol: APIProtocolAdaptive,
			Source: UpstreamKeyModelRouteSourceManual, Enabled: true, Status: UpstreamKeyModelRouteStatusAvailable,
		}},
	}
	selected, ok := account.WithEffectiveUpstreamTarget("kimi-k2", PlatformKimi)
	if !ok {
		t.Fatal("expected model route to resolve")
	}
	if account.Platform != PlatformOpenAI || selected.Platform != PlatformOpenAI {
		t.Fatalf("physical platform must remain unchanged: original=%s selected=%s", account.Platform, selected.Platform)
	}
	if selected.EffectivePlatform() != PlatformKimi || selected.GetMappedModel("kimi-k2") != "moonshot-v1-128k" || selected.GetAPIProtocol() != APIProtocolAdaptive {
		t.Fatalf("unexpected effective target: %#v", selected.EffectiveUpstreamTarget)
	}
}

func TestAccountModelRouteDataFailsClosedAndLegacyStillFallsBack(t *testing.T) {
	routed := &Account{Platform: PlatformOpenAI, UpstreamModelRoutes: []UpstreamKeyModelRoute{{
		PublicModel: "private-model", Status: UpstreamKeyModelRouteStatusUnknown,
	}}}
	if _, ok := routed.WithEffectiveUpstreamTarget("private-model", PlatformOpenAI); ok {
		t.Fatal("unknown route must not fall back to the physical platform")
	}
	legacy := &Account{Platform: PlatformOpenAI, Credentials: map[string]any{"model_mapping": map[string]any{"public": "upstream"}}}
	selected, ok := legacy.WithEffectiveUpstreamTarget("public", PlatformOpenAI)
	if !ok || selected.GetMappedModel("public") != "upstream" || selected.EffectiveUpstreamTarget.RouteSource != UpstreamKeyModelRouteSourceLegacy {
		t.Fatalf("legacy account mapping must remain available: %#v", selected)
	}
}

func TestNormalizeUpstreamKeyModelRouteAPIProtocol(t *testing.T) {
	for _, protocol := range []string{"", APIProtocolAdaptive, APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic} {
		normalized, err := NormalizeUpstreamKeyModelRouteAPIProtocol(PlatformKimi, protocol)
		if err != nil || normalized != protocol {
			t.Fatalf("protocol %q should be accepted: normalized=%q err=%v", protocol, normalized, err)
		}
	}
	if _, err := NormalizeUpstreamKeyModelRouteAPIProtocol(PlatformKimi, "custom"); err == nil {
		t.Fatal("unknown protocol must be rejected")
	}
	if _, err := NormalizeUpstreamKeyModelRouteAPIProtocol(PlatformZhipu, APIProtocolResponses); err == nil {
		t.Fatal("Zhipu Responses route must be rejected")
	}
}

func TestEffectiveUpstreamRouteUsesPhysicalEndpointAndNeverProviderDefault(t *testing.T) {
	endpoint := "https://mixed.example/v1"
	for _, tc := range []struct {
		platform string
		protocol string
		resolve  func(*Account) string
	}{
		{PlatformKimi, APIProtocolChatCompletions, func(account *Account) string { return account.GetOpenAIBaseURL() }},
		{PlatformAnthropic, APIProtocolAnthropic, func(account *Account) string { return account.GetAnthropicProtocolBaseURL() }},
		{PlatformGemini, "", func(account *Account) string {
			return account.GetGeminiBaseURL("https://generativelanguage.googleapis.com")
		}},
		{PlatformGrok, APIProtocolResponses, func(account *Account) string { return account.GetGrokBaseURLOr("https://api.x.ai") }},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			account := &Account{
				ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "secret", "base_url": endpoint},
				UpstreamModelRoutes: []UpstreamKeyModelRoute{{
					ID: 1, PublicModel: "mixed-model", UpstreamModel: "remote-model",
					TargetPlatform: tc.platform, APIProtocol: tc.protocol,
					Source: UpstreamKeyModelRouteSourceManual, Enabled: true, Status: UpstreamKeyModelRouteStatusAvailable,
				}},
			}
			selected, ok := account.WithEffectiveUpstreamTarget("mixed-model", tc.platform)
			if !ok {
				t.Fatal("expected route to resolve")
			}
			if got := tc.resolve(selected); got != endpoint {
				t.Fatalf("route must retain physical endpoint: got %q want %q", got, endpoint)
			}
			selected.EffectiveUpstreamTarget.UpstreamEndpoint = ""
			if got := tc.resolve(selected); got != "" {
				t.Fatalf("missing routed endpoint must fail closed, got %q", got)
			}
		})
	}
}

func TestRoutedAPIKeyKeepsPhysicalCredentialAcrossTargetPlatforms(t *testing.T) {
	account := &Account{
		ID: 9, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "sk-physical"},
		UpstreamModelRoutes: []UpstreamKeyModelRoute{{
			PublicModel: "claude-alias", UpstreamModel: "claude-sonnet-4-6",
			TargetPlatform: PlatformAnthropic, Enabled: true, Status: UpstreamKeyModelRouteStatusAvailable,
		}},
	}
	selected, ok := account.WithEffectiveUpstreamTarget("claude-alias", PlatformAnthropic)
	if !ok {
		t.Fatal("expected route to resolve")
	}
	if got := selected.GetOpenAIProtocolAPIKey(); got != "sk-physical" {
		t.Fatalf("routed API key must retain physical credential, got %q", got)
	}
}
