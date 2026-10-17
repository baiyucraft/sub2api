package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func ttftAttemptService() (*OpenAIGatewayService, OpenAITTFTGuardConfigSnapshot, *Account, int64) {
	cfg := enabledOpenAITTFTGuardConfig(75*time.Second, 20)
	svc := &OpenAIGatewayService{cfg: &config.Config{}, toolCorrector: NewCodexToolCorrector()}
	svc.cfg.Gateway.MaxLineSize = defaultMaxLineSize
	svc.SetOpenAITTFTGuardConfigProvider(openAITTFTGuardConfigProviderFunc(func() OpenAITTFTGuardConfigSnapshot { return cfg }))
	return svc, cfg, &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}, 100
}

func ttftSeedProtection(t *testing.T, svc *OpenAIGatewayService, account *Account, group int64) {
	t.Helper()
	runtimeID, generation := svc.OpenAITTFTGuardObservationIdentity(group)
	first := time.Now().Add(-5 * time.Minute)
	require.True(t, svc.ReportOpenAITTFTGuardObservation(context.Background(), OpenAITTFTGuardObservation{
		GroupID: group, AccountID: account.ID, Model: "gpt-test", AttemptID: "slow", LogicalRequestID: "slow", RuntimeID: runtimeID, PolicyGeneration: generation,
		AlgorithmVersion: openAITTFTGuardAlgorithmVersion,
		SentAt:           first.Add(-225 * time.Second), FirstTokenAt: first, CompletedAt: first.Add(time.Second), TTFTMs: 225000, Success: true,
	}))
}

func ttftAttemptContext(svc *OpenAIGatewayService, group int64, session string) context.Context {
	ctx := WithSchedulingProtectionGroup(context.Background(), &group)
	ctx = WithUpstreamCacheGuardLogicalSession(ctx, 8, session)
	return WithOpenAITTFTGuardAttempt(ctx, svc, 8, &group)
}

func ttftFinishFast(ctx context.Context, account *Account) {
	markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
	a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	a.mu.Lock()
	a.sentAt = time.Now().Add(-time.Second)
	a.firstTokenAt = a.sentAt.Add(500 * time.Millisecond)
	a.mu.Unlock()
	// A parser-local duration with another start time must not override events.
	misleadingDuration := 225000
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: &misleadingDuration}, nil)
}

func TestTTFTAttemptOrdinaryRecoveryUsesSendClockAndReliableSessions(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	ttftSeedProtection(t, svc, account, group)
	for _, session := range []string{"explicit:a", "explicit:a", "explicit:b"} {
		ctx := ttftAttemptContext(svc, group, session)
		require.Nil(t, ctx.Value(cacheUsageAttemptContextKey{}), "TTFT must not depend on cache admission")
		ttftFinishFast(ctx, account)
		FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, errors.New("duplicate callback"))
	}
	view := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
	require.Len(t, view, 1)
	require.Equal(t, "normal", view[0].State)
	require.InDelta(t, 500, view[0].LastTTFTMs, 1)
	require.Len(t, view[0].Executions, 1)
	require.EqualValues(t, 3, view[0].Executions[0].SentCount)
	require.EqualValues(t, 3, view[0].Executions[0].CompletedCount)
}

func TestTTFTAttemptMissingSessionAndSameRequestRetriesCannotRecover(t *testing.T) {
	for _, missingSession := range []bool{true, false} {
		t.Run(map[bool]string{true: "no_session", false: "same_logical_retry"}[missingSession], func(t *testing.T) {
			svc, cfg, account, group := ttftAttemptService()
			ttftSeedProtection(t, svc, account, group)
			base := WithUpstreamCacheGuardScheduling(context.Background(), 8)
			for i := 0; i < 4; i++ {
				ctx := base
				if !missingSession {
					ctx = WithUpstreamCacheGuardLogicalSession(ctx, 8, "explicit:one")
				}
				ctx = WithOpenAITTFTGuardAttempt(ctx, svc, 8, &group)
				ttftFinishFast(ctx, account)
			}
			view := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
			require.Equal(t, "protected", view[0].State)
			require.Equal(t, 1, view[0].RecoverySamples)
		})
	}
}

func TestTTFTAttemptActualTransportAndUnsentFailure(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	upstream := &httpUpstreamRecorder{err: errors.New("transport failure")}
	svc.httpUpstream = upstream
	ctx := ttftAttemptContext(svc, group, "")
	PrepareOpenAIUpstreamCacheGuardSend(ctx, account, "gpt-test", "openai")
	request := httptest.NewRequest(http.MethodPost, "https://upstream.invalid/v1/responses", nil).WithContext(ctx)
	_, err := svc.doOpenAIUpstream(request, "", account)
	require.Error(t, err)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, nil, err)
	view := svc.ttftExecutionViews(account.ID, 0, group, "gpt-test", time.Now())
	require.Len(t, view, 1)
	require.True(t, view[0].SendAttempted)
	require.True(t, view[0].Completed)
	require.False(t, view[0].Succeeded)
	unsent := ttftAttemptContext(svc, group, "")
	FinishOpenAIUpstreamCacheGuardAttempt(unsent, &OpenAIForwardResult{}, nil)
	view = svc.ttftExecutionViews(account.ID, 0, group, "gpt-test", time.Now())
	require.EqualValues(t, 1, view[0].SentCount)
	require.EqualValues(t, 1, view[0].CompletedCount)
	_ = cfg
}

func TestTTFTAttemptSendKeepsPreparedOutboundModelAcrossChainedMapping(t *testing.T) {
	for _, source := range []string{"http_prepared", "http_cache_holder", "plugin_metadata", "ws_explicit"} {
		t.Run(source, func(t *testing.T) {
			svc, cfg, _, group := ttftAttemptService()
			account := &Account{
				ID: 17, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Credentials: map[string]any{
					"model_mapping": map[string]any{"public": "upstream-b", "upstream-b": "upstream-c"},
				},
			}
			outbound := canonicalOpenAIAccountSchedulingModel(account, "public")
			require.Equal(t, "upstream-b", outbound)
			ctx := ttftAttemptContext(svc, group, "explicit:chained-mapping")
			g := svc.getOpenAITTFTGuard()
			for i := 0; i < openAITTFTGuardProbeEvery; i++ {
				g.recordAllocationOpportunity(freshTTFTOpportunityContext(), group, outbound, cfg)
			}
			ticket := g.reserveTrialOpportunity(ctx, group, []Account{*account}, "public", cfg, nil)
			require.NotNil(t, ticket)
			t.Cleanup(func() { ticket.finish(false, nil) })
			require.Equal(t, outbound, ticket.key.model)
			ctx = context.WithValue(ctx, protectionSelectionKey{}, &protectionSelection{service: svc, groupID: group, trial: ticket, allocationModel: outbound})
			model, protocol := "", "openai"
			switch source {
			case "http_prepared":
				PrepareOpenAIUpstreamCacheGuardSend(ctx, account, outbound, protocol)
			case "http_cache_holder":
				ctx = context.WithValue(ctx, cacheUsageAttemptContextKey{}, &cacheUsageAttempt{model: outbound, protocol: protocol})
			case "plugin_metadata":
				ctx = context.WithValue(ctx, pluginRequestMetadataKey{}, pluginRequestMetadata{Model: outbound})
			case "ws_explicit":
				model, protocol = outbound, "openai_ws"
			}
			require.NoError(t, MarkSchedulingProtectionTrialSent(ctx, account, outbound))
			markOpenAITTFTGuardSend(ctx, account, model, protocol)
			a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
			a.mu.Lock()
			gotModel := a.model
			a.sentAt = time.Now().Add(-time.Second)
			a.firstTokenAt = a.sentAt.Add(500 * time.Millisecond)
			a.mu.Unlock()
			require.Equal(t, outbound, gotModel, "send facts must retain the admitted outbound identity")
			FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{}, nil)
			views := svc.ttftExecutionViews(account.ID, 0, group, outbound, time.Now())
			require.Len(t, views, 1)
			require.True(t, views[0].Trial)
			require.Empty(t, svc.ttftExecutionViews(account.ID, 0, group, "upstream-c", time.Now()))
			require.Len(t, g.observations, 1)
			for key, raw := range g.observations {
				require.Equal(t, outbound, key.model)
				require.Len(t, raw.facts, 1)
			}
		})
	}
}

func TestTTFTAttemptRealStreamReadersPublishFirstVisibleToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range []string{"responses_native", "responses_passthrough", "responses_to_cc", "responses_to_messages", "cc_native", "cc_to_responses", "cc_to_messages"} {
		for _, outcome := range []string{"success", "missing_terminal", "writer_failure"} {
			t.Run(route+"/"+outcome, func(t *testing.T) {
				svc, cfg, account, group := ttftAttemptService()
				ctx := ttftAttemptContext(svc, group, "explicit:stream")
				markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
				a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
				a.sentAt = time.Now().Add(-time.Second)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
				if outcome == "writer_failure" {
					c.Writer = &failWriteResponseWriter{ResponseWriter: c.Writer}
				}
				body := cacheGuardHTTPOutcomeSSE(strings.HasPrefix(route, "responses_"), outcome)
				response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
				result, err := cacheGuardHTTPOutcomeForward(svc, ctx, c, response, account, route)
				require.False(t, a.firstTokenAt.IsZero(), "the actual parser must record the event")
				FinishOpenAIUpstreamCacheGuardAttempt(ctx, result, err)
				view := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
				require.Len(t, view, 1)
				require.GreaterOrEqual(t, view[0].LastTTFTMs, int64(1000))
				require.Equal(t, outcome == "success", view[0].Executions[0].Succeeded)
			})
		}
	}
}

func TestTTFTExecutionAggregationSeparatesAllIdentityDimensions(t *testing.T) {
	svc, _, _, _ := ttftAttemptService()
	base := OpenAITTFTGuardObservation{AccountID: 7, UpstreamKeyID: 9, GroupID: 100, Model: "gpt-test", RuntimeID: "one", SentAt: time.Now()}
	for i := 0; i < 6; i++ {
		o, protocol := base, "openai"
		switch i {
		case 1:
			o.UpstreamKeyID++
		case 2:
			o.GroupID++
		case 3:
			o.Model = "other"
		case 4:
			protocol = "openai_ws"
		case 5:
			o.RuntimeID = "two"
		}
		svc.recordTTFTExecution(context.Background(), o, protocol)
	}
	require.Len(t, svc.ttftExecutions.entries, 6)
	require.Len(t, svc.ttftExecutionViews(7, 9, 100, "gpt-test", time.Now()), 3)
}

func TestTTFTAttemptFirstTokenPhaseProtectsBeforeTerminalWithoutDoubleSampling(t *testing.T) {
	svc, cfg, account, group := ttftAttemptService()
	ctx := ttftAttemptContext(svc, group, "explicit:phase")
	markOpenAITTFTGuardSend(ctx, account, "gpt-test", "openai")
	a := ctx.Value(openAITTFTAttemptKey{}).(*openAITTFTAttempt)
	a.mu.Lock()
	a.sentAt = time.Now().Add(-225 * time.Second)
	a.firstTokenAt = time.Time{}
	a.mu.Unlock()
	markOpenAITTFTGuardFirstToken(ctx)
	before := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
	require.Equal(t, "protected", before[0].State, "slow first token protects while stream is still open")
	require.EqualValues(t, 1, before[0].SampleCount)
	FinishOpenAIUpstreamCacheGuardAttempt(ctx, &OpenAIForwardResult{FirstTokenMs: func() *int { v := 225000; return &v }()}, errors.New("terminal failure"))
	after := svc.TTFTProtectionSnapshots(account.ID, 0, group, cfg)
	require.EqualValues(t, 1, after[0].SampleCount, "terminal merge must not duplicate the latency sample")
	require.Equal(t, "protected", after[0].State)
}
