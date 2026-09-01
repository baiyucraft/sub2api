//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type subscriptionBillingAPIKeyServiceStub struct{}

func (subscriptionBillingAPIKeyServiceStub) UpdateQuotaUsed(context.Context, int64, float64) error {
	return nil
}

func (subscriptionBillingAPIKeyServiceStub) UpdateRateLimitUsage(context.Context, int64, float64) error {
	return nil
}

// TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier locks in the fix
// that subscription-mode billing honours the group (and any user-specific) rate
// multiplier — i.e. cmd.SubscriptionCost tracks ActualCost (= TotalCost *
// RateMultiplier), not raw TotalCost.
func TestBuildUsageBillingCommand_SubscriptionAppliesRateMultiplier(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	subID := int64(42)

	tests := []struct {
		name           string
		totalCost      float64
		actualCost     float64
		isSubscription bool
		wantSub        float64
		wantBalance    float64
	}{
		{
			name:           "subscription with 2x multiplier consumes 2x quota",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: true,
			wantSub:        2.0,
			wantBalance:    0,
		},
		{
			name:           "subscription with 0.5x multiplier consumes 0.5x quota",
			totalCost:      1.0,
			actualCost:     0.5,
			isSubscription: true,
			wantSub:        0.5,
			wantBalance:    0,
		},
		{
			name:           "free subscription (multiplier 0) consumes no quota",
			totalCost:      1.0,
			actualCost:     0,
			isSubscription: true,
			wantSub:        0,
			wantBalance:    0,
		},
		{
			name:           "balance billing keeps using ActualCost (regression)",
			totalCost:      1.0,
			actualCost:     2.0,
			isSubscription: false,
			wantSub:        0,
			wantBalance:    2.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &postUsageBillingParams{
				Cost:               &CostBreakdown{TotalCost: tt.totalCost, ActualCost: tt.actualCost},
				User:               &User{ID: 1},
				APIKey:             &APIKey{ID: 2, GroupID: &groupID},
				Account:            &Account{ID: 3},
				Subscription:       &UserSubscription{ID: subID},
				IsSubscriptionBill: tt.isSubscription,
			}

			cmd := buildUsageBillingCommand("req-1", nil, p)
			if cmd == nil {
				t.Fatal("buildUsageBillingCommand returned nil")
			}
			if cmd.SubscriptionCost != tt.wantSub {
				t.Errorf("SubscriptionCost = %v, want %v", cmd.SubscriptionCost, tt.wantSub)
			}
			if cmd.BalanceCost != tt.wantBalance {
				t.Errorf("BalanceCost = %v, want %v", cmd.BalanceCost, tt.wantBalance)
			}
		})
	}
}

func TestBuildUsageBillingCommand_ZeroUserChargeStillMetersGroupPrice(t *testing.T) {
	t.Parallel()

	groupID := int64(7)
	apiKeyService := subscriptionBillingAPIKeyServiceStub{}
	p := &postUsageBillingParams{
		Cost: &CostBreakdown{
			TotalCost:         1.25,
			ActualCost:        0,
			QuotaMeterCost:    1.25,
			QuotaMeterCostSet: true,
		},
		User:          &User{ID: 1},
		APIKey:        &APIKey{ID: 2, GroupID: &groupID, Quota: 100, RateLimit5h: 100},
		Account:       &Account{ID: 3},
		APIKeyService: apiKeyService,
	}

	cmd := buildUsageBillingCommand("req-zero", nil, p)
	require.NotNil(t, cmd)
	require.Zero(t, cmd.BalanceCost)
	require.Zero(t, cmd.SubscriptionCost)
	require.Equal(t, 1.25, cmd.APIKeyQuotaCost)
	require.Equal(t, 1.25, cmd.APIKeyRateLimitCost)
}

func TestPostUsageBillingParams_ExplicitZeroQuotaMeterDoesNotFallbackToActualCost(t *testing.T) {
	t.Parallel()

	p := &postUsageBillingParams{Cost: &CostBreakdown{
		ActualCost:        9,
		QuotaMeterCost:    0,
		QuotaMeterCostSet: true,
	}}
	require.Zero(t, p.quotaMeterCost())
}
