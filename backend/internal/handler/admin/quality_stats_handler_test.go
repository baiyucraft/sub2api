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

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type qualityHandlerUsageRepoStub struct {
	service.UsageLogRepository
	accountCalls atomic.Int32
	groupCalls   atomic.Int32
	mu           sync.Mutex
	accountIDs   []int64
	groupIDs     []int64
	loadStarted  chan struct{}
	releaseLoad  chan struct{}
}

func (s *qualityHandlerUsageRepoStub) GetAccountQualityStatsBatch(_ context.Context, ids []int64, _, _, _ time.Time) (map[int64]service.AccountQualitySamples, error) {
	s.accountCalls.Add(1)
	s.mu.Lock()
	s.accountIDs = append([]int64(nil), ids...)
	s.mu.Unlock()
	if s.loadStarted != nil {
		select {
		case s.loadStarted <- struct{}{}:
		default:
		}
	}
	if s.releaseLoad != nil {
		<-s.releaseLoad
	}
	return qualityHandlerSamples(ids), nil
}

func (s *qualityHandlerUsageRepoStub) GetGroupQualityStatsBatch(_ context.Context, ids []int64, _, _, _ time.Time) (map[int64]service.AccountQualitySamples, error) {
	s.groupCalls.Add(1)
	s.mu.Lock()
	s.groupIDs = append([]int64(nil), ids...)
	s.mu.Unlock()
	return qualityHandlerSamples(ids), nil
}

func qualityHandlerSamples(ids []int64) map[int64]service.AccountQualitySamples {
	result := make(map[int64]service.AccountQualitySamples, len(ids))
	for _, id := range ids {
		result[id] = service.AccountQualitySamples{SuccessfulRequests1h: 3}
	}
	return result
}

func TestAccountQualityStatsHandlerETagLimitsAndNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &qualityHandlerUsageRepoStub{}
	router := newAccountQualityStatsTestRouter(repo)

	previousCache := accountQualityStatsBatchCache
	accountQualityStatsBatchCache = newBoundedSnapshotCache(30*time.Second, qualityStatsCacheEntries)
	t.Cleanup(func() { accountQualityStatsBatchCache = previousCache })

	recorder := performQualityRequest(router, "/quality", `{"account_ids":[2,1,2,0,-1]}`, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "miss", recorder.Header().Get("X-Snapshot-Cache"))
	etag := recorder.Header().Get("ETag")
	require.NotEmpty(t, etag)
	require.Equal(t, "If-None-Match", recorder.Header().Get("Vary"))
	repo.mu.Lock()
	require.Equal(t, []int64{1, 2}, repo.accountIDs)
	repo.mu.Unlock()

	notModified := performQualityRequest(router, "/quality", `{"account_ids":[1,2]}`, etag)
	require.Equal(t, http.StatusNotModified, notModified.Code)
	require.Equal(t, "hit", notModified.Header().Get("X-Snapshot-Cache"))
	require.Equal(t, int32(1), repo.accountCalls.Load())

	empty := performQualityRequest(router, "/quality", `{"account_ids":[0,-1]}`, "")
	require.Equal(t, http.StatusOK, empty.Code)
	require.Contains(t, empty.Body.String(), `"stats":{}`)
	require.Equal(t, int32(1), repo.accountCalls.Load())

	badJSON := performQualityRequest(router, "/quality", `{"account_ids":`, "")
	require.Equal(t, http.StatusBadRequest, badJSON.Code)

	tooManyIDs := make([]int64, qualityStatsMaxRawIDs+1)
	for i := range tooManyIDs {
		tooManyIDs[i] = int64(i + 1)
	}
	body, err := json.Marshal(batchAccountQualityStatsRequest{AccountIDs: tooManyIDs})
	require.NoError(t, err)
	tooMany := performQualityRequest(router, "/quality", string(body), "")
	require.Equal(t, http.StatusBadRequest, tooMany.Code)
	require.Equal(t, int32(1), repo.accountCalls.Load())

	oversizedBody := `{"account_ids":[3],"padding":"` + strings.Repeat("x", int(qualityStatsRequestBodyLimit)) + `"}`
	oversized := performQualityRequest(router, "/quality", oversizedBody, "")
	require.Equal(t, http.StatusRequestEntityTooLarge, oversized.Code)
	require.Equal(t, int32(1), repo.accountCalls.Load())
}

func TestGroupQualityStatsHandlerUsesIndependentCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &qualityHandlerUsageRepoStub{}
	dashboard := service.NewDashboardService(repo, nil, nil, nil)
	handler := &GroupHandler{dashboardService: dashboard}
	router := gin.New()
	router.POST("/quality", middleware.RequestBodyLimit(qualityStatsRequestBodyLimit), handler.GetBatchQualityStats)

	previousCache := groupQualityStatsBatchCache
	groupQualityStatsBatchCache = newBoundedSnapshotCache(30*time.Second, qualityStatsCacheEntries)
	t.Cleanup(func() { groupQualityStatsBatchCache = previousCache })

	recorder := performQualityRequest(router, "/quality", `{"group_ids":[9,7,9,-1]}`, "")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.NotEmpty(t, recorder.Header().Get("ETag"))
	repo.mu.Lock()
	require.Equal(t, []int64{7, 9}, repo.groupIDs)
	repo.mu.Unlock()
	require.Equal(t, int32(1), repo.groupCalls.Load())
}

func TestQualityStatsCacheCapacityExpirationAndSingleflight(t *testing.T) {
	t.Run("capacity", func(t *testing.T) {
		cache := newBoundedSnapshotCache(time.Minute, 2)
		cache.Set("a", 1)
		time.Sleep(time.Millisecond)
		cache.Set("b", 2)
		cache.Set("c", 3)
		require.Len(t, cache.items, 2)
		_, ok := cache.Get("a")
		require.False(t, ok)
		_, ok = cache.Get("b")
		require.True(t, ok)
	})

	t.Run("expired entries are cleaned on set", func(t *testing.T) {
		cache := newBoundedSnapshotCache(time.Millisecond, 2)
		cache.Set("expired", 1)
		time.Sleep(5 * time.Millisecond)
		cache.Set("fresh", 2)
		require.Len(t, cache.items, 1)
	})

	t.Run("concurrent cold load", func(t *testing.T) {
		cache := newBoundedSnapshotCache(time.Minute, 2)
		var loads atomic.Int32
		start := make(chan struct{})
		errCh := make(chan error, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, _, err := cache.GetOrLoad("shared", func() (any, error) {
					loads.Add(1)
					time.Sleep(20 * time.Millisecond)
					return "value", nil
				})
				errCh <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			require.NoError(t, err)
		}
		require.Equal(t, int32(1), loads.Load())
	})
}

func newAccountQualityStatsTestRouter(repo *qualityHandlerUsageRepoStub) *gin.Engine {
	usage := service.NewAccountUsageService(nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler := &AccountHandler{accountUsageService: usage}
	router := gin.New()
	router.POST("/quality", middleware.RequestBodyLimit(qualityStatsRequestBodyLimit), handler.GetBatchQualityStats)
	return router
}

func performQualityRequest(router http.Handler, path, body, etag string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}
