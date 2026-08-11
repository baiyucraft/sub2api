package service

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

// UpstreamHealthStatus is provider-neutral. Suspended and recovering keys are
// temporarily excluded from business scheduling; probes remain allowed.
type UpstreamHealthStatus string

const (
	UpstreamHealthHealthy    UpstreamHealthStatus = "healthy"
	UpstreamHealthDegraded   UpstreamHealthStatus = "degraded"
	UpstreamHealthSuspended  UpstreamHealthStatus = "suspended"
	UpstreamHealthObserving  UpstreamHealthStatus = "observing"
	UpstreamHealthRecovering UpstreamHealthStatus = "recovering"
	UpstreamHealthDisabled   UpstreamHealthStatus = "disabled"

	upstreamHealthRecoverySamplesRequired = 3
	UpstreamHealthHistoryLimit            = 30
	UpstreamHealthListHistoryLimit        = 24
	UpstreamHealthTrafficSuccessInterval  = 5 * time.Minute
	UpstreamHealthObservationRetention    = 35 * 24 * time.Hour
)

type UpstreamHealthObservation struct {
	ID               int64                `json:"id,omitempty"`
	UpstreamConfigID int64                `json:"upstream_config_id,omitempty"`
	UpstreamKeyID    int64                `json:"upstream_key_id,omitempty"`
	AccountID        *int64               `json:"account_id,omitempty"`
	Platform         string               `json:"platform,omitempty"`
	Model            string               `json:"model,omitempty"`
	Protocol         string               `json:"protocol,omitempty"`
	ObservedAt       time.Time            `json:"observed_at"`
	State            UpstreamHealthStatus `json:"state"`
	Source           string               `json:"source"`
	Result           string               `json:"result"`
	Reason           string               `json:"reason,omitempty"`
	HTTPStatus       *int                 `json:"http_status,omitempty"`
	TTFTMs           *int64               `json:"ttft_ms,omitempty"`
	DurationMs       *int64               `json:"duration_ms,omitempty"`
	InputTokens      *int64               `json:"input_tokens,omitempty"`
	OutputTokens     *int64               `json:"output_tokens,omitempty"`
	OutputTPS        *float64             `json:"output_tps,omitempty"`
}

type UpstreamHealthHistoryReader interface {
	ListUpstreamHealthHistories(ctx context.Context, keyIDs []int64, limit int) (map[int64][]UpstreamHealthObservation, error)
}

type UpstreamHealthTrendPoint struct {
	Bucket          time.Time                    `json:"bucket"`
	State           UpstreamHealthStatus         `json:"state"`
	StateCounts     map[UpstreamHealthStatus]int `json:"state_counts"`
	TTFTP50Ms       *float64                     `json:"ttft_p50_ms,omitempty"`
	TTFTP95Ms       *float64                     `json:"ttft_p95_ms,omitempty"`
	DurationAvgMs   *float64                     `json:"duration_avg_ms,omitempty"`
	SampleCount     int                          `json:"sample_count"`
	TTFTSampleCount int                          `json:"ttft_sample_count"`
	PrimarySource   string                       `json:"primary_source,omitempty"`
	LatestReason    string                       `json:"latest_reason,omitempty"`
	LatestResult    string                       `json:"latest_result,omitempty"`
}

type UpstreamHealthTrend struct {
	KeyID         int64                      `json:"key_id"`
	Range         string                     `json:"range"`
	StartAt       time.Time                  `json:"start_at"`
	EndAt         time.Time                  `json:"end_at"`
	BucketSeconds int64                      `json:"bucket_seconds"`
	Points        []UpstreamHealthTrendPoint `json:"points"`
}

func UpstreamHealthHistoryFromExtra(extra map[string]any, limit int) []UpstreamHealthObservation {
	if extra == nil {
		return nil
	}
	raw, ok := extra["health_history"]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var history []UpstreamHealthObservation
	if err := json.Unmarshal(encoded, &history); err != nil {
		return nil
	}
	valid := history[:0]
	for _, item := range history {
		if item.ObservedAt.IsZero() || !validUpstreamHealthStatus(item.State) {
			continue
		}
		item.ObservedAt = item.ObservedAt.UTC()
		item.Source = strings.TrimSpace(item.Source)
		item.Result = strings.TrimSpace(item.Result)
		item.Reason = strings.TrimSpace(item.Reason)
		valid = append(valid, item)
	}
	if limit > 0 && len(valid) > limit {
		valid = valid[len(valid)-limit:]
	}
	return append([]UpstreamHealthObservation(nil), valid...)
}

// AppendUpstreamHealthObservation mutates the already row-locked extra map.
// Traffic successes are durably sampled at most once every five minutes;
// failures, probes and administrator changes are always retained.
func AppendUpstreamHealthObservation(extra map[string]any, item UpstreamHealthObservation) {
	if extra == nil || item.ObservedAt.IsZero() || !validUpstreamHealthStatus(item.State) {
		return
	}
	item.ObservedAt = item.ObservedAt.UTC()
	item.Source = strings.TrimSpace(item.Source)
	item.Result = strings.TrimSpace(item.Result)
	item.Reason = strings.TrimSpace(item.Reason)
	history := UpstreamHealthHistoryFromExtra(extra, UpstreamHealthHistoryLimit)
	if item.Source == "traffic" && item.Reason == "traffic_succeeded" {
		for i := len(history) - 1; i >= 0; i-- {
			previous := history[i]
			if previous.Source != "traffic" || previous.Reason != "traffic_succeeded" {
				continue
			}
			if item.ObservedAt.Sub(previous.ObservedAt) < UpstreamHealthTrafficSuccessInterval {
				return
			}
			break
		}
	}
	history = append(history, item)
	if len(history) > UpstreamHealthHistoryLimit {
		history = history[len(history)-UpstreamHealthHistoryLimit:]
	}
	extra["health_history"] = history
}

func validUpstreamHealthStatus(status UpstreamHealthStatus) bool {
	switch status {
	case UpstreamHealthHealthy, UpstreamHealthDegraded, UpstreamHealthSuspended,
		UpstreamHealthObserving, UpstreamHealthRecovering, UpstreamHealthDisabled:
		return true
	default:
		return false
	}
}

type UpstreamHealthSnapshot struct {
	KeyID                   int64                `json:"key_id"`
	Status                  UpstreamHealthStatus `json:"status"`
	ObservationEnabled      bool                 `json:"observation_enabled"`
	Reason                  string               `json:"reason,omitempty"`
	LastProbeAt             *time.Time           `json:"last_probe_at,omitempty"`
	LastProbeStatus         string               `json:"last_probe_status,omitempty"`
	LastProbeTTFTMs         *int64               `json:"last_probe_ttft_ms,omitempty"`
	LastEvidenceAt          *time.Time           `json:"last_evidence_at,omitempty"`
	LastTrafficStatus       string               `json:"last_traffic_status,omitempty"`
	ConsecutiveFails        int                  `json:"consecutive_failures"`
	RecoverySamples         int                  `json:"recovery_samples"`
	RecoverySamplesRequired int                  `json:"recovery_samples_required"`
	UpdatedAt               time.Time            `json:"updated_at"`
}

type UpstreamHealthRegistry struct {
	mu    sync.RWMutex
	items map[int64]UpstreamHealthSnapshot
}

// UpstreamHealthTransition is the atomic before/after view of one health
// mutation. Callers use it to persist the snapshot and append a state-change
// event without taking a second, racy registry snapshot.
type UpstreamHealthTransition struct {
	Previous UpstreamHealthSnapshot
	Current  UpstreamHealthSnapshot
}

func (t UpstreamHealthTransition) StateChanged() bool {
	return t.Previous.Status != t.Current.Status ||
		t.Previous.ObservationEnabled != t.Current.ObservationEnabled
}

var globalUpstreamHealth = &UpstreamHealthRegistry{items: make(map[int64]UpstreamHealthSnapshot)}

func GlobalUpstreamHealthRegistry() *UpstreamHealthRegistry { return globalUpstreamHealth }

func defaultUpstreamHealthSnapshot(keyID int64) UpstreamHealthSnapshot {
	return UpstreamHealthSnapshot{
		KeyID:                   keyID,
		Status:                  UpstreamHealthObserving,
		ObservationEnabled:      true,
		RecoverySamplesRequired: upstreamHealthRecoverySamplesRequired,
	}
}

func normalizeUpstreamHealthSnapshot(item UpstreamHealthSnapshot) UpstreamHealthSnapshot {
	if !validUpstreamHealthStatus(item.Status) {
		item.Status = UpstreamHealthObserving
	}
	if item.RecoverySamplesRequired <= 0 {
		item.RecoverySamplesRequired = upstreamHealthRecoverySamplesRequired
	}
	if !item.ObservationEnabled {
		item.Status = UpstreamHealthDisabled
		item.RecoverySamples = 0
	}
	return item
}

func (r *UpstreamHealthRegistry) Snapshot(keyID int64) UpstreamHealthSnapshot {
	if r == nil || keyID <= 0 {
		return defaultUpstreamHealthSnapshot(keyID)
	}
	r.mu.RLock()
	item, ok := r.items[keyID]
	r.mu.RUnlock()
	if !ok {
		return defaultUpstreamHealthSnapshot(keyID)
	}
	return item
}

// Hydrate restores a durable snapshot without treating the read as evidence.
func (r *UpstreamHealthRegistry) Hydrate(item UpstreamHealthSnapshot) UpstreamHealthSnapshot {
	if r == nil || item.KeyID <= 0 {
		return normalizeUpstreamHealthSnapshot(item)
	}
	item = normalizeUpstreamHealthSnapshot(item)
	r.mu.Lock()
	r.items[item.KeyID] = item
	r.mu.Unlock()
	return item
}

func (r *UpstreamHealthRegistry) SetObservationTransition(keyID int64, enabled bool, now time.Time) UpstreamHealthTransition {
	if now.IsZero() {
		now = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[keyID]
	if !ok {
		item = defaultUpstreamHealthSnapshot(keyID)
	}
	previous := item
	item.KeyID = keyID
	item.ObservationEnabled = enabled
	item.UpdatedAt = now
	item.ConsecutiveFails = 0
	item.RecoverySamples = 0
	if !enabled {
		item.Status = UpstreamHealthDisabled
		item.Reason = "observation_disabled"
	} else if item.Status == UpstreamHealthDisabled {
		item.Status = UpstreamHealthObserving
		item.Reason = "observation_enabled"
	}
	r.items[keyID] = normalizeUpstreamHealthSnapshot(item)
	return UpstreamHealthTransition{Previous: previous, Current: r.items[keyID]}
}

func (r *UpstreamHealthRegistry) SetObservation(keyID int64, enabled bool, now time.Time) UpstreamHealthSnapshot {
	return r.SetObservationTransition(keyID, enabled, now).Current
}

func (r *UpstreamHealthRegistry) recordSuccessTransition(keyID int64, status, reason string, ttftMs *int64, now time.Time, probe bool) UpstreamHealthTransition {
	if now.IsZero() {
		now = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[keyID]
	if !ok {
		item = defaultUpstreamHealthSnapshot(keyID)
	}
	previous := item
	if !item.ObservationEnabled || item.Status == UpstreamHealthDisabled {
		if probe {
			item.LastProbeAt = &now
			item.LastProbeStatus = strings.TrimSpace(status)
			item.LastProbeTTFTMs = cloneUpstreamHealthInt64(ttftMs)
			item.Reason = strings.TrimSpace(reason)
			item.UpdatedAt = now
			r.items[keyID] = normalizeUpstreamHealthSnapshot(item)
		}
		return UpstreamHealthTransition{Previous: previous, Current: r.items[keyID]}
	}
	item.UpdatedAt = now
	item.Reason = strings.TrimSpace(reason)
	item.ConsecutiveFails = 0
	if probe {
		item.LastProbeAt = &now
		item.LastProbeStatus = strings.TrimSpace(status)
		item.LastProbeTTFTMs = cloneUpstreamHealthInt64(ttftMs)
	} else {
		item.LastEvidenceAt = &now
		item.LastTrafficStatus = strings.TrimSpace(status)
	}
	if item.Status == UpstreamHealthSuspended || item.Status == UpstreamHealthRecovering {
		item.Status = UpstreamHealthRecovering
		item.RecoverySamples++
		if item.RecoverySamples >= upstreamHealthRecoverySamplesRequired {
			item.Status = UpstreamHealthHealthy
			item.RecoverySamples = 0
			item.Reason = "recovered"
		}
	} else {
		item.Status = UpstreamHealthHealthy
		item.RecoverySamples = 0
	}
	item = normalizeUpstreamHealthSnapshot(item)
	r.items[keyID] = item
	return UpstreamHealthTransition{Previous: previous, Current: item}
}

func (r *UpstreamHealthRegistry) recordFailureTransition(keyID int64, status, reason string, ttftMs *int64, now time.Time, probe bool) UpstreamHealthTransition {
	if now.IsZero() {
		now = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[keyID]
	if !ok {
		item = defaultUpstreamHealthSnapshot(keyID)
	}
	previous := item
	if !item.ObservationEnabled || item.Status == UpstreamHealthDisabled {
		if probe {
			item.LastProbeAt = &now
			item.LastProbeStatus = strings.TrimSpace(status)
			item.LastProbeTTFTMs = cloneUpstreamHealthInt64(ttftMs)
			item.Reason = strings.TrimSpace(reason)
			item.UpdatedAt = now
			r.items[keyID] = normalizeUpstreamHealthSnapshot(item)
		}
		return UpstreamHealthTransition{Previous: previous, Current: r.items[keyID]}
	}
	item.UpdatedAt = now
	item.Reason = strings.TrimSpace(reason)
	item.RecoverySamples = 0
	if probe {
		item.LastProbeAt = &now
		item.LastProbeStatus = strings.TrimSpace(status)
		item.LastProbeTTFTMs = cloneUpstreamHealthInt64(ttftMs)
	} else {
		item.LastEvidenceAt = &now
		item.LastTrafficStatus = strings.TrimSpace(status)
	}
	if item.Reason == "capacity_limited" {
		// Provider-wide capacity signals are useful observations but do not prove
		// that this key is invalid. Keep the key degraded without accumulating
		// toward a global key suspension.
		item.ConsecutiveFails = 0
		item.Status = UpstreamHealthDegraded
	} else {
		item.ConsecutiveFails++
	}
	if status == "401" || status == "403" || item.ConsecutiveFails >= 3 {
		item.Status = UpstreamHealthSuspended
	} else if item.Status != UpstreamHealthDegraded {
		item.Status = UpstreamHealthDegraded
	}
	item = normalizeUpstreamHealthSnapshot(item)
	r.items[keyID] = item
	return UpstreamHealthTransition{Previous: previous, Current: item}
}

func (r *UpstreamHealthRegistry) RecordProbe(keyID int64, status, reason string, now time.Time) UpstreamHealthSnapshot {
	return r.RecordProbeTransition(keyID, status, reason, now).Current
}

func (r *UpstreamHealthRegistry) RecordProbeTransition(keyID int64, status, reason string, now time.Time) UpstreamHealthTransition {
	return r.RecordProbeWithTTFTTransition(keyID, status, reason, nil, now)
}

func (r *UpstreamHealthRegistry) RecordProbeWithTTFTTransition(keyID int64, status, reason string, ttftMs *int64, now time.Time) UpstreamHealthTransition {
	return r.recordSuccessTransition(keyID, status, reason, ttftMs, now, true)
}

func (r *UpstreamHealthRegistry) RecordProbeFailure(keyID int64, status, reason string, now time.Time) UpstreamHealthSnapshot {
	return r.RecordProbeFailureTransition(keyID, status, reason, now).Current
}

func (r *UpstreamHealthRegistry) RecordProbeFailureTransition(keyID int64, status, reason string, now time.Time) UpstreamHealthTransition {
	return r.RecordProbeFailureWithTTFTTransition(keyID, status, reason, nil, now)
}

func (r *UpstreamHealthRegistry) RecordProbeFailureWithTTFTTransition(keyID int64, status, reason string, ttftMs *int64, now time.Time) UpstreamHealthTransition {
	return r.recordFailureTransition(keyID, status, reason, ttftMs, now, true)
}

func (r *UpstreamHealthRegistry) RecordTrafficSuccess(keyID int64, status, reason string, now time.Time) UpstreamHealthSnapshot {
	return r.RecordTrafficSuccessTransition(keyID, status, reason, now).Current
}

func (r *UpstreamHealthRegistry) RecordTrafficSuccessTransition(keyID int64, status, reason string, now time.Time) UpstreamHealthTransition {
	return r.recordSuccessTransition(keyID, status, reason, nil, now, false)
}

func (r *UpstreamHealthRegistry) RecordTrafficFailure(keyID int64, status, reason string, now time.Time) UpstreamHealthSnapshot {
	return r.RecordTrafficFailureTransition(keyID, status, reason, now).Current
}

func (r *UpstreamHealthRegistry) RecordTrafficFailureTransition(keyID int64, status, reason string, now time.Time) UpstreamHealthTransition {
	return r.recordFailureTransition(keyID, status, reason, nil, now, false)
}

func cloneUpstreamHealthInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// RecordFailure remains as a compatibility alias for probe failures.
func (r *UpstreamHealthRegistry) RecordFailure(keyID int64, status, reason string, now time.Time) UpstreamHealthSnapshot {
	return r.RecordProbeFailure(keyID, status, reason, now)
}

func (r *UpstreamHealthRegistry) Snapshots(keyIDs []int64) map[int64]UpstreamHealthSnapshot {
	out := make(map[int64]UpstreamHealthSnapshot, len(keyIDs))
	if r == nil {
		return out
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range keyIDs {
		if id <= 0 {
			continue
		}
		item, ok := r.items[id]
		if !ok {
			item = defaultUpstreamHealthSnapshot(id)
		}
		out[id] = item
	}
	return out
}

func (r *UpstreamHealthRegistry) ExcludedKeyIDs(keyIDs []int64) map[int64]struct{} {
	out := make(map[int64]struct{})
	for id, item := range r.Snapshots(keyIDs) {
		if item.Status == UpstreamHealthSuspended || item.Status == UpstreamHealthRecovering {
			out[id] = struct{}{}
		}
	}
	return out
}

// HasTemporaryExclusions is a cheap process-wide guard used before account
// hydration. Most requests run while no Key is suspended, so the scheduling
// wrapper preserves the original single repository read in that common case.
func (r *UpstreamHealthRegistry) HasTemporaryExclusions() bool {
	if r == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, item := range r.items {
		if item.Status == UpstreamHealthSuspended || item.Status == UpstreamHealthRecovering {
			return true
		}
	}
	return false
}

func SortUpstreamHealthSnapshots(items []UpstreamHealthSnapshot) {
	sort.Slice(items, func(i, j int) bool { return items[i].KeyID < items[j].KeyID })
}
