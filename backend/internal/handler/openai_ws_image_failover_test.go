//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWSImageFailoverExhaustionPreservesOrdinaryFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	imageFailure := &service.UpstreamFailoverError{
		StatusCode: http.StatusForbidden,
		Scope:      service.GatewayFailureScopeAccountCapability,
		Reason:     service.OpenAIImagePermissionDeniedReason,
	}
	for _, tc := range []struct {
		name       string
		ordinary   *service.UpstreamFailoverError
		wantStatus int
		wantType   string
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "pure_image_permission",
			wantStatus: http.StatusServiceUnavailable, wantType: "server_error",
			wantCode: "image_generation_unavailable", wantMsg: "No image-capable upstream account is available",
		},
		{
			name:       "network_then_image_permission",
			ordinary:   &service.UpstreamFailoverError{StatusCode: http.StatusBadGateway, Reason: "network_error"},
			wantStatus: http.StatusBadGateway, wantType: "upstream_error",
			wantCode: "network_error", wantMsg: "upstream service temporarily unavailable",
		},
		{
			name:       "ordinary_auth_then_image_permission",
			ordinary:   &service.UpstreamFailoverError{StatusCode: http.StatusForbidden, Reason: "authentication_failed"},
			wantStatus: http.StatusForbidden, wantType: "authentication_error",
			wantCode: "authentication_failed", wantMsg: "upstream websocket authentication failed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := service.WithOpenAIImageSelectionTrace(context.Background())
			service.RecordOpenAIImageFailover(ctx, tc.ordinary)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil).WithContext(ctx)

			(&OpenAIGatewayHandler{}).closeOpenAIWSFailoverExhausted(c, nil, imageFailure)

			failure, ok := service.GetOpsStreamError(c)
			require.True(t, ok)
			require.Equal(t, tc.wantStatus, failure.IntendedStatus)
			require.Equal(t, tc.wantType, failure.ErrType)
			require.Equal(t, tc.wantCode, failure.Code)
			require.Equal(t, tc.wantMsg, failure.Message)
			require.True(t, failure.CountTowardsSLA)
		})
	}
}
