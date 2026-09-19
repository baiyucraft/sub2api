package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type groupTTFTGuardPolicyManagerStub struct {
	view  service.GroupTTFTGuardPolicyView
	input service.GroupTTFTGuardPolicyInput
}

func (s *groupTTFTGuardPolicyManagerStub) Resolve(context.Context, int64) (service.GroupTTFTGuardResolvedPolicy, error) {
	return service.GroupTTFTGuardResolvedPolicy{}, nil
}
func (s *groupTTFTGuardPolicyManagerStub) Invalidate(int64) {}
func (s *groupTTFTGuardPolicyManagerStub) ListPolicies(context.Context) ([]service.GroupTTFTGuardPolicyView, error) {
	return []service.GroupTTFTGuardPolicyView{s.view}, nil
}
func (s *groupTTFTGuardPolicyManagerStub) GetPolicy(context.Context, int64) (*service.GroupTTFTGuardPolicyView, error) {
	view := s.view
	return &view, nil
}
func (s *groupTTFTGuardPolicyManagerStub) PutPolicy(_ context.Context, _ int64, input service.GroupTTFTGuardPolicyInput) (*service.GroupTTFTGuardPolicyView, error) {
	s.input = input
	view := s.view
	return &view, nil
}

func TestGroupHandlerTTFTGuardPolicyEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	threshold, samples := 12, 4
	manager := &groupTTFTGuardPolicyManagerStub{view: service.GroupTTFTGuardPolicyView{
		GroupID: 7, GroupName: "pro", GroupPlatform: service.PlatformOpenAI,
		Mode: service.GroupTTFTGuardModeEnabled, DegradationTTFTSeconds: &threshold, MinSamples: &samples,
		Enabled: true, EffectiveEnabled: true, EffectiveDegradationTTFTSeconds: 12,
		EffectiveMinSamples: 4, Source: service.GroupTTFTGuardSourceGroup,
	}}
	handler := NewGroupHandler(nil, nil, nil)
	handler.SetTTFTGuardPolicyService(manager)
	router := gin.New()
	router.GET("/policies", handler.ListTTFTGuardPolicies)
	router.GET("/groups/:id/policy", handler.GetTTFTGuardPolicy)
	router.PUT("/groups/:id/policy", handler.PutTTFTGuardPolicy)

	for _, path := range []string{"/policies", "/groups/7/policy"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, recorder.Code)
		require.Contains(t, recorder.Body.String(), `"group_name":"pro"`)
	}

	body, err := json.Marshal(service.GroupTTFTGuardPolicyInput{
		Mode: service.GroupTTFTGuardModeEnabled, DegradationTTFTSeconds: &threshold, MinSamples: &samples,
	})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/groups/7/policy", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, service.GroupTTFTGuardModeEnabled, manager.input.Mode)
	require.Equal(t, 12, *manager.input.DegradationTTFTSeconds)
}
