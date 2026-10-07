package main

import (
	"context"
	"errors"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Reuses the runtime repository's configuration and exact key boundary.
// It never initializes the application, migrations or background tasks.
func clearDashboardSnapshot(ctx context.Context, cache service.DashboardStatsCache) error {
	if err := cache.DeleteDashboardStats(ctx); err != nil {
		return err
	}
	_, err := cache.GetDashboardStats(ctx)
	if errors.Is(err, service.ErrDashboardStatsCacheMiss) {
		return nil
	}
	return errors.New("dashboard snapshot absence could not be verified")
}
