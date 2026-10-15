//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

func TestValidateOrderInputRejectsUnknownOrderType(t *testing.T) {
	svc := &PaymentService{}
	for _, orderType := range []string{"x", "Balance", " balance", "subscription "} {
		for _, disabled := range []bool{false, true} {
			cfg := &PaymentConfig{BalanceDisabled: disabled}
			plan, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: orderType, Amount: 10}, cfg)
			require.Nil(t, plan)
			require.Error(t, err, "order type %q must be rejected", orderType)
			require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err), "order type %q", orderType)
		}
	}
}

func TestValidateOrderInputKeepsBalanceDisabledSwitch(t *testing.T) {
	svc := &PaymentService{}
	_, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeBalance, Amount: 10}, &PaymentConfig{BalanceDisabled: true})
	require.Equal(t, "BALANCE_PAYMENT_DISABLED", infraerrors.Reason(err))

	plan, err := svc.validateOrderInput(context.Background(), CreateOrderRequest{OrderType: payment.OrderTypeBalance, Amount: 10}, &PaymentConfig{})
	require.NoError(t, err)
	require.Nil(t, plan)
}

func TestFulfillmentRejectsUnknownOrderType(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).
		SetOrderType("x").
		ClearPlanID().
		ClearSubscriptionGroupID().
		ClearSubscriptionDays().
		Save(ctx)
	require.NoError(t, err)

	userRepo := &mockUserRepo{getByIDUser: &User{ID: order.UserID}}
	userRepo.updateBalanceFn = func(context.Context, int64, float64) error {
		t.Fatal("unknown order type must not credit balance")
		return nil
	}
	svc := &PaymentService{entClient: client, userRepo: userRepo}

	err = svc.executeFulfillment(ctx, order.ID)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err))
	err = svc.ExecuteBalanceFulfillment(ctx, order.ID)
	require.Equal(t, "INVALID_ORDER_TYPE", infraerrors.Reason(err))

	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusPaid, reloaded.Status)
}
