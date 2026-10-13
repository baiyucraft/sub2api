package upstreamcacheusage

import (
	"github.com/tidwall/gjson"
	"testing"
	"time"
)

func TestOpenAIFieldPresenceAndValidity(t *testing.T) {
	cases := []struct {
		name, body string
		status     Status
		read       int64
	}{
		{"zero", `{"input_tokens":2048,"input_tokens_details":{"cached_tokens":0}}`, Known, 0},
		{"missing", `{"input_tokens":2048}`, Absent, 0},
		{"missing_total", `{"cached_tokens":1536}`, Absent, 1536},
		{"invalid_usage_array", `[]`, Invalid, 0},
		{"priority_zero", `{"input_tokens":2048,"input_tokens_details":{"cached_tokens":0},"cached_tokens":1800}`, Known, 0},
		{"alias", `{"prompt_tokens":2048,"cache_read_tokens":1536}`, Known, 1536},
		{"null", `{"input_tokens":2048,"cached_tokens":null}`, Invalid, 0},
		{"string", `{"input_tokens":2048,"cached_tokens":"1536"}`, Invalid, 0},
		{"negative", `{"input_tokens":2048,"cached_tokens":-1}`, Invalid, -1},
		{"fraction", `{"input_tokens":2048,"cached_tokens":1.5}`, Invalid, 0},
		{"overflow", `{"input_tokens":9223372036854775808,"cached_tokens":0}`, Invalid, 0},
		{"read_exceeds_total", `{"input_tokens":20,"cached_tokens":21}`, Invalid, 21},
		{"buckets_exceed_total", `{"input_tokens":20,"cached_tokens":10,"cache_write_tokens":11}`, Invalid, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := ParseOpenAI(gjson.Parse(tc.body))
			if e.Status != tc.status || e.CacheReadTokens != tc.read {
				t.Fatalf("unexpected evidence: %+v", e)
			}
		})
	}
}

func TestNativeProtocolsShareInclusiveDenominator(t *testing.T) {
	openai := ParseOpenAI(gjson.Parse(`{"input_tokens":2000,"cached_tokens":1600,"cache_creation_tokens":200}`))
	anthropic := ParseAnthropic(gjson.Parse(`{"input_tokens":200,"cache_read_input_tokens":1600,"cache_creation_input_tokens":200}`))
	gemini := ParseGemini(gjson.Parse(`{"promptTokenCount":2000,"cachedContentTokenCount":1600}`))
	for _, e := range []*Evidence{openai, anthropic, gemini} {
		rate, ok := e.Rate()
		if !ok || rate != 80 || e.TotalInputTokens != 2000 {
			t.Fatalf("bad denominator: %+v rate=%v", e, rate)
		}
	}
	kimi := ParseAnthropic(gjson.Parse(`{"input_tokens":2000,"prompt_tokens":2000,"prompt_cache_hit_tokens":1600}`))
	if kimi.TotalInputTokens != 2000 || kimi.CacheReadTokens != 1600 {
		t.Fatalf("compatible total counted twice: %+v", kimi)
	}
}

func TestCompletionDoesNotMutateRawFact(t *testing.T) {
	raw := ParseOpenAI(gjson.Parse(`{"input_tokens":100,"cached_tokens":80}`))
	at := time.Date(2026, 10, 13, 1, 2, 3, 0, time.UTC)
	done := Complete(raw, "", true, at)
	if done == raw || raw.Success || !raw.CompletedAt.IsZero() || !done.Success || !done.CompletedAt.Equal(at) {
		t.Fatalf("completion mutated raw evidence")
	}
}
