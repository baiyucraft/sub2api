package handler

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpsModelAllowlistRejectionHTTPRecordsBlockedCandidateWithoutBody(t *testing.T) {
	for _, tc := range []struct {
		path, body  string
		requestType int16
	}{
		{"/v1/responses", `{"model":"allowed","model":"blocked","stream":true,"input":"secret-prompt"}`, 2},
		{"/v1/messages", `{"model":"blocked","messages":[{"content":"secret-prompt"}]}`, 1},
		{"/v1/chat/completions", `{"model":"blocked","messages":[]}`, 1},
		{"/v1beta/models/blocked:generateContent", `{"contents":"secret-prompt"}`, 1},
		{"/v1beta/models/blocked:streamGenerateContent", `{"contents":"secret-prompt"}`, 2},
		{"/v1/realtime?model=blocked", "", 3},
		{"/v1/live", `{"session":{"model":"blocked"},"secret":"secret-prompt"}`, 1},
	} {
		t.Run(tc.path, func(t *testing.T) {
			setupOpsErrorLogTestQueue(t, 2)
			ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			key := &service.APIKey{ID: 11, UserID: 12, Group: &service.Group{ID: 21, Platform: service.PlatformOpenAI, ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"allowed"}}}}
			r := gin.New()
			r.Use(OpsErrorLoggerMiddleware(ops), func(c *gin.Context) { c.Set(string(middleware2.ContextKeyAPIKey), key) }, middleware2.GroupModelAllowlist())
			r.POST("/v1/responses", func(c *gin.Context) { t.Error("blocked request reached handler") })
			r.POST("/v1/messages", func(c *gin.Context) { t.Error("blocked request reached handler") })
			r.POST("/v1/chat/completions", func(c *gin.Context) { t.Error("blocked request reached handler") })
			r.POST("/v1beta/models/*modelAction", func(c *gin.Context) { t.Error("blocked request reached handler") })
			r.POST("/v1/live", func(c *gin.Context) { t.Error("blocked request reached handler") })
			r.GET("/v1/realtime", func(c *gin.Context) { t.Error("blocked request reached handler") })
			method := http.MethodPost
			if strings.Contains(tc.path, "realtime") {
				method = http.MethodGet
			}
			req := httptest.NewRequest(method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.requestType == 3 {
				req.Header.Set("Connection", "Upgrade")
				req.Header.Set("Upgrade", "websocket")
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusNotFound, w.Code)
			entry := nextOpsModelAllowlistRejection(t)
			assertOpsModelAllowlistRejection(t, entry, "blocked", 21)
			require.NotNil(t, entry.RequestType)
			require.Equal(t, tc.requestType, *entry.RequestType)
			require.Empty(t, opsErrorLogQueue)
		})
	}
}

func TestOpsModelAllowlistRejectionMultipartRecordsModelOnly(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	key := &service.APIKey{ID: 11, UserID: 12, Group: &service.Group{ID: 21, ModelAllowlist: service.GroupModelAllowlist{Enabled: true, Models: []string{"allowed"}}}}
	r := gin.New()
	r.Use(OpsErrorLoggerMiddleware(ops), func(c *gin.Context) { c.Set(string(middleware2.ContextKeyAPIKey), key) }, middleware2.GroupModelAllowlist())
	r.POST("/v1/images/edits", func(c *gin.Context) { t.Error("blocked request reached handler") })
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	require.NoError(t, w.WriteField("model", "blocked"))
	require.NoError(t, w.WriteField("prompt", "secret-prompt"))
	require.NoError(t, w.Close())
	req := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	response := httptest.NewRecorder()
	r.ServeHTTP(response, req)
	require.Equal(t, http.StatusNotFound, response.Code)
	assertOpsModelAllowlistRejection(t, nextOpsModelAllowlistRejection(t), "blocked", 21)
}

func nextOpsModelAllowlistRejection(t *testing.T) *service.OpsInsertErrorLogInput {
	t.Helper()
	select {
	case job := <-opsErrorLogQueue:
		return job.entry
	case <-time.After(3 * time.Second):
		t.Fatal("model allowlist rejection was not enqueued")
		return nil
	}
}

func assertOpsModelAllowlistRejection(t *testing.T, entry *service.OpsInsertErrorLogInput, model string, groupID int64) {
	t.Helper()
	require.Equal(t, service.OpsErrorReasonModelNotAllowed, entry.ErrorType)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "client_request", entry.ErrorSource)
	require.True(t, entry.IsBusinessLimited)
	require.Equal(t, http.StatusNotFound, entry.StatusCode)
	require.Equal(t, model, entry.Model)
	require.Equal(t, model, entry.RequestedModel)
	require.NotNil(t, entry.GroupID)
	require.Equal(t, groupID, *entry.GroupID)
	require.Empty(t, entry.ErrorBody)
	require.NotContains(t, entry.ErrorMessage, "secret-prompt")
	require.Nil(t, entry.AccountID)
	require.Empty(t, entry.UpstreamEndpoint)
	require.Empty(t, entry.UpstreamModel)
	require.Nil(t, entry.UpstreamStatusCode)
	require.Nil(t, entry.UpstreamErrorMessage)
	require.Nil(t, entry.UpstreamErrorDetail)
	require.Empty(t, entry.UpstreamErrors)
	require.Nil(t, entry.UpstreamErrorsJSON)
}

func TestOpsModelAllowlistRejectionWebSocketFirstAndLaterTurn(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeDedicated} {
		for _, first := range []bool{true, false} {
			t.Run(mode+"/first="+map[bool]string{true: "true", false: "false"}[first], func(t *testing.T) {
				setupOpsErrorLogTestQueue(t, 2)
				ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
				tc := openAIResponsesWSUsageLogCase{opsService: ops, group: wsAllowlistGroup(true, "gpt-5.4"), ingressMode: mode}
				if first {
					tc.firstPayload = `{"type":"response.create","model":"gpt-5.4","model":"gpt-4.1","input":"secret-prompt"}`
					tc.firstFrameCloseExpected = true
				} else {
					tc.firstPayload = `{"type":"response.create","model":"gpt-5.4"}`
					tc.secondPayload = `{"type":"response.create","model":"gpt-4.1","input":"secret-prompt"}`
					tc.secondTurnCloseExpected = true
				}
				runOpenAIResponsesWebSocketUsageLogCase(t, tc)
				select {
				case job := <-opsErrorLogQueue:
					assertOpsModelAllowlistRejection(t, job.entry, "gpt-4.1", 4201)
					require.NotNil(t, job.entry.RequestType)
					require.Equal(t, int16(3), *job.entry.RequestType)
					require.Empty(t, opsErrorLogQueue)
				case <-time.After(3 * time.Second):
					t.Fatal("WebSocket rejection was not enqueued")
				}
			})
		}
	}
}

func TestOpsModelAllowlistRejectionIgnoresPriorTurnAttributionAndDeduplicates(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 2)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	setOpsRequestContext(c, "old-model", true)
	setOpsSelectedAccount(c, 99)
	service.SetOpsUpstreamModel(c, "old-upstream")
	c.Set(service.OpsUpstreamStatusCodeKey, http.StatusBadGateway)
	c.Set(service.OpsUpstreamErrorMessageKey, "secret-prompt")
	c.Set(opsDedicatedErrorRecordedKey, true) // an earlier turn must not swallow this rejection
	key := &service.APIKey{ID: 11, UserID: 12, Group: &service.Group{ID: 22, Platform: service.PlatformOpenAI}}
	service.MarkOpsModelAllowlistRejection(c, key, "blocked", true, true)
	key.Group.ID = 23 // admission snapshot must not change after the denial
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	logOpsModelAllowlistRejection(c, ops)
	logOpsModelAllowlistRejection(c, ops)
	assertOpsModelAllowlistRejection(t, nextOpsModelAllowlistRejection(t), "blocked", 22)
	require.Empty(t, opsErrorLogQueue)
}

func TestOpsModelAllowlistRejectionMonitoringOffDoesNotEnqueue(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 1)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	key := &service.APIKey{ID: 11, Group: &service.Group{ID: 21}}
	service.MarkOpsModelAllowlistRejection(c, key, "blocked", false, false)
	ops := service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	ops.SetMonitoringEnabled(false)
	logOpsModelAllowlistRejection(c, ops)
	require.Empty(t, opsErrorLogQueue)
}
