package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	defaultOpenAITTFTGuardDegradationSeconds = 20
	defaultOpenAITTFTGuardSettingsMinSamples = 5
	minOpenAITTFTGuardDegradationSeconds     = 5
	maxOpenAITTFTGuardDegradationSeconds     = 300
	minOpenAITTFTGuardSamples                = 2
	maxOpenAITTFTGuardSamples                = 20

	openAITTFTGuardConfigCacheTTL   = 60 * time.Second
	openAITTFTGuardConfigErrorTTL   = 5 * time.Second
	openAITTFTGuardConfigDBTimeout  = 5 * time.Second
	openAITTFTGuardConfigRefreshKey = "openai_ttft_guard_settings"
)

// OpenAITTFTGuardSettings is the persisted/admin-facing representation.
// Runtime consumers receive a duration-based OpenAITTFTGuardConfigSnapshot.
type OpenAITTFTGuardSettings struct {
	Enabled                bool `json:"enabled"`
	DegradationTTFTSeconds int  `json:"degradation_ttft_seconds"`
	MinSamples             int  `json:"min_samples"`
}

type cachedOpenAITTFTGuardConfig struct {
	snapshot  OpenAITTFTGuardConfigSnapshot
	expiresAt int64
}

func DefaultOpenAITTFTGuardSettings() *OpenAITTFTGuardSettings {
	return &OpenAITTFTGuardSettings{
		Enabled:                false,
		DegradationTTFTSeconds: defaultOpenAITTFTGuardDegradationSeconds,
		MinSamples:             defaultOpenAITTFTGuardSettingsMinSamples,
	}
}

func validateOpenAITTFTGuardSettings(settings *OpenAITTFTGuardSettings) error {
	if settings == nil {
		return infraerrors.BadRequest("INVALID_OPENAI_TTFT_GUARD_SETTINGS", "OpenAI TTFT guard settings are required")
	}
	if settings.DegradationTTFTSeconds < minOpenAITTFTGuardDegradationSeconds || settings.DegradationTTFTSeconds > maxOpenAITTFTGuardDegradationSeconds {
		return infraerrors.BadRequest(
			"INVALID_OPENAI_TTFT_GUARD_THRESHOLD",
			fmt.Sprintf("degradation_ttft_seconds must be between %d and %d", minOpenAITTFTGuardDegradationSeconds, maxOpenAITTFTGuardDegradationSeconds),
		)
	}
	if settings.MinSamples < minOpenAITTFTGuardSamples || settings.MinSamples > maxOpenAITTFTGuardSamples {
		return infraerrors.BadRequest(
			"INVALID_OPENAI_TTFT_GUARD_MIN_SAMPLES",
			fmt.Sprintf("min_samples must be between %d and %d", minOpenAITTFTGuardSamples, maxOpenAITTFTGuardSamples),
		)
	}
	return nil
}

func openAITTFTGuardSnapshot(settings *OpenAITTFTGuardSettings) OpenAITTFTGuardConfigSnapshot {
	if settings == nil {
		settings = DefaultOpenAITTFTGuardSettings()
	}
	return OpenAITTFTGuardConfigSnapshot{
		Enabled:    settings.Enabled,
		Threshold:  time.Duration(settings.DegradationTTFTSeconds) * time.Second,
		MinSamples: settings.MinSamples,
	}
}

func parseOpenAITTFTGuardSettings(raw string) (*OpenAITTFTGuardSettings, error) {
	if raw == "" {
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	var settings OpenAITTFTGuardSettings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return nil, fmt.Errorf("unmarshal OpenAI TTFT guard settings: %w", err)
	}
	if err := validateOpenAITTFTGuardSettings(&settings); err != nil {
		return nil, err
	}
	return &settings, nil
}

// GetOpenAITTFTGuardSettings reads the persisted admin settings. Missing or
// malformed data fails closed to the disabled defaults.
func (s *SettingService) GetOpenAITTFTGuardSettings(ctx context.Context) (*OpenAITTFTGuardSettings, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	revision := s.openAITTFTGuardRevision.Load()
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAITTFTGuardSettings)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultOpenAITTFTGuardSettings(), nil
		}
		return nil, fmt.Errorf("get OpenAI TTFT guard settings: %w", err)
	}
	settings, parseErr := parseOpenAITTFTGuardSettings(raw)
	if parseErr != nil {
		slog.Warn("invalid OpenAI TTFT guard settings, using disabled defaults", "error", parseErr)
		return DefaultOpenAITTFTGuardSettings(), nil
	}
	if s.openAITTFTGuardRevision.Load() == revision {
		s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	}
	return settings, nil
}

// SetOpenAITTFTGuardSettings persists a validated config and publishes it to
// the runtime snapshot immediately after the database write succeeds.
func (s *SettingService) SetOpenAITTFTGuardSettings(ctx context.Context, settings *OpenAITTFTGuardSettings) error {
	if err := validateOpenAITTFTGuardSettings(settings); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	s.openAITTFTGuardUpdateMu.Lock()
	raw, err := json.Marshal(settings)
	if err != nil {
		s.openAITTFTGuardUpdateMu.Unlock()
		return fmt.Errorf("marshal OpenAI TTFT guard settings: %w", err)
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAITTFTGuardSettings, string(raw)); err != nil {
		s.openAITTFTGuardUpdateMu.Unlock()
		return fmt.Errorf("set OpenAI TTFT guard settings: %w", err)
	}
	s.openAITTFTGuardRevision.Add(1)
	s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	s.openAITTFTGuardUpdateMu.Unlock()
	return s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
}

// SetOpenAITTFTGuardAndProbeModels persists both upstream-management settings
// in one repository operation and refreshes the TTFT hot snapshot afterwards.
func (s *SettingService) SetOpenAITTFTGuardAndProbeModels(ctx context.Context, settings *OpenAITTFTGuardSettings, models UpstreamProbeModels) error {
	intervalSeconds, err := s.GetUpstreamProbeIntervalSeconds(ctx)
	if err != nil {
		return err
	}
	return s.SetOpenAITTFTGuardProbeModelsAndInterval(ctx, settings, models, intervalSeconds)
}

// SetOpenAITTFTGuardProbeModelsAndInterval validates the complete management
// settings payload before atomically persisting any of it.
func (s *SettingService) SetOpenAITTFTGuardProbeModelsAndInterval(ctx context.Context, settings *OpenAITTFTGuardSettings, models UpstreamProbeModels, intervalSeconds int) error {
	if err := validateOpenAITTFTGuardSettings(settings); err != nil {
		return err
	}
	models.OpenAI = strings.TrimSpace(models.OpenAI)
	models.Anthropic = strings.TrimSpace(models.Anthropic)
	models.Gemini = strings.TrimSpace(models.Gemini)
	if err := validateUpstreamProbeModels(models); err != nil {
		return err
	}
	if err := validateUpstreamProbeIntervalSeconds(intervalSeconds); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	ttftRaw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal OpenAI TTFT guard settings: %w", err)
	}
	probeRaw, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("marshal upstream probe models: %w", err)
	}
	s.openAITTFTGuardUpdateMu.Lock()
	if err := s.settingRepo.SetMultiple(ctx, map[string]string{
		SettingKeyOpenAITTFTGuardSettings:      string(ttftRaw),
		SettingKeyUpstreamProbeModels:          string(probeRaw),
		SettingKeyUpstreamProbeIntervalSeconds: strconv.Itoa(intervalSeconds),
	}); err != nil {
		s.openAITTFTGuardUpdateMu.Unlock()
		return fmt.Errorf("set upstream management settings: %w", err)
	}
	s.openAITTFTGuardRevision.Add(1)
	s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	s.openAITTFTGuardUpdateMu.Unlock()
	return s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
}

// SetOpenAITTFTGuardProbeModelsIntervalAndGuard atomically persists all
// upstream-management settings. The legacy three-setting method above stays
// available for older callers.
func (s *SettingService) SetOpenAITTFTGuardProbeModelsIntervalAndGuard(ctx context.Context, settings *OpenAITTFTGuardSettings, models UpstreamProbeModels, intervalSeconds int, guard UpstreamProbeGuardSettings) error {
	return s.setOpenAITTFTGuardProbeModelsIntervalAndGuard(ctx, settings, models, intervalSeconds, guard, nil, false)
}

// SetOpenAITTFTGuardProbeModelsIntervalAndGuardWithAliases atomically persists
// all upstream-management settings, including the global model alias rules.
func (s *SettingService) SetOpenAITTFTGuardProbeModelsIntervalAndGuardWithAliases(ctx context.Context, settings *OpenAITTFTGuardSettings, models UpstreamProbeModels, intervalSeconds int, guard UpstreamProbeGuardSettings, aliases map[string]string) error {
	return s.setOpenAITTFTGuardProbeModelsIntervalAndGuard(ctx, settings, models, intervalSeconds, guard, aliases, true)
}

func (s *SettingService) setOpenAITTFTGuardProbeModelsIntervalAndGuard(ctx context.Context, settings *OpenAITTFTGuardSettings, models UpstreamProbeModels, intervalSeconds int, guard UpstreamProbeGuardSettings, aliases map[string]string, includeAliases bool) error {
	if err := validateOpenAITTFTGuardSettings(settings); err != nil {
		return err
	}
	models.OpenAI = strings.TrimSpace(models.OpenAI)
	models.Anthropic = strings.TrimSpace(models.Anthropic)
	models.Gemini = strings.TrimSpace(models.Gemini)
	if err := validateUpstreamProbeModels(models); err != nil {
		return err
	}
	if err := validateUpstreamProbeIntervalSeconds(intervalSeconds); err != nil {
		return err
	}
	guardRaw, _, err := s.validateAndMarshalUpstreamProbeGuard(guard)
	if err != nil {
		return err
	}
	var aliasRaw string
	if includeAliases {
		normalizedAliases, normalizeErr := NormalizeUpstreamModelAliasRules(aliases)
		if normalizeErr != nil {
			return normalizeErr
		}
		aliasBytes, marshalErr := json.Marshal(normalizedAliases)
		if marshalErr != nil {
			return fmt.Errorf("marshal upstream model alias rules: %w", marshalErr)
		}
		aliasRaw = string(aliasBytes)
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	ttftRaw, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("marshal OpenAI TTFT guard settings: %w", err)
	}
	probeRaw, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("marshal upstream probe models: %w", err)
	}
	s.openAITTFTGuardUpdateMu.Lock()
	values := map[string]string{
		SettingKeyOpenAITTFTGuardSettings:      string(ttftRaw),
		SettingKeyUpstreamProbeModels:          string(probeRaw),
		SettingKeyUpstreamProbeIntervalSeconds: strconv.Itoa(intervalSeconds),
		SettingKeyUpstreamProbeGuardSettings:   guardRaw,
	}
	if includeAliases {
		values[SettingKeyUpstreamModelAliasRules] = aliasRaw
	}
	if err := s.settingRepo.SetMultiple(ctx, values); err != nil {
		s.openAITTFTGuardUpdateMu.Unlock()
		return fmt.Errorf("set upstream management settings: %w", err)
	}
	s.openAITTFTGuardRevision.Add(1)
	s.storeOpenAITTFTGuardSnapshot(openAITTFTGuardSnapshot(settings), openAITTFTGuardConfigCacheTTL)
	s.openAITTFTGuardUpdateMu.Unlock()
	return s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
}

// OpenAITTFTGuardConfigSnapshot implements OpenAITTFTGuardConfigProvider. It
// only performs an atomic read on the hot path and refreshes stale data in the
// background.
func (s *SettingService) OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot {
	defaults := openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	if s == nil {
		return defaults
	}
	cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig)
	if cached != nil && time.Now().UnixNano() < cached.expiresAt {
		return cached.snapshot
	}
	s.openAITTFTGuardConfigSF.DoChan(openAITTFTGuardConfigRefreshKey, func() (any, error) {
		s.refreshOpenAITTFTGuardConfig(context.Background())
		return nil, nil
	})
	if cached != nil {
		return cached.snapshot
	}
	return defaults
}

// WarmOpenAITTFTGuardConfig synchronously loads the DB-backed config. It is a
// cold-path helper for deterministic startup/tests; scheduler reads do not use it.
func (s *SettingService) WarmOpenAITTFTGuardConfig(ctx context.Context) OpenAITTFTGuardConfigSnapshot {
	if s == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	}
	s.refreshOpenAITTFTGuardConfig(ctx)
	cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig)
	if cached == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	}
	return cached.snapshot
}

func (s *SettingService) RefreshOpenAITTFTGuardConfig(ctx context.Context) (OpenAITTFTGuardConfigSnapshot, bool) {
	if s == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings()), false
	}
	ok := s.refreshOpenAITTFTGuardConfig(ctx)
	cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig)
	if cached == nil {
		return openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings()), ok
	}
	return cached.snapshot, ok
}

func (s *SettingService) refreshOpenAITTFTGuardConfig(ctx context.Context) bool {
	if s == nil || s.settingRepo == nil {
		return false
	}
	revision := s.openAITTFTGuardRevision.Load()
	dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openAITTFTGuardConfigDBTimeout)
	defer cancel()

	snapshot := openAITTFTGuardSnapshot(DefaultOpenAITTFTGuardSettings())
	ttl := openAITTFTGuardConfigCacheTTL
	refreshFailed := false
	raw, err := s.settingRepo.GetValue(dbCtx, SettingKeyOpenAITTFTGuardSettings)
	if err == nil {
		settings, parseErr := parseOpenAITTFTGuardSettings(raw)
		if parseErr == nil {
			snapshot = openAITTFTGuardSnapshot(settings)
		} else {
			refreshFailed = true
			ttl = openAITTFTGuardConfigErrorTTL
			slog.Warn("failed to refresh OpenAI TTFT guard settings, preserving last valid config", "error", parseErr)
		}
	} else if !errors.Is(err, ErrSettingNotFound) {
		refreshFailed = true
		ttl = openAITTFTGuardConfigErrorTTL
		slog.Warn("failed to refresh OpenAI TTFT guard settings, preserving last valid config", "error", err)
	}
	if refreshFailed {
		if cached, _ := s.openAITTFTGuardConfigCache.Load().(*cachedOpenAITTFTGuardConfig); cached != nil {
			snapshot = cached.snapshot
		}
	}

	// A PUT may have committed while this refresh was reading. Never overwrite
	// the just-published snapshot with an older database observation.
	if s.openAITTFTGuardRevision.Load() != revision {
		return true
	}
	s.storeOpenAITTFTGuardSnapshot(snapshot, ttl)
	return !refreshFailed
}

func (s *SettingService) SetOpenAITTFTGuardInvalidationBus(bus GroupTTFTGuardPolicyInvalidationBus) {
	if s == nil {
		return
	}
	s.openAITTFTGuardBusMu.Lock()
	s.openAITTFTGuardBus = bus
	s.openAITTFTGuardBusMu.Unlock()
}

func (s *SettingService) notifyOpenAITTFTGuardGlobalUpdate(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.openAITTFTGuardBusMu.RLock()
	bus := s.openAITTFTGuardBus
	s.openAITTFTGuardBusMu.RUnlock()
	if bus == nil {
		return nil
	}
	if err := bus.NotifyGlobalUpdate(ctx); err != nil {
		// Persistence and the local hot snapshot have already committed. Do not
		// report the save as failed after that point; remote instances retain
		// their last valid snapshot and converge through the normal cache refresh.
		slog.Warn("failed to publish OpenAI TTFT guard global invalidation", "error", err)
	}
	return nil
}

func (s *SettingService) storeOpenAITTFTGuardSnapshot(snapshot OpenAITTFTGuardConfigSnapshot, ttl time.Duration) {
	s.openAITTFTGuardConfigCache.Store(&cachedOpenAITTFTGuardConfig{
		snapshot:  snapshot,
		expiresAt: time.Now().Add(ttl).UnixNano(),
	})
}
