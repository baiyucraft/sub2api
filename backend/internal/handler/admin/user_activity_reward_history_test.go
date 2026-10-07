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

type activityHistoryAdminStub struct {
	service.AdminService
	userID     int64
	page       int
	pageSize   int
	typeFilter string
}

func (s *activityHistoryAdminStub) GetUserBalanceHistory(_ context.Context, userID int64, page, pageSize int, typeFilter string) (*service.UserBalanceHistoryPage, error) {
	s.userID, s.page, s.pageSize, s.typeFilter = userID, page, pageSize, typeFilter
	claimedAt := time.Date(2026, 10, 7, 4, 0, 0, 0, time.UTC)
	return &service.UserBalanceHistoryPage{
		Items: []service.UserBalanceHistoryRecord{{ID: 1, Type: "activity_reward", Value: 0,
			Status: "credited", RecordSource: "activity_reward", SourceID: 1,
			ActivityType: "daily_gift", CreatedAt: claimedAt, UsedAt: &claimedAt}},
		Total: 3, TotalRecharged: 100, TotalRewarded: 1.25,
	}, nil
}

func TestActivityRewardBalanceHistoryAPIKeepsLifetimeTotalsAndOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &activityHistoryAdminStub{}
	h := &UserHandler{adminService: stub}
	router := gin.New()
	router.GET("/users/:id/balance-history", h.GetBalanceHistory)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/users/7/balance-history?page=2&page_size=2&type=activity_reward", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var response struct {
		Data struct {
			Items []struct {
				Code         string  `json:"code"`
				Value        float64 `json:"value"`
				RecordSource string  `json:"record_source"`
				SourceID     int64   `json:"source_id"`
				ActivityType string  `json:"activity_type"`
			} `json:"items"`
			Total          int64   `json:"total"`
			Pages          int     `json:"pages"`
			TotalRewarded  float64 `json:"total_rewarded"`
			TotalRecharged float64 `json:"total_recharged"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, int64(7), stub.userID)
	require.Equal(t, 2, stub.page)
	require.Equal(t, "activity_reward", stub.typeFilter)
	require.Equal(t, 1.25, response.Data.TotalRewarded)
	require.Equal(t, 100.0, response.Data.TotalRecharged)
	require.Equal(t, 2, response.Data.Pages)
	require.Len(t, response.Data.Items, 1)
	require.Empty(t, response.Data.Items[0].Code)
	require.Zero(t, response.Data.Items[0].Value)
	require.Equal(t, "activity_reward", response.Data.Items[0].RecordSource)
	require.Equal(t, "daily_gift", response.Data.Items[0].ActivityType)
	require.Equal(t, int64(1), response.Data.Items[0].SourceID)
}
