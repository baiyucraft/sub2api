package admin

import (
	"strconv"
	"strings"
	"time"
)

var accountQualityStatsBatchCache = newBoundedSnapshotCache(30*time.Second, qualityStatsCacheEntries)

func buildAccountQualityStatsBatchCacheKey(accountIDs []int64) string {
	if len(accountIDs) == 0 {
		return "accounts_quality_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(accountIDs) * 6)
	_, _ = b.WriteString("accounts_quality_stats:")
	for i, id := range accountIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}
