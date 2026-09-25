//go:build unit

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDeferredWebSocketBillingChecksFirstTurnFinalGroup(t *testing.T) {
	gin.SetMode(gin.TestMode)
	standard := &service.Group{ID: 1, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeStandard}
	subscribed := &service.Group{ID: 2, Platform: service.PlatformOpenAI, Status: service.StatusActive, Hydrated: true, SubscriptionType: service.SubscriptionTypeSubscription}
	user := &service.User{ID: 13, Role: service.RoleUser, Status: service.StatusActive, Balance: 0}
	cfg := &config.Config{RunMode: config.RunModeStandard}
	subscription := &service.UserSubscription{
		ID: 201, UserID: user.ID, GroupID: subscribed.ID, Status: service.SubscriptionStatusActive,
		ExpiresAt:          time.Now().Add(time.Hour),
		DailyWindowStart:   ptrTime(time.Now()),
		WeeklyWindowStart:  ptrTime(time.Now()),
		MonthlyWindowStart: ptrTime(time.Now()),
	}
	subscriptionRepo := &stubUserSubscriptionRepo{getActive: func(context.Context, int64, int64) (*service.UserSubscription, error) {
		clone := *subscription
		return &clone, nil
	}}
	subscriptionService := service.NewSubscriptionService(nil, subscriptionRepo, nil, nil, cfg)
	t.Cleanup(subscriptionService.Stop)

	for _, tc := range []struct {
		name     string
		initial  *service.Group
		final    *service.Group
		wantCode string
	}{
		{"subscription to unpaid balance", subscribed, standard, "INSUFFICIENT_BALANCE"},
		{"unpaid balance to valid subscription", standard, subscribed, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := &service.APIKey{ID: 103, UserID: user.ID, Status: service.StatusActive, User: user, GroupID: &tc.initial.ID, Group: tc.initial}
			router := gin.New()
			router.Use(DeferAPIKeyGroupBilling())
			router.Use(func(c *gin.Context) { c.Set(string(ContextKeyAPIKey), key); c.Next() })
			router.Use(FinalAPIKeyGroupBilling(subscriptionService, cfg))
			router.GET("/v1/responses", func(c *gin.Context) {
				require.True(t, NeedsDeferredWebSocketGroupBilling(c))
				clone := *key
				clone.GroupID, clone.Group = &tc.final.ID, tc.final
				c.Set(string(ContextKeyAPIKey), &clone)
				failure := ValidateFinalAPIKeyGroupBilling(c, subscriptionService, cfg)
				if tc.wantCode == "" {
					require.Nil(t, failure)
					selected, ok := GetSubscriptionFromContext(c)
					require.True(t, ok)
					require.Equal(t, subscribed.ID, selected.GroupID)
				} else {
					require.NotNil(t, failure)
					require.Equal(t, tc.wantCode, failure.Code)
				}
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/responses", nil))
			require.Equal(t, http.StatusNoContent, response.Code)
		})
	}
}
