package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strings"
	"unicode/utf8"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	gatewayChannelCustomizationMaxRules       = 100
	gatewayChannelCustomizationMaxTargets     = 100
	gatewayChannelCustomizationMaxStringBytes = 256
	gatewayChannelCustomizationMaxBodyBytes   = 64 * 1024
	gatewayChannelCustomizationMaxDelayMs     = 10000
)

var gatewayCustomizationMethodPattern = regexp.MustCompile(`^[A-Z][A-Z0-9!#$%&'*+.^_` + "`" + `|~-]*$`)

// GatewayChannelCustomizationSettings 是渠道定制的持久化配置。
// 规则顺序即匹配优先级，运行时不会重排。
type GatewayChannelCustomizationSettings struct {
	Rules []GatewayChannelCustomizationRule `json:"rules"`
}

// GatewayChannelCustomizationRule 描述一个认证后请求的本地响应规则。
type GatewayChannelCustomizationRule struct {
	Name               string              `json:"name"`
	Enabled            bool                `json:"enabled"`
	APIKeyIDs          []int64             `json:"api_key_ids"`
	APIKeyNames        []string            `json:"api_key_names"`
	UserIDs            []int64             `json:"user_ids"`
	UserEmails         []string            `json:"user_emails"`
	Methods            []string            `json:"methods"`
	ExactPaths         []string            `json:"exact_paths"`
	PathPrefixes       []string            `json:"path_prefixes"`
	UserAgentContains  []string            `json:"user_agent_contains"`
	QueryParams        map[string][]string `json:"query_params"`
	RequestMessageText string              `json:"request_message_text,omitempty"`
	StatusCode         int                 `json:"status_code"`
	ContentType        string              `json:"content_type"`
	Body               string              `json:"body"`
	MinDelayMs         int                 `json:"min_delay_ms"`
	MaxDelayMs         int                 `json:"max_delay_ms"`
}

// GatewayChannelCustomizationRuntime 是设置服务使用的窄运行时接口。
// service 包不依赖具体 observer 实现，便于扩展独立挂载和测试替换。
type GatewayChannelCustomizationRuntime interface {
	Apply(context.Context, GatewayChannelCustomizationSettings) error
}

func DefaultGatewayChannelCustomizationSettings() *GatewayChannelCustomizationSettings {
	return &GatewayChannelCustomizationSettings{Rules: []GatewayChannelCustomizationRule{}}
}

func NormalizeGatewayChannelCustomizationSettings(settings *GatewayChannelCustomizationSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_SETTINGS", "settings cannot be nil")
	}
	if len(settings.Rules) > gatewayChannelCustomizationMaxRules {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RULES", fmt.Sprintf("at most %d rules are allowed", gatewayChannelCustomizationMaxRules))
	}
	for i := range settings.Rules {
		if err := normalizeAndValidateGatewayCustomizationRule(&settings.Rules[i], i); err != nil {
			return err
		}
	}
	return nil
}

func normalizeAndValidateGatewayCustomizationRule(rule *GatewayChannelCustomizationRule, index int) error {
	rule.Name = strings.TrimSpace(rule.Name)
	if rule.Name == "" {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RULE", fmt.Sprintf("rule %d name is required", index+1))
	}
	if !validUTF8AndMax(rule.Name, gatewayChannelCustomizationMaxStringBytes) {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RULE", fmt.Sprintf("rule %d name is too long or invalid", index+1))
	}
	var err error
	if rule.APIKeyIDs, err = normalizeCustomizationIDs(rule.APIKeyIDs, index, "API key IDs"); err != nil {
		return err
	}
	if rule.UserIDs, err = normalizeCustomizationIDs(rule.UserIDs, index, "user IDs"); err != nil {
		return err
	}
	rule.APIKeyNames = normalizeCustomizationStrings(rule.APIKeyNames)
	rule.UserEmails = normalizeCustomizationEmails(rule.UserEmails)
	rule.Methods = normalizeCustomizationMethods(rule.Methods)
	rule.ExactPaths = normalizeCustomizationPaths(rule.ExactPaths)
	rule.PathPrefixes = normalizeCustomizationPaths(rule.PathPrefixes)
	rule.UserAgentContains = normalizeCustomizationStrings(rule.UserAgentContains)
	rule.RequestMessageText = strings.TrimSpace(rule.RequestMessageText)
	if err := validateCustomizationStringList(rule.APIKeyNames, index, "API key names"); err != nil {
		return err
	}
	if err := validateCustomizationStringList(rule.UserEmails, index, "user emails"); err != nil {
		return err
	}
	if err := validateCustomizationStringList(rule.Methods, index, "methods"); err != nil {
		return err
	}
	if err := validateCustomizationStringList(rule.ExactPaths, index, "exact paths"); err != nil {
		return err
	}
	if err := validateCustomizationStringList(rule.PathPrefixes, index, "path prefixes"); err != nil {
		return err
	}
	if err := validateCustomizationStringList(rule.UserAgentContains, index, "user agent conditions"); err != nil {
		return err
	}
	if !validUTF8AndMax(rule.RequestMessageText, gatewayChannelCustomizationMaxStringBytes) {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d request message text is too long or invalid", index+1))
	}
	if err := normalizeCustomizationQueryParams(rule, index); err != nil {
		return err
	}
	if rule.StatusCode == 0 {
		rule.StatusCode = http.StatusOK
	}
	if rule.StatusCode < 200 || rule.StatusCode > 599 {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RESPONSE", fmt.Sprintf("rule %d status code must be between 200 and 599", index+1))
	}
	rule.ContentType = strings.TrimSpace(rule.ContentType)
	if rule.ContentType == "" {
		rule.ContentType = "application/json"
	}
	if !validUTF8AndMax(rule.ContentType, gatewayChannelCustomizationMaxStringBytes) {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RESPONSE", fmt.Sprintf("rule %d content type is too long or invalid", index+1))
	}
	if !utf8.ValidString(rule.Body) || len([]byte(rule.Body)) > gatewayChannelCustomizationMaxBodyBytes {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RESPONSE", fmt.Sprintf("rule %d body must be valid UTF-8 and at most %d bytes", index+1, gatewayChannelCustomizationMaxBodyBytes))
	}
	if rule.MinDelayMs < 0 || rule.MinDelayMs > gatewayChannelCustomizationMaxDelayMs || rule.MaxDelayMs < 0 || rule.MaxDelayMs > gatewayChannelCustomizationMaxDelayMs || rule.MinDelayMs > rule.MaxDelayMs {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_DELAY", fmt.Sprintf("rule %d delay must be between 0 and %d milliseconds", index+1, gatewayChannelCustomizationMaxDelayMs))
	}
	if statusMustNotHaveBody(rule.StatusCode) && rule.Body != "" {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RESPONSE", fmt.Sprintf("rule %d status code %d cannot have a response body", index+1, rule.StatusCode))
	}
	if rule.Enabled && (!hasCustomizationTarget(*rule) || !hasCustomizationCondition(*rule)) {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_RULE", fmt.Sprintf("enabled rule %d requires at least one target and one request condition", index+1))
	}
	return nil
}

func (s *SettingService) GetGatewayChannelCustomizationSettings(ctx context.Context) (*GatewayChannelCustomizationSettings, error) {
	defaults := DefaultGatewayChannelCustomizationSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayChannelCustomizationSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return nil, fmt.Errorf("get gateway channel customization settings: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return defaults, nil
	}
	var settings GatewayChannelCustomizationSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("unmarshal gateway channel customization settings: %w", err)
	}
	if err := NormalizeGatewayChannelCustomizationSettings(&settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// GetGatewayChannelCustomizationBundle 返回页面所需的观察器和短路规则配置。
func (s *SettingService) GetGatewayChannelCustomizationBundle(ctx context.Context) (*GatewayChannelCustomizationBundle, error) {
	observerSettings, err := s.GetGatewayRequestObserverSettings(ctx)
	if err != nil {
		return nil, err
	}
	customizationSettings, err := s.GetGatewayChannelCustomizationSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &GatewayChannelCustomizationBundle{Observer: *observerSettings, Rules: cloneGatewayChannelCustomizationSettings(*customizationSettings).Rules}, nil
}

type GatewayChannelCustomizationBundle struct {
	Observer GatewayRequestObserverSettings    `json:"observer"`
	Rules    []GatewayChannelCustomizationRule `json:"rules"`
}

func (s *SettingService) SetGatewayChannelCustomizationSettings(ctx context.Context, observerSettings *GatewayRequestObserverSettings, customizationSettings *GatewayChannelCustomizationSettings) error {
	if err := validateGatewayRequestObserverSettings(observerSettings); err != nil {
		return err
	}
	if err := NormalizeGatewayChannelCustomizationSettings(customizationSettings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	observerRaw, err := json.Marshal(observerSettings)
	if err != nil {
		return fmt.Errorf("marshal gateway request observer settings: %w", err)
	}
	customizationRaw, err := json.Marshal(customizationSettings)
	if err != nil {
		return fmt.Errorf("marshal gateway channel customization settings: %w", err)
	}
	previous, err := s.GetGatewayChannelCustomizationBundle(ctx)
	if err != nil {
		return err
	}
	observerRuntime := s.gatewayRequestObserverRuntimeSnapshot()
	customizationRuntime := s.gatewayChannelCustomizationRuntimeSnapshot()
	observerApplied := false
	customizationApplied := false
	rollback := func() {
		if customizationApplied && customizationRuntime != nil {
			if rollbackErr := customizationRuntime.Apply(ctx, GatewayChannelCustomizationSettings{Rules: previous.Rules}); rollbackErr != nil {
				// 回滚失败只记录在调用方错误之后，不能覆盖原始失败原因。
				fmt.Printf("gateway channel customization rollback failed: %v\n", rollbackErr)
			}
		}
		if observerApplied && observerRuntime != nil {
			if rollbackErr := observerRuntime.Apply(ctx, previous.Observer); rollbackErr != nil {
				fmt.Printf("gateway request observer rollback failed: %v\n", rollbackErr)
			}
		}
	}
	if observerRuntime != nil {
		if err := observerRuntime.Apply(ctx, *observerSettings); err != nil {
			return fmt.Errorf("apply gateway request observer settings: %w", err)
		}
		observerApplied = true
	}
	if customizationRuntime != nil {
		if err := customizationRuntime.Apply(ctx, *customizationSettings); err != nil {
			rollback()
			return fmt.Errorf("apply gateway channel customization settings: %w", err)
		}
		customizationApplied = true
	}
	if err := s.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeyGatewayRequestObserverSettings:      string(observerRaw),
		SettingKeyGatewayChannelCustomizationSettings: string(customizationRaw),
	}); err != nil {
		rollback()
		return fmt.Errorf("set gateway channel customization settings: %w", err)
	}
	return nil
}

func normalizeCustomizationIDs(values []int64, index int, label string) ([]int64, error) {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_TARGET", fmt.Sprintf("rule %d %s must be positive", index+1, label))
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if len(result) > gatewayChannelCustomizationMaxTargets {
		return nil, infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_TARGET", fmt.Sprintf("rule %d %s contain at most %d entries", index+1, label, gatewayChannelCustomizationMaxTargets))
	}
	return result, nil
}

func normalizeCustomizationStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeCustomizationEmails(values []string) []string {
	result := normalizeCustomizationStrings(values)
	for i := range result {
		result[i] = strings.ToLower(result[i])
		if _, err := mail.ParseAddress(result[i]); err != nil {
			// 具体错误在统一校验中返回，保留规范化后的值以便错误定位。
			continue
		}
	}
	return result
}

func normalizeCustomizationMethods(values []string) []string {
	result := normalizeCustomizationStrings(values)
	for i := range result {
		result[i] = strings.ToUpper(result[i])
	}
	return result
}

func normalizeCustomizationPaths(values []string) []string {
	result := normalizeCustomizationStrings(values)
	for i := range result {
		if !strings.HasPrefix(result[i], "/") {
			result[i] = "/" + result[i]
		}
	}
	return result
}

func validateCustomizationStringList(values []string, index int, label string) error {
	for _, value := range values {
		if !validUTF8AndMax(value, gatewayChannelCustomizationMaxStringBytes) {
			return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d %s contain an invalid or oversized value", index+1, label))
		}
	}
	if label == "methods" {
		for _, method := range values {
			if !gatewayCustomizationMethodPattern.MatchString(method) {
				return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d contains an invalid HTTP method", index+1))
			}
		}
	}
	if label == "user emails" {
		for _, email := range values {
			parsed, err := mail.ParseAddress(email)
			if err != nil || parsed.Address != email || !strings.Contains(email, "@") {
				return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_TARGET", fmt.Sprintf("rule %d contains an invalid user email", index+1))
			}
		}
	}
	return nil
}

func normalizeCustomizationQueryParams(rule *GatewayChannelCustomizationRule, index int) error {
	if len(rule.QueryParams) == 0 {
		rule.QueryParams = map[string][]string{}
		return nil
	}
	result := make(map[string][]string, len(rule.QueryParams))
	if len(rule.QueryParams) > gatewayChannelCustomizationMaxTargets {
		return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d has too many query parameter names", index+1))
	}
	for key, values := range rule.QueryParams {
		key = strings.TrimSpace(key)
		if key == "" || !validUTF8AndMax(key, gatewayChannelCustomizationMaxStringBytes) {
			return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d has an invalid query parameter name", index+1))
		}
		values = normalizeCustomizationStrings(values)
		if len(values) == 0 {
			return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d query parameter %q needs at least one value", index+1, key))
		}
		if len(values) > gatewayChannelCustomizationMaxTargets {
			return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d query parameter %q has too many values", index+1, key))
		}
		for _, value := range values {
			if !validUTF8AndMax(value, gatewayChannelCustomizationMaxStringBytes) {
				return infraerrors.BadRequest("INVALID_GATEWAY_CHANNEL_CUSTOMIZATION_CONDITION", fmt.Sprintf("rule %d query parameter %q has an invalid value", index+1, key))
			}
		}
		result[key] = values
	}
	rule.QueryParams = result
	return nil
}

func hasCustomizationTarget(rule GatewayChannelCustomizationRule) bool {
	return len(rule.APIKeyIDs) > 0 || len(rule.APIKeyNames) > 0 || len(rule.UserIDs) > 0 || len(rule.UserEmails) > 0
}

func hasCustomizationCondition(rule GatewayChannelCustomizationRule) bool {
	return len(rule.Methods) > 0 || len(rule.ExactPaths) > 0 || len(rule.PathPrefixes) > 0 || len(rule.UserAgentContains) > 0 || len(rule.QueryParams) > 0 || rule.RequestMessageText != ""
}

func statusMustNotHaveBody(status int) bool {
	return status >= 100 && status < 200 || status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified
}

func validUTF8AndMax(value string, maxBytes int) bool {
	return utf8.ValidString(value) && len([]byte(value)) <= maxBytes
}

func cloneGatewayChannelCustomizationSettings(in GatewayChannelCustomizationSettings) GatewayChannelCustomizationSettings {
	out := GatewayChannelCustomizationSettings{Rules: make([]GatewayChannelCustomizationRule, len(in.Rules))}
	for i, rule := range in.Rules {
		out.Rules[i] = rule
		out.Rules[i].APIKeyIDs = append([]int64(nil), rule.APIKeyIDs...)
		out.Rules[i].APIKeyNames = append([]string(nil), rule.APIKeyNames...)
		out.Rules[i].UserIDs = append([]int64(nil), rule.UserIDs...)
		out.Rules[i].UserEmails = append([]string(nil), rule.UserEmails...)
		out.Rules[i].Methods = append([]string(nil), rule.Methods...)
		out.Rules[i].ExactPaths = append([]string(nil), rule.ExactPaths...)
		out.Rules[i].PathPrefixes = append([]string(nil), rule.PathPrefixes...)
		out.Rules[i].UserAgentContains = append([]string(nil), rule.UserAgentContains...)
		out.Rules[i].RequestMessageText = rule.RequestMessageText
		out.Rules[i].QueryParams = make(map[string][]string, len(rule.QueryParams))
		for key, values := range rule.QueryParams {
			out.Rules[i].QueryParams[key] = append([]string(nil), values...)
		}
	}
	return out
}

func equalGatewayChannelCustomizationSettings(a, b GatewayChannelCustomizationSettings) bool {
	left, right := cloneGatewayChannelCustomizationSettings(a), cloneGatewayChannelCustomizationSettings(b)
	return customizationJSONEqual(left, right)
}

func customizationJSONEqual(a, b GatewayChannelCustomizationSettings) bool {
	// JSON 比较只用于已规范化的快照等价判断；排序 map key 不影响编码结果。
	ba, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ba) == string(bb)
}
