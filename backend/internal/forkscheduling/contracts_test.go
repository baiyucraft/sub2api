package forkscheduling

import "testing"

func TestConcurrencyTargetNormalizesOnlyInvalidUpstreamTargets(t *testing.T) {
	tests := []struct {
		name   string
		target ConcurrencyTarget
		want   ConcurrencyTarget
		key    string
	}{
		{name: "account", target: ConcurrencyTarget{Kind: ConcurrencyTargetAccount, ID: 7, Limit: 2}, want: ConcurrencyTarget{Kind: ConcurrencyTargetAccount, ID: 7, Limit: 2}, key: "account:7"},
		{name: "upstream", target: ConcurrencyTarget{Kind: ConcurrencyTargetUpstream, ID: 9, Limit: 100}, want: ConcurrencyTarget{Kind: ConcurrencyTargetUpstream, ID: 9, Limit: 100}, key: "upstream:9"},
		{name: "missing upstream id falls back to account", target: ConcurrencyTarget{Kind: ConcurrencyTargetUpstream, ID: 0, Limit: 100}, want: ConcurrencyTarget{Kind: ConcurrencyTargetAccount, ID: 0, Limit: 100}, key: "account:0"},
		{name: "unknown kind falls back to account", target: ConcurrencyTarget{Kind: "unknown", ID: 11}, want: ConcurrencyTarget{Kind: ConcurrencyTargetAccount, ID: 11}, key: "account:11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.target.Normalized(); got != tt.want {
				t.Fatalf("Normalized() = %#v, want %#v", got, tt.want)
			}
			if got := tt.target.Key(); got != tt.key {
				t.Fatalf("Key() = %q, want %q", got, tt.key)
			}
		})
	}
}
