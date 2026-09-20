package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsPluginMetadataUpstream struct {
	HTTPUpstream
	requests chan wsPluginMetadataRequest
}

type wsPluginMetadataRequest struct {
	metadata  pluginRequestMetadata
	bodyModel string
}

func (u *wsPluginMetadataUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	defer req.Body.Close()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	bodyModel := gjson.GetBytes(body, "model").String()
	metadata, _ := req.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	u.requests <- wsPluginMetadataRequest{metadata: metadata, bodyModel: bodyModel}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bridge\",\"model\":%q,\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n", bodyModel))),
	}, nil
}

func TestOpenAIPluginHTTPBridgeCarriesModelMetadataEveryTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	account := passthroughLifecycleAccount()
	account.Type = AccountTypeOAuth
	account.Credentials["access_token"] = "test-token"
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
	upstream := &wsPluginMetadataUpstream{requests: make(chan wsPluginMetadataRequest, 2)}
	cfg := passthroughLifecycleConfig()
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.MaxLineSize = defaultMaxLineSize
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		pluginManager: newUnavailableScopedGatewayPlugin(account.ID, "managed-model"),
	}
	// Managing any model requires the HTTP bridge for the entire account.
	// No native WS pool/dialer is installed in this service.
	server, serverErr := startPassthroughHookRecordingServer(t, ctx, svc, account, nil)
	defer server.Close()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	for _, tt := range []struct{ requestedModel, outboundModel string }{
		{"gpt-5.1", "gpt-5.4"},
		{"gpt-5.2", "gpt-5.2"},
	} {
		require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":"ping"}`, tt.requestedModel))))
		_, payload, readErr := client.Read(ctx)
		require.NoError(t, readErr)
		require.Equal(t, "response.completed", gjson.GetBytes(payload, "type").String())
		request := <-upstream.requests
		require.Equal(t, tt.outboundModel, request.bodyModel)
		require.Equal(t, request.bodyModel, request.metadata.Model)
		require.Equal(t, PluginAccountIdentityRevision(account), request.metadata.IdentityRevision)
	}
	require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
	select {
	case proxyErr := <-serverErr:
		require.NoError(t, proxyErr)
	case <-ctx.Done():
		t.Fatal("HTTP bridge did not close")
	}
}

func TestOpenAIPluginManagedAccountRequiresHTTPBridge(t *testing.T) {
	account := &Account{ID: 41, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	svc := &OpenAIGatewayService{pluginManager: newUnavailableScopedGatewayPlugin(account.ID, "managed-model")}
	require.True(t, svc.shouldBridgeOpenAIPluginAccount(account))
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, svc.checkOpenAIPluginNativeTurn(account), &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.statusCode)
	account.ID++
	require.False(t, svc.shouldBridgeOpenAIPluginAccount(account))
	require.NoError(t, svc.checkOpenAIPluginNativeTurn(account))
	account.ID--
	account.Type = AccountTypeAPIKey
	require.False(t, svc.shouldBridgeOpenAIPluginAccount(account))
	svc.pluginManager = nil
	require.False(t, svc.shouldBridgeOpenAIPluginAccount(account))
}

func TestOpenAINativePassthroughRejectsTurnAfterPluginScopeAdded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	upstream := newStagedPassthroughConn()
	upstream.Send(`{"type":"response.completed","response":{"id":"resp_before_scope","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`)
	account := passthroughLifecycleAccount()
	account.Type = AccountTypeOAuth
	account.Credentials["access_token"] = "test-token"
	account.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModePassthrough
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.cfg.Gateway.OpenAIWS.OAuthEnabled = true
	manager := newUnavailableScopedGatewayPlugin(account.ID, "managed-model")
	repo := manager.repo.(*unavailableScopedGatewayPluginRepo)
	repo.setBindingEnabled(false)
	manager.route.Store(nil)
	svc.pluginManager = manager
	server, serverErr := startPassthroughHookRecordingServer(t, ctx, svc, account, nil)
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer func() { _ = client.CloseNow() }()
	requirePassthroughUpstreamWrite(t, upstream, time.Second)
	_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	repo.setBindingEnabled(true)
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1"}`)))
	select {
	case proxyErr := <-serverErr:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, proxyErr, &closeErr)
		require.Equal(t, coderws.StatusTryAgainLater, closeErr.statusCode)
	case <-ctx.Done():
		t.Fatal("native connection did not request reconnect")
	}
	require.Empty(t, upstream.writes, "a newly managed account must reject the next native turn, even for an unmanaged model")
}
