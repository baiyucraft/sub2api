//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatStreamingForkCacheEvidenceCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for name, handle := range compatStreamHandlers() {
		for _, tc := range []struct {
			name         string
			stream       string
			readError    bool
			disconnect   bool
			finalWrite   bool
			wantError    bool
			wantComplete bool
		}{
			{name: "terminal", stream: anthropicStreamWithMidStreamText(), wantComplete: true},
			{name: "stop_reason_only", stream: anthropicStreamWithoutMessageStop()},
			{name: "missing_terminal", stream: anthropicStreamTruncatedBeforeMessageDelta(), wantError: true},
			{name: "read_error_before_terminal", stream: anthropicStreamTruncatedBeforeMessageDelta(), readError: true, wantError: true},
			{name: "read_error_after_terminal", stream: anthropicStreamWithMidStreamText(), readError: true},
			{name: "disconnect_drains_terminal", stream: anthropicStreamWithMidStreamText(), disconnect: true, wantComplete: true},
			{name: "disconnect_on_completion_frame", stream: anthropicStreamWithMidStreamText(), disconnect: true, finalWrite: true, wantComplete: true},
			{name: "disconnect_without_terminal", stream: anthropicStreamTruncatedBeforeMessageDelta(), disconnect: true},
			{name: "done_marker", stream: anthropicStreamTruncatedBeforeMessageDelta() + "data: [DONE]\n\n", wantComplete: true},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				var writer http.ResponseWriter = httptest.NewRecorder()
				if tc.disconnect {
					marker := "second"
					if tc.finalWrite {
						marker = compatStreamTerminalMarkers[name]
					}
					writer = &disconnectOnMarkerResponseWriter{ResponseRecorder: httptest.NewRecorder(), marker: marker}
				}
				c, _ := gin.CreateTestContext(writer)
				body := io.Reader(strings.NewReader(tc.stream))
				if tc.readError {
					body = &errAfterReader{data: strings.NewReader(tc.stream), err: io.ErrUnexpectedEOF}
				}
				result, err := handle(&GatewayService{}, &http.Response{Header: http.Header{}, Body: io.NopCloser(body)}, c)
				if tc.wantError {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				require.NotNil(t, result, "observed usage must remain billable on truncated streams")
				require.Equal(t, tc.disconnect, result.ClientDisconnect)
				require.NotNil(t, result.CacheUsageEvidence)
				require.Same(t, result.CacheUsageEvidence, result.Usage.CacheUsageEvidence)
				require.True(t, result.CacheUsageEvidence.Known())
				require.EqualValues(t, 35, result.CacheUsageEvidence.TotalInputTokens)
				require.EqualValues(t, 11, result.CacheUsageEvidence.CacheReadTokens)
				require.EqualValues(t, 4, result.CacheUsageEvidence.CacheCreationTokens)
				require.Equal(t, !tc.wantComplete, result.CacheUsageEvidence.StreamIncomplete)
				if tc.disconnect {
					// The lifecycle independently excludes disconnected requests even when
					// draining reaches a complete upstream cache fact for settlement.
					disconnected := writer.(*disconnectOnMarkerResponseWriter)
					require.True(t, disconnected.failed)
					require.Zero(t, disconnected.writesAfterError)
					require.Zero(t, disconnected.flushesAfterError)
				}
			})
		}
	}
}
