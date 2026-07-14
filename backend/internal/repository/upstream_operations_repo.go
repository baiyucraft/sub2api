package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbupstreambalancesnapshot "github.com/Wei-Shaw/sub2api/ent/upstreambalancesnapshot"
	dbupstreamevent "github.com/Wei-Shaw/sub2api/ent/upstreamevent"
	dbupstreamincident "github.com/Wei-Shaw/sub2api/ent/upstreamincident"
	dbupstreamkeyratesnapshot "github.com/Wei-Shaw/sub2api/ent/upstreamkeyratesnapshot"
	dbupstreamsyncrun "github.com/Wei-Shaw/sub2api/ent/upstreamsyncrun"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *upstreamConfigRepository) GetUpstreamSettings(ctx context.Context) (*service.UpstreamSettings, error) {
	rows, err := r.client.Setting.Query().Where(func(s *entsql.Selector) {
		s.Where(entsql.In(s.C("key"),
			service.SettingKeyUpstreamBalanceLowThresholdCNY,
			service.SettingKeyUpstreamSub2APINotInCNConfirmed,
			service.SettingKeyUpstreamCostIncludedGroupIDs,
		))
	}).All(ctx)
	if err != nil {
		return nil, err
	}
	settings := &service.UpstreamSettings{}
	for _, row := range rows {
		switch row.Key {
		case service.SettingKeyUpstreamBalanceLowThresholdCNY:
			settings.BalanceLowThresholdCNY, _ = strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
		case service.SettingKeyUpstreamSub2APINotInCNConfirmed:
			settings.Sub2APINotInCNConfirmed, _ = strconv.ParseBool(strings.TrimSpace(row.Value))
		case service.SettingKeyUpstreamCostIncludedGroupIDs:
			_ = json.Unmarshal([]byte(row.Value), &settings.CostIncludedGroupIDs)
		}
	}
	return settings, nil
}

func (r *upstreamConfigRepository) UpdateUpstreamSettings(ctx context.Context, settings service.UpstreamSettings) error {
	return r.withTx(ctx, func(txCtx context.Context, client *dbent.Client) error {
		now := time.Now().UTC()
		values := map[string]string{
			service.SettingKeyUpstreamBalanceLowThresholdCNY:  strconv.FormatFloat(settings.BalanceLowThresholdCNY, 'f', 8, 64),
			service.SettingKeyUpstreamSub2APINotInCNConfirmed: strconv.FormatBool(settings.Sub2APINotInCNConfirmed),
		}
		groupIDs, err := json.Marshal(service.NormalizeUpstreamGroupIDsForPersistence(settings.CostIncludedGroupIDs))
		if err != nil {
			return err
		}
		values[service.SettingKeyUpstreamCostIncludedGroupIDs] = string(groupIDs)
		for key, value := range values {
			if err := client.Setting.Create().
				SetKey(key).
				SetValue(value).
				SetUpdatedAt(now).
				OnConflictColumns("key").
				UpdateNewValues().
				Exec(txCtx); err != nil {
				return err
			}
		}
		configs, err := client.UpstreamConfig.Query().All(txCtx)
		if err != nil {
			return err
		}
		for _, config := range configs {
			balance, ok := numberFromMap(config.Extra, "balance_cny")
			if !ok {
				continue
			}
			if err := evaluateUpstreamBalanceIncident(txCtx, client, config.ID, balance, settings.BalanceLowThresholdCNY, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *upstreamConfigRepository) CreateSyncRun(ctx context.Context, trigger string, totalConfigs int, startedAt time.Time) (int64, error) {
	row, err := r.client.UpstreamSyncRun.Create().
		SetTrigger(trigger).
		SetStatus("running").
		SetStartedAt(startedAt).
		SetTotalConfigs(totalConfigs).
		Save(ctx)
	if err != nil {
		return 0, err
	}
	return row.ID, nil
}

func (r *upstreamConfigRepository) RecordSyncResult(ctx context.Context, record *service.UpstreamSyncRecord) error {
	if record == nil {
		return nil
	}
	builder := r.client.UpstreamSyncResult.Create().
		SetSyncRunID(record.RunID).
		SetUpstreamConfigID(record.ConfigID).
		SetConfigName(record.ConfigName).
		SetProvider(record.Provider).
		SetStatus(record.Status).
		SetStage(record.Stage).
		SetErrorCode(record.ErrorCode).
		SetRetryable(record.Retryable).
		SetRemoteKeyCount(record.RemoteKeyCount).
		SetPersistedKeyCount(record.PersistedKeyCount).
		SetFallbackKeyCount(record.FallbackKeyCount).
		SetUnresolvedKeyCount(record.UnresolvedKeyCount).
		SetUpdatedAccountCount(record.UpdatedAccountCount).
		SetWarnings(record.Warnings).
		SetDurationMs(record.DurationMS).
		SetStartedAt(record.StartedAt).
		SetFinishedAt(record.FinishedAt)
	if record.SafeMessage != "" {
		builder.SetSafeMessage(record.SafeMessage)
	}
	if record.HTTPStatus != nil {
		builder.SetHTTPStatus(*record.HTTPStatus)
	}
	return builder.
		OnConflictColumns("sync_run_id", "upstream_config_id").
		UpdateNewValues().
		Exec(ctx)
}

func (r *upstreamConfigRepository) FinishSyncRun(ctx context.Context, id int64, status string, success, partial, failed int, finishedAt time.Time) error {
	return r.client.UpstreamSyncRun.UpdateOneID(id).
		SetStatus(status).
		SetSuccessConfigs(success).
		SetPartialConfigs(partial).
		SetFailedConfigs(failed).
		SetFinishedAt(finishedAt).
		Exec(ctx)
}

func (r *upstreamConfigRepository) ListSyncRuns(ctx context.Context, limit, offset int) ([]service.UpstreamSyncRun, int64, error) {
	total, err := r.client.UpstreamSyncRun.Query().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := r.client.UpstreamSyncRun.Query().
		Order(dbent.Desc(dbupstreamsyncrun.FieldStartedAt)).Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]service.UpstreamSyncRun, 0, len(rows))
	for _, row := range rows {
		out = append(out, syncRunEntity(row))
	}
	return out, int64(total), nil
}

func (r *upstreamConfigRepository) GetSyncRun(ctx context.Context, id int64) (*service.UpstreamSyncRun, error) {
	row, err := r.client.UpstreamSyncRun.Query().Where(dbupstreamsyncrun.IDEQ(id)).WithResults().Only(ctx)
	if err != nil {
		return nil, err
	}
	out := syncRunEntity(row)
	for _, result := range row.Edges.Results {
		out.Results = append(out.Results, service.UpstreamSyncRecord{
			ID: result.ID, RunID: result.SyncRunID, ConfigID: result.UpstreamConfigID,
			ConfigName: result.ConfigName, Provider: result.Provider, Status: result.Status,
			Stage: result.Stage, ErrorCode: result.ErrorCode, SafeMessage: valueOrEmpty(result.SafeMessage),
			Retryable: result.Retryable, HTTPStatus: result.HTTPStatus,
			RemoteKeyCount: result.RemoteKeyCount, PersistedKeyCount: result.PersistedKeyCount,
			FallbackKeyCount: result.FallbackKeyCount, UnresolvedKeyCount: result.UnresolvedKeyCount,
			UpdatedAccountCount: result.UpdatedAccountCount, Warnings: result.Warnings,
			DurationMS: result.DurationMs, StartedAt: result.StartedAt, FinishedAt: result.FinishedAt,
		})
	}
	return &out, nil
}

func syncRunEntity(row *dbent.UpstreamSyncRun) service.UpstreamSyncRun {
	return service.UpstreamSyncRun{
		ID: row.ID, Trigger: row.Trigger, Status: row.Status, TotalConfigs: row.TotalConfigs,
		SuccessConfigs: row.SuccessConfigs, PartialConfigs: row.PartialConfigs, FailedConfigs: row.FailedConfigs,
		StartedAt: row.StartedAt, FinishedAt: row.FinishedAt,
	}
}

func (r *upstreamConfigRepository) ListUpstreamEvents(ctx context.Context, configID int64, limit, offset int) ([]service.UpstreamEvent, int64, error) {
	query := r.client.UpstreamEvent.Query()
	if configID > 0 {
		query = query.Where(dbupstreamevent.UpstreamConfigIDEQ(configID))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := query.Order(dbent.Desc(dbupstreamevent.FieldOccurredAt)).Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]service.UpstreamEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, service.UpstreamEvent{ID: row.ID, ConfigID: row.UpstreamConfigID, KeyID: row.UpstreamKeyID, AccountID: row.AccountID, RunID: row.SyncRunID, Type: row.EventType, Severity: row.Severity, Message: valueOrEmpty(row.Message), Payload: row.Payload, CreatedAt: row.OccurredAt})
	}
	return out, int64(total), nil
}

func (r *upstreamConfigRepository) ListUpstreamIncidents(ctx context.Context, configID int64, status string, limit, offset int) ([]service.UpstreamIncident, int64, error) {
	query := r.client.UpstreamIncident.Query()
	if configID > 0 {
		query = query.Where(dbupstreamincident.UpstreamConfigIDEQ(configID))
	}
	if status != "" {
		query = query.Where(dbupstreamincident.StatusEQ(status))
	}
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := query.Order(dbent.Desc(dbupstreamincident.FieldLastSeenAt)).Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]service.UpstreamIncident, 0, len(rows))
	for _, row := range rows {
		out = append(out, service.UpstreamIncident{ID: row.ID, ConfigID: row.UpstreamConfigID, Type: row.IncidentType, Status: row.Status, MetricValue: row.MetricValue, ThresholdValue: row.ThresholdValue, Metadata: row.Details, OpenedAt: row.FirstSeenAt, LastObservedAt: row.LastSeenAt, ResolvedAt: row.ResolvedAt})
	}
	return out, int64(total), nil
}

func (r *upstreamConfigRepository) ListUpstreamBalanceHistory(ctx context.Context, configID int64, limit, offset int) ([]service.UpstreamBalanceSnapshot, int64, error) {
	query := r.client.UpstreamBalanceSnapshot.Query().Where(dbupstreambalancesnapshot.UpstreamConfigIDEQ(configID))
	total, err := query.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := query.Order(dbent.Desc(dbupstreambalancesnapshot.FieldObservedAt)).Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]service.UpstreamBalanceSnapshot, 0, len(rows))
	for _, row := range rows {
		out = append(out, service.UpstreamBalanceSnapshot{ID: row.ID, ConfigID: row.UpstreamConfigID, RunID: row.SyncRunID, Provider: row.Provider, BalanceRaw: row.BalanceRaw, UsedRaw: row.UsedRaw, TotalRaw: row.TotalRaw, BalanceCNY: row.BalanceCny, UsedCNY: row.UsedCny, TotalRechargedCNY: row.TotalRechargedCny, CurrencySource: row.CurrencySource, CurrencyToCNYRate: row.CurrencyToCnyRate, CurrencyRateSource: row.CurrencyRateSource, RechargeRate: row.RechargeRate, BalanceFormulaVersion: row.BalanceFormulaVersion, Metadata: row.Metadata, ObservedAt: row.ObservedAt})
	}
	return out, int64(total), nil
}

func createRechargeRateChangedEvent(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, old, current float64, at time.Time) error {
	return client.UpstreamEvent.Create().SetUpstreamConfigID(config.ID).SetEventType("recharge_rate_changed").SetSeverity("info").SetSource("admin").SetMessage("Upstream recharge rate changed").SetPayload(map[string]any{"old_rate": old, "new_rate": current}).SetOccurredAt(at).Exec(ctx)
}

func recalculateLockedUpstreamAccounts(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, keys []*dbent.UpstreamKey, accounts []*dbent.Account, at time.Time) ([]int64, error) {
	for _, key := range keys {
		if key.SourceRateMultiplier == nil {
			continue
		}
		actual, rateErr := service.NormalizeUpstreamActualRate(*key.SourceRateMultiplier, config.RechargeRate)
		if rateErr != nil {
			return nil, rateErr
		}
		previous := key.RateMultiplier
		if previous != nil && sameRate(*previous, actual) {
			continue
		}
		if _, err := client.UpstreamKey.UpdateOneID(key.ID).SetRateMultiplier(actual).Save(ctx); err != nil {
			return nil, err
		}
		key.RateMultiplier = &actual
		if err := client.UpstreamKeyRateSnapshot.Create().
			SetUpstreamConfigID(config.ID).
			SetUpstreamKeyID(key.ID).
			SetKeyNameSnapshot(key.Name).
			SetKeyHashSnapshot(key.KeyHash).
			SetProvider(config.Provider).
			SetRawRateMultiplier(*key.SourceRateMultiplier).
			SetRechargeRate(config.RechargeRate).
			SetEffectiveCostMultiplier(actual).
			SetSource("recharge_rate_change").
			SetObservedAt(at).
			Exec(ctx); err != nil {
			return nil, err
		}
		if previous != nil {
			if err := createActualRateChangeEvent(ctx, client, config.ID, key.ID, 0, *previous, actual, at); err != nil {
				return nil, err
			}
		}
	}
	byID := make(map[int64]*dbent.UpstreamKey, len(keys))
	for _, key := range keys {
		byID[key.ID] = key
	}
	changed := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		if account.UpstreamKeyID == nil {
			continue
		}
		key := byID[*account.UpstreamKeyID]
		if key == nil {
			continue
		}
		ok, err := syncUpstreamAccount(ctx, client, account, config, key, nil, at)
		if err != nil {
			return nil, err
		}
		if ok {
			changed = append(changed, account.ID)
		}
	}
	return changed, nil
}

func recalculateLockedUpstreamBalance(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, oldRate, newRate float64, at time.Time) error {
	if config == nil || oldRate <= 0 || newRate <= 0 || oldRate == newRate {
		return nil
	}
	extra := copyJSONMap(config.Extra)
	formulaVersion, formulaVersionOK := integerFromMap(extra, "balance_formula_version")
	if formulaVersionOK && formulaVersion == 2 {
		factor := newRate / oldRate
		for _, key := range []string{"balance_cny", "used_cny", "total_recharged_cny"} {
			if value, ok := numberFromMap(extra, key); ok {
				extra[key] = roundPersistedBalanceAmount(value * factor)
			}
		}
	} else if err := recalculateLegacyBalanceExtra(extra, config.Provider, newRate); err != nil {
		return err
	}
	extra["recharge_rate"] = newRate
	extra["balance_formula_version"] = 2
	if _, err := client.UpstreamConfig.UpdateOneID(config.ID).SetExtra(extra).Save(ctx); err != nil {
		return err
	}
	updates := map[string]any{
		"balance_cny":             extra["balance_cny"],
		"used_cny":                extra["used_cny"],
		"total_recharged_cny":     extra["total_recharged_cny"],
		"currency_source":         extra["currency_source"],
		"currency_to_cny_rate":    extra["currency_to_cny_rate"],
		"currency_rate_source":    extra["currency_rate_source"],
		"recharge_rate":           newRate,
		"balance_formula_version": 2,
		"balance_recalculated":    true,
	}
	for _, key := range []string{"sub2api_balance", "sub2api_total_recharged", "upstream_provider_snapshot"} {
		if value, exists := extra[key]; exists {
			updates[key] = value
		}
	}
	if err := persistUpstreamBalanceState(ctx, client, config, 0, updates, at); err != nil {
		return err
	}
	if balance, ok := numberFromMap(extra, "balance_cny"); ok {
		threshold, err := upstreamBalanceThreshold(ctx, client)
		if err != nil {
			return err
		}
		if err := evaluateUpstreamBalanceIncident(ctx, client, config.ID, balance, threshold, at); err != nil {
			return err
		}
	}
	return client.UpstreamEvent.Create().
		SetUpstreamConfigID(config.ID).
		SetEventType("balance_recalculated").
		SetSeverity("info").
		SetSource("admin").
		SetMessage("Upstream balance recalculated after recharge rate change").
		SetPayload(map[string]any{"old_rate": oldRate, "new_rate": newRate}).
		SetOccurredAt(at).
		Exec(ctx)
}

func recalculateLegacyBalanceExtra(extra map[string]any, provider string, rechargeRate float64) error {
	if extra == nil {
		return nil
	}
	needsBalance, needsUsed, needsTotal := false, false, false
	_, needsBalance = numberFromMap(extra, "balance_cny")
	_, needsUsed = numberFromMap(extra, "used_cny")
	_, needsTotal = numberFromMap(extra, "total_recharged_cny")
	if !needsBalance && !needsUsed && !needsTotal {
		return nil
	}

	var balance, used, total float64
	var balanceOK, usedOK, totalOK bool
	if provider == service.UpstreamProviderSub2API {
		balance, balanceOK = numberFromMap(extra, "sub2api_balance")
		total, totalOK = numberFromMap(extra, "sub2api_total_recharged")
		if balanceOK && totalOK {
			used, usedOK = maxFloatValue(total-balance, 0), true
		}
	} else {
		snapshot, _ := extra["upstream_provider_snapshot"].(map[string]any)
		if snapshot == nil {
			return fmt.Errorf("upstream balance raw snapshot unavailable for recharge-rate change")
		}
		rateSource, _ := extra["currency_rate_source"].(string)
		useBase := strings.EqualFold(rateSource, "admin_override")
		balanceKey, usedKey, totalKey := "balance_amount", "used_amount", "total_amount"
		if useBase {
			balanceKey, usedKey, totalKey = "base_balance_amount", "base_used_amount", "base_total_amount"
		}
		balance, balanceOK = numberFromMap(snapshot, balanceKey)
		used, usedOK = numberFromMap(snapshot, usedKey)
		total, totalOK = numberFromMap(snapshot, totalKey)
		if rate, ok := numberFromMap(extra, "currency_to_cny_rate"); ok && rate > 0 {
			balance *= rate
			used *= rate
			total *= rate
		} else if currency, _ := extra["currency_source"].(string); strings.EqualFold(currency, "CNY") {
			// CNY snapshots are already denominated in the target currency.
		} else if needsBalance || needsUsed || needsTotal {
			return fmt.Errorf("upstream balance currency rate unavailable for recharge-rate change")
		}
	}

	if needsBalance {
		if !balanceOK {
			return fmt.Errorf("upstream raw balance unavailable for recharge-rate change")
		}
		extra["balance_cny"] = roundPersistedBalanceAmount(balance * rechargeRate)
	}
	if needsUsed {
		if !usedOK {
			return fmt.Errorf("upstream raw used balance unavailable for recharge-rate change")
		}
		extra["used_cny"] = roundPersistedBalanceAmount(used * rechargeRate)
	}
	if needsTotal {
		if !totalOK {
			return fmt.Errorf("upstream raw total balance unavailable for recharge-rate change")
		}
		extra["total_recharged_cny"] = roundPersistedBalanceAmount(total * rechargeRate)
	}
	return nil
}

func persistUpstreamBalanceState(ctx context.Context, client *dbent.Client, config *dbent.UpstreamConfig, runID int64, updates map[string]any, at time.Time) error {
	if updates == nil {
		updates = map[string]any{}
	}
	if _, ok := numberFromMap(updates, "recharge_rate"); !ok {
		rate := config.RechargeRate
		if rate <= 0 {
			rate = 1
		}
		updates["recharge_rate"] = rate
	}
	if _, ok := integerFromMap(updates, "balance_formula_version"); !ok {
		updates["balance_formula_version"] = 2
	}
	balance, balanceOK := numberFromMap(updates, "balance_cny")
	builder := client.UpstreamBalanceSnapshot.Create().SetUpstreamConfigID(config.ID).SetProvider(config.Provider).SetObservedAt(at).SetMetadata(copyJSONMap(updates))
	if balanceOK {
		builder.SetBalanceCny(balance)
	}
	if runID > 0 {
		builder.SetSyncRunID(runID)
	}
	if value, ok := numberFromMap(updates, "used_cny"); ok {
		builder.SetUsedCny(value)
	}
	if value, ok := numberFromMap(updates, "total_recharged_cny"); ok {
		builder.SetTotalRechargedCny(value)
	}
	if value, ok := numberFromMap(updates, "currency_to_cny_rate"); ok {
		builder.SetCurrencyToCnyRate(value)
	}
	if value, ok := numberFromMap(updates, "recharge_rate"); ok {
		builder.SetRechargeRate(value)
	}
	if value, ok := integerFromMap(updates, "balance_formula_version"); ok {
		builder.SetBalanceFormulaVersion(value)
	}
	if config.Provider == service.UpstreamProviderSub2API {
		if value, ok := numberFromMap(updates, "sub2api_balance"); ok {
			builder.SetBalanceRaw(value)
		}
		if value, ok := numberFromMap(updates, "sub2api_total_recharged"); ok {
			builder.SetTotalRaw(value)
		}
		if total, totalOK := numberFromMap(updates, "sub2api_total_recharged"); totalOK {
			if balance, balanceOK := numberFromMap(updates, "sub2api_balance"); balanceOK {
				builder.SetUsedRaw(maxFloatValue(total-balance, 0))
			}
		}
	} else if snapshot, ok := updates["upstream_provider_snapshot"].(map[string]any); ok {
		if value, ok := numberFromMap(snapshot, "remain_quota_raw"); ok {
			builder.SetBalanceRaw(value)
		}
		if value, ok := numberFromMap(snapshot, "used_quota_raw"); ok {
			builder.SetUsedRaw(value)
		}
		if value, ok := numberFromMap(snapshot, "total_quota_raw"); ok {
			builder.SetTotalRaw(value)
		}
	}
	if value := strings.TrimSpace(fmt.Sprint(updates["currency_source"])); value != "" {
		builder.SetCurrencySource(value)
	}
	if value := strings.TrimSpace(fmt.Sprint(updates["currency_rate_source"])); value != "" {
		builder.SetCurrencyRateSource(value)
	}
	if err := builder.Exec(ctx); err != nil {
		return err
	}
	oldRateSource := strings.TrimSpace(fmt.Sprint(config.Extra["currency_rate_source"]))
	newRateSource := strings.TrimSpace(fmt.Sprint(updates["currency_rate_source"]))
	if newRateSource != "" && oldRateSource != "" && oldRateSource != newRateSource {
		event := client.UpstreamEvent.Create().SetUpstreamConfigID(config.ID).SetEventType("currency_conversion_changed").SetSeverity("info").SetSource("sync").SetMessage("Upstream CNY conversion source changed").SetPayload(map[string]any{"old_source": oldRateSource, "new_source": newRateSource}).SetOccurredAt(at)
		if runID > 0 {
			event.SetSyncRunID(runID)
		}
		if err := event.Exec(ctx); err != nil {
			return err
		}
	}

	if !balanceOK {
		return nil
	}
	threshold, err := upstreamBalanceThreshold(ctx, client)
	if err != nil {
		return err
	}
	return evaluateUpstreamBalanceIncident(ctx, client, config.ID, balance, threshold, at)
}

func evaluateUpstreamBalanceIncident(ctx context.Context, client *dbent.Client, configID int64, balance, threshold float64, at time.Time) error {
	incidentKey := "balance_low"
	incident, incidentErr := client.UpstreamIncident.Query().Where(dbupstreamincident.UpstreamConfigIDEQ(configID), dbupstreamincident.IncidentKeyEQ(incidentKey)).Only(ctx)
	if incidentErr != nil && !dbent.IsNotFound(incidentErr) {
		return incidentErr
	}
	if threshold > 0 && balance < threshold {
		if incident == nil {
			return client.UpstreamIncident.Create().SetUpstreamConfigID(configID).SetIncidentKey(incidentKey).SetIncidentType("balance_low").SetSeverity("warning").SetStatus("open").SetTitle("Upstream balance is low").SetDescription("Upstream balance fell below the configured CNY threshold").SetMetricValue(balance).SetThresholdValue(threshold).SetDetails(map[string]any{"currency": "CNY"}).SetFirstSeenAt(at).SetLastSeenAt(at).Exec(ctx)
		}
		return client.UpstreamIncident.UpdateOne(incident).SetStatus("open").SetMetricValue(balance).SetThresholdValue(threshold).SetLastSeenAt(at).AddOccurrenceCount(1).ClearResolvedAt().Exec(ctx)
	}
	if incident != nil && incident.Status == "open" {
		return client.UpstreamIncident.UpdateOne(incident).SetStatus("resolved").SetMetricValue(balance).SetLastSeenAt(at).SetResolvedAt(at).Exec(ctx)
	}
	return nil
}

func upstreamBalanceThreshold(ctx context.Context, client *dbent.Client) (float64, error) {
	row, err := client.Setting.Query().Where(func(s *entsql.Selector) {
		s.Where(entsql.EQ(s.C("key"), service.SettingKeyUpstreamBalanceLowThresholdCNY))
	}).Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return 0, nil
		}
		return 0, err
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(row.Value), 64)
	if err != nil {
		return 0, nil
	}
	return value, nil
}

func numberFromMap(values map[string]any, key string) (float64, bool) {
	if values == nil {
		return 0, false
	}
	switch value := values[key].(type) {
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case json.Number:
		parsed, err := value.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func integerFromMap(values map[string]any, key string) (int, bool) {
	value, ok := numberFromMap(values, key)
	maxInt := int(^uint(0) >> 1)
	minInt := -maxInt - 1
	if !ok || value != math.Trunc(value) || value < float64(minInt) || value > float64(maxInt) {
		return 0, false
	}
	return int(value), true
}

func roundPersistedBalanceAmount(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	return math.Round(value*1e10) / 1e10
}

func maxFloatValue(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func (r *upstreamConfigRepository) CleanupUpstreamOperationHistory(ctx context.Context, now time.Time) error {
	client := clientFromContext(ctx, r.client)
	if _, err := client.UpstreamSyncRun.Delete().Where(dbupstreamsyncrun.StartedAtLT(now.AddDate(0, 0, -30))).Exec(ctx); err != nil {
		return err
	}
	if _, err := client.UpstreamEvent.Delete().Where(dbupstreamevent.OccurredAtLT(now.AddDate(0, 0, -90))).Exec(ctx); err != nil {
		return err
	}
	if _, err := client.UpstreamBalanceSnapshot.Delete().Where(dbupstreambalancesnapshot.ObservedAtLT(now.AddDate(0, 0, -90))).Exec(ctx); err != nil {
		return err
	}
	if _, err := client.UpstreamKeyRateSnapshot.Delete().Where(dbupstreamkeyratesnapshot.ObservedAtLT(now.AddDate(0, 0, -90))).Exec(ctx); err != nil {
		return err
	}
	_, err := client.UpstreamIncident.Delete().Where(dbupstreamincident.StatusEQ("resolved"), dbupstreamincident.ResolvedAtNotNil(), dbupstreamincident.ResolvedAtLT(now.AddDate(0, 0, -90))).Exec(ctx)
	return err
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
