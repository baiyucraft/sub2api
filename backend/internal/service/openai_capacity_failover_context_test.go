package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAICapacityExcludedTargetsCloneAndSharedPoolMatch(t *testing.T) {
	upstreamID := int64(42)
	accountA := &Account{ID: 1, Concurrency: 4, UpstreamConfigID: &upstreamID, UpstreamConcurrencyLimit: 8}
	accountB := &Account{ID: 2, Concurrency: 4, UpstreamConfigID: &upstreamID, UpstreamConcurrencyLimit: 8}
	ordinary := &Account{ID: 3, Concurrency: 4}

	targets := map[string]struct{}{accountA.SchedulingConcurrencyTarget().Key(): {}}
	ctx := WithOpenAICapacityExcludedTargets(context.Background(), targets)
	delete(targets, accountA.SchedulingConcurrencyTarget().Key())

	require.True(t, openAICapacityTargetExcluded(ctx, accountA))
	require.True(t, openAICapacityTargetExcluded(ctx, accountB))
	require.False(t, openAICapacityTargetExcluded(ctx, ordinary))
}

func TestWithOpenAICapacityExcludedTargetsIgnoresEmptyInput(t *testing.T) {
	ctx := context.Background()
	require.Equal(t, ctx, WithOpenAICapacityExcludedTargets(ctx, nil))
	require.Empty(t, openAICapacityExcludedTargets(ctx))
}
