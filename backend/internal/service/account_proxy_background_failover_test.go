package service

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithAccountProxyFallbackRetriesTransportFailureInBindingOrder(t *testing.T) {
	account := &Account{
		ID:       42,
		ProxyIDs: []int64{11, 12},
		Proxies: []*Proxy{
			{ID: 11, Name: "first", Status: StatusActive},
			{ID: 12, Name: "second", Status: StatusActive},
		},
	}
	var attempted []int64

	result, err := withAccountProxyFallback(context.Background(), account, func(attempt *Account) (string, error) {
		attempted = append(attempted, *attempt.ProxyID)
		if *attempt.ProxyID == 11 {
			return "", &url.Error{Op: "Post", URL: "https://example.invalid", Err: errors.New("connection reset")}
		}
		return attempt.Proxy.Name, nil
	})

	require.NoError(t, err)
	require.Equal(t, "second", result)
	require.Equal(t, []int64{11, 12}, attempted)
	require.Nil(t, account.ProxyID, "background attempts must not mutate the persisted account shell")
}

func TestWithAccountProxyFallbackDoesNotRetryAccountLevelFailure(t *testing.T) {
	account := &Account{
		ID:       42,
		ProxyIDs: []int64{11, 12},
		Proxies: []*Proxy{
			{ID: 11, Status: StatusActive},
			{ID: 12, Status: StatusActive},
		},
	}
	attempts := 0
	wantErr := errors.New("invalid_grant")

	_, err := withAccountProxyFallback(context.Background(), account, func(*Account) (string, error) {
		attempts++
		return "", wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, 1, attempts)
}

func TestAccountProxyAttemptsSkipsUnavailableRoutes(t *testing.T) {
	account := &Account{
		ID:       42,
		ProxyIDs: []int64{11, 12, 13},
		Proxies: []*Proxy{
			{ID: 11, Status: StatusDisabled},
			{ID: 12, Status: StatusActive},
			{ID: 13, Status: StatusDisabled},
		},
	}

	attempts := accountProxyAttempts(account)

	require.Len(t, attempts, 1)
	require.Equal(t, int64(12), *attempts[0].ProxyID)
}

func TestAccountProxyAttemptsKeepsSelectedRouteFirst(t *testing.T) {
	account := &Account{
		ID:       42,
		ProxyID:  int64Ptr(12),
		Proxy:    &Proxy{ID: 12, Status: StatusActive},
		ProxyIDs: []int64{11, 12, 13},
		Proxies: []*Proxy{
			{ID: 11, Status: StatusActive},
			{ID: 12, Status: StatusActive},
			{ID: 13, Status: StatusActive},
		},
	}

	attempts := accountProxyAttempts(account)

	require.Len(t, attempts, 3)
	require.Equal(t, []int64{12, 11, 13}, []int64{*attempts[0].ProxyID, *attempts[1].ProxyID, *attempts[2].ProxyID})
}

func TestWithAccountProxyFallbackRejectsConfiguredButUnavailableRoutes(t *testing.T) {
	account := &Account{
		ID:       42,
		ProxyIDs: []int64{11},
		Proxies:  []*Proxy{{ID: 11, Status: StatusDisabled}},
	}
	called := false

	_, err := withAccountProxyFallback(context.Background(), account, func(*Account) (string, error) {
		called = true
		return "", nil
	})

	require.ErrorIs(t, err, ErrNoAvailableAccountProxyRoutes)
	require.False(t, called)
}

func TestWithAccountProxyFallbackDoesNotRetryCanceledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	account := &Account{
		ID:       42,
		ProxyIDs: []int64{11, 12},
		Proxies: []*Proxy{
			{ID: 11, Status: StatusActive},
			{ID: 12, Status: StatusActive},
		},
	}
	attempts := 0

	_, err := withAccountProxyFallback(ctx, account, func(*Account) (string, error) {
		attempts++
		return "", context.Canceled
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, attempts)
}
