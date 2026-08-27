package service

import (
	"context"
	"time"
)

type UpstreamDashboardWindow string

const (
	UpstreamDashboardWindow1h  UpstreamDashboardWindow = "1h"
	UpstreamDashboardWindow24h UpstreamDashboardWindow = "24h"
	UpstreamDashboardWindow7d  UpstreamDashboardWindow = "7d"
	UpstreamDashboardWindow15d UpstreamDashboardWindow = "15d"
	UpstreamDashboardWindow30d UpstreamDashboardWindow = "30d"
)

type UpstreamDashboardFilter struct {
	Window   UpstreamDashboardWindow
	Provider string
	Status   string
	Search   string
	Now      time.Time
}

type UpstreamDashboardResponse struct {
	Window  string                  `json:"window"`
	StartAt time.Time               `json:"start_at"`
	EndAt   time.Time               `json:"end_at"`
	Items   []UpstreamDashboardCard `json:"items"`
}

type UpstreamDashboardCard struct {
	ID                       int64                         `json:"id"`
	Name                     string                        `json:"name"`
	Provider                 string                        `json:"provider"`
	SiteURL                  string                        `json:"site_url"`
	Enabled                  bool                          `json:"enabled"`
	ConfigStatus             string                        `json:"config_status"`
	OverallStatus            string                        `json:"overall_status"`
	Requests                 int64                         `json:"requests"`
	CompletedRequests        int64                         `json:"completed_requests"`
	FailedRequests           int64                         `json:"failed_requests"`
	SuccessRate              *float64                      `json:"success_rate"`
	Error429                 int64                         `json:"error_429"`
	Error5xx                 int64                         `json:"error_5xx"`
	Timeouts                 int64                         `json:"timeouts"`
	AuthConfigErrors         int64                         `json:"auth_config_errors"`
	P50TTFTMs                *int64                        `json:"p50_ttft_ms"`
	P95TTFTMs                *int64                        `json:"p95_ttft_ms"`
	P50LatencyMs             *int64                        `json:"p50_latency_ms"`
	P95LatencyMs             *int64                        `json:"p95_latency_ms"`
	AccountCount             int64                         `json:"account_count"`
	SchedulableAccountCount  int64                         `json:"schedulable_account_count"`
	TempUnschedulableCount   int64                         `json:"temp_unschedulable_count"`
	Revenue                  *float64                      `json:"revenue"`
	UpstreamCost             *float64                      `json:"upstream_cost"`
	EstimatedGrossProfit     *float64                      `json:"estimated_gross_profit"`
	EstimatedGrossProfitRate *float64                      `json:"estimated_gross_profit_rate"`
	Probe                    UpstreamDashboardProbeSummary `json:"probe"`
	DataQuality              string                        `json:"data_quality"`
	Trend                    []UpstreamDashboardTrendPoint `json:"trend,omitempty"`
}

type UpstreamDashboardProbeSummary struct {
	Samples           int64      `json:"samples"`
	HealthySamples    int64      `json:"healthy_samples"`
	LatestState       string     `json:"latest_state"`
	LatestReason      string     `json:"latest_reason"`
	LatestObservedAt  *time.Time `json:"latest_observed_at"`
	AverageTTFTMs     *float64   `json:"average_ttft_ms"`
	AverageDurationMs *float64   `json:"average_duration_ms"`
	ConfidenceSamples int64      `json:"confidence_samples"`
	ConfidenceStatus  string     `json:"confidence_status"`
}

type UpstreamDashboardTrendPoint struct {
	Bucket       time.Time `json:"bucket"`
	Requests     int64     `json:"requests"`
	Errors       int64     `json:"errors"`
	Revenue      float64   `json:"revenue"`
	UpstreamCost float64   `json:"upstream_cost"`
}

type UpstreamDashboardRepository interface {
	GetUpstreamDashboard(ctx context.Context, filter UpstreamDashboardFilter) (*UpstreamDashboardResponse, error)
}

func (s *UpstreamConfigService) GetUpstreamDashboard(ctx context.Context, filter UpstreamDashboardFilter) (*UpstreamDashboardResponse, error) {
	if repo, ok := s.repo.(UpstreamDashboardRepository); ok {
		result, err := repo.GetUpstreamDashboard(ctx, filter)
		if err != nil {
			return nil, err
		}
		if result == nil || filter.Status == "" {
			return result, nil
		}
		filtered := result.Items[:0]
		for _, item := range result.Items {
			if item.OverallStatus == filter.Status {
				filtered = append(filtered, item)
			}
		}
		result.Items = filtered
		return result, nil
	}
	return &UpstreamDashboardResponse{Window: string(filter.Window), Items: []UpstreamDashboardCard{}}, nil
}
