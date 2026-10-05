package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIImagePolicyWSPerTurnDenial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.CodexImageGenerationBridgeEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"resp_text","model":"gpt-6.1-sol","usage":{"input_tokens":1,"output_tokens":1}}}`)}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: capture})
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg = cfg
	svc.openaiWSResolver = NewOpenAIWSProtocolResolver(cfg)
	svc.openaiWSPool = pool
	a := imagePolicyAccount(false)
	a.Status, a.Schedulable, a.Concurrency = StatusActive, true, 1
	a.Extra = map[string]any{"openai_oauth_responses_websockets_v2_enabled": true}
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			errCh <- err
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		_, first, err := conn.Read(ctx)
		if err != nil {
			errCh <- err
			return
		}
		original := string(first)
		c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.98.0")
		c.Request = r.Clone(ctx)
		c.Request.Header.Set("User-Agent", "codex_cli_rs/0.98.0")
		err = svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, a, "test-token", first, nil)
		if string(first) != original {
			t.Error("account transform changed the canonical WS frame")
		}
		errCh <- err
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer conn.CloseNow()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6.1-sol","input":"hello","tools":[{"type":"namespace","name":"image_gen","tools":[{"type":"function","name":"imagegen","parameters":{"type":"object"}}]}]}`)))
	_, reply, err := conn.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "resp_text", gjson.GetBytes(reply, "response.id").String())
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","input":"draw","tools":[{"type":"image_generation"}],"tool_choice":"none"}`)))
	select {
	case err = <-errCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusTryAgainLater, closeErr.statusCode)
		var failure *UpstreamFailoverError
		require.False(t, errors.As(err, &failure), "later turns must not trigger replay of the initial frame")
	case <-ctx.Done():
		t.Fatal("WS permission check did not complete")
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	require.Len(t, capture.writes, 1)
	require.NotContains(t, requestToJSONString(capture.writes[0]), "image_gen")
}
