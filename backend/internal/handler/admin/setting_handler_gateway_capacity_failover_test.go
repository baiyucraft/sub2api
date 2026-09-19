package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type gatewayCapacityHandlerSettingRepo struct {
	value string
}

func (r *gatewayCapacityHandlerSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	if r.value == "" {
		return nil, service.ErrSettingNotFound
	}
	return &service.Setting{Value: r.value}, nil
}
func (r *gatewayCapacityHandlerSettingRepo) GetValue(context.Context, string) (string, error) {
	if r.value == "" {
		return "", service.ErrSettingNotFound
	}
	return r.value, nil
}
func (r *gatewayCapacityHandlerSettingRepo) Set(_ context.Context, _, value string) error {
	r.value = value
	return nil
}
func (r *gatewayCapacityHandlerSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *gatewayCapacityHandlerSettingRepo) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *gatewayCapacityHandlerSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *gatewayCapacityHandlerSettingRepo) Delete(context.Context, string) error { return nil }

func newGatewayCapacitySettingsHandler(repo service.SettingRepository) *SettingHandler {
	cfg := &config.Config{Gateway: config.GatewayConfig{Scheduling: config.GatewaySchedulingConfig{
		CapacityFailoverEnabled:             false,
		CapacityFailoverMaxSwitches:         10,
		CapacityFailoverExhaustedStatusCode: http.StatusServiceUnavailable,
	}}}
	return NewSettingHandler(service.NewSettingService(repo, cfg), nil, nil, nil, nil, nil, nil)
}

func TestGatewayCapacityFailoverSettingsHandlerGetFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/settings/gateway-capacity-failover", nil)

	newGatewayCapacitySettingsHandler(&gatewayCapacityHandlerSettingRepo{}).GetGatewayCapacityFailoverSettings(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.False(t, gjson.Get(w.Body.String(), "data.enabled").Bool())
	require.Equal(t, int64(10), gjson.Get(w.Body.String(), "data.max_switches").Int())
	require.Equal(t, int64(503), gjson.Get(w.Body.String(), "data.exhausted_status_code").Int())
}

func TestGatewayCapacityFailoverSettingsHandlerUpdate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &gatewayCapacityHandlerSettingRepo{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPut, "/admin/settings/gateway-capacity-failover", bytes.NewBufferString(
		`{"enabled":true,"max_switches":0,"exhausted_status_code":429}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")

	newGatewayCapacitySettingsHandler(repo).UpdateGatewayCapacityFailoverSettings(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"enabled":true,"max_switches":0,"exhausted_status_code":429}`, repo.value)
}

func TestGatewayCapacityFailoverSettingsHandlerRejectsInvalidInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"enabled":true,"max_switches":1001,"exhausted_status_code":503}`,
		`{"enabled":true,"max_switches":3,"exhausted_status_code":399}`,
		`{"enabled":true,"max_switches":3}`,
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPut, "/admin/settings/gateway-capacity-failover", bytes.NewBufferString(body))
		c.Request.Header.Set("Content-Type", "application/json")

		newGatewayCapacitySettingsHandler(&gatewayCapacityHandlerSettingRepo{}).UpdateGatewayCapacityFailoverSettings(c)
		require.Equal(t, http.StatusBadRequest, w.Code, body)
	}
}
