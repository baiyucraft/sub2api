//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCreateAccountValidatesAndPreservesOrderedProxyIDs(t *testing.T) {
	proxyIDs := []int64{12, 11}
	repo := &upstreamBillingProbeAccountRepo{}
	svc := &adminServiceImpl{
		accountRepo: repo,
		proxyRepo: &proxyRepoStub{proxies: []Proxy{
			{ID: 11, Status: StatusActive},
			{ID: 12, Status: StatusActive},
		}},
	}
	created, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "multi", Platform: PlatformGemini, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "x"}, ProxyIDs: &proxyIDs,
		SkipDefaultGroupBind: true,
	})
	require.NoError(t, err)
	require.Equal(t, proxyIDs, created.ProxyIDs)
	require.NotNil(t, created.ProxyID)
	require.Equal(t, int64(12), *created.ProxyID)
}

func TestCreateAccountRejectsDuplicateUnavailableAndExpiredProxyIDs(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	svc := &adminServiceImpl{
		accountRepo: &upstreamBillingProbeAccountRepo{},
		proxyRepo: &proxyRepoStub{proxies: []Proxy{
			{ID: 1, Status: StatusActive},
			{ID: 2, Status: StatusDisabled},
			{ID: 3, Status: StatusActive, ExpiresAt: &expired},
		}},
	}
	for name, proxyIDs := range map[string][]int64{
		"duplicate": {1, 1},
		"missing":   {4},
		"disabled":  {2},
		"expired":   {3},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
				Name: "invalid", Platform: PlatformGemini, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "x"}, ProxyIDs: &proxyIDs,
				SkipDefaultGroupBind: true,
			})
			require.Error(t, err)
		})
	}
}

func TestCreateAccountLegacyProxyIDUsesMultiProxyAvailabilityValidation(t *testing.T) {
	expired := time.Now().Add(-time.Minute)
	for name, proxy := range map[string]Proxy{
		"missing":  {},
		"disabled": {ID: 2, Status: StatusDisabled},
		"expired":  {ID: 3, Status: StatusActive, ExpiresAt: &expired},
	} {
		t.Run(name, func(t *testing.T) {
			proxyID := map[string]int64{"missing": 4, "disabled": 2, "expired": 3}[name]
			proxies := []Proxy(nil)
			if proxy.ID != 0 {
				proxies = append(proxies, proxy)
			}
			svc := &adminServiceImpl{
				accountRepo: &upstreamBillingProbeAccountRepo{},
				proxyRepo:   &proxyRepoStub{proxies: proxies},
			}
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
				Name: "legacy-invalid", Platform: PlatformGemini, Type: AccountTypeAPIKey,
				Credentials: map[string]any{"api_key": "x"}, ProxyID: &proxyID,
				SkipDefaultGroupBind: true,
			})
			require.Error(t, err)
		})
	}
}
