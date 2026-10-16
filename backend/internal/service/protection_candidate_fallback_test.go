package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

// The directory changes after protection has read its hint. Per-ID reads are
// authoritative throughout, while ordinary fallback must fetch the new list.
type protectionFallbackAccountRepo struct {
	AccountRepository
	initial, current []Account
	latest           map[int64]Account
	lists, gets      int
}

func (r *protectionFallbackAccountRepo) directory() ([]Account, error) {
	r.lists++
	if r.lists == 1 {
		return append([]Account(nil), r.initial...), nil
	}
	return append([]Account(nil), r.current...), nil
}
func (r *protectionFallbackAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.gets++
	if account, ok := r.latest[id]; ok {
		return &account, nil
	}
	return nil, ErrNoAvailableAccounts
}
func (r *protectionFallbackAccountRepo) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return r.directory()
}
func (r *protectionFallbackAccountRepo) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return r.directory()
}
func (r *protectionFallbackAccountRepo) ListSchedulableByPlatform(context.Context, string) ([]Account, error) {
	return r.directory()
}

func TestGatewayProtectionPublicFallbackReloadsRejectedDirectory(t *testing.T) {
	for _, entry := range []string{"model", "load"} {
		for _, change := range []string{"disabled", "key_changed"} {
			for _, newAccount := range []bool{false, true} {
				name := entry + "/" + change + map[bool]string{false: "/empty", true: "/replacement"}[newAccount]
				t.Run(name, func(t *testing.T) {
					original, ctx, _, _ := cacheGuardSelectionFixture(t, false, []float64{95}, nil, false, time.Now().Add(time.Hour), nil)
					first := original.accountRepo.(schedulerTestOpenAIAccountRepo).accounts[0]
					first.AccountGroups = []AccountGroup{{AccountID: first.ID, GroupID: 100}}
					latest := first
					if change == "disabled" {
						latest.Schedulable = false
					} else {
						key := int64(99)
						latest.UpstreamKeyID = &key
					}
					replacement := first
					replacement.ID = 3
					key, upstream := int64(3), int64(3)
					replacement.UpstreamKeyID = &key
					replacement.UpstreamConfigID = &upstream
					replacement.AccountGroups = []AccountGroup{{AccountID: 3, GroupID: 100}}
					repo := &protectionFallbackAccountRepo{initial: []Account{first}, latest: map[int64]Account{first.ID: latest, replacement.ID: replacement}}
					if newAccount {
						repo.current = []Account{replacement}
					}
					group := &Group{ID: 100, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, SubscriptionType: SubscriptionTypeStandard}
					ctx = context.WithValue(ctx, ctxkey.Group, group)
					cfg := &config.Config{}
					cfg.Gateway.Scheduling.LoadBatchEnabled = true
					var acquired []int64
					svc := &GatewayService{accountRepo: repo, cfg: cfg, concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquiredIDs: &acquired})}
					var selected *Account
					var err error
					if entry == "model" {
						selected, err = svc.SelectAccountForModelWithExclusions(ctx, &group.ID, "conversation", "gpt-test", nil)
					} else {
						var result *AccountSelectionResult
						result, err = svc.SelectAccountWithLoadAwareness(ctx, &group.ID, "conversation", "gpt-test", nil, "", 0)
						if result != nil {
							selected = result.Account
							if result.ReleaseFunc != nil {
								defer result.ReleaseFunc()
							}
						}
					}
					require.Positive(t, repo.gets, "the protection tier must reject the authoritative changed account")
					require.NotContains(t, acquired, first.ID, "a stale nomination must fail before capacity acquisition")
					require.GreaterOrEqual(t, repo.lists, 2, "ordinary fallback must independently reload its directory")
					if newAccount {
						require.NoError(t, err)
						require.NotNil(t, selected)
						require.Equal(t, int64(3), selected.ID)
						require.True(t, selected.IsSchedulable())
						require.Equal(t, int64(3), accountUpstreamKeyID(selected))
					} else {
						require.ErrorIs(t, err, ErrNoAvailableAccounts)
						require.Nil(t, selected)
					}
				})
			}
		}
	}
}

func TestOpenAIProtectionInternalFallbackReloadsRejectedDirectory(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, newAccount := range []bool{false, true} {
			t.Run(map[bool]string{false: "legacy", true: "advanced"}[advanced]+map[bool]string{false: "/empty", true: "/replacement"}[newAccount], func(t *testing.T) {
				svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{60, 95}, map[int64]bool{1: true}, true, time.Time{}, nil)
				initial := svc.accountRepo.(schedulerTestOpenAIAccountRepo).accounts
				source, target := initial[0], initial[1]
				source.Schedulable = false
				key := int64(22)
				target.UpstreamKeyID = &key
				replacement := initial[1]
				replacement.ID = 3
				key, upstream := int64(3), int64(3)
				replacement.UpstreamKeyID = &key
				replacement.UpstreamConfigID = &upstream
				repo := &protectionFallbackAccountRepo{initial: initial, latest: map[int64]Account{1: source, 2: target, 3: replacement}}
				if newAccount {
					repo.current = []Account{replacement}
				}
				svc.accountRepo = repo
				svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return OpenAITTFTGuardConfigSnapshot{} }))
				group := int64(100)
				result, _, err, handled := svc.selectWithCacheGuardPreference(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, false)
				require.True(t, handled, "exercise the protection function's internal ordinary fallback")
				require.Positive(t, repo.gets)
				require.GreaterOrEqual(t, repo.lists, 2, "the changed Key's old evidence cannot keep the original candidate pool alive")
				if result != nil && result.ReleaseFunc != nil {
					defer result.ReleaseFunc()
				}
				if newAccount {
					require.NoError(t, err)
					require.NotNil(t, result)
					require.Equal(t, int64(3), result.Account.ID)
					require.True(t, result.Account.IsSchedulable())
					require.Equal(t, int64(3), accountUpstreamKeyID(result.Account))
					require.False(t, result.IsSchedulingProtectionMigration(), "ordinary fallback cannot borrow prior cache evidence for migration")
				} else {
					require.ErrorIs(t, err, ErrNoAvailableAccounts)
					require.Nil(t, result)
				}
			})
		}
	}
}

func TestWithoutProtectionCandidateReadsLeavesOriginalHintIsolated(t *testing.T) {
	ctx := withProtectionCandidateReads(context.Background())
	owner := &GatewayService{}
	key := protectionCandidateKey(owner, nil, PlatformOpenAI, false)
	rememberProtectionCandidates(ctx, key, []Account{{ID: 1}}, false)
	ordinary := withoutProtectionCandidateReads(ctx)
	_, _, known := readProtectionCandidates(ordinary, key)
	require.False(t, known)
	rememberProtectionCandidates(ordinary, key, []Account{{ID: 2}}, false)
	original, _, known := readProtectionCandidates(ctx, key)
	require.True(t, known)
	require.Equal(t, int64(1), original[0].ID)
}
