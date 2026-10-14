package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type upstreamManagementPlatformsSettingRepo struct {
	service.SettingRepository
	values map[string]string
	writes int
}

func (r *upstreamManagementPlatformsSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	value, ok := r.values[key]
	if !ok {
		return "", service.ErrSettingNotFound
	}
	return value, nil
}

func (r *upstreamManagementPlatformsSettingRepo) Set(_ context.Context, key, value string) error {
	r.writes++
	r.values[key] = value
	return nil
}

func (r *upstreamManagementPlatformsSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	r.writes++
	for key, value := range values {
		r.values[key] = value
	}
	return nil
}

func TestUpstreamManagementSchedulingPlatformsHTTPDistinguishesOmissionAndEmptyAndProtectsProbeAlias(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, path, extra, want string
		status                  int
	}{
		{"omitted", "/settings", "", `["openai"]`, http.StatusOK},
		{"explicit empty", "/settings", `,"scheduling_protection_platforms":[]`, `[]`, http.StatusOK},
		{"normalized", "/settings", `,"scheduling_protection_platforms":[" OPENAI ","anthropic","openai"]`, `["anthropic","openai"]`, http.StatusOK},
		{"composite rejected", "/settings", `,"scheduling_protection_platforms":["composite"]`, `["openai"]`, http.StatusBadRequest},
		{"unknown rejected", "/settings", `,"scheduling_protection_platforms":["unknown"]`, `["openai"]`, http.StatusBadRequest},
		{"probe alias preserves", "/probe-settings", `,"scheduling_protection_platforms":[]`, `["openai"]`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &upstreamManagementPlatformsSettingRepo{values: map[string]string{service.SettingKeySchedulingProtectionPlatforms: `["openai"]`}}
			svc := service.NewUpstreamConfigService(nil, nil, nil)
			svc.SetHealthProbeDependencies(nil, service.NewSettingService(repo, nil))
			h := NewUpstreamConfigHandler(svc)
			router := gin.New()
			router.PUT("/settings", h.PutUpstreamManagementSettings)
			router.PUT("/probe-settings", h.PutUpstreamManagementSettings)
			body := `{"ttft_guard":{"enabled":false,"degradation_ttft_seconds":20,"min_samples":5},"probe_models":{"openai":"gpt-5.4-mini","anthropic":"claude-sonnet-4-5","gemini":"gemini-2.5-flash"},"probe_interval_seconds":300` + tc.extra + `}`
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
			require.JSONEq(t, tc.want, repo.values[service.SettingKeySchedulingProtectionPlatforms])
			var payload struct {
				Reason string `json:"reason"`
				Data   struct {
					Platforms []string `json:"scheduling_protection_platforms"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			if tc.status == http.StatusBadRequest {
				require.Zero(t, repo.writes, "reject invalid platforms before any management settings write")
				require.Equal(t, "INVALID_SCHEDULING_PROTECTION_PLATFORMS", payload.Reason)
			} else {
				require.NotNil(t, payload.Data.Platforms)
				platforms, err := json.Marshal(payload.Data.Platforms)
				require.NoError(t, err)
				require.JSONEq(t, tc.want, string(platforms))
			}
		})
	}
}
