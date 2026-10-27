package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestImageSelectionFailureKeepsOrdinaryUpstreamError(t *testing.T) {
	image := &service.UpstreamFailoverError{StatusCode: 403, Scope: service.GatewayFailureScopeAccountCapability, Reason: "image_permission_denied"}
	network := &service.UpstreamFailoverError{StatusCode: 502}
	for _, tt := range []struct {
		name           string
		previous, last *service.UpstreamFailoverError
		wantImage      bool
	}{
		{"hard intent alone is insufficient", nil, nil, false},
		{"confirmed image denial", nil, image, true},
		{"network followed by image denial", network, image, false},
		{"ordinary upstream failure", nil, network, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := service.WithOpenAIImageSelectionTrace(context.Background())
			ctx = service.WithOpenAIImageRequestDescriptor(ctx, service.OpenAIImageRequest{Explicit: true})
			service.RecordOpenAIImageFailover(ctx, tt.previous)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			h := &OpenAIGatewayHandler{}
			require.Equal(t, tt.wantImage, h.handleOpenAIImageSelectionExhausted(c, ctx, tt.last, false))
			if tt.wantImage {
				require.Equal(t, 503, rec.Code)
				require.Contains(t, rec.Body.String(), "image_generation_unavailable")
			} else if tt.last != nil {
				h.handleFailoverExhausted(c, tt.last, false)
				require.Equal(t, 502, rec.Code)
				require.NotContains(t, rec.Body.String(), "image_generation_unavailable")
			}
		})
	}
}

func TestImagePolicyLogsOnlyAllowlistedMetadata(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	log := zap.New(core)
	ctx := logger.IntoContext(context.Background(), log)
	body := []byte(`{"tools":[{"type":"image_generation"}],"previous_response_id":"resp_private","input":[{"type":"function_call_output","call_id":"call_private","output":"secret_prompt"}]}`)
	r := service.DescribeOpenAIImageRequest("/v1/responses", "text-model", body, false)
	logOpenAIIngressImagePolicy(log, r, &service.Group{AllowImageGeneration: false}, true)
	ctx = service.WithOpenAIImageRequestDescriptor(ctx, r)
	logOpenAIImageFailurePolicy(ctx, &service.UpstreamFailoverError{StatusCode: 502, ClientMessage: "secret_prompt"})
	require.Len(t, logs.All(), 2)
	allowed := map[string]bool{"source": true, "required": true, "native": true, "passive": true, "history": true, "unknown_history": true, "lite": true, "stripped": true, "strip_reason": true, "failure_category": true, "upstream_status": true, "image_filtered": true}
	for _, entry := range logs.All() {
		for key, value := range entry.ContextMap() {
			require.True(t, allowed[key], key)
			require.NotContains(t, fmt.Sprint(value), "resp_private")
			require.NotContains(t, fmt.Sprint(value), "call_private")
			require.NotContains(t, fmt.Sprint(value), "secret_prompt")
		}
	}
}
