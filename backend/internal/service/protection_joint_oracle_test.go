package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

// The version-two contract intentionally makes quality precede cache benefit
// in every priority mode. The pre-optimization trace remains in testdata for
// historical provenance; the current trace runs in a fixed-clock synctest bubble.
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

func TestJointProtectionMatchesQualityFirstV2Golden(t *testing.T) {
	var output jointOracleOutput
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
								accounts[2].Priority = 2 // a worse outer priority pool cannot win on quality.
								svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
							}
							preference := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
							trace := jointOracleTrace{Name: name, CacheTiers: preference.Tiers, Keep: preference.KeepAccountID, MigrationPlan: preference.Migration, StickyBefore: sticky.sessionBindings[svc.openAISessionCacheKey("conversation")]}
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
		return
	}
	expected, err := os.ReadFile("testdata/protection_joint_quality_first_v2.json")
	require.NoError(t, err)
	require.JSONEq(t, string(expected), string(actual))
}
