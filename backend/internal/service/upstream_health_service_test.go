package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type healthEventCaptureRepo struct {
	UpstreamConfigRepository
	mu      sync.Mutex
	patches []map[string]any
	events  []*UpstreamEvent
	err     error
	keys    []UpstreamKey
}

func (r *healthEventCaptureRepo) ListAllKeysForHealth(context.Context) ([]UpstreamKey, error) {
	return append([]UpstreamKey(nil), r.keys...), nil
}

func (r *healthEventCaptureRepo) PatchKeyHealth(context.Context, int64, map[string]any) error {
	return r.err
}

func (r *healthEventCaptureRepo) PatchKeyHealthWithEvent(_ context.Context, _ int64, health map[string]any, event *UpstreamEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.patches = append(r.patches, health)
	if event != nil {
		cp := *event
		cp.Payload = make(map[string]any, len(event.Payload))
		for key, value := range event.Payload {
			cp.Payload[key] = value
		}
		r.events = append(r.events, &cp)
	}
	return nil
}

func TestUpstreamConfigServiceObservationWritesStateChangeEvent(t *testing.T) {
	const keyID = 92001
	GlobalUpstreamHealthRegistry().Hydrate(defaultUpstreamHealthSnapshot(keyID))
	repo := &healthEventCaptureRepo{}
	svc := &UpstreamConfigService{repo: repo}

	item, err := svc.SetKeyObservation(context.Background(), keyID, false)
	require.NoError(t, err)
	require.Equal(t, UpstreamHealthDisabled, item.Status)
	require.Len(t, repo.events, 1)
	require.Equal(t, "key_health_state_changed", repo.events[0].Type)
	require.Equal(t, "warning", repo.events[0].Severity)
	require.Equal(t, "observing", repo.events[0].Payload["previous_status"])
	require.Equal(t, "disabled", repo.events[0].Payload["current_status"])

	_, err = svc.SetKeyObservation(context.Background(), keyID, false)
	require.NoError(t, err)
	require.Len(t, repo.events, 1, "same-state evidence must not append another transition event")
}

func TestUpstreamConfigServiceHealthPersistenceFailureRollsBackRegistry(t *testing.T) {
	const keyID = 92002
	previous := defaultUpstreamHealthSnapshot(keyID)
	previous.Status = UpstreamHealthHealthy
	previous.UpdatedAt = time.Date(2026, 8, 10, 7, 0, 0, 0, time.UTC)
	GlobalUpstreamHealthRegistry().Hydrate(previous)
	repo := &healthEventCaptureRepo{err: errors.New("write failed")}
	svc := &UpstreamConfigService{repo: repo}

	_, err := svc.SetKeyObservation(context.Background(), keyID, false)
	require.Error(t, err)
	require.Equal(t, previous, GlobalUpstreamHealthRegistry().Snapshot(keyID))
}

type keyEventsCaptureRepo struct {
	UpstreamConfigRepository
	configID int64
	keyID    int64
	limit    int
	offset   int
	events   []UpstreamEvent
	total    int64
}

func (r *keyEventsCaptureRepo) ListUpstreamEventsByKeyID(_ context.Context, configID, keyID int64, limit, offset int) ([]UpstreamEvent, int64, error) {
	r.configID, r.keyID, r.limit, r.offset = configID, keyID, limit, offset
	return r.events, r.total, nil
}

func TestUpstreamConfigServiceListKeyEventsUsesKeyScopedPagination(t *testing.T) {
	repo := &keyEventsCaptureRepo{events: []UpstreamEvent{{
		ID:      1,
		Message: `probe failed api_key=sk-event-secret`,
		Payload: map[string]any{
			"secret": "removed",
			"nested": map[string]any{"authorization": "Bearer sensitive", "status": "401"},
		},
	}}, total: 17}
	svc := &UpstreamConfigService{repo: repo}

	items, total, err := svc.ListKeyEvents(context.Background(), 8, 9, 999, -4)
	require.NoError(t, err)
	require.Equal(t, int64(17), total)
	require.Equal(t, int64(8), repo.configID)
	require.Equal(t, int64(9), repo.keyID)
	require.Equal(t, 200, repo.limit)
	require.Zero(t, repo.offset)
	require.Len(t, items, 1)
	require.NotContains(t, items[0].Message, "sk-event-secret")
	require.Equal(t, "***", items[0].Payload["secret"])
	nested := items[0].Payload["nested"].(map[string]any)
	require.Equal(t, "***", nested["authorization"])
	require.Equal(t, "401", nested["status"])
}

type healthRunnerFake struct {
	ids        []int64
	listCalls  atomic.Int32
	probeCalls atomic.Int32
	active     atomic.Int32
	maxActive  atomic.Int32
	delay      time.Duration
	started    chan struct{}
	release    chan struct{}
}

func (f *healthRunnerFake) ListDueHealthProbeKeyIDs(context.Context, time.Time, int) ([]int64, error) {
	f.listCalls.Add(1)
	return append([]int64(nil), f.ids...), nil
}

func (f *healthRunnerFake) ProbeKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	f.probeCalls.Add(1)
	active := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		current := f.maxActive.Load()
		if active <= current || f.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return UpstreamHealthSnapshot{}, ctx.Err()
		case <-f.release:
		}
	} else if f.delay > 0 {
		timer := time.NewTimer(f.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return UpstreamHealthSnapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
	return UpstreamHealthSnapshot{KeyID: keyID}, nil
}

func TestUpstreamHealthProbeRunnerHonorsBudgetAndConcurrency(t *testing.T) {
	fake := &healthRunnerFake{ids: []int64{1, 2, 3, 4, 5}, delay: 10 * time.Millisecond}
	runner := NewUpstreamHealthProbeRunner(fake, time.Minute, 3, 2)

	require.NoError(t, runner.RunOnce(context.Background()))
	require.Equal(t, int32(3), fake.probeCalls.Load())
	require.LessOrEqual(t, fake.maxActive.Load(), int32(2))
}

func TestUpstreamHealthProbeRunnerSingleflightsSameKey(t *testing.T) {
	fake := &healthRunnerFake{started: make(chan struct{}, 1), release: make(chan struct{})}
	runner := NewUpstreamHealthProbeRunner(fake, time.Minute, 2, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_, _ = runner.ProbeKey(context.Background(), 77)
		}()
	}
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	time.Sleep(10 * time.Millisecond)
	require.Equal(t, int32(1), fake.probeCalls.Load())
	close(fake.release)
	wg.Wait()
}

func TestUpstreamHealthProbeRunnerStopCancelsLoop(t *testing.T) {
	fake := &healthRunnerFake{ids: []int64{1}, started: make(chan struct{}, 1), release: make(chan struct{})}
	runner := NewUpstreamHealthProbeRunner(fake, time.Hour, 1, 1)
	runner.Start(context.Background())
	select {
	case <-fake.started:
	case <-time.After(time.Second):
		t.Fatal("probe loop did not start")
	}
	runner.Stop()
	require.Eventually(t, func() bool { return fake.active.Load() == 0 }, time.Second, 10*time.Millisecond)
}

func TestUpstreamTrafficEvidenceClassifiesAndPersistsTransitions(t *testing.T) {
	const keyID = 92020
	GlobalUpstreamHealthRegistry().Hydrate(defaultUpstreamHealthSnapshot(keyID))
	repo := &healthEventCaptureRepo{}
	svc := &UpstreamConfigService{repo: repo}
	account := &Account{ID: 7, UpstreamKeyID: int64Ptr(keyID)}

	svc.RecordUpstreamTrafficFailure(context.Background(), account, 429)
	item := GlobalUpstreamHealthRegistry().Snapshot(keyID)
	require.Equal(t, UpstreamHealthDegraded, item.Status)
	require.Equal(t, "capacity_limited", item.Reason)
	require.Zero(t, item.ConsecutiveFails)

	svc.RecordUpstreamTrafficFailure(context.Background(), account, 401)
	item = GlobalUpstreamHealthRegistry().Snapshot(keyID)
	require.Equal(t, UpstreamHealthSuspended, item.Status)
	require.Equal(t, "authentication_failed", item.Reason)

	for i := 0; i < upstreamHealthRecoverySamplesRequired; i++ {
		svc.RecordUpstreamTrafficSuccess(context.Background(), account, 200)
	}
	item = GlobalUpstreamHealthRegistry().Snapshot(keyID)
	require.Equal(t, UpstreamHealthHealthy, item.Status)
	require.Equal(t, "recovered", item.Reason)
	require.GreaterOrEqual(t, len(repo.events), 3)
}

func TestUpstreamTrafficEvidenceIgnoresRequestScopedErrorsAndOrdinaryAccounts(t *testing.T) {
	const keyID = 92021
	GlobalUpstreamHealthRegistry().Hydrate(defaultUpstreamHealthSnapshot(keyID))
	repo := &healthEventCaptureRepo{}
	svc := &UpstreamConfigService{repo: repo}

	svc.RecordUpstreamTrafficFailure(context.Background(), &Account{ID: 8, UpstreamKeyID: int64Ptr(keyID)}, 400)
	svc.RecordUpstreamTrafficFailure(context.Background(), &Account{ID: 9}, 500)
	require.Equal(t, defaultUpstreamHealthSnapshot(keyID), GlobalUpstreamHealthRegistry().Snapshot(keyID))
	require.Empty(t, repo.patches)
}

func TestUpstreamConfigServiceListDueHealthProbeKeys(t *testing.T) {
	now := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	active := StatusActive
	repo := &healthEventCaptureRepo{keys: []UpstreamKey{
		{ID: 92013, Status: active},
		{ID: 92011, Status: active},
		{ID: 92012, Status: "inactive"},
		{ID: 92014, Status: active},
	}}
	disabled := defaultUpstreamHealthSnapshot(92013)
	disabled.ObservationEnabled = false
	disabled.Status = UpstreamHealthDisabled
	GlobalUpstreamHealthRegistry().Hydrate(disabled)
	recent := defaultUpstreamHealthSnapshot(92014)
	recent.LastProbeAt = upstreamHealthTimePtr(now.Add(-30 * time.Second))
	GlobalUpstreamHealthRegistry().Hydrate(recent)
	GlobalUpstreamHealthRegistry().Hydrate(defaultUpstreamHealthSnapshot(92011))
	svc := &UpstreamConfigService{repo: repo}

	ids, err := svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Equal(t, []int64{92011}, ids)
}

func upstreamHealthTimePtr(value time.Time) *time.Time { return &value }
