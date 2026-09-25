package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomizedChannelUsageFields(t *testing.T) {
	ctx := WithChannelCustomizationModel(context.Background(), "A", "B")
	fields := CustomizedChannelUsageFields(ctx, ChannelUsageFields{ChannelMappedModel: "C"}, "D")
	require.Equal(t, "A", fields.OriginalModel)
	require.Equal(t, "A→B→C→D", fields.ModelMappingChain)
	require.Equal(t, BillingModelSourceRequested, fields.BillingModelSource)
	require.Equal(t, ChannelUsageFields{ChannelMappedModel: "C"}, CustomizedChannelUsageFields(context.Background(), ChannelUsageFields{ChannelMappedModel: "C"}, "D"))
}

func TestChannelCustomizationBillingSnapshotUsesOriginalGroup(t *testing.T) {
	original := &Group{ID: 11, Name: "original", RateMultiplier: 0.7}
	target := &Group{ID: 22, Name: "target", RateMultiplier: 2}
	key := &APIKey{ID: 7, GroupID: &target.ID, Group: target}
	subscription := &UserSubscription{ID: 31, GroupID: original.ID}
	ctx := WithChannelCustomizationBillingSnapshot(context.Background(), &APIKey{ID: key.ID, GroupID: &original.ID, Group: original}, subscription, true)

	billingKey := ChannelCustomizationBillingAPIKey(ctx, key)
	billingSubscription := ChannelCustomizationBillingSubscription(ctx, nil)
	require.Same(t, original, billingKey.Group)
	require.Equal(t, original.ID, *billingKey.GroupID)
	require.Same(t, subscription, billingSubscription)
	// The routing API key remains independently usable for forwarding.
	require.Same(t, target, key.Group)
}

func TestNormalizeGatewayCustomizationModelMappingValidation(t *testing.T) {
	base := GatewayChannelCustomizationRule{Name: "map", Enabled: true, Action: GatewayChannelCustomizationActionModelMapping, APIKeyNames: []string{"key"}, Models: []string{"A"}, TargetModel: "B"}
	for _, tc := range []struct {
		name   string
		change func(*GatewayChannelCustomizationRule)
		valid  bool
	}{
		{"valid", func(*GatewayChannelCustomizationRule) {}, true},
		{"no source", func(r *GatewayChannelCustomizationRule) { r.Models = nil }, false},
		{"no target", func(r *GatewayChannelCustomizationRule) { r.TargetModel = " " }, false},
		{"too long", func(r *GatewayChannelCustomizationRule) {
			r.TargetModel = string(make([]byte, gatewayChannelCustomizationMaxStringBytes+1))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := base
			tc.change(&rule)
			settings := GatewayChannelCustomizationSettings{Rules: []GatewayChannelCustomizationRule{rule}}
			err := NormalizeGatewayChannelCustomizationSettings(&settings)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
