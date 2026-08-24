package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type healthEventCaptureRepo struct {
	UpstreamConfigRepository
	mu        sync.Mutex
	patches   []map[string]any
	events    []*UpstreamEvent
	err       error
	keys      []UpstreamKey
	histories map[int64][]UpstreamHealthObservation
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

func (r *healthEventCaptureRepo) PatchKeyHealthWithObservation(ctx context.Context, keyID int64, health map[string]any, event *UpstreamEvent, observation *UpstreamHealthObservation) error {
	if err := r.PatchKeyHealthWithEvent(ctx, keyID, health, event); err != nil {
		return err
	}
	if observation != nil {
		r.mu.Lock()
		if r.histories == nil {
			r.histories = map[int64][]UpstreamHealthObservation{}
		}
		r.histories[keyID] = append(r.histories[keyID], *observation)
		r.mu.Unlock()
	}
	return nil
}

func (r *healthEventCaptureRepo) ListKeyHealthHistories(_ context.Context, keyIDs []int64, limit int) (map[int64][]UpstreamHealthObservation, error) {
	out := map[int64][]UpstreamHealthObservation{}
	for _, keyID := range keyIDs {
		items := append([]UpstreamHealthObservation(nil), r.histories[keyID]...)
		if limit > 0 && len(items) > limit {
			items = items[len(items)-limit:]
		}
		if len(items) > 0 {
			out[keyID] = items
		}
	}
	return out, nil
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
	require.Len(t, repo.histories[keyID], 1)
	require.Equal(t, "admin", repo.histories[keyID][0].Source)
	require.Equal(t, "disabled", repo.histories[keyID][0].Result)

	_, err = svc.SetKeyObservation(context.Background(), keyID, false)
	require.NoError(t, err)
	require.Len(t, repo.events, 1, "same-state evidence must not append another transition event")
}

func TestNewUpstreamConfigServiceHydratesDurableObservationPreference(t *testing.T) {
	const keyID = 92003
	GlobalUpstreamHealthRegistry().Forget(keyID)
	defer GlobalUpstreamHealthRegistry().Forget(keyID)

	repo := &healthEventCaptureRepo{keys: []UpstreamKey{{
		ID:                      keyID,
		ObservationEnabled:      false,
		ObservationEnabledKnown: true,
		Extra: map[string]any{"health": map[string]any{
			"status":              string(UpstreamHealthHealthy),
			"observation_enabled": true,
		}},
	}}}
	_ = NewUpstreamConfigService(repo, nil, nil)

	item := GlobalUpstreamHealthRegistry().Snapshot(keyID)
	require.False(t, item.ObservationEnabled)
	require.Equal(t, UpstreamHealthDisabled, item.Status)
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
	require.Equal(t, "UPSTREAM_HEALTH_EVIDENCE_PERSIST_FAILED", infraerrors.Reason(err))
	require.Equal(t, "failed to save upstream health evidence", infraerrors.Message(err))
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

type healthProbeLockRepo struct {
	healthEventCaptureRepo
	lockMu sync.Mutex
	held   bool
	key    UpstreamKey
}

func (r *healthProbeLockRepo) GetKeyByID(context.Context, int64) (*UpstreamKey, error) {
	item := r.key
	return &item, nil
}

func (r *healthProbeLockRepo) WithUpstreamHealthProbeLock(ctx context.Context, _ int64, fn func(context.Context) error) (bool, error) {
	r.lockMu.Lock()
	if r.held {
		r.lockMu.Unlock()
		return false, nil
	}
	r.held = true
	r.lockMu.Unlock()
	defer func() {
		r.lockMu.Lock()
		r.held = false
		r.lockMu.Unlock()
	}()
	return true, fn(ctx)
}

type healthProbeAccountRepo struct {
	AccountRepository
	account Account
}

func (r *healthProbeAccountRepo) ListByUpstreamKeyID(context.Context, int64) ([]Account, error) {
	return []Account{r.account}, nil
}

type blockingUpstreamHealthProber struct {
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

type successfulUpstreamHealthProber struct{}

type probeScheduleReport struct {
	accountID    int64
	model        string
	success      bool
	firstTokenMs *int
}

type probeScheduleReporter struct {
	reports []probeScheduleReport
}

func (r *probeScheduleReporter) ReportOpenAIAccountScheduleResult(accountID int64, model string, success bool, firstTokenMs *int) {
	var copied *int
	if firstTokenMs != nil {
		value := *firstTokenMs
		copied = &value
	}
	r.reports = append(r.reports, probeScheduleReport{accountID: accountID, model: model, success: success, firstTokenMs: copied})
}

type mixedJuiceUpstreamHealthProber struct{}

func (*mixedJuiceUpstreamHealthProber) RunUpstreamHealthProbe(_ context.Context, _ *Account, model string) (UpstreamHealthProbeResult, error) {
	ttft := int64(35)
	duration := int64(60)
	score := 0
	return UpstreamHealthProbeResult{
		Model: model, Protocol: upstreamHealthProbeProtocolOpenAI,
		Result: "success", Reason: "probe_confidence_mixed", TTFTMs: &ttft, DurationMs: &duration,
		ConfidenceScore: &score, ConfidenceStatus: "mixed",
	}, nil
}

type failedAfterFirstTokenUpstreamHealthProber struct{}

func (*failedAfterFirstTokenUpstreamHealthProber) RunUpstreamHealthProbe(_ context.Context, _ *Account, model string) (UpstreamHealthProbeResult, error) {
	ttft := int64(45)
	return UpstreamHealthProbeResult{Model: model, Protocol: upstreamHealthProbeProtocolOpenAI, TTFTMs: &ttft}, errors.New("stream interrupted after first token")
}

func (*successfulUpstreamHealthProber) RunUpstreamHealthProbe(_ context.Context, _ *Account, model string) (UpstreamHealthProbeResult, error) {
	ttft := int64(25)
	duration := int64(40)
	return UpstreamHealthProbeResult{
		Model: model, Protocol: upstreamHealthProbeProtocolOpenAI,
		Result: "success", Reason: "probe_succeeded", TTFTMs: &ttft, DurationMs: &duration,
	}, nil
}

func TestUpstreamConfigServiceProbeReportsOpenAIScheduleResultBeforeConfidenceDegradation(t *testing.T) {
	const keyID int64 = 92030
	active := StatusActive
	platform := PlatformOpenAI
	repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active, Platform: &platform}}
	accountRepo := &healthProbeAccountRepo{account: Account{ID: 84, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID)}}
	reporter := &probeScheduleReporter{}
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe: "{\"enabled\":true}",
	}}
	settingService := NewSettingService(settingsRepo, nil)

	svc := NewUpstreamConfigService(repo, nil, accountRepo)
	svc.SetHealthProbeDependencies(&mixedJuiceUpstreamHealthProber{}, settingService)
	svc.SetOpenAIScheduleReporter(reporter)

	_, err := svc.ProbeKey(context.Background(), keyID)
	require.Error(t, err)
	require.Equal(t, "UPSTREAM_KEY_PROBE_FAILED", infraerrors.Reason(err))
	require.Len(t, reporter.reports, 1)
	require.Equal(t, int64(84), reporter.reports[0].accountID)
	require.Equal(t, true, reporter.reports[0].success, "network/stream completion succeeded before Juice quality degradation")
	require.NotNil(t, reporter.reports[0].firstTokenMs)
	require.Equal(t, 35, *reporter.reports[0].firstTokenMs)
}

func TestUpstreamConfigServiceProbeReportsFailureAndTTFTAfterStreamError(t *testing.T) {
	const keyID int64 = 92031
	active := StatusActive
	platform := PlatformOpenAI
	repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active, Platform: &platform}}
	accountRepo := &healthProbeAccountRepo{account: Account{ID: 85, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID)}}
	reporter := &probeScheduleReporter{}
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe: "{\"enabled\":true}",
	}}
	settingService := NewSettingService(settingsRepo, nil)

	svc := NewUpstreamConfigService(repo, nil, accountRepo)
	svc.SetHealthProbeDependencies(&failedAfterFirstTokenUpstreamHealthProber{}, settingService)
	svc.SetOpenAIScheduleReporter(reporter)

	_, err := svc.ProbeKey(context.Background(), keyID)
	require.Error(t, err)
	require.Len(t, reporter.reports, 1)
	require.False(t, reporter.reports[0].success)
	require.NotNil(t, reporter.reports[0].firstTokenMs)
	require.Equal(t, 45, *reporter.reports[0].firstTokenMs)
}

func TestUpstreamConfigServiceProbeReportsNonOpenAIWhenConfidenceEnabled(t *testing.T) {
	const keyID int64 = 92032
	active := StatusActive
	platform := PlatformAnthropic
	repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active, Platform: &platform}}
	accountRepo := &healthProbeAccountRepo{account: Account{ID: 86, Type: AccountTypeAPIKey, Platform: PlatformAnthropic, UpstreamKeyID: int64Ptr(keyID)}}
	reporter := &probeScheduleReporter{}
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamConfidenceProbe: "{\"enabled\":true}",
	}}

	svc := NewUpstreamConfigService(repo, nil, accountRepo)
	svc.SetHealthProbeDependencies(&successfulUpstreamHealthProber{}, NewSettingService(settingsRepo, nil))
	svc.SetOpenAIScheduleReporter(reporter)

	_, err := svc.ProbeKey(context.Background(), keyID)
	require.NoError(t, err)
	require.Len(t, reporter.reports, 1)
	require.Equal(t, int64(86), reporter.reports[0].accountID)
	require.True(t, reporter.reports[0].success)
}

func TestUpstreamConfigServiceProbeDoesNotReportOpenAIWithoutEnabledConfidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "missing"},
		{name: "disabled", raw: "{\"enabled\":false}"},
		{name: "invalid", raw: "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const keyID int64 = 92033
			active := StatusActive
			platform := PlatformOpenAI
			repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active, Platform: &platform}}
			accountRepo := &healthProbeAccountRepo{account: Account{ID: 87, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID)}}
			reporter := &probeScheduleReporter{}
			settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
			if tc.raw != "" {
				settingsRepo.values[SettingKeyUpstreamConfidenceProbe] = tc.raw
			}

			svc := NewUpstreamConfigService(repo, nil, accountRepo)
			svc.SetHealthProbeDependencies(&successfulUpstreamHealthProber{}, NewSettingService(settingsRepo, nil))
			svc.SetOpenAIScheduleReporter(reporter)

			_, err := svc.ProbeKey(context.Background(), keyID)
			require.NoError(t, err)
			require.Empty(t, reporter.reports)
		})
	}
}

func (p *blockingUpstreamHealthProber) RunUpstreamHealthProbe(ctx context.Context, _ *Account, model string) (UpstreamHealthProbeResult, error) {
	p.calls.Add(1)
	select {
	case p.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return UpstreamHealthProbeResult{}, ctx.Err()
	case <-p.release:
	}
	ttft := int64(25)
	duration := int64(40)
	return UpstreamHealthProbeResult{Model: model, Protocol: upstreamHealthProbeProtocolOpenAI, Result: "success", Reason: "probe_succeeded", TTFTMs: &ttft, DurationMs: &duration}, nil
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

type healthDueRunnerFake struct {
	*healthRunnerFake
	dueCalls atomic.Int32
}

func (f *healthDueRunnerFake) ProbeDueKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	f.dueCalls.Add(1)
	return f.healthRunnerFake.ProbeKey(ctx, keyID)
}

func TestUpstreamHealthProbeRunnerUsesDueProbePathWhenAvailable(t *testing.T) {
	fake := &healthDueRunnerFake{healthRunnerFake: &healthRunnerFake{ids: []int64{11, 12}, delay: time.Millisecond}}
	runner := NewUpstreamHealthProbeRunner(fake, time.Minute, 2, 1)

	require.NoError(t, runner.RunOnce(context.Background()))
	require.Equal(t, int32(2), fake.dueCalls.Load())
	require.Equal(t, int32(2), fake.probeCalls.Load())
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

func TestUpstreamConfigServiceProbeKeyRejectsCrossInstanceDuplicate(t *testing.T) {
	const keyID int64 = 92016
	active := StatusActive
	repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active}}
	accountRepo := &healthProbeAccountRepo{account: Account{ID: 84, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID)}}
	prober := &blockingUpstreamHealthProber{started: make(chan struct{}, 1), release: make(chan struct{})}
	first := NewUpstreamConfigService(repo, nil, accountRepo)
	second := NewUpstreamConfigService(repo, nil, accountRepo)
	first.SetHealthProbeDependencies(prober, nil)
	second.SetHealthProbeDependencies(prober, nil)

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.ProbeKey(context.Background(), keyID)
		firstDone <- err
	}()
	select {
	case <-prober.started:
	case <-time.After(time.Second):
		t.Fatal("first probe did not start")
	}

	_, err := second.ProbeKey(context.Background(), keyID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "already in progress")
	require.Equal(t, int32(1), prober.calls.Load())

	close(prober.release)
	require.NoError(t, <-firstDone)
}

func TestUpstreamConfigServiceProbeKeyMapsEvidencePersistenceFailure(t *testing.T) {
	const keyID int64 = 92017
	previous := defaultUpstreamHealthSnapshot(keyID)
	previous.Status = UpstreamHealthObserving
	previous.UpdatedAt = time.Date(2026, 8, 12, 7, 0, 0, 0, time.UTC)
	GlobalUpstreamHealthRegistry().Hydrate(previous)

	repo := &healthProbeLockRepo{
		healthEventCaptureRepo: healthEventCaptureRepo{err: errors.New("duplicate key value violates upstream_events_pkey")},
		key:                    UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: StatusActive},
	}
	accountRepo := &healthProbeAccountRepo{account: Account{
		ID: 84, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID),
	}}
	svc := NewUpstreamConfigService(repo, nil, accountRepo)
	svc.SetHealthProbeDependencies(&successfulUpstreamHealthProber{}, nil)

	_, err := svc.ProbeKey(context.Background(), keyID)
	require.Error(t, err)
	require.Equal(t, 500, infraerrors.Code(err))
	require.Equal(t, "UPSTREAM_HEALTH_EVIDENCE_PERSIST_FAILED", infraerrors.Reason(err))
	require.Equal(t, "failed to save upstream health evidence", infraerrors.Message(err))
	require.Equal(t, previous, GlobalUpstreamHealthRegistry().Snapshot(keyID))
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
	require.Equal(t, "traffic", repo.histories[keyID][0].Source)
	require.Equal(t, "429", repo.histories[keyID][0].Result)

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
	require.GreaterOrEqual(t, len(repo.histories[keyID]), 3)
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

func TestUpstreamConfigServiceListDueHealthProbeKeysReloadsConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	active := StatusActive
	const keyID int64 = 92015
	repo := &healthEventCaptureRepo{keys: []UpstreamKey{{ID: keyID, Status: active}}}
	lastProbe := defaultUpstreamHealthSnapshot(keyID)
	lastProbe.LastProbeAt = upstreamHealthTimePtr(now.Add(-2 * time.Minute))
	GlobalUpstreamHealthRegistry().Hydrate(lastProbe)
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{
		SettingKeyUpstreamProbeIntervalSeconds: "300",
	}}
	svc := NewUpstreamConfigService(repo, nil, nil)
	svc.SetHealthProbeDependencies(nil, NewSettingService(settingsRepo, nil))

	ids, err := svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Empty(t, ids, "a two-minute-old probe remains fresh at a five-minute interval")

	settingsRepo.values[SettingKeyUpstreamProbeIntervalSeconds] = "60"
	ids, err = svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Equal(t, []int64{keyID}, ids, "the next scan observes the updated interval without restarting the runner")
}

func TestUpstreamConfigServiceListDueHealthProbeKeysUsesConfidenceIndependentMode(t *testing.T) {
	now := time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC)
	cutoff := now.Add(-time.Duration(DefaultUpstreamProbeIntervalSeconds) * time.Second)
	active := StatusActive
	openai := PlatformOpenAI
	gemini := "gemini"
	const openAIEnabledID int64 = 92101
	const nonOpenAIID int64 = 92104
	repo := &healthEventCaptureRepo{keys: []UpstreamKey{
		{ID: openAIEnabledID, Status: active, Platform: &openai},
		{ID: nonOpenAIID, Status: active, Platform: &gemini},
	}}
	for _, keyID := range []int64{openAIEnabledID, nonOpenAIID} {
		item := defaultUpstreamHealthSnapshot(keyID)
		item.LastEvidenceAt = upstreamHealthTimePtr(now.Add(-10 * time.Second))
		item.LastProbeAt = upstreamHealthTimePtr(cutoff.Add(-time.Second))
		GlobalUpstreamHealthRegistry().Hydrate(item)
	}
	settingsRepo := &upstreamManagementSettingRepoStub{values: map[string]string{}}
	raw, err := json.Marshal(UpstreamConfidenceProbeSettings{Enabled: true})
	require.NoError(t, err)
	settingsRepo.values[SettingKeyUpstreamConfidenceProbe] = string(raw)
	svc := NewUpstreamConfigService(repo, nil, nil)
	svc.SetHealthProbeDependencies(nil, NewSettingService(settingsRepo, nil))

	ids, err := svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Equal(t, []int64{openAIEnabledID, nonOpenAIID}, ids)

	settingsRepo.values[SettingKeyUpstreamConfidenceProbe] = "{\"enabled\":false}"
	ids, err = svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Empty(t, ids, "disabled or missing confidence settings use traffic freshness suppression")
	settingsRepo.values[SettingKeyUpstreamConfidenceProbe] = "{"
	ids, err = svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Empty(t, ids, "invalid confidence settings are treated as disabled")
	delete(settingsRepo.values, SettingKeyUpstreamConfidenceProbe)
	ids, err = svc.ListDueHealthProbeKeyIDs(context.Background(), now, 20)
	require.NoError(t, err)
	require.Empty(t, ids, "missing confidence settings are treated as disabled")
}

func TestUpstreamConfigServiceProbeDueKeyRechecksTrafficFreshness(t *testing.T) {
	const keyID int64 = 92105
	now := time.Now().UTC()
	active := StatusActive
	platform := PlatformOpenAI
	previous := defaultUpstreamHealthSnapshot(keyID)
	previous.LastEvidenceAt = upstreamHealthTimePtr(now.Add(-time.Second))
	previous.LastProbeAt = upstreamHealthTimePtr(now.Add(-2 * time.Minute))
	GlobalUpstreamHealthRegistry().Hydrate(previous)
	repo := &healthProbeLockRepo{key: UpstreamKey{ID: keyID, UpstreamConfigID: 42, Status: active, Platform: &platform}}
	prober := &successfulUpstreamHealthProber{}
	service := NewUpstreamConfigService(repo, nil, &healthProbeAccountRepo{account: Account{ID: 84, Type: AccountTypeAPIKey, Platform: PlatformOpenAI, UpstreamKeyID: int64Ptr(keyID)}})
	service.SetHealthProbeDependencies(prober, nil)

	item, err := service.ProbeDueKey(context.Background(), keyID)
	require.NoError(t, err)
	require.Equal(t, previous, item)
	require.Empty(t, repo.histories[keyID], "fresh real traffic must suppress the background probe")

	_, err = service.ProbeKey(context.Background(), keyID)
	require.NoError(t, err, "manual probes remain forceful")
	require.Len(t, repo.histories[keyID], 1)
}

func TestUpstreamConfigServiceListsBoundedHealthHistoriesInOneBatch(t *testing.T) {
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC)
	repo := &healthEventCaptureRepo{histories: map[int64][]UpstreamHealthObservation{
		81: {
			{ObservedAt: base, State: UpstreamHealthHealthy, Source: "probe", Result: "success"},
			{ObservedAt: base.Add(time.Minute), State: UpstreamHealthDegraded, Source: "traffic", Result: "500"},
		},
		82: {{ObservedAt: base.Add(2 * time.Minute), State: UpstreamHealthHealthy, Source: "traffic", Result: "200"}},
	}}
	svc := &UpstreamConfigService{repo: repo}

	histories, err := svc.ListUpstreamHealthHistories(context.Background(), []int64{82, 81}, 1)
	require.NoError(t, err)
	require.Len(t, histories[81], 1)
	require.Equal(t, "500", histories[81][0].Result)
	require.Len(t, histories[82], 1)
}

func upstreamHealthTimePtr(value time.Time) *time.Time { return &value }
