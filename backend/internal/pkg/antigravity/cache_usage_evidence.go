package antigravity

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/tidwall/gjson"
)

// ObserveGeminiCacheUsage observes the original fields before typed zero defaults, subtraction,
// Claude conversion, or a usage hook can alter their meaning.
func ObserveGeminiCacheUsage(raw []byte, previous *upstreamcacheusage.Evidence) *upstreamcacheusage.Evidence {
	root := gjson.ParseBytes(raw)
	if root.Get("response").IsObject() {
		root = root.Get("response")
	}
	usage := root.Get("usageMetadata")
	if previous != nil && (!usage.Exists() || (usage.IsObject() && !usage.Get("promptTokenCount").Exists() && !usage.Get("cachedContentTokenCount").Exists())) {
		return previous
	}
	return upstreamcacheusage.ParseGemini(usage)
}
