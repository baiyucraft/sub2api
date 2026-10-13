package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// State-machine and SQL tests cover snapshot production separately. These
// fixtures exercise both real scheduler implementations against a frozen shared
// read snapshot, including actual slot acquisition and sticky cache writes.
type cacheGuardSelectionStore struct {
	upstreamcacheguard.Store
	rows []upstreamcacheguard.RuntimeRow
}

func (s *cacheGuardSelectionStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return s.rows, nil
}
func (s *cacheGuardSelectionStore) Transact(context.Context, int64, func(upstreamcacheguard.Tx) error) error {
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
				{"high_cache_slow", []float64{90, 70}, nil, map[int64]int{1: 60000}, nil, 1},
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
			svc, ctx, _, _ := cacheGuardSelectionFixture(t, true, []float64{60, 90}, map[int64]bool{1: true}, true, time.Now().Add(5*time.Minute), nil)
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
			require.Greater(t, repo.reads, 1)
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
				require.NoError(t, runner.engine.Refresh(ctx))
				group := int64(100)
				if mode == upstreamcacheguard.Observe {
					snapshot := runner.engine.CachedSnapshot(group)
					require.NotNil(t, snapshot.Accounts[0].DerivedProtection, "observe displays the linked source")
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

func TestUpstreamCacheGuardLinkedGroupProtectionChangesOnlyNewSessionPreference(t *testing.T) {
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
				require.NoError(t, runner.engine.Refresh(ctx))
				group := int64(100)
				snapshot := runner.engine.CachedSnapshot(group)
				require.Equal(t, upstreamcacheguard.Healthy, snapshot.Accounts[0].Phase)
				require.Equal(t, 90.0, snapshot.Accounts[0].CachePercent)
				require.NotNil(t, snapshot.Accounts[0].DerivedProtection)
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
