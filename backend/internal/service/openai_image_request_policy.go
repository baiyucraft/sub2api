package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// OpenAIImageRequest describes client intent before account-specific transforms.
// It contains no prompt or tool arguments and is safe to retain across failover.
type OpenAIImageRequest struct {
	Source           string
	Endpoint         string
	Model            string
	IsCodex          bool
	Native           bool
	NativeRequired   bool
	Explicit         bool
	Passive          bool
	History          bool
	UnknownHistory   bool
	PreviousResponse bool
	Lite             bool
	Valid            bool
}

func (r OpenAIImageRequest) RequiresCapability() bool {
	return r.Explicit || r.History || r.NativeRequired
}

func (r OpenAIImageRequest) CanStripPassive() bool {
	return r.Valid && (r.Native || r.Passive) && !r.RequiresCapability()
}

// DescribeOpenAIImageRequest never infers image intent from free-form prompt text.
func DescribeOpenAIImageRequest(endpoint, model string, body []byte, isCodex bool) OpenAIImageRequest {
	r := OpenAIImageRequest{Endpoint: endpoint, Model: model, IsCodex: isCodex, Source: "ordinary"}
	r.Explicit = IsImageGenerationEndpoint(endpoint) || isOpenAIImageGenerationModel(model)
	r.Valid = gjson.ValidBytes(body) && gjson.ParseBytes(body).IsObject()
	if r.Valid {
		root := gjson.ParseBytes(body)
		r.Explicit = r.Explicit || isOpenAIImageGenerationModel(root.Get("model").String()) || openAIJSONToolChoiceSelectsExplicitImageGeneration(root.Get("tool_choice"))
		r.PreviousResponse = strings.TrimSpace(root.Get("previous_response_id").String()) != ""
		r.Lite = isOpenAIResponsesLiteWebSocketPayload(body)
		inspectTools := func(tools gjson.Result) {
			tools.ForEach(func(_, tool gjson.Result) bool {
				r.Native = r.Native || isOpenAIImageGenerationType(tool.Get("type").String())
				r.Passive = r.Passive || isPassiveOpenAIImageTool(tool)
				return true
			})
		}
		inspectTools(root.Get("tools"))
		root.Get("input").ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "additional_tools" {
				inspectTools(item.Get("tools"))
			}
			return true
		})
		r.Explicit = r.Explicit || openAIRequiredChoiceOnlyImages(root)
		r.History, r.UnknownHistory = describeOpenAIImageHistory(root)
		r.UnknownHistory = r.UnknownHistory || r.PreviousResponse
		r.Valid = !hasDuplicateOpenAIImagePolicyKeys(root, true)
	}
	switch {
	case r.Explicit:
		r.Source = "explicit"
	case r.History:
		r.Source = "history"
	case r.Native:
		r.Source = "native"
	case r.Passive:
		r.Source = "passive"
	}
	return r
}

func hasDuplicateOpenAIImagePolicyKeys(v gjson.Result, topLevel bool) bool {
	if !v.IsArray() && !v.IsObject() {
		return false
	}
	seen := make(map[string]gjson.Result)
	duplicate := false
	repeatedModel, modelVariant := false, false
	v.ForEach(func(k, child gjson.Result) bool {
		if v.IsObject() {
			key := k.String()
			if topLevel && key != "model" && strings.EqualFold(key, "model") {
				modelVariant = true
			}
			if previous, exists := seen[key]; exists {
				// Existing model admission accepts identical top-level strings.
				// Compare decoded values exactly; other duplicate keys stay ambiguous.
				if topLevel && key == "model" && previous.Type == gjson.String && child.Type == gjson.String && previous.Str == child.Str {
					repeatedModel = true
					return true
				}
				duplicate = true
				return false
			}
			seen[key] = child
		}
		duplicate = hasDuplicateOpenAIImagePolicyKeys(child, false)
		return !duplicate
	})
	return duplicate || (repeatedModel && modelVariant)
}

func isPassiveOpenAIImageTool(tool gjson.Result) bool {
	return isImageGenNamespaceTool(tool) || (tool.Get("type").String() == "function" &&
		(isOpenAIImageGenFunctionReference(tool.Get("namespace").String(), tool.Get("name").String()) ||
			isOpenAIImageGenFunctionReference(tool.Get("function.namespace").String(), tool.Get("function.name").String())))
}

type openAIImageRequestContextKey struct{}
type openAIImagePolicyContextKey struct{}

func WithOpenAIImageRequest(ctx context.Context, endpoint, model string, body []byte, isCodex bool) context.Context {
	return WithOpenAIImageRequestDescriptor(ctx, DescribeOpenAIImageRequest(endpoint, model, body, isCodex))
}

// WithOpenAIImageRequestDescriptor lets ingress supply header-only Lite and
// compact metadata before scheduling without retaining the request body.
func WithOpenAIImageRequestDescriptor(ctx context.Context, r OpenAIImageRequest) context.Context {
	return context.WithValue(ctx, openAIImageRequestContextKey{}, r)
}

func OpenAIImageRequestFromContext(ctx context.Context) (OpenAIImageRequest, bool) {
	if ctx == nil {
		return OpenAIImageRequest{}, false
	}
	r, ok := ctx.Value(openAIImageRequestContextKey{}).(OpenAIImageRequest)
	return r, ok
}

type openAIImageRequestPolicy struct {
	groupID           int64
	bridge            bool
	resolved          bool
	cached            bool
	staleAfterSeconds int
	groupAllowed      *bool
	stripOptional     bool
}

// WithOpenAIImageRequestPolicy caches only request-scoped channel/global policy;
// account overrides are evaluated independently for every candidate.
func (s *OpenAIGatewayService) WithOpenAIImageRequestPolicy(ctx context.Context, groupID *int64) context.Context {
	prior, _ := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy)
	id := int64(0)
	if groupID != nil {
		id = *groupID
	} else if prior.groupAllowed != nil {
		id = prior.groupID
	}
	if prior.cached && prior.groupID == id {
		return ctx
	}
	p := openAIImageRequestPolicy{groupID: id, resolved: true, cached: true, staleAfterSeconds: 86400}
	if prior.staleAfterSeconds > 0 && (!prior.cached || prior.groupID == id) {
		p.staleAfterSeconds = prior.staleAfterSeconds
	}
	if !prior.cached || prior.groupID == id {
		p.groupAllowed = prior.groupAllowed
		p.stripOptional = prior.stripOptional
	}
	p.bridge = s != nil && s.cfg != nil && s.cfg.Gateway.CodexImageGenerationBridgeEnabled
	if s != nil && s.channelService != nil && groupID != nil {
		ch, err := s.channelService.GetChannelForGroup(ctx, id)
		if err != nil {
			p.resolved = false
			p.bridge = false
			slog.WarnContext(ctx, "openai_image_bridge_policy_unavailable", "group_id", id)
		} else if override := ch.CodexImageGenerationBridgeOverride(PlatformOpenAI); override != nil {
			p.bridge = *override
		}
	}
	return context.WithValue(ctx, openAIImagePolicyContextKey{}, p)
}

// WithOpenAIImagePermissionStaleAfter uses the group's existing pricing snapshot
// freshness setting without coupling capability filtering to image cost routing.
func WithOpenAIImagePermissionStaleAfter(ctx context.Context, seconds int) context.Context {
	p, _ := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy)
	p.staleAfterSeconds = seconds
	return context.WithValue(ctx, openAIImagePolicyContextKey{}, p)
}

// WithOpenAIImageRequestGroup supplies local policy without a per-candidate
// repository lookup. Call after resolving the actual request group.
func WithOpenAIImageRequestGroup(ctx context.Context, group *Group) context.Context {
	p, _ := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy)
	allowed := GroupAllowsImageGeneration(group)
	p.groupAllowed = &allowed
	p.stripOptional = group != nil && group.StripOptionalImageTools
	if group != nil {
		if p.groupID != group.ID {
			p.cached = false
			p.resolved = false
			p.bridge = false
		}
		p.staleAfterSeconds = group.ImageCostStaleAfterSeconds
		p.groupID = group.ID
	}
	return context.WithValue(ctx, openAIImagePolicyContextKey{}, p)
}

const (
	OpenAIImagePermissionDenied  = "denied"
	OpenAIImagePermissionAllowed = "allowed"
	OpenAIImagePermissionUnknown = "unknown"
)

func openAIImagePermissionSnapshot(account *Account) *UpstreamKeyImagePricing {
	if account == nil {
		return nil
	}
	if account.UpstreamImagePricing != nil {
		return account.UpstreamImagePricing
	}
	if p, ok := parseSub2APIImagePricingSnapshot(account.Extra); ok {
		return &UpstreamKeyImagePricing{Supported: p.AllowImageGeneration, Status: p.Status, Stale: p.Stale, ObservedAt: p.ObservedAt}
	}
	// Keep historical permissions after provider removal, including stale denies.
	if p, ok := parseLCodexImageCapabilitySnapshot(account.Extra); ok {
		return &UpstreamKeyImagePricing{Supported: p.AllowImageGeneration, Status: p.Status, Stale: p.Stale, ObservedAt: p.ObservedAt}
	}
	return nil
}

func openAIImageSnapshotKnown(p *UpstreamKeyImagePricing) bool {
	return p != nil && (p.Status == UpstreamKeyImagePricingStatusAvailable || p.Status == UpstreamKeyImagePricingStatusPartial || p.Status == UpstreamKeyImagePricingStatusDisabled)
}

// OpenAIImagePermissionRank returns permission, never inferring denial from an
// unavailable pricing probe. An explicit negative remains denied until refreshed
// to allowed; only positive snapshots age into unknown.
func OpenAIImagePermissionRank(account *Account, staleAfterSeconds int) string {
	p := openAIImagePermissionSnapshot(account)
	if !openAIImageSnapshotKnown(p) {
		return OpenAIImagePermissionUnknown
	}
	if !p.Supported {
		return OpenAIImagePermissionDenied
	}
	if staleAfterSeconds <= 0 {
		staleAfterSeconds = 86400
	}
	if p.Stale || p.ObservedAt == nil || time.Since(*p.ObservedAt) > time.Duration(staleAfterSeconds)*time.Second || p.ObservedAt.After(time.Now().Add(time.Minute)) {
		return OpenAIImagePermissionUnknown
	}
	return OpenAIImagePermissionAllowed
}

func openAIImageStaleAfter(ctx context.Context) int {
	if p, ok := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy); ok && p.staleAfterSeconds > 0 {
		return p.staleAfterSeconds
	}
	return 86400
}

func openAIImageAutomaticStrip(r OpenAIImageRequest, account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI || !r.CanStripPassive() {
		return false
	}
	if r.IsCodex {
		if _, ok := account.CodexImageGenerationExplicitToolPolicyOverride(); ok {
			return false
		}
		if account.CodexImageGenerationBridgeOverride() != nil {
			return false
		}
	}
	p := openAIImagePermissionSnapshot(account)
	return openAIImageSnapshotKnown(p) && !p.Supported
}

// logOpenAIImageRequestDecision is field-allowlisted: never attach the payload,
// extra/credentials maps, tool arguments, or user-provided continuation IDs.
func logOpenAIImageRequestDecision(ctx context.Context, r OpenAIImageRequest, account *Account, stripped, hosted bool) {
	if account == nil || account.Platform != PlatformOpenAI || (!r.RequiresCapability() && !r.Native && !r.Passive && !hosted) {
		return
	}
	attrs := []any{"account_id", account.ID, "image_source", r.Source,
		"image_required", r.RequiresCapability(), "image_history_unknown", r.UnknownHistory, "image_passive_stripped", stripped && r.CanStripPassive() && !openAIImageManualStrip(r, account),
		"image_policy_stripped", stripped && openAIImageManualStrip(r, account), "image_hosted_eligible", hosted,
		"image_permission_rank", OpenAIImagePermissionRank(account, openAIImageStaleAfter(ctx))}
	if p := openAIImagePermissionSnapshot(account); p != nil {
		attrs = append(attrs, "image_snapshot_status", p.Status, "image_snapshot_stale", p.Stale)
		if p.ObservedAt != nil {
			attrs = append(attrs, "image_snapshot_age_seconds", int64(time.Since(*p.ObservedAt).Seconds()))
		}
	}
	slog.InfoContext(ctx, "openai_image_request_policy", attrs...)
}

func openAIImageManualStrip(r OpenAIImageRequest, account *Account) bool {
	return r.IsCodex && account != nil && account.CodexImageGenerationExplicitToolPolicy() == codexImageGenerationExplicitToolPolicyStrip
}

// AccountRequiresOpenAIImageCapability is candidate-specific: an optional bridge
// on one account never makes the ordinary request require images on every account.
func (s *OpenAIGatewayService) AccountRequiresOpenAIImageCapability(ctx context.Context, groupID *int64, account *Account) bool {
	r, ok := OpenAIImageRequestFromContext(ctx)
	if !ok || account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	if openAIImageManualStrip(r, account) {
		return IsImageGenerationEndpoint(r.Endpoint) || isOpenAIImageGenerationModel(r.Model) || r.History
	}
	if r.RequiresCapability() {
		return true
	}
	if openAIImageContextStripsOptional(ctx) {
		return false
	}
	return ((r.Native || r.Passive) && !openAIImageAutomaticStrip(r, account)) || s.CanInjectOpenAIHostedImageGeneration(ctx, groupID, account)
}

// AccountRequiresOpenAIResponsesForImage distinguishes native hosted image work
// from passive client tools, which may legitimately use Chat Completions (#4476).
func (s *OpenAIGatewayService) AccountRequiresOpenAIResponsesForImage(ctx context.Context, groupID *int64, account *Account) bool {
	r, ok := OpenAIImageRequestFromContext(ctx)
	if !ok || account == nil || account.Platform != PlatformOpenAI || IsImageGenerationEndpoint(r.Endpoint) {
		return false
	}
	if openAIImageManualStrip(r, account) {
		return isOpenAIImageGenerationModel(r.Model) || r.History
	}
	if !r.RequiresCapability() && openAIImageContextStripsOptional(ctx) {
		return false
	}
	return r.RequiresCapability() || (r.Native && !openAIImageAutomaticStrip(r, account)) || s.CanInjectOpenAIHostedImageGeneration(ctx, groupID, account)
}

// CanInjectOpenAIHostedImageGeneration performs the final selected-account gate.
// Unknown permission is a compatibility fallback, but stale negative evidence
// suppresses optional injection until a successful permission refresh.
func (s *OpenAIGatewayService) CanInjectOpenAIHostedImageGeneration(ctx context.Context, groupID *int64, account *Account) bool {
	r, ok := OpenAIImageRequestFromContext(ctx)
	if !ok || !r.IsCodex || r.Lite || normalizeImageGenerationEndpoint(r.Endpoint) == openAIResponsesCompactEndpoint || account == nil || account.Platform != PlatformOpenAI || openAIImageManualStrip(r, account) {
		return false
	}
	if !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses) || account.isRateLimitActiveForKey(openAIImageGenerationRateLimitKey) {
		return false
	}
	if openAIImageContextStripsOptional(ctx) {
		return false
	}
	p := openAIImagePermissionSnapshot(account)
	if openAIImageSnapshotKnown(p) && !p.Supported {
		return false
	}
	if override := account.CodexImageGenerationBridgeOverride(); override != nil {
		return *override
	}
	ctx = s.WithOpenAIImageRequestPolicy(ctx, groupID)
	policy, _ := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy)
	return policy.resolved && policy.bridge
}

// DeriveOpenAIImageRequestBody never mutates body; call it with the original body
// for each account. Automatic following removes only unused passive declarations.
func DeriveOpenAIImageRequestBody(body []byte, r OpenAIImageRequest, account *Account) ([]byte, bool, error) {
	if openAIImageManualStrip(r, account) {
		if r.CanStripPassive() {
			return stripOptionalOpenAIImageTools(body, r)
		}
		var payload map[string]any
		if err := decodeOpenAIJSONUseNumber(body, &payload); err != nil {
			return body, false, err
		}
		if !stripOpenAIImageGenerationTools(payload) {
			return body, false, nil
		}
		next, err := json.Marshal(payload)
		return next, err == nil, err
	}
	if !openAIImageAutomaticStrip(r, account) {
		return body, false, nil
	}
	return stripOptionalOpenAIImageTools(body, r)
}

// CheckOpenAIImageRequestPermission is shared by HTTP and WS after all payload
// transforms. It rejects only image capability, never disables text on an account.
func CheckOpenAIImageRequestPermission(ctx context.Context, r OpenAIImageRequest, account *Account) error {
	if original, ok := OpenAIImageRequestFromContext(ctx); ok {
		r.History = r.History || original.History
		r.NativeRequired = r.NativeRequired || (r.Native && original.NativeRequired)
	}
	if account == nil || account.Platform != PlatformOpenAI || (!r.RequiresCapability() && !r.Native && !r.Passive) {
		return nil
	}
	reason := ""
	if OpenAIImagePermissionRank(account, openAIImageStaleAfter(ctx)) == OpenAIImagePermissionDenied {
		reason = "image_permission_denied"
	}
	if account.isRateLimitActiveForKey(openAIImageGenerationRateLimitKey) {
		reason = "image_capability_cooldown"
	}
	if (r.RequiresCapability() || r.Native) && !IsImageGenerationEndpoint(r.Endpoint) && !account.SupportsOpenAIEndpointCapability(OpenAIEndpointCapabilityResponses) {
		reason = "image_responses_unsupported"
	}
	if reason == "" {
		return nil
	}
	return &UpstreamFailoverError{StatusCode: 503, Scope: GatewayFailureScopeAccountCapability, Reason: GatewayFailureReason(reason), NextAccountAction: NextAccountRetry, ClientStatusCode: 503, ClientMessage: "No image-capable upstream account is available", OriginAccountID: account.ID, OriginPlatform: account.Platform}
}
