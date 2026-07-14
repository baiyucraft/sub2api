package service

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	SettingKeyUpstreamBalanceLowThresholdCNY  = "upstream_balance_low_threshold_cny"
	SettingKeyUpstreamSub2APINotInCNConfirmed = "upstream_sub2api_not_in_cn_confirmed"

	UpstreamSyncTriggerManualSingle = "manual_single"
	UpstreamSyncTriggerManualBatch  = "manual_batch"
	UpstreamSyncTriggerScheduled    = "scheduled"

	UpstreamSyncStatusSucceeded = "succeeded"
	UpstreamSyncStatusPartial   = "partial"
	UpstreamSyncStatusFailed    = "failed"
)

type UpstreamSettings struct {
	BalanceLowThresholdCNY  float64 `json:"balance_low_threshold_cny"`
	Sub2APINotInCNConfirmed bool    `json:"sub2api_not_in_cn_confirmed"`
}

type UpstreamSettingsReader interface {
	GetUpstreamSettings(ctx context.Context) (*UpstreamSettings, error)
}

type UpstreamSyncRun struct {
	ID             int64                `json:"id"`
	Trigger        string               `json:"trigger"`
	Status         string               `json:"status"`
	TotalConfigs   int                  `json:"total_configs"`
	SuccessConfigs int                  `json:"success_configs"`
	PartialConfigs int                  `json:"partial_configs"`
	FailedConfigs  int                  `json:"failed_configs"`
	StartedAt      time.Time            `json:"started_at"`
	FinishedAt     *time.Time           `json:"finished_at,omitempty"`
	Results        []UpstreamSyncRecord `json:"results,omitempty"`
}

type UpstreamSyncRecord struct {
	ID                  int64     `json:"id"`
	RunID               int64     `json:"run_id"`
	ConfigID            int64     `json:"config_id"`
	ConfigName          string    `json:"config_name"`
	Provider            string    `json:"provider"`
	Status              string    `json:"status"`
	Stage               string    `json:"stage,omitempty"`
	ErrorCode           string    `json:"error_code,omitempty"`
	SafeMessage         string    `json:"safe_message,omitempty"`
	Retryable           bool      `json:"retryable"`
	HTTPStatus          *int      `json:"http_status,omitempty"`
	RemoteKeyCount      int       `json:"remote_key_count"`
	PersistedKeyCount   int       `json:"persisted_key_count"`
	FallbackKeyCount    int       `json:"fallback_key_count"`
	UnresolvedKeyCount  int       `json:"unresolved_key_count"`
	UpdatedAccountCount int       `json:"updated_account_count"`
	Warnings            []string  `json:"warnings,omitempty"`
	DurationMS          int64     `json:"duration_ms"`
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
}

type UpstreamEvent struct {
	ID        int64          `json:"id"`
	ConfigID  int64          `json:"config_id"`
	KeyID     *int64         `json:"key_id,omitempty"`
	AccountID *int64         `json:"account_id,omitempty"`
	RunID     *int64         `json:"run_id,omitempty"`
	Type      string         `json:"type"`
	Severity  string         `json:"severity"`
	Message   string         `json:"message"`
	Payload   map[string]any `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type UpstreamIncident struct {
	ID             int64          `json:"id"`
	ConfigID       int64          `json:"config_id"`
	Type           string         `json:"type"`
	Status         string         `json:"status"`
	MetricValue    *float64       `json:"metric_value,omitempty"`
	ThresholdValue *float64       `json:"threshold_value,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	OpenedAt       time.Time      `json:"opened_at"`
	LastObservedAt time.Time      `json:"last_observed_at"`
	ResolvedAt     *time.Time     `json:"resolved_at,omitempty"`
}

type UpstreamBalanceSnapshot struct {
	ID                 int64          `json:"id"`
	ConfigID           int64          `json:"config_id"`
	RunID              *int64         `json:"run_id,omitempty"`
	Provider           string         `json:"provider"`
	BalanceRaw         *float64       `json:"balance_raw,omitempty"`
	UsedRaw            *float64       `json:"used_raw,omitempty"`
	TotalRaw           *float64       `json:"total_raw,omitempty"`
	BalanceCNY         *float64       `json:"balance_cny,omitempty"`
	UsedCNY            *float64       `json:"used_cny,omitempty"`
	TotalRechargedCNY  *float64       `json:"total_recharged_cny,omitempty"`
	CurrencySource     string         `json:"currency_source"`
	CurrencyToCNYRate  *float64       `json:"currency_to_cny_rate,omitempty"`
	CurrencyRateSource string         `json:"currency_rate_source"`
	Metadata           map[string]any `json:"metadata,omitempty"`
	ObservedAt         time.Time      `json:"observed_at"`
}

type UpstreamUsageTrendPoint struct {
	Bucket           string  `json:"bucket"`
	Requests         int64   `json:"requests"`
	UpstreamBaseCost float64 `json:"upstream_base_cost"`
	UpstreamCost     float64 `json:"upstream_cost"`
	BilledCost       float64 `json:"billed_cost"`
	GrossProfit      float64 `json:"gross_profit"`
	UnconvertedCost  float64 `json:"unconverted_cost"`
}

type UpstreamUsageTrend struct {
	Range                    string                    `json:"range"`
	Currency                 string                    `json:"currency"`
	LegacyAttributedRequests int64                     `json:"legacy_attributed_requests"`
	Points                   []UpstreamUsageTrendPoint `json:"points"`
}

// UpstreamKeyRateSnapshot is a non-secret observation of one upstream key's rate.
type UpstreamKeyRateSnapshot struct {
	ID             int64     `json:"id"`
	ConfigID       int64     `json:"config_id"`
	KeyID          *int64    `json:"key_id,omitempty"`
	RemoteKeyID    *int64    `json:"remote_key_id,omitempty"`
	KeyName        string    `json:"key_name"`
	RateMultiplier float64   `json:"rate_multiplier"`
	ObservedAt     time.Time `json:"observed_at"`
}

type UpstreamKeyRateChange struct {
	Type       string    `json:"type"`
	OldRate    *float64  `json:"old_rate,omitempty"`
	NewRate    *float64  `json:"new_rate,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type UpstreamKeyRateTrendPoint struct {
	Bucket         string  `json:"bucket"`
	RateMultiplier float64 `json:"rate_multiplier"`
}

type UpstreamKeyRateTrend struct {
	Range           string                      `json:"range"`
	ConfigID        int64                       `json:"config_id"`
	KeyID           int64                       `json:"key_id"`
	RemoteKeyID     *int64                      `json:"remote_key_id,omitempty"`
	KeyName         string                      `json:"key_name"`
	CurrentRate     *float64                    `json:"current_rate,omitempty"`
	PreviousRate    *float64                    `json:"previous_rate,omitempty"`
	FirstObservedAt *time.Time                  `json:"first_observed_at,omitempty"`
	LastChangedAt   *time.Time                  `json:"last_changed_at,omitempty"`
	Points          []UpstreamKeyRateTrendPoint `json:"points"`
	Changes         []UpstreamKeyRateChange     `json:"changes"`
}

type UpstreamKeyRateCatalogItem struct {
	KeyID          int64      `json:"key_id"`
	Name           string     `json:"name"`
	RemoteKeyID    *int64     `json:"remote_key_id,omitempty"`
	Status         string     `json:"status"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	CurrentRate    *float64   `json:"current_rate,omitempty"`
	LastObservedAt *time.Time `json:"last_observed_at,omitempty"`
	LastChangedAt  *time.Time `json:"last_changed_at,omitempty"`
}

type UpstreamOperationsRepository interface {
	GetUpstreamSettings(ctx context.Context) (*UpstreamSettings, error)
	UpdateUpstreamSettings(ctx context.Context, settings UpstreamSettings) error
	CreateSyncRun(ctx context.Context, trigger string, totalConfigs int, startedAt time.Time) (int64, error)
	RecordSyncResult(ctx context.Context, record *UpstreamSyncRecord) error
	FinishSyncRun(ctx context.Context, id int64, status string, success, partial, failed int, finishedAt time.Time) error
	ListSyncRuns(ctx context.Context, limit, offset int) ([]UpstreamSyncRun, int64, error)
	GetSyncRun(ctx context.Context, id int64) (*UpstreamSyncRun, error)
	ListUpstreamEvents(ctx context.Context, configID int64, limit, offset int) ([]UpstreamEvent, int64, error)
	ListUpstreamIncidents(ctx context.Context, configID int64, status string, limit, offset int) ([]UpstreamIncident, int64, error)
	ListUpstreamBalanceHistory(ctx context.Context, configID int64, limit, offset int) ([]UpstreamBalanceSnapshot, int64, error)
	GetUpstreamUsageTrend(ctx context.Context, configID int64, rangeName string, now time.Time) (*UpstreamUsageTrend, error)
	GetUpstreamKeyRateTrend(ctx context.Context, configID, keyID int64, rangeName string, now time.Time) (*UpstreamKeyRateTrend, error)
	ListUpstreamKeyRateTrendKeys(ctx context.Context, configID int64) ([]UpstreamKeyRateCatalogItem, error)
	CleanupUpstreamOperationHistory(ctx context.Context, now time.Time) error
}

func (s *UpstreamConfigService) operationsRepo() (UpstreamOperationsRepository, error) {
	repo, ok := s.repo.(UpstreamOperationsRepository)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("UPSTREAM_OPERATIONS_UNAVAILABLE", "upstream operations repository is unavailable")
	}
	return repo, nil
}

func (s *UpstreamConfigService) GetUpstreamSettings(ctx context.Context) (*UpstreamSettings, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, err
	}
	return repo.GetUpstreamSettings(ctx)
}

func (s *UpstreamConfigService) UpdateUpstreamSettings(ctx context.Context, settings UpstreamSettings) error {
	if !finiteNonNegative(settings.BalanceLowThresholdCNY) {
		return infraerrors.BadRequest("UPSTREAM_BALANCE_THRESHOLD_INVALID", "balance_low_threshold_cny must be a finite non-negative number")
	}
	repo, err := s.operationsRepo()
	if err != nil {
		return err
	}
	return repo.UpdateUpstreamSettings(ctx, settings)
}

func (s *UpstreamConfigService) ListSyncRuns(ctx context.Context, limit, offset int) ([]UpstreamSyncRun, int64, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, 0, err
	}
	return repo.ListSyncRuns(ctx, boundedLimit(limit), upstreamMaxInt(offset, 0))
}

func (s *UpstreamConfigService) GetSyncRun(ctx context.Context, id int64) (*UpstreamSyncRun, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, err
	}
	return repo.GetSyncRun(ctx, id)
}

func (s *UpstreamConfigService) ListEvents(ctx context.Context, configID int64, limit, offset int) ([]UpstreamEvent, int64, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, 0, err
	}
	items, total, err := repo.ListUpstreamEvents(ctx, configID, boundedLimit(limit), upstreamMaxInt(offset, 0))
	if err != nil {
		return nil, 0, err
	}
	return projectUpstreamEvents(items), total, nil
}

func projectUpstreamEvents(events []UpstreamEvent) []UpstreamEvent {
	projected := make([]UpstreamEvent, len(events))
	for i := range events {
		projected[i] = events[i]
		if !isKeyRateEvent(events[i].Type) {
			continue
		}
		projected[i].Payload = projectKeyRateEventPayload(events[i].Payload)
	}
	return projected
}

func isKeyRateEvent(eventType string) bool {
	return eventType == "key_rate_changed" || eventType == "key_effective_rate_changed" || eventType == "key_actual_rate_changed"
}

func projectKeyRateEventPayload(payload map[string]any) map[string]any {
	projected := make(map[string]any, 2)
	if value, ok := eventRateValue(payload, "old_rate", "old_effective_rate"); ok {
		projected["old_rate"] = value
	}
	if value, ok := eventRateValue(payload, "new_rate", "new_effective_rate"); ok {
		projected["new_rate"] = value
	}
	if len(projected) == 0 {
		return nil
	}
	return projected
}

func eventRateValue(payload map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := finiteAnyFloat(payload[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func (s *UpstreamConfigService) ListIncidents(ctx context.Context, configID int64, status string, limit, offset int) ([]UpstreamIncident, int64, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, 0, err
	}
	return repo.ListUpstreamIncidents(ctx, configID, strings.TrimSpace(status), boundedLimit(limit), upstreamMaxInt(offset, 0))
}

func (s *UpstreamConfigService) ListBalanceHistory(ctx context.Context, configID int64, limit, offset int) ([]UpstreamBalanceSnapshot, int64, error) {
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, 0, err
	}
	return repo.ListUpstreamBalanceHistory(ctx, configID, boundedLimit(limit), upstreamMaxInt(offset, 0))
}

func (s *UpstreamConfigService) GetUsageTrend(ctx context.Context, configID int64, rangeName string) (*UpstreamUsageTrend, error) {
	rangeName = strings.ToLower(strings.TrimSpace(rangeName))
	if rangeName == "" {
		rangeName = "24h"
	}
	if rangeName != "24h" && rangeName != "7d" && rangeName != "30d" {
		return nil, infraerrors.BadRequest("UPSTREAM_TREND_RANGE_INVALID", "range must be one of 24h, 7d, 30d")
	}
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, err
	}
	return repo.GetUpstreamUsageTrend(ctx, configID, rangeName, time.Now().UTC())
}

func (s *UpstreamConfigService) GetKeyRateTrend(ctx context.Context, configID, keyID int64, rangeName string) (*UpstreamKeyRateTrend, error) {
	rangeName = strings.ToLower(strings.TrimSpace(rangeName))
	if rangeName == "" {
		rangeName = "24h"
	}
	if rangeName != "24h" && rangeName != "7d" && rangeName != "30d" {
		return nil, infraerrors.BadRequest("UPSTREAM_RATE_TREND_RANGE_INVALID", "range must be one of 24h, 7d, 30d")
	}
	if configID <= 0 || keyID <= 0 {
		return nil, infraerrors.BadRequest("UPSTREAM_RATE_TREND_ID_INVALID", "config_id and key_id must be positive")
	}
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, err
	}
	return repo.GetUpstreamKeyRateTrend(ctx, configID, keyID, rangeName, time.Now().UTC())
}

func (s *UpstreamConfigService) ListKeyRateTrendKeys(ctx context.Context, configID int64) ([]UpstreamKeyRateCatalogItem, error) {
	if configID <= 0 {
		return nil, infraerrors.BadRequest("UPSTREAM_RATE_TREND_CONFIG_INVALID", "config_id must be positive")
	}
	repo, err := s.operationsRepo()
	if err != nil {
		return nil, err
	}
	return repo.ListUpstreamKeyRateTrendKeys(ctx, configID)
}

func (s *UpstreamConfigService) beginSyncRun(ctx context.Context, trigger string, total int) (int64, error) {
	repo, ok := s.repo.(UpstreamOperationsRepository)
	if !ok {
		return 0, nil
	}
	return repo.CreateSyncRun(ctx, trigger, total, time.Now().UTC())
}

func (s *UpstreamConfigService) persistSyncResult(ctx context.Context, startedAt time.Time, result UpstreamConfigSyncResult) error {
	repo, ok := s.repo.(UpstreamOperationsRepository)
	if !ok || result.RunID <= 0 {
		return nil
	}
	finishedAt := time.Now().UTC()
	status := result.Status
	if status == "" {
		if result.Success {
			status = UpstreamSyncStatusSucceeded
		} else {
			status = UpstreamSyncStatusFailed
		}
	}
	return repo.RecordSyncResult(ctx, &UpstreamSyncRecord{
		RunID:               result.RunID,
		ConfigID:            result.ConfigID,
		ConfigName:          result.Name,
		Provider:            result.Provider,
		Status:              status,
		Stage:               result.Stage,
		ErrorCode:           result.ErrorCode,
		SafeMessage:         result.Error,
		Retryable:           result.Retryable,
		RemoteKeyCount:      result.KeyCount + result.UnresolvedKeyCount,
		PersistedKeyCount:   result.KeyCount,
		FallbackKeyCount:    result.FallbackKeyCount,
		UnresolvedKeyCount:  result.UnresolvedKeyCount,
		UpdatedAccountCount: result.UpdatedAccountCount,
		Warnings:            append([]string(nil), result.Warnings...),
		DurationMS:          finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:           startedAt,
		FinishedAt:          finishedAt,
	})
}

func (s *UpstreamConfigService) finishSyncRun(ctx context.Context, runID int64, results []UpstreamConfigSyncResult) error {
	if runID <= 0 {
		return nil
	}
	repo, ok := s.repo.(UpstreamOperationsRepository)
	if !ok {
		return nil
	}
	success, partial, failed := 0, 0, 0
	for _, result := range results {
		switch result.Status {
		case UpstreamSyncStatusPartial:
			partial++
		case UpstreamSyncStatusSucceeded:
			success++
		default:
			failed++
		}
	}
	status := UpstreamSyncStatusSucceeded
	if failed > 0 {
		status = UpstreamSyncStatusFailed
		if success > 0 || partial > 0 {
			status = UpstreamSyncStatusPartial
		}
	} else if partial > 0 {
		status = UpstreamSyncStatusPartial
	}
	if err := repo.FinishSyncRun(ctx, runID, status, success, partial, failed, time.Now().UTC()); err != nil {
		return err
	}
	return repo.CleanupUpstreamOperationHistory(ctx, time.Now().UTC())
}

func classifyUpstreamSyncFailure(err error, fallbackStage string) (stage, code string, retryable bool) {
	stage = fallbackStage
	code = "unknown"
	if err == nil {
		return stage, code, false
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "proxy"):
		return "proxy", "network", true
	case strings.Contains(text, "login"), strings.Contains(text, "token"), strings.Contains(text, "401"), strings.Contains(text, "403"):
		return "auth", "auth", false
	case strings.Contains(text, "turnstile"), strings.Contains(text, "2fa"), strings.Contains(text, "captcha"):
		return "auth", "verification", false
	case strings.Contains(text, "group"):
		return "groups", "protocol", false
	case strings.Contains(text, "key"):
		return "keys_page", "protocol", false
	case strings.Contains(text, "timeout"), strings.Contains(text, "connection"), strings.Contains(text, "network"):
		return stage, "network", true
	case strings.Contains(text, "status 5"), strings.Contains(text, "bad gateway"):
		return stage, "upstream", true
	case strings.Contains(text, "profile"), strings.Contains(text, "quota"), strings.Contains(text, "usage"), strings.Contains(text, "incompatible response"):
		return "profile", "protocol", false
	case stage == "persist", stage == "account_apply":
		return stage, "database", true
	default:
		return stage, code, false
	}
}

func normalizeProviderBalanceExtra(cfg *UpstreamConfig, updates map[string]any) map[string]any {
	if cfg == nil || len(updates) == 0 {
		return updates
	}
	if updates == nil {
		updates = map[string]any{}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if cfg.Provider == UpstreamProviderSub2API {
		balance, balanceOK := finiteAnyFloat(updates["sub2api_balance"])
		total, totalOK := finiteAnyFloat(updates["sub2api_total_recharged"])
		if balanceOK {
			updates["balance_cny"] = balance
			updates["used_cny"] = maxFloat(total-balance, 0)
		}
		if totalOK {
			updates["total_recharged_cny"] = total
		}
		if balanceOK || totalOK {
			updates["currency_source"] = "CNY"
			updates["currency_to_cny_rate"] = 1.0
			updates["currency_rate_source"] = "provider_default"
			updates["currency_converted_at"] = now
		}
		return updates
	}

	snapshot, _ := updates["upstream_provider_snapshot"].(map[string]any)
	if snapshot == nil {
		return updates
	}
	currency := strings.ToUpper(strings.TrimSpace(anyString(snapshot["currency"])))
	balance, balanceOK := finiteAnyFloat(snapshot["balance_amount"])
	used, usedOK := finiteAnyFloat(snapshot["used_amount"])
	total, totalOK := finiteAnyFloat(snapshot["total_amount"])
	rate := 0.0
	rateSource := ""
	if cfg.BalanceToCNYRate != nil && *cfg.BalanceToCNYRate > 0 {
		rate, rateSource = *cfg.BalanceToCNYRate, "admin_override"
		balance, balanceOK = finiteAnyFloat(snapshot["base_balance_amount"])
		used, usedOK = finiteAnyFloat(snapshot["base_used_amount"])
		total, totalOK = finiteAnyFloat(snapshot["base_total_amount"])
		if !balanceOK || !usedOK || !totalOK {
			quotaPerUnit, quotaOK := finiteAnyFloat(snapshot["quota_per_unit"])
			balanceRaw, balanceRawOK := finiteAnyFloat(snapshot["remain_quota_raw"])
			usedRaw, usedRawOK := finiteAnyFloat(snapshot["used_quota_raw"])
			totalRaw, totalRawOK := finiteAnyFloat(snapshot["total_quota_raw"])
			if quotaOK && quotaPerUnit > 0 {
				if !balanceOK && balanceRawOK {
					balance, balanceOK = balanceRaw/quotaPerUnit, true
				}
				if !usedOK && usedRawOK {
					used, usedOK = usedRaw/quotaPerUnit, true
				}
				if !totalOK && totalRawOK {
					total, totalOK = totalRaw/quotaPerUnit, true
				}
			}
		}
	} else {
		switch currency {
		case "CNY":
			rate, rateSource = 1, "provider"
		case "USD":
			if value, ok := finiteAnyFloat(snapshot["usd_exchange_rate"]); ok && value > 0 {
				rate, rateSource = value, "provider"
			}
		}
	}
	updates["currency_source"] = currency
	if rate <= 0 {
		updates["balance_cny"] = nil
		updates["used_cny"] = nil
		updates["total_recharged_cny"] = nil
		updates["currency_to_cny_rate"] = nil
		updates["currency_rate_source"] = "unavailable"
		updates["currency_converted_at"] = ""
		return updates
	}
	updates["currency_to_cny_rate"] = rate
	updates["currency_rate_source"] = rateSource
	updates["currency_converted_at"] = now
	if balanceOK {
		updates["balance_cny"] = balance * rate
	}
	if usedOK {
		updates["used_cny"] = used * rate
	}
	if totalOK {
		updates["total_recharged_cny"] = total * rate
	}
	return updates
}

func upstreamCostToCNYRate(cfg *UpstreamConfig, extra map[string]any) (float64, bool) {
	if rate, ok := finiteAnyFloat(extra["currency_to_cny_rate"]); ok && rate > 0 {
		return rate, true
	}
	if cfg != nil && cfg.Provider == UpstreamProviderSub2API {
		return 1, true
	}
	if cfg != nil && cfg.BalanceToCNYRate != nil && *cfg.BalanceToCNYRate > 0 {
		return *cfg.BalanceToCNYRate, true
	}
	return 0, false
}

func finiteAnyFloat(value any) (float64, bool) {
	var parsed float64
	switch v := value.(type) {
	case float64:
		parsed = v
	case float32:
		parsed = float64(v)
	case int:
		parsed = float64(v)
	case int64:
		parsed = float64(v)
	case string:
		var err error
		parsed, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return parsed, !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
}

func anyString(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func boundedLimit(value int) int {
	if value <= 0 {
		return 50
	}
	if value > 200 {
		return 200
	}
	return value
}

func upstreamMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
