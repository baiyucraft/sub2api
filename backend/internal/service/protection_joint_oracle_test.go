package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// Historical pre-optimization and quality-first-v2 traces stay frozen. Version
// four admits all qualified ordinary accounts to one native pool. For those
// deliberately changed cases an independent call to the unwrapped scheduler is
// the reference for its existing bounded acquisition/priority behavior. All
// unchanged routing, trial and recovery fields still use the historical oracle.
// The current trace runs in a fixed-clock synctest bubble.
// Account selection has a deterministic seed derived from the same session,
// model and group. The scenario generator uses a separate fixed random source.
// SUB2API_JOINT_ORACLE_OUTPUT exports an actual trace for an isolated comparison;
// it never overwrites the checked-in golden implicitly.
type jointOracleTrace struct {
	Name          string    `json:"name"`
	CacheTiers    [][]int64 `json:"cache_tiers"`
	Keep          int64     `json:"keep"`
	MigrationPlan bool      `json:"migration_plan"`
	AttemptOrder  []int64   `json:"attempt_order"`
	Account       int64     `json:"account"`
	Acquired      bool      `json:"acquired"`
	Migration     bool      `json:"migration"`
	StickyBefore  int64     `json:"sticky_before"`
	StickyAfter   int64     `json:"sticky_after"`
	Quota         int64     `json:"quota"`
}

type jointOracleOutput struct {
	Selections []jointOracleTrace             `json:"selections"`
	TrialPlans []upstreamcacheguard.TrialPlan `json:"trial_plans"`
}

// The existing scheduler fixture supplies immutable account observations. This
// transactional overlay adds real persistent group/session state for PlanTrial,
// including duplicate requests and rounds that must retain their first ordinal.
type jointOracleStore struct {
	*cacheGuardSelectionStore
	group int64
	data  map[string]json.RawMessage
}

type jointOracleTx struct {
	upstreamcacheguard.Tx
	store *jointOracleStore
}

func (s *jointOracleStore) Transact(ctx context.Context, group int64, fn func(upstreamcacheguard.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.group = group
	return fn(&jointOracleTx{store: s})
}

func (tx *jointOracleTx) Read(kind, key string, out any) (int64, error) {
	raw := tx.store.data[kind+":"+key]
	if raw == nil {
		return 0, nil
	}
	return 1, json.Unmarshal(raw, out)
}

func (tx *jointOracleTx) Write(kind, key string, value any) (int64, error) {
	raw, err := json.Marshal(value)
	if err == nil {
		tx.store.data[kind+":"+key] = raw
	}
	return 1, err
}

func (s *jointOracleStore) quota(t *testing.T) int64 {
	t.Helper()
	var count int64
	for key, raw := range s.data {
		if len(key) > len(upstreamcacheguard.GroupKind) && key[:len(upstreamcacheguard.GroupKind)+1] == upstreamcacheguard.GroupKind+":" {
			var state upstreamcacheguard.GroupState
			require.NoError(t, json.Unmarshal(raw, &state))
			count += state.NaturalNewCount
		}
	}
	return count
}

func TestJointProtectionMatchesQualifiedPoolV4Contract(t *testing.T) {
	var output jointOracleOutput
	nativeQualified := map[string]jointOracleTrace{}
	synctest.Test(t, func(t *testing.T) {
		rng := rand.New(rand.NewSource(1017))
		for _, advanced := range []bool{false, true} {
			for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
				for scenario := 0; scenario < 12; scenario++ {
					for _, capacityMode := range []string{"available", "best_full", "all_full"} {
						func() {
							name := fmt.Sprintf("advanced=%t/%s/scenario=%d/%s", advanced, priority, scenario, capacityMode)
							resetOpenAIAdvancedSchedulerSettingCacheForTest()
							rates := []float64{float64(55 + rng.Intn(8)), float64(85 + rng.Intn(8)), float64(70 + rng.Intn(8))}
							protected := map[int64]bool{}
							pending := scenario%3 == 1
							residence := time.Time{}
							if pending {
								protected[1] = true
							}
							if scenario%3 == 2 {
								residence = time.Now().Add(time.Minute)
							}
							capacity := map[int64]bool{1: true, 2: true, 3: true}
							if capacityMode != "available" {
								capacity[2] = false
							}
							if capacityMode == "all_full" {
								capacity[1], capacity[3] = false, false
							}
							svc, ctx, runner, sticky := cacheGuardSelectionFixture(t, advanced, rates, protected, pending, residence, capacity)
							identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
							identity.RequestID = fmt.Sprintf("oracle-%d", scenario)
							ctx = context.WithValue(ctx, cacheGuardSchedulingKey{}, identity)
							store := &jointOracleStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore), data: map[string]json.RawMessage{}}
							runner.store, runner.engine = store, upstreamcacheguard.NewEngine(store)
							settings := DefaultUpstreamCacheGuardSettings()
							settings.Mode = upstreamcacheguard.Protect
							require.NoError(t, runner.engine.ApplySettings(settings))
							require.NoError(t, runner.engine.Refresh(ctx))
							cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
							svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: priority, cfg: cfg})
							group := int64(100)
							accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
							require.NoError(t, err)
							if scenario >= 3 {
								ms := []int{1000, 35000, 60000}[scenario%3]
								for i := 0; i < 5; i++ {
									svc.ReportOpenAIAccountScheduleResultForGroup(&group, &accounts[1], "gpt-test", true, &ms)
								}
							}
							if scenario >= 6 {
								accounts[2].Priority = 2 // Protection may choose 3 before the worse-cache priority-one account.
								svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
							}
							preference := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
							trace := jointOracleTrace{Name: name, CacheTiers: preference.Tiers, Keep: preference.KeepAccountID, MigrationPlan: preference.Migration, StickyBefore: sticky.sessionBindings[svc.openAISessionCacheKey("conversation")]}
							if scenario%3 != 2 {
								// The fixture has 21 valid requests per account and a 75%
								// protection threshold: account 1 is always protected,
								// account 2 never is, and account 3 is qualified only at
								// 75% or higher. Account 2 has bad TTFT in pending cases
								// from scenario 4 onwards. Build this finite contract
								// independently of the production classification helpers.
								require.Equal(t, float64(75), settings.ThresholdPercent)
								require.Equal(t, 20, settings.MinValidRequests)
								tiers := [][]int64{{2}, {3}}
								if pending && scenario >= 3 {
									tiers = [][]int64{{3}}
								} else if rates[2] >= 75 {
									tiers = [][]int64{{2, 3}}
								}
								if !pending {
									tiers = append(tiers, []int64{1})
								}
								nativeQualified[name] = jointOracleNativeQualifiedReference(t, svc, ctx, group, capacity, sticky, tiers, pending)
							}
							attempted := []int64{}
							svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: capacity, acquiredIDs: &attempted})
							selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
							require.NoError(t, err)
							require.NotNil(t, selection)
							trace.AttemptOrder, trace.Account, trace.Acquired = attempted, selection.Account.ID, selection.Acquired
							trace.Migration = selection.IsSchedulingProtectionMigration()
							if selection.Acquired {
								require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ContextWithSelectionProfitGate(ctx, selection), &group, "conversation", selection.Account.ID))
							}
							trace.StickyAfter = sticky.sessionBindings[svc.openAISessionCacheKey("conversation")]
							trace.Quota = store.quota(t)
							if selection.ReleaseFunc != nil {
								selection.ReleaseFunc()
							}
							output.Selections = append(output.Selections, trace)
						}()
					}
				}
			}
		}
		// An independent pair of engines shares the same transactional session and
		// group store. A changed request/model on the same identity is not new.
		_, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, false, time.Time{}, nil)
		store := &jointOracleStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore), data: map[string]json.RawMessage{}}
		engines := []*upstreamcacheguard.Engine{upstreamcacheguard.NewEngine(store), upstreamcacheguard.NewEngine(store)}
		for _, engine := range engines {
			require.NoError(t, engine.ApplySettings(runner.engine.Settings()))
		}
		for i := 1; i <= 100; i++ {
			request := upstreamcacheguard.Request{ID: fmt.Sprintf("request-%d", i), LogicalSessionDigest: fmt.Sprintf("%064x", i), APIKeyID: 1, GroupID: 100, Model: "gpt-test", Protocol: "openai", StartedAt: time.Now()}
			plan, err := engines[i%2].PlanTrial(ctx, request)
			require.NoError(t, err)
			again, err := engines[(i+1)%2].PlanTrial(ctx, request)
			require.NoError(t, err)
			require.Equal(t, plan, again)
			output.TrialPlans = append(output.TrialPlans, plan)
			request.ID += "-next"
			request.Model = "other-model"
			request.StartedAt = request.StartedAt.Add(time.Hour)
			next, err := engines[i%2].PlanTrial(ctx, request)
			require.NoError(t, err)
			output.TrialPlans = append(output.TrialPlans, next)
		}
		require.Equal(t, int64(100), store.quota(t))
	})
	sort.Slice(output.Selections, func(i, j int) bool { return output.Selections[i].Name < output.Selections[j].Name })
	actual, err := json.MarshalIndent(output, "", "  ")
	require.NoError(t, err)
	if path := os.Getenv("SUB2API_JOINT_ORACLE_OUTPUT"); path != "" {
		require.NoError(t, os.WriteFile(path, append(actual, '\n'), 0600))
	}
	expected, err := os.ReadFile("testdata/protection_joint_quality_first_v2.json")
	require.NoError(t, err)
	var contract jointOracleOutput
	require.NoError(t, json.Unmarshal(jointProtectionQualityFirstV3Expected(t, expected), &contract))
	for i := range contract.Selections {
		trace := &contract.Selections[i]
		if native, changed := nativeQualified[trace.Name]; changed {
			trace.AttemptOrder, trace.Account, trace.Acquired = native.AttemptOrder, native.Account, native.Acquired
			trace.StickyAfter, trace.Migration = native.StickyAfter, native.Migration
		}
	}
	require.Len(t, nativeQualified, 96, "two schedulers, two policies, eight non-resident cases, three capacities")
	want, err := json.Marshal(contract)
	require.NoError(t, err)
	if path := os.Getenv("SUB2API_JOINT_ORACLE_OUTPUT"); path != "" {
		require.NoError(t, os.WriteFile(path+".expected.json", want, 0600))
	}
	require.JSONEq(t, string(want), string(actual))
}

func jointOracleNativeQualifiedReference(t *testing.T, svc *OpenAIGatewayService, ctx context.Context, group int64, capacity map[int64]bool, sticky *schedulerTestGatewayCache, tiers [][]int64, migrating bool) jointOracleTrace {
	t.Helper()
	oldConcurrency := svc.concurrencyService
	attempts := []int64{}
	svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: capacity, acquiredIDs: &attempts})
	cacheKey := svc.openAISessionCacheKey("conversation")
	oldBinding, existed := sticky.sessionBindings[cacheKey]
	defer func() {
		svc.concurrencyService = oldConcurrency
		if existed {
			sticky.sessionBindings[cacheKey] = oldBinding
		} else {
			delete(sticky.sessionBindings, cacheKey)
		}
	}()
	var result *AccountSelectionResult
	for _, tier := range tiers {
		excluded := map[int64]struct{}{1: {}, 2: {}, 3: {}}
		for _, id := range tier {
			delete(excluded, id)
		}
		var err error
		result, _, err = svc.selectAccountWithSchedulerOnce(withCacheGuardTentativeSelection(ctx), &group, "", "conversation", "gpt-test", excluded, nil, true, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, true)
		require.NoError(t, err)
		require.NotNil(t, result)
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
		if result.Acquired {
			break
		}
	}
	if !result.Acquired {
		if migrating {
			// The original owner is a legal capacity fallback, independently of
			// the native algorithm used to select the migration candidates.
			scheduler := &defaultOpenAIAccountScheduler{service: svc, stats: svc.openaiAccountStats}
			var err error
			result, _, err = scheduler.selectBySessionHash(withCacheGuardTentativeSelection(ctx), OpenAIAccountScheduleRequest{GroupID: &group, Platform: PlatformOpenAI, SessionHash: "conversation", StickyAccountID: 1, PreserveStickyBinding: true, DisableStickyEscape: true, RequestedModel: "gpt-test", RequiredTransport: OpenAIUpstreamTransportAny})
			require.NoError(t, err)
		} else {
			var err error
			result, _, err = svc.selectAccountWithSchedulerOnce(ctx, &group, "", "conversation", "gpt-test", nil, nil, true, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, true)
			require.NoError(t, err)
		}
		require.NotNil(t, result)
		if result.ReleaseFunc != nil {
			result.ReleaseFunc()
		}
	}
	trace := jointOracleTrace{Account: result.Account.ID, Acquired: result.Acquired, AttemptOrder: attempts, StickyAfter: sticky.sessionBindings[cacheKey], Migration: migrating && result.Acquired && result.Account.ID != 1}
	if result.Acquired {
		trace.StickyAfter = result.Account.ID
	}
	return trace
}

// The fixture fixes disjoint cache bands: account 2 is 85..92%, account 3 is
// 70..77%, and account 1 is 55..62%. Scenario 6/9 therefore uses 2 -> 3 -> 1
// regardless of account 3's Priority. In scenario 7/10 account 2 has degraded
// TTFT, so an eligible low-cache source migrates to 3; when every slot is full it
// retains source 1. All other scenarios (including residence, quota accounting,
// and the final built-in ordinary fallback) retain the original frozen trace.
func jointProtectionQualityFirstV3Expected(t *testing.T, frozenV2 []byte) []byte {
	t.Helper()
	var expected jointOracleOutput
	require.NoError(t, json.Unmarshal(frozenV2, &expected))
	changed := 0
	for i := range expected.Selections {
		trace := &expected.Selections[i]
		crossNew := strings.Contains(trace.Name, "/scenario=6/") || strings.Contains(trace.Name, "/scenario=9/")
		crossMigration := strings.Contains(trace.Name, "/scenario=7/") || strings.Contains(trace.Name, "/scenario=10/")
		if !crossNew && !crossMigration {
			continue
		}
		changed++
		advanced := strings.HasPrefix(trace.Name, "advanced=true/")
		allFull := strings.HasSuffix(trace.Name, "/all_full")
		bestFull := strings.HasSuffix(trace.Name, "/best_full")
		if crossNew {
			require.Equal(t, [][]int64{{2}, {1}, {3}}, trace.CacheTiers, "the overlay must start from the frozen v2 outer-pool contract")
			trace.CacheTiers = [][]int64{{2}, {3}, {1}}
			if bestFull {
				trace.AttemptOrder = []int64{2, 3}
				if advanced {
					trace.AttemptOrder = []int64{2, 2, 3}
				}
				trace.Account, trace.StickyAfter = 3, 3
			} else if allFull {
				if advanced {
					// Only the first six capacity attempts change. Ordinary
					// scheduling still uses its original priority and retry order.
					require.GreaterOrEqual(t, len(trace.AttemptOrder), 6)
					require.Equal(t, []int64{2, 2, 1, 1, 3, 3}, trace.AttemptOrder[:6])
					trace.AttemptOrder = append([]int64{2, 2, 3, 3, 1, 1}, trace.AttemptOrder[6:]...)
				} else {
					require.Equal(t, []int64{2, 1, 3, 1}, trace.AttemptOrder)
					trace.AttemptOrder = []int64{2, 3, 1, 1}
				}
			}
			continue
		}
		require.Equal(t, [][]int64{{2}}, trace.CacheTiers)
		trace.CacheTiers = [][]int64{{2}, {3}}
		if allFull {
			trace.AttemptOrder = []int64{3, 1}
			if advanced {
				trace.AttemptOrder = []int64{3, 3, 1}
			}
		} else {
			trace.AttemptOrder = []int64{3}
			trace.Account, trace.StickyAfter, trace.Migration = 3, 3, true
		}
	}
	require.Equal(t, 48, changed, "two schedulers, two policies, four changed scenarios and three capacities")
	raw, err := json.Marshal(expected)
	require.NoError(t, err)
	return raw
}
