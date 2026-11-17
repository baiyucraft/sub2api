//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Exercise both real CC conversion paths before billing: usage and a synthetic
// downstream final event must not establish successful upstream completion.
func TestCCFallbackHealthCompletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const done = "data: [DONE]\n\n"
	finish := func(reason string) string {
		return "data: " + `{"id":"chatcmpl_health","choices":[{"index":0,"delta":{},"finish_reason":"` + reason + `"}]}` + "\n\n"
	}
	for _, route := range []string{"messages", "responses"} {
		for _, tc := range []struct {
			name          string
			terminal      string
			cached        bool
			incomplete    bool
			readError     bool
			writeError    bool
			lateCancel    bool
			invalidTool   bool
			responsesOnly bool
		}{
			{name: "missing_terminal_with_known_cache", cached: true, incomplete: true},
			{name: "error_then_done", terminal: "data: " + `{"error":{"message":"upstream failed","type":"server_error"}}` + "\n\n" + done, cached: true, incomplete: true},
			{name: "content_filter_then_done", terminal: finish("content_filter") + done, cached: true, incomplete: true},
			{name: "failed_finish_then_stop_and_done", terminal: finish("error") + finish("stop") + done, cached: true, incomplete: true},
			{name: "finish_without_done_or_cache", terminal: finish("stop")},
			{name: "done_without_finish_or_cache", terminal: done},
			{name: "length_finish_without_cache", terminal: finish("length") + done},
			{name: "read_error_preserves_partial_usage", readError: true, cached: true, incomplete: true},
			{name: "finish_then_read_error", terminal: finish("stop"), readError: true, incomplete: true},
			{name: "write_error_still_drains_usage", terminal: finish("stop") + done, writeError: true},
			{name: "cancel_after_complete", terminal: finish("stop") + done, lateCancel: true},
			{name: "invalid_tool_arguments", terminal: finish("tool_calls") + done, invalidTool: true, responsesOnly: true, incomplete: true},
		} {
			if tc.responsesOnly && route != "responses" {
				continue
			}
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				previous := upstreamHealthEvidenceRecorder()
				health := &usageCompletionHealthRecorder{}
				SetGlobalUpstreamHealthEvidenceRecorder(health)
				t.Cleanup(func() { SetGlobalUpstreamHealthEvidenceRecorder(previous) })

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writer := newOpenAIResponseFlushRecorder()
				if tc.writeError {
					writer.failAfterWrites = 0
				}
				c, _ := gin.CreateTestContext(writer)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+route, nil).WithContext(ctx)

				usage := `{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107}`
				if tc.cached {
					usage = `{"prompt_tokens":100,"completion_tokens":7,"total_tokens":107,"prompt_tokens_details":{"cached_tokens":80}}`
				}
				wire := "data: " + `{"id":"chatcmpl_health","model":"gpt-5.1","choices":[{"index":0,"delta":{"role":"assistant","content":"partial response"},"finish_reason":null}],"usage":` + usage + "}\n\n"
				if tc.invalidTool {
					wire += "data: " + `{"id":"chatcmpl_health","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_bad","type":"function","function":{"name":"lookup","arguments":"{"}}]},"finish_reason":null}]}` + "\n\n"
				}
				wire += tc.terminal
				var body io.ReadCloser = io.NopCloser(strings.NewReader(wire))
				if tc.readError {
					body = &errTailReader{data: []byte(wire), err: io.ErrUnexpectedEOF}
				}
				resp := &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"cc-health"}},
					Body:       body,
				}
				defer resp.Body.Close()
				forwarder := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig()}
				var result *OpenAIForwardResult
				var err error
				if route == "messages" {
					result, err = forwarder.streamChatCompletionsAsAnthropic(c, resp, "gpt-5.1", "gpt-5.1", "gpt-5.1", nil, nil, time.Now())
				} else {
					result, err = forwarder.streamChatCompletionsAsResponses(c, resp, "gpt-5.1", nil, nil, false, nil, "gpt-5.1", "gpt-5.1", nil, nil, time.Now())
				}
				if tc.readError || tc.invalidTool {
					require.Error(t, err)
				} else {
					require.NoError(t, err, "retain the existing CC fallback error contract")
				}
				require.NotNil(t, result)
				require.Equal(t, tc.incomplete, result.UpstreamIncomplete)
				require.Equal(t, tc.writeError, result.ClientDisconnect)
				require.Equal(t, !tc.incomplete, result.SucceededForScheduling())
				require.Equal(t, 100, result.Usage.InputTokens)
				require.Equal(t, 7, result.Usage.OutputTokens)
				if tc.cached {
					require.Equal(t, 80, result.Usage.CacheReadInputTokens)
					require.NotNil(t, result.Usage.CacheUsageEvidence)
					require.Equal(t, upstreamcacheusage.Known, result.Usage.CacheUsageEvidence.Status)
					require.True(t, result.Usage.CacheUsageEvidence.StreamIncomplete, "raw known cache fields cannot establish stream completion")
				} else if result.Usage.CacheUsageEvidence != nil {
					require.NotEqual(t, upstreamcacheusage.Known, result.Usage.CacheUsageEvidence.Status, "successful completion does not require cache fields")
				}
				if tc.lateCancel {
					cancel()
				}

				logs := &openAIRecordUsageLogRepoStub{inserted: true}
				users := &openAIRecordUsageUserRepoStub{}
				billing := newOpenAIRecordUsageServiceForTest(logs, users, &openAIRecordUsageSubRepoStub{}, nil)
				require.NoError(t, billing.RecordUsage(ctx, &OpenAIRecordUsageInput{
					Result: result, ForwardFailed: err != nil,
					APIKey: &APIKey{ID: 12}, User: &User{ID: 11}, Account: &Account{ID: 13},
				}))
				wantSuccesses := 1
				if tc.incomplete || tc.writeError {
					wantSuccesses = 0
				}
				require.Equal(t, wantSuccesses, health.successes, "partial usage or a synthetic final event cannot recover upstream health")
				require.Equal(t, 1, logs.calls)
				require.Equal(t, 1, users.deductCalls, "unsuccessful completion must retain billable usage")
				// Billing stores ordinary input and cache reads in disjoint buckets.
				require.Equal(t, 100, logs.lastLog.InputTokens+logs.lastLog.CacheReadTokens)
				require.Equal(t, result.Usage.CacheReadInputTokens, logs.lastLog.CacheReadTokens)
				require.Equal(t, 7, logs.lastLog.OutputTokens)
			})
		}
	}
}
