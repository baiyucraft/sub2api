// Package forkscheduling contains the provider-neutral contracts used to
// isolate fork scheduling extensions from the official gateway packages.
package forkscheduling

import (
	"context"
	"strconv"
	"time"
)

const (
	ConcurrencyTargetAccount  = "account"
	ConcurrencyTargetUpstream = "upstream"

	UpstreamConcurrencySourceOverride  = "override"
	UpstreamConcurrencySourceProvider  = "provider"
	UpstreamConcurrencySourceUnlimited = "unlimited"
	UpstreamConcurrencySourceDefault   = "default"

	DefaultUpstreamSchedulerConcurrency = 100
	MaxUpstreamSchedulerConcurrency     = 1_000_000

	UpstreamSchedulerConcurrencyOverrideKey = "scheduler_concurrency_override"
)

// AccountRef is the minimum identity needed by fork scheduling adapters.
// It intentionally contains no credentials or mutable official domain object.
type AccountRef struct {
	ID               int64
	UpstreamConfigID int64
	UpstreamKeyID    int64
}

// GroupRelationView describes the scheduling-relevant part of one group edge.
type GroupRelationView struct {
	GroupID   int64
	Preferred bool
}

// CandidateView is a safe copy of the values used by fork-only pool rules.
type CandidateView struct {
	ID          int64      `json:"id"`
	GroupID     int64      `json:"group_id,omitempty"`
	Model       string     `json:"model,omitempty"`
	Priority    int        `json:"priority"`
	Rate        float64    `json:"rate"`
	RateKnown   bool       `json:"rate_known"`
	Preferred   bool       `json:"preferred"`
	LoadRate    int        `json:"load_rate"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
	OAuth       bool       `json:"oauth"`
	CompactTier int        `json:"compact_tier"`
}

// PreferredPoolPolicy is intentionally limited to pure candidate decisions.
// It cannot acquire slots, query repositories or mutate account state.
type PreferredPoolPolicy interface {
	PartitionPreferredIndices(candidates []CandidateView) (preferred, ordinary []int)
	CompareWithinPreferred(left, right CandidateView) int
}

type RateOrder interface {
	Compare(left, right CandidateView) int
}

// UpstreamSchedulerConcurrency is the normalized shared capacity contract.
// Limit == 0 means explicitly unlimited.
type UpstreamSchedulerConcurrency struct {
	Limit       int    `json:"limit"`
	Source      string `json:"source"`
	UsesDefault bool   `json:"uses_default"`
	Unlimited   bool   `json:"unlimited"`
	Override    *int   `json:"override,omitempty"`
}

// ConcurrencyTarget identifies exactly one account or shared upstream pool.
type ConcurrencyTarget struct {
	Kind  string `json:"kind"`
	ID    int64  `json:"id"`
	Limit int    `json:"limit"`
}

// ConcurrencyAccountView is the minimal account data needed to resolve the
// existing account-vs-shared-upstream target rule.
type ConcurrencyAccountView struct {
	AccountID         int64
	UpstreamConfigID  int64
	AccountLimit      int
	UpstreamLimit     int
	UpstreamUnlimited bool
}

type TargetResolver interface {
	Resolve(view ConcurrencyAccountView) ConcurrencyTarget
}

type CapacityAccountView struct {
	ID             int64
	MaxConcurrency int
	TargetKind     string
	TargetID       int64
}

type CapacityLoadInfo struct {
	AccountID          int64
	CurrentConcurrency int
	WaitingCount       int
	LoadRate           int
}

// CapacityRuntime exposes only the account/upstream scheduling resource
// surface. User slots, API-key activity and WebSocket ingress leases remain
// separate official concerns.
type CapacityRuntime interface {
	AcquireTargetSlot(ctx context.Context, target ConcurrencyTarget) (AcquireResult, error)
	IncrementTargetWaitCount(ctx context.Context, target ConcurrencyTarget, maxWait int) (bool, error)
	DecrementTargetWaitCount(ctx context.Context, target ConcurrencyTarget)
	GetTargetWaitingCount(ctx context.Context, target ConcurrencyTarget) (int, error)
	GetAccountsLoadBatch(ctx context.Context, accounts []CapacityAccountView) (map[int64]CapacityLoadInfo, error)
}

func (t ConcurrencyTarget) Normalized() ConcurrencyTarget {
	if t.Kind != ConcurrencyTargetUpstream || t.ID <= 0 {
		t.Kind = ConcurrencyTargetAccount
	}
	return t
}

func (t ConcurrencyTarget) Key() string {
	t = t.Normalized()
	return t.Kind + ":" + formatInt64(t.ID)
}

func formatInt64(value int64) string {
	return strconv.FormatInt(value, 10)
}

// AcquireResult is the ownership boundary for a scheduler lease.
type AcquireResult struct {
	Acquired    bool
	ReleaseFunc func()
}

// AccountRPMLimiter is the optional atomic account-level RPM gate.
type AccountRPMLimiter interface {
	TryAcquireRPM(ctx context.Context, accountID int64, limit int) (allowed bool, count int, retryAfter time.Duration, err error)
}

type RPMReader interface {
	GetRPM(ctx context.Context, accountID int64) (int, error)
	GetRPMBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error)
}

type TTFTConfig struct {
	Enabled    bool          `json:"enabled"`
	Threshold  time.Duration `json:"threshold"`
	MinSamples int           `json:"min_samples"`
	Source     string        `json:"source,omitempty"`
	GroupName  string        `json:"group_name,omitempty"`
}

type TTFTSample struct {
	GroupID      int64  `json:"group_id"`
	AccountID    int64  `json:"account_id"`
	Model        string `json:"model"`
	Success      bool   `json:"success"`
	FirstTokenMs *int   `json:"first_token_ms,omitempty"`
}

type TTFTDegradation struct {
	GroupID                 int64
	GroupName               string
	PolicySource            string
	Model                   string
	Reason                  string
	ThresholdMs             int64
	LastTTFTMs              int64
	EWMAms                  float64
	SampleCount             uint64
	DegradedAt              time.Time
	LastSampleAt            time.Time
	ExpiresAt               time.Time
	RecoverySamples         int
	RecoverySamplesRequired int
}

// TTFTObserver records one real sample and may mutate legacy state.
type TTFTObserver interface {
	Report(sample TTFTSample, cfg TTFTConfig)
}

// TTFTExcluder is stateful: exclusions may advance probe or LRU state.
type TTFTExcluder interface {
	Exclusions(candidates []CandidateView, callerExcluded map[int64]struct{}, cfg TTFTConfig) map[int64]struct{}
}

// TTFTDegradationReader is a read-only administrative view. Implementations
// may perform bounded TTL cleanup but must not advance probe state.
type TTFTDegradationReader interface {
	Degradations(accountIDs []int64) map[int64][]TTFTDegradation
}

// TTFTRuntime is a convenience aggregate; consumers should depend on the
// narrow interface above whenever they need only one operation.
type TTFTRuntime interface {
	TTFTObserver
	TTFTExcluder
	TTFTDegradationReader
}

type HealthStatus string

const (
	HealthHealthy    HealthStatus = "healthy"
	HealthDegraded   HealthStatus = "degraded"
	HealthSuspended  HealthStatus = "suspended"
	HealthObserving  HealthStatus = "observing"
	HealthRecovering HealthStatus = "recovering"
	HealthDisabled   HealthStatus = "disabled"
)

type HealthSnapshot struct {
	KeyID              int64
	Status             HealthStatus
	ObservationEnabled bool
	Reason             string
	UpdatedAt          time.Time
}

type HealthTransition struct {
	Previous HealthSnapshot
	Current  HealthSnapshot
}

type HealthReader interface {
	Snapshot(keyID int64) HealthSnapshot
	Snapshots(keyIDs []int64) map[int64]HealthSnapshot
	ExcludedKeyIDs(keyIDs []int64) map[int64]struct{}
	HasTemporaryExclusions() bool
}

// HealthEvidenceSink receives request/probe evidence. Persistence and key
// locking remain owned by the service bridge. Errors are returned so callers
// can distinguish an applied transition from a persistence rollback.
type HealthEvidenceSink interface {
	RecordTrafficSuccess(keyID int64, status, reason string, now time.Time) (HealthTransition, error)
	RecordTrafficFailure(keyID int64, status, reason string, now time.Time) (HealthTransition, error)
}

type HealthLifecycle interface {
	SetObservation(keyID int64, enabled bool, now time.Time) (HealthTransition, error)
	ResetProbeSuspension(keyID int64, now time.Time) (HealthTransition, bool, error)
	Forget(keyID int64)
}

// HealthRuntime is a convenience aggregate for one legacy registry instance.
type HealthRuntime interface {
	HealthReader
	HealthEvidenceSink
	HealthLifecycle
}

// RetryPolicyReader isolates the installed upstream retry policy from Account.
type RetryPolicyReader interface {
	StatusCodes() []int
	IsRetryableStatus(statusCode int) bool
}

// Runtime groups optional narrow views for a single application instance.
// A nil member means that capability is unavailable; it does not imply that
// the existing legacy service should be disabled.
type Runtime struct {
	TTFT            TTFTRuntime
	HealthReader    HealthReader
	HealthEvidence  HealthEvidenceSink
	HealthLifecycle HealthLifecycle
	RPMReader       RPMReader
	RPM             AccountRPMLimiter
	Capacity        CapacityRuntime
}
