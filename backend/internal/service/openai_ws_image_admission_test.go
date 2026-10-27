package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestWSImageAdmissionUsesInheritedSessionAndReleasesAfterTurn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		choice    string
		strip     bool
		wantImage bool
	}{
		{"retained_native", `"auto"`, false, true},
		{"stripped_optional_native", `"auto"`, true, false},
		{"explicit_inherited_choice", `{"type":"image_generation"}`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			upstream := newStagedPassthroughConn()
			account := passthroughLifecycleAccount()
			account.UpstreamImagePricing = imagePolicyAccount(true).UpstreamImagePricing
			svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
			var sends, acquisitions, releases atomic.Int32
			var held atomic.Bool
			completed := make(chan struct{}, 3)
			server, serverErr := startPassthroughLifecycleServerWithHooks(t, ctx, svc, account, func(c *gin.Context) *OpenAIWSIngressHooks {
				c.Set("api_key", &APIKey{Group: &Group{AllowImageGeneration: true, StripOptionalImageTools: tc.strip}})
				return &OpenAIWSIngressHooks{
					BeforeImageSend: func(r OpenAIImageRequest) error {
						sends.Add(1)
						if r.RequiresCapability() && held.CompareAndSwap(false, true) {
							acquisitions.Add(1)
						}
						return nil
					},
					AfterTurn: func(_ int, _ *OpenAIForwardResult, _ error) {
						if held.Swap(false) {
							releases.Add(1)
						}
						completed <- struct{}{}
					},
				}
			})
			defer server.Close()
			client := dialPassthroughLifecycleClient(t, server)
			defer client.CloseNow()
			finish := func(id string) {
				upstream.Send(`{"type":"response.completed","response":{"id":"` + id + `","model":"gpt-5.1"}}`)
				_, err := readPassthroughLifecycleFrame(t, client, time.Second)
				require.NoError(t, err)
				select {
				case <-completed:
				case <-time.After(time.Second):
					t.Fatal("AfterTurn did not release image admission")
				}
			}
			write := func(body string) []byte {
				writeCtx, cancelWrite := context.WithTimeout(context.Background(), time.Second)
				defer cancelWrite()
				require.NoError(t, client.Write(writeCtx, coderws.MessageText, []byte(body)))
				return requirePassthroughUpstreamWrite(t, upstream, time.Second)
			}
			requirePassthroughUpstreamWrite(t, upstream, time.Second)
			finish("resp_first")
			require.Zero(t, acquisitions.Load(), "ordinary first turn must not acquire image admission")
			sessionFrame := write(`{"type":"session.update","session":{"tools":[{"type":"image_generation"}],"tool_choice":` + tc.choice + `}}`)
			require.Equal(t, int32(1), sends.Load(), "session.update must not acquire a turn slot")
			responseFrame := write(`{"type":"response.create","input":[]}`)
			wantTools := 0
			if tc.wantImage {
				wantTools = 1
			}
			require.Len(t, gjson.GetBytes(sessionFrame, "session.tools").Array(), wantTools)
			// Unchanged fields may be omitted from response.create because the
			// upstream inherits their values from the physical session.update.
			effectiveTools := gjson.GetBytes(responseFrame, "tools")
			if !effectiveTools.Exists() {
				effectiveTools = gjson.GetBytes(sessionFrame, "session.tools")
			}
			require.Len(t, effectiveTools.Array(), wantTools, "physical frames must establish the same inherited policy seen by admission")
			if tc.wantImage {
				effectiveChoice := gjson.GetBytes(responseFrame, "tool_choice")
				if !effectiveChoice.Exists() {
					effectiveChoice = gjson.GetBytes(sessionFrame, "session.tool_choice")
				}
				require.JSONEq(t, tc.choice, effectiveChoice.Raw, "upstream must retain the inherited tool choice")
			}
			require.Equal(t, tc.wantImage, held.Load(), "admission must see inherited declarations and choices after cleanup")
			finish("resp_second")
			require.False(t, held.Load())
			want := int32(0)
			if tc.wantImage {
				want = 1
			}
			require.Equal(t, want, acquisitions.Load())
			require.Equal(t, want, releases.Load())
			clearedSessionFrame := write(`{"type":"session.update","session":{"tools":[],"tool_choice":"auto"}}`)
			require.True(t, gjson.GetBytes(clearedSessionFrame, "session.tools").IsArray(), "physical session update must explicitly clear tools")
			require.Empty(t, gjson.GetBytes(clearedSessionFrame, "session.tools").Array())
			clearedResponseFrame := write(`{"type":"response.create","input":[]}`)
			require.Empty(t, gjson.GetBytes(clearedResponseFrame, "tools").Array(), "next physical response must retain the cleared session")
			require.False(t, held.Load(), "clearing session tools must restore ordinary admission")
			finish("resp_third")
			require.NoError(t, client.CloseNow())
			// The relay drains upstream for 1200ms after a client disconnect.
			// Finish the fake peer explicitly instead of waiting for that timer.
			upstream.Fail(io.EOF)
			select {
			case err := <-serverErr:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("passthrough did not exit")
			}
		})
	}
}

func TestWSImageAdmissionRechecksNativeRecoveryBeforeWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	chosen := imagePolicyAccount(true)
	chosen.Type = AccountTypeAPIKey
	chosen.Status, chosen.Schedulable, chosen.Concurrency = StatusActive, true, 1
	chosen.Extra = map[string]any{"openai_apikey_responses_websockets_v2_enabled": true}
	capture := &openAIWSCaptureConn{events: [][]byte{[]byte(`{"type":"error","error":{"type":"invalid_request_error","param":"truncation","message":"Unsupported parameter: truncation"}}`)}}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSQueueDialer{conns: []openAIWSClientConn{capture}})
	svc := newOpenAIImageGenerationControlTestService(&httpUpstreamRecorder{})
	svc.cfg, svc.openaiWSResolver, svc.openaiWSPool = cfg, NewOpenAIWSProtocolResolver(cfg), pool
	var checks, releases atomic.Int32
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
		errCh <- svc.ProxyResponsesWebSocketFromClient(req.Context(), c, conn, chosen, "test-token", first, &OpenAIWSIngressHooks{
			BeforeImageSend: func(r OpenAIImageRequest) error {
				if !r.RequiresCapability() {
					return errors.New("lost hard image intent")
				}
				if checks.Add(1) > 1 {
					return errors.New("image admission denied on recovery")
				}
				return nil
			},
			AfterTurn: func(_ int, _ *OpenAIForwardResult, _ error) { releases.Add(1) },
		})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-6.1-sol","tools":[{"type":"image_generation"}],"tool_choice":"auto","truncation":"auto","input":[]}`)))
	select {
	case err := <-errCh:
		require.ErrorContains(t, err, "image admission denied on recovery")
	case <-ctx.Done():
		t.Fatal("recovery admission did not finish")
	}
	require.Equal(t, int32(2), checks.Load())
	require.Equal(t, int32(1), releases.Load(), "same-turn recovery must release once")
	capture.mu.Lock()
	defer capture.mu.Unlock()
	require.Len(t, capture.writes, 1, "denied recovery must not reach the socket")
}
