package upstreamcacheguard

import (
	"context"
	"time"
)

type stageObserverKey struct{}

// WithStageObserver attaches request-local timing only. Observers receive fixed
// code labels and durations, never session identifiers, SQL, or request content.
func WithStageObserver(ctx context.Context, observer func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, stageObserverKey{}, observer)
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
