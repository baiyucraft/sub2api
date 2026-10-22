package service

import (
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

const cacheGuardDiagnosticCapacity = 4096

var cacheGuardDiagnosticProcessID = newCacheGuardID()

type cacheGuardDiagnosticIdentity struct {
	groupID, accountID, keyID int64
}
type cacheGuardDiagnosticCounter struct {
	epoch        uint64
	observedFrom time.Time
	observedAt   time.Time
	counts       map[string]uint64
}

// This is a bounded process-local diagnostic window, not billing or a durable
// request ledger. Epoch changes make capacity eviction and counter resets clear.
type cacheGuardDiagnostics struct {
	mu        sync.Mutex
	entries   map[cacheGuardDiagnosticIdentity]*cacheGuardDiagnosticCounter
	order     []cacheGuardDiagnosticIdentity
	next      int
	epoch     uint64
	evictions uint64
}

func (d *cacheGuardDiagnostics) record(event string, groupID, accountID, keyID int64) {
	id := cacheGuardDiagnosticIdentity{groupID, accountID, keyID}
	d.mu.Lock()
	now := time.Now().UTC()
	if d.entries == nil {
		d.entries = map[cacheGuardDiagnosticIdentity]*cacheGuardDiagnosticCounter{}
	}
	counter := d.entries[id]
	if counter == nil {
		if len(d.order) == cacheGuardDiagnosticCapacity {
			delete(d.entries, d.order[d.next])
			d.evictions++
			d.order[d.next] = id
			d.next = (d.next + 1) % cacheGuardDiagnosticCapacity
		} else {
			d.order = append(d.order, id)
		}
		d.epoch++
		counter = &cacheGuardDiagnosticCounter{epoch: d.epoch, observedFrom: now, counts: map[string]uint64{}}
		d.entries[id] = counter
	}
	counter.counts[event]++
	counter.observedAt = now
	count, epoch := counter.counts[event], counter.epoch
	observedFrom, evictions := counter.observedFrom, d.evictions
	var counts map[string]uint64
	anomaly := strings.HasPrefix(event, "skipped_") || event == "enqueue_failed" || event == "completion_queue_full"
	// Success identities can churn beyond the bounded window. Never turn their
	// repeated first occurrence after eviction into a per-request log stream.
	if (anomaly && count == 1) || count%1000 == 0 {
		counts = make(map[string]uint64, len(counter.counts))
		for event, count := range counter.counts {
			counts[event] = count
		}
	}
	d.mu.Unlock()
	if counts != nil {
		name, reason := "upstream_cache_guard_sampling", event
		if strings.HasPrefix(event, "skipped_") {
			name, reason = "upstream_cache_guard_admission_skipped", strings.TrimPrefix(event, "skipped_")
		}
		slog.Info(name, "event", event, "reason", reason, "count", count, "count_scope", "process_account_window", "counter_epoch", epoch,
			"process_id", cacheGuardDiagnosticProcessID, "observed_from", observedFrom, "observed_at", now, "evictions", evictions,
			"group_id", groupID, "account_id", accountID, "upstream_key_id", keyID, "counts", counts)
	}
}

type UpstreamCacheGuardSamplingCounter struct {
	GroupID       int64             `json:"group_id"`
	AccountID     int64             `json:"account_id"`
	UpstreamKeyID int64             `json:"upstream_key_id"`
	CounterEpoch  uint64            `json:"counter_epoch"`
	ObservedFrom  time.Time         `json:"observed_from"`
	ObservedAt    time.Time         `json:"observed_at"`
	Counts        map[string]uint64 `json:"counts"`
}

type UpstreamCacheGuardSamplingSnapshot struct {
	GroupID             int64      `json:"group_id"`
	EvaluatedAt         time.Time  `json:"evaluated_at"`
	PolicyVersion       uint64     `json:"policy_version"`
	AccountPolicyEpoch  uint64     `json:"account_policy_epoch"`
	Consistent          bool       `json:"consistent"`
	PolicyRegistered    bool       `json:"policy_registered"`
	PolicyEpochMismatch bool       `json:"policy_epoch_mismatch"`
	AccountPublished    bool       `json:"account_published"`
	AccountLoadedAt     *time.Time `json:"account_loaded_at"`
	MetadataLoadedAt    *time.Time `json:"metadata_loaded_at"`
	Fresh               bool       `json:"fresh"`
	Available           bool       `json:"available"`
	HasPendingGap       bool       `json:"has_account_gap"`
	HasSamplingFailure  bool       `json:"has_sampling_failure"`
	ScopeSelected       bool       `json:"scope_selected"`
	Reason              string     `json:"reason"`
}

type UpstreamCacheGuardSamplingDiagnostics struct {
	ProcessID       string                               `json:"process_id"`
	AccountID       int64                                `json:"account_id"`
	UpstreamKeyID   int64                                `json:"upstream_key_id"`
	RunnerInstalled bool                                 `json:"runner_installed"`
	RunnerClosed    bool                                 `json:"runner_closed"`
	Ready           bool                                 `json:"ready"`
	ObservedAt      time.Time                            `json:"observed_at"`
	CountScope      string                               `json:"count_scope"`
	CompleteHistory bool                                 `json:"complete_history"`
	Capacity        int                                  `json:"capacity"`
	Evictions       uint64                               `json:"evictions"`
	Counters        []UpstreamCacheGuardSamplingCounter  `json:"counters"`
	Snapshots       []UpstreamCacheGuardSamplingSnapshot `json:"snapshots"`
}

func (d *cacheGuardDiagnostics) snapshot(accountID, keyID int64) ([]UpstreamCacheGuardSamplingCounter, uint64) {
	d.mu.Lock()
	rows := make([]UpstreamCacheGuardSamplingCounter, 0)
	for id, counter := range d.entries {
		if id.accountID != accountID || id.keyID != keyID {
			continue
		}
		counts := make(map[string]uint64, len(counter.counts))
		for event, count := range counter.counts {
			counts[event] = count
		}
		rows = append(rows, UpstreamCacheGuardSamplingCounter{GroupID: id.groupID, AccountID: id.accountID,
			UpstreamKeyID: id.keyID, CounterEpoch: counter.epoch, ObservedFrom: counter.observedFrom, ObservedAt: counter.observedAt, Counts: counts})
	}
	evictions := d.evictions
	d.mu.Unlock()
	sort.Slice(rows, func(i, j int) bool { return rows[i].GroupID < rows[j].GroupID })
	return rows, evictions
}

// Process counters describe only retained observations since each identity's
// epoch. They are deliberately never presented as a complete billing window.
func GetUpstreamCacheGuardSamplingDiagnostics(account *Account) UpstreamCacheGuardSamplingDiagnostics {
	result := UpstreamCacheGuardSamplingDiagnostics{ProcessID: cacheGuardDiagnosticProcessID, ObservedAt: time.Now().UTC(),
		CountScope: "process_account_window", Capacity: cacheGuardDiagnosticCapacity,
		Counters: []UpstreamCacheGuardSamplingCounter{}, Snapshots: []UpstreamCacheGuardSamplingSnapshot{}}
	if account == nil {
		return result
	}
	result.AccountID, result.UpstreamKeyID = account.ID, accountUpstreamKeyID(account)
	r := currentUpstreamCacheGuardRunner()
	if r == nil {
		return result
	}
	result.RunnerInstalled, result.RunnerClosed = true, r.closed.Load()
	result.Counters, result.Evictions = r.diagnostics.snapshot(result.AccountID, result.UpstreamKeyID)
	result.Ready = !result.RunnerClosed && r.engine != nil
	if !result.Ready {
		return result
	}
	groups := map[int64]struct{}{}
	for _, id := range account.GroupIDs {
		groups[id] = struct{}{}
	}
	for _, group := range account.Groups {
		if group != nil {
			groups[group.ID] = struct{}{}
		}
	}
	for _, group := range account.AccountGroups {
		groups[group.GroupID] = struct{}{}
	}
	ids := make([]int64, 0, len(groups))
	for id := range groups {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, groupID := range ids {
		view := r.engine.CachedAdmissionReadView(upstreamcacheguard.Request{GroupID: groupID, AccountID: account.ID, UpstreamKeyID: result.UpstreamKeyID})
		d := view.Diagnostics
		reason := d.UnavailableReason
		if !view.Consistent {
			reason = "policy_changed_during_read"
		}
		result.Snapshots = append(result.Snapshots, UpstreamCacheGuardSamplingSnapshot{GroupID: groupID,
			EvaluatedAt: view.EvaluatedAt, PolicyVersion: d.PolicyVersion, AccountPolicyEpoch: d.AccountPolicyEpoch,
			Consistent: view.Consistent, PolicyRegistered: d.PolicyRegistered, PolicyEpochMismatch: d.PolicyEpochMismatch,
			AccountPublished: d.AccountPublished, AccountLoadedAt: cacheGuardTime(d.AccountLoadedAt), MetadataLoadedAt: cacheGuardTime(d.MetadataLoadedAt),
			Fresh: d.Fresh, Available: d.Available, HasPendingGap: d.HasPendingGap, HasSamplingFailure: d.HasSamplingFailure,
			ScopeSelected: r.protectionScopeSelected(groupID, account), Reason: reason})
	}
	return result
}
