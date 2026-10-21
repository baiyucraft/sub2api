// Package upstreamcacheguard implements persistent, pre-billing cache affinity policy.
// Logical session keys must be caller-provided HMAC digests, never raw headers.
package upstreamcacheguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/upstreamcacheusage"
)

type Mode string

const (
	Disabled Mode = "disabled"
	Observe  Mode = "observe"
	Protect  Mode = "protect"
)

type Settings struct {
	Mode                            Mode    `json:"mode"`
	GroupIDs                        []int64 `json:"group_ids"`
	ThresholdPercent                float64 `json:"threshold_percent"`
	WindowMinutes                   int     `json:"window_minutes"`
	MinValidRequests                int     `json:"min_valid_requests"`
	RecoveryThresholdPercent        float64 `json:"recovery_threshold_percent"`
	CooldownMinutes                 int     `json:"cooldown_minutes"`
	TrialPercent                    int     `json:"trial_percent"`
	ProtectionEvidenceExpiryMinutes int     `json:"protection_evidence_expiry_minutes"`
	Priority                        string  `json:"scheduling_priority,omitempty"`
}

func DefaultSettings() Settings {
	return Settings{Mode: Disabled, GroupIDs: []int64{}, ThresholdPercent: 75, WindowMinutes: 60, MinValidRequests: 20, RecoveryThresholdPercent: 80, CooldownMinutes: 10, TrialPercent: 10, ProtectionEvidenceExpiryMinutes: 15, Priority: "cache_first"}
}

// Rows written before this setting existed decode as zero. Interpret those
// values consistently in durable projections as well as the settings API.
func protectionEvidenceExpiryMinutes(s Settings) int {
	if s.ProtectionEvidenceExpiryMinutes == 0 {
		return 15
	}
	return s.ProtectionEvidenceExpiryMinutes
}
func (s Settings) Validate() error {
	if s.Priority != "" && s.Priority != "cache_first" && s.Priority != "ttft_first" {
		return errors.New("invalid scheduling protection priority")
	}
	if s.Mode != Disabled && s.Mode != Observe && s.Mode != Protect {
		return errors.New("invalid cache guard mode")
	}
	if math.IsNaN(s.ThresholdPercent) || math.IsInf(s.ThresholdPercent, 0) || s.ThresholdPercent < 0 || s.ThresholdPercent > 100 || math.IsNaN(s.RecoveryThresholdPercent) || math.IsInf(s.RecoveryThresholdPercent, 0) || s.RecoveryThresholdPercent <= s.ThresholdPercent || s.RecoveryThresholdPercent > 100 {
		return errors.New("invalid cache guard thresholds")
	}
	// Zero is accepted for rows written before this field existed and means the
	// default expiry. Settings service normalizes it before persistence.
	if s.WindowMinutes < 5 || s.WindowMinutes > 1440 || s.MinValidRequests < 1 || s.MinValidRequests > 100000 || s.CooldownMinutes < 1 || s.CooldownMinutes > 1440 || s.TrialPercent < 1 || s.TrialPercent > 100 || s.ProtectionEvidenceExpiryMinutes < 0 || s.ProtectionEvidenceExpiryMinutes > 1440 {
		return errors.New("invalid cache guard limits")
	}
	for _, id := range s.GroupIDs {
		if id <= 0 {
			return errors.New("invalid cache guard group")
		}
	}
	return nil
}
func (s Settings) ContainsGroup(id int64) bool {
	if id <= 0 {
		return false
	}
	if len(s.GroupIDs) == 0 {
		return true
	}
	for _, x := range s.GroupIDs {
		if x == id {
			return true
		}
	}
	return false
}

type RawUsage = upstreamcacheusage.Evidence
type Request struct {
	ID                   string    `json:"id"`
	LogicalSessionDigest string    `json:"logical_session_digest"`
	APIKeyID             int64     `json:"api_key_id"`
	GroupID              int64     `json:"group_id"`
	AccountID            int64     `json:"account_id"`
	UpstreamKeyID        int64     `json:"upstream_key_id"`
	Model                string    `json:"model"`
	Protocol             string    `json:"protocol"`
	StartedAt            time.Time `json:"started_at"`
}
type Admission struct {
	PolicySignature  string    `json:"policy_signature,omitempty"`
	RequestID        string    `json:"request_id"`
	SessionKey       string    `json:"session_key"`
	GroupID          int64     `json:"group_id"`
	AccountID        int64     `json:"account_id"`
	UpstreamKeyID    int64     `json:"upstream_key_id,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	NaturalNew       bool      `json:"natural_new"`
	WarmAtStart      bool      `json:"warm_at_start"`
	ProtectedAtStart bool      `json:"protected_at_start"`
	Trial            bool      `json:"trial"`
	Sample           bool      `json:"sample"`
	CohortID         string    `json:"cohort_id,omitempty"`
	Mode             Mode      `json:"mode"`
	Completed        bool      `json:"completed"`
	RouteKey         string    `json:"route_key"`
}
type TrialPlan struct {
	RequestID    string `json:"request_id"`
	SessionKey   string `json:"session_key"`
	GroupID      int64  `json:"group_id"`
	NaturalNew   bool   `json:"natural_new"`
	TrialSlot    bool   `json:"trial_slot"`
	TrialOrdinal int64  `json:"trial_ordinal"`
}
type Evidence struct {
	ID               string    `json:"id"`
	Request          Request   `json:"request"`
	Admission        Admission `json:"admission"`
	RawUsage         RawUsage  `json:"raw_usage"`
	CompletedAt      time.Time `json:"completed_at"`
	Success          bool      `json:"success"`
	ProtectionPaused bool      `json:"protection_paused,omitempty"`
}

func (e Evidence) ValidSample() bool {
	return e.Success && e.Admission.Sample && e.Admission.WarmAtStart && e.RawUsage.Known()
}

type Phase string

const (
	Healthy  Phase = "healthy"
	Degraded Phase = "degraded"
	Cooling  Phase = "cooling"
	Trial    Phase = "trial"
	Unknown  Phase = "unknown"
)

type AccountState struct {
	// Policies share immutable observations, but own recovery/reset boundaries.
	Policies               map[string]PolicyProjection `json:"policies,omitempty"`
	PolicySignature        string                      `json:"policy_signature,omitempty"`
	AccountID              int64                       `json:"account_id"`
	UpstreamKeyID          int64                       `json:"upstream_key_id"`
	Model                  string                      `json:"model"`
	Protocol               string                      `json:"protocol"`
	Phase                  Phase                       `json:"phase"`
	ValidRequests          int                         `json:"valid_requests"`
	CachePercent           float64                     `json:"cache_percent"`
	TotalInputTokens       int64                       `json:"total_input_tokens"`
	CacheReadTokens        int64                       `json:"cache_read_tokens"`
	UpdatedAt              time.Time                   `json:"updated_at"`
	LastEvidenceAt         time.Time                   `json:"last_evidence_at"`
	QualitySince           time.Time                   `json:"quality_since,omitempty"`
	DegradedAt             time.Time                   `json:"degraded_at,omitempty"`
	LastDegradedEvidenceAt *time.Time                  `json:"last_degraded_evidence_at,omitempty"`
	CooldownUntil          time.Time                   `json:"cooldown_until,omitempty"`
	CohortID               string                      `json:"cohort_id,omitempty"`
	FirstTrialAt           time.Time                   `json:"first_trial_at,omitempty"`
	TrialValidRequests     int                         `json:"trial_valid_requests"`
	TrialSessions          int                         `json:"trial_sessions"`
	TrialCachePercent      float64                     `json:"trial_cache_percent"`
	GroupIDs               []int64                     `json:"group_ids,omitempty"`
	TrialGroupIDs          []int64                     `json:"trial_group_ids,omitempty"`
	LegacyQualitySince     map[int64]time.Time         `json:"legacy_quality_since,omitempty"`
}

type PolicyProjection struct {
	Settings Settings     `json:"settings"`
	State    AccountState `json:"state"`
}
type SessionRoute struct {
	PolicySignature  string    `json:"policy_signature,omitempty"`
	AccountID        int64     `json:"account_id"`
	UpstreamKeyID    int64     `json:"upstream_key_id"`
	LastSuccessfulAt time.Time `json:"last_successful_at"`
	LastCachePercent float64   `json:"last_cache_percent"`
	LowStreak        int       `json:"low_streak"`
	PendingLow       bool      `json:"pending_low"`
	HighCacheSeen    bool      `json:"high_cache_seen"`
}
type SessionState struct {
	PolicySignature     string                  `json:"policy_signature,omitempty"`
	Key                 string                  `json:"key"`
	FirstSeenAt         time.Time               `json:"first_seen_at"`
	NaturalRequestID    string                  `json:"natural_request_id"`
	NaturalTrialSlot    bool                    `json:"natural_trial_slot"`
	NaturalTrialOrdinal int64                   `json:"natural_trial_ordinal"`
	NaturalAccountID    int64                   `json:"natural_account_id"`
	LastStartedAt       time.Time               `json:"last_started_at"`
	LastCompletedAt     time.Time               `json:"last_completed_at"`
	LastAccountID       int64                   `json:"last_account_id"`
	LastModel           string                  `json:"last_model"`
	LastProtocol        string                  `json:"last_protocol"`
	LastRouteKey        string                  `json:"last_route_key"`
	ResidenceUntil      time.Time               `json:"residence_until"`
	TrialAccountID      int64                   `json:"trial_account_id"`
	TrialModel          string                  `json:"trial_model"`
	TrialProtocol       string                  `json:"trial_protocol"`
	CohortID            string                  `json:"cohort_id,omitempty"`
	Routes              map[string]SessionRoute `json:"routes"`
	// Admissions is retained for legacy stores and migration input only. Split
	// stores keep the immutable per-request records outside the session body.
	Admissions map[string]Admission `json:"admissions,omitempty"`
}
type GroupState struct {
	NaturalNewCount int64 `json:"natural_new_count"`
	TrialCount      int64 `json:"trial_count"`
}
type AccountSnapshot struct {
	AccountState
	Version           int64              `json:"version"`
	LoadedAt          time.Time          `json:"loaded_at"`
	Available         bool               `json:"available"`
	Fresh             bool               `json:"fresh"`
	DerivedProtection *DerivedProtection `json:"derived_protection,omitempty"`
}
type Snapshot struct {
	GroupID         int64             `json:"group_id"`
	Mode            Mode              `json:"mode"`
	Available       bool              `json:"available"`
	Fresh           bool              `json:"fresh"`
	LoadedAt        time.Time         `json:"loaded_at"`
	Accounts        []AccountSnapshot `json:"accounts"`
	NaturalNewCount int64             `json:"natural_new_count"`
	TrialCount      int64             `json:"trial_count"`
	SamplingGap     bool              `json:"sampling_gap"`
}
type SessionRead struct {
	Known              bool      `json:"known"`
	Fresh              bool      `json:"fresh"`
	PreferredAccountID int64     `json:"preferred_account_id"`
	Protected          bool      `json:"protected"`
	PendingLow         bool      `json:"pending_low"`
	WarmAtStart        bool      `json:"warm_at_start"`
	Trial              bool      `json:"trial"`
	CohortID           string    `json:"cohort_id,omitempty"`
	ResidenceUntil     time.Time `json:"residence_until"`
	LastSuccessfulAt   time.Time `json:"last_successful_at"`
}
type Candidate struct {
	AccountID, UpstreamKeyID int64
	Model, Protocol          string
	Preference               int
	OriginalIndex            int
}

const (
	GroupKind      = "group"
	AccountKind    = "account"
	SessionKind    = "session"
	MaxFacts       = 100000
	MaxRuntimeRows = 250000
)

type RuntimeRow struct {
	GroupID   int64
	Kind, Key string
	Version   int64
	Payload   json.RawMessage
	UpdatedAt time.Time
}

// Tx operates under a database transaction. Stores acquire account, session,
// and (only when needed) group quota locks in that order.
type Tx interface {
	Read(kind, key string, out any) (int64, error)
	Write(kind, key string, value any) (int64, error)
	InsertFact(Evidence) (bool, error)
	Facts(accountID int64, since time.Time) ([]Evidence, error)
}
type Store interface {
	Transact(context.Context, int64, func(Tx) error) error
	Enqueue(context.Context, Evidence) error
	ClaimPending(context.Context, string, int, time.Duration) ([]Evidence, error)
	HasPending(context.Context) (bool, error)
	HasPendingBefore(context.Context, time.Time) (bool, error)
	Rows(context.Context, []int64) ([]RuntimeRow, error)
	Cleanup(context.Context, time.Time) error
}

// Account transactions acquire the account identity lock before the optional
// group lock. Group-only trial planning must never upgrade to an account lock.
type AccountStore interface {
	TransactAccount(context.Context, int64, int64, int64, func(Tx) error) error
	AccountRows(context.Context) ([]AccountRuntimeRow, error)
	AccountWindows(context.Context, time.Time, time.Time) (map[string]AccountWindow, error)
}

// AccountWindow is a read-only projection of processed facts for one current
// account/Key identity. Version ties it to the policy reset boundaries used.
type AccountWindow struct {
	NextBoundary time.Time
	// Err isolates a malformed projection to this account/Key. Query-wide
	// failures are returned by AccountWindows itself.
	Err                                    error
	AccountID, UpstreamKeyID, Version      int64
	State                                  AccountState
	ValidRequests                          int
	TotalInputTokens, CacheReadTokens      int64
	TrialValidRequests, TrialSessions      int
	TrialInputTokens, TrialCacheReadTokens int64
	LastEvidenceAt                         time.Time
	GroupIDs, TrialGroupIDs                []int64
	Policies                               map[string]AccountWindow
}

type AccountTx interface {
	Tx
	ReadAccount(int64, int64, any) (int64, error)
	WriteAccount(int64, int64, any) (int64, error)
	AccountFacts(int64, int64, time.Time) ([]Evidence, error)
	LegacyAccounts(int64, int64) ([]LegacyAccountState, error)
}

type LegacyAccountState struct {
	GroupID int64
	AccountState
}

type AccountRuntimeRow struct {
	AccountID, UpstreamKeyID, Version int64
	Payload                           []byte
	UpdatedAt                         time.Time
}

// IdentityVersion is a lightweight, commit-visible revision. FactVersion also
// advances for pending and facts-only writes, independently of state writes.
type IdentityVersion struct {
	AccountID, UpstreamKeyID, Version, FactVersion int64
	Legacy                                         bool
}
type SessionIdentity struct {
	GroupID int64
	Key     string
	Version int64
}
type SessionLoader interface {
	LoadSession(context.Context, int64, string) (*SessionState, int64, error)
}
type IncrementalStore interface {
	SessionLoader
	IdentityVersions(context.Context) ([]IdentityVersion, error)
	ActiveRuntimeRows(context.Context, []SessionIdentity, time.Time) ([]RuntimeRow, error)
	AccountRowsFor(context.Context, []int64) ([]AccountRuntimeRow, error)
	AccountWindowsFor(context.Context, []int64, time.Time, time.Time) (map[string]AccountWindow, error)
}
type BatchCleanupStore interface {
	CleanupBatch(context.Context, time.Time, int) (int64, error)
}
type FactVersionTx interface {
	FactVersion(int64, int64) (int64, error)
}

// AdmissionTx stores per-request classifications independently of the small
// session identity. All operations run under the same session transaction lock
// as Tx.Read/Write, preserving atomic admission and completion semantics.
type AdmissionTx interface {
	ReadAdmission(sessionKey, key string) (Admission, bool, error)
	WriteAdmission(sessionKey, key string, value Admission) error
	PendingAdmissions(sessionKey string, accountID int64, routeKey string) ([]Admission, error)
	RetainedAdmissionCount(sessionKey string, since time.Time) (int, error)
}

// AdmissionFailure contains only opaque identifiers for a transient completion
// failure. A refresh checks just these records for durable acknowledgments.
type AdmissionFailure struct {
	AccountID, UpstreamKeyID int64
	Key                      string
}

// PendingFact identifies an already persisted completion that has not entered
// the account window yet. Unfinished admissions alone do not prove a sampling gap.
type PendingFact struct {
	AccountID, UpstreamKeyID int64
	CompletedAt              time.Time
}

type AdmissionHealth struct {
	PendingFacts []PendingFact
	Acknowledged []AdmissionFailure
}

type AdmissionStore interface {
	// Return unprocessed facts strictly older than cutoff, grouped by their
	// frozen account/Key identity, including facts held by a consumer lease.
	AdmissionHealth(ctx context.Context, cutoff time.Time, failures []AdmissionFailure) (AdmissionHealth, error)
}

var ErrUnavailable = errors.New("cache guard persistence unavailable")

func validateRequest(r Request) error {
	if r.ID == "" || len(r.ID) > 200 || r.GroupID <= 0 || r.APIKeyID <= 0 || r.AccountID <= 0 || r.UpstreamKeyID <= 0 || r.StartedAt.IsZero() || len(r.Model) > 256 || r.Model == "" || len(r.Protocol) > 32 || r.Protocol == "" {
		return errors.New("invalid cache guard request")
	}
	if len(r.LogicalSessionDigest) < 32 || len(r.LogicalSessionDigest) > 128 {
		return errors.New("cache guard requires logical session HMAC")
	}
	for _, c := range r.LogicalSessionDigest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return fmt.Errorf("cache guard session digest is not hex")
		}
	}
	return nil
}
