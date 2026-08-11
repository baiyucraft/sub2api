package service

import (
	"context"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

type upstreamHealthTrendRepository interface {
	GetUpstreamHealthTrend(ctx context.Context, keyID int64, rangeName string, now time.Time) (*UpstreamHealthTrend, error)
}

func NormalizeUpstreamHealthTrendRange(rangeName string) (string, time.Duration, time.Duration, error) {
	rangeName = strings.ToLower(strings.TrimSpace(rangeName))
	if rangeName == "" {
		rangeName = "6h"
	}
	switch rangeName {
	case "6h":
		return rangeName, 6 * time.Hour, 5 * time.Minute, nil
	case "24h":
		return rangeName, 24 * time.Hour, 15 * time.Minute, nil
	case "7d":
		return rangeName, 7 * 24 * time.Hour, 2 * time.Hour, nil
	case "30d":
		return rangeName, 30 * 24 * time.Hour, 12 * time.Hour, nil
	default:
		return "", 0, 0, infraerrors.BadRequest("UPSTREAM_HEALTH_TREND_RANGE_INVALID", "range must be one of 6h, 24h, 7d, 30d")
	}
}

func (s *UpstreamConfigService) GetUpstreamHealthTrend(ctx context.Context, keyID int64, rangeName string) (*UpstreamHealthTrend, error) {
	if keyID <= 0 {
		return nil, infraerrors.BadRequest("UPSTREAM_HEALTH_TREND_KEY_INVALID", "upstream key id must be positive")
	}
	rangeName, _, _, err := NormalizeUpstreamHealthTrendRange(rangeName)
	if err != nil {
		return nil, err
	}
	repo, ok := s.repo.(upstreamHealthTrendRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("UPSTREAM_HEALTH_TREND_UNAVAILABLE", "upstream health trend repository is unavailable")
	}
	return repo.GetUpstreamHealthTrend(ctx, keyID, rangeName, time.Now().UTC())
}
