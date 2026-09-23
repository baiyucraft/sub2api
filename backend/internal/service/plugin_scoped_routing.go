package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

type pluginRequestMetadata struct {
	Model, IdentityRevision string
	ConfigRevision          uint64
}
type pluginRequestMetadataKey struct{}

type PluginAdmissionError struct {
	AccountID int64
	Model     string
}

func (e *PluginAdmissionError) Error() string { return "plugin admission unavailable" }

func pluginScopeContains(scope []PluginManagedTarget, accountID int64, model string) bool {
	for _, target := range scope {
		if target.AccountID != accountID {
			continue
		}
		for _, candidate := range target.Models {
			if candidate == model {
				return true
			}
		}
	}
	return false
}

func scopedPluginRoute(installation *PluginInstallation, runtime *pluginRuntime, unavailable string) *pluginRoute {
	scope := installation.ManagedScope
	if scope == nil && pluginRequiresFeature(installation.Manifest, "scoped-routing.v1") {
		scope = []PluginManagedTarget{}
	}
	return &pluginRoute{pluginID: installation.ID, runtime: runtime, rolloutPercent: bindingRollout(installation.Bindings), unavailable: unavailable,
		scope: scope, configRevision: installation.ConfigRevision, oauthLike: pluginRequiresFeature(installation.Manifest, "oauth-like.v1")}
}

func normalizePluginScope(targets []*pluginv1.ManagedTarget) ([]PluginManagedTarget, error) {
	if len(targets) > 10000 {
		return nil, errors.New("plugin scope too large")
	}
	byAccount := make(map[int64]map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil || target.AccountId <= 0 || len(target.Models) == 0 || len(target.Models) > 256 {
			return nil, errors.New("invalid plugin scope")
		}
		if byAccount[target.AccountId] == nil {
			byAccount[target.AccountId] = map[string]struct{}{}
		}
		for _, model := range target.Models {
			if model == "" || len(model) > 256 || strings.TrimSpace(model) != model {
				return nil, errors.New("invalid scoped model")
			}
			byAccount[target.AccountId][model] = struct{}{}
		}
	}
	scope := make([]PluginManagedTarget, 0, len(byAccount))
	for id, models := range byAccount {
		target := PluginManagedTarget{AccountID: id, Models: make([]string, 0, len(models))}
		for model := range models {
			target.Models = append(target.Models, model)
		}
		sort.Strings(target.Models)
		scope = append(scope, target)
	}
	sort.Slice(scope, func(i, j int) bool { return scope[i].AccountID < scope[j].AccountID })
	return scope, nil
}

func (r *pluginRuntime) validateScopedConfig(ctx context.Context, raw []byte) ([]byte, []PluginManagedTarget, error) {
	validation, err := r.api.ValidateConfig(ctx, &pluginv1.ValidateConfigRequest{ConfigJson: raw})
	if err != nil || validation == nil || !validation.Valid || !validation.ScopedRouting {
		return nil, nil, errors.New("plugin scoped configuration validation failed")
	}
	canonical := validation.NormalizedConfigJson
	if len(canonical) == 0 {
		canonical = raw
	}
	if len(canonical) > pluginConfigMaxBytes || !json.Valid(canonical) {
		return nil, nil, errors.New("invalid normalized plugin configuration")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(canonical, &object) != nil || object == nil {
		return nil, nil, errors.New("plugin configuration must be an object")
	}
	scope, err := normalizePluginScope(validation.ManagedTargets)
	return canonical, scope, err
}

func (r *pluginRuntime) applyScopedConfig(ctx context.Context, raw []byte, revision uint64, active bool) error {
	return r.applyAndRememberScopedConfig(ctx, raw, revision, active)
}

// Configuration and its fail-closed scope become authoritative in one transaction.
// A runtime never begins harvesting a merely validated or uncommitted draft.
func (m *PluginManager) saveScopedConfig(ctx context.Context, installation *PluginInstallation, raw json.RawMessage) (json.RawMessage, error) {
	repo, ok := m.repo.(PluginScopeRepository)
	if !ok {
		return nil, errors.New("host scoped configuration storage unavailable")
	}
	m.mu.Lock()
	runtime := m.runtimes[installation.ID]
	m.mu.Unlock()
	temporary := runtime == nil
	if temporary {
		var err error
		installation, err = m.ensureLocalInstallation(ctx, installation)
		if err != nil {
			return nil, err
		}
		runtime, err = m.newRuntime(ctx, installation)
		if err != nil {
			return nil, err
		}
		defer runtime.kill()
	}
	validationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	canonical, scope, err := runtime.validateScopedConfig(validationCtx, raw)
	cancel()
	if err != nil {
		return nil, err
	}
	if len(scope) > 0 {
		if m.accountDirectory == nil {
			return nil, errors.New("account directory unavailable")
		}
		directoryScope := pluginAccountScopeFromManifest(installation.Manifest)
		infos, err := m.accountDirectory.ListPluginAccounts(ctx, directoryScope, PlatformOpenAI, "")
		if err != nil {
			return nil, errors.New("account directory unavailable")
		}
		allowed := map[int64]bool{}
		for _, info := range infos {
			allowed[info.ID] = true
		}
		for _, target := range scope {
			if !allowed[target.AccountID] {
				return nil, errors.New("managed account is outside plugin capability")
			}
		}
	}
	encrypted, err := m.encryptor.Encrypt(string(canonical))
	if err != nil {
		return nil, err
	}
	revision, err := repo.UpdateScopedConfig(ctx, installation.ID, encrypted, installation.BinarySHA256, installation.ConfigRevision, scope)
	if err != nil {
		return nil, err
	}
	updated := *installation
	updated.ConfigEncrypted = encrypted
	updated.ConfigRevision = revision
	updated.ManagedScope = scope
	active := hasEnabledOpenAIBinding(updated.Bindings)
	if active {
		m.route.Store(scopedPluginRoute(&updated, nil, "configuration activating"))
	}
	if !temporary {
		applyCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		err = runtime.applyScopedConfig(applyCtx, canonical, revision, active)
		stop()
		if err != nil {
			runtime.kill()
			m.mu.Lock()
			delete(m.runtimes, updated.ID)
			m.mu.Unlock()
			return nil, errors.New("configuration saved; plugin activation pending")
		}
		if active {
			m.route.Store(scopedPluginRoute(&updated, runtime, ""))
		}
	}
	return canonical, nil
}

// Read the persisted scope before admission so a second instance cannot use a
// stale configuration to bypass a newly enabled target or an explicit disable.
func (m *PluginManager) currentScopedRoute(ctx context.Context) (*pluginRoute, error) {
	route := m.route.Load()
	if m.repo == nil {
		return route, nil
	}
	if route == nil || route.pluginID == 0 {
		return m.persistedEnabledRoute(ctx, route)
	}
	current, err := m.repo.GetByID(ctx, route.pluginID)
	if err != nil {
		return route, err
	}
	if !hasEnabledOpenAIBinding(current.Bindings) {
		// Another instance may have disabled this plugin and enabled another.
		// A stale local route must not hide the new authoritative binding.
		return m.persistedEnabledRoute(ctx, route)
	}
	if current.State != PluginStateEnabled || current.ConfigRevision != route.configRevision || route.runtime == nil || route.runtime.installation.BinarySHA256 != current.BinarySHA256 {
		return scopedPluginRoute(current, nil, "plugin synchronization pending"), nil
	}
	return route, nil
}

func (m *PluginManager) persistedEnabledRoute(ctx context.Context, previous *pluginRoute) (*pluginRoute, error) {
	installations, err := m.repo.List(ctx)
	if err != nil {
		return previous, err
	}
	var current *pluginRoute
	for _, installation := range installations {
		if !hasEnabledOpenAIBinding(installation.Bindings) {
			continue
		}
		if current != nil {
			return nil, errors.New("multiple authoritative plugin bindings")
		}
		current = scopedPluginRoute(installation, nil, "plugin synchronization pending")
	}
	return current, nil
}

func (m *PluginManager) AdmitOpenAIAccount(ctx context.Context, account *Account, outboundModel string) bool {
	if m == nil || account == nil || !account.IsOpenAIOAuthLike() || account.IsShadow() {
		return true
	}
	checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	route, err := m.currentScopedRoute(checkCtx)
	if route == nil {
		return err == nil
	}
	if route.scope == nil {
		return true
	}
	if !pluginScopeContains(route.scope, account.ID, outboundModel) {
		return true
	}
	if err != nil || route.runtime == nil || route.runtime.draining.Load() || route.runtime.client == nil || route.runtime.client.Exited() {
		return false
	}
	identity := PluginAccountIdentityRevision(account)
	response, err := route.runtime.api.AdmitBatch(checkCtx, &pluginv1.AdmitBatchRequest{ConfigRevision: route.configRevision, Candidates: []*pluginv1.AdmissionCandidate{{AccountId: account.ID, OutboundModel: outboundModel, IdentityRevision: identity}}})
	if err != nil || response == nil || response.ConfigRevision != route.configRevision || len(response.Decisions) != 1 {
		return false
	}
	decision := response.Decisions[0]
	return decision != nil && decision.AccountId == account.ID && decision.OutboundModel == outboundModel && decision.IdentityRevision == identity && decision.Allowed
}

func (s *OpenAIGatewayService) preparePluginRequest(ctx context.Context, account *Account, model string, req *http.Request) error {
	if req == nil || account == nil {
		return nil
	}
	metadata := pluginRequestMetadata{Model: model, IdentityRevision: PluginAccountIdentityRevision(account)}
	if s.pluginManager != nil {
		if route := s.pluginManager.route.Load(); route != nil {
			metadata.ConfigRevision = route.configRevision
		}
	}
	*req = *req.WithContext(context.WithValue(req.Context(), pluginRequestMetadataKey{}, metadata))
	return nil
}

func (m *PluginManager) roundTripScoped(ctx context.Context, request *http.Request, proxyURL string, account *Account) (*http.Response, bool, error) {
	if account == nil || !account.IsOpenAIOAuthLike() || account.IsShadow() {
		return nil, false, nil
	}
	metadata, _ := ctx.Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	route, err := m.currentScopedRoute(ctx)
	if route == nil {
		if err != nil {
			return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
		}
		return nil, false, nil
	}
	if metadata.Model == "" {
		for _, target := range route.scope {
			if target.AccountID == account.ID {
				return nil, true, &PluginAdmissionError{AccountID: account.ID}
			}
		}
		return nil, false, nil
	}
	if !pluginScopeContains(route.scope, account.ID, metadata.Model) {
		return nil, false, nil
	}
	if err != nil || !m.AdmitOpenAIAccount(ctx, account, metadata.Model) || route.runtime == nil {
		return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
	}
	metadata.ConfigRevision = route.configRevision
	ctx = context.WithValue(ctx, pluginRequestMetadataKey{}, metadata)
	if !route.runtime.beginRequest() {
		return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
	}
	guard, ok := m.repo.(PluginRequestGuardRepository)
	if !ok {
		route.runtime.finishRequest()
		return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
	}
	requestID := rand.Text()
	if err := guard.BeginPluginRequest(ctx, route.pluginID, route.runtime.installation.BinarySHA256, route.configRevision, requestID); err != nil {
		route.runtime.finishRequest()
		return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
	}
	finish := func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		// Failure intentionally retains the marker; maintenance must not guess
		// whether a request has completed when its accounting is unavailable.
		_ = guard.EndPluginRequest(releaseCtx, route.pluginID, requestID)
	}
	if err := route.runtime.registerRequestCompletion(requestID, finish); err != nil {
		finish()
		route.runtime.finishRequest()
		return nil, true, &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
	}
	ctx = withPluginRequestID(ctx, requestID)
	response, err := route.runtime.roundTrip(ctx, request, proxyURL, account)
	if err != nil {
		var transportErr *PluginTransportError
		if errors.As(err, &transportErr) && !transportErr.RequestSent && strings.HasPrefix(transportErr.Code, "plugin_admission_") {
			err = &PluginAdmissionError{AccountID: account.ID, Model: metadata.Model}
		}
	}
	return response, true, err
}
