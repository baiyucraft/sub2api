package observer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestServiceDisabledDoesNotCreateFileOrReadBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{}))

	readCount := 0
	router := gin.New()
	router.Use(observer.Middleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", &countingReader{Reader: strings.NewReader(`{"prompt":"ignored"}`), count: &readCount})
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusNoContent, recorder.Code)
	require.Zero(t, readCount)
	require.NoError(t, observer.Shutdown(context.Background()))
	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestNewServiceUsesFixedAbsoluteOutputPath(t *testing.T) {
	observer := NewService()

	require.Equal(t, service.GatewayRequestObserverOutputPath, observer.outputPath)
	require.True(t, strings.HasPrefix(observer.outputPath, "/"))
	require.Equal(t, "/app/.tmp/maibon-probe-observation/requests.jsonl", observer.outputPath)
}

func TestServiceMatchesAPIKeyNameAndPreservesBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{
		Enabled:     true,
		APIKeyNames: []string{"maibon-gpt"},
	}))

	const body = `{"prompt":"hello","api_key":"secret-value"}`
	var downstreamBody string
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 12, UserID: 34, Name: "maibon-gpt"})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		raw, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		downstreamBody = string(raw)
		c.Header("Content-Type", "application/json")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages?token=secret", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "observer-test")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, body, downstreamBody)
	require.NoError(t, observer.Shutdown(context.Background()))

	entries := readObservations(t, path)
	require.Len(t, entries, 1)
	entry := entries[0]
	require.Equal(t, float64(12), entry["api_key_id"])
	require.Equal(t, "maibon-gpt", entry["api_key_name"])
	require.Equal(t, []any{"api_key"}, entry["matched_by"])
	require.NotContains(t, entry["request_body"], "secret-value")
	require.Contains(t, entry["request_body"], "***")
	require.Equal(t, "token=***", entry["query"])
	require.Equal(t, "observer-test", entry["user_agent"])
}

func TestServiceMatchesPlatformUserIDAndEmailWithORSemantics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{
		Enabled:     true,
		APIKeyNames: []string{"other-key"},
		UserIDs:     []int64{456},
		UserEmails:  []string{"target@example.com"},
	}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			ID:     99,
			UserID: 7,
			Name:   "maibon-gpt",
			User:   &service.User{ID: 7, Email: "TARGET@example.com"},
		})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/v1/messages", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.AccountID, int64(789)))
		c.Status(http.StatusAccepted)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"prompt":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.NoError(t, observer.Shutdown(context.Background()))

	entries := readObservations(t, path)
	require.Len(t, entries, 1)
	require.Equal(t, int64(7), int64(entries[0]["user_id"].(float64)))
	require.Equal(t, int64(789), int64(entries[0]["account_id"].(float64)))
	require.Equal(t, []any{"user_email"}, entries[0]["matched_by"])
}

func TestServiceMatchesPlatformUserIDAndRecordsBothSources(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{
		Enabled:    true,
		UserIDs:    []int64{27},
		UserEmails: []string{"1069167864@qq.com"},
	}))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			UserID: 27,
			User:   &service.User{ID: 27, Email: "1069167864@qq.com"},
		})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/probe", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
		c.Status(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(`{"probe":true}`)))
	require.NoError(t, observer.Shutdown(context.Background()))

	entries := readObservations(t, path)
	require.Len(t, entries, 1)
	require.Equal(t, []any{"user_id", "user_email"}, entries[0]["matched_by"])
}

func TestServiceDoesNotReadUnmatchedUserBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{
		Enabled:    true,
		UserEmails: []string{"target@example.com"},
	}))

	readCount := 0
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
			UserID: 7,
			User:   &service.User{ID: 7, Email: "other@example.com"},
		})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/probe", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodPost, "/probe", &countingReader{Reader: strings.NewReader(`{"probe":true}`), count: &readCount})
	router.ServeHTTP(httptest.NewRecorder(), req)
	require.Zero(t, readCount)
	require.NoError(t, observer.Shutdown(context.Background()))
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Empty(t, raw)
}

func TestServiceTruncatesCapturedBodyButHashesReadBody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 8, 4)
	require.NoError(t, observer.Apply(context.Background(), Settings{
		Enabled:     true,
		APIKeyNames: []string{"maibon-gpt"},
	}))

	const body = `{"prompt":"0123456789"}`
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{Name: "maibon-gpt"})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/probe", func(c *gin.Context) {
		_, _ = io.ReadAll(c.Request.Body)
		c.Status(http.StatusOK)
	})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/probe", strings.NewReader(body)))
	require.NoError(t, observer.Shutdown(context.Background()))

	entries := readObservations(t, path)
	require.Len(t, entries, 1)
	require.True(t, entries[0]["request_body_truncated"].(bool))
	require.Equal(t, float64(8), entries[0]["captured_body_bytes"])
	digest := sha256.Sum256([]byte(body))
	require.Equal(t, hex.EncodeToString(digest[:]), entries[0]["request_body_sha256"])
}

func TestServiceRuntimeDisableFlushesAndStopsRecording(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.jsonl")
	observer := newService(path, 256, 4)
	settings := Settings{Enabled: true, APIKeyNames: []string{"maibon-gpt"}}
	require.NoError(t, observer.Apply(context.Background(), settings))

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, Name: "maibon-gpt"})
		c.Next()
	})
	router.Use(observer.Middleware())
	router.POST("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/probe", nil))
	require.NoError(t, observer.Apply(context.Background(), Settings{}))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/probe", nil))
	require.NoError(t, observer.Shutdown(context.Background()))

	require.Len(t, readObservations(t, path), 1)
	require.False(t, observer.Active())
}

func readObservations(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	entries := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		var entry map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &entry))
		entries = append(entries, entry)
	}
	return entries
}

type countingReader struct {
	*strings.Reader
	count *int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	*r.count += n
	return n, err
}

func (r *countingReader) Close() error { return nil }
