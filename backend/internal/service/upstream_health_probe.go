package service

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	defaultUpstreamHealthProbeInterval    = 30 * time.Second
	defaultUpstreamHealthProbeBudget      = 20
	defaultUpstreamHealthProbeConcurrency = 4
)

// UpstreamHealthProbeProvider is the small seam used by the background runner.
// It intentionally does not depend on account scheduling state: active probes
// are allowed to run even when a derived account is paused or unschedulable.
type UpstreamHealthProbeProvider interface {
	ListDueHealthProbeKeyIDs(ctx context.Context, now time.Time, limit int) ([]int64, error)
	ProbeKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error)
}

// UpstreamHealthProbeRunner runs bounded, best-effort probes. A runner is
// safe to start more than once; only the first Start call creates the loop.
type UpstreamHealthProbeRunner struct {
	provider    UpstreamHealthProbeProvider
	interval    time.Duration
	budget      int
	concurrency int
	startOnce   sync.Once
	stopOnce    sync.Once
	cancel      context.CancelFunc
	probeGroup  singleflight.Group
}

func NewUpstreamHealthProbeRunner(provider UpstreamHealthProbeProvider, interval time.Duration, budget, concurrency int) *UpstreamHealthProbeRunner {
	if interval <= 0 {
		interval = defaultUpstreamHealthProbeInterval
	}
	if budget <= 0 {
		budget = defaultUpstreamHealthProbeBudget
	}
	if concurrency <= 0 {
		concurrency = defaultUpstreamHealthProbeConcurrency
	}
	if concurrency > budget {
		concurrency = budget
	}
	return &UpstreamHealthProbeRunner{provider: provider, interval: interval, budget: budget, concurrency: concurrency}
}

func (r *UpstreamHealthProbeRunner) Start(ctx context.Context) {
	if r == nil || r.provider == nil {
		return
	}
	r.startOnce.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		runCtx, cancel := context.WithCancel(ctx)
		r.cancel = cancel
		go r.loop(runCtx)
	})
}

func (r *UpstreamHealthProbeRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
	})
}

func (r *UpstreamHealthProbeRunner) loop(ctx context.Context) {
	if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
		// A failed cycle is intentionally not retried immediately; the next
		// ticker interval provides the fixed probe budget and natural backoff.
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.RunOnce(ctx)
		}
	}
}

func (r *UpstreamHealthProbeRunner) RunOnce(ctx context.Context) error {
	if r == nil || r.provider == nil {
		return nil
	}
	ids, err := r.provider.ListDueHealthProbeKeyIDs(ctx, time.Now().UTC(), r.budget)
	if err != nil {
		return err
	}
	if len(ids) > r.budget {
		ids = ids[:r.budget]
	}
	if len(ids) == 0 {
		return nil
	}
	workers := r.concurrency
	if workers > len(ids) {
		workers = len(ids)
	}
	jobs := make(chan int64)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id, ok := <-jobs:
					if !ok {
						return
					}
					if _, probeErr := r.ProbeKey(ctx, id); probeErr != nil {
						errMu.Lock()
						if firstErr == nil {
							firstErr = probeErr
						}
						errMu.Unlock()
					}
				}
			}
		}()
	}
sendJobs:
	for _, id := range ids {
		select {
		case <-ctx.Done():
			break sendJobs
		case jobs <- id:
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

// ProbeKey delegates through the provider. The service provider already
// singleflights requests; the runner remains a transparent seam for future
// providers and tests.
func (r *UpstreamHealthProbeRunner) ProbeKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	result := r.probeGroup.DoChan(strconv.FormatInt(keyID, 10), func() (any, error) {
		return r.provider.ProbeKey(ctx, keyID)
	})
	select {
	case output := <-result:
		if output.Err != nil {
			return UpstreamHealthSnapshot{}, output.Err
		}
		item, ok := output.Val.(UpstreamHealthSnapshot)
		if !ok {
			return UpstreamHealthSnapshot{}, fmt.Errorf("invalid upstream health probe result")
		}
		return item, nil
	case <-ctx.Done():
		return UpstreamHealthSnapshot{}, ctx.Err()
	}
}

// ListDueHealthProbeKeyIDs returns active, observing keys whose last probe is
// older than the current configured freshness window. It is deterministic for
// a given settings snapshot.
func (s *UpstreamConfigService) ListDueHealthProbeKeyIDs(ctx context.Context, now time.Time, limit int) ([]int64, error) {
	if limit <= 0 {
		limit = defaultUpstreamHealthProbeBudget
	}
	repo, ok := s.repo.(upstreamKeyHealthRepository)
	if !ok {
		return nil, nil
	}
	keys, err := repo.ListAllKeysForHealth(ctx)
	if err != nil {
		return nil, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	cutoff := now.Add(-s.effectiveHealthProbeInterval(ctx))
	ids := make([]int64, 0, len(keys))
	for _, key := range keys {
		if key.ID <= 0 || !upstreamKeyIsActive(&key) {
			continue
		}
		item := GlobalUpstreamHealthRegistry().Snapshot(key.ID)
		// The persisted column is authoritative even when the in-memory
		// registry was rebuilt after a restart or a key was restored.
		if key.ObservationEnabledKnown {
			applyPersistedObservationPreference(&item, key)
			item = normalizeUpstreamHealthSnapshot(item)
		}
		if !item.ObservationEnabled || item.Status == UpstreamHealthDisabled {
			continue
		}
		if item.LastProbeAt != nil && item.LastProbeAt.After(cutoff) {
			continue
		}
		ids = append(ids, key.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (s *UpstreamConfigService) effectiveHealthProbeInterval(ctx context.Context) time.Duration {
	seconds := int64(DefaultUpstreamProbeIntervalSeconds)
	if s != nil {
		if cached := s.healthProbeIntervalSeconds.Load(); cached >= MinUpstreamProbeIntervalSeconds && cached <= MaxUpstreamProbeIntervalSeconds {
			seconds = cached
		}
		if s.settingService != nil {
			if configured, err := s.settingService.GetUpstreamProbeIntervalSeconds(ctx); err == nil {
				seconds = int64(configured)
				s.healthProbeIntervalSeconds.Store(seconds)
			}
		}
	}
	return time.Duration(seconds) * time.Second
}
