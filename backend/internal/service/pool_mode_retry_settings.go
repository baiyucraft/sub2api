package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
)

// globalPoolModeRetryStatusConfig is the process-wide retry-status policy used
// by upstream accounts. Account-level credential values are intentionally not
// consulted once this policy is installed, so newly-created accounts inherit
// the same behavior automatically.
type globalPoolModeRetryStatusConfig struct {
	codes      []int
	configured bool
}

var globalPoolModeRetryStatusConfigValue atomic.Value // globalPoolModeRetryStatusConfig

func init() {
	globalPoolModeRetryStatusConfigValue.Store(globalPoolModeRetryStatusConfig{})
}

// SetGlobalPoolModeRetryStatusCodes installs the single retry-status policy.
// An empty list means the system default (401, 403, 429).
func SetGlobalPoolModeRetryStatusCodes(codes []int) {
	normalized, err := normalizeUpstreamPoolModeRetryStatusCodes(codes)
	if err != nil || len(normalized) == 0 {
		normalized = cloneUpstreamPoolModeRetryStatusCodes(defaultUpstreamPoolModeRetryStatusCodes)
	}
	globalPoolModeRetryStatusConfigValue.Store(globalPoolModeRetryStatusConfig{
		codes:      normalized,
		configured: true,
	})
}

// ResetGlobalPoolModeRetryStatusCodesForTest restores legacy account-local
// resolution for isolated unit tests. Production wiring never calls this.
func ResetGlobalPoolModeRetryStatusCodesForTest() {
	globalPoolModeRetryStatusConfigValue.Store(globalPoolModeRetryStatusConfig{})
}

// WarmUpstreamPoolModeRetryStatusCodes loads the single policy from persistent
// settings during application startup. Any missing or malformed value falls
// back to the documented defaults so a bad setting cannot disable retries.
func (s *SettingService) WarmUpstreamPoolModeRetryStatusCodes(ctx context.Context) {
	codes := cloneUpstreamPoolModeRetryStatusCodes(defaultUpstreamPoolModeRetryStatusCodes)
	if s != nil && s.settingRepo != nil {
		raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamPoolModeRetryStatusCodes)
		if err == nil {
			var stored []int
			if json.Unmarshal([]byte(raw), &stored) == nil {
				if normalized, normalizeErr := normalizeUpstreamPoolModeRetryStatusCodes(stored); normalizeErr == nil && len(normalized) > 0 {
					codes = normalized
				}
			}
		} else if !errors.Is(err, ErrSettingNotFound) {
			// Keep defaults on read failures; startup must remain available.
		}
	}
	SetGlobalPoolModeRetryStatusCodes(codes)
}

func globalPoolModeRetryStatusConfigured() ([]int, bool) {
	value := globalPoolModeRetryStatusConfigValue.Load()
	state, ok := value.(globalPoolModeRetryStatusConfig)
	if !ok || !state.configured {
		return nil, false
	}
	return cloneUpstreamPoolModeRetryStatusCodes(state.codes), true
}
