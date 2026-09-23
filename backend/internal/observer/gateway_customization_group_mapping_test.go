package observer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type customizationTargetResolverStub struct {
	group *service.Group
	err   error
	calls int
}

type customizationSubscriptionResolverStub struct {
	subscription     *service.UserSubscription
	maintained       *service.UserSubscription
	getErr           error
	validateErr      error
	maintenanceErr   error
	needsMaintenance bool
	getCalls         int
	validateCalls    int
	maintenanceCalls int
}

func (s *customizationSubscriptionResolverStub) GetActiveSubscription(context.Context, int64, int64) (*service.UserSubscription, error) {
	s.getCalls++
	return s.subscription, s.getErr
}

func (s *customizationSubscriptionResolverStub) ValidateAndCheckLimits(*service.UserSubscription, *service.Group) (bool, error) {
	s.validateCalls++
	return s.needsMaintenance, s.validateErr
}

func (s *customizationSubscriptionResolverStub) EnsureWindowMaintenance(context.Context, *service.UserSubscription) (*service.UserSubscription, error) {
	s.maintenanceCalls++
	return s.maintained, s.maintenanceErr
}

func (s *customizationTargetResolverStub) ResolveCustomizationTargetGroup(context.Context, *service.APIKey, int64) (*service.Group, error) {
	s.calls++
	return s.group, s.err
}

func TestCustomizationGroupMappingOverridesOnlyRequestContextAndReplaysBody(t *testing.T) {
	targetGroupID := int64(22)
	originalGroup := &service.Group{ID: 11, Name: "ordinary", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	targetGroup := &service.Group{ID: targetGroupID, Name: "gpt-pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	apiKey := &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42, User: &service.User{ID: 42}, GroupID: &originalGroup.ID, Group: originalGroup}
	resolver := &customizationTargetResolverStub{group: targetGroup}
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-to-pro", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"},
		RequestMessageText: "create animation",
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	const body = `{"input":"  create animation  "}`
	var downstreamBody string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Group, originalGroup))
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(resolver, nil))
	router.Use(customization.LocalResponseMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) {
		mapped, ok := middleware.GetAPIKeyFromContext(c)
		require.True(t, ok)
		require.NotSame(t, apiKey, mapped)
		require.Equal(t, targetGroupID, *mapped.GroupID)
		require.Same(t, targetGroup, mapped.Group)
		require.Same(t, targetGroup, c.Request.Context().Value(ctxkey.Group))
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		downstreamBody = string(raw)
		c.String(http.StatusOK, "mapped")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body)))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "mapped", recorder.Body.String())
	require.Equal(t, body, downstreamBody)
	require.Equal(t, int64(11), *apiKey.GroupID)
	require.Same(t, originalGroup, apiKey.Group)
	require.Equal(t, 1, resolver.calls)
	require.Equal(t, []int64{1}, customization.HitCounts([]service.GatewayChannelCustomizationRule{rule}))
}

func TestCustomizationGroupMappingMatchesRequestModelWithoutMessageCondition(t *testing.T) {
	targetGroupID := int64(22)
	targetGroup := &service.Group{ID: targetGroupID, Name: "gpt-pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-astra", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, Models: []string{"gpt-6-astra"},
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	tests := []struct {
		name         string
		apiKey       string
		body         string
		mapped       bool
		expectedBody string
	}{
		{name: "exact model", apiKey: "maibon-gpt", body: `{"model":"gpt-6-astra","input":"anything"}`, mapped: true},
		{name: "case and whitespace insensitive", apiKey: "maibon-gpt", body: `{"model":"  GPT-6-ASTRA ","input":"anything"}`, mapped: true},
		{name: "different model", apiKey: "maibon-gpt", body: `{"model":"gpt-5.6-luna","input":"anything"}`},
		{name: "missing model", apiKey: "maibon-gpt", body: `{"input":"anything"}`},
		{name: "different key", apiKey: "other-key", body: `{"model":"gpt-6-astra","input":"anything"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &customizationTargetResolverStub{group: targetGroup}
			var downstreamBody string
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: tt.apiKey, UserID: 42})
				c.Next()
			})
			router.Use(customization.GroupMappingMiddleware(resolver, nil))
			router.POST("/v1/responses", func(c *gin.Context) {
				raw, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				downstreamBody = string(raw)
				c.String(http.StatusOK, "downstream")
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tt.body)))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, tt.body, downstreamBody)
			if tt.mapped {
				require.Equal(t, 1, resolver.calls)
			} else {
				require.Zero(t, resolver.calls)
			}
		})
	}
}

func TestCustomizationGroupMappingModelAndMessageConditionsUseAND(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-astra-animation", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, Models: []string{"gpt-6-astra"},
		RequestMessageText: "create animation",
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	for _, tt := range []struct {
		name   string
		body   string
		mapped bool
	}{
		{name: "both conditions match", body: `{"model":"gpt-6-astra","input":"create animation"}`, mapped: true},
		{name: "model matches only", body: `{"model":"gpt-6-astra","input":"other"}`},
		{name: "message matches only", body: `{"model":"gpt-5.6-luna","input":"create animation"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &customizationTargetResolverStub{group: &service.Group{ID: targetGroupID, Name: "gpt-pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true}}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", UserID: 42})
				c.Next()
			})
			router.Use(customization.GroupMappingMiddleware(resolver, nil))
			router.POST("/v1/responses", func(c *gin.Context) { c.Status(http.StatusNoContent) })
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(tt.body)))
			if tt.mapped {
				require.Equal(t, 1, resolver.calls)
			} else {
				require.Zero(t, resolver.calls)
			}
		})
	}
}

func TestCustomizationMayMatchGroupMappingUsesRequestMetadataWithoutReadingBody(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{
		{Name: "local", Enabled: true, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"}, StatusCode: http.StatusOK},
		{Name: "map", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"}, RequestMessageText: "create animation"},
	}}))

	const body = `{"input":"  create animation  "}`
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	require.True(t, customization.MayMatchGroupMapping(c))
	replayed, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	require.Equal(t, body, string(replayed))

	nonMatching, _ := gin.CreateTestContext(httptest.NewRecorder())
	nonMatching.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	require.False(t, customization.MayMatchGroupMapping(nonMatching))
}

func TestCustomizationFirstMatchingLocalResponsePreventsLaterGroupMapping(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{
		{Name: "first-local", Enabled: true, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"}, StatusCode: http.StatusAccepted, Body: "local"},
		{Name: "later-map", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"}},
	}}))
	resolver := &customizationTargetResolverStub{group: &service.Group{ID: targetGroupID, Status: service.StatusActive, Hydrated: true, Platform: service.PlatformOpenAI}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(resolver, nil))
	router.Use(customization.LocalResponseMiddleware())
	router.POST("/v1/responses", func(c *gin.Context) { c.String(http.StatusOK, "downstream") })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, "local", recorder.Body.String())
	require.Zero(t, resolver.calls)
}

func TestCustomizationGroupMappingRejectsInvalidTargetWithoutFallback(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{Name: "map", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"}}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	resolver := &customizationTargetResolverStub{err: service.ErrGroupNotAllowed}
	downstreamCalled := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(resolver, nil))
	router.POST("/v1/responses", func(c *gin.Context) { downstreamCalled = true })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.False(t, downstreamCalled)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "CUSTOMIZATION_TARGET_GROUP_NOT_ALLOWED")
	require.Equal(t, []int64{1}, customization.HitCounts([]service.GatewayChannelCustomizationRule{rule}))
}

func TestCustomizationGroupMappingUsesGoogleErrorEnvelope(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{Name: "map", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1beta/models/gemini:generateContent"}}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(&customizationTargetResolverStub{err: service.ErrGroupNotAllowed}, nil))
	router.POST("/v1beta/models/gemini:generateContent", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code)
	var payload struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.Equal(t, http.StatusForbidden, payload.Error.Code)
	require.NotEmpty(t, payload.Error.Status)
	require.Contains(t, payload.Error.Message, "not allowed")
}

func TestCustomizationBillingEndpointDoesNotApplyGroupMapping(t *testing.T) {
	targetGroupID := int64(22)
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{Name: "map", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping, TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/sub2api/billing"}}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	resolver := &customizationTargetResolverStub{group: &service.Group{ID: targetGroupID, Status: service.StatusActive, Hydrated: true, Platform: service.PlatformOpenAI}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(resolver, nil))
	router.GET("/v1/sub2api/billing", func(c *gin.Context) { c.String(http.StatusOK, "original") })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/sub2api/billing", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "original", recorder.Body.String())
	require.Zero(t, resolver.calls)
	require.Equal(t, []int64{0}, customization.HitCounts([]service.GatewayChannelCustomizationRule{rule}))
}

func TestCustomizationGroupMappingSubscriptionTargetLoadsActiveSubscription(t *testing.T) {
	targetGroupID := int64(22)
	target := &service.Group{
		ID: targetGroupID, Name: "subscription-pro", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeSubscription,
	}
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-subscription", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"},
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	initial := &service.UserSubscription{ID: 101, UserID: 42, GroupID: targetGroupID}
	subscriptions := &customizationSubscriptionResolverStub{subscription: initial}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", UserID: 42, User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(&customizationTargetResolverStub{group: target}, subscriptions))
	router.POST("/v1/responses", func(c *gin.Context) {
		subscription, ok := middleware.GetSubscriptionFromContext(c)
		require.True(t, ok)
		require.Same(t, initial, subscription)
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Equal(t, 1, subscriptions.getCalls)
	require.Zero(t, subscriptions.validateCalls)
	require.Zero(t, subscriptions.maintenanceCalls)
}

func TestCustomizationGroupMappingSubscriptionTargetRejectsUnavailableSubscription(t *testing.T) {
	targetGroupID := int64(22)
	target := &service.Group{
		ID: targetGroupID, Name: "subscription-pro", Platform: service.PlatformOpenAI,
		Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeSubscription,
	}
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-subscription", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"},
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	downstreamCalled := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", UserID: 42, User: &service.User{ID: 42}})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(&customizationTargetResolverStub{group: target}, &customizationSubscriptionResolverStub{getErr: errors.New("not found")}))
	router.POST("/v1/responses", func(c *gin.Context) { downstreamCalled = true })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.False(t, downstreamCalled)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "CUSTOMIZATION_TARGET_SUBSCRIPTION_INVALID")
}

func TestCustomizationGroupMappingStandardTargetClearsOriginalSubscriptionContext(t *testing.T) {
	targetGroupID := int64(22)
	target := &service.Group{ID: targetGroupID, Name: "standard-pro", Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard}
	customization := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "map-standard", Enabled: true, Action: service.GatewayChannelCustomizationActionGroupMapping,
		TargetGroupID: &targetGroupID, APIKeyNames: []string{"maibon-gpt"}, ExactPaths: []string{"/v1/responses"},
	}
	require.NoError(t, customization.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt", UserID: 42, User: &service.User{ID: 42}})
		c.Set(string(middleware.ContextKeySubscription), &service.UserSubscription{ID: 99})
		c.Next()
	})
	router.Use(customization.GroupMappingMiddleware(&customizationTargetResolverStub{group: target}, nil))
	router.POST("/v1/responses", func(c *gin.Context) {
		_, ok := middleware.GetSubscriptionFromContext(c)
		require.False(t, ok)
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", nil))
	require.Equal(t, http.StatusNoContent, recorder.Code)
}

func TestCustomizationTargetCompatibleHonorsGatewayEntryPlatform(t *testing.T) {
	group := func(platform string) *service.Group {
		return &service.Group{ID: 22, Platform: platform, Status: service.StatusActive, Hydrated: true}
	}
	contextForPath := func(path string) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		return c
	}

	require.False(t, customizationTargetCompatible(nil, group(service.PlatformOpenAI)))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, customizationTargetCompatible(c, group(service.PlatformOpenAI)))
	require.False(t, customizationTargetCompatible(contextForPath("/v1beta/models/gemini:generateContent"), group(service.PlatformOpenAI)))
	require.True(t, customizationTargetCompatible(contextForPath("/v1beta/models/gemini:generateContent"), group(service.PlatformGemini)))
	require.True(t, customizationTargetCompatible(contextForPath("/v1beta/models/gemini:generateContent"), group(service.PlatformComposite)))
	require.False(t, customizationTargetCompatible(contextForPath("/antigravity/v1/messages"), group(service.PlatformOpenAI)))
	require.True(t, customizationTargetCompatible(contextForPath("/antigravity/v1/messages"), group(service.PlatformAntigravity)))
	require.True(t, customizationTargetCompatible(contextForPath("/antigravity/v1/messages"), group(service.PlatformComposite)))

	forced := contextForPath("/v1/responses")
	forced.Set(string(middleware.ContextKeyForcePlatform), service.PlatformAntigravity)
	require.False(t, customizationTargetCompatible(forced, group(service.PlatformOpenAI)))
	require.True(t, customizationTargetCompatible(forced, group(service.PlatformAntigravity)))
	require.True(t, customizationTargetCompatible(forced, group(service.PlatformComposite)))
}
