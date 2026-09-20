package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type dataResponse struct {
	Code int         `json:"code"`
	Data dataPayload `json:"data"`
}

type dataPayload struct {
	Type                    string        `json:"type"`
	Version                 int           `json:"version"`
	Proxies                 []dataProxy   `json:"proxies"`
	Accounts                []dataAccount `json:"accounts"`
	SkippedShadows          int           `json:"skipped_shadows"`
	SkippedUpstreamAccounts int           `json:"skipped_upstream_accounts"`
}

type dataProxy struct {
	ProxyKey string `json:"proxy_key"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Status   string `json:"status"`
}

type dataAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
	Extra       map[string]any `json:"extra"`
	ProxyKey    *string        `json:"proxy_key"`
	Concurrency int            `json:"concurrency"`
	Priority    int            `json:"priority"`
}

func setupAccountDataRouter() (*gin.Engine, *stubAdminService) {
	adminSvc := newStubAdminService()
	return setupAccountDataRouterWithService(adminSvc), adminSvc
}

func setupAccountDataRouterWithService(adminSvc service.AdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()

	h := NewAccountHandler(
		adminSvc,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
	)
	if importSvc, ok := adminSvc.(*accountDataImportAdminService); ok {
		h.SetProxyIPGroupService(service.NewProxyIPGroupAdminService(
			&accountDataProxyIPGroupRepo{groups: importSvc.proxyGroupsByID},
			nil,
		))
	}

	router.GET("/api/v1/admin/accounts/data", h.ExportData)
	router.POST("/api/v1/admin/accounts/data", h.ImportData)
	return router
}

type accountDataImportAdminService struct {
	*stubAdminService
	groupsByID      map[int64]*service.Group
	proxyGroupsByID map[int64]*service.ProxyIPGroup
	nextAccountID   int64
}

func newAccountDataImportAdminService() *accountDataImportAdminService {
	return &accountDataImportAdminService{
		stubAdminService: newStubAdminService(),
		groupsByID:       make(map[int64]*service.Group),
		proxyGroupsByID:  make(map[int64]*service.ProxyIPGroup),
		nextAccountID:    300,
	}
}

type accountDataProxyIPGroupRepo struct {
	groups map[int64]*service.ProxyIPGroup
}

func (r *accountDataProxyIPGroupRepo) GetByID(_ context.Context, id int64) (*service.ProxyIPGroup, error) {
	group, ok := r.groups[id]
	if !ok {
		return nil, service.ErrProxyIPGroupNotFound
	}
	clone := *group
	clone.ProxyIDs = append([]int64(nil), group.ProxyIDs...)
	return &clone, nil
}

func (r *accountDataProxyIPGroupRepo) List(context.Context) ([]service.ProxyIPGroup, error) {
	return nil, nil
}

func (r *accountDataProxyIPGroupRepo) Create(context.Context, *service.ProxyIPGroup) error {
	return nil
}

func (r *accountDataProxyIPGroupRepo) Update(context.Context, *service.ProxyIPGroup) error {
	return nil
}

func (r *accountDataProxyIPGroupRepo) Delete(context.Context, int64) error { return nil }

func (r *accountDataProxyIPGroupRepo) CountAccounts(context.Context, int64) (int64, error) {
	return 0, nil
}

func (s *accountDataImportAdminService) GetGroup(_ context.Context, id int64) (*service.Group, error) {
	group, ok := s.groupsByID[id]
	if !ok {
		return nil, service.ErrGroupNotFound
	}
	clone := *group
	return &clone, nil
}

func (s *accountDataImportAdminService) CreateAccount(ctx context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	created, err := s.stubAdminService.CreateAccount(ctx, input)
	if err != nil {
		return nil, err
	}
	s.nextAccountID++
	created.ID = s.nextAccountID
	created.Platform = input.Platform
	created.Type = input.Type
	return created, nil
}

func TestExportDataIncludesSecrets(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()

	proxyID := int64(11)
	adminSvc.proxies = []service.Proxy{
		{
			ID:       proxyID,
			Name:     "proxy",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   service.StatusActive,
		},
		{
			ID:       12,
			Name:     "orphan",
			Protocol: "https",
			Host:     "10.0.0.1",
			Port:     443,
			Username: "o",
			Password: "p",
			Status:   service.StatusActive,
		},
	}
	adminSvc.accounts = []service.Account{
		{
			ID:          21,
			Name:        "account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			Extra:       map[string]any{"note": "x"},
			ProxyID:     &proxyID,
			Concurrency: 3,
			Priority:    50,
			Status:      service.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Empty(t, resp.Data.Type)
	require.Equal(t, 0, resp.Data.Version)
	require.Len(t, resp.Data.Proxies, 1)
	require.Equal(t, "pass", resp.Data.Proxies[0].Password)
	require.Len(t, resp.Data.Accounts, 1)
	require.Equal(t, "secret", resp.Data.Accounts[0].Credentials["token"])
}

func TestExportDataWithoutProxies(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()

	proxyID := int64(11)
	adminSvc.proxies = []service.Proxy{
		{
			ID:       proxyID,
			Name:     "proxy",
			Protocol: "http",
			Host:     "127.0.0.1",
			Port:     8080,
			Username: "user",
			Password: "pass",
			Status:   service.StatusActive,
		},
	}
	adminSvc.accounts = []service.Account{
		{
			ID:          21,
			Name:        "account",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			ProxyID:     &proxyID,
			Concurrency: 3,
			Priority:    50,
			Status:      service.StatusDisabled,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Proxies, 0)
	require.Len(t, resp.Data.Accounts, 1)
	require.Nil(t, resp.Data.Accounts[0].ProxyKey)
}

// TestExportDataExcludesSparkShadow 验证外审第5轮 P1/P2:导出时排除 spark 影子账号
// (影子无凭据、导入侧强制 credentials 非空,混入会产出无法还原的坏备份),并透出跳过计数。
func TestExportDataExcludesSparkShadow(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()

	parentID := int64(21)
	adminSvc.accounts = []service.Account{
		{
			ID:          parentID,
			Name:        "mother",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeOAuth,
			Credentials: map[string]any{"token": "secret"},
			Status:      service.StatusActive,
		},
		{
			ID:              22,
			Name:            "mother (Spark)",
			Platform:        service.PlatformOpenAI,
			Type:            service.AccountTypeOAuth,
			Credentials:     map[string]any{}, // 影子恒空凭据
			ParentAccountID: &parentID,        // 影子标记
			QuotaDimension:  service.QuotaDimensionSpark,
			Status:          service.StatusActive,
		},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Accounts, 1, "影子应被排除,仅导出母账号")
	require.Equal(t, "mother", resp.Data.Accounts[0].Name)
	require.Equal(t, 1, resp.Data.SkippedShadows, "跳过的影子数量应透出")
}

func TestExportDataExcludesUpstreamBoundAccount(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	cfgID := int64(10)
	keyID := int64(20)
	adminSvc.accounts = []service.Account{
		{ID: 1, Name: "ordinary", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Credentials: map[string]any{"api_key": "sk-local"}},
		{ID: 2, Name: "可达鸭-pro", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, UpstreamConfigID: &cfgID, UpstreamKeyID: &keyID},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Accounts, 1)
	require.Equal(t, "ordinary", resp.Data.Accounts[0].Name)
	require.Equal(t, 1, resp.Data.SkippedUpstreamAccounts)
}

func TestExportDataPassesAccountFiltersAndSort(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	adminSvc.accounts = []service.Account{
		{ID: 1, Name: "acc-1", Status: service.StatusActive},
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/accounts/data?platform=openai&type=oauth&status=active&group=12&privacy_mode=blocked&search=keyword&sort_by=priority&sort_order=desc",
		nil,
	)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Equal(t, 1, adminSvc.lastListAccounts.calls)
	require.Equal(t, "openai", adminSvc.lastListAccounts.platform)
	require.Equal(t, "oauth", adminSvc.lastListAccounts.accountType)
	require.Equal(t, "active", adminSvc.lastListAccounts.status)
	require.Equal(t, int64(12), adminSvc.lastListAccounts.groupID)
	require.Equal(t, "blocked", adminSvc.lastListAccounts.privacyMode)
	require.Equal(t, "keyword", adminSvc.lastListAccounts.search)
	require.Equal(t, "priority", adminSvc.lastListAccounts.sortBy)
	require.Equal(t, "desc", adminSvc.lastListAccounts.sortOrder)
}

func TestExportDataSelectedIDsOverrideFilters(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/admin/accounts/data?ids=1,2&platform=openai&search=keyword&sort_by=priority&sort_order=desc",
		nil,
	)
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, 0, resp.Code)
	require.Len(t, resp.Data.Accounts, 2)
	require.Equal(t, 0, adminSvc.lastListAccounts.calls)
}

func TestImportDataReusesProxyAndSkipsDefaultGroup(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()

	adminSvc.proxies = []service.Proxy{
		{
			ID:       1,
			Name:     "proxy",
			Protocol: "socks5",
			Host:     "1.2.3.4",
			Port:     1080,
			Username: "u",
			Password: "p",
			Status:   service.StatusActive,
		},
	}

	dataPayload := map[string]any{
		"data": map[string]any{
			"type":    dataType,
			"version": dataVersion,
			"proxies": []map[string]any{
				{
					"proxy_key": "socks5|1.2.3.4|1080|u|p",
					"name":      "proxy",
					"protocol":  "socks5",
					"host":      "1.2.3.4",
					"port":      1080,
					"username":  "u",
					"password":  "p",
					"status":    "active",
				},
			},
			"accounts": []map[string]any{
				{
					"name":        "acc",
					"platform":    service.PlatformOpenAI,
					"type":        service.AccountTypeOAuth,
					"credentials": map[string]any{"token": "x"},
					"proxy_key":   "socks5|1.2.3.4|1080|u|p",
					"concurrency": 3,
					"priority":    50,
				},
			},
		},
		"skip_default_group_bind": true,
	}

	body, _ := json.Marshal(dataPayload)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	require.Len(t, adminSvc.createdProxies, 0)
	require.Len(t, adminSvc.createdAccounts, 1)
	require.True(t, adminSvc.createdAccounts[0].SkipDefaultGroupBind)
}

func TestImportDataRejectsDeprecatedCopyProxyIDs(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	data := map[string]any{
		"data": map[string]any{
			"type": dataType, "version": dataVersion, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "codex", "platform": service.PlatformOpenAI, "type": service.AccountTypeOAuth,
				"credentials": map[string]any{"token": "x"},
				"concurrency": 2, "rate_multiplier": 0.5,
			}},
		},
		"copy_proxy_ids": []int64{11, 12, 11},
	}
	body, _ := json.Marshal(data)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"reason":"COPY_PROXY_IMPORT_DEPRECATED"`)
	require.Empty(t, adminSvc.createdAccounts)
}

func TestImportDataUsesProxyGroupAndBindsNormalizedPreferredGroups(t *testing.T) {
	adminSvc := newAccountDataImportAdminService()
	adminSvc.proxyGroupsByID[22] = &service.ProxyIPGroup{ID: 22, Name: "Asia", PerIPConcurrency: 10, ProxyIDs: []int64{11, 12}}
	adminSvc.groupsByID[7] = &service.Group{ID: 7, Platform: service.PlatformOpenAI, Status: service.StatusActive}
	adminSvc.groupsByID[9] = &service.Group{ID: 9, Platform: service.PlatformComposite, Status: service.StatusActive}
	router := setupAccountDataRouterWithService(adminSvc)

	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"type": dataType, "version": dataVersion, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "codex", "platform": service.PlatformOpenAI, "type": service.AccountTypeOAuth,
				"credentials": map[string]any{"token": "x"}, "concurrency": 2, "priority": 50,
			}},
		},
		"proxy_ip_group_id":               22,
		"override_concurrency":            4,
		"override_priority":               1,
		"override_rate_multiplier":        0,
		"override_codex_fingerprint_mode": "session",
		"group_ids":                       []int64{7, 9, 7},
		"preferred_group_ids":             []int64{9, 9},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, adminSvc.createdAccounts, 1)
	created := adminSvc.createdAccounts[0]
	require.Nil(t, created.ProxyID)
	require.NotNil(t, created.ProxyIPGroupID)
	require.Equal(t, int64(22), *created.ProxyIPGroupID)
	require.Equal(t, []int64{7, 9}, created.GroupIDs)
	require.NotNil(t, created.PreferredGroupIDs)
	require.Equal(t, []int64{9}, *created.PreferredGroupIDs)
	require.True(t, created.SkipDefaultGroupBind)
	require.False(t, created.SkipMixedChannelCheck)
	require.Equal(t, 4, created.Concurrency)
	require.Equal(t, 1, created.Priority)
	require.NotNil(t, created.RateMultiplier)
	require.Equal(t, float64(0), *created.RateMultiplier)
	require.Equal(t, "session", created.Extra["codex_fingerprint_mode"])
	require.NotContains(t, created.Extra, "codex_fingerprint_seed")
	require.NotContains(t, created.Extra, "codex_import_replica_fingerprint_seed")
}

func TestImportDataExplicitEmptyGroupsSuppressDefaultBinding(t *testing.T) {
	adminSvc := newAccountDataImportAdminService()
	router := setupAccountDataRouterWithService(adminSvc)
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"type": dataType, "version": dataVersion, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
		},
		"group_ids":           []int64{},
		"preferred_group_ids": []int64{},
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, adminSvc.createdAccounts, 1)
	require.True(t, adminSvc.createdAccounts[0].SkipDefaultGroupBind)
	require.NotNil(t, adminSvc.createdAccounts[0].PreferredGroupIDs)
	require.Empty(t, *adminSvc.createdAccounts[0].PreferredGroupIDs)
}

func TestImportDataMissingGroupFieldsPreservesLegacyBindingBehavior(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"type": dataType, "version": dataVersion, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
		},
		"skip_default_group_bind": false,
	})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, adminSvc.createdAccounts, 1)
	require.False(t, adminSvc.createdAccounts[0].SkipDefaultGroupBind)
}

func TestImportDataRejectsInvalidGroupSelectionsBeforeCreation(t *testing.T) {
	tests := []struct {
		name      string
		accounts  []any
		groups    map[int64]*service.Group
		groupIDs  any
		preferred any
	}{
		{
			name: "preferred field missing",
			accounts: []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
			groupIDs: []int64{7},
		},
		{
			name: "preferred group is not selected",
			accounts: []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
			groupIDs: []int64{7}, preferred: []int64{8},
		},
		{
			name: "mixed platform import",
			accounts: []any{
				map[string]any{"name": "openai", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey, "credentials": map[string]any{"api_key": "x"}},
				map[string]any{"name": "anthropic", "platform": service.PlatformAnthropic, "type": service.AccountTypeAPIKey, "credentials": map[string]any{"api_key": "y"}},
			},
			groups:   map[int64]*service.Group{9: {ID: 9, Platform: service.PlatformComposite}},
			groupIDs: []int64{9}, preferred: []int64{},
		},
		{
			name: "group platform mismatch",
			accounts: []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
			groups:   map[int64]*service.Group{7: {ID: 7, Platform: service.PlatformAnthropic}},
			groupIDs: []int64{7}, preferred: []int64{},
		},
		{
			name: "group does not exist",
			accounts: []any{map[string]any{
				"name": "account", "platform": service.PlatformOpenAI, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"},
			}},
			groupIDs: []int64{404}, preferred: []int64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adminSvc := newAccountDataImportAdminService()
			adminSvc.groupsByID = tt.groups
			if adminSvc.groupsByID == nil {
				adminSvc.groupsByID = make(map[int64]*service.Group)
			}
			router := setupAccountDataRouterWithService(adminSvc)
			payload := map[string]any{
				"data": map[string]any{
					"type": dataType, "version": dataVersion, "proxies": []any{}, "accounts": tt.accounts,
				},
				"group_ids": tt.groupIDs,
			}
			if tt.preferred != nil {
				payload["preferred_group_ids"] = tt.preferred
			}
			body, err := json.Marshal(payload)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Empty(t, adminSvc.createdAccounts)
		})
	}
}

func TestNormalizeDataImportOptionsRejectsDeprecatedCopyProxyIDs(t *testing.T) {
	req := DataImportRequest{CopyProxyIDs: []int64{1, 2, 1}}
	err := normalizeDataImportOptions(&req)
	require.Error(t, err)
	require.Equal(t, "COPY_PROXY_IMPORT_DEPRECATED", infraerrors.Reason(err))
}

func TestImportDataIgnoresCodexOverrideForNonOpenAIAccounts(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	data := map[string]any{
		"data": map[string]any{
			"type": dataType, "version": dataVersion, "proxies": []any{},
			"accounts": []any{map[string]any{
				"name": "gemini", "platform": service.PlatformGemini, "type": service.AccountTypeAPIKey,
				"credentials": map[string]any{"api_key": "x"}, "extra": map[string]any{"region": "global"},
				"concurrency": 2,
			}},
		},
		"override_codex_fingerprint_mode": "full",
	}
	body, _ := json.Marshal(data)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, adminSvc.createdAccounts, 1)
	require.Equal(t, map[string]any{"region": "global"}, adminSvc.createdAccounts[0].Extra)
}

func TestExportDataExcludesCodexTicketMaterial(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	extra := map[string]any{
		"codex_turn_ticket:gpt-6-astra": map[string]any{"state": "private-ticket-blob", "length": 292},
		"codex_harvest_proxy_url":       "http://user:legacy-proxy-secret@proxy.example.com:8080",
		"ordinary":                      "retained",
	}
	adminSvc.accounts = []service.Account{{ID: 21, Name: "account", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, Credentials: map[string]any{"access_token": "backup-token"}, Extra: extra}}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/accounts/data?include_proxies=false", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var resp dataResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data.Accounts, 1)
	require.Equal(t, map[string]any{"ordinary": "retained"}, resp.Data.Accounts[0].Extra)
	require.Equal(t, "backup-token", resp.Data.Accounts[0].Credentials["access_token"])
	require.NotContains(t, rec.Body.String(), "private-ticket-blob")
	require.NotContains(t, rec.Body.String(), "legacy-proxy-secret")
	require.Contains(t, extra, "codex_turn_ticket:gpt-6-astra")
	require.Contains(t, extra, "codex_harvest_proxy_url")
}

func TestImportDataStripsHistoricalCodexTicketMaterial(t *testing.T) {
	router, adminSvc := setupAccountDataRouter()
	extra := map[string]any{
		"codex_turn_ticket:custom":     map[string]any{"state": "private-state"},
		"codex_ticket_config":          map[string]any{"proxy_url": "http://private@host:8080"},
		"codex_ticket_watchdog":        map[string]any{"last_reason": "private-watchdog"},
		"codex_ticket_watchdog:custom": "private-model-watchdog",
		"codex_harvest_proxy_url":      "http://private-legacy@host:8080", "custom": true,
	}
	body, err := json.Marshal(map[string]any{"data": map[string]any{
		"type": dataType, "version": dataVersion, "proxies": []any{},
		"accounts": []any{map[string]any{
			"name": "legacy", "platform": service.PlatformOpenAI, "type": service.AccountTypeOAuth,
			"credentials": map[string]any{"access_token": "token"}, "extra": extra, "concurrency": 1,
		}},
	}})
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/data", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, adminSvc.createdAccounts, 1)
	require.Equal(t, map[string]any{"custom": true}, adminSvc.createdAccounts[0].Extra)
	require.NotContains(t, rec.Body.String(), "private-")
	require.Len(t, extra, 6)
}
