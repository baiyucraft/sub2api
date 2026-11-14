package upstreamcacheguard

import (
	"context"
	"time"
)

type stageObserverKey struct{}
type stageCounterKey struct{}

// WithStageObserver attaches request-local timing only. Observers receive fixed
// code labels and durations, never session identifiers, SQL, or request content.
func WithStageObserver(ctx context.Context, observer func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, stageObserverKey{}, observer)
}

// WithStageCounter attaches numeric work sizes to the same refresh-local
// diagnostics. Call sites use fixed code labels; values never contain identities.
func WithStageCounter(ctx context.Context, observer func(string, int64)) context.Context {
	return context.WithValue(ctx, stageCounterKey{}, observer)
}

func ObserveStageCount(ctx context.Context, name string, value int64) {
	if observer, _ := ctx.Value(stageCounterKey{}).(func(string, int64)); observer != nil {
		observer(name, value)
	}
}

// MeasureStage is a no-op without a request observer, including background work.
// Timings can overlap (a transaction includes its lock/read/write stages).
func MeasureStage(ctx context.Context, stage string) func() {
	observer, _ := ctx.Value(stageObserverKey{}).(func(string, time.Duration))
	if observer == nil {
		return func() {}
	}
	start := time.Now()
	return func() { observer(stage, time.Since(start)) }
}
