package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dashboardBatchUsersUsageRepoStub struct {
	service.UsageLogRepository
	calls       atomic.Int32
	mu          sync.Mutex
	userIDs     []int64
	loadStarted chan struct{}
	releaseLoad chan struct{}
	waitForCtx  bool
}

func (s *dashboardBatchUsersUsageRepoStub) GetBatchUserUsageStats(
	ctx context.Context,
	userIDs []int64,
	_, _ time.Time,
) (map[int64]*usagestats.BatchUserUsageStats, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.userIDs = append([]int64(nil), userIDs...)
	s.mu.Unlock()
	if s.loadStarted != nil {
		select {
		case s.loadStarted <- struct{}{}:
		default:
		}
	}
	if s.waitForCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if s.releaseLoad != nil {
		<-s.releaseLoad
	}
	result := make(map[int64]*usagestats.BatchUserUsageStats, len(userIDs))
	for _, id := range userIDs {
		result[id] = &usagestats.BatchUserUsageStats{
			UserID: id,
			Today: usagestats.UserUsageWindow{
				InputTokens:  10,
				OutputTokens: 2,
				TotalTokens:  12,
				UserSpend:    1.25,
				AccountCost:  0.75,
			},
			AggregationStatus: usagestats.UserUsageAggregationAvailable,
			TodayActualCost:   1.25,
			TotalActualCost:   4.5,
		}
	}
	return result, nil
}

func TestDashboardBatchUsersUsageHandlerLimitsETagAndNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardBatchUsersUsageRepoStub{}
	router := newDashboardBatchUsersUsageRouter(repo)

	previousCache := dashboardBatchUsersUsageCache
	dashboardBatchUsersUsageCache = newBoundedSnapshotCache(30*time.Second, dashboardUsersUsageCacheEntries)
	t.Cleanup(func() { dashboardBatchUsersUsageCache = previousCache })

	first := performDashboardBatchUsersUsageRequest(router, `{"user_ids":[2,1,2,0,-1]}`, "")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "miss", first.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, "If-None-Match", first.Header().Get("Vary"))
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)
	repo.mu.Lock()
	require.Equal(t, []int64{1, 2}, repo.userIDs)
	repo.mu.Unlock()

	var body struct {
		Data struct {
			Stats map[string]usagestats.BatchUserUsageStats `json:"stats"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(first.Body.Bytes(), &body))
	require.EqualValues(t, 12, body.Data.Stats["1"].Today.TotalTokens)
	require.Equal(t, usagestats.UserUsageAggregationAvailable, body.Data.Stats["1"].AggregationStatus)

	notModified := performDashboardBatchUsersUsageRequest(router, `{"user_ids":[1,2]}`, etag)
	require.Equal(t, http.StatusNotModified, notModified.Code)
	require.Equal(t, "hit", notModified.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.calls.Load())

	empty := performDashboardBatchUsersUsageRequest(router, `{"user_ids":[0,-1]}`, "")
	require.Equal(t, http.StatusOK, empty.Code)
	require.Contains(t, empty.Body.String(), `"stats":{}`)

	tooMany := make([]int64, dashboardUsersUsageMaxRawIDs+1)
	for i := range tooMany {
		tooMany[i] = int64(i + 1)
	}
	raw, err := json.Marshal(BatchUsersUsageRequest{UserIDs: tooMany})
	require.NoError(t, err)
	tooManyResponse := performDashboardBatchUsersUsageRequest(router, string(raw), "")
	require.Equal(t, http.StatusBadRequest, tooManyResponse.Code)

	oversized := performDashboardBatchUsersUsageRequest(
		router,
		`{"user_ids":[3],"padding":"`+strings.Repeat("x", int(qualityStatsRequestBodyLimit))+`"}`,
		"",
	)
	require.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code)
	require.Equal(t, int32(1), repo.calls.Load())
}

func TestDashboardBatchUsersUsageHandlerSingleflight(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardBatchUsersUsageRepoStub{
		loadStarted: make(chan struct{}, 1),
		releaseLoad: make(chan struct{}),
	}
	router := newDashboardBatchUsersUsageRouter(repo)

	previousCache := dashboardBatchUsersUsageCache
	dashboardBatchUsersUsageCache = newBoundedSnapshotCache(30*time.Second, dashboardUsersUsageCacheEntries)
	t.Cleanup(func() { dashboardBatchUsersUsageCache = previousCache })

	const workers = 6
	start := make(chan struct{})
	results := make(chan int, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := performDashboardBatchUsersUsageRequest(router, `{"user_ids":[9]}`, "")
			results <- rec.Code
		}()
	}
	close(start)
	<-repo.loadStarted
	time.Sleep(20 * time.Millisecond)
	close(repo.releaseLoad)
	wg.Wait()
	close(results)
	for status := range results {
		require.Equal(t, http.StatusOK, status)
	}
	require.Equal(t, int32(1), repo.calls.Load())
}

func TestDashboardBatchUsersUsageHandlerQueryTimeoutIsRedacted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &dashboardBatchUsersUsageRepoStub{waitForCtx: true}
	router := newDashboardBatchUsersUsageRouter(repo)

	previousCache := dashboardBatchUsersUsageCache
	previousTimeout := dashboardUsersUsageQueryTimeout
	dashboardBatchUsersUsageCache = newBoundedSnapshotCache(30*time.Second, dashboardUsersUsageCacheEntries)
	dashboardUsersUsageQueryTimeout = 10 * time.Millisecond
	t.Cleanup(func() {
		dashboardBatchUsersUsageCache = previousCache
		dashboardUsersUsageQueryTimeout = previousTimeout
	})

	recorder := performDashboardBatchUsersUsageRequest(router, `{"user_ids":[11]}`, "")
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Failed to get user usage stats")
	require.NotContains(t, recorder.Body.String(), "context deadline exceeded")
	require.NotContains(t, recorder.Body.String(), "SELECT")
	require.Equal(t, int32(1), repo.calls.Load())
}

func newDashboardBatchUsersUsageRouter(repo service.UsageLogRepository) *gin.Engine {
	dashboardSvc := service.NewDashboardService(repo, nil, nil, nil)
	handler := NewDashboardHandler(dashboardSvc, nil)
	router := gin.New()
	router.POST(
		"/admin/dashboard/users-usage",
		middleware.RequestBodyLimit(qualityStatsRequestBodyLimit),
		handler.GetBatchUsersUsage,
	)
	return router
}

func performDashboardBatchUsersUsageRequest(router http.Handler, body, etag string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/dashboard/users-usage", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}
