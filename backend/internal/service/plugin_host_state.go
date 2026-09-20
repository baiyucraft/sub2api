package service

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	pluginStateMaxNamespaceBytes = 128
	pluginStateMaxKeyBytes       = 512
	pluginStateMaxValueBytes     = 256 * 1024
	pluginStateMaxOwnerBytes     = 256
	pluginStateMaxLeaseSeconds   = 300
)

func (s *pluginHostServiceServer) stateReady() bool {
	return s != nil && s.stateStore != nil && isValidPluginKVSegment(s.pluginKey, pluginKVMaxPluginKeyLen)
}

func (s *pluginHostServiceServer) StateGet(ctx context.Context, req *pluginv1.StateGetRequest) (*pluginv1.StateGetResponse, error) {
	if !s.stateReady() {
		return nil, status.Error(codes.Unavailable, "plugin state store unavailable")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validatePluginStateRPCSlot(req.Namespace, req.Key); err != nil {
		return nil, err
	}
	record, err := s.stateStore.StateGet(ctx, s.pluginKey, req.Namespace, req.Key)
	if err != nil {
		return nil, pluginStateRPCError(err)
	}
	if record.Version < 0 || record.Found && record.Version == 0 {
		return nil, status.Error(codes.Internal, "invalid plugin state result")
	}
	response := &pluginv1.StateGetResponse{Found: record.Found, Version: uint64(record.Version)}
	if record.Found {
		if len(record.Value) > pluginStateMaxValueBytes {
			return nil, status.Error(codes.ResourceExhausted, "plugin state value exceeds limit")
		}
		response.Value = record.Value
	}
	return response, nil
}

func (s *pluginHostServiceServer) StateCompareAndSwap(ctx context.Context, req *pluginv1.StateCompareAndSwapRequest) (*pluginv1.StateCompareAndSwapResponse, error) {
	if !s.stateReady() {
		return nil, status.Error(codes.Unavailable, "plugin state store unavailable")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validatePluginStateRPCSlot(req.Namespace, req.Key); err != nil {
		return nil, err
	}
	if req.ExpectedVersion > math.MaxInt64 || req.Delete && req.ExpectedVersion == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid expected_version")
	}
	if len(req.Value) > pluginStateMaxValueBytes {
		return nil, status.Error(codes.InvalidArgument, "plugin state value exceeds limit")
	}
	if req.TtlSeconds != 0 {
		return nil, status.Error(codes.InvalidArgument, "state ttl_seconds must be zero; state expiry is not supported")
	}
	lease, err := pluginStateRPCLease(req.LeaseOwner, req.LeaseFence, true)
	if err != nil {
		return nil, err
	}
	mutation := PluginStateMutation{Value: req.Value, ExpectedVersion: int64(req.ExpectedVersion), Lease: lease}
	var record PluginStateRecord
	if req.Delete {
		record, err = s.stateStore.StateDelete(ctx, s.pluginKey, req.Namespace, req.Key, mutation)
	} else {
		record, err = s.stateStore.StateCAS(ctx, s.pluginKey, req.Namespace, req.Key, mutation)
	}
	if err != nil {
		return nil, pluginStateRPCError(err)
	}
	if record.Version <= mutation.ExpectedVersion {
		return nil, status.Error(codes.Internal, "invalid plugin state revision")
	}
	return &pluginv1.StateCompareAndSwapResponse{Version: uint64(record.Version)}, nil
}

func (s *pluginHostServiceServer) StateList(ctx context.Context, req *pluginv1.StateListRequest) (*pluginv1.StateListResponse, error) {
	if !s.stateReady() {
		return nil, status.Error(codes.Unavailable, "plugin state store unavailable")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validatePluginStateRPCNamespace(req.Namespace); err != nil {
		return nil, err
	}
	if err := validatePluginStateRPCText(req.KeyPrefix, pluginStateMaxKeyBytes, true); err != nil {
		return nil, err
	}
	if err := validatePluginStateRPCText(req.AfterKey, pluginStateMaxKeyBytes, true); err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	if limit <= 0 {
		limit = PluginStateDefaultListLimit
	}
	if limit > PluginStateMaxListLimit {
		limit = PluginStateMaxListLimit
	}
	records, next, err := s.stateStore.StateList(ctx, s.pluginKey, req.Namespace, req.KeyPrefix, req.AfterKey, limit)
	if err != nil {
		return nil, pluginStateRPCError(err)
	}
	if len(records) > limit {
		return nil, status.Error(codes.Internal, "invalid plugin state page")
	}
	keys := make([]string, 0, len(records))
	for _, record := range records {
		keys = append(keys, record.Key)
	}
	return &pluginv1.StateListResponse{Keys: keys, NextKey: next}, nil
}

func (s *pluginHostServiceServer) AcquireLease(ctx context.Context, req *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	if err := s.validatePluginLeaseRPC(req); err != nil {
		return nil, err
	}
	if req.Fence != 0 {
		return nil, status.Error(codes.InvalidArgument, "acquire fence must be zero")
	}
	// The optional owner is only a caller hint. The store issues a fresh secret
	// owner token; callers must use the returned owner and fence for later calls.
	if err := validatePluginStateRPCText(req.Owner, pluginStateMaxOwnerBytes, true); err != nil {
		return nil, err
	}
	ttl, err := pluginStateRPCLeaseTTL(req.TtlSeconds)
	if err != nil {
		return nil, err
	}
	lease, err := s.stateStore.LeaseAcquire(ctx, s.pluginKey, req.Namespace, req.Key, ttl)
	if err != nil {
		return nil, pluginStateRPCError(err)
	}
	return pluginStateRPCLeaseResponse(lease)
}

func (s *pluginHostServiceServer) RenewLease(ctx context.Context, req *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	if err := s.validatePluginLeaseRPC(req); err != nil {
		return nil, err
	}
	lease, err := pluginStateRPCLease(req.Owner, req.Fence, false)
	if err != nil {
		return nil, err
	}
	ttl, err := pluginStateRPCLeaseTTL(req.TtlSeconds)
	if err != nil {
		return nil, err
	}
	renewed, err := s.stateStore.LeaseRenew(ctx, s.pluginKey, req.Namespace, req.Key, *lease, ttl)
	if err != nil {
		return nil, pluginStateRPCError(err)
	}
	return pluginStateRPCLeaseResponse(renewed)
}

func (s *pluginHostServiceServer) ReleaseLease(ctx context.Context, req *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	if err := s.validatePluginLeaseRPC(req); err != nil {
		return nil, err
	}
	if req.TtlSeconds != 0 {
		return nil, status.Error(codes.InvalidArgument, "release ttl_seconds must be zero")
	}
	lease, err := pluginStateRPCLease(req.Owner, req.Fence, false)
	if err != nil {
		return nil, err
	}
	if err := s.stateStore.LeaseRelease(ctx, s.pluginKey, req.Namespace, req.Key, *lease); err != nil {
		return nil, pluginStateRPCError(err)
	}
	return &pluginv1.LeaseResponse{Acquired: false}, nil
}

func (s *pluginHostServiceServer) validatePluginLeaseRPC(req *pluginv1.LeaseRequest) error {
	if !s.stateReady() {
		return status.Error(codes.Unavailable, "plugin state store unavailable")
	}
	if req == nil {
		return status.Error(codes.InvalidArgument, "request is required")
	}
	return validatePluginStateRPCSlot(req.Namespace, req.Key)
}

func validatePluginStateRPCSlot(namespace, key string) error {
	if err := validatePluginStateRPCNamespace(namespace); err != nil {
		return err
	}
	return validatePluginStateRPCText(key, pluginStateMaxKeyBytes, false)
}

func validatePluginStateRPCNamespace(namespace string) error {
	if !isValidPluginKVSegment(namespace, pluginStateMaxNamespaceBytes) {
		return status.Error(codes.InvalidArgument, "namespace must use letters, digits, '.', '_' or '-' within 128 bytes")
	}
	return nil
}

// Keys are bound SQL values, not paths or Redis patterns; '/' and '%' are literal.
func validatePluginStateRPCText(value string, maxBytes int, allowEmpty bool) error {
	if !allowEmpty && value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return status.Error(codes.InvalidArgument, "invalid state key, cursor or lease owner")
	}
	return nil
}

func pluginStateRPCLease(owner string, fence uint64, optional bool) (*PluginLease, error) {
	if optional && owner == "" && fence == 0 {
		return nil, nil
	}
	if err := validatePluginStateRPCText(owner, pluginStateMaxOwnerBytes, false); err != nil {
		return nil, err
	}
	if fence == 0 || fence > math.MaxInt64 {
		return nil, status.Error(codes.InvalidArgument, "lease owner and positive int64 fence are required")
	}
	return &PluginLease{Owner: owner, Fence: int64(fence)}, nil
}

func pluginStateRPCLeaseTTL(seconds int64) (time.Duration, error) {
	// Check before multiplication, so overflowing protobuf input cannot wrap.
	if seconds < 1 || seconds > pluginStateMaxLeaseSeconds {
		return 0, status.Error(codes.InvalidArgument, "lease ttl_seconds must be between 1 and 300")
	}
	return time.Duration(seconds) * time.Second, nil
}

func pluginStateRPCLeaseResponse(lease PluginLease) (*pluginv1.LeaseResponse, error) {
	if lease.Owner == "" || lease.Fence <= 0 || lease.ExpiresAt.IsZero() || len(lease.Owner) > pluginStateMaxOwnerBytes {
		return nil, status.Error(codes.Internal, "invalid plugin lease result")
	}
	return &pluginv1.LeaseResponse{Acquired: true, Owner: lease.Owner, Fence: uint64(lease.Fence), ExpiresAt: lease.ExpiresAt.Unix()}, nil
}

func pluginStateRPCError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrPluginLeaseLost):
		return status.Error(codes.FailedPrecondition, "plugin lease lost")
	case errors.Is(err, ErrPluginStateConflict):
		return status.Error(codes.Aborted, "plugin state conflict")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "plugin state operation canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "plugin state operation timed out")
	default:
		// Do not forward database, cipher, lease token or arbitrary gRPC details.
		return status.Error(codes.Internal, "plugin state operation failed")
	}
}
