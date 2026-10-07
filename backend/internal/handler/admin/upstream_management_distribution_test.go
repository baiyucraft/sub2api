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

func TestUpstreamHealthAdminActionsPreserveDistribution(t *testing.T) {
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
					Distribution *service.UpstreamConfidenceDistribution `json:"confidence_distribution"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
			require.Equal(t, distribution, payload.Data.Distribution)
		})
	}
}
