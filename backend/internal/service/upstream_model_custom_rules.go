package service

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	AccountUpstreamModelCustomRulesExtraKey = "upstream_model_custom_rules"

	UpstreamModelCustomRuleActionAllow = "allow"
	UpstreamModelCustomRuleActionMap   = "map"
	UpstreamModelCustomRuleActionDeny  = "deny"

	MaxUpstreamModelCustomRules = 1000
	MaxUpstreamModelCustomName  = 120
)

// UpstreamModelCustomRule is an account-local override layered on top of the
// last successful automatic model snapshot.
type UpstreamModelCustomRule struct {
	Source string `json:"source"`
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
}

func NormalizeUpstreamModelCustomRules(rules []UpstreamModelCustomRule) ([]UpstreamModelCustomRule, error) {
	if len(rules) > MaxUpstreamModelCustomRules {
		return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULES_TOO_MANY", fmt.Sprintf("upstream_model_custom_rules cannot contain more than %d entries", MaxUpstreamModelCustomRules))
	}
	normalized := make([]UpstreamModelCustomRule, 0, len(rules))
	seen := make(map[string]struct{}, len(rules))
	for _, rule := range rules {
		rule.Source = strings.TrimSpace(rule.Source)
		rule.Action = strings.ToLower(strings.TrimSpace(rule.Action))
		rule.Target = strings.TrimSpace(rule.Target)
		if rule.Source == "" {
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "custom model rule source is required")
		}
		if len(rule.Source) > MaxUpstreamModelCustomName || len(rule.Target) > MaxUpstreamModelCustomName {
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_TOO_LONG", fmt.Sprintf("custom model rule names cannot exceed %d characters", MaxUpstreamModelCustomName))
		}
		if _, exists := seen[rule.Source]; exists {
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_DUPLICATE", "custom model rule source must be unique")
		}
		seen[rule.Source] = struct{}{}
		switch rule.Action {
		case UpstreamModelCustomRuleActionAllow, UpstreamModelCustomRuleActionDeny:
			if rule.Target != "" {
				return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "allow and deny rules cannot contain a target")
			}
		case UpstreamModelCustomRuleActionMap:
			if rule.Target == "" {
				return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "map rule target is required")
			}
		default:
			return nil, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "custom model rule action must be allow, map, or deny")
		}
		normalized = append(normalized, rule)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].Source < normalized[j].Source })
	return normalized, nil
}

// UpstreamModelCustomRulesFromExtra preserves the distinction between an
// absent field and an explicitly empty rule list.
func UpstreamModelCustomRulesFromExtra(extra map[string]any) ([]UpstreamModelCustomRule, bool, error) {
	if extra == nil {
		return nil, false, nil
	}
	raw, exists := extra[AccountUpstreamModelCustomRulesExtraKey]
	if !exists {
		return nil, false, nil
	}
	if raw == nil {
		return []UpstreamModelCustomRule{}, true, nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, true, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "custom model rules must be an array")
	}
	var rules []UpstreamModelCustomRule
	if err := json.Unmarshal(encoded, &rules); err != nil {
		return nil, true, infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULE_INVALID", "custom model rules must be an array")
	}
	normalized, err := NormalizeUpstreamModelCustomRules(rules)
	return normalized, true, err
}

func modelSetFromAutomaticMapping(auto map[string]string) map[string]struct{} {
	models := make(map[string]struct{}, len(auto)*2)
	for source, target := range auto {
		if source = strings.TrimSpace(source); source != "" {
			models[source] = struct{}{}
		}
		if target = strings.TrimSpace(target); target != "" {
			models[target] = struct{}{}
		}
	}
	return models
}

// ApplyUpstreamModelCustomRules omits temporarily unavailable custom rules
// from scheduling while keeping the rule itself persisted for later recovery.
func ApplyUpstreamModelCustomRules(auto map[string]string, rules []UpstreamModelCustomRule) (map[string]string, error) {
	normalized, err := NormalizeUpstreamModelCustomRules(rules)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(auto)+len(normalized))
	for source, target := range auto {
		source = strings.TrimSpace(source)
		target = strings.TrimSpace(target)
		if source != "" && target != "" {
			result[source] = target
		}
	}
	models := modelSetFromAutomaticMapping(auto)
	for _, rule := range normalized {
		switch rule.Action {
		case UpstreamModelCustomRuleActionDeny:
			delete(result, rule.Source)
		case UpstreamModelCustomRuleActionAllow:
			if _, available := models[rule.Source]; available {
				result[rule.Source] = rule.Source
			} else {
				delete(result, rule.Source)
			}
		case UpstreamModelCustomRuleActionMap:
			if _, available := models[rule.Target]; available {
				result[rule.Source] = rule.Target
			} else {
				delete(result, rule.Source)
			}
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

// MergeUpstreamModelMappingsWithCustomRules refreshes automatic identity
// mappings and layers account-local rules on top. If the rule field has never
// been created, legacy manual mappings are retained for compatibility.
func MergeUpstreamModelMappingsWithCustomRules(models []string, current, previousAuto map[string]string, rules []UpstreamModelCustomRule, rulesPresent bool) (map[string]string, map[string]string, error) {
	models = dedupeAndSortModelIDs(models)
	auto := identityMapping(models)
	modelSet := make(map[string]struct{}, len(models))
	for _, model := range models {
		modelSet[model] = struct{}{}
	}
	if !rulesPresent {
		result := make(map[string]string, len(auto)+len(current))
		for source, target := range auto {
			result[source] = target
		}
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
				result[source] = target
			}
		}
		if len(result) == 0 {
			result = nil
		}
		return result, auto, nil
	}
	result, err := ApplyUpstreamModelCustomRules(auto, rules)
	return result, auto, err
}

func identityMapping(models []string) map[string]string {
	if len(models) == 0 {
		return nil
	}
	result := make(map[string]string, len(models))
	for _, model := range models {
		result[model] = model
	}
	return result
}

// prepareUpstreamModelCustomRuleUpdate validates an explicit administrator
// update, stores the rules in account extra, and refreshes the effective model
// mapping from the latest automatic snapshot when one is available.
func prepareUpstreamModelCustomRuleUpdate(account *Account, input *UpdateAccountInput) error {
	if account == nil || input == nil {
		return nil
	}
	var (
		rules   []UpstreamModelCustomRule
		present bool
		err     error
	)
	if input.UpstreamModelCustomRules != nil {
		rules, err = NormalizeUpstreamModelCustomRules(*input.UpstreamModelCustomRules)
		present = true
	} else {
		rules, present, err = UpstreamModelCustomRulesFromExtra(input.Extra)
	}
	if err != nil || !present {
		return err
	}
	if !account.IsUpstreamBound() || account.UpstreamLifecycleOwner != AccountUpstreamLifecycleOwnerSyncManaged {
		return infraerrors.BadRequest("UPSTREAM_MODEL_CUSTOM_RULES_NOT_SUPPORTED", "custom model rules are only supported for sync-managed upstream accounts")
	}
	if input.Extra == nil {
		input.Extra = make(map[string]any)
	}
	// Keep an explicit empty array so a later sync knows that legacy manual
	// mappings were intentionally cleared and must not be resurrected.
	input.Extra[AccountUpstreamModelCustomRulesExtraKey] = rules

	auto := account.upstreamModelSyncState().AutoMapping
	if auto == nil {
		return nil
	}
	mapping, err := ApplyUpstreamModelCustomRules(auto, rules)
	if err != nil {
		return err
	}
	if input.Credentials == nil {
		input.Credentials = make(map[string]any)
	}
	if mapping == nil {
		delete(input.Credentials, "model_mapping")
	} else {
		input.Credentials["model_mapping"] = mapping
	}
	return nil
}
