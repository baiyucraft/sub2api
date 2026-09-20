package service

import (
	"context"
	"strings"
	"sync"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

type openAISchedulingAdmissionKey struct {
	accountID int64
	model     string
	identity  string
}

type openAISchedulingAdmissionContextKey struct{}

// Selection hints live only for one scheduling pass. The transport performs its
// own authoritative admission and never consumes this table.
type openAISchedulingAdmission struct {
	manager        *PluginManager
	requestedModel string
	requireCompact bool
	mu             sync.Mutex
	allowed        map[openAISchedulingAdmissionKey]bool
}

func (s *OpenAIGatewayService) withOpenAISchedulingAdmission(ctx context.Context, model string, requireCompact bool) context.Context {
	model = strings.TrimSpace(model)
	if s == nil || s.pluginManager == nil || model == "" {
		return ctx
	}
	if current, ok := ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission); ok &&
		current.manager == s.pluginManager && current.requestedModel == model && current.requireCompact == requireCompact {
		return ctx
	}
	return context.WithValue(ctx, openAISchedulingAdmissionContextKey{}, &openAISchedulingAdmission{
		manager: s.pluginManager, requestedModel: model, requireCompact: requireCompact,
		allowed: make(map[openAISchedulingAdmissionKey]bool),
	})
}

func (s *OpenAIGatewayService) prefetchOpenAISchedulingAdmission(ctx context.Context, accounts []Account, model string, requireCompact bool) context.Context {
	ctx = s.withOpenAISchedulingAdmission(ctx, model, requireCompact)
	batch, _ := ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission)
	if batch == nil || s == nil || batch.manager != s.pluginManager || strings.TrimSpace(model) == "" {
		return ctx
	}
	keys := make([]openAISchedulingAdmissionKey, 0, len(accounts))
	for i := range accounts {
		if key, ok := s.openAISchedulingAdmissionKey(&accounts[i], batch.requestedModel, batch.requireCompact); ok {
			keys = append(keys, key)
		}
	}
	batch.prefetch(ctx, keys)
	return ctx
}

func (s *OpenAIGatewayService) openAISchedulingAdmissionKey(account *Account, model string, requireCompact bool) (openAISchedulingAdmissionKey, bool) {
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsShadow() || strings.TrimSpace(model) == "" {
		return openAISchedulingAdmissionKey{}, false
	}
	return openAISchedulingAdmissionKey{account.ID, s.openAIAccountOutboundModel(account, model, requireCompact), PluginAccountIdentityRevision(account)}, true
}

func (s *OpenAIGatewayService) isOpenAISchedulingAdmissionBlocked(ctx context.Context, account *Account, model string, requireCompact bool) bool {
	if s == nil || s.pluginManager == nil {
		return false
	}
	model = strings.TrimSpace(model)
	batch, _ := ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission)
	if batch == nil || batch.manager != s.pluginManager || batch.requestedModel != model {
		ctx = s.withOpenAISchedulingAdmission(ctx, model, requireCompact)
		batch, _ = ctx.Value(openAISchedulingAdmissionContextKey{}).(*openAISchedulingAdmission)
	}
	if batch == nil || model == "" {
		return false
	}
	// Eligibility helpers deliberately defer compact capability checks by passing
	// false. Admission must still use the actual outbound model of this pass.
	key, eligible := s.openAISchedulingAdmissionKey(account, model, batch.requireCompact)
	if !eligible {
		return false
	}
	batch.prefetch(ctx, []openAISchedulingAdmissionKey{key})
	batch.mu.Lock()
	defer batch.mu.Unlock()
	return !batch.allowed[key]
}

func (b *openAISchedulingAdmission) prefetch(ctx context.Context, keys []openAISchedulingAdmissionKey) {
	b.mu.Lock()
	defer b.mu.Unlock()
	missing := make([]openAISchedulingAdmissionKey, 0, len(keys))
	seen := make(map[openAISchedulingAdmissionKey]bool, len(keys))
	for _, key := range keys {
		if _, exists := b.allowed[key]; exists || seen[key] {
			continue
		}
		seen[key] = true
		missing = append(missing, key)
	}
	if len(missing) == 0 {
		return
	}
	for key, allowed := range admitOpenAISchedulingBatch(ctx, b.manager, missing) {
		b.allowed[key] = allowed
	}
}

func admitOpenAISchedulingBatch(ctx context.Context, manager *PluginManager, keys []openAISchedulingAdmissionKey) map[openAISchedulingAdmissionKey]bool {
	allowed := make(map[openAISchedulingAdmissionKey]bool, len(keys))
	for _, key := range keys {
		allowed[key] = false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	route, err := manager.currentScopedRoute(checkCtx)
	managed := make(map[openAISchedulingAdmissionKey]bool)
	candidates := make([]*pluginv1.AdmissionCandidate, 0, len(keys))
	for _, key := range keys {
		if route == nil {
			allowed[key] = err == nil
		} else if route.scope == nil || !pluginScopeContains(route.scope, key.accountID, key.model) {
			allowed[key] = true
		} else {
			managed[key] = true
			candidates = append(candidates, &pluginv1.AdmissionCandidate{AccountId: key.accountID, OutboundModel: key.model, IdentityRevision: key.identity})
		}
	}
	if len(candidates) == 0 || err != nil || checkCtx.Err() != nil || route.runtime == nil ||
		route.runtime.draining.Load() || route.runtime.client == nil || route.runtime.client.Exited() || route.runtime.api == nil {
		return allowed
	}
	response, err := route.runtime.api.AdmitBatch(checkCtx, &pluginv1.AdmitBatchRequest{ConfigRevision: route.configRevision, Candidates: candidates})
	if err != nil || checkCtx.Err() != nil || response == nil || response.ConfigRevision != route.configRevision || len(response.Decisions) != len(candidates) {
		return allowed
	}
	decisions := make(map[openAISchedulingAdmissionKey]bool, len(candidates))
	for _, decision := range response.Decisions {
		if decision == nil {
			return allowed
		}
		key := openAISchedulingAdmissionKey{decision.AccountId, decision.OutboundModel, decision.IdentityRevision}
		if _, duplicate := decisions[key]; !managed[key] || duplicate {
			return allowed
		}
		decisions[key] = decision.Allowed
	}
	for key, decision := range decisions {
		allowed[key] = decision
	}
	return allowed
}
