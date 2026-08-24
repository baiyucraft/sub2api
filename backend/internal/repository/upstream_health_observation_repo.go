package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbupstreamhealthobservation "github.com/Wei-Shaw/sub2api/ent/upstreamhealthobservation"
	dbupstreamkey "github.com/Wei-Shaw/sub2api/ent/upstreamkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func persistUpstreamHealthObservation(ctx context.Context, client *dbent.Client, keyID, configID int64, item *service.UpstreamHealthObservation) error {
	if item == nil || keyID <= 0 || item.ObservedAt.IsZero() {
		return nil
	}
	if item.UpstreamKeyID <= 0 {
		item.UpstreamKeyID = keyID
	}
	if item.UpstreamConfigID <= 0 {
		item.UpstreamConfigID = configID
	}
	source := strings.ToLower(strings.TrimSpace(item.Source))
	if source == "traffic" {
		source = "business"
	}
	if source == "" {
		source = "probe"
	}
	builder := client.UpstreamHealthObservation.Create().
		SetUpstreamConfigID(item.UpstreamConfigID).
		SetUpstreamKeyID(item.UpstreamKeyID).
		SetNillableAccountID(item.AccountID).
		SetPlatform(strings.TrimSpace(item.Platform)).
		SetModel(strings.TrimSpace(item.Model)).
		SetProtocol(strings.TrimSpace(item.Protocol)).
		SetSource(source).
		SetState(string(item.State)).
		SetResult(strings.TrimSpace(item.Result)).
		SetReason(strings.TrimSpace(item.Reason)).
		SetNillableHTTPStatus(item.HTTPStatus).
		SetNillableTtftMs(item.TTFTMs).
		SetNillableDurationMs(item.DurationMs).
		SetNillableInputTokens(item.InputTokens).
		SetNillableOutputTokens(item.OutputTokens).
		SetNillableOutputTps(item.OutputTPS).
		SetNillableConfidenceScore(item.ConfidenceScore).
		SetNillableConfidencePromptVersion(nonEmptyStringPtr(item.ConfidencePromptVersion)).
		SetNillableRequestedEffort(nonEmptyStringPtr(item.RequestedEffort)).
		SetNillableReasoningTokens(item.ReasoningTokens).
		SetNillableConfidenceStatus(nonEmptyStringPtr(item.ConfidenceStatus)).
		SetObservedAt(item.ObservedAt.UTC())
	if len(item.ConfidenceChecks) > 0 {
		builder.SetConfidenceChecks(item.ConfidenceChecks)
	}
	if err := builder.Exec(ctx); err != nil {
		return err
	}
	return nil
}

func nonEmptyStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func healthValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func upstreamHealthIntPtr(value int) *int { return &value }

const upstreamHealthObservationCleanupInterval = 24 * time.Hour

func (r *upstreamConfigRepository) claimUpstreamHealthObservationCleanup(now time.Time) (bool, int64) {
	if r == nil {
		return false, 0
	}
	nowUnix := now.UTC().Unix()
	for {
		previous := r.healthObservationCleanupAtUnix.Load()
		if previous > 0 && nowUnix-previous < int64(upstreamHealthObservationCleanupInterval/time.Second) {
			return false, previous
		}
		if r.healthObservationCleanupAtUnix.CompareAndSwap(previous, nowUnix) {
			return true, previous
		}
	}
}

func cleanupExpiredUpstreamHealthObservations(ctx context.Context, client *dbent.Client, now time.Time) error {
	_, err := client.UpstreamHealthObservation.Delete().
		Where(dbupstreamhealthobservation.ObservedAtLT(now.UTC().Add(-service.UpstreamHealthObservationRetention))).
		Exec(ctx)
	return err
}

func (r *upstreamConfigRepository) listPersistedUpstreamHealthHistories(ctx context.Context, keyIDs []int64, limit int) (map[int64][]service.UpstreamHealthObservation, error) {
	out := make(map[int64][]service.UpstreamHealthObservation, len(keyIDs))
	driver, ok := r.client.Driver().(*entsql.Driver)
	if ok && driver.Dialect() == dialect.Postgres {
		rows, err := driver.DB().QueryContext(ctx, `
SELECT id, upstream_config_id, upstream_key_id, account_id, platform, model, protocol,
       source, state, result, reason, http_status, ttft_ms, duration_ms,
       input_tokens, output_tokens, output_tps, confidence_score, confidence_prompt_version,
       requested_effort, reasoning_tokens, confidence_checks, confidence_status, observed_at
FROM (
    SELECT o.*, ROW_NUMBER() OVER (
        PARTITION BY upstream_key_id ORDER BY observed_at DESC, id DESC
    ) AS row_num
    FROM upstream_health_observations o
    WHERE upstream_key_id = ANY($1)
) ranked
WHERE row_num <= $2
ORDER BY upstream_key_id ASC, observed_at ASC, id ASC`, pq.Array(keyIDs), limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			item, err := scanUpstreamHealthObservation(rows)
			if err != nil {
				return nil, err
			}
			out[item.UpstreamKeyID] = append(out[item.UpstreamKeyID], item)
		}
		return out, rows.Err()
	}

	// SQLite-backed repository tests and non-Postgres tooling use a small,
	// portable fallback. Production always takes the window-query path above.
	for _, keyID := range keyIDs {
		rows, err := r.client.UpstreamHealthObservation.Query().
			Where(dbupstreamhealthobservation.UpstreamKeyIDEQ(keyID)).
			Order(dbent.Desc(dbupstreamhealthobservation.FieldObservedAt), dbent.Desc(dbupstreamhealthobservation.FieldID)).
			Limit(limit).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for i := len(rows) - 1; i >= 0; i-- {
			out[keyID] = append(out[keyID], upstreamHealthObservationEntityToService(rows[i]))
		}
	}
	return out, nil
}

type sqlRowScanner interface {
	Scan(dest ...any) error
}

func scanUpstreamHealthObservation(row sqlRowScanner) (service.UpstreamHealthObservation, error) {
	var item service.UpstreamHealthObservation
	var state string
	var accountID, ttft, duration, inputTokens, outputTokens sql.NullInt64
	var httpStatus sql.NullInt64
	var outputTPS sql.NullFloat64
	var confidenceScore, reasoningTokens sql.NullInt64
	var promptVersion, requestedEffort, confidenceStatus sql.NullString
	var confidenceChecks []byte
	err := row.Scan(
		&item.ID, &item.UpstreamConfigID, &item.UpstreamKeyID, &accountID,
		&item.Platform, &item.Model, &item.Protocol, &item.Source, &state,
		&item.Result, &item.Reason, &httpStatus, &ttft, &duration,
		&inputTokens, &outputTokens, &outputTPS, &confidenceScore, &promptVersion,
		&requestedEffort, &reasoningTokens, &confidenceChecks, &confidenceStatus, &item.ObservedAt,
	)
	if err != nil {
		return item, err
	}
	item.State = service.UpstreamHealthStatus(state)
	if accountID.Valid {
		item.AccountID = upstreamHealthInt64Ptr(accountID.Int64)
	}
	if httpStatus.Valid {
		value := int(httpStatus.Int64)
		item.HTTPStatus = &value
	}
	if ttft.Valid {
		item.TTFTMs = upstreamHealthInt64Ptr(ttft.Int64)
	}
	if duration.Valid {
		item.DurationMs = upstreamHealthInt64Ptr(duration.Int64)
	}
	if inputTokens.Valid {
		item.InputTokens = upstreamHealthInt64Ptr(inputTokens.Int64)
	}
	if outputTokens.Valid {
		item.OutputTokens = upstreamHealthInt64Ptr(outputTokens.Int64)
	}
	if outputTPS.Valid {
		item.OutputTPS = floatPtr(outputTPS.Float64)
	}
	if confidenceScore.Valid {
		item.ConfidenceScore = upstreamHealthIntPtr(int(confidenceScore.Int64))
	}
	if promptVersion.Valid {
		item.ConfidencePromptVersion = promptVersion.String
	}
	if requestedEffort.Valid {
		item.RequestedEffort = requestedEffort.String
	}
	if reasoningTokens.Valid {
		item.ReasoningTokens = upstreamHealthInt64Ptr(reasoningTokens.Int64)
	}
	if len(confidenceChecks) > 0 {
		_ = json.Unmarshal(confidenceChecks, &item.ConfidenceChecks)
	}
	if confidenceStatus.Valid {
		item.ConfidenceStatus = confidenceStatus.String
	}
	item.ObservedAt = item.ObservedAt.UTC()
	return item, nil
}

func upstreamHealthObservationEntityToService(row *dbent.UpstreamHealthObservation) service.UpstreamHealthObservation {
	return service.UpstreamHealthObservation{
		ID: row.ID, UpstreamConfigID: row.UpstreamConfigID, UpstreamKeyID: row.UpstreamKeyID,
		AccountID: row.AccountID, Platform: row.Platform, Model: row.Model, Protocol: row.Protocol,
		Source: row.Source, State: service.UpstreamHealthStatus(row.State), Result: row.Result, Reason: row.Reason,
		HTTPStatus: row.HTTPStatus, TTFTMs: row.TtftMs, DurationMs: row.DurationMs,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens, OutputTPS: row.OutputTps,
		ConfidenceScore: row.ConfidenceScore, ConfidencePromptVersion: healthValueOrEmpty(row.ConfidencePromptVersion), RequestedEffort: healthValueOrEmpty(row.RequestedEffort), ReasoningTokens: row.ReasoningTokens, ConfidenceChecks: row.ConfidenceChecks, ConfidenceStatus: healthValueOrEmpty(row.ConfidenceStatus),
		ObservedAt: row.ObservedAt.UTC(),
	}
}

func (r *upstreamConfigRepository) GetUpstreamHealthConfidence(ctx context.Context, keyID int64) (service.UpstreamHealthConfidenceSummary, error) {
	var summary service.UpstreamHealthConfidenceSummary
	now := time.Now().UTC()
	rows, err := r.client.UpstreamHealthObservation.Query().Where(
		dbupstreamhealthobservation.UpstreamKeyIDEQ(keyID),
		dbupstreamhealthobservation.PlatformEQ(service.PlatformOpenAI),
		dbupstreamhealthobservation.SourceEQ("probe"),
		dbupstreamhealthobservation.ConfidencePromptVersionEQ(service.UpstreamConfidencePromptVersion),
		dbupstreamhealthobservation.RequestedEffortEQ(service.UpstreamConfidenceDefaultEffort),
		dbupstreamhealthobservation.ObservedAtGTE(now.Add(-7*24*time.Hour)),
	).Order(dbent.Desc(dbupstreamhealthobservation.FieldObservedAt), dbent.Desc(dbupstreamhealthobservation.FieldID)).All(ctx)
	if err != nil {
		return summary, err
	}
	return aggregateUpstreamHealthConfidence(now, rows), nil
}

func aggregateUpstreamHealthConfidence(now time.Time, rows []*dbent.UpstreamHealthObservation) service.UpstreamHealthConfidenceSummary {
	summary := service.UpstreamHealthConfidenceSummary{
		Status: "data_insufficient", RequestedEffort: service.UpstreamConfidenceDefaultEffort,
		PromptVersion: service.UpstreamConfidencePromptVersion,
	}
	var success24, success7, mixed7, n24, n7 int
	for _, row := range rows {
		valid := row.ConfidenceChecks["valid_completed"]
		if valid <= 0 {
			continue
		}
		success := row.ConfidenceChecks["current_success"]
		age := now.Sub(row.ObservedAt)
		if age <= 7*24*time.Hour {
			success7 += success
			n7 += valid
			mixed7 += row.ConfidenceChecks["mixed"]
		}
		if age <= 24*time.Hour {
			success24 += success
			n24 += valid
		}
		if summary.LastScore == nil {
			summary.LastScore = row.ConfidenceScore
			t := row.ObservedAt.UTC()
			summary.LastProbeAt = &t
			summary.Status = healthValueOrEmpty(row.ConfidenceStatus)
			summary.RequestedEffort = healthValueOrEmpty(row.RequestedEffort)
			summary.ReasoningTokens = row.ReasoningTokens
			summary.Breakdown = row.ConfidenceChecks
			summary.PromptVersion = healthValueOrEmpty(row.ConfidencePromptVersion)
		}
	}
	if n24 > 0 {
		v := float64(success24) / float64(n24) * 100
		summary.Score24h = &v
	}
	if n7 > 0 {
		v := float64(success7) / float64(n7) * 100
		summary.Score7d = &v
	}
	summary.SampleCount24h, summary.SampleCount7d = n24, n7
	switch {
	case n7 == 0:
		summary.Status = "data_insufficient"
	case mixed7 > 0:
		summary.Status = "mixed"
	case success7 > 0:
		summary.Status = "current_success"
	default:
		summary.Status = "unsuccessful"
	}
	return summary
}

func (r *upstreamConfigRepository) GetUpstreamHealthTrend(ctx context.Context, keyID int64, rangeName string, now time.Time) (*service.UpstreamHealthTrend, error) {
	rangeName, window, _, err := service.NormalizeUpstreamHealthTrendRange(rangeName)
	if err != nil {
		return nil, err
	}
	exists, err := r.client.UpstreamKey.Query().Where(dbupstreamkey.IDEQ(keyID)).Exist(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, service.ErrUpstreamKeyNotFound
	}
	now = now.UTC()
	start := now.Add(-window)
	rows, err := r.client.UpstreamHealthObservation.Query().
		Where(
			dbupstreamhealthobservation.UpstreamKeyIDEQ(keyID),
			dbupstreamhealthobservation.ObservedAtGTE(start),
			dbupstreamhealthobservation.ObservedAtLTE(now),
		).
		Order(dbent.Asc(dbupstreamhealthobservation.FieldObservedAt), dbent.Asc(dbupstreamhealthobservation.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]service.UpstreamHealthObservation, 0, len(rows))
	for _, row := range rows {
		items = append(items, upstreamHealthObservationEntityToService(row))
	}
	return aggregateUpstreamHealthTrend(keyID, rangeName, now, items)
}

func aggregateUpstreamHealthTrend(keyID int64, rangeName string, now time.Time, items []service.UpstreamHealthObservation) (*service.UpstreamHealthTrend, error) {
	rangeName, window, bucketSize, err := service.NormalizeUpstreamHealthTrendRange(rangeName)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	start := now.Add(-window)
	type bucketValues struct {
		point      service.UpstreamHealthTrendPoint
		ttft       []float64
		durations  []float64
		sources    map[string]int
		latestTime time.Time
	}
	buckets := make(map[time.Time]*bucketValues)
	for _, item := range items {
		if item.ObservedAt.Before(start) || item.ObservedAt.After(now) {
			continue
		}
		bucket := time.Unix((item.ObservedAt.Unix()/int64(bucketSize.Seconds()))*int64(bucketSize.Seconds()), 0).UTC()
		values := buckets[bucket]
		if values == nil {
			values = &bucketValues{point: service.UpstreamHealthTrendPoint{Bucket: bucket, StateCounts: map[service.UpstreamHealthStatus]int{}}, sources: map[string]int{}}
			buckets[bucket] = values
		}
		values.point.SampleCount++
		values.point.StateCounts[item.State]++
		if upstreamHealthStateSeverity(item.State) > upstreamHealthStateSeverity(values.point.State) {
			values.point.State = item.State
		}
		if upstreamHealthObservationHasValidTTFT(item) {
			values.ttft = append(values.ttft, float64(*item.TTFTMs))
		}
		if item.DurationMs != nil && *item.DurationMs >= 0 {
			values.durations = append(values.durations, float64(*item.DurationMs))
		}
		values.sources[item.Source]++
		if !item.ObservedAt.Before(values.latestTime) {
			values.latestTime = item.ObservedAt
			values.point.LatestReason = item.Reason
			values.point.LatestResult = item.Result
		}
	}
	trend := &service.UpstreamHealthTrend{KeyID: keyID, Range: rangeName, StartAt: start, EndAt: now, BucketSeconds: int64(bucketSize.Seconds()), Points: []service.UpstreamHealthTrendPoint{}}
	ordered := make([]time.Time, 0, len(buckets))
	for bucket := range buckets {
		ordered = append(ordered, bucket)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Before(ordered[j]) })
	for _, bucket := range ordered {
		values := buckets[bucket]
		values.point.TTFTSampleCount = len(values.ttft)
		values.point.TTFTP50Ms = percentile(values.ttft, 0.50)
		values.point.TTFTP95Ms = percentile(values.ttft, 0.95)
		values.point.DurationAvgMs = average(values.durations)
		values.point.PrimarySource = mostFrequentSource(values.sources)
		trend.Points = append(trend.Points, values.point)
	}
	return trend, nil
}

func upstreamHealthObservationHasValidTTFT(item service.UpstreamHealthObservation) bool {
	if item.TTFTMs == nil || *item.TTFTMs < 0 {
		return false
	}
	result := strings.ToLower(strings.TrimSpace(item.Result))
	if result == "" || result == "success" || result == "probe_succeeded" || result == "completed" {
		return true
	}
	return false
}

func upstreamHealthStateSeverity(state service.UpstreamHealthStatus) int {
	switch state {
	case service.UpstreamHealthSuspended:
		return 6
	case service.UpstreamHealthDegraded:
		return 5
	case service.UpstreamHealthRecovering:
		return 4
	case service.UpstreamHealthObserving:
		return 3
	case service.UpstreamHealthDisabled:
		return 2
	case service.UpstreamHealthHealthy:
		return 1
	default:
		return 0
	}
}

func percentile(values []float64, quantile float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return floatPtr(sorted[0])
	}
	position := quantile * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	value := sorted[lower]
	if upper != lower {
		value += (sorted[upper] - sorted[lower]) * (position - float64(lower))
	}
	return floatPtr(value)
}

func average(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return floatPtr(sum / float64(len(values)))
}

func mostFrequentSource(counts map[string]int) string {
	best, bestCount := "", 0
	for source, count := range counts {
		if count > bestCount || (count == bestCount && source < best) {
			best, bestCount = source, count
		}
	}
	return best
}

func upstreamHealthInt64Ptr(value int64) *int64 { return &value }
