package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"math"
)

// Quality data is evaluated for every refresh. Only concurrent exact-window
// service reads share work; completed snapshots have no TTL cache.
type accountListQuality struct {
	Stats  map[int64]service.AccountQualityStats `json:"quality_stats"`
	Status string                                `json:"quality_stats_status"`
}

func accountListQualityETag(include bool, stats map[int64]service.AccountQualityStats, status string) any {
	if !include {
		return nil
	}
	return accountListQuality{Stats: stats, Status: status}
}
func accountListETagExtension(values []any) any {
	if len(values) > 5 {
		return values[5]
	}
	return nil
}
func writeAccountListPage(c *gin.Context, items any, total int64, page, pageSize int, include bool, stats map[int64]service.AccountQualityStats, status string) {
	if !include {
		response.Paginated(c, items, total, page, pageSize)
		return
	}
	response.Success(c, struct {
		response.PaginatedData
		accountListQuality
	}{
		PaginatedData:      response.PaginatedData{Items: items, Total: total, Page: page, PageSize: pageSize, Pages: max(1, int(math.Ceil(float64(total)/float64(pageSize))))},
		accountListQuality: accountListQuality{Stats: stats, Status: status},
	})
}
