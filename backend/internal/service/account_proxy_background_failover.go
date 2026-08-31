package service

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

var ErrNoAvailableAccountProxyRoutes = errors.New("no available account proxy routes")

// accountProxyAttempts returns shallow account copies pinned to each usable
// proxy in binding order. Account credentials and metadata are read-only for
// these background operations, so copying the account shell is sufficient.
func accountProxyAttempts(account *Account) []*Account {
	if account == nil {
		return nil
	}
	proxies := AvailableAccountProxyRoutes(account, time.Now())
	if len(proxies) == 0 {
		if accountHasConfiguredProxy(account) {
			return nil
		}
		return []*Account{account}
	}
	if account.Proxy != nil {
		for index, proxy := range proxies {
			if proxy.ID != account.Proxy.ID {
				continue
			}
			if index > 0 {
				proxies = append([]*Proxy{proxy}, append(proxies[:index], proxies[index+1:]...)...)
			}
			break
		}
	}
	if len(proxies) == 1 && account.Proxy != nil && account.Proxy.ID == proxies[0].ID {
		return []*Account{account}
	}
	attempts := make([]*Account, 0, len(proxies))
	for _, proxy := range proxies {
		copyAccount := *account
		proxyID := proxy.ID
		copyAccount.ProxyID = &proxyID
		copyAccount.Proxy = proxy
		attempts = append(attempts, &copyAccount)
	}
	return attempts
}

func isAccountProxyTransportError(ctx context.Context, err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"proxyconnect", "proxy connection", "tls handshake", "connection reset",
		"connection refused", "no route to host", "network is unreachable",
		"no such host", "unexpected eof",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func withAccountProxyFallback[T any](ctx context.Context, account *Account, operation func(*Account) (T, error)) (T, error) {
	var zero T
	attempts := accountProxyAttempts(account)
	if len(attempts) == 0 {
		if accountHasConfiguredProxy(account) {
			return zero, ErrNoAvailableAccountProxyRoutes
		}
		return operation(account)
	}
	var lastErr error
	for index, attempt := range attempts {
		result, err := operation(attempt)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if index == len(attempts)-1 || !isAccountProxyTransportError(ctx, err) {
			return zero, err
		}
	}
	return zero, lastErr
}
