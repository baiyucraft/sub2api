package service

import "context"

// channelMonitorRunnerTaskContextKey carries the scheduled task context into
// RunCheck without changing the public/manual RunCheck contract. The child
// request context may expire because of the probe timeout; the task context
// lets persistence distinguish that from a scheduler cancellation caused by
// Stop, Unschedule, or Schedule replacement.
type channelMonitorRunnerTaskContextKey struct{}

func withChannelMonitorRunnerTaskContext(ctx, taskCtx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if taskCtx == nil {
		taskCtx = ctx
	}
	return context.WithValue(ctx, channelMonitorRunnerTaskContextKey{}, taskCtx)
}

func channelMonitorRunnerTaskCancelled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	taskCtx, ok := ctx.Value(channelMonitorRunnerTaskContextKey{}).(context.Context)
	return ok && taskCtx != nil && taskCtx.Err() != nil
}
