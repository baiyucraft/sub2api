package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/wire"
)

// GatewayRequestCustomizer 是认证后的可选本地响应扩展。
type GatewayRequestCustomizer interface {
	Middleware() gin.HandlerFunc
	Apply(context.Context, service.GatewayChannelCustomizationSettings) error
	Settings(context.Context) (service.GatewayChannelCustomizationSettings, error)
}

type CustomizationService struct {
	current atomic.Pointer[customizationRuntimeState]
	hits    sync.Map
}

type customizationRuntimeState struct {
	generation uint64
	settings   service.GatewayChannelCustomizationSettings
	rules      []compiledCustomizationRule
}

type compiledCustomizationRule struct {
	rule                  service.GatewayChannelCustomizationRule
	apiKeyIDs             map[int64]struct{}
	apiKeyNames           map[string]struct{}
	userIDs               map[int64]struct{}
	userEmails            map[string]struct{}
	methods               map[string]struct{}
	exactPaths            map[string]struct{}
	pathPrefixes          []string
	userAgentContains     []string
	queryParams           map[string]map[string]struct{}
	requestMessagePattern *regexp.Regexp
	fingerprint           string
}

const customizationRequestBodyMaxBytes = 256 * 1024

type replayRequestBody struct {
	io.Reader
	original io.Closer
}

const customizationDecisionContextKey = "gateway_customization_decision"

type customizationDecision struct {
	state *customizationRuntimeState
	rule  compiledCustomizationRule
}

type customizationTargetGroupResolver interface {
	ResolveCustomizationTargetGroup(context.Context, *service.APIKey, int64) (*service.Group, error)
}

type customizationSubscriptionResolver interface {
	GetActiveSubscription(context.Context, int64, int64) (*service.UserSubscription, error)
}

func (b *replayRequestBody) Close() error {
	if b.original == nil {
		return nil
	}
	return b.original.Close()
}

var CustomizationProviderSet = wire.NewSet(NewCustomizationService)

func NewCustomizationService() *CustomizationService { return &CustomizationService{} }

func (s *CustomizationService) Settings(context.Context) (service.GatewayChannelCustomizationSettings, error) {
	if state := s.current.Load(); state != nil {
		return cloneCustomizationSettings(state.settings), nil
	}
	return service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{}}, nil
}

func (s *CustomizationService) Apply(_ context.Context, settings service.GatewayChannelCustomizationSettings) error {
	if err := service.NormalizeGatewayChannelCustomizationSettings(&settings); err != nil {
		return err
	}
	previous := s.current.Load()
	if previous != nil && serviceGatewayCustomizationSettingsEqual(previous.settings, settings) {
		return nil
	}
	compiledRules, err := compileCustomizationRules(settings.Rules)
	if err != nil {
		return err
	}
	next := &customizationRuntimeState{
		generation: generationOfCustomization(previous) + 1,
		settings:   cloneCustomizationSettings(settings),
		rules:      compiledRules,
	}
	s.current.Store(next)
	return nil
}

// Middleware keeps the historical local-response middleware contract.
func (s *CustomizationService) Middleware() gin.HandlerFunc { return s.LocalResponseMiddleware() }

// MayMatchGroupMapping reports whether the request metadata could match an
// enabled group-mapping rule. Target and message conditions are evaluated after
// API key authentication; this preflight deliberately does not read the body.
func (s *CustomizationService) MayMatchGroupMapping(c *gin.Context) bool {
	if s == nil || c == nil {
		return false
	}
	state := s.current.Load()
	if state == nil {
		return false
	}
	for _, rule := range state.rules {
		if rule.rule.Action == service.GatewayChannelCustomizationActionGroupMapping && matchesCustomizationRequestMetadata(rule, c) {
			return true
		}
	}
	return false
}

// GroupMappingMiddleware applies a matched request-scoped group override after
// authentication and before group/model admission. Billing introspection is
// intentionally exempt so it continues to describe the API key's bound group.
func (s *CustomizationService) GroupMappingMiddleware(apiKeyService customizationTargetGroupResolver, subscriptionService customizationSubscriptionResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isCustomizationBillingRequest(c) {
			c.Next()
			return
		}
		decision := s.matchDecision(c)
		if decision == nil || decision.rule.rule.Action != service.GatewayChannelCustomizationActionGroupMapping {
			c.Next()
			return
		}
		if s.current.Load() != decision.state {
			c.Next()
			return
		}
		s.incrementHit(decision.rule.fingerprint)
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || decision.rule.rule.TargetGroupID == nil {
			middleware.AbortWithRequestError(c, http.StatusForbidden, "CUSTOMIZATION_GROUP_MAPPING_UNAVAILABLE", "Request group mapping is unavailable")
			return
		}
		if apiKeyService == nil {
			middleware.AbortWithRequestError(c, http.StatusServiceUnavailable, "CUSTOMIZATION_GROUP_MAPPING_UNAVAILABLE", "Request group mapping is unavailable")
			return
		}
		target, err := apiKeyService.ResolveCustomizationTargetGroup(c.Request.Context(), apiKey, *decision.rule.rule.TargetGroupID)
		if err != nil {
			status, code, message := customizationGroupMappingError(err)
			middleware.AbortWithRequestError(c, status, code, message)
			return
		}
		if !customizationTargetCompatible(c, target) {
			middleware.AbortWithRequestError(c, http.StatusForbidden, "CUSTOMIZATION_TARGET_GROUP_INCOMPATIBLE", "Target group is incompatible with this gateway endpoint")
			return
		}

		var subscription *service.UserSubscription
		if target.IsSubscriptionType() {
			if subscriptionService == nil {
				middleware.AbortWithRequestError(c, http.StatusForbidden, "CUSTOMIZATION_TARGET_SUBSCRIPTION_REQUIRED", "No active subscription found for the target group")
				return
			}
			subscription, err = subscriptionService.GetActiveSubscription(c.Request.Context(), apiKey.User.ID, target.ID)
			if err != nil {
				middleware.AbortWithRequestError(c, http.StatusForbidden, "CUSTOMIZATION_TARGET_SUBSCRIPTION_INVALID", "Target group subscription is unavailable")
				return
			}
		}

		cloned := cloneCustomizationAPIKeyWithGroup(apiKey, target)
		c.Set(string(middleware.ContextKeyAPIKey), cloned)
		if subscription != nil {
			c.Set(string(middleware.ContextKeySubscription), subscription)
		} else {
			c.Set(string(middleware.ContextKeySubscription), nil)
		}
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, target))
		c.Next()
	}
}

// LocalResponseMiddleware handles the historical short-circuit action after
// group/model admission. It reuses the decision made by GroupMappingMiddleware.
func (s *CustomizationService) LocalResponseMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		decision := s.matchDecision(c)
		if decision == nil || decision.rule.rule.Action != service.GatewayChannelCustomizationActionLocalResponse {
			c.Next()
			return
		}
		rule := decision.rule
		if !waitCustomizationDelay(c.Request.Context(), rule.rule.MinDelayMs, rule.rule.MaxDelayMs) {
			c.Abort()
			return
		}
		if s.current.Load() != decision.state {
			c.Next()
			return
		}
		s.incrementHit(rule.fingerprint)
		c.Header("Content-Type", rule.rule.ContentType)
		if rule.rule.Body == "" {
			c.Status(rule.rule.StatusCode)
		} else {
			c.Data(rule.rule.StatusCode, rule.rule.ContentType, []byte(rule.rule.Body))
		}
		c.Abort()
	}
}

func (s *CustomizationService) matchDecision(c *gin.Context) *customizationDecision {
	if c == nil {
		return nil
	}
	if cached, ok := c.Get(customizationDecisionContextKey); ok {
		decision, _ := cached.(*customizationDecision)
		return decision
	}
	state := s.current.Load()
	if state == nil || len(state.rules) == 0 {
		c.Set(customizationDecisionContextKey, (*customizationDecision)(nil))
		return nil
	}
	apiKey, _ := middleware.GetAPIKeyFromContext(c)
	userID, userEmail := customizationUser(c, apiKey)
	for _, rule := range state.rules {
		if matchesCustomizationRule(rule, c, apiKey, userID, userEmail) {
			decision := &customizationDecision{state: state, rule: rule}
			c.Set(customizationDecisionContextKey, decision)
			return decision
		}
	}
	c.Set(customizationDecisionContextKey, (*customizationDecision)(nil))
	return nil
}

func isCustomizationBillingRequest(c *gin.Context) bool {
	return c != nil && c.Request != nil && c.Request.URL != nil && c.Request.URL.Path == "/v1/sub2api/billing"
}

func cloneCustomizationAPIKeyWithGroup(apiKey *service.APIKey, group *service.Group) *service.APIKey {
	if apiKey == nil || group == nil {
		return apiKey
	}
	cloned := *apiKey
	groupID := group.ID
	cloned.GroupID = &groupID
	cloned.Group = group
	return &cloned
}

func customizationTargetCompatible(c *gin.Context, group *service.Group) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil || !service.IsGroupContextValid(group) {
		return false
	}
	if forced, ok := middleware.GetForcePlatformFromContext(c); ok && forced != "" {
		return group.Platform == forced || group.Platform == service.PlatformComposite
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/antigravity/") {
		return group.Platform == service.PlatformAntigravity || group.Platform == service.PlatformComposite
	}
	if strings.HasPrefix(path, "/v1beta/") {
		return group.Platform == service.PlatformGemini || group.Platform == service.PlatformComposite
	}
	return true
}

func customizationGroupMappingError(err error) (int, string, string) {
	switch {
	case errors.Is(err, service.ErrGroupNotAllowed):
		return http.StatusForbidden, "CUSTOMIZATION_TARGET_GROUP_NOT_ALLOWED", "User is not allowed to use the target group"
	case errors.Is(err, service.ErrCustomizationTargetGroupInactive):
		return http.StatusForbidden, "CUSTOMIZATION_TARGET_GROUP_INACTIVE", "Target group is not active"
	default:
		return http.StatusBadRequest, "CUSTOMIZATION_TARGET_GROUP_INVALID", "Target group does not exist or cannot be loaded"
	}
}

func compileCustomizationRules(rules []service.GatewayChannelCustomizationRule) ([]compiledCustomizationRule, error) {
	compiled := make([]compiledCustomizationRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		item := compiledCustomizationRule{
			rule:              rule,
			apiKeyIDs:         make(map[int64]struct{}, len(rule.APIKeyIDs)),
			apiKeyNames:       make(map[string]struct{}, len(rule.APIKeyNames)),
			userIDs:           make(map[int64]struct{}, len(rule.UserIDs)),
			userEmails:        make(map[string]struct{}, len(rule.UserEmails)),
			methods:           make(map[string]struct{}, len(rule.Methods)),
			exactPaths:        make(map[string]struct{}, len(rule.ExactPaths)),
			pathPrefixes:      append([]string(nil), rule.PathPrefixes...),
			userAgentContains: append([]string(nil), rule.UserAgentContains...),
			queryParams:       make(map[string]map[string]struct{}, len(rule.QueryParams)),
		}
		for _, id := range rule.APIKeyIDs {
			item.apiKeyIDs[id] = struct{}{}
		}
		for _, name := range rule.APIKeyNames {
			item.apiKeyNames[strings.ToLower(name)] = struct{}{}
		}
		for _, id := range rule.UserIDs {
			item.userIDs[id] = struct{}{}
		}
		for _, email := range rule.UserEmails {
			item.userEmails[strings.ToLower(email)] = struct{}{}
		}
		for _, method := range rule.Methods {
			item.methods[method] = struct{}{}
		}
		for _, path := range rule.ExactPaths {
			item.exactPaths[path] = struct{}{}
		}
		for key, values := range rule.QueryParams {
			item.queryParams[key] = make(map[string]struct{}, len(values))
			for _, value := range values {
				item.queryParams[key][value] = struct{}{}
			}
		}
		if rule.RequestMessageMatchMode == service.GatewayChannelCustomizationRequestMessageMatchModeRegex && rule.RequestMessageText != "" {
			pattern, err := regexp.Compile(fullRequestMessagePattern(rule.RequestMessageText))
			if err != nil {
				return nil, fmt.Errorf("compile request message regex for rule %q: %w", rule.Name, err)
			}
			item.requestMessagePattern = pattern
		}
		item.fingerprint = customizationHitFingerprint(rule)
		compiled = append(compiled, item)
	}
	return compiled, nil
}

func matchesCustomizationRule(rule compiledCustomizationRule, c *gin.Context, apiKey *service.APIKey, userID int64, userEmail string) bool {
	if !matchesCustomizationTarget(rule, apiKey, userID, userEmail) {
		return false
	}
	return matchesCustomizationRequestConditions(rule, c)
}

func matchesCustomizationTarget(rule compiledCustomizationRule, apiKey *service.APIKey, userID int64, userEmail string) bool {
	targetMatched := false
	if apiKey != nil {
		_, targetMatched = rule.apiKeyIDs[apiKey.ID]
		if !targetMatched {
			_, targetMatched = rule.apiKeyNames[strings.ToLower(strings.TrimSpace(apiKey.Name))]
		}
	}
	if !targetMatched {
		_, targetMatched = rule.userIDs[userID]
	}
	if !targetMatched {
		_, targetMatched = rule.userEmails[strings.ToLower(strings.TrimSpace(userEmail))]
	}
	if !targetMatched {
		return false
	}
	return true
}

func matchesCustomizationRequestConditions(rule compiledCustomizationRule, c *gin.Context) bool {
	if !matchesCustomizationRequestMetadata(rule, c) {
		return false
	}
	if rule.rule.RequestMessageText != "" && !matchesRequestMessageText(c, rule) {
		return false
	}
	return true
}

func matchesCustomizationRequestMetadata(rule compiledCustomizationRule, c *gin.Context) bool {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return false
	}
	request := c.Request
	methodMatched := len(rule.methods) == 0
	if !methodMatched {
		_, methodMatched = rule.methods[request.Method]
	}
	pathMatched := len(rule.exactPaths) == 0 && len(rule.pathPrefixes) == 0
	if !pathMatched {
		_, pathMatched = rule.exactPaths[request.URL.Path]
		if !pathMatched {
			for _, prefix := range rule.pathPrefixes {
				if strings.HasPrefix(request.URL.Path, prefix) {
					pathMatched = true
					break
				}
			}
		}
	}
	uaMatched := len(rule.userAgentContains) == 0
	if !uaMatched {
		ua := request.UserAgent()
		for _, part := range rule.userAgentContains {
			if strings.Contains(ua, part) {
				uaMatched = true
				break
			}
		}
	}
	queryMatched := len(rule.queryParams) == 0
	if !queryMatched {
		queryMatched = true
		for key, values := range rule.queryParams {
			actual := request.URL.Query()[key]
			found := false
			for _, value := range actual {
				if _, ok := values[value]; ok {
					found = true
					break
				}
			}
			if !found {
				queryMatched = false
				break
			}
		}
	}
	if !methodMatched || !pathMatched || !uaMatched || !queryMatched {
		return false
	}
	return true
}

func matchesRequestMessageText(c *gin.Context, rule compiledCustomizationRule) bool {
	if c == nil || c.Request == nil || c.Request.Body == nil {
		return false
	}
	original := c.Request.Body
	prefix, err := io.ReadAll(io.LimitReader(original, customizationRequestBodyMaxBytes+1))
	c.Request.Body = &replayRequestBody{
		Reader:   io.MultiReader(bytes.NewReader(prefix), original),
		original: original,
	}
	if err != nil || len(prefix) > customizationRequestBodyMaxBytes {
		return false
	}
	return requestBodyMatchesMessageText(prefix, rule)
}

func requestBodyMatchesMessageText(body []byte, rule compiledCustomizationRule) bool {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return false
	}
	texts := make([]string, 0, 1)
	for key, raw := range root {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		switch key {
		case "input":
			collectInputMessageTexts(value, &texts)
		case "messages":
			collectMessagesMessageTexts(value, &texts)
		case "contents":
			collectContentsMessageTexts(value, &texts)
		default:
			continue
		}
	}
	if len(texts) != 1 {
		return false
	}
	if rule.rule.RequestMessageMatchMode == service.GatewayChannelCustomizationRequestMessageMatchModeRegex {
		return rule.requestMessagePattern != nil && rule.requestMessagePattern.MatchString(texts[0])
	}
	return strings.TrimSpace(texts[0]) == strings.TrimSpace(rule.rule.RequestMessageText)
}

func fullRequestMessagePattern(pattern string) string {
	return `\A(?:` + pattern + `)\z`
}

func collectInputMessageTexts(value any, texts *[]string) {
	switch item := value.(type) {
	case string:
		*texts = append(*texts, item)
	case []any:
		for _, child := range item {
			collectInputMessageTexts(child, texts)
		}
	case map[string]any:
		if role, ok := item["role"].(string); ok && !strings.EqualFold(role, "user") {
			return
		}
		if text, ok := item["text"].(string); ok {
			*texts = append(*texts, text)
		}
		if content, ok := item["content"]; ok {
			collectContentMessageTexts(content, texts)
		}
	}
}

func collectMessagesMessageTexts(value any, texts *[]string) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, ok := message["role"].(string); ok && !strings.EqualFold(role, "user") {
			continue
		}
		if content, ok := message["content"]; ok {
			collectContentMessageTexts(content, texts)
		}
	}
}

func collectContentsMessageTexts(value any, texts *[]string) {
	items, ok := value.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		content, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, ok := content["role"].(string); ok && !strings.EqualFold(role, "user") {
			continue
		}
		if parts, ok := content["parts"]; ok {
			collectContentMessageTexts(parts, texts)
		}
	}
}

func collectContentMessageTexts(value any, texts *[]string) {
	switch item := value.(type) {
	case string:
		*texts = append(*texts, item)
	case []any:
		for _, child := range item {
			collectContentMessageTexts(child, texts)
		}
	case map[string]any:
		if text, ok := item["text"].(string); ok {
			*texts = append(*texts, text)
		}
	}
}

func customizationUser(c *gin.Context, apiKey *service.APIKey) (int64, string) {
	var userID int64
	var email string
	if apiKey != nil {
		userID, email = apiKey.UserID, ""
		if apiKey.User != nil {
			if apiKey.User.ID > 0 {
				userID = apiKey.User.ID
			}
			email = apiKey.User.Email
		}
	}
	if subject, ok := middleware.GetAuthSubjectFromContext(c); ok && subject.UserID > 0 {
		userID = subject.UserID
	}
	return userID, email
}

func waitCustomizationDelay(ctx context.Context, minMs, maxMs int) bool {
	delay := minMs
	if maxMs > minMs {
		delay += rand.Intn(maxMs - minMs + 1)
	}
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func cloneCustomizationSettings(in service.GatewayChannelCustomizationSettings) service.GatewayChannelCustomizationSettings {
	return serviceCloneGatewayCustomizationSettings(in)
}

func serviceCloneGatewayCustomizationSettings(in service.GatewayChannelCustomizationSettings) service.GatewayChannelCustomizationSettings {
	out := service.GatewayChannelCustomizationSettings{Rules: make([]service.GatewayChannelCustomizationRule, len(in.Rules))}
	copy(out.Rules, in.Rules)
	for i := range out.Rules {
		rule := &out.Rules[i]
		rule.APIKeyIDs = append([]int64(nil), rule.APIKeyIDs...)
		rule.APIKeyNames = append([]string(nil), rule.APIKeyNames...)
		rule.UserIDs = append([]int64(nil), rule.UserIDs...)
		rule.UserEmails = append([]string(nil), rule.UserEmails...)
		rule.Methods = append([]string(nil), rule.Methods...)
		rule.ExactPaths = append([]string(nil), rule.ExactPaths...)
		rule.PathPrefixes = append([]string(nil), rule.PathPrefixes...)
		rule.UserAgentContains = append([]string(nil), rule.UserAgentContains...)
		rule.QueryParams = make(map[string][]string, len(rule.QueryParams))
		for key, values := range rule.QueryParams {
			rule.QueryParams[key] = append([]string(nil), values...)
		}
	}
	return out
}

func serviceGatewayCustomizationSettingsEqual(a, b service.GatewayChannelCustomizationSettings) bool {
	return customizationSettingsJSONEqual(a, b)
}

func customizationSettingsJSONEqual(a, b service.GatewayChannelCustomizationSettings) bool {
	// 配置已规范化；JSON 比较仅用于判断是否需要替换原子快照。
	return customizationSettingsBytes(a) == customizationSettingsBytes(b)
}

func customizationSettingsBytes(settings service.GatewayChannelCustomizationSettings) string {
	data, err := json.Marshal(settings)
	if err != nil {
		return ""
	}
	return string(data)
}

func mustJSON(value any) []byte {
	// 仅处理内存中的已知配置结构，序列化失败时返回空字节使其进入替换路径。
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return data
}

func (s *CustomizationService) HitCounts(rules []service.GatewayChannelCustomizationRule) []int64 {
	out := make([]int64, len(rules))
	if s == nil {
		return out
	}
	for i := range rules {
		out[i] = s.hitCount(customizationHitFingerprint(rules[i]))
	}
	return out
}

func (s *CustomizationService) incrementHit(fingerprint string) {
	if s == nil || fingerprint == "" {
		return
	}
	actual, _ := s.hits.LoadOrStore(fingerprint, &atomic.Int64{})
	actual.(*atomic.Int64).Add(1)
}

func (s *CustomizationService) hitCount(fingerprint string) int64 {
	if s == nil || fingerprint == "" {
		return 0
	}
	value, ok := s.hits.Load(fingerprint)
	if !ok {
		return 0
	}
	return value.(*atomic.Int64).Load()
}

func customizationHitFingerprint(rule service.GatewayChannelCustomizationRule) string {
	query := make(map[string][]string, len(rule.QueryParams))
	for key, values := range rule.QueryParams {
		query[key] = sortedStrings(values)
	}
	matchMode := rule.RequestMessageMatchMode
	if matchMode == "" {
		matchMode = service.GatewayChannelCustomizationRequestMessageMatchModeExact
	}
	requestMessageText := rule.RequestMessageText
	if matchMode == service.GatewayChannelCustomizationRequestMessageMatchModeExact {
		requestMessageText = strings.TrimSpace(requestMessageText)
	}
	action := rule.Action
	if action == "" {
		action = service.GatewayChannelCustomizationActionLocalResponse
	}
	payload := struct {
		Name                    string              `json:"name"`
		Action                  string              `json:"action"`
		TargetGroupID           *int64              `json:"target_group_id,omitempty"`
		APIKeyIDs               []int64             `json:"api_key_ids"`
		APIKeyNames             []string            `json:"api_key_names"`
		UserIDs                 []int64             `json:"user_ids"`
		UserEmails              []string            `json:"user_emails"`
		Methods                 []string            `json:"methods"`
		ExactPaths              []string            `json:"exact_paths"`
		PathPrefixes            []string            `json:"path_prefixes"`
		UserAgentContains       []string            `json:"user_agent_contains"`
		QueryParams             map[string][]string `json:"query_params"`
		RequestMessageMatchMode string              `json:"request_message_match_mode"`
		RequestMessageText      string              `json:"request_message_text"`
	}{
		Name:                    strings.TrimSpace(rule.Name),
		Action:                  action,
		TargetGroupID:           rule.TargetGroupID,
		APIKeyIDs:               sortedInt64s(rule.APIKeyIDs),
		APIKeyNames:             sortedLowerStrings(rule.APIKeyNames),
		UserIDs:                 sortedInt64s(rule.UserIDs),
		UserEmails:              sortedLowerStrings(rule.UserEmails),
		Methods:                 sortedStrings(rule.Methods),
		ExactPaths:              sortedStrings(rule.ExactPaths),
		PathPrefixes:            sortedStrings(rule.PathPrefixes),
		UserAgentContains:       sortedStrings(rule.UserAgentContains),
		QueryParams:             query,
		RequestMessageMatchMode: matchMode,
		RequestMessageText:      requestMessageText,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	return string(data)
}

func sortedStrings(values []string) []string {
	out := append([]string(nil), values...)
	slices.Sort(out)
	return out
}

func sortedLowerStrings(values []string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = strings.ToLower(strings.TrimSpace(value))
	}
	slices.Sort(out)
	return out
}

func sortedInt64s(values []int64) []int64 {
	out := append([]int64(nil), values...)
	slices.Sort(out)
	return out
}

func generationOfCustomization(state *customizationRuntimeState) uint64 {
	if state == nil {
		return 0
	}
	return state.generation
}
