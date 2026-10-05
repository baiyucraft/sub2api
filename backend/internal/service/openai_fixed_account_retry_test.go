package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestFixedOpenAIRetryDoesNotSelectAnotherAccount(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(map[bool]string{false: "available", true: "full"}[full], func(t *testing.T) {
			accounts := []Account{
				{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2},
				{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2},
			}
			var acquiredIDs, releasedIDs []int64
			svc := &OpenAIGatewayService{
				cfg: &config.Config{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts},
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{
					acquireResults: map[int64]bool{11: !full, 12: true}, acquiredIDs: &acquiredIDs, releasedIDs: &releasedIDs,
				}),
			}
			selection, decision, err := svc.SelectAccountWithSchedulerForCapability(
				WithOpenAIFixedRetryAccount(context.Background(), 11), nil, "", "", "gpt-6.1-sol", nil,
				OpenAIUpstreamTransportAny, OpenAIEndpointCapabilityChatCompletions, false, false, true,
			)
			if full {
				require.ErrorIs(t, err, ErrOpenAIFixedRetryUnavailable)
				require.Nil(t, selection)
			} else {
				require.NoError(t, err)
				require.Equal(t, int64(11), selection.Account.ID)
				require.Equal(t, "same_account_retry", decision.Layer)
				selection.ReleaseFunc()
				require.Equal(t, []int64{11}, releasedIDs)
			}
			require.Equal(t, []int64{11}, acquiredIDs)
		})
	}
}

func TestFixedOpenAIRetryRechecksModelAndExclusion(t *testing.T) {
	account := Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 2,
		Credentials: map[string]any{"model_mapping": map[string]any{"gpt-6.1-sol": "gpt-6.1-sol"}},
	}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{account}}}
	ctx := WithOpenAIFixedRetryAccount(context.Background(), 11)
	for _, req := range []struct {
		model    string
		excluded map[int64]struct{}
	}{
		{"unsupported", nil}, {"gpt-6.1-sol", map[int64]struct{}{11: {}}},
	} {
		selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, nil, "", "", req.model, req.excluded, OpenAIUpstreamTransportAny, "", false, false, true)
		require.ErrorIs(t, err, ErrOpenAIFixedRetryUnavailable)
		require.Nil(t, selection)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err := svc.SelectAccountWithSchedulerForCapability(canceled, nil, "", "", "gpt-6.1-sol", nil, OpenAIUpstreamTransportAny, "", false, false, true)
	require.ErrorIs(t, err, context.Canceled)
}
