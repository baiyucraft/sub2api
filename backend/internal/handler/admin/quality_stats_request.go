package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

const (
	qualityStatsRequestBodyLimit int64 = 64 << 10
	qualityStatsMaxRawIDs              = 1000
	qualityStatsCacheEntries           = 256
)

type batchAccountQualityStatsRequest struct {
	AccountIDs []int64 `json:"account_ids"`
}

type batchGroupQualityStatsRequest struct {
	GroupIDs []int64 `json:"group_ids"`
}

// bindQualityStatsJSON preserves the route-level MaxBytesReader error so an
// oversized management request is reported as 413 instead of a generic 400.
func bindQualityStatsJSON(c *gin.Context, dst any) bool {
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Request body is too large")
			return false
		}
		response.BadRequest(c, "Invalid request: "+err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			response.BadRequest(c, "Invalid request: multiple JSON values")
			return false
		}
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(c, http.StatusRequestEntityTooLarge, "Request body is too large")
			return false
		}
		response.BadRequest(c, "Invalid request: "+err.Error())
		return false
	}
	return true
}
