package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayProtectionAdmissionCache struct {
	GatewayCache
	bound    int64
	writes   int
	writeErr error
}

func (c *gatewayProtectionAdmissionCache) SetSessionAccountID(_ context.Context, _ int64, _ string, id int64, _ time.Duration) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.bound = id
	c.writes++
	return nil
}

type gatewayProtectionAdmissionUpstream struct {
	cache *gatewayProtectionAdmissionCache
	calls int
}

func (u *gatewayProtectionAdmissionUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.calls++
	if u.cache.bound != 2 {
		return nil, errors.New("migration was not committed at physical send")
	}
	return nil, errors.New("physical transport failure after send")
}

func (u *gatewayProtectionAdmissionUpstream) DoWithTLS(req *http.Request, proxy string, id int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, concurrency)
}

func TestGatewayProtectionAdmissionRealForwardPreservesSourceUntilSend(t *testing.T) {
	for _, platform := range []string{PlatformAnthropic, PlatformGemini} {
		for _, boundary := range []string{"credential", "binding", "canceled", "sent_transport_failure"} {
			t.Run(platform+"/"+boundary, func(t *testing.T) {
				// These provider paths opt into cache protection explicitly; the
				// default platform range remains OpenAI only.
				cacheGuardTestRunner(t)
				policy := newProtectionPlatformScopeResolver()
				policy.selected = map[string]bool{platform: true}
				policy.groups[100] = platform
				currentUpstreamCacheGuardRunner().SetProtectionPolicyResolver(policy)
				cache := &gatewayProtectionAdmissionCache{bound: 1}
				upstream := &gatewayProtectionAdmissionUpstream{cache: cache}
				svc := &GatewayService{cache: cache, httpUpstream: upstream, cfg: &config.Config{}}
				keyID := int64(2)
				account := &Account{ID: 2, Platform: platform, Type: AccountTypeAPIKey, UpstreamKeyID: &keyID, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key"}}
				if boundary == "credential" {
					account.Credentials = nil
				}
				if boundary == "binding" {
					cache.writeErr = errors.New("binding unavailable")
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				group := int64(100)
				selection := &AccountSelectionResult{Account: account, Acquired: true, cacheGuardMigrationAccountID: account.ID}
				ctx = svc.WithGatewaySchedulingProtectionMigration(ContextWithSelectionProfitGate(ctx, selection), &group, "conversation", selection)
				require.Equal(t, int64(1), cache.bound, "post-slot nomination must remain tentative")
				if boundary == "canceled" {
					cancel()
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
				var err error
				if platform == PlatformAnthropic {
					_, err = svc.forwardAnthropicAPIKeyPassthroughWithInput(ctx, c, account, anthropicPassthroughForwardInput{Body: []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`), RequestModel: "claude-test", StartTime: time.Now()})
				} else {
					gemini := &GeminiMessagesCompatService{httpUpstream: upstream, cfg: &config.Config{}}
					_, err = gemini.ForwardNative(ctx, c, account, "gemini-test", "generateContent", false, []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`))
				}
				require.Error(t, err)
				if boundary == "sent_transport_failure" {
					require.Equal(t, 1, upstream.calls)
					require.Equal(t, 1, cache.writes)
					require.Equal(t, int64(2), cache.bound, "a failure after physical send does not roll back an admitted migration")
				} else {
					require.Zero(t, upstream.calls, "pre-send rejection must not reach transport")
					require.Zero(t, cache.writes)
					require.Equal(t, int64(1), cache.bound)
				}
			})
		}
	}
}

type gatewayProtectionDeniedRPM struct{ RPMCache }

func (gatewayProtectionDeniedRPM) TryAcquireRPM(context.Context, int64, int) (bool, int, time.Duration, error) {
	return false, 1, time.Second, nil
}

func TestGatewayProtectionAdmissionRPMRefusalPreservesSource(t *testing.T) {
	cache := &gatewayProtectionAdmissionCache{bound: 1}
	svc := &GatewayService{cache: cache}
	account := &Account{ID: 2, RPMLimit: 1}
	selection := &AccountSelectionResult{Account: account, Acquired: true, cacheGuardMigrationAccountID: account.ID}
	ctx := svc.WithGatewaySchedulingProtectionMigration(ContextWithSelectionProfitGate(context.Background(), selection), nil, "conversation", selection)
	allowed, _, err := TryAcquireAccountRPM(ctx, gatewayProtectionDeniedRPM{}, account)
	require.NoError(t, err)
	require.False(t, allowed)
	require.Equal(t, int64(1), cache.bound)
	require.Zero(t, cache.writes)
}
