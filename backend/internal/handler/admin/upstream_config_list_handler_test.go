package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamConfigListHandlerRepo struct {
	service.UpstreamConfigRepository
	service.UpstreamOperationsRepository
	params pagination.PaginationParams
	filter service.UpstreamConfigListFilter
}

func (r *upstreamConfigListHandlerRepo) List(_ context.Context, params pagination.PaginationParams, filter service.UpstreamConfigListFilter) ([]service.UpstreamConfig, *pagination.PaginationResult, error) {
	r.params = params
	r.filter = filter
	return []service.UpstreamConfig{}, &pagination.PaginationResult{Total: 0, Page: params.Page, PageSize: params.PageSize}, nil
}

func (r *upstreamConfigListHandlerRepo) GetUpstreamSettings(context.Context) (*service.UpstreamSettings, error) {
	return &service.UpstreamSettings{BalanceLowThresholdCNY: 10}, nil
}

func TestUpstreamConfigListHandlerPassesFiltersAndSort(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &upstreamConfigListHandlerRepo{}
	handler := NewUpstreamConfigHandler(service.NewUpstreamConfigService(repo, nil, nil))
	router := gin.New()
	router.GET("/admin/upstream-configs", handler.List)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/upstream-configs?page=2&page_size=30&provider=newapi&status=active&search=main&sort_by=balance_cny&sort_order=desc", nil)
	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, pagination.PaginationParams{Page: 2, PageSize: 30, SortBy: "balance_cny", SortOrder: "desc"}, repo.params)
	require.Equal(t, service.UpstreamConfigListFilter{
		Provider:             "newapi",
		Status:               "active",
		Search:               "main",
		BalanceSortAvailable: true,
	}, repo.filter)
}

var _ service.UpstreamConfigRepository = (*upstreamConfigListHandlerRepo)(nil)
var _ service.UpstreamSettingsReader = (*upstreamConfigListHandlerRepo)(nil)
var _ service.UpstreamOperationsRepository = (*upstreamConfigListHandlerRepo)(nil)
