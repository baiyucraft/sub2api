package service

import (
	"context"
	"sync"

	"github.com/gin-gonic/gin"
)

type cacheGuardEntryDiagnosticsKey struct{}

// Entry diagnostics also cover requests which cannot install a sampling
// attempt. They never grant admission, create a session or query the store.
type cacheGuardEntryDiagnostics struct {
	mu                        sync.Mutex
	groupID                   int64
	event                     string
	accountID, keyID          int64
	platform, model, protocol string
	sent                      bool
}

func WithUpstreamCacheGuardSamplingDiagnostics(ctx context.Context, groupID *int64, c *gin.Context, body []byte, stream bool) context.Context {
	if ctx == nil || groupID == nil || *groupID <= 0 {
		return ctx
	}
	path := ""
	if c != nil && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}
	event := "eligible_text_request"
	switch {
	case !UpstreamCacheGuardTextRequest(path, body):
		event = "skipped_unsupported_request"
	case !stream:
		event = "skipped_non_streaming"
	case UpstreamCacheGuardLogicalSession(c, body) == "":
		event = "skipped_missing_reliable_session"
	}
	return context.WithValue(ctx, cacheGuardEntryDiagnosticsKey{}, &cacheGuardEntryDiagnostics{groupID: *groupID, event: event})
}

func withCacheGuardWSTurnEntryDiagnostics(ctx context.Context, body []byte) context.Context {
	seed, _ := ctx.Value(cacheGuardEntryDiagnosticsKey{}).(*cacheGuardEntryDiagnostics)
	if seed == nil {
		return ctx
	}
	event := "eligible_text_request"
	if !UpstreamCacheGuardTextRequest("/v1/responses", body) {
		event = "skipped_unsupported_request"
	} else if session, _ := ctx.Value(cacheGuardLogicalSessionKey{}).(string); session == "" && UpstreamCacheGuardLogicalSession(nil, body) == "" {
		event = "skipped_missing_reliable_session"
	}
	return context.WithValue(ctx, cacheGuardEntryDiagnosticsKey{}, &cacheGuardEntryDiagnostics{groupID: seed.groupID, event: event})
}

func prepareCacheGuardEntryDiagnostics(ctx context.Context, account *Account, model, protocol string) {
	if ctx == nil || account == nil {
		return
	}
	d, _ := ctx.Value(cacheGuardEntryDiagnosticsKey{}).(*cacheGuardEntryDiagnostics)
	if d == nil {
		return
	}
	d.mu.Lock()
	if !d.sent {
		d.accountID, d.keyID = account.ID, accountUpstreamKeyID(account)
		d.platform, d.model, d.protocol = account.Platform, model, protocol
	}
	d.mu.Unlock()
}

// Same-account transport retries share one entry observation. Counts describe
// retained process observations, not billing records or persisted facts.
func recordCacheGuardEntryDiagnostics(ctx context.Context, account *Account) {
	d, _ := ctx.Value(cacheGuardEntryDiagnosticsKey{}).(*cacheGuardEntryDiagnostics)
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.sent || d.accountID != account.ID || d.keyID != accountUpstreamKeyID(account) || d.protocol == "" || d.model == "" {
		d.mu.Unlock()
		return
	}
	d.sent = true
	groupID, event := d.groupID, d.event
	d.mu.Unlock()
	if runner := currentUpstreamCacheGuardRunner(); runner != nil {
		runner.diagnostics.record(event, groupID, account.ID, accountUpstreamKeyID(account))
	}
}
