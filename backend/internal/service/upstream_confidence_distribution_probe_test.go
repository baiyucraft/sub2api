package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

type distributionServiceTestRepo struct{ *healthProbeLockRepo }

func (*distributionServiceTestRepo) ClaimConfidenceDistribution(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*DistributionAttempt, error) {
	return &DistributionAttempt{Sequence: 1, ProbeID: DistributionProbeCountry}, nil
}

type distributionWindowTestRepo struct {
	*healthProbeLockRepo
	state  *ConfidenceDistributionState
	alerts int
}

func (r *distributionWindowTestRepo) ClaimConfidenceDistribution(_ context.Context, _ int64, identity ConfidenceDistributionIdentity, now time.Time) (*DistributionAttempt, error) {
	if r.state == nil || r.state.Fingerprint != identity.Fingerprint {
		var err error
		r.state, err = NewConfidenceDistributionStateForIdentity(identity)
		if err != nil {
			return nil, err
		}
	}
	return r.state.Claim(now)
}
func (r *distributionWindowTestRepo) FinishConfidenceDistribution(_ context.Context, _ int64, attempt *DistributionAttempt, sample DistributionSample, now time.Time) (*UpstreamConfidenceDistribution, bool, error) {
	summary, alert, err := r.state.Finish(attempt, sample, now)
	if alert {
		r.alerts++
	}
	return summary, alert, err
}
func (r *distributionWindowTestRepo) LoadConfidenceDistribution(context.Context, int64, time.Time) (*UpstreamConfidenceDistribution, error) {
	return r.state.Summary()
}
func (r *distributionWindowTestRepo) LoadConfidenceDistributionForSeries(_ context.Context, _ int64, identity ConfidenceDistributionIdentity, _ time.Time) (*UpstreamConfidenceDistribution, error) {
	if r.state == nil || r.state.Fingerprint != identity.Fingerprint {
		return nil, nil
	}
	return r.state.Summary()
}
func (*distributionWindowTestRepo) GetUpstreamHealthConfidence(context.Context, int64) (UpstreamHealthConfidenceSummary, error) {
	legacy := 100.0
	return UpstreamHealthConfidenceSummary{Score7d: &legacy, PromptVersion: UpstreamConfidencePromptVersion}, nil
}

type distributionSequenceHTTPStub struct {
	upstreamHealthProbeHTTPStub
	repo   *distributionWindowTestRepo
	counts map[string]map[string]int
}

type distributionUnavailableTestRepo struct {
	*distributionWindowTestRepo
	readerErr, distributionErr error
	distributionReads          int
}

func (r *distributionUnavailableTestRepo) GetUpstreamHealthConfidence(context.Context, int64) (UpstreamHealthConfidenceSummary, error) {
	oldScore, lastScore := 100.0, 100
	return UpstreamHealthConfidenceSummary{
		Distribution: &UpstreamConfidenceDistribution{Status: "match", Attempted: 128, ClosestModel: "gpt-6.1-sol", Matches: map[string]float64{"gpt-6.1-sol": 1}},
		Score24h:     &oldScore, Score7d: &oldScore, LastScore: &lastScore, Status: "current_success",
		LastEvidence: map[string]any{"kind": "juice", "old": "evidence"}, PromptVersion: UpstreamConfidencePromptVersion,
	}, r.readerErr
}

func (r *distributionUnavailableTestRepo) LoadConfidenceDistributionForSeries(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*UpstreamConfidenceDistribution, error) {
	r.distributionReads++
	return &UpstreamConfidenceDistribution{Status: "match", Attempted: 128, ClosestModel: "gpt-6.1-sol", Matches: map[string]float64{"gpt-6.1-sol": 1}}, r.distributionErr
}

func TestDistributionConfidenceUnavailableClearsCachedVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name, reason               string
		mutate                     func(*Account)
		readerErr, distributionErr error
		noResolver                 bool
		wantDistributionReads      int
	}{
		{name: "missing credentials", reason: "probe_credentials_missing", mutate: func(a *Account) { delete(a.Credentials, "api_key") }},
		{name: "invalid endpoint", reason: "probe_base_url_invalid", mutate: func(a *Account) { a.Credentials["base_url"] = "not-a-url-sensitive-secret" }},
		{name: "unsupported model", reason: "probe_model_unsupported", mutate: func(a *Account) { a.Credentials["model_mapping"] = map[string]any{"different": "model"} }},
		{name: "missing resolver", reason: "request_configuration_unavailable", noResolver: true},
		{name: "legacy read failed", reason: "confidence_read_unavailable", readerErr: errors.New("synthetic-sensitive-db-error")},
		{name: "distribution read failed", reason: "confidence_read_unavailable", distributionErr: errors.New("synthetic-sensitive-db-error"), wantDistributionReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := distributionIdentityTestAccount()
			account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
			if tc.mutate != nil {
				tc.mutate(account)
			}
			repo := &distributionUnavailableTestRepo{distributionWindowTestRepo: &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{}}, readerErr: tc.readerErr, distributionErr: tc.distributionErr}
			accounts := &healthProbeAccountRepo{account: *account}
			svc := NewUpstreamConfigService(repo, nil, accounts)
			settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}, nil)
			client := distributionIdentityTestService()
			upstream := &upstreamHealthProbeHTTPStub{}
			client.httpUpstream = upstream
			svc.SetHealthProbeDependencies(client, settings)
			if tc.noResolver {
				svc.accountProber = nil
			}
			summary, err := svc.GetUpstreamHealthConfidence(context.Background(), 501)
			require.NoError(t, err, "callers merge the clean summary only when no error is returned")
			require.NotNil(t, summary.Distribution)
			require.Equal(t, "insufficient", summary.Distribution.Status)
			require.Equal(t, []string{tc.reason}, summary.Distribution.Reasons)
			require.Nil(t, summary.Score24h)
			require.Nil(t, summary.Score7d)
			require.Nil(t, summary.LastScore)
			require.Empty(t, summary.LastEvidence)
			require.Empty(t, summary.Distribution.Matches)
			require.Empty(t, summary.Distribution.Scores)
			require.Empty(t, summary.Distribution.ClosestModel)
			require.Zero(t, summary.Distribution.Attempted)
			require.Nil(t, summary.Distribution.SeriesReset)
			require.Equal(t, tc.wantDistributionReads, repo.distributionReads)
			require.Nil(t, repo.state, "reading failed configuration never creates or claims a series")
			require.Empty(t, upstream.requests)
			old, _ := repo.GetUpstreamHealthConfidence(context.Background(), 501)
			cached := MergeUpstreamHealthConfidence(UpstreamHealthSnapshot{Status: UpstreamHealthHealthy}, old)
			merged := MergeUpstreamHealthConfidence(cached, summary)
			require.Nil(t, merged.ConfidenceScore7d)
			require.Nil(t, merged.ConfidenceLastScore)
			require.Empty(t, merged.ConfidenceEvidence)
			require.Equal(t, "insufficient", merged.ConfidenceDistribution.Status)
			require.Equal(t, UpstreamHealthHealthy, merged.Status, "display failures do not alter health")
			public, marshalErr := json.Marshal(merged)
			require.NoError(t, marshalErr)
			require.NotContains(t, string(public), "sensitive")
			require.NotContains(t, string(public), `"old":`)
		})
	}
}

func (s *distributionSequenceHTTPStub) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	probeID := s.repo.state.Pending.ProbeID
	answer := "unknown-extra-sample"
	for category, n := range s.counts[probeID] {
		if n > 0 {
			answer = category
			s.counts[probeID][category]--
			break
		}
	}
	s.stream = fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", answer)
	return s.Do(req, proxyURL, accountID, concurrency)
}

func TestDistributionProbeFullWindowIntegration(t *testing.T) {
	const keyID int64 = 92321
	GlobalUpstreamHealthRegistry().Forget(keyID)
	defer GlobalUpstreamHealthRegistry().Forget(keyID)
	platform := PlatformOpenAI
	repo := &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: StatusActive, Platform: &platform}}}
	accounts := &healthProbeAccountRepo{account: Account{ID: 22321, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, UpstreamKeyID: int64Ptr(keyID), Credentials: map[string]any{"api_key": "synthetic-key", "base_url": "https://probe.example"}}}
	var fixture distributionOracleFixture
	for _, candidate := range distributionFixtures(t, "responses") {
		if candidate.Expected.Model == "gpt-6-astra" {
			fixture = candidate
			break
		}
	}
	require.NotNil(t, fixture.Counts)
	upstream := &distributionSequenceHTTPStub{repo: repo, counts: fixture.Counts}
	client := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
	settings := NewSettingService(&upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}, nil)
	client.SetSettingService(settings)
	svc := NewUpstreamConfigService(repo, nil, accounts)
	svc.SetHealthProbeDependencies(client, settings)
	reporter := &probeScheduleReporter{}
	svc.SetOpenAIScheduleReporter(reporter)
	for i := 1; i <= 128; i++ {
		// Automatic whitelist refreshes must not discard an otherwise identical
		// request window, including its sampling boundary at 127/128/129.
		accounts.account.Credentials["model_mapping"] = map[string]any{UpstreamConfidenceDistributionClaimedModel: UpstreamConfidenceDistributionClaimedModel, fmt.Sprintf("unrelated-%d", i): "unrelated"}
		accounts.account.Extra = map[string]any{"upstream_model_sync": map[string]any{"checked_at": i}}
		item, err := svc.ProbeKey(context.Background(), keyID)
		require.NoError(t, err)
		require.Equal(t, "success", item.LastProbeStatus)
		require.Len(t, upstream.requests, i)
		require.NotNil(t, item.ConfidenceDistribution)
		require.Equal(t, i, item.ConfidenceDistribution.Attempted)
		require.Nil(t, item.ConfidenceScore7d, "old Juice ratios must not be presented for a new series")
		if i < 128 {
			require.Equal(t, "collecting", item.ConfidenceDistribution.Status)
		} else {
			require.Equal(t, "mismatch", item.ConfidenceDistribution.Status)
			require.Equal(t, "gpt-6-astra", item.ConfidenceDistribution.ClosestModel)
			require.Equal(t, 128, item.ConfidenceDistribution.ValidSamples)
		}
	}
	require.Equal(t, 1, repo.alerts)
	require.Empty(t, reporter.reports)
	require.Nil(t, accounts.account.TempUnschedulableUntil)
	require.Len(t, repo.histories[keyID], 128)
	require.Equal(t, "distribution", repo.histories[keyID][127].ConfidenceEvidence["kind"])
	require.Nil(t, repo.histories[keyID][127].ConfidenceScore)
	seriesID := repo.state.SeriesID
	oldest := repo.state.Samples[0].Sequence
	item, err := svc.ProbeKey(context.Background(), keyID)
	require.NoError(t, err)
	require.Equal(t, seriesID, repo.state.SeriesID)
	require.Equal(t, 128, item.ConfidenceDistribution.Attempted)
	require.Len(t, repo.state.Samples, 128)
	require.Equal(t, oldest+1, repo.state.Samples[0].Sequence)
	require.Len(t, upstream.requests, 129)
	accounts.account.Credentials["api_key"] = "rotated-synthetic-key"
	summary, err := svc.GetUpstreamHealthConfidence(context.Background(), keyID)
	require.NoError(t, err)
	require.Equal(t, "collecting", summary.Distribution.Status)
	require.Zero(t, summary.Distribution.Attempted)
	require.Len(t, upstream.requests, 129, "reading a changed series does not issue a probe")
}
func (*distributionServiceTestRepo) FinishConfidenceDistribution(context.Context, int64, *DistributionAttempt, DistributionSample, time.Time) (*UpstreamConfidenceDistribution, bool, error) {
	return &UpstreamConfidenceDistribution{Status: "mismatch"}, false, nil
}
func (*distributionServiceTestRepo) LoadConfidenceDistribution(context.Context, int64, time.Time) (*UpstreamConfidenceDistribution, error) {
	return nil, nil
}
func (*distributionServiceTestRepo) LoadConfidenceDistributionForSeries(context.Context, int64, ConfidenceDistributionIdentity, time.Time) (*UpstreamConfidenceDistribution, error) {
	return nil, nil
}

func distributionProbeTestContext(claimed *int, probeID string) context.Context {
	return context.WithValue(context.Background(), distributionProbeContextKey{}, distributionProbeClaim(func(context.Context, ConfidenceDistributionIdentity) (*DistributionAttempt, error) {
		*claimed = *claimed + 1
		return &DistributionAttempt{SeriesID: "test-series", LeaseToken: "test-lease", Sequence: 1, ProbeID: probeID}, nil
	}))
}

func TestDistributionProbeRequestContracts(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		for _, probeID := range []string{DistributionProbePunctuation, DistributionProbeCountry, DistributionProbeInteger} {
			t.Run(protocol+"/"+probeID, func(t *testing.T) {
				stream := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"France!\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
				if protocol == "chat_completions" {
					stream = "data: {\"choices\":[{\"delta\":{\"content\":\"France!\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
				}
				upstream := &upstreamHealthProbeHTTPStub{stream: stream}
				account := &Account{ID: 401, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://probe.example"}}
				// Use the existing endpoint capability contract to select Chat.
				if protocol == "chat_completions" {
					account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
				}
				svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
				claims := 0
				result, err := svc.RunUpstreamHealthProbe(distributionProbeTestContext(&claims, probeID), account, "ignored")
				require.NoError(t, err)
				require.Equal(t, 1, claims)
				require.Len(t, upstream.requests, 1)
				require.True(t, HTTPUpstreamRedirectsDisabled(upstream.req.Context()))
				require.Nil(t, result.ConfidenceScore)
				require.True(t, result.distributionSample.Valid)
				require.Equal(t, "france!", result.distributionSample.Answer)
				var body map[string]any
				require.NoError(t, json.Unmarshal(upstream.body, &body))
				require.Equal(t, "gpt-6.1-sol", body["model"])
				require.Equal(t, true, body["stream"])
				field := "input"
				if protocol == "responses" {
					require.Equal(t, float64(128), body["max_output_tokens"])
					require.Equal(t, false, body["store"])
					require.Equal(t, map[string]any{"effort": "low"}, body["reasoning"])
				} else {
					field = "messages"
					require.Equal(t, float64(128), body["max_tokens"])
					require.Equal(t, "low", body["reasoning_effort"])
				}
				require.Equal(t, []any{map[string]any{"role": "system", "content": "."}, map[string]any{"role": "user", "content": DistributionPrompt(probeID)}}, body[field])
				require.NotContains(t, body, "instructions")
			})
		}
	}
}

func TestDistributionProbePreflightAndFailures(t *testing.T) {
	cases := []struct {
		name, stream string
		missingKey   bool
		wantClaims   int
		wantErr      bool
	}{
		{"missing credentials", "", true, 0, true},
		{"incomplete", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"47\"}\n\ndata: [DONE]\n\n", false, 1, true},
		{"empty", "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n", false, 1, false},
		{"truncated", "data: {\"type\":\"response.incomplete\"}\n\n", false, 1, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			account := &Account{ID: 402, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://probe.example"}}
			if tc.missingKey {
				delete(account.Credentials, "api_key")
			}
			upstream := &upstreamHealthProbeHTTPStub{stream: tc.stream}
			svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
			claims := 0
			result, err := svc.RunUpstreamHealthProbe(distributionProbeTestContext(&claims, DistributionProbeInteger), account, "ignored")
			require.Equal(t, tc.wantErr, err != nil, fmt.Sprint(err))
			require.Equal(t, tc.wantClaims, claims)
			require.Len(t, upstream.requests, tc.wantClaims)
			require.False(t, result.distributionSample.Valid)
		})
	}
}

func TestDistributionProbeJSONPreservesNormalizationBoundaries(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		for _, tc := range []struct {
			name, raw, want string
			valid           bool
		}{
			{"whitespace", " New Zealand ", "new zealand", true},
			{"raw_length_limit", strings.Repeat(" ", 65536) + "47", "", false},
			{"empty", "", "", false},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				payload := map[string]any{"status": "completed", "output_text": tc.raw}
				account := &Account{ID: 403, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "synthetic", "base_url": "https://probe.example"}}
				if protocol == "chat_completions" {
					account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
					payload = map[string]any{"choices": []any{map[string]any{
						"message": map[string]any{"content": tc.raw}, "finish_reason": "stop",
					}}}
				}
				body, err := json.Marshal(payload)
				require.NoError(t, err)
				upstream := &upstreamHealthProbeHTTPStub{stream: string(body)}
				svc := &AccountTestService{httpUpstream: upstream, cfg: &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}}
				claims := 0
				result, err := svc.RunUpstreamHealthProbe(distributionProbeTestContext(&claims, DistributionProbeCountry), account, "ignored")
				require.NoError(t, err)
				require.Equal(t, 1, claims)
				require.Len(t, upstream.requests, 1)
				require.Equal(t, tc.valid, result.distributionSample.Valid)
				require.Equal(t, tc.want, result.distributionSample.Answer)
			})
		}
	}
	result := &UpstreamHealthProbeResult{distributionAttempt: &DistributionAttempt{Sequence: 1}}
	text, err := parseOpenAIUpstreamHealthJSON([]byte(`{"status":"completed","output":[{"content":[{"text":"New"},{"text":" "},{"text":"Zealand"}]}]}`), time.Now(), result)
	require.NoError(t, err)
	require.Equal(t, "New Zealand", text)
	terminal := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"content":[{"text":"New"},{"text":" "},{"text":"Zealand"}]}]}}`)
	require.Equal(t, "New Zealand", extractOpenAIHealthTerminalText(terminal, true))
	result = &UpstreamHealthProbeResult{distributionAttempt: &DistributionAttempt{Sequence: 1}}
	text, err = parseOpenAIUpstreamHealthStream(strings.NewReader("data: "+string(terminal)+"\n\n"), time.Now(), result)
	require.NoError(t, err)
	require.Equal(t, "New Zealand", text)
}
