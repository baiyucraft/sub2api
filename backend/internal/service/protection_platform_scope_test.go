package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/stretchr/testify/require"
)

type protectionPlatformScopeResolver struct {
	selected map[string]bool
	groups   map[int64]string
	cfg      OpenAITTFTGuardConfigSnapshot
}

type protectionPlatformScopeHotReadResolver struct {
	*protectionPlatformScopeResolver
	pauseOnRead  bool
	resolveCalls int
}

func (r *protectionPlatformScopeHotReadResolver) pauseScope() {
	if r.pauseOnRead {
		r.pauseOnRead = false
		r.selected[PlatformOpenAI] = false
	}
}

func (r *protectionPlatformScopeHotReadResolver) ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy {
	policies := r.protectionPlatformScopeResolver.ProtectionPolicies()
	r.pauseScope()
	return policies
}

func (r *protectionPlatformScopeHotReadResolver) ResolveProtection(_ context.Context, groupID int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	r.resolveCalls++
	r.pauseScope()
	policy := r.protectionPlatformScopeResolver.ProtectionPolicies()[groupID]
	if !r.selected[PlatformOpenAI] {
		policy.TTFT.Enabled = false
	}
	return policy, nil
}

type protectionConfigLegacyResolver struct {
	policy GroupSchedulingProtectionResolvedPolicy
	calls  int
}

func (r *protectionConfigLegacyResolver) ResolveProtection(context.Context, int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	r.calls++
	return r.policy, nil
}

func (r *protectionPlatformScopeResolver) PlatformSelected(platform string) bool {
	return platform != PlatformComposite && r.selected[platform]
}
func (r *protectionPlatformScopeResolver) ProtectionGroupPlatform(groupID int64) (string, bool) {
	platform, ok := r.groups[groupID]
	return platform, ok
}
func (r *protectionPlatformScopeResolver) ResolveProtection(_ context.Context, id int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	return r.ProtectionPolicies()[id], nil
}
func (r *protectionPlatformScopeResolver) ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy {
	result := map[int64]GroupSchedulingProtectionResolvedPolicy{}
	for id, platform := range r.groups {
		result[id] = GroupSchedulingProtectionResolvedPolicy{GroupID: id, GroupPlatform: platform, Priority: SchedulingPriorityTTFTFirst, TTFT: GroupTTFTGuardResolvedPolicy{GroupID: id, Enabled: r.cfg.Enabled, Threshold: r.cfg.Threshold, MinSamples: r.cfg.MinSamples, Source: "custom"}, Cache: UpstreamCacheGuardSettings{Mode: upstreamcacheguard.Protect}}
	}
	return result
}

func newProtectionPlatformScopeResolver() *protectionPlatformScopeResolver {
	return &protectionPlatformScopeResolver{selected: map[string]bool{PlatformOpenAI: true}, groups: map[int64]string{100: PlatformOpenAI}, cfg: enabledOpenAITTFTGuardConfig(20*time.Second, 5)}
}

func TestProtectionPlatformScopeCacheExcludesUnselectedGroupsAndAccounts(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{60, 95}, nil, false, time.Time{}, nil)
	reader := newProtectionPlatformScopeResolver()
	runner.SetProtectionPolicyResolver(reader)
	svc.SetGroupSchedulingProtectionPolicyResolver(reader)
	group := int64(100)
	accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
	require.NoError(t, err)
	preference := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, preference.Active)
	require.Contains(t, preference.ScopeIDs, int64(1))
	accounts[0].Platform = PlatformAnthropic
	preference = buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
	require.True(t, preference.Active)
	require.NotContains(t, preference.ScopeIDs, int64(1), "an unselected account remains outside all preference exclusions")
	preference.Migration = true
	require.NotContains(t, preference.exclusionsFor([]int64{2}, nil), int64(1))
	for _, platform := range []string{PlatformComposite, PlatformAnthropic} {
		reader.groups[group] = platform
		require.False(t, buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil).Active)
		require.Nil(t, BeginUpstreamCacheGuardRequest(ctx, 1, &group, "conversation", "gpt-test", "openai", &accounts[1]))
	}
	reader.groups[group] = PlatformOpenAI
	reader.selected = map[string]bool{}
	require.False(t, buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil).Active)
	require.Nil(t, BeginUpstreamCacheGuardRequest(ctx, 1, &group, "conversation", "gpt-test", "openai", &accounts[1]))
	reader.selected[PlatformOpenAI] = true
	require.True(t, buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil).Active)
}

func TestProtectionPlatformScopeTTFTPauseKeepsHistoryAndOrdinaryRouting(t *testing.T) {
	svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{60, 95}, nil, false, time.Time{}, nil)
	reader := newProtectionPlatformScopeResolver()
	runner.SetProtectionPolicyResolver(reader)
	svc.SetGroupSchedulingProtectionPolicyResolver(reader)
	group := int64(100)
	account, err := svc.accountRepo.GetByID(ctx, 1)
	require.NoError(t, err)
	slow := 60000
	svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &slow)
	require.NotEmpty(t, svc.OpenAITTFTGuardDegradations([]int64{1})[1])
	guard := svc.getOpenAITTFTGuard()
	rawCount, projections := len(guard.observations), len(guard.entries)
	samples := map[openAITTFTGuardKey]uint64{}
	for key, raw := range guard.observations {
		samples[key] = raw.sampleCount
	}
	account.Platform = PlatformAnthropic
	svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &slow)
	account.Platform = PlatformOpenAI
	for _, platform := range []string{PlatformOpenAI, PlatformComposite} {
		reader.groups[group] = platform
		reader.selected[PlatformOpenAI] = platform == PlatformComposite
		svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &slow)
		_, exclusions := svc.openAIGuardExclusionParts(ctx, &group, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)
		require.Empty(t, exclusions)
		require.Empty(t, svc.OpenAITTFTGuardDegradations([]int64{1}))
		require.Len(t, guard.observations, rawCount)
		require.Len(t, guard.entries, projections, "scope pause must not clear historical projections or recovery cohorts")
		for key, raw := range guard.observations {
			require.Equal(t, samples[key], raw.sampleCount, "an excluded account or group contributes no TTFT observations")
		}
		_, _, err, handled := svc.selectWithCacheGuardPreference(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, "", "", false, PlatformOpenAI, false, false)
		require.NoError(t, err)
		require.False(t, handled, "ordinary routing owns requests outside the selected group scope")
	}
	reader.groups[group], reader.selected[PlatformOpenAI] = PlatformOpenAI, true
	require.NotEmpty(t, svc.OpenAITTFTGuardDegradations([]int64{1})[1])
}

func TestProtectionPlatformScopeHotChangeAtSendAndFinish(t *testing.T) {
	for _, migration := range []bool{false, true} {
		t.Run(map[bool]string{false: "trial", true: "migration"}[migration], func(t *testing.T) {
			reader := newProtectionPlatformScopeResolver()
			g, cfg, candidates := protectionRegressionGuard(t)
			fast := 3000
			protectionRegressionTrial(t, g, cfg, candidates).finish(true, &fast)
			ticket := protectionRegressionTrial(t, g, cfg, candidates)
			svc := &OpenAIGatewayService{groupProtectionPolicyResolver: reader}
			ctx := context.WithValue(context.Background(), protectionSelectionKey{}, &protectionSelection{service: svc, groupID: 100, trial: ticket})
			key := int64(11)
			account := &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}
			commits := 0
			if migration {
				ctx = WithSchedulingProtectionBeforeSend(ctx, 1, func(context.Context) error { commits++; return nil })
			}
			reader.selected[PlatformOpenAI] = false
			err := runSchedulingProtectionBeforeSend(ctx, account, "gpt-test")
			if migration {
				var reselect *UpstreamFailoverError
				require.ErrorAs(t, err, &reselect)
				require.True(t, reselect.ShouldRetryNextAccount())
				require.True(t, reselect.PluginAdmissionRejected)
				require.False(t, reselect.ShouldReportAccountScheduleFailure())
			} else {
				require.NoError(t, err, "scope pause does not reject ordinary trial traffic")
			}
			require.Zero(t, commits)
			require.False(t, ticket.sent.Load())
			reader.selected[PlatformOpenAI] = true
			require.NoError(t, runSchedulingProtectionBeforeSend(ctx, account, "gpt-test"))
			require.False(t, ticket.sent.Load(), "scope admission is not an upstream send")
			require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, account, "gpt-test"))
			require.True(t, ticket.sent.Load())
			reader.selected[PlatformOpenAI] = false
			require.NoError(t, runSchedulingProtectionBeforeSend(ctx, account, "gpt-test"), "an already committed session can continue as ordinary traffic after scope pause")
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
			require.Empty(t, g.trialLeases, "a paused scope still releases a finished physical trial")
			require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "trial finish alone has no terminal observation")
			reader.selected[PlatformOpenAI] = true
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
			require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "late duplicate completion after reselect cannot revive a paused trial")
		})
	}
}

func TestProtectionPlatformScopeChangedPreservesBothSendBoundaryClassifications(t *testing.T) {
	for _, path := range []string{"openai", "general_gateway"} {
		t.Run(path, func(t *testing.T) {
			reader := newProtectionPlatformScopeResolver()
			reader.selected[PlatformOpenAI] = false
			cacheGuardTestRunner(t)
			currentUpstreamCacheGuardRunner().SetProtectionPolicyResolver(reader)
			group := int64(100)
			account := &Account{ID: 41, Platform: PlatformOpenAI}
			commits := 0
			ctx := WithSchedulingProtectionGroup(context.Background(), &group)
			ctx = WithSchedulingProtectionBeforeSend(ctx, account.ID, func(context.Context) error { commits++; return nil })
			var err error
			if path == "openai" {
				upstream := &pluginAdmissionBridgeUpstream{}
				svc := &OpenAIGatewayService{httpUpstream: upstream}
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				_, err = svc.doOpenAIUpstream(req, "", account)
				require.Empty(t, upstream.models, "scope rejection must precede physical HTTP send")
				original := err
				err = svc.handleOpenAIUpstreamTransportError(ctx, nil, account, err, false)
				require.Same(t, original, err, "OpenAI transport handling must preserve request-local admission metadata")
			} else {
				err = beginGatewaySchedulingProtectionSend(ctx, account, "gpt-test", "anthropic")
			}
			var failover *UpstreamFailoverError
			require.ErrorAs(t, err, &failover)
			require.True(t, failover.PluginAdmissionRejected)
			require.True(t, failover.ShouldRetryNextAccount())
			require.False(t, failover.ShouldReportAccountScheduleFailure())
			require.Equal(t, GatewayFailureScopeRequest, failover.Scope)
			require.Equal(t, GatewayFailureReason("scheduling_protection_scope_changed"), failover.Reason)
			failover.BindOriginAccount(account)
			require.Zero(t, failover.OriginAccountID)
			require.Zero(t, commits)
		})
	}
}

func TestProtectionPlatformScopeInflightCacheCompletionStillPersistsTerminal(t *testing.T) {
	store := cacheGuardTestRunner(t)
	reader := newProtectionPlatformScopeResolver()
	reader.groups[4] = PlatformOpenAI
	currentUpstreamCacheGuardRunner().SetProtectionPolicyResolver(reader)
	ctx := cacheGuardTestAdmittedContext()
	ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt).admission.AccountPlatform = PlatformOpenAI
	reader.selected[PlatformOpenAI] = false
	FinishUpstreamCacheGuardAttempt(ctx, &UpstreamCacheUsageEvidence{Status: "known", TotalInputTokens: 2000, CacheReadTokens: 1900}, true, time.Now())
	require.Len(t, store.events, 1)
	require.True(t, store.events[0].ProtectionPaused)
	require.True(t, store.events[0].ValidSample(), "raw factual completion remains usable without advancing recovery")
	require.False(t, currentUpstreamCacheGuardRunner().pendingFailure.Load())
}

func TestProtectionPlatformScopeCanceledDuringTTFTPolicyReadPreservesProjection(t *testing.T) {
	for _, operation := range []string{"report", "exclusions"} {
		t.Run(operation, func(t *testing.T) {
			svc, ctx, runner, _ := cacheGuardSelectionFixture(t, false, []float64{60, 95}, nil, false, time.Time{}, nil)
			reader := &protectionPlatformScopeHotReadResolver{protectionPlatformScopeResolver: newProtectionPlatformScopeResolver()}
			runner.SetProtectionPolicyResolver(reader)
			svc.SetGroupSchedulingProtectionPolicyResolver(reader)
			group := int64(100)
			account, err := svc.accountRepo.GetByID(ctx, 1)
			require.NoError(t, err)
			slow := 60000
			for i := 0; i < 5; i++ {
				svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &slow)
			}
			guard := svc.getOpenAITTFTGuard()
			require.NotEmpty(t, guard.entries)
			before := map[openAITTFTGuardKey]openAITTFTGuardEntry{}
			for key, state := range guard.entries {
				state.consecutiveRecovered = 2
				before[key] = *state
			}
			samples := map[openAITTFTGuardKey]uint64{}
			for key, raw := range guard.observations {
				samples[key] = raw.sampleCount
			}
			// The first scope gate succeeds; the policy read then cancels the
			// scope deterministically, before config synchronization can prune.
			reader.pauseOnRead = true
			if operation == "report" {
				svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &slow)
			} else {
				_, exclusions := svc.openAIGuardExclusionParts(ctx, &group, PlatformOpenAI, "gpt-test", OpenAIUpstreamTransportAny, "", "", false, nil)
				require.Empty(t, exclusions)
			}
			require.False(t, reader.selected[PlatformOpenAI])
			require.Zero(t, reader.resolveCalls, "runtime must use the hot raw policy snapshot without API effective resolution or database fallback")
			require.Len(t, guard.entries, len(before))
			for key, state := range before {
				require.Equal(t, state, *guard.entries[key], "scope cancellation must preserve recovery progress and projection identity")
			}
			for key, count := range samples {
				require.Equal(t, count, guard.observations[key].sampleCount)
			}
			reader.selected[PlatformOpenAI] = true
			require.NotEmpty(t, svc.OpenAITTFTGuardDegradations([]int64{account.ID})[account.ID])
		})
	}
}

func TestProtectionPlatformScopeTTFTConfigKeepsLegacyResolverSemantics(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		reader := &protectionConfigLegacyResolver{policy: GroupSchedulingProtectionResolvedPolicy{TTFT: GroupTTFTGuardResolvedPolicy{Enabled: enabled, Threshold: 25 * time.Second, MinSamples: 6, Source: "custom", GroupName: "legacy"}}}
		svc := &OpenAIGatewayService{groupProtectionPolicyResolver: reader}
		cfg, valid := svc.resolveOpenAITTFTGuardConfig(context.Background(), 100)
		require.True(t, valid)
		require.Equal(t, enabled, cfg.Enabled)
		require.Equal(t, 25*time.Second, cfg.Threshold)
		require.Equal(t, 6, cfg.MinSamples)
		require.Equal(t, "custom", cfg.Source)
		require.Equal(t, "legacy", cfg.GroupName)
		require.Equal(t, 1, reader.calls)
	}
}

func TestProtectionPlatformScopePausedTerminalCannotReplaySessionSlowStreak(t *testing.T) {
	reader := newProtectionPlatformScopeResolver()
	svc := &OpenAIGatewayService{groupProtectionPolicyResolver: reader}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
	ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:paused-terminal")
	ctx = context.WithValue(ctx, protectionSelectionKey{}, &protectionSelection{service: svc, groupID: 100, threshold: reader.cfg.Threshold})
	attempt := &cacheUsageAttempt{account: account, model: "gpt-test", protocol: "openai"}
	ctx = context.WithValue(ctx, cacheUsageAttemptContextKey{}, attempt)
	key, valid := protectionSessionKey(ctx, 100, account, "gpt-test", "openai")
	require.True(t, valid)
	state := svc.protectionSession(key, time.Now())
	state.slow, state.threshold = 2, reader.cfg.Threshold
	before := state.last
	slow := 60000
	reader.selected[PlatformOpenAI] = false
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &slow}, nil)
	require.True(t, attempt.protectionFinished, "the first terminal consumes the session completion even while scope is paused")
	reader.selected[PlatformOpenAI] = true
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &slow}, nil)
	require.Equal(t, 2, state.slow, "a duplicated completion after reselect cannot revive a paused slow-streak update")
	require.Equal(t, before, state.last)
}
