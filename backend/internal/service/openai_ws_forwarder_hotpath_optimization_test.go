package service

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/stretchr/testify/require"
)

func TestParseOpenAIWSEventEnvelope(t *testing.T) {
	eventType, responseID, response := parseOpenAIWSEventEnvelope([]byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.1"}}`))
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_1", responseID)
	require.True(t, response.Exists())
	require.Equal(t, `{"id":"resp_1","model":"gpt-5.1"}`, response.Raw)

	eventType, responseID, response = parseOpenAIWSEventEnvelope([]byte(`{"type":"response.delta","id":"evt_1"}`))
	require.Equal(t, "response.delta", eventType)
	require.Equal(t, "evt_1", responseID)
	require.False(t, response.Exists())
}

func TestParseOpenAIWSResponseUsageFromCompletedEvent(t *testing.T) {
	usage := &OpenAIUsage{}
	parseOpenAIWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`),
		usage,
	)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
	require.Equal(t, &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 11, CacheReadTokens: 3, Source: "usage"}, usage.CacheUsageEvidence)
	firstRaw := usage.CacheUsageEvidence

	parseOpenAIWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"prompt_tokens":19,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":4}}}}`),
		usage,
	)
	require.Equal(t, 19, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
	require.Equal(t, &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 19, CacheReadTokens: 4, Source: "usage"}, usage.CacheUsageEvidence)
	require.Equal(t, int64(11), firstRaw.TotalInputTokens, "later terminal usage must not mutate prior evidence")
	secondRaw := usage.CacheUsageEvidence

	parseOpenAIWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`),
		usage,
	)
	require.Equal(t, OpenAIUsage{InputTokens: 19, OutputTokens: 5, CacheReadInputTokens: 4,
		CacheUsageEvidence: &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", Source: "usage"}}, *usage)
	require.False(t, usage.CacheUsageEvidence.Known(), "explicit zero total is not a valid sample despite retained billing tokens")
	require.Equal(t, int64(19), secondRaw.TotalInputTokens)
	require.Equal(t, int64(4), secondRaw.CacheReadTokens)

	parseOpenAIWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.failed","response":{"usage":{"input_tokens":3,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`),
		usage,
	)
	require.Equal(t, OpenAIUsage{InputTokens: 3,
		CacheUsageEvidence: &UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 3, Source: "usage", StreamIncomplete: true}}, *usage)
	require.False(t, usage.CacheUsageEvidence.Success, "raw field presence does not mark a failed transport terminal successful")

	t.Run("terminal raw presence is independent of billing fallback", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			payload     string
			wantBilling OpenAIUsage
			wantRaw     UpstreamCacheUsageEvidence
		}{
			{"missing cache", `{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4}}}`, OpenAIUsage{InputTokens: 3, OutputTokens: 4}, UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Absent, Protocol: "openai", TotalInputTokens: 3, Source: "usage"}},
			{"missing usage", `{"type":"response.completed","response":{}}`, OpenAIUsage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 80}, UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Absent, Protocol: "openai", Source: "usage"}},
			{"explicit cache zero", `{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"input_tokens_details":{"cached_tokens":0}}}}`, OpenAIUsage{InputTokens: 3, OutputTokens: 4}, UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Known, Protocol: "openai", TotalInputTokens: 3, Source: "usage"}},
			{"invalid cache null", `{"type":"response.completed","response":{"usage":{"input_tokens":3,"output_tokens":4,"input_tokens_details":{"cached_tokens":null}}}}`, OpenAIUsage{InputTokens: 3, OutputTokens: 4}, UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Invalid, Protocol: "openai", TotalInputTokens: 3, Source: "usage"}},
			{"malformed terminal", `{"type":"response.completed","response":{"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":0}}}} trailing`, OpenAIUsage{InputTokens: 100, OutputTokens: 7, CacheReadInputTokens: 80}, UpstreamCacheUsageEvidence{Status: upstreamcacheusage.Absent, Protocol: "openai", Source: "usage"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				progressive := OpenAIUsage{}
				parseOpenAIWSResponseUsageFromCompletedEvent([]byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":100,"output_tokens":7,"input_tokens_details":{"cached_tokens":80}}}}`), &progressive)
				priorRaw := progressive.CacheUsageEvidence
				require.NotNil(t, priorRaw)
				require.True(t, priorRaw.Known())
				parseOpenAIWSResponseUsageFromCompletedEvent([]byte(tc.payload), &progressive)
				want := tc.wantBilling
				want.CacheUsageEvidence = &tc.wantRaw
				require.Equal(t, want, progressive)
				require.Equal(t, int64(100), priorRaw.TotalInputTokens)
				require.Equal(t, int64(80), priorRaw.CacheReadTokens)
			})
		}
	})
}

func TestOpenAIWSEventShouldParseUsageTerminalEvents(t *testing.T) {
	t.Parallel()

	for _, eventType := range []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	} {
		require.True(t, openAIWSEventShouldParseUsage(eventType), eventType)
		require.True(t, openAIWSEventShouldParseUsage("  "+eventType+"  "), eventType)
		message := []byte(`{"type":"` + eventType + `","response":{"id":"resp_without_usage"}}`)
		require.True(t, openAIWSMessageShouldParseUsage(eventType, message), "terminal without usage must clear progressive evidence")
	}
	require.True(t, openAIWSMessageShouldParseUsage("error", []byte(`{"type":"error","error":{"message":"failed"}}`)))
	require.False(t, openAIWSEventShouldParseUsage("response.output_text.delta"))
	require.True(t, openAIWSEventShouldParseUsage("response.output_text.done"))
	require.False(t, openAIWSEventShouldParseUsage(""))
	require.False(t, openAIWSMessageShouldParseUsage("response.in_progress", []byte(`{"type":"response.in_progress"}`)))
	require.True(t, openAIWSMessageShouldParseUsage("response.in_progress", []byte(`{"type":"response.in_progress","usage":{}}`)))
	require.False(t, openAIWSMessageShouldParseUsage("response.output_text.delta", []byte(`{"type":"response.output_text.delta","usage":{}}`)))
}

func TestOpenAIWSErrorEventHelpers_ConsistentWithWrapper(t *testing.T) {
	message := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)
	codeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(message)

	wrappedReason, wrappedRecoverable := classifyOpenAIWSErrorEvent(message)
	rawReason, rawRecoverable := classifyOpenAIWSErrorEventFromRaw(codeRaw, errTypeRaw, errMsgRaw)
	require.Equal(t, wrappedReason, rawReason)
	require.Equal(t, wrappedRecoverable, rawRecoverable)

	wrappedStatus := openAIWSErrorHTTPStatus(message)
	rawStatus := openAIWSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw)
	require.Equal(t, wrappedStatus, rawStatus)
	require.Equal(t, http.StatusBadRequest, rawStatus)

	wrappedCode, wrappedType, wrappedMsg := summarizeOpenAIWSErrorEventFields(message)
	rawCode, rawType, rawMsg := summarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMsgRaw)
	require.Equal(t, wrappedCode, rawCode)
	require.Equal(t, wrappedType, rawType)
	require.Equal(t, wrappedMsg, rawMsg)
}

func TestOpenAIWSMessageLikelyContainsToolCalls(t *testing.T) {
	require.False(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_text.delta","delta":"hello"}`)))
	require.True(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"tool_calls":[{"id":"tc1"}]}}`)))
	require.True(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"type":"function_call"}}`)))
}

func TestReplaceOpenAIWSMessageModel_OptimizedStillCorrect(t *testing.T) {
	noModel := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	require.Equal(t, string(noModel), string(replaceOpenAIWSMessageModel(noModel, "gpt-5.1", "custom-model")))

	rootOnly := []byte(`{"type":"response.created","model":"gpt-5.1"}`)
	require.Equal(t, `{"type":"response.created","model":"custom-model"}`, string(replaceOpenAIWSMessageModel(rootOnly, "gpt-5.1", "custom-model")))

	responseOnly := []byte(`{"type":"response.completed","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"type":"response.completed","response":{"model":"custom-model"}}`, string(replaceOpenAIWSMessageModel(responseOnly, "gpt-5.1", "custom-model")))

	both := []byte(`{"model":"gpt-5.1","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"model":"custom-model","response":{"model":"custom-model"}}`, string(replaceOpenAIWSMessageModel(both, "gpt-5.1", "custom-model")))
}
