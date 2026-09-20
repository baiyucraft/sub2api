//go:build unit

package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type pluginAdmissionHandlerAttempt struct {
	accountID int64
	model     string
	path      string
	body      string
	forced    bool
}

type pluginAdmissionHandlerUpstream struct {
	service.HTTPUpstream
	mu             sync.Mutex
	attempts       []pluginAdmissionHandlerAttempt
	reject         func(pluginAdmissionHandlerAttempt) bool
	onReject       func()
	failIDs        map[int64]bool
	streamError    bool
	transportError func(pluginAdmissionHandlerAttempt) error
	respond        func(pluginAdmissionHandlerAttempt) *http.Response
}

func (u *pluginAdmissionHandlerUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	attempt := pluginAdmissionHandlerAttempt{
		accountID: accountID,
		model:     gjson.GetBytes(body, "model").String(),
		path:      req.URL.Path,
		body:      string(body),
		forced:    service.IsForceCacheBilling(req.Context()),
	}
	u.mu.Lock()
	u.attempts = append(u.attempts, attempt)
	u.mu.Unlock()
	if u.reject != nil && u.reject(attempt) {
		if u.onReject != nil {
			u.onReject()
		}
		return nil, fmt.Errorf("outbound admission: %w", &service.PluginAdmissionError{AccountID: accountID, Model: attempt.model})
	}
	if u.failIDs[accountID] {
		return &http.Response{
			StatusCode: http.StatusBadGateway,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"upstream unavailable"}}`)),
		}, nil
	}
	if u.transportError != nil {
		if err := u.transportError(attempt); err != nil {
			return nil, err
		}
	}
	if u.respond != nil {
		return u.respond(attempt), nil
	}
	if u.streamError {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Body: io.NopCloser(io.MultiReader(
				strings.NewReader("data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n"),
				pluginAdmissionHandlerErrorReader{err: &service.PluginAdmissionError{AccountID: accountID, Model: attempt.model}},
			)),
		}, nil
	}
	response := fmt.Sprintf(`{"id":"resp_plugin_admission_%d","object":"response","status":"completed","model":%q,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`, accountID, attempt.model)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\n")),
	}, nil
}

type pluginAdmissionHandlerErrorReader struct{ err error }

func (r pluginAdmissionHandlerErrorReader) Read([]byte) (int, error) { return 0, r.err }

func (u *pluginAdmissionHandlerUpstream) snapshot() []pluginAdmissionHandlerAttempt {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]pluginAdmissionHandlerAttempt(nil), u.attempts...)
}

func pluginAdmissionHandlerAccounts() []service.Account {
	accounts := make([]service.Account, 3)
	for i := range accounts {
		accounts[i] = service.Account{
			ID: int64(i + 1), Name: fmt.Sprintf("admission-%d", i+1),
			Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: i,
			Credentials: map[string]any{"access_token": "test-access-token"},
			Extra: map[string]any{
				"openai_oauth_responses_websockets_v2_enabled": true,
				"openai_oauth_responses_websockets_v2_mode":    service.OpenAIWSIngressModeHTTPBridge,
			},
		}
	}
	return accounts
}

type pluginAdmissionHandlerRepo struct {
	openAIImagesFailoverAccountRepo
}

func (r pluginAdmissionHandlerRepo) ListModelAvailabilityCandidates(_ context.Context, _ *int64, platforms []string, _ bool) ([]service.Account, error) {
	var accounts []service.Account
	for _, platform := range platforms {
		accounts = append(accounts, r.accountsForPlatform(platform)...)
	}
	return accounts, nil
}

func newPluginAdmissionHandler(t *testing.T, upstream service.HTTPUpstream, accounts []service.Account, budget int) *OpenAIGatewayHandler {
	t.Helper()
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(
		pluginAdmissionHandlerRepo{openAIImagesFailoverAccountRepo{accounts: accounts}}, nil, nil, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, billing, upstream, &service.DeferredService{},
		nil, nil, nil, nil, nil, nil, nil,
	)
	cache := &concurrencyCacheMock{
		acquireUserSlotFn:    func(context.Context, int64, int, string) (bool, error) { return true, nil },
		acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) { return true, nil },
	}
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(cache), billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = budget
	return h
}

func setPluginAdmissionHandlerAuth(c *gin.Context) {
	groupID := int64(7301)
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{
		ID: 7302, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, AllowMessagesDispatch: true},
		User:  &service.User{ID: 7303, Status: service.StatusActive},
	})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 7303, Concurrency: 1})
}

func requirePluginAdmissionAttempts(t *testing.T, upstream *pluginAdmissionHandlerUpstream, ids ...int64) {
	t.Helper()
	attempts := upstream.snapshot()
	actual := make([]int64, 0, len(attempts))
	for _, attempt := range attempts {
		actual = append(actual, attempt.accountID)
		require.False(t, attempt.forced, "admission rejection must not force cache billing")
	}
	require.Equal(t, ids, actual)
}

func TestPluginAdmissionHTTPHandlerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	endpoints := []struct {
		name string
		path string
		body string
		run  func(*OpenAIGatewayHandler, *gin.Context)
	}{
		{"responses", "/v1/responses", `{"model":"gpt-5.1","input":"hello","stream":false}`, (*OpenAIGatewayHandler).Responses},
		{"messages", "/v1/messages", `{"model":"gpt-5.1","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`, (*OpenAIGatewayHandler).Messages},
		{"chat", "/v1/chat/completions", `{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false}`, (*OpenAIGatewayHandler).ChatCompletions},
	}
	for _, endpoint := range endpoints {
		for _, scenario := range []struct {
			name         string
			budget       int
			rejectAll    bool
			writeOutput  bool
			failSecond   bool
			wantIDs      []int64
			wantStatus   int
			wantSwitches int
		}{
			{name: "zero budget still selects healthy", wantIDs: []int64{1, 2}, wantStatus: 200},
			{name: "all rejected uses no-account response", rejectAll: true, wantIDs: []int64{1, 2, 3}, wantStatus: 503},
			{name: "rejection preserves upstream failover budget", budget: 1, failSecond: true, wantIDs: []int64{1, 2, 3}, wantStatus: 200, wantSwitches: 1},
			{name: "real upstream failure still obeys zero budget", failSecond: true, wantIDs: []int64{1, 2}, wantStatus: 502},
			{name: "output prohibits replay", writeOutput: true, wantIDs: []int64{1}, wantStatus: 200},
		} {
			t.Run(endpoint.name+"/"+scenario.name, func(t *testing.T) {
				upstream := &pluginAdmissionHandlerUpstream{
					reject:  func(a pluginAdmissionHandlerAttempt) bool { return scenario.rejectAll || a.accountID == 1 },
					failIDs: map[int64]bool{2: scenario.failSecond},
				}
				h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts(), scenario.budget)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodPost, endpoint.path, bytes.NewBufferString(endpoint.body))
				switchCount := 0
				c.Request = c.Request.WithContext(service.WithMonitorSwitchReporter(c.Request.Context(), func(count int) { switchCount = count }))
				c.Request.Header.Set("Content-Type", "application/json")
				setPluginAdmissionHandlerAuth(c)
				if scenario.writeOutput {
					upstream.onReject = func() {
						c.Header("Content-Type", "text/event-stream")
						_, _ = c.Writer.WriteString("data: {\"type\":\"response.output_text.delta\",\"delta\":\"visible\"}\n\n")
					}
				}

				endpoint.run(h, c)

				require.Equal(t, scenario.wantStatus, recorder.Code, recorder.Body.String())
				requirePluginAdmissionAttempts(t, upstream, scenario.wantIDs...)
				require.Equal(t, scenario.wantSwitches, switchCount)
				require.False(t, service.IsForceCacheBilling(c.Request.Context()))
				if scenario.rejectAll {
					require.Equal(t, "api_error", gjson.GetBytes(recorder.Body.Bytes(), "error.type").String())
					require.Equal(t, "Service temporarily unavailable", gjson.GetBytes(recorder.Body.Bytes(), "error.message").String())
					_, recorded := c.Get(service.OpsUpstreamErrorsKey)
					require.False(t, recorded, "pre-send rejection is not an upstream failure")
				}
			})
		}
	}
}

func newPluginAdmissionWSClient(t *testing.T, h *OpenAIGatewayHandler) (*coderws.Conn, <-chan struct{}) {
	t.Helper()
	done := make(chan struct{})
	router := gin.New()
	router.Use(func(c *gin.Context) { setPluginAdmissionHandlerAuth(c) })
	router.GET("/v1/responses", func(c *gin.Context) {
		defer close(done)
		h.ResponsesWebSocket(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn, done
}

func pluginAdmissionWSTurn(t *testing.T, conn *coderws.Conn, payload string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
	_, response, err := conn.Read(ctx)
	return response, err
}

func waitPluginAdmissionWSHandler(t *testing.T, conn *coderws.Conn, done <-chan struct{}) {
	t.Helper()
	_ = conn.Close(coderws.StatusNormalClosure, "done")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("websocket handler did not finish")
	}
}

func TestPluginAdmissionWSHandlerFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("all_rejected=%t", all), func(t *testing.T) {
			upstream := &pluginAdmissionHandlerUpstream{reject: func(a pluginAdmissionHandlerAttempt) bool { return all || a.accountID == 1 }}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts(), 0)
			conn, done := newPluginAdmissionWSClient(t, h)
			response, err := pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.1","input":"hello"}`)
			if all {
				var closeErr coderws.CloseError
				require.ErrorAs(t, err, &closeErr)
				require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
				require.Equal(t, "no available account", closeErr.Reason)
				requirePluginAdmissionAttempts(t, upstream, 1, 2, 3)
			} else {
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
				requirePluginAdmissionAttempts(t, upstream, 1, 2)
			}
			waitPluginAdmissionWSHandler(t, conn, done)
			metrics := h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics()
			require.Zero(t, metrics.AccountSwitchTotal)
			if all {
				require.Zero(t, metrics.RuntimeStatsAccountCount)
			}
		})
	}
}

func TestPluginAdmissionWSHandlerCurrentTurnModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	accounts := pluginAdmissionHandlerAccounts()
	accounts[0].Credentials["model_mapping"] = map[string]any{"gpt-5.1": "gpt-5.1", "gpt-5.2": "gpt-5.2"}
	accounts[1].Credentials["model_mapping"] = map[string]any{"gpt-5.1": "gpt-5.1"}
	accounts[2].Credentials["model_mapping"] = map[string]any{"gpt-5.2": "gpt-5.2"}
	upstream := &pluginAdmissionHandlerUpstream{reject: func(a pluginAdmissionHandlerAttempt) bool {
		return a.accountID == 1 && a.model == "gpt-5.2"
	}}
	h := newPluginAdmissionHandler(t, upstream, accounts, 0)
	conn, done := newPluginAdmissionWSClient(t, h)
	response, err := pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.1","input":[{"role":"user","content":"first turn"}]}`)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
	response, err = pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.2","input":[{"role":"user","content":"current turn"}]}`)
	require.NoError(t, err)
	require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String(), string(response))
	require.Equal(t, "gpt-5.2", gjson.GetBytes(response, "response.model").String())
	waitPluginAdmissionWSHandler(t, conn, done)
	requirePluginAdmissionAttempts(t, upstream, 1, 1, 3)
	attempts := upstream.snapshot()
	require.Contains(t, attempts[2].body, "current turn")
	require.Equal(t, "gpt-5.2", attempts[2].model)
	require.Zero(t, h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics().AccountSwitchTotal)
}

func TestPluginAdmissionHandlerNoReplayAfterOutput(t *testing.T) {
	c, _ := newOpenAIResponsesFailoverTestContext(t, nil)
	before := service.OpenAICompactKeepaliveAdjustedWrittenSize(c)
	err := &service.UpstreamFailoverError{PluginAdmissionRejected: true, SafeToFailoverAfterWrite: true}
	require.True(t, openAIForwardMayFailover(c, before, err))
	_, _ = c.Writer.WriteString("data: {\"delta\":\"visible\"}\n\n")
	require.False(t, openAIForwardMayFailover(c, before, err), "generic retry hint cannot override semantic output")
}

func TestPluginAdmissionWSHandlerOrdinaryLaterTurnErrorDoesNotReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, requestSent := range []bool{false, true} {
		t.Run(fmt.Sprintf("request_sent=%t", requestSent), func(t *testing.T) {
			upstream := &pluginAdmissionHandlerUpstream{transportError: func(a pluginAdmissionHandlerAttempt) error {
				if !strings.Contains(a.body, "current turn") {
					return nil
				}
				if requestSent {
					return &service.PluginTransportError{Code: "plugin_admission_unavailable", Message: "already sent", RequestSent: true}
				}
				return errors.New("upstream connection failed")
			}}
			h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts(), 3)
			conn, done := newPluginAdmissionWSClient(t, h)
			response, err := pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.2","input":"first turn"}`)
			require.NoError(t, err)
			require.Equal(t, "response.completed", gjson.GetBytes(response, "type").String())
			response, err = pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.2","input":"current turn"}`)
			require.NoError(t, err)
			require.Equal(t, "error", gjson.GetBytes(response, "type").String(), string(response))
			require.Equal(t, int64(502), gjson.GetBytes(response, "status").Int())
			waitPluginAdmissionWSHandler(t, conn, done)
			requirePluginAdmissionAttempts(t, upstream, 1, 1)
		})
	}
}

func TestPluginAdmissionWSHandlerNoReplayAfterOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &pluginAdmissionHandlerUpstream{streamError: true}
	h := newPluginAdmissionHandler(t, upstream, pluginAdmissionHandlerAccounts(), 3)
	conn, done := newPluginAdmissionWSClient(t, h)
	response, err := pluginAdmissionWSTurn(t, conn, `{"type":"response.create","model":"gpt-5.1","input":"hello"}`)
	require.NoError(t, err)
	require.Equal(t, "visible", gjson.GetBytes(response, "delta").String())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = conn.Read(ctx)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusInternalError, closeErr.Code)
	waitPluginAdmissionWSHandler(t, conn, done)
	requirePluginAdmissionAttempts(t, upstream, 1)
	require.Zero(t, h.gatewayService.SnapshotOpenAIAccountSchedulerMetrics().AccountSwitchTotal)
}

func TestPluginAdmissionHandlerWSHealthAttribution(t *testing.T) {
	for _, err := range []error{
		&service.PluginAdmissionError{AccountID: 1, Model: "gpt-5.1"},
		&service.UpstreamFailoverError{PluginAdmissionRejected: true, StatusCode: http.StatusServiceUnavailable},
		service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "Account plugin policy changed; reconnect to use HTTP bridge", nil),
	} {
		require.False(t, shouldReportOpenAIWSProxyAccountFailure(fmt.Errorf("turn rejected: %w", err)))
	}
	require.True(t, shouldReportOpenAIWSProxyAccountFailure(errors.New("upstream connection failed")))
	require.True(t, shouldReportOpenAIWSProxyAccountFailure(service.NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "upstream unavailable", nil)))
}

func TestPluginAdmissionHandlerFailoverStatePreservesBilling(t *testing.T) {
	state := NewFailoverState(0, true)
	unscheduler := &mockTempUnscheduler{}
	err := &service.UpstreamFailoverError{
		PluginAdmissionRejected: true, StatusCode: http.StatusServiceUnavailable,
		RetryableOnSameAccount: true, ForceCacheBilling: true,
	}
	for _, id := range []int64{1, 2} {
		require.Equal(t, FailoverContinue, state.HandleFailoverError(context.Background(), unscheduler, id, service.PlatformOpenAI, 3, err))
	}
	require.Len(t, state.FailedAccountIDs, 2)
	require.Empty(t, state.SameAccountRetryCount)
	require.Zero(t, state.SwitchCount)
	require.False(t, state.ForceCacheBilling)
	require.Empty(t, unscheduler.calls)
	require.Equal(t, FailoverExhausted, state.HandleSelectionExhausted(context.Background()))
}
