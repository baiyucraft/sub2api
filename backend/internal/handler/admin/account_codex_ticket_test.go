package admin

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountResponseRedactsRetiredCodexTicketMaterial(t *testing.T) {
	account := &service.Account{ID: 41, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{
			"codex_ticket_config":          map[string]any{"proxy_url": "http://user:private-proxy@host:8080"},
			"codex_turn_ticket:custom":     map[string]any{"state": "private-state"},
			"codex_ticket_watchdog":        map[string]any{"last_reason": "private-watchdog"},
			"codex_ticket_watchdog:custom": "private-model-watchdog",
			"codex_harvest_proxy_url":      "http://user:private-legacy@host:8080",
			"custom":                       true,
		},
	}
	h := &AccountHandler{}
	for _, response := range []any{h.accountResponseFromService(account), h.accountListResponseFromService(account)} {
		encoded, err := json.Marshal(response)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "private-")
		require.NotContains(t, string(encoded), "codex_turn_tickets")
		require.NotContains(t, string(encoded), "codex_ticket")
		require.Contains(t, string(encoded), `"custom":true`)
	}
	require.Len(t, account.Extra, 6)
}
