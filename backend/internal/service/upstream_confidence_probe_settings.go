package service

import (
	"context"
	"encoding/json"
	"errors"
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
	return UpstreamConfidenceProbeSettings{Enabled: false, ReasoningEffort: "low", LongContextMaxTokens: 2048, QualityDegradeThreshold: 0, PromptVersion: UpstreamConfidenceDistributionPromptVersion}
}

func normalizeUpstreamConfidenceProbeSettings(value UpstreamConfidenceProbeSettings) (UpstreamConfidenceProbeSettings, error) {
	defaults := DefaultUpstreamConfidenceProbeSettings()
	if strings.TrimSpace(value.ReasoningEffort) == "" {
		value.ReasoningEffort = defaults.ReasoningEffort
	}
	value.ReasoningEffort = "low"
	value.LongContextEnabled = false
	value.LongContextMaxTokens = defaults.LongContextMaxTokens
	if value.LongContextMaxTokens <= 0 {
		value.LongContextMaxTokens = defaults.LongContextMaxTokens
	}
	if value.LongContextMaxTokens > 16384 {
		return value, fmt.Errorf("long_context_max_tokens is too large")
	}
	// Kept on the wire for old clients. Distribution verdicts never use this
	// legacy percentage threshold or feed into account health.
	value.QualityDegradeThreshold = 0
	if strings.TrimSpace(value.PromptVersion) == "" {
		value.PromptVersion = defaults.PromptVersion
	}
	value.PromptVersion = UpstreamConfidenceDistributionPromptVersion
	return value, nil
}

func (s *SettingService) GetUpstreamConfidenceProbeSettings(ctx context.Context) (UpstreamConfidenceProbeSettings, error) {
	settings, _, err := s.GetUpstreamConfidenceProbeSettingsState(ctx)
	if err != nil {
		return DefaultUpstreamConfidenceProbeSettings(), nil
	}
	return settings, nil
}

// GetUpstreamConfidenceProbeSettingsState distinguishes an explicitly stored
// confidence configuration from the safe disabled default. Read, parse, and
// validation failures are returned to callers that need to make scheduling
// decisions; those callers must treat them as disabled.
func (s *SettingService) GetUpstreamConfidenceProbeSettingsState(ctx context.Context) (UpstreamConfidenceProbeSettings, bool, error) {
	defaults := DefaultUpstreamConfidenceProbeSettings()
	if s == nil || s.settingRepo == nil {
		return defaults, false, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamConfidenceProbe)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return defaults, false, nil
		}
		return defaults, false, err
	}
	if strings.TrimSpace(raw) == "" {
		return defaults, false, nil
	}
	var value UpstreamConfidenceProbeSettings
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return defaults, false, err
	}
	value, err = normalizeUpstreamConfidenceProbeSettings(value)
	if err != nil {
		return defaults, false, err
	}
	return value, true, nil
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
