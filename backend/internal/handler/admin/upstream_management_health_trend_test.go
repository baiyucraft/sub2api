package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamHealthTrendHandlerRepo struct {
	service.UpstreamConfigRepository
	trend *service.UpstreamHealthTrend
}

func (r *upstreamHealthTrendHandlerRepo) GetUpstreamHealthTrend(_ context.Context, keyID int64, rangeName string, _ time.Time) (*service.UpstreamHealthTrend, error) {
	result := *r.trend
	result.KeyID = keyID
	result.Range = rangeName
	return &result, nil
}

func TestGetKeyHealthTrendAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	now := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	repo := &upstreamHealthTrendHandlerRepo{trend: &service.UpstreamHealthTrend{
		StartAt: now.Add(-6 * time.Hour), EndAt: now, BucketSeconds: 300,
		Points: []service.UpstreamHealthTrendPoint{{Bucket: now, State: service.UpstreamHealthHealthy, SampleCount: 1}},
	}}
	handler := NewUpstreamConfigHandler(service.NewUpstreamConfigService(repo, nil, nil))
	router := gin.New()
	router.GET("/api/v1/admin/upstream-management/keys/:id/health-trend", handler.GetKeyHealthTrendAdmin)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/keys/42/health-trend?range=6h", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Code int                         `json:"code"`
		Data service.UpstreamHealthTrend `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Zero(t, payload.Code)
	require.Equal(t, int64(42), payload.Data.KeyID)
	require.Equal(t, "6h", payload.Data.Range)
	require.Equal(t, int64(300), payload.Data.BucketSeconds)
	require.Len(t, payload.Data.Points, 1)
}

func TestGetKeyHealthTrendAdminRejectsInvalidRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewUpstreamConfigHandler(service.NewUpstreamConfigService(&upstreamHealthTrendHandlerRepo{}, nil, nil))
	router := gin.New()
	router.GET("/api/v1/admin/upstream-management/keys/:id/health-trend", handler.GetKeyHealthTrendAdmin)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/keys/42/health-trend?range=90d", nil))
	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
