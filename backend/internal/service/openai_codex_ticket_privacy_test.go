package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRetiredCodexTicketPrivacyHelpers(t *testing.T) {
	privateKeys := []string{"codex_ticket_config", "codex_turn_ticket:any-model", "codex_ticket_watchdog", "codex_ticket_watchdog:any-model", "codex_harvest_proxy_url"}
	for _, key := range privateKeys {
		t.Run(key, func(t *testing.T) {
			current := map[string]any{key: "stored-private", "unrelated": "old"}
			incoming := map[string]any{key: "forged-private", "unrelated": "new"}
			require.True(t, IsOpenAICodexTicketPrivateExtraKey(key))
			require.Equal(t, map[string]any{"unrelated": "old"}, RedactOpenAICodexTicketExtra(current))
			require.Equal(t, map[string]any{"unrelated": "new"}, MergeOpenAICodexTicketExtra(incoming, nil))
			require.Equal(t, map[string]any{key: "stored-private", "unrelated": "new"}, MergeOpenAICodexTicketExtra(incoming, current))
			require.Equal(t, map[string]any{key: "stored-private"}, MergeOpenAICodexTicketExtra(nil, current))
			require.Equal(t, "stored-private", current[key])
			require.Equal(t, "forged-private", incoming[key])
		})
	}
	require.False(t, IsOpenAICodexTicketPrivateExtraKey("custom"))
	require.Nil(t, RedactOpenAICodexTicketExtra(nil))
	require.Nil(t, MergeOpenAICodexTicketExtra(nil, nil))
}
