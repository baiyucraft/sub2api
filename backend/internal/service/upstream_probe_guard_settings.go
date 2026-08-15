package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultUpstreamProbeGuardSuspendAfterFailures = 3
	DefaultUpstreamProbeGuardRecoverySuccesses    = 3
	MinUpstreamProbeGuardThreshold                = 1
	MaxUpstreamProbeGuardThreshold                = 20
)

// UpstreamProbeGuardSettings is global to all upstream keys. The custom codes
// extend the built-in probe classification; they never replace it.
type UpstreamProbeGuardSettings struct {
	Enabled                 bool  `json:"enabled"`
	SuspendAfterFailures    int   `json:"suspend_after_failures"`
	RecoverySuccesses       int   `json:"recovery_successes"`
	CustomErrorCodesEnabled bool  `json:"custom_error_codes_enabled"`
	CustomErrorCodes        []int `json:"custom_error_codes"`
}

func DefaultUpstreamProbeGuardSettings() UpstreamProbeGuardSettings {
	return UpstreamProbeGuardSettings{
		Enabled:              true,
		SuspendAfterFailures: DefaultUpstreamProbeGuardSuspendAfterFailures,
		RecoverySuccesses:    DefaultUpstreamProbeGuardRecoverySuccesses,
		CustomErrorCodes:     []int{},
	}
}

func NormalizeUpstreamProbeGuardSettings(settings UpstreamProbeGuardSettings) (UpstreamProbeGuardSettings, error) {
	if settings.SuspendAfterFailures == 0 {
		settings.SuspendAfterFailures = DefaultUpstreamProbeGuardSuspendAfterFailures
	}
	if settings.RecoverySuccesses == 0 {
		settings.RecoverySuccesses = DefaultUpstreamProbeGuardRecoverySuccesses
	}
	if settings.SuspendAfterFailures < MinUpstreamProbeGuardThreshold || settings.SuspendAfterFailures > MaxUpstreamProbeGuardThreshold {
		return UpstreamProbeGuardSettings{}, infraerrors.BadRequest("UPSTREAM_PROBE_GUARD_FAILURE_THRESHOLD_INVALID", "suspend_after_failures must be between 1 and 20")
	}
	if settings.RecoverySuccesses < MinUpstreamProbeGuardThreshold || settings.RecoverySuccesses > MaxUpstreamProbeGuardThreshold {
		return UpstreamProbeGuardSettings{}, infraerrors.BadRequest("UPSTREAM_PROBE_GUARD_RECOVERY_THRESHOLD_INVALID", "recovery_successes must be between 1 and 20")
	}
	seen := make(map[int]struct{}, len(settings.CustomErrorCodes))
	codes := make([]int, 0, len(settings.CustomErrorCodes))
	for _, code := range settings.CustomErrorCodes {
		if code < 100 || code > 599 {
			return UpstreamProbeGuardSettings{}, infraerrors.BadRequest("UPSTREAM_PROBE_GUARD_ERROR_CODE_INVALID", fmt.Sprintf("custom error code %d must be between 100 and 599", code))
		}
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	sort.Ints(codes)
	settings.CustomErrorCodes = codes
	return settings, nil
}

func (s *SettingService) GetUpstreamProbeGuardSettings(ctx context.Context) (UpstreamProbeGuardSettings, error) {
	defaults := DefaultUpstreamProbeGuardSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamProbeGuardSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return defaults, err
	}
	var settings UpstreamProbeGuardSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return defaults, fmt.Errorf("unmarshal upstream probe guard settings: %w", err)
	}
	normalized, err := NormalizeUpstreamProbeGuardSettings(settings)
	if err != nil {
		return defaults, nil
	}
	return normalized, nil
}

func (s *SettingService) validateAndMarshalUpstreamProbeGuard(settings UpstreamProbeGuardSettings) (string, UpstreamProbeGuardSettings, error) {
	settings, err := NormalizeUpstreamProbeGuardSettings(settings)
	if err != nil {
		return "", UpstreamProbeGuardSettings{}, err
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return "", UpstreamProbeGuardSettings{}, fmt.Errorf("marshal upstream probe guard settings: %w", err)
	}
	return string(raw), settings, nil
}

func (s *SettingService) SetUpstreamProbeGuardSettings(ctx context.Context, settings UpstreamProbeGuardSettings) error {
	raw, _, err := s.validateAndMarshalUpstreamProbeGuard(settings)
	if err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	return s.settingRepo.Set(ctx, SettingKeyUpstreamProbeGuardSettings, raw)
}

func upstreamProbeGuardCustomCodeSet(settings UpstreamProbeGuardSettings) map[int]struct{} {
	set := make(map[int]struct{})
	if !settings.CustomErrorCodesEnabled {
		return set
	}
	for _, code := range settings.CustomErrorCodes {
		set[code] = struct{}{}
	}
	return set
}

func upstreamProbeStatusCode(status string) int {
	status = strings.TrimSpace(status)
	var code int
	if _, err := fmt.Sscanf(status, "%d", &code); err != nil {
		return 0
	}
	return code
}
