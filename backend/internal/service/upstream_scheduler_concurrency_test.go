//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveUpstreamSchedulerConcurrency(t *testing.T) {
	tests := []struct {
		name          string
		extra         map[string]any
		wantLimit     int
		wantSource    string
		wantDefault   bool
		wantUnlimited bool
		wantOverride  *int
	}{
		{
			name: "manual override wins",
			extra: map[string]any{
				UpstreamSchedulerConcurrencyOverrideKey: 37,
				upstreamConcurrencySnapshotKey: map[string]any{
					"status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsLimited, "limit": 12,
				},
			},
			wantLimit: 37, wantSource: UpstreamConcurrencySourceOverride, wantOverride: intPointer(37),
		},
		{
			name: "limited provider snapshot",
			extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
				"status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsLimited, "limit": "41",
			}},
			wantLimit: 41, wantSource: UpstreamConcurrencySourceProvider,
		},
		{
			name: "positive provider defined snapshot",
			extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
				"status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsProviderDefined, "raw_value": 52,
			}},
			wantLimit: 52, wantSource: UpstreamConcurrencySourceProvider,
		},
		{
			name: "explicit unlimited snapshot",
			extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
				"status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsUnlimited, "raw_value": 0,
			}},
			wantSource: UpstreamConcurrencySourceUnlimited, wantUnlimited: true,
		},
		{
			name: "stale snapshot defaults",
			extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
				"status": upstreamConcurrencyStatusStale, "semantics": upstreamConcurrencySemanticsLimited, "limit": 9,
			}},
			wantLimit: DefaultUpstreamSchedulerConcurrency, wantSource: UpstreamConcurrencySourceDefault, wantDefault: true,
		},
		{
			name: "unsupported snapshot defaults",
			extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
				"status": upstreamConcurrencyStatusUnsupported, "semantics": upstreamConcurrencySemanticsUnknown,
			}},
			wantLimit: DefaultUpstreamSchedulerConcurrency, wantSource: UpstreamConcurrencySourceDefault, wantDefault: true,
		},
		{
			name: "invalid override and snapshot default",
			extra: map[string]any{
				UpstreamSchedulerConcurrencyOverrideKey: MaxUpstreamSchedulerConcurrency + 1,
				upstreamConcurrencySnapshotKey: map[string]any{
					"status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsLimited, "limit": -1,
				},
			},
			wantLimit: DefaultUpstreamSchedulerConcurrency, wantSource: UpstreamConcurrencySourceDefault, wantDefault: true,
		},
		{
			name:      "maximum override accepted",
			extra:     map[string]any{UpstreamSchedulerConcurrencyOverrideKey: MaxUpstreamSchedulerConcurrency},
			wantLimit: MaxUpstreamSchedulerConcurrency, wantSource: UpstreamConcurrencySourceOverride, wantOverride: intPointer(MaxUpstreamSchedulerConcurrency),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveUpstreamSchedulerConcurrency(tt.extra)
			require.Equal(t, tt.wantLimit, got.Limit)
			require.Equal(t, tt.wantSource, got.Source)
			require.Equal(t, tt.wantDefault, got.UsesDefault)
			require.Equal(t, tt.wantUnlimited, got.Unlimited)
			require.Equal(t, tt.wantOverride, got.Override)
		})
	}
}

type targetConcurrencyCacheForTest struct {
	stubConcurrencyCacheForTest
	acquiredTargets []ConcurrencyTarget
	releasedTargets []ConcurrencyTarget
}

func (c *targetConcurrencyCacheForTest) AcquireConcurrencyTargetSlot(_ context.Context, target ConcurrencyTarget, _ string) (bool, error) {
	c.acquiredTargets = append(c.acquiredTargets, target)
	return true, nil
}

func (c *targetConcurrencyCacheForTest) ReleaseConcurrencyTargetSlot(_ context.Context, target ConcurrencyTarget, _ string) error {
	c.releasedTargets = append(c.releasedTargets, target)
	return nil
}

func (c *targetConcurrencyCacheForTest) IncrementConcurrencyTargetWaitCount(context.Context, ConcurrencyTarget, int) (bool, error) {
	return true, nil
}

func (c *targetConcurrencyCacheForTest) DecrementConcurrencyTargetWaitCount(context.Context, ConcurrencyTarget) error {
	return nil
}

func (c *targetConcurrencyCacheForTest) GetConcurrencyTargetWaitingCount(context.Context, ConcurrencyTarget) (int, error) {
	return 0, nil
}

func TestAcquireTargetSlot_UnlimitedStillTracksAndReleases(t *testing.T) {
	cache := &targetConcurrencyCacheForTest{}
	svc := &ConcurrencyService{cache: cache}
	target := ConcurrencyTarget{Kind: ConcurrencyTargetUpstream, ID: 91, Limit: 0}

	result, err := svc.AcquireTargetSlot(context.Background(), target)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	require.Equal(t, []ConcurrencyTarget{target}, cache.acquiredTargets)

	result.ReleaseFunc()
	require.Equal(t, []ConcurrencyTarget{target}, cache.releasedTargets)
}
