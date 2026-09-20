package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type pluginHandlerReadRepo struct {
	service.PluginRepository
	err   error
	calls int
}

func (r *pluginHandlerReadRepo) GetByID(context.Context, int64) (*service.PluginInstallation, error) {
	r.calls++
	return &service.PluginInstallation{ID: 7, State: service.PluginStateDisabled}, r.err
}

type pluginHandlerDirectory struct {
	service.PluginAccountDirectory
}

func (*pluginHandlerDirectory) ListPluginResources(context.Context) (*service.PluginResources, error) {
	return &service.PluginResources{
		Accounts: []service.PluginResourceAccount{{ID: 7, Name: "account", Platform: "openai", AccountType: "setup-token", GroupIDs: []int64{3}}},
		Groups:   []service.PluginResourceGroup{{ID: 3, Name: "group"}},
		Proxies:  []service.PluginResourceProxy{{ID: 9, Name: "proxy", Protocol: "http", Host: "proxy.example", Port: 8080}},
	}, nil
}

func (*pluginHandlerDirectory) ResolvePluginProxy(context.Context, int64) (string, error) {
	panic("admin resources must not resolve credentials")
}

func newPluginHandlerTestRouter(repo *pluginHandlerReadRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.Plugins.MaxUploadBytes = 1024
	manager := service.NewPluginManager(repo, nil, cfg, service.PluginHostInfo{}, nil)
	manager.SetAccountDirectory(&pluginHandlerDirectory{})
	h := NewPluginHandler(manager)
	router := gin.New()
	router.GET("/plugins/:id/resources", h.Resources)
	router.POST("/plugins/:id/actions", h.RunAction)
	router.POST("/plugins/:id/upgrade", h.Upgrade)
	return router
}

func TestPluginHandlerResourcesReturnsOnlyPublicDirectory(t *testing.T) {
	repo := &pluginHandlerReadRepo{}
	router := newPluginHandlerTestRouter(repo)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plugins/7/resources", nil))
	require.Equal(t, http.StatusOK, response.Code)
	var envelope struct {
		Data service.PluginResources `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	require.Len(t, envelope.Data.Accounts, 1)
	require.Equal(t, []int64{3}, envelope.Data.Accounts[0].GroupIDs)
	require.Len(t, envelope.Data.Proxies, 1)
	for _, secret := range []string{"access_token", "refresh_token", "password", "username", "proxy_url", "credentials"} {
		require.NotContains(t, response.Body.String(), secret)
	}
	require.Equal(t, 1, repo.calls)
}

func TestPluginHandlerActionsValidateInputAndRefuseDisabledRuntime(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		status, reads int
	}{
		{"empty", "", http.StatusBadRequest, 0},
		{"trailing", `{"action_id":"id","name":"refresh","payload":{}} {}`, http.StatusBadRequest, 0},
		{"client revision", `{"action_id":"id","name":"refresh","payload":{},"config_revision":999}`, http.StatusBadRequest, 0},
		{"invalid id", `{"action_id":"bad/id","name":"refresh","payload":{}}`, http.StatusBadRequest, 0},
		{"null payload", `{"action_id":"id","name":"refresh","payload":null}`, http.StatusBadRequest, 0},
		{"large payload", `{"action_id":"id","name":"refresh","payload":{"x":"` + strings.Repeat("a", service.PluginActionMaxPayloadBytes) + `"}}`, http.StatusBadRequest, 0},
		{"disabled", `{"action_id":"id","name":"refresh","payload":{}}`, http.StatusConflict, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &pluginHandlerReadRepo{}
			router := newPluginHandlerTestRouter(repo)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/plugins/7/actions", strings.NewReader(tc.payload))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.Equal(t, tc.reads, repo.calls)
		})
	}
}

func TestPluginHandlerNewEndpointsDoNotReflectBackendErrors(t *testing.T) {
	router := newPluginHandlerTestRouter(&pluginHandlerReadRepo{err: errors.New("http://user:secret-password@proxy token=secret-token")})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/plugins/7/resources", nil))
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), "secret")
}

func TestPluginHandlerUpgradeRejectsInvalidPackageBeforeManager(t *testing.T) {
	for _, tc := range []struct {
		name, filename string
		size           int
	}{
		{"extension", "test.zip", 4}, {"size", "test.s2plugin", 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			file, err := writer.CreateFormFile("plugin", tc.filename)
			require.NoError(t, err)
			_, err = file.Write([]byte(strings.Repeat("a", tc.size)))
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			repo := &pluginHandlerReadRepo{}
			router := newPluginHandlerTestRouter(repo)
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/plugins/7/upgrade", &body)
			request.Header.Set("Content-Type", writer.FormDataContentType())
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusBadRequest, response.Code)
			require.Zero(t, repo.calls)
		})
	}
}
