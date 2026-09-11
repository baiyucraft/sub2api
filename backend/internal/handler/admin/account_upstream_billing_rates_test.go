package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamBillingPreferredContextAdminService struct {
	*stubAdminService
	preferred bool
}

func (s *upstreamBillingPreferredContextAdminService) ListAccounts(ctx context.Context, page, pageSize int, platform, accountType, status, search string, groupID int64, privacyMode string, sortBy, sortOrder string) ([]service.Account, int64, error) {
	s.preferred, _ = service.AccountListPreferredFromContext(ctx)
	return s.stubAdminService.ListAccounts(ctx, page, pageSize, platform, accountType, status, search, groupID, privacyMode, sortBy, sortOrder)
}

func setupUpstreamBillingRatesRouter() (*gin.Engine, *upstreamBillingPreferredContextAdminService) {
	gin.SetMode(gin.TestMode)
	svc := &upstreamBillingPreferredContextAdminService{stubAdminService: newStubAdminService()}
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/admin/accounts/upstream-billing-rates", handler.GetUpstreamBillingRates)
	return router, svc
}

func TestGetUpstreamBillingRatesPassesPreferredFilterToAccountListing(t *testing.T) {
	router, svc := setupUpstreamBillingRatesRouter()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-rates?preferred=1", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, svc.preferred)
}

func TestGetUpstreamBillingRatesRejectsInvalidPreferredFilter(t *testing.T) {
	router, _ := setupUpstreamBillingRatesRouter()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/accounts/upstream-billing-rates?preferred=maybe", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
}
