package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamModelSyncStateUsesFreshAndEnforceWindows(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	state := upstreamModelSyncStateMap(UpstreamModelSyncStatusAvailable, UpstreamModelSyncSourceLive, now, UpstreamModelSyncState{}, 2, "checksum", "", "")
	account := &Account{UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Extra: map[string]any{AccountUpstreamModelSyncExtraKey: state}}
	if !account.upstreamModelSyncFresh(now.Add(29 * time.Minute)) {
		t.Fatal("expected sync state to be fresh")
	}
	if account.upstreamModelSyncFresh(now.Add(31 * time.Minute)) {
		t.Fatal("expected sync state to expire freshness")
	}
	if !account.upstreamModelSyncFresh(now.Add(UpstreamModelSyncFreshDuration)) || account.upstreamModelSyncFresh(now.Add(UpstreamModelSyncFreshDuration).Add(time.Nanosecond)) {
		t.Fatal("fresh boundary must be inclusive only at fresh_until")
	}
	if !account.automaticModelMappingEnforced(now.Add(23 * time.Hour)) {
		t.Fatal("expected automatic mapping to remain enforced for 24 hours")
	}
	if account.automaticModelMappingEnforced(now.Add(25 * time.Hour)) {
		t.Fatal("expected automatic mapping to stop being enforced after 24 hours")
	}
	if !account.automaticModelMappingEnforced(now.Add(UpstreamModelSyncEnforceDuration)) || account.automaticModelMappingEnforced(now.Add(UpstreamModelSyncEnforceDuration).Add(time.Nanosecond)) {
		t.Fatal("enforce boundary must be inclusive only at enforce_until")
	}
}

func TestIdentityModelMappingIsDeterministicAndRejectsEmpty(t *testing.T) {
	mapping := identityModelMapping([]string{" b ", "a", "b", "a"})
	if len(mapping) != 2 || mapping["a"] != "a" || mapping["b"] != "b" {
		t.Fatalf("unexpected identity mapping: %#v", mapping)
	}
	if identityModelMapping(nil) != nil {
		t.Fatal("empty model list must not create an allow-all mapping")
	}
	if upstreamModelChecksum([]string{"b", "a"}) != upstreamModelChecksum([]string{"a", "b"}) {
		t.Fatal("model checksum must be order independent")
	}
}

func TestSyncManagedMappingIsBypassedWhenStaleButManualMappingRemains(t *testing.T) {
	now := time.Now().UTC().Add(-25 * time.Hour)
	state := upstreamModelSyncStateMap(UpstreamModelSyncStatusStale, UpstreamModelSyncSourceLive, now, UpstreamModelSyncState{LastSuccessAt: &now, EnforceUntil: upstreamModelSyncTimePtr(now.Add(24 * time.Hour))}, 1, "old", "upstream", "upstream")
	managed := &Account{UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Credentials: map[string]any{"model_mapping": map[string]any{"old": "old"}}, Extra: map[string]any{AccountUpstreamModelSyncExtraKey: state}}
	if managed.automaticModelMappingEnforced(time.Now().UTC()) {
		t.Fatal("stale automatic mapping must not be enforced")
	}
	manual := &Account{UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerManual, Credentials: managed.Credentials, Extra: managed.Extra}
	if !manual.automaticModelMappingEnforced(time.Now().UTC()) {
		t.Fatal("manual mapping semantics must remain unchanged")
	}
}

func TestNewAPIModelLimitsArePreferredWhenValid(t *testing.T) {
	account := &Account{UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsExtraKey:        []string{"gpt-a", "gpt-b"},
	}}
	svc := &AccountTestService{}
	models, source, err := svc.fetchModelsForUpstreamAccount(testContext(), account, nil)
	if err != nil || source != UpstreamModelSyncSourceNewAPI || len(models) != 2 {
		t.Fatalf("unexpected NewAPI model limit result: models=%v source=%s err=%v", models, source, err)
	}
}

type upstreamModelSyncRepoStub struct {
	stubOpenAIAccountRepo
	mapping map[string]string
	state   map[string]any
	delay   time.Duration
	calls   atomic.Int32
}

func (r *upstreamModelSyncRepoStub) PersistUpstreamModelSync(_ context.Context, _ int64, mapping map[string]string, state map[string]any) error {
	r.calls.Add(1)
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mapping = mapping
	r.state = state
	return nil
}

func TestSyncUpstreamAccountModelsSingleflightDeduplicatesConcurrentForceRefresh(t *testing.T) {
	repo := &upstreamModelSyncRepoStub{delay: 40 * time.Millisecond}
	svc := &AccountTestService{accountRepo: repo}
	account := &Account{ID: 88, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsExtraKey:        []string{"model-a", "model-b"},
	}}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := svc.SyncUpstreamAccountModels(context.Background(), account, nil, true)
			if err != nil || !result.Updated {
				t.Errorf("concurrent force refresh result=%+v err=%v", result, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := repo.calls.Load(); got != 1 {
		t.Fatalf("expected one persisted sync, got %d", got)
	}
}

func TestSyncManagedAccountPersistsNewAPIIdentityMappingAndState(t *testing.T) {
	repo := &upstreamModelSyncRepoStub{}
	svc := &AccountTestService{accountRepo: repo}
	account := &Account{ID: 9, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"model_mapping": map[string]any{"removed-model": "removed-model"},
	}, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsExtraKey:        []any{"gpt-b", "gpt-a", "gpt-a"},
	}}
	result, err := svc.SyncUpstreamAccountModels(context.Background(), account, nil, true)
	if err != nil {
		t.Fatalf("SyncUpstreamAccountModels() error = %v", err)
	}
	if !result.Updated || len(repo.mapping) != 2 || repo.mapping["gpt-a"] != "gpt-a" || repo.mapping["removed-model"] != "" {
		t.Fatalf("unexpected persisted mapping: result=%+v mapping=%v", result, repo.mapping)
	}
	if repo.state["status"] != UpstreamModelSyncStatusAvailable || repo.state["fresh_until"] == nil || repo.state["enforce_until"] == nil || repo.state["checksum"] == nil {
		t.Fatalf("unexpected persisted state: %#v", repo.state)
	}
}

func TestManualForceRefreshIsNotCoalescedIntoScheduledFreshSkip(t *testing.T) {
	repo := &upstreamModelSyncRepoStub{delay: 20 * time.Millisecond}
	svc := &AccountTestService{accountRepo: repo}
	now := time.Now().UTC()
	account := &Account{ID: 89, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
		"model_mapping": map[string]any{"old-model": "old-model"},
	}, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsExtraKey:        []string{"new-model"},
		AccountUpstreamModelSyncExtraKey:        upstreamModelSyncStateMap(UpstreamModelSyncStatusAvailable, UpstreamModelSyncSourceNewAPI, now, UpstreamModelSyncState{}, 1, "old", "", ""),
	}}

	start := make(chan struct{})
	results := make(chan UpstreamModelSyncResult, 2)
	errs := make(chan error, 2)
	for _, force := range []bool{false, true} {
		force := force
		go func() {
			<-start
			result, err := svc.SyncUpstreamAccountModels(context.Background(), account, nil, force)
			results <- result
			errs <- err
		}()
	}
	close(start)

	updated := 0
	skipped := 0
	for range 2 {
		result := <-results
		if result.Updated {
			updated++
		}
		if result.Skipped {
			skipped++
		}
		if err := <-errs; err != nil {
			t.Fatalf("concurrent refresh failed: %v", err)
		}
	}
	if updated != 1 || skipped != 1 || repo.calls.Load() != 1 {
		t.Fatalf("manual force must execute once while scheduled fresh sync skips: updated=%d skipped=%d persisted=%d", updated, skipped, repo.calls.Load())
	}
}

func TestSyncManagedAccountFailureKeepsMappingAndWritesSafeState(t *testing.T) {
	repo := &upstreamModelSyncRepoStub{}
	svc := &AccountTestService{accountRepo: repo}
	now := time.Now().UTC().Add(-time.Hour)
	account := &Account{ID: 10, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"old": "old"}}, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsInvalidExtraKey: true,
		AccountUpstreamModelSyncExtraKey:        upstreamModelSyncStateMap(UpstreamModelSyncStatusAvailable, UpstreamModelSyncSourceNewAPI, now, UpstreamModelSyncState{}, 1, "old-checksum", "", ""),
	}}
	_, err := svc.SyncUpstreamAccountModels(context.Background(), account, nil, true)
	if err == nil {
		t.Fatal("expected invalid NewAPI model limits to fail")
	}
	if repo.mapping != nil {
		t.Fatalf("failure must not replace the old mapping: %#v", repo.mapping)
	}
	if repo.state["status"] != UpstreamModelSyncStatusStale || repo.state["error_code"] != "upstream_error" || repo.state["checksum"] != "old-checksum" {
		t.Fatalf("unexpected failure state: %#v", repo.state)
	}
}

type upstreamModelSyncBatchRepoStub struct {
	stubOpenAIAccountRepo
	accountsByKey map[int64][]Account
	active        atomic.Int32
	maxActive     atomic.Int32
	mu            sync.Mutex
	persisted     map[int64]int
}

func (r *upstreamModelSyncBatchRepoStub) ListByUpstreamKeyID(_ context.Context, keyID int64) ([]Account, error) {
	return append([]Account(nil), r.accountsByKey[keyID]...), nil
}

func (r *upstreamModelSyncBatchRepoStub) PersistUpstreamModelSync(_ context.Context, accountID int64, _ map[string]string, _ map[string]any) error {
	active := r.active.Add(1)
	for {
		current := r.maxActive.Load()
		if active <= current || r.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	time.Sleep(20 * time.Millisecond)
	r.active.Add(-1)
	r.mu.Lock()
	if r.persisted == nil {
		r.persisted = map[int64]int{}
	}
	r.persisted[accountID]++
	r.mu.Unlock()
	return nil
}

func TestManagedModelSyncBatchBoundsConcurrencyAndDeduplicatesAccounts(t *testing.T) {
	repo := &upstreamModelSyncBatchRepoStub{accountsByKey: map[int64][]Account{}}
	keys := make([]UpstreamKey, 0, 8)
	for i := int64(1); i <= 8; i++ {
		account := Account{ID: i, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{
			AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
			AccountNewAPIModelLimitsEnabledExtraKey: true,
			AccountNewAPIModelLimitsExtraKey:        []string{"gpt-a"},
		}}
		repo.accountsByKey[i] = []Account{account, account}
		keys = append(keys, UpstreamKey{ID: i, Status: StatusActive, Extra: account.Extra})
	}
	testService := &AccountTestService{accountRepo: repo}
	svc := &UpstreamConfigService{accountRepo: repo, accountTestService: testService}
	stats := svc.syncManagedUpstreamAccountModels(context.Background(), keys, false)
	if stats.Attempted != 8 || stats.Updated != 8 || stats.Failed != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if got := repo.maxActive.Load(); got > UpstreamModelSyncConcurrency || got < 2 {
		t.Fatalf("unexpected maximum concurrency: %d", got)
	}
	for id, count := range repo.persisted {
		if count != 1 {
			t.Fatalf("account %d persisted %d times", id, count)
		}
	}
}

func TestManagedModelSyncScheduledSkipsFreshButManualForcesRefresh(t *testing.T) {
	now := time.Now().UTC()
	repo := &upstreamModelSyncBatchRepoStub{accountsByKey: map[int64][]Account{}}
	account := Account{ID: 51, UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"model_mapping": map[string]any{"old-model": "old-model"}}, Extra: map[string]any{
		AccountUpstreamProviderKey:              AccountUpstreamProviderNewAPI,
		AccountNewAPIModelLimitsEnabledExtraKey: true,
		AccountNewAPIModelLimitsExtraKey:        []string{"new-model"},
		AccountUpstreamModelSyncExtraKey:        upstreamModelSyncStateMap(UpstreamModelSyncStatusAvailable, UpstreamModelSyncSourceNewAPI, now, UpstreamModelSyncState{}, 1, "old", "", ""),
	}}
	repo.accountsByKey[7] = []Account{account}
	key := UpstreamKey{ID: 7, Status: StatusActive, Extra: account.Extra}
	testService := &AccountTestService{accountRepo: repo}
	svc := &UpstreamConfigService{accountRepo: repo, accountTestService: testService}

	scheduled := svc.syncManagedUpstreamAccountModels(context.Background(), []UpstreamKey{key}, false)
	if scheduled.Skipped != 1 || scheduled.Attempted != 0 || repo.maxActive.Load() != 0 {
		t.Fatalf("scheduled sync must skip a fresh account: %+v", scheduled)
	}
	manual := svc.syncManagedUpstreamAccountModels(context.Background(), []UpstreamKey{key}, true)
	if manual.Attempted != 1 || manual.Updated != 1 || repo.persisted[account.ID] != 1 {
		t.Fatalf("manual sync must force refresh: %+v persisted=%v", manual, repo.persisted)
	}
}

func testContext() context.Context                        { return context.Background() }
func upstreamModelSyncTimePtr(value time.Time) *time.Time { return &value }
