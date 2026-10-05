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
			ctx := WithOpenAIImageRequest(context.Background(), "/v1/responses", "gpt-6.1-sol", []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"none"}`), true)
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
