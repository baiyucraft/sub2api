package service

import (
	"context"
	"log/slog"
	"time"
)

// accountRPMPrefetchContextKey carries a request-local snapshot of current
// minute counters. It is only an optimization; final admission must still use
// the atomic limiter immediately before the upstream request.
type accountRPMPrefetchContextKey struct{}

var accountRPMPrefetchKey accountRPMPrefetchContextKey

type accountRPMRetryAfterContextKey struct{}

var accountRPMRetryAfterKey accountRPMRetryAfterContextKey

// RecordAccountRPMRetryAfter stores the shortest retry window observed while
// a request tries alternate accounts. It returns a derived context so callers
// can keep the value across handler/service boundaries without package-private
// context keys.
func RecordAccountRPMRetryAfter(ctx context.Context, retryAfter time.Duration) context.Context {
	if ctx == nil || retryAfter <= 0 {
		return ctx
	}
	seconds := int(retryAfter / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	if current, ok := ctx.Value(accountRPMRetryAfterKey).(int); ok && current > 0 && current <= seconds {
		return ctx
	}
	return context.WithValue(ctx, accountRPMRetryAfterKey, seconds)
}

// AccountRPMRetryAfter returns the shortest recorded retry window in seconds.
func AccountRPMRetryAfter(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	seconds, _ := ctx.Value(accountRPMRetryAfterKey).(int)
	return seconds
}

func withAccountRPMPrefetch(ctx context.Context, cache RPMCache, accounts []Account) context.Context {
	if cache == nil {
		return ctx
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		if accounts[i].RPMLimit > 0 {
			ids = append(ids, accounts[i].ID)
		}
	}
	if len(ids) == 0 {
		return ctx
	}
	counts, err := cache.GetRPMBatch(ctx, ids)
	if err != nil {
		return ctx // Redis failure is fail-open.
	}
	return context.WithValue(ctx, accountRPMPrefetchKey, counts)
}

func accountRPMFromPrefetch(ctx context.Context, accountID int64) (int, bool) {
	counts, ok := ctx.Value(accountRPMPrefetchKey).(map[int64]int)
	if !ok {
		return 0, false
	}
	count, found := counts[accountID]
	return count, found
}

// IsAccountSchedulableForRPM applies the non-authoritative pre-check for a
// generic account-level RPM limit. A missing cache or failed read is fail-open.
func IsAccountSchedulableForRPM(ctx context.Context, cache RPMCache, account *Account) bool {
	if account == nil || account.RPMLimit <= 0 || cache == nil {
		return true
	}
	current, ok := accountRPMFromPrefetch(ctx, account.ID)
	if !ok {
		var err error
		current, err = cache.GetRPM(ctx, account.ID)
		if err != nil {
			return true
		}
	}
	return current < account.RPMLimit
}

// TryAcquireAccountRPM reserves the authoritative account RPM slot immediately
// before an actual upstream request. Redis failures intentionally fail open.
func TryAcquireAccountRPM(ctx context.Context, cache RPMCache, account *Account) (allowed bool, retryAfter time.Duration, err error) {
	if account == nil || account.RPMLimit <= 0 || cache == nil {
		return true, 0, nil
	}
	limiter, ok := cache.(AccountRPMLimiter)
	if !ok {
		return true, 0, nil
	}
	allowed, _, retryAfter, err = limiter.TryAcquireRPM(ctx, account.ID, account.RPMLimit)
	if err != nil {
		slog.Warn("account_rpm_limiter_unavailable_fail_open", "account_id", account.ID, "error", err)
		return true, 0, err
	}
	return allowed, retryAfter, nil
}
