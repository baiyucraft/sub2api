package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func pluginAdmissionLiveAccount(id int64) Account {
	return Account{
		ID: id, Name: fmt.Sprintf("live-admission-%d", id), Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: int(id),
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
	}
}

func TestPluginAdmissionLiveSessionModel(t *testing.T) {
	for _, tc := range []struct {
		name, session, model string
		rejected             bool
	}{
		{name: "managed", session: `{"model":"gpt-live-managed"}`, model: "gpt-live-managed", rejected: true},
		{name: "unmanaged", session: `{"model":"gpt-live-other","custom":{"keep":true}}`, model: "gpt-live-other"},
		{name: "missing", session: `{"instructions":"test"}`, rejected: true},
		{name: "empty", session: `{"model":"  "}`, rejected: true},
		{name: "null", session: `{"model":null}`, rejected: true},
		{name: "not_string", session: `{"model":123}`, rejected: true},
		{name: "nested_only", session: `{"custom":{"model":"gpt-live-other"}}`, rejected: true},
		{name: "duplicate", session: `{"model":"gpt-live-other","model":"gpt-live-managed"}`, rejected: true},
		{name: "case_variant", session: `{"model":"gpt-live-other","Model":"gpt-live-managed"}`, rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account := pluginAdmissionLiveAccount(1)
			// Live preserves Session; account mapping is not an outbound rewrite here.
			account.Credentials["model_mapping"] = map[string]any{"gpt-live-other": "gpt-live-managed"}
			manager := &PluginManager{}
			manager.route.Store(&pluginRoute{pluginID: 1,
				scope:       []PluginManagedTarget{{AccountID: account.ID, Models: []string{"gpt-live-managed"}}},
				unavailable: "runtime unavailable",
			})
			upstream := &liveHTTPUpstreamStub{}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: manager, httpUpstream: upstream}
			request := &LiveCallRequest{SDP: "v=offer\r\n", Session: json.RawMessage(tc.session)}
			require.NoError(t, ValidateLiveCallRequest(request))
			require.Equal(t, tc.model, openAILivePluginModel(request.Session))
			created, err := svc.createUpstreamLiveCall(context.Background(), &account, request, "test-attestation")
			require.Equal(t, tc.session, string(request.Session))
			if tc.rejected {
				var failoverErr *UpstreamFailoverError
				require.ErrorAs(t, err, &failoverErr)
				require.True(t, failoverErr.PluginAdmissionRejected)
				require.True(t, failoverErr.ShouldRetryNextAccount())
				require.False(t, failoverErr.ShouldReportAccountScheduleFailure())
				require.False(t, failoverErr.ForceCacheBilling)
				require.Nil(t, created)
				require.Nil(t, upstream.request, "unknown or managed models cannot bypass scoped admission")
				return
			}
			require.NoError(t, err)
			require.NotNil(t, created)
			metadata, ok := upstream.request.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
			require.True(t, ok)
			require.Equal(t, tc.model, metadata.Model)
			require.JSONEq(t, tc.session, gjson.GetBytes(upstream.body, "session").Raw)
		})
	}
}

func TestPluginAdmissionLiveUnknownModelWithoutManagedScopePreservesRequest(t *testing.T) {
	account := pluginAdmissionLiveAccount(1)
	manager := &PluginManager{}
	upstream := &liveHTTPUpstreamStub{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, pluginManager: manager, httpUpstream: upstream}
	request := &LiveCallRequest{SDP: "v=offer\r\n", Session: json.RawMessage(`{"instructions":"test"}`)}
	created, err := svc.createUpstreamLiveCall(context.Background(), &account, request, "test-attestation")
	require.NoError(t, err)
	require.NotNil(t, created)
	metadata, ok := upstream.request.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	require.True(t, ok)
	require.Empty(t, metadata.Model, "the billing default gpt-live is not an outbound model")
	require.JSONEq(t, string(request.Session), gjson.GetBytes(upstream.body, "session").Raw)
}

type pluginAdmissionLiveStore struct {
	*liveTestStore
	observerDone chan struct{}
}

func (s *pluginAdmissionLiveStore) ClaimLiveController(context.Context, string, string, string) (bool, error) {
	close(s.observerDone)
	return false, nil
}

type pluginAdmissionLiveConcurrencyCache struct {
	schedulerTestConcurrencyCache
	liveAcquired []int64
	liveReleased []int64
}

func (c *pluginAdmissionLiveConcurrencyCache) AcquireLiveLease(_ context.Context, accountID int64, _ int, _ int64, _ int, _ int64, _ string, _ bool) (bool, error) {
	c.liveAcquired = append(c.liveAcquired, accountID)
	return true, nil
}

func (c *pluginAdmissionLiveConcurrencyCache) RefreshLiveLease(context.Context, int64, int64, int64, string) (bool, error) {
	return true, nil
}

func (c *pluginAdmissionLiveConcurrencyCache) ReleaseLiveLease(_ context.Context, accountID int64, _ int64, _ int64, _ string) error {
	c.liveReleased = append(c.liveReleased, accountID)
	return nil
}

type pluginAdmissionLiveUpstream struct {
	HTTPUpstream
	t        *testing.T
	outcomes string
	ids      []int64
}

func (u *pluginAdmissionLiveUpstream) Do(request *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.ids = append(u.ids, accountID)
	metadata, ok := request.Context().Value(pluginRequestMetadataKey{}).(pluginRequestMetadata)
	require.True(u.t, ok)
	require.Equal(u.t, "gpt-live-managed", metadata.Model)
	require.False(u.t, IsForceCacheBilling(request.Context()))
	status := http.StatusOK
	switch u.outcomes[int(accountID)-1] {
	case 'a':
		u.t.Fatal("admission-rejected account reached HTTP upstream")
	case 'e':
		status = http.StatusBadGateway
	case 'b':
		status = http.StatusBadRequest
	case 'n':
		return nil, errors.New("ordinary transport failure")
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Location": {"/backend-api/codex/call_test"}},
		Body: io.NopCloser(strings.NewReader("v=answer\r\n"))}, nil
}

func TestPluginAdmissionLiveCreatePreservesRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name, outcomes string
		wantSelected   int
		wantHTTP       []int64
		wantSuccess    bool
		wantAdmission  bool
		repeat         bool
	}{
		{name: "five_rejections_then_success", outcomes: "aaaaas", wantSelected: 6, wantHTTP: []int64{6}, wantSuccess: true},
		{name: "rejections_preserve_all_four_attempts", outcomes: "aaaaaeees", wantSelected: 9, wantHTTP: []int64{6, 7, 8, 9}, wantSuccess: true},
		{name: "rejections_after_real_failures", outcomes: "eeeaaas", wantSelected: 7, wantHTTP: []int64{1, 2, 3, 7}, wantSuccess: true},
		{name: "four_real_failures_stop", outcomes: "aaaaaeeees", wantSelected: 9, wantHTTP: []int64{6, 7, 8, 9}},
		{name: "ordinary_failures_unchanged", outcomes: "eeees", wantSelected: 4, wantHTTP: []int64{1, 2, 3, 4}},
		{name: "ordinary_transport_errors_unchanged", outcomes: "nnnns", wantSelected: 4, wantHTTP: []int64{1, 2, 3, 4}},
		{name: "ordinary_bad_request_is_terminal", outcomes: "aabs", wantSelected: 3, wantHTTP: []int64{3}},
		{name: "all_rejected_is_request_local", outcomes: "aaaaa", wantSelected: 5, wantHTTP: []int64{}, wantAdmission: true, repeat: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			t.Cleanup(resetOpenAIAdvancedSchedulerSettingCacheForTest)
			accounts := make([]Account, len(tc.outcomes))
			scope := make([]PluginManagedTarget, 0)
			for i, outcome := range tc.outcomes {
				accounts[i] = pluginAdmissionLiveAccount(int64(i + 1))
				if outcome == 'a' {
					scope = append(scope, PluginManagedTarget{AccountID: accounts[i].ID, Models: []string{"gpt-live-managed"}})
				}
			}
			manager := &PluginManager{}
			manager.route.Store(&pluginRoute{pluginID: 1, scope: scope, unavailable: "runtime unavailable"})
			store := &pluginAdmissionLiveStore{
				liveTestStore: &liveTestStore{GatewayCache: &schedulerTestGatewayCache{}},
				observerDone:  make(chan struct{}),
			}
			var acquired, released []int64
			cache := &pluginAdmissionLiveConcurrencyCache{schedulerTestConcurrencyCache: schedulerTestConcurrencyCache{
				acquiredIDs: &acquired, releasedIDs: &released,
			}}
			upstream := &pluginAdmissionLiveUpstream{t: t, outcomes: tc.outcomes, ids: []int64{}}
			cfg := &config.Config{RunMode: config.RunModeSimple, JWT: config.JWTConfig{Secret: "live-plugin-admission-test"}}
			svc := &OpenAIGatewayService{
				cfg: cfg, accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: store,
				concurrencyService: NewConcurrencyService(cache), httpUpstream: upstream, pluginManager: manager,
				liveAttestation: liveAttestationStub{header: "test-attestation"}, liveAttestationCipher: newLiveAttestationCipher(cfg),
			}
			runs := 1
			if tc.repeat {
				runs = 2
			}
			for run := 0; run < runs; run++ {
				start := len(acquired)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				created, err := svc.CreateLiveCall(ctx,
					&LiveCallRequest{SDP: "v=offer\r\n", Session: json.RawMessage(`{"model":"gpt-live-managed"}`)},
					LiveCallIdentity{UserID: 11, APIKeyID: 12}, 2,
				)
				cancel()
				wantIDs := make([]int64, tc.wantSelected)
				for i := range wantIDs {
					wantIDs[i] = int64(i + 1)
				}
				require.Equal(t, wantIDs, acquired[start:])
				require.Equal(t, acquired, released, "all temporary scheduler slots must be released")
				require.Equal(t, acquired, cache.liveAcquired)
				require.Equal(t, tc.wantHTTP, upstream.ids)
				if tc.wantSuccess {
					require.NoError(t, err)
					require.NotNil(t, created)
					require.Equal(t, int64(tc.wantSelected), created.Account.ID)
					require.Equal(t, acquired[:len(acquired)-1], cache.liveReleased, "only the successful call retains its Live lease")
					select {
					case <-store.observerDone:
					case <-time.After(time.Second):
						t.Fatal("Live observer did not stop in test fixture")
					}
				} else {
					require.Error(t, err)
					require.Nil(t, created)
					require.Nil(t, store.record, "admission failures do not create billable Live calls")
					require.Equal(t, acquired, cache.liveReleased)
					if tc.wantAdmission {
						var failoverErr *UpstreamFailoverError
						require.ErrorAs(t, err, &failoverErr)
						require.True(t, failoverErr.PluginAdmissionRejected)
					}
				}
			}
			for _, account := range accounts {
				require.True(t, account.Schedulable)
				require.Equal(t, StatusActive, account.Status)
				require.Nil(t, account.TempUnschedulableUntil)
			}
		})
	}
}
