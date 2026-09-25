package observer

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCustomizationModelMappingFirstMatchAndCombination(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		group        bool
	}{
		{"model only", service.GatewayChannelCustomizationActionModelMapping, false},
		{"group and model", service.GatewayChannelCustomizationActionGroupMapping, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := &service.Group{ID: 1, Name: "original", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			target := &service.Group{ID: 2, Name: "target", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
			key := &service.APIKey{ID: 3, Name: "test-key", UserID: 4, User: &service.User{ID: 4}, GroupID: &original.ID, Group: original}
			rule := service.GatewayChannelCustomizationRule{Name: "first", Enabled: true, Action: tc.action, APIKeyNames: []string{"test-key"}, Models: []string{"A"}, TargetModel: "B"}
			if tc.group {
				rule.TargetGroupID = &target.ID
			}
			later := rule
			later.Name, later.TargetModel = "second", "D"
			customization := NewCustomizationService()
			require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule, later}}))
			resolver := &customizationTargetResolverStub{group: target}
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), key); c.Next() })
			router.Use(customization.GroupMappingMiddleware(resolver, nil))
			router.Use(func(c *gin.Context) {
				// The actual group allowlist has to see A, never the target model B.
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), `"model":"A"`)
				c.Request.Body = io.NopCloser(strings.NewReader(string(body)))
				c.Next()
			})
			var admittedGroup int64
			var channelLookupModel string
			router.Use(customization.ModelMappingMiddleware(func(_ context.Context, apiKey *service.APIKey, model string) bool {
				admittedGroup = *apiKey.GroupID
				return model == "A"
			}, func(_ context.Context, _ *service.APIKey, model string) string {
				channelLookupModel = model
				return "C"
			}))
			router.POST("/v1/responses", func(c *gin.Context) {
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				require.Contains(t, string(body), `"model":"B"`)
				mapped, ok := service.ChannelCustomizationModelFromContext(c.Request.Context())
				require.True(t, ok)
				require.Equal(t, service.ChannelCustomizationModel{Original: "A", Target: "B"}, mapped)
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"A","input":"hi"}`))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			require.Equal(t, http.StatusNoContent, response.Code)
			require.Equal(t, "B", channelLookupModel)
			if tc.group {
				require.Equal(t, target.ID, admittedGroup)
			} else {
				require.Equal(t, original.ID, admittedGroup)
			}
			require.Equal(t, []int64{1, 0}, customization.HitCounts([]service.GatewayChannelCustomizationRule{rule, later}))
		})
	}
}

func TestCustomizationModelMappingRejectsAmbiguousOrUnpriced(t *testing.T) {
	key := &service.APIKey{ID: 3, Name: "test-key", GroupID: func() *int64 { id := int64(1); return &id }()}
	rule := service.GatewayChannelCustomizationRule{Name: "map", Enabled: true, Action: service.GatewayChannelCustomizationActionModelMapping, APIKeyNames: []string{"test-key"}, Models: []string{"A"}, TargetModel: "B"}
	customization := NewCustomizationService()
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	for _, tc := range []struct {
		name, body string
		priced     bool
		code       int
	}{
		{"duplicate", `{"model":"A","model":"C"}`, true, http.StatusBadRequest},
		{"case variant", `{"model":"A","Model":"C"}`, true, http.StatusBadRequest},
		{"unpriced", `{"model":"A"}`, false, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), key); c.Next() })
			router.Use(customization.GroupMappingMiddleware(nil, nil))
			router.Use(customization.ModelMappingMiddleware(func(_ context.Context, _ *service.APIKey, _ string) bool { return tc.priced }))
			router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.code, response.Code)
		})
	}
}

func TestCustomizationWebSocketTurnMappingKeepsConnectionGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initial := &service.Group{ID: 1, Name: "initial", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	group := &service.Group{ID: 2, Name: "mapped", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	otherID := int64(3)
	key := &service.APIKey{ID: 7, Name: "test-key", UserID: 8, User: &service.User{ID: 8}, GroupID: &initial.ID, Group: initial}
	first := service.GatewayChannelCustomizationRule{Name: "first", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &group.ID, TargetModel: "B", APIKeyNames: []string{"test-key"}, Models: []string{"A"}}
	second := service.GatewayChannelCustomizationRule{Name: "second", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &otherID, TargetModel: "Y", APIKeyNames: []string{"test-key"}, Models: []string{"X"}}
	customization := NewCustomizationService()
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{first, second}}))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	ctx.Set(string(middleware.ContextKeyAPIKey), key)
	resolver := &customizationTargetResolverStub{group: group}
	hook := &WebSocketTurnCustomizer{service: customization, groups: resolver, pricing: func(_ context.Context, _ *service.APIKey, model string) bool { return model == "A" || model == "X" }}
	mapped, changed, err := hook.ApplyTurn(ctx, []byte(`{"type":"response.create","model":"A"}`), "A", true)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "B", mapped)
	selected, _ := middleware.GetAPIKeyFromContext(ctx)
	require.Equal(t, group.ID, *selected.GroupID)
	_, _, err = hook.ApplyTurn(ctx, []byte(`{"type":"response.create","model":"X"}`), "X", false)
	require.ErrorContains(t, err, "reconnect")
	selected, _ = middleware.GetAPIKeyFromContext(ctx)
	require.Equal(t, group.ID, *selected.GroupID)
	require.Equal(t, []int64{1, 0}, customization.HitCounts([]service.GatewayChannelCustomizationRule{first, second}))
}

func TestCustomizationWebSocketGroupMappingCountsOnlyFirstTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	initial := &service.Group{ID: 1, Name: "initial", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	target := &service.Group{ID: 2, Name: "target", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	key := &service.APIKey{ID: 7, Name: "test-key", UserID: 8, User: &service.User{ID: 8}, GroupID: &initial.ID, Group: initial}
	rule := service.GatewayChannelCustomizationRule{Name: "group", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &target.ID, APIKeyNames: []string{"test-key"}, ExactPaths: []string{"/v1/responses"}}
	customization := NewCustomizationService()
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(string(middleware.ContextKeyAPIKey), key); c.Next() })
	router.Use(customization.GroupMappingMiddleware(&customizationTargetResolverStub{group: target}, nil))
	router.GET("/v1/responses", func(c *gin.Context) {
		selected, _ := middleware.GetAPIKeyFromContext(c)
		require.Equal(t, initial.ID, *selected.GroupID)
		customizer := WebSocketTurnCustomizerFromContext(c)
		require.NotNil(t, customizer)
		_, _, err := customizer.ApplyTurn(c, []byte(`{"type":"response.create","model":"A"}`), "A", true)
		require.NoError(t, err)
		selected, _ = middleware.GetAPIKeyFromContext(c)
		require.Equal(t, target.ID, *selected.GroupID)
		c.Status(http.StatusNoContent)
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
	require.Equal(t, http.StatusNoContent, response.Code)
	require.Equal(t, []int64{1}, customization.HitCounts([]service.GatewayChannelCustomizationRule{rule}))
}
