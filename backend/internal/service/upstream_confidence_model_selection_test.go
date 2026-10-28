package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestConfidenceModelSelectionLegacyReadAndExplicitSave(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}
	settings := NewSettingService(repo, nil)
	before := repo.values[SettingKeyUpstreamConfidenceProbe]
	old, configured, err := settings.GetUpstreamConfidenceProbeSettingsState(ctx)
	require.NoError(t, err)
	require.True(t, configured)
	require.Zero(t, old.ModelSelectionVersion)
	models := DefaultUpstreamProbeModels()
	models.OpenAI = "gpt-6-astra"
	require.Equal(t, UpstreamConfidenceDistributionClaimedModel, EffectiveUpstreamConfidenceProbeModels(models, old).OpenAI)
	require.Equal(t, before, repo.values[SettingKeyUpstreamConfidenceProbe], "reading cannot migrate the active selection")
	require.NoError(t, settings.SetUpstreamConfidenceProbeSettings(ctx, old))
	saved, _, err := settings.GetUpstreamConfidenceProbeSettingsState(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, saved.ModelSelectionVersion)
	require.Equal(t, "gpt-6-astra", EffectiveUpstreamConfidenceProbeModels(models, saved).OpenAI)
	old.Enabled = false
	require.Equal(t, "gpt-6-astra", EffectiveUpstreamConfidenceProbeModels(models, old).OpenAI)
}

func TestConfidenceModelSelectionDedicatedModelSaveOptsInAtomically(t *testing.T) {
	ctx := context.Background()
	repo := &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}
	settings := NewSettingService(repo, nil)
	upstream := NewUpstreamConfigService(nil, nil, nil)
	upstream.SetHealthProbeDependencies(nil, settings)
	models := DefaultUpstreamProbeModels()
	models.OpenAI = "gpt-6-astra"
	require.NoError(t, upstream.SetProbeModels(ctx, models))
	require.Equal(t, 1, repo.setMultipleCalls)
	require.Len(t, repo.lastMultiple, 2)
	saved, _, err := settings.GetUpstreamConfidenceProbeSettingsState(ctx)
	require.NoError(t, err)
	require.True(t, saved.Enabled)
	require.Equal(t, 1, saved.ModelSelectionVersion)
	storedModels, err := settings.GetUpstreamProbeModels(ctx)
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", EffectiveUpstreamConfidenceProbeModels(storedModels, saved).OpenAI)

	before := repo.values[SettingKeyUpstreamProbeModels]
	repo.getValueErr = errors.New("read unavailable")
	models.OpenAI = "gpt-6-luna"
	require.Error(t, upstream.SetProbeModels(ctx, models))
	require.Equal(t, 1, repo.setMultipleCalls, "selection cannot be saved without its current confidence settings")
	require.Equal(t, before, repo.values[SettingKeyUpstreamProbeModels])
}

func TestConfidenceModelSelectionUnavailableStorageRetainsTarget(t *testing.T) {
	account := distributionIdentityTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"selected-alias": "gpt-6-astra"}
	models := DefaultUpstreamProbeModels()
	models.OpenAI = "selected-alias"
	out := map[int64]UpstreamHealthConfidenceSummary{}
	upstream := NewUpstreamConfigService(nil, nil, nil)
	require.NoError(t, upstream.attachConfidenceFingerprintAttributions(context.Background(), []int64{1}, map[int64][]Account{1: {*account}}, out, time.Now().UTC(), models))
	require.NotNil(t, out[1].Attribution)
	require.Equal(t, "selected-alias", out[1].Attribution.TargetModel)
	require.Equal(t, "gpt-6-astra", out[1].Attribution.Model)
	require.Equal(t, upstreamHealthProbeProtocolOpenAI, out[1].Attribution.Protocol)
	require.Equal(t, "fingerprint_storage_unavailable", out[1].Attribution.Reason)
}

func TestConfidenceModelSelectionNoDistributionBaselineDoesNotCollect128(t *testing.T) {
	models := DefaultUpstreamProbeModels()
	models.OpenAI = "custom-unsupported-model"
	rawModels, err := json.Marshal(models)
	require.NoError(t, err)
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe: `{"enabled":true,"model_selection_version":1}`,
		SettingKeyUpstreamProbeModels:     string(rawModels),
	}}
	account := distributionIdentityTestAccount()
	account.ID, account.UpstreamKeyID = 101, int64Ptr(1)
	accounts := &confidenceBatchAccountRepo{healthProbeAccountRepo: &healthProbeAccountRepo{}, accounts: map[int64][]Account{1: {*account}}}
	repo := &confidenceBatchSummaryRepo{distributionWindowTestRepo: &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{}}}
	svc := NewUpstreamConfigService(repo, nil, accounts)
	svc.SetHealthProbeDependencies(distributionIdentityTestService(), NewSettingService(settingsRepo, nil))
	got, err := svc.GetUpstreamHealthConfidences(context.Background(), []int64{1})
	require.NoError(t, err)
	require.Nil(t, got[1].Distribution)
	require.Zero(t, repo.distributionBatchCalls)
	public := BuildConfidenceSummary(got[1].Distribution, got[1].Attribution)
	require.NotNil(t, public)
	require.NotEqual(t, "collecting", public.Status)
	require.NotContains(t, public.Methods, "distribution")

	unavailable := unavailableConfidenceDistributionSummary("responses", "confidence_read_unavailable")
	require.Equal(t, "unavailable", unavailable.Distribution.Status)
}

func TestDistributionModelSelectionUsesExactTargetAndProtocol(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", upstreamHealthProbeProtocolOpenAI, upstreamHealthProbeProtocolOpenAIChat} {
		for _, model := range []string{UpstreamConfidenceDistributionClaimedModel, "gpt-6-astra", "gpt-5.6-terra", "gpt-6-luna"} {
			require.True(t, DistributionProbeSupportsModel(PlatformOpenAI, model, protocol))
		}
	}
	for _, model := range []string{"gpt-6-sol", "vendor/gpt-6.1-sol", "gpt-6.1-sol-preview", distributionOther} {
		require.False(t, DistributionProbeSupportsModel(PlatformOpenAI, model, "responses"))
	}
	require.False(t, DistributionProbeSupportsModel(PlatformAnthropic, UpstreamConfidenceDistributionClaimedModel, "responses"))
	require.False(t, DistributionProbeSupportsModel(PlatformOpenAI, UpstreamConfidenceDistributionClaimedModel, "adaptive"))
	client, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra"}
	identity, err := client.ResolveConfidenceDistributionIdentityForModel(account, "alias")
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", identity.ClaimedModel)
	account.Credentials["model_mapping"] = map[string]any{"alias": "gpt-6-astra-nearby"}
	_, err = client.ResolveConfidenceDistributionIdentityForModel(account, "alias")
	require.Error(t, err)
}

func TestDistributionModelSelectionRequestAndClaimUseOneFrozenTarget(t *testing.T) {
	client, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"selected-alias": "gpt-6-astra"}
	upstream := &upstreamHealthProbeHTTPStub{stream: `{"status":"completed","output_text":"Canada"}`}
	client.httpUpstream = upstream
	expected, err := client.ResolveConfidenceDistributionIdentityForModel(account, "selected-alias")
	require.NoError(t, err)
	ctx := context.WithValue(context.Background(), distributionProbeModelContextKey{}, "selected-alias")
	claims := 0
	ctx = context.WithValue(ctx, distributionProbeContextKey{}, distributionProbeClaim(func(_ context.Context, identity ConfidenceDistributionIdentity) (*DistributionAttempt, error) {
		claims++
		require.Equal(t, expected, identity)
		// A concurrent account edit cannot change the already claimed payload.
		account.Credentials["model_mapping"] = map[string]any{"selected-alias": "gpt-6-luna"}
		return &DistributionAttempt{Sequence: 1, ValidSequence: 1, ProbeID: DistributionProbeCountry, ClientVersion: identity.ClientVersion}, nil
	}))
	result, err := client.RunUpstreamHealthProbe(ctx, account, "selected-alias")
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", result.Model)
	require.Equal(t, "gpt-6-astra", gjson.GetBytes(upstream.body, "model").String())
	require.Equal(t, "selected-alias", result.ConfidenceEvidence["requested_model"])
	require.Equal(t, "gpt-6-astra", result.ConfidenceEvidence["claimed_model"])
	require.Equal(t, 1, claims)
	require.Len(t, upstream.requests, 1)
}

func TestDistributionModelSelectionScoresAndPersistsClaimedTarget(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions"} {
		for _, fixture := range distributionFixtures(t, protocol) {
			if fixture.Expected.Model == distributionOther {
				continue
			}
			samples := distributionSamplesFromCounts(fixture.Counts)
			original, err := ScoreUpstreamConfidenceDistribution(protocol, samples)
			require.NoError(t, err)
			selected, err := ScoreUpstreamConfidenceDistributionForModel(protocol, fixture.Expected.Model, samples)
			require.NoError(t, err)
			require.Equal(t, "match", selected.Status)
			require.Equal(t, fixture.Expected.Model, selected.ClaimedModel)
			require.Equal(t, original.Scores, selected.Scores)
			require.Equal(t, original.Matches, selected.Matches)
		}
	}
	client, account := distributionIdentityTestService(), distributionIdentityTestAccount()
	identity, err := client.ResolveConfidenceDistributionIdentityForModel(account, "gpt-6-astra")
	require.NoError(t, err)
	state, err := NewConfidenceDistributionStateForIdentity(identity)
	require.NoError(t, err)
	raw, err := json.Marshal(state)
	require.NoError(t, err)
	var restored ConfidenceDistributionState
	require.NoError(t, json.Unmarshal(raw, &restored))
	summary, err := restored.Summary()
	require.NoError(t, err)
	require.Equal(t, "gpt-6-astra", summary.ClaimedModel)
	legacy, err := client.ResolveConfidenceDistributionIdentity(account)
	require.NoError(t, err)
	old, err := NewConfidenceDistributionStateForIdentity(legacy)
	require.NoError(t, err)
	old.ClaimedModel = "" // Existing version-3 JSON did not have this field.
	series := old.SeriesID
	resetReasons, upgrade := old.IdentityChange(legacy)
	require.Empty(t, resetReasons)
	require.True(t, upgrade)
	old.UpgradeIdentity(legacy)
	require.Equal(t, series, old.SeriesID)
	_, err = old.Claim(time.Now().UTC())
	require.NoError(t, err)
}
