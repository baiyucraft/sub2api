package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type auxiliaryProxyHTTPUpstream struct {
	mu      sync.Mutex
	proxies []string
	do      func(*http.Request, string) (*http.Response, error)
}

func (u *auxiliaryProxyHTTPUpstream) Do(req *http.Request, proxyURL string, _ int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	u.proxies = append(u.proxies, proxyURL)
	u.mu.Unlock()
	return u.do(req, proxyURL)
}

func (u *auxiliaryProxyHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

func (u *auxiliaryProxyHTTPUpstream) attemptedProxies() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.proxies...)
}

func auxiliaryProxy(id int64, name, host string) *Proxy {
	return &Proxy{ID: id, Name: name, Protocol: "http", Host: host, Port: 8080, Status: StatusActive}
}

func auxiliaryMultiProxyAccount(account *Account) (*Proxy, *Proxy) {
	first := auxiliaryProxy(11, "first", "proxy-one.example")
	second := auxiliaryProxy(12, "second", "proxy-two.example")
	account.ProxyID = &first.ID
	account.Proxy = first
	account.ProxyIDs = []int64{first.ID, second.ID}
	account.Proxies = []*Proxy{first, second}
	return first, second
}

func auxiliaryTransportError(req *http.Request) error {
	return &url.Error{Op: req.Method, URL: req.URL.String(), Err: errors.New("connection reset by peer")}
}

func TestOpenAIInputTokensRetriesNextAccountProxyOnTransportFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))

	account := &Account{
		ID: 101, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 5,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://api.openai.com"},
	}
	first, second := auxiliaryMultiProxyAccount(account)
	upstream := &auxiliaryProxyHTTPUpstream{do: func(req *http.Request, proxyURL string) (*http.Response, error) {
		if proxyURL == first.URL() {
			return nil, auxiliaryTransportError(req)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`))}, nil
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}

	err := svc.ForwardCountTokensAsAnthropic(context.Background(), c, account, body, "gpt-5.4")

	require.NoError(t, err)
	require.Equal(t, []string{first.URL(), second.URL()}, upstream.attemptedProxies())
	require.JSONEq(t, `{"input_tokens":42}`, recorder.Body.String())
}

func TestGeminiModelsRetriesNextAccountProxyOnTransportFailure(t *testing.T) {
	account := &Account{
		ID: 102, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 5,
		Credentials: map[string]any{"api_key": "gemini-key", "base_url": "https://generativelanguage.googleapis.com"},
	}
	first, second := auxiliaryMultiProxyAccount(account)
	upstream := &auxiliaryProxyHTTPUpstream{do: func(req *http.Request, proxyURL string) (*http.Response, error) {
		if proxyURL == first.URL() {
			return nil, auxiliaryTransportError(req)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"models/gemini-2.0-flash"}]}`))}, nil
	}}
	svc := &AccountTestService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}

	models, err := svc.FetchUpstreamSupportedModels(context.Background(), account)

	require.NoError(t, err)
	require.Equal(t, []string{"gemini-2.0-flash"}, models)
	require.Equal(t, []string{first.URL(), second.URL()}, upstream.attemptedProxies())
}

func TestUpstreamModelsDoesNotRetryNextProxyOnHTTPAuthFailure(t *testing.T) {
	account := &Account{
		ID: 103, Platform: PlatformGemini, Type: AccountTypeAPIKey, Concurrency: 5,
		Credentials: map[string]any{"api_key": "gemini-key", "base_url": "https://generativelanguage.googleapis.com"},
	}
	first, _ := auxiliaryMultiProxyAccount(account)
	upstream := &auxiliaryProxyHTTPUpstream{do: func(_ *http.Request, _ string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"invalid key"}}`))}, nil
	}}
	svc := &AccountTestService{
		cfg:          &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}},
		httpUpstream: upstream,
	}

	_, err := svc.FetchUpstreamSupportedModels(context.Background(), account)

	require.Error(t, err)
	require.Equal(t, []string{first.URL()}, upstream.attemptedProxies())
}

func TestCodexModelsManifestRetriesNextAccountProxyOnTransportFailure(t *testing.T) {
	account := newCodexModelsAPIKeyTestAccount("https://upstream.example/v1")
	first, second := auxiliaryMultiProxyAccount(account)
	upstream := &auxiliaryProxyHTTPUpstream{do: func(req *http.Request, proxyURL string) (*http.Response, error) {
		if proxyURL == first.URL() {
			return nil, auxiliaryTransportError(req)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"gpt-5.4"}]}`))}, nil
	}}
	svc := newCodexModelsAPIKeyTestService(upstream)

	manifest, err := svc.FetchCodexModelsManifest(context.Background(), account, "0.150.0", "")

	require.NoError(t, err)
	require.NotNil(t, manifest)
	require.Equal(t, []string{first.URL(), second.URL()}, upstream.attemptedProxies())
}

func TestUpstreamBillingProbeRetriesTransportFailureBeforePersisting(t *testing.T) {
	account := &Account{
		ID: 104, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 5,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://relay.example"},
		Extra:       map[string]any{UpstreamBillingProbeEnabledExtraKey: true},
	}
	first, second := auxiliaryMultiProxyAccount(account)
	repo := &upstreamBillingProbeAccountRepo{accounts: map[int64]*Account{account.ID: account}}
	upstream := &auxiliaryProxyHTTPUpstream{do: func(req *http.Request, proxyURL string) (*http.Response, error) {
		if proxyURL == first.URL() {
			return nil, auxiliaryTransportError(req)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{
			"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token",
			"group_rate_multiplier":0.8,"resolved_rate_multiplier":0.8,
			"peak_rate_enabled":false,"effective_rate_multiplier":0.8,
			"observed_at":"2026-07-13T01:00:00Z"
		}`))}, nil
	}}
	svc := newUpstreamBillingProbeTestService(repo, upstream, &upstreamBillingProbeSettingRepo{})

	snapshot, err := svc.probeLoadedAccount(context.Background(), account, 30)

	require.NoError(t, err)
	require.Equal(t, UpstreamBillingProbeStatusOK, snapshot.Status)
	require.Equal(t, []string{first.URL(), second.URL()}, upstream.attemptedProxies())
	require.Zero(t, snapshot.FailureCount)
}
