package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpdateAccountPreservesCodexTicketOnEdit(t *testing.T) {
	private := map[string]any{
		"codex_turn_ticket:custom":     map[string]any{"state": "historical-state"},
		"codex_ticket_config":          map[string]any{"proxy_url": "http://user:private@host:8080"},
		"codex_ticket_watchdog":        map[string]any{"trigger_count": 3},
		"codex_ticket_watchdog:custom": map[string]any{"trigger_count": 4},
		"codex_harvest_proxy_url":      "http://user:legacy@host:8080",
	}
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: private}
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{41: account}}
	svc := &adminServiceImpl{accountRepo: repo}
	for key, value := range private {
		for _, extra := range []map[string]any{{key: "forged", "custom": true}, {"custom": true}} {
			updated, err := svc.UpdateAccount(context.Background(), 41, &UpdateAccountInput{Extra: extra})
			require.NoError(t, err)
			require.Equal(t, value, updated.Extra[key])
			require.Equal(t, true, updated.Extra["custom"])
		}
		require.NoError(t, svc.UpdateAccountExtra(context.Background(), 41, map[string]any{key: "forged"}))
		require.Equal(t, value, repo.accounts[41].Extra[key])
	}
}

func TestCreateAccountDropsUserSuppliedCodexTickets(t *testing.T) {
	extra := map[string]any{"codex_turn_ticket:custom": map[string]any{"state": "forged"}, "codex_harvest_proxy_url": "http://private@host:8080", "custom": true}
	account, err := buildAccountForCreate(&CreateAccountInput{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, extra)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"custom": true}, account.Extra)
	require.Contains(t, extra, "codex_turn_ticket:custom")
}
