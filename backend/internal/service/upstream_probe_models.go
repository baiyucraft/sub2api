package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultUpstreamProbeIntervalSeconds = 300
	MinUpstreamProbeIntervalSeconds     = 60
	MaxUpstreamProbeIntervalSeconds     = 3600
)

type UpstreamProbeModels struct {
	OpenAI    string `json:"openai"`
	Anthropic string `json:"anthropic"`
	Gemini    string `json:"gemini"`
}

func DefaultUpstreamProbeModels() UpstreamProbeModels {
	return UpstreamProbeModels{
		OpenAI:    "gpt-4o-mini",
		Anthropic: "claude-3-5-haiku-latest",
		Gemini:    "gemini-2.0-flash",
	}
}

func (m UpstreamProbeModels) ModelFor(platform string) string {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformOpenAI:
		return m.OpenAI
	case PlatformAnthropic:
		return m.Anthropic
	case PlatformGemini:
		return m.Gemini
	default:
		return ""
	}
}

func validateUpstreamProbeModels(models UpstreamProbeModels) error {
	for platform, model := range map[string]string{
		PlatformOpenAI: models.OpenAI, PlatformAnthropic: models.Anthropic, PlatformGemini: models.Gemini,
	} {
		model = strings.TrimSpace(model)
		if model == "" {
			return infraerrors.BadRequest("UPSTREAM_PROBE_MODEL_REQUIRED", fmt.Sprintf("probe model for %s is required", platform))
		}
		if len(model) > 120 {
			return infraerrors.BadRequest("UPSTREAM_PROBE_MODEL_TOO_LONG", fmt.Sprintf("probe model for %s is too long", platform))
		}
	}
	return nil
}

func (s *SettingService) GetUpstreamProbeModels(ctx context.Context) (UpstreamProbeModels, error) {
	defaults := DefaultUpstreamProbeModels()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamProbeModels)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, nil
		}
		return defaults, err
	}
	var models UpstreamProbeModels
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return defaults, fmt.Errorf("unmarshal upstream probe models: %w", err)
	}
	if err := validateUpstreamProbeModels(models); err != nil {
		return defaults, err
	}
	return models, nil
}

func (s *SettingService) SetUpstreamProbeModels(ctx context.Context, models UpstreamProbeModels) error {
	models.OpenAI = strings.TrimSpace(models.OpenAI)
	models.Anthropic = strings.TrimSpace(models.Anthropic)
	models.Gemini = strings.TrimSpace(models.Gemini)
	if err := validateUpstreamProbeModels(models); err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	raw, err := json.Marshal(models)
	if err != nil {
		return err
	}
	return s.settingRepo.Set(ctx, SettingKeyUpstreamProbeModels, string(raw))
}

func validateUpstreamProbeIntervalSeconds(seconds int) error {
	if seconds < MinUpstreamProbeIntervalSeconds || seconds > MaxUpstreamProbeIntervalSeconds {
		return infraerrors.BadRequest("UPSTREAM_PROBE_INTERVAL_INVALID", "probe_interval_seconds must be between 60 and 3600")
	}
	return nil
}

func (s *SettingService) GetUpstreamProbeIntervalSeconds(ctx context.Context) (int, error) {
	if s == nil || s.settingRepo == nil {
		return DefaultUpstreamProbeIntervalSeconds, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamProbeIntervalSeconds)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return DefaultUpstreamProbeIntervalSeconds, nil
		}
		return DefaultUpstreamProbeIntervalSeconds, err
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return DefaultUpstreamProbeIntervalSeconds, fmt.Errorf("parse upstream probe interval: %w", err)
	}
	if err := validateUpstreamProbeIntervalSeconds(seconds); err != nil {
		return DefaultUpstreamProbeIntervalSeconds, err
	}
	return seconds, nil
}
