package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type tokenCountAdmissionCache struct {
	service.GatewayCache
	concurrencyCacheMock
	proxyAcquired bool
	proxyReleases atomic.Int32
	bindings      map[string]int64
}

func (c *tokenCountAdmissionCache) AcquireAccountProxySlot(context.Context, int64, int64, int, string) (bool, error) {
	return c.proxyAcquired, nil
}

func (c *tokenCountAdmissionCache) ReleaseAccountProxySlot(context.Context, int64, int64, string) error {
	c.proxyReleases.Add(1)
	return nil
}

func (c *tokenCountAdmissionCache) GetAccountProxyConcurrency(context.Context, int64, int64) (int, error) {
	return 0, nil
}

func (c *tokenCountAdmissionCache) GetOpenAIProxyGroupBinding(_ context.Context, accountID int64, sessionHash string) (int64, error) {
	proxyID, ok := c.bindings[tokenCountBindingKey(accountID, sessionHash)]
	if !ok {
		return 0, service.ErrOpenAIProxyGroupBindingNotFound
	}
	return proxyID, nil
}

func (c *tokenCountAdmissionCache) ClaimOpenAIProxyGroupBinding(_ context.Context, accountID int64, sessionHash string, proxyID int64, _ time.Duration) (int64, error) {
	if c.bindings == nil {
		c.bindings = make(map[string]int64)
	}
	if bound := c.bindings[tokenCountBindingKey(accountID, sessionHash)]; bound > 0 {
		return bound, nil
	}
	c.bindings[tokenCountBindingKey(accountID, sessionHash)] = proxyID
	return proxyID, nil
}

func (c *tokenCountAdmissionCache) ReplaceOpenAIProxyGroupBindingIfMatch(_ context.Context, accountID int64, sessionHash string, oldProxyID, newProxyID int64, _ time.Duration) (bool, error) {
	key := tokenCountBindingKey(accountID, sessionHash)
	if c.bindings[key] != oldProxyID {
		return false, nil
	}
	c.bindings[key] = newProxyID
	return true, nil
}

func (c *tokenCountAdmissionCache) DeleteOpenAIProxyGroupBindingIfMatch(_ context.Context, accountID int64, sessionHash string, proxyID int64) error {
	key := tokenCountBindingKey(accountID, sessionHash)
	if c.bindings[key] == proxyID {
		delete(c.bindings, key)
	}
	return nil
}

func tokenCountBindingKey(accountID int64, sessionHash string) string {
	return strconv.FormatInt(accountID, 10) + ":" + sessionHash
}

type tokenCountProxyGroupRepo struct {
	service.ProxyIPGroupRepository
	group *service.ProxyIPGroup
}

func (r *tokenCountProxyGroupRepo) GetByID(context.Context, int64) (*service.ProxyIPGroup, error) {
	return r.group, nil
}

type tokenCountProxyRepo struct {
	service.ProxyRepository
	proxies []service.Proxy
}

func (r *tokenCountProxyRepo) ListByIDs(context.Context, []int64) ([]service.Proxy, error) {
	return append([]service.Proxy(nil), r.proxies...), nil
}

func newTokenCountAdmissionHandler(cache *tokenCountAdmissionCache, group *service.ProxyIPGroup, proxies []service.Proxy) *OpenAIGatewayHandler {
	concurrency := service.NewConcurrencyService(cache)
	gateway := service.NewOpenAIGatewayService(
		nil, nil, nil, nil, nil, nil,
		cache,
		&config.Config{},
		nil,
		concurrency,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	gateway.SetOpenAIProxyGroupRepositories(
		&tokenCountProxyGroupRepo{group: group},
		&tokenCountProxyRepo{proxies: proxies},
	)
	return &OpenAIGatewayHandler{
		gatewayService:    gateway,
		concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0),
	}
}

func TestAcquireOpenAITokenCountAdmissionUsesAndReleasesBothSlots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(71)
	cache := &tokenCountAdmissionCache{
		proxyAcquired: true,
		bindings:      make(map[string]int64),
	}
	cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
	h := newTokenCountAdmissionHandler(cache, &service.ProxyIPGroup{
		ID: groupID, PerIPConcurrency: 2, ProxyIDs: []int64{9},
	}, []service.Proxy{{ID: 9, Name: "proxy-9", Protocol: "http", Host: "127.0.0.9", Port: 8080, Status: service.StatusActive}})
	account := &service.Account{
		ID: 901, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 4, ProxyIPGroupID: &groupID,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/input_tokens", nil)
	var wroteError bool

	resolved, release, admitted := h.acquireOpenAITokenCountAdmission(c, account, "session-a", zap.NewNop(), func(int, string, string, string) {
		wroteError = true
	})
	require.True(t, admitted)
	require.False(t, wroteError)
	require.NotNil(t, resolved.ProxyID)
	require.EqualValues(t, 9, *resolved.ProxyID)
	require.NotNil(t, release)

	release()
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.releaseAccountCalled))
	require.EqualValues(t, 1, cache.proxyReleases.Load())
}

func TestAcquireOpenAITokenCountAdmissionReleasesAccountSlotWhenProxyGroupFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(72)
	cache := &tokenCountAdmissionCache{proxyAcquired: true, bindings: make(map[string]int64)}
	cache.acquireAccountSlotFn = func(context.Context, int64, int, string) (bool, error) { return true, nil }
	h := newTokenCountAdmissionHandler(cache, &service.ProxyIPGroup{
		ID: groupID, PerIPConcurrency: 2,
	}, nil)
	account := &service.Account{
		ID: 902, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Concurrency: 4, ProxyIPGroupID: &groupID,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	status := 0
	code := ""

	resolved, release, admitted := h.acquireOpenAITokenCountAdmission(c, account, "session-a", zap.NewNop(), func(gotStatus int, _ string, gotCode string, _ string) {
		status = gotStatus
		code = gotCode
	})
	require.False(t, admitted)
	require.Nil(t, resolved)
	require.Nil(t, release)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, service.OpenAIProxyGroupNoEgressCode, code)
	require.EqualValues(t, 1, atomic.LoadInt32(&cache.releaseAccountCalled))
	require.Zero(t, cache.proxyReleases.Load())
}
