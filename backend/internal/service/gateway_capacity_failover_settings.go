package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	gatewayCapacityFailoverCacheTTL  = 60 * time.Second
	gatewayCapacityFailoverErrorTTL  = 5 * time.Second
	gatewayCapacityFailoverDBTimeout = 5 * time.Second
	gatewayCapacityFailoverCacheKey  = "gateway_capacity_failover_settings"

	GatewayCapacityFailoverMaxSwitchesMin = 0
	GatewayCapacityFailoverMaxSwitchesMax = 1000
)

// GatewayCapacityFailoverSettings is the persisted admin-facing configuration.
// Deployment configuration remains the fallback when no database setting exists.
type GatewayCapacityFailoverSettings struct {
	Enabled             bool `json:"enabled"`
	MaxSwitches         int  `json:"max_switches"`
	ExhaustedStatusCode int  `json:"exhausted_status_code"`
}

// GatewayCapacityFailoverConfigProvider is the narrow read-only contract used
// by the HTTP handler. Storage and cache ownership remain in the service layer.
type GatewayCapacityFailoverConfigProvider interface {
	GatewayCapacityFailoverSettingsSnapshot(context.Context) GatewayCapacityFailoverSettings
}

type cachedGatewayCapacityFailoverSettings struct {
	settings  GatewayCapacityFailoverSettings
	expiresAt int64
}

func DefaultGatewayCapacityFailoverSettings(cfg *config.Config) GatewayCapacityFailoverSettings {
	settings := GatewayCapacityFailoverSettings{
		Enabled:             false,
		MaxSwitches:         10,
		ExhaustedStatusCode: 503,
	}
	if cfg == nil {
		return settings
	}
	scheduling := cfg.Gateway.Scheduling
	settings.Enabled = scheduling.CapacityFailoverEnabled
	settings.MaxSwitches = scheduling.CapacityFailoverMaxSwitches
	settings.ExhaustedStatusCode = scheduling.CapacityFailoverExhaustedStatusCode
	if settings.MaxSwitches < GatewayCapacityFailoverMaxSwitchesMin {
		settings.MaxSwitches = GatewayCapacityFailoverMaxSwitchesMin
	}
	if settings.MaxSwitches > GatewayCapacityFailoverMaxSwitchesMax {
		settings.MaxSwitches = GatewayCapacityFailoverMaxSwitchesMax
	}
	if settings.ExhaustedStatusCode < 400 || settings.ExhaustedStatusCode > 599 {
		settings.ExhaustedStatusCode = 503
	}
	return settings
}

func validateGatewayCapacityFailoverSettings(settings *GatewayCapacityFailoverSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_GATEWAY_CAPACITY_FAILOVER_SETTINGS", "settings are required")
	}
	if settings.MaxSwitches < GatewayCapacityFailoverMaxSwitchesMin || settings.MaxSwitches > GatewayCapacityFailoverMaxSwitchesMax {
		return infraerrors.BadRequest(
			"INVALID_GATEWAY_CAPACITY_FAILOVER_MAX_SWITCHES",
			fmt.Sprintf("max_switches must be between %d and %d", GatewayCapacityFailoverMaxSwitchesMin, GatewayCapacityFailoverMaxSwitchesMax),
		)
	}
	if settings.ExhaustedStatusCode < 400 || settings.ExhaustedStatusCode > 599 {
		return infraerrors.BadRequest("INVALID_GATEWAY_CAPACITY_FAILOVER_STATUS_CODE", "exhausted_status_code must be between 400 and 599")
	}
	return nil
}

func decodeGatewayCapacityFailoverSettings(raw string) (*GatewayCapacityFailoverSettings, error) {
	// Pointer fields distinguish a missing legacy field from an explicit zero.
	// max_switches=0 means unlimited, while a missing field inherits the
	// current built-in default.
	var persisted struct {
		Enabled             *bool `json:"enabled"`
		MaxSwitches         *int  `json:"max_switches"`
		ExhaustedStatusCode *int  `json:"exhausted_status_code"`
	}
	if err := json.Unmarshal([]byte(raw), &persisted); err != nil {
		return nil, fmt.Errorf("unmarshal gateway capacity failover settings: %w", err)
	}
	settings := DefaultGatewayCapacityFailoverSettings(nil)
	if persisted.Enabled != nil {
		settings.Enabled = *persisted.Enabled
	}
	if persisted.MaxSwitches != nil {
		settings.MaxSwitches = *persisted.MaxSwitches
	}
	if persisted.ExhaustedStatusCode != nil {
		settings.ExhaustedStatusCode = *persisted.ExhaustedStatusCode
	}
	if err := validateGatewayCapacityFailoverSettings(&settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// GetGatewayCapacityFailoverSettings is the cold-path admin read. Missing or
// malformed data falls back to deployment configuration.
func (s *SettingService) GetGatewayCapacityFailoverSettings(ctx context.Context) (*GatewayCapacityFailoverSettings, error) {
	fallback := DefaultGatewayCapacityFailoverSettings(nil)
	if s != nil {
		fallback = DefaultGatewayCapacityFailoverSettings(s.cfg)
	}
	if s == nil || s.settingRepo == nil {
		return &fallback, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyGatewayCapacityFailoverSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return &fallback, nil
		}
		return nil, fmt.Errorf("get gateway capacity failover settings: %w", err)
	}
	settings, err := decodeGatewayCapacityFailoverSettings(raw)
	if err != nil {
		slog.Warn("invalid gateway capacity failover settings; using deployment fallback", "error", err)
		return &fallback, nil
	}
	return settings, nil
}

// SetGatewayCapacityFailoverSettings persists the setting and publishes the
// current-process snapshot immediately after the write succeeds.
func (s *SettingService) SetGatewayCapacityFailoverSettings(ctx context.Context, settings *GatewayCapacityFailoverSettings) error {
	if err := validateGatewayCapacityFailoverSettings(settings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	s.gatewayCapacityFailoverUpdateMu.Lock()
	defer s.gatewayCapacityFailoverUpdateMu.Unlock()
	raw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal gateway capacity failover settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyGatewayCapacityFailoverSettings, string(raw)); err != nil {
		return fmt.Errorf("set gateway capacity failover settings: %w", err)
	}
	s.gatewayCapacityFailoverRevision.Add(1)
	s.storeGatewayCapacityFailoverSettings(*settings, gatewayCapacityFailoverCacheTTL)
	return nil
}

// GatewayCapacityFailoverSettingsSnapshot is safe for request hot paths. It
// returns the latest snapshot immediately and refreshes stale data in the
// background; no gateway request waits on the settings database.
func (s *SettingService) GatewayCapacityFailoverSettingsSnapshot(ctx context.Context) GatewayCapacityFailoverSettings {
	fallback := DefaultGatewayCapacityFailoverSettings(nil)
	if s == nil {
		return fallback
	}
	fallback = DefaultGatewayCapacityFailoverSettings(s.cfg)
	if cached, ok := s.gatewayCapacityFailoverCache.Load().(*cachedGatewayCapacityFailoverSettings); ok && cached != nil {
		if time.Now().UnixNano() < cached.expiresAt {
			return cached.settings
		}
		s.refreshGatewayCapacityFailoverSettingsAsync(ctx)
		return cached.settings
	}
	s.refreshGatewayCapacityFailoverSettingsAsync(ctx)
	return fallback
}

// WarmGatewayCapacityFailoverSettings synchronously refreshes the snapshot for
// startup and deterministic tests. Normal gateway requests use the snapshot method.
func (s *SettingService) WarmGatewayCapacityFailoverSettings(ctx context.Context) GatewayCapacityFailoverSettings {
	if s == nil {
		return DefaultGatewayCapacityFailoverSettings(nil)
	}
	s.refreshGatewayCapacityFailoverSettings(ctx)
	if cached, ok := s.gatewayCapacityFailoverCache.Load().(*cachedGatewayCapacityFailoverSettings); ok && cached != nil {
		return cached.settings
	}
	return DefaultGatewayCapacityFailoverSettings(s.cfg)
}

func (s *SettingService) refreshGatewayCapacityFailoverSettingsAsync(ctx context.Context) {
	s.gatewayCapacityFailoverSF.DoChan(gatewayCapacityFailoverCacheKey, func() (any, error) {
		s.refreshGatewayCapacityFailoverSettings(ctx)
		return nil, nil
	})
}

func (s *SettingService) refreshGatewayCapacityFailoverSettings(ctx context.Context) {
	if s == nil || s.settingRepo == nil {
		return
	}
	revision := s.gatewayCapacityFailoverRevision.Load()
	if ctx == nil {
		ctx = context.Background()
	}
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gatewayCapacityFailoverDBTimeout)
	defer cancel()

	settings := DefaultGatewayCapacityFailoverSettings(s.cfg)
	ttl := gatewayCapacityFailoverCacheTTL
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyGatewayCapacityFailoverSettings)
	if err == nil {
		decoded, decodeErr := decodeGatewayCapacityFailoverSettings(raw)
		if decodeErr != nil {
			ttl = gatewayCapacityFailoverErrorTTL
			if prior, ok := s.gatewayCapacityFailoverCache.Load().(*cachedGatewayCapacityFailoverSettings); ok && prior != nil {
				settings = prior.settings
			}
			slog.Warn("failed to refresh gateway capacity failover settings; using deployment fallback", "error", decodeErr)
		} else {
			settings = *decoded
		}
	} else if !errors.Is(err, ErrSettingNotFound) {
		ttl = gatewayCapacityFailoverErrorTTL
		if prior, ok := s.gatewayCapacityFailoverCache.Load().(*cachedGatewayCapacityFailoverSettings); ok && prior != nil {
			settings = prior.settings
		}
		slog.Warn("failed to refresh gateway capacity failover settings", "error", err)
	}
	if s.gatewayCapacityFailoverRevision.Load() != revision {
		return
	}
	s.storeGatewayCapacityFailoverSettings(settings, ttl)
}

func (s *SettingService) storeGatewayCapacityFailoverSettings(settings GatewayCapacityFailoverSettings, ttl time.Duration) {
	s.gatewayCapacityFailoverCache.Store(&cachedGatewayCapacityFailoverSettings{
		settings:  settings,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
