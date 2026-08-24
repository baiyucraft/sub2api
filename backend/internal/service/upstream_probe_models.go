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
	OpenAI     string            `json:"openai"`
	Anthropic  string            `json:"anthropic"`
	Gemini     string            `json:"gemini"`
	Additional map[string]string `json:"-"`
}

// UpstreamProbePlatform describes a provider that can appear in the probe
// settings catalog. ProbeSupported is deliberately separate from model
// availability: some providers expose a model directory but have no safe
// active health-probe protocol.
type UpstreamProbePlatform struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Models         []string `json:"models"`
	ProbeSupported bool     `json:"probe_supported"`
	ProbeReason    string   `json:"probe_reason,omitempty"`
}

func (m UpstreamProbeModels) AsMap() map[string]string {
	result := make(map[string]string, len(m.Additional)+3)
	if value := strings.TrimSpace(m.OpenAI); value != "" {
		result[PlatformOpenAI] = value
	}
	if value := strings.TrimSpace(m.Anthropic); value != "" {
		result[PlatformAnthropic] = value
	}
	if value := strings.TrimSpace(m.Gemini); value != "" {
		result[PlatformGemini] = value
	}
	for platform, value := range m.Additional {
		if platform = strings.ToLower(strings.TrimSpace(platform)); platform != "" && strings.TrimSpace(value) != "" {
			result[platform] = strings.TrimSpace(value)
		}
	}
	return result
}

func UpstreamProbeModelsFromMap(values map[string]string) UpstreamProbeModels {
	models := UpstreamProbeModels{Additional: make(map[string]string)}
	for platform, value := range values {
		platform = strings.ToLower(strings.TrimSpace(platform))
		value = strings.TrimSpace(value)
		if platform == "" || value == "" {
			continue
		}
		switch platform {
		case PlatformOpenAI:
			models.OpenAI = value
		case PlatformAnthropic:
			models.Anthropic = value
		case PlatformGemini:
			models.Gemini = value
		default:
			models.Additional[platform] = value
		}
	}
	return models
}

func (m UpstreamProbeModels) MarshalJSON() ([]byte, error) { return json.Marshal(m.AsMap()) }

func (m *UpstreamProbeModels) UnmarshalJSON(data []byte) error {
	var values map[string]string
	if err := json.Unmarshal(data, &values); err != nil {
		return err
	}
	*m = UpstreamProbeModelsFromMap(values)
	return nil
}

func DefaultUpstreamProbeModels() UpstreamProbeModels {
	return UpstreamProbeModels{
		OpenAI:    "gpt-4o-mini",
		Anthropic: "claude-3-5-haiku-latest",
		Gemini:    "gemini-2.0-flash",
		Additional: map[string]string{
			PlatformKimi:     "moonshot-v1-8k",
			PlatformZhipu:    "glm-4-flash",
			PlatformDeepseek: "deepseek-chat",
		},
	}
}

func (m UpstreamProbeModels) ModelFor(platform string) string {
	return m.AsMap()[strings.ToLower(strings.TrimSpace(platform))]
}

func validateUpstreamProbeModels(models UpstreamProbeModels) error {
	for platform, model := range map[string]string{
		PlatformOpenAI:    models.OpenAI,
		PlatformAnthropic: models.Anthropic,
		PlatformGemini:    models.Gemini,
	} {
		model = strings.TrimSpace(model)
		if model == "" {
			return infraerrors.BadRequest("UPSTREAM_PROBE_MODEL_REQUIRED", fmt.Sprintf("probe model for %s is required", platform))
		}
		if len(model) > 120 {
			return infraerrors.BadRequest("UPSTREAM_PROBE_MODEL_TOO_LONG", fmt.Sprintf("probe model for %s is too long", platform))
		}
	}
	for platform, model := range models.Additional {
		platform = strings.TrimSpace(platform)
		model = strings.TrimSpace(model)
		if platform == "" || model == "" {
			continue
		}
		if len(model) > 120 {
			return infraerrors.BadRequest("UPSTREAM_PROBE_MODEL_TOO_LONG", fmt.Sprintf("probe model for %s is too long", platform))
		}
	}
	return nil
}

func DefaultUpstreamProbePlatformCatalog() []UpstreamProbePlatform {
	entries := RegisteredPlatformCatalog()
	result := make([]UpstreamProbePlatform, 0, len(entries))
	for _, entry := range entries {
		result = append(result, UpstreamProbePlatform{
			ID: entry.ID, Label: entry.Label, Models: cloneStrings(entry.DefaultModels),
			ProbeSupported: entry.ProbeSupported, ProbeReason: entry.ProbeReason,
		})
	}
	return result
}

func UpstreamProbePlatformSupported(platform string) bool {
	platform = strings.ToLower(strings.TrimSpace(platform))
	for _, entry := range DefaultUpstreamProbePlatformCatalog() {
		if entry.ID == platform {
			return entry.ProbeSupported
		}
	}
	return false
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
