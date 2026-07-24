package admin

import (
	"strconv"
	"strings"
	"time"
)

var groupQualityStatsBatchCache = newBoundedSnapshotCache(30*time.Second, qualityStatsCacheEntries)

func buildGroupQualityStatsBatchCacheKey(groupIDs []int64) string {
	if len(groupIDs) == 0 {
		return "groups_quality_stats_empty"
	}
	var b strings.Builder
	b.Grow(len(groupIDs) * 6)
	_, _ = b.WriteString("groups_quality_stats:")
	for i, id := range groupIDs {
		if i > 0 {
			_ = b.WriteByte(',')
		}
		_, _ = b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}
