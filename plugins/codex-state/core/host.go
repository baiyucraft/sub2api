package core

import (
	"context"
	"errors"
	"net/http"
	"time"
)

var (
	ErrUnavailable    = errors.New("state unavailable")
	ErrConflict       = errors.New("state version conflict")
	ErrLeaseBusy      = errors.New("lease busy")
	ErrStale          = errors.New("stale identity, configuration or fence")
	ErrDisabled       = errors.New("state runtime disabled")
	ErrActionConflict = errors.New("action_id already used for a different action")
)

type Identity struct {
	AccountID int64
	Revision  string
	Eligible  bool
	Endpoint  string
	Headers   http.Header
	Egresses  []Egress
}

type Egress struct{ ProxyURL string }

// Guard is host-issued scope metadata. Revisions never come from configuration
// JSON. Identity revision excludes proxy URLs and proxy group membership.
type Guard struct {
	AccountID        int64  `json:"account_id"`
	Model            string `json:"model"`
	ConfigRevision   string `json:"config_revision"`
	IdentityRevision string `json:"identity_revision"`
}

type StoredState struct {
	Version uint64
	Data    []byte
}
type Lease struct {
	Key       string
	Owner     string
	Fence     uint64
	ExpiresAt time.Time
	Guard     Guard
}
type LeaseRequest struct {
	Key   string
	Owner string
	TTL   time.Duration
	Guard Guard
}
type Mutation struct {
	Key             string
	ExpectedVersion uint64
	Data            []byte
	Guard           Guard
	Lease           Lease
}

// Host must implement linearizable reads and CAS. A never-used state is version
// zero; a tombstone retains its version. Guard-qualified state keys isolate old
// configuration/identity generations. Writes must reject inactive runtime or a
// changed identity. StateCAS atomically checks version and the unexpired lease
// owner/fence for the exact key, including scope authorization.
// Fences strictly increase on reacquisition; expired leases cannot be renewed.
// Reads or writes must never silently fall back to local cached state on errors.
// The RPC implementation owns persistence and credentials; core never opens a DB.
type Host interface {
	Identity(context.Context, int64) (Identity, error)
	StateGet(context.Context, string, Guard) (StoredState, error)
	StateCAS(context.Context, Mutation) (uint64, error)
	LeaseAcquire(context.Context, LeaseRequest) (Lease, error)
	LeaseRenew(context.Context, Lease, time.Duration) (Lease, error)
	LeaseRelease(context.Context, Lease) error
}

type DirectoryAccount struct {
	AccountID                int64
	Present                  bool
	Name                     string
	AccountType              string
	GroupIDs                 []int64
	BusinessEgressConfigured bool
}

// StatusDirectory is a credential-free live resource projection. Health uses
// this optional read to combine account metadata with ticket status without
// exposing account extras, credentials or proxy URLs.
type StatusDirectory interface {
	DirectoryAccounts(context.Context) ([]DirectoryAccount, error)
}

type ProbeRequest struct {
	Identity     Identity
	Model        string
	State        string
	ProxyURL     string
	DialProxyURL string
}

// Completed is true only after terminal evidence, never merely HTTP 200.
type ProbeResult struct {
	State      string
	StatusCode int
	Completed  bool
	Model      string
}
type Prober interface {
	Probe(context.Context, ProbeRequest) (ProbeResult, error)
}

type Ticket struct {
	State       string    `json:"state"`
	Fingerprint string    `json:"fingerprint"`
	Version     uint64    `json:"version"`
	CapturedAt  time.Time `json:"captured_at"`
	IssuedAt    time.Time `json:"issued_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Slot struct {
	Guard            Guard           `json:"guard"`
	Active           *Ticket         `json:"active,omitempty"`
	Ready            *Ticket         `json:"ready,omitempty"`
	NextVersion      uint64          `json:"next_version"`
	Strikes          int             `json:"strikes"`
	Attempts         int             `json:"attempts"`
	CooldownUntil    time.Time       `json:"cooldown_until"`
	LastError        string          `json:"last_error"`
	Harvesting       bool            `json:"harvesting"`
	JobEpoch         uint64          `json:"job_epoch"`
	HarvestUntil     time.Time       `json:"harvest_until"`
	Paused           bool            `json:"paused"`
	HarvestRequested bool            `json:"harvest_requested"`
	Actions          map[string]bool `json:"actions,omitempty"`
}

type Receipt struct {
	Guard         Guard
	TicketVersion uint64
	Fingerprint   string
}

type completionContextKey struct{}

// IsCompletionContext identifies bounded cleanup for an already-issued receipt.
// The RPC adapter may allow it while runtime_active=false drains old requests.
func IsCompletionContext(ctx context.Context) bool {
	value, _ := ctx.Value(completionContextKey{}).(bool)
	return value
}
