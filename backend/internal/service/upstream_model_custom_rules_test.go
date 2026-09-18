package service

import (
	"reflect"
	"testing"
)

func TestApplyUpstreamModelCustomRules(t *testing.T) {
	auto := map[string]string{
		"gpt-a": "gpt-a",
		"gpt-b": "gpt-b",
		"gpt-c": "gpt-c",
	}
	rules := []UpstreamModelCustomRule{
		{Source: "gpt-a", Action: UpstreamModelCustomRuleActionDeny},
		{Source: "public-b", Action: UpstreamModelCustomRuleActionMap, Target: "gpt-b"},
		{Source: "waiting", Action: UpstreamModelCustomRuleActionMap, Target: "missing"},
		{Source: "gpt-c", Action: UpstreamModelCustomRuleActionAllow},
	}

	got, err := ApplyUpstreamModelCustomRules(auto, rules)
	if err != nil {
		t.Fatalf("ApplyUpstreamModelCustomRules() error = %v", err)
	}
	want := map[string]string{
		"gpt-b":    "gpt-b",
		"gpt-c":    "gpt-c",
		"public-b": "gpt-b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApplyUpstreamModelCustomRules() = %#v, want %#v", got, want)
	}
}

func TestMergeUpstreamModelMappingsWithCustomRulesExplicitEmptyRemovesLegacyManual(t *testing.T) {
	current := map[string]string{
		"gpt-a":  "gpt-a",
		"public": "gpt-b",
	}
	previousAuto := map[string]string{"gpt-a": "gpt-a"}

	legacyResult, _, err := MergeUpstreamModelMappingsWithCustomRules(
		[]string{"gpt-a", "gpt-b"}, current, previousAuto, nil, false,
	)
	if err != nil {
		t.Fatalf("legacy merge error = %v", err)
	}
	if legacyResult["public"] != "gpt-b" {
		t.Fatalf("legacy manual mapping was not preserved: %#v", legacyResult)
	}

	clearedResult, auto, err := MergeUpstreamModelMappingsWithCustomRules(
		[]string{"gpt-a", "gpt-b"}, current, previousAuto, []UpstreamModelCustomRule{}, true,
	)
	if err != nil {
		t.Fatalf("explicit-empty merge error = %v", err)
	}
	want := map[string]string{"gpt-a": "gpt-a", "gpt-b": "gpt-b"}
	if !reflect.DeepEqual(clearedResult, want) || !reflect.DeepEqual(auto, want) {
		t.Fatalf("explicit-empty result = %#v auto = %#v, want %#v", clearedResult, auto, want)
	}
}

func TestUpstreamModelCustomRulesFromExtraKeepsExplicitEmpty(t *testing.T) {
	rules, present, err := UpstreamModelCustomRulesFromExtra(map[string]any{
		AccountUpstreamModelCustomRulesExtraKey: []any{},
	})
	if err != nil {
		t.Fatalf("UpstreamModelCustomRulesFromExtra() error = %v", err)
	}
	if !present || rules == nil || len(rules) != 0 {
		t.Fatalf("expected explicit empty rules, got present=%v rules=%#v", present, rules)
	}
}

func TestNormalizeUpstreamModelCustomRulesRejectsDuplicateSource(t *testing.T) {
	_, err := NormalizeUpstreamModelCustomRules([]UpstreamModelCustomRule{
		{Source: "gpt-a", Action: UpstreamModelCustomRuleActionAllow},
		{Source: " gpt-a ", Action: UpstreamModelCustomRuleActionDeny},
	})
	if err == nil {
		t.Fatal("expected duplicate source error")
	}
}
