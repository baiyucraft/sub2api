package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type gatewayProtectionRecheckRepo struct {
	AccountRepository
	account *Account
	err     error
}

func (r gatewayProtectionRecheckRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, r.err
}

type gatewayProtectionRecheckSnapshot struct {
	SchedulerCache
	account *Account
}

func (r *gatewayProtectionRecheckSnapshot) GetAccount(context.Context, int64) (*Account, error) {
	return r.account, nil
}

type gatewayProtectionQuotaCache struct {
	SessionLimitCache
	allowed bool
	err     error
	writes  int
}

func (c *gatewayProtectionQuotaCache) RegisterSession(context.Context, int64, string, int, time.Duration) (bool, error) {
	c.writes++
	return c.allowed, c.err
}

type gatewayProtectionCapacityCache struct {
	ConcurrencyCache
	limit    int
	releases int
}

func (c *gatewayProtectionCapacityCache) AcquireAccountSlot(_ context.Context, _ int64, limit int, _ string) (bool, error) {
	c.limit = limit
	return true, nil
}
func (c *gatewayProtectionCapacityCache) ReleaseAccountSlot(context.Context, int64, string) error {
	c.releases++
	return nil
}

func gatewayProtectionRecheckAccount(platform string) Account {
	key := int64(2)
	return Account{ID: 2, Platform: platform, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 3, UpstreamKeyID: &key, GroupIDs: []int64{100}, AccountGroups: []AccountGroup{{GroupID: 100}}}
}

func TestGatewayProtectionTentativeSessionOnlyCommitsChosenTier(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		cache := &gatewayProtectionQuotaCache{allowed: allowed}
		svc := &GatewayService{sessionLimitCache: cache}
		account := gatewayProtectionRecheckAccount(PlatformAnthropic)
		account.Type = AccountTypeOAuth
		account.Extra = map[string]any{"max_sessions": 1}
		ctx := withCacheGuardTentativeSelection(context.Background())
		require.True(t, svc.checkAndRegisterSession(ctx, &account, "session"))
		require.Zero(t, cache.writes, "discarded tiers cannot create or refresh session quota")
		released := 0
		result := &AccountSelectionResult{Account: &account, Acquired: true, ReleaseFunc: func() { released++ }}
		err := svc.commitGatewayProtectionSession(ctx, result, "session")
		require.Equal(t, 1, cache.writes)
		if allowed {
			require.NoError(t, err)
			require.Zero(t, released)
		} else {
			require.ErrorIs(t, err, ErrNoAvailableAccounts)
			require.Equal(t, 1, released)
			require.Nil(t, result.ReleaseFunc)
		}
	}
	cache := &gatewayProtectionQuotaCache{err: errors.New("redis unavailable")}
	svc := &GatewayService{sessionLimitCache: cache}
	account := gatewayProtectionRecheckAccount(PlatformAnthropic)
	account.Type = AccountTypeOAuth
	account.Extra = map[string]any{"max_sessions": 1}
	require.NoError(t, svc.commitGatewayProtectionSession(context.Background(), &AccountSelectionResult{Account: &account}, "session"), "ordinary fail-open behavior is preserved")
}

func TestGatewayProtectionRechecksCapacityAndRejectsChangedIdentity(t *testing.T) {
	original := gatewayProtectionRecheckAccount(PlatformAnthropic)
	latest := original
	latest.Concurrency = 1
	capacity := &gatewayProtectionCapacityCache{}
	svc := &GatewayService{accountRepo: gatewayProtectionRecheckRepo{account: &latest}, concurrencyService: NewConcurrencyService(capacity)}
	ctx := withCacheGuardTentativeSelection(withGatewayProtectionRecheck(context.Background(), &original.GroupIDs[0], ""))
	slot, err := svc.tryAcquireAccountSlot(ctx, &original)
	require.NoError(t, err)
	require.True(t, slot.Acquired)
	require.Equal(t, 1, capacity.limit, "actual acquisition uses the current capacity")
	_, err = svc.newSelectionResult(ctx, &original, true, slot.ReleaseFunc, nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Equal(t, 1, capacity.releases, "a changed nomination cannot retain its acquired resources")
	latest = original
	key := int64(9)
	latest.UpstreamKeyID = &key
	_, err = svc.newSelectionResult(ctx, &original, true, func() { capacity.releases++ }, nil)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.Equal(t, 2, capacity.releases)
	latest = original
	latest.Schedulable = false
	_, err = svc.tryAcquireAccountSlot(ctx, &original)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestGatewayProtectionTerminalUsesDatabaseOverStaleSnapshot(t *testing.T) {
	selected := gatewayProtectionRecheckAccount(PlatformAnthropic)
	changed := selected
	changed.Schedulable = false
	snapshot := NewSchedulerSnapshotService(&gatewayProtectionRecheckSnapshot{account: &selected}, nil, gatewayProtectionRecheckRepo{}, nil, nil)
	svc := &GatewayService{accountRepo: gatewayProtectionRecheckRepo{account: &changed}, schedulerSnapshot: snapshot}
	ctx := withGatewayProtectionRecheck(context.Background(), &selected.GroupIDs[0], "")
	_, err := svc.refreshGatewayProtectionCandidate(ctx, &selected)
	require.NoError(t, err, "tentative reads may use current scheduler snapshots")
	_, err = svc.refreshGatewayProtectionCandidateAt(ctx, &selected, true)
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "terminal validation observes persisted disable even before snapshot propagation")
}

func TestOpenAIProtectionTerminalDatabaseIdentityAndStateWithoutProfitGate(t *testing.T) {
	for _, change := range []string{"none", "key", "state", "group", "capacity", "repository_error"} {
		t.Run(change, func(t *testing.T) {
			selected := gatewayProtectionRecheckAccount(PlatformOpenAI)
			latest := selected
			switch change {
			case "key":
				key := int64(99)
				latest.UpstreamKeyID = &key
			case "state":
				latest.Schedulable = false
			case "group":
				latest.GroupIDs = []int64{101}
				latest.AccountGroups = []AccountGroup{{GroupID: 101}}
			case "capacity":
				latest.Concurrency = 1
			}
			repo := gatewayProtectionRecheckRepo{account: &latest}
			if change == "repository_error" {
				repo.err = errors.New("database unavailable")
			}
			svc := &OpenAIGatewayService{accountRepo: repo}
			ctx := WithSchedulingProtectionGroup(context.Background(), &selected.GroupIDs[0])
			err := svc.ValidateSchedulingProtectionSend(ctx, &selected)
			if change == "none" {
				require.NoError(t, err)
			} else {
				var retry *UpstreamFailoverError
				require.ErrorAs(t, err, &retry)
				require.True(t, retry.PluginAdmissionRejected)
				require.Equal(t, GatewayFailureScopeRequest, retry.Scope)
			}
		})
	}
}

func TestSchedulingProtectionCommitReceivesTransportContext(t *testing.T) {
	account := gatewayProtectionRecheckAccount(PlatformOpenAI)
	type transportKey struct{}
	called := 0
	ctx := WithSchedulingProtectionBeforeSend(context.Background(), account.ID, func(sendCtx context.Context) error {
		called++
		require.Equal(t, "transport-value", sendCtx.Value(transportKey{}))
		require.Equal(t, "actual-model", schedulingProtectionSendValidationModel(sendCtx))
		return nil
	})
	ctx = context.WithValue(ctx, transportKey{}, "transport-value")
	require.NoError(t, runSchedulingProtectionBeforeSend(ctx, &account, "actual-model"))
	require.NoError(t, runSchedulingProtectionBeforeSend(ctx, &account, "actual-model"))
	require.Equal(t, 1, called)
}

func TestOpenAIProtectionTerminalPreservesPublicModelMapping(t *testing.T) {
	selected := gatewayProtectionRecheckAccount(PlatformOpenAI)
	selected.Credentials = map[string]any{"model_mapping": map[string]any{"public": "upstream"}}
	latest := selected
	svc := &OpenAIGatewayService{accountRepo: gatewayProtectionRecheckRepo{account: &latest}}
	ctx := WithSchedulingProtectionGroup(context.Background(), &selected.GroupIDs[0])
	ctx = context.WithValue(ctx, pluginRequestMetadataKey{}, pluginRequestMetadata{Model: "public"})
	ctx = context.WithValue(ctx, cacheUsageAttemptContextKey{}, &cacheUsageAttempt{account: &selected, model: "upstream"})
	require.NoError(t, svc.ValidateSchedulingProtectionSend(ctx, &selected), "mapped outbound names are not public whitelist keys")
	latest.Credentials = map[string]any{"model_mapping": map[string]any{"other": "upstream"}}
	require.Error(t, svc.ValidateSchedulingProtectionSend(ctx, &selected), "a changed whitelist still invalidates the nomination")
}
