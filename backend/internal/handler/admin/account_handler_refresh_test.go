package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountRefreshConfidenceReader struct {
	accountListUpstreamHealthReader
	batchCalls  int
	singleCalls int
	batchKeys   []int64
	summaries   map[int64]service.UpstreamHealthConfidenceSummary
	err         error
}

func (r *accountRefreshConfidenceReader) GetUpstreamHealthConfidences(_ context.Context, keyIDs []int64) (map[int64]service.UpstreamHealthConfidenceSummary, error) {
	r.batchCalls++
	r.batchKeys = append([]int64(nil), keyIDs...)
	return r.summaries, r.err
}

func (r *accountRefreshConfidenceReader) GetUpstreamHealthConfidence(_ context.Context, keyID int64) (service.UpstreamHealthConfidenceSummary, error) {
	r.singleCalls++
	return r.summaries[keyID], nil
}

func TestAccountHandlerUpstreamRefreshBatchesConfidenceAndKeepsETag(t *testing.T) {
	for _, lite := range []string{"0", "1"} {
		t.Run("lite="+lite, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			adminSvc := newStubAdminService()
			now := time.Now().UTC().Truncate(time.Second)
			configID, firstKey, secondKey := int64(19001), int64(19101), int64(19102)
			for i, keyID := range []*int64{&firstKey, &secondKey, &firstKey} {
				adminSvc.accounts = append(adminSvc.accounts, service.Account{
					ID: int64(19201 + i), Name: "upstream-refresh", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true,
					UpstreamConfigID: &configID, UpstreamKeyID: keyID, CreatedAt: now, UpdatedAt: now,
				})
			}
			firstScore, secondScore := 87.0, 93.0
			reader := &accountRefreshConfidenceReader{summaries: map[int64]service.UpstreamHealthConfidenceSummary{
				firstKey:  {Score24h: &firstScore, Status: "current_success", SampleCount24h: 21},
				secondKey: {Score24h: &secondScore, Status: "current_success", SampleCount24h: 22},
			}}
			handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			handler.SetUpstreamHealthHistoryReader(reader)
			router := gin.New()
			router.GET("/api/v1/admin/upstream-management/accounts", handler.ListUpstreamManagement)
			path := "/api/v1/admin/upstream-management/accounts?lite=" + lite
			first := httptest.NewRecorder()
			router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusOK, first.Code)
			require.Equal(t, 1, reader.batchCalls)
			require.Zero(t, reader.singleCalls)
			require.Equal(t, []int64{firstKey, secondKey}, reader.batchKeys)
			require.Equal(t, reader.batchKeys, reader.keyIDs)
			var payload struct {
				Data struct {
					Items []struct {
						Health struct {
							Score   float64 `json:"confidence_score_24h"`
							Samples int     `json:"confidence_sample_count_24h"`
						} `json:"upstream_health"`
					} `json:"items"`
				} `json:"data"`
			}
			require.NoError(t, json.Unmarshal(first.Body.Bytes(), &payload))
			require.Len(t, payload.Data.Items, 3)
			require.Equal(t, firstScore, payload.Data.Items[0].Health.Score)
			require.Equal(t, secondScore, payload.Data.Items[1].Health.Score)
			require.Equal(t, 21, payload.Data.Items[2].Health.Samples)
			etag := first.Header().Get("ETag")
			require.NotEmpty(t, etag)
			unchanged := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("If-None-Match", etag)
			router.ServeHTTP(unchanged, request)
			require.Equal(t, http.StatusNotModified, unchanged.Code)
			firstScore = 95
			changed := httptest.NewRecorder()
			request = httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("If-None-Match", etag)
			router.ServeHTTP(changed, request)
			require.Equal(t, http.StatusOK, changed.Code)
			require.NotEqual(t, etag, changed.Header().Get("ETag"))
			require.Equal(t, 3, reader.batchCalls)
			require.Zero(t, reader.singleCalls)
		})
	}
}

func TestAccountListConfidenceBatchFailureKeepsPartialResultsWithoutPointQueries(t *testing.T) {
	reader := &accountRefreshConfidenceReader{
		summaries: map[int64]service.UpstreamHealthConfidenceSummary{41: {Status: "data_insufficient"}},
		err:       errors.New("read unavailable"),
	}
	handler := &AccountHandler{upstreamHealthReader: reader}
	results := handler.accountListHealthConfidences(context.Background(), []int64{41, 42})
	require.Equal(t, reader.summaries, results)
	require.Equal(t, 1, reader.batchCalls)
	require.Zero(t, reader.singleCalls)
	handler.accountListHealthConfidences(context.Background(), nil)
	require.Equal(t, 1, reader.batchCalls)
}

type accountRefreshLegacyConfidenceReader struct {
	accountListUpstreamHealthReader
	keys []int64
}

func (r *accountRefreshLegacyConfidenceReader) GetUpstreamHealthConfidence(_ context.Context, keyID int64) (service.UpstreamHealthConfidenceSummary, error) {
	r.keys = append(r.keys, keyID)
	if keyID == 42 {
		return service.UpstreamHealthConfidenceSummary{}, errors.New("read unavailable")
	}
	return service.UpstreamHealthConfidenceSummary{Status: "data_insufficient"}, nil
}

func TestAccountListConfidenceLegacyReaderUsesUniqueValidKeys(t *testing.T) {
	first, second, zero := int64(41), int64(42), int64(0)
	accounts := []service.Account{
		{UpstreamKeyID: &first}, {UpstreamKeyID: &first}, {UpstreamKeyID: &second},
		{UpstreamKeyID: &zero}, {},
	}
	keys := accountListUpstreamKeyIDs(accounts)
	require.Equal(t, []int64{41, 42}, keys)
	reader := &accountRefreshLegacyConfidenceReader{}
	handler := &AccountHandler{upstreamHealthReader: reader}
	results := handler.accountListHealthConfidences(context.Background(), keys)
	require.Equal(t, keys, reader.keys)
	require.Len(t, results, 1)
	require.Equal(t, "data_insufficient", results[41].Status)
}
