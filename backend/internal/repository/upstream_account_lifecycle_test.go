package repository

import (
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLegacySyncManagedAccountSignatureRequiresCompleteSynchronizerShape(t *testing.T) {
	config := &dbent.UpstreamConfig{
		ID:       41,
		Name:     "provider",
		Provider: service.UpstreamProviderSub2API,
		AuthMode: service.UpstreamAuthModeManualJWT,
	}
	key := &dbent.UpstreamKey{ID: 51, UpstreamConfigID: config.ID, Name: "key"}
	name, err := service.BuildUpstreamAccountName(config.Name, key.Name)
	require.NoError(t, err)

	valid := func() *dbent.Account {
		configID := config.ID
		keyID := key.ID
		return &dbent.Account{
			Name:             name,
			Type:             service.AccountTypeAPIKey,
			Credentials:      map[string]any{"pool_mode": true},
			Extra:            map[string]any{service.AccountUpstreamProviderKey: config.Provider, service.AccountSub2APIRateSyncAdapterKey: config.AuthMode},
			UpstreamConfigID: &configID,
			UpstreamKeyID:    &keyID,
		}
	}

	require.True(t, legacySyncManagedAccountSignature(valid(), config, key))

	tests := []struct {
		name   string
		mutate func(*dbent.Account)
	}{
		{name: "wrong account type", mutate: func(account *dbent.Account) { account.Type = service.AccountTypeOAuth }},
		{name: "child account", mutate: func(account *dbent.Account) { parentID := int64(99); account.ParentAccountID = &parentID }},
		{name: "proxied account", mutate: func(account *dbent.Account) { proxyID := int64(88); account.ProxyID = &proxyID }},
		{name: "pool mode false", mutate: func(account *dbent.Account) { account.Credentials = map[string]any{"pool_mode": false} }},
		{name: "extra credential", mutate: func(account *dbent.Account) { account.Credentials["api_key"] = "manual" }},
		{name: "wrong provider", mutate: func(account *dbent.Account) {
			account.Extra[service.AccountUpstreamProviderKey] = service.UpstreamProviderNewAPI
		}},
		{name: "wrong adapter", mutate: func(account *dbent.Account) {
			account.Extra[service.AccountSub2APIRateSyncAdapterKey] = service.UpstreamAuthModeCookie
		}},
		{name: "renamed account", mutate: func(account *dbent.Account) { account.Name += "-manual" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := valid()
			tt.mutate(account)
			require.False(t, legacySyncManagedAccountSignature(account, config, key))
		})
	}
}

func TestUpstreamKeyMissingEligibleRequiresCountAndGracePeriod(t *testing.T) {
	missingSince := time.Date(2026, 8, 17, 1, 0, 0, 0, time.UTC)

	require.False(t, upstreamKeyMissingEligible(upstreamKeyMissingThreshold-1, &missingSince, missingSince.Add(upstreamKeyMissingGrace)))
	require.False(t, upstreamKeyMissingEligible(upstreamKeyMissingThreshold, &missingSince, missingSince.Add(upstreamKeyMissingGrace-time.Nanosecond)))
	require.True(t, upstreamKeyMissingEligible(upstreamKeyMissingThreshold, &missingSince, missingSince.Add(upstreamKeyMissingGrace)))
	require.False(t, upstreamKeyMissingEligible(upstreamKeyMissingThreshold, nil, missingSince.Add(upstreamKeyMissingGrace)))
}

func TestAllAccountsSyncManagedRejectsMixedOwnership(t *testing.T) {
	require.False(t, allAccountsSyncManaged(nil))
	require.False(t, allAccountsSyncManaged([]*dbent.Account{nil}))
	require.True(t, allAccountsSyncManaged([]*dbent.Account{
		{UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
		{UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
	}))
	require.False(t, allAccountsSyncManaged([]*dbent.Account{
		{UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerSyncManaged},
		{UpstreamLifecycleOwner: service.AccountUpstreamLifecycleOwnerManual},
	}))
}
