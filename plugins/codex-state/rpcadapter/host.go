package rpcadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const namespace = "codex-state-v1"

func (h *Host) AvailableAccounts(ctx context.Context) (map[int64]bool, error) {
	client, err := h.connection(nil)
	if err != nil {
		return nil, err
	}
	response, err := client.ListResources(ctx, &pluginv1.ListResourcesRequest{})
	if err != nil {
		return nil, mapError(err)
	}
	if response == nil {
		return nil, core.ErrUnavailable
	}
	var resources struct {
		Accounts []struct {
			ID          int64  `json:"id"`
			Platform    string `json:"platform"`
			AccountType string `json:"account_type"`
		} `json:"accounts"`
	}
	if json.Unmarshal(response.ResourcesJson, &resources) != nil {
		return nil, core.ErrUnavailable
	}
	available := map[int64]bool{}
	for _, account := range resources.Accounts {
		if account.ID > 0 && account.Platform == "openai" && (account.AccountType == "oauth" || account.AccountType == "setup-token") {
			available[account.ID] = true
		}
	}
	return available, nil
}

type Host struct {
	mu       sync.RWMutex
	client   pluginv1.HostServiceClient
	revision string
	active   bool
}

func (h *Host) set(client pluginv1.HostServiceClient) { h.mu.Lock(); h.client = client; h.mu.Unlock() }
func (h *Host) configure(revision string, active bool) {
	h.mu.Lock()
	h.revision = revision
	h.active = active
	h.mu.Unlock()
}
func (h *Host) connection(g *core.Guard) (pluginv1.HostServiceClient, error) {
	return h.connectionFor(context.Background(), g)
}

func (h *Host) connectionFor(ctx context.Context, g *core.Guard) (pluginv1.HostServiceClient, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.client == nil {
		return nil, core.ErrUnavailable
	}
	if g != nil && ((!h.active && !core.IsCompletionContext(ctx)) || h.revision != g.ConfigRevision) {
		return nil, core.ErrStale
	}
	return h.client, nil
}

// Revision-qualified keys make a late write invisible to every newer identity
// or config generation, even though SDK v1 CAS has no Guard protobuf fields.
// Action journals intentionally retain their global action_id namespace.
func remoteKey(key string, g core.Guard) string {
	if strings.HasPrefix(key, "actions/") {
		return key
	}
	digest := sha256.Sum256([]byte(g.ConfigRevision + "\x00" + g.IdentityRevision))
	return key + "/" + hex.EncodeToString(digest[:])
}

func (h *Host) Identity(ctx context.Context, id int64) (core.Identity, error) {
	client, err := h.connection(nil)
	if err != nil {
		return core.Identity{}, err
	}
	r, err := client.ResolveOutboundIdentity(ctx, &pluginv1.ResolveOutboundIdentityRequest{AccountId: id})
	if err != nil {
		return core.Identity{}, mapError(err)
	}
	if r == nil || !r.Found || r.AccountId != id || r.IdentityRevision == "" || r.Platform != "openai" || (r.AccountType != "oauth" && r.AccountType != "setup-token") {
		return core.Identity{}, core.ErrUnavailable
	}
	headers := decodeHeaders(r.Headers)
	if headers.Get("Authorization") == "" && r.Token != "" {
		headers.Set("Authorization", "Bearer "+r.Token)
	}
	identity := core.Identity{AccountID: id, Revision: r.IdentityRevision, Eligible: true, Endpoint: core.DefaultEndpoint, Headers: headers}
	for _, egress := range r.Egresses {
		if egress != nil {
			identity.Egresses = append(identity.Egresses, core.Egress{ProxyURL: egress.ProxyUrl})
		}
	}
	return identity, nil
}

func (h *Host) StateGet(ctx context.Context, key string, g core.Guard) (core.StoredState, error) {
	client, err := h.connectionFor(ctx, &g)
	if err != nil {
		return core.StoredState{}, err
	}
	r, err := client.StateGet(ctx, &pluginv1.StateGetRequest{Namespace: namespace, Key: remoteKey(key, g)})
	if err != nil {
		return core.StoredState{}, mapError(err)
	}
	if r == nil {
		return core.StoredState{}, core.ErrUnavailable
	}
	data := r.Value
	if !r.Found {
		data = nil
	}
	return core.StoredState{Version: r.Version, Data: data}, nil
}

func (h *Host) StateCAS(ctx context.Context, m core.Mutation) (uint64, error) {
	client, err := h.connectionFor(ctx, &m.Guard)
	if err != nil {
		return 0, err
	}
	if m.Lease.Key != m.Key || m.Lease.Guard != m.Guard {
		return 0, core.ErrStale
	}
	identity, err := h.Identity(ctx, m.Guard.AccountID)
	if err != nil || identity.Revision != m.Guard.IdentityRevision {
		return 0, core.ErrStale
	}
	if _, err = h.connectionFor(ctx, &m.Guard); err != nil {
		return 0, err
	}
	r, err := client.StateCompareAndSwap(ctx, &pluginv1.StateCompareAndSwapRequest{Namespace: namespace, Key: remoteKey(m.Key, m.Guard), ExpectedVersion: m.ExpectedVersion, Value: m.Data, Delete: m.Data == nil, LeaseOwner: m.Lease.Owner, LeaseFence: m.Lease.Fence})
	if err != nil {
		return 0, mapError(err)
	}
	if r == nil {
		return 0, core.ErrUnavailable
	}
	return r.Version, nil
}

func ttl(duration time.Duration) int64 {
	seconds := int64((duration + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}
func leaseResult(key string, g core.Guard, r *pluginv1.LeaseResponse) (core.Lease, error) {
	if r == nil || !r.Acquired {
		return core.Lease{}, core.ErrLeaseBusy
	}
	if r.Owner == "" || r.Fence == 0 {
		return core.Lease{}, core.ErrStale
	}
	return core.Lease{Key: key, Owner: r.Owner, Fence: r.Fence, ExpiresAt: time.Unix(r.ExpiresAt, 0), Guard: g}, nil
}

func (h *Host) LeaseAcquire(ctx context.Context, r core.LeaseRequest) (core.Lease, error) {
	client, err := h.connectionFor(ctx, &r.Guard)
	if err != nil {
		return core.Lease{}, err
	}
	response, err := client.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: namespace, Key: remoteKey(r.Key, r.Guard), Owner: r.Owner, TtlSeconds: ttl(r.TTL)})
	if err != nil {
		if status.Code(err) == codes.Aborted || status.Code(err) == codes.AlreadyExists {
			return core.Lease{}, core.ErrLeaseBusy
		}
		return core.Lease{}, mapError(err)
	}
	return leaseResult(r.Key, r.Guard, response)
}
func (h *Host) LeaseRenew(ctx context.Context, l core.Lease, duration time.Duration) (core.Lease, error) {
	client, err := h.connectionFor(ctx, &l.Guard)
	if err != nil {
		return core.Lease{}, err
	}
	r, err := client.RenewLease(ctx, &pluginv1.LeaseRequest{Namespace: namespace, Key: remoteKey(l.Key, l.Guard), Owner: l.Owner, Fence: l.Fence, TtlSeconds: ttl(duration)})
	if err != nil {
		return core.Lease{}, mapError(err)
	}
	return leaseResult(l.Key, l.Guard, r)
}
func (h *Host) LeaseRelease(ctx context.Context, l core.Lease) error {
	client, err := h.connection(nil)
	if err != nil {
		return err
	}
	_, err = client.ReleaseLease(ctx, &pluginv1.LeaseRequest{Namespace: namespace, Key: remoteKey(l.Key, l.Guard), Owner: l.Owner, Fence: l.Fence})
	return mapError(err)
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Aborted, codes.AlreadyExists:
		return core.ErrConflict
	case codes.FailedPrecondition, codes.PermissionDenied:
		return core.ErrStale
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	}
	return core.ErrUnavailable
}
func decodeHeaders(input map[string]*pluginv1.HeaderValues) http.Header {
	out := make(http.Header, len(input))
	for key, value := range input {
		if value != nil {
			out[http.CanonicalHeaderKey(key)] = append([]string(nil), value.Values...)
		}
	}
	return out
}
func encodeHeaders(input http.Header) map[string]*pluginv1.HeaderValues {
	out := make(map[string]*pluginv1.HeaderValues, len(input))
	for key, values := range input {
		out[key] = &pluginv1.HeaderValues{Values: append([]string(nil), values...)}
	}
	return out
}
