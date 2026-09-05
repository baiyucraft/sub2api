package admin

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/handler/dto"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type accountListTTFTGuardReader struct {
	accountIDs []int64
	states     map[int64][]service.OpenAITTFTGuardDegradation
}

type accountListUpstreamHealthReader struct {
	keyIDs []int64
	limit  int
	states map[int64][]service.UpstreamHealthObservation
}

func (r *accountListUpstreamHealthReader) ListUpstreamHealthHistories(_ context.Context, keyIDs []int64, limit int) (map[int64][]service.UpstreamHealthObservation, error) {
	r.keyIDs = append([]int64(nil), keyIDs...)
	r.limit = limit
	return r.states, nil
}

func (r *accountListTTFTGuardReader) OpenAITTFTGuardDegradations(accountIDs []int64) map[int64][]service.OpenAITTFTGuardDegradation {
	r.accountIDs = append([]int64(nil), accountIDs...)
	return r.states
}

func TestAccountHandlerListLiteUsesCompactDTOAndETag(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(77)
	adminSvc.accounts = []service.Account{{
		ID: 501, Name: "compact-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"email": "compact@example.com", "access_token": strings.Repeat("x", 4096)},
		Extra:       map[string]any{"privacy_mode": "training_off"}, Status: service.StatusActive,
		Schedulable: true, Concurrency: 4, GroupIDs: []int64{groupID},
		Groups:        []*service.Group{{ID: groupID, Name: "codex", Platform: service.PlatformOpenAI}},
		AccountGroups: []service.AccountGroup{{AccountID: 501, GroupID: groupID, Priority: 2, Group: &service.Group{ID: groupID, Name: "codex", Platform: service.PlatformOpenAI}}},
		CreatedAt:     now, UpdatedAt: now,
	}}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Header().Get("ETag"))

	var litePayload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &litePayload))
	require.Len(t, litePayload.Data.Items, 1)
	liteItem := litePayload.Data.Items[0]
	require.Equal(t, float64(501), liteItem["id"])
	require.Equal(t, []any{float64(groupID)}, liteItem["group_ids"])
	require.Equal(t, true, liteItem["schedulable"])
	require.NotContains(t, liteItem, "groups")
	require.NotContains(t, liteItem, "account_groups")
	credentials, ok := liteItem["credentials"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "compact@example.com", credentials["email"])
	require.NotContains(t, credentials, "access_token")
	credentialsStatus, ok := liteItem["credentials_status"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, credentialsStatus["has_access_token"])

	// The ETag must represent the same compact body and return 304 on refresh.
	rec304 := httptest.NewRecorder()
	req304 := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	req304.Header.Set("If-None-Match", rec.Header().Get("ETag"))
	router.ServeHTTP(rec304, req304)
	require.Equal(t, http.StatusNotModified, rec304.Code)

	// Omitting lite preserves the legacy full response shape.
	recFull := httptest.NewRecorder()
	reqFull := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20", nil)
	router.ServeHTTP(recFull, reqFull)
	require.Equal(t, http.StatusOK, recFull.Code)
	var fullPayload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recFull.Body.Bytes(), &fullPayload))
	require.Contains(t, fullPayload.Data.Items[0], "groups")
	require.Contains(t, fullPayload.Data.Items[0], "account_groups")
}

func TestAccountListItemKeepsUpstreamCapabilityProjections(t *testing.T) {
	source := &dto.Account{
		UpstreamImagePricing: &dto.UpstreamImagePricing{Supported: true},
		UpstreamVideoPricing: &dto.UpstreamVideoPricing{Supported: true},
		UpstreamLongContext:  &dto.UpstreamLongContext{Enabled: true},
		UpstreamModelSync:    &dto.UpstreamModelSync{Mode: "sync_managed", Status: "available", ModelCount: 3},
	}
	compact := dto.AccountListItemFromAccount(source)
	require.Equal(t, source.UpstreamImagePricing, compact.UpstreamImagePricing)
	require.Equal(t, source.UpstreamVideoPricing, compact.UpstreamVideoPricing)
	require.Equal(t, source.UpstreamLongContext, compact.UpstreamLongContext)
	require.Equal(t, source.UpstreamModelSync, compact.UpstreamModelSync)
}

func TestAccountHandlerListLiteKeepsUpstreamProjections(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	configID, keyID := int64(71), int64(72)
	configName, keyName, masked, site := "provider", "key", "sk-***", "https://provider.example"
	enabled := true
	adminSvc.accounts = []service.Account{{
		ID: 701, Name: "provider-key", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 5,
		UpstreamConfigID: &configID, UpstreamKeyID: &keyID, UpstreamConfigName: &configName,
		UpstreamKeyName: &keyName, UpstreamKeyMasked: &masked, UpstreamSiteURL: &site,
		UpstreamSchedulingEnabled: &enabled,
		Credentials:               map[string]any{"api_key": "must-not-leak"},
	}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?lite=1&scope=upstream", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	item := payload.Data.Items[0]
	require.Equal(t, float64(configID), item["upstream_config_id"])
	require.Equal(t, float64(keyID), item["upstream_key_id"])
	require.Equal(t, configName, item["upstream_config_name"])
	require.Equal(t, keyName, item["upstream_key_name"])
	require.Equal(t, masked, item["upstream_key_masked"])
	require.Equal(t, site, item["upstream_site_url"])
	require.Equal(t, true, item["upstream_scheduling_enabled"])
	require.Contains(t, item, "scheduler_concurrency_limit")
	require.Contains(t, item, "upstream_health")
	require.Contains(t, item, "available_actions")
	require.NotContains(t, item, "groups")
	require.NotContains(t, item, "account_groups")
	require.NotContains(t, rec.Body.String(), "must-not-leak")
}

func TestAccountHandlerListLiteStaysBelowResponseBudget(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	accounts := make([]service.Account, 20)
	for i := range accounts {
		id := int64(600 + i)
		groupID := int64(800 + i)
		accounts[i] = service.Account{
			ID: id, Name: "account-" + strconv.Itoa(i), Platform: service.PlatformOpenAI,
			Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true,
			Concurrency: 4, GroupIDs: []int64{groupID},
			Groups:        []*service.Group{{ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}},
			AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID, Group: &service.Group{ID: groupID, Name: "group-" + strconv.Itoa(i), Description: strings.Repeat("description ", 20)}}},
			CreatedAt:     now, UpdatedAt: now,
		}
	}
	adminSvc.accounts = accounts

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&lite=1", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Less(t, rec.Body.Len(), 80*1024)

	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	_, err := zw.Write(rec.Body.Bytes())
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.Less(t, compressed.Len(), 15*1024)
}

func setupAccountListRouter() (*gin.Engine, *stubAdminService) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/accounts", handler.List)
	return router, adminSvc
}

func TestAccountHandlerListIncludesCreatedAt(t *testing.T) {
	router, adminSvc := setupAccountListRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&sort_by=created_at&sort_order=desc", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "created_at", adminSvc.lastListAccounts.sortBy)

	var payload struct {
		Data struct {
			Items []struct {
				ID        int64  `json:"id"`
				CreatedAt string `json:"created_at"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)

	createdAt := payload.Data.Items[0].CreatedAt
	require.NotEmpty(t, createdAt)
	require.True(t, strings.HasSuffix(createdAt, "Z"), "created_at should be serialized as UTC")
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	require.NoError(t, err)
	_, offset := parsed.Zone()
	require.Equal(t, 0, offset)
}

func TestAccountHandlerListReturnsSchedulerScoresPerGroup(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(41)
	adminSvc.accounts = []service.Account{
		{
			ID:          101,
			Name:        "account-high-priority",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			AccountGroups: []service.AccountGroup{
				{AccountID: 101, GroupID: groupID, Priority: 100, Group: &service.Group{ID: groupID, Name: "openai"}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:          102,
			Name:        "account-low-priority",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    100000,
			AccountGroups: []service.AccountGroup{
				{AccountID: 102, GroupID: groupID, Priority: 1, Group: &service.Group{ID: groupID, Name: "openai"}},
			},
			GroupIDs:  []int64{groupID},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID       *int64  `json:"group_id"`
					GroupName     string  `json:"group_name"`
					GroupPriority *int    `json:"group_priority"`
					BaseScore     float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)

	var high, low *struct {
		ID             int64 `json:"id"`
		SchedulerScore struct {
			BaseScore float64 `json:"base_score"`
		} `json:"scheduler_score"`
		SchedulerScores []struct {
			GroupID       *int64  `json:"group_id"`
			GroupName     string  `json:"group_name"`
			GroupPriority *int    `json:"group_priority"`
			BaseScore     float64 `json:"base_score"`
		} `json:"scheduler_scores"`
	}
	for i := range payload.Data.Items {
		item := &payload.Data.Items[i]
		switch item.ID {
		case 101:
			high = item
		case 102:
			low = item
		}
	}
	require.NotNil(t, high)
	require.NotNil(t, low)
	require.Len(t, high.SchedulerScores, 1)
	require.Len(t, low.SchedulerScores, 1)
	require.Equal(t, groupID, *high.SchedulerScores[0].GroupID)
	require.Equal(t, "openai", high.SchedulerScores[0].GroupName)
	require.Equal(t, 100, *high.SchedulerScores[0].GroupPriority)
	require.Equal(t, 1, *low.SchedulerScores[0].GroupPriority)
	require.Greater(t, high.SchedulerScores[0].BaseScore, low.SchedulerScores[0].BaseScore)
}

func TestAccountHandlerListSkipsSchedulerScoresByDefault(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	adminSvc.accounts = []service.Account{
		{
			ID:          110,
			Name:        "openai-account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 10,
			Priority:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20&platform=openai", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Zero(t, adminSvc.schedulerScoreFilterCalls)
	require.Zero(t, adminSvc.openAISchedulerScorePoolCalls)

	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.NotContains(t, payload.Data.Items[0], "scheduler_score")
	require.NotContains(t, payload.Data.Items[0], "scheduler_scores")
}

func TestAccountHandlerListKeepsSchedulerScoreScopedToFilter(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	groupID := int64(42)
	visibleAccount := service.Account{
		ID:          201,
		Name:        "visible-low-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		AccountGroups: []service.AccountGroup{
			{AccountID: 201, GroupID: groupID, Priority: 1, Group: &service.Group{ID: groupID, Name: "openai"}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	hiddenGroupPeer := service.Account{
		ID:          202,
		Name:        "hidden-high-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		AccountGroups: []service.AccountGroup{
			{AccountID: 202, GroupID: groupID, Priority: 2, Group: &service.Group{ID: groupID, Name: "openai"}},
		},
		GroupIDs:  []int64{groupID},
		CreatedAt: now,
		UpdatedAt: now,
	}
	adminSvc.accounts = []service.Account{visibleAccount}
	adminSvc.accountSchedulerScoreFilterAccounts = []service.Account{visibleAccount, hiddenGroupPeer}
	adminSvc.openAISchedulerScorePoolAccounts = []service.Account{visibleAccount, hiddenGroupPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID   *int64  `json:"group_id"`
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	item := payload.Data.Items[0]
	require.Equal(t, int64(201), item.ID)
	require.Len(t, item.SchedulerScores, 1)
	require.Equal(t, groupID, *item.SchedulerScores[0].GroupID)
	require.Equal(t, item.SchedulerScores[0].BaseScore, item.SchedulerScore.BaseScore)
}

func TestAccountHandlerListSchedulerScoreIgnoresPagination(t *testing.T) {
	router, adminSvc := setupAccountListRouter()
	now := time.Now().UTC()
	visibleAccount := service.Account{
		ID:          301,
		Name:        "visible-low-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    100000,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	hiddenFilterPeer := service.Account{
		ID:          302,
		Name:        "hidden-high-priority",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 10,
		Priority:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	adminSvc.accounts = []service.Account{visibleAccount}
	adminSvc.accountSchedulerScoreFilterAccounts = []service.Account{visibleAccount, hiddenFilterPeer}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=1&platform=openai&include_scheduler_score=1", nil)
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Data struct {
			Items []struct {
				ID             int64 `json:"id"`
				SchedulerScore struct {
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_score"`
				SchedulerScores []struct {
					GroupID   *int64  `json:"group_id"`
					BaseScore float64 `json:"base_score"`
				} `json:"scheduler_scores"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 1)
	require.Equal(t, int64(301), payload.Data.Items[0].ID)
	require.Less(t, payload.Data.Items[0].SchedulerScore.BaseScore, 3.75)
	require.Empty(t, payload.Data.Items[0].SchedulerScores)
}

func TestAccountHandlerListIncludesTTFTGuardDegradationsForOpenAIOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	now := time.Now().UTC().Truncate(time.Second)
	adminSvc.accounts = []service.Account{
		{ID: 501, Name: "openai-account", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, CreatedAt: now, UpdatedAt: now},
		{ID: 502, Name: "anthropic-account", Platform: service.PlatformAnthropic, Type: service.AccountTypeOAuth, Status: service.StatusActive, Schedulable: true, CreatedAt: now, UpdatedAt: now},
	}
	degradedAt := now.Add(-10 * time.Minute)
	reader := &accountListTTFTGuardReader{states: map[int64][]service.OpenAITTFTGuardDegradation{
		501: {{Model: "gpt-5.4-mini", Reason: "critical_sample", ThresholdMs: 20_000, LastTTFTMs: 61_000, EWMAms: 61_000, SampleCount: 1, DegradedAt: degradedAt, LastSampleAt: now.Add(-time.Minute), ExpiresAt: now.Add(5 * time.Minute), RecoverySamplesRequired: 3}},
		502: {{Model: "should-not-leak", Reason: "critical_sample"}},
	}}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetOpenAITTFTGuardDegradationReader(reader)
	router.GET("/api/v1/admin/accounts", handler.List)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	firstETag := rec.Header().Get("ETag")
	require.NotEmpty(t, firstETag)
	require.Equal(t, []int64{501}, reader.accountIDs, "only OpenAI account IDs should be sent to the runtime reader")

	var payload struct {
		Data struct {
			Items []struct {
				ID                    int64                                `json:"id"`
				TTFTGuardDegradations []service.OpenAITTFTGuardDegradation `json:"ttft_guard_degradations"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Len(t, payload.Data.Items, 2)
	require.Len(t, payload.Data.Items[0].TTFTGuardDegradations, 1)
	require.Equal(t, "gpt-5.4-mini", payload.Data.Items[0].TTFTGuardDegradations[0].Model)
	require.Empty(t, payload.Data.Items[1].TTFTGuardDegradations)

	reader.states[501][0].RecoverySamples = 1
	changedRec := httptest.NewRecorder()
	changedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20", nil)
	changedReq.Header.Set("If-None-Match", firstETag)
	router.ServeHTTP(changedRec, changedReq)
	require.Equal(t, http.StatusOK, changedRec.Code)
	changedETag := changedRec.Header().Get("ETag")
	require.NotEqual(t, firstETag, changedETag, "runtime degradation changes must invalidate the account-list ETag")

	unchangedRec := httptest.NewRecorder()
	unchangedReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?page=1&page_size=20", nil)
	unchangedReq.Header.Set("If-None-Match", changedETag)
	router.ServeHTTP(unchangedRec, unchangedReq)
	require.Equal(t, http.StatusNotModified, unchangedRec.Code)
}

func TestAccountHandlerListSeparatesOrdinaryAndUpstreamScopes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	now := time.Now().UTC().Truncate(time.Second)
	configID, keyID := int64(81), int64(91)
	adminSvc.accounts = []service.Account{
		{ID: 601, Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, CreatedAt: now, UpdatedAt: now},
		{ID: 602, Name: "upstream", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: false, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, CreatedAt: now, UpdatedAt: now},
	}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	observedAt := now.Add(-time.Minute)
	healthReader := &accountListUpstreamHealthReader{states: map[int64][]service.UpstreamHealthObservation{
		keyID: {{ObservedAt: observedAt, State: service.UpstreamHealthHealthy, Source: "probe", Result: "success", Reason: "probe_succeeded"}},
	}}
	handler.SetUpstreamHealthHistoryReader(healthReader)
	router.GET("/api/v1/admin/accounts", handler.List)
	router.GET("/api/v1/admin/upstream-management/accounts", handler.ListUpstreamManagement)

	ordinaryRec := httptest.NewRecorder()
	router.ServeHTTP(ordinaryRec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts?scope=ordinary", nil))
	require.Equal(t, http.StatusOK, ordinaryRec.Code)

	upstreamRec := httptest.NewRecorder()
	router.ServeHTTP(upstreamRec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/accounts?upstream_config_id=81&upstream_key_id=91", nil))
	require.Equal(t, http.StatusOK, upstreamRec.Code)

	var ordinaryPayload, upstreamPayload struct {
		Data struct {
			Items []struct {
				ID               int64    `json:"id"`
				AvailableActions []string `json:"available_actions"`
				UpstreamHealth   struct {
					History []service.UpstreamHealthObservation `json:"history"`
				} `json:"upstream_health"`
			} `json:"items"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(ordinaryRec.Body.Bytes(), &ordinaryPayload))
	require.NoError(t, json.Unmarshal(upstreamRec.Body.Bytes(), &upstreamPayload))
	require.Equal(t, []int64{601}, []int64{ordinaryPayload.Data.Items[0].ID})
	require.Empty(t, ordinaryPayload.Data.Items[0].AvailableActions)
	require.Equal(t, []int64{602}, []int64{upstreamPayload.Data.Items[0].ID})
	require.ElementsMatch(t, []string{"edit", "test", "stats", "schedule", "toggle_schedulable", "recover_state", "probe_key", "toggle_observation", "events", "rate_trend"}, upstreamPayload.Data.Items[0].AvailableActions)
	require.Equal(t, []int64{keyID}, healthReader.keyIDs)
	require.Equal(t, service.UpstreamHealthListHistoryLimit, healthReader.limit)
	require.Len(t, upstreamPayload.Data.Items[0].UpstreamHealth.History, 1)
	require.Equal(t, "probe", upstreamPayload.Data.Items[0].UpstreamHealth.History[0].Source)
}

func TestAccountHandlerListRejectsInvalidUpstreamFilterID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(newStubAdminService(), nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.GET("/api/v1/admin/upstream-management/accounts", handler.ListUpstreamManagement)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/accounts?upstream_key_id=not-a-number", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAccountHandlerUpstreamHealthHistoryInvalidatesETag(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	adminSvc := newStubAdminService()
	now := time.Now().UTC().Truncate(time.Second)
	configID, keyID := int64(181), int64(191)
	adminSvc.accounts = []service.Account{{
		ID: 701, Name: "upstream-history", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
		CreatedAt: now, UpdatedAt: now,
	}}
	reader := &accountListUpstreamHealthReader{states: map[int64][]service.UpstreamHealthObservation{
		keyID: {{ObservedAt: now.Add(-time.Minute), State: service.UpstreamHealthHealthy, Source: "probe", Result: "success"}},
	}}
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handler.SetUpstreamHealthHistoryReader(reader)
	router.GET("/api/v1/admin/upstream-management/accounts", handler.ListUpstreamManagement)

	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/accounts", nil))
	require.Equal(t, http.StatusOK, first.Code)
	firstETag := first.Header().Get("ETag")
	require.NotEmpty(t, firstETag)
	require.Equal(t, []int64{keyID}, reader.keyIDs)

	reader.states[keyID] = append(reader.states[keyID], service.UpstreamHealthObservation{
		ObservedAt: now, State: service.UpstreamHealthDegraded, Source: "traffic", Result: "500", Reason: "upstream_server_error",
	})
	changed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/admin/upstream-management/accounts", nil)
	request.Header.Set("If-None-Match", firstETag)
	router.ServeHTTP(changed, request)
	require.Equal(t, http.StatusOK, changed.Code)
	require.NotEqual(t, firstETag, changed.Header().Get("ETag"))
}
