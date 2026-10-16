package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/tidwall/gjson"
)

type cacheUsageAttemptContextKey struct{}

// One holder belongs to one physical-account attempt, not an entire retry chain
// or WebSocket connection. Probes never install this request metadata.
type cacheUsageAttempt struct {
	mu                 sync.Mutex
	apiKeyID           int64
	groupID            *int64
	sessionHash        string
	admission          *UpstreamCacheGuardAdmission
	begun              bool
	beginError         error
	finished           bool
	protectionFinished bool
	account            *Account
	model              string
	protocol           string
}

func WithUpstreamCacheGuardAttempt(ctx context.Context, apiKeyID int64, groupID *int64, sessionHash string, stream bool) context.Context {
	if ctx == nil || !stream || apiKeyID <= 0 || groupID == nil || strings.TrimSpace(sessionHash) == "" {
		return ctx
	}
	identity, ok := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if !ok || identity.APIKeyID != apiKeyID || identity.RequestID == "" {
		return ctx
	}
	group := *groupID
	return context.WithValue(ctx, cacheUsageAttemptContextKey{}, &cacheUsageAttempt{apiKeyID: apiKeyID, groupID: &group, sessionHash: sessionHash})
}

// BeginUpstreamCacheGuardSend freezes warm/cohort state immediately before the
// first send to this account. Same-account transport retries share the holder.
func BeginUpstreamCacheGuardSend(ctx context.Context, account *Account, model, protocol string) error {
	if ctx == nil {
		return nil
	}
	if protocol == "openai_ws" {
		PrepareOpenAIUpstreamCacheGuardSend(ctx, account, model, protocol)
		if err := runSchedulingProtectionBeforeSend(ctx, account, model); err != nil {
			return err
		}
	}
	return beginUpstreamCacheGuardSend(ctx, account, model, protocol)
}

// HTTP OpenAI paths prepare metadata before host/plugin admission, then freeze
// the admission at their shared physical transport entry. Other providers and
// WS frame writes keep their existing immediate Begin behavior.
func PrepareOpenAIUpstreamCacheGuardSend(ctx context.Context, account *Account, model, protocol string) {
	if ctx == nil {
		return
	}
	a, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.begun {
		a.account, a.model, a.protocol = account, model, protocol
	}
}

func beginUpstreamCacheGuardSend(ctx context.Context, account *Account, model, protocol string) error {
	if !schedulingProtectionSendSelected(ctx, account) {
		return nil
	}
	a, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.begun {
		return a.beginError
	}
	a.begun = true
	a.account, a.model, a.protocol = account, model, protocol
	a.admission, a.beginError = beginUpstreamCacheGuardRequest(ctx, a.apiKeyID, a.groupID, a.sessionHash, model, protocol, account)
	return a.beginError
}

func FinishUpstreamCacheGuardAttempt(ctx context.Context, evidence *UpstreamCacheUsageEvidence, success bool, completedAt time.Time) {
	if ctx == nil {
		return
	}
	a, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	admission := a.admission
	a.mu.Unlock()
	if admission == nil {
		return
	}
	protocol := ""
	if evidence != nil {
		protocol = evidence.Protocol
	}
	completed := upstreamcacheusage.Complete(evidence, protocol, success, completedAt)
	ObserveUpstreamCacheGuardResult(admission, completed, success && evidence != nil && evidence.Known() && !evidence.StreamIncomplete, completedAt)
}

func UpstreamCacheGuardTextRequest(path string, body []byte) bool {
	path = strings.ToLower(path)
	if strings.Contains(path, "compact") || strings.Contains(path, "/images") || strings.Contains(path, "/audio") || strings.Contains(path, "/video") || strings.Contains(path, "web_search") || strings.Contains(path, "count_tokens") {
		return false
	}
	root := gjson.ParseBytes(body)
	model := strings.ToLower(root.Get("model").String())
	if strings.Contains(model, "image") || strings.Contains(model, "video") || strings.Contains(model, "tts") || strings.Contains(model, "transcribe") || strings.Contains(model, "audio") {
		return false
	}
	for _, tool := range root.Get("tools").Array() {
		typ := strings.ToLower(tool.Get("type").String())
		if typ == "image_generation" {
			return false
		}
	}
	return true
}

// WithUpstreamCacheGuardTurn derives an independent logical request from the
// authenticated connection metadata. Same-turn transport retries retain it.
func WithUpstreamCacheGuardTurn(ctx context.Context, body []byte, first bool) context.Context {
	a, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	if a == nil || !UpstreamCacheGuardTextRequest("/v1/responses", body) {
		return context.WithValue(ctx, cacheUsageAttemptContextKey{}, (*cacheUsageAttempt)(nil))
	}
	if !first {
		ctx = context.WithValue(ctx, cacheGuardProtectedSelectionKey{}, false)
		ctx = context.WithValue(ctx, cacheGuardSchedulingKey{}, cacheGuardSchedulingIdentity{})
		ctx = WithUpstreamCacheGuardScheduling(ctx, a.apiKeyID)
	}
	// Explicit frame markers can start another logical conversation on the
	// same connection. A handshake session header remains the stronger signal.
	headerFixed, _ := ctx.Value(cacheGuardLogicalSessionHeaderKey{}).(bool)
	if !headerFixed {
		if session := UpstreamCacheGuardLogicalSession(nil, body); session != "" {
			ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, session)
		}
	}
	return WithUpstreamCacheGuardAttempt(ctx, a.apiKeyID, a.groupID, a.sessionHash, true)
}

func FinishOpenAIUpstreamCacheGuardAttempt(ctx context.Context, result *OpenAIForwardResult, err error) {
	var evidence *UpstreamCacheUsageEvidence
	success := err == nil && result != nil && !result.ClientDisconnect && result.SucceededForScheduling()
	if result != nil {
		evidence = result.CacheUsageEvidence
		if evidence == nil {
			evidence = result.Usage.CacheUsageEvidence
		}
		if result.ImageCount > 0 || result.VideoCount > 0 || result.AudioUsage != nil || result.WebSearchCalls > 0 {
			success = false
		}
	}
	if selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); selection != nil {
		var ms *int
		if result != nil {
			ms = result.FirstTokenMs
		}
		if selection.trial != nil && selection.trial.sent.Load() {
			if schedulingProtectionSendSelected(ctx, &Account{ID: selection.trial.key.accountID, Platform: PlatformOpenAI}) {
				selection.trial.finish(success && ctx.Err() == nil, ms)
			} else {
				selection.trial.once.Do(func() {})
			}
		}
		a, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
		if a != nil {
			a.mu.Lock()
			if !a.protectionFinished {
				a.protectionFinished = true
				if schedulingProtectionSendSelected(ctx, a.account) {
					var ms *int
					if result != nil {
						ms = result.FirstTokenMs
					}
					if key, ok := protectionSessionKey(ctx, selection.groupID, a.account, a.model, a.protocol); ok && a.account != nil {
						now := time.Now()
						e := selection.service.protectionSession(key, now)
						e.mu.Lock()
						if now.Sub(e.last) >= 15*time.Minute || e.threshold != selection.threshold {
							e.slow = 0
						}
						e.threshold = selection.threshold
						if success && ctx.Err() == nil && ms != nil && time.Duration(*ms)*time.Millisecond > selection.threshold {
							e.slow++
						} else {
							e.slow = 0
						}
						e.last = now
						e.mu.Unlock()
					}
				}
			}
			a.mu.Unlock()
		}
	}
	FinishUpstreamCacheGuardAttempt(ctx, evidence, success && ctx.Err() == nil, time.Now())
}

func FinishGatewayUpstreamCacheGuardAttempt(ctx context.Context, result *ForwardResult, err error) {
	var evidence *UpstreamCacheUsageEvidence
	success := err == nil && result != nil && !result.ClientDisconnect
	if result != nil {
		evidence = result.CacheUsageEvidence
		if evidence == nil {
			evidence = result.Usage.CacheUsageEvidence
		}
		if result.ImageCount > 0 || result.AudioUsage != nil {
			success = false
		}
	}
	FinishUpstreamCacheGuardAttempt(ctx, evidence, success && ctx.Err() == nil, time.Now())
}

func cacheUsageWithStreamCompletion(evidence *UpstreamCacheUsageEvidence, completed bool) *UpstreamCacheUsageEvidence {
	if evidence == nil {
		return nil
	}
	out := *evidence
	out.StreamIncomplete = !completed
	return &out
}
