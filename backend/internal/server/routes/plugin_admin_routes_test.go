package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestPluginAdminRoutesAuthenticationAndStepUp(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handlers := &handler.Handlers{Admin: &handler.AdminHandlers{Plugin: adminhandler.NewPluginHandler(nil)}}
	stepUpCalls := 0
	adminAuth := middleware.AdminAuthMiddleware(func(c *gin.Context) {
		if c.GetHeader("Authorization") != "admin" {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		c.Next()
	})
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusForbidden)
	})
	RegisterAdminRoutes(router.Group("/api/v1"), handlers, adminAuth, middleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }), stepUp, nil, nil)
	for _, tc := range []struct {
		method, path  string
		status, steps int
	}{
		{http.MethodGet, "/api/v1/admin/plugins/0/resources", http.StatusBadRequest, 0},
		{http.MethodPost, "/api/v1/admin/plugins/7/actions", http.StatusForbidden, 1},
		{http.MethodPost, "/api/v1/admin/plugins/7/upgrade", http.StatusForbidden, 1},
	} {
		t.Run(tc.path, func(t *testing.T) {
			for _, authorized := range []bool{false, true} {
				stepUpCalls = 0
				request := httptest.NewRequest(tc.method, tc.path, nil)
				if authorized {
					request.Header.Set("Authorization", "admin")
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if authorized {
					require.Equal(t, tc.status, response.Code)
					require.Equal(t, tc.steps, stepUpCalls)
				} else {
					require.Equal(t, http.StatusUnauthorized, response.Code)
					require.Zero(t, stepUpCalls)
				}
			}
		})
	}
	for _, route := range router.Routes() {
		require.NotEqual(t, "/api/v1/admin/accounts/:id/codex-ticket", route.Path)
		require.NotEqual(t, "/api/v1/admin/accounts/:id/codex-ticket/harvest", route.Path)
	}
}

func TestPluginPackageWriteAuthAllowsAdminAPIKeyOnlyForPackageLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stepUpCalls := 0
	stepUp := middleware.StepUpAuthMiddleware(func(c *gin.Context) {
		stepUpCalls++
		c.AbortWithStatus(http.StatusForbidden)
	})

	for _, tc := range []struct {
		name          string
		packageWrite  bool
		authMethod    string
		wantStatus    int
		wantStepCalls int
	}{
		{name: "admin key package write", packageWrite: true, authMethod: "admin_api_key", wantStatus: http.StatusNoContent},
		{name: "jwt package write", packageWrite: true, authMethod: "jwt", wantStatus: http.StatusForbidden, wantStepCalls: 1},
		{name: "admin key ordinary sensitive write", authMethod: "admin_api_key", wantStatus: http.StatusForbidden, wantStepCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stepUpCalls = 0
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("auth_method", tc.authMethod)
				c.Next()
			})
			auth := gin.HandlerFunc(stepUp)
			if tc.packageWrite {
				auth = pluginPackageWriteAuth(stepUp)
			}
			router.POST("/write", auth, func(c *gin.Context) { c.Status(http.StatusNoContent) })

			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/write", nil)
			router.ServeHTTP(response, request)

			require.Equal(t, tc.wantStatus, response.Code)
			require.Equal(t, tc.wantStepCalls, stepUpCalls)
		})
	}
}
