package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func disabledSub2APITestConfig(baseURL string) UpstreamConfig {
	return UpstreamConfig{
		ID: 7, Name: "Sub2API", Provider: UpstreamProviderSub2API,
		SiteURL: baseURL, AuthMode: UpstreamAuthModeManualJWT, Status: StatusActive,
		Credentials: map[string]any{AccountCredentialSub2APIAccessToken: "jwt-disabled-key-test"},
	}
}

func TestSyncSub2APIDisabledKeysStatusCompatibility(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status string
		keep   bool
	}{
		{name: "active", status: `,"status":"active"`, keep: true},
		{name: "inactive", status: `,"status":"inactive"`},
		{name: "disabled", status: `,"status":"disabled"`},
		{name: "normalized inactive", status: `,"status":"  InAcTiVe \t"`},
		{name: "normalized disabled", status: `,"status":"\t DiSaBlEd  "`},
		{name: "missing", keep: true},
		{name: "empty", status: `,"status":""`, keep: true},
		{name: "null", status: `,"status":null`, keep: true},
		{name: "expired", status: `,"status":"expired"`, keep: true},
		{name: "quota exhausted", status: `,"status":"quota_exhausted"`, keep: true},
		{name: "unknown", status: `,"status":"future_state"`, keep: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case sub2APIKeysPath:
					_, _ = fmt.Fprintf(w, `{"code":0,"data":{"items":[{"id":11,"key":"sk-status","group":{"id":3,"platform":"openai","rate_multiplier":0.2}%s}],"total":1,"pages":1}}`, tt.status)
				case "/api/v1/groups/available":
					_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
				case "/api/v1/groups/rates":
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := disabledSub2APITestConfig(server.URL)

			snapshot, err := syncSub2APIUpstreamSnapshot(context.Background(), &cfg, "", false)

			require.NoError(t, err)
			require.True(t, snapshot.KeysComplete)
			if tt.keep {
				require.Len(t, snapshot.Keys, 1)
				require.Equal(t, StatusActive, snapshot.Keys[0].Status)
				require.Equal(t, int64(11), *snapshot.Keys[0].RemoteKeyID)
			} else {
				require.Empty(t, snapshot.Keys)
			}
			require.Zero(t, snapshot.UnresolvedKeyCount)
		})
	}
}

func TestSyncSub2APIDisabledKeysPreservesRawPaginationValidation(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprintf("fallback=%v", fallback), func(t *testing.T) {
			var pages []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if fallback && r.URL.Path == sub2APIKeysPath {
					http.NotFound(w, r)
					return
				}
				switch r.URL.Path {
				case sub2APIKeysPath, sub2APIKeysFallback:
					page, err := strconv.Atoi(r.URL.Query().Get("page"))
					require.NoError(t, err)
					pages = append(pages, page)
					if page == 1 {
						_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-disabled","status":"disabled"},{"id":2,"key":"sk-inactive","status":"inactive"}],"total":3,"pages":2}}`))
					} else {
						_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":3,"key":"sk-active","status":"active","group":{"id":3,"platform":"openai","rate_multiplier":0.2}}],"total":3,"pages":2}}`))
					}
				case "/api/v1/groups/available":
					_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
				case "/api/v1/groups/rates":
					_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := disabledSub2APITestConfig(server.URL)

			snapshot, err := syncSub2APIUpstreamSnapshot(context.Background(), &cfg, "", false)

			require.NoError(t, err)
			require.Equal(t, []int{1, 2}, pages)
			require.True(t, snapshot.KeysComplete)
			require.Len(t, snapshot.Keys, 1)
			require.Equal(t, int64(3), *snapshot.Keys[0].RemoteKeyID)
		})
	}
}

type disabledSub2APISnapshotRepo struct {
	*upstreamConfigAtomicCaptureRepo
	fallbackCalls int
	appliedKeys   [][]UpstreamKey
}

func (r *disabledSub2APISnapshotRepo) ListKeysForMaskedFallback(context.Context, int64, []int64) ([]UpstreamKey, error) {
	r.fallbackCalls++
	return nil, fmt.Errorf("disabled keys must not load masked credentials or previous pricing")
}

func (r *disabledSub2APISnapshotRepo) ApplySyncSnapshot(ctx context.Context, configID, runID int64, keys []UpstreamKey, extra map[string]any, checkedAt time.Time, complete bool) ([]UpstreamKey, UpstreamKeyReconcileResult, int, error) {
	r.appliedKeys = append(r.appliedKeys, append([]UpstreamKey(nil), keys...))
	return r.upstreamConfigAtomicCaptureRepo.ApplySyncSnapshot(ctx, configID, runID, keys, extra, checkedAt, complete)
}

type disabledSub2APIAccountRepo struct {
	*sub2APIRateSyncAccountRepo
	bindingCalls int
	createCalls  int
}

func (r *disabledSub2APIAccountRepo) ListByUpstreamKeyID(context.Context, int64) ([]Account, error) {
	r.bindingCalls++
	return nil, nil
}

func (r *disabledSub2APIAccountRepo) Create(context.Context, *Account) error {
	r.createCalls++
	return nil
}

func TestUpstreamConfigServiceSub2APIDisabledKeysApplyCompleteEmptySnapshot(t *testing.T) {
	var billingCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case sub2APIKeysPath:
			// These records would require fallback or yield unsupported-platform
			// warnings if processed before the disabled-status filter.
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-***","status":"inactive","group":{"id":3,"platform":"future-platform"}},{"id":2,"key":"sk-disabled","status":"disabled","group":{"id":4,"platform":"future-platform","rate_multiplier":-1}}],"total":2,"pages":1}}`))
		case "/api/v1/groups/available":
			_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case sub2APIProfilePath:
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":1,"balance":1,"total_recharged":1}}`))
		case "/v1/sub2api/billing":
			billingCalls.Add(1)
			http.Error(w, "unexpected disabled-key billing probe", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := disabledSub2APITestConfig(server.URL)
	repo := &disabledSub2APISnapshotRepo{upstreamConfigAtomicCaptureRepo: &upstreamConfigAtomicCaptureRepo{
		upstreamConfigServiceRepo: &upstreamConfigServiceRepo{configs: []UpstreamConfig{cfg}},
	}}
	accounts := &disabledSub2APIAccountRepo{sub2APIRateSyncAccountRepo: &sub2APIRateSyncAccountRepo{}}
	svc := NewUpstreamConfigService(repo, nil, accounts)

	keys, result, err := svc.SyncKeys(context.Background(), cfg.ID)

	require.NoError(t, err)
	require.Empty(t, keys)
	require.True(t, result.Success)
	require.Equal(t, UpstreamSyncStatusSucceeded, result.Status)
	require.Equal(t, []bool{true}, repo.completeValues)
	require.Len(t, repo.appliedKeys, 1)
	require.Empty(t, repo.appliedKeys[0])
	require.Zero(t, result.KeyCount)
	require.Zero(t, result.FallbackKeyCount)
	require.Zero(t, result.UnresolvedKeyCount)
	require.Zero(t, result.UpdatedAccountCount)
	require.Empty(t, result.Warnings)
	require.Zero(t, repo.fallbackCalls)
	require.Zero(t, accounts.bindingCalls)
	require.Zero(t, accounts.createCalls)
	require.Zero(t, billingCalls.Load())
}

func TestUpstreamConfigServiceSub2APIDisabledKeysDoNotApplyIncompletePages(t *testing.T) {
	for _, failure := range []string{"http failure", "short total", "changing total", "duplicate ids", "invalid id"} {
		t.Run(failure, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != sub2APIKeysPath {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("page") == "1" {
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-disabled","status":"disabled"}],"total":2,"pages":2}}`))
					return
				}
				switch failure {
				case "http failure":
					http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
				case "short total":
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[],"total":2,"pages":2}}`))
				case "changing total":
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":2,"key":"sk-active"}],"total":3,"pages":2}}`))
				case "duplicate ids":
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-disabled","status":"disabled"}],"total":2,"pages":2}}`))
				case "invalid id":
					_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":0,"key":"sk-disabled","status":"disabled"}],"total":2,"pages":2}}`))
				}
			}))
			defer server.Close()
			cfg := disabledSub2APITestConfig(server.URL)
			repo := &disabledSub2APISnapshotRepo{upstreamConfigAtomicCaptureRepo: &upstreamConfigAtomicCaptureRepo{
				upstreamConfigServiceRepo: &upstreamConfigServiceRepo{configs: []UpstreamConfig{cfg}},
			}}
			svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

			_, result, err := svc.SyncKeys(context.Background(), cfg.ID)

			require.Error(t, err)
			require.False(t, result.Success)
			require.Empty(t, repo.completeValues)
			require.Empty(t, repo.appliedKeys)
			require.Zero(t, repo.fallbackCalls)
		})
	}
}

func TestSyncSub2APIDisabledKeysAuthHandleRetainsRawStatus(t *testing.T) {
	var raw sub2APIUpstreamKey
	require.NoError(t, json.Unmarshal([]byte(`{"id":19,"key":"sk-***","status":"DISABLED","group":{"id":4,"platform":"future-platform"}}`), &raw))
	cfg := disabledSub2APITestConfig("https://sub2api-disabled.example")
	ctx := withUpstreamAuthHandle(context.Background(), &UpstreamAuthHandle{Value: sub2APIAuthValue{
		Session: &sub2APIUserLoginSession{keys: []sub2APIUpstreamKey{raw}, keysComplete: true},
	}})

	snapshot, err := syncSub2APIUpstreamSnapshot(ctx, &cfg, "", false)

	require.NoError(t, err)
	require.True(t, snapshot.KeysComplete)
	require.Empty(t, snapshot.Keys)
	require.Zero(t, snapshot.UnresolvedKeyCount)
	require.Empty(t, snapshot.Warnings)
}
