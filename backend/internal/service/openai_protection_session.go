package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"
)

type protectionSessionState struct {
	mu             sync.Mutex
	slow           int
	threshold      time.Duration
	last           time.Time
	residenceUntil time.Time
}
type protectionSelectionKey struct{}
type protectionSelection struct {
	service   *OpenAIGatewayService
	trial     *openAITTFTTrial
	threshold time.Duration
	groupID   int64
}

// Only an explicit logical identity is reliable; request-content hashes never
// establish continuity. The route key also freezes actual account/key/model.
func protectionSessionKey(ctx context.Context, groupID int64, a *Account, model, protocol string) (string, bool) {
	stable, _ := ctx.Value(cacheGuardLogicalSessionKey{}).(string)
	identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if stable == "" || !ok || identity.APIKeyID <= 0 || groupID <= 0 {
		return "", false
	}
	route := ""
	if a != nil {
		route = fmt.Sprintf(":%d:%d:%s:%s", a.ID, accountUpstreamKeyID(a), model, protocol)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s%s", identity.APIKeyID, groupID, stable, route)))), true
}

func (s *OpenAIGatewayService) protectionSession(key string, now time.Time) *protectionSessionState {
	if v, ok := s.protectionSessions.Load(key); ok {
		return v.(*protectionSessionState)
	}
	count := 0
	s.protectionSessions.Range(func(k, v any) bool {
		e := v.(*protectionSessionState)
		e.mu.Lock()
		expired := now.Sub(e.last) > 30*time.Minute
		e.mu.Unlock()
		if expired {
			s.protectionSessions.Delete(k)
		} else {
			count++
		}
		return true
	})
	if count >= openAITTFTGuardDefaultMaxEntries {
		s.protectionSessions.Range(func(k, v any) bool { s.protectionSessions.Delete(k); return false })
	}
	e := &protectionSessionState{last: now}
	actual, _ := s.protectionSessions.LoadOrStore(key, e)
	return actual.(*protectionSessionState)
}

func (s *OpenAIGatewayService) sessionTTFTCanMigrate(ctx context.Context, groupID int64, a *Account, model, protocol string, threshold time.Duration) bool {
	key, ok := protectionSessionKey(ctx, groupID, a, model, protocol)
	if !ok {
		return false
	}
	v, ok := s.protectionSessions.Load(key)
	if !ok {
		return false
	}
	e := v.(*protectionSessionState)
	e.mu.Lock()
	slow := e.slow >= 2 && e.threshold == threshold && time.Since(e.last) < 15*time.Minute
	e.mu.Unlock()
	return slow && !s.protectionSessionResident(ctx, groupID)
}
func (s *OpenAIGatewayService) protectionSessionResident(ctx context.Context, groupID int64) bool {
	key, ok := protectionSessionKey(ctx, groupID, nil, "", "")
	if !ok {
		return false
	}
	v, ok := s.protectionSessions.Load(key)
	if !ok {
		return false
	}
	e := v.(*protectionSessionState)
	e.mu.Lock()
	defer e.mu.Unlock()
	return time.Now().Before(e.residenceUntil)
}
func (s *OpenAIGatewayService) recordProtectionMigration(ctx context.Context, groupID *int64) {
	if groupID == nil {
		return
	}
	key, ok := protectionSessionKey(ctx, *groupID, nil, "", "")
	if !ok {
		return
	}
	now := time.Now()
	e := s.protectionSession(key, now)
	e.mu.Lock()
	e.residenceUntil = now.Add(5 * time.Minute)
	e.last = now
	e.mu.Unlock()
}

func (s *OpenAIGatewayService) attachProtectionSelection(result *AccountSelectionResult, groupID int64, model string, cfg OpenAITTFTGuardConfigSnapshot, trials ...*openAITTFTTrial) {
	if result == nil || result.Account == nil || !SchedulingProtectionGroupSelected(s.groupProtectionPolicyResolver, groupID) || !schedulingProtectionAccountSelected(s.groupProtectionPolicyResolver, result.Account) {
		return
	}
	var trial *openAITTFTTrial
	if len(trials) > 0 {
		trial = trials[0]
	}
	result.protection = &protectionSelection{service: s, threshold: cfg.Threshold, groupID: groupID, trial: trial}
}
