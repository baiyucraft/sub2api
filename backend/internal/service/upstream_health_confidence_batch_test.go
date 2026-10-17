package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type confidenceBatchSettingsRepo struct {
	*upstreamManagementSettingRepoStub
	reads map[string]int
}

func (r *confidenceBatchSettingsRepo) GetValue(ctx context.Context, key string) (string, error) {
	r.reads[key]++
	return r.upstreamManagementSettingRepoStub.GetValue(ctx, key)
}

type confidenceBatchAccountRepo struct {
	*healthProbeAccountRepo
	accounts    map[int64][]Account
	batchCalls  int
	singleCalls int
}

func (r *confidenceBatchAccountRepo) ListByUpstreamKeyID(_ context.Context, id int64) ([]Account, error) {
	r.singleCalls++
	return r.accounts[id], nil
}

func (r *confidenceBatchAccountRepo) ListByUpstreamKeyIDs(_ context.Context, ids []int64) ([]Account, error) {
	r.batchCalls++
	var accounts []Account
	for _, id := range ids {
		accounts = append(accounts, r.accounts[id]...)
	}
	return accounts, nil
}

type confidenceBatchSummaryRepo struct {
	*distributionWindowTestRepo
	legacy                     UpstreamHealthConfidenceSummary
	legacyErr                  error
	evaluatedAt                time.Time
	distributionAt             time.Time
	results                    map[int64]ConfidenceDistributionReadResult
	batchCalls, singleCalls    int
	distributionBatchCalls     int
	distributionSingleCalls    int
	lastIDs                    []int64
	lastDistributionIdentities map[int64]ConfidenceDistributionIdentity
}

func (r *confidenceBatchSummaryRepo) GetUpstreamHealthConfidence(context.Context, int64) (UpstreamHealthConfidenceSummary, error) {
	r.singleCalls++
	return r.legacy, r.legacyErr
}

func (r *confidenceBatchSummaryRepo) GetUpstreamHealthConfidences(_ context.Context, ids []int64) (map[int64]UpstreamHealthConfidenceSummary, error) {
	r.batchCalls++
	r.lastIDs = append([]int64(nil), ids...)
	out := map[int64]UpstreamHealthConfidenceSummary{}
	for _, id := range ids {
		out[id] = r.legacy
	}
	return out, r.legacyErr
}

func (r *confidenceBatchSummaryRepo) GetUpstreamHealthConfidencesAt(ctx context.Context, ids []int64, now time.Time) (map[int64]UpstreamHealthConfidenceSummary, error) {
	r.evaluatedAt = now
	return r.GetUpstreamHealthConfidences(ctx, ids)
}

func TestUpstreamHealthConfidencesIndependentReadIgnoresLegacyFailureAndUsesFixedClock(t *testing.T) {
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":true}`}}
	account := distributionIdentityTestAccount()
	account.ID, account.UpstreamKeyID = 101, int64Ptr(1)
	legacyAccount := *account
	legacyAccount.ID, legacyAccount.UpstreamKeyID, legacyAccount.Platform = 102, int64Ptr(2), PlatformAnthropic
	accounts := &confidenceBatchAccountRepo{healthProbeAccountRepo: &healthProbeAccountRepo{}, accounts: map[int64][]Account{1: {*account}, 2: {legacyAccount}}}
	repo := &confidenceBatchSummaryRepo{distributionWindowTestRepo: &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{}},
		legacyErr: errors.New("legacy read failed"), results: map[int64]ConfidenceDistributionReadResult{1: {Distribution: &UpstreamConfidenceDistribution{Status: "match", Attempted: 128}}}}
	svc := NewUpstreamConfigService(repo, nil, accounts)
	svc.SetHealthProbeDependencies(distributionIdentityTestService(), NewSettingService(settingsRepo, nil))
	now := time.Date(2026, 10, 17, 12, 0, 0, 123000, time.FixedZone("request", 3600))
	got, err := svc.GetUpstreamHealthConfidencesAt(context.Background(), []int64{1, 2}, now)
	require.NoError(t, err)
	require.Equal(t, "match", got[1].Status)
	require.Equal(t, "confidence_read_unavailable", got[2].Distribution.Reasons[0])
	require.Equal(t, []int64{2}, repo.lastIDs)
	require.Equal(t, now.UTC(), repo.evaluatedAt)
	require.Equal(t, now.UTC(), repo.distributionAt)
	repo.batchCalls = 0
	_, err = svc.GetUpstreamHealthConfidencesAt(context.Background(), []int64{1}, now)
	require.NoError(t, err)
	require.Zero(t, repo.batchCalls, "an all-distribution page performs no legacy read")
	single, err := svc.GetUpstreamHealthConfidence(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, "match", single.Status, "single-key display also ignores irrelevant legacy errors")
	require.Zero(t, repo.batchCalls)
}

func (r *confidenceBatchSummaryRepo) LoadConfidenceDistributionForSeries(_ context.Context, id int64, _ ConfidenceDistributionIdentity, _ time.Time) (*UpstreamConfidenceDistribution, error) {
	r.distributionSingleCalls++
	result := r.results[id]
	return result.Distribution, result.Err
}

func (r *confidenceBatchSummaryRepo) LoadConfidenceDistributionsForSeries(_ context.Context, identities map[int64]ConfidenceDistributionIdentity, now time.Time) (map[int64]ConfidenceDistributionReadResult, error) {
	r.distributionBatchCalls++
	r.distributionAt = now
	r.lastDistributionIdentities = identities
	return r.results, nil
}

func TestUpstreamHealthConfidencesBatchEquivalentAndReadsConfigurationOnce(t *testing.T) {
	settingsRepo := &confidenceBatchSettingsRepo{upstreamManagementSettingRepoStub: &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe:        `{"enabled":true}`,
		SettingKeyOpenAICodexClientVersionSynced: "0.130.0",
	}}, reads: map[string]int{}}
	settings := NewSettingService(settingsRepo, nil)
	accountsRepo := &confidenceBatchAccountRepo{healthProbeAccountRepo: &healthProbeAccountRepo{}, accounts: map[int64][]Account{}}
	for id := int64(1); id <= 7; id++ {
		account := distributionIdentityTestAccount()
		account.ID, account.UpstreamKeyID = id+100, int64Ptr(id)
		if id == 3 {
			delete(account.Credentials, "api_key")
		}
		if id == 4 {
			account.Extra = map[string]any{"openai_responses_mode": "force_chat_completions"}
		}
		if id == 5 {
			account.Platform = PlatformAnthropic
		}
		accountsRepo.accounts[id] = []Account{*account}
	}
	duplicate := accountsRepo.accounts[2][0]
	duplicate.ID += 100
	accountsRepo.accounts[2] = append(accountsRepo.accounts[2], duplicate)
	oldScore := 100.0
	repo := &confidenceBatchSummaryRepo{distributionWindowTestRepo: &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{}},
		legacy: UpstreamHealthConfidenceSummary{Score7d: &oldScore, Status: "current_success", PromptVersion: UpstreamConfidencePromptVersion},
		results: map[int64]ConfidenceDistributionReadResult{
			1: {Distribution: &UpstreamConfidenceDistribution{Status: "match", Attempted: 128}},
			4: {Distribution: &UpstreamConfidenceDistribution{Status: "insufficient", Attempted: 12}},
			6: {Err: errors.New("synthetic-read-error")},
		}}
	svc := NewUpstreamConfigService(repo, nil, accountsRepo)
	client := distributionIdentityTestService()
	client.settingService = settings
	svc.SetHealthProbeDependencies(client, settings)
	ids := []int64{8, 7, 6, 5, 4, 3, 2, 1, 1, 0, -1}
	got, err := svc.GetUpstreamHealthConfidences(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, got, 8)
	require.Equal(t, []int64{5}, repo.lastIDs, "distribution keys never read legacy evidence")
	require.Equal(t, 1, repo.batchCalls)
	require.Zero(t, repo.singleCalls)
	require.Equal(t, 1, accountsRepo.batchCalls)
	require.Zero(t, accountsRepo.singleCalls)
	require.Equal(t, 1, repo.distributionBatchCalls)
	require.Zero(t, repo.distributionSingleCalls)
	require.Equal(t, map[string]int{SettingKeyUpstreamConfidenceProbe: 1, SettingKeyOpenAICodexClientVersion: 1, SettingKeyOpenAICodexClientVersionSynced: 1}, settingsRepo.reads)
	require.Len(t, repo.lastDistributionIdentities, 4)
	require.Equal(t, "chat_completions", repo.lastDistributionIdentities[4].Protocol)
	for id := int64(1); id <= 8; id++ {
		want, err := svc.GetUpstreamHealthConfidence(context.Background(), id)
		require.NoError(t, err)
		require.Equal(t, want, got[id], "batch/single key %d", id)
	}
	for _, id := range []int64{1, 2, 3, 4, 6, 7, 8} {
		require.Nil(t, got[id].Score7d, "independent mode must not fall back to the old score")
	}
	require.Equal(t, &oldScore, got[5].Score7d, "ordinary platform retains existing behavior")
}

func TestUpstreamHealthConfidencesBatchDisabledModeAndEmptyInput(t *testing.T) {
	repo := &confidenceBatchSummaryRepo{distributionWindowTestRepo: &distributionWindowTestRepo{healthProbeLockRepo: &healthProbeLockRepo{}}, legacy: UpstreamHealthConfidenceSummary{Status: "data_insufficient"}}
	settingsRepo := &confidenceBatchSettingsRepo{upstreamManagementSettingRepoStub: &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamConfidenceProbe: `{"enabled":false}`}}, reads: map[string]int{}}
	accounts := &confidenceBatchAccountRepo{healthProbeAccountRepo: &healthProbeAccountRepo{}}
	svc := NewUpstreamConfigService(repo, nil, accounts)
	svc.SetHealthProbeDependencies(nil, NewSettingService(settingsRepo, nil))
	got, err := svc.GetUpstreamHealthConfidences(context.Background(), []int64{1, 2, 1})
	require.NoError(t, err)
	require.Equal(t, map[int64]UpstreamHealthConfidenceSummary{1: repo.legacy, 2: repo.legacy}, got)
	require.Equal(t, 1, settingsRepo.reads[SettingKeyUpstreamConfidenceProbe])
	require.Zero(t, accounts.batchCalls)
	require.Zero(t, repo.distributionBatchCalls)
	got, err = svc.GetUpstreamHealthConfidences(context.Background(), nil)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Equal(t, 1, repo.batchCalls)
	require.Equal(t, 1, settingsRepo.reads[SettingKeyUpstreamConfidenceProbe])
}

func TestDistributionIdentityBatchPolicyFailuresAndOverridesRemainIsolated(t *testing.T) {
	settingsRepo := &confidenceBatchSettingsRepo{upstreamManagementSettingRepoStub: &upstreamManagementSettingRepoStub{getValueErr: errors.New("synthetic-policy-error")}, reads: map[string]int{}}
	client := distributionIdentityTestService()
	client.settingService = NewSettingService(settingsRepo, nil)
	first, second, overridden := distributionIdentityTestAccount(), distributionIdentityTestAccount(), distributionIdentityTestAccount()
	second.ID++
	overridden.ID += 2
	overridden.Credentials["header_override_enabled"] = true
	overridden.Credentials["header_overrides"] = map[string]any{"User-Agent": "synthetic-fixed-agent", "version": "0.130.0"}
	got := client.ResolveConfidenceDistributionIdentities(context.Background(), []*Account{first, second, overridden})
	require.Error(t, got[first.ID].Err)
	require.Error(t, got[second.ID].Err)
	require.NoError(t, got[overridden.ID].Err)
	require.Equal(t, 1, settingsRepo.reads[SettingKeyOpenAICodexClientVersion])
	want, err := client.ResolveConfidenceDistributionIdentity(overridden)
	require.NoError(t, err)
	require.Equal(t, want, got[overridden.ID].Identity)
}
