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
	// Keep the global alias setting bounded so a malformed admin payload cannot
	// turn into an unbounded scheduler/account credential update.
	MaxUpstreamModelAliasRules = 1000
	MaxUpstreamModelAliasName  = 120
)

// NormalizeUpstreamModelAliasRules trims model names and validates the global
// alias map. A fresh map is returned so callers can safely retain it.
func NormalizeUpstreamModelAliasRules(rules map[string]string) (map[string]string, error) {
	if rules == nil {
		return map[string]string{}, nil
	}
	if len(rules) > MaxUpstreamModelAliasRules {
		return nil, infraerrors.BadRequest("UPSTREAM_MODEL_ALIAS_RULES_TOO_MANY", fmt.Sprintf("model_alias_rules cannot contain more than %d entries", MaxUpstreamModelAliasRules))
	}
	normalized := make(map[string]string, len(rules))
	for source, target := range rules {
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if source == "" || target == "" {
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_ALIAS_RULE_INVALID", "model alias source and target are required")
		}
		if len(source) > MaxUpstreamModelAliasName || len(target) > MaxUpstreamModelAliasName {
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_ALIAS_RULE_TOO_LONG", fmt.Sprintf("model alias names cannot exceed %d characters", MaxUpstreamModelAliasName))
		}
		normalized[source] = target
	}
	return normalized, nil
}

func (s *SettingService) GetUpstreamModelAliasRules(ctx context.Context) (map[string]string, error) {
	if s == nil || s.settingRepo == nil {
		return map[string]string{}, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyUpstreamModelAliasRules)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var rules map[string]string
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("unmarshal upstream model alias rules: %w", err)
	}
	if rules == nil {
		rules = map[string]string{}
	}
	return NormalizeUpstreamModelAliasRules(rules)
}

func (s *SettingService) SetUpstreamModelAliasRules(ctx context.Context, rules map[string]string) error {
	normalized, err := NormalizeUpstreamModelAliasRules(rules)
	if err != nil {
		return err
	}
	if s == nil || s.settingRepo == nil {
		return fmt.Errorf("setting repository is unavailable")
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("marshal upstream model alias rules: %w", err)
	}
	return s.settingRepo.Set(ctx, SettingKeyUpstreamModelAliasRules, string(raw))
}

// MergeUpstreamModelMappings computes the account mapping for a successful
// model sync. Automatic entries (identity mappings for real models and global
// aliases) are recomputed; manual entries are retained only while their
// target still exists in the live model list.
func MergeUpstreamModelMappings(models []string, aliases, current, previousAuto map[string]string) (map[string]string, map[string]string, error) {
	models = dedupeAndSortModelIDs(models)
	normalizedAliases, err := NormalizeUpstreamModelAliasRules(aliases)
	if err != nil {
		return nil, nil, err
	}
	modelSet := make(map[string]struct{}, len(models))
	for _, model := range models {
		modelSet[model] = struct{}{}
	}

	auto := make(map[string]string, len(models)+len(normalizedAliases))
	for _, model := range models {
		auto[model] = model
	}
	aliasSources := make([]string, 0, len(normalizedAliases))
	for source := range normalizedAliases {
		aliasSources = append(aliasSources, source)
	}
	sort.Strings(aliasSources)
	for _, source := range aliasSources {
		target := normalizedAliases[source]
		if _, sourceExists := modelSet[source]; sourceExists {
			auto[source] = source
			continue
		}
		if _, targetExists := modelSet[target]; targetExists {
			auto[source] = target
		}
	}

	// Determine which current entries are manual. With no historical snapshot,
	// identity mappings are the legacy automatically generated entries.
	manual := make(map[string]string)
	for source, target := range current {
		isAutomatic := false
		if previousAuto != nil {
			if oldTarget, exists := previousAuto[source]; exists && oldTarget == target {
				isAutomatic = true
			}
		} else if source == target {
			isAutomatic = true
		}
		if isAutomatic {
			continue
		}
		if _, targetExists := modelSet[target]; targetExists {
			manual[source] = target
		}
	}

	// Manual mappings win on source collisions: a global alias must not
	// overwrite an administrator's explicit mapping.
	result := make(map[string]string, len(auto)+len(manual))
	for source, target := range auto {
		result[source] = target
	}
	for source, target := range manual {
		result[source] = target
	}
	if len(result) == 0 {
		result = nil
	}
	if len(auto) == 0 {
		auto = nil
	}
	return result, auto, nil
}
