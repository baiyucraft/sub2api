package service

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// gatewayInputTokenEstimateKey is intentionally private. The estimate is an
// internal scheduling hint derived from the already-read request body; it is
// not part of the public request context contract.
type gatewayInputTokenEstimateKey struct{}

// WithGatewayInputTokenEstimate attaches the estimated number of input tokens
// to a request context. A non-positive value is retained as zero so callers
// can distinguish an explicitly estimated empty request from a missing hint.
func WithGatewayInputTokenEstimate(ctx context.Context, tokens int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tokens < 0 {
		tokens = 0
	}
	return context.WithValue(ctx, gatewayInputTokenEstimateKey{}, tokens)
}

// GatewayInputTokenEstimate returns the request's estimated input token count.
// The boolean is false when the handler has not installed an estimate yet;
// schedulers treat that case as backward-compatible unlimited input.
func GatewayInputTokenEstimate(ctx context.Context) (int, bool) {
	if ctx == nil {
		return 0, false
	}
	tokens, ok := ctx.Value(gatewayInputTokenEstimateKey{}).(int)
	if !ok {
		return 0, false
	}
	if tokens < 0 {
		return 0, true
	}
	return tokens, true
}

// AccountInputLengthFailureReason reports why an account cannot serve the
// current request because its configured minimum input threshold is not met.
// Missing request estimates intentionally pass so older/internal callers that
// do not read a body first keep their existing scheduling behavior.
func AccountInputLengthFailureReason(ctx context.Context, account *Account) string {
	if account == nil || account.ProbeMinInputTokens <= 0 {
		return ""
	}
	tokens, ok := GatewayInputTokenEstimate(ctx)
	if !ok || tokens >= account.ProbeMinInputTokens {
		return ""
	}
	return "input_too_short"
}

// IsAccountInputLengthEligible is the boolean form used by lightweight
// schedulers and compatibility paths.
func IsAccountInputLengthEligible(ctx context.Context, account *Account) bool {
	return AccountInputLengthFailureReason(ctx, account) == ""
}

// EstimateGatewayInputTokens estimates user-provided input from a JSON
// gateway body. It deliberately ignores model names, roles, JSON field names,
// and other metadata. The estimate is conservative and deterministic: four
// Unicode code points are treated as roughly one token, with non-empty text
// rounded up to at least one token.
func EstimateGatewayInputTokens(body []byte, protocol string) int {
	if len(bytes.TrimSpace(body)) == 0 {
		return 0
	}
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		return 0
	}

	var text strings.Builder
	collectGatewayInputText(&text, payload, "")
	if text.Len() == 0 {
		return 0
	}
	// Keep protocol in the signature for call sites and future protocol-specific
	// extraction without making model-specific assumptions today.
	_ = strings.ToLower(strings.TrimSpace(protocol))
	count := utf8.RuneCountInString(text.String())
	if count <= 0 {
		return 0
	}
	tokens := (count + 3) / 4
	if tokens < 1 {
		return 1
	}
	return tokens
}

func collectGatewayInputText(out *strings.Builder, value any, key string) {
	switch typed := value.(type) {
	case string:
		if gatewayInputTextKey(key) && strings.TrimSpace(typed) != "" {
			out.WriteString(typed)
			out.WriteByte('\n')
		}
	case []any:
		for _, item := range typed {
			collectGatewayInputText(out, item, key)
		}
	case map[string]any:
		for childKey, child := range typed {
			collectGatewayInputText(out, child, childKey)
		}
	}
}

func gatewayInputTextKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "content", "text", "input_text", "output_text", "instructions", "system", "prompt", "input":
		return true
	default:
		return false
	}
}
