//go:build unit

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingHandlerGPT61SolLongContextRoundTrip(t *testing.T) {
	const key = service.SettingKeyOpenAIGPT61SolNewLongContextEnabled
	for _, tt := range []struct {
		name    string
		initial map[string]string
		payload map[string]any
		want    bool
	}{
		{"absent defaults off", nil, map[string]any{}, false},
		{"omitted preserves enabled", map[string]string{key: "true"}, map[string]any{"site_name": "Other edit"}, true},
		{"omitted preserves disabled", map[string]string{key: "false"}, map[string]any{"site_name": "Other edit"}, false},
		{"enable", nil, map[string]any{key: true}, true},
		{"disable", map[string]string{key: "true"}, map[string]any{key: false}, false},
		{"invalid stored value stays off", map[string]string{key: "TRUE"}, map[string]any{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h, repo := newStepUpSwitchTestHandler(t, tt.initial)
			rec := doUpdateSettings(t, h, tt.payload, nil)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tt.want, repo.values[key] == "true")
			var response struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
			require.Equal(t, tt.want, response.Data[key])

			getRec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(getRec)
			c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
			h.GetSettings(c)
			require.Equal(t, http.StatusOK, getRec.Code)
			require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &response))
			require.Equal(t, tt.want, response.Data[key])
		})
	}
}

func TestSettingHandlerGPT61SolLongContextRejectsNonBoolean(t *testing.T) {
	const key = service.SettingKeyOpenAIGPT61SolNewLongContextEnabled
	for _, value := range []any{"true", 1} {
		h, repo := newStepUpSwitchTestHandler(t, map[string]string{key: "false"})
		rec := doUpdateSettings(t, h, map[string]any{key: value}, nil)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Equal(t, "false", repo.values[key])
	}
}

func TestSettingHandlerGPT61SolLongContextAuditAndPrivacy(t *testing.T) {
	const key = service.SettingKeyOpenAIGPT61SolNewLongContextEnabled
	before, after := &service.SystemSettings{}, &service.SystemSettings{}
	require.NotContains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), key)
	after.OpenAIGPT61SolNewLongContextEnabled = true
	require.Contains(t, diffSettings(before, after, nil, nil, UpdateSettingsRequest{}), key)
	for _, public := range []any{service.PublicSettings{}, dto.PublicSettings{}} {
		body, err := json.Marshal(public)
		require.NoError(t, err)
		require.NotContains(t, string(body), key)
	}
}
