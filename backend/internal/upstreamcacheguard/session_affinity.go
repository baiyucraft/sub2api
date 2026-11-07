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
		(session.AffinityGeneration > 0 && session.LastAccountID == 0 && session.AcceptedRouteKey == ""))
}

// A serialized admission fences older completions but is not proof that the
// terminal before-send checks and sticky binding succeeded. Until this route
// generation has a real successful completion, leave owner resolution to the
// caller's existing sticky binding. Older rows retain their successful identity.
func sessionPreferredRoute(session SessionState) (string, SessionRoute, string, string) {
	if session.RouteGeneration > 0 && session.AcceptedRouteKey != "" {
		if session.OwnerRouteGeneration != session.RouteGeneration {
			return "", SessionRoute{}, "", ""
		}
		route := session.Routes[session.AcceptedRouteKey]
		return session.AcceptedRouteKey, route, route.Model, route.Protocol
	}
	route := session.Routes[session.LastRouteKey]
	// Legacy route JSON did not always repeat the session-level owner ID.
	if route.AccountID == 0 {
		route.AccountID = session.LastAccountID
	}
	return session.LastRouteKey, route, session.LastModel, session.LastProtocol
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
	session.AcceptedRouteKey = ""
	session.LastCompletedAt = time.Time{}
	session.ResidenceUntil = time.Time{}
	for key, route := range session.Routes {
		route.HighCacheSeen, route.PendingLow, route.LowStreak = false, false, 0
		route.LowWindow = nil
		session.Routes[key] = route
	}
}
