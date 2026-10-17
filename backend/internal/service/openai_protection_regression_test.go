package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func protectionRegressionGuard(t *testing.T) (*openAITTFTGuard, OpenAITTFTGuardConfigSnapshot, []openAITTFTGuardCandidate) {
	t.Helper()
	g := newOpenAITTFTGuard()
	cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
	slow := 60000
	g.reportForKey(100, 1, 11, "gpt-test", true, &slow, cfg)
	return g, cfg, []openAITTFTGuardCandidate{{accountID: 1, upstreamKeyID: 11, groupID: 100, model: "gpt-test"}}
}

func protectionRegressionTrial(t *testing.T, g *openAITTFTGuard, cfg OpenAITTFTGuardConfigSnapshot, candidates []openAITTFTGuardCandidate) *openAITTFTTrial {
	t.Helper()
	require.NotEmpty(t, candidates)
	c := candidates[0]
	for i := 0; i < openAITTFTGuardProbeEvery; i++ {
		g.recordAllocationOpportunity(freshTTFTOpportunityContext(), c.groupID, c.model, cfg)
	}
	key := c.upstreamKeyID
	ticket := g.reserveTrialOpportunity(freshTTFTOpportunityContext(), c.groupID, []Account{{ID: c.accountID, UpstreamKeyID: &key}}, c.model, cfg, nil)
	require.NotNil(t, ticket)
	return ticket
}

func TestProtectionRegressionCacheGroupOverridesReachScheduler(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, customEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/custom_enabled=%t", advanced, customEnabled), func(t *testing.T) {
				svc, ctx, runner, _ := cacheGuardSelectionFixture(t, advanced, []float64{60, 95}, nil, false, time.Time{}, nil)
				global := runner.engine.Settings()
				custom := global
				if customEnabled {
					global.Mode = upstreamcacheguard.Disabled
					custom.Mode = upstreamcacheguard.Protect
				} else {
					custom.Mode = upstreamcacheguard.Disabled
				}
				require.NoError(t, runner.engine.ApplySettings(global))
				require.NoError(t, runner.engine.ApplyPolicySettings(map[int64]upstreamcacheguard.Settings{100: custom}))
				require.NoError(t, runner.engine.Refresh(ctx))
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				cfg.Enabled = false
				svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return cfg }))
				group := int64(100)
				accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
				require.NoError(t, err)
				p := buildCacheGuardPreference(ctx, &group, "conversation", "gpt-test", "openai", accounts, nil)
				require.Equal(t, customEnabled, p.Active)
				if customEnabled {
					result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, result)
					require.True(t, result.Acquired)
					require.Equal(t, int64(2), result.Account.ID, "custom cache protection must prefer the higher cache account even while global mode is disabled")
					if result.ReleaseFunc != nil {
						result.ReleaseFunc()
					}
				}
			})
		}
	}
}

func TestProtectionRegressionLaterUnsentNominationKeepsAdmittedTicket(t *testing.T) {
	g, cfg, candidates := protectionRegressionGuard(t)
	first := protectionRegressionTrial(t, g, cfg, candidates)
	key := int64(11)
	require.Nil(t, g.reserveTrialOpportunity(freshTTFTOpportunityContext(), 100, []Account{{ID: 1, UpstreamKeyID: &key}}, "gpt-test", cfg, nil), "one identity cannot receive a concurrent nomination")
	fast := 3000
	first.finish(true, &fast)
	first.finish(true, &fast)
	require.Zero(t, g.entries[first.key].consecutiveRecovered, "ticket completion alone is not identified recovery evidence")
	require.Empty(t, g.trialLeases)
	next := protectionRegressionTrial(t, g, cfg, candidates)
	require.Greater(t, next.cycle, first.cycle)
	next.finish(false, nil)
}

func TestProtectionRegressionConcurrentTicketsCompleteOncePerCycle(t *testing.T) {
	g, cfg, candidates := protectionRegressionGuard(t)
	first := protectionRegressionTrial(t, g, cfg, candidates)
	key := int64(11)
	require.Nil(t, g.claimTrial(100, &Account{ID: 1, UpstreamKeyID: &key}, "gpt-test", cfg), "diagnostic claims cannot mint duplicate leases")
	require.True(t, first.markSent())
	first.finish(true, nil)
	first.finish(true, nil)
	require.Empty(t, g.trialLeases)
	require.Zero(t, g.entries[first.key].consecutiveRecovered)
	require.True(t, g.entries[first.key].degraded)
}

func TestProtectionRegressionTrialCompletesWithoutCacheHolder(t *testing.T) {
	g, cfg, candidates := protectionRegressionGuard(t)
	ticket := protectionRegressionTrial(t, g, cfg, candidates)
	ctx := context.WithValue(context.Background(), protectionSelectionKey{}, &protectionSelection{trial: ticket})
	require.Nil(t, ctx.Value(cacheUsageAttemptContextKey{}))
	key := int64(11)
	require.NoError(t, BeginUpstreamCacheGuardSend(ctx, &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}, "gpt-test", "openai_ws"))
	fast := 3000
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
	require.Zero(t, g.entries[ticket.key].consecutiveRecovered)
}

func TestProtectionRegressionUnfinishedWSTurnResetsRecovery(t *testing.T) {
	g, cfg, candidates := protectionRegressionGuard(t)
	fast := 3000
	first := protectionRegressionTrial(t, g, cfg, candidates)
	first.finish(true, &fast)
	require.Zero(t, g.entries[first.key].consecutiveRecovered)
	ticket := protectionRegressionTrial(t, g, cfg, candidates)
	ctx := context.WithValue(context.Background(), protectionSelectionKey{}, &protectionSelection{trial: ticket})
	turns := newUpstreamCacheGuardWSTurns(ctx)
	key := int64(11)
	_, err := turns.beginAdmitted(1, &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}, []byte(`{"type":"response.create","model":"gpt-test"}`), "gpt-test", "openai_ws")
	require.NoError(t, err)
	turns.finishRemaining()
	require.Empty(t, turns.pending)
	require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "an abandoned turn is a failed recovery attempt")
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
	require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "a late callback must not reverse abandonment")
}

func TestProtectionRegressionPreSendFailureKeepsRecoveryAndCacheAdmission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, boundary := range []string{"credential", "plugin", "binding", "actual_send"} {
		t.Run(boundary, func(t *testing.T) {
			g, cfg, candidates := protectionRegressionGuard(t)
			fast := 3000
			protectionRegressionTrial(t, g, cfg, candidates).finish(true, &fast)
			ticket := protectionRegressionTrial(t, g, cfg, candidates)
			key := int64(11)
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, UpstreamKeyID: &key}
			group := int64(100)
			ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
			ctx = context.WithValue(ctx, protectionSelectionKey{}, &protectionSelection{trial: ticket})
			ctx = WithUpstreamCacheGuardAttempt(ctx, 1, &group, "stable-session", true)
			bindCount := 0
			ctx = WithSchedulingProtectionBeforeSend(ctx, 1, func(context.Context) error {
				if boundary == "binding" {
					return errors.New("sticky storage unavailable")
				}
				bindCount++
				return nil
			})
			upstream := &httpUpstreamRecorder{err: errors.New("upstream transport failed")}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-test","input":"hello","stream":true}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body)).WithContext(ctx)
			var err error
			if boundary == "credential" {
				_, err = svc.Forward(ctx, c, account, body)
			} else {
				if boundary == "plugin" {
					account.Type = AccountTypeOAuth
					manager := &PluginManager{}
					manager.route.Store(&pluginRoute{pluginID: 1, scope: []PluginManagedTarget{{AccountID: 1, Models: []string{"gpt-test"}}}, unavailable: "runtime unavailable"})
					svc.pluginManager = manager
				}
				PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
				req, buildErr := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid/v1/responses", bytes.NewReader(body))
				require.NoError(t, buildErr)
				require.NoError(t, svc.preparePluginRequest(ctx, account, "gpt-test", req))
				_, err = svc.doOpenAIUpstream(req, "", account)
			}
			require.Error(t, err)
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, err)
			attempt := ctx.Value(cacheUsageAttemptContextKey{}).(*cacheUsageAttempt)
			if boundary == "actual_send" {
				require.Equal(t, 1, bindCount)
				require.Len(t, upstream.requests, 1)
				require.True(t, attempt.begun)
				require.True(t, ticket.sent.Load())
				require.Zero(t, g.entries[ticket.key].consecutiveRecovered)
			} else {
				require.Zero(t, bindCount)
				require.Empty(t, upstream.requests)
				if boundary == "binding" {
					require.True(t, attempt.begun, "synchronous cache admission precedes the final sticky commit")
				} else {
					require.False(t, attempt.begun, "cache cohort must not begin before host and plugin admission")
				}
				require.False(t, ticket.sent.Load())
				require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "an unsent ticket is not recovery evidence")
			}
		})
	}
}

func TestProtectionRegressionMigrationAccessorMatchesActualSelection(t *testing.T) {
	var missing *AccountSelectionResult
	require.False(t, missing.IsSchedulingProtectionMigration())
	require.False(t, (&AccountSelectionResult{cacheGuardMigrationAccountID: 2}).IsSchedulingProtectionMigration())
	require.False(t, (&AccountSelectionResult{Account: &Account{ID: 1}, cacheGuardMigrationAccountID: 2}).IsSchedulingProtectionMigration())
	require.True(t, (&AccountSelectionResult{Account: &Account{ID: 2}, cacheGuardMigrationAccountID: 2}).IsSchedulingProtectionMigration())
}

type protectionRegressionFrameConn struct{ writes int }

func (*protectionRegressionFrameConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	return coderws.MessageText, nil, errors.New("no frame")
}
func (c *protectionRegressionFrameConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	c.writes++
	return nil
}
func (*protectionRegressionFrameConn) Close() error { return nil }

func TestProtectionRegressionWSWriteHonorsBindingAdmission(t *testing.T) {
	for _, boundary := range []string{"binding", "cancelled", "admitted"} {
		t.Run(boundary, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			commits := 0
			ctx = WithSchedulingProtectionBeforeSend(ctx, 1, func(context.Context) error {
				if boundary == "binding" {
					return errors.New("sticky storage unavailable")
				}
				commits++
				return nil
			})
			if boundary == "cancelled" {
				cancel()
			}
			inner := &protectionRegressionFrameConn{}
			turns := newUpstreamCacheGuardWSTurns(ctx)
			writer := &upstreamCacheGuardWSFrameConn{inner: inner, turns: turns, account: &Account{ID: 1, Platform: PlatformOpenAI}}
			body := []byte(`{"type":"response.create","model":"gpt-test"}`)
			err := writer.WriteFrame(context.WithoutCancel(ctx), coderws.MessageText, body)
			if boundary == "admitted" {
				require.NoError(t, err)
				require.NoError(t, writer.WriteFrame(ctx, coderws.MessageText, body))
				require.Equal(t, 1, commits, "successive WS turns share one migration commit")
				require.Equal(t, 2, inner.writes)
				writer.account = &Account{ID: 2, Platform: PlatformOpenAI}
				require.Error(t, writer.WriteFrame(ctx, coderws.MessageText, body))
				require.Equal(t, 2, inner.writes, "a reused context must not send to a changed account")
			} else {
				require.Error(t, err)
				require.Zero(t, commits)
				require.Zero(t, inner.writes)
				require.Empty(t, turns.pending)
			}
			turns.finishRemaining()
		})
	}
}

func TestProtectionRegressionTrialSendRequiresFinalIdentity(t *testing.T) {
	for _, boundary := range []string{"model_changed", "key_changed", "cancelled", "metadata_matches"} {
		t.Run(boundary, func(t *testing.T) {
			g, cfg, candidates := protectionRegressionGuard(t)
			fast := 3000
			protectionRegressionTrial(t, g, cfg, candidates).finish(true, &fast)
			ticket := protectionRegressionTrial(t, g, cfg, candidates)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx = context.WithValue(ctx, protectionSelectionKey{}, &protectionSelection{trial: ticket})
			model := "gpt-test"
			if boundary == "model_changed" {
				model = "gpt-other"
			}
			ctx = context.WithValue(ctx, pluginRequestMetadataKey{}, pluginRequestMetadata{Model: model})
			key := int64(11)
			if boundary == "key_changed" {
				key = 12
			}
			if boundary == "cancelled" {
				cancel()
			}
			err := runSchedulingProtectionBeforeSend(ctx, &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key})
			if boundary == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			} else if boundary == "metadata_matches" {
				require.NoError(t, err)
				require.False(t, ticket.sent.Load(), "admission alone is not transport send")
				require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, &Account{ID: 1, Platform: PlatformOpenAI, UpstreamKeyID: &key}, model))
			} else {
				require.Error(t, err)
			}
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &fast}, nil)
			if boundary == "metadata_matches" {
				require.True(t, ticket.sent.Load())
				require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "send alone without terminal identified facts never creates recovery")
			} else {
				require.False(t, ticket.sent.Load())
				require.Zero(t, g.entries[ticket.key].consecutiveRecovered, "a changed final route or cancelled unsent attempt is not evidence for the nominated model")
			}
		})
	}
}

func TestProtectionRegressionTTFTTrialRespectsPriorityPools(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		for _, firstPoolAvailable := range []bool{false, true} {
			t.Run(fmt.Sprintf("advanced=%t/first_pool_available=%t", advanced, firstPoolAvailable), func(t *testing.T) {
				svc, ctx, runner, _ := cacheGuardSelectionFixture(t, advanced, []float64{95, 95}, nil, false, time.Time{}, map[int64]bool{1: firstPoolAvailable, 2: true})
				global := runner.engine.Settings()
				global.Mode = upstreamcacheguard.Disabled
				require.NoError(t, runner.engine.ApplySettings(global))
				repo := svc.accountRepo.(schedulerTestOpenAIAccountRepo)
				repo.accounts[1].Priority = 2
				svc.accountRepo = repo
				cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
				svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: SchedulingPriorityTTFTFirst, cfg: cfg})
				group := int64(100)
				fast, slow := 3000, 60000
				for i := 0; i < 5; i++ {
					svc.ReportOpenAIAccountScheduleResultForGroup(&group, &repo.accounts[0], "gpt-test", true, &fast)
				}
				svc.ReportOpenAIAccountScheduleResultForGroup(&group, &repo.accounts[1], "gpt-test", true, &slow)
				fundTTFTTrial(svc.getOpenAITTFTGuard(), group, cfg)
				result, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "natural-new-session", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.True(t, result.Acquired)
				want := int64(2)
				if firstPoolAvailable {
					want = 1
				}
				require.Equal(t, want, result.Account.ID)
				if firstPoolAvailable {
					require.Nil(t, result.protection.trial, "a low-priority recovery nomination cannot take capacity ahead of a healthy preferred pool")
				} else {
					require.NotNil(t, result.protection.trial, "a recovery attempt is allowed after higher-priority capacity is exhausted")
				}
				if result.ReleaseFunc != nil {
					result.ReleaseFunc()
				}
			})
		}
	}
}

func TestProtectionRegressionSlowSessionRequiresSameActualModel(t *testing.T) {
	for _, sameModel := range []bool{false, true} {
		t.Run(fmt.Sprintf("same_actual_model=%t", sameModel), func(t *testing.T) {
			svc := &OpenAIGatewayService{}
			ctx := WithUpstreamCacheGuardScheduling(context.Background(), 1)
			ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:stable-session")
			keyA, keyB := int64(1), int64(2)
			actualTarget := "gpt-b"
			if sameModel {
				actualTarget = "gpt-a"
			}
			accounts := []Account{
				{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1, UpstreamKeyID: &keyA, Credentials: map[string]any{"model_mapping": map[string]any{"public": "gpt-a"}}},
				{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Priority: 1, UpstreamKeyID: &keyB, Credentials: map[string]any{"model_mapping": map[string]any{"public": actualTarget}}},
			}
			cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			sourceModel := canonicalOpenAIAccountSchedulingModel(&accounts[0], "public")
			require.Equal(t, "gpt-a", sourceModel)
			key, ok := protectionSessionKey(ctx, 100, &accounts[0], sourceModel, "openai")
			require.True(t, ok)
			state := svc.protectionSession(key, time.Now())
			state.slow, state.threshold = 2, cfg.Threshold
			quality := map[int64]forkscheduling.TTFTQuality{1: {Degraded: true, SampleCount: 5, EWMAms: 35000}, 2: {SampleCount: 5, EWMAms: 3000}}
			p := svc.jointSessionPreference(ctx, cacheGuardPreference{Active: true, KeepAccountID: 1}, 100, "public", "openai", accounts, quality, cfg, SchedulingPriorityTTFTFirst)
			require.Equal(t, sameModel, p.Migration)
			if sameModel {
				require.Equal(t, [][]int64{{2}}, p.Tiers)
			} else {
				require.Empty(t, p.Tiers, "a fast different actual model does not provide evidence for the current model")
			}
		})
	}
}

func TestProtectionRegressionMigrationResidenceBlocksBothTriggersUntilExpiry(t *testing.T) {
	for _, priority := range []string{SchedulingPriorityCacheFirst, SchedulingPriorityTTFTFirst} {
		t.Run(priority, func(t *testing.T) {
			svc, ctx, _, _ := cacheGuardSelectionFixture(t, false, []float64{40, 90}, nil, false, time.Time{}, nil)
			ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:residence")
			group := int64(100)
			accounts, err := svc.accountRepo.ListSchedulableByGroupID(ctx, group)
			require.NoError(t, err)
			cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			key, ok := protectionSessionKey(ctx, group, &accounts[0], "gpt-test", "openai")
			require.True(t, ok)
			streak := svc.protectionSession(key, time.Now())
			streak.slow, streak.threshold = 2, cfg.Threshold
			quality := map[int64]forkscheduling.TTFTQuality{
				1: {Degraded: true, SampleCount: 5, EWMAms: 35000},
				2: {SampleCount: 5, EWMAms: 4000},
			}
			preference := cacheGuardPreference{Active: true, KeepAccountID: 1, Migration: true, Tiers: [][]int64{{2}}}
			before := time.Now()
			svc.recordProtectionMigration(ctx, &group)
			after := time.Now()
			residenceKey, ok := protectionSessionKey(ctx, group, nil, "", "")
			require.True(t, ok)
			residence := svc.protectionSession(residenceKey, after)
			require.False(t, residence.residenceUntil.Before(before.Add(5*time.Minute)))
			require.False(t, residence.residenceUntil.After(after.Add(5*time.Minute)))
			blocked := svc.jointSessionPreference(ctx, preference, group, "gpt-test", "openai", accounts, quality, cfg, priority)
			require.False(t, blocked.Migration, "a committed migration's residence suppresses both active triggers")
			require.Empty(t, blocked.Tiers)
			require.Equal(t, int64(1), blocked.KeepAccountID)
			residence.mu.Lock()
			residence.residenceUntil = time.Now().Add(-time.Millisecond)
			residence.mu.Unlock()
			resumed := svc.jointSessionPreference(ctx, preference, group, "gpt-test", "openai", accounts, quality, cfg, priority)
			require.True(t, resumed.Migration)
			require.Equal(t, [][]int64{{2}}, resumed.Tiers, "expiry restores the jointly justified target")
		})
	}
}

func TestProtectionRegressionFixedRetryKeepsSourceWithoutResidence(t *testing.T) {
	for _, advanced := range []bool{false, true} {
		t.Run(fmt.Sprintf("advanced=%t", advanced), func(t *testing.T) {
			svc, ctx, _, _ := cacheGuardSelectionFixture(t, advanced, []float64{60, 90}, map[int64]bool{1: true}, true, time.Time{}, nil)
			ctx = context.WithValue(ctx, cacheGuardLogicalSessionKey{}, "explicit:fixed-retry")
			group := int64(100)
			cfg := enabledOpenAITTFTGuardConfig(20*time.Second, 5)
			svc.SetGroupSchedulingProtectionPolicyResolver(jointPolicyResolver{priority: SchedulingPriorityTTFTFirst, cfg: cfg})
			for id, ms := range map[int64]int{1: 35000, 2: 4000} {
				account, err := svc.accountRepo.GetByID(ctx, id)
				require.NoError(t, err)
				for i := 0; i < 5; i++ {
					svc.ReportOpenAIAccountScheduleResultForGroup(&group, account, "gpt-test", true, &ms)
				}
				if id == 1 {
					key, ok := protectionSessionKey(ctx, group, account, "gpt-test", "openai")
					require.True(t, ok)
					streak := svc.protectionSession(key, time.Now())
					streak.slow, streak.threshold = 2, cfg.Threshold
				}
			}
			require.False(t, svc.protectionSessionResident(ctx, group))
			for _, fixed := range []bool{false, true} {
				attemptCtx := ctx
				if fixed {
					attemptCtx = WithOpenAIFixedRetryAccount(ctx, 1)
				}
				selection, _, err := svc.SelectAccountWithScheduler(attemptCtx, &group, "", "conversation", "gpt-test", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				want := int64(2)
				if fixed {
					want = 1
				}
				require.Equal(t, want, selection.Account.ID, "only the fixed retry boundary should suppress the justified joint migration")
				if selection.ReleaseFunc != nil {
					selection.ReleaseFunc()
				}
			}
		})
	}
}
