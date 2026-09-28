package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAccountHandlerCreatePassesUnifiedProxyID(t *testing.T) {
	svc := newStubAdminService()
	router := setupAccountMixedChannelRouter(svc)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts", bytes.NewBufferString(`{"name":"codex","platform":"openai","type":"oauth","credentials":{"access_token":"test"},"proxy_id":36}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, svc.createdAccounts, 1)
	require.Equal(t, int64(36), *svc.createdAccounts[0].ProxyID)
	require.Nil(t, svc.createdAccounts[0].ProxyIPGroupID)
}

func TestAccountHandlerUpdatePassesUnifiedProxyIDAndLegacyInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		proxyID  *int64
		legacyID *int64
	}{
		{name: "positive binding", body: `{"proxy_id":36}`, proxyID: proxyBindingTestID(36)},
		{name: "clear binding", body: `{"proxy_id":0}`, proxyID: proxyBindingTestID(0)},
		{name: "omitted binding", body: `{}`},
		{name: "legacy group input", body: `{"proxy_ip_group_id":12}`, legacyID: proxyBindingTestID(12)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newStubAdminService()
			router := setupAccountMixedChannelRouter(svc)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/accounts/7", bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.proxyID, svc.lastUpdateAccountInput.ProxyID)
			require.Equal(t, tc.legacyID, svc.lastUpdateAccountInput.ProxyIPGroupID)
		})
	}
}

func proxyBindingTestID(value int64) *int64 { return &value }
