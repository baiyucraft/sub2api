package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type sessionSwitchRuntimeStub struct {
	excluded      map[int64]struct{}
	trip          bool
	recordedModel string
	clearedModel  string
}

func (s *sessionSwitchRuntimeStub) SessionSwitchExcludedAccountIDs(context.Context, *service.APIKey, *int64, string, string) map[int64]struct{} {
	return s.excluded
}

func (s *sessionSwitchRuntimeStub) RecordSessionSwitchFailure(_ context.Context, _ *service.APIKey, _ *int64, _ string, routeModel string, _ int64, _ int) bool {
	s.recordedModel = routeModel
	return s.trip
}

func (s *sessionSwitchRuntimeStub) ClearSessionSwitchFailures(_ context.Context, _ *service.APIKey, _ *int64, _ string, routeModel string, _ int64) {
	s.clearedModel = routeModel
}

func TestSessionSwitchGuardRecordFailureKeepsSharedErrorImmutable(t *testing.T) {
	runtime := &sessionSwitchRuntimeStub{trip: true}
	failed := make(map[int64]struct{})
	guard := newSessionSwitchGuard(runtime, context.Background(), &service.APIKey{ID: 1}, sessionSwitchInt64Ptr(2), "session", failed)
	failoverErr := &service.UpstreamFailoverError{StatusCode: 503, RetryableOnSameAccount: true}

	effective, tripped := guard.RecordFailure("model-a", 7, failoverErr)

	require.True(t, tripped)
	require.False(t, effective.RetryableOnSameAccount)
	require.True(t, failoverErr.RetryableOnSameAccount)
	require.Contains(t, failed, int64(7))
	require.Equal(t, "model-a", runtime.recordedModel)
}

func TestSessionSwitchGuardRestoresCooldownAfterShared503Backoff(t *testing.T) {
	runtime := &sessionSwitchRuntimeStub{excluded: map[int64]struct{}{200: {}}, trip: true}
	fs := NewFailoverState(3, false)
	fs.LastFailoverErr = newTestFailoverErr(503, false, false)
	fs.FailedAccountIDs[100] = struct{}{}
	fs.SwitchCount = 1
	guard := newSessionSwitchGuard(runtime, context.Background(), &service.APIKey{ID: 1}, sessionSwitchInt64Ptr(2), "session", fs.FailedAccountIDs)
	guard.MergeExclusions("model-a")

	action := guard.HandleSelectionExhausted(context.Background(), fs)

	require.Equal(t, FailoverContinue, action)
	require.NotContains(t, fs.FailedAccountIDs, int64(100))
	require.Contains(t, fs.FailedAccountIDs, int64(200))

	guard.RecordStatus("model-a", 300, 503)
	require.Contains(t, fs.FailedAccountIDs, int64(300), "guard must follow the replacement exclusion map")

	start := time.Now()
	require.Equal(t, FailoverExhausted, guard.HandleSelectionExhausted(context.Background(), fs))
	require.Less(t, time.Since(start), 200*time.Millisecond, "persistent-only exclusions must not enter another backoff")
}

func TestSessionSwitchGuardPersistentExhaustionPreservesCancellation(t *testing.T) {
	runtime := &sessionSwitchRuntimeStub{excluded: map[int64]struct{}{200: {}}}
	fs := NewFailoverState(3, false)
	fs.LastFailoverErr = newTestFailoverErr(503, false, false)
	guard := newSessionSwitchGuard(runtime, context.Background(), &service.APIKey{ID: 1}, sessionSwitchInt64Ptr(2), "session", fs.FailedAccountIDs)
	guard.MergeExclusions("model-a")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.Equal(t, FailoverCanceled, guard.HandleSelectionExhausted(ctx, fs))
}

func TestSessionSwitchGuardClearSuccessDelegatesToRuntime(t *testing.T) {
	runtime := &sessionSwitchRuntimeStub{}
	guard := newSessionSwitchGuard(runtime, context.Background(), &service.APIKey{ID: 1}, sessionSwitchInt64Ptr(2), "session", make(map[int64]struct{}))

	guard.ClearSuccess("model-b", 8)

	require.Equal(t, "model-b", runtime.clearedModel)
}

func TestOpenAIWSStickyFallbackIsNotUsedAsSessionSwitchIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	gateway := &service.OpenAIGatewayService{}

	stickyHash, switchHash := openAIWSStickyAndSessionSwitchHashes(gateway, c, []byte(`{}`), "coarse-fallback")

	require.NotEmpty(t, stickyHash)
	require.Empty(t, switchHash)
}

func TestOpenAIWSContentDerivedStickyHashIsNotUsedAsSessionSwitchIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	gateway := &service.OpenAIGatewayService{}
	body := []byte(`{"type":"response.create","response":{"model":"gpt-5.6-sol","input":"same content"}}`)

	stickyHash, switchHash := openAIWSStickyAndSessionSwitchHashes(gateway, c, body, "coarse-fallback")

	require.NotEmpty(t, stickyHash)
	require.Empty(t, switchHash)
}

func TestOpenAIWSReliableSessionHashIsSharedWithSessionSwitch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request.Header.Set("session_id", "reliable-session")
	gateway := &service.OpenAIGatewayService{}

	stickyHash, switchHash := openAIWSStickyAndSessionSwitchHashes(gateway, c, []byte(`{}`), "coarse-fallback")

	require.NotEmpty(t, stickyHash)
	require.Equal(t, stickyHash, switchHash)
}

func sessionSwitchInt64Ptr(value int64) *int64 { return &value }
