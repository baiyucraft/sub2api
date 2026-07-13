package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseOptionalUpstreamConcurrency(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		want       int64
		wantRaw    any
		wantAbsent bool
		wantErr    bool
	}{
		{name: "missing", wantAbsent: true},
		{name: "json zero", raw: `0`, want: 0, wantRaw: int64(0)},
		{name: "json integer", raw: `42`, want: 42, wantRaw: int64(42)},
		{name: "json max int64", raw: `9223372036854775807`, want: 9223372036854775807, wantRaw: int64(9223372036854775807)},
		{name: "decimal string", raw: `"42"`, want: 42, wantRaw: "42"},
		{name: "decimal string leading zeros", raw: `"00042"`, want: 42, wantRaw: "00042"},
		{name: "negative number", raw: `-1`, wantErr: true},
		{name: "fraction", raw: `1.5`, wantErr: true},
		{name: "exponent", raw: `1e3`, wantErr: true},
		{name: "negative string", raw: `"-1"`, wantErr: true},
		{name: "fraction string", raw: `"1.5"`, wantErr: true},
		{name: "exponent string", raw: `"1e3"`, wantErr: true},
		{name: "padded string", raw: `" 1 "`, wantErr: true},
		{name: "number overflow", raw: `9223372036854775808`, wantErr: true},
		{name: "string overflow", raw: `"9223372036854775808"`, wantErr: true},
		{name: "null", raw: `null`, wantErr: true},
		{name: "boolean", raw: `true`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, rawValue, present, err := parseOptionalUpstreamConcurrency(json.RawMessage(tt.raw))
			if tt.wantAbsent {
				require.False(t, present)
				require.NoError(t, err)
				return
			}
			require.True(t, present)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, value)
			require.Equal(t, tt.wantRaw, rawValue)
		})
	}
}

func TestUpstreamConcurrencySnapshotSemantics(t *testing.T) {
	t.Run("sub2api limited", func(t *testing.T) {
		updates, warning := upstreamConcurrencySnapshotUpdates(&UpstreamConfig{}, UpstreamProviderSub2API, json.RawMessage(`12`), nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusCurrent, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsLimited, snapshot["semantics"])
		require.Equal(t, "12", snapshot["raw_value"])
		require.Equal(t, "12", snapshot["limit"])
		require.NotEmpty(t, snapshot["observed_at"])
	})

	t.Run("sub2api zero is unlimited", func(t *testing.T) {
		updates, warning := upstreamConcurrencySnapshotUpdates(&UpstreamConfig{}, UpstreamProviderSub2API, json.RawMessage(`"0"`), nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusCurrent, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnlimited, snapshot["semantics"])
		require.Equal(t, "0", snapshot["raw_value"])
		require.NotContains(t, snapshot, "limit")
	})

	t.Run("max int64 string stays exact", func(t *testing.T) {
		updates, warning := upstreamConcurrencySnapshotUpdates(&UpstreamConfig{}, UpstreamProviderSub2API, json.RawMessage(`"9223372036854775807"`), nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusCurrent, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsLimited, snapshot["semantics"])
		require.Equal(t, "9223372036854775807", snapshot["raw_value"])
		require.Equal(t, "9223372036854775807", snapshot["limit"])
	})

	t.Run("newapi keeps only raw value", func(t *testing.T) {
		updates, warning := upstreamConcurrencySnapshotUpdates(&UpstreamConfig{}, UpstreamProviderNewAPI, json.RawMessage(`"0042"`), nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusCurrent, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsProviderDefined, snapshot["semantics"])
		require.Equal(t, "0042", snapshot["raw_value"])
		require.NotContains(t, snapshot, "limit")
	})

	t.Run("missing replaces history with unsupported", func(t *testing.T) {
		cfg := &UpstreamConfig{Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
			"version": 1, "provider": UpstreamProviderNewAPI, "status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsProviderDefined, "raw_value": "99",
		}}}
		updates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, nil, nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusUnsupported, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "raw_value")
		require.NotContains(t, snapshot, "observed_at")
	})

	t.Run("invalid replaces history and warning is redacted", func(t *testing.T) {
		cfg := &UpstreamConfig{Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{"limit": int64(99)}}}
		updates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, json.RawMessage(`"sk-sensitive-value"`), nil)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Equal(t, upstreamConcurrencyStatusUnsupported, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "limit")
		require.NotContains(t, snapshot, "raw_value")
		require.NotEmpty(t, warning)
		require.NotContains(t, warning, "sk-sensitive-value")
		require.NotContains(t, snapshot, "observed_at")
	})

	t.Run("profile failure preserves history as stale", func(t *testing.T) {
		cfg := &UpstreamConfig{Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
			"version": 1, "provider": UpstreamProviderSub2API, "status": upstreamConcurrencyStatusCurrent, "semantics": upstreamConcurrencySemanticsLimited, "raw_value": "8", "limit": "8", "observed_at": "earlier",
		}}}
		updates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderSub2API, nil, context.DeadlineExceeded)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsLimited, snapshot["semantics"])
		require.Equal(t, "8", snapshot["raw_value"])
		require.Equal(t, "8", snapshot["limit"])
		require.Equal(t, "earlier", snapshot["observed_at"])
	})

	t.Run("first profile failure is stale without value", func(t *testing.T) {
		updates, warning := upstreamConcurrencySnapshotUpdates(&UpstreamConfig{}, UpstreamProviderNewAPI, nil, context.DeadlineExceeded)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "observed_at")
		require.NotContains(t, snapshot, "raw_value")
		require.NotContains(t, snapshot, "limit")
	})

	t.Run("profile failure does not preserve unsupported history", func(t *testing.T) {
		cfg := &UpstreamConfig{Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
			"version": 1, "provider": UpstreamProviderNewAPI, "status": upstreamConcurrencyStatusUnsupported, "semantics": upstreamConcurrencySemanticsUnknown,
			"raw_value": "must-not-survive", "limit": int64(99), "observed_at": "invalid",
		}}}
		updates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, nil, context.DeadlineExceeded)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "raw_value")
		require.NotContains(t, snapshot, "limit")
		require.NotContains(t, snapshot, "observed_at")
	})

	t.Run("profile failure does not inherit another provider", func(t *testing.T) {
		cfg := &UpstreamConfig{Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
			"version": 1, "provider": UpstreamProviderSub2API, "status": upstreamConcurrencyStatusCurrent,
			"semantics": upstreamConcurrencySemanticsLimited, "raw_value": "8", "limit": "8", "observed_at": "earlier",
		}}}
		updates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, nil, context.DeadlineExceeded)
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Empty(t, warning)
		require.Equal(t, UpstreamProviderNewAPI, snapshot["provider"])
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "raw_value")
		require.NotContains(t, snapshot, "limit")
		require.NotContains(t, snapshot, "observed_at")
	})
}

func TestNilProfileWithoutErrorWritesStaleConcurrencySnapshot(t *testing.T) {
	t.Run("sub2api preserves history", func(t *testing.T) {
		cfg := &UpstreamConfig{Credentials: map[string]any{}, Extra: map[string]any{upstreamConcurrencySnapshotKey: map[string]any{
			"version": 1, "provider": UpstreamProviderSub2API, "status": upstreamConcurrencyStatusCurrent,
			"semantics": upstreamConcurrencySemanticsLimited, "raw_value": "7", "limit": "7", "observed_at": "earlier",
		}}}
		updates, warning := sub2APIProfileExtraUpdates(cfg, nil, nil)
		require.Empty(t, warning)
		require.NotContains(t, updates, "sub2api_balance")
		require.Contains(t, updates["sub2api_balance_last_error"], "null data")
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsLimited, snapshot["semantics"])
		require.Equal(t, "7", snapshot["raw_value"])
		require.Equal(t, "7", snapshot["limit"])
		require.Equal(t, "earlier", snapshot["observed_at"])
	})

	t.Run("newapi first failure has no value", func(t *testing.T) {
		cfg := &UpstreamConfig{Credentials: map[string]any{}}
		updates, warning := newAPIProfileExtraUpdates(cfg, nil, nil, nil, nil)
		require.Empty(t, warning)
		require.NotContains(t, updates, "upstream_provider_snapshot")
		require.Contains(t, updates["upstream_provider_snapshot_last_error"], "null data")
		snapshot := requireConcurrencySnapshot(t, updates)
		require.Equal(t, upstreamConcurrencyStatusStale, snapshot["status"])
		require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
		require.NotContains(t, snapshot, "raw_value")
		require.NotContains(t, snapshot, "limit")
		require.NotContains(t, snapshot, "observed_at")
	})
}

func TestUpstreamConfigServicePersistsSub2APIConcurrencySnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.07}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/v1/auth/me":
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":7,"email":"owner@example.com","balance":10,"total_recharged":20,"concurrency":"16"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID: 81, Name: "Sub2API", Provider: UpstreamProviderSub2API, SiteURL: server.URL,
		AuthMode: UpstreamAuthModeUserLogin, Status: StatusActive, Sub2APINotInCNConfirmed: true,
		Credentials: map[string]any{
			AccountCredentialSub2APILoginEmail: "owner@example.com", AccountCredentialSub2APILoginPassword: "secret",
		},
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), 81)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Len(t, repo.extraUpdates, 1)
	snapshot := requireConcurrencySnapshot(t, repo.extraUpdates[0].updates)
	require.Equal(t, upstreamConcurrencyStatusCurrent, snapshot["status"])
	require.Equal(t, upstreamConcurrencySemanticsLimited, snapshot["semantics"])
	require.Equal(t, "16", snapshot["raw_value"])
	require.Equal(t, "16", snapshot["limit"])
}

func TestUpstreamConfigServiceNewAPIInvalidConcurrencyWarnsWithoutChangingAccountConcurrency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/user/login":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798}}`))
		case "/api/user/self/groups":
			_, _ = w.Write([]byte(`{"success":true,"data":{"default":{"ratio":0.06}}}`))
		case "/api/token/":
			_, _ = w.Write([]byte(`{"success":true,"data":{"page":1,"page_size":100,"total":1,"items":[{"id":14287,"user_id":4798,"key":"sk-plus","status":1,"name":"plus","group":"default"}]}}`))
		case "/api/user/self":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":4798,"quota":10,"used_quota":4,"concurrency":"1e3"}}`))
		case "/api/status":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota_per_unit":500000}}`))
		case "/api/log/self/stat":
			_, _ = w.Write([]byte(`{"success":true,"data":{"quota":2}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	configID := int64(82)
	keyID := int64(88)
	repo := &upstreamConfigServiceRepo{
		configs: []UpstreamConfig{{
			ID: configID, Name: "NewAPI", Provider: UpstreamProviderNewAPI, SiteURL: server.URL,
			AuthMode: UpstreamAuthModeUserLogin, Status: StatusActive,
			Credentials: map[string]any{
				AccountCredentialNewAPILoginUsername: "owner", AccountCredentialNewAPILoginPassword: "secret",
			},
		}},
		keys: []UpstreamKey{{
			ID: keyID, UpstreamConfigID: configID, Key: "sk-plus", KeyHash: HashUpstreamKey("sk-plus"), Platform: PlatformOpenAI, Status: StatusActive,
		}},
	}
	accountRepo := &sub2APIRateSyncAccountRepo{accounts: []Account{{
		ID: 501, Type: AccountTypeAPIKey, Status: StatusActive, UpstreamConfigID: &configID, UpstreamKeyID: &keyID, Concurrency: 17,
	}}}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)

	_, result, err := svc.SyncKeys(context.Background(), configID)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, UpstreamSyncStatusPartial, result.Status)
	require.Contains(t, result.Warnings, "newapi concurrency value is invalid and was ignored")
	require.Len(t, repo.extraUpdates, 1)
	snapshot := requireConcurrencySnapshot(t, repo.extraUpdates[0].updates)
	require.Equal(t, upstreamConcurrencyStatusUnsupported, snapshot["status"])
	require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
	require.NotContains(t, snapshot, "raw_value")
	require.Equal(t, true, repo.extraUpdates[0].updates["upstream_provider_snapshot_partial"])
	require.Len(t, accountRepo.bulkUpdates, 1)
	require.Nil(t, accountRepo.bulkUpdates[0].updates.Concurrency)
}

func TestUpstreamConfigServiceSub2APIInvalidConcurrencyWarnsWithoutBlockingSync(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/auth/login":
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"jwt-upstream"}}`))
		case "/api/v1/keys":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":1,"key":"sk-upstream","group_id":10,"group":{"id":10,"platform":"openai","rate_multiplier":0.07}}],"page":1,"page_size":100,"pages":1}}`))
		case "/api/v1/groups/rates":
			_, _ = w.Write([]byte(`{"code":0,"data":{}}`))
		case "/api/v1/auth/me":
			_, _ = w.Write([]byte(`{"code":0,"data":{"id":7,"balance":10,"total_recharged":20,"concurrency":1.5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	repo := &upstreamConfigServiceRepo{configs: []UpstreamConfig{{
		ID: 83, Name: "Sub2API", Provider: UpstreamProviderSub2API, SiteURL: server.URL,
		AuthMode: UpstreamAuthModeUserLogin, Status: StatusActive, Sub2APINotInCNConfirmed: true,
		Credentials: map[string]any{
			AccountCredentialSub2APILoginEmail: "owner@example.com", AccountCredentialSub2APILoginPassword: "secret",
		},
	}}}
	svc := NewUpstreamConfigService(repo, nil, &sub2APIRateSyncAccountRepo{})

	_, result, err := svc.SyncKeys(context.Background(), 83)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Equal(t, UpstreamSyncStatusPartial, result.Status)
	require.Contains(t, result.Warnings, "sub2api concurrency value is invalid and was ignored")
	require.Len(t, repo.extraUpdates, 1)
	snapshot := requireConcurrencySnapshot(t, repo.extraUpdates[0].updates)
	require.Equal(t, upstreamConcurrencyStatusUnsupported, snapshot["status"])
	require.Equal(t, upstreamConcurrencySemanticsUnknown, snapshot["semantics"])
	require.NotContains(t, snapshot, "raw_value")
	require.NotContains(t, snapshot, "limit")
}

func requireConcurrencySnapshot(t *testing.T, updates map[string]any) map[string]any {
	t.Helper()
	snapshot, ok := updates[upstreamConcurrencySnapshotKey].(map[string]any)
	require.True(t, ok)
	require.Equal(t, 1, snapshot["version"])
	require.NotEmpty(t, snapshot["last_checked_at"])
	require.NotContains(t, snapshot, "synced_at")
	require.NotContains(t, snapshot, "stale_at")
	require.NotContains(t, snapshot, "last_known_status")
	return snapshot
}
