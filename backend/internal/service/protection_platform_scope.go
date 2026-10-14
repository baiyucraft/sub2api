package service

import "context"

type schedulingProtectionPlatformReader interface {
	PlatformSelected(string) bool
}

// The reader is the shared, atomically published policy snapshot. Legacy
// callers without a platform reader use the default OpenAI scope.
func schedulingProtectionPlatformSelected(reader any, platform string) bool {
	platforms, _ := reader.(schedulingProtectionPlatformReader)
	return SchedulingProtectionPlatformSelected(platforms, platform)
}

func schedulingProtectionAccountSelected(reader any, account *Account) bool {
	return account != nil && schedulingProtectionPlatformSelected(reader, account.Platform)
}

func SchedulingProtectionGroupSelected(reader any, groupID int64) bool {
	platforms, _ := reader.(schedulingProtectionPlatformReader)
	platform := PlatformOpenAI
	if groups, ok := reader.(interface{ ProtectionGroupPlatform(int64) (string, bool) }); ok {
		var known bool
		platform, known = groups.ProtectionGroupPlatform(groupID)
		if !known {
			return false
		}
	} else if groups, ok := reader.(interface {
		ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy
	}); ok {
		policy, known := groups.ProtectionPolicies()[groupID]
		if !known {
			return false
		}
		if policy.GroupPlatform != "" {
			platform = policy.GroupPlatform
		}
	}
	return SchedulingProtectionPlatformSelected(platforms, platform)
}

func (r *UpstreamCacheGuardRunner) protectionPolicyReader() GroupSchedulingProtectionPolicyResolver {
	if r == nil {
		return nil
	}
	r.policyMu.RLock()
	defer r.policyMu.RUnlock()
	return r.policyResolver
}

func (r *UpstreamCacheGuardRunner) protectionScopeSelected(groupID int64, account *Account) bool {
	reader := r.protectionPolicyReader()
	return SchedulingProtectionGroupSelected(reader, groupID) && schedulingProtectionAccountSelected(reader, account)
}

type schedulingProtectionGroupKey struct{}

// Freeze the group identity, while platform membership remains a hot read at
// each physical send. This also covers attempts without a cache usage holder.
func WithSchedulingProtectionGroup(ctx context.Context, groupID *int64) context.Context {
	if ctx == nil || groupID == nil {
		return ctx
	}
	return context.WithValue(ctx, schedulingProtectionGroupKey{}, *groupID)
}

func schedulingProtectionSendSelected(ctx context.Context, account *Account) bool {
	groupID, _ := ctx.Value(schedulingProtectionGroupKey{}).(int64)
	var reader any
	if selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); selection != nil {
		if groupID == 0 {
			groupID = selection.groupID
		}
		if selection.service != nil {
			reader = selection.service.groupProtectionPolicyResolver
		}
	}
	if attempt, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); groupID == 0 && attempt != nil && attempt.groupID != nil {
		groupID = *attempt.groupID
	}
	if reader == nil {
		reader = currentUpstreamCacheGuardRunner().protectionPolicyReader()
	}
	return schedulingProtectionAccountSelected(reader, account) && (groupID == 0 || SchedulingProtectionGroupSelected(reader, groupID))
}
