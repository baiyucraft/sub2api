package service

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type upstreamManagementSettingRepoStub struct {
	values           map[string]string
	setMultipleCalls int
	lastMultiple     map[string]string
}

func (r *upstreamManagementSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *upstreamManagementSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (r *upstreamManagementSettingRepoStub) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	return nil
}

func (r *upstreamManagementSettingRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, errors.New("unexpected GetMultiple call")
}

func (r *upstreamManagementSettingRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	r.setMultipleCalls++
	r.lastMultiple = make(map[string]string, len(values))
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range values {
		r.lastMultiple[key] = value
		r.values[key] = value
	}
	return nil
}

func (r *upstreamManagementSettingRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, errors.New("unexpected GetAll call")
}

func (r *upstreamManagementSettingRepoStub) Delete(context.Context, string) error {
	return errors.New("unexpected Delete call")
}

func TestSetManagementSettingsPersistsAtomicallyAndPublishesTTFT(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)
	upstreamService := NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	settings := UpstreamManagementSettings{
		TTFTGuard:            OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 35, MinSamples: 6},
		ProbeModels:          UpstreamProbeModels{OpenAI: " gpt-custom ", Anthropic: "claude-custom", Gemini: "gemini-custom"},
		ProbeIntervalSeconds: 600,
		ProbeGuard: UpstreamProbeGuardSettings{
			Enabled: true, SuspendAfterFailures: 4, RecoverySuccesses: 2,
			CustomErrorCodesEnabled: true, CustomErrorCodes: []int{429, 404, 404},
		},
	}

	require.NoError(t, upstreamService.SetManagementSettings(context.Background(), settings))
	require.Equal(t, 1, repo.setMultipleCalls)
	require.Len(t, repo.lastMultiple, 4)
	require.JSONEq(t, `{"enabled":true,"degradation_ttft_seconds":35,"min_samples":6}`, repo.lastMultiple[SettingKeyOpenAITTFTGuardSettings])
	require.JSONEq(t, `{"openai":"gpt-custom","anthropic":"claude-custom","gemini":"gemini-custom"}`, repo.lastMultiple[SettingKeyUpstreamProbeModels])
	require.Equal(t, "600", repo.lastMultiple[SettingKeyUpstreamProbeIntervalSeconds])
	require.JSONEq(t, `{"enabled":true,"suspend_after_failures":4,"recovery_successes":2,"custom_error_codes_enabled":true,"custom_error_codes":[404,429]}`, repo.lastMultiple[SettingKeyUpstreamProbeGuardSettings])
	snapshot := settingService.OpenAITTFTGuardConfigSnapshot()
	require.True(t, snapshot.Enabled)
	require.Equal(t, 35*time.Second, snapshot.Threshold)
	require.Equal(t, 6, snapshot.MinSamples)
}

func TestSetManagementSettingsValidatesEverythingBeforeWriting(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)
	upstreamService := NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	settings := UpstreamManagementSettings{
		TTFTGuard:            OpenAITTFTGuardSettings{Enabled: true, DegradationTTFTSeconds: 20, MinSamples: 5},
		ProbeModels:          UpstreamProbeModels{OpenAI: "", Anthropic: "claude-custom", Gemini: "gemini-custom"},
		ProbeIntervalSeconds: 300,
	}

	require.Error(t, upstreamService.SetManagementSettings(context.Background(), settings))
	require.Zero(t, repo.setMultipleCalls)
	require.Empty(t, repo.values)
}

func TestUpstreamProbeIntervalDefaultsAndValidatesRange(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)

	value, err := settingService.GetUpstreamProbeIntervalSeconds(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultUpstreamProbeIntervalSeconds, value)

	for _, invalid := range []int{59, 3601} {
		err := settingService.SetOpenAITTFTGuardProbeModelsAndInterval(context.Background(), DefaultOpenAITTFTGuardSettings(), DefaultUpstreamProbeModels(), invalid)
		require.Error(t, err)
		require.Zero(t, repo.setMultipleCalls)
	}
}

type upstreamProbeCandidateRepoStub struct {
	AccountRepository
	accounts  []Account
	recent    map[string][]string
	recentErr error
}

func (r *upstreamProbeCandidateRepoStub) ListWithFiltersScoped(_ context.Context, _ pagination.PaginationParams, _, _, _, _ string, _ int64, _ string, _ AccountListScope) ([]Account, *pagination.PaginationResult, error) {
	return append([]Account(nil), r.accounts...), &pagination.PaginationResult{Page: 1, PageSize: len(r.accounts), Total: int64(len(r.accounts))}, nil
}

func (r *upstreamProbeCandidateRepoStub) ListAllWithFiltersScoped(context.Context, string, string, string, string, int64, string, AccountListScope) ([]Account, error) {
	return append([]Account(nil), r.accounts...), nil
}

func (r *upstreamProbeCandidateRepoStub) ListRecentUpstreamProbeModels(context.Context, time.Time, int) (map[string][]string, error) {
	return r.recent, r.recentErr
}

func TestGetProbeModelCandidatesCombinesDynamicConfiguredRecentAndFallback(t *testing.T) {
	settingRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamProbeModels: `{"openai":"gpt-configured","anthropic":"claude-configured","gemini":"gemini-configured"}`,
	}}
	accountRepo := &upstreamProbeCandidateRepoStub{
		accounts: []Account{
			{Platform: PlatformOpenAI, Credentials: map[string]any{
				"model_mapping":   map[string]any{"gpt-public": "gpt-upstream"},
				"model_whitelist": []any{"gpt-whitelist", "gpt-public"},
			}},
			{Platform: PlatformAnthropic, Credentials: map[string]any{"model_mapping": map[string]any{"claude-public": "claude-upstream"}}},
		},
		recent: map[string][]string{
			PlatformOpenAI: {"gpt-recent", "gpt-public"},
			PlatformGemini: {"gemini-recent"},
		},
	}
	upstreamService := NewUpstreamConfigService(nil, nil, accountRepo)
	upstreamService.SetHealthProbeDependencies(nil, NewSettingService(settingRepo, nil))

	candidates, err := upstreamService.GetProbeModelCandidates(context.Background())
	require.NoError(t, err)
	for _, model := range []string{"gpt-configured", "gpt-public", "gpt-upstream", "gpt-whitelist", "gpt-recent"} {
		require.Contains(t, candidates[PlatformOpenAI], model)
	}
	require.Contains(t, candidates[PlatformAnthropic], "claude-configured")
	require.Contains(t, candidates[PlatformAnthropic], "claude-upstream")
	require.Contains(t, candidates[PlatformGemini], "gemini-configured")
	require.Contains(t, candidates[PlatformGemini], "gemini-recent")
	require.Contains(t, candidates[PlatformOpenAI], "gpt-5.4-mini")
	for _, platform := range []string{PlatformOpenAI, PlatformAnthropic, PlatformGemini} {
		require.True(t, sort.StringsAreSorted(candidates[platform]))
		require.Equal(t, len(candidates[platform]), len(uniqueStrings(candidates[platform])))
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
