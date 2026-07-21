package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAITTFTGuardHandlerRepoStub struct {
	value string
}

func (r *openAITTFTGuardHandlerRepoStub) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *openAITTFTGuardHandlerRepoStub) GetValue(context.Context, string) (string, error) {
	if r.value == "" {
		return "", service.ErrSettingNotFound
	}
	return r.value, nil
}
func (r *openAITTFTGuardHandlerRepoStub) Set(_ context.Context, _ string, value string) error {
	r.value = value
	return nil
}
func (r *openAITTFTGuardHandlerRepoStub) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (r *openAITTFTGuardHandlerRepoStub) SetMultiple(context.Context, map[string]string) error {
	return nil
}
func (r *openAITTFTGuardHandlerRepoStub) GetAll(context.Context) (map[string]string, error) {
	return nil, nil
}
func (r *openAITTFTGuardHandlerRepoStub) Delete(context.Context, string) error { return nil }

func setupOpenAITTFTGuardSettingsRouter() (*gin.Engine, *openAITTFTGuardHandlerRepoStub) {
	gin.SetMode(gin.TestMode)
	repo := &openAITTFTGuardHandlerRepoStub{}
	handler := NewSettingHandler(service.NewSettingService(repo, nil), nil, nil, nil, nil, nil, nil)
	router := gin.New()
	router.GET("/admin/settings/openai-ttft-guard", handler.GetOpenAITTFTGuardSettings)
	router.PUT("/admin/settings/openai-ttft-guard", handler.UpdateOpenAITTFTGuardSettings)
	return router, repo
}

func TestSettingHandlerGetOpenAITTFTGuardSettingsReturnsDefaults(t *testing.T) {
	router, _ := setupOpenAITTFTGuardSettingsRouter()
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/settings/openai-ttft-guard", nil))

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Data service.OpenAITTFTGuardSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, 20, payload.Data.DegradationTTFTSeconds)
	require.Equal(t, 5, payload.Data.MinSamples)
	require.False(t, payload.Data.Enabled)
}

func TestSettingHandlerUpdateOpenAITTFTGuardSettingsPersistsAndReturnsValue(t *testing.T) {
	router, repo := setupOpenAITTFTGuardSettingsRouter()
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/admin/settings/openai-ttft-guard", bytes.NewBufferString(
		`{"enabled":true,"degradation_ttft_seconds":35,"min_samples":7}`,
	))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"enabled":true,"degradation_ttft_seconds":35,"min_samples":7}`, repo.value)
	var payload struct {
		Data service.OpenAITTFTGuardSettings `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Data.Enabled)
	require.Equal(t, 35, payload.Data.DegradationTTFTSeconds)
	require.Equal(t, 7, payload.Data.MinSamples)
}

func TestSettingHandlerUpdateOpenAITTFTGuardSettingsRejectsInvalidOrIncompleteInput(t *testing.T) {
	router, _ := setupOpenAITTFTGuardSettingsRouter()
	for _, body := range []string{
		`{"enabled":true,"degradation_ttft_seconds":4,"min_samples":5}`,
		`{"enabled":true,"degradation_ttft_seconds":20,"min_samples":21}`,
		`{"enabled":true,"degradation_ttft_seconds":20}`,
	} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/admin/settings/openai-ttft-guard", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, req)
		require.Equal(t, http.StatusBadRequest, recorder.Code, body)
	}
}
