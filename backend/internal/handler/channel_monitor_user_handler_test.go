//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestedMonitorRange_PrefersRangeOverLegacyRateRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors?range=15d&rate_range=30d", nil)

	require.Equal(t, "15d", requestedMonitorRange(c))
}

func TestRequestedMonitorRange_FallsBackToLegacyRateRange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors?rate_range=7d", nil)

	require.Equal(t, "7d", requestedMonitorRange(c))
}

func TestRequestedMonitorRange_ExplicitEmptyRangeStillTakesPrecedence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors?range=&rate_range=30d", nil)

	require.Empty(t, requestedMonitorRange(c))
}

func TestChannelMonitorUserList_UnauthenticatedReturns401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ChannelMonitorUserHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors", nil)

	h.List(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestChannelMonitorUserList_InvalidRangeFailsBeforeAuthorizationLookup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &ChannelMonitorUserHandler{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors?range=60d", nil)

	h.List(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

type channelMonitorUserRepoStub struct {
	service.ChannelMonitorRepository
}

func (s *channelMonitorUserRepoStub) ListEnabled(context.Context) ([]*service.ChannelMonitor, error) {
	return []*service.ChannelMonitor{{
		ID: 1, Name: "monitor", PrimaryModel: "gpt", Enabled: true,
		CredentialMode: service.ChannelMonitorCredentialManual,
	}}, nil
}

func (s *channelMonitorUserRepoStub) ListLatestForMonitorIDs(context.Context, []int64) (map[int64][]*service.ChannelMonitorLatest, error) {
	return map[int64][]*service.ChannelMonitorLatest{}, nil
}

func (s *channelMonitorUserRepoStub) ComputeAvailabilityForMonitors(_ context.Context, _ []int64, windowDays int) (map[int64][]*service.ChannelMonitorAvailability, error) {
	return map[int64][]*service.ChannelMonitorAvailability{
		1: {{Model: "gpt", AvailabilityPct: float64(windowDays)}},
	}, nil
}

func (s *channelMonitorUserRepoStub) ListRecentHistoryForMonitors(context.Context, []int64, map[int64]string, int) (map[int64][]*service.ChannelMonitorHistoryEntry, error) {
	return map[int64][]*service.ChannelMonitorHistoryEntry{}, nil
}

type channelMonitorGroupServiceStub struct {
	requestedUserID int64
}

func (s *channelMonitorGroupServiceStub) GetAvailableGroups(_ context.Context, userID int64) ([]service.Group, error) {
	s.requestedUserID = userID
	return []service.Group{}, nil
}

func TestChannelMonitorUserList_ReturnsUnifiedRangeAndAvailability(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groups := &channelMonitorGroupServiceStub{}
	h := &ChannelMonitorUserHandler{
		monitorService: service.NewChannelMonitorService(&channelMonitorUserRepoStub{}, nil),
		apiKeyService:  groups,
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/channel-monitors?range=15d&rate_range=30d", nil)

	h.List(c)

	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Range string `json:"range"`
			Items []struct {
				Availability   float64 `json:"availability"`
				Availability7d float64 `json:"availability_7d"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Equal(t, "15d", body.Data.Range)
	require.Len(t, body.Data.Items, 1)
	require.Equal(t, 15.0, body.Data.Items[0].Availability)
	require.Equal(t, 7.0, body.Data.Items[0].Availability7d)
	require.Equal(t, int64(42), groups.requestedUserID)
}
