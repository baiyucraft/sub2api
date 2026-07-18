//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type retryProbeHandler struct {
	mu          sync.Mutex
	calls       int
	requestIDs  []string
	failures    int
	cancelFirst context.CancelFunc
	switches    int
}

type unknownMonitorRepo struct {
	ChannelMonitorRepository
	monitor *ChannelMonitor
	rows    []*ChannelMonitorHistoryRow
}

func (r *unknownMonitorRepo) GetByID(context.Context, int64) (*ChannelMonitor, error) {
	return r.monitor, nil
}

func (r *unknownMonitorRepo) InsertHistoryBatch(_ context.Context, rows []*ChannelMonitorHistoryRow) error {
	r.rows = append(r.rows, rows...)
	return nil
}

func (r *unknownMonitorRepo) MarkChecked(context.Context, int64, time.Time) error {
	return nil
}

type failingMonitorDecryptor struct{}

func (failingMonitorDecryptor) Encrypt(string) (string, error) { return "", nil }
func (failingMonitorDecryptor) Decrypt(string) (string, error) {
	return "", context.Canceled
}

func (h *retryProbeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.calls++
	call := h.calls
	h.requestIDs = append(h.requestIDs, r.Header.Get("X-Client-Request-ID"))
	cancel := h.cancelFirst
	if call == 1 {
		h.cancelFirst = nil
	}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	_ = r.Body.Close()
	if call <= h.failures {
		http.Error(w, "temporary gateway failure", http.StatusBadGateway)
		return
	}
	if h.switches > 0 {
		w.Header().Set("X-Sub2API-Monitor-Switches", fmt.Sprintf("%d", h.switches))
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{
			"message": map[string]string{"content": answerFromOpenAIRequest(body)},
		}},
	})
}

func TestRunCheckForModel_GatewaySwitchHeaderMakesSuccessDegraded(t *testing.T) {
	h := &retryProbeHandler{switches: 1}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	swapMonitorHTTPClient(t)

	res := runCheckForModelWithRetry(
		context.Background(), MonitorProviderOpenAI, srv.URL, "sk-test", "gpt-test", nil, 1, 0,
	)
	require.Equal(t, MonitorStatusDegraded, res.Status)
	require.Contains(t, res.Message, "换号")
	require.Equal(t, 1, res.AccountSwitchCount)
}

func TestRunCheckForModelWithRetry_RetrySuccessIsDegradedAndUsesNewRequestIDs(t *testing.T) {
	h := &retryProbeHandler{failures: 1}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	swapMonitorHTTPClient(t)

	res := runCheckForModelWithRetry(
		context.Background(), MonitorProviderOpenAI, srv.URL, "sk-test", "gpt-test", nil, 3, time.Millisecond,
	)
	require.Equal(t, MonitorStatusDegraded, res.Status)
	require.Contains(t, res.Message, "第 2 次探测成功")

	h.mu.Lock()
	defer h.mu.Unlock()
	require.Equal(t, 2, h.calls)
	require.Len(t, h.requestIDs, 2)
	require.NotEmpty(t, h.requestIDs[0])
	require.NotEqual(t, h.requestIDs[0], h.requestIDs[1])
}

func TestRunCheckForModelWithRetry_AttemptBounds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		attempts int
	}{
		{name: "one", attempts: 1},
		{name: "three", attempts: 3},
		{name: "five", attempts: 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &retryProbeHandler{failures: 10}
			srv := httptest.NewServer(h)
			t.Cleanup(srv.Close)
			swapMonitorHTTPClient(t)

			res := runCheckForModelWithRetry(
				context.Background(), MonitorProviderOpenAI, srv.URL, "sk-test", "gpt-test", nil, tc.attempts, 0,
			)
			require.Equal(t, MonitorStatusError, res.Status)
			h.mu.Lock()
			require.Equal(t, tc.attempts, h.calls)
			h.mu.Unlock()
		})
	}
}

func TestRunCheckForModelWithRetry_CancelStopsBeforeNextAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &retryProbeHandler{failures: 10, cancelFirst: cancel}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	swapMonitorHTTPClient(t)

	res := runCheckForModelWithRetry(
		ctx, MonitorProviderOpenAI, srv.URL, "sk-test", "gpt-test", nil, 5, time.Hour,
	)
	require.Equal(t, MonitorStatusError, res.Status)
	require.Contains(t, res.Message, "取消")
	h.mu.Lock()
	require.Equal(t, 1, h.calls)
	h.mu.Unlock()
}

func TestFinalizeOperationalOrDegraded_SlowResponseIsDegraded(t *testing.T) {
	res := &CheckResult{Model: "gpt-test"}
	require.Equal(t, MonitorStatusDegraded, finalizeOperationalOrDegraded(res, monitorDegradedThreshold, 6000).Status)
}

func TestNormalizeMaxProbeAttempts(t *testing.T) {
	require.Equal(t, monitorDefaultMaxProbeAttempts, normalizeMaxProbeAttempts(0))
	require.NoError(t, validateMaxProbeAttempts(1))
	require.NoError(t, validateMaxProbeAttempts(5))
	require.Error(t, validateMaxProbeAttempts(0))
	require.Error(t, validateMaxProbeAttempts(6))
}

func TestRunCheck_DecryptFailureIsUnknownAndPersisted(t *testing.T) {
	repo := &unknownMonitorRepo{monitor: &ChannelMonitor{
		ID:               42,
		Name:             "broken-key",
		Provider:         MonitorProviderOpenAI,
		Endpoint:         "https://api.example.com",
		APIKey:           "encrypted",
		PrimaryModel:     "gpt-test",
		MaxProbeAttempts: 3,
	}}
	svc := NewChannelMonitorService(repo, failingMonitorDecryptor{})

	results, err := svc.RunCheck(context.Background(), 42)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, MonitorStatusUnknown, results[0].Status)
	require.Contains(t, results[0].Message, "解密")
	require.Len(t, repo.rows, 1)
	require.Equal(t, MonitorStatusUnknown, repo.rows[0].Status)
}
