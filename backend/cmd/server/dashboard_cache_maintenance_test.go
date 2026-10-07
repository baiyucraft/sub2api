package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type dashboardMaintenanceCache struct {
	deleteError error
	getError    error
	value       string
	deleted     bool
}

func TestClearDashboardSnapshotOnlyDeletesTheConfiguredKey(t *testing.T) {
	for _, prefix := range []string{"prod", "prod:", "", "  prod  "} {
		t.Run(prefix, func(t *testing.T) {
			r := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: r.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			cfg := &config.Config{Dashboard: config.DashboardCacheConfig{KeyPrefix: prefix}}
			cache := repository.NewDashboardCache(client, cfg)
			ctx := context.Background()
			require.NoError(t, cache.SetDashboardStats(ctx, "stale", time.Hour))
			r.Set("billing:inflight", "preserve")
			r.Set("other:dashboard:stats:v1", "preserve")
			require.NoError(t, clearDashboardSnapshot(ctx, cache))
			require.NoError(t, clearDashboardSnapshot(ctx, cache))
			_, err := cache.GetDashboardStats(ctx)
			require.ErrorIs(t, err, service.ErrDashboardStatsCacheMiss)
			require.True(t, r.Exists("billing:inflight"))
			require.True(t, r.Exists("other:dashboard:stats:v1"))
		})
	}
}

func (c *dashboardMaintenanceCache) DeleteDashboardStats(context.Context) error {
	if c.deleteError == nil {
		c.deleted = true
	}
	return c.deleteError
}
func (c *dashboardMaintenanceCache) GetDashboardStats(context.Context) (string, error) {
	return c.value, c.getError
}
func (c *dashboardMaintenanceCache) SetDashboardStats(context.Context, string, time.Duration) error {
	panic("maintenance must not populate cache")
}

func TestClearDashboardSnapshotRequiresVerifiedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cache   dashboardMaintenanceCache
		success bool
	}{
		{"absent", dashboardMaintenanceCache{getError: service.ErrDashboardStatsCacheMiss}, true},
		{"delete failed", dashboardMaintenanceCache{deleteError: errors.New("private")}, false},
		{"read failed", dashboardMaintenanceCache{getError: errors.New("private")}, false},
		{"still present", dashboardMaintenanceCache{value: "stale"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := clearDashboardSnapshot(context.Background(), &tc.cache)
			if tc.success {
				require.NoError(t, err)
				require.True(t, tc.cache.deleted)
			} else {
				require.Error(t, err)
			}
		})
	}
}
