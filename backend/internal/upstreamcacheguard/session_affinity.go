package upstreamcacheguard

import "time"

const defaultSessionAffinityTTL = time.Hour

// SetSessionAffinityTTL shares OpenAI's configured sticky idle lifetime. It is
// independent of cache warmness, account evidence and recovery policy versions.
func (e *Engine) SetSessionAffinityTTL(ttl time.Duration) {
	if ttl <= 0 {
		ttl = defaultSessionAffinityTTL
	}
	e.sessionAffinityTTL.Store(int64(ttl))
}

func (e *Engine) sessionAffinityLifetime() time.Duration {
	if ttl := time.Duration(e.sessionAffinityTTL.Load()); ttl > 0 {
		return ttl
	}
	return defaultSessionAffinityTTL
}

func openAISessionAffinity(protocol string) bool {
	return protocol == "openai" || protocol == "openai_ws"
}

func sessionAffinityActivity(session SessionState) time.Time {
	last := session.LastStartedAt
	if session.LastCompletedAt.After(last) {
		last = session.LastCompletedAt
	}
	if last.IsZero() {
		// Older typed rows can lack the session-level activity fields while
		// retaining a real successful completion on their current route.
		last = session.Routes[session.LastRouteKey].LastSuccessfulAt
	}
	return last
}

func (e *Engine) sessionAffinityIdle(session SessionState, protocol string, at time.Time) bool {
	if !openAISessionAffinity(protocol) {
		return false
	}
	last := sessionAffinityActivity(session)
	if last.IsZero() {
		// A legacy route without a reliable activity time cannot be immortal.
		return session.LastAccountID > 0
	}
	return !at.Before(last) && at.Sub(last) >= e.sessionAffinityLifetime()
}

func (e *Engine) sessionAffinityExpired(session SessionState, protocol string, at time.Time) bool {
	return openAISessionAffinity(protocol) && (e.sessionAffinityIdle(session, protocol, at) ||
		(session.AffinityGeneration > 0 && session.LastAccountID == 0))
}

// Only a new, real admission advances the fence. Reads and trial planning leave
// the durable identity, trial history and activity unchanged. The caller checks
// immutable admission replay before reaching this function.
func (e *Engine) resetExpiredSessionAffinity(session *SessionState, r Request) {
	if !e.sessionAffinityIdle(*session, r.Protocol, r.StartedAt) {
		return
	}
	session.AffinityGeneration++
	session.LastAccountID = 0
	session.LastModel, session.LastProtocol, session.LastRouteKey = "", "", ""
	session.LastCompletedAt = time.Time{}
	session.ResidenceUntil = time.Time{}
	for key, route := range session.Routes {
		route.HighCacheSeen, route.PendingLow, route.LowStreak = false, false, 0
		route.LowWindow = nil
		session.Routes[key] = route
	}
}
