package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

const (
	cacheGuardRefreshBudget       = 3 * time.Second
	cacheGuardPendingHealthBudget = 500 * time.Millisecond
	cacheGuardRefreshLogInterval  = time.Minute
)

// Initialize lazily as well as in production so manually constructed runners
// retain the same cancellation contract without requiring test-only wiring.
func (r *UpstreamCacheGuardRunner) refreshContext() context.Context {
	r.refreshLifecycleMu.Lock()
	defer r.refreshLifecycleMu.Unlock()
	if r.refreshCtx == nil {
		r.refreshCtx, r.refreshCancel = context.WithCancel(context.Background())
	}
	if r.closed.Load() {
		r.refreshCancel()
	}
	return r.refreshCtx
}

func (r *UpstreamCacheGuardRunner) cancelRefresh() {
	r.refreshLifecycleMu.Lock()
	defer r.refreshLifecycleMu.Unlock()
	if r.refreshCancel != nil {
		r.refreshCancel()
	}
}

func cacheGuardRefreshStage(ctx context.Context, stage string, run func() error) error {
	done := upstreamcacheguard.MeasureStage(ctx, stage)
	defer done()
	if err := ctx.Err(); err != nil {
		return &upstreamcacheguard.RefreshStageError{Stage: stage, Err: err}
	}
	err := run()
	err = errors.Join(err, ctx.Err())
	if err != nil {
		return &upstreamcacheguard.RefreshStageError{Stage: stage, Err: err}
	}
	return nil
}

type cacheGuardRefreshTimings struct {
	mu     sync.Mutex
	stages map[string]float64
}

func newCacheGuardRefreshTimings() *cacheGuardRefreshTimings {
	return &cacheGuardRefreshTimings{stages: map[string]float64{}}
}

func (t *cacheGuardRefreshTimings) observe(stage string, elapsed time.Duration) {
	t.mu.Lock()
	t.stages[stage] += float64(elapsed) / float64(time.Millisecond)
	t.mu.Unlock()
}

func (t *cacheGuardRefreshTimings) snapshot() map[string]float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	stages := make(map[string]float64, len(t.stages))
	for stage, elapsed := range t.stages {
		stages[stage] = elapsed
	}
	return stages
}

type cacheGuardRefreshLogState struct {
	failures    uint64
	lastSuccess time.Time
	lastLogged  map[string]time.Time
}

type cacheGuardRefreshDiagnostics struct {
	mu     sync.Mutex
	states map[string]cacheGuardRefreshLogState
}

func (d *cacheGuardRefreshDiagnostics) record(source string, err error, started time.Time, budget time.Duration, timings map[string]float64) {
	d.recordAt(time.Now(), source, err, time.Since(started), budget, timings)
}

func (d *cacheGuardRefreshDiagnostics) recordAt(now time.Time, source string, err error, elapsed, budget time.Duration, timings map[string]float64) {
	stage, reason := source, "database_error"
	var failedStage *upstreamcacheguard.RefreshStageError
	if errors.As(err, &failedStage) {
		stage = failedStage.Stage
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, upstreamcacheguard.ErrUnavailable):
		reason = "unavailable"
	}
	d.mu.Lock()
	if d.states == nil {
		d.states = map[string]cacheGuardRefreshLogState{}
	}
	state := d.states[source]
	lastSuccess := state.lastSuccess
	logFailure, recovered := false, false
	if err == nil {
		recovered = state.failures != 0
		state.lastSuccess = now
		state.lastLogged = nil
	} else {
		state.failures++
		kind := stage + ":" + reason
		lastLogged, logged := state.lastLogged[kind]
		logFailure = !logged || now.Sub(lastLogged) >= cacheGuardRefreshLogInterval
		if logFailure {
			if state.lastLogged == nil {
				state.lastLogged = map[string]time.Time{}
			}
			state.lastLogged[kind] = now
		}
	}
	failures := state.failures
	if err == nil {
		state.failures = 0
	}
	d.states[source] = state
	d.mu.Unlock()
	if !logFailure && !recovered {
		return
	}
	age := int64(-1)
	if !lastSuccess.IsZero() && !now.Before(lastSuccess) {
		age = now.Sub(lastSuccess).Milliseconds()
	}
	attrs := []any{"source", source, "stage", stage, "elapsed_ms", float64(elapsed) / float64(time.Millisecond), "budget_ms", budget.Milliseconds(), "consecutive_failures", failures, "last_success_age_ms", age, "stage_elapsed_ms", timings}
	if recovered {
		slog.Info("upstream_cache_guard_refresh_recovered", append(attrs, "reason", "recovered")...)
	} else {
		// Never log err.Error(): repository errors may contain SQL or evidence.
		slog.Warn("upstream_cache_guard_refresh_failed", append(attrs, "reason", reason)...)
	}
}
