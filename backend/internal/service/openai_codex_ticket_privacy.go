package service

import (
	"maps"
	"strings"
)

// IsOpenAICodexTicketExtraKey identifies historical server-owned STATE fields.
// These names remain reserved after the built-in runtime has been retired.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return key == "codex_ticket_watchdog" || key == "codex_ticket_config" ||
		strings.HasPrefix(key, "codex_ticket_watchdog:") || strings.HasPrefix(key, "codex_turn_ticket:")
}

// IsOpenAICodexTicketPrivateExtraKey includes credentials in legacy proxy fields.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// MergeOpenAICodexTicketExtra keeps persisted private fields opaque and unchanged
// during ordinary edits, and strips user-supplied material on create/import.
// The repository also applies this under its row lock to protect concurrent edits.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := RedactOpenAICodexTicketExtra(extra)
	for key, value := range current {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// RedactOpenAICodexTicketExtra removes historical STATE material from public
// responses and exports without changing the stored account or unrelated fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
