package service

import (
	"context"
	"time"
)

// SessionSwitchScope identifies one API key session and routing model. State
// stored for this scope must not affect another key, group, session, or model.
type SessionSwitchScope struct {
	APIKeyID    int64
	GroupID     int64
	SessionHash string
	RouteModel  string
}

// SessionSwitchAccountScope extends a session scope with the currently used
// upstream account whose consecutive failures are being tracked.
type SessionSwitchAccountScope struct {
	SessionSwitchScope
	AccountID int64
}

// SessionSwitchFailureState is returned after one failure is recorded.
type SessionSwitchFailureState struct {
	FailureCount  int64
	Tripped       bool
	CooldownUntil time.Time
}

// SessionSwitchStateStore persists cross-request failure counters and
// session-local account cooldowns. Callers intentionally own fail-open policy:
// repository errors are returned unchanged and must not block normal routing.
type SessionSwitchStateStore interface {
	RecordSessionSwitchFailure(ctx context.Context, scope SessionSwitchAccountScope, window time.Duration, threshold int, cooldown time.Duration) (SessionSwitchFailureState, error)
	ClearSessionSwitchFailures(ctx context.Context, scope SessionSwitchAccountScope) error
	ListSessionSwitchCooldownAccountIDs(ctx context.Context, scope SessionSwitchScope) ([]int64, error)
}
