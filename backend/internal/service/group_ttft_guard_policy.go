package service

import (
	"context"
	"fmt"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	GroupTTFTGuardModeInherit  = "inherit"
	GroupTTFTGuardModeEnabled  = "enabled"
	GroupTTFTGuardModeDisabled = "disabled"

	GroupTTFTGuardSourceGlobal   = "global"
	GroupTTFTGuardSourceGroup    = "group"
	GroupTTFTGuardSourceDisabled = "disabled"

	groupTTFTGuardPolicyCacheTTL = 30 * time.Second
)

// GroupTTFTGuardPolicyRecord is the repository projection. Nil policy fields
// mean that the group currently inherits without a persisted override row.
type GroupTTFTGuardPolicyRecord struct {
	GroupID                int64
	GroupName              string
	GroupPlatform          string
	Mode                   *string
	DegradationTTFTSeconds *int
	MinSamples             *int
	UpdatedAt              *time.Time
}

// GroupTTFTGuardPolicyRepository owns the fork table and its scheduler outbox
// transaction. It intentionally exposes no upstream Group domain object.
type GroupTTFTGuardPolicyRepository interface {
	List(ctx context.Context) ([]GroupTTFTGuardPolicyRecord, error)
	Get(ctx context.Context, groupID int64) (*GroupTTFTGuardPolicyRecord, error)
	Put(ctx context.Context, groupID int64, mode string, degradationTTFTSeconds, minSamples int) (bool, error)
}

// GroupTTFTGuardResolvedPolicy is the narrow runtime contract consumed by the
// scheduler. Threshold is a duration so callers cannot accidentally treat it
// as milliseconds.
type GroupTTFTGuardResolvedPolicy struct {
	GroupID    int64
	GroupName  string
	Mode       string
	Enabled    bool
	Threshold  time.Duration
	MinSamples int
	Source     string
}

// GroupTTFTGuardPolicyResolver is the exported runtime boundary. Implementors
// may cache, but Invalidate must make a subsequent Resolve observe storage.
type GroupTTFTGuardPolicyResolver interface {
	Resolve(ctx context.Context, groupID int64) (GroupTTFTGuardResolvedPolicy, error)
	Invalidate(groupID int64)
}

// GroupTTFTGuardPolicyInvalidationBus broadcasts fork policy invalidations to
// every application instance. The scheduler outbox remains the durable trigger;
// this bus only fans the committed event out to process-local caches/state.
type GroupTTFTGuardPolicyInvalidationBus interface {
	NotifyUpdate(ctx context.Context, groupID int64) error
	NotifyGlobalUpdate(ctx context.Context) error
	SubscribeUpdates(ctx context.Context, handler func(groupID int64))
}

type GroupTTFTGuardPolicyEventInvalidator interface {
	Invalidate(groupID int64)
	BroadcastInvalidation(ctx context.Context, groupID int64) error
}

type GroupTTFTGuardPolicyInput struct {
	Mode                   string `json:"mode"`
	DegradationTTFTSeconds *int   `json:"degradation_ttft_seconds,omitempty"`
	MinSamples             *int   `json:"min_samples,omitempty"`
}

type GroupTTFTGuardPolicyView struct {
	GroupID                         int64      `json:"group_id"`
	GroupName                       string     `json:"group_name"`
	GroupPlatform                   string     `json:"group_platform"`
	Mode                            string     `json:"mode"`
	DegradationTTFTSeconds          *int       `json:"degradation_ttft_seconds"`
	MinSamples                      *int       `json:"min_samples"`
	Enabled                         bool       `json:"enabled"`
	EffectiveEnabled                bool       `json:"effective_enabled"`
	EffectiveDegradationTTFTSeconds int        `json:"effective_degradation_ttft_seconds"`
	EffectiveMinSamples             int        `json:"effective_min_samples"`
	GlobalEnabled                   bool       `json:"global_enabled"`
	GlobalDegradationTTFTSeconds    int        `json:"global_degradation_ttft_seconds"`
	GlobalMinSamples                int        `json:"global_min_samples"`
	Source                          string     `json:"source"`
	UpdatedAt                       *time.Time `json:"updated_at,omitempty"`
}

type GroupTTFTGuardPolicyManager interface {
	GroupTTFTGuardPolicyResolver
	ListPolicies(ctx context.Context) ([]GroupTTFTGuardPolicyView, error)
	GetPolicy(ctx context.Context, groupID int64) (*GroupTTFTGuardPolicyView, error)
	PutPolicy(ctx context.Context, groupID int64, input GroupTTFTGuardPolicyInput) (*GroupTTFTGuardPolicyView, error)
}

type groupTTFTGuardGlobalSettingsProvider interface {
	GetOpenAITTFTGuardSettings(ctx context.Context) (*OpenAITTFTGuardSettings, error)
	OpenAITTFTGuardConfigSnapshot() OpenAITTFTGuardConfigSnapshot
	RefreshOpenAITTFTGuardConfig(ctx context.Context) (OpenAITTFTGuardConfigSnapshot, bool)
}

type cachedGroupTTFTGuardPolicy struct {
	policy    GroupTTFTGuardResolvedPolicy
	expiresAt time.Time
}

type GroupTTFTGuardPolicyService struct {
	repo               GroupTTFTGuardPolicyRepository
	globalSettings     groupTTFTGuardGlobalSettingsProvider
	cacheTTL           time.Duration
	cacheMu            sync.RWMutex
	cache              map[int64]cachedGroupTTFTGuardPolicy
	cacheGenerations   map[int64]uint64
	runtimeInvalidator func(int64)
	globalInvalidator  func(OpenAITTFTGuardConfigSnapshot)
	invalidationBus    GroupTTFTGuardPolicyInvalidationBus
}

func NewGroupTTFTGuardPolicyService(repo GroupTTFTGuardPolicyRepository, settings *SettingService, invalidationBus GroupTTFTGuardPolicyInvalidationBus) *GroupTTFTGuardPolicyService {
	service := &GroupTTFTGuardPolicyService{
		repo:             repo,
		globalSettings:   settings,
		cacheTTL:         groupTTFTGuardPolicyCacheTTL,
		cache:            make(map[int64]cachedGroupTTFTGuardPolicy),
		cacheGenerations: make(map[int64]uint64),
		invalidationBus:  invalidationBus,
	}
	if invalidationBus != nil {
		if settings != nil {
			settings.SetOpenAITTFTGuardInvalidationBus(invalidationBus)
		}
		invalidationBus.SubscribeUpdates(context.Background(), func(groupID int64) {
			if groupID > 0 {
				service.Invalidate(groupID)
				return
			}
			service.refreshGlobalSettingsAndInvalidateInherited(context.Background())
		})
	}
	return service
}

func (s *GroupTTFTGuardPolicyService) Resolve(ctx context.Context, groupID int64) (GroupTTFTGuardResolvedPolicy, error) {
	if groupID <= 0 {
		return GroupTTFTGuardResolvedPolicy{}, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be positive")
	}
	if cached, ok := s.loadCached(groupID); ok {
		return s.refreshInheritedPolicy(cached), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		generation := s.cacheGeneration(groupID)
		record, err := s.repo.Get(ctx, groupID)
		if err != nil {
			return GroupTTFTGuardResolvedPolicy{}, err
		}
		if err := validateGroupTTFTGuardPlatform(record.GroupPlatform); err != nil {
			return GroupTTFTGuardResolvedPolicy{}, err
		}
		global, err := s.globalSettings.GetOpenAITTFTGuardSettings(ctx)
		if err != nil {
			return GroupTTFTGuardResolvedPolicy{}, fmt.Errorf("resolve global OpenAI TTFT guard settings: %w", err)
		}
		_, resolved, err := normalizeGroupTTFTGuardPolicy(record, global)
		if err != nil {
			return GroupTTFTGuardResolvedPolicy{}, err
		}
		if s.storeCachedIfCurrent(groupID, generation, resolved) {
			return resolved, nil
		}
		if attempt == 1 {
			return resolved, nil
		}
	}
	return GroupTTFTGuardResolvedPolicy{}, fmt.Errorf("resolve group TTFT guard policy: cache generation retry exhausted")
}

func (s *GroupTTFTGuardPolicyService) Invalidate(groupID int64) {
	if s == nil || groupID <= 0 {
		return
	}
	s.cacheMu.Lock()
	delete(s.cache, groupID)
	if s.cacheGenerations == nil {
		s.cacheGenerations = make(map[int64]uint64)
	}
	s.cacheGenerations[groupID]++
	runtimeInvalidator := s.runtimeInvalidator
	s.cacheMu.Unlock()
	if runtimeInvalidator != nil {
		runtimeInvalidator(groupID)
	}
}

// SetRuntimeInvalidator connects policy invalidation to the in-process TTFT
// runtime. The callback must only clear runtime state; it must not call back
// into Invalidate.
func (s *GroupTTFTGuardPolicyService) SetRuntimeInvalidator(invalidator func(int64)) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	s.runtimeInvalidator = invalidator
	s.cacheMu.Unlock()
}

func (s *GroupTTFTGuardPolicyService) SetGlobalRuntimeInvalidator(invalidator func(OpenAITTFTGuardConfigSnapshot)) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	s.globalInvalidator = invalidator
	s.cacheMu.Unlock()
}

func (s *GroupTTFTGuardPolicyService) BroadcastInvalidation(ctx context.Context, groupID int64) error {
	if s == nil || s.invalidationBus == nil || groupID <= 0 {
		return nil
	}
	return s.invalidationBus.NotifyUpdate(ctx, groupID)
}

func (s *GroupTTFTGuardPolicyService) refreshGlobalSettingsAndInvalidateInherited(ctx context.Context) {
	if s == nil || s.globalSettings == nil {
		return
	}
	snapshot, ok := s.globalSettings.RefreshOpenAITTFTGuardConfig(ctx)
	if !ok {
		return
	}
	s.cacheMu.RLock()
	invalidate := s.globalInvalidator
	s.cacheMu.RUnlock()
	if invalidate != nil {
		invalidate(snapshot)
	}
}

func (s *GroupTTFTGuardPolicyService) ListPolicies(ctx context.Context) ([]GroupTTFTGuardPolicyView, error) {
	records, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	global, err := s.globalSettings.GetOpenAITTFTGuardSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve global OpenAI TTFT guard settings: %w", err)
	}
	result := make([]GroupTTFTGuardPolicyView, 0, len(records))
	for i := range records {
		view, _, normalizeErr := normalizeGroupTTFTGuardPolicy(&records[i], global)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		result = append(result, view)
	}
	return result, nil
}

func (s *GroupTTFTGuardPolicyService) GetPolicy(ctx context.Context, groupID int64) (*GroupTTFTGuardPolicyView, error) {
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be positive")
	}
	record, err := s.repo.Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if err := validateGroupTTFTGuardPlatform(record.GroupPlatform); err != nil {
		return nil, err
	}
	global, err := s.globalSettings.GetOpenAITTFTGuardSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve global OpenAI TTFT guard settings: %w", err)
	}
	view, _, err := normalizeGroupTTFTGuardPolicy(record, global)
	return &view, err
}

func (s *GroupTTFTGuardPolicyService) PutPolicy(ctx context.Context, groupID int64, input GroupTTFTGuardPolicyInput) (*GroupTTFTGuardPolicyView, error) {
	if groupID <= 0 {
		return nil, infraerrors.BadRequest("INVALID_GROUP_ID", "group_id must be positive")
	}
	record, err := s.repo.Get(ctx, groupID)
	if err != nil {
		return nil, err
	}
	if err := validateGroupTTFTGuardPlatform(record.GroupPlatform); err != nil {
		return nil, err
	}
	global, err := s.globalSettings.GetOpenAITTFTGuardSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve global OpenAI TTFT guard settings: %w", err)
	}
	mode := input.Mode
	if err := validateGroupTTFTGuardMode(mode); err != nil {
		return nil, err
	}
	threshold := valueOrRecordOrDefault(input.DegradationTTFTSeconds, record.DegradationTTFTSeconds, global.DegradationTTFTSeconds)
	minSamples := valueOrRecordOrDefault(input.MinSamples, record.MinSamples, global.MinSamples)
	if err := validateGroupTTFTGuardValues(threshold, minSamples); err != nil {
		return nil, err
	}
	if mode == GroupTTFTGuardModeEnabled && ((input.DegradationTTFTSeconds == nil && record.DegradationTTFTSeconds == nil) || (input.MinSamples == nil && record.MinSamples == nil)) {
		return nil, infraerrors.BadRequest("GROUP_TTFT_GUARD_VALUES_REQUIRED", "enabled mode requires degradation_ttft_seconds and min_samples")
	}
	changed, err := s.repo.Put(ctx, groupID, mode, threshold, minSamples)
	if err != nil {
		return nil, err
	}
	if changed {
		s.Invalidate(groupID)
	}
	return s.GetPolicy(ctx, groupID)
}

func normalizeGroupTTFTGuardPolicy(record *GroupTTFTGuardPolicyRecord, global *OpenAITTFTGuardSettings) (GroupTTFTGuardPolicyView, GroupTTFTGuardResolvedPolicy, error) {
	if global == nil {
		global = DefaultOpenAITTFTGuardSettings()
	}
	mode := GroupTTFTGuardModeInherit
	if record.Mode != nil {
		mode = *record.Mode
	}
	if err := validateGroupTTFTGuardMode(mode); err != nil {
		return GroupTTFTGuardPolicyView{}, GroupTTFTGuardResolvedPolicy{}, err
	}
	threshold := valueOrRecordOrDefault(nil, record.DegradationTTFTSeconds, global.DegradationTTFTSeconds)
	minSamples := valueOrRecordOrDefault(nil, record.MinSamples, global.MinSamples)
	if err := validateGroupTTFTGuardValues(threshold, minSamples); err != nil {
		return GroupTTFTGuardPolicyView{}, GroupTTFTGuardResolvedPolicy{}, err
	}
	resolved := GroupTTFTGuardResolvedPolicy{
		GroupID: record.GroupID, GroupName: record.GroupName, Mode: mode,
		Threshold: time.Duration(threshold) * time.Second, MinSamples: minSamples,
	}
	switch mode {
	case GroupTTFTGuardModeInherit:
		resolved.Enabled = global.Enabled
		resolved.Threshold = time.Duration(global.DegradationTTFTSeconds) * time.Second
		resolved.MinSamples = global.MinSamples
		resolved.Source = GroupTTFTGuardSourceGlobal
	case GroupTTFTGuardModeEnabled:
		resolved.Enabled = true
		resolved.Source = GroupTTFTGuardSourceGroup
	case GroupTTFTGuardModeDisabled:
		resolved.Enabled = false
		resolved.Source = GroupTTFTGuardSourceDisabled
	}
	view := GroupTTFTGuardPolicyView{
		GroupID: record.GroupID, GroupName: record.GroupName, GroupPlatform: record.GroupPlatform,
		Mode: mode, DegradationTTFTSeconds: record.DegradationTTFTSeconds, MinSamples: record.MinSamples,
		Enabled: resolved.Enabled, EffectiveEnabled: resolved.Enabled,
		EffectiveDegradationTTFTSeconds: int(resolved.Threshold / time.Second), EffectiveMinSamples: resolved.MinSamples,
		GlobalEnabled: global.Enabled, GlobalDegradationTTFTSeconds: global.DegradationTTFTSeconds, GlobalMinSamples: global.MinSamples,
		Source: resolved.Source, UpdatedAt: record.UpdatedAt,
	}
	return view, resolved, nil
}

func validateGroupTTFTGuardPlatform(platform string) error {
	if platform != PlatformOpenAI && platform != PlatformComposite {
		return infraerrors.BadRequest("GROUP_TTFT_GUARD_UNSUPPORTED_PLATFORM", "TTFT guard policies are only supported for OpenAI and Composite groups")
	}
	return nil
}

func validateGroupTTFTGuardMode(mode string) error {
	switch mode {
	case GroupTTFTGuardModeInherit, GroupTTFTGuardModeEnabled, GroupTTFTGuardModeDisabled:
		return nil
	default:
		return infraerrors.BadRequest("INVALID_GROUP_TTFT_GUARD_MODE", "mode must be inherit, enabled, or disabled")
	}
}

func validateGroupTTFTGuardValues(threshold, minSamples int) error {
	if threshold < minOpenAITTFTGuardDegradationSeconds || threshold > maxOpenAITTFTGuardDegradationSeconds {
		return infraerrors.BadRequest("INVALID_GROUP_TTFT_GUARD_THRESHOLD", "degradation_ttft_seconds must be between 5 and 300")
	}
	if minSamples < minOpenAITTFTGuardSamples || minSamples > maxOpenAITTFTGuardSamples {
		return infraerrors.BadRequest("INVALID_GROUP_TTFT_GUARD_MIN_SAMPLES", "min_samples must be between 2 and 20")
	}
	return nil
}

func valueOrRecordOrDefault(input, stored *int, fallback int) int {
	if input != nil {
		return *input
	}
	if stored != nil {
		return *stored
	}
	return fallback
}

func (s *GroupTTFTGuardPolicyService) loadCached(groupID int64) (GroupTTFTGuardResolvedPolicy, bool) {
	if s == nil {
		return GroupTTFTGuardResolvedPolicy{}, false
	}
	s.cacheMu.RLock()
	cached, ok := s.cache[groupID]
	s.cacheMu.RUnlock()
	if !ok || time.Now().After(cached.expiresAt) {
		if ok {
			s.clearCachedPolicy(groupID)
		}
		return GroupTTFTGuardResolvedPolicy{}, false
	}
	return cached.policy, true
}

func (s *GroupTTFTGuardPolicyService) cacheGeneration(groupID int64) uint64 {
	if s == nil {
		return 0
	}
	s.cacheMu.RLock()
	generation := s.cacheGenerations[groupID]
	s.cacheMu.RUnlock()
	return generation
}

func (s *GroupTTFTGuardPolicyService) storeCachedIfCurrent(groupID int64, generation uint64, policy GroupTTFTGuardResolvedPolicy) bool {
	if s == nil {
		return false
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if s.cacheGenerations[groupID] != generation {
		return false
	}
	s.cache[groupID] = cachedGroupTTFTGuardPolicy{policy: policy, expiresAt: time.Now().Add(s.cacheTTL)}
	return true
}

func (s *GroupTTFTGuardPolicyService) refreshInheritedPolicy(policy GroupTTFTGuardResolvedPolicy) GroupTTFTGuardResolvedPolicy {
	if s == nil || policy.Source != GroupTTFTGuardSourceGlobal || s.globalSettings == nil {
		return policy
	}
	global := normalizeOpenAITTFTGuardConfig(s.globalSettings.OpenAITTFTGuardConfigSnapshot())
	if policy.Enabled == global.Enabled && policy.Threshold == global.Threshold && policy.MinSamples == global.MinSamples {
		return policy
	}
	policy.Enabled = global.Enabled
	policy.Threshold = global.Threshold
	policy.MinSamples = global.MinSamples
	s.cacheMu.Lock()
	if cached, ok := s.cache[policy.GroupID]; ok && cached.policy.Source == GroupTTFTGuardSourceGlobal {
		cached.policy = policy
		s.cache[policy.GroupID] = cached
	}
	s.cacheMu.Unlock()
	return policy
}

func (s *GroupTTFTGuardPolicyService) clearCachedPolicy(groupID int64) {
	if s == nil {
		return
	}
	s.cacheMu.Lock()
	delete(s.cache, groupID)
	s.cacheMu.Unlock()
}
