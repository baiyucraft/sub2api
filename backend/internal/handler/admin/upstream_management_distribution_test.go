package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type distributionAdminRepo struct {
	service.UpstreamConfigRepository
	key          service.UpstreamKey
	distribution *service.UpstreamConfidenceDistribution
}

func (r *distributionAdminRepo) GetKeyByID(context.Context, int64) (*service.UpstreamKey, error) {
	return &r.key, nil
}

func (*distributionAdminRepo) PatchKeyHealthWithObservation(context.Context, int64, map[string]any, *service.UpstreamEvent, *service.UpstreamHealthObservation) error {
	return nil
}

func (r *distributionAdminRepo) GetUpstreamHealthConfidence(context.Context, int64) (service.UpstreamHealthConfidenceSummary, error) {
	return service.UpstreamHealthConfidenceSummary{Distribution: r.distribution, Status: r.distribution.Status}, nil
}

type distributionAdminAccountRepo struct{ service.AccountRepository }

func (*distributionAdminAccountRepo) ListByUpstreamKeyID(context.Context, int64) ([]service.Account, error) {
	return []service.Account{{ID: 89431, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey}}, nil
}

type distributionAdminProber struct{}

func (*distributionAdminProber) RunUpstreamHealthProbe(context.Context, *service.Account, string) (service.UpstreamHealthProbeResult, error) {
	return service.UpstreamHealthProbeResult{Result: "success"}, nil
}

func TestUpstreamHealthAdminActionsPublishUnifiedConfidence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const keyID int64 = 89431
	at := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, path, body string
		pending          bool
	}{
		{"manual probe", "probe", "", false},
		{"observation toggle", "observation", `{"enabled":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.GlobalUpstreamHealthRegistry().Forget(keyID)
			t.Cleanup(func() { service.GlobalUpstreamHealthRegistry().Forget(keyID) })
			distribution := &service.UpstreamConfidenceDistribution{Status: "collecting", WindowSize: 128, Attempted: 42,
				SeriesReset: &service.ConfidenceDistributionSeriesReset{Pending: tc.pending, At: &at, Reasons: []string{"credential_changed"}, PreviousAttempted: 113}}
			repo := &distributionAdminRepo{key: service.UpstreamKey{ID: keyID, Status: service.StatusActive}, distribution: distribution}
			svc := service.NewUpstreamConfigService(repo, nil, &distributionAdminAccountRepo{})
			svc.SetHealthProbeDependencies(&distributionAdminProber{}, nil)
			handler := NewUpstreamConfigHandler(svc)
			router := gin.New()
			router.POST("/keys/:id/probe", handler.ProbeKeyAdmin)
			router.POST("/keys/:id/observation", handler.SetKeyObservationAdmin)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/keys/89431/"+tc.path, strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
			var payload struct {
				Data struct {
					Summary *service.ConfidenceSummary `json:"confidence_summary"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Equal(t, service.BuildConfidenceSummary(distribution, nil), payload.Data.Summary)
			require.NotContains(t, recorder.Body.String(), `"confidence_distribution"`)
			require.NotContains(t, recorder.Body.String(), `"confidence_attribution"`)
		})
	}
}

func TestUpstreamHealthSnapshotJSONPublishesOnlyOptionalUnifiedSummary(t *testing.T) {
	value := service.ConfidenceFingerprintAttribution{
		Status: "unknown", Platform: "anthropic", Model: "claude-test", Protocol: "anthropic_messages",
		ProtocolStatus: "compatible",
	}
	snapshot := service.MergeUpstreamHealthConfidence(service.UpstreamHealthSnapshot{}, service.UpstreamHealthConfidenceSummary{Attribution: &value})
	raw, err := json.Marshal(snapshot)
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	require.Equal(t, "insufficient", payload["confidence_summary"].(map[string]any)["status"])
	require.NotContains(t, payload, "confidence_distribution")
	require.NotContains(t, payload, "confidence_attribution")

	legacyRaw, err := json.Marshal(service.UpstreamHealthSnapshot{})
	require.NoError(t, err)
	var legacyPayload map[string]any
	require.NoError(t, json.Unmarshal(legacyRaw, &legacyPayload))
	_, exists := legacyPayload["confidence_summary"]
	require.False(t, exists)
}

func TestAccountHandlerUpstreamConfidenceSummaryInvalidatesETag(t *testing.T) {
	for _, lite := range []string{"0", "1"} {
		t.Run("lite="+lite, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			adminSvc := newStubAdminService()
			configID, keyID := int64(88101), int64(88102)
			adminSvc.accounts = []service.Account{{
				ID: 88103, Name: "confidence-etag", Platform: service.PlatformOpenAI,
				Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
				UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
			}}
			distribution := &service.UpstreamConfidenceDistribution{
				Status: "collecting", ClaimedModel: "gpt-6.1-sol", ValidSamples: 127,
			}
			reader := &accountRefreshConfidenceReader{summaries: map[int64]service.UpstreamHealthConfidenceSummary{
				keyID: {Distribution: distribution},
			}}
			handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			handler.SetUpstreamHealthHistoryReader(reader)
			router := gin.New()
			router.GET("/accounts", handler.ListUpstreamManagement)
			request := func(etag string) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/accounts?lite="+lite, nil)
				req.Header.Set("If-None-Match", etag)
				router.ServeHTTP(recorder, req)
				return recorder
			}
			first := request("")
			require.Equal(t, http.StatusOK, first.Code)
			require.Contains(t, first.Body.String(), `"confidence_summary"`)
			require.NotContains(t, first.Body.String(), `"confidence_distribution"`)
			require.NotContains(t, first.Body.String(), `"confidence_attribution"`)
			etag := first.Header().Get("ETag")
			require.NotEmpty(t, etag)
			require.Equal(t, http.StatusNotModified, request(etag).Code)

			// Only the unified evidence changes; no account timestamp or health
			// event is modified to force invalidation.
			distribution.Status, distribution.ValidSamples = "match", 128
			distribution.ClosestModel = distribution.ClaimedModel
			changed := request(etag)
			require.Equal(t, http.StatusOK, changed.Code)
			require.NotEqual(t, etag, changed.Header().Get("ETag"))
			require.Contains(t, changed.Body.String(), `"status":"match"`)
			require.Equal(t, http.StatusNotModified, request(changed.Header().Get("ETag")).Code)
			require.Equal(t, 4, reader.batchCalls)
			require.Zero(t, reader.singleCalls)
		})
	}
}
