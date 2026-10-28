package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service/modeltrace_numeric"
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

func (*fingerprintOnlyProbeRepo) RecordConfidenceFingerprintSample(context.Context, int64, ConfidenceFingerprintIdentity, string, ConfidenceFingerprintEvidence, time.Time) error {
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

func (*fingerprintBusyProbeRepo) RecordConfidenceFingerprintSample(context.Context, int64, ConfidenceFingerprintIdentity, string, ConfidenceFingerprintEvidence, time.Time) error {
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
	if err != nil || first == nil || first.Method != "distribution" {
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
	if state.NextSequence != 3 || len(state.Samples) != 0 || state.LastProtocolEvidence == nil {
		t.Fatalf("state after rotation = %#v", state)
	}
	changed := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://other.example.test")
	newSeries, err := NewConfidenceFingerprintState(changed)
	if err != nil {
		t.Fatal(err)
	}
	newSeries.NextSequence = state.NextSequence
	next, err := newSeries.Claim(now.Add(4 * time.Second))
	if err != nil || next == nil || next.Method != "distribution" || next.Sequence != 3 {
		t.Fatalf("identity change without baseline must preserve distribution-first policy: %#v, %v", next, err)
	}
}

func TestConfidenceFingerprintSamplingMethodsByBaseline(t *testing.T) {
	openAI := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://example.test")
	state := &ConfidenceFingerprintState{Components: openAI.Components, SeriesID: "openai-series"}
	for sequence := int64(1); sequence <= 20; sequence++ {
		if got := state.samplingMethod(sequence); got != "distribution" {
			t.Fatalf("OpenAI without a behavioral baseline sequence %d = %q, want distribution", sequence, got)
		}
	}

	other := NewConfidenceFingerprintIdentity("anthropic", "claude", "messages", "https://example.test")
	otherState := &ConfidenceFingerprintState{Components: other.Components, SeriesID: "anthropic-series"}
	if got := otherState.samplingMethod(1); got != "protocol" {
		t.Fatalf("non-OpenAI without baseline = %q, want protocol", got)
	}
	other.Components["baseline"] = ChanshuiBehavioralBaselineVersion + "+modeltrace"
	other.Components["baseline_chanshui_behavioral"] = ChanshuiBehavioralBaselineVersion
	other.Components["baseline_modeltrace_numeric"] = "modeltrace-v1"
	otherState.Components = other.Components
	if got := otherState.samplingMethod(1); got != "chanshui_behavioral" {
		t.Fatalf("non-OpenAI first reference sample = %q, want Chanshui", got)
	}
	if got := otherState.samplingMethod(2); got != "modeltrace_numeric" {
		t.Fatalf("non-OpenAI second reference sample = %q, want ModelTrace", got)
	}
}

func TestConfidenceFingerprintSamplingUsesShuffledNinetyTenBlocksWithBaseline(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-5.6-terra", "responses", "https://example.test")
	identity.Components["baseline"] = "reviewed-v1"
	identity.Components["baseline_chanshui_behavioral"] = ChanshuiBehavioralBaselineVersion
	identity.Components["baseline_modeltrace_numeric"] = "modeltrace-v1"
	firstState := &ConfidenceFingerprintState{Components: identity.Components, SeriesID: "series-a"}
	secondState := &ConfidenceFingerprintState{Components: identity.Components, SeriesID: "series-b"}
	behavioralSlots := map[int64]bool{}
	sequencesBySlot := make(map[int64]map[int64]bool)
	for block := int64(0); block < 5; block++ {
		count := 0
		for offset := int64(1); offset <= 10; offset++ {
			sequence := block*10 + offset
			method := firstState.samplingMethod(sequence)
			if isReferenceFingerprintMethod(method) {
				count++
				slot := (sequence - 1) % 10
				behavioralSlots[slot] = true
				if sequencesBySlot[slot] == nil {
					sequencesBySlot[slot] = make(map[int64]bool)
				}
				sequencesBySlot[slot][sequence] = true
			} else if method != "distribution" {
				t.Fatalf("unexpected method %q at sequence %d", method, sequence)
			}
		}
		if count != 1 {
			t.Fatalf("block %d has %d reference samples, want exactly 1", block, count)
		}
	}
	if len(behavioralSlots) < 2 {
		t.Fatalf("behavioral slot did not move between blocks: %v", behavioralSlots)
	}
	replayState := &ConfidenceFingerprintState{Components: identity.Components, SeriesID: "series-a"}
	for sequence := int64(1); sequence <= 50; sequence++ {
		if firstState.samplingMethod(sequence) != replayState.samplingMethod(sequence) {
			t.Fatalf("same series and sequence %d must be reproducible", sequence)
		}
	}
	if firstState.samplingMethod(1) == secondState.samplingMethod(1) {
		for sequence := int64(1); sequence <= 10; sequence++ {
			if firstState.samplingMethod(sequence) != secondState.samplingMethod(sequence) {
				return
			}
		}
		t.Fatal("different series should eventually spread the behavioral slot")
	}
}

func TestConfidenceFingerprintReferenceBaselinesRequireExactModelCoverage(t *testing.T) {
	covered := NewConfidenceFingerprintIdentity("openai", "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	if covered.Components["baseline_chanshui_behavioral"] == "" || covered.Components["baseline_modeltrace_numeric"] == "" {
		t.Fatalf("expected both pinned references for GPT-5.6 Sol: %#v", covered.Components)
	}
	uncovered := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	if uncovered.Baseline != "" || uncovered.Components["baseline_chanshui_behavioral"] != "" || uncovered.Components["baseline_modeltrace_numeric"] != "" {
		t.Fatalf("GPT-6.1 Sol must not borrow GPT-5.6/GPT-6 baselines: %#v", uncovered.Components)
	}
}

func TestConfidenceFingerprintBaselineRequiresExactModelAndSupportedProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, platform, model, protocol string
		wantBaseline                    bool
	}{
		{"native openai protocol", PlatformOpenAI, "gpt-6-sol", upstreamHealthProbeProtocolOpenAI, true},
		{"native chat protocol", PlatformOpenAI, "gpt-6-sol", upstreamHealthProbeProtocolOpenAIChat, true},
		{"unsupported adaptive protocol", PlatformOpenAI, "gpt-6-sol", "adaptive_multi_protocol", false},
		{"unsupported protocol alias", PlatformOpenAI, "gpt-6-sol", "responses", false},
		{"different namespace", PlatformOpenAI, "vendor/gpt-6-sol", upstreamHealthProbeProtocolOpenAI, false},
		{"namespaced modeltrace target", PlatformOpenAI, "openai/gpt-6-sol", upstreamHealthProbeProtocolOpenAI, false},
		{"wrong provider family", PlatformAnthropic, "gpt-6-sol", upstreamHealthProbeProtocolAnthropic, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := NewConfidenceFingerprintIdentity(tc.platform, tc.model, tc.protocol, "https://example.test")
			got := identity.Baseline != ""
			if got != tc.wantBaseline {
				t.Fatalf("baseline %q for %s/%s/%s, want present=%t", identity.Baseline, tc.platform, tc.model, tc.protocol, tc.wantBaseline)
			}
		})
	}
}

func TestConfidenceFingerprintScoresReferenceMethodsOnlyAfterThreeChallenges(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	chanshuiResponses := []string{
		"可能是 7 分，但我不确定。",
		"无法作出法律承诺。",
		"# 建议\n1. 先拆解问题\n2. 写测试\n3. 自动化",
	}
	for index, response := range chanshuiResponses {
		feature, err := ChanshuiBehaviorFeatureForChallenge(index, response)
		if err != nil {
			t.Fatal(err)
		}
		sequence := int64(index + 1)
		if err := state.RecordMethodEvidence("chanshui_behavioral", sequence, now.Add(time.Duration(index)*time.Minute), ConfidenceFingerprintEvidence{
			ProtocolStatus: "compatible", MethodSequence: sequence, ChallengeIndex: index, Feature: feature,
		}); err != nil {
			t.Fatal(err)
		}
	}
	state.MethodSequences = map[string]int64{"chanshui_behavioral": 3}
	for index := 0; index < 3; index++ {
		values := make([]int, 240)
		for position := range values {
			values[position] = (position*37+index*13)%355 + 1
		}
		sequence := int64(index + 1)
		if err := state.RecordMethodEvidence("modeltrace_numeric", sequence, now.Add(time.Duration(index)*time.Minute), ConfidenceFingerprintEvidence{
			ProtocolStatus: "compatible", MethodSequence: sequence, ChallengeIndex: index, Numbers: values,
		}); err != nil {
			t.Fatal(err)
		}
	}
	state.MethodSequences["modeltrace_numeric"] = 3

	result := ScoreConfidenceFingerprintAt(identity, state, now.Add(4*time.Minute))
	for _, method := range []string{"chanshui_behavioral", "modeltrace_numeric"} {
		summary := result.Methods[method]
		if summary.Status != "reference_only" || summary.SampleCount != 3 || len(summary.Candidates) != 3 || summary.Participating {
			t.Fatalf("%s should return reference ranking only: %#v", method, summary)
		}
	}
	if result.Status != "reference_only" || result.Reason != "reference_ranking_only" {
		t.Fatalf("reference rankings must not become an identity verdict: %#v", result)
	}

	state.MethodSequences["modeltrace_numeric"] = 4
	result = ScoreConfidenceFingerprintAt(identity, state, now.Add(4*time.Minute))
	if got := result.Methods["modeltrace_numeric"]; got.Status != "reference_only" || got.SampleCount != 3 || got.CurrentSampleCount != 0 {
		t.Fatalf("a new incomplete challenge set must preserve the last complete evidence: %#v", got)
	}
}

func TestConfidenceFingerprintReferenceProgressIsAccurateAndExpires(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	responses := []string{"可能是 7 分。", "无法作出法律承诺。", "# 建议\n1. 先拆解问题\n2. 写测试"}
	for index := 0; index < 2; index++ {
		feature, err := ChanshuiBehaviorFeatureForChallenge(index, responses[index])
		if err != nil {
			t.Fatal(err)
		}
		sequence := int64(index + 1)
		if err := state.RecordMethodEvidence("chanshui_behavioral", sequence, now.Add(time.Duration(index)*time.Minute), ConfidenceFingerprintEvidence{
			ProtocolStatus: "compatible", MethodSequence: sequence, ChallengeIndex: index, Feature: feature,
		}); err != nil {
			t.Fatal(err)
		}
	}
	state.MethodSequences = map[string]int64{"chanshui_behavioral": 2}
	progress := ScoreConfidenceFingerprintAt(identity, state, now.Add(2*time.Minute)).Methods["chanshui_behavioral"]
	if progress.Status != "insufficient" || progress.SampleCount != 2 || progress.MinimumSamples != 3 || len(progress.Candidates) != 0 {
		t.Fatalf("two valid responses should report 2/3 without scoring: %#v", progress)
	}
	feature, err := ChanshuiBehaviorFeatureForChallenge(2, responses[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RecordMethodEvidence("chanshui_behavioral", 3, now.Add(3*time.Minute), ConfidenceFingerprintEvidence{
		ProtocolStatus: "compatible", MethodSequence: 3, ChallengeIndex: 2, Feature: feature,
	}); err != nil {
		t.Fatal(err)
	}
	state.MethodSequences["chanshui_behavioral"] = 3
	complete := ScoreConfidenceFingerprintAt(identity, state, now.Add(4*time.Minute)).Methods["chanshui_behavioral"]
	if complete.Status != "reference_only" || complete.SampleCount != 3 || len(complete.Candidates) == 0 {
		t.Fatalf("complete challenge set should produce reference candidates: %#v", complete)
	}
	expiredAt := now.Add(UpstreamHealthObservationRetention + 4*time.Minute)
	expired := ScoreConfidenceFingerprintAt(identity, state, expiredAt).Methods["chanshui_behavioral"]
	if expired.Status != "insufficient" || expired.SampleCount != 0 || len(expired.Candidates) != 0 {
		t.Fatalf("expired samples must not produce progress or candidates: %#v", expired)
	}
	state.MethodSequences["chanshui_behavioral"] = 4
	feature, err = ChanshuiBehaviorFeatureForChallenge(0, responses[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := state.RecordMethodEvidence("chanshui_behavioral", 4, expiredAt.Add(time.Minute), ConfidenceFingerprintEvidence{
		ProtocolStatus: "compatible", MethodSequence: 4, ChallengeIndex: 0, Feature: feature,
	}); err != nil {
		t.Fatal(err)
	}
	newWindow := ScoreConfidenceFingerprintAt(identity, state, expiredAt.Add(2*time.Minute)).Methods["chanshui_behavioral"]
	if newWindow.Status != "insufficient" || newWindow.SampleCount != 1 || len(newWindow.Candidates) != 0 {
		t.Fatalf("new challenge block should show 1/3 and hide the prior ranking: %#v", newWindow)
	}
}

func TestConfidenceFingerprintReferenceSummaryDoesNotClaimMissingBaseline(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity(PlatformOpenAI, "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	result := ScoreConfidenceFingerprintAt(identity, nil, now)
	if result.Status != "insufficient" || result.Reason != "insufficient_samples" || len(result.Methods) != 3 {
		t.Fatalf("applicable but uncollected references are insufficient, not missing: %#v", result)
	}
	for _, distributionStatus := range []string{"insufficient", "match", "mismatch"} {
		fingerprint := ConfidenceFingerprintAttribution{Platform: PlatformOpenAI, Methods: map[string]ConfidenceFingerprintMethodSummary{
			"chanshui_behavioral": {Status: "reference_only", SampleCount: 3, MinimumSamples: 3},
			"behavioral":          {Status: "unknown", Reason: "no_matching_baseline"},
		}}
		merged := MergeConfidenceFingerprintAttribution(&UpstreamConfidenceDistribution{Status: distributionStatus}, fingerprint)
		want := "reference_only"
		if distributionStatus != "insufficient" {
			want = "provisional_" + distributionStatus
		}
		if merged.Status != want || merged.Reason == "no_matching_baseline" {
			t.Fatalf("reference ranking must remain separate from distribution verdict: %#v", merged)
		}
	}
}

func TestConfidenceFingerprintReferenceClaimsSurviveRestartWithoutExtraSamples(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity(PlatformOpenAI, "gpt-5.6-terra", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	counts := map[string]int{}
	responses := []string{"可能是 7 分。", "无法作出法律承诺。", "# 建议\n1. 写测试\n2. 自动化"}
	for sequence := 0; sequence < 60; sequence++ {
		at := now.Add(time.Duration(sequence) * time.Minute)
		attempt, err := state.Claim(at)
		if err != nil || attempt == nil {
			t.Fatalf("claim %d = %#v, %v", sequence, attempt, err)
		}
		counts[attempt.Method]++
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		restored := &ConfidenceFingerprintState{}
		if err := json.Unmarshal(raw, restored); err != nil {
			t.Fatal(err)
		}
		evidence := ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}
		if isReferenceFingerprintMethod(attempt.Method) {
			index := int((attempt.MethodSequence - 1) % 3)
			response := responses[index]
			if attempt.Method == "modeltrace_numeric" {
				response = strings.Repeat("17 73 109 ", 100)
			}
			addReferenceFingerprintPayload(&evidence, attempt, response)
		}
		if err := restored.Finish(attempt, evidence, at.Add(time.Second)); err != nil {
			t.Fatalf("finish after restart: %v", err)
		}
		if err := restored.Finish(attempt, evidence, at.Add(2*time.Second)); err == nil {
			t.Fatal("replayed completion must not add a second sample")
		}
		state = restored
	}
	if counts["distribution"] != 54 || counts["chanshui_behavioral"] != 3 || counts["modeltrace_numeric"] != 3 || len(state.Samples) != 6 {
		t.Fatalf("sixty claims changed the sampling budget: %v, samples=%d", counts, len(state.Samples))
	}
	result := ScoreConfidenceFingerprintAt(identity, state, now.Add(time.Hour))
	if result.Status != "reference_only" || result.Methods["chanshui_behavioral"].SampleCount != 3 || result.Methods["modeltrace_numeric"].SampleCount != 3 {
		t.Fatalf("restart lost reference challenge progress: %#v", result)
	}
}

func TestConfidenceFingerprintReferenceRejectsShortAndTruncatedEvidence(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity(PlatformOpenAI, "gpt-5.6-sol", upstreamHealthProbeProtocolOpenAI, "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	state.MethodSequences = map[string]int64{"modeltrace_numeric": 3}
	for index := 0; index < 3; index++ {
		minimum, err := modeltrace_numeric.MinimumForChallenge(index)
		if err != nil {
			t.Fatal(err)
		}
		attempt := &ConfidenceFingerprintAttempt{Method: "modeltrace_numeric", MethodSequence: int64(index + 1)}
		evidence := ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}
		addReferenceFingerprintPayload(&evidence, attempt, strings.Repeat("17 ", minimum-1))
		if len(evidence.Numbers) != 0 {
			t.Fatalf("challenge %d accepted %d values below its minimum %d", index, len(evidence.Numbers), minimum)
		}
		addReferenceFingerprintPayload(&evidence, attempt, strings.Repeat("17 ", minimum))
		if len(evidence.Numbers) != minimum {
			t.Fatalf("challenge %d rejected exact minimum", index)
		}
		if err := state.RecordMethodEvidence(attempt.Method, attempt.MethodSequence, now, evidence); err != nil {
			t.Fatal(err)
		}
	}
	if got := ScoreConfidenceFingerprintAt(identity, state, now).Methods["modeltrace_numeric"]; got.Status != "reference_only" {
		t.Fatalf("exact source minimum should score: %#v", got)
	}
	state.Samples[1].Evidence.FinishClass = "finish_reason:max_tokens"
	if got := ScoreConfidenceFingerprintAt(identity, state, now).Methods["modeltrace_numeric"]; got.Status != "insufficient" || got.SampleCount != 2 || len(got.Candidates) != 0 {
		t.Fatalf("historical truncated evidence must not score: %#v", got)
	}
}

func TestConfidenceFingerprintOldStateKeepsSequenceAndPendingMethod(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://example.test")
	state := &ConfidenceFingerprintState{
		PolicyVersion: ConfidenceFingerprintPolicyVersion, SeriesID: "legacy-series", Fingerprint: identity.Fingerprint,
		Components: identity.Components, NextSequence: 9,
		Pending:      &ConfidenceFingerprintAttempt{SeriesID: "legacy-series", Token: "lease", Method: "behavioral", Sequence: 8},
		LeaseExpires: time.Now().UTC().Add(time.Minute), Samples: []ConfidenceFingerprintSample{{Method: "behavioral"}},
	}
	if err := state.Validate(); err != nil {
		t.Fatal(err)
	}
	if attempt, err := state.Claim(time.Now().UTC()); err != nil || attempt != nil {
		t.Fatalf("legacy active attempt must remain pending: %#v, %v", attempt, err)
	}
	if state.NextSequence != 9 || state.Pending.Method != "behavioral" || len(state.Samples) != 1 {
		t.Fatalf("legacy state was reset: %#v", state)
	}
	attempt, err := state.Claim(state.LeaseExpires.Add(time.Second))
	if err != nil || attempt == nil || attempt.Sequence != 9 || attempt.Method != "distribution" {
		t.Fatalf("legacy sequence should continue under new no-baseline policy: %#v, %v", attempt, err)
	}
	if state.SamplingPolicyVersion != ConfidenceFingerprintSamplingPolicyVersion {
		t.Fatalf("sampling policy version = %d", state.SamplingPolicyVersion)
	}
}

func TestConfidenceFingerprintProtocolEvidenceDoesNotConsumeBehaviorSamples(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("anthropic", "claude", "messages", "https://example.test")
	identity.Components["baseline"] = "reviewed-v1"
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	for i := 0; i < ConfidenceFingerprintSampleLimit+10; i++ {
		if err := state.RecordEvidence("protocol", now.Add(time.Duration(i)*time.Minute), ConfidenceFingerprintEvidence{
			ProtocolStatus: "compatible", Route: "anthropic_messages",
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 7; i++ {
		if err := state.RecordEvidence("behavioral", now.Add(time.Hour+time.Duration(i)*time.Minute), ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 100; i++ {
		if err := state.RecordEvidence("protocol", now.Add(2*time.Hour+time.Duration(i)*time.Minute), ConfidenceFingerprintEvidence{
			ProtocolStatus: "compatible", Route: "anthropic_messages",
		}); err != nil {
			t.Fatal(err)
		}
	}
	result := ScoreConfidenceFingerprintAt(identity, state, now.Add(4*time.Hour))
	if got := result.Methods["behavioral"].SampleCount; got != 7 {
		t.Fatalf("protocol refreshes changed behavioral sample count to %d, want 7", got)
	}
	if result.ObservedProtocol != "anthropic_messages" || result.ProtocolStatus != "compatible" {
		t.Fatalf("latest protocol evidence = %#v, want latest compatible route", result)
	}
	if len(state.Samples) != 7 {
		t.Fatalf("protocol evidence consumed behavioral sample capacity: %d", len(state.Samples))
	}
}

func TestConfidenceFingerprintFinishRejectsMethodMutation(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity("openai", "gpt-6.1-sol", "responses", "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	attempt, err := state.Claim(now)
	if err != nil || attempt == nil {
		t.Fatalf("claim = %#v, %v", attempt, err)
	}
	mutated := *attempt
	mutated.Method = "protocol"
	if err := state.Finish(&mutated, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, now.Add(time.Second)); err == nil {
		t.Fatal("finish must reject a changed method on an existing lease")
	}
	if state.Pending == nil || state.Pending.Method != attempt.Method || len(state.Samples) != 0 {
		t.Fatalf("invalid method mutation changed pending state: %#v", state)
	}
}

func TestConfidenceFingerprintFinishRejectsChallengeOrTimeMutation(t *testing.T) {
	identity := NewConfidenceFingerprintIdentity(PlatformAnthropic, "claude-sonnet-4-6", upstreamHealthProbeProtocolAnthropic, "https://example.test")
	state, err := NewConfidenceFingerprintState(identity)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	attempt, err := state.Claim(now)
	if err != nil || attempt == nil {
		t.Fatalf("claim = %#v, %v", attempt, err)
	}
	for _, mutation := range []func(*ConfidenceFingerprintAttempt){
		func(a *ConfidenceFingerprintAttempt) { a.MethodSequence++ },
		func(a *ConfidenceFingerprintAttempt) { a.ObservedAt = a.ObservedAt.Add(time.Second) },
	} {
		mutated := *attempt
		mutation(&mutated)
		if err := state.Finish(&mutated, ConfidenceFingerprintEvidence{ProtocolStatus: "compatible"}, now.Add(time.Second)); err == nil {
			t.Fatal("completion must match the persisted challenge and observation time")
		}
		if state.Pending == nil || len(state.Samples) != 0 {
			t.Fatal("rejected completion mutated the persisted state")
		}
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
