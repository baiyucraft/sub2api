package observer

import (
	"context"
	"encoding/json"
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

func TestCustomizationLongExactMessageKeepsFixedResponseAndScope(t *testing.T) {
	question := strings.Repeat("完整题目中的条件必须保持一致。\n", 20) + "苹果 7 桃子 6 西瓜 4"
	rule := service.GatewayChannelCustomizationRule{
		Name: "long-exact", Enabled: true, UserIDs: []int64{27},
		Methods: []string{http.MethodPost}, ExactPaths: []string{"/v1/responses"}, Models: []string{"gpt-6.1-sol"},
		RequestMessageText: question, StatusCode: http.StatusOK, ContentType: "text/plain", Body: "21",
	}
	customizer := NewCustomizationService()
	require.NoError(t, customizer.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{rule}}))
	for _, tc := range []struct {
		name  string
		user  int64
		model string
		input any
		path  string
		hit   bool
	}{
		{"full text", 27, "gpt-6.1-sol", question, "/v1/responses", true},
		{"edge whitespace", 27, "gpt-6.1-sol", "\n" + question + "\n", "/v1/responses", true},
		{"changed number", 27, "gpt-6.1-sol", strings.Replace(question, "苹果 7", "苹果 8", 1), "/v1/responses", false},
		{"changed internal whitespace", 27, "gpt-6.1-sol", strings.Replace(question, "\n", " ", 1), "/v1/responses", false},
		{"other user", 28, "gpt-6.1-sol", question, "/v1/responses", false},
		{"other model", 27, "gpt-6-sol", question, "/v1/responses", false},
		{"other path", 27, "gpt-6.1-sol", question, "/v1/chat/completions", false},
		{"multiple user texts", 27, "gpt-6.1-sol", []string{question, "other question"}, "/v1/responses", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"model": tc.model, "input": tc.input})
			require.NoError(t, err)
			var downstreamBody string
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, UserID: tc.user})
				c.Next()
			}, customizer.Middleware())
			router.POST(tc.path, func(c *gin.Context) {
				raw, readErr := io.ReadAll(c.Request.Body)
				require.NoError(t, readErr)
				downstreamBody = string(raw)
				c.String(http.StatusOK, "ordinary")
			})
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if tc.hit {
				require.Equal(t, "21", recorder.Body.String())
				require.Empty(t, downstreamBody)
			} else {
				require.Equal(t, "ordinary", recorder.Body.String())
				require.Equal(t, string(body), downstreamBody)
			}
		})
	}
}

func TestCustomizationMatchesExactMessageTextAcrossRequestShapes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "responses input string", body: "{\"input\":\"hi\"}"},
		{name: "anthropic messages", body: "{\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"},
		{name: "gemini contents", body: "{\"contents\":[{\"parts\":[{\"text\":\"hi\"}]}]}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serviceUnderTest := NewCustomizationService()
			require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
				Rules: []service.GatewayChannelCustomizationRule{{
					Name: "message-probe", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
					RequestMessageText: "hi", StatusCode: http.StatusAccepted, ContentType: "text/plain", Body: "short-circuited",
				}},
			}))

			called := false
			var downstreamBody string
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
				c.Next()
			})
			router.Use(serviceUnderTest.Middleware())
			router.POST("/probe", func(c *gin.Context) {
				called = true
				raw, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				downstreamBody = string(raw)
				c.String(http.StatusOK, "downstream")
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(tt.body)))

			require.False(t, called)
			require.Equal(t, http.StatusAccepted, recorder.Code)
			require.Equal(t, "short-circuited", recorder.Body.String())
			require.Empty(t, downstreamBody)
		})
	}
}

func TestCustomizationLegacyMessageModeDefaultsToExact(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "legacy-exact", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
			RequestMessageText: "hi", StatusCode: http.StatusAccepted, Body: "short-circuited",
		}},
	}))

	settings, err := serviceUnderTest.Settings(context.Background())
	require.NoError(t, err)
	require.Equal(t, service.GatewayChannelCustomizationRequestMessageMatchModeExact, settings.Rules[0].RequestMessageMatchMode)
}

func TestCustomizationRegexMatchesFullSingleMessage(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "dynamic-arithmetic", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
			RequestMessageMatchMode: service.GatewayChannelCustomizationRequestMessageMatchModeRegex,
			RequestMessageText:      "Q: [0-9]+ [+-] [0-9]+ = \\?\\nA:",
			StatusCode:              http.StatusAccepted, Body: "short-circuited",
		}},
	}))

	called := false
	var downstreamBody string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.POST("/probe", func(c *gin.Context) {
		called = true
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		downstreamBody = string(raw)
		c.String(http.StatusOK, "downstream")
	})

	body := "{\"input\":\"Q: 41 + 23 = ?\\nA:\"}"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body)))

	require.False(t, called)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, "short-circuited", recorder.Body.String())
	require.Empty(t, downstreamBody)
}

func TestCustomizationRegexPreservesMessageWhitespace(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "whitespace-sensitive", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
			RequestMessageMatchMode: service.GatewayChannelCustomizationRequestMessageMatchModeRegex,
			RequestMessageText:      `^ hello $`, StatusCode: http.StatusAccepted, Body: "short-circuited",
		}},
	}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.POST("/probe", func(c *gin.Context) { c.String(http.StatusOK, "downstream") })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{"input":" hello "}`)))

	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.Equal(t, "short-circuited", recorder.Body.String())
}

func TestCustomizationRegexRequiresFullTextAndSingleMessage(t *testing.T) {
	pattern := "Q: [0-9]+ [+-] [0-9]+ = \\?\\nA:"
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "prefix", body: "{\"input\":\"prefix Q: 41 + 23 = ?\\nA:\"}"},
		{name: "suffix", body: "{\"input\":\"Q: 41 + 23 = ?\\nA: suffix\"}"},
		{name: "multiple messages", body: "{\"input\":[\"Q: 41 + 23 = ?\\nA:\",\"Q: 1 + 1 = ?\\nA:\"]}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceUnderTest := NewCustomizationService()
			require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
				Rules: []service.GatewayChannelCustomizationRule{{
					Name: "dynamic-arithmetic", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
					RequestMessageMatchMode: service.GatewayChannelCustomizationRequestMessageMatchModeRegex,
					RequestMessageText:      pattern, StatusCode: http.StatusAccepted, Body: "short-circuited",
				}},
			}))

			called := false
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
				c.Next()
			})
			router.Use(serviceUnderTest.Middleware())
			router.POST("/probe", func(c *gin.Context) {
				called = true
				c.String(http.StatusOK, "downstream")
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(tt.body)))

			require.True(t, called)
			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "downstream", recorder.Body.String())
		})
	}
}

func TestCustomizationRejectsInvalidRequestMessageMatchModeOrRegex(t *testing.T) {
	for _, tt := range []struct {
		name string
		rule service.GatewayChannelCustomizationRule
	}{
		{
			name: "invalid mode",
			rule: service.GatewayChannelCustomizationRule{
				Name: "invalid-mode", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
				RequestMessageMatchMode: "glob", RequestMessageText: "hi",
			},
		},
		{
			name: "invalid regex",
			rule: service.GatewayChannelCustomizationRule{
				Name: "invalid-regex", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
				RequestMessageMatchMode: service.GatewayChannelCustomizationRequestMessageMatchModeRegex,
				RequestMessageText:      "[",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			serviceUnderTest := NewCustomizationService()
			err := serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{Rules: []service.GatewayChannelCustomizationRule{tt.rule}})
			require.Error(t, err)
		})
	}
}

func TestCustomizationMessageMismatchRestoresBodyAndDoesNotShortCircuit(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "message-probe", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
			RequestMessageText: "hi", StatusCode: http.StatusAccepted, Body: "short-circuited",
		}},
	}))

	const body = "{\"input\":\"hello\"}"
	var downstreamBody string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
		c.Next()
	})
	router.Use(serviceUnderTest.Middleware())
	router.POST("/probe", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		downstreamBody = string(raw)
		c.String(http.StatusOK, "downstream")
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body)))

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "downstream", recorder.Body.String())
	require.Equal(t, body, downstreamBody)
}

func TestCustomizationMessageConditionDoesNotShortCircuitMalformedOrOversizedBody(t *testing.T) {
	serviceUnderTest := NewCustomizationService()
	require.NoError(t, serviceUnderTest.Apply(context.Background(), service.GatewayChannelCustomizationSettings{
		Rules: []service.GatewayChannelCustomizationRule{{
			Name: "message-probe", Enabled: true, APIKeyNames: []string{"maibon-gpt"},
			RequestMessageText: "hi", StatusCode: http.StatusAccepted, Body: "short-circuited",
		}},
	}))

	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "malformed", body: "{\"input\":\"hi\""},
		{name: "oversized", body: "{\"input\":\"" + strings.Repeat("x", customizationRequestBodyMaxBytes) + "\"}"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var downstreamBody string
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 7, Name: "maibon-gpt", UserID: 42})
				c.Next()
			})
			router.Use(serviceUnderTest.Middleware())
			router.POST("/probe", func(c *gin.Context) {
				raw, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				downstreamBody = string(raw)
				c.String(http.StatusOK, "downstream")
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(tt.body)))

			require.Equal(t, http.StatusOK, recorder.Code)
			require.Equal(t, "downstream", recorder.Body.String())
			require.Equal(t, tt.body, downstreamBody)
		})
	}
}
