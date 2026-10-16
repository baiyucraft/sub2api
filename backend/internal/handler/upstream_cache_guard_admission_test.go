package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// Frozen persisted state feeds the public runner and the real scheduler. The
// handler fixture never synthesizes or reflects the private migration marker.
type cacheGuardAdmissionStore struct {
	upstreamcacheguard.Store
	rows []upstreamcacheguard.RuntimeRow
}

func (s *cacheGuardAdmissionStore) Rows(context.Context, []int64) ([]upstreamcacheguard.RuntimeRow, error) {
	return s.rows, nil
}

func (s *cacheGuardAdmissionStore) AccountRows(context.Context) ([]upstreamcacheguard.AccountRuntimeRow, error) {
	var result []upstreamcacheguard.AccountRuntimeRow
	for _, row := range s.rows {
		if row.Kind != upstreamcacheguard.AccountKind {
			continue
		}
		var state upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &state); err != nil {
			return nil, err
		}
		result = append(result, upstreamcacheguard.AccountRuntimeRow{AccountID: state.AccountID, UpstreamKeyID: state.UpstreamKeyID, Version: row.Version, Payload: row.Payload})
	}
	return result, nil
}

func (s *cacheGuardAdmissionStore) AccountWindows(ctx context.Context, _, _ time.Time) (map[string]upstreamcacheguard.AccountWindow, error) {
	rows, err := s.AccountRows(ctx)
	if err != nil {
		return nil, err
	}
	result := make(map[string]upstreamcacheguard.AccountWindow, len(rows))
	for _, row := range rows {
		var state upstreamcacheguard.AccountState
		if err := json.Unmarshal(row.Payload, &state); err != nil {
			return nil, err
		}
		// Fixed scheduler quality fixtures do not represent transaction facts.
		window := upstreamcacheguard.AccountWindow{
			AccountID: row.AccountID, UpstreamKeyID: row.UpstreamKeyID, Version: row.Version, State: state,
			ValidRequests: state.ValidRequests, TotalInputTokens: int64(state.ValidRequests) * 1000,
			CacheReadTokens: int64(state.CachePercent*10) * int64(state.ValidRequests),
		}
		window.Policies = make(map[string]upstreamcacheguard.AccountWindow, len(state.Policies))
		for signature := range state.Policies {
			projection := window
			projection.Policies = nil
			window.Policies[signature] = projection
		}
		result[fmt.Sprintf("%d:%d", row.AccountID, row.UpstreamKeyID)] = window
	}
	return result, nil
}

func (s *cacheGuardAdmissionStore) TransactAccount(context.Context, int64, int64, int64, func(upstreamcacheguard.Tx) error) error {
	return upstreamcacheguard.ErrUnavailable
}

func (s *cacheGuardAdmissionStore) Transact(context.Context, int64, func(upstreamcacheguard.Tx) error) error {
	return nil
}

func (s *cacheGuardAdmissionStore) ClaimPending(context.Context, string, int, time.Duration) ([]upstreamcacheguard.Evidence, error) {
	return nil, nil
}

func (s *cacheGuardAdmissionStore) HasPendingBefore(context.Context, time.Time) (bool, error) {
	return false, nil
}

func (s *cacheGuardAdmissionStore) appendRow(t *testing.T, kind, key string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	s.rows = append(s.rows, upstreamcacheguard.RuntimeRow{GroupID: 100, Kind: kind, Key: key, Version: 1, Payload: raw, UpdatedAt: time.Now()})
}

type cacheGuardAdmissionSettingsRepo struct {
	service.SettingRepository
	raw string
}

func (r *cacheGuardAdmissionSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if key == service.SettingKeyUpstreamCacheGuard {
		return r.raw, nil
	}
	return "", service.ErrSettingNotFound
}

type cacheGuardAdmissionCache struct {
	service.GatewayCache
	concurrencyCacheMock
	stickyID int64
	writes   int
	attempts []int64
	writeErr error
}

type cacheGuardAdmissionRPMCache struct{ service.RPMCache }

func (*cacheGuardAdmissionRPMCache) TryAcquireRPM(context.Context, int64, int) (bool, int, time.Duration, error) {
	return false, 1, time.Second, nil
}

func (c *cacheGuardAdmissionCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return c.stickyID, nil
}

func (c *cacheGuardAdmissionCache) SetSessionAccountID(_ context.Context, _ int64, _ string, id int64, _ time.Duration) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.stickyID = id
	c.writes++
	return nil
}

func (c *cacheGuardAdmissionCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *cacheGuardAdmissionCache) DeleteSessionAccountID(context.Context, int64, string) error {
	c.stickyID = 0
	return nil
}

func newCacheGuardAdmissionFixture(t *testing.T, targetCapacity bool) (*OpenAIGatewayHandler, context.Context, *cacheGuardAdmissionCache) {
	t.Helper()
	const session = "conversation"
	const secret = "handler-cache-guard-test"
	const protocol = "openai"
	groupID := int64(100)
	ctx := service.WithUpstreamCacheGuardScheduling(context.Background(), 1)
	store := &cacheGuardAdmissionStore{}
	hmacDigest := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(hmacDigest, "upstream-cache-guard:v1:1:100:%s", session)
	req := upstreamcacheguard.Request{APIKeyID: 1, GroupID: groupID, AccountID: 1, UpstreamKeyID: 1, LogicalSessionDigest: hex.EncodeToString(hmacDigest.Sum(nil)), Model: "gpt-test", Protocol: protocol}
	routeKey := upstreamcacheguard.WarmRouteKey(req)
	store.appendRow(t, upstreamcacheguard.SessionKind, upstreamcacheguard.SessionKey(req), upstreamcacheguard.SessionState{
		Key: upstreamcacheguard.SessionKey(req), FirstSeenAt: time.Now().Add(-time.Hour), LastAccountID: 1,
		LastModel: req.Model, LastProtocol: protocol, LastRouteKey: routeKey, LastCompletedAt: time.Now().Add(-time.Second),
		Routes: map[string]upstreamcacheguard.SessionRoute{routeKey: {
			AccountID: 1, UpstreamKeyID: 1, LastSuccessfulAt: time.Now().Add(-time.Second), LowStreak: 2, PendingLow: true,
		}},
	})
	var accounts []service.Account
	settings := service.DefaultUpstreamCacheGuardSettings()
	settings.Mode = upstreamcacheguard.Protect
	for id := int64(1); id <= 2; id++ {
		key, upstreamID := id, id
		account := service.Account{ID: id, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{groupID},
			UpstreamConfigID: &upstreamID, UpstreamKeyID: &key, UpstreamConcurrencyLimit: 1}
		phase, rate := upstreamcacheguard.Healthy, float64(90)
		if id == 1 {
			phase, rate = upstreamcacheguard.Cooling, 60
		}
		accounts = append(accounts, account)
		state := upstreamcacheguard.AccountState{
			AccountID: id, UpstreamKeyID: id, Phase: phase, ValidRequests: 21, CachePercent: rate, CooldownUntil: time.Now().Add(time.Hour),
		}
		state.Policies = map[string]upstreamcacheguard.PolicyProjection{upstreamcacheguard.PolicySignature(settings): {Settings: settings, State: state}}
		store.appendRow(t, upstreamcacheguard.AccountKind, fmt.Sprint(id), state)
	}
	raw, err := json.Marshal(settings)
	require.NoError(t, err)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.JWT.Secret = secret
	runner := service.ProvideUpstreamCacheGuardRunner(store, service.NewSettingService(&cacheGuardAdmissionSettingsRepo{raw: string(raw)}, cfg), cfg, nil)
	t.Cleanup(runner.Close)
	require.Eventually(t, func() bool {
		states := service.UpstreamCacheGuardAccountStates(&accounts[0])
		return len(states) == 1 && states[0].State == "protected"
	}, time.Second, time.Millisecond)
	cache := &cacheGuardAdmissionCache{stickyID: 1}
	cache.acquireAccountSlotFn = func(_ context.Context, id int64, _ int, _ string) (bool, error) {
		cache.attempts = append(cache.attempts, id)
		return id != 2 || targetCapacity, nil
	}
	concurrency := service.NewConcurrencyService(cache)
	gw := service.NewOpenAIGatewayService(codexModelsFailoverAccountRepo{accounts: accounts}, nil, nil, nil, nil, nil,
		cache, cfg, nil, concurrency, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	return &OpenAIGatewayHandler{gatewayService: gw, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, 0)}, ctx, cache
}

func TestCacheGuardHandlerTerminalBindingWithoutProfitGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name           string
		targetCapacity bool
		wantSelection  int64
		wantResult     openAISlotAcquireResult
		wantBinding    int64
	}{
		{"admitted", true, 2, openAISlotAcquireOK, 2},
		{"target_full", false, 1, openAISlotAcquireOK, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// HTTP Responses/Messages/Chat share this terminal handler boundary.
			h, ctx, cache := newCacheGuardAdmissionFixture(t, tc.targetCapacity)
			groupID := int64(100)
			selection, _, err := h.gatewayService.SelectAccountWithScheduler(ctx, &groupID, "", "conversation", "gpt-test", nil, service.OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.True(t, selection.Acquired)
			require.False(t, selection.ProfitGateActive())
			require.Equal(t, tc.wantSelection, selection.Account.ID)
			require.Equal(t, tc.wantSelection == 2, selection.RequiresStickyAdmissionBinding())
			require.Contains(t, cache.attempts, int64(2), "target capacity must be tried by the real scheduler")
			require.Equal(t, int64(1), cache.stickyID, "selection alone must preserve the source binding")
			require.Zero(t, cache.writes)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			streamStarted := false
			release, result := h.acquireResponsesAccountSlot(c, &groupID, "conversation", selection, true, &streamStarted, zap.NewNop())
			require.Equal(t, tc.wantResult, result, "response=%s", w.Body.String())
			require.Equal(t, int64(1), cache.stickyID, "slot admission must not commit migration ahead of RPM and physical send")
			require.Zero(t, cache.writes)
			admitted := h.withSchedulingProtectionSendAdmission(service.ContextWithSelectionProfitGate(ctx, selection), &groupID, "conversation", selection)
			require.NoError(t, service.BeginUpstreamCacheGuardSend(admitted, selection.Account, "gpt-test", "openai_ws"))
			require.NoError(t, service.BeginUpstreamCacheGuardSend(admitted, selection.Account, "gpt-test", "openai_ws"))
			require.Equal(t, tc.wantBinding, cache.stickyID)
			if tc.wantBinding == 2 {
				require.Equal(t, 1, cache.writes)
			} else {
				require.Zero(t, cache.writes)
			}
			require.Empty(t, w.Body.String())
			if release != nil {
				release()
			}
			require.EqualValues(t, 1, cache.releaseAccountCalled, "admitted and rejected attempts must release their slot")
		})
	}
}

func TestCacheGuardHandlerPreservesBindingOnFinalAdmissionFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, boundary := range []string{"rpm", "attempt_budget", "credential", "binding"} {
		t.Run(boundary, func(t *testing.T) {
			h, ctx, cache := newCacheGuardAdmissionFixture(t, true)
			groupID := int64(100)
			selection, _, err := h.gatewayService.SelectAccountWithScheduler(ctx, &groupID, "", "conversation", "gpt-test", nil, service.OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.True(t, selection.IsSchedulingProtectionMigration())
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			streamStarted := false
			release, outcome := h.acquireResponsesAccountSlot(c, &groupID, "conversation", selection, false, &streamStarted, zap.NewNop())
			require.Equal(t, openAISlotAcquireOK, outcome)
			admitted := h.withSchedulingProtectionSendAdmission(service.ContextWithSelectionProfitGate(ctx, selection), &groupID, "conversation", selection)
			switch boundary {
			case "rpm":
				selection.Account.RPMLimit = 1
				h.gatewayService.SetRPMCache(&cacheGuardAdmissionRPMCache{})
				allowed, _, err := h.acquireOpenAIAccountRPM(c, selection.Account, release, map[int64]struct{}{}, zap.NewNop())
				require.NoError(t, err)
				require.False(t, allowed)
				release = nil
			case "attempt_budget":
				require.False(t, (openAIUpstreamAttemptBudget{1: {}}).allows(selection.Account.ID, 0))
			case "credential":
				_, _, err := h.gatewayService.GetRequestCredential(admitted, c, selection.Account)
				require.Error(t, err)
			case "binding":
				cache.writeErr = errors.New("sticky storage unavailable")
				require.Error(t, service.BeginUpstreamCacheGuardSend(admitted, selection.Account, "gpt-test", "openai_ws"))
			}
			require.Equal(t, int64(1), cache.stickyID)
			require.Zero(t, cache.writes)
			if release != nil {
				release()
			}
		})
	}
}

func TestCacheGuardHandlerTerminalRejectsChangedKeyWithoutProfitGate(t *testing.T) {
	h, ctx, cache := newCacheGuardAdmissionFixture(t, true)
	group := int64(100)
	selection, _, err := h.gatewayService.SelectAccountWithScheduler(ctx, &group, "", "conversation", "gpt-test", nil, service.OpenAIUpstreamTransportAny, false)
	require.NoError(t, err)
	require.True(t, selection.IsSchedulingProtectionMigration())
	require.False(t, selection.ProfitGateActive())
	defer selection.ReleaseFunc()
	// The nominated snapshot differs from the authoritative current database.
	key := int64(999)
	selection.Account.UpstreamKeyID = &key
	admitted := h.withSchedulingProtectionSendAdmission(service.ContextWithSelectionProfitGate(ctx, selection), &group, "conversation", selection)
	err = service.BeginUpstreamCacheGuardSend(admitted, selection.Account, "gpt-test", "openai_ws")
	var retry *service.UpstreamFailoverError
	require.ErrorAs(t, err, &retry)
	require.True(t, retry.PluginAdmissionRejected)
	require.False(t, retry.ShouldReportAccountScheduleFailure())
	require.Zero(t, cache.writes)
	require.Equal(t, int64(1), cache.stickyID)
}
