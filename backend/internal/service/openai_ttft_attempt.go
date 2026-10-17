package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type openAITTFTAttemptKey struct{}

// openAITTFTPhysicalAttemptKey identifies the physical send represented by a
// derived transport context. The root attempt remains in the request context
// so the handler can finish the latest physical send after an in-place retry,
// while late callbacks retain the old derived context and its own clock.
type openAITTFTPhysicalAttemptKey struct{}

const openAITTFTResponseContextKey = "openai_ttft_response_context"

// This holder belongs to one physical forwarding attempt. A logical request
// survives account/transport retries; a WS response.create starts another one.
// It is independent of cache admission and of the presence of session markers.
type openAITTFTAttempt struct {
	mu                                     sync.Mutex
	service                                *OpenAIGatewayService
	parent                                 *openAITTFTAttempt
	groupID                                int64
	apiKeyID                               int64
	attemptID, logicalRequestID, sessionID string
	account                                *Account
	accountID, upstreamKeyID               int64
	model, protocol, runtimeID             string
	preparedModel, preparedProtocol        string
	algorithmVersion                       string
	policyGeneration                       uint64
	sentAt, firstTokenAt                   time.Time
	finished                               bool
	active                                 *openAITTFTAttempt
}

func WithOpenAITTFTGuardAttempt(ctx context.Context, svc *OpenAIGatewayService, apiKeyID int64, groupID *int64) context.Context {
	if ctx == nil || svc == nil || groupID == nil || *groupID <= 0 || apiKeyID <= 0 {
		return ctx
	}
	ctx = WithUpstreamCacheGuardScheduling(ctx, apiKeyID)
	id, _ := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	if id.RequestID == "" {
		return ctx
	}
	session, _ := ctx.Value(cacheGuardLogicalSessionKey{}).(string)
	if session != "" {
		// Account/model/group migrations never manufacture a second session.
		session = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d:%s", apiKeyID, session))))
	}
	return context.WithValue(ctx, openAITTFTAttemptKey{}, &openAITTFTAttempt{
		service: svc, groupID: *groupID, apiKeyID: apiKeyID, attemptID: newCacheGuardID(), logicalRequestID: id.RequestID, sessionID: session,
		algorithmVersion: openAITTFTGuardAlgorithmVersion,
		active:           nil,
	})
}

// currentOpenAITTFTAttempt resolves a physical attempt from a transport
// context. A handler context has only the logical root and therefore resolves
// to the latest active physical attempt.
func currentOpenAITTFTAttempt(ctx context.Context) *openAITTFTAttempt {
	if ctx == nil {
		return nil
	}
	if physical, _ := ctx.Value(openAITTFTPhysicalAttemptKey{}).(*openAITTFTAttempt); physical != nil {
		return physical
	}
	root, _ := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	if root == nil {
		return nil
	}
	root.mu.Lock()
	active := root.active
	root.mu.Unlock()
	if active != nil {
		return active
	}
	return root
}

// prepareOpenAITTFTPhysicalAttempt is called once by the shared OpenAI
// transport entry for every actual outbound send. A retry keeps the logical
// request and reliable session identity but receives a fresh attempt clock and
// ID. The previous send is closed as an unsuccessful/no-TTFT fact before the
// new send can begin, so a late callback can never mutate the new attempt.
func prepareOpenAITTFTPhysicalAttempt(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	root, _ := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	if root == nil {
		return ctx
	}
	root.mu.Lock()
	active := root.active
	if active == nil {
		active = root
		root.active = root
	}
	needsRotation := false
	if active == root {
		needsRotation = !active.sentAt.IsZero() // root.mu is already held
	} else {
		active.mu.Lock()
		needsRotation = !active.sentAt.IsZero()
		active.mu.Unlock()
	}
	if !needsRotation {
		root.mu.Unlock()
		return context.WithValue(ctx, openAITTFTPhysicalAttemptKey{}, active)
	}
	child := &openAITTFTAttempt{
		service:          root.service,
		parent:           root,
		groupID:          root.groupID,
		apiKeyID:         root.apiKeyID,
		attemptID:        newCacheGuardID(),
		logicalRequestID: root.logicalRequestID,
		sessionID:        root.sessionID,
		algorithmVersion: openAITTFTGuardAlgorithmVersion,
		model:            root.preparedModel,
		protocol:         root.preparedProtocol,
		active:           nil,
	}
	root.active = child
	root.mu.Unlock()
	// Finish the previous physical attempt outside the root lock. This records
	// an unsuccessful terminal fact without touching cache-cohort accounting.
	finishOpenAITTFTPhysicalAttempt(context.WithValue(ctx, openAITTFTPhysicalAttemptKey{}, active), nil, false)
	return context.WithValue(ctx, openAITTFTPhysicalAttemptKey{}, child)
}

// Called only after local admission, at the actual HTTP/plugin/frame write.
// send_attempted deliberately does not assert receipt by the upstream.
func markOpenAITTFTGuardSend(ctx context.Context, account *Account, model, protocol string) {
	a := currentOpenAITTFTAttempt(ctx)
	if a == nil || account == nil || account.Platform != PlatformOpenAI || !schedulingProtectionSendSelected(ctx, account) {
		return
	}
	if model == "" {
		if cached, _ := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt); cached != nil {
			cached.mu.Lock()
			model, protocol = cached.model, cached.protocol
			cached.mu.Unlock()
		}
	}
	if model == "" {
		metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
		model = metadata.Model
	}
	if model == "" {
		a.mu.Lock()
		model = a.model
		if a.protocol != "" {
			protocol = a.protocol
		}
		a.mu.Unlock()
	}
	// All model sources reaching this boundary (prepared cache holder, plugin
	// metadata, or the transport fallback) already describe the actual outbound
	// model. Reapplying account model_mapping here would map an alias chain a
	// second time (for example public -> B -> C), putting the fact under a key
	// different from the model that was sent upstream.
	model = normalizeOpenAIAccountModelTransientModel(model)
	if model == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sentAt.IsZero() || a.finished {
		return
	}
	a.account, a.model, a.protocol = account, model, protocol
	a.accountID, a.upstreamKeyID = account.ID, accountUpstreamKeyID(account)
	a.runtimeID, a.policyGeneration = a.service.OpenAITTFTGuardObservationIdentity(a.groupID)
	a.sentAt = time.Now()
	a.service.OpenAITTFTGuardMarkCurrentKey(account.ID, accountUpstreamKeyID(account), a.sentAt)
	a.service.recordTTFTExecution(ctx, OpenAITTFTGuardObservation{GroupID: a.groupID, AccountID: account.ID, UpstreamKeyID: accountUpstreamKeyID(account), Model: model, AttemptID: a.attemptID, LogicalRequestID: a.logicalRequestID, SessionID: a.sessionID, RuntimeID: a.runtimeID, AlgorithmVersion: a.algorithmVersion, PolicyGeneration: a.policyGeneration, SentAt: a.sentAt, Trial: openAITTFTAttemptIsTrial(ctx)}, protocol)
}

func openAITTFTAttemptIsTrial(ctx context.Context) bool {
	p, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection)
	return p != nil && p.trial != nil && p.trial.sent.Load()
}

// Record the event at parsing/visible-output time, never at stream completion.
func markOpenAITTFTGuardFirstToken(ctx context.Context) {
	if ctx == nil {
		return
	}
	a := currentOpenAITTFTAttempt(ctx)
	if a == nil {
		return
	}
	var observation OpenAITTFTGuardObservation
	a.mu.Lock()
	if !a.sentAt.IsZero() && a.firstTokenAt.IsZero() && !a.finished {
		a.firstTokenAt = time.Now()
		ms := int(a.firstTokenAt.Sub(a.sentAt).Milliseconds())
		if ms < 1 {
			ms = 1
		}
		observation = OpenAITTFTGuardObservation{GroupID: a.groupID, AccountID: a.accountID, UpstreamKeyID: a.upstreamKeyID, Model: a.model, AttemptID: a.attemptID, LogicalRequestID: a.logicalRequestID, SessionID: a.sessionID, RuntimeID: a.runtimeID, AlgorithmVersion: a.algorithmVersion, PolicyGeneration: a.policyGeneration, SentAt: a.sentAt, FirstTokenAt: a.firstTokenAt, TTFTMs: ms, Trial: openAITTFTAttemptIsTrial(ctx), FirstTokenOnly: true}
	}
	a.mu.Unlock()
	if !observation.FirstTokenAt.IsZero() {
		a.service.ReportOpenAITTFTGuardObservation(ctx, observation)
	}
}

func markOpenAITTFTGuardFirstTokenForRequest(c *gin.Context) {
	if c == nil {
		return
	}
	if responseCtx, ok := c.Get(openAITTFTResponseContextKey); ok {
		if ctx, ok := responseCtx.(context.Context); ok {
			markOpenAITTFTGuardFirstToken(ctx)
			return
		}
	}
	if c.Request != nil {
		markOpenAITTFTGuardFirstToken(c.Request.Context())
	}
}

// bindOpenAITTFTResponseContext installs the physical response context for
// stream parsers that report first visible output through a gin context.
func bindOpenAITTFTResponseContext(c *gin.Context, response *http.Response) func() {
	if c == nil || response == nil || response.Request == nil {
		return func() {}
	}
	if _, ok := response.Request.Context().Value(openAITTFTPhysicalAttemptKey{}).(*openAITTFTAttempt); ok {
		previous, existed := c.Get(openAITTFTResponseContextKey)
		c.Set(openAITTFTResponseContextKey, response.Request.Context())
		return func() {
			if existed {
				c.Set(openAITTFTResponseContextKey, previous)
			} else {
				c.Set(openAITTFTResponseContextKey, nil)
			}
		}
	}
	return func() {}
}

// bindOpenAITTFTResponseAttempt makes stream parsers use the exact physical
// request context returned by the transport. This prevents an old response's
// callback from resolving through the logical root to a newer retry attempt.
func bindOpenAITTFTResponseAttempt(c *gin.Context, response *http.Response) func() {
	if c == nil || c.Request == nil || response == nil || response.Request == nil {
		return func() {}
	}
	if _, ok := response.Request.Context().Value(openAITTFTPhysicalAttemptKey{}).(*openAITTFTAttempt); !ok {
		return func() {}
	}
	previous := c.Request
	c.Request = c.Request.WithContext(response.Request.Context())
	return func() { c.Request = previous }
}

func openAITTFTResponseContext(response *http.Response, fallback context.Context) context.Context {
	if response != nil && response.Request != nil {
		if _, ok := response.Request.Context().Value(openAITTFTPhysicalAttemptKey{}).(*openAITTFTAttempt); ok {
			return response.Request.Context()
		}
	}
	return fallback
}

func openAITTFTGuardPreparedModel(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	a := currentOpenAITTFTAttempt(ctx)
	if a == nil {
		return ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.model
}

func finishOpenAITTFTGuardAttempt(ctx context.Context, result *OpenAIForwardResult, success bool) {
	finishOpenAITTFTPhysicalAttempt(ctx, result, success)
}

func finishOpenAITTFTPhysicalAttempt(ctx context.Context, result *OpenAIForwardResult, success bool) {
	a := currentOpenAITTFTAttempt(ctx)
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.finished {
		a.mu.Unlock()
		return
	}
	a.finished = true
	if a.accountID <= 0 || a.sentAt.IsZero() {
		a.mu.Unlock()
		return
	}
	observed := OpenAITTFTGuardObservation{GroupID: a.groupID, AccountID: a.accountID, UpstreamKeyID: a.upstreamKeyID, Model: a.model,
		AttemptID: a.attemptID, LogicalRequestID: a.logicalRequestID, SessionID: a.sessionID, RuntimeID: a.runtimeID, AlgorithmVersion: a.algorithmVersion, PolicyGeneration: a.policyGeneration,
		SentAt: a.sentAt, FirstTokenAt: a.firstTokenAt, CompletedAt: time.Now(), Success: success, Canceled: ctx.Err() != nil}
	if result != nil {
		if result.FirstTokenMs != nil {
			observed.TTFTMs = *result.FirstTokenMs
		}
		if observed.FirstTokenAt.IsZero() && !result.FirstTokenAt.IsZero() {
			observed.FirstTokenAt = result.FirstTokenAt
		}
	}
	// Parser durations can include preparation before the transport send. The
	// event and send timestamps share the physical-attempt clock and take
	// precedence; completion time must never manufacture a first-token event.
	if !observed.FirstTokenAt.IsZero() {
		observed.TTFTMs = int(observed.FirstTokenAt.Sub(observed.SentAt).Milliseconds())
		if observed.FirstTokenAt.After(observed.SentAt) && observed.TTFTMs < 1 {
			observed.TTFTMs = 1
		}
	}
	if selection, _ := ctx.Value(protectionSelectionKey{}).(*protectionSelection); selection != nil {
		observed.Trial = selection.trial != nil && selection.trial.sent.Load()
	}
	svc, protocol := a.service, a.protocol
	a.mu.Unlock()
	decision := ttftRecoveryDecision{}
	if !svc.reportOpenAITTFTGuardObservation(ctx, observed, &decision) || decision.reason == "" {
		decision = ttftRecoveryDecision{reason: "observation_rejected"}
	}
	svc.recordTTFTExecution(ctx, observed, protocol, decision)
}
