//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestForward_OAuthWebSearchHistoryDeclaresTool drives the Codex local
// compaction shape (web_search_call history, tools:[]) through Forward and
// asserts the body reaching chatgpt.com declares web_search (#7927).
func TestForward_OAuthWebSearchHistoryDeclaresTool(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		name := "transform"
		if passthrough {
			name = "passthrough"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			s := newAstraOAuthSetup(t, passthrough)
			inner := `{"id":"resp_test","model":"gpt-5.5","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
			s.upstream.resp = &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader(codexCompletedSSE(inner))),
			}

			result, err := s.svc.Forward(context.Background(), s.c, s.account, []byte(openAIWebSearchHistoryCompactionBody))
			require.NoError(t, err)
			require.NotNil(t, result)
			require.NotNil(t, s.upstream.lastReq)
			require.Equal(t, astraProCodexResponsesURL, s.upstream.lastReq.URL.String())

			forwarded := s.upstream.lastBody
			require.Equal(t, "web_search_call", gjson.GetBytes(forwarded, "input.1.type").String())
			tools := gjson.GetBytes(forwarded, "tools").Array()
			require.Len(t, tools, 1)
			require.Equal(t, "web_search", tools[0].Get("type").String())
			require.Equal(t, "none", gjson.GetBytes(forwarded, "tool_choice").String())
		})
	}
}
