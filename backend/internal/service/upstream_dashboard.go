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
	ConfigID int64
	Window   UpstreamDashboardWindow
	Provider string
	Status   string
	Search   string
	Now      time.Time
}

type UpstreamDashboardResponse struct {
	Window  string                   `json:"window"`
	StartAt time.Time                `json:"start_at"`
	EndAt   time.Time                `json:"end_at"`
	Items   []UpstreamDashboardCard  `json:"items"`
	Summary UpstreamDashboardSummary `json:"summary"`
}

type UpstreamDashboardSummary struct {
	TotalConfigurations      int64 `json:"total_configurations"`
	TrafficConfigurations    int64 `json:"traffic_configurations"`
	AttentionConfigurations  int64 `json:"attention_configurations"`
	SchedulableAccounts      int64 `json:"schedulable_accounts"`
	OpenIncidents            int64 `json:"open_incidents"`
	BalanceLowConfigurations int64 `json:"balance_low_configurations"`
}

type UpstreamDashboardCard struct {
	ID                       int64    `json:"id"`
	Name                     string   `json:"name"`
	Provider                 string   `json:"provider"`
	SiteURL                  string   `json:"site_url"`
	Enabled                  bool     `json:"enabled"`
	ConfigStatus             string   `json:"config_status"`
	OverallStatus            string   `json:"overall_status"`
	Requests                 int64    `json:"requests"`
	CompletedRequests        int64    `json:"completed_requests"`
	FailedRequests           int64    `json:"failed_requests"`
	SuccessRate              *float64 `json:"success_rate"`
	Error429                 int64    `json:"error_429"`
	Error5xx                 int64    `json:"error_5xx"`
	Timeouts                 int64    `json:"timeouts"`
	AuthConfigErrors         int64    `json:"auth_config_errors"`
	P50TTFTMs                *int64   `json:"p50_ttft_ms"`
	P95TTFTMs                *int64   `json:"p95_ttft_ms"`
	P50LatencyMs             *int64   `json:"p50_latency_ms"`
	P95LatencyMs             *int64   `json:"p95_latency_ms"`
	AccountCount             int64    `json:"account_count"`
	SchedulableAccountCount  int64    `json:"schedulable_account_count"`
	TempUnschedulableCount   int64    `json:"temp_unschedulable_count"`
	Revenue                  *float64 `json:"revenue"`
	UpstreamCost             *float64 `json:"upstream_cost"`
	EstimatedGrossProfit     *float64 `json:"estimated_gross_profit"`
	EstimatedGrossProfitRate *float64 `json:"estimated_gross_profit_rate"`
	// BalanceCNY is the latest persisted balance converted to CNY. A nil value
	// is intentionally preserved when no snapshot exists or conversion data is
	// unavailable; BalanceAvailable/BalanceUnavailableReason explain the case.
	BalanceCNY                  *float64                      `json:"balance_cny,omitempty"`
	BalanceObservedAt           *time.Time                    `json:"balance_observed_at,omitempty"`
	BalanceAvailable            bool                          `json:"balance_available"`
	BalanceUnavailableReason    string                        `json:"balance_unavailable_reason,omitempty"`
	BalanceLow                  bool                          `json:"balance_low"`
	BalanceThresholdCNY         *float64                      `json:"balance_threshold_cny,omitempty"`
	OpenIncidentCount           int64                         `json:"open_incident_count"`
	LastRateChangeAt            *time.Time                    `json:"last_rate_change_at,omitempty"`
	LastRateChangeOldMultiplier *float64                      `json:"last_rate_change_old_multiplier,omitempty"`
	LastRateChangeNewMultiplier *float64                      `json:"last_rate_change_new_multiplier,omitempty"`
	Probe                       UpstreamDashboardProbeSummary `json:"probe"`
	DataQuality                 string                        `json:"data_quality"`
	Trend                       []UpstreamDashboardTrendPoint `json:"trend,omitempty"`
}

// UpstreamDashboardDetail contains the card snapshot plus drill-down data.
// It is intentionally separate from the list DTO so the list query remains
// bounded while the drawer can request richer data on demand.
type UpstreamDashboardDetail struct {
	UpstreamDashboardCard
	Traffic           UpstreamDashboardTraffic      `json:"traffic"`
	Errors            []UpstreamDashboardError      `json:"recent_errors"`
	RecentIncidents   []UpstreamDashboardIncident   `json:"recent_incidents,omitempty"`
	RecentRateChanges []UpstreamDashboardRateChange `json:"recent_rate_changes,omitempty"`
	ProfitUnavailable bool                          `json:"profit_unavailable"`
	ProfitReason      string                        `json:"profit_reason,omitempty"`
}

type UpstreamDashboardIncident struct {
	ID              int64          `json:"id"`
	ConfigID        int64          `json:"config_id"`
	Type            string         `json:"type"`
	Severity        string         `json:"severity"`
	Status          string         `json:"status"`
	Title           string         `json:"title"`
	MetricValue     *float64       `json:"metric_value,omitempty"`
	ThresholdValue  *float64       `json:"threshold_value,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	OpenedAt        time.Time      `json:"opened_at"`
	LastObservedAt  time.Time      `json:"last_observed_at"`
	ResolvedAt      *time.Time     `json:"resolved_at,omitempty"`
	OccurrenceCount int64          `json:"occurrence_count"`
}

type UpstreamDashboardRateChange struct {
	Type       string    `json:"type"`
	OldRate    *float64  `json:"old_rate,omitempty"`
	NewRate    *float64  `json:"new_rate,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type UpstreamDashboardTraffic struct {
	Models []UpstreamDashboardModelStat `json:"models,omitempty"`
}

type UpstreamDashboardModelStat struct {
	Model    string `json:"model"`
	Requests int64  `json:"requests"`
}

type UpstreamDashboardError struct {
	OccurredAt string `json:"occurred_at"`
	Model      string `json:"model"`
	Category   string `json:"category"`
	StatusCode int    `json:"status_code"`
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

type UpstreamDashboardDetailRepository interface {
	GetUpstreamDashboardDetail(ctx context.Context, id int64, filter UpstreamDashboardFilter) (*UpstreamDashboardDetail, error)
}

func (s *UpstreamConfigService) GetUpstreamDashboard(ctx context.Context, filter UpstreamDashboardFilter) (*UpstreamDashboardResponse, error) {
	if repo, ok := s.repo.(UpstreamDashboardRepository); ok {
		result, err := repo.GetUpstreamDashboard(ctx, filter)
		if err != nil {
			return nil, err
		}
		if result == nil || filter.Status == "" {
			if result != nil {
				populateUpstreamDashboardSummary(result)
			}
			return result, nil
		}
		filtered := result.Items[:0]
		for _, item := range result.Items {
			if item.OverallStatus == filter.Status {
				filtered = append(filtered, item)
			}
		}
		result.Items = filtered
		populateUpstreamDashboardSummary(result)
		return result, nil
	}
	result := &UpstreamDashboardResponse{Window: string(filter.Window), Items: []UpstreamDashboardCard{}}
	populateUpstreamDashboardSummary(result)
	return result, nil
}

func populateUpstreamDashboardSummary(result *UpstreamDashboardResponse) {
	if result == nil {
		return
	}
	var summary UpstreamDashboardSummary
	summary.TotalConfigurations = int64(len(result.Items))
	for _, item := range result.Items {
		if item.Requests > 0 {
			summary.TrafficConfigurations++
		}
		if item.OverallStatus == "critical" || item.OverallStatus == "degraded" {
			summary.AttentionConfigurations++
		}
		if item.SchedulableAccountCount > 0 {
			summary.SchedulableAccounts += item.SchedulableAccountCount
		}
		summary.OpenIncidents += item.OpenIncidentCount
		if item.BalanceLow {
			summary.BalanceLowConfigurations++
		}
	}
	result.Summary = summary
}

func (s *UpstreamConfigService) GetUpstreamDashboardDetail(ctx context.Context, id int64, filter UpstreamDashboardFilter) (*UpstreamDashboardDetail, error) {
	if repo, ok := s.repo.(UpstreamDashboardDetailRepository); ok {
		return repo.GetUpstreamDashboardDetail(ctx, id, filter)
	}
	result, err := s.GetUpstreamDashboard(ctx, filter)
	if err != nil {
		return nil, err
	}
	for _, item := range result.Items {
		if item.ID == id {
			return &UpstreamDashboardDetail{UpstreamDashboardCard: item}, nil
		}
	}
	return nil, ErrUpstreamConfigNotFound
}
