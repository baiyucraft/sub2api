const EDITABLE_CREDENTIAL_KEYS = new Set([
  'account_scheduling_threshold',
  'compact_model_mapping',
  'custom_error_codes',
  'custom_error_codes_enabled',
  'header_override_enabled',
  'header_overrides',
  'intercept_warmup_requests',
  'model_mapping',
  'openai_capabilities',
  'pool_mode',
  'pool_mode_retry_count',
  'pool_mode_retry_status_codes',
  'temp_unschedulable_enabled',
  'temp_unschedulable_rules'
])

const EDITABLE_EXTRA_KEYS = new Set([
  'allow_overages',
  'anthropic_apikey_auth_scheme',
  'anthropic_passthrough',
  'auto_pause_5h_disabled',
  'auto_pause_5h_threshold',
  'auto_pause_7d_disabled',
  'auto_pause_7d_threshold',
  'codex_image_generation_bridge',
  'codex_image_generation_explicit_tool_policy',
  'mixed_scheduling',
  'openai_apikey_responses_websockets_v2_enabled',
  'openai_apikey_responses_websockets_v2_mode',
  'openai_compact_mode',
  'openai_long_context_billing_enabled',
  'openai_passthrough',
  'openai_responses_mode',
  'quota_daily_limit',
  'quota_daily_reset_hour',
  'quota_daily_reset_mode',
  'quota_limit',
  'quota_notify_daily_enabled',
  'quota_notify_daily_threshold',
  'quota_notify_daily_threshold_type',
  'quota_notify_total_enabled',
  'quota_notify_total_threshold',
  'quota_notify_total_threshold_type',
  'quota_notify_weekly_enabled',
  'quota_notify_weekly_threshold',
  'quota_notify_weekly_threshold_type',
  'quota_reset_timezone',
  'quota_weekly_limit',
  'quota_weekly_reset_day',
  'quota_weekly_reset_hour',
  'quota_weekly_reset_mode',
  'web_search_emulation'
])

function pickKeys(source: Record<string, unknown> | undefined, allowed: Set<string>) {
  if (!source) return undefined
  return Object.fromEntries(Object.entries(source).filter(([key]) => allowed.has(key)))
}

export function pickUpstreamAccountEditableCredentials(source: Record<string, unknown> | undefined) {
  return pickKeys(source, EDITABLE_CREDENTIAL_KEYS)
}

export function pickUpstreamAccountEditableExtra(source: Record<string, unknown> | undefined) {
  return pickKeys(source, EDITABLE_EXTRA_KEYS)
}
