package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type rejectingProxyStatsService struct{ service.AdminService }

func (s rejectingProxyStatsService) GetProxy(context.Context, int64) (*service.Proxy, error) {
	return nil, infraerrors.BadRequest("PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED", "proxy-group bindings cannot be managed as real proxies")
}

func TestProxyStatsRejectPositiveGroupBindingID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/api/v1/admin/proxies/:id/stats", NewProxyHandler(rejectingProxyStatsService{newStubAdminService()}).GetStats)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/proxies/36/stats", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "PROXY_IP_GROUP_VIRTUAL_OPERATION_UNSUPPORTED")
}

func TestProxyNativeListsKeepPositiveGroupBindingID(t *testing.T) {
	groupID := int64(12)
	group := service.Proxy{
		ID: 36, Name: "US pool", BindingType: "proxy_ip_group",
		ProxyIPGroupID: &groupID, MemberCount: 3, PerIPConcurrency: 10,
	}
	router, svc := setupAdminRouter()
	svc.proxies = []service.Proxy{group}
	svc.proxyCounts = []service.ProxyWithAccountCount{{Proxy: group}}
	for _, path := range []string{
		"/api/v1/admin/proxies", "/api/v1/admin/proxies/all",
		"/api/v1/admin/proxies/all?with_count=true",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			data := body["data"]
			if page, ok := data.(map[string]any); ok {
				data = page["items"]
			}
			rows := data.([]any)
			require.Len(t, rows, 1)
			item := rows[0].(map[string]any)
			require.Equal(t, float64(36), item["id"])
			require.Equal(t, "proxy_ip_group", item["binding_type"])
			require.Equal(t, float64(12), item["proxy_ip_group_id"])
			require.NotContains(t, item, "password")
		})
	}
}
