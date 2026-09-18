package service

import "context"

type openAICapacityExcludedTargetsContextKey struct{}

// WithOpenAICapacityExcludedTargets carries request-local shared concurrency
// targets that must not be selected again after their wait queues filled.
func WithOpenAICapacityExcludedTargets(ctx context.Context, targets map[string]struct{}) context.Context {
	if ctx == nil || len(targets) == 0 {
		return ctx
	}
	cloned := make(map[string]struct{}, len(targets))
	for key := range targets {
		if key != "" {
			cloned[key] = struct{}{}
		}
	}
	if len(cloned) == 0 {
		return ctx
	}
	return context.WithValue(ctx, openAICapacityExcludedTargetsContextKey{}, cloned)
}

func openAICapacityExcludedTargets(ctx context.Context) map[string]struct{} {
	if ctx == nil {
		return nil
	}
	targets, _ := ctx.Value(openAICapacityExcludedTargetsContextKey{}).(map[string]struct{})
	return targets
}

func openAICapacityTargetExcluded(ctx context.Context, account *Account) bool {
	return openAICapacityTargetKeyExcluded(openAICapacityExcludedTargets(ctx), account)
}

func openAICapacityTargetKeyExcluded(targets map[string]struct{}, account *Account) bool {
	if account == nil || len(targets) == 0 {
		return false
	}
	_, excluded := targets[account.SchedulingConcurrencyTarget().Key()]
	return excluded
}
