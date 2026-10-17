package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountListQualityResponseRepo struct {
	service.UsageLogRepository
	calls     int
	end       time.Time
	err       error
	successes int64
}

func (r *accountListQualityResponseRepo) GetAccountQualityStatsBatch(_ context.Context, ids []int64, _, _, end time.Time) (map[int64]service.AccountQualitySamples, error) {
	r.calls++
	r.end = end
	stats := make(map[int64]service.AccountQualitySamples)
	for _, id := range ids {
		stats[id] = service.AccountQualitySamples{SuccessfulRequests1h: r.successes}
	}
	return stats, r.err
}

func TestAccountListIncludesQualityAndEvaluatedAtWithoutCachingCompletedResults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &accountListQualityResponseRepo{successes: 3}
	usage := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	admin := newStubAdminService()
	admin.accounts = []service.Account{{ID: 42, Name: "quality", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}
	h := NewAccountHandler(admin, nil, nil, nil, nil, nil, nil, usage, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/accounts", h.List)
	request := func(url, etag string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		router.ServeHTTP(r, req)
		return r
	}
	for _, lite := range []string{"0", "1"} {
		t.Run("lite="+lite, func(t *testing.T) {
			url := "/accounts?include_quality=1&lite=" + lite
			first := request(url, "")
			require.Equal(t, http.StatusOK, first.Code)
			var payload struct {
				Data struct {
					Items  []map[string]any                      `json:"items"`
					Stats  map[int64]service.AccountQualityStats `json:"quality_stats"`
					Status string                                `json:"quality_stats_status"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(first.Body.Bytes(), &payload))
			require.Equal(t, "ok", payload.Data.Status)
			require.Len(t, payload.Data.Stats, 1)
			require.Equal(t, int64(3), payload.Data.Stats[42].Activity.SuccessfulRequestCount)
			evaluatedAt, err := time.Parse(time.RFC3339Nano, first.Header().Get("X-Account-Evaluated-At"))
			require.NoError(t, err)
			require.True(t, evaluatedAt.Equal(repo.end), "quality window and response use one evaluation instant")
			calls := repo.calls
			unchanged := request(url, first.Header().Get("ETag"))
			require.Equal(t, http.StatusNotModified, unchanged.Code, "evaluation timestamp must not alter ETag")
			require.Equal(t, calls+1, repo.calls, "304 still evaluates new facts")
			repo.successes = 4
			changed := request(url, first.Header().Get("ETag"))
			require.Equal(t, http.StatusOK, changed.Code)
			require.NotEqual(t, first.Header().Get("ETag"), changed.Header().Get("ETag"))
			repo.err = errors.New("temporary database failure")
			failed := request(url, changed.Header().Get("ETag"))
			require.Equal(t, http.StatusOK, failed.Code)
			require.Contains(t, failed.Body.String(), `"quality_stats_status":"unavailable"`)
			require.NotEqual(t, changed.Header().Get("ETag"), failed.Header().Get("ETag"))
			repo.err, repo.successes = nil, 3
		})
	}
	calls := repo.calls
	legacy := request("/accounts?lite=1", "")
	require.Equal(t, http.StatusOK, legacy.Code)
	require.NotContains(t, legacy.Body.String(), `"quality_stats"`)
	require.Equal(t, calls, repo.calls)
}

func TestAccountListQualityFilterFailureDoesNotReturnOrdinaryUnfilteredPage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewAccountHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/accounts", h.List)
	r := httptest.NewRecorder()
	router.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/accounts?scope=upstream&quality_filter=1h-B&include_quality=1", nil))
	require.Equal(t, http.StatusServiceUnavailable, r.Code)
	require.Contains(t, r.Body.String(), `"reason":"ACCOUNT_QUALITY_FILTER_UNAVAILABLE"`)
}
