package handler

import (
	"context"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// sessionSwitchGuard isolates the fork-specific session switching policy from
// the shared failover state machine. Handler entrypoints only provide stable
// lifecycle hooks; the runtime owns eligibility, persistence, and fail-open
// behavior.
type sessionSwitchGuard struct {
	runtime     service.SessionSwitchRuntime
	ctx         context.Context
	apiKey      *service.APIKey
	groupID     *int64
	sessionHash string

	failedAccountIDs     map[int64]struct{}
	persistentExclusions map[int64]struct{}
}

func newSessionSwitchGuard(
	runtime service.SessionSwitchRuntime,
	ctx context.Context,
	apiKey *service.APIKey,
	groupID *int64,
	sessionHash string,
	failedAccountIDs map[int64]struct{},
) *sessionSwitchGuard {
	if failedAccountIDs == nil {
		failedAccountIDs = make(map[int64]struct{})
	}
	return &sessionSwitchGuard{
		runtime:              runtime,
		ctx:                  ctx,
		apiKey:               apiKey,
		groupID:              groupID,
		sessionHash:          sessionHash,
		failedAccountIDs:     failedAccountIDs,
		persistentExclusions: make(map[int64]struct{}),
	}
}

func (g *sessionSwitchGuard) MergeExclusions(routeModel string) {
	if g == nil || g.runtime == nil {
		return
	}
	g.addPersistentExclusions(g.runtime.SessionSwitchExcludedAccountIDs(
		g.ctx, g.apiKey, g.groupID, g.sessionHash, routeModel,
	))
}

func (g *sessionSwitchGuard) RecordFailure(
	routeModel string,
	accountID int64,
	failoverErr *service.UpstreamFailoverError,
) (*service.UpstreamFailoverError, bool) {
	if failoverErr == nil {
		return nil, false
	}
	tripped := g.RecordStatus(routeModel, accountID, failoverErr.StatusCode)
	if !tripped || !failoverErr.ShouldRetryNextAccount() || !failoverErr.RetryableOnSameAccount {
		return failoverErr, tripped
	}

	// The threshold only changes this request's retry strategy. Do not mutate
	// the shared error instance because other observers may still read it.
	effective := *failoverErr
	effective.RetryableOnSameAccount = false
	return &effective, true
}

func (g *sessionSwitchGuard) RecordStatus(routeModel string, accountID int64, statusCode int) bool {
	if g == nil || g.runtime == nil || accountID <= 0 {
		return false
	}
	tripped := g.runtime.RecordSessionSwitchFailure(
		g.ctx, g.apiKey, g.groupID, g.sessionHash, routeModel, accountID, statusCode,
	)
	if tripped {
		g.addPersistentExclusions(map[int64]struct{}{accountID: {}})
	}
	return tripped
}

func (g *sessionSwitchGuard) ClearSuccess(routeModel string, accountID int64) {
	if g == nil || g.runtime == nil || accountID <= 0 {
		return
	}
	g.runtime.ClearSessionSwitchFailures(
		g.ctx, g.apiKey, g.groupID, g.sessionHash, routeModel, accountID,
	)
}

// HandleSelectionExhausted preserves session cooldown exclusions when the
// shared 503 backoff path clears ordinary per-request failures.
func (g *sessionSwitchGuard) HandleSelectionExhausted(ctx context.Context, state *FailoverState) FailoverAction {
	if state == nil {
		return FailoverExhausted
	}
	if ctx != nil && ctx.Err() != nil {
		return FailoverCanceled
	}
	if state.LastFailoverErr != nil &&
		state.LastFailoverErr.StatusCode == http.StatusServiceUnavailable &&
		g.allFailedAccountsArePersistentExclusions(state.FailedAccountIDs) {
		return FailoverExhausted
	}
	action := state.HandleSelectionExhausted(ctx)
	if action == FailoverContinue {
		g.failedAccountIDs = state.FailedAccountIDs
		g.restorePersistentExclusions(state.FailedAccountIDs)
	}
	return action
}

func (g *sessionSwitchGuard) allFailedAccountsArePersistentExclusions(failed map[int64]struct{}) bool {
	if g == nil || len(failed) == 0 || len(g.persistentExclusions) == 0 {
		return false
	}
	for accountID := range failed {
		if _, ok := g.persistentExclusions[accountID]; !ok {
			return false
		}
	}
	return true
}

func (g *sessionSwitchGuard) addPersistentExclusions(accountIDs map[int64]struct{}) {
	if g == nil {
		return
	}
	for accountID := range accountIDs {
		if accountID <= 0 {
			continue
		}
		g.persistentExclusions[accountID] = struct{}{}
		g.failedAccountIDs[accountID] = struct{}{}
	}
}

func (g *sessionSwitchGuard) restorePersistentExclusions(target map[int64]struct{}) {
	if g == nil || target == nil {
		return
	}
	for accountID := range g.persistentExclusions {
		target[accountID] = struct{}{}
	}
}

// openAIWSStickyAndSessionSwitchHashes keeps the existing coarse fallback for
// connection stickiness, but only exposes an explicit client session hash to
// the cross-request switch policy. Content-derived and coarse user/key/group
// fallbacks are not reliable session identities and must not share failures.
func openAIWSStickyAndSessionSwitchHashes(
	gateway *service.OpenAIGatewayService,
	c *gin.Context,
	body []byte,
	fallbackSeed string,
) (stickyHash string, sessionSwitchHash string) {
	if gateway == nil {
		return "", ""
	}
	stickyHash = gateway.GenerateSessionHash(c, body)
	sessionSwitchHash = gateway.GenerateExplicitSessionHash(c, body)
	if stickyHash != "" {
		return stickyHash, sessionSwitchHash
	}
	return gateway.GenerateSessionHashWithFallback(c, body, fallbackSeed), sessionSwitchHash
}
