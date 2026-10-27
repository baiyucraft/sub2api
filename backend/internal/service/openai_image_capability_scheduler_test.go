package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestResponsesImageCapabilityRoutingIndependentOfCost(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			accounts := []Account{*imagePolicyAccount(false), *imagePolicyAccount(true), *imagePolicyAccount(true)}
			accounts[1].UpstreamImagePricing = nil
			for i := range accounts {
				accounts[i].ID = int64(i + 1)
				accounts[i].Status = StatusActive
				accounts[i].Schedulable = true
				accounts[i].Concurrency = 2
				accounts[i].Priority = i
			}
			cache := &schedulerTestGatewayCache{}
			svc := &OpenAIGatewayService{
				cfg: &config.Config{}, cache: cache,
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: accounts},
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
			}
			ctx := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`), true)
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "", "", "gpt-6.1-sol", nil, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, err)
			require.Equal(t, int64(3), selection.Account.ID)
			selection.ReleaseFunc()
			selection, _, err = svc.SelectAccountWithSchedulerForCapability(ctx, nil, "", "", "gpt-6.1-sol", map[int64]struct{}{3: {}}, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.NoError(t, err)
			require.Equal(t, int64(2), selection.Account.ID)
			selection.ReleaseFunc()
			_, _, err = svc.SelectAccountWithSchedulerForCapability(ctx, nil, "", "", "gpt-6.1-sol", map[int64]struct{}{2: {}, 3: {}}, OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityResponses, false, false, false)
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
		})
	}
}

func TestOpenAIImageSelectionTracePreservesOrdinaryFailures(t *testing.T) {
	svc := &OpenAIGatewayService{}
	ctx := WithOpenAIImageSelectionTrace(context.Background())
	require.Same(t, ctx, WithOpenAIImageSelectionTrace(ctx))
	require.False(t, OpenAIImageSelectionFiltered(ctx))
	require.False(t, OpenAIImageSelectionExhaustion(ctx, nil))
	ctx = WithOpenAIImageRequest(ctx, openAIResponsesEndpoint, "text-model", []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`), false)
	req := OpenAIAccountScheduleRequest{RequestedModel: "text-model"}
	unknown := imagePolicyAccount(true)
	unknown.UpstreamImagePricing = nil
	preference := context.WithValue(ctx, openAIImageKnownOnlyKey{}, true)
	require.Equal(t, "image_permission_unknown_fallback", svc.openAIImageCandidateFailure(preference, unknown, req))
	require.False(t, OpenAIImageSelectionFiltered(ctx))
	require.Empty(t, svc.openAIImageCandidateFailure(ctx, unknown, req))
	denied := imagePolicyAccount(false)
	denied.Status = StatusActive
	denied.Schedulable = true
	require.Equal(t, "image_permission_denied", svc.openAIImageCandidateFailure(ctx, denied, req))
	require.True(t, OpenAIImageSelectionFiltered(ctx))
	require.True(t, OpenAIImageSelectionExhaustion(ctx, nil))
	imageFailure := &UpstreamFailoverError{Scope: GatewayFailureScopeAccountCapability, Reason: OpenAIImagePermissionDeniedReason, StatusCode: 403}
	RecordOpenAIImageFailover(ctx, imageFailure)
	require.True(t, OpenAIImageSelectionExhaustion(ctx, imageFailure))
	ordinary := &UpstreamFailoverError{Scope: GatewayFailureScopeAccount, Reason: "network_error", StatusCode: 502}
	RecordOpenAIImageFailover(ctx, ordinary)
	require.False(t, OpenAIImageSelectionExhaustion(ctx, imageFailure))
	require.False(t, OpenAIImageSelectionExhaustion(ctx, nil))
	require.Equal(t, ordinary, OpenAIImageFailoverForExhaustion(ctx, imageFailure))
	latest := &UpstreamFailoverError{Scope: GatewayFailureScopeAccount, Reason: "upstream_503", StatusCode: 503}
	RecordOpenAIImageFailover(ctx, latest)
	RecordOpenAIImageFailover(ctx, imageFailure)
	require.Equal(t, latest, OpenAIImageFailoverForExhaustion(ctx, imageFailure), "ordinary failures retain the existing last-failure ordering")
	refresh := &UpstreamFailoverError{Scope: GatewayFailureScopeAccountCapability, Reason: "image_permission_refresh_unavailable", StatusCode: 503}
	require.False(t, IsOpenAIImagePermissionFailover(refresh), "failed metadata refresh is not proof of missing image capability")
	RecordOpenAIImageFailover(ctx, refresh)
	RecordOpenAIImageFailover(ctx, imageFailure)
	require.False(t, OpenAIImageSelectionExhaustion(ctx, imageFailure))
	require.Equal(t, refresh, OpenAIImageFailoverForExhaustion(ctx, imageFailure))
	require.Same(t, imageFailure, OpenAIImageFailoverForExhaustion(context.Background(), imageFailure))
	// A fresh request must not inherit a previous request's filtering evidence.
	require.False(t, OpenAIImageSelectionFiltered(WithOpenAIImageSelectionTrace(context.Background())))
	modelCtx := WithOpenAIImageSelectionTrace(context.Background())
	modelCtx = WithOpenAIImageRequest(modelCtx, openAIResponsesEndpoint, "missing-model", []byte(`{"tool_choice":{"type":"image_generation"}}`), false)
	denied.Type = AccountTypeAPIKey
	denied.Credentials = map[string]any{"model_mapping": map[string]any{"only-supported-model": "only-supported-model"}}
	require.Equal(t, "image_permission_denied", svc.openAIImageCandidateFailure(modelCtx, denied, OpenAIAccountScheduleRequest{RequestedModel: "missing-model"}))
	require.False(t, OpenAIImageSelectionFiltered(modelCtx), "an unrelated unsupported model must keep ordinary no-account semantics")
}

func TestOpenAIImageSelectionTraceResetBetweenSuccessfulTurns(t *testing.T) {
	ctx := WithOpenAIImageSelectionTrace(context.Background())
	child := context.WithValue(ctx, openAIImageKnownOnlyKey{}, false)
	trace := ctx.Value(openAIImageSelectionTraceKey{}).(*openAIImageSelectionTrace)
	trace.filtered.Store(true)
	ordinary := &UpstreamFailoverError{Scope: GatewayFailureScopeAccount, Reason: "network_error", StatusCode: 502}
	image := &UpstreamFailoverError{Scope: GatewayFailureScopeAccountCapability, Reason: OpenAIImagePermissionDeniedReason, StatusCode: 403}
	RecordOpenAIImageFailover(child, ordinary)
	require.Equal(t, ordinary, OpenAIImageFailoverForExhaustion(ctx, image), "attempts in one turn preserve ordinary failures")

	ResetOpenAIImageSelectionTrace(child)
	require.Same(t, ctx, WithOpenAIImageSelectionTrace(ctx), "reset preserves the trace shared by existing contexts")
	require.False(t, OpenAIImageSelectionFiltered(ctx), "a successful turn must clear its candidate-filtering evidence")
	require.False(t, OpenAIImageSelectionExhaustion(ctx, nil))
	require.Same(t, image, OpenAIImageFailoverForExhaustion(ctx, image), "the next turn must not inherit the previous network error")
	require.True(t, OpenAIImageSelectionExhaustion(child, image))

	ResetOpenAIImageSelectionTrace(nil)
	ResetOpenAIImageSelectionTrace(context.Background())
}

func TestOpenAIImageSelectionTraceIgnoresOrdinaryCandidateVetoes(t *testing.T) {
	tests := []struct {
		name      string
		configure func(context.Context, *OpenAIGatewayService, *Account, *OpenAIAccountScheduleRequest) context.Context
	}{
		{"inactive", func(ctx context.Context, _ *OpenAIGatewayService, a *Account, _ *OpenAIAccountScheduleRequest) context.Context {
			a.Status = StatusDisabled
			return ctx
		}},
		{"excluded", func(ctx context.Context, _ *OpenAIGatewayService, a *Account, req *OpenAIAccountScheduleRequest) context.Context {
			req.ExcludedIDs = map[int64]struct{}{a.ID: {}}
			return ctx
		}},
		{"privacy", func(ctx context.Context, _ *OpenAIGatewayService, _ *Account, req *OpenAIAccountScheduleRequest) context.Context {
			req.RequirePrivacySet = true
			return ctx
		}},
		{"capacity", func(ctx context.Context, _ *OpenAIGatewayService, a *Account, _ *OpenAIAccountScheduleRequest) context.Context {
			return WithOpenAICapacityExcludedTargets(ctx, map[string]struct{}{a.SchedulingConcurrencyTarget().Key(): {}})
		}},
		{"rpm", func(ctx context.Context, svc *OpenAIGatewayService, a *Account, _ *OpenAIAccountScheduleRequest) context.Context {
			a.RPMLimit = 1
			svc.SetRPMCache(&accountRPMGateTestCache{counts: map[int64]int{a.ID: 1}})
			return ctx
		}},
		{"proxy", func(ctx context.Context, svc *OpenAIGatewayService, a *Account, _ *OpenAIAccountScheduleRequest) context.Context {
			proxyID := int64(7)
			a.ProxyID = &proxyID
			svc.openaiProxyStreamCircuit = newOpenAIProxyStreamCircuit(openAIProxyStreamCircuitSettings{failureThreshold: 1, failureWindow: time.Minute, quarantineTTL: time.Minute, maxEntries: 16})
			svc.openaiProxyStreamCircuit.recordFailure(proxyID, time.Now())
			return ctx
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			a := imagePolicyAccount(false)
			a.ID, a.Status, a.Schedulable = 41, StatusActive, true
			ctx := WithOpenAIImageSelectionTrace(context.Background())
			ctx = WithOpenAIImageRequest(ctx, openAIResponsesEndpoint, "text-model", []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"required"}`), false)
			req := OpenAIAccountScheduleRequest{RequestedModel: "text-model"}
			ctx = tt.configure(ctx, svc, a, &req)
			require.Equal(t, "image_permission_denied", svc.openAIImageCandidateFailure(ctx, a, req))
			require.False(t, OpenAIImageSelectionExhaustion(ctx, nil), "ordinary no-account reasons must not become image permission errors")
		})
	}
}

func TestImageCandidateCooldownAndMappedModel(t *testing.T) {
	svc := &OpenAIGatewayService{}
	account := imagePolicyAccount(true)
	setAccountModelRateLimitSnapshot(account, openAIImageGenerationRateLimitKey, time.Now().Add(time.Minute), "fixture", time.Now())
	ordinary := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", []byte(`{"input":"hello"}`), true)
	req := OpenAIAccountScheduleRequest{RequestedModel: "gpt-6.1-sol"}
	require.Empty(t, svc.openAIImageCandidateFailure(ordinary, account, req))
	image := WithOpenAIImageRequest(ordinary, "/v1/responses", "gpt-6.1-sol", []byte(`{"tools":[{"type":"image_generation"}]}`), true)
	require.Equal(t, "image_capability_cooldown", svc.openAIImageCandidateFailure(image, account, req))
	account.Extra = nil
	account.Credentials = map[string]any{"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-image-1"}}
	account.UpstreamImagePricing.Supported = false
	require.Equal(t, "image_permission_denied", svc.openAIImageCandidateFailure(ordinary, account, req))
}
