package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	gatewayRequestObserverMaxTargets  = 100
	gatewayRequestObserverMaxNameLen  = 128
	gatewayRequestObserverMaxEmailLen = 320
	// GatewayRequestObserverOutputPath is fixed server-side so short-lived
	// request observations never depend on the process working directory.
	GatewayRequestObserverOutputPath = "/app/.tmp/maibon-probe-observation/requests.jsonl"
)

// GatewayRequestObserverSettings is the persisted, admin-facing observer
// configuration. It contains selectors only; output and capture limits remain
// fixed server-side so the feature stays narrowly scoped.
type GatewayRequestObserverSettings struct {
	Enabled     bool     `json:"enabled"`
	APIKeyIDs   []int64  `json:"api_key_ids"`
	APIKeyNames []string `json:"api_key_names"`
	UserIDs     []int64  `json:"user_ids"`
	UserEmails  []string `json:"user_emails"`
}

func DefaultGatewayRequestObserverSettings() *GatewayRequestObserverSettings {
	return &GatewayRequestObserverSettings{
		APIKeyIDs:   []int64{},
		APIKeyNames: []string{},
		UserIDs:     []int64{},
		UserEmails:  []string{},
	}
}

func normalizeGatewayRequestObserverSettings(settings *GatewayRequestObserverSettings) {
	if settings == nil {
		return
	}
	settings.APIKeyIDs = normalizePositiveIDs(settings.APIKeyIDs)
	settings.UserIDs = normalizePositiveIDs(settings.UserIDs)
	settings.APIKeyNames = normalizeObserverNames(settings.APIKeyNames)
	settings.UserEmails = normalizeObserverEmails(settings.UserEmails)
}

func validateGatewayRequestObserverSettings(settings *GatewayRequestObserverSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_SETTINGS", "settings cannot be nil")
	}
	for _, id := range settings.APIKeyIDs {
		if id <= 0 {
			return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_ID", "API key IDs must be positive")
		}
	}
	for _, id := range settings.UserIDs {
		if id <= 0 {
			return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_ID", "user IDs must be positive")
		}
	}
	for _, email := range settings.UserEmails {
		if len(strings.TrimSpace(email)) > gatewayRequestObserverMaxEmailLen {
			return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_EMAIL", fmt.Sprintf("user email must be at most %d characters", gatewayRequestObserverMaxEmailLen))
		}
	}
	normalizeGatewayRequestObserverSettings(settings)
	if len(settings.APIKeyIDs) > gatewayRequestObserverMaxTargets || len(settings.APIKeyNames) > gatewayRequestObserverMaxTargets || len(settings.UserIDs) > gatewayRequestObserverMaxTargets || len(settings.UserEmails) > gatewayRequestObserverMaxTargets {
		return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_TARGETS", fmt.Sprintf("each observer target list must contain at most %d entries", gatewayRequestObserverMaxTargets))
	}
	if settings.Enabled && len(settings.APIKeyIDs) == 0 && len(settings.APIKeyNames) == 0 && len(settings.UserIDs) == 0 && len(settings.UserEmails) == 0 {
		return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_TARGETS", "at least one API key or user target is required when observer is enabled")
	}
	for _, name := range settings.APIKeyNames {
		if len(name) > gatewayRequestObserverMaxNameLen {
			return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_NAME", fmt.Sprintf("API key name must be at most %d characters", gatewayRequestObserverMaxNameLen))
		}
	}
	for _, email := range settings.UserEmails {
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || !strings.Contains(email, "@") {
			return infraerrors.BadRequest("INVALID_GATEWAY_REQUEST_OBSERVER_EMAIL", "user emails must be valid email addresses")
		}
	}
	return nil
}

func (s *SettingService) GetGatewayRequestObserverSettings(ctx context.Context) (*GatewayRequestObserverSettings, error) {
	defaults := DefaultGatewayRequestObserverSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayRequestObserverSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return nil, fmt.Errorf("get gateway request observer settings: %w", err)
	}
	if strings.TrimSpace(raw) == "" {
		return defaults, nil
	}
	settings, err := decodeGatewayRequestObserverSettings(raw)
	if err != nil {
		slog.Warn("invalid gateway request observer settings; using disabled defaults", "error", err)
		return defaults, nil
	}
	return settings, nil
}

func decodeGatewayRequestObserverSettings(raw string) (*GatewayRequestObserverSettings, error) {
	type persistedSettings struct {
		Enabled     bool     `json:"enabled"`
		APIKeyIDs   []int64  `json:"api_key_ids"`
		APIKeyNames []string `json:"api_key_names"`
		UserIDs     []int64  `json:"user_ids"`
		UserEmails  []string `json:"user_emails"`
		AccountIDs  []int64  `json:"account_ids"`
	}

	var persisted persistedSettings
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return nil, err
	}
	settings := &GatewayRequestObserverSettings{
		Enabled: persisted.Enabled, APIKeyIDs: persisted.APIKeyIDs, APIKeyNames: persisted.APIKeyNames,
		UserIDs: persisted.UserIDs, UserEmails: persisted.UserEmails,
	}
	if err := validateGatewayRequestObserverSettings(settings); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *SettingService) SetGatewayRequestObserverSettings(ctx context.Context, settings *GatewayRequestObserverSettings) error {
	if err := validateGatewayRequestObserverSettings(settings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal gateway request observer settings: %w", err)
	}
	runtime := s.gatewayRequestObserverRuntimeSnapshot()
	var previous *GatewayRequestObserverSettings
	if runtime != nil {
		current, err := s.GetGatewayRequestObserverSettings(ctx)
		if err != nil {
			return err
		}
		previous = current
		if err := runtime.Apply(ctx, *settings); err != nil {
			return fmt.Errorf("apply gateway request observer settings: %w", err)
		}
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGatewayRequestObserverSettings, string(raw)); err != nil {
		if runtime != nil && previous != nil {
			if rollbackErr := runtime.Apply(ctx, *previous); rollbackErr != nil {
				slog.Warn("rollback gateway request observer settings failed", "error", rollbackErr)
			}
		}
		return fmt.Errorf("set gateway request observer settings: %w", err)
	}
	return nil
}

func normalizePositiveIDs(values []int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if result == nil {
		return []int64{}
	}
	return result
}

func normalizeObserverNames(values []string) []string {
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
	if result == nil {
		return []string{}
	}
	return result
}

func normalizeObserverEmails(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	if result == nil {
		return []string{}
	}
	return result
}
