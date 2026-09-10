package observer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	rule              service.GatewayChannelCustomizationRule
	apiKeyIDs         map[int64]struct{}
	apiKeyNames       map[string]struct{}
	userIDs           map[int64]struct{}
	userEmails        map[string]struct{}
	methods           map[string]struct{}
	exactPaths        map[string]struct{}
	pathPrefixes      []string
	userAgentContains []string
	queryParams       map[string]map[string]struct{}
	fingerprint       string
}

const customizationRequestBodyMaxBytes = 256 * 1024

type replayRequestBody struct {
	io.Reader
	original io.Closer
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
	next := &customizationRuntimeState{
		generation: generationOfCustomization(previous) + 1,
		settings:   cloneCustomizationSettings(settings),
		rules:      compileCustomizationRules(settings.Rules),
	}
	s.current.Store(next)
	return nil
}

// Middleware 只在认证上下文中匹配目标；只有规则声明消息文本条件时才读取请求体。
func (s *CustomizationService) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		state := s.current.Load()
		if state == nil || len(state.rules) == 0 {
			c.Next()
			return
		}
		apiKey, _ := middleware.GetAPIKeyFromContext(c)
		userID, userEmail := customizationUser(c, apiKey)
		for _, rule := range state.rules {
			if !matchesCustomizationRule(rule, c, apiKey, userID, userEmail) {
				continue
			}
			if !waitCustomizationDelay(c.Request.Context(), rule.rule.MinDelayMs, rule.rule.MaxDelayMs) {
				c.Abort()
				return
			}
			if s.current.Load() != state {
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
			return
		}
		c.Next()
	}
}

func compileCustomizationRules(rules []service.GatewayChannelCustomizationRule) []compiledCustomizationRule {
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
		item.fingerprint = customizationHitFingerprint(rule)
		compiled = append(compiled, item)
	}
	return compiled
}

func matchesCustomizationRule(rule compiledCustomizationRule, c *gin.Context, apiKey *service.APIKey, userID int64, userEmail string) bool {
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
	if rule.rule.RequestMessageText != "" && !matchesRequestMessageText(c, rule.rule.RequestMessageText) {
		return false
	}
	return true
}

func matchesRequestMessageText(c *gin.Context, expected string) bool {
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
	return requestBodyContainsExactMessageText(prefix, expected)
}

func requestBodyContainsExactMessageText(body []byte, expected string) bool {
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
	return len(texts) == 1 && texts[0] == expected
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
	payload := struct {
		Name               string              `json:"name"`
		APIKeyIDs          []int64             `json:"api_key_ids"`
		APIKeyNames        []string            `json:"api_key_names"`
		UserIDs            []int64             `json:"user_ids"`
		UserEmails         []string            `json:"user_emails"`
		Methods            []string            `json:"methods"`
		ExactPaths         []string            `json:"exact_paths"`
		PathPrefixes       []string            `json:"path_prefixes"`
		UserAgentContains  []string            `json:"user_agent_contains"`
		QueryParams        map[string][]string `json:"query_params"`
		RequestMessageText string              `json:"request_message_text"`
	}{
		Name:               strings.TrimSpace(rule.Name),
		APIKeyIDs:          sortedInt64s(rule.APIKeyIDs),
		APIKeyNames:        sortedLowerStrings(rule.APIKeyNames),
		UserIDs:            sortedInt64s(rule.UserIDs),
		UserEmails:         sortedLowerStrings(rule.UserEmails),
		Methods:            sortedStrings(rule.Methods),
		ExactPaths:         sortedStrings(rule.ExactPaths),
		PathPrefixes:       sortedStrings(rule.PathPrefixes),
		UserAgentContains:  sortedStrings(rule.UserAgentContains),
		QueryParams:        query,
		RequestMessageText: strings.TrimSpace(rule.RequestMessageText),
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
