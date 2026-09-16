package admin

import (
	"bytes"
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

type upstreamManagementSettingsHandlerRepoStub struct {
	values         map[string]string
	writes         int
	getMultipleErr error
}

func (r *upstreamManagementSettingsHandlerRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}

func (r *upstreamManagementSettingsHandlerRepoStub) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *upstreamManagementSettingsHandlerRepoStub) Set(_ context.Context, key, value string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[key] = value
	r.writes++
	return nil
}

func (r *upstreamManagementSettingsHandlerRepoStub) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	if r.getMultipleErr != nil {
		return nil, r.getMultipleErr
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		if value, ok := r.values[key]; ok {
			values[key] = value
		}
	}
	return values, nil
}

func (r *upstreamManagementSettingsHandlerRepoStub) SetMultiple(_ context.Context, values map[string]string) error {
	if r.values == nil {
		r.values = map[string]string{}
	}
	for key, value := range values {
		r.values[key] = value
	}
	r.writes++
	return nil
}

func (r *upstreamManagementSettingsHandlerRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}

func (r *upstreamManagementSettingsHandlerRepoStub) Delete(context.Context, string) error {
	return nil
}

func setupUpstreamManagementSettingsRouter(repo *upstreamManagementSettingsHandlerRepoStub) *gin.Engine {
	gin.SetMode(gin.TestMode)
	settingService := service.NewSettingService(repo, nil)
	upstreamService := service.NewUpstreamConfigService(nil, nil, nil)
	upstreamService.SetHealthProbeDependencies(nil, settingService)
	handler := NewUpstreamConfigHandler(upstreamService)
	router := gin.New()
	router.PUT("/admin/upstream/management-settings", handler.PutUpstreamManagementSettings)
	return router
}

func validUpstreamManagementSettingsBody() map[string]any {
	return map[string]any{
		"ttft_guard": map[string]any{
			"enabled": false, "degradation_ttft_seconds": 20, "min_samples": 5,
		},
		"probe_models": map[string]any{
			"openai": "gpt-test", "anthropic": "claude-test", "gemini": "gemini-test",
		},
		"probe_interval_seconds": 300,
	}
}

func putUpstreamManagementSettings(t *testing.T, router *gin.Engine, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/upstream/management-settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestPutUpstreamManagementSettingsPersistsAndReturnsSessionSwitchFields(t *testing.T) {
	repo := &upstreamManagementSettingsHandlerRepoStub{values: map[string]string{}}
	router := setupUpstreamManagementSettingsRouter(repo)
	body := validUpstreamManagementSettingsBody()
	body["session_switch_window_seconds"] = 120
	body["session_switch_failure_threshold"] = 5
	body["session_switch_cooldown_seconds"] = 900
	body["session_switch_status_codes"] = []int{503, 502, 503}

	recorder := putUpstreamManagementSettings(t, router, body)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Data service.UpstreamManagementSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, 120, payload.Data.SessionSwitchWindowSeconds)
	require.Equal(t, 5, payload.Data.SessionSwitchFailureThreshold)
	require.Equal(t, 900, payload.Data.SessionSwitchCooldownSeconds)
	require.Equal(t, []int{502, 503}, payload.Data.SessionSwitchStatusCodes)
	require.Equal(t, "120", repo.values[service.SettingKeySessionSwitchWindowSeconds])
	require.JSONEq(t, `[502,503]`, repo.values[service.SettingKeySessionSwitchStatusCodes])
}

func TestPutUpstreamManagementSettingsPreservesOmittedSessionSwitchFields(t *testing.T) {
	repo := &upstreamManagementSettingsHandlerRepoStub{values: map[string]string{
		service.SettingKeySessionSwitchWindowSeconds:    "90",
		service.SettingKeySessionSwitchFailureThreshold: "4",
		service.SettingKeySessionSwitchCooldownSeconds:  "450",
		service.SettingKeySessionSwitchStatusCodes:      `[503,502,503]`,
	}}
	router := setupUpstreamManagementSettingsRouter(repo)

	recorder := putUpstreamManagementSettings(t, router, validUpstreamManagementSettingsBody())

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var payload struct {
		Data service.UpstreamManagementSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, 90, payload.Data.SessionSwitchWindowSeconds)
	require.Equal(t, 4, payload.Data.SessionSwitchFailureThreshold)
	require.Equal(t, 450, payload.Data.SessionSwitchCooldownSeconds)
	require.Equal(t, []int{502, 503}, payload.Data.SessionSwitchStatusCodes)
}

func TestPutUpstreamManagementSettingsRejectsExplicitZeroSessionSwitchField(t *testing.T) {
	repo := &upstreamManagementSettingsHandlerRepoStub{values: map[string]string{}}
	router := setupUpstreamManagementSettingsRouter(repo)
	body := validUpstreamManagementSettingsBody()
	body["session_switch_window_seconds"] = 0

	recorder := putUpstreamManagementSettings(t, router, body)

	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
}

func TestPutUpstreamManagementSettingsRejectsEmptySessionSwitchStatusCodes(t *testing.T) {
	repo := &upstreamManagementSettingsHandlerRepoStub{values: map[string]string{}}
	router := setupUpstreamManagementSettingsRouter(repo)
	body := validUpstreamManagementSettingsBody()
	body["session_switch_status_codes"] = []int{}

	recorder := putUpstreamManagementSettings(t, router, body)

	require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	require.Zero(t, repo.writes)
}

func TestPutUpstreamManagementSettingsStopsWhenCurrentSettingsCannotBeRead(t *testing.T) {
	repo := &upstreamManagementSettingsHandlerRepoStub{
		values:         map[string]string{},
		getMultipleErr: errors.New("settings unavailable"),
	}
	router := setupUpstreamManagementSettingsRouter(repo)

	recorder := putUpstreamManagementSettings(t, router, validUpstreamManagementSettingsBody())

	require.GreaterOrEqual(t, recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	require.Zero(t, repo.writes)
}
