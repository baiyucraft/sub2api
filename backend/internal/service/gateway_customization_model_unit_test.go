//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGatewayRecordUsageCustomizedModelBillsOriginalAndAuditsForwarded(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
	groupID := int64(912)
	svc.channelService = newTestChannelServiceForStats(t, &Channel{ID: 1, Status: StatusActive}, groupID, PlatformDeepseek)
	svc.resolver = NewModelPricingResolver(svc.channelService, svc.billingService)
	aInput, aOutput := 2e-6, 4e-6
	cInput, cOutput := 1e-6, 1e-6
	group := &Group{ID: groupID, Platform: PlatformDeepseek, RateMultiplier: 0.5, ModelPricing: []ChannelModelPricing{
		{Models: []string{"A"}, BillingMode: BillingModeToken, InputPrice: &aInput, OutputPrice: &aOutput},
		{Models: []string{"C"}, BillingMode: BillingModeToken, InputPrice: &cInput, OutputPrice: &cOutput},
	}}
	err := svc.RecordUsage(WithChannelCustomizationModel(context.Background(), "A", "B"), &RecordUsageInput{
		Result: &ForwardResult{RequestID: "customized-model", Model: "C", UpstreamModel: "C", Usage: ClaudeUsage{InputTokens: 100, OutputTokens: 50}},
		APIKey: &APIKey{ID: 3, GroupID: &groupID, Group: group}, User: &User{ID: 4}, Account: &Account{ID: 5, Platform: PlatformDeepseek},
		ChannelUsageFields: ChannelUsageFields{OriginalModel: "B", ChannelMappedModel: "C", BillingModelSource: BillingModelSourceChannelMapped},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, "A", usageRepo.lastLog.RequestedModel)
	require.Equal(t, "A→B→C", *usageRepo.lastLog.ModelMappingChain)
	require.InDelta(t, (100*aInput+50*aOutput)*0.5, usageRepo.lastLog.ActualCost, 1e-12)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NotNil(t, usageRepo.lastLog.UpstreamModel)
	require.Equal(t, "C", *usageRepo.lastLog.UpstreamModel)
}
