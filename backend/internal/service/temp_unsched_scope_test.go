//go:build unit

package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type tempUnschedScopeRepoStub struct {
	modelNotFoundAccountRepoStub
	tempReasons []string
	errorCalls  int
}

func (r *tempUnschedScopeRepoStub) SetError(context.Context, int64, string) error {
	r.errorCalls++
	return nil
}

func (r *tempUnschedScopeRepoStub) SetTempUnschedulable(_ context.Context, _ int64, _ time.Time, reason string) error {
	r.tempCalls++
	r.tempReasons = append(r.tempReasons, reason)
	return nil
}

func newTempUnschedScopeAccount(scope string, extra map[string]any) *Account {
	rule := map[string]any{
		"error_code":       float64(http.StatusServiceUnavailable),
		"keywords":         []any{"unavailable"},
		"duration_minutes": float64(10),
	}
	if scope != "" {
		rule["scope"] = scope
	}
	for k, v := range extra {
		rule[k] = v
	}
	return &Account{
		ID:          202,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Credentials: map[string]any{
			"temp_unschedulable_enabled": true,
			"temp_unschedulable_rules":   []any{rule},
		},
	}
}

var tempUnschedScopeBody = []byte(`{"error":{"message":"upstream unavailable"}}`)

var (
	tempUnschedScope401Body         = []byte(`{"error":{"message":"unauthorized"}}`)
	tempUnschedScopeRevoked401Body  = []byte(`{"error":{"code":"token_revoked","message":"unauthorized"}}`)
	tempUnschedScopeDetail401Body   = []byte(`{"detail":"Unauthorized"}`)
	tempUnschedScopePrevious401Mark = `{"status_code":401,"until_unix":1735689600}`
)

func tempUnschedScope401Rule(scope string) map[string]any {
	rule := map[string]any{
		"error_code":       float64(http.StatusUnauthorized),
		"keywords":         []any{"unauthorized"},
		"duration_minutes": float64(10),
	}
	if scope != "" {
		rule["scope"] = scope
	}
	return rule
}

func newTempUnschedScope401Account(platform string, scopes ...string) *Account {
	account := newTempUnschedScopeAccount("", nil)
	account.Platform = platform
	rules := make([]any, 0, len(scopes))
	for _, scope := range scopes {
		rules = append(rules, tempUnschedScope401Rule(scope))
	}
	account.Credentials["temp_unschedulable_rules"] = rules
	return account
}

func TestGetTempUnschedulableRules_ParsesScope(t *testing.T) {
	mk := func(keyword string, scope any) map[string]any {
		rule := map[string]any{
			"error_code":       float64(http.StatusServiceUnavailable),
			"keywords":         []any{keyword},
			"duration_minutes": float64(1),
		}
		if scope != nil {
			rule["scope"] = scope
		}
		return rule
	}
	account := &Account{Credentials: map[string]any{
		"temp_unschedulable_rules": []any{
			mk("a", "account"),
			mk("b", " Model "),
			mk("c", "whatever"),
			mk("d", nil),
		},
	}}

	rules := account.GetTempUnschedulableRules()

	require.Len(t, rules, 4)
	require.Equal(t, TempUnschedScopeAccount, rules[0].Scope)
	require.Equal(t, TempUnschedScopeModel, rules[1].Scope, "大小写与首尾空白应被归一化")
	require.Empty(t, rules[2].Scope, "未知取值视为未设置")
	require.Empty(t, rules[3].Scope)
}

func TestTempUnschedRuleUsesModelScope(t *testing.T) {
	cases := []struct {
		name   string
		scope  string
		model  string
		status int
		want   bool
	}{
		{"缺省 模型已知 非401 走模型级", "", "gpt-5.4", http.StatusServiceUnavailable, true},
		{"缺省 401 走账号级", "", "gpt-5.4", http.StatusUnauthorized, false},
		{"缺省 模型未知 走账号级", "", "", http.StatusServiceUnavailable, false},
		{"显式账号级 模型已知 仍账号级", TempUnschedScopeAccount, "gpt-5.4", http.StatusServiceUnavailable, false},
		{"显式模型级 401 仍模型级", TempUnschedScopeModel, "gpt-5.4", http.StatusUnauthorized, true},
		{"显式模型级 模型未知 兜底账号级", TempUnschedScopeModel, "  ", http.StatusServiceUnavailable, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tempUnschedRuleUsesModelScope(TempUnschedulableRule{Scope: tc.scope}, tc.model, tc.status)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestHandleTempUnschedulable_ExplicitAccountScopePausesWholeAccountWithKnownModel(t *testing.T) {
	repo := &tempUnschedScopeRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newTempUnschedScopeAccount(TempUnschedScopeAccount, nil)

	handled := svc.HandleTempUnschedulable(context.Background(), account, http.StatusServiceUnavailable, tempUnschedScopeBody, "gpt-5.4")

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.tempReasons[0]), &state))
	require.Equal(t, TempUnschedScopeAccount, state.Scope)
	require.Empty(t, state.Model)
}

func TestCheckErrorPolicy_ExplicitModelScopeKeeps401ModelScoped(t *testing.T) {
	repo := &tempUnschedScopeRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newTempUnschedScopeAccount(TempUnschedScopeModel, map[string]any{
		"error_code": float64(http.StatusUnauthorized),
		"keywords":   []any{"unauthorized"},
	})

	result := svc.CheckErrorPolicy(context.Background(), account, http.StatusUnauthorized,
		[]byte(`{"error":{"message":"unauthorized"}}`), "gpt-5.4")

	require.Equal(t, ErrorPolicyTempUnscheduled, result)
	require.Zero(t, repo.tempCalls, "运营人员显式选了模型级，401 也不应连累整个账号")
	require.Len(t, repo.modelRateLimitCalls, 1)
	require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)

	var state TempUnschedState
	require.NoError(t, json.Unmarshal([]byte(repo.modelRateLimitCalls[0].reason), &state))
	require.Equal(t, TempUnschedScopeModel, state.Scope)
	require.Equal(t, "gpt-5.4", state.Model)
	require.Equal(t, http.StatusUnauthorized, state.StatusCode)
}

func TestHandleTempUnschedulable_ExplicitModelScopeFallsBackToAccountWhenModelUnknown(t *testing.T) {
	repo := &tempUnschedScopeRepoStub{}
	svc := &RateLimitService{accountRepo: repo}
	account := newTempUnschedScopeAccount(TempUnschedScopeModel, nil)

	handled := svc.HandleTempUnschedulable(context.Background(), account, http.StatusServiceUnavailable, tempUnschedScopeBody)

	require.True(t, handled)
	require.Equal(t, 1, repo.tempCalls)
	require.Empty(t, repo.modelRateLimitCalls)
}

func TestOpenAIFailoverSideEffects_401FollowsTempUnschedScope(t *testing.T) {
	cases := []struct {
		name         string
		scopes       []string
		body         []byte
		poolMode     bool
		oauth        bool
		wantModel    bool
		wantTemp     bool
		wantSetError bool
		wantBlocked  bool
	}{
		{name: "显式模型级 只冷却模型", scopes: []string{TempUnschedScopeModel}, wantModel: true},
		{name: "显式模型级 池模式 只冷却模型", scopes: []string{TempUnschedScopeModel}, poolMode: true, wantModel: true},
		{name: "缺省 走认证错误处理", scopes: []string{""}, wantSetError: true, wantBlocked: true},
		{name: "显式账号级 走认证错误处理", scopes: []string{TempUnschedScopeAccount}, wantSetError: true, wantBlocked: true},
		{name: "首条命中为账号级 走认证错误处理", scopes: []string{TempUnschedScopeAccount, TempUnschedScopeModel}, wantSetError: true, wantBlocked: true},
		{name: "缺省 池模式 保持跳过", scopes: []string{""}, poolMode: true},
		{name: "显式模型级 token_revoked 走认证错误处理", scopes: []string{TempUnschedScopeModel}, body: tempUnschedScopeRevoked401Body, wantSetError: true, wantBlocked: true},
		{name: "显式模型级 detail Unauthorized 走认证错误处理", scopes: []string{TempUnschedScopeModel}, body: tempUnschedScopeDetail401Body, wantSetError: true, wantBlocked: true},
		{name: "显式模型级 token_revoked 池模式 保持跳过", scopes: []string{TempUnschedScopeModel}, body: tempUnschedScopeRevoked401Body, poolMode: true},
		{name: "显式模型级 OAuth 走刷新窗口", scopes: []string{TempUnschedScopeModel}, oauth: true, wantTemp: true, wantBlocked: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &tempUnschedScopeRepoStub{}
			gateway := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo, cfg: &config.Config{}}}
			account := newTempUnschedScope401Account(PlatformOpenAI, tc.scopes...)
			if tc.poolMode {
				account.Credentials["pool_mode"] = true
			}
			if tc.oauth {
				account.Type = AccountTypeOAuth
				account.Credentials["refresh_token"] = "rt"
			}
			body := tc.body
			if body == nil {
				body = tempUnschedScope401Body
			}

			require.True(t, gateway.shouldFailoverOpenAIUpstreamResponse(account, http.StatusUnauthorized, "unauthorized", body))
			gateway.handleFailoverSideEffects(context.Background(), &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{},
			}, account, body, "gpt-5.4")

			require.Equal(t, tc.wantTemp, repo.tempCalls == 1, "SetTempUnschedulable=%d", repo.tempCalls)
			require.Equal(t, tc.wantSetError, repo.errorCalls == 1, "SetError=%d", repo.errorCalls)
			require.Equal(t, tc.wantBlocked, gateway.isOpenAIAccountRuntimeBlocked(account))
			if tc.wantModel {
				require.Len(t, repo.modelRateLimitCalls, 1)
				require.Equal(t, "gpt-5.4", repo.modelRateLimitCalls[0].scope)
			} else {
				require.Empty(t, repo.modelRateLimitCalls)
			}
		})
	}
}

func TestHandleUpstreamError_ExplicitModelScope401(t *testing.T) {
	t.Run("模型已知 只冷却模型", func(t *testing.T) {
		repo := &tempUnschedScopeRepoStub{}
		svc := &RateLimitService{accountRepo: repo}
		account := newTempUnschedScope401Account(PlatformAnthropic, TempUnschedScopeModel)

		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, tempUnschedScope401Body, "claude-sonnet-4-5")

		require.True(t, shouldDisable)
		require.Zero(t, repo.errorCalls)
		require.Zero(t, repo.tempCalls)
		require.Len(t, repo.modelRateLimitCalls, 1)
		require.Equal(t, "claude-sonnet-4-5", repo.modelRateLimitCalls[0].scope)
	})

	t.Run("模型未知 按账号级规则走认证错误处理", func(t *testing.T) {
		repo := &tempUnschedScopeRepoStub{}
		svc := &RateLimitService{accountRepo: repo}
		account := newTempUnschedScope401Account(PlatformAnthropic, TempUnschedScopeModel)

		shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, tempUnschedScope401Body)

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.errorCalls)
		require.Empty(t, repo.modelRateLimitCalls)
	})
}

func TestCheckErrorPolicy_401FollowsTempUnschedScope(t *testing.T) {
	cases := []struct {
		name        string
		scope       string
		body        []byte
		oauth       bool
		previous401 bool
		want        ErrorPolicyResult
		wantModel   bool
		wantTemp    bool
	}{
		{name: "显式模型级 不参与重复 401 升级", scope: TempUnschedScopeModel, previous401: true, want: ErrorPolicyTempUnscheduled, wantModel: true},
		{name: "缺省 第二次 401 升级", previous401: true, want: ErrorPolicyNone},
		{name: "显式模型级 token_revoked 按账号暂停", scope: TempUnschedScopeModel, body: tempUnschedScopeRevoked401Body, want: ErrorPolicyTempUnscheduled, wantTemp: true},
		{name: "显式模型级 detail Unauthorized 按账号暂停", scope: TempUnschedScopeModel, body: tempUnschedScopeDetail401Body, want: ErrorPolicyTempUnscheduled, wantTemp: true},
		{name: "显式模型级 OAuth 按账号暂停", scope: TempUnschedScopeModel, oauth: true, want: ErrorPolicyTempUnscheduled, wantTemp: true},
		{name: "显式模型级 凭据级第二次 401 升级", scope: TempUnschedScopeModel, body: tempUnschedScopeDetail401Body, previous401: true, want: ErrorPolicyNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &tempUnschedScopeRepoStub{}
			svc := &RateLimitService{accountRepo: repo}
			account := newTempUnschedScope401Account(PlatformOpenAI, tc.scope)
			if tc.oauth {
				account.Type = AccountTypeOAuth
			}
			if tc.previous401 {
				account.TempUnschedulableReason = tempUnschedScopePrevious401Mark
			}
			body := tc.body
			if body == nil {
				body = tempUnschedScope401Body
			}

			result := svc.CheckErrorPolicy(context.Background(), account, http.StatusUnauthorized, body, "gpt-5.4")

			require.Equal(t, tc.want, result)
			require.Equal(t, tc.wantTemp, repo.tempCalls == 1, "SetTempUnschedulable=%d", repo.tempCalls)
			require.Equal(t, tc.wantModel, len(repo.modelRateLimitCalls) == 1)
		})
	}
}
