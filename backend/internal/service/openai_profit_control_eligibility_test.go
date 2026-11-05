package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

func TestOpenAIProfitControlEligibilityVetoReason(t *testing.T) {
	threshold := 0.7
	rate := func(value float64) *Account { return &Account{RateMultiplier: &value} }
	gate := &openAIProfitControlGate{groupID: 92001234567, platform: PlatformOpenAI, threshold: threshold}
	exempt := *gate
	exempt.zeroRateExempt = true
	for _, tc := range []struct {
		name    string
		gate    *openAIProfitControlGate
		account *Account
		reason  string
	}{
		{"no_gate", nil, &Account{}, ""},
		{"nil_account", gate, nil, ""},
		{"explicit_zero_exempt", &exempt, &Account{}, ""},
		{"below_threshold", gate, rate(0.5), ""},
		{"threshold_equal", gate, rate(threshold), ""},
		{"epsilon", gate, rate(threshold + 1e-12), ""},
		{"above_threshold", gate, rate(0.8), openAIProfitFilterReasonThreshold},
		{"free_upstream", gate, rate(0), ""},
		{"missing_rate", gate, &Account{}, openAIProfitFilterReasonInvalidAccountRate},
		{"negative_rate", gate, rate(-1), openAIProfitFilterReasonInvalidAccountRate},
		{"nan_rate", gate, rate(math.NaN()), openAIProfitFilterReasonInvalidAccountRate},
		{"positive_inf", gate, rate(math.Inf(1)), openAIProfitFilterReasonInvalidAccountRate},
		{"negative_inf", gate, rate(math.Inf(-1)), openAIProfitFilterReasonInvalidAccountRate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, tc.gate)
			vetoed, reason := openAIProfitControlEligibilityVetoReason(ctx, tc.account)
			require.Equal(t, tc.reason != "", vetoed)
			require.Equal(t, tc.reason, reason)
		})
	}

	stats := openAIProfitControlObserverInstance.stats(gate.groupID, gate.platform)
	t.Cleanup(func() {
		openAIProfitControlObserverInstance.groups.Delete(profitControlObserverKey(gate.groupID, gate.platform))
	})
	beforeThreshold, beforeInvalid := stats.vetoThreshold.Load(), stats.vetoInvalidRate.Load()
	ctx := context.WithValue(context.Background(), openAIProfitControlGateCtxKey{}, gate)
	for range 3 {
		vetoed, _ := openAIProfitControlEligibilityVetoReason(ctx, rate(0.8))
		require.True(t, vetoed)
		vetoed, _ = openAIProfitControlEligibilityVetoReason(ctx, &Account{})
		require.True(t, vetoed)
	}
	require.Equal(t, beforeThreshold, stats.vetoThreshold.Load(), "候选预筛不得增加实际否决观测")
	require.Equal(t, beforeInvalid, stats.vetoInvalidRate.Load())
	vetoed, reason := openAIProfitControlVetoReason(ctx, rate(0.8))
	require.True(t, vetoed)
	require.Equal(t, openAIProfitFilterReasonThreshold, reason)
	require.Equal(t, beforeThreshold+1, stats.vetoThreshold.Load(), "实际选号与终检仍保留原否决观测")
	vetoed, reason = openAIProfitControlVetoReason(ctx, &Account{})
	require.True(t, vetoed)
	require.Equal(t, openAIProfitFilterReasonInvalidAccountRate, reason)
	require.Equal(t, beforeInvalid+1, stats.vetoInvalidRate.Load())
}

type profitControlSelectionReadRepo struct {
	schedulerTestOpenAIAccountRepo
	onList func(context.Context, int64)
}

func (r profitControlSelectionReadRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	r.onList(ctx, groupID)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (r profitControlSelectionReadRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	r.onList(ctx, groupID)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupID(ctx, groupID)
}

func (r profitControlSelectionReadRepo) ListUpstreamSchedulingEnabledAccountIDs(_ context.Context, accountIDs []int64) ([]int64, error) {
	return append([]int64(nil), accountIDs...), nil
}

func TestProfitControlGatePrecedesProtectionCandidateReads(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, mode := range []string{"direct", "existing_gate", "composite", "suppressed", "disabled"} {
			name := map[bool]string{false: "legacy", true: "advanced"}[advanced] + "/" + mode
			t.Run(name, func(t *testing.T) {
				svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{95}, nil, false, time.Time{}, nil)
				group := profitControlTestGroup(100, 0.5, 0)
				ctx = context.WithValue(ctx, ctxkey.Group, group)
				pricingAt := time.Date(2026, time.November, 5, 1, 2, 3, 0, time.UTC)
				ctx = context.WithValue(ctx, openAIPricingAtCtxKey{}, pricingAt)
				var expectedGate *openAIProfitControlGate
				switch mode {
				case "existing_gate":
					ctx = svc.withOpenAIProfitControlGate(ctx, &group.ID)
					expectedGate, _ = ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
					group.ProfitMinMargin = 0.9
				case "composite":
					billing := profitControlTestGroup(1001, 0, 0)
					billing.Platform = PlatformComposite
					group.RateMultiplier = 99
					ctx = context.WithValue(ctx, ctxkey.Group, billing)
					svc.schedulerSnapshot = &SchedulerSnapshotService{groupRepo: profitControlGroupRepo{group: group}}
				case "suppressed":
					ctx = WithOpenAIProfitControlSuppressed(ctx)
				case "disabled":
					group.ProfitControlEnabled = false
				}
				base := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
				profitControlTestAccountWithRate(&base.accounts[0], 0.3)
				reads := 0
				svc.accountRepo = profitControlSelectionReadRepo{schedulerTestOpenAIAccountRepo: base, onList: func(readCtx context.Context, groupID int64) {
					reads++
					require.Equal(t, group.ID, groupID)
					require.Equal(t, pricingAt, OpenAIPricingAtFromContext(readCtx), "保护预筛不得重新冻结计费时刻")
					gate, _ := readCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
					if mode == "suppressed" || mode == "disabled" {
						require.Nil(t, gate)
						return
					}
					require.NotNil(t, gate, "读取保护候选之前就应有当前实际分组的利润门")
					require.Equal(t, group.ID, gate.groupID)
					require.Equal(t, pricingAt, gate.pricingAt)
					require.InDelta(t, 0.5, gate.threshold, 1e-12, "composite 的收入倍率应来自计费父组")
					if expectedGate != nil {
						require.Same(t, expectedGate, gate, "同组请求重入必须复用原利润门")
					}
				}}
				if svc.schedulerSnapshot != nil {
					svc.schedulerSnapshot.accountRepo = svc.accountRepo
				}
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group.ID, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				if result.ReleaseFunc != nil {
					t.Cleanup(result.ReleaseFunc)
				}
				require.Positive(t, reads)
				require.Equal(t, int64(1), result.Account.ID)
				if mode != "suppressed" && mode != "disabled" {
					require.True(t, result.ProfitGateActive(), "handler 必须能重放实际选号使用的利润门")
				}
			})
		}
	}
}

func TestGatewayProtectionProfitPrefilterPrecedesTrialNomination(t *testing.T) {
	for _, tc := range []struct {
		name        string
		rates       []float64
		wantIDs     []int64
		wantPlans   int
		token       bool
		gateEnabled bool
	}{
		{"mixed", []float64{0.8, 0.3}, []int64{2}, 1, true, true},
		{"all_rejected", []float64{0.8, 0.9}, nil, 0, true, true},
		{"non_token_request", []float64{0.8, 0.9}, []int64{1, 2}, 1, false, true},
		{"disabled_gate", []float64{0.8, 0.9}, []int64{1, 2}, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{95, 90}, nil, false, time.Time{}, nil)
			base := original.accountRepo.(schedulerTestOpenAIAccountRepo)
			for i, rate := range tc.rates {
				profitControlTestAccountWithRate(&base.accounts[i], rate)
			}
			group := profitControlTestGroup(100, 0.5, 0)
			group.ProfitControlEnabled = tc.gateEnabled
			ctx = context.WithValue(ctx, ctxkey.Group, group)
			var pricingAt time.Time
			if tc.token {
				ctx, pricingAt = WithGatewayTokenRequestPricing(ctx)
			}
			svc := &GatewayService{accountRepo: base, cfg: original.cfg, cache: original.cache}
			store := runner.store.(*cacheGuardSelectionStore)
			beforePlans := store.trialPlans
			preference, guardCtx, effectiveGroupID, _ := svc.gatewayCacheGuardPreference(ctx, &group.ID, "conversation", "gpt-test", nil)
			require.NotNil(t, effectiveGroupID)
			require.Equal(t, group.ID, *effectiveGroupID)
			require.ElementsMatch(t, tc.wantIDs, preference.CandidateIDs, "利润不合格账号不能参与保护分档和试用提名")
			require.Equal(t, beforePlans+tc.wantPlans, store.trialPlans, "全部利润不合格时不得提前分配自然试用序号")
			for _, tier := range preference.Tiers {
				for _, id := range tier {
					require.Contains(t, tc.wantIDs, id)
				}
			}
			gate, _ := guardCtx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
			if tc.token && tc.gateEnabled {
				require.NotNil(t, gate)
				require.Equal(t, pricingAt, gate.pricingAt, "候选预筛与后续选号使用同一请求计费时刻")
				require.Equal(t, group.ID, gate.groupID)
				require.InDelta(t, 0.5, gate.threshold, 1e-12)
			} else {
				require.Nil(t, gate, "非token和未启用利润门的原边界保持不变")
			}
		})
	}
}
