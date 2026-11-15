package antigravity

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/stretchr/testify/require"
)

func TestCacheUsageSurvivesActualGeminiConverters(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		for _, tc := range []struct {
			name, fields string
			status       upstreamcacheusage.Status
		}{
			{"hit", `,"cachedContentTokenCount":80`, upstreamcacheusage.Known},
			{"explicit_zero", `,"cachedContentTokenCount":0`, upstreamcacheusage.Known},
			{"missing", "", upstreamcacheusage.Absent},
			{"over_total", `,"cachedContentTokenCount":101`, upstreamcacheusage.Invalid},
		} {
			t.Run(tc.name+map[bool]string{true: "/wrapped", false: "/direct"}[wrapped], func(t *testing.T) {
				raw := `{"candidates":[{"content":{"parts":[{"text":"reply"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100` + tc.fields + `}}`
				if wrapped {
					raw = `{"response":` + raw + `}`
				}
				body, usage, err := TransformGeminiToClaude([]byte(raw), "test")
				require.NoError(t, err)
				require.NotNil(t, usage.CacheUsageEvidence)
				require.Equal(t, tc.status, usage.CacheUsageEvidence.Status)
				require.NotContains(t, string(body), "CacheUsageEvidence")
				processor := NewStreamingProcessor("test")
				processor.SetUsageMapHook(func(m map[string]any) { m["cache_read_input_tokens"] = 99999 })
				processor.ProcessLine("data: " + raw)
				_, streamUsage := processor.Finish()
				require.Equal(t, usage.CacheUsageEvidence, streamUsage.CacheUsageEvidence)
				serialized, err := json.Marshal(streamUsage)
				require.NoError(t, err)
				require.False(t, strings.Contains(string(serialized), "CacheUsageEvidence"))
			})
		}
	}
}

func TestGeminiRawEvidenceIgnoresOnlyOutputRevisions(t *testing.T) {
	processor := NewStreamingProcessor("test")
	processor.ProcessLine(`data: {"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":80}}`)
	processor.ProcessLine(`data: {"usageMetadata":{"candidatesTokenCount":7}}`)
	_, usage := processor.Finish()
	require.True(t, usage.CacheUsageEvidence.Known())
	require.Equal(t, int64(80), usage.CacheUsageEvidence.CacheReadTokens)
	processor.ProcessLine(`data: {"usageMetadata":{"promptTokenCount":150}}`)
	_, usage = processor.Finish()
	require.Equal(t, upstreamcacheusage.Absent, usage.CacheUsageEvidence.Status)
	processor.ProcessLine(`data: {"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":null}}`)
	_, usage = processor.Finish()
	require.Equal(t, upstreamcacheusage.Invalid, usage.CacheUsageEvidence.Status)
}
