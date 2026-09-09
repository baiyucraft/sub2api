package observer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCustomizationShortCircuitsFirstMatchingRule(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{
			{
				Name: "first", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
				Methods: []string{http.MethodGet}, ExactPaths: []string{"/v1/models"},
				StatusCode: http.StatusAccepted, ContentType: "application/json", Body: `{"first":true}`,
			},
			{
				Name: "second", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
				Methods: []string{http.MethodGet}, ExactPaths: []string{"/v1/models"},
				StatusCode: http.StatusTeapot, Body: `{"second":true}`,
			},
		},
	}))

	called := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.GET("/v1/models", func(c *gin.Context) {
		called = true
		c.String(http.StatusInternalServerError, "downstream")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	require.False(t, called)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, `{"first":true}`, recorder.Body.String())
	require.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
}

func TestCustomizationMatchesUserAndRequestConditionGroups(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "user-probe", Enabled: true, UserEmails: []string{"1069167864@qq.com"},
			Methods:      []string{http.MethodGet, http.MethodPost},
			PathPrefixes: []string{"/probe/"}, UserAgentContains: []string{"probe-client"},
			QueryParams: map[string][]string{"check": []string{"health", "ready"}},
			StatusCode:  http.StatusOK, ContentType: "text/plain", Body: "ready",
		}},
	}))

	called := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID: 8, UserID: 43, User: &service.User{ID: 43, Email: "1069167864@qq.com"},
		})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.Any("/probe/*path", func(c *gin.Context) {
		called = true
		c.String(http.StatusInternalServerError, "downstream")
	})

	request := httptest.NewRequest(http.MethodGet, "/probe/status?check=ready", nil)
	request.Header.Set("User-Agent", "probe-client/1.0")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	require.False(t, called)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "ready", recorder.Body.String())

	called = false
	request = httptest.NewRequest(http.MethodGet, "/probe/status?check=other", nil)
	request.Header.Set("User-Agent", "probe-client/1.0")
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.True(t, called)
	require.Equal(t, http.StatusInternalServerError, recorder.Code)
}

func TestCustomizationDisabledAndCanceledRequestDoNotShortCircuit(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	rule := service.GatewayChannelCustomizationRule{
		Name: "disabled", Enabled: false, APIKeyIDs: []int64{9},
		Methods: []string{http.MethodGet}, ExactPaths: []string{"/probe"}, Body: "ignored",
	}
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))

	called := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 9, Name: "probe"})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.GET("/probe", func(c *gin.Context) {
		called = true
		c.String(http.StatusOK, "ordinary")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/probe", strings.NewReader("body")))
	require.True(t, called)
	require.Equal(t, "ordinary", recorder.Body.String())

	rule.Enabled = true
	rule.MinDelayMs = 100
	rule.MaxDelayMs = 100
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/probe", nil).WithContext(ctx)
	recorder = httptest.NewRecorder()
	called = false
	router.ServeHTTP(recorder, request)
	require.False(t, called)
}
