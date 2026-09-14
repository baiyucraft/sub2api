package service

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// UpstreamKeyModelRouteSource identifies who owns a model capability route.
const (
	UpstreamKeyModelRouteSourceAuto   = "auto"
	UpstreamKeyModelRouteSourceManual = "manual"
	UpstreamKeyModelRouteSourceLegacy = "legacy"
)

var ErrUpstreamKeyModelRouteNotFound = infraerrors.NotFound(
	"UPSTREAM_KEY_MODEL_ROUTE_NOT_FOUND",
	"upstream key model route not found",
)

// UpstreamKeyModelRouteStatus describes whether a route can participate in
// request dispatch. Unknown and ambiguous routes are intentionally retained
// for administrator diagnostics but are not schedulable.
const (
	UpstreamKeyModelRouteStatusAvailable   = "available"
	UpstreamKeyModelRouteStatusUnknown     = "unknown"
	UpstreamKeyModelRouteStatusAmbiguous   = "ambiguous"
	UpstreamKeyModelRouteStatusUnsupported = "unsupported"
	UpstreamKeyModelRouteStatusStale       = "stale"
	UpstreamKeyModelRouteStatusDisabled    = "disabled"
)

// UpstreamKeyModelRoute is the model-level capability of one physical key.
// It deliberately contains no credential or endpoint secret.
type UpstreamKeyModelRoute struct {
	ID             int64      `json:"id"`
	UpstreamKeyID  int64      `json:"upstream_key_id"`
	KeyName        string     `json:"key_name,omitempty"`
	PublicModel    string     `json:"public_model"`
	UpstreamModel  string     `json:"upstream_model"`
	TargetPlatform string     `json:"target_platform"`
	APIProtocol    string     `json:"api_protocol,omitempty"`
	Source         string     `json:"source"`
	Enabled        bool       `json:"enabled"`
	Priority       int        `json:"priority"`
	Status         string     `json:"status"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	LastError      *string    `json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// EffectiveUpstreamTarget is the request-local routing decision for one
// physical account. It intentionally carries no credential: authentication,
// concurrency, RPM and health state remain owned by AccountID/UpstreamKeyID.
type EffectiveUpstreamTarget struct {
	AccountID        int64
	UpstreamKeyID    *int64
	TargetPlatform   string
	PublicModel      string
	UpstreamModel    string
	APIProtocol      string
	UpstreamEndpoint string
	RouteSource      string
}

func (r UpstreamKeyModelRoute) IsSchedulable() bool {
	return r.Enabled &&
		strings.EqualFold(strings.TrimSpace(r.Status), UpstreamKeyModelRouteStatusAvailable) &&
		IsConcreteRequestPlatform(r.TargetPlatform) &&
		strings.TrimSpace(r.PublicModel) != ""
}

// ResolveUpstreamModelRoute performs an exact public-model lookup. Once a key
// has route rows, unmatched/unknown rows fail closed instead of falling back to
// the key's legacy platform and accidentally exposing a model on the wrong
// provider.
func (a *Account) ResolveUpstreamModelRoute(publicModel, targetPlatform string) (UpstreamKeyModelRoute, bool) {
	if a == nil || len(a.UpstreamModelRoutes) == 0 {
		return UpstreamKeyModelRoute{}, false
	}
	publicModel = strings.TrimSpace(publicModel)
	targetPlatform = strings.ToLower(strings.TrimSpace(targetPlatform))
	var selected *UpstreamKeyModelRoute
	for i := range a.UpstreamModelRoutes {
		route := &a.UpstreamModelRoutes[i]
		if !route.IsSchedulable() || !strings.EqualFold(strings.TrimSpace(route.PublicModel), publicModel) {
			continue
		}
		if targetPlatform != "" && !strings.EqualFold(strings.TrimSpace(route.TargetPlatform), targetPlatform) {
			continue
		}
		if selected == nil || route.Priority < selected.Priority ||
			(route.Priority == selected.Priority && route.ID < selected.ID) {
			selected = route
		}
	}
	if selected == nil {
		return UpstreamKeyModelRoute{}, false
	}
	return *selected, true
}

func (a *Account) HasUpstreamModelRoutes() bool {
	return a != nil && len(a.UpstreamModelRoutes) > 0
}

func (a *Account) HasRouteTargetPlatform(platform string) bool {
	if a == nil {
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	for i := range a.UpstreamModelRoutes {
		route := a.UpstreamModelRoutes[i]
		if route.IsSchedulable() && strings.EqualFold(strings.TrimSpace(route.TargetPlatform), platform) {
			return true
		}
	}
	return false
}

// ResolveEffectiveUpstreamTarget resolves a model route and retains the legacy
// platform + model_mapping behavior only for accounts that have no route data.
func (a *Account) ResolveEffectiveUpstreamTarget(publicModel, targetPlatform string) (EffectiveUpstreamTarget, bool) {
	if a == nil {
		return EffectiveUpstreamTarget{}, false
	}
	publicModel = strings.TrimSpace(publicModel)
	targetPlatform = strings.ToLower(strings.TrimSpace(targetPlatform))
	if route, ok := a.ResolveUpstreamModelRoute(publicModel, targetPlatform); ok {
		upstreamModel := strings.TrimSpace(route.UpstreamModel)
		if upstreamModel == "" {
			upstreamModel = publicModel
		}
		protocol := strings.TrimSpace(route.APIProtocol)
		if protocol == "" {
			protocol = strings.TrimSpace(a.GetCredential("api_protocol"))
		}
		if protocol == "" {
			protocol = a.GetAPIProtocol()
		}
		return EffectiveUpstreamTarget{
			AccountID: a.ID, UpstreamKeyID: a.UpstreamKeyID,
			TargetPlatform: strings.ToLower(strings.TrimSpace(route.TargetPlatform)),
			PublicModel:    publicModel, UpstreamModel: upstreamModel,
			APIProtocol: protocol, UpstreamEndpoint: strings.TrimSpace(a.GetCredential("base_url")),
			RouteSource: strings.TrimSpace(route.Source),
		}, true
	}
	if a.HasUpstreamModelRoutes() {
		return EffectiveUpstreamTarget{}, false
	}
	// Legacy mixed scheduling intentionally lets an Antigravity account serve
	// Anthropic/Gemini groups without a model-route row. Keep the physical
	// account platform as the effective target so the existing Antigravity
	// forwarding path remains selected; model-level routes are still required
	// for all other cross-platform assignments.
	if targetPlatform != "" && !strings.EqualFold(a.Platform, targetPlatform) {
		mixedAntigravity := strings.EqualFold(strings.TrimSpace(a.Platform), PlatformAntigravity) &&
			a.IsMixedSchedulingEnabled() &&
			(strings.EqualFold(targetPlatform, PlatformAnthropic) || strings.EqualFold(targetPlatform, PlatformGemini))
		if !mixedAntigravity {
			return EffectiveUpstreamTarget{}, false
		}
	}
	return EffectiveUpstreamTarget{
		AccountID: a.ID, UpstreamKeyID: a.UpstreamKeyID,
		TargetPlatform: strings.ToLower(strings.TrimSpace(a.Platform)),
		PublicModel:    publicModel, UpstreamModel: a.GetMappedModel(publicModel),
		APIProtocol: a.GetAPIProtocol(), UpstreamEndpoint: strings.TrimSpace(a.GetCredential("base_url")),
		RouteSource: UpstreamKeyModelRouteSourceLegacy,
	}, true
}

// WithEffectiveUpstreamTarget returns a request-local shallow account copy.
// It never mutates scheduler/cache objects shared by concurrent requests.
func (a *Account) WithEffectiveUpstreamTarget(publicModel, targetPlatform string) (*Account, bool) {
	target, ok := a.ResolveEffectiveUpstreamTarget(publicModel, targetPlatform)
	if !ok {
		return nil, false
	}
	copy := *a
	copy.EffectiveUpstreamTarget = &target
	return &copy, true
}

func (a *Account) EffectivePlatform() string {
	if a != nil && a.EffectiveUpstreamTarget != nil && strings.TrimSpace(a.EffectiveUpstreamTarget.TargetPlatform) != "" {
		return strings.ToLower(strings.TrimSpace(a.EffectiveUpstreamTarget.TargetPlatform))
	}
	if a == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(a.Platform))
}

func (a *Account) EffectiveModel(requestedModel string) string {
	if a != nil && a.EffectiveUpstreamTarget != nil && strings.TrimSpace(a.EffectiveUpstreamTarget.UpstreamModel) != "" {
		return strings.TrimSpace(a.EffectiveUpstreamTarget.UpstreamModel)
	}
	if a == nil {
		return requestedModel
	}
	return a.GetMappedModel(requestedModel)
}

func (a *Account) EffectiveAPIProtocol() string {
	if a != nil && a.EffectiveUpstreamTarget != nil && strings.TrimSpace(a.EffectiveUpstreamTarget.APIProtocol) != "" {
		return strings.TrimSpace(a.EffectiveUpstreamTarget.APIProtocol)
	}
	if a == nil {
		return APIProtocolChatCompletions
	}
	return a.GetAPIProtocol()
}

// EffectiveUpstreamEndpoint returns the request-local endpoint selected with a
// model route. The boolean distinguishes a routed request with a deliberately
// missing endpoint from an ordinary legacy account, allowing callers to fail
// closed instead of substituting a provider default for a mixed physical key.
func (a *Account) EffectiveUpstreamEndpoint() (string, bool) {
	if a == nil || a.EffectiveUpstreamTarget == nil {
		return "", false
	}
	return strings.TrimSpace(a.EffectiveUpstreamTarget.UpstreamEndpoint), true
}

// NormalizeUpstreamKeyModelRouteAPIProtocol validates the optional per-route
// protocol override. An empty value inherits the physical account protocol.
// Zhipu has no native Responses endpoint, so an explicit Responses override is
// rejected rather than leaving a route that can only fail at runtime.
func NormalizeUpstreamKeyModelRouteAPIProtocol(platform, protocol string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	switch protocol {
	case "", APIProtocolAdaptive, APIProtocolChatCompletions, APIProtocolResponses, APIProtocolAnthropic:
	default:
		return "", infraerrors.BadRequest("UPSTREAM_KEY_MODEL_ROUTE_PROTOCOL_INVALID", "upstream key model route API protocol is invalid")
	}
	if platform == PlatformZhipu && protocol == APIProtocolResponses {
		return "", infraerrors.BadRequest("UPSTREAM_KEY_MODEL_ROUTE_PROTOCOL_UNSUPPORTED", "Zhipu model routes do not support the Responses protocol")
	}
	return protocol, nil
}

// UpstreamKeyModelRouteRepository is optional on UpstreamConfigRepository so
// lightweight repositories used by existing tests remain source compatible.
// Production repositories implement it to persist and retrieve model routes.
type UpstreamKeyModelRouteRepository interface {
	ListUpstreamKeyModelRoutes(ctx context.Context, keyID int64) ([]UpstreamKeyModelRoute, error)
	ListUpstreamKeyModelRoutesByConfig(ctx context.Context, configID int64) ([]UpstreamKeyModelRoute, error)
	SyncUpstreamKeyModelRoutes(ctx context.Context, keyID int64, routes []UpstreamKeyModelRoute, complete bool, observedAt time.Time) error
	SaveUpstreamKeyModelRoute(ctx context.Context, route *UpstreamKeyModelRoute) error
	RestoreUpstreamKeyModelRouteAuto(ctx context.Context, route *UpstreamKeyModelRoute) error
	DeleteUpstreamKeyModelRoute(ctx context.Context, keyID, routeID int64) error
}

type upstreamKeyModelRouteSyncRepository interface {
	SyncUpstreamKeyModelRoutes(ctx context.Context, keyID int64, routes []UpstreamKeyModelRoute, complete bool, observedAt time.Time) error
}

// ListKeyModelRoutes returns the model routes for a key when the configured
// repository supports model-level routing.
func (s *UpstreamConfigService) ListKeyModelRoutes(ctx context.Context, keyID int64) ([]UpstreamKeyModelRoute, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUpstreamConfigNotFound
	}
	repo, ok := s.repo.(UpstreamKeyModelRouteRepository)
	if !ok {
		return nil, nil
	}
	return repo.ListUpstreamKeyModelRoutes(ctx, keyID)
}

// ListModelRoutes returns all model routes belonging to an upstream config.
func (s *UpstreamConfigService) ListModelRoutes(ctx context.Context, configID int64) ([]UpstreamKeyModelRoute, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUpstreamConfigNotFound
	}
	repo, ok := s.repo.(UpstreamKeyModelRouteRepository)
	if !ok {
		return nil, nil
	}
	return repo.ListUpstreamKeyModelRoutesByConfig(ctx, configID)
}

// SaveKeyModelRoute persists an administrator-owned model route. Automatic
// sync may subsequently refresh a route only while it remains non-manual.
func (s *UpstreamConfigService) SaveKeyModelRoute(ctx context.Context, route *UpstreamKeyModelRoute) error {
	if s == nil || s.repo == nil {
		return ErrUpstreamConfigNotFound
	}
	if route == nil || route.UpstreamKeyID <= 0 || strings.TrimSpace(route.PublicModel) == "" {
		return infraerrors.BadRequest("UPSTREAM_KEY_MODEL_ROUTE_INVALID", "upstream key model route is invalid")
	}
	route.TargetPlatform = strings.ToLower(strings.TrimSpace(route.TargetPlatform))
	if !IsConcreteRequestPlatform(route.TargetPlatform) {
		return infraerrors.BadRequest("UPSTREAM_KEY_MODEL_ROUTE_PLATFORM_INVALID", "upstream key model route target platform is invalid")
	}
	protocol, err := NormalizeUpstreamKeyModelRouteAPIProtocol(route.TargetPlatform, route.APIProtocol)
	if err != nil {
		return err
	}
	route.APIProtocol = protocol
	route.Source = UpstreamKeyModelRouteSourceManual
	if route.Status == "" {
		route.Status = UpstreamKeyModelRouteStatusAvailable
	}
	err = func() error {
		repo, ok := s.repo.(UpstreamKeyModelRouteRepository)
		if !ok {
			return nil
		}
		return repo.SaveUpstreamKeyModelRoute(ctx, route)
	}()
	if err == nil {
		s.refreshKeyModelRouteScheduling(ctx, route.UpstreamKeyID)
	}
	return err
}

func (s *UpstreamConfigService) DeleteKeyModelRoute(ctx context.Context, keyID, routeID int64) error {
	if s == nil || s.repo == nil {
		return ErrUpstreamConfigNotFound
	}
	repo, ok := s.repo.(UpstreamKeyModelRouteRepository)
	if !ok {
		return nil
	}
	err := repo.DeleteUpstreamKeyModelRoute(ctx, keyID, routeID)
	if err == nil {
		s.refreshKeyModelRouteScheduling(ctx, keyID)
	}
	return err
}

// RestoreKeyModelRouteAuto removes the administrator ownership marker and
// re-runs the same fail-closed detector used by upstream synchronization.
func (s *UpstreamConfigService) RestoreKeyModelRouteAuto(ctx context.Context, keyID, routeID int64) (*UpstreamKeyModelRoute, error) {
	if s == nil || s.repo == nil {
		return nil, ErrUpstreamConfigNotFound
	}
	repo, ok := s.repo.(UpstreamKeyModelRouteRepository)
	if !ok {
		return nil, ErrUpstreamKeyModelRouteNotFound
	}
	routes, err := repo.ListUpstreamKeyModelRoutes(ctx, keyID)
	if err != nil {
		return nil, err
	}
	var existing *UpstreamKeyModelRoute
	for i := range routes {
		if routes[i].ID == routeID {
			existing = &routes[i]
			break
		}
	}
	if existing == nil {
		return nil, ErrUpstreamKeyModelRouteNotFound
	}
	modelForDetection := strings.TrimSpace(existing.UpstreamModel)
	if modelForDetection == "" {
		modelForDetection = strings.TrimSpace(existing.PublicModel)
	}
	now := time.Now().UTC()
	autoRoutes := BuildAutoUpstreamKeyModelRoutes(keyID, []string{modelForDetection}, now)
	if len(autoRoutes) != 1 {
		return nil, infraerrors.BadRequest("UPSTREAM_KEY_MODEL_ROUTE_INVALID", "upstream key model route cannot be restored automatically")
	}
	restored := autoRoutes[0]
	restored.ID = existing.ID
	restored.PublicModel = existing.PublicModel
	restored.UpstreamModel = modelForDetection
	restored.APIProtocol = ""
	if err := repo.RestoreUpstreamKeyModelRouteAuto(ctx, &restored); err != nil {
		return nil, err
	}
	s.refreshKeyModelRouteScheduling(ctx, keyID)
	return &restored, nil
}

func (s *UpstreamConfigService) refreshKeyModelRouteScheduling(ctx context.Context, keyID int64) {
	if s == nil || s.schedulerSnapshot == nil || keyID <= 0 {
		return
	}
	if err := s.schedulerSnapshot.RefreshUpstreamKeyRoutes(ctx, keyID); err != nil {
		slog.Warn("failed to refresh scheduler after upstream model route change", "key_id", keyID, "error", err)
	}
}

// BuildAutoUpstreamKeyModelRoutes converts a successful upstream model list
// into model-level routes. Detection is deliberately fail-closed: an unknown
// or ambiguous model is retained as disabled diagnostic data, rather than
// being guessed as the key's legacy platform.
func BuildAutoUpstreamKeyModelRoutes(keyID int64, models []string, observedAt time.Time) []UpstreamKeyModelRoute {
	if keyID <= 0 || len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	routes := make([]UpstreamKeyModelRoute, 0, len(models))
	for _, raw := range models {
		model := strings.TrimSpace(raw)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		route := UpstreamKeyModelRoute{
			UpstreamKeyID: keyID,
			PublicModel:   model,
			UpstreamModel: model,
			Source:        UpstreamKeyModelRouteSourceAuto,
			Enabled:       false,
			Priority:      100,
			Status:        UpstreamKeyModelRouteStatusUnknown,
			LastSeenAt:    routeTimePtr(observedAt),
		}
		if platform, detectionStatus := detectModelPlatformDetailed(model); detectionStatus == UpstreamKeyModelRouteStatusAvailable && IsConcreteRequestPlatform(platform) {
			route.TargetPlatform = platform
			route.Enabled = true
			route.Status = UpstreamKeyModelRouteStatusAvailable
		} else {
			route.Status = detectionStatus
			reason := "model platform could not be detected"
			if detectionStatus == UpstreamKeyModelRouteStatusAmbiguous {
				reason = "model platform is ambiguous; provider metadata or manual override is required"
			}
			route.LastError = &reason
		}
		routes = append(routes, route)
	}
	sort.SliceStable(routes, func(i, j int) bool {
		return strings.ToLower(routes[i].PublicModel) < strings.ToLower(routes[j].PublicModel)
	})
	return routes
}

func routeTimePtr(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	value = value.UTC()
	return &value
}
