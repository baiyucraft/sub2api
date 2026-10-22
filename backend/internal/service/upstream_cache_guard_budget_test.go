package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCacheGuardBudgetDoesNotChargeTimeBetweenDatabaseStages(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	first, release := cacheGuardDatabaseContextForStage(ctx, "session_load")
	require.NoError(t, first.Err())
	release()
	b := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget
	b.mu.Lock()
	used := b.activeElapsed
	// The initial deadline is already past, but only actual stage time counts.
	b.startedAt = time.Now().Add(-time.Second)
	b.deadline = time.Now().Add(-time.Second)
	b.mu.Unlock()
	second, secondRelease := cacheGuardDatabaseContextForStage(ctx, "admission")
	defer secondRelease()
	require.NoError(t, second.Err())
	deadline, ok := second.Deadline()
	require.True(t, ok)
	require.Greater(t, time.Until(deadline), 70*time.Millisecond-used)
	require.False(t, protectionOrdinaryFallback(ctx))
}

func TestCacheGuardBudgetStagesSerializeAndWaitingCallerCanCancel(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	_, release := cacheGuardDatabaseContextForStage(ctx, "session_load")
	waiting, cancelWaiting := context.WithCancel(ctx)
	started := make(chan struct{})
	returned := make(chan context.Context, 1)
	go func() {
		close(started)
		child, cancel := cacheGuardDatabaseContextForStage(waiting, "trial_plan")
		defer cancel()
		returned <- child
	}()
	<-started
	select {
	case <-returned:
		t.Fatal("a concurrent stage acquired an occupied lease")
	default:
	}
	cancelWaiting()
	select {
	case child := <-returned:
		require.ErrorIs(t, child.Err(), context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waiting stage did not honor cancellation")
	}
	release()
	b := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget
	b.mu.Lock()
	used := b.activeElapsed
	b.mu.Unlock()
	release()
	b.mu.Lock()
	require.Equal(t, used, b.activeElapsed)
	b.mu.Unlock()
	next, nextRelease := cacheGuardDatabaseContextForStage(ctx, "admission")
	defer nextRelease()
	require.NoError(t, next.Err(), "canceled waiter must not strand the lease")
	require.False(t, b.exhausted.Load())
}

func TestCacheGuardBudgetUsesRemainingAllowanceAndRetainsRetryState(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	b := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget
	b.activeElapsed = 90 * time.Millisecond
	b.ttftOpportunity.Store(true)
	b.ttftTrialAttempted.Store(true)
	child, release := cacheGuardDatabaseContextForStage(ctx, "admission")
	deadline, ok := child.Deadline()
	require.True(t, ok)
	require.LessOrEqual(t, time.Until(deadline), 10*time.Millisecond)
	<-child.Done()
	release()
	require.True(t, b.exhausted.Load())
	require.True(t, protectionOrdinaryFallback(ctx))
	require.NoError(t, ctx.Err())
	retry := WithUpstreamCacheGuardScheduling(ctx, 1)
	require.Same(t, b, retry.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget)
	denied, deniedRelease := cacheGuardDatabaseContextForStage(retry, "trial_plan")
	defer deniedRelease()
	require.ErrorIs(t, denied.Err(), context.DeadlineExceeded)
	require.True(t, b.ttftOpportunity.Load())
	require.True(t, b.ttftTrialAttempted.Load())
}

func TestCacheGuardBudgetConcurrentStageStartsAfterPreviousRelease(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	_, release := cacheGuardDatabaseContextForStage(ctx, "session_load")
	acquired := make(chan context.Context, 1)
	finish := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		child, cancel := cacheGuardDatabaseContextForStage(ctx, "admission")
		acquired <- child
		<-finish
		cancel()
	}()
	select {
	case <-acquired:
		release()
		close(finish)
		t.Fatal("concurrent stage spent an already leased allowance")
	case <-time.After(5 * time.Millisecond):
	}
	release()
	select {
	case child := <-acquired:
		require.NoError(t, child.Err())
	case <-time.After(time.Second):
		t.Fatal("stage did not acquire the released allowance")
	}
	close(finish)
	<-done
}

func TestCacheGuardBudgetRespectsCallerDeadline(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	parent, cancel := context.WithDeadline(ctx, time.Now().Add(20*time.Millisecond))
	defer cancel()
	child, release := cacheGuardDatabaseContextForStage(parent, "admission")
	defer release()
	parentDeadline, _ := parent.Deadline()
	childDeadline, _ := child.Deadline()
	require.Equal(t, parentDeadline, childDeadline)
	<-child.Done()
	release()
	b := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget
	require.False(t, b.exhausted.Load(), "a shorter caller deadline does not exhaust 100 ms")
	require.True(t, b.ordinary.Load())
}

func TestCacheGuardBudgetReleaseDetectsElapsedAllowanceWithoutTimerNotification(t *testing.T) {
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	b := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity).budget
	child, release := cacheGuardDatabaseContextForStage(ctx, "admission")
	require.NoError(t, child.Err())
	b.mu.Lock()
	b.activeElapsed = cacheGuardDatabaseBudget
	b.mu.Unlock()
	release()
	require.True(t, b.exhausted.Load())
	require.True(t, b.ordinary.Load())
}
