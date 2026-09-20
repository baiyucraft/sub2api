package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type pluginHostStateCall struct {
	operation, plugin, namespace, key, prefix, after string
	limit                                            int
	mutation                                         PluginStateMutation
	lease                                            PluginLease
	ttl                                              time.Duration
}

type pluginHostStateTestStore struct {
	calls   []pluginHostStateCall
	record  PluginStateRecord
	records []PluginStateRecord
	next    string
	lease   PluginLease
	err     error
}

func (s *pluginHostStateTestStore) StateGet(_ context.Context, p, n, k string) (PluginStateRecord, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "get", plugin: p, namespace: n, key: k})
	return s.record, s.err
}
func (s *pluginHostStateTestStore) StateCAS(_ context.Context, p, n, k string, m PluginStateMutation) (PluginStateRecord, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "cas", plugin: p, namespace: n, key: k, mutation: m})
	return s.record, s.err
}
func (s *pluginHostStateTestStore) StateDelete(_ context.Context, p, n, k string, m PluginStateMutation) (PluginStateRecord, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "delete", plugin: p, namespace: n, key: k, mutation: m})
	return s.record, s.err
}
func (s *pluginHostStateTestStore) StateList(_ context.Context, p, n, prefix, after string, limit int) ([]PluginStateRecord, string, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "list", plugin: p, namespace: n, prefix: prefix, after: after, limit: limit})
	return s.records, s.next, s.err
}
func (s *pluginHostStateTestStore) LeaseAcquire(_ context.Context, p, n, k string, ttl time.Duration) (PluginLease, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "acquire", plugin: p, namespace: n, key: k, ttl: ttl})
	return s.lease, s.err
}
func (s *pluginHostStateTestStore) LeaseRenew(_ context.Context, p, n, k string, l PluginLease, ttl time.Duration) (PluginLease, error) {
	s.calls = append(s.calls, pluginHostStateCall{operation: "renew", plugin: p, namespace: n, key: k, ttl: ttl, lease: l})
	return s.lease, s.err
}
func (s *pluginHostStateTestStore) LeaseRelease(_ context.Context, p, n, k string, l PluginLease) error {
	s.calls = append(s.calls, pluginHostStateCall{operation: "release", plugin: p, namespace: n, key: k, lease: l})
	return s.err
}

func newPluginHostStateTestServer() (*pluginHostServiceServer, *pluginHostStateTestStore) {
	store := &pluginHostStateTestStore{
		record: PluginStateRecord{Key: "accounts/42/generation", Value: []byte{0, 0xff, 'v'}, Version: 4, Found: true},
		lease:  PluginLease{Owner: "host-secret-token", Fence: 7, ExpiresAt: time.Unix(2000000000, 500000000)},
	}
	// The PostgreSQL service remains usable without a Redis KV store.
	return &pluginHostServiceServer{pluginKey: "codex-state", stateStore: store}, store
}

func TestPluginHostStateGetAndTombstone(t *testing.T) {
	for _, found := range []bool{true, false} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			server, store := newPluginHostStateTestServer()
			store.record.Found = found
			response, err := server.StateGet(context.Background(), &pluginv1.StateGetRequest{Namespace: "codex-state-v1", Key: "accounts/42/generation"})
			require.NoError(t, err)
			require.Equal(t, found, response.Found)
			require.EqualValues(t, 4, response.Version, "a tombstone keeps its CAS revision")
			if found {
				require.Equal(t, store.record.Value, response.Value)
			} else {
				require.Empty(t, response.Value)
			}
			require.Equal(t, []pluginHostStateCall{{operation: "get", plugin: "codex-state", namespace: "codex-state-v1", key: "accounts/42/generation"}}, store.calls)
		})
	}
}

func TestPluginHostStateCASRouting(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		for _, fenced := range []bool{false, true} {
			t.Run(fmt.Sprintf("delete=%t/fenced=%t", deleting, fenced), func(t *testing.T) {
				server, store := newPluginHostStateTestServer()
				req := &pluginv1.StateCompareAndSwapRequest{Namespace: "n", Key: "slots/1", ExpectedVersion: 3, Value: []byte("value"), Delete: deleting}
				if fenced {
					req.LeaseOwner, req.LeaseFence = "returned-token", 2
				}
				response, err := server.StateCompareAndSwap(context.Background(), req)
				require.NoError(t, err)
				require.EqualValues(t, 4, response.Version)
				require.Len(t, store.calls, 1)
				call := store.calls[0]
				require.Equal(t, map[bool]string{true: "delete", false: "cas"}[deleting], call.operation)
				require.Equal(t, "codex-state", call.plugin)
				require.Equal(t, "n", call.namespace)
				require.Equal(t, "slots/1", call.key)
				require.Equal(t, req.Value, call.mutation.Value)
				require.EqualValues(t, 3, call.mutation.ExpectedVersion)
				if fenced {
					require.Equal(t, &PluginLease{Owner: "returned-token", Fence: 2}, call.mutation.Lease)
				} else {
					require.Nil(t, call.mutation.Lease)
				}
			})
		}
	}
}

func TestPluginHostStateBindsPluginIdentity(t *testing.T) {
	first, store := newPluginHostStateTestServer()
	second := &pluginHostServiceServer{pluginKey: "another-plugin", stateStore: store}
	for _, server := range []*pluginHostServiceServer{first, second} {
		_, err := server.StateGet(context.Background(), &pluginv1.StateGetRequest{Namespace: "same", Key: "same"})
		require.NoError(t, err)
	}
	require.Equal(t, "codex-state", store.calls[0].plugin)
	require.Equal(t, "another-plugin", store.calls[1].plugin)
}

func TestPluginHostStateListPagination(t *testing.T) {
	for _, limit := range []int32{-1, 0, 1, 200, math.MaxInt32} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			server, store := newPluginHostStateTestServer()
			store.records = []PluginStateRecord{{Key: "slots/%_2", Found: true}}
			store.next = "slots/%_2"
			response, err := server.StateList(context.Background(), &pluginv1.StateListRequest{Namespace: "n", KeyPrefix: "slots/%_", AfterKey: "slots/%_1", Limit: limit})
			require.NoError(t, err)
			require.Equal(t, []string{"slots/%_2"}, response.Keys)
			require.Equal(t, "slots/%_2", response.NextKey)
			want := int(limit)
			if want <= 0 {
				want = PluginStateDefaultListLimit
			}
			require.Equal(t, min(want, PluginStateMaxListLimit), store.calls[0].limit)
			require.Equal(t, "slots/%_", store.calls[0].prefix)
			require.Equal(t, "slots/%_1", store.calls[0].after)
		})
	}
}

func TestPluginHostStateLeaseRPC(t *testing.T) {
	server, store := newPluginHostStateTestServer()
	ctx := context.Background()
	response, err := server.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "slots/1", Owner: "plugin-instance:hint", TtlSeconds: 30})
	require.NoError(t, err)
	require.True(t, response.Acquired)
	require.Equal(t, store.lease.Owner, response.Owner, "the caller hint is not the fencing credential")
	require.EqualValues(t, store.lease.Fence, response.Fence)
	require.Equal(t, store.lease.ExpiresAt.Unix(), response.ExpiresAt)
	require.Equal(t, 30*time.Second, store.calls[0].ttl)
	_, err = server.RenewLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "slots/1", Owner: response.Owner, Fence: response.Fence, TtlSeconds: 60})
	require.NoError(t, err)
	require.Equal(t, PluginLease{Owner: response.Owner, Fence: int64(response.Fence)}, store.calls[1].lease)
	require.Equal(t, time.Minute, store.calls[1].ttl)
	released, err := server.ReleaseLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "slots/1", Owner: response.Owner, Fence: response.Fence})
	require.NoError(t, err)
	require.False(t, released.Acquired)
	require.Equal(t, store.calls[1].lease, store.calls[2].lease)
	for _, call := range store.calls {
		require.Equal(t, "codex-state", call.plugin)
		require.Equal(t, "n", call.namespace)
		require.Equal(t, "slots/1", call.key)
	}
}

func TestPluginHostStateRejectsInvalidCAS(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pluginv1.StateCompareAndSwapRequest)
	}{
		{"empty_namespace", func(r *pluginv1.StateCompareAndSwapRequest) { r.Namespace = "" }},
		{"namespace_path", func(r *pluginv1.StateCompareAndSwapRequest) { r.Namespace = "a/b" }},
		{"namespace_limit", func(r *pluginv1.StateCompareAndSwapRequest) { r.Namespace = strings.Repeat("n", 129) }},
		{"empty_key", func(r *pluginv1.StateCompareAndSwapRequest) { r.Key = "" }},
		{"key_limit", func(r *pluginv1.StateCompareAndSwapRequest) { r.Key = strings.Repeat("k", 513) }},
		{"nul_key", func(r *pluginv1.StateCompareAndSwapRequest) { r.Key = "a\x00b" }},
		{"invalid_utf8", func(r *pluginv1.StateCompareAndSwapRequest) { r.Key = string([]byte{0xff}) }},
		{"value_limit", func(r *pluginv1.StateCompareAndSwapRequest) { r.Value = make([]byte, pluginStateMaxValueBytes+1) }},
		{"version_overflow", func(r *pluginv1.StateCompareAndSwapRequest) { r.ExpectedVersion = math.MaxUint64 }},
		{"delete_zero", func(r *pluginv1.StateCompareAndSwapRequest) { r.Delete = true; r.ExpectedVersion = 0 }},
		{"negative_ttl", func(r *pluginv1.StateCompareAndSwapRequest) { r.TtlSeconds = -1 }},
		{"positive_ttl", func(r *pluginv1.StateCompareAndSwapRequest) { r.TtlSeconds = 1 }},
		{"ttl_overflow", func(r *pluginv1.StateCompareAndSwapRequest) { r.TtlSeconds = math.MaxInt64 }},
		{"owner_without_fence", func(r *pluginv1.StateCompareAndSwapRequest) { r.LeaseOwner = "owner" }},
		{"fence_without_owner", func(r *pluginv1.StateCompareAndSwapRequest) { r.LeaseFence = 1 }},
		{"fence_overflow", func(r *pluginv1.StateCompareAndSwapRequest) { r.LeaseOwner = "owner"; r.LeaseFence = math.MaxUint64 }},
		{"owner_limit", func(r *pluginv1.StateCompareAndSwapRequest) {
			r.LeaseOwner = strings.Repeat("o", 257)
			r.LeaseFence = 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, store := newPluginHostStateTestServer()
			req := &pluginv1.StateCompareAndSwapRequest{Namespace: "n", Key: "slots/1", ExpectedVersion: 3}
			tc.mutate(req)
			_, err := server.StateCompareAndSwap(context.Background(), req)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Empty(t, store.calls, "validation must happen before storage")
		})
	}
}

func TestPluginHostStateLimits(t *testing.T) {
	server, store := newPluginHostStateTestServer()
	ctx := context.Background()
	_, err := server.StateCompareAndSwap(ctx, &pluginv1.StateCompareAndSwapRequest{
		Namespace: strings.Repeat("n", 128), Key: strings.Repeat("k", 512), Value: make([]byte, pluginStateMaxValueBytes),
	})
	require.NoError(t, err)
	for _, seconds := range []int64{1, 300} {
		_, err := server.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", TtlSeconds: seconds})
		require.NoError(t, err)
	}
	for _, seconds := range []int64{-1, 0, 301, math.MaxInt64} {
		store.calls = nil
		_, err := server.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", TtlSeconds: seconds})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		_, err = server.RenewLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "token", Fence: 1, TtlSeconds: seconds})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Empty(t, store.calls)
	}
	_, err = server.ReleaseLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "token", Fence: 1, TtlSeconds: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.RenewLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "token", Fence: math.MaxUint64, TtlSeconds: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.ReleaseLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "token", Fence: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Fence: 1, TtlSeconds: 1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	for _, req := range []*pluginv1.StateListRequest{
		{Namespace: ""},
		{Namespace: "n", KeyPrefix: strings.Repeat("k", 513)},
		{Namespace: "n", AfterKey: "bad\x00cursor"},
	} {
		_, err = server.StateList(ctx, req)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	require.Empty(t, store.calls)
}

func pluginHostStateTestCalls(server *pluginHostServiceServer, nilRequests bool) []func() error {
	ctx := context.Background()
	get := &pluginv1.StateGetRequest{Namespace: "n", Key: "k"}
	cas := &pluginv1.StateCompareAndSwapRequest{Namespace: "n", Key: "k", ExpectedVersion: 3}
	list := &pluginv1.StateListRequest{Namespace: "n"}
	acquire := &pluginv1.LeaseRequest{Namespace: "n", Key: "k", TtlSeconds: 1}
	renew := &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "owner", Fence: 1, TtlSeconds: 1}
	release := &pluginv1.LeaseRequest{Namespace: "n", Key: "k", Owner: "owner", Fence: 1}
	if nilRequests {
		get, cas, list, acquire, renew, release = nil, nil, nil, nil, nil, nil
	}
	return []func() error{
		func() error { _, err := server.StateGet(ctx, get); return err },
		func() error { _, err := server.StateCompareAndSwap(ctx, cas); return err },
		func() error { _, err := server.StateList(ctx, list); return err },
		func() error { _, err := server.AcquireLease(ctx, acquire); return err },
		func() error { _, err := server.RenewLease(ctx, renew); return err },
		func() error { _, err := server.ReleaseLease(ctx, release); return err },
	}
}

func TestPluginHostStateReadinessAndNilRequests(t *testing.T) {
	for _, server := range []*pluginHostServiceServer{
		nil, {}, {pluginKey: "valid"}, {pluginKey: "invalid/key", stateStore: &pluginHostStateTestStore{}},
	} {
		for _, call := range pluginHostStateTestCalls(server, false) {
			require.Equal(t, codes.Unavailable, status.Code(call()))
		}
	}
	server, store := newPluginHostStateTestServer()
	for _, call := range pluginHostStateTestCalls(server, true) {
		require.Equal(t, codes.InvalidArgument, status.Code(call()))
	}
	require.Empty(t, store.calls)
}

func TestPluginHostStateErrorsAreSanitizedAndDistinct(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code codes.Code
	}{
		{ErrPluginStateConflict, codes.Aborted},
		{ErrPluginLeaseLost, codes.FailedPrecondition},
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{errors.New("database connection password=ciphertext"), codes.Internal},
		{status.Error(codes.PermissionDenied, "private upstream details"), codes.Internal},
	} {
		t.Run(tc.code.String()+"/"+fmt.Sprintf("%T", tc.err), func(t *testing.T) {
			server, store := newPluginHostStateTestServer()
			store.err = fmt.Errorf("sensitive-owner-token: %w", tc.err)
			for _, call := range pluginHostStateTestCalls(server, false) {
				err := call()
				require.Equal(t, tc.code, status.Code(err))
				require.NotContains(t, err.Error(), "sensitive-owner-token")
				require.NotContains(t, err.Error(), "ciphertext")
				require.NotContains(t, err.Error(), "private upstream")
			}
		})
	}
}

func TestPluginHostStateRejectsInvalidStoreResponses(t *testing.T) {
	ctx := context.Background()
	server, store := newPluginHostStateTestServer()
	for _, record := range []PluginStateRecord{
		{Version: -1},
		{Found: true, Version: 0},
	} {
		store.record = record
		_, err := server.StateGet(ctx, &pluginv1.StateGetRequest{Namespace: "n", Key: "k"})
		require.Equal(t, codes.Internal, status.Code(err))
	}
	store.record = PluginStateRecord{Found: true, Version: 1, Value: make([]byte, pluginStateMaxValueBytes+1)}
	_, err := server.StateGet(ctx, &pluginv1.StateGetRequest{Namespace: "n", Key: "k"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	_, err = server.StateCompareAndSwap(ctx, &pluginv1.StateCompareAndSwapRequest{Namespace: "n", Key: "k", ExpectedVersion: 1})
	require.Equal(t, codes.Internal, status.Code(err))
	store.records = []PluginStateRecord{{Key: "a"}, {Key: "b"}}
	_, err = server.StateList(ctx, &pluginv1.StateListRequest{Namespace: "n", Limit: 1})
	require.Equal(t, codes.Internal, status.Code(err))
	for _, lease := range []PluginLease{
		{Owner: "", Fence: 1, ExpiresAt: time.Now()},
		{Owner: "owner", Fence: -1, ExpiresAt: time.Now()},
		{Owner: "owner", Fence: 1},
	} {
		store.lease = lease
		_, err = server.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "k", TtlSeconds: 1})
		require.Equal(t, codes.Internal, status.Code(err))
	}
}

func TestPluginHostStateGRPCDispatch(t *testing.T) {
	host, store := newPluginHostStateTestServer()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	pluginv1.RegisterHostServiceServer(server, host)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection, err := grpc.NewClient("passthrough:///plugin-state", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	client := pluginv1.NewHostServiceClient(connection)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	get, err := client.StateGet(ctx, &pluginv1.StateGetRequest{Namespace: "n", Key: "accounts/42/generation"})
	require.NoError(t, err)
	require.Equal(t, uint64(4), get.Version)
	require.Equal(t, store.record.Value, get.Value)
	lease, err := client.AcquireLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "accounts/42/generation", TtlSeconds: 30})
	require.NoError(t, err)
	require.Equal(t, store.lease.Owner, lease.Owner)
	_, err = client.StateCompareAndSwap(ctx, &pluginv1.StateCompareAndSwapRequest{Namespace: "n", Key: "accounts/42/generation", ExpectedVersion: 3, LeaseOwner: lease.Owner, LeaseFence: lease.Fence})
	require.NoError(t, err)
	_, err = client.StateList(ctx, &pluginv1.StateListRequest{Namespace: "n"})
	require.NoError(t, err)
	_, err = client.RenewLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "accounts/42/generation", Owner: lease.Owner, Fence: lease.Fence, TtlSeconds: 60})
	require.NoError(t, err)
	_, err = client.ReleaseLease(ctx, &pluginv1.LeaseRequest{Namespace: "n", Key: "accounts/42/generation", Owner: lease.Owner, Fence: lease.Fence})
	require.NoError(t, err)
}
