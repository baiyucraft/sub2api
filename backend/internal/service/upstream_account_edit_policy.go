package service

import (
	"fmt"
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Upstream-derived accounts inherit identity, credentials, origin, proxy and
// scheduling price fields from their upstream config/key. The fields below are
// account-local behavior controls and are intentionally editable from the
// upstream-management account editor.
var upstreamAccountEditableCredentialKeys = map[string]struct{}{
	"account_scheduling_threshold": {},
	"compact_model_mapping":        {},
	"custom_error_codes":           {},
	"custom_error_codes_enabled":   {},
	"header_override_enabled":      {},
	"header_overrides":             {},
	"intercept_warmup_requests":    {},
	"model_mapping":                {},
	"openai_capabilities":          {},
	"pool_mode":                    {},
	"pool_mode_retry_count":        {},
	"pool_mode_retry_status_codes": {},
	"temp_unschedulable_enabled":   {},
	"temp_unschedulable_rules":     {},
}

var upstreamAccountEditableExtraKeys = map[string]struct{}{
	"allow_overages":                                {},
	"anthropic_apikey_auth_scheme":                  {},
	"anthropic_passthrough":                         {},
	"auto_pause_5h_disabled":                        {},
	"auto_pause_5h_threshold":                       {},
	"auto_pause_7d_disabled":                        {},
	"auto_pause_7d_threshold":                       {},
	"codex_image_generation_bridge":                 {},
	"codex_image_generation_explicit_tool_policy":   {},
	"mixed_scheduling":                              {},
	"openai_apikey_responses_websockets_v2_enabled": {},
	"openai_apikey_responses_websockets_v2_mode":    {},
	"openai_compact_mode":                           {},
	"openai_long_context_billing_enabled":           {},
	"openai_passthrough":                            {},
	"openai_responses_mode":                         {},
	"quota_daily_limit":                             {},
	"quota_daily_reset_hour":                        {},
	"quota_daily_reset_mode":                        {},
	"quota_limit":                                   {},
	"quota_notify_daily_enabled":                    {},
	"quota_notify_daily_threshold":                  {},
	"quota_notify_daily_threshold_type":             {},
	"quota_notify_total_enabled":                    {},
	"quota_notify_total_threshold":                  {},
	"quota_notify_total_threshold_type":             {},
	"quota_notify_weekly_enabled":                   {},
	"quota_notify_weekly_threshold":                 {},
	"quota_notify_weekly_threshold_type":            {},
	"quota_reset_timezone":                          {},
	"quota_weekly_limit":                            {},
	"quota_weekly_reset_day":                        {},
	"quota_weekly_reset_hour":                       {},
	"quota_weekly_reset_mode":                       {},
	"web_search_emulation":                          {},
}

func validateUpstreamAccountEditableKeys(values map[string]any, allowed map[string]struct{}, field string) error {
	for key := range values {
		if _, ok := allowed[key]; ok {
			continue
		}
		return infraerrors.BadRequest(
			"UPSTREAM_ACCOUNT_DERIVED_FIELDS_READ_ONLY",
			fmt.Sprintf("upstream account %s field %q is derived or runtime-managed", field, key),
		)
	}
	return nil
}

func validateUpstreamAccountEditableUpdate(input *UpdateAccountInput) error {
	if input == nil {
		return nil
	}
	if err := validateUpstreamAccountEditableKeys(input.Credentials, upstreamAccountEditableCredentialKeys, "credentials"); err != nil {
		return err
	}
	if err := validateUpstreamAccountEditableKeys(input.Extra, upstreamAccountEditableExtraKeys, "extra"); err != nil {
		return err
	}
	return validateUpstreamAccountEditableCredentialValues(input.Credentials)
}

func validateUpstreamAccountEditableCredentialValues(credentials map[string]any) error {
	if value, ok := credentials["pool_mode"]; ok {
		if _, valid := value.(bool); !valid {
			return infraerrors.BadRequest("INVALID_UPSTREAM_ACCOUNT_POOL_MODE", "pool_mode must be a boolean")
		}
	}
	if value, ok := credentials["pool_mode_retry_count"]; ok {
		count, valid := upstreamAccountInteger(value)
		if !valid || count < 0 || count > maxPoolModeRetryCount {
			return infraerrors.BadRequest("INVALID_UPSTREAM_ACCOUNT_POOL_MODE_RETRY_COUNT", "pool_mode_retry_count must be an integer between 0 and 10")
		}
	}
	if value, ok := credentials["pool_mode_retry_status_codes"]; ok && !validUpstreamAccountHTTPStatusCodes(value) {
		return infraerrors.BadRequest("INVALID_UPSTREAM_ACCOUNT_POOL_MODE_RETRY_STATUS_CODES", "pool_mode_retry_status_codes must contain HTTP status codes between 100 and 599")
	}
	return nil
}

func upstreamAccountInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float32:
		value := float64(typed)
		return int(value), !math.IsNaN(value) && !math.IsInf(value, 0) && value == math.Trunc(value)
	case float64:
		return int(typed), !math.IsNaN(typed) && !math.IsInf(typed, 0) && typed == math.Trunc(typed)
	default:
		return 0, false
	}
}

func validUpstreamAccountHTTPStatusCodes(value any) bool {
	var values []any
	switch typed := value.(type) {
	case []any:
		values = typed
	case []int:
		values = make([]any, len(typed))
		for index, item := range typed {
			values[index] = item
		}
	default:
		return false
	}
	for _, item := range values {
		code, valid := upstreamAccountInteger(item)
		if !valid || code < 100 || code > 599 {
			return false
		}
	}
	return true
}

// mergeUpstreamAccountEditableCredentials replaces the editable snapshot while
// preserving provider-derived or runtime-managed credential keys already held
// by the account. Shared base_url/api_key values are stripped before this runs.
func mergeUpstreamAccountEditableCredentials(existing, incoming map[string]any) map[string]any {
	merged := make(map[string]any, len(existing)+len(incoming))
	for key, value := range existing {
		if _, editable := upstreamAccountEditableCredentialKeys[key]; editable {
			continue
		}
		merged[key] = value
	}
	for key, value := range incoming {
		merged[key] = value
	}
	return merged
}

// mergeUpstreamAccountEditableExtra applies replacement semantics only to the
// editable subset while preserving health, probe, usage and other runtime keys.
func mergeUpstreamAccountEditableExtra(existing, incoming map[string]any) map[string]any {
	merged := make(map[string]any, len(existing)+len(incoming))
	for key, value := range existing {
		if _, editable := upstreamAccountEditableExtraKeys[key]; editable {
			continue
		}
		merged[key] = value
	}
	for key, value := range incoming {
		merged[key] = value
	}
	return merged
}

func isAllowedUpstreamAccountBulkCredentialsUpdate(credentials map[string]any) bool {
	if credentials == nil {
		return true
	}
	for key := range credentials {
		if key != "model_mapping" {
			return false
		}
	}
	return true
}
