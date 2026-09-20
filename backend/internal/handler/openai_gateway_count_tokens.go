package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

func (h *OpenAIGatewayHandler) acquireOpenAITokenCountAdmission(
	c *gin.Context,
	account *service.Account,
	sessionHash string,
	reqLog *zap.Logger,
	writeError openAISlotErrorWriter,
) (*service.Account, func(), bool) {
	if account == nil || h == nil || h.concurrencyHelper == nil || h.gatewayService == nil {
		writeError(http.StatusServiceUnavailable, "api_error", "", "No available accounts")
		return nil, nil, false
	}

	ctx := c.Request.Context()
	accountRelease, acquired, err := h.concurrencyHelper.TryAcquireAccountSlotForAccount(ctx, account)
	if err != nil {
		reqLog.Warn("openai.token_count_account_slot_acquire_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		status, errType, code, message := concurrencyErrorResponse(err, "account")
		writeError(status, errType, code, message)
		return nil, nil, false
	}
	if !acquired {
		markOpsRoutingCapacityLimited(c)
		writeError(http.StatusServiceUnavailable, "api_error", gatewayCapacityExhaustedCode, gatewayCapacityExhaustedMessage)
		return nil, nil, false
	}

	resolved, proxyRelease, err := h.gatewayService.AcquireOpenAIProxyGroupEgress(ctx, account, sessionHash)
	if err != nil {
		if accountRelease != nil {
			accountRelease()
		}
		code := "openai_proxy_group_unavailable"
		message := "Proxy group routing is temporarily unavailable"
		switch {
		case errors.Is(err, service.ErrOpenAIProxyGroupCapacityFull):
			markOpsRoutingCapacityLimited(c)
			code = gatewayCapacityExhaustedCode
			message = gatewayCapacityExhaustedMessage
		case errors.Is(err, service.ErrOpenAIProxyGroupNoEgress):
			code = service.OpenAIProxyGroupNoEgressCode
			message = "The selected account has no available proxy-group egress"
		}
		reqLog.Warn("openai.token_count_proxy_group_admission_failed",
			zap.Int64("account_id", account.ID),
			zap.String("code", code),
			zap.Error(err),
		)
		writeError(http.StatusServiceUnavailable, "api_error", code, message)
		return nil, nil, false
	}

	release := wrapReleaseOnDone(ctx, combineOpenAIReleaseFuncs(proxyRelease, accountRelease))
	return resolved, release, true
}

// Only pre-send plugin admission rejection can retry a token-count request.
func (h *OpenAIGatewayHandler) forwardOpenAITokenCountWithPluginFailover(
	c *gin.Context,
	apiKey *service.APIKey,
	account *service.Account,
	sessionHash, routingModel, requestedModel string,
	reqLog *zap.Logger,
	writeError openAISlotErrorWriter,
	forward func(*service.Account) error,
) {
	excludedIDs := make(map[int64]struct{})
	for {
		resolved, release, admitted := h.acquireOpenAITokenCountAdmission(c, account, sessionHash, reqLog, writeError)
		if !admitted {
			return
		}
		account = resolved
		setOpsSelectedAccount(c, account.ID, account.Platform)
		err := func() error {
			defer release()
			return forward(account)
		}()
		var failoverErr *service.UpstreamFailoverError
		if !errors.As(err, &failoverErr) || !failoverErr.PluginAdmissionRejected || c.Writer.Written() || failoverClientGone(c) {
			return
		}
		if !failoverErr.ShouldRetryNextAccount() {
			writeError(failoverErr.StatusCode, "api_error", "", "No available accounts for the requested model")
			return
		}
		excludedIDs[account.ID] = struct{}{}
		account, err = h.gatewayService.SelectAccountForTokenCountWithExclusions(
			c.Request.Context(), apiKey.GroupID, sessionHash, routingModel,
			service.OpenAIEndpointCapabilityChatCompletions, openAICompatibleRequestPlatform(c.Request.Context(), apiKey), excludedIDs,
		)
		if err != nil || account == nil {
			if failoverClientGone(c) {
				return
			}
			cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, routingModel, requestedModel)
			if !cls.ModelNotFound {
				if err != nil {
					markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
				} else {
					markOpsRoutingCapacityLimited(c)
				}
			}
			writeError(cls.Status, cls.ErrType, "", cls.Message)
			return
		}
	}
}

// ResponsesInputTokens handles native OpenAI POST
// /v1/responses/input_tokens requests without routing them through the normal
// Responses generation and usage-recording pipeline.
func (h *OpenAIGatewayHandler) ResponsesInputTokens(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.responses_input_tokens",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.errorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String || strings.TrimSpace(modelResult.String()) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	reqModel := strings.TrimSpace(modelResult.String())
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}

	setOpsRequestContext(c, reqModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))
	if decision := h.checkSecurityAudit(c, reqLog, apiKey, subject, service.ContentModerationProtocolOpenAIResponses, reqModel, body); decision != nil && !decision.AllowNextStage {
		h.openAISecurityAuditError(c, decision)
		return
	}

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_input_tokens.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	routingModel := reqModel
	forwardBody := body
	if channelMapping.Mapped {
		routingModel = channelMapping.MappedModel
		forwardBody = h.gatewayService.ReplaceModelInBody(body, routingModel)
	}

	// Token counting is not billed, so it must not be excluded by the profit gate.
	c.Request = c.Request.WithContext(service.WithOpenAIProfitControlSuppressed(c.Request.Context()))
	requestPlatform := openAICompatibleRequestPlatform(c.Request.Context(), apiKey)
	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	requestStart := time.Now()
	account, err := h.gatewayService.SelectAccountForTokenCount(
		c.Request.Context(),
		apiKey.GroupID,
		sessionHash,
		routingModel,
		service.OpenAIEndpointCapabilityChatCompletions,
		requestPlatform,
	)
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	if err != nil {
		reqLog.Warn("openai_input_tokens.account_select_failed", zap.Error(openAICompatibleSelectionErrorForLog(err, requestPlatform)))
		cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, routingModel, reqModel)
		if !cls.ModelNotFound {
			markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		}
		h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
		return
	}
	if account == nil {
		cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, routingModel, reqModel)
		if !cls.ModelNotFound {
			markOpsRoutingCapacityLimited(c)
		}
		h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
		return
	}
	h.forwardOpenAITokenCountWithPluginFailover(
		c,
		apiKey,
		account,
		sessionHash,
		routingModel,
		reqModel,
		reqLog,
		func(status int, errType, _ string, message string) {
			h.errorResponse(c, status, errType, message)
		},
		func(account *service.Account) error {
			err := h.gatewayService.ForwardResponsesInputTokens(c.Request.Context(), c, account, forwardBody)
			if err != nil {
				reqLog.Error("openai_input_tokens.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
			return err
		},
	)
}

// GrokCountTokens handles Anthropic-compatible count_tokens requests locally.
// The route middleware already authenticates the API key and resolves the
// group; this handler intentionally does not select an account or check billing.
func (h *OpenAIGatewayHandler) GrokCountTokens(c *gin.Context) {
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.anthropicErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	bodyRef := service.NewRequestBodyRef(body)
	parsedReq, err := service.ParseGatewayRequest(bodyRef, domain.PlatformAnthropic)
	if err != nil {
		logRequestBodyParseFailure(requestLogger(c, "handler.openai_gateway.grok_count_tokens"), body, err)
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	if parsedReq.Model == "" {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	estimated, err := service.EstimateGrokCountTokens(parsedReq.Body.Bytes())
	if err != nil {
		requestLogger(c, "handler.openai_gateway.grok_count_tokens").Warn("grok_count_tokens.local_estimate_failed", zap.Error(err))
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}

	setOpsRequestContext(c, parsedReq.Model, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))
	c.JSON(http.StatusOK, gin.H{"input_tokens": estimated})
}

// CountTokens handles Anthropic-compatible POST /v1/messages/count_tokens for OpenAI groups.
// It validates billing and routes to an OpenAI token-count bridge without recording usage.
func (h *OpenAIGatewayHandler) CountTokens(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.anthropicErrorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.count_tokens",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	if !allowOpenAICompatibleMessagesDispatch(c, apiKey) {
		h.anthropicErrorResponse(c, http.StatusForbidden, "permission_error",
			"This group does not allow /v1/messages dispatch")
		return
	}

	if !h.ensureResponsesDependencies(c, reqLog) {
		return
	}

	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if maxErr, ok := extractMaxBytesError(err); ok {
			h.anthropicErrorResponse(c, http.StatusRequestEntityTooLarge, "invalid_request_error", buildBodyTooLargeMessage(maxErr.Limit))
			return
		}
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}
	if len(body) == 0 {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	bodyRef := service.NewRequestBodyRef(body)
	parsedReq, err := service.ParseGatewayRequest(bodyRef, domain.PlatformAnthropic)
	if err != nil {
		logRequestBodyParseFailure(reqLog, body, err)
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	body = parsedReq.Body.Bytes()
	if parsedReq.Model == "" {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	reqModel := parsedReq.Model
	ensureCompositeTargetPlatform(c, apiKey, reqModel)
	// composite+grok 在路由层已分流到 GrokCountTokens，这里可达的目标平台是
	// openai 与 CN 供应商；CN 账号由 ForwardCountTokensAsAnthropic 本地估算。
	if !openAICompatibleTextTargetAllowed(c, apiKey, reqModel) {
		h.anthropicErrorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by this OpenAI-compatible endpoint for composite groups")
		return
	}
	routingModel := service.NormalizeOpenAICompatRequestedModel(reqModel)
	preferredMappedModel := resolveOpenAIMessagesDispatchMappedModel(c, apiKey, reqModel)
	reqLog = reqLog.With(zap.String("model", reqModel), zap.Bool("stream", parsedReq.Stream))

	setOpsRequestContext(c, reqModel, false)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(false, false)))

	channelMapping, _ := h.gatewayService.ResolveChannelMappingAndRestrict(c.Request.Context(), apiKey.GroupID, reqModel)
	mappedBodyForMessages := newOpenAIModelMappedBodyCache(body, h.gatewayService.ReplaceModelInBody)

	subscription, _ := middleware2.GetSubscriptionFromContext(c)
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		reqLog.Info("openai_count_tokens.billing_eligibility_check_failed", zap.Error(err))
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.anthropicErrorResponse(c, status, code, message)
		return
	}

	requestStart := time.Now()
	// count_tokens 不计费：显式豁免利润门，避免高倍率账号池被门排除后连
	// token 计数都返回 no available accounts。
	c.Request = c.Request.WithContext(service.WithOpenAIProfitControlSuppressed(c.Request.Context()))
	sessionHash := h.gatewayService.GenerateSessionHash(c, body)
	currentRoutingModel := routingModel
	if preferredMappedModel != "" {
		currentRoutingModel = preferredMappedModel
	}
	account, err := h.gatewayService.SelectAccountForTokenCount(
		c.Request.Context(),
		apiKey.GroupID,
		sessionHash,
		currentRoutingModel,
		service.OpenAIEndpointCapabilityChatCompletions,
		openAICompatibleRequestPlatform(c.Request.Context(), apiKey),
	)
	service.SetOpsLatencyMs(c, service.OpsAuthLatencyMsKey, time.Since(requestStart).Milliseconds())
	if err != nil {
		requestPlatform := openAICompatibleRequestPlatform(c.Request.Context(), apiKey)
		reqLog.Warn("openai_count_tokens.account_select_failed", zap.Error(openAICompatibleSelectionErrorForLog(err, requestPlatform)))
		cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, currentRoutingModel, reqModel)
		if !cls.ModelNotFound {
			markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		}
		h.anthropicErrorResponse(c, cls.Status, cls.ErrType, cls.Message)
		return
	}
	if account == nil {
		cls := classifyOpenAICompatibleNoAccountErrorFromGin(c, h.gatewayService, apiKey, currentRoutingModel, reqModel)
		if !cls.ModelNotFound {
			markOpsRoutingCapacityLimited(c)
		}
		h.anthropicErrorResponse(c, cls.Status, cls.ErrType, cls.Message)
		return
	}
	forwardBody := mappedBodyForMessages(channelMapping.Mapped, channelMapping.MappedModel)
	defaultMappedModel := preferredMappedModel
	h.forwardOpenAITokenCountWithPluginFailover(
		c,
		apiKey,
		account,
		sessionHash,
		currentRoutingModel,
		reqModel,
		reqLog,
		func(status int, errType, _ string, message string) {
			h.anthropicErrorResponse(c, status, errType, message)
		},
		func(account *service.Account) error {
			err := h.gatewayService.ForwardCountTokensAsAnthropic(c.Request.Context(), c, account, forwardBody, defaultMappedModel)
			if err != nil {
				reqLog.Error("openai_count_tokens.forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
			}
			return err
		},
	)
}
