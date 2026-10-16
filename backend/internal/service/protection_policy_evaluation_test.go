package service

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type evaluationPolicyProvider struct {
	current atomic.Pointer[GroupSchedulingProtectionPolicySnapshot]
}

type evaluationCachePolicyVersion struct{ version atomic.Uint64 }

func (r *evaluationCachePolicyVersion) PolicyVersion() uint64 { return r.version.Load() }

func TestProtectionPolicyEvaluationRejectsCacheEpochChange(t *testing.T) {
	cache := &evaluationCachePolicyVersion{}
	cache.version.Store(1)
	ctx := withProtectionCachePolicyEvaluation(context.Background(), cache)
	require.True(t, protectionPolicyEvaluationCurrent(ctx))
	cache.version.Store(2)
	require.False(t, protectionPolicyEvaluationCurrent(ctx))
	require.Same(t, ctx, withProtectionCachePolicyEvaluation(ctx, cache))
}

func (p *evaluationPolicyProvider) ProtectionPoliciesSnapshot() *GroupSchedulingProtectionPolicySnapshot {
	return p.current.Load()
}
func (p *evaluationPolicyProvider) ResolveProtection(_ context.Context, groupID int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	policy, _ := p.current.Load().Policy(groupID)
	return policy, nil
}

func TestProtectionPolicyEvaluationRetainsRevisionAndRejectsChangedDecision(t *testing.T) {
	provider := &evaluationPolicyProvider{}
	initial := &GroupSchedulingProtectionPolicySnapshot{revision: 1, policies: map[int64]GroupSchedulingProtectionResolvedPolicy{7: {GroupID: 7, Priority: SchedulingPriorityCacheFirst}}}
	provider.current.Store(initial)
	ctx := withProtectionPolicyEvaluation(context.Background(), provider)
	require.True(t, protectionPolicyEvaluationCurrent(ctx))
	provider.current.Store(&GroupSchedulingProtectionPolicySnapshot{revision: 2, policies: map[int64]GroupSchedulingProtectionResolvedPolicy{7: {GroupID: 7, Priority: SchedulingPriorityTTFTFirst}}})
	policy, found := protectionPolicyForEvaluation(ctx, 7)
	require.True(t, found)
	require.Equal(t, SchedulingPriorityCacheFirst, policy.Priority)
	require.False(t, protectionPolicyEvaluationCurrent(ctx))
	require.Same(t, ctx, withProtectionPolicyEvaluation(ctx, provider), "nested decisions must not mix revisions")
}

func TestGatewayProtectionPolicyChangeReleasesTentativeCapacity(t *testing.T) {
	provider := &evaluationPolicyProvider{}
	provider.current.Store(&GroupSchedulingProtectionPolicySnapshot{revision: 1})
	ctx := withProtectionPolicyEvaluation(context.Background(), provider)
	released := 0
	attempted := 0
	p := cacheGuardPreference{Active: true, Tiers: [][]int64{{1}, {2}}, ScopeIDs: map[int64]struct{}{1: {}, 2: {}}}
	result, err, handled := selectGatewayCacheGuardTiers(ctx, p, nil, func(context.Context, map[int64]struct{}) (*AccountSelectionResult, error) {
		attempted++
		provider.current.Store(&GroupSchedulingProtectionPolicySnapshot{revision: 2})
		return &AccountSelectionResult{Account: &Account{ID: 1}, Acquired: true, ReleaseFunc: func() { released++ }}, nil
	})
	require.NoError(t, err)
	require.Nil(t, result)
	require.False(t, handled, "a changed policy returns control to ordinary scheduling")
	require.Equal(t, 1, released)
	require.Equal(t, 1, attempted)
}
