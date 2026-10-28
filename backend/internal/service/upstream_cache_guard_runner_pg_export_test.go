//go:build cacheguardpg

package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type cacheGuardPerformanceSettingRepo struct {
	*upstreamManagementSettingRepoStub
	reads int
}

func (r *cacheGuardPerformanceSettingRepo) GetValue(ctx context.Context, key string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.reads++
	return r.upstreamManagementSettingRepoStub.GetValue(ctx, key)
}

type cacheGuardPerformancePolicyResolver struct {
	snapshot *GroupSchedulingProtectionPolicySnapshot
}

func (r *cacheGuardPerformancePolicyResolver) ProtectionPoliciesSnapshot() *GroupSchedulingProtectionPolicySnapshot {
	return r.snapshot
}

func (r *cacheGuardPerformancePolicyResolver) ResolveProtection(ctx context.Context, id int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	if err := ctx.Err(); err != nil {
		return GroupSchedulingProtectionResolvedPolicy{}, err
	}
	value, ok := r.snapshot.Policy(id)
	if !ok {
		return value, errors.New("synthetic policy not found")
	}
	return value, nil
}

// CacheGuardRunnerPostgresPerformanceHandle exists only in cacheguardpg test
// binaries. It calls the real private refresh synchronously without workers,
// global runner installation, HTTP handlers, or a production API.
type CacheGuardRunnerPostgresPerformanceHandle struct {
	runner   *UpstreamCacheGuardRunner
	settings *cacheGuardPerformanceSettingRepo
}

type CacheGuardRunnerPostgresPerformanceRun struct {
	ElapsedMillis     float64 `json:"elapsed_ms"`
	SettingsReads     int     `json:"settings_reads"`
	AvailableAccounts int     `json:"available_accounts"`
	Policies          int     `json:"policies"`
	Succeeded         bool    `json:"succeeded"`
}

func NewCacheGuardRunnerPostgresPerformanceHandle(t *testing.T, store upstreamcacheguard.Store) *CacheGuardRunnerPostgresPerformanceHandle {
	t.Helper()
	settings := upstreamcacheguard.DefaultSettings()
	settings.Mode, settings.MinValidRequests = upstreamcacheguard.Protect, 5
	settings.GroupIDs = []int64{8, 21}
	encoded, err := json.Marshal(settings)
	require.NoError(t, err)
	repo := &cacheGuardPerformanceSettingRepo{upstreamManagementSettingRepoStub: &upstreamManagementSettingRepoStub{values: map[string]string{SettingKeyUpstreamCacheGuard: string(encoded)}}}
	r := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store, owner: "synthetic-runner-performance", settings: NewSettingService(repo, nil), secret: []byte("synthetic-cache-guard-performance-secret")}
	r.engine.SetOrdinaryRecoveryIdentityGeneration(strings.Repeat("a", 64))
	snapshot := &GroupSchedulingProtectionPolicySnapshot{revision: 1, globalCache: &settings,
		policies: map[int64]GroupSchedulingProtectionResolvedPolicy{}, cachePolicies: map[int64]UpstreamCacheGuardSettings{}, ordinaryRecoveryGenerations: map[string]string{}}
	for index, group := range []int64{8, 21, 42} {
		policy := settings
		policy.ThresholdPercent += float64(index)
		snapshot.cachePolicies[group] = policy
		snapshot.policies[group] = GroupSchedulingProtectionResolvedPolicy{GroupID: group, GroupPlatform: "openai", PlatformSelected: true, Cache: policy}
		snapshot.ordinaryRecoveryGenerations[upstreamcacheguard.PolicySignature(policy)] = "synthetic-policy-generation"
	}
	r.SetProtectionPolicyResolver(&cacheGuardPerformancePolicyResolver{snapshot: snapshot})
	t.Cleanup(func() { r.cancelRefresh(); r.engine.Close() })
	return &CacheGuardRunnerPostgresPerformanceHandle{runner: r, settings: repo}
}

func (h *CacheGuardRunnerPostgresPerformanceHandle) Refresh() CacheGuardRunnerPostgresPerformanceRun {
	before := h.settings.reads
	started := time.Now()
	h.runner.refresh()
	elapsed := time.Since(started)
	result := CacheGuardRunnerPostgresPerformanceRun{ElapsedMillis: float64(elapsed) / float64(time.Millisecond), SettingsReads: h.settings.reads - before,
		Succeeded: h.runner.available.Load() && !h.runner.pendingUnknown.Load() && h.runner.appliedOrdinary}
	for account := int64(100); account < 156; account++ {
		snapshot, available := h.runner.engine.CachedSharedAccount(account, account+1000)
		if available && !snapshot.LoadedAt.IsZero() {
			result.AvailableAccounts++
		}
		result.Policies += len(snapshot.Policies)
	}
	return result
}
