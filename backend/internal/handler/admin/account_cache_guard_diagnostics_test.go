package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheGuardDiagnosticsAdminStub struct {
	*stubAdminService
	account *service.Account
	err     error
	calls   int
}

func (s *cacheGuardDiagnosticsAdminStub) GetAccount(_ context.Context, _ int64) (*service.Account, error) {
	s.calls++
	return s.account, s.err
}

func cacheGuardDiagnosticsRouter(svc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := &AccountHandler{adminService: svc}
	router.GET("/api/v1/admin/accounts/:id/cache-guard-diagnostics", h.GetCacheGuardDiagnostics)
	return router
}

func TestGetCacheGuardDiagnosticsRejectsInvalidIDsBeforeAccountRead(t *testing.T) {
	for _, id := range []string{"bad", "0", "-1", "9223372036854775808"} {
		t.Run(id, func(t *testing.T) {
			svc := &cacheGuardDiagnosticsAdminStub{stubAdminService: newStubAdminService()}
			recorder := httptest.NewRecorder()
			cacheGuardDiagnosticsRouter(svc).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/"+id+"/cache-guard-diagnostics", nil))
			require.Equal(t, http.StatusBadRequest, recorder.Code)
			require.Zero(t, svc.calls)
		})
	}
}

func TestGetCacheGuardDiagnosticsPreservesAccountReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status int
	}{{"missing", service.ErrAccountNotFound, http.StatusNotFound}, {"unavailable", errors.New("database unavailable"), http.StatusInternalServerError}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &cacheGuardDiagnosticsAdminStub{stubAdminService: newStubAdminService(), err: tc.err}
			recorder := httptest.NewRecorder()
			cacheGuardDiagnosticsRouter(svc).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/1/cache-guard-diagnostics", nil))
			require.Equal(t, tc.status, recorder.Code)
			require.Equal(t, 1, svc.calls)
		})
	}
}

func TestGetCacheGuardDiagnosticsIsUncachedAndDoesNotExposeAccountSecrets(t *testing.T) {
	key := int64(9)
	svc := &cacheGuardDiagnosticsAdminStub{stubAdminService: newStubAdminService(), account: &service.Account{
		ID: 7, Name: "secret-account-name", UpstreamKeyID: &key,
		Credentials: map[string]any{"api_key": "secret-api-key", "access_token": "secret-access-token"},
		Extra:       map[string]any{"session_key": "secret-session", "request_body": "secret-prompt"},
	}}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/7/cache-guard-diagnostics", nil)
	request.Header.Set("If-None-Match", "old-diagnostic")
	cacheGuardDiagnosticsRouter(svc).ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "no-store", recorder.Header().Get("Cache-Control"))
	require.Empty(t, recorder.Header().Get("ETag"))
	var payload struct {
		Data service.UpstreamCacheGuardSamplingDiagnostics `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, int64(7), payload.Data.AccountID)
	require.Equal(t, key, payload.Data.UpstreamKeyID)
	require.False(t, payload.Data.CompleteHistory)
	for _, secret := range []string{"secret-account-name", "secret-api-key", "secret-access-token", "secret-session", "secret-prompt", "credentials", "request_body", "session_key", "logical_session_digest"} {
		require.NotContains(t, recorder.Body.String(), secret)
	}
}
