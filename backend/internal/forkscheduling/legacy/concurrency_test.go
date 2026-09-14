package legacy

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
)

var _ forkscheduling.TargetResolver = TargetPolicy{}

func TestResolveUpstreamSchedulerConcurrencyPreservesLegacyPrecedence(t *testing.T) {
	got := ResolveUpstreamSchedulerConcurrency(map[string]any{
		forkscheduling.UpstreamSchedulerConcurrencyOverrideKey: 37,
		"upstream_concurrency_snapshot": map[string]any{
			"status": "current", "semantics": "limited", "limit": 12,
		},
	})
	if got.Limit != 37 || got.Source != forkscheduling.UpstreamConcurrencySourceOverride || got.Override == nil || *got.Override != 37 {
		t.Fatalf("override result = %#v", got)
	}

	got = ResolveUpstreamSchedulerConcurrency(map[string]any{
		"upstream_concurrency_snapshot": map[string]any{
			"status": "current", "semantics": "unlimited", "raw_value": 0,
		},
	})
	if !got.Unlimited || got.Source != forkscheduling.UpstreamConcurrencySourceUnlimited {
		t.Fatalf("unlimited result = %#v", got)
	}

	got = ResolveUpstreamSchedulerConcurrency(map[string]any{
		"upstream_concurrency_snapshot": map[string]any{
			"status": "stale", "semantics": "limited", "limit": 12,
		},
	})
	if !got.UsesDefault || got.Limit != forkscheduling.DefaultUpstreamSchedulerConcurrency {
		t.Fatalf("fallback result = %#v", got)
	}
}

func TestResolveConcurrencyTargetPreservesAccountAndSharedUpstreamRules(t *testing.T) {
	got := ResolveConcurrencyTarget(forkscheduling.ConcurrencyAccountView{AccountID: 7, AccountLimit: 0})
	if got.Kind != forkscheduling.ConcurrencyTargetAccount || got.ID != 7 || got.Limit != 1 {
		t.Fatalf("account target = %#v", got)
	}

	got = ResolveConcurrencyTarget(forkscheduling.ConcurrencyAccountView{AccountID: 7, UpstreamConfigID: 9, UpstreamLimit: 42})
	if got.Kind != forkscheduling.ConcurrencyTargetUpstream || got.ID != 9 || got.Limit != 42 {
		t.Fatalf("shared target = %#v", got)
	}

	got = ResolveConcurrencyTarget(forkscheduling.ConcurrencyAccountView{AccountID: 7, UpstreamConfigID: 9, UpstreamUnlimited: true, UpstreamLimit: 42})
	if got.Kind != forkscheduling.ConcurrencyTargetUpstream || got.ID != 9 || got.Limit != 0 {
		t.Fatalf("unlimited target = %#v", got)
	}
}
