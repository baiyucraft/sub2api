package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func upstream429TestAccount(id int64) *service.Account {
	configID := int64(7001)
	keyID := id + 9000
	return &service.Account{
		ID:               id,
		Platform:         service.PlatformOpenAI,
		UpstreamConfigID: &configID,
		UpstreamKeyID:    &keyID,
	}
}

func upstream429TestError(retryAfter string) *service.UpstreamFailoverError {
	headers := http.Header{}
	if retryAfter != "" {
		headers.Set("Retry-After", retryAfter)
	}
	return &service.UpstreamFailoverError{
		StatusCode:      http.StatusTooManyRequests,
		ResponseBody:    []byte(`{"error":{"message":"Too many pending requests"}}`),
		ResponseHeaders: headers,
	}
}

func TestUpstream429CapacityFailoverUsesSharedSwitchBudget(t *testing.T) {
	c, _ := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         1,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	account := upstream429TestAccount(801)
	err := upstream429TestError("30")

	require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, account, err))
	require.Equal(t, account.ID, err.OriginAccountID)
	require.True(t, err.OriginUpstreamBound)
	require.False(t, allowUpstream429CapacitySwitch(c, provider, nil, account, err))
	state := getUpstream429CapacityState(c)
	require.Equal(t, 1, state.switchCount)
	require.Len(t, state.excludedAccountIDs, 1)
}

func TestUpstream429CapacityFailoverTracksSameAccountRetries(t *testing.T) {
	c, _ := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         2,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	account := upstream429TestAccount(807)
	err := upstream429TestError("17")

	noteUpstream429SameAccountRetry(c, account, err, 2)
	require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, account, err))

	state := getUpstream429CapacityState(c)
	require.Equal(t, 2, state.sameAccountRetryCount)
	require.Equal(t, 1, state.switchCount)
	require.Equal(t, 17, state.shortestRetryAfter)
}

func TestUpstream429CapacityFailoverDoesNotConsumeBudgetForOtherErrors(t *testing.T) {
	c, _ := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         1,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	account := upstream429TestAccount(808)
	err := upstream429TestError("")
	err.StatusCode = http.StatusBadGateway

	require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, account, err))
	require.Zero(t, getUpstream429CapacityState(c).switchCount)
}

func TestUpstream429CapacityFailoverDisabledPreservesLegacySwitching(t *testing.T) {
	c, _ := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             false,
		MaxSwitches:         1,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	account := upstream429TestAccount(802)
	err := upstream429TestError("")

	for range 3 {
		require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, account, err))
	}
	_, ok := upstream429CapacityExhaustion(c, provider, nil, err)
	require.False(t, ok)
}

func TestUpstream429CapacityFailoverZeroBudgetIsUnlimited(t *testing.T) {
	c, _ := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         0,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}

	for id := int64(820); id < 845; id++ {
		require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, upstream429TestAccount(id), upstream429TestError("")))
	}
	require.Equal(t, 25, getUpstream429CapacityState(c).switchCount)
}

func TestUpstream429CapacityExhaustionUsesShortestRetryAfter(t *testing.T) {
	c, recorder := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         10,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	accountA := upstream429TestAccount(803)
	accountB := upstream429TestAccount(804)
	errA := upstream429TestError("45")
	errB := upstream429TestError("12")

	require.True(t, allowUpstream429CapacitySwitch(c, provider, nil, accountA, errA))
	bindUpstreamFailoverAccount(c, accountB, errB)
	h := &OpenAIGatewayHandler{capacityFailoverProvider: provider}
	h.handleFailoverExhausted(c, errB, false)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, "12", recorder.Header().Get("Retry-After"))
	require.Equal(t, gatewayCapacityExhaustedCode, gjson.Get(recorder.Body.String(), "error.code").String())
	require.Equal(t, gatewayCapacityExhaustedMessage, gjson.Get(recorder.Body.String(), "error.message").String())
}

func TestUpstream429CapacityExhaustionUsesConfiguredStatusCode(t *testing.T) {
	c, recorder := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         10,
		ExhaustedStatusCode: 509,
	}}
	err := upstream429TestError("")
	bindUpstreamFailoverAccount(c, upstream429TestAccount(846), err)

	(&OpenAIGatewayHandler{capacityFailoverProvider: provider}).handleFailoverExhausted(c, err, false)

	require.Equal(t, 509, recorder.Code)
	require.Equal(t, gatewayCapacityExhaustedCode, gjson.Get(recorder.Body.String(), "error.code").String())
}

func TestUpstream429CapacityExhaustionDefaultsRetryAfterAndIgnoresOrdinaryAccount(t *testing.T) {
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         10,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}

	t.Run("default retry after", func(t *testing.T) {
		c, recorder := newOpenAICapacityTestContext()
		err := upstream429TestError("invalid")
		bindUpstreamFailoverAccount(c, upstream429TestAccount(805), err)
		(&OpenAIGatewayHandler{capacityFailoverProvider: provider}).handleFailoverExhausted(c, err, false)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Equal(t, "5", recorder.Header().Get("Retry-After"))
	})

	t.Run("oversized retry after", func(t *testing.T) {
		c, recorder := newOpenAICapacityTestContext()
		err := upstream429TestError("604801")
		bindUpstreamFailoverAccount(c, upstream429TestAccount(809), err)
		(&OpenAIGatewayHandler{capacityFailoverProvider: provider}).handleFailoverExhausted(c, err, false)
		require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		require.Equal(t, "5", recorder.Header().Get("Retry-After"))
	})

	t.Run("ordinary account remains 429", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		err := upstream429TestError("9")
		bindUpstreamFailoverAccount(c, &service.Account{ID: 806, Platform: service.PlatformOpenAI}, err)
		(&OpenAIGatewayHandler{capacityFailoverProvider: provider}).handleFailoverExhausted(c, err, false)
		require.Equal(t, http.StatusTooManyRequests, recorder.Code)
		require.NotEqual(t, gatewayCapacityExhaustedCode, gjson.Get(recorder.Body.String(), "error.code").String())
	})
}

func TestGatewayChatCompletionsCapacityExhaustionIncludesStableCode(t *testing.T) {
	c, recorder := newOpenAICapacityTestContext()
	provider := &gatewayCapacityFailoverProviderStub{settings: service.GatewayCapacityFailoverSettings{
		Enabled:             true,
		MaxSwitches:         10,
		ExhaustedStatusCode: http.StatusServiceUnavailable,
	}}
	err := upstream429TestError("6")
	bindUpstreamFailoverAccount(c, upstream429TestAccount(810), err)
	decision, ok := upstream429CapacityExhaustion(c, provider, nil, err)
	require.True(t, ok)

	(&GatewayHandler{}).chatCompletionsErrorResponseWithCode(c, decision.statusCode, "api_error", gatewayCapacityExhaustedCode, gatewayCapacityExhaustedMessage)

	require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	require.Equal(t, gatewayCapacityExhaustedCode, gjson.Get(recorder.Body.String(), "error.code").String())
}
