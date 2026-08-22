package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const SettingKeyUpstreamConfidenceProbe = "upstream_confidence_probe_settings"

type UpstreamConfidenceProbeSettings struct {
	Enabled                 bool   `json:"enabled"`
	ReasoningEffort         string `json:"reasoning_effort"`
	LongContextEnabled      bool   `json:"long_context_enabled"`
	LongContextMaxTokens    int    `json:"long_context_max_tokens"`
	QualityDegradeThreshold int    `json:"quality_degrade_threshold"`
	PromptVersion           string `json:"prompt_version"`
}

func DefaultUpstreamConfidenceProbeSettings() UpstreamConfidenceProbeSettings {
	return UpstreamConfidenceProbeSettings{Enabled: true, ReasoningEffort: upstreamConfidenceDefaultEffort, LongContextMaxTokens: 2048, QualityDegradeThreshold: 70, PromptVersion: upstreamConfidencePromptVersion}
}

func normalizeUpstreamConfidenceProbeSettings(value UpstreamConfidenceProbeSettings) (UpstreamConfidenceProbeSettings, error) {
	defaults := DefaultUpstreamConfidenceProbeSettings()
	if strings.TrimSpace(value.ReasoningEffort) == "" {
		value.ReasoningEffort = defaults.ReasoningEffort
	}
	value.ReasoningEffort = strings.ToLower(strings.TrimSpace(value.ReasoningEffort))
	if value.ReasoningEffort != "low" && value.ReasoningEffort != "medium" && value.ReasoningEffort != "high" {
		return value, fmt.Errorf("reasoning_effort must be low, medium, or high")
	}
	if value.LongContextMaxTokens <= 0 {
		value.LongContextMaxTokens = defaults.LongContextMaxTokens
	}
	if value.LongContextMaxTokens > 16384 {
		return value, fmt.Errorf("long_context_max_tokens is too large")
	}
	if value.QualityDegradeThreshold == 0 {
		value.QualityDegradeThreshold = defaults.QualityDegradeThreshold
	}
	if value.QualityDegradeThreshold < 0 || value.QualityDegradeThreshold > 100 {
		return value, fmt.Errorf("quality_degrade_threshold must be between 0 and 100")
	}
	if strings.TrimSpace(value.PromptVersion) == "" {
		value.PromptVersion = defaults.PromptVersion
	}
	if value.PromptVersion != upstreamConfidencePromptVersion {
		return value, fmt.Errorf("unsupported confidence prompt version")
	}
	return value, nil
}

func (s *SettingService) GetUpstreamConfidenceProbeSettings(ctx context.Context) (UpstreamConfidenceProbeSettings, error) {
	defaults := DefaultUpstreamConfidenceProbeSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamConfidenceProbe)
	if err != nil || strings.TrimSpace(raw) == "" {
		return defaults, nil
	}
	var value UpstreamConfidenceProbeSettings
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return defaults, nil
	}
	return normalizeUpstreamConfidenceProbeSettings(value)
}

func (s *SettingService) SetUpstreamConfidenceProbeSettings(ctx context.Context, value UpstreamConfidenceProbeSettings) error {
	value, err := normalizeUpstreamConfidenceProbeSettings(value)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	return s.settingRepo.Set(ctx, SettingKeyUpstreamConfidenceProbe, string(raw))
}
