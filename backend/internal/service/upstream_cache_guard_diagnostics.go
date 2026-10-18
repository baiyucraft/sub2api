package service

import (
	"log/slog"
	"strings"
	"sync"
)

const cacheGuardDiagnosticCapacity = 4096

type cacheGuardDiagnosticIdentity struct {
	groupID, accountID, keyID int64
}
type cacheGuardDiagnosticCounter struct {
	epoch  uint64
	counts map[string]uint64
}

// This is a bounded process-local diagnostic window, not billing or a durable
// request ledger. Epoch changes make capacity eviction and counter resets clear.
type cacheGuardDiagnostics struct {
	mu      sync.Mutex
	entries map[cacheGuardDiagnosticIdentity]*cacheGuardDiagnosticCounter
	order   []cacheGuardDiagnosticIdentity
	next    int
	epoch   uint64
}

func (d *cacheGuardDiagnostics) record(event string, groupID, accountID, keyID int64) {
	id := cacheGuardDiagnosticIdentity{groupID, accountID, keyID}
	d.mu.Lock()
	if d.entries == nil {
		d.entries = map[cacheGuardDiagnosticIdentity]*cacheGuardDiagnosticCounter{}
	}
	counter := d.entries[id]
	if counter == nil {
		if len(d.order) == cacheGuardDiagnosticCapacity {
			delete(d.entries, d.order[d.next])
			d.order[d.next] = id
			d.next = (d.next + 1) % cacheGuardDiagnosticCapacity
		} else {
			d.order = append(d.order, id)
		}
		d.epoch++
		counter = &cacheGuardDiagnosticCounter{epoch: d.epoch, counts: map[string]uint64{}}
		d.entries[id] = counter
	}
	counter.counts[event]++
	count, epoch := counter.counts[event], counter.epoch
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
			"group_id", groupID, "account_id", accountID, "upstream_key_id", keyID, "counts", counts)
	}
}
