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
	getValueErr      error
	setMultipleCalls int
	lastMultiple     map[string]string
}

func (r *upstreamManagementSettingRepoStub) Get(context.Context, string) (*Setting, error) {
	return nil, ErrSettingNotFound
}

func (r *upstreamManagementSettingRepoStub) GetValue(_ context.Context, key string) (string, error) {
	if r.getValueErr != nil {
		return "", r.getValueErr
	}
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

func (r *upstreamManagementSettingRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
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
		ModelAliasRules: map[string]string{" gpt-5.6-luna ": " gpt-5.6-terra "},
	}

	require.NoError(t, upstreamService.SetManagementSettings(context.Background(), settings))
	require.Equal(t, 2, repo.setMultipleCalls)
	require.JSONEq(t, `{"enabled":true,"degradation_ttft_seconds":35,"min_samples":6}`, repo.values[SettingKeyOpenAITTFTGuardSettings])
	require.JSONEq(t, `{"openai":"gpt-custom","anthropic":"claude-custom","gemini":"gemini-custom"}`, repo.values[SettingKeyUpstreamProbeModels])
	require.Equal(t, "600", repo.values[SettingKeyUpstreamProbeIntervalSeconds])
	require.JSONEq(t, `{"enabled":true,"suspend_after_failures":4,"recovery_successes":2,"custom_error_codes_enabled":true,"custom_error_codes":[404,429]}`, repo.values[SettingKeyUpstreamProbeGuardSettings])
	require.JSONEq(t, `{"gpt-5.6-luna":"gpt-5.6-terra"}`, repo.values[SettingKeyUpstreamModelAliasRules])
	require.Equal(t, "60", repo.values[SettingKeySessionSwitchWindowSeconds])
	require.Equal(t, "3", repo.values[SettingKeySessionSwitchFailureThreshold])
	require.Equal(t, "300", repo.values[SettingKeySessionSwitchCooldownSeconds])
	require.JSONEq(t, `[502,503]`, repo.values[SettingKeySessionSwitchStatusCodes])
	snapshot := settingService.OpenAITTFTGuardConfigSnapshot()
	require.True(t, snapshot.Enabled)
	require.Equal(t, 35*time.Second, snapshot.Threshold)
	require.Equal(t, 6, snapshot.MinSamples)
}

func TestGetSessionSwitchSettingsDefaultsAndNormalizes(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   SessionSwitchSettings
	}{
		{
			name:   "all settings missing",
			values: map[string]string{},
			want:   DefaultSessionSwitchSettings(),
		},
		{
			name: "missing values fall back independently",
			values: map[string]string{
				SettingKeySessionSwitchWindowSeconds: "120",
			},
			want: SessionSwitchSettings{
				WindowSeconds: 120, FailureThreshold: 3, CooldownSeconds: 300, StatusCodes: []int{502, 503},
			},
		},
		{
			name: "status codes are sorted and deduplicated",
			values: map[string]string{
				SettingKeySessionSwitchWindowSeconds:    "90",
				SettingKeySessionSwitchFailureThreshold: "4",
				SettingKeySessionSwitchCooldownSeconds:  "600",
				SettingKeySessionSwitchStatusCodes:      `[503,502,503]`,
			},
			want: SessionSwitchSettings{
				WindowSeconds: 90, FailureThreshold: 4, CooldownSeconds: 600, StatusCodes: []int{502, 503},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settingService := NewSettingService(&upstreamManagementSettingRepoStub{values: tt.values}, nil)
			got, err := settingService.GetSessionSwitchSettings(context.Background())
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestGetSessionSwitchSettingsRejectsMissingStore(t *testing.T) {
	_, err := NewSettingService(nil, nil).GetSessionSwitchSettings(context.Background())
	require.Error(t, err)
}

func TestSetManagementSettingsPersistsNormalizedSessionSwitchSettings(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)
	upstreamService := NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	settings := validUpstreamManagementSettings()
	settings.SessionSwitchWindowSeconds = 120
	settings.SessionSwitchFailureThreshold = 5
	settings.SessionSwitchCooldownSeconds = 900
	settings.SessionSwitchStatusCodes = []int{503, 502, 503}

	require.NoError(t, upstreamService.SetManagementSettings(context.Background(), settings))
	require.Equal(t, "120", repo.values[SettingKeySessionSwitchWindowSeconds])
	require.Equal(t, "5", repo.values[SettingKeySessionSwitchFailureThreshold])
	require.Equal(t, "900", repo.values[SettingKeySessionSwitchCooldownSeconds])
	require.JSONEq(t, `[502,503]`, repo.values[SettingKeySessionSwitchStatusCodes])

}

func TestSetManagementSettingsRejectsInvalidSessionSwitchSettingsBeforeWrite(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*UpstreamManagementSettings)
	}{
		{name: "window below minimum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchWindowSeconds = 9 }},
		{name: "window above maximum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchWindowSeconds = 3601 }},
		{name: "threshold below minimum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchFailureThreshold = 0 }},
		{name: "threshold above maximum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchFailureThreshold = 21 }},
		{name: "cooldown below minimum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchCooldownSeconds = 9 }},
		{name: "cooldown above maximum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchCooldownSeconds = 3601 }},
		{name: "status below minimum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchStatusCodes = []int{99} }},
		{name: "status above maximum", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchStatusCodes = []int{600} }},
		{name: "status list empty", mutate: func(settings *UpstreamManagementSettings) { settings.SessionSwitchStatusCodes = []int{} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
			upstreamService := NewUpstreamConfigService(nil, nil, nil)
			upstreamService.SetHealthProbeDependencies(nil, NewSettingService(repo, nil))
			settings := validUpstreamManagementSettings()
			settings.SessionSwitchWindowSeconds = 60
			settings.SessionSwitchFailureThreshold = 3
			settings.SessionSwitchCooldownSeconds = 300
			settings.SessionSwitchStatusCodes = []int{502, 503}
			tt.mutate(&settings)

			require.Error(t, upstreamService.SetManagementSettings(context.Background(), settings))
			require.Zero(t, repo.setMultipleCalls)
			require.Empty(t, repo.values)
		})
	}
}

func TestGetSessionSwitchSettingsRejectsInvalidStoredValues(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "invalid integer", values: map[string]string{SettingKeySessionSwitchWindowSeconds: "not-a-number"}},
		{name: "window out of range", values: map[string]string{SettingKeySessionSwitchWindowSeconds: "9"}},
		{name: "invalid status JSON", values: map[string]string{SettingKeySessionSwitchStatusCodes: `{`}},
		{name: "status out of range", values: map[string]string{SettingKeySessionSwitchStatusCodes: `[600]`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settingService := NewSettingService(&upstreamManagementSettingRepoStub{values: tt.values}, nil)
			_, err := settingService.GetSessionSwitchSettings(context.Background())
			require.Error(t, err)
		})
	}
}

func TestManagementSettingsRejectsInvalidModelAliasRulesBeforeWrite(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)
	upstreamService := NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	settings := UpstreamManagementSettings{
		TTFTGuard:   OpenAITTFTGuardSettings{Enabled: false, DegradationTTFTSeconds: 20, MinSamples: 5},
		ProbeModels: DefaultUpstreamProbeModels(), ProbeIntervalSeconds: 300,
		ModelAliasRules: map[string]string{" ": "gpt-5"},
	}
	require.Error(t, upstreamService.SetManagementSettings(context.Background(), settings))
	require.Zero(t, repo.setMultipleCalls)
}

func TestGetManagementSettingsDefaultsMissingModelAliasesToEmpty(t *testing.T) {
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	settingService := NewSettingService(repo, nil)
	upstreamService := NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	got, err := upstreamService.GetManagementSettings(context.Background())
	require.NoError(t, err)
	require.Empty(t, got.ModelAliasRules)
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

type upstreamRetryStatusCodesRepoStub struct {
	AccountRepository
	accounts []Account
	updated  map[int64]map[string]any
	errors   map[int64]error
}

func (r *upstreamRetryStatusCodesRepoStub) ListAllWithFiltersScoped(_ context.Context, _ string, _ string, _ string, _ string, _ int64, _ string, scope AccountListScope) ([]Account, error) {
	if scope != AccountListScopeUpstream {
		return nil, errors.New("unexpected account scope")
	}
	return append([]Account(nil), r.accounts...), nil
}

func (r *upstreamRetryStatusCodesRepoStub) ListWithFiltersScoped(_ context.Context, _ pagination.PaginationParams, _ string, _ string, _ string, _ string, _ int64, _ string, scope AccountListScope) ([]Account, *pagination.PaginationResult, error) {
	accounts, err := r.ListAllWithFiltersScoped(context.Background(), "", "", "", "", 0, "", scope)
	return accounts, &pagination.PaginationResult{Page: 1, PageSize: len(accounts), Total: int64(len(accounts))}, err
}

func (r *upstreamRetryStatusCodesRepoStub) UpdateCredentials(_ context.Context, id int64, credentials map[string]any) error {
	if err := r.errors[id]; err != nil {
		return err
	}
	if r.updated == nil {
		r.updated = make(map[int64]map[string]any)
	}
	cloned := make(map[string]any, len(credentials))
	for key, value := range credentials {
		cloned[key] = value
	}
	r.updated[id] = cloned
	return nil
}

func TestSetManagementSettingsWithRetryStatusCodesUsesOneGlobalPolicy(t *testing.T) {
	t.Cleanup(ResetGlobalPoolModeRetryStatusCodesForTest)
	settingRepo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	accountRepo := &upstreamRetryStatusCodesRepoStub{
		accounts: []Account{
			{ID: 11, Credentials: map[string]any{"api_key": "upstream-1"}},
			{ID: 12, Credentials: map[string]any{"api_key": "upstream-2", "pool_mode_retry_status_codes": []any{401}}},
		},
	}
	// The scoped repository contract guarantees these are upstream-bound rows;
	// ordinary accounts are intentionally not returned by the stub.
	service := NewUpstreamConfigService(nil, nil, accountRepo)
	service.SetHealthProbeDependencies(nil, NewSettingService(settingRepo, nil))
	codes := []int{429, 502, 429}
	err := service.SetManagementSettingsWithRetryStatusCodes(context.Background(), validUpstreamManagementSettings(), &codes)
	require.NoError(t, err)
	require.Empty(t, accountRepo.updated)
	require.JSONEq(t, `[429,502]`, settingRepo.values[SettingKeyUpstreamPoolModeRetryStatusCodes])
	upstreamConfigID := int64(1)
	upstreamKeyID := int64(2)
	upstream := &Account{UpstreamConfigID: &upstreamConfigID, UpstreamKeyID: &upstreamKeyID, Credentials: map[string]any{"pool_mode_retry_status_codes": []any{401}}}
	require.True(t, upstream.IsPoolModeRetryableStatus(502))
	require.False(t, upstream.IsPoolModeRetryableStatus(401))
}

func TestSetManagementSettingsWithRetryStatusCodesEmptyRestoresDefaults(t *testing.T) {
	t.Cleanup(ResetGlobalPoolModeRetryStatusCodesForTest)
	settingRepo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	accountRepo := &upstreamRetryStatusCodesRepoStub{accounts: []Account{{ID: 21, Credentials: map[string]any{
		"api_key": "upstream", "pool_mode_retry_status_codes": []any{502},
	}}}}
	service := NewUpstreamConfigService(nil, nil, accountRepo)
	service.SetHealthProbeDependencies(nil, NewSettingService(settingRepo, nil))
	codes := []int{}
	err := service.SetManagementSettingsWithRetryStatusCodes(context.Background(), validUpstreamManagementSettings(), &codes)
	require.NoError(t, err)
	require.Empty(t, accountRepo.updated)
	require.JSONEq(t, `[]`, settingRepo.values[SettingKeyUpstreamPoolModeRetryStatusCodes])
	got, err := service.GetManagementSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{401, 403, 429}, got.PoolModeRetryStatusCodes)
}

func TestWarmUpstreamPoolModeRetryStatusCodesAppliesToNewAccounts(t *testing.T) {
	t.Cleanup(ResetGlobalPoolModeRetryStatusCodesForTest)
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamPoolModeRetryStatusCodes: `[502, 502]`,
	}}
	settingService := NewSettingService(repo, nil)
	settingService.WarmUpstreamPoolModeRetryStatusCodes(context.Background())
	configID, keyID := int64(1), int64(2)
	newAccount := &Account{
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
		Credentials:      map[string]any{"pool_mode_retry_status_codes": []any{401}},
	}
	require.True(t, newAccount.IsPoolModeRetryableStatus(502))
	require.False(t, newAccount.IsPoolModeRetryableStatus(401))
}

func TestSetManagementSettingsWithRetryStatusCodesRejectsInvalidCodesBeforeWrite(t *testing.T) {
	settingRepo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	accountRepo := &upstreamRetryStatusCodesRepoStub{accounts: []Account{{ID: 31}}}
	service := NewUpstreamConfigService(nil, nil, accountRepo)
	service.SetHealthProbeDependencies(nil, NewSettingService(settingRepo, nil))
	for _, codes := range [][]int{{99}, {600}, {401, 401, 0}} {
		err := service.SetManagementSettingsWithRetryStatusCodes(context.Background(), validUpstreamManagementSettings(), &codes)
		require.Error(t, err)
	}
	require.Empty(t, accountRepo.updated)
	require.Empty(t, settingRepo.values)
}

func TestGetManagementSettingsLoadsRetryStatusCodesWithDefaultFallback(t *testing.T) {
	settingRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamPoolModeRetryStatusCodes: `[503,401,503]`,
	}}
	service := NewUpstreamConfigService(nil, nil, nil)
	service.SetHealthProbeDependencies(nil, NewSettingService(settingRepo, nil))
	got, err := service.GetManagementSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, []int{401, 503}, got.PoolModeRetryStatusCodes)
}

func validUpstreamManagementSettings() UpstreamManagementSettings {
	return UpstreamManagementSettings{
		TTFTGuard:   OpenAITTFTGuardSettings{Enabled: false, DegradationTTFTSeconds: 20, MinSamples: 5},
		ProbeModels: DefaultUpstreamProbeModels(), ProbeIntervalSeconds: 300,
		ProbeGuard: DefaultUpstreamProbeGuardSettings(), ModelAliasRules: map[string]string{},
		ConfidenceProbe: DefaultUpstreamConfidenceProbeSettings(),
	}
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
			{Platform: PlatformKimi, Credentials: map[string]any{"model_mapping": map[string]any{"kimi-public": "kimi-upstream"}}},
			{Platform: PlatformZhipu, Credentials: map[string]any{"model_whitelist": []any{"glm-custom"}}},
			{Platform: PlatformDeepseek, Credentials: map[string]any{"model_whitelist": []any{"deepseek-custom"}}},
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
	require.Contains(t, candidates[PlatformKimi], "kimi-upstream")
	require.Contains(t, candidates[PlatformZhipu], "glm-custom")
	require.Contains(t, candidates[PlatformDeepseek], "deepseek-custom")
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
