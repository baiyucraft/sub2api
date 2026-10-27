package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fingerprintBusyProbeRepo struct {
	*distributionWindowTestRepo
	fingerprintAttempt *ConfidenceFingerprintAttempt
	fingerprintFinish  int
}

type fingerprintOnlyProbeRepo struct {
	*healthProbeLockRepo
	fingerprintAttempt *ConfidenceFingerprintAttempt
}

func (r *fingerprintOnlyProbeRepo) ClaimConfidenceFingerprint(context.Context, int64, ConfidenceFingerprintIdentity, time.Time) (*ConfidenceFingerprintAttempt, error) {
	return r.fingerprintAttempt, nil
}

func (*fingerprintOnlyProbeRepo) FinishConfidenceFingerprint(context.Context, int64, *ConfidenceFingerprintAttempt, ConfidenceFingerprintIdentity, ConfidenceFingerprintEvidence, time.Time) error {
	return nil
}

func (*fingerprintOnlyProbeRepo) RecordConfidenceFingerprintSample(context.Context, int64, ConfidenceFingerprintIdentity, ConfidenceFingerprintEvidence, time.Time) error {
	return nil
}

func (*fingerprintOnlyProbeRepo) LoadConfidenceFingerprints(_ context.Context, identities map[int64]ConfidenceFingerprintIdentity) (map[int64]ConfidenceFingerprintReadResult, error) {
	results := make(map[int64]ConfidenceFingerprintReadResult, len(identities))
	for keyID := range identities {
		results[keyID] = ConfidenceFingerprintReadResult{}
	}
	return results, nil
}

func (r *fingerprintBusyProbeRepo) ClaimConfidenceFingerprint(context.Context, int64, ConfidenceFingerprintIdentity, time.Time) (*ConfidenceFingerprintAttempt, error) {
	return r.fingerprintAttempt, nil
}

func (r *fingerprintBusyProbeRepo) FinishConfidenceFingerprint(_ context.Context, _ int64, _ *ConfidenceFingerprintAttempt, _ ConfidenceFingerprintIdentity, _ ConfidenceFingerprintEvidence, _ time.Time) error {
	r.fingerprintFinish++
	return nil
}

func (*fingerprintBusyProbeRepo) RecordConfidenceFingerprintSample(context.Context, int64, ConfidenceFingerprintIdentity, ConfidenceFingerprintEvidence, time.Time) error {
	return nil
}

func (*fingerprintBusyProbeRepo) LoadConfidenceFingerprints(_ context.Context, identities map[int64]ConfidenceFingerprintIdentity) (map[int64]ConfidenceFingerprintReadResult, error) {
	results := make(map[int64]ConfidenceFingerprintReadResult, len(identities))
	for keyID := range identities {
		results[keyID] = ConfidenceFingerprintReadResult{}
	}
	return results, nil
}

func (*fingerprintBusyProbeRepo) ClaimConfidenceDistribution(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*DistributionAttempt, error) {
	return nil, nil
}

type fingerprintBusyTestProber struct {
	*AccountTestService
	calls int
}

func (p *fingerprintBusyTestProber) RunUpstreamHealthProbe(ctx context.Context, account *Account, model string) (UpstreamHealthProbeResult, error) {
	p.calls++
	if claim, ok := ctx.Value(distributionProbeContextKey{}).(distributionProbeClaim); ok {
		identity, err := p.ResolveConfidenceDistributionIdentity(account)
		if err != nil {
			return UpstreamHealthProbeResult{}, err
		}
		attempt, err := claim(ctx, identity)
		if err != nil {
			return UpstreamHealthProbeResult{}, err
		}
		if attempt == nil {
			return UpstreamHealthProbeResult{Model: model, Protocol: upstreamHealthProbeProtocolOpenAI}, errDistributionProbeBusy
		}
	}
	return UpstreamHealthProbeResult{Model: account.GetMappedModel(model), Protocol: upstreamHealthProbeProtocolOpenAI, Result: "success", Reason: "probe_succeeded"}, nil
}

func TestConfidenceFingerprintBusyLeaseDoesNotSkipHealthProbe(t *testing.T) {
	for _, test := range []struct {
		name    string
		attempt *ConfidenceFingerprintAttempt
		wantRun int
	}{
		{name: "fingerprint claim already leased", wantRun: 2},
		{name: "distribution claim already leased", attempt: &ConfidenceFingerprintAttempt{SeriesID: "series", Token: "token", Method: "distribution", ObservedAt: time.Now().UTC(), Sequence: 2}, wantRun: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			const keyID int64 = 99421
			GlobalUpstreamHealthRegistry().Forget(keyID)
			t.Cleanup(func() { GlobalUpstreamHealthRegistry().Forget(keyID) })
			account := distributionIdentityTestAccount()
			account.UpstreamKeyID = int64Ptr(keyID)
			platform := PlatformOpenAI
			windowRepo := &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: StatusActive, Platform: &platform}}}
			repo := &fingerprintBusyProbeRepo{distributionWindowTestRepo: windowRepo, fingerprintAttempt: test.attempt}
			accounts := &healthProbeAccountRepo{account: *account}
			settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}, nil)
			prober := &fingerprintBusyTestProber{AccountTestService: distributionIdentityTestService()}
			svc := NewUpstreamConfigService(repo, nil, accounts)
			svc.SetHealthProbeDependencies(prober, settings)

			item, err := svc.ProbeKey(context.Background(), keyID)

			if errors.Is(err, errDistributionProbeBusy) {
				t.Fatalf("diagnostic sampling contention escaped as a health-probe failure: %v", err)
			}
			if err != nil {
				t.Fatalf("health probe failed during fingerprint contention: %v", err)
			}
			if item.LastProbeStatus != "success" || prober.calls != test.wantRun {
				t.Fatalf("health result/status calls = %q/%d, want success/%d", item.LastProbeStatus, prober.calls, test.wantRun)
			}
			if len(windowRepo.histories[keyID]) != 1 {
				t.Fatalf("health observation count = %d, want 1", len(windowRepo.histories[keyID]))
			}
			if test.attempt != nil && repo.fingerprintFinish != 1 {
				t.Fatalf("distribution fallback should close the fingerprint lease, finish count=%d", repo.fingerprintFinish)
			}
		})
	}
}

func TestConfidenceFingerprintStorageWithoutDistributionCollectorFallsBackToHealthProbe(t *testing.T) {
	const keyID int64 = 99422
	GlobalUpstreamHealthRegistry().Forget(keyID)
	t.Cleanup(func() { GlobalUpstreamHealthRegistry().Forget(keyID) })
	account := distributionIdentityTestAccount()
	account.UpstreamKeyID = int64Ptr(keyID)
	platform := PlatformOpenAI
	repo := &fingerprintOnlyProbeRepo{
		healthProbeLockRepo: &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: StatusActive, Platform: &platform}},
	}
	accounts := &healthProbeAccountRepo{account: *account}
	settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}, nil)
	prober := &fingerprintBusyTestProber{AccountTestService: distributionIdentityTestService()}
	svc := NewUpstreamConfigService(repo, nil, accounts)
	svc.SetHealthProbeDependencies(prober, settings)

	item, err := svc.ProbeKey(context.Background(), keyID)
	if err != nil {
		t.Fatalf("health probe should continue when both optional collectors are unavailable: %v", err)
	}
	if item.LastProbeStatus != "success" || prober.calls != 1 {
		t.Fatalf("health result/status calls = %q/%d, want success/1", item.LastProbeStatus, prober.calls)
	}
	if item.ConfidenceAttribution == nil || item.ConfidenceAttribution.Status == "match" || item.ConfidenceAttribution.Status == "mismatch" {
		t.Fatalf("missing distribution evidence must not produce a model identity conclusion: %#v", item.ConfidenceAttribution)
	}
}

func TestConfidenceFingerprintIdentityRedactsEndpointAndBindsModelProtocol(t *testing.T) {
	first := NewConfidenceFingerprintIdentity("OpenAI", "gpt-6.1-sol", "responses", "https://user:secret@example.test/v1?token=secret#fragment")
	second := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://example.test/v1?token=other")
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("endpoint identity should ignore credentials, query, and fragment")
	}
	changedModel := NewConfidenceFingerprintIdentity("openai", "other-model", "responses", "https://example.test/v1")
	changedProtocol := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "chat_completions", "https://example.test/v1")
	if first.Fingerprint == changedModel.Fingerprint || first.Fingerprint == changedProtocol.Fingerprint {
		t.Fatal("model and protocol changes must start new series")
	}
	if first.Endpoint == "" || first.Endpoint == "https://example.test/v1" {
		t.Fatalf("endpoint identity must be a digest, got %q", first.Endpoint)
	}
}

func TestConfidenceFingerprintIdentityUsesAPIKeyAndEffectiveModel(t *testing.T) {
	account := &Account{
		ID: 9, Type: AccountTypeAPIKey, Platform: PlatformAnthropic,
		Credentials: map[string]any{"api_key": "secret-key", "base_url": "https://anthropic.example/v1"},
	}
	identity, ok := ConfidenceFingerprintIdentityForAccount(account, "claude-alias")
	if !ok || identity.Model != "claude-alias" || identity.Protocol != upstreamHealthProbeProtocolAnthropic {
		t.Fatalf("identity = %#v, supported=%t", identity, ok)
	}
	if strings.Contains(identity.Fingerprint, "secret-key") || strings.Contains(identity.Endpoint, "anthropic.example") {
		t.Fatal("identity must not persist plaintext credentials or endpoint")
	}
	account.Type = AccountTypeOAuth
	if _, ok := ConfidenceFingerprintIdentityForAccount(account, "claude-alias"); ok {
		t.Fatal("OAuth accounts are outside the API Key fingerprint scope")
	}
}

func TestConfidenceFingerprintClaimRotationAndLease(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	first, err := state.Claim(now)
	if err != nil || first == nil || first.Method != "behavioral" {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if second, err := state.Claim(now.Add(time.Second)); err != nil || second != nil {
		t.Fatalf("active lease must block second claim, got %#v, %v", second, err)
	}
	if err := state.Finish(first, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	second, err := state.Claim(now.Add(2 * time.Second))
	if err != nil || second == nil || second.Method != "distribution" {
		t.Fatalf("second claim = %#v, %v", second, err)
	}
	if err := state.Finish(second, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if state.NextSequence != 3 || len(state.Samples) != 2 {
		t.Fatalf("state after rotation = %#v", state)
	}
	changed := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://other.example.test")
	newSeries, err := NewConfidenceFingerprintState(changed)
	if err != nil {
		t.Fatal(err)
	}
	newSeries.NextSequence = state.NextSequence
	next, err := newSeries.Claim(now.Add(4 * time.Second))
	if err != nil || next == nil || next.Method != "behavioral" || next.Sequence != 3 {
		t.Fatalf("identity change must preserve alternation: %#v, %v", next, err)
	}
}

func TestConfidenceFingerprintUnknownBaselineDoesNotInferModelMismatch(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("deepseek", "deepseek-chat", "openai_chat_completions", "https://api.example.test/v1")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < ConfidenceFingerprintMinimumSamples+1; i++ {
		state.Samples = append(state.Samples, ConfidenceFingerprintSample{Method: "behavioral", Evidence: ConfidenceFingerprintEvidence{ProtocolStatus: "compatible", Shape: "openai_chat_completions"}})
	}
	now := time.Now().UTC()
	for i := range state.Samples {
		state.Samples[i].ObservedAt = now
	}
	result := ScoreConfidenceFingerprintAt(identity, state, now)
	if result.Status != "unknown" || result.Methods["behavioral"].Status != "unknown" || result.Methods["behavioral"].SampleCount != ConfidenceFingerprintMinimumSamples+1 {
		t.Fatalf("uncalibrated behavior evidence must remain unknown: %#v", result)
	}
	state.Samples[len(state.Samples)-1].Evidence.ProtocolStatus = "mismatch"
	state.Samples[len(state.Samples)-1].Evidence.ProtocolReason = "protocol_shape_mismatch"
	result = ScoreConfidenceFingerprintAt(identity, state, now)
	if result.ProtocolStatus != "mismatch" || result.Status != "unknown" {
		t.Fatalf("protocol anomaly must remain separate from attribution: %#v", result)
	}
}

func TestConfidenceFingerprintIdentityMatchesConfiguredProbeRoute(t *testing.T) {
	openAI := &Account{ID: 11, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{
		"api_key": "openai-key", "base_url": "https://responses.example/v1",
	}}
	responses, ok := ConfidenceFingerprintIdentityForAccount(openAI, "gpt-6.1-sol")
	if !ok || responses.Protocol != upstreamHealthProbeProtocolOpenAI || responses.Endpoint != confidenceFingerprintEndpointIdentity(openAI.GetOpenAIBaseURL()) {
		t.Fatalf("Responses identity does not match the probe route: %#v", responses)
	}
	openAI.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
	chat, ok := ConfidenceFingerprintIdentityForAccount(openAI, "gpt-6.1-sol")
	if !ok || chat.Protocol != upstreamHealthProbeProtocolOpenAIChat || chat.Endpoint != confidenceFingerprintEndpointIdentity(openAI.GetOpenAIFormatBaseURL()) {
		t.Fatalf("Chat identity does not match the probe route: %#v", chat)
	}
	if responses.Fingerprint == chat.Fingerprint {
		t.Fatal("changing the configured OpenAI protocol must start a new series")
	}
}

func TestConfidenceFingerprintAdaptiveCNUsesStableConfiguredSeries(t *testing.T) {
	account := &Account{
		ID: 12, Type: AccountTypeAPIKey, Platform: PlatformDeepseek,
		Credentials: map[string]any{
			"api_key":      "deepseek-key",
			"api_protocol": APIProtocolAdaptive,
			"api_base_urls": map[string]any{
				APIProtocolChatCompletions: "https://chat.example/v1",
				APIProtocolResponses:       "https://responses.example/v1",
				APIProtocolAnthropic:       "https://anthropic.example",
			},
		},
	}
	account.UpstreamKeyID = int64Ptr(99)
	configured, ok := ConfidenceFingerprintIdentityForAccount(account, "deepseek-flash")
	if !ok || configured.Protocol != "adaptive_multi_protocol" {
		t.Fatalf("adaptive identity = %#v, supported=%t", configured, ok)
	}
	chatRoute, ok := ConfidenceFingerprintIdentityForEffectiveModel(account, "deepseek-flash", upstreamHealthProbeProtocolOpenAIChat)
	if !ok || chatRoute.Protocol != upstreamHealthProbeProtocolOpenAIChat {
		t.Fatalf("effective chat route identity = %#v, supported=%t", chatRoute, ok)
	}
	if configured.Fingerprint == chatRoute.Fingerprint {
		t.Fatal("the stored route evidence must remain distinct from the stable adaptive series identity")
	}

	changedEndpoint := *account
	changedEndpoint.Credentials = map[string]any{}
	for key, value := range account.Credentials {
		changedEndpoint.Credentials[key] = value
	}
	changedEndpoint.Credentials["api_base_urls"] = map[string]any{
		APIProtocolChatCompletions: "https://changed-chat.example/v1",
		APIProtocolResponses:       "https://responses.example/v1",
		APIProtocolAnthropic:       "https://anthropic.example",
	}
	changed, ok := ConfidenceFingerprintIdentityForAccount(&changedEndpoint, "deepseek-flash")
	if !ok || changed.Fingerprint == configured.Fingerprint {
		t.Fatal("changing any configured adaptive route endpoint must start a new series")
	}
	changedModel, ok := ConfidenceFingerprintIdentityForAccount(account, "other-model")
	if !ok || changedModel.Fingerprint == configured.Fingerprint {
		t.Fatal("changing the mapped model must start a new adaptive series")
	}
}

func TestConfidenceFingerprintOpenAIMethodFusion(t *testing.T) {
	behavior := ConfidenceFingerprintAttribution{Status: "unknown", Platform: PlatformOpenAI, Methods: map[string]ConfidenceFingerprintMethodSummary{
		"behavioral": {Status: "match", SampleCount: 16, MinimumSamples: 12, BaselineVersion: "behavior-v1"},
	}}
	for _, test := range []struct {
		name               string
		distributionStatus string
		want               string
	}{
		{name: "methods agree", distributionStatus: "match", want: "confirmed_match"},
		{name: "single method", distributionStatus: "insufficient", want: "provisional_match"},
		{name: "conflict", distributionStatus: "mismatch", want: "conflict"},
		{name: "both insufficient", distributionStatus: "insufficient", want: "insufficient"},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := behavior
			if test.name == "both insufficient" {
				method = ConfidenceFingerprintAttribution{Status: "insufficient", Platform: PlatformOpenAI, Methods: map[string]ConfidenceFingerprintMethodSummary{
					"behavioral": {Status: "insufficient", SampleCount: 2, MinimumSamples: 12, Reason: "insufficient_samples"},
				}}
			}
			distribution := &UpstreamConfidenceDistribution{Status: test.distributionStatus, ValidSamples: UpstreamConfidenceDistributionWindowSize, BaselineVersion: "distribution-v1"}
			if test.distributionStatus == "insufficient" {
				distribution.ValidSamples = 2
			}
			got := MergeConfidenceFingerprintAttribution(distribution, method)
			if got.Status != test.want {
				t.Fatalf("status = %q, want %q", got.Status, test.want)
			}
			if got.Methods["distribution"].Status != test.distributionStatus {
				t.Fatalf("distribution method status = %#v", got.Methods["distribution"])
			}
		})
	}
}

func TestConfidenceFingerprintProtocolEvidenceIsIndependent(t *testing.T) {
	compatible := ClassifyConfidenceFingerprintResponse("anthropic_messages", "success", "probe_succeeded", nil)
	if compatible.ProtocolStatus != "compatible" || compatible.Shape != "anthropic_messages" {
		t.Fatalf("compatible protocol evidence = %#v", compatible)
	}
	mismatch := ClassifyConfidenceFingerprintResponse("gemini_stream_generate_content", "invalid_response", "probe_protocol_mismatch", nil)
	if mismatch.ProtocolStatus != "mismatch" || mismatch.ProtocolReason == "" {
		t.Fatalf("protocol mismatch evidence = %#v", mismatch)
	}
	if mismatch.OutputClass != "" {
		t.Fatalf("protocol anomaly must not infer model class: %#v", mismatch)
	}
}

func TestConfidenceFingerprintOpenAIProbeTurnMarker(t *testing.T) {
	ctx := context.WithValue(context.Background(), confidenceFingerprintAttemptContextKey{}, &ConfidenceFingerprintAttempt{Method: "behavioral"})
	if _, ok := ctx.Value(confidenceFingerprintAttemptContextKey{}).(*ConfidenceFingerprintAttempt); !ok {
		t.Fatal("fingerprint turn marker missing")
	}
}

func TestConfidenceFingerprintAdaptiveProbeKeepsOneFinalRoute(t *testing.T) {
	result := UpstreamHealthProbeResult{Protocol: upstreamHealthProbeProtocolAdaptive, ProtocolResults: map[string]UpstreamHealthProbeResult{
		upstreamHealthProbeProtocolOpenAI:     {Protocol: upstreamHealthProbeProtocolOpenAI, Result: "invalid_response", Reason: "probe_protocol_mismatch", Model: "deepseek-chat"},
		upstreamHealthProbeProtocolOpenAIChat: {Protocol: upstreamHealthProbeProtocolOpenAIChat, Result: "success", Model: "deepseek-chat"},
		upstreamHealthProbeProtocolAnthropic:  {Protocol: upstreamHealthProbeProtocolAnthropic, Result: "success", Model: "deepseek-chat"},
	}}
	selected, protocol := effectiveFingerprintProbeResult(result)
	if protocol != upstreamHealthProbeProtocolOpenAIChat || selected.Model != "deepseek-chat" {
		t.Fatalf("adaptive fingerprint must use the primary successful route once: %#v, %q", selected, protocol)
	}
	failed := UpstreamHealthProbeResult{Protocol: upstreamHealthProbeProtocolAdaptive, ProtocolResults: map[string]UpstreamHealthProbeResult{
		upstreamHealthProbeProtocolOpenAI:     {Protocol: upstreamHealthProbeProtocolOpenAI, Result: "invalid_response", Reason: "probe_protocol_mismatch", Model: "deepseek-chat"},
		upstreamHealthProbeProtocolOpenAIChat: {Protocol: upstreamHealthProbeProtocolOpenAIChat, Result: "invalid_response", Reason: "probe_response_mismatch", Model: "deepseek-chat"},
	}}
	selected, protocol = effectiveFingerprintProbeResult(failed)
	if protocol != upstreamHealthProbeProtocolOpenAIChat || selected.Reason != "probe_response_mismatch" {
		t.Fatalf("failed adaptive probe must report its final route only: %#v, %q", selected, protocol)
	}
}
