package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsImagePermissionRepo struct {
	AccountRepository
	mu           sync.Mutex
	account      *Account
	reads        int
	onRead       func(int, *Account) *Account
	cooldownKeys []string
}

func (r *wsImagePermissionRepo) GetByID(_ context.Context, _ int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	a := r.account
	if r.onRead != nil {
		a = r.onRead(r.reads, a)
	}
	return a, nil
}

func (r *wsImagePermissionRepo) SetModelRateLimit(_ context.Context, _ int64, key string, _ time.Time, _ ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cooldownKeys = append(r.cooldownKeys, key)
	return nil
}

func TestWSImagePermissionRefreshPreservesChosenIdentity(t *testing.T) {
	chosen := imagePolicyAccount(true)
	chosen.Proxy = &Proxy{ID: 11, Host: "chosen.local"}
	chosen.Credentials = map[string]any{"api_key": "chosen-test"}
	chosen.Concurrency = 7
	chosen.GroupIDs = []int64{7}
	fresh := imagePolicyAccount(false)
	fresh.Proxy = &Proxy{ID: 22, Host: "new.local"}
	fresh.Credentials = map[string]any{"api_key": "new-test"}
	fresh.Concurrency = 1
	fresh.GroupIDs = []int64{5}
	fresh.Extra = map[string]any{featureKeyCodexImageGenerationBridge: false}
	svc := &OpenAIGatewayService{accountRepo: &wsImagePermissionRepo{account: fresh}}
	view, err := svc.openAIWSImagePermissionView(context.Background(), chosen)
	require.NoError(t, err)
	require.Same(t, chosen.Proxy, view.Proxy)
	require.Equal(t, chosen.Credentials, view.Credentials)
	require.Equal(t, chosen.GroupIDs, view.GroupIDs)
	require.Equal(t, 7, view.Concurrency)
	require.Equal(t, fresh.Extra, view.Extra)
	require.Equal(t, OpenAIImagePermissionDenied, OpenAIImagePermissionRank(view, 86400))
	require.Equal(t, OpenAIImagePermissionAllowed, OpenAIImagePermissionRank(chosen, 86400))
}

func TestWSImageWriteGuardSessionInheritanceAndFreshPermission(t *testing.T) {
	for _, typ := range []coderws.MessageType{coderws.MessageText, coderws.MessageBinary} {
		t.Run(typ.String(), func(t *testing.T) {
			chosen := imagePolicyAccount(true)
			fresh := imagePolicyAccount(true)
			repo := &wsImagePermissionRepo{account: fresh}
			svc := &OpenAIGatewayService{accountRepo: repo}
			capture := &openAIWSCaptureConn{}
			guard := &openAIWSImageGuardFrameConn{inner: capture, prepare: func(ctx context.Context, body []byte, r OpenAIImageRequest) ([]byte, error) {
				prepared, err := svc.prepareOpenAIWSImageSend(ctx, nil, chosen, body, r, false)
				return prepared.body, err
			}}
			ctx := context.Background()
			require.NoError(t, guard.WriteFrame(ctx, typ, []byte(`{"type":"session.update","session":{"model":"gpt-6.1-sol","tools":[{"type":"image_generation"}],"tool_choice":"none"}}`)))
			repo.account = imagePolicyAccount(false)
			var failure *UpstreamFailoverError
			require.ErrorAs(t, guard.WriteFrame(ctx, typ, []byte(`{"type":"response.create","input":"hello"}`)), &failure)
			require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
			require.Len(t, capture.writes, 1)
			repo.account = imagePolicyAccount(true)
			setAccountModelRateLimitSnapshot(repo.account, openAIImageGenerationRateLimitKey, time.Now().Add(time.Hour), "test", time.Now())
			require.ErrorAs(t, guard.WriteFrame(ctx, typ, []byte(`{"type":"response.create"}`)), &failure)
			require.Equal(t, GatewayFailureReason("image_capability_cooldown"), failure.Reason)
			require.Len(t, capture.writes, 1)
		})
	}
}

func TestWSImageSendPassiveStripAndContinuation(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &wsImagePermissionRepo{account: imagePolicyAccount(false)}}
	chosen := imagePolicyAccount(true)
	body := []byte(`{"type":"response.create","model":"gpt-6.1-sol","tools":[{"type":"namespace","name":"image_gen"}]}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "gpt-6.1-sol", body, true)
	prepared, err := svc.prepareOpenAIWSImageSend(context.Background(), nil, chosen, body, r, false)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(prepared.body, "tools").Exists())
	require.True(t, gjson.GetBytes(body, "tools").Exists())
	for _, history := range []string{
		`{"previous_response_id":"resp_test","tools":[{"type":"namespace","name":"image_gen"}]}`,
		`{"input":[{"type":"function_call","namespace":"image_gen","name":"imagegen"}],"tools":[{"type":"namespace","name":"image_gen"}]}`,
	} {
		r = DescribeOpenAIImageRequest(openAIResponsesEndpoint, "gpt-6.1-sol", []byte(history), true)
		_, err = svc.prepareOpenAIWSImageSend(context.Background(), nil, chosen, []byte(history), r, false)
		require.Error(t, err)
	}
}

func TestWSImageSendHeaderLiteSuppressesHostedInjection(t *testing.T) {
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	body := []byte(`{"type":"response.create","model":"gpt-6.1-sol","input":"hello"}`)
	r := DescribeOpenAIImageRequest(openAIResponsesEndpoint, "gpt-6.1-sol", body, true)
	headerDescriptor := r
	headerDescriptor.Lite = true
	ctx := WithOpenAIImageRequestDescriptor(context.Background(), headerDescriptor)
	prepared, err := svc.prepareOpenAIWSImageSend(ctx, nil, imagePolicyAccount(true), body, r, true)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(prepared.body, "tools").Exists())
}

func TestWSImageStandalone403ClassificationAndCooldown(t *testing.T) {
	repo := &wsImagePermissionRepo{account: imagePolicyAccount(true)}
	svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
	for _, body := range []string{
		`{"type":"error","error":{"status":403,"message":"Image generation is not enabled for this group"}}`,
		`{"type":"error","error":{"type":"permission_error","message":"Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","response":{"error":{"status_code":403,"message":"Image generation is not enabled for this group"}}}`,
		`{"type":"response.failed","response":{"error":{"type":"permission_error","message":"Image generation is not enabled for this group"}}}`,
		`{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","response":{"error":{"message":"Image generation is not enabled for this group"}}}`,
	} {
		failure := openAIWSImagePermissionFailure([]byte(body), http.Header{})
		require.NotNil(t, failure)
		require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
		require.False(t, failure.RetryableOnSameAccount)
		require.True(t, svc.handleOpenAIWSFailureAccountSideEffects(context.Background(), repo.account, "gpt-6.1-sol", nil, []byte(body)))
	}
	require.Len(t, repo.cooldownKeys, 6)
	for _, key := range repo.cooldownKeys {
		require.Equal(t, openAIImageGenerationRateLimitKey, key)
	}
	for _, body := range []string{
		`{"type":"error","error":{"status":403,"message":"other image failure"}}`,
		`{"type":"error","error":{"status":403}}`,
		`{"type":"error","error":{"status":502,"message":"Image generation is not enabled for this group"}}`,
		`{"type":"error","status":502,"error":{"message":"Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","status_code":502,"response":{"error":{"status":403,"message":"Image generation is not enabled for this group"}}}`,
		`{"type":"response.failed","response":{"error":{"status_code":502,"message":"Image generation is not enabled for this group"}}}`,
	} {
		require.Nil(t, openAIWSImagePermissionFailure([]byte(body), nil))
	}
}

func TestWSImageCtxPoolRefreshAfterHooksAndRecovery(t *testing.T) {
	for _, mode := range []string{"after_hook", "rejected_field_recovery", "reconnect", "previous_response_recovery"} {
		t.Run(mode, func(t *testing.T) {
			recovery := mode != "after_hook"
			gin.SetMode(gin.TestMode)
			cfg := newOpenAIWSV2TestConfig()
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
			cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
			chosen := imagePolicyAccount(true)
			chosen.Type = AccountTypeAPIKey
			chosen.Status, chosen.Schedulable, chosen.Concurrency = StatusActive, true, 1
			chosen.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
			repo := &wsImagePermissionRepo{account: chosen}
			repo.onRead = func(n int, a *Account) *Account {
				if !recovery || n >= 2 {
					return imagePolicyAccount(false)
				}
				return a
			}
			capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"error","error":{"type":"invalid_request_error","param":"truncation","message":"Unsupported parameter: truncation"}}`)}}
			if mode == "previous_response_recovery" {
				capture.events = [][]byte{[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"Previous response not found"}}`)}
			} else if mode == "reconnect" {
				capture.events = nil
			}
			second := &openAIWSCaptureConn{}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(&openAIWSQueueDialer{conns: []openAIWSClientConn{capture, second}})
			svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
			svc.cfg, svc.openaiWSResolver, svc.openaiWSPool, svc.accountRepo = cfg, NewOpenAIWSProtocolResolver(cfg), pool, repo
			var turns atomic.Int32
			errCh := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				conn, err := coderws.Accept(w, req, nil)
				if err != nil {
					errCh <- err
					return
				}
				defer conn.CloseNow()
				_, first, err := conn.Read(req.Context())
				if err != nil {
					errCh <- err
					return
				}
				c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test")
				errCh <- svc.ProxyResponsesWebSocketFromClient(req.Context(), c, conn, chosen, "test-token", first, &OpenAIWSIngressHooks{BeforeTurn: func(int) error { turns.Add(1); return nil }})
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			body := `{"type":"response.create","model":"gpt-6.1-sol","tools":[{"type":"image_generation"}],"tool_choice":"auto","truncation":"auto","input":[]}`
			if mode == "previous_response_recovery" {
				body = strings.Replace(body, `"input":[]`, `"input":[],"previous_response_id":"resp_test"`, 1)
			}
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(body)))
			select {
			case err = <-errCh:
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
			case <-ctx.Done():
				t.Fatal("WS guard did not finish")
			}
			require.Equal(t, int32(1), turns.Load())
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if recovery {
				require.Len(t, capture.writes, 1)
				require.Equal(t, 2, repo.reads)
			} else {
				require.Empty(t, capture.writes)
			}
			second.mu.Lock()
			defer second.mu.Unlock()
			require.Empty(t, second.writes)
		})
	}
}

func TestWSImagePassthroughFreshPermissionAfterBeforeTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	account := passthroughLifecycleAccount()
	account.UpstreamImagePricing = imagePolicyAccount(true).UpstreamImagePricing
	repo := &wsImagePermissionRepo{account: account}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.accountRepo = repo
	var beforeTurns atomic.Int32
	server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(*gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{BeforeTurn: func(turn int) error {
			beforeTurns.Add(1)
			repo.mu.Lock()
			fresh := *account
			fresh.UpstreamImagePricing = imagePolicyAccount(false).UpstreamImagePricing
			repo.account = &fresh
			repo.mu.Unlock()
			return nil
		}}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_first","model":"gpt-5.1"}}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
	err = client.Write(writeCtx, coderws.MessageBinary, []byte(`{"type":"response.create","tools":[{"type":"image_generation"}],"tool_choice":"none"}`))
	cancelWrite()
	require.NoError(t, err)
	_, err = readPassthroughLifecycleFrame(t, client, time.Second)
	var peerClose coderws.CloseError
	require.ErrorAs(t, err, &peerClose)
	require.Equal(t, coderws.StatusTryAgainLater, peerClose.Code)
	select {
	case err = <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		var failure *UpstreamFailoverError
		require.False(t, errors.As(err, &failure))
	case <-time.After(3 * time.Second):
		t.Fatal("later image request was not checked")
	}
	require.Equal(t, int32(1), beforeTurns.Load())
	select {
	case <-upstream.writes:
		t.Fatal("denied later frame reached upstream")
	default:
	}
}

func TestWSImagePassthrough403AfterOutputDoesNotReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	account := passthroughLifecycleAccount()
	repo := &wsImagePermissionRepo{account: account}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.accountRepo = repo
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"response.output_text.delta","delta":"started"}`)
	_, err := readPassthroughLifecycleFrame(t, client, time.Second)
	require.NoError(t, err)
	upstream.Send(`{"type":"error","error":{"status":403,"message":"Image generation is not enabled for this group"}}`)
	_, err = readPassthroughLifecycleFrame(t, client, time.Second)
	var peerClose coderws.CloseError
	require.ErrorAs(t, err, &peerClose)
	require.Equal(t, coderws.StatusTryAgainLater, peerClose.Code)
	select {
	case err = <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		var failure *UpstreamFailoverError
		require.False(t, errors.As(err, &failure))
	case <-time.After(3 * time.Second):
		t.Fatal("image 403 did not stop relay")
	}
	require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
	select {
	case <-upstream.writes:
		t.Fatal("output was replayed")
	default:
	}
}

func TestWSImagePassthroughStandalone403NoReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	account := passthroughLifecycleAccount()
	repo := &wsImagePermissionRepo{account: account}
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.accountRepo = repo
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	server, serverErr := startPassthroughLifecycleServer(t, ctx, svc, account)
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	upstream.Send(`{"type":"error","error":{"status":403,"message":"Image generation is not enabled for this group"}}`)
	select {
	case err := <-serverErr:
		var failure *UpstreamFailoverError
		require.True(t, errors.As(err, &failure))
		require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
	case <-time.After(3 * time.Second):
		t.Fatal("standalone image 403 did not stop relay")
	}
	require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
	select {
	case <-upstream.writes:
		t.Fatal("403 replayed initial frame")
	default:
	}
}

func newWSImageHTTPForwardTestService(t *testing.T, prewarm bool, events [][]byte) (*OpenAIGatewayService, *Account, *openAIWSCaptureConn, *openAIWSCaptureDialer) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled = prewarm
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 2
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	capture := &openAIWSCaptureConn{events: events}
	dialer := &openAIWSCaptureDialer{conn: capture}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)
	t.Cleanup(pool.Close)
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg, svc.openaiWSResolver, svc.openaiWSPool = cfg, NewOpenAIWSProtocolResolver(cfg), pool
	a := imagePolicyAccount(true)
	a.Type, a.Status, a.Schedulable, a.Concurrency = AccountTypeAPIKey, StatusActive, true, 1
	a.Credentials = map[string]any{"api_key": "test-token"}
	a.Extra = map[string]any{"responses_websockets_v2_enabled": true}
	return svc, a, capture, dialer
}

func TestWSImageHTTPNativeSendRefreshAfterAcquireAndPrewarm(t *testing.T) {
	for _, prewarm := range []bool{false, true} {
		t.Run(map[bool]string{false: "after_acquire", true: "after_prewarm"}[prewarm], func(t *testing.T) {
			svc, a, capture, _ := newWSImageHTTPForwardTestService(t, prewarm, [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_prewarm","usage":{"input_tokens":0,"output_tokens":0}}}`)})
			repo := &wsImagePermissionRepo{account: a}
			repo.onRead = func(n int, latest *Account) *Account {
				if !prewarm || n >= 2 {
					return imagePolicyAccount(false)
				}
				return latest
			}
			svc.accountRepo = repo
			c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test")
			original := []byte(`{"model":"client-model","tools":[{"type":"image_generation"}],"tool_choice":"none","input":[]}`)
			ctx := WithOpenAIImageRequest(context.Background(), openAIResponsesEndpoint, "client-model", original, false)
			// Earlier transforms may remove declarations; the original descriptor
			// remains authoritative at the physical send boundary.
			reqBody := map[string]any{"model": "upstream-model", "stream": false, "input": []any{}}
			before := string(payloadAsJSONBytes(reqBody))
			result, err := svc.forwardOpenAIWSV2(ctx, c, a, reqBody, "", "", "test-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, false, false, "client-model", "upstream-model", time.Now(), 1, "", nil)
			require.Nil(t, result)
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
			require.Equal(t, GatewayFailureReason("image_permission_denied"), failure.Reason)
			require.Equal(t, before, string(payloadAsJSONBytes(reqBody)))
			reason, retryable := classifyOpenAIWSReconnectReason(err)
			require.Empty(t, reason)
			require.False(t, retryable)
			require.False(t, svc.isOpenAIWSFallbackCooling(a.ID))
			capture.mu.Lock()
			defer capture.mu.Unlock()
			if prewarm {
				require.Len(t, capture.writes, 1)
				require.False(t, capture.writes[0]["generate"].(bool))
				require.Equal(t, 2, repo.reads)
			} else {
				require.Empty(t, capture.writes)
			}
		})
	}
}

func TestWSImageHTTPNativeSendAllowedAndPassiveDenied(t *testing.T) {
	for _, supported := range []bool{true, false} {
		t.Run(map[bool]string{true: "allowed_native", false: "denied_passive"}[supported], func(t *testing.T) {
			svc, a, capture, _ := newWSImageHTTPForwardTestService(t, false, [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_ok","model":"upstream-model","usage":{"input_tokens":4,"output_tokens":2}}}`)})
			svc.accountRepo = &wsImagePermissionRepo{account: imagePolicyAccount(supported)}
			body := []byte(`{"model":"upstream-model","stream":false,"seed":9007199254740993,"tools":[{"type":"image_generation","model":"gpt-image-1","size":"1024x1024"}],"tool_choice":"none","input":[]}`)
			if !supported {
				body = []byte(`{"model":"upstream-model","stream":false,"seed":9007199254740993,"tools":[{"type":"namespace","name":"image_gen"}],"input":[]}`)
			}
			var reqBody map[string]any
			require.NoError(t, decodeOpenAIJSONUseNumber(body, &reqBody))
			before := string(payloadAsJSONBytes(reqBody))
			ctx := WithOpenAIImageRequest(context.Background(), openAIResponsesEndpoint, "client-model", body, true)
			c, rec := newOpenAIImageGenerationControlTestContext(true, "unit-test")
			result, err := svc.forwardOpenAIWSV2(ctx, c, a, reqBody, "", "", "test-token", OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2}, true, false, "client-model", "upstream-model", time.Now(), 1, "", nil)
			require.NoError(t, err)
			require.Equal(t, "client-model", result.Model)
			require.Equal(t, "upstream-model", result.UpstreamModel)
			require.Equal(t, 4, result.Usage.InputTokens)
			require.Equal(t, "client-model", gjson.GetBytes(rec.Body.Bytes(), "model").String())
			require.Equal(t, before, string(payloadAsJSONBytes(reqBody)))
			capture.mu.Lock()
			defer capture.mu.Unlock()
			require.Len(t, capture.writes, 1)
			wire := requestToJSONString(capture.writes[0])
			require.Equal(t, "9007199254740993", gjson.Get(wire, "seed").Raw)
			require.Equal(t, "upstream-model", gjson.Get(wire, "model").String())
			if supported {
				require.Equal(t, "image_generation", gjson.Get(wire, "tools.0.type").String())
				require.Equal(t, "gpt-image-1", gjson.Get(wire, "tools.0.model").String())
				require.Equal(t, "none", gjson.Get(wire, "tool_choice").String())
			} else {
				require.False(t, gjson.Get(wire, "tools").Exists())
			}
		})
	}
}

func TestWSImageHTTPNative403BeforeAndAfterOutput(t *testing.T) {
	for _, event := range []string{"error", "response.failed"} {
		for _, output := range []bool{false, true} {
			t.Run(event+map[bool]string{false: "_before_output", true: "_after_output"}[output], func(t *testing.T) {
				failureEvent := `{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`
				if event == "response.failed" {
					failureEvent = `{"type":"response.failed","response":{"id":"resp_test","status":"failed","error":{"message":"Image generation is not enabled for this group"}}}`
				}
				events := [][]byte{[]byte(`{"type":"response.created","response":{"id":"resp_test","model":"gpt-6.1-sol"}}`)}
				if output {
					events = append(events, []byte(`{"type":"response.output_text.delta","delta":"started"}`))
				}
				events = append(events, []byte(failureEvent))
				svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, events)
				repo := &wsImagePermissionRepo{account: a}
				svc.accountRepo = repo
				svc.rateLimitService = &RateLimitService{accountRepo: repo}
				c, rec := newOpenAIImageGenerationControlTestContext(true, "unit-test")
				body := []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[]}`)
				result, err := svc.Forward(context.Background(), c, a, body)
				require.Error(t, err)
				var failure *UpstreamFailoverError
				if output {
					require.NotNil(t, result, "Forward retains billing after semantic output")
					require.False(t, errors.As(err, &failure))
					require.Contains(t, rec.Body.String(), `"type":"response.failed"`)
					require.Contains(t, rec.Body.String(), `"status":"failed"`)
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
				} else {
					require.Nil(t, result)
					require.ErrorAs(t, err, &failure)
					require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
					require.False(t, failure.RetryableOnSameAccount)
					require.False(t, c.Writer.Written())
				}
				reason, retryable := classifyOpenAIWSReconnectReason(err)
				require.Empty(t, reason)
				require.False(t, retryable)
				require.False(t, a.IsOpenAIWSForceHTTPEnabled())
				require.False(t, svc.isOpenAIWSFallbackCooling(a.ID))
				require.Equal(t, 1, dialer.DialCount())
				require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
				capture.mu.Lock()
				defer capture.mu.Unlock()
				require.Len(t, capture.writes, 1)
			})
		}
	}
}

func TestWSImageHTTPNativeExplicit502DoesNotCooldown(t *testing.T) {
	for _, event := range []string{
		`{"type":"error","status":502,"error":{"message":"Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","status_code":502,"response":{"id":"resp_failed","status":"failed","error":{"message":"Image generation is not enabled for this group"}}}`,
	} {
		svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, [][]byte{[]byte(event)})
		repo := &wsImagePermissionRepo{account: a}
		svc.accountRepo = repo
		svc.rateLimitService = &RateLimitService{accountRepo: repo}
		c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test")
		_, err := svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-6.1-sol","stream":false,"input":[]}`))
		var failure *UpstreamFailoverError
		if errors.As(err, &failure) {
			require.False(t, IsOpenAIImagePermissionFailover(failure))
		}
		require.Empty(t, repo.cooldownKeys)
		require.False(t, a.IsOpenAIWSForceHTTPEnabled())
		require.Equal(t, 1, dialer.DialCount())
		capture.mu.Lock()
		require.Len(t, capture.writes, 1)
		capture.mu.Unlock()
	}
}

func TestWSImageHTTPNativePrewarm403StopsBusinessSend(t *testing.T) {
	svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, true, [][]byte{[]byte(`{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`)})
	repo := &wsImagePermissionRepo{account: a}
	svc.accountRepo = repo
	svc.rateLimitService = &RateLimitService{accountRepo: repo}
	c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test")
	_, err := svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-6.1-sol","stream":true,"input":[{"role":"user","content":"hello"}]}`))
	var failure *UpstreamFailoverError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
	require.False(t, c.Writer.Written())
	require.False(t, a.IsOpenAIWSForceHTTPEnabled())
	require.False(t, svc.isOpenAIWSFallbackCooling(a.ID))
	require.Equal(t, 1, dialer.DialCount())
	require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
	capture.mu.Lock()
	defer capture.mu.Unlock()
	require.Len(t, capture.writes, 1)
	require.False(t, capture.writes[0]["generate"].(bool))
}

func TestWSImageHTTPNativeImageOutputThen403PreservesBillingWithoutReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, event := range []string{"error", "response.failed"} {
			t.Run(map[bool]string{false: "nonstream", true: "buffered_stream"}[stream]+"_"+event, func(t *testing.T) {
				failureEvent := `{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`
				if event == "response.failed" {
					failureEvent = `{"type":"response.failed","response":{"id":"resp_image","status":"failed","error":{"message":"Image generation is not enabled for this group"}}}`
				}
				svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, [][]byte{
					[]byte(`{"type":"response.created","response":{"id":"resp_image","model":"upstream-model"}}`),
					[]byte(`{"type":"response.output_item.done","item":{"id":"ig_test","type":"image_generation_call","status":"completed","result":"test-image","size":"1536x1024"},"response":{"id":"resp_image","usage":{"input_tokens":9,"output_tokens":4,"output_tokens_details":{"image_tokens":3}}}}`),
					[]byte(failureEvent),
				})
				a.Credentials["model_mapping"] = map[string]any{"client-model": "upstream-model"}
				repo := &wsImagePermissionRepo{account: a}
				svc.accountRepo = repo
				svc.rateLimitService = &RateLimitService{accountRepo: repo}
				c, rec := newOpenAIImageGenerationControlTestContext(true, "unit-test")
				body := []byte(`{"model":"client-model","stream":false,"input":"draw","tools":[{"type":"image_generation","model":"gpt-image-2","size":"1024x1024"}],"tool_choice":"auto"}`)
				if stream {
					body = []byte(strings.Replace(string(body), `"stream":false`, `"stream":true`, 1))
				}
				before := string(body)
				result, err := svc.Forward(context.Background(), c, a, body)
				var outputErr *openAIWSImageOutputError
				require.ErrorAs(t, err, &outputErr)
				var failoverErr *UpstreamFailoverError
				require.False(t, errors.As(err, &failoverErr), "semantic output must not be replayed even before HTTP commit")
				require.NotNil(t, result)
				require.Equal(t, "client-model", result.Model)
				require.Equal(t, "upstream-model", result.UpstreamModel)
				require.Equal(t, "gpt-image-2", result.BillingModel)
				require.Equal(t, "response.failed", result.UpstreamTerminalEvent)
				require.Equal(t, 9, result.Usage.InputTokens)
				require.Equal(t, 4, result.Usage.OutputTokens)
				require.Equal(t, 3, result.Usage.ImageOutputTokens)
				require.Equal(t, 1, result.ImageCount)
				require.Equal(t, "1K", result.ImageSize)
				require.Equal(t, []string{"1536x1024"}, result.ImageOutputSizes)
				require.Equal(t, before, string(body))
				if stream {
					require.Contains(t, rec.Body.String(), `"type":"response.output_item.done"`)
					require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
				} else {
					require.Equal(t, "failed", gjson.GetBytes(rec.Body.Bytes(), "status").String())
				}
				require.Contains(t, rec.Body.String(), "Image generation is not enabled for this group")
				require.True(t, c.Writer.Written())
				require.Equal(t, 1, dialer.DialCount())
				require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
				require.False(t, a.IsOpenAIWSForceHTTPEnabled())
				require.False(t, svc.isOpenAIWSFallbackCooling(a.ID))
				reason, retryable := classifyOpenAIWSReconnectReason(err)
				require.Empty(t, reason)
				require.False(t, retryable)
				capture.mu.Lock()
				defer capture.mu.Unlock()
				require.Len(t, capture.writes, 1)
				require.Equal(t, "upstream-model", capture.writes[0]["model"])
			})
		}
	}
}

func TestWSImageHTTPNativeOutputItemDoneThen403DoesNotReplay(t *testing.T) {
	items := []struct {
		name string
		item string
	}{
		{"text", `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"completed text"}]}`},
		{"function", `{"type":"function_call","name":"lookup","call_id":"call_test","arguments":"{}"}`},
	}
	for _, item := range items {
		for _, stream := range []bool{false, true} {
			for _, event := range []string{"error", "response.failed"} {
				t.Run(item.name+map[bool]string{false: "_nonstream_", true: "_buffered_stream_"}[stream]+event, func(t *testing.T) {
					failureEvent := `{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`
					if event == "response.failed" {
						failureEvent = `{"type":"response.failed","response":{"id":"resp_done","status":"failed","error":{"message":"Image generation is not enabled for this group"}}}`
					}
					svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, [][]byte{
						[]byte(`{"type":"response.created","response":{"id":"resp_done","model":"gpt-6.1-sol"}}`),
						[]byte(`{"type":"response.output_item.done","item":` + item.item + `,"response":{"id":"resp_done","usage":{"input_tokens":9,"output_tokens":4}}}`),
						[]byte(failureEvent),
					})
					repo := &wsImagePermissionRepo{account: a}
					svc.accountRepo = repo
					svc.rateLimitService = &RateLimitService{accountRepo: repo}
					c, rec := newOpenAIImageGenerationControlTestContext(true, "unit-test")
					body := []byte(`{"model":"gpt-6.1-sol","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `,"input":[]}`)
					before := string(body)
					result, err := svc.Forward(context.Background(), c, a, body)
					var outputErr *openAIWSImageOutputError
					require.ErrorAs(t, err, &outputErr)
					var failure *UpstreamFailoverError
					require.False(t, errors.As(err, &failure), "completed semantic item must not be replayed before HTTP commit")
					require.NotNil(t, result)
					require.Equal(t, "gpt-6.1-sol", result.BillingModel)
					require.Equal(t, 9, result.Usage.InputTokens)
					require.Equal(t, 4, result.Usage.OutputTokens)
					require.Zero(t, result.ImageCount)
					require.Equal(t, "response.failed", result.UpstreamTerminalEvent)
					require.Equal(t, before, string(body))
					if stream {
						require.Contains(t, rec.Body.String(), `"type":"response.output_item.done"`)
						require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
					} else {
						require.Equal(t, "failed", gjson.GetBytes(rec.Body.Bytes(), "status").String())
					}
					require.Contains(t, rec.Body.String(), "Image generation is not enabled for this group")
					require.True(t, c.Writer.Written())
					require.Equal(t, 1, dialer.DialCount())
					require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
					require.False(t, a.IsOpenAIWSForceHTTPEnabled())
					require.False(t, svc.isOpenAIWSFallbackCooling(a.ID))
					reason, retryable := classifyOpenAIWSReconnectReason(err)
					require.Empty(t, reason)
					require.False(t, retryable)
					capture.mu.Lock()
					defer capture.mu.Unlock()
					require.Len(t, capture.writes, 1)
				})
			}
		}
	}
}

func TestWSImageHTTPNativeOutputFailurePreservesUsageWithoutReplay(t *testing.T) {
	items := []struct {
		name string
		item string
	}{
		{"text", `{"type":"message","content":[{"type":"output_text","text":"completed text"}]}`},
		{"function", `{"type":"function_call","name":"lookup","call_id":"call_test","arguments":"{}"}`},
		{"image", `{"id":"ig_test","type":"image_generation_call","status":"completed","result":"test-image","size":"1536x1024"}`},
	}
	for _, item := range items {
		for _, stream := range []bool{false, true} {
			for _, failure := range []string{"disconnect", "timeout", "error", "invalid_json"} {
				t.Run(item.name+map[bool]string{false: "_nonstream_", true: "_buffered_stream_"}[stream]+failure, func(t *testing.T) {
					events := [][]byte{
						[]byte(`{"type":"response.created","response":{"id":"resp_partial","model":"gpt-6.1-sol"}}`),
						[]byte(`{"type":"response.output_item.done","item":` + item.item + `,"response":{"id":"resp_partial","usage":{"input_tokens":9,"output_tokens":4}}}`),
					}
					switch failure {
					case "timeout":
						events = append(events, []byte(`{"type":"response.in_progress"}`))
					case "error":
						events = append(events, []byte(`{"type":"error","error":{"type":"server_error","code":"server_error","message":"Temporary upstream error"}}`))
					case "invalid_json":
						events = append(events, []byte(`{"type":"response.in_progress"`))
					}
					svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, events)
					if failure == "timeout" {
						capture.readDelays = []time.Duration{0, 0, 3 * time.Second}
					}
					repo := &wsImagePermissionRepo{account: a}
					svc.accountRepo = repo
					svc.rateLimitService = &RateLimitService{accountRepo: repo}
					c, rec := newOpenAIImageGenerationControlTestContext(true, "unit-test")
					body := []byte(`{"model":"gpt-6.1-sol","stream":` + map[bool]string{false: "false", true: "true"}[stream] + `,"input":[]}`)
					result, err := svc.Forward(context.Background(), c, a, body)
					require.Error(t, err)
					var fallback *openAIWSFallbackError
					var accountFailure *UpstreamFailoverError
					require.False(t, errors.As(err, &fallback))
					require.False(t, errors.As(err, &accountFailure))
					require.NotNil(t, result)
					require.Equal(t, 9, result.Usage.InputTokens)
					require.Equal(t, 4, result.Usage.OutputTokens)
					require.Equal(t, "response.failed", result.UpstreamTerminalEvent)
					if item.name == "image" {
						require.Equal(t, 1, result.ImageCount)
						require.Equal(t, []string{"1536x1024"}, result.ImageOutputSizes)
					} else {
						require.Zero(t, result.ImageCount)
					}
					if stream {
						require.Equal(t, 1, strings.Count(rec.Body.String(), `"type":"response.failed"`))
					} else {
						require.Equal(t, "failed", gjson.GetBytes(rec.Body.Bytes(), "status").String())
					}
					require.Empty(t, repo.cooldownKeys, "ordinary failures must not become image permission cooldown")
					require.Equal(t, 1, dialer.DialCount())
					capture.mu.Lock()
					defer capture.mu.Unlock()
					require.Len(t, capture.writes, 1)
				})
			}
		}
	}
}

func TestWSImageHTTPNativeEmptyOutputItemDoesNotCommit(t *testing.T) {
	items := []struct {
		name string
		item string
	}{
		{"empty", `{}`},
		{"unknown_json", `{"type":"unknown","arguments":"{}","content":[{"text":"not output"}]}`},
		{"empty_text", `{"type":"message","content":[{"type":"output_text","text":""}]}`},
		{"whitespace_text", `{"type":"message","content":[{"type":"output_text","text":"   "}]}`},
		{"untyped_text", `{"type":"message","content":[{"text":"not output"}]}`},
		{"missing_function_name", `{"type":"function_call","call_id":"call_test","arguments":"{}"}`},
		{"missing_function_call_id", `{"type":"function_call","name":"lookup","arguments":"{}"}`},
		{"nonstring_arguments", `{"type":"function_call","name":"lookup","call_id":"call_test","arguments":{}}`},
	}
	for _, item := range items {
		for _, stream := range []bool{false, true} {
			t.Run(item.name+map[bool]string{false: "_nonstream", true: "_buffered_stream"}[stream], func(t *testing.T) {
				svc, a, capture, dialer := newWSImageHTTPForwardTestService(t, false, [][]byte{
					[]byte(`{"type":"response.created","response":{"id":"resp_empty","model":"gpt-6.1-sol"}}`),
					[]byte(`{"type":"response.output_item.done","item":` + item.item + `}`),
					[]byte(`{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`),
				})
				repo := &wsImagePermissionRepo{account: a}
				svc.accountRepo = repo
				svc.rateLimitService = &RateLimitService{accountRepo: repo}
				c, _ := newOpenAIImageGenerationControlTestContext(true, "unit-test")
				result, err := svc.Forward(context.Background(), c, a, []byte(`{"model":"gpt-6.1-sol","stream":`+map[bool]string{false: "false", true: "true"}[stream]+`,"input":[]}`))
				var failure *UpstreamFailoverError
				require.ErrorAs(t, err, &failure)
				require.Equal(t, GatewayFailureScopeAccountCapability, failure.Scope)
				require.False(t, failure.RetryableOnSameAccount)
				require.Nil(t, result)
				require.False(t, c.Writer.Written(), "empty or malformed items remain buffered")
				require.Equal(t, 1, dialer.DialCount())
				require.Equal(t, []string{openAIImageGenerationRateLimitKey}, repo.cooldownKeys)
				capture.mu.Lock()
				defer capture.mu.Unlock()
				require.Len(t, capture.writes, 1)
			})
		}
	}
}
