package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type budgetContractStore struct {
	*cacheGuardSelectionStore
	slow  bool
	reads int
}

// Unlike runner queue-only tests, binding lifecycle tests must expose their
// durable session rows to refresh instead of dropping the in-memory read view.
type protectionBindingLifecycleStore struct {
	*runnerTestStore
	groupID              int64
	planningTransactions int
}

func (s *protectionBindingLifecycleStore) Transact(ctx context.Context, groupID int64, fn func(upstreamcacheguard.Tx) error) error {
	s.planningTransactions++
	return s.runnerTestStore.Transact(ctx, groupID, fn)
}

func (s *protectionBindingLifecycleStore) Rows(ctx context.Context, _ []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []upstreamcacheguard.RuntimeRow
	for key, raw := range s.rows {
		for _, kind := range []string{upstreamcacheguard.SessionKind, upstreamcacheguard.GroupKind} {
			prefix := kind + ":"
			if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
				rows = append(rows, upstreamcacheguard.RuntimeRow{GroupID: s.groupID, Kind: kind, Key: key[len(prefix):], Version: 1, Payload: append(json.RawMessage(nil), raw...), UpdatedAt: time.Now()})
			}
		}
	}
	return rows, ctx.Err()
}

func (s *budgetContractStore) TransactAccount(ctx context.Context, group, account, key int64, fn func(upstreamcacheguard.Tx) error) error {
	if s.slow {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.cacheGuardSelectionStore.TransactAccount(ctx, group, account, key, fn)
}
func (s *budgetContractStore) LoadSession(ctx context.Context, group int64, key string) (*upstreamcacheguard.SessionState, int64, error) {
	s.reads++
	for _, row := range s.rows {
		if row.GroupID == group && row.Kind == upstreamcacheguard.SessionKind && row.Key == key {
			var session upstreamcacheguard.SessionState
			if err := json.Unmarshal(row.Payload, &session); err != nil {
				return nil, 0, err
			}
			return &session, row.Version, nil
		}
	}
	return nil, 0, ctx.Err()
}

func TestProtectionBudgetMemoryHitStartsOnlyAtActualAdmission(t *testing.T) {
	_, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, true, time.Time{}, nil)
	store := &budgetContractStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore)}
	runner.store, runner.engine = store, upstreamcacheguard.NewEngine(store)
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, runner.engine.ApplySettings(settings))
	require.NoError(t, runner.engine.Refresh(ctx))
	group := int64(100)
	request, ok := runner.logicalRequest(ctx, &group, "conversation", "gpt-test", "openai")
	require.True(t, ok)
	identity := ctx.Value(cacheGuardSchedulingKey{}).(cacheGuardSchedulingIdentity)
	session, err := runner.engine.SessionReadAtWithDatabaseContext(ctx, request, time.Now(), cacheGuardDatabaseContext)
	require.NoError(t, err)
	require.True(t, session.Known)
	require.True(t, identity.budget.deadline.IsZero(), "an in-memory hit must not consume the database budget")
	require.Zero(t, store.reads)
	request.LogicalSessionDigest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err = runner.engine.SessionReadAtWithDatabaseContext(ctx, request, time.Now(), cacheGuardDatabaseContext)
	require.NoError(t, err)
	require.Equal(t, 1, store.reads)
	require.False(t, identity.budget.deadline.IsZero(), "a real cold database read starts the shared budget")
}

func TestProtectionBudgetTransportRejectsBeforeBindingAndOrdinaryRetriesSameAccount(t *testing.T) {
	for _, protocol := range []string{"openai", "openai_ws", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			svc, ctx, runner, cache := cacheGuardSelectionFixture(t, false, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
			group := int64(100)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.True(t, selection.IsSchedulingProtectionMigration())
			store := &budgetContractStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore)}
			runner.store, runner.engine = store, upstreamcacheguard.NewEngine(store)
			settings := DefaultUpstreamCacheGuardSettings()
			settings.Mode = upstreamcacheguard.Protect
			require.NoError(t, runner.engine.ApplySettings(settings))
			require.NoError(t, runner.engine.Refresh(ctx))
			store.slow = true
			attemptCtx := ContextWithSelectionProfitGate(ctx, selection)
			require.True(t, selection.cacheGuardProtectedSelection)
			require.Equal(t, true, attemptCtx.Value(cacheGuardProtectedSelectionKey{}))
			attemptCtx = WithUpstreamCacheGuardAttempt(attemptCtx, 1, &group, "conversation", true)
			bound := false
			attemptCtx = WithSchedulingProtectionBeforeSend(attemptCtx, selection.Account.ID, func(context.Context) error { bound = true; return nil })
			upstream := &httpUpstreamRecorder{}
			svc.httpUpstream = upstream
			PrepareOpenAIUpstreamCacheGuardSend(attemptCtx, selection.Account, "gpt-test", protocol)
			if protocol == "openai" {
				request, buildErr := http.NewRequestWithContext(attemptCtx, http.MethodPost, "https://fixture.invalid/v1/responses", bytes.NewReader([]byte(`{"model":"gpt-test"}`)))
				require.NoError(t, buildErr)
				_, err = svc.doOpenAIUpstream(request, "", selection.Account)
			} else if protocol == "openai_ws" {
				err = BeginUpstreamCacheGuardSend(attemptCtx, selection.Account, "gpt-test", protocol)
			} else {
				err = beginGatewaySchedulingProtectionSend(attemptCtx, selection.Account, "gpt-test", protocol)
			}
			var fallback *UpstreamFailoverError
			require.ErrorAs(t, err, &fallback)
			require.True(t, fallback.ProtectionOrdinaryFallback)
			require.True(t, fallback.PluginAdmissionRejected)
			require.False(t, fallback.ShouldReportAccountScheduleFailure())
			require.Empty(t, upstream.requests)
			require.False(t, bound)
			require.Equal(t, int64(1), cache.sessionBindings[svc.openAISessionCacheKey("conversation")])
			require.NoError(t, ctx.Err(), "ordinary request retains its original deadline")
			selection.ReleaseFunc()
			ordinary, _, selectErr := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, selectErr)
			require.NotNil(t, ordinary)
			require.False(t, ordinary.IsSchedulingProtectionMigration())
			require.Equal(t, int64(1), ordinary.Account.ID, "source sticky remains available under ordinary scheduling")
			ordinaryCtx := ContextWithSelectionProfitGate(ctx, ordinary)
			ordinaryCtx = WithUpstreamCacheGuardAttempt(ordinaryCtx, 1, &group, "conversation", true)
			require.NoError(t, BeginUpstreamCacheGuardSend(ordinaryCtx, ordinary.Account, "gpt-test", protocol), "ordinary retry must pass without restarting protection SQL")
			if ordinary.ReleaseFunc != nil {
				ordinary.ReleaseFunc()
			}
		})
	}
}

func TestProtectionBudgetExistingWSTurnRetainsPhysicalBindingAndClearsOldSelection(t *testing.T) {
	_, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{90}, nil, true, time.Time{}, nil)
	store := &budgetContractStore{cacheGuardSelectionStore: runner.store.(*cacheGuardSelectionStore)}
	runner.store, runner.engine = store, upstreamcacheguard.NewEngine(store)
	settings := DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	require.NoError(t, runner.engine.ApplySettings(settings))
	require.NoError(t, runner.engine.Refresh(ctx))
	store.slow = true
	group := int64(100)
	key := int64(1)
	account := &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key, UpstreamConfigID: &key}
	selected := &AccountSelectionResult{Account: account, cacheGuardProtectedSelection: true, cacheGuardMigrationAccountID: 1}
	ctx = ContextWithSelectionProfitGate(ctx, selected)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "conversation", true)
	ctx = WithSchedulingProtectionBeforeSend(ctx, 1, func(context.Context) error { return nil })
	callback := ctx.Value(schedulingProtectionBeforeSendKey{}).(*schedulingProtectionBeforeSend)
	callback.committed.Store(true)
	next := WithUpstreamCacheGuardTurn(ctx, []byte(`{"type":"response.create","model":"gpt-test"}`), false)
	require.Equal(t, false, next.Value(cacheGuardProtectedSelectionKey{}))
	require.NoError(t, BeginUpstreamCacheGuardSend(next, account, "gpt-test", "openai_ws"), "an established connection continues ordinary traffic after cache admission timeout")
	require.NoError(t, next.Err())
	ordinary := ContextWithSelectionProfitGate(ctx, &AccountSelectionResult{Account: account})
	require.Equal(t, false, ordinary.Value(cacheGuardProtectedSelectionKey{}))
	require.Equal(t, int64(0), ordinary.Value(cacheGuardMigrationKey{}))
	require.Nil(t, ordinary.Value(protectionSelectionKey{}))
}

func TestProtectionBindingFailureCompletesAdmissionWithoutValidSample(t *testing.T) {
	store := newRunnerTestStore()
	runner := testRunner(t, store)
	runner.secret = []byte("test-only-binding-failure")
	settings := runner.engine.Settings()
	lifecycleStore := &protectionBindingLifecycleStore{runnerTestStore: store, groupID: 1}
	runner.store, runner.engine = lifecycleStore, upstreamcacheguard.NewEngine(lifecycleStore)
	require.NoError(t, runner.engine.ApplySettings(settings))
	// Seed the account independently from the request whose binding will fail.
	testRunnerAdmission(t, runner)
	group, key, configID := int64(1), int64(3), int64(1)
	rate, sourceRate, sourceKey := .10, .20, int64(1)
	account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 10, RateMultiplier: &rate, GroupIDs: []int64{group}, UpstreamConfigID: &configID, UpstreamKeyID: &key, UpstreamConcurrencyLimit: 1}
	sourceAccount := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 20, RateMultiplier: &sourceRate, GroupIDs: []int64{group}, UpstreamConfigID: &configID, UpstreamKeyID: &sourceKey, UpstreamConcurrencyLimit: 1}
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	previous, ok := runner.logicalRequest(ctx, &group, "binding-failure", "gpt-test", "openai")
	require.True(t, ok)
	previous.ID, previous.AccountID, previous.UpstreamKeyID, previous.StartedAt = "established-owner", sourceAccount.ID, sourceKey, time.Now().Add(-time.Minute)
	priorAdmission, err := runner.engine.Admit(ctx, previous)
	require.NoError(t, err)
	require.NoError(t, runner.engine.Complete(ctx, upstreamcacheguard.Evidence{ID: "established-owner-completion", Request: previous, Admission: priorAdmission, Success: true, RawUsage: upstreamcacheguard.RawUsage{Status: upstreamcacheusage.Known, TotalInputTokens: 100, CacheReadTokens: 95}, CompletedAt: previous.StartedAt.Add(time.Second)}))
	require.NoError(t, runner.engine.Refresh(ctx))
	sticky := &schedulerTestGatewayCache{sessionBindings: map[string]int64{}}
	svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{*sourceAccount, *account}}, cache: sticky, cfg: &config.Config{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService("false"), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}), openaiAccountStats: newOpenAIAccountRuntimeStats()}
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return enabledOpenAITTFTGuardConfig(20*time.Second, 5) }))
	sticky.sessionBindings[svc.openAISessionCacheKey("binding-failure")] = sourceAccount.ID
	selected, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "binding-failure", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, selected)
	require.Equal(t, account.ID, selected.Account.ID, "exercise a real cheaper-target selection before rejecting its send")
	require.True(t, selected.IsSchedulingProtectionMigration())
	if selected.ReleaseFunc != nil {
		defer selected.ReleaseFunc()
	}
	ctx = ContextWithSelectionProfitGate(ctx, selected)
	ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "binding-failure", true)
	bindErr := errors.New("sticky storage failed")
	ctx = WithSchedulingProtectionBeforeSend(ctx, account.ID, func(context.Context) error { return bindErr })
	PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
	require.ErrorIs(t, runSchedulingProtectionBeforeSend(ctx, account, "gpt-test"), bindErr)
	attempt := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
	require.NotNil(t, attempt.admission, "classification commits before the binding callback")
	require.False(t, attempt.admission.Admission.Completed)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, bindErr)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, bindErr)
	runner.processPending()
	var session upstreamcacheguard.SessionState
	require.NoError(t, store.Transact(context.Background(), group, func(tx upstreamcacheguard.Tx) error {
		_, err := tx.Read(upstreamcacheguard.SessionKind, attempt.admission.Admission.SessionKey, &session)
		return err
	}))
	require.Len(t, session.Admissions, 2)
	for _, admission := range session.Admissions {
		require.True(t, admission.Completed, "a pre-send failure cannot strand its synchronous admission")
	}
	require.Len(t, store.events, 2, "finishing twice must enqueue only one new completion")
	for id, fact := range store.events {
		require.True(t, store.processed[id])
		require.False(t, fact.ValidSample(), "no upstream send must not create cache or recovery evidence")
	}
	require.Equal(t, sourceAccount.ID, sticky.sessionBindings[svc.openAISessionCacheKey("binding-failure")])
	require.Equal(t, sourceAccount.ID, session.LastAccountID, "a rejected target cannot become a completed owner")
	read := runner.engine.CachedSessionRead(previous)
	require.True(t, read.Known)
	require.Zero(t, read.PreferredAccountID, "a tentative admission must defer to the unchanged native binding")
	nextCtx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	next, _, err := svc.SelectAccountWithScheduler(nextCtx, &group, "", "binding-failure", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.Equal(t, sourceAccount.ID, next.Account.ID, "failed terminal binding must not leak the unsent target into the next selection")
	if next.ReleaseFunc != nil {
		next.ReleaseFunc()
	}
	// The converse must also hold: after a successful terminal callback, the
	// native binding already owns routing before the target's first completion.
	retryCtx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	retryCtx = ContextWithSelectionProfitGate(retryCtx, selected)
	retryCtx = WithUpstreamCacheGuardAttempt(retryCtx, 1, &group, "binding-failure", true)
	retryCtx = WithSchedulingProtectionBeforeSend(retryCtx, account.ID, func(sendCtx context.Context) error {
		return svc.BindStickySessionAfterProfitAdmission(sendCtx, &group, "binding-failure", account.ID)
	})
	PrepareOpenAIUpstreamCacheGuardSend(retryCtx, account, "gpt-test", "openai")
	require.NoError(t, runSchedulingProtectionBeforeSend(retryCtx, account, "gpt-test"))
	require.Equal(t, account.ID, sticky.sessionBindings[svc.openAISessionCacheKey("binding-failure")])
	read = runner.engine.CachedSessionRead(previous)
	require.True(t, read.Known)
	require.Zero(t, read.PreferredAccountID, "uncompleted classification itself is not an owner confirmation")
	pendingCtx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	pending, _, err := svc.SelectAccountWithScheduler(pendingCtx, &group, "", "binding-failure", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.NotNil(t, pending)
	require.Equal(t, account.ID, pending.Account.ID, "successful terminal binding must apply before the first completion")
	if pending.ReleaseFunc != nil {
		pending.ReleaseFunc()
	}
	require.Zero(t, lifecycleStore.planningTransactions, "known pending sessions must not add a trial-planning SQL transaction")
}
