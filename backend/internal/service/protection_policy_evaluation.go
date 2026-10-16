package service

import "context"

type protectionPolicySnapshotProvider interface {
	ProtectionPoliciesSnapshot() *GroupSchedulingProtectionPolicySnapshot
}
type protectionPolicyEvaluationKey struct{}
type protectionPolicyEvaluation struct {
	provider protectionPolicySnapshotProvider
	snapshot *GroupSchedulingProtectionPolicySnapshot
}

type protectionCachePolicyEvaluationKey struct{}
type protectionPolicyVersionReader interface{ PolicyVersion() uint64 }
type protectionCachePolicyEvaluation struct {
	reader  protectionPolicyVersionReader
	version uint64
}

func withProtectionCachePolicyEvaluation(ctx context.Context, reader protectionPolicyVersionReader) context.Context {
	if _, exists := ctx.Value(protectionCachePolicyEvaluationKey{}).(protectionCachePolicyEvaluation); exists || reader == nil {
		return ctx
	}
	return context.WithValue(ctx, protectionCachePolicyEvaluationKey{}, protectionCachePolicyEvaluation{reader, reader.PolicyVersion()})
}

func withProtectionPolicyEvaluation(ctx context.Context, resolver GroupSchedulingProtectionPolicyResolver) context.Context {
	if runner := currentUpstreamCacheGuardRunner(); runner != nil && runner.engine != nil {
		ctx = withProtectionCachePolicyEvaluation(ctx, runner.engine)
	}
	if _, ok := ctx.Value(protectionPolicyEvaluationKey{}).(protectionPolicyEvaluation); ok {
		return ctx
	}
	if provider, ok := resolver.(protectionPolicySnapshotProvider); ok {
		return context.WithValue(ctx, protectionPolicyEvaluationKey{}, protectionPolicyEvaluation{provider, provider.ProtectionPoliciesSnapshot()})
	}
	return ctx
}
func protectionPolicyForEvaluation(ctx context.Context, groupID int64) (GroupSchedulingProtectionResolvedPolicy, bool) {
	if evaluation, ok := ctx.Value(protectionPolicyEvaluationKey{}).(protectionPolicyEvaluation); ok {
		return evaluation.snapshot.Policy(groupID)
	}
	return GroupSchedulingProtectionResolvedPolicy{}, false
}
func protectionPolicyEvaluationCurrent(ctx context.Context) bool {
	if cache, exists := ctx.Value(protectionCachePolicyEvaluationKey{}).(protectionCachePolicyEvaluation); exists && cache.reader.PolicyVersion() != cache.version {
		return false
	}
	evaluation, ok := ctx.Value(protectionPolicyEvaluationKey{}).(protectionPolicyEvaluation)
	return !ok || evaluation.provider.ProtectionPoliciesSnapshot() == evaluation.snapshot
}
