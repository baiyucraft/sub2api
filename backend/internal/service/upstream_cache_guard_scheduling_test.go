package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// State-machine and SQL tests cover snapshot production separately. These
// fixtures exercise both real scheduler implementations against a frozen shared
// read snapshot, including actual slot acquisition and sticky cache writes.
type cacheGuardSelectionStore struct {
	upstreamcacheguard.Store
	rows         []upstreamcacheguard.RuntimeRow
	accounts     map[string]upstreamcacheguard.AccountState
	failAccounts map[int64]bool
	trialPlans   int
}

func cacheGuardSelectionIdentity(id, keyID int64) string { return fmt.Sprintf("%d:%d", id, keyID) }

func (s *cacheGuardSelectionStore) seed(id, keyID int64) (upstreamcacheguard.AccountState, bool, error) {
	a := upstreamcacheguard.AccountState{AccountID: id, UpstreamKeyID: keyID, Phase: upstreamcacheguard.Healthy}
	found := false
	for _, row := range s.rows {
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var previous upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &previous); err != nil {
			return a, false, err
		}
		if previous.AccountID != id || previous.UpstreamKeyID != keyID {
			continue
		}
		found = true
		a.GroupIDs = append(a.GroupIDs, row.GroupID)
		if previous.Phase != upstreamcacheguard.Healthy && previous.Phase != "" {
			a.Phase = previous.Phase
			if previous.CooldownUntil.After(a.CooldownUntil) {
				a.CooldownUntil = previous.CooldownUntil
			}
		}
		if previous.Phase == upstreamcacheguard.Trial {
			a.CohortID = "fixture"
			a.TrialGroupIDs = append(a.TrialGroupIDs, row.GroupID)
			a.FirstTrialAt = previous.FirstTrialAt
		}
	}
	return a, found, nil
}

func (s *cacheGuardSelectionStore) AccountRows(context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	seen := map[string]upstreamcacheguard.AccountRuntimeRow{}
	for _, row := range s.rows {
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var a upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &a); err != nil {
			return nil, err
		}
		if a.UpstreamKeyID == 0 {
			continue
		}
		seen[cacheGuardSelectionIdentity(a.AccountID, a.UpstreamKeyID)] = upstreamcacheguard.AccountRuntimeRow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Version: 1}
	}
	for id, a := range s.accounts {
		seen[id] = upstreamcacheguard.AccountRuntimeRow{AccountID: a.AccountID, UpstreamKeyID: a.UpstreamKeyID, Version: 2}
	}
	rows := make([]upstreamcacheguard.AccountRuntimeRow, 0, len(seen))
	for _, row := range seen {
		rows = append(rows, row)
	}
	return rows, nil
}

// Scheduler fixtures store fixed quality rows, not durable facts. Build the
// same account-window projection without inventing transaction evidence.
func (s *cacheGuardSelectionStore) AccountWindows(_ context.Context, _, _ time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	windows := map[string]upstreamcacheguard.AccountWindow{}
	for _, row := range s.rows {
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var state upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &state); err != nil {
			return nil, err
		}
		if state.UpstreamKeyID <= 0 {
			continue
		}
		identity := cacheGuardSelectionIdentity(state.AccountID, state.UpstreamKeyID)
		window, ok := windows[identity]
		if !ok {
			current, exists := s.accounts[identity]
			if !exists {
				var err error
				current, _, err = s.seed(state.AccountID, state.UpstreamKeyID)
				if err != nil {
					return nil, err
				}
			}
			window = upstreamcacheguard.AccountWindow{AccountID: state.AccountID, UpstreamKeyID: state.UpstreamKeyID, Version: int64(len(s.rows)*2 + 1), State: current}
		}
		window.ValidRequests += state.ValidRequests
		window.TotalInputTokens += int64(state.ValidRequests) * 1000
		window.CacheReadTokens += int64(math.Round(state.CachePercent*10)) * int64(state.ValidRequests)
		window.GroupIDs = append(window.GroupIDs, row.GroupID)
		if state.Phase == upstreamcacheguard.Trial {
			window.TrialValidRequests += state.TrialValidRequests
			window.TrialInputTokens += int64(state.TrialValidRequests) * 1000
			window.TrialCacheReadTokens += int64(math.Round(state.TrialCachePercent*10)) * int64(state.TrialValidRequests)
			window.TrialSessions += state.TrialSessions
			window.TrialGroupIDs = append(window.TrialGroupIDs, row.GroupID)
		}
		windows[identity] = window
	}
	for identity, state := range s.accounts {
		window, ok := windows[identity]
		if !ok {
			window = upstreamcacheguard.AccountWindow{AccountID: state.AccountID, UpstreamKeyID: state.UpstreamKeyID, State: state}
		}
		window.State, window.Version = state, int64(len(s.rows)*2+2)
		window.Policies = map[string]upstreamcacheguard.AccountWindow{}
		for signature := range state.Policies {
			projection := window
			projection.Policies = nil
			window.Policies[signature] = projection
		}
		windows[identity] = window
	}
	return windows, nil
}

func (s *cacheGuardSelectionStore) TransactAccount(_ context.Context, _, accountID, keyID int64, fn func(upstreamcacheguard.Tx) error) error {
	if s.failAccounts[accountID] {
		return fmt.Errorf("account %d refresh failed", accountID)
	}
	return fn(&cacheGuardSelectionTx{store: s, accountID: accountID, keyID: keyID})
}

type cacheGuardSelectionTx struct {
	upstreamcacheguard.Tx
	store            *cacheGuardSelectionStore
	accountID, keyID int64
}

func (tx *cacheGuardSelectionTx) Read(kind, key string, out any) (int64, error) {
	for _, row := range tx.store.rows {
		if row.Kind == kind && row.Key == key {
			return row.Version, json.Unmarshal(row.Payload, out)
		}
	}
	return 0, nil
}

func (tx *cacheGuardSelectionTx) Write(kind, key string, value any) (int64, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	for i := range tx.store.rows {
		row := &tx.store.rows[i]
		if row.Kind == kind && row.Key == key {
			row.Version++
			row.Payload = raw
			return row.Version, nil
		}
	}
	tx.store.rows = append(tx.store.rows, upstreamcacheguard.RuntimeRow{GroupID: 100, Kind: kind, Key: key, Version: 1, Payload: raw})
	return 1, nil
}

func (tx *cacheGuardSelectionTx) ReadAccount(id, keyID int64, out any) (int64, error) {
	if a, exists := tx.store.accounts[cacheGuardSelectionIdentity(id, keyID)]; exists {
		raw, err := json.Marshal(a)
		if err != nil {
			return 0, err
		}
		return 1, json.Unmarshal(raw, out)
	}
	a, exists, err := tx.store.seed(id, keyID)
	if err != nil || !exists {
		return 0, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return 0, err
	}
	return 1, json.Unmarshal(raw, out)
}

func (tx *cacheGuardSelectionTx) WriteAccount(id, keyID int64, value any) (int64, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}
	var a upstreamcacheguard.AccountState
	if err = json.Unmarshal(raw, &a); err != nil {
		return 0, err
	}
	if tx.store.accounts == nil {
		tx.store.accounts = map[string]upstreamcacheguard.AccountState{}
	}
	tx.store.accounts[cacheGuardSelectionIdentity(id, keyID)] = a
	return 2, nil
}

func (tx *cacheGuardSelectionTx) AccountFacts(id, keyID int64, since time.Time) ([]upstreamcacheguard.Evidence, error) {
	var facts []upstreamcacheguard.Evidence
	for _, row := range tx.store.rows {
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var a upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &a); err != nil {
			return nil, err
		}
		if a.AccountID != id || a.UpstreamKeyID != keyID {
			continue
		}
		for i := 0; i < a.ValidRequests; i++ {
			completed := time.Now().UTC().Add(-10 * time.Second)
			if completed.Before(since) {
				continue
			}
			trial := a.Phase == upstreamcacheguard.Trial && i < a.TrialValidRequests
			read := int64(math.Round(a.CachePercent * 10))
			if trial {
				read = int64(math.Round(a.TrialCachePercent * 10))
			}
			facts = append(facts, upstreamcacheguard.Evidence{
				ID:        fmt.Sprintf("fixture-%d-%d-%d", id, row.GroupID, i),
				Request:   upstreamcacheguard.Request{GroupID: row.GroupID, AccountID: id, UpstreamKeyID: keyID},
				Admission: upstreamcacheguard.Admission{Sample: true, WarmAtStart: true, Trial: trial, CohortID: "fixture", SessionKey: fmt.Sprintf("session-%d", i%3)},
				RawUsage:  upstreamcacheguard.RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 1000, CacheReadTokens: read},
				Success:   true, CompletedAt: completed,
			})
		}
	}
	return facts, nil
}

func (tx *cacheGuardSelectionTx) LegacyAccounts(int64, int64) ([]upstreamcacheguard.LegacyAccountState, error) {
	return nil, nil
}

func (s *cacheGuardSelectionStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return s.rows, nil
}
func (s *cacheGuardSelectionStore) Transact(context.Context, int64, func(upstreamcacheguard.Tx) error) error {
	s.trialPlans++
	return nil
}
func (s *cacheGuardSelectionStore) row(t *testing.T, kind, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	s.rows = append(s.rows, upstreamcacheguard.RuntimeRow{GroupID: 100, Kind: kind, Key: key, Version: 1, Payload: raw, UpdatedAt: time.Now()})
}

func cacheGuardSelectionFixture(t *testing.T, advanced bool, rates []float64, protected map[int64]bool, pending bool, residence time.Time, capacity map[int64]bool) (*OpenAIGatewayService, context.Context, *UpstreamCacheGuardRunner, *schedulerTestGatewayCache) {
	t.Helper()
	store := &cacheGuardSelectionStore{}
	r := &UpstreamCacheGuardRunner{engine: upstreamcacheguard.NewEngine(store), store: store, secret: []byte("shared-test-hmac")}
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, r.engine.ApplySettings(settings))
	old := installedCacheGuardRunner.Swap(r)
	t.Cleanup(func() { installedCacheGuardRunner.Store(old); resetOpenAIAdvancedSchedulerSettingCacheForTest() })
	r.available.Store(true)
	group := int64(100)
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	req, ok := r.logicalRequest(ctx, &group, "conversation", "gpt-test", "openai")
	require.True(t, ok)
	var accounts []Account
	for i, rate := range rates {
		id := int64(i + 1)
		key := id
		configID := id
		a := Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{group}, UpstreamConfigID: &configID, UpstreamKeyID: &key, UpstreamConcurrencyLimit: 1}
		accounts = append(accounts, a)
		phase := upstreamcacheguard.Healthy
		if protected[id] {
			phase = upstreamcacheguard.Cooling
		}
		store.row(t, upstreamcacheguard.AccountKind, "account-"+newCacheGuardID(), upstreamcacheguard.AccountState{AccountID: id, UpstreamKeyID: key, Phase: phase, ValidRequests: 21, CachePercent: rate, CooldownUntil: time.Now().Add(time.Hour)})
	}
	if pending || !residence.IsZero() {
		source := req
		source.AccountID = 1
		source.UpstreamKeyID = 1
		store.row(t, upstreamcacheguard.SessionKind, upstreamcacheguard.SessionKey(req), upstreamcacheguard.SessionState{Key: upstreamcacheguard.SessionKey(req), FirstSeenAt: time.Now().Add(-time.Hour), LastAccountID: 1, LastModel: "gpt-test", LastProtocol: "openai", LastRouteKey: upstreamcacheguard.WarmRouteKey(source), LastCompletedAt: time.Now().Add(-time.Second), ResidenceUntil: residence, Routes: map[string]upstreamcacheguard.SessionRoute{upstreamcacheguard.WarmRouteKey(source): {AccountID: 1, UpstreamKeyID: 1, LastSuccessfulAt: time.Now().Add(-time.Second), LastCachePercent: rates[0], LowStreak: 2, PendingLow: pending}}})
	}
	require.NoError(t, r.engine.Refresh(ctx))
	cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
	enabled := "false"
	if advanced {
		enabled = "true"
	}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: &config.Config{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(enabled), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: capacity}), openaiAccountStats: newOpenAIAccountRuntimeStats()}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return enabledOpenAITTFTGuardConfig(20*time.Second, 5) }))
	if pending || !residence.IsZero() {
		cache.sessionBindings[svc.openAISessionCacheKey("conversation")] = 1
	}
	return svc, ctx, r, cache
}

func TestUpstreamCacheGuardRealSchedulersPrioritizeCacheAndFailOpen(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		name := "legacy"
		if advanced {
			name = "advanced"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				rates     []float64
				protected map[int64]bool
				slow      map[int64]int
				capacity  map[int64]bool
				want      int64
			}{
				{"high_cache_slow", []float64{90, 70}, nil, map[int64]int{1: 60000}, nil, 2},
				{"five_point_band_ttft_first", []float64{90, 85}, nil, map[int64]int{1: 60000}, nil, 2},
				{"best_full_use_other_capacity", []float64{95, 75}, nil, nil, map[int64]bool{1: false, 2: true}, 2},
				{"all_protected_still_available", []float64{20, 25}, map[int64]bool{1: true, 2: true}, map[int64]int{1: 90000, 2: 60000}, nil, 2},
				{"unique_protected_still_available", []float64{20}, map[int64]bool{1: true}, map[int64]int{1: 60000}, nil, 1},
			} {
				t.Run(tc.name, func(t *testing.T) {
					svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, tc.rates, tc.protected, false, time.Time{}, tc.capacity)
					group := int64(100)
					for id, ms := range tc.slow {
						acct, _ := svc.accountRepo.GetByID(ctx, id)
						svc.ReportOpenAIAccountScheduleResultForGroup(&group, acct, "gpt-test", true, &ms)
					}
					selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.Equal(t, tc.want, selection.Account.ID)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				})
			}
		})
	}
}

func TestUpstreamCacheGuardMigrationCommitsOnlyAfterCapacityAndTerminalAdmission(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, available := range []bool{false, true} {
			name := "legacy"
			if advanced {
				name = "advanced"
			}
			if available {
				name += "/target_available"
			} else {
				name += "/target_full"
			}
			t.Run(name, func(t *testing.T) {
				svc, ctx, runner, cache := cacheGuardSelectionFixture(t, advanced, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, map[int64]bool{1: true, 2: available})
				group := int64(100)
				accounts, _ := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
				preference := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
				require.True(t, preference.Migration, "session=%+v snapshot=%+v", func() upstreamcacheguard.SessionRead {
					req, _ := runner.logicalRequest(ctx, &group, "conversation", "gpt-test", "openai")
					return runner.engine.CachedSessionRead(req)
				}(), runner.engine.CachedSnapshot(group))
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				expected := int64(1)
				if available {
					expected = 2
				}
				require.Equal(t, expected, selection.Account.ID)
				require.Equal(t, available, selection.RequiresStickyAdmissionBinding())
				require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
				require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, selection), &group, "conversation", selection.Account.ID))
				require.Equal(t, expected, cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestUpstreamCacheGuardResidenceAndFixedRetryKeepSource(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "residence", true: "fixed_retry"}[fixed], func(t *testing.T) {
			residence := time.Now().Add(5 * time.Minute)
			if fixed {
				residence = time.Now().Add(-time.Minute)
			}
			svc, ctx, _, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, residence, nil)
			if fixed {
				ctx = WithOpenAIFixedRetryAccount(ctx, 1)
			}
			group := int64(100)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, int64(1), selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

type cacheGuardRefreshingAccountRepo struct {
	schedulerTestOpenAIAccountRepo
	initial []Account
	reads   int
	gets    int
}

func (r *cacheGuardRefreshingAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.gets++
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func (r *cacheGuardRefreshingAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	r.reads++
	if r.reads == 1 {
		return append([]Account(nil), r.initial...), nil
	}
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func TestUpstreamCacheGuardMigrationRejectsRefreshedCandidateOutsideTier(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			svc, ctx, _, cache := cacheGuardSelectionFixture(t, advanced, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, map[int64]bool{1: true, 2: false, 3: true})
			svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return OpenAITTFTGuardConfigSnapshot{} }))
			initial := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
			added := initial[1]
			added.ID = 3
			key, configID := int64(3), int64(3)
			added.UpstreamKeyID, added.UpstreamConfigID = &key, &configID
			repo := &cacheGuardRefreshingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: append(append([]Account(nil), initial...), added)}, initial: initial}
			svc.accountRepo = repo
			group := int64(100)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, 1, repo.reads, "all protection tiers reuse the initial candidate directory")
			require.Equal(t, int64(1), selection.Account.ID, "a newly visible account with no cache evidence cannot replace the source")
			require.False(t, selection.RequiresStickyAdmissionBinding())
			require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func cacheGuardRewriteSessionProtocol(t *testing.T, runner *UpstreamCacheGuardRunner, ctx context.Context, protocol string) {
	t.Helper()
	store := runner.store.(*cacheGuardSelectionStore)
	group := int64(100)
	req, ok := runner.logicalRequest(ctx, &group, "conversation", "gpt-test", protocol)
	require.True(t, ok)
	req.AccountID, req.UpstreamKeyID = 1, 1
	for i := range store.rows {
		if store.rows[i].Kind != upstreamcacheguard.SessionKind {
			continue
		}
		var session upstreamcacheguard.SessionState
		require.NoError(t, json.Unmarshal(store.rows[i].Payload, &session))
		previous := session.Routes[session.LastRouteKey]
		session.LastProtocol = protocol
		session.LastRouteKey = upstreamcacheguard.WarmRouteKey(req)
		session.Routes = map[string]upstreamcacheguard.SessionRoute{session.LastRouteKey: previous}
		store.rows[i].Payload, _ = json.Marshal(session)
	}
	require.NoError(t, runner.engine.Refresh(ctx))
}

func TestUpstreamCacheGuardNativeAnthropicWarmRouteCanMigrate(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
			svc, ctx, runner, cache := cacheGuardSelectionFixture(t, advanced, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
			reader := newProtectionPlatformScopeResolver()
			reader.selected[PlatformKimi] = true // Native Anthropic protocol still uses the actual Kimi account platform.
			runner.SetProtectionPolicyResolver(reader)
			svc.SetGroupSchedulingProtectionPolicyResolver(reader)
			accounts := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
			for i := range accounts {
				accounts[i].Platform = PlatformKimi
				accounts[i].Credentials = map[string]any{"api_protocol": APIProtocolAnthropic}
			}
			svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
			cacheGuardRewriteSessionProtocol(t, runner, ctx, "anthropic")
			group := int64(100)
			selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, false, true, PlatformKimi)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(2), selection.Account.ID, "read the same native Anthropic warm context that forwarding observes")
			require.True(t, selection.RequiresStickyAdmissionBinding())
			require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

func TestUpstreamCacheGuardExpiredSourceQualityDoesNotBecomeZeroPercent(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, true, []float64{0, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
	store := runner.store.(*cacheGuardSelectionStore)
	for i := range store.rows {
		if store.rows[i].Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var state upstreamcacheguard.AccountState
		require.NoError(t, json.Unmarshal(store.rows[i].Payload, &state))
		if state.AccountID == 1 {
			state.ValidRequests = 0
			store.rows[i].Payload, _ = json.Marshal(state)
		}
	}
	require.NoError(t, runner.engine.Refresh(ctx))
	group := int64(100)
	selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), selection.Account.ID)
	require.False(t, selection.RequiresStickyAdmissionBinding())
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestUpstreamCacheGuardObserveAndDisabledUseExistingRouting(t *testing.T) {
	for _, mode := range []upstreamcacheguard.Mode{upstreamcacheguard.Disabled, upstreamcacheguard.Observe} {
		for _, advanced := range []bool{false, true} {
			t.Run(string(mode)+"/"+map[bool]string{false: "legacy", true: "advanced"}[advanced], func(t *testing.T) {
				svc, ctx, runner, _ := cacheGuardSelectionFixture(t, advanced, []float64{90, 70}, nil, false, time.Time{}, nil)
				settings := runner.engine.Settings()
				settings.Mode = mode
				require.NoError(t, runner.engine.ApplySettings(settings))
				store := runner.store.(*cacheGuardSelectionStore)
				source := upstreamcacheguard.AccountState{AccountID: 1, UpstreamKeyID: 1, Phase: upstreamcacheguard.Cooling, ValidRequests: 21, CachePercent: 20, CooldownUntil: time.Now().Add(time.Hour)}
				raw, err := json.Marshal(source)
				require.NoError(t, err)
				store.rows = append(store.rows, upstreamcacheguard.RuntimeRow{GroupID: 200, Kind: upstreamcacheguard.AccountKind, Key: "1", Version: 1, Payload: raw})
				store.accounts = nil // Rehydrate the synthetic fixture with the new cross-group row.
				require.NoError(t, runner.engine.Refresh(ctx))
				group := int64(100)
				if mode == upstreamcacheguard.Observe {
					account, fresh := runner.engine.CachedAccount(1, 1)
					require.True(t, fresh)
					require.Equal(t, upstreamcacheguard.Cooling, account.Phase, "observation uses the same cross-group account decision")
				}
				ms := 60000
				account, _ := svc.accountRepo.GetByID(ctx, 1)
				svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &ms)
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.Equal(t, int64(2), selection.Account.ID, "cache observation must not override the existing TTFT route")
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestUpstreamCacheGuardAccountProtectionChangesOnlyNewSessionPreference(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, oldSession := range []bool{false, true} {
			name := map[bool]string{false: "legacy", true: "advanced"}[advanced] + "/" + map[bool]string{false: "new", true: "old_high_cache"}[oldSession]
			t.Run(name, func(t *testing.T) {
				residence := time.Time{}
				if oldSession {
					residence = time.Now().Add(time.Minute)
				}
				svc, ctx, runner, cache := cacheGuardSelectionFixture(t, advanced, []float64{90, 70}, nil, false, residence, nil)
				store := runner.store.(*cacheGuardSelectionStore)
				source := upstreamcacheguard.AccountState{AccountID: 1, UpstreamKeyID: 1, Phase: upstreamcacheguard.Cooling, ValidRequests: 21, CachePercent: 20, CooldownUntil: time.Now().Add(time.Hour)}
				raw, err := json.Marshal(source)
				require.NoError(t, err)
				store.rows = append(store.rows, upstreamcacheguard.RuntimeRow{GroupID: 200, Kind: upstreamcacheguard.AccountKind, Key: "1", Version: 1, Payload: raw})
				store.accounts = nil // Rehydrate the synthetic fixture with the new cross-group row.
				require.NoError(t, runner.engine.Refresh(ctx))
				group := int64(100)
				account, fresh := runner.engine.CachedAccount(1, 1)
				require.True(t, fresh)
				require.Equal(t, upstreamcacheguard.Cooling, account.Phase)
				require.InDelta(t, 55, account.CachePercent, 0.01, "the account window combines both group samples")
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				want := int64(2)
				if oldSession {
					want = 1
					require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
				}
				require.Equal(t, want, selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestUpstreamCacheGuardAccountRefreshFailureKeepsOnlyExistingAffinity(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
	group := int64(100)
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
	require.NoError(t, err)
	store := runner.store.(*cacheGuardSelectionStore)
	store.failAccounts = map[int64]bool{1: true}
	require.Error(t, runner.engine.Refresh(ctx))
	runner.available.Store(false) // Refresh health is independent of account availability.
	failed, usable := runner.engine.CachedAccountForGroup(group, 1, 1)
	require.True(t, usable)
	require.False(t, failed.Fresh)
	healthy, usable := runner.engine.CachedAccountForGroup(group, 2, 2)
	require.True(t, usable)
	require.True(t, healthy.Fresh)

	before := store.trialPlans
	p := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, p.Active)
	require.Equal(t, int64(1), p.KeepAccountID)
	require.False(t, p.Migration)
	require.Empty(t, p.Tiers)
	require.Equal(t, before, store.trialPlans, "stale affinity must not create a trial plan")

	p = buildCacheGuardPreference(ctx, &group, "new-conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, p.Active)
	require.Equal(t, [][]int64{{2}}, p.Tiers, "only independently fresh accounts may receive new preferences")
	require.Zero(t, p.KeepAccountID)

	// Failure to sample the stale source must not prevent its ordinary request
	// from being selected and sent, or revoke the healthy account's preference.
	require.Nil(t, BeginUpstreamCacheGuardRequest(ctx, 1, &group, "conversation", "gpt-test", "openai", &accounts[0]))
	p = buildCacheGuardPreference(ctx, &group, "another-conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, p.Active)
	require.Equal(t, [][]int64{{2}}, p.Tiers)
}

func TestUpstreamCacheGuardStaleSnapshotSkipsNewTrialsAndKeepsRealSchedulerAffinity(t *testing.T) {
	svc, ctx, runner, cache := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return OpenAITTFTGuardConfigSnapshot{} }))
	group := int64(100)
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
	require.NoError(t, err)
	// Exercise the real entry point after the whole snapshot becomes stale;
	// engine tests cover the exact boundary without wall-clock delays.
	time.Sleep(upstreamcacheguard.RefreshLifetime + 20*time.Millisecond)
	runner.available.Store(false)
	store := runner.store.(*cacheGuardSelectionStore)
	before := store.trialPlans
	p := buildCacheGuardPreference(ctx, &group, "new-conversation", "gpt-test", "openai", accounts, nil)
	require.False(t, p.Active)
	require.Equal(t, before, store.trialPlans)
	admission := BeginUpstreamCacheGuardRequest(ctx, 1, &group, "new-conversation", "gpt-test", "openai", &accounts[0])
	require.NotNil(t, admission, "stale preferences must not discard ordinary request facts")
	require.True(t, admission.Admission.CaptureOnly)
	require.False(t, admission.Admission.Trial)
	_, _, err, handled := svc.selectWithCacheGuardPreference(ctx, &group, "", "new-conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, false)
	require.NoError(t, err)
	require.False(t, handled, "a new session must fall back to ordinary scheduling")
	selection, _, err, handled := svc.selectWithCacheGuardPreference(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, false)
	require.NoError(t, err)
	require.True(t, handled, "the OpenAI entry point must retain usable stale affinity")
	require.NotNil(t, selection)
	require.Equal(t, int64(1), selection.Account.ID)
	require.False(t, selection.RequiresStickyAdmissionBinding())
	require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
	require.Equal(t, before, store.trialPlans)
	if selection.ReleaseFunc != nil {
		selection.ReleaseFunc()
	}
}

func TestUpstreamCacheGuardStaleTargetCannotTriggerMigrationOrTrial(t *testing.T) {
	for _, allStale := range []bool{false, true} {
		t.Run(fmt.Sprintf("all_stale_%v", allStale), func(t *testing.T) {
			svc, ctx, runner, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
			store := runner.store.(*cacheGuardSelectionStore)
			store.failAccounts = map[int64]bool{2: true}
			if allStale {
				store.failAccounts[1] = true
			}
			require.Error(t, runner.engine.Refresh(ctx))
			group := int64(100)
			accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
			require.NoError(t, err)
			before := store.trialPlans
			p := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
			require.True(t, p.Active)
			require.Equal(t, int64(1), p.KeepAccountID)
			require.False(t, p.Migration)
			require.Empty(t, p.Tiers)
			require.Equal(t, before, store.trialPlans)
			if allStale {
				p = buildCacheGuardPreference(ctx, &group, "new-conversation", "gpt-test", "openai", accounts, nil)
				require.False(t, p.Active)
				require.Equal(t, before, store.trialPlans)
			}
		})
	}
}

func markCacheGuardSelectionUnknown(t *testing.T, ctx context.Context, runner *UpstreamCacheGuardRunner, id int64) {
	t.Helper()
	store := runner.store.(*cacheGuardSelectionStore)
	identity := cacheGuardSelectionIdentity(id, id)
	a, exists := store.accounts[identity]
	require.True(t, exists)
	anchor := time.Now().UTC()
	a.Phase, a.LastDegradedEvidenceAt = upstreamcacheguard.Unknown, &anchor
	for signature, projection := range a.Policies {
		projection.State.Phase = upstreamcacheguard.Unknown
		projection.State.LastDegradedEvidenceAt = &anchor
		a.Policies[signature] = projection
	}
	store.accounts[identity] = a
	require.NoError(t, runner.engine.Refresh(ctx))
	snapshot, usable := runner.engine.CachedAccountForGroup(100, id, id)
	require.True(t, usable)
	require.True(t, snapshot.Fresh)
	require.Equal(t, upstreamcacheguard.Unknown, snapshot.Phase)
	require.Greater(t, snapshot.ValidRequests, runner.engine.Settings().MinValidRequests,
		"fixture retains old window samples so only the Unknown phase excludes its rate")
}

func TestUpstreamCacheGuardUnknownEvidenceUsesUnknownCandidatePool(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, true, []float64{90, 89}, nil, false, time.Time{}, nil)
	markCacheGuardSelectionUnknown(t, ctx, runner, 1)
	group := int64(100)
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
	require.NoError(t, err)
	p := buildCacheGuardPreference(ctx, &group, "new-conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, p.Active)
	require.Len(t, p.Tiers, 2, "expired 90%% must not share the healthy 89%% rate band")
	require.ElementsMatch(t, [][]int64{{1}, {2}}, p.Tiers,
		"Unknown keeps its ordinary unknown-pool opportunity, regardless of its old sample count")
	require.False(t, p.Migration)
}

func TestUpstreamCacheGuardUnknownTargetCannotTriggerMigration(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
	markCacheGuardSelectionUnknown(t, ctx, runner, 2)
	group := int64(100)
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
	require.NoError(t, err)
	p := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, p.Active)
	require.Equal(t, int64(1), p.KeepAccountID)
	require.False(t, p.Migration, "expired target evidence cannot justify migrating an existing session")
	require.Empty(t, p.Tiers)
}
