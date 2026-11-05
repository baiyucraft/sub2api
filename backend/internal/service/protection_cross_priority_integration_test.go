package service

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Count directory reads separately from terminal account reads: crossing a soft
// priority boundary must reuse the same immutable directory, while each actual
// acquisition keeps its normal current-account check.
type crossPriorityCountingAccountRepo struct {
	schedulerTestOpenAIAccountRepo
	directoryReads atomic.Int64
	accountReads   atomic.Int64
}

func (r *crossPriorityCountingAccountRepo) GetByID(ctx context.Context, id int64) (*Account, error) {
	r.accountReads.Add(1)
	return r.schedulerTestOpenAIAccountRepo.GetByID(ctx, id)
}

func (r *crossPriorityCountingAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	r.directoryReads.Add(1)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupIDAndPlatform(ctx, groupID, platform)
}

func (r *crossPriorityCountingAccountRepo) ListSchedulableByGroupID(ctx context.Context, groupID int64) ([]Account, error) {
	r.directoryReads.Add(1)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByGroupID(ctx, groupID)
}

func (r *crossPriorityCountingAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]Account, error) {
	r.directoryReads.Add(1)
	return r.schedulerTestOpenAIAccountRepo.ListSchedulableByPlatform(ctx, platform)
}

func crossPriorityConfigureAccounts(t *testing.T, svc *OpenAIGatewayService, priorities ...int) []Account {
	t.Helper()
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(context.Background(), 100)
	require.NoError(t, err)
	require.Len(t, accounts, len(priorities))
	for i, priority := range priorities {
		accounts[i].Priority = priority
	}
	svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
	return accounts
}

func crossPriorityAdvancedMode(svc *OpenAIGatewayService, advanced, stickyWeighted bool) {
	resetOpenAIAdvancedSchedulerSettingCacheForTest()
	svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(strconv.FormatBool(advanced), strconv.FormatBool(stickyWeighted))
}

func TestProtectionCrossPriorityRealSchedulersApplyProtectionBeforePriority(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, stickyWeighted := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/sticky_weighted=%t", advanced, stickyWeighted), func(t *testing.T) {
				svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{20, 90, 89}, map[int64]bool{1: true}, false, time.Time{}, nil)
				crossPriorityAdvancedMode(svc, advanced, stickyWeighted)
				crossPriorityConfigureAccounts(t, svc, 1, 4, 3)
				group := int64(100)
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.True(t, selection.Acquired)
				// Accounts 2/3 share the same five-point cache band. The built-in
				// priority rule chooses 3 inside that band, but protected account 1
				// cannot win merely because its priority is numerically smaller.
				require.Equal(t, int64(3), selection.Account.ID)
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionCrossPriorityPreviousResponseHonorsMovability(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, stickyWeighted := range []bool{false, true} {
			for _, canMove := range []bool{false, true} {
				t.Run(fmt.Sprintf("advanced=%t/sticky_weighted=%t/can_move=%t", advanced, stickyWeighted, canMove), func(t *testing.T) {
					svc, ctx, _, sticky := cacheGuardSelectionFixture(t, advanced, []float64{20, 95}, map[int64]bool{1: true}, true, time.Time{}, nil)
					crossPriorityAdvancedMode(svc, advanced, stickyWeighted)
					crossPriorityConfigureAccounts(t, svc, 3, 4)
					group := int64(100)
					const responseID = "resp_cross_priority_owner"
					require.NoError(t, svc.getOpenAIWSStateStore().BindResponseAccount(ctx, group, responseID, 1, time.Hour))
					selection, _, err := svc.SelectAccountWithSchedulerForCapability(ctx, &group, responseID, "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", false, canMove, true)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.True(t, selection.Acquired)
					want := int64(1)
					if canMove {
						want = 2
					}
					require.Equal(t, want, selection.Account.ID)
					require.Equal(t, canMove, selection.IsSchedulingProtectionMigration())
					require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")], "selection cannot commit migration before final send admission")
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				})
			}
		}
	}
}

func TestProtectionCrossPriorityMigrationFallsBackToLegalSource(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, stickyWeighted := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/sticky_weighted=%t", advanced, stickyWeighted), func(t *testing.T) {
				svc, ctx, _, sticky := cacheGuardSelectionFixture(t, advanced, []float64{20, 95}, map[int64]bool{1: true}, true, time.Time{}, map[int64]bool{1: true, 2: false})
				crossPriorityAdvancedMode(svc, advanced, stickyWeighted)
				crossPriorityConfigureAccounts(t, svc, 3, 4)
				group := int64(100)
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.True(t, selection.Acquired)
				require.Equal(t, int64(1), selection.Account.ID)
				require.False(t, selection.IsSchedulingProtectionMigration())
				require.Equal(t, int64(1), sticky.sessionBindings[svc.openAISessionCacheKey("conversation")])
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionCrossPriorityProfitVetoPrecedesProtectedPreference(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			for _, affordableTarget := range []bool{false, true} {
				t.Run(fmt.Sprintf("advanced=%t/migration=%t/affordable_target=%t", advanced, pending, affordableTarget), func(t *testing.T) {
					rates := []float64{20, 95}
					priorities := []int{3, 4}
					if affordableTarget {
						rates, priorities = append(rates, 85), append(priorities, 5)
					}
					svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, rates, map[int64]bool{1: true}, pending, time.Time{}, nil)
					accounts := crossPriorityConfigureAccounts(t, svc, priorities...)
					for i := range accounts {
						rate := 0.3
						if accounts[i].ID == 2 {
							rate = 0.8
						}
						accounts[i].RateMultiplier = &rate
					}
					svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
					ctx = context.WithValue(ctx, openAIProfitControlGateCtxKey{}, &openAIProfitControlGate{groupID: 100, platform: PlatformOpenAI, threshold: 0.5, pricingAt: time.Now()})
					attempts := []int64{}
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &attempts})
					group := int64(100)
					selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					want := int64(1)
					if affordableTarget {
						want = 3
					}
					require.Equal(t, want, selection.Account.ID)
					require.NotContains(t, attempts, int64(2), "an unprofitable account must be excluded before acquiring capacity, even when it has the best cache rate")
					require.True(t, selection.ProfitGateActive())
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
				})
			}
		}
	}
}

func TestProtectionCrossPriorityReusesDirectoryAndExistingRechecks(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, pending := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/migration=%t", advanced, pending), func(t *testing.T) {
				type counts struct {
					directory, account int64
					attempts           []int64
				}
				run := func(t *testing.T, crossPool bool) counts {
					t.Helper()
					svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{20, 95, 85}, map[int64]bool{1: true}, pending, time.Time{}, nil)
					priorities := []int{3, 3, 3}
					if crossPool {
						priorities = []int{3, 4, 5}
					}
					accounts := crossPriorityConfigureAccounts(t, svc, priorities...)
					repo := &crossPriorityCountingAccountRepo{schedulerTestOpenAIAccountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}}
					svc.accountRepo = repo
					attempts := []int64{}
					svc.concurrencyService = NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: true, 2: false, 3: true}, acquiredIDs: &attempts})
					group := int64(100)
					selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.Equal(t, int64(3), selection.Account.ID)
					require.True(t, selection.Acquired)
					if selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					return counts{directory: repo.directoryReads.Load(), account: repo.accountReads.Load(), attempts: attempts}
				}
				var baseline, crossPool counts
				t.Run("same_priority", func(t *testing.T) { baseline = run(t, false) })
				t.Run("cross_priority", func(t *testing.T) { crossPool = run(t, true) })
				require.Equal(t, int64(1), baseline.directory)
				require.Equal(t, baseline.directory, crossPool.directory, "each protection tier must reuse the request's original directory")
				require.Positive(t, baseline.account, "actual candidates still require account rechecks")
				require.Equal(t, baseline.account, crossPool.account, "crossing Priority must not add account reads")
				require.Equal(t, baseline.attempts, crossPool.attempts, "the original advanced acquire/retry algorithm remains unchanged within each protection tier")
			})
		}
	}
}
