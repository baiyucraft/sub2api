//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const openAIImagePermissionTestBody = `{"error":{"type":"permission_error","message":"Image generation is not enabled for this group"}}`

func TestOpenAIImagePermissionDeniedClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"fixed_403", 403, openAIImagePermissionTestBody, true},
		{"fixed_403_prefix", 403, `{"error":{"message":"403: Image generation is not enabled for this group"}}`, true},
		{"prefix_without_space", 403, `{"error":{"message":"403:Image generation is not enabled for this group"}}`, false},
		{"wrong_prefix", 403, `{"error":{"message":"502: Image generation is not enabled for this group"}}`, false},
		{"duplicate_prefix", 403, `{"error":{"message":"403: 403: Image generation is not enabled for this group"}}`, false},
		{"prefix_with_suffix", 403, `{"error":{"message":"403: Image generation is not enabled for this group; retry"}}`, false},
		{"prefix_in_other_field", 403, `{"message":"403: Image generation is not enabled for this group"}`, false},
		{"prefix_in_nested_field", 403, `{"response":{"error":{"message":"403: Image generation is not enabled for this group"}}}`, false},
		{"response_error", 403, `{"response":{"error":{"message":"Image generation is not enabled for this group"}}}`, true},
		{"detail_message", 403, `{"detail":{"message":"Image generation is not enabled for this group"}}`, true},
		{"scalar_detail", 403, `{"detail":"Image generation is not enabled for this group"}`, true},
		{"message", 403, `{"message":"Image generation is not enabled for this group"}`, true},
		{"bad_gateway_same_message", 502, openAIImagePermissionTestBody, false},
		{"bad_gateway_prefixed_message", 502, `{"error":{"message":"403: Image generation is not enabled for this group"}}`, false},
		{"bad_gateway", 502, `{"error":{"message":"Upstream request failed"}}`, false},
		{"wrong_status", 400, openAIImagePermissionTestBody, false},
		{"generic_403", 403, `{"error":{"message":"Access forbidden"}}`, false},
		{"different_permission", 403, `{"error":{"message":"You do not have permission to generate images"}}`, false},
		{"embedded_message", 403, `{"error":{"message":"Invalid input: Image generation is not enabled for this group"}}`, false},
		{"echoed_prompt", 403, `{"error":{"message":"Invalid input"},"input":{"message":"Image generation is not enabled for this group"}}`, false},
		{"html", 403, `<html>Image generation is not enabled for this group</html>`, false},
		{"plain_text", 403, `Image generation is not enabled for this group`, false},
		{"invalid_json", 403, `{"error":{"message":"Image generation is not enabled for this group"}`, false},
		{"account_deactivated", 403, `{"error":{"code":"account_deactivated","message":"Image generation is not enabled for this group"}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, IsOpenAIImagePermissionDenied(tt.status, []byte(tt.body)))
		})
	}
}

func TestOpenAIImagePermissionDeniedFailoverDisallowsPooledSameAccountRetry(t *testing.T) {
	account := &Account{ID: 712, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"pool_mode": true}}
	require.True(t, account.IsPoolModeRetryableStatus(http.StatusForbidden))
	svc := &OpenAIGatewayService{}
	headers := http.Header{"X-Request-Id": []string{"fixture_request"}}
	body := []byte(openAIImagePermissionTestBody)
	failoverErr := svc.newOpenAIAccountFailoverError(account, http.StatusForbidden, headers, body, "", false, true)
	require.True(t, failoverErr.IsOpenAIImagePermissionDenied())
	require.Equal(t, GatewayFailureScopeAccountCapability, failoverErr.Scope)
	require.Equal(t, OpenAIImagePermissionDeniedReason, failoverErr.Reason)
	require.Equal(t, GatewayFailureStageInference, failoverErr.Stage)
	require.False(t, failoverErr.IsCredentialFailure())
	require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.False(t, failoverErr.RequestScopedTransient)
	require.True(t, failoverErr.ShouldRetryNextAccount())
	require.Equal(t, NextAccountRetry, failoverErr.NextAccountAction)
	require.Equal(t, http.StatusForbidden, failoverErr.StatusCode)
	require.Equal(t, http.StatusServiceUnavailable, failoverErr.ClientStatusCode)
	require.Equal(t, body, failoverErr.ResponseBody)
	headers.Set("X-Request-Id", "changed")
	require.Equal(t, "fixture_request", failoverErr.ResponseHeaders.Get("X-Request-Id"))
	_, _, healthPenalty := classifyOpenAIAPIKeyHealthFailure(failoverErr)
	require.False(t, healthPenalty)
}

func TestOpenAIImagePermissionDeniedDoesNotReclassifyOrdinaryFailover(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{
		{http.StatusForbidden, `{"error":{"message":"Access forbidden"}}`},
		{http.StatusBadGateway, openAIImagePermissionTestBody},
		{http.StatusBadGateway, `{"error":{"message":"Upstream request failed"}}`},
	} {
		err := newOpenAIUpstreamFailoverError(tt.status, nil, []byte(tt.body), openAIImagePermissionDeniedMessage, true)
		require.False(t, err.IsOpenAIImagePermissionDenied())
		require.True(t, err.RetryableOnSameAccount)
		require.Empty(t, err.Scope)
		require.Empty(t, err.Reason)
	}
	var nilError *UpstreamFailoverError
	require.False(t, nilError.IsOpenAIImagePermissionDenied())
}

func TestOpenAIImagePermissionDeniedStreamUsesSemantic403(t *testing.T) {
	for _, body := range []string{
		`{"type":"error","error":{"message":"Image generation is not enabled for this group"}}`,
		`{"type":"error","error":{"message":"403: Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"message":"Image generation is not enabled for this group"}}}`,
		`{"type":"response.failed","response":{"status":"failed","error":{"type":"invalid_request_error","code":"upstream_error","message":"Image generation is not enabled for this group"}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			payload := []byte(body)
			message := extractOpenAISSEErrorMessage(payload)
			require.False(t, IsOpenAIImagePermissionDenied(http.StatusOK, payload), "a transport 200 alone is not a permission denial")
			require.Equal(t, http.StatusForbidden, openAIStreamFailureStatus(payload, message))
			require.True(t, openAIStreamFailedEventShouldFailover(payload, message))
			require.True(t, openAIStreamErrorEventShouldFailover(payload, message))
			require.False(t, openAIStreamCredentialAuthFailure(payload))
			repo := &imagePermissionAccountRepo{}
			rateLimiter := &RateLimitService{accountRepo: repo}
			gateway := &OpenAIGatewayService{rateLimitService: rateLimiter}
			account := &Account{ID: 715, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{"pool_mode": true}}
			require.False(t, openAIStreamFailedEventRetryableOnSameAccount(account, payload, message))
			failover := gateway.newOpenAIStreamFailoverErrorWithModel(nil, account, false, "fixture_stream", payload, message, "gpt-6.1-sol")
			require.Equal(t, http.StatusForbidden, failover.StatusCode)
			require.True(t, IsOpenAIImagePermissionFailover(failover))
			require.False(t, failover.ShouldReportAccountScheduleFailure())
			require.False(t, failover.RetryableOnSameAccount)
			require.Equal(t, payload, failover.ResponseBody)
			require.Equal(t, http.StatusServiceUnavailable, failover.ClientStatusCode)
			require.Len(t, repo.modelCalls, 1)
			require.Equal(t, "openai:image_generation", repo.modelCalls[0].scope)
			require.Equal(t, string(OpenAIImagePermissionDeniedReason), repo.modelCalls[0].reason)
			require.WithinDuration(t, time.Now().Add(30*time.Minute), repo.modelCalls[0].resetAt, time.Second)
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.True(t, account.IsSchedulable())
		})
	}
	for _, body := range []string{
		`{"type":"error","error":{"status_code":502,"message":"Image generation is not enabled for this group"}}`,
		`{"type":"response.failed","response":{"error":{"status":502,"message":"Image generation is not enabled for this group"}}}`,
		`{"type":"error","error":{"message":"Upstream request failed"},"input":{"message":"Image generation is not enabled for this group"}}`,
	} {
		payload := []byte(body)
		message := extractOpenAISSEErrorMessage(payload)
		status := openAIStreamFailureStatus(payload, message)
		require.Equal(t, http.StatusBadGateway, status)
		failover := newOpenAIUpstreamFailoverError(status, nil, payload, message, true)
		require.False(t, IsOpenAIImagePermissionFailover(failover))
		require.True(t, failover.RetryableOnSameAccount)
		require.True(t, failover.ShouldReportAccountScheduleFailure())
	}
	payload := []byte(`{"type":"error","error":{"type":"permission_error","message":"Access denied by request policy"}}`)
	require.False(t, openAIStreamErrorEventShouldFailover(payload, extractOpenAISSEErrorMessage(payload)))
}

func TestOpenAIImagePermissionFailoverIncludesLocalAdmission(t *testing.T) {
	require.False(t, IsOpenAIImagePermissionFailover(nil))
	require.True(t, IsOpenAIImagePermissionFailover(newOpenAIUpstreamFailoverError(403, nil, []byte(openAIImagePermissionTestBody), "", true)))
	for _, scope := range []GatewayFailureScope{GatewayFailureScopeAccount, GatewayFailureScopeAccountCapability} {
		for _, reason := range []GatewayFailureReason{"image_permission_denied", "image_capability_cooldown", "image_responses_unsupported", "image_permission_refresh_unavailable"} {
			e := &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, Scope: scope, Reason: reason}
			require.True(t, IsOpenAIImagePermissionFailover(e))
			require.False(t, e.ShouldReportAccountScheduleFailure())
			_, _, healthPenalty := classifyOpenAIAPIKeyHealthFailure(e)
			require.False(t, healthPenalty, "post-output health reporting also excludes local image admission")
		}
	}
	for _, e := range []*UpstreamFailoverError{
		{StatusCode: 502},
		{StatusCode: 403, Scope: GatewayFailureScopeAccount},
		{Scope: GatewayFailureScopeAccountCapability, Reason: "image_permission_denied_other"},
		{Scope: GatewayFailureScopeRequest, Reason: "image_permission_denied"},
		{Scope: GatewayFailureScopeProvider, Reason: "image_capability_cooldown"},
		{Scope: GatewayFailureScopeAccount, Reason: OpenAIUpstreamAccessStateReason},
	} {
		require.False(t, IsOpenAIImagePermissionFailover(e))
	}
	require.True(t, (&UpstreamFailoverError{StatusCode: 502}).ShouldReportAccountScheduleFailure())
	_, _, healthPenalty := classifyOpenAIAPIKeyHealthFailure(&UpstreamFailoverError{StatusCode: http.StatusBadGateway})
	require.True(t, healthPenalty)
}

type imagePermissionAccountRepo struct {
	rateLimitAccountRepoStub
	modelCalls []modelNotFoundRateLimitCall
	modelErr   error
}

func (r *imagePermissionAccountRepo) SetModelRateLimit(_ context.Context, id int64, scope string, resetAt time.Time, reason ...string) error {
	call := modelNotFoundRateLimitCall{accountID: id, scope: scope, resetAt: resetAt}
	if len(reason) > 0 {
		call.reason = reason[0]
	}
	r.modelCalls = append(r.modelCalls, call)
	return r.modelErr
}

func TestOpenAIImagePermissionDeniedCoolsOnlyImageCapability(t *testing.T) {
	for _, mode := range []string{"oauth", "apikey", "pool", "custom_codes", "temp_rule", "write_failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := &imagePermissionAccountRepo{}
			counter := &openAI403CounterCacheStub{counts: []int64{3}}
			blocker := &runtimeBlockRecorder{}
			rateLimiter := &RateLimitService{accountRepo: repo, openAI403CounterCache: counter, runtimeBlocker: blocker}
			gateway := &OpenAIGatewayService{rateLimitService: rateLimiter}
			account := &Account{ID: 713, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Credentials: map[string]any{}}
			switch mode {
			case "oauth":
				account.Type = AccountTypeOAuth
			case "pool":
				account.Credentials["pool_mode"] = true
			case "custom_codes":
				account.Credentials["custom_error_codes_enabled"] = true
				account.Credentials["custom_error_codes"] = []any{float64(http.StatusForbidden)}
			case "temp_rule":
				account.Credentials["temp_unschedulable_enabled"] = true
				account.Credentials["temp_unschedulable_rules"] = []any{map[string]any{
					"error_code": float64(http.StatusForbidden), "keywords": []any{"Image generation"}, "duration_minutes": float64(10),
				}}
			case "write_failure":
				repo.modelErr = errors.New("fixture write failure")
			}
			body := []byte(openAIImagePermissionTestBody)
			ctx := context.Background()
			require.Equal(t, ErrorPolicyNone, rateLimiter.CheckErrorPolicy(ctx, account, http.StatusForbidden, body, "gpt-6.1-sol"))
			require.False(t, rateLimiter.HandleTempUnschedulable(ctx, account, http.StatusForbidden, body, "gpt-6.1-sol"))
			require.False(t, shouldReportUpstreamHealthFailure(account, http.StatusForbidden, body))
			before := time.Now()
			for i := 0; i < 3; i++ {
				require.False(t, gateway.handleOpenAIAccountUpstreamError(ctx, account, http.StatusForbidden, nil, body, "gpt-6.1-sol"))
			}
			require.Len(t, repo.modelCalls, 3)
			for _, call := range repo.modelCalls {
				require.Equal(t, account.ID, call.accountID)
				require.Equal(t, "openai:image_generation", call.scope)
				require.Equal(t, string(OpenAIImagePermissionDeniedReason), call.reason)
				require.WithinDuration(t, before.Add(30*time.Minute), call.resetAt, time.Second)
			}
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
			require.Zero(t, repo.rateLimitedCalls)
			require.Zero(t, repo.updateCredentialsCalls)
			require.Zero(t, repo.updateExtraCalls)
			require.Equal(t, []int64{3}, counter.counts)
			require.Empty(t, blocker.accounts)
			require.False(t, gateway.isOpenAIAccountRuntimeBlocked(account))
			require.True(t, account.IsSchedulable())
			err := gateway.newOpenAIAccountFailoverError(account, 403, nil, body, "", false, true)
			require.True(t, err.IsOpenAIImagePermissionDenied())
			require.False(t, err.RetryableOnSameAccount)
			require.True(t, err.ShouldRetryNextAccount())
			// Materialize the persisted scope to check existing request admission.
			call := repo.modelCalls[0]
			setAccountModelRateLimitSnapshot(account, call.scope, call.resetAt, call.reason, before)
			require.True(t, account.isModelRateLimitedWithContext(ctx, "gpt-image-2"))
			require.True(t, account.isModelRateLimitedWithContext(WithOpenAIImageGenerationIntent(ctx), "gpt-6.1-sol"))
			require.False(t, account.isModelRateLimitedWithContext(ctx, "gpt-6.1-sol"))
			require.True(t, account.IsSchedulable())
		})
	}
}

func TestHandleOpenAIImagePermissionDeniedOnlyHandlesFixedOpenAI403(t *testing.T) {
	tests := []struct {
		name     string
		account  *Account
		status   int
		body     string
		writeErr error
		want     bool
	}{
		{"openai_403", &Account{ID: 714, Platform: PlatformOpenAI}, 403, openAIImagePermissionTestBody, nil, true},
		{"write_failure", &Account{ID: 714, Platform: PlatformOpenAI}, 403, openAIImagePermissionTestBody, errors.New("write failure"), true},
		{"nil_account", nil, 403, openAIImagePermissionTestBody, nil, false},
		{"other_platform", &Account{Platform: PlatformAnthropic}, 403, openAIImagePermissionTestBody, nil, false},
		{"bad_gateway", &Account{Platform: PlatformOpenAI}, 502, openAIImagePermissionTestBody, nil, false},
		{"generic_403", &Account{Platform: PlatformOpenAI}, 403, `{"error":{"message":"Access forbidden"}}`, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &imagePermissionAccountRepo{modelErr: tt.writeErr}
			svc := &RateLimitService{accountRepo: repo}
			require.Equal(t, tt.want, svc.HandleOpenAIImagePermissionDenied(context.Background(), tt.account, tt.status, []byte(tt.body)))
			if tt.want {
				require.Len(t, repo.modelCalls, 1)
			} else {
				require.Empty(t, repo.modelCalls)
			}
			require.Zero(t, repo.setErrorCalls)
			require.Zero(t, repo.tempCalls)
		})
	}
	var nilService *RateLimitService
	require.False(t, nilService.HandleOpenAIImagePermissionDenied(context.Background(), &Account{Platform: PlatformOpenAI}, 403, []byte(openAIImagePermissionTestBody)))
	require.False(t, (&RateLimitService{}).HandleOpenAIImagePermissionDenied(context.Background(), &Account{Platform: PlatformOpenAI}, 403, []byte(openAIImagePermissionTestBody)))
}
