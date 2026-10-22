package service

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

const cacheGuardDatabaseBudget = 100 * time.Millisecond

// One logical request owns a cumulative database allowance. A lease keeps
// overlapping stages from spending the same remaining allowance twice.
type cacheGuardRequestBudget struct {
	ttftOpportunity    atomic.Bool
	ttftTrialAttempted atomic.Bool
	mu                 sync.Mutex
	lease              chan struct{}
	startedAt          time.Time
	deadline           time.Time
	exhausted          atomic.Bool
	ordinary           atomic.Bool
	selectionFallback  atomic.Bool
	stages             map[string]time.Duration
	activeElapsed      time.Duration
	groupID            int64
	accountID          int64
	upstreamKeyID      int64
}

func cacheGuardDatabaseContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return cacheGuardDatabaseContextForStage(ctx, "session_load")
}

func cacheGuardDatabaseContextForStage(ctx context.Context, operation string) (context.Context, context.CancelFunc) {
	identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if !ok || identity.budget == nil {
		return context.WithTimeout(ctx, cacheGuardDatabaseBudget)
	}
	b := identity.budget
	b.mu.Lock()
	if b.lease == nil {
		b.lease = make(chan struct{}, 1)
	}
	lease := b.lease
	b.mu.Unlock()
	select {
	case lease <- struct{}{}:
	case <-ctx.Done():
		child, cancel := context.WithCancel(ctx)
		cancel()
		return child, cancel
	}
	if ctx.Err() != nil {
		<-lease
		child, cancel := context.WithCancel(ctx)
		cancel()
		return child, cancel
	}
	started := time.Now()
	b.mu.Lock()
	if b.startedAt.IsZero() {
		b.startedAt = started
	}
	remaining := cacheGuardDatabaseBudget - b.activeElapsed
	if b.ordinary.Load() || b.exhausted.Load() {
		remaining = 0
	}
	b.deadline = started.Add(remaining)
	deadline := b.deadline
	b.mu.Unlock()
	child, cancel := context.WithDeadline(ctx, deadline)
	child = upstreamcacheguard.WithStageObserver(child, func(stage string, elapsed time.Duration) {
		b.mu.Lock()
		if b.stages == nil {
			b.stages = map[string]time.Duration{}
		}
		b.stages[stage] += elapsed
		b.mu.Unlock()
	})
	var finished sync.Once
	return child, func() {
		finished.Do(func() {
			ended := time.Now()
			timedOut := child.Err() == context.DeadlineExceeded
			cancel()
			b.mu.Lock()
			b.activeElapsed += ended.Sub(started)
			elapsed := b.activeElapsed
			wallElapsed := ended.Sub(b.startedAt)
			gap := wallElapsed - elapsed
			if gap < 0 {
				gap = 0
			}
			budgetExhausted := elapsed >= cacheGuardDatabaseBudget
			if timedOut || budgetExhausted {
				b.ordinary.Store(true)
			}
			logExhausted := budgetExhausted && b.exhausted.CompareAndSwap(false, true)
			var stages map[string]float64
			if logExhausted {
				stages = make(map[string]float64, len(b.stages))
				for stage, duration := range b.stages {
					stages[stage] = float64(duration) / float64(time.Millisecond)
				}
			}
			groupID, accountID, keyID := b.groupID, b.accountID, b.upstreamKeyID
			b.mu.Unlock()
			<-lease
			if logExhausted {
				slog.Warn("upstream_cache_guard_budget_exhausted", "budget_ms", 100, "fallback", "ordinary_scheduler", "operation", operation,
					"budget_elapsed_ms", float64(elapsed)/float64(time.Millisecond), "between_operations_ms", float64(gap)/float64(time.Millisecond),
					"wall_elapsed_ms", float64(wallElapsed)/float64(time.Millisecond),
					"stages_ms", stages, "group_id", groupID, "account_id", accountID, "upstream_key_id", keyID)
			}
		})
	}
}

func protectionOrdinaryFallback(ctx context.Context) bool {
	identity, _ := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	return identity.budget != nil && (identity.budget.ordinary.Load() || identity.budget.selectionFallback.Load())
}

// Disable stale protection decisions without spending the remaining capture
// budget. The handler retries ordinary selection without excluding the account.
func protectionSnapshotFallbackError(ctx context.Context) *UpstreamFailoverError {
	identity, _ := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if identity.budget != nil {
		identity.budget.selectionFallback.Store(true)
	}
	failure := schedulingProtectionScopeChangedError()
	failure.Reason = "scheduling_protection_snapshot_unavailable"
	failure.ProtectionOrdinaryFallback = true
	return failure
}

func protectionBudgetFallbackError() *UpstreamFailoverError {
	failure := schedulingProtectionScopeChangedError()
	failure.Reason = "scheduling_protection_budget_exhausted"
	failure.ProtectionOrdinaryFallback = true
	return failure
}
