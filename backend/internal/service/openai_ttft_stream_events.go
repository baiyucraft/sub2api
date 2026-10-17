package service

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// markOpenAITTFTGuardForResponsesEvent records guard TTFT only when a
// Responses event contains visible model output. Transport/lifecycle events
// such as response.created and keepalive must not start the guard clock.
func markOpenAITTFTGuardForResponsesEvent(c *gin.Context, payload, eventType string) {
	if openAIStreamDataStartsVisibleOutput(payload, eventType) {
		markOpenAITTFTGuardFirstTokenForRequest(c)
	}
}

// anthropicStreamEventStartsVisibleOutput is the equivalent boundary for
// native Anthropic streams. Empty message/block starts, pings and usage-only
// deltas describe state and cannot manufacture a first-token observation.
func anthropicStreamEventStartsVisibleOutput(event *apicompat.AnthropicStreamEvent) bool {
	if event == nil {
		return false
	}
	switch strings.TrimSpace(event.Type) {
	case "message_start":
		if event.Message != nil {
			for i := range event.Message.Content {
				if anthropicBlockHasVisibleOutput(&event.Message.Content[i]) {
					return true
				}
			}
		}
	case "content_block_start":
		return anthropicBlockHasVisibleOutput(event.ContentBlock)
	case "content_block_delta":
		if event.Delta != nil {
			return event.Delta.Text != "" || event.Delta.Thinking != "" ||
				event.Delta.PartialJSON != "" || event.Delta.Signature != ""
		}
	}
	return false
}

func anthropicBlockHasVisibleOutput(block *apicompat.AnthropicContentBlock) bool {
	if block == nil {
		return false
	}
	return block.Text != "" || block.Thinking != "" || block.Signature != "" || block.Data != "" ||
		(strings.TrimSpace(string(block.Input)) != "" && strings.TrimSpace(string(block.Input)) != "{}" && strings.TrimSpace(string(block.Input)) != "null")
}

func markOpenAITTFTGuardForAnthropicPayload(c *gin.Context, payload string) {
	var event apicompat.AnthropicStreamEvent
	if json.Unmarshal([]byte(payload), &event) == nil {
		markOpenAITTFTGuardForAnthropicEvent(c, &event)
	}
}

// openAIChatCompletionsChunkStartsVisibleOutput recognizes a real Chat
// Completions delta while ignoring role-only, finish-only and usage-only
// chunks. Raw CC passthrough has no Responses event type to classify.
func openAIChatCompletionsChunkStartsVisibleOutput(payload string) bool {
	if !gjson.Valid(payload) {
		return false
	}
	for _, choice := range gjson.Get(payload, "choices").Array() {
		delta := choice.Get("delta")
		for _, path := range []string{"content", "refusal", "reasoning_content"} {
			if delta.Get(path).String() != "" {
				return true
			}
		}
		for _, call := range delta.Get("tool_calls").Array() {
			if call.Get("function.arguments").String() != "" || call.Get("function.input").String() != "" {
				return true
			}
		}
		if delta.Get("audio.transcript").String() != "" || delta.Get("audio.data").String() != "" {
			return true
		}
	}
	return false
}

func markOpenAITTFTGuardForAnthropicEvent(c *gin.Context, event *apicompat.AnthropicStreamEvent) {
	if anthropicStreamEventStartsVisibleOutput(event) {
		markOpenAITTFTGuardFirstTokenForRequest(c)
	}
}
