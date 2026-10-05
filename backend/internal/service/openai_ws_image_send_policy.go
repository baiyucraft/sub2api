package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAIWSImageSendPayload struct {
	body    []byte
	billing OpenAIResponsesImageBillingConfig
}

// Refresh only admission metadata: the socket's chosen credential, proxy,
// physical identity, and concurrency ownership must not change mid-session.
func (s *OpenAIGatewayService) openAIWSImagePermissionView(ctx context.Context, chosen *Account) (*Account, error) {
	if chosen == nil {
		return nil, errors.New("missing websocket account")
	}
	var latest *Account
	var err error
	switch {
	case s.accountRepo != nil:
		latest, err = s.accountRepo.GetByID(ctx, chosen.ID)
	case s.schedulerSnapshot != nil:
		latest, err = s.schedulerSnapshot.GetAccount(ctx, chosen.ID)
	default:
		return chosen, nil // Direct callers without persistence use their supplied snapshot.
	}
	if err != nil || latest == nil || latest.ID != chosen.ID || latest.Platform != chosen.Platform {
		return nil, &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, Scope: GatewayFailureScopeAccountCapability, Reason: "image_permission_refresh_unavailable", NextAccountAction: NextAccountRetry, ClientStatusCode: http.StatusServiceUnavailable, ClientMessage: "Unable to verify upstream image permission", OriginAccountID: chosen.ID, OriginPlatform: chosen.Platform}
	}
	view := *chosen
	view.Extra = latest.Extra
	view.UpstreamImagePricing = latest.UpstreamImagePricing
	return &view, nil
}

// prepareOpenAIWSImageSend runs immediately before a physical write, including
// reconnect/recovery writes. It never calls turn hooks or performs billing.
func (s *OpenAIGatewayService) prepareOpenAIWSImageSend(ctx context.Context, c *gin.Context, chosen *Account, body []byte, original OpenAIImageRequest, hosted bool) (openAIWSImageSendPayload, error) {
	result := openAIWSImageSendPayload{body: body}
	if chosen == nil || chosen.Platform != PlatformOpenAI {
		return result, nil
	}
	if ingress, ok := OpenAIImageRequestFromContext(ctx); ok {
		original.Lite = original.Lite || ingress.Lite
	}
	view, err := s.openAIWSImagePermissionView(ctx, chosen)
	if err != nil {
		return result, err
	}
	var apiKey *APIKey
	if c != nil {
		apiKey = getAPIKeyFromContext(c)
	}
	var groupID *int64
	if apiKey != nil {
		groupID = apiKey.GroupID
	}
	ctx = WithOpenAIImageRequestDescriptor(ctx, original)
	if c != nil {
		ctx = s.WithOpenAIImageRequestPolicy(ctx, groupID)
		ctx = WithOpenAIImageRequestGroup(ctx, apiKeyGroup(apiKey))
	}
	next, changed, err := DeriveOpenAIImageRequestBody(body, original, view)
	if err != nil {
		return result, err
	}
	final := DescribeOpenAIImageRequest(openAIResponsesEndpoint, gjson.GetBytes(next, "model").String(), next, original.IsCodex)
	if !openAIImageManualStrip(original, view) {
		final.Native = final.Native || original.Native
		final.Explicit = final.Explicit || original.Explicit
	}
	final.History = final.History || original.History
	final.Explicit = final.Explicit || IsImageGenerationEndpoint(original.Endpoint) || isOpenAIImageGenerationModel(original.Model)
	groupAllowed := true
	if p, ok := ctx.Value(openAIImagePolicyContextKey{}).(openAIImageRequestPolicy); ok && p.groupAllowed != nil {
		groupAllowed = *p.groupAllowed
	}
	if final.RequiresCapability() && !groupAllowed {
		return result, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, ImageGenerationPermissionMessage(), nil)
	}
	if err := CheckOpenAIImageRequestPermission(ctx, final, view); err != nil {
		return result, err
	}
	eligible := hosted && s.CanInjectOpenAIHostedImageGeneration(ctx, groupID, view)
	if eligible {
		var payload map[string]any
		if err := decodeOpenAIJSONUseNumber(next, &payload); err != nil {
			return result, err
		}
		modified := ensureOpenAIResponsesImageGenerationTool(payload)
		modified = ensureOpenAIResponsesImageGenerationToolChoiceAuto(payload) || modified
		modified = normalizeOpenAIResponsesImageGenerationTools(payload) || modified
		modified = applyCodexImageGenerationBridgeInstructions(payload) || modified
		if modified {
			next, err = json.Marshal(payload)
			if err != nil {
				return result, err
			}
		}
	}
	if stripped, _, stripErr := stripCodexSparkImageGenerationToolFromRawPayload(next, gjson.GetBytes(next, "model").String()); stripErr != nil {
		return result, stripErr
	} else {
		next = stripped
	}
	final = DescribeOpenAIImageRequest(openAIResponsesEndpoint, gjson.GetBytes(next, "model").String(), next, original.IsCodex)
	if err := CheckOpenAIImageRequestPermission(ctx, final, view); err != nil {
		return result, err
	}
	logOpenAIImageRequestDecision(ctx, original, view, changed, eligible)
	result.body = next
	if IsImageGenerationIntent(openAIResponsesEndpoint, final.Model, next) {
		result.billing, err = resolveOpenAIResponsesImageBillingConfigDetailedFromBody(next, original.Model)
	}
	return result, err
}

// Match HTTP/SSE classification: the exact structured entitlement message can
// supply an absent 403, but an explicit conflicting status always wins.
func openAIWSImagePermissionFailure(payload []byte, headers http.Header) *UpstreamFailoverError {
	event := gjson.GetBytes(payload, "type").String()
	if event != "error" && event != "response.failed" {
		return nil
	}
	status := openAIStreamFailureStatus(payload, extractOpenAISSEErrorMessage(payload))
	if !IsOpenAIImagePermissionDenied(status, payload) {
		return nil
	}
	return newOpenAIUpstreamFailoverError(status, headers, payload, extractOpenAISSEErrorMessage(payload), false)
}

func (s *OpenAIGatewayService) handleOpenAIWSImagePermissionDenied(ctx context.Context, account *Account, payload []byte) bool {
	if openAIWSImagePermissionFailure(payload, nil) == nil {
		return false
	}
	if s != nil && s.rateLimitService != nil {
		s.rateLimitService.HandleOpenAIImagePermissionDenied(ctx, account, http.StatusForbidden, payload)
	}
	return true
}

// The write-side wrapper is downstream of parsing and turn hooks, so every
// actual send is refreshed without consuming the turn's admission budget twice.
type openAIWSImageGuardFrameConn struct {
	inner    openaiwsv2.FrameConn
	prepare  func(context.Context, []byte, OpenAIImageRequest) ([]byte, error)
	describe func([]byte) OpenAIImageRequest
	session  []byte
}

func (c *openAIWSImageGuardFrameConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	return c.inner.ReadFrame(ctx)
}
func (c *openAIWSImageGuardFrameConn) Close() error { return c.inner.Close() }
func (c *openAIWSImageGuardFrameConn) WriteFrame(ctx context.Context, typ coderws.MessageType, payload []byte) error {
	if typ != coderws.MessageText && typ != coderws.MessageBinary {
		return c.inner.WriteFrame(ctx, typ, payload)
	}
	event := strings.TrimSpace(gjson.GetBytes(payload, "type").String())
	if event != "response.create" && event != "session.update" {
		return c.inner.WriteFrame(ctx, typ, payload)
	}
	body := payload
	if event == "session.update" {
		body = []byte(gjson.GetBytes(payload, "session").Raw)
		if !gjson.ParseBytes(body).IsObject() {
			// A frame without a session object cannot change image declarations.
			// Preserve passthrough validation and forwarding for unrelated events.
			return c.inner.WriteFrame(ctx, typ, payload)
		}
	}
	effective, err := openAIWSInheritImageSession(body, c.session)
	if err != nil {
		return err
	}
	original := DescribeOpenAIImageRequest(openAIResponsesEndpoint, gjson.GetBytes(effective, "model").String(), effective, false)
	if c.describe != nil {
		raw := c.describe(payload)
		if raw.Model != "" {
			original.Model = raw.Model
		}
		original.IsCodex = raw.IsCodex
		original.Lite = original.Lite || raw.Lite
		original.Native = original.Native || raw.Native
		original.Explicit = original.Explicit || raw.Explicit
		original.Passive = original.Passive || raw.Passive
		original.History = original.History || raw.History
		original.PreviousResponse = original.PreviousResponse || raw.PreviousResponse
	}
	prepared, err := c.prepare(ctx, effective, original)
	if err != nil {
		return err
	}
	// Omitted session fields inherit upstream. An empty tools array is required
	// to override a stripped inherited declaration rather than omit it again.
	out := prepared
	for _, key := range []string{"tools", "tool_choice", "model"} {
		if gjson.GetBytes(body, key).Exists() {
			continue
		}
		before := gjson.GetBytes(effective, key)
		after := gjson.GetBytes(out, key)
		if !before.Exists() {
			continue
		}
		if before.Raw == after.Raw {
			out, err = sjson.DeleteBytes(out, key)
		} else if key == "tools" && !after.Exists() {
			out, err = sjson.SetRawBytes(out, key, []byte("[]"))
		}
		if err != nil {
			return err
		}
	}
	if event == "session.update" {
		// Clearing an explicitly supplied declaration must clear session state too.
		if gjson.GetBytes(effective, "tools").Exists() && !gjson.GetBytes(prepared, "tools").Exists() {
			out, err = sjson.SetRawBytes(out, "tools", []byte("[]"))
			if err != nil {
				return err
			}
		}
		payload, err = sjson.SetRawBytes(payload, "session", out)
		if err != nil {
			return err
		}
	} else {
		payload = out
	}
	if err := c.inner.WriteFrame(ctx, typ, payload); err != nil {
		return err
	}
	if event == "session.update" {
		c.session = prepared
	}
	return nil
}

func openAIWSInheritImageSession(body, session []byte) ([]byte, error) {
	out := body
	for _, key := range []string{"tools", "tool_choice", "model"} {
		if gjson.GetBytes(out, key).Exists() {
			continue
		}
		value := gjson.GetBytes(session, key)
		if !value.Exists() {
			continue
		}
		var err error
		out, err = sjson.SetRawBytes(out, key, []byte(value.Raw))
		if err != nil {
			return body, err
		}
	}
	return out, nil
}
