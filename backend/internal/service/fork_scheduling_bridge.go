package service

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/forkscheduling"
	"github.com/Wei-Shaw/sub2api/internal/forkscheduling/legacy"
)

// legacyCandidateView converts an official service Account into the bounded
// value consumed by fork-only comparison rules. No credential or mutable
// service object crosses the package boundary.
func legacyCandidateView(account *Account, preferred bool, loadRate int) legacy.Candidate {
	rate, rateKnown := upstreamSourceSchedulingRate(account)
	var lastUsedAt *time.Time
	if account != nil && account.LastUsedAt != nil {
		value := *account.LastUsedAt
		lastUsedAt = &value
	}
	return legacy.Candidate{
		ID:          accountID(account),
		Priority:    accountPriority(account),
		Rate:        rate,
		RateKnown:   rateKnown,
		Preferred:   preferred,
		LoadRate:    loadRate,
		LastUsedAt:  lastUsedAt,
		OAuth:       account != nil && account.IsOpenAIOAuthLike(),
		CompactTier: openAICompactSupportTier(account),
	}
}

func accountID(account *Account) int64 {
	if account == nil {
		return 0
	}
	return account.ID
}

func accountPriority(account *Account) int {
	if account == nil {
		return 0
	}
	return account.Priority
}

type legacyTTFTRuntime struct {
	guard *openAITTFTGuard
}

func (r legacyTTFTRuntime) Report(sample forkscheduling.TTFTSample, cfg forkscheduling.TTFTConfig) {
	if r.guard == nil {
		return
	}
	r.guard.report(sample.GroupID, sample.AccountID, sample.Model, sample.Success, sample.FirstTokenMs, openAITTFTGuardConfigFromContract(cfg))
}

func (r legacyTTFTRuntime) Exclusions(candidates []forkscheduling.CandidateView, callerExcluded map[int64]struct{}, cfg forkscheduling.TTFTConfig) map[int64]struct{} {
	if r.guard == nil {
		return nil
	}
	legacyCandidates := make([]openAITTFTGuardCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		legacyCandidates = append(legacyCandidates, openAITTFTGuardCandidate{groupID: candidate.GroupID, accountID: candidate.ID, model: candidate.Model})
	}
	return r.guard.exclusions(legacyCandidates, callerExcluded, openAITTFTGuardConfigFromContract(cfg))
}

func (r legacyTTFTRuntime) Degradations(accountIDs []int64) map[int64][]forkscheduling.TTFTDegradation {
	if r.guard == nil {
		return nil
	}
	degradations := r.guard.degradations(accountIDs)
	if len(degradations) == 0 {
		return nil
	}
	result := make(map[int64][]forkscheduling.TTFTDegradation, len(degradations))
	for accountID, items := range degradations {
		converted := make([]forkscheduling.TTFTDegradation, 0, len(items))
		for _, item := range items {
			converted = append(converted, forkscheduling.TTFTDegradation{
				GroupID:                 item.GroupID,
				GroupName:               item.GroupName,
				PolicySource:            item.PolicySource,
				Model:                   item.Model,
				Reason:                  item.Reason,
				ThresholdMs:             item.ThresholdMs,
				LastTTFTMs:              item.LastTTFTMs,
				EWMAms:                  item.EWMAms,
				SampleCount:             item.SampleCount,
				DegradedAt:              item.DegradedAt,
				LastSampleAt:            item.LastSampleAt,
				ExpiresAt:               item.ExpiresAt,
				RecoverySamples:         item.RecoverySamples,
				RecoverySamplesRequired: item.RecoverySamplesRequired,
			})
		}
		result[accountID] = converted
	}
	return result
}

func openAITTFTGuardConfigFromContract(cfg forkscheduling.TTFTConfig) OpenAITTFTGuardConfigSnapshot {
	return OpenAITTFTGuardConfigSnapshot{Enabled: cfg.Enabled, Threshold: cfg.Threshold, MinSamples: cfg.MinSamples, Source: cfg.Source, GroupName: cfg.GroupName}
}

type legacyHealthRuntime struct {
	registry *UpstreamHealthRegistry
	owner    legacyHealthRuntimeOwner
}

// legacyHealthRuntimeOwner is intentionally unexported. It is implemented by
// UpstreamConfigService so the contract layer cannot acquire a second health
// writer or bypass the service's key lock, persistence and rollback path.
type legacyHealthRuntimeOwner interface {
	recordUpstreamTrafficEvidenceAt(ctx context.Context, keyID int64, success bool, status, reason string, now time.Time) (UpstreamHealthTransition, error)
	setKeyObservationAt(ctx context.Context, keyID int64, enabled bool, now time.Time) (UpstreamHealthTransition, error)
	clearProbeSuspensionAt(ctx context.Context, keyID int64, now time.Time) (UpstreamHealthTransition, bool, error)
}

type legacyRetryPolicy struct {
	account *Account
}

type legacyCapacityRuntime struct {
	service *ConcurrencyService
}

func (r legacyCapacityRuntime) AcquireTargetSlot(ctx context.Context, target forkscheduling.ConcurrencyTarget) (forkscheduling.AcquireResult, error) {
	if r.service == nil {
		return forkscheduling.AcquireResult{Acquired: true, ReleaseFunc: func() {}}, nil
	}
	result, err := r.service.AcquireTargetSlot(ctx, target)
	if err != nil || result == nil {
		return forkscheduling.AcquireResult{}, err
	}
	return forkscheduling.AcquireResult{Acquired: result.Acquired, ReleaseFunc: result.ReleaseFunc}, nil
}

func (r legacyCapacityRuntime) IncrementTargetWaitCount(ctx context.Context, target forkscheduling.ConcurrencyTarget, maxWait int) (bool, error) {
	if r.service == nil {
		return true, nil
	}
	return r.service.IncrementTargetWaitCount(ctx, target, maxWait)
}

func (r legacyCapacityRuntime) DecrementTargetWaitCount(ctx context.Context, target forkscheduling.ConcurrencyTarget) {
	if r.service != nil {
		r.service.DecrementTargetWaitCount(ctx, target)
	}
}

func (r legacyCapacityRuntime) GetTargetWaitingCount(ctx context.Context, target forkscheduling.ConcurrencyTarget) (int, error) {
	if r.service == nil {
		return 0, nil
	}
	return r.service.GetTargetWaitingCount(ctx, target)
}

func (r legacyCapacityRuntime) GetAccountsLoadBatch(ctx context.Context, accounts []forkscheduling.CapacityAccountView) (map[int64]forkscheduling.CapacityLoadInfo, error) {
	if r.service == nil {
		return map[int64]forkscheduling.CapacityLoadInfo{}, nil
	}
	views := make([]AccountWithConcurrency, 0, len(accounts))
	for _, account := range accounts {
		views = append(views, AccountWithConcurrency{ID: account.ID, MaxConcurrency: account.MaxConcurrency, TargetKind: account.TargetKind, TargetID: account.TargetID})
	}
	loads, err := r.service.GetAccountsLoadBatch(ctx, views)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]forkscheduling.CapacityLoadInfo, len(loads))
	for accountID, load := range loads {
		if load == nil {
			continue
		}
		result[accountID] = forkscheduling.CapacityLoadInfo{AccountID: load.AccountID, CurrentConcurrency: load.CurrentConcurrency, WaitingCount: load.WaitingCount, LoadRate: load.LoadRate}
	}
	return result, nil
}

func (p legacyRetryPolicy) StatusCodes() []int {
	if codes, configured := globalPoolModeRetryStatusConfigured(); configured && p.account != nil && p.account.IsUpstreamBound() {
		return cloneUpstreamPoolModeRetryStatusCodes(codes)
	}
	if p.account != nil {
		if codes := p.account.GetPoolModeRetryStatusCodes(); codes != nil {
			return cloneUpstreamPoolModeRetryStatusCodes(codes)
		}
	}
	return cloneUpstreamPoolModeRetryStatusCodes(legacy.DefaultPoolModeRetryStatusCodes)
}

func (p legacyRetryPolicy) IsRetryableStatus(statusCode int) bool {
	return legacy.RetryableStatus(p.StatusCodes(), statusCode)
}

func retryPolicyForAccount(account *Account) forkscheduling.RetryPolicyReader {
	return legacyRetryPolicy{account: account}
}

func (r legacyHealthRuntime) Snapshot(keyID int64) forkscheduling.HealthSnapshot {
	if r.registry == nil {
		return forkscheduling.HealthSnapshot{KeyID: keyID}
	}
	return healthSnapshotToContract(r.registry.Snapshot(keyID))
}

func (r legacyHealthRuntime) Snapshots(keyIDs []int64) map[int64]forkscheduling.HealthSnapshot {
	if r.registry == nil {
		return nil
	}
	snapshots := r.registry.Snapshots(keyIDs)
	result := make(map[int64]forkscheduling.HealthSnapshot, len(snapshots))
	for keyID, snapshot := range snapshots {
		result[keyID] = healthSnapshotToContract(snapshot)
	}
	return result
}

func (r legacyHealthRuntime) ExcludedKeyIDs(keyIDs []int64) map[int64]struct{} {
	if r.registry == nil {
		return nil
	}
	return r.registry.ExcludedKeyIDs(keyIDs)
}

func (r legacyHealthRuntime) HasTemporaryExclusions() bool {
	return r.registry != nil && r.registry.HasTemporaryExclusions()
}

func (r legacyHealthRuntime) RecordTrafficSuccess(keyID int64, status, reason string, now time.Time) (forkscheduling.HealthTransition, error) {
	if r.registry == nil {
		return forkscheduling.HealthTransition{}, nil
	}
	previous := r.registry.Snapshot(keyID)
	if owner := r.healthOwner(); owner != nil {
		transition, err := owner.recordUpstreamTrafficEvidenceAt(context.Background(), keyID, true, status, reason, now)
		return healthTransitionToContract(transition), err
	}
	return healthTransitionToContract(UpstreamHealthTransition{Previous: previous, Current: previous}), nil
}

func (r legacyHealthRuntime) RecordTrafficFailure(keyID int64, status, reason string, now time.Time) (forkscheduling.HealthTransition, error) {
	if r.registry == nil {
		return forkscheduling.HealthTransition{}, nil
	}
	previous := r.registry.Snapshot(keyID)
	if owner := r.healthOwner(); owner != nil {
		transition, err := owner.recordUpstreamTrafficEvidenceAt(context.Background(), keyID, false, status, reason, now)
		return healthTransitionToContract(transition), err
	}
	return healthTransitionToContract(UpstreamHealthTransition{Previous: previous, Current: previous}), nil
}

func (r legacyHealthRuntime) SetObservation(keyID int64, enabled bool, now time.Time) (forkscheduling.HealthTransition, error) {
	if r.registry == nil {
		return forkscheduling.HealthTransition{}, nil
	}
	previous := r.registry.Snapshot(keyID)
	if owner := r.healthOwner(); owner != nil {
		transition, err := owner.setKeyObservationAt(context.Background(), keyID, enabled, now)
		return healthTransitionToContract(transition), err
	}
	return healthTransitionToContract(UpstreamHealthTransition{Previous: previous, Current: previous}), nil
}

func (r legacyHealthRuntime) ResetProbeSuspension(keyID int64, now time.Time) (forkscheduling.HealthTransition, bool, error) {
	if r.registry == nil {
		return forkscheduling.HealthTransition{}, false, nil
	}
	previous := r.registry.Snapshot(keyID)
	if owner := r.healthOwner(); owner != nil {
		transition, changed, err := owner.clearProbeSuspensionAt(context.Background(), keyID, now)
		return healthTransitionToContract(transition), changed, err
	}
	return healthTransitionToContract(UpstreamHealthTransition{Previous: previous, Current: previous}), false, nil
}

func (r legacyHealthRuntime) Forget(keyID int64) {
	if r.registry != nil {
		r.registry.Forget(keyID)
	}
}

func (r legacyHealthRuntime) healthOwner() legacyHealthRuntimeOwner {
	if r.owner != nil {
		return r.owner
	}
	if recorder := upstreamHealthEvidenceRecorder(); recorder != nil {
		owner, _ := recorder.(legacyHealthRuntimeOwner)
		return owner
	}
	return nil
}

func healthSnapshotToContract(snapshot UpstreamHealthSnapshot) forkscheduling.HealthSnapshot {
	return forkscheduling.HealthSnapshot{KeyID: snapshot.KeyID, Status: forkscheduling.HealthStatus(snapshot.Status), ObservationEnabled: snapshot.ObservationEnabled, Reason: snapshot.Reason, UpdatedAt: snapshot.UpdatedAt}
}

func healthTransitionToContract(transition UpstreamHealthTransition) forkscheduling.HealthTransition {
	return forkscheduling.HealthTransition{Previous: healthSnapshotToContract(transition.Previous), Current: healthSnapshotToContract(transition.Current)}
}

func (s *OpenAIGatewayService) forkTTFTRuntime() forkscheduling.TTFTRuntime {
	if s == nil {
		return legacyTTFTRuntime{}
	}
	return s.ForkSchedulingRuntime().TTFT
}

func (s *OpenAIGatewayService) ForkSchedulingRuntime() forkscheduling.Runtime {
	if s == nil {
		return forkscheduling.Runtime{}
	}
	var rpm forkscheduling.AccountRPMLimiter
	var rpmReader forkscheduling.RPMReader
	if cache := s.currentRPMCache(); cache != nil {
		if candidate, ok := cache.(forkscheduling.RPMReader); ok {
			rpmReader = candidate
		}
		if candidate, ok := cache.(forkscheduling.AccountRPMLimiter); ok {
			rpm = candidate
		}
	}
	healthRuntime := legacyHealthRuntime{registry: GlobalUpstreamHealthRegistry()}
	return forkscheduling.Runtime{
		TTFT:            legacyTTFTRuntime{guard: s.getOpenAITTFTGuard()},
		HealthReader:    healthRuntime,
		HealthEvidence:  healthRuntime,
		HealthLifecycle: healthRuntime,
		RPMReader:       rpmReader,
		RPM:             rpm,
		Capacity:        legacyCapacityRuntime{service: s.concurrencyService},
	}
}

func (s *OpenAIGatewayService) forkHealthReader() forkscheduling.HealthReader {
	if s == nil {
		return legacyHealthRuntime{}
	}
	return s.ForkSchedulingRuntime().HealthReader
}

func (s *GatewayService) forkHealthReader() forkscheduling.HealthReader {
	if s == nil {
		return legacyHealthRuntime{}
	}
	return legacyHealthRuntime{registry: GlobalUpstreamHealthRegistry()}
}
