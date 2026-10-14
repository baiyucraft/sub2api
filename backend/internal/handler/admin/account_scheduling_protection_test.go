package admin

import (
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accountProtectionPolicyStub map[int64]service.GroupSchedulingProtectionResolvedPolicy

func (s accountProtectionPolicyStub) ProtectionPolicies() map[int64]service.GroupSchedulingProtectionResolvedPolicy {
	return s
}

type accountProtectionTTFTStub struct {
	seenKeyID int64
	states    map[int64][]service.TTFTProtectionModelView
}

func (s *accountProtectionTTFTStub) OpenAITTFTGuardDegradations([]int64) map[int64][]service.OpenAITTFTGuardDegradation {
	return nil
}

func (s *accountProtectionTTFTStub) TTFTProtectionSnapshots(_ int64, keyID int64, groupID int64, _ service.OpenAITTFTGuardConfigSnapshot) []service.TTFTProtectionModelView {
	s.seenKeyID = keyID
	return append([]service.TTFTProtectionModelView{}, s.states[groupID]...)
}

func TestAccountUpstreamProtectionProjectsSharedEvidenceAndGroupPolicies(t *testing.T) {
	reader := &accountProtectionTTFTStub{states: map[int64][]service.TTFTProtectionModelView{
		0: {{Model: "gpt-test", State: "observed", SampleCount: 23}},
		1: {{Model: "gpt-test", State: "normal"}, {Model: "slow-model", State: "draining"}},
		2: {{Model: "gpt-test", State: "disabled"}},
	}}
	policies := accountProtectionPolicyStub{
		1: {GroupID: 1, TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true, Threshold: 2 * time.Second, MinSamples: 5, Source: "custom", GroupName: "strict"}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect", ThresholdPercent: 70, RecoveryThresholdPercent: 85, MinValidRequests: 20}, CacheMode: "enabled", CacheSource: "custom", Priority: "ttft_first", PrioritySource: "custom"},
		2: {GroupID: 2, TTFT: service.GroupTTFTGuardResolvedPolicy{Source: "disabled", GroupName: "off"}, Cache: service.UpstreamCacheGuardSettings{Mode: "disabled"}, CacheMode: "disabled", CacheSource: "disabled", Priority: "cache_first", PrioritySource: "global"},
	}
	h := &AccountHandler{ttftGuardReader: reader, protectionPolicyReader: policies}
	upstreamID, keyID := int64(12), int64(45)
	account := &service.Account{ID: 9, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstreamID, UpstreamKeyID: &keyID, GroupIDs: []int64{2, 1, 1}, Groups: []*service.Group{{ID: 3}, {ID: 2}}}
	cache := service.UpstreamCacheGuardAccountView{Summary: &service.UpstreamCacheGuardSummary{State: "protected"}, States: []service.UpstreamCacheGuardState{{GroupID: 1, State: "protected", ValidRequests: 23}}}
	view := h.upstreamProtection(account, cache)
	require.Equal(t, keyID, reader.seenKeyID)
	require.Equal(t, "observed", view.TTFTModels[0].State)
	require.Equal(t, cache.Summary, view.Cache)
	require.Len(t, view.Groups, 3)
	require.Equal(t, int64(1), view.Groups[0].GroupID)
	require.Equal(t, "draining", view.Groups[0].TTFT.State)
	require.Equal(t, int64(2000), view.Groups[0].TTFT.ThresholdMs)
	require.Equal(t, "ttft_first", view.Groups[0].Priority)
	require.Equal(t, "custom", view.Groups[0].Cache.Source)
	require.Equal(t, float64(70), view.Groups[0].Cache.ThresholdPercent)
	require.Equal(t, "disabled", view.Groups[1].TTFT.State)
	require.Equal(t, "disabled", view.Groups[1].Cache.State)
	require.Equal(t, "unavailable", view.Groups[2].TTFT.State)
	require.Equal(t, "unavailable", view.Groups[2].Cache.State)
}

func TestAccountUpstreamProtectionInsufficientUnavailableAndSimpleMode(t *testing.T) {
	upstreamID, keyID := int64(12), int64(45)
	account := &service.Account{ID: 9, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstreamID, UpstreamKeyID: &keyID, GroupIDs: []int64{1}}
	h := &AccountHandler{protectionPolicyReader: accountProtectionPolicyStub{1: {TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect"}}}}
	view := h.upstreamProtection(account, service.UpstreamCacheGuardAccountView{})
	require.Equal(t, "unavailable", view.Groups[0].TTFT.State)
	require.Equal(t, "unavailable", view.Groups[0].Cache.State)
	h.ttftGuardReader = &accountProtectionTTFTStub{}
	view = h.upstreamProtection(account, service.UpstreamCacheGuardAccountView{})
	require.Equal(t, "insufficient", view.Groups[0].TTFT.State, "an available runtime with no observations is distinct from an unavailable runtime")
	h.cfg = &config.Config{RunMode: config.RunModeSimple}
	require.Nil(t, h.upstreamProtection(account, service.UpstreamCacheGuardAccountView{}))
	h.cfg = nil
	account.UpstreamKeyID = nil
	require.Nil(t, h.upstreamProtection(account, service.UpstreamCacheGuardAccountView{}))
}

func TestAccountTTFTCompatibilityDTOFiltersOldKey(t *testing.T) {
	upstreamID, keyID := int64(12), int64(45)
	account := &service.Account{UpstreamConfigID: &upstreamID, UpstreamKeyID: &keyID}
	states := []service.OpenAITTFTGuardDegradation{{UpstreamKeyID: 44, Model: "old"}, {UpstreamKeyID: 45, Model: "current"}, {Model: "unknown"}}
	filtered := accountCurrentKeyTTFTDegradations(account, states)
	require.Len(t, filtered, 1)
	require.Equal(t, "current", filtered[0].Model)
	account.UpstreamKeyID = nil
	require.Equal(t, states, accountCurrentKeyTTFTDegradations(account, states))
}

type accountProtectionScopedPolicyStub struct {
	accountProtectionPolicyStub
	selected map[string]bool
}

func (s accountProtectionScopedPolicyStub) PlatformSelected(platform string) bool {
	return platform != service.PlatformComposite && s.selected[platform]
}

func TestAccountUpstreamProtectionPlatformScopeOverridesOldExecutionLabels(t *testing.T) {
	policies := accountProtectionScopedPolicyStub{accountProtectionPolicyStub: accountProtectionPolicyStub{
		1: {GroupID: 1, GroupPlatform: service.PlatformOpenAI, TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect"}},
		2: {GroupID: 2, GroupPlatform: service.PlatformComposite, TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect"}},
		3: {GroupID: 3, GroupPlatform: service.PlatformAnthropic, TTFT: service.GroupTTFTGuardResolvedPolicy{Enabled: true}, Cache: service.UpstreamCacheGuardSettings{Mode: "protect"}},
	}, selected: map[string]bool{}}
	reader := &accountProtectionTTFTStub{states: map[int64][]service.TTFTProtectionModelView{0: {{Model: "gpt-test", State: "observed", SampleCount: 23}}, 1: {{Model: "gpt-test", State: "recovering", SampleCount: 23, RecoverySamples: 2}}}}
	h := &AccountHandler{ttftGuardReader: reader, protectionPolicyReader: policies}
	upstreamID, keyID := int64(12), int64(45)
	account := &service.Account{ID: 9, Platform: service.PlatformOpenAI, UpstreamConfigID: &upstreamID, UpstreamKeyID: &keyID, GroupIDs: []int64{1, 2, 3}}
	rate := float64(90)
	cache := service.UpstreamCacheGuardAccountView{Summary: &service.UpstreamCacheGuardSummary{State: "protected", RatePercent: &rate, ValidRequests: 23, RecoverySessions: 3}, States: []service.UpstreamCacheGuardState{{GroupID: 1, State: "recovering", RecoverySessions: 3, TrialParticipant: true}}}
	view := h.upstreamProtection(account, cache)
	require.False(t, view.PlatformSelected)
	require.Equal(t, "platform_not_selected", view.Cache.State)
	require.Zero(t, view.Cache.RecoverySessions)
	require.Equal(t, 23, view.Cache.ValidRequests)
	require.Equal(t, "observed", view.TTFTModels[0].State)
	for _, group := range view.Groups {
		require.False(t, group.PlatformSelected)
		require.Equal(t, "platform_not_selected", group.TTFT.State)
		require.Equal(t, "platform_not_selected", group.Cache.State)
		require.Zero(t, group.Cache.RecoverySessions)
		require.False(t, group.Cache.TrialParticipant)
	}
	degradations := []service.OpenAITTFTGuardDegradation{{GroupID: 1, UpstreamKeyID: keyID}, {GroupID: 2, UpstreamKeyID: keyID}}
	require.Empty(t, accountCurrentKeyTTFTDegradations(account, degradations, policies))
	policies.selected[service.PlatformOpenAI] = true
	view = h.upstreamProtection(account, cache)
	require.True(t, view.PlatformSelected)
	require.True(t, view.Groups[0].PlatformSelected)
	require.Equal(t, "recovering", view.Groups[0].TTFT.State)
	require.False(t, view.Groups[1].PlatformSelected)
	require.False(t, view.Groups[2].PlatformSelected)
	require.Len(t, accountCurrentKeyTTFTDegradations(account, degradations, policies), 1)
	account.Platform = service.PlatformAnthropic
	view = h.upstreamProtection(account, cache)
	require.False(t, view.PlatformSelected, "a selected group cannot include an unselected physical account")
	for _, group := range view.Groups {
		require.Equal(t, "platform_not_selected", group.Cache.State)
	}
}
