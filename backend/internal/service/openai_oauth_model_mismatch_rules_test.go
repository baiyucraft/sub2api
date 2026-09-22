package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeOpenAIOAuthModelMismatchRulesTrimsAndRejectsDuplicateSources(t *testing.T) {
	rules, err := NormalizeOpenAIOAuthModelMismatchRules([]OpenAIOAuthModelMismatchRule{
		{Source: "  GPT-6-ASTRA ", Target: " gpt-5.6-luna "},
	})
	require.NoError(t, err)
	require.Equal(t, []OpenAIOAuthModelMismatchRule{{Source: "GPT-6-ASTRA", Target: "gpt-5.6-luna"}}, rules)

	_, err = NormalizeOpenAIOAuthModelMismatchRules([]OpenAIOAuthModelMismatchRule{
		{Source: "gpt-6-astra", Target: "gpt-5.6-luna"},
		{Source: " GPT-6-ASTRA ", Target: "gpt-5.6-sol"},
	})
	require.Error(t, err)
}

func TestNormalizeOpenAIOAuthModelMismatchRulesRejectsEmptyAndOverlongNames(t *testing.T) {
	for _, rules := range [][]OpenAIOAuthModelMismatchRule{
		{{Source: "", Target: "gpt-5.6-luna"}},
		{{Source: "gpt-6-astra", Target: ""}},
		{{Source: "x", Target: string(make([]rune, MaxOpenAIOAuthModelMismatchName+1))}},
	} {
		_, err := NormalizeOpenAIOAuthModelMismatchRules(rules)
		require.Error(t, err)
	}
}

func TestMatchOpenAIOAuthModelMismatchRuleIsCaseInsensitiveAndExact(t *testing.T) {
	rules := []OpenAIOAuthModelMismatchRule{{Source: "gpt-6-astra", Target: "gpt-5.6-luna"}}
	rule, ok := MatchOpenAIOAuthModelMismatchRule(rules, " GPT-6-ASTRA ", "GPT-5.6-LUNA")
	require.True(t, ok)
	require.Equal(t, rules[0], rule)

	_, ok = MatchOpenAIOAuthModelMismatchRule(rules, "gpt-6-astra-preview", "gpt-5.6-luna")
	require.False(t, ok)
	_, ok = MatchOpenAIOAuthModelMismatchRule(rules, "gpt-6-astra", "gpt-5.6-sol")
	require.False(t, ok)
}

func TestOpenAIOAuthAutoDisabledModelsAreDeduplicatedAndAffectScheduling(t *testing.T) {
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Extra: map[string]any{
			OpenAIOAuthAutoDisabledModelsExtraKey: []any{" gpt-6-astra ", "GPT-6-ASTRA", ""},
		},
	}
	require.Equal(t, []string{"gpt-6-astra"}, account.OpenAIOAuthAutoDisabledModels())
	require.True(t, account.IsOpenAIOAuthModelAutoDisabled("GPT-6-ASTRA"))
	require.False(t, account.IsModelSupported("gpt-6-astra"))
	require.True(t, account.IsModelSupported("gpt-5.6-luna"))
}

func TestOpenAIOAuthAutoDisabledStateDoesNotAffectOtherAccountTypes(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeSetupToken} {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     accountType,
			Extra: map[string]any{
				OpenAIOAuthAutoDisabledModelsExtraKey: []any{"gpt-6-astra"},
			},
		}
		require.False(t, account.IsOpenAIOAuthModelAutoDisabled("gpt-6-astra"))
		require.True(t, account.IsModelSupported("gpt-6-astra"))
	}
}
