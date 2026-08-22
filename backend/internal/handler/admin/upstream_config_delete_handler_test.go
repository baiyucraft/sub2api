package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamConfigDeleteHandlerRepo struct {
	service.UpstreamConfigRepository
	accountCount int64
	deleteCalls  int
	lastOptions  service.UpstreamConfigDeleteOptions
}

func (r *upstreamConfigDeleteHandlerRepo) CountAccounts(context.Context, int64) (int64, error) {
	return r.accountCount, nil
}

func (r *upstreamConfigDeleteHandlerRepo) Delete(context.Context, int64) error {
	r.deleteCalls++
	return nil
}

func (r *upstreamConfigDeleteHandlerRepo) DeleteWithOptions(_ context.Context, _ int64, options service.UpstreamConfigDeleteOptions) (service.UpstreamConfigDeleteResult, error) {
	r.lastOptions = options
	return service.UpstreamConfigDeleteResult{DeletedAccountCount: 2, DeletedKeyCount: 3}, nil
}

func TestUpstreamConfigDeleteHandlerAcceptsLegacyAndCascadeBodies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &upstreamConfigDeleteHandlerRepo{}
	handler := NewUpstreamConfigHandler(service.NewUpstreamConfigService(repo, nil, nil))
	router := gin.New()
	router.DELETE("/admin/upstream-configs/:id", handler.Delete)

	legacy := httptest.NewRecorder()
	router.ServeHTTP(legacy, httptest.NewRequest(http.MethodDelete, "/admin/upstream-configs/9", nil))
	require.Equal(t, http.StatusOK, legacy.Code)
	require.Equal(t, 1, repo.deleteCalls)

	cascade := httptest.NewRecorder()
	router.ServeHTTP(cascade, httptest.NewRequest(http.MethodDelete, "/admin/upstream-configs/9", strings.NewReader(`{"delete_sync_managed_accounts":true}`)))
	require.Equal(t, http.StatusOK, cascade.Code)
	require.True(t, repo.lastOptions.DeleteSyncManagedAccounts)
	require.Contains(t, cascade.Body.String(), `"deleted_account_count":2`)
	require.Contains(t, cascade.Body.String(), `"deleted_key_count":3`)
}

func TestUpstreamConfigDeleteHandlerRejectsMalformedOrTrailingJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &upstreamConfigDeleteHandlerRepo{}
	handler := NewUpstreamConfigHandler(service.NewUpstreamConfigService(repo, nil, nil))
	router := gin.New()
	router.DELETE("/admin/upstream-configs/:id", handler.Delete)

	for _, body := range []string{`{"delete_sync_managed_accounts":`, `{} {}`, `null`} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodDelete, "/admin/upstream-configs/9", strings.NewReader(body)))
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
	}
	require.Zero(t, repo.deleteCalls)
}

// Keep the embedded repository's compile-time contract visible if the
// interface gains methods used by the handler in the future.
var _ service.UpstreamConfigRepository = (*upstreamConfigDeleteHandlerRepo)(nil)
