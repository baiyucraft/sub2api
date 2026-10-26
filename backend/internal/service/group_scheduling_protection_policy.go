package service

import (
	"context"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/upstreamcacheguard"
)

const (
	SchedulingPriorityInherit              = "inherit"
	SchedulingPriorityCacheFirst           = "cache_first"
	SchedulingPriorityTTFTFirst            = "ttft_first"
	SettingKeySchedulingProtectionPriority = "upstream_scheduling_priority"
)

type GroupCacheGuardPolicyInput struct {
	Mode                     string   `json:"mode"`
	ThresholdPercent         *float64 `json:"threshold_percent"`
	RecoveryThresholdPercent *float64 `json:"recovery_threshold_percent"`
	MinValidRequests         *int     `json:"min_valid_requests"`
}

type GroupSchedulingProtectionPolicyInput struct {
	TTFT     *GroupTTFTGuardPolicyInput  `json:"ttft"`
	Cache    *GroupCacheGuardPolicyInput `json:"cache"`
	Priority string                      `json:"priority"`
}

type GroupSchedulingProtectionPolicyRecord struct {
	TTFT                     GroupTTFTGuardPolicyRecord
	CacheMode                *string
	ThresholdPercent         *float64
	RecoveryThresholdPercent *float64
	MinValidRequests         *int
	Priority                 *string
	UpdatedAt                *time.Time
}

type GroupSchedulingProtectionPolicyWrite struct {
	TTFTMode                 string
	TTFTThresholdSeconds     int
	TTFTMinSamples           int
	CacheMode                string
	ThresholdPercent         float64
	RecoveryThresholdPercent float64
	MinValidRequests         int
	Priority                 string
}

type GroupSchedulingProtectionPolicyRepository interface {
	List(context.Context) ([]GroupSchedulingProtectionPolicyRecord, error)
	Get(context.Context, int64) (*GroupSchedulingProtectionPolicyRecord, error)
	Put(context.Context, int64, GroupSchedulingProtectionPolicyWrite) (bool, error)
}

type GroupCacheGuardPolicyView struct {
	Mode                              string                  `json:"mode"`
	ThresholdPercent                  *float64                `json:"threshold_percent"`
	RecoveryThresholdPercent          *float64                `json:"recovery_threshold_percent"`
	MinValidRequests                  *int                    `json:"min_valid_requests"`
	EffectiveMode                     upstreamcacheguard.Mode `json:"effective_mode"`
	EffectiveThresholdPercent         float64                 `json:"effective_threshold_percent"`
	EffectiveRecoveryThresholdPercent float64                 `json:"effective_recovery_threshold_percent"`
	EffectiveMinValidRequests         int                     `json:"effective_min_valid_requests"`
	GlobalMode                        upstreamcacheguard.Mode `json:"global_mode"`
	GlobalThresholdPercent            float64                 `json:"global_threshold_percent"`
	GlobalRecoveryThresholdPercent    float64                 `json:"global_recovery_threshold_percent"`
	GlobalMinValidRequests            int                     `json:"global_min_valid_requests"`
	WindowMinutes                     int                     `json:"window_minutes"`
	CooldownMinutes                   int                     `json:"cooldown_minutes"`
	TrialPercent                      int                     `json:"trial_percent"`
	Source                            string                  `json:"source"`
}

type GroupSchedulingProtectionPolicyView struct {
	PlatformSelected  bool                      `json:"platform_selected"`
	GroupID           int64                     `json:"group_id"`
	GroupName         string                    `json:"group_name"`
	GroupPlatform     string                    `json:"group_platform"`
	TTFT              GroupTTFTGuardPolicyView  `json:"ttft"`
	Cache             GroupCacheGuardPolicyView `json:"cache"`
	Priority          string                    `json:"priority"`
	EffectivePriority string                    `json:"effective_priority"`
	GlobalPriority    string                    `json:"global_priority"`
	PrioritySource    string                    `json:"priority_source"`
	UpdatedAt         *time.Time                `json:"updated_at,omitempty"`
}

type GroupSchedulingProtectionResolvedPolicy struct {
	GroupPlatform    string
	PlatformSelected bool
	GroupID          int64
	TTFT             GroupTTFTGuardResolvedPolicy
	Cache            UpstreamCacheGuardSettings
	Priority         string
	CacheMode        string
	CacheSource      string
	PrioritySource   string
}

// GroupSchedulingProtectionPolicySnapshot is immutable after publication. It
// can be retained for one scheduling evaluation without copying every group.
// Accessors return owned values, including the only slice in a policy.
type GroupSchedulingProtectionPolicySnapshot struct {
	revision                    uint64
	policies                    map[int64]GroupSchedulingProtectionResolvedPolicy
	ordinaryRecoveryGenerations map[string]string
	globalCache                 *UpstreamCacheGuardSettings
	cachePolicies               map[int64]UpstreamCacheGuardSettings
}

func (s *GroupSchedulingProtectionPolicySnapshot) Revision() uint64 {
	if s == nil {
		return 0
	}
	return s.revision
}

func cloneSchedulingProtectionPolicy(p GroupSchedulingProtectionResolvedPolicy) GroupSchedulingProtectionResolvedPolicy {
	p.Cache.GroupIDs = append([]int64(nil), p.Cache.GroupIDs...)
	return p
}

func (s *GroupSchedulingProtectionPolicySnapshot) Policy(groupID int64) (GroupSchedulingProtectionResolvedPolicy, bool) {
	if s == nil {
		return GroupSchedulingProtectionResolvedPolicy{}, false
	}
	p, ok := s.policies[groupID]
	return cloneSchedulingProtectionPolicy(p), ok
}

func (s *GroupSchedulingProtectionPolicySnapshot) Policies() map[int64]GroupSchedulingProtectionResolvedPolicy {
	if s == nil {
		return nil
	}
	out := make(map[int64]GroupSchedulingProtectionResolvedPolicy, len(s.policies))
	for id, p := range s.policies {
		out[id] = cloneSchedulingProtectionPolicy(p)
	}
	return out
}

type GroupSchedulingProtectionPolicyResolver interface {
	ResolveProtection(context.Context, int64) (GroupSchedulingProtectionResolvedPolicy, error)
}

type GroupSchedulingProtectionPolicyManager interface {
	GroupSchedulingProtectionPolicyResolver
	ListProtectionPolicies(context.Context) ([]GroupSchedulingProtectionPolicyView, error)
	GetProtectionPolicy(context.Context, int64) (*GroupSchedulingProtectionPolicyView, error)
	PutProtectionPolicy(context.Context, int64, GroupSchedulingProtectionPolicyInput) (*GroupSchedulingProtectionPolicyView, error)
}

type GroupSchedulingProtectionPolicyService struct {
	repo       GroupSchedulingProtectionPolicyRepository
	settings   *SettingService
	ttft       *GroupTTFTGuardPolicyService
	bus        GroupTTFTGuardPolicyInvalidationBus
	mu         sync.RWMutex
	snapshot   map[int64]GroupSchedulingProtectionResolvedPolicy
	published  *GroupSchedulingProtectionPolicySnapshot
	platforms  []string
	loadedAt   time.Time
	refreshing atomic.Bool
	refreshMu  sync.Mutex
}

func NewGroupSchedulingProtectionPolicyService(repo GroupSchedulingProtectionPolicyRepository, settings *SettingService, ttft *GroupTTFTGuardPolicyService, bus GroupTTFTGuardPolicyInvalidationBus) *GroupSchedulingProtectionPolicyService {
	s := &GroupSchedulingProtectionPolicyService{repo: repo, settings: settings, ttft: ttft, bus: bus, snapshot: map[int64]GroupSchedulingProtectionResolvedPolicy{}}
	_ = s.RefreshProtectionPolicies(context.Background())
	if bus != nil {
		bus.SubscribeUpdates(context.Background(), func(int64) { _ = s.RefreshProtectionPolicies(context.Background()) })
	}
	return s
}

func validSchedulingPriority(priority string, inherit bool) bool {
	return priority == SchedulingPriorityCacheFirst || priority == SchedulingPriorityTTFTFirst || (inherit && priority == SchedulingPriorityInherit)
}

func (s *SettingService) GetSchedulingProtectionPriority(ctx context.Context) (string, error) {
	if s == nil || s.settingRepo == nil {
		return SchedulingPriorityCacheFirst, nil
	}
	value, err := s.settingRepo.GetValue(ctx, SettingKeySchedulingProtectionPriority)
	if err == ErrSettingNotFound {
		return SchedulingPriorityCacheFirst, nil
	}
	if err != nil {
		return "", err
	}
	if !validSchedulingPriority(value, false) {
		return SchedulingPriorityCacheFirst, nil
	}
	return value, nil
}

func (s *SettingService) SetSchedulingProtectionPriority(ctx context.Context, value string) error {
	if !validSchedulingPriority(value, false) {
		return infraerrors.BadRequest("INVALID_SCHEDULING_PRIORITY", "scheduling_priority must be cache_first or ttft_first")
	}
	if s == nil || s.settingRepo == nil {
		return infraerrors.ServiceUnavailable("SCHEDULING_PROTECTION_UNAVAILABLE", "scheduling protection settings are unavailable")
	}
	if err := s.settingRepo.Set(ctx, SettingKeySchedulingProtectionPriority, value); err != nil {
		return err
	}
	s.notifyOpenAITTFTGuardGlobalUpdate(ctx)
	return nil
}

func (s *GroupSchedulingProtectionPolicyService) globals(ctx context.Context) (*OpenAITTFTGuardSettings, UpstreamCacheGuardSettings, string, error) {
	if _, err := s.settings.GetSchedulingProtectionPlatforms(ctx); err != nil {
		return nil, UpstreamCacheGuardSettings{}, "", err
	}
	ttft, err := s.settings.GetOpenAITTFTGuardSettings(ctx)
	if err != nil {
		return nil, UpstreamCacheGuardSettings{}, "", err
	}
	cache, err := s.settings.GetUpstreamCacheGuardSettings(ctx)
	if err != nil {
		return nil, cache, "", err
	}
	priority, err := s.settings.GetSchedulingProtectionPriority(ctx)
	return ttft, cache, priority, err
}

func normalizeSchedulingProtectionPolicy(record GroupSchedulingProtectionPolicyRecord, ttftGlobal *OpenAITTFTGuardSettings, cacheGlobal UpstreamCacheGuardSettings, priorityGlobal string, platformScope ...[]string) (GroupSchedulingProtectionPolicyView, GroupSchedulingProtectionResolvedPolicy, error) {
	platforms := DefaultSchedulingProtectionPlatforms()
	if len(platformScope) > 0 {
		platforms = platformScope[0]
	}
	selected := schedulingProtectionContainsPlatform(platforms, record.TTFT.GroupPlatform)
	ttft, ttftResolved, err := normalizeGroupTTFTGuardPolicy(&record.TTFT, ttftGlobal)
	if err != nil {
		return GroupSchedulingProtectionPolicyView{}, GroupSchedulingProtectionResolvedPolicy{}, err
	}
	if record.TTFT.GroupPlatform != PlatformOpenAI {
		ttft.EffectiveEnabled, ttft.Enabled, ttftResolved.Enabled = false, false, false
	}
	mode := GroupTTFTGuardModeInherit
	if record.CacheMode != nil {
		mode = *record.CacheMode
	}
	if err := validateGroupTTFTGuardMode(mode); err != nil {
		return GroupSchedulingProtectionPolicyView{}, GroupSchedulingProtectionResolvedPolicy{}, err
	}
	cache := cacheGlobal
	cache.GroupIDs = []int64{}
	source := GroupTTFTGuardSourceGlobal
	if mode == GroupTTFTGuardModeEnabled {
		cache.Mode = upstreamcacheguard.Protect
		source = GroupTTFTGuardSourceGroup
		if record.ThresholdPercent != nil {
			cache.ThresholdPercent = *record.ThresholdPercent
		}
		if record.RecoveryThresholdPercent != nil {
			cache.RecoveryThresholdPercent = *record.RecoveryThresholdPercent
		}
		if record.MinValidRequests != nil {
			cache.MinValidRequests = *record.MinValidRequests
		}
	} else if mode == GroupTTFTGuardModeDisabled {
		cache.Mode, source = upstreamcacheguard.Disabled, GroupTTFTGuardSourceDisabled
	}
	if err := cache.Validate(); err != nil {
		return GroupSchedulingProtectionPolicyView{}, GroupSchedulingProtectionResolvedPolicy{}, infraerrors.BadRequest("INVALID_GROUP_CACHE_GUARD_POLICY", err.Error())
	}
	priority, effectivePriority, prioritySource := SchedulingPriorityInherit, priorityGlobal, GroupTTFTGuardSourceGlobal
	if record.Priority != nil {
		priority = *record.Priority
	}
	if !validSchedulingPriority(priority, true) {
		return GroupSchedulingProtectionPolicyView{}, GroupSchedulingProtectionResolvedPolicy{}, infraerrors.BadRequest("INVALID_SCHEDULING_PRIORITY", "invalid group scheduling priority")
	}
	if priority != SchedulingPriorityInherit {
		effectivePriority, prioritySource = priority, GroupTTFTGuardSourceGroup
	}
	cache.Priority = effectivePriority
	view := GroupSchedulingProtectionPolicyView{GroupID: record.TTFT.GroupID, GroupName: record.TTFT.GroupName, GroupPlatform: record.TTFT.GroupPlatform, TTFT: ttft,
		Cache: GroupCacheGuardPolicyView{Mode: mode, ThresholdPercent: record.ThresholdPercent, RecoveryThresholdPercent: record.RecoveryThresholdPercent, MinValidRequests: record.MinValidRequests,
			EffectiveMode: cache.Mode, EffectiveThresholdPercent: cache.ThresholdPercent, EffectiveRecoveryThresholdPercent: cache.RecoveryThresholdPercent, EffectiveMinValidRequests: cache.MinValidRequests,
			GlobalMode: cacheGlobal.Mode, GlobalThresholdPercent: cacheGlobal.ThresholdPercent, GlobalRecoveryThresholdPercent: cacheGlobal.RecoveryThresholdPercent, GlobalMinValidRequests: cacheGlobal.MinValidRequests,
			WindowMinutes: cache.WindowMinutes, CooldownMinutes: cache.CooldownMinutes, TrialPercent: cache.TrialPercent, Source: source},
		Priority: priority, EffectivePriority: effectivePriority, GlobalPriority: priorityGlobal, PrioritySource: prioritySource, UpdatedAt: record.UpdatedAt}
	view.PlatformSelected = selected
	view.TTFT.PlatformSelected = selected
	if !selected {
		view.TTFT.Enabled, view.TTFT.EffectiveEnabled = false, false
		view.Cache.EffectiveMode = upstreamcacheguard.Disabled
	}
	return view, GroupSchedulingProtectionResolvedPolicy{GroupID: view.GroupID, GroupPlatform: record.TTFT.GroupPlatform, PlatformSelected: selected, TTFT: ttftResolved, Cache: cache, Priority: effectivePriority, CacheMode: mode, CacheSource: source, PrioritySource: prioritySource}, nil
}

func (s *GroupSchedulingProtectionPolicyService) RefreshProtectionPolicies(ctx context.Context) error {
	// Serialize refreshes so an older database read cannot replace a newer PUT.
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	records, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	ttft, cache, priority, err := s.globals(ctx)
	if err != nil {
		return err
	}
	next := make(map[int64]GroupSchedulingProtectionResolvedPolicy, len(records))
	for _, record := range records {
		_, resolved, err := normalizeSchedulingProtectionPolicy(record, ttft, cache, priority, s.settings.SchedulingProtectionPlatformsSnapshot())
		if err != nil {
			return err
		}
		next[record.TTFT.GroupID] = resolved
	}
	platforms := s.settings.SchedulingProtectionPlatformsSnapshot()
	revisions, err := s.settings.ordinaryRecoverySettingRevisions(ctx)
	if err != nil {
		return err
	}
	generations := ordinaryRecoveryPolicyGenerations(cache, next, records, revisions)
	s.mu.Lock()
	// Refresh success updates health even when policy content is unchanged.
	// Never mutate a published map: evaluations may still retain its revision.
	if s.published == nil || !reflect.DeepEqual(s.snapshot, next) || !reflect.DeepEqual(s.platforms, platforms) || !reflect.DeepEqual(s.published.ordinaryRecoveryGenerations, generations) || s.published.globalCache == nil || cacheGuardConfigurationIdentity(*s.published.globalCache) != cacheGuardConfigurationIdentity(cache) {
		revision := uint64(1)
		if s.published != nil {
			revision = s.published.revision + 1
		}
		s.snapshot = next
		global := cache
		global.GroupIDs = append([]int64(nil), cache.GroupIDs...)
		values := make(map[int64]UpstreamCacheGuardSettings, len(next))
		for id, policy := range next {
			values[id] = policy.Cache
		}
		s.published = &GroupSchedulingProtectionPolicySnapshot{revision: revision, policies: next, ordinaryRecoveryGenerations: generations, globalCache: &global, cachePolicies: values}
		s.platforms = platforms
	}
	s.loadedAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *GroupSchedulingProtectionPolicyService) ProtectionPoliciesSnapshot() *GroupSchedulingProtectionPolicySnapshot {
	if s == nil {
		return nil
	}
	s.refreshStale()
	s.mu.RLock()
	published := s.published
	s.mu.RUnlock()
	if published != nil {
		return published
	}
	// Lightweight integrations may supply an already loaded map. Publish an
	// owned copy once so captured request identity remains stable as in production.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published == nil {
		policies := make(map[int64]GroupSchedulingProtectionResolvedPolicy, len(s.snapshot))
		for id, policy := range s.snapshot {
			policies[id] = cloneSchedulingProtectionPolicy(policy)
		}
		s.published = &GroupSchedulingProtectionPolicySnapshot{revision: 1, policies: policies}
	}
	return s.published
}

// ProtectionPolicy returns the stored policy. The live platform gate remains
// separate so pausing a platform never destroys observations or recovery state.
func (s *GroupSchedulingProtectionPolicyService) ProtectionPolicy(groupID int64) (GroupSchedulingProtectionResolvedPolicy, bool) {
	return s.ProtectionPoliciesSnapshot().Policy(groupID)
}

func (s *GroupSchedulingProtectionPolicyService) refreshStale() {
	s.mu.RLock()
	stale := time.Since(s.loadedAt) >= 30*time.Second
	s.mu.RUnlock()
	if stale && s.refreshing.CompareAndSwap(false, true) {
		go func() {
			defer s.refreshing.Store(false)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = s.RefreshProtectionPolicies(ctx)
		}()
	}
}

func (s *GroupSchedulingProtectionPolicyService) CachePolicies() map[int64]UpstreamCacheGuardSettings {
	s.refreshStale()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[int64]UpstreamCacheGuardSettings, len(s.snapshot))
	for id, policy := range s.snapshot {
		policy.Cache.GroupIDs = append([]int64{}, policy.Cache.GroupIDs...)
		out[id] = policy.Cache
	}
	return out
}

// PlatformSelected reads the shared platform range without database I/O.
func (s *GroupSchedulingProtectionPolicyService) PlatformSelected(platform string) bool {
	return schedulingProtectionContainsPlatform(s.ProtectionPlatformsSnapshot(), platform)
}

func (s *GroupSchedulingProtectionPolicyService) ProtectionPlatformsSnapshot() []string {
	if s == nil {
		return DefaultSchedulingProtectionPlatforms()
	}
	if s.settings != nil {
		return s.settings.SchedulingProtectionPlatformsSnapshot()
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.platforms == nil {
		return DefaultSchedulingProtectionPlatforms()
	}
	return append([]string{}, s.platforms...)
}

func (s *GroupSchedulingProtectionPolicyService) ProtectionGroupPlatform(groupID int64) (string, bool) {
	if s == nil {
		return "", false
	}
	s.refreshStale()
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy, ok := s.snapshot[groupID]
	return policy.GroupPlatform, ok
}

func (s *GroupSchedulingProtectionPolicyService) effectiveProtectionPolicy(policy GroupSchedulingProtectionResolvedPolicy) GroupSchedulingProtectionResolvedPolicy {
	policy.PlatformSelected = s.PlatformSelected(policy.GroupPlatform)
	if !policy.PlatformSelected {
		policy.TTFT.Enabled = false
		policy.Cache.Mode = upstreamcacheguard.Disabled
	}
	return policy
}

// ProtectionPolicies returns a caller-owned hot snapshot without database I/O.
func (s *GroupSchedulingProtectionPolicyService) ProtectionPolicies() map[int64]GroupSchedulingProtectionResolvedPolicy {
	return s.ProtectionPoliciesSnapshot().Policies()
}

func (s *GroupSchedulingProtectionPolicyService) CacheSettings(groupID int64) (UpstreamCacheGuardSettings, bool) {
	s.refreshStale()
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy, ok := s.snapshot[groupID]
	return cloneSchedulingProtectionPolicy(policy).Cache, ok
}

func (s *GroupSchedulingProtectionPolicyService) CachePolicyMetadata(groupID int64) (string, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	policy, ok := s.snapshot[groupID]
	if !ok {
		return GroupTTFTGuardModeInherit, GroupTTFTGuardSourceGlobal
	}
	return policy.CacheMode, policy.CacheSource
}

func (s *GroupSchedulingProtectionPolicyService) ResolveProtection(ctx context.Context, groupID int64) (GroupSchedulingProtectionResolvedPolicy, error) {
	if groupID <= 0 {
		return GroupSchedulingProtectionResolvedPolicy{}, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be positive")
	}
	s.refreshStale()
	s.mu.RLock()
	policy, ok := s.snapshot[groupID]
	s.mu.RUnlock()
	if ok {
		return s.effectiveProtectionPolicy(cloneSchedulingProtectionPolicy(policy)), nil
	}
	record, err := s.repo.Get(ctx, groupID)
	if err != nil {
		return GroupSchedulingProtectionResolvedPolicy{}, err
	}
	ttft, cache, priority, err := s.globals(ctx)
	if err != nil {
		return GroupSchedulingProtectionResolvedPolicy{}, err
	}
	_, resolved, err := normalizeSchedulingProtectionPolicy(*record, ttft, cache, priority, s.settings.SchedulingProtectionPlatformsSnapshot())
	return s.effectiveProtectionPolicy(resolved), err
}

func (s *GroupSchedulingProtectionPolicyService) ListProtectionPolicies(ctx context.Context) ([]GroupSchedulingProtectionPolicyView, error) {
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	ttft, cache, priority, err := s.globals(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]GroupSchedulingProtectionPolicyView, 0, len(records))
	for _, record := range records {
		view, _, err := normalizeSchedulingProtectionPolicy(record, ttft, cache, priority, s.settings.SchedulingProtectionPlatformsSnapshot())
		if err != nil {
			return nil, err
		}
		if view.PlatformSelected {
			views = append(views, view)
		}
	}
	return views, nil
}

func (s *GroupSchedulingProtectionPolicyService) GetProtectionPolicy(ctx context.Context, id int64) (*GroupSchedulingProtectionPolicyView, error) {
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	ttft, cache, priority, err := s.globals(ctx)
	if err != nil {
		return nil, err
	}
	view, _, err := normalizeSchedulingProtectionPolicy(*record, ttft, cache, priority, s.settings.SchedulingProtectionPlatformsSnapshot())
	return &view, err
}

func (s *GroupSchedulingProtectionPolicyService) PutProtectionPolicy(ctx context.Context, id int64, input GroupSchedulingProtectionPolicyInput) (*GroupSchedulingProtectionPolicyView, error) {
	if input.TTFT == nil || input.Cache == nil || !validSchedulingPriority(input.Priority, true) {
		return nil, infraerrors.BadRequest("INVALID_GROUP_PROTECTION_POLICY", "ttft, cache and priority are required")
	}
	record, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.settings.GetSchedulingProtectionPlatforms(ctx); err != nil {
		return nil, err
	}
	if !s.PlatformSelected(record.TTFT.GroupPlatform) {
		return nil, groupProtectionPlatformNotSelected()
	}
	if err := validateGroupTTFTGuardMode(input.TTFT.Mode); err != nil {
		return nil, err
	}
	if err := validateGroupTTFTGuardMode(input.Cache.Mode); err != nil {
		return nil, err
	}
	if input.TTFT.Mode == GroupTTFTGuardModeEnabled {
		if err := validateGroupTTFTGuardPlatform(record.TTFT.GroupPlatform); err != nil {
			return nil, err
		}
	}
	ttft, cache, _, err := s.globals(ctx)
	if err != nil {
		return nil, err
	}
	w := GroupSchedulingProtectionPolicyWrite{TTFTMode: input.TTFT.Mode, TTFTThresholdSeconds: valueOrRecordOrDefault(input.TTFT.DegradationTTFTSeconds, record.TTFT.DegradationTTFTSeconds, ttft.DegradationTTFTSeconds), TTFTMinSamples: valueOrRecordOrDefault(input.TTFT.MinSamples, record.TTFT.MinSamples, ttft.MinSamples), CacheMode: input.Cache.Mode, ThresholdPercent: cache.ThresholdPercent, RecoveryThresholdPercent: cache.RecoveryThresholdPercent, MinValidRequests: cache.MinValidRequests, Priority: input.Priority}
	if err := validateGroupTTFTGuardValues(w.TTFTThresholdSeconds, w.TTFTMinSamples); err != nil {
		return nil, err
	}
	if input.Cache.ThresholdPercent != nil {
		w.ThresholdPercent = *input.Cache.ThresholdPercent
	} else if record.ThresholdPercent != nil {
		w.ThresholdPercent = *record.ThresholdPercent
	}
	if input.Cache.RecoveryThresholdPercent != nil {
		w.RecoveryThresholdPercent = *input.Cache.RecoveryThresholdPercent
	} else if record.RecoveryThresholdPercent != nil {
		w.RecoveryThresholdPercent = *record.RecoveryThresholdPercent
	}
	if input.Cache.MinValidRequests != nil {
		w.MinValidRequests = *input.Cache.MinValidRequests
	} else if record.MinValidRequests != nil {
		w.MinValidRequests = *record.MinValidRequests
	}
	cache.ThresholdPercent, cache.RecoveryThresholdPercent, cache.MinValidRequests = w.ThresholdPercent, w.RecoveryThresholdPercent, w.MinValidRequests
	if err := cache.Validate(); err != nil {
		return nil, infraerrors.BadRequest("INVALID_GROUP_CACHE_GUARD_POLICY", err.Error())
	}
	if _, err := s.repo.Put(ctx, id, w); err != nil {
		return nil, err
	}
	if s.ttft != nil {
		s.ttft.Invalidate(id)
	}
	_ = s.RefreshProtectionPolicies(ctx)
	if s.bus != nil {
		_ = s.bus.NotifyUpdate(ctx, id)
	}
	return s.GetProtectionPolicy(ctx, id)
}
