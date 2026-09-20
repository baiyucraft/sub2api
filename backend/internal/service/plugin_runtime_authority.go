package service

import (
	"context"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This is a fail-closed preflight, not a transaction fence. The repository must
// still enforce state CAS and lease fencing; installation state may change
// between this read and the mutation. Do not take scopedConfigMu here: ApplyConfig
// may call back into HostService while its acknowledgement is pending.
func (s *pluginRequestHostServiceServer) checkRuntimeAuthority(ctx context.Context) error {
	if s == nil || s.runtime == nil || s.repository == nil || s.HostServiceServer == nil {
		return status.Error(codes.Unavailable, "plugin runtime authority unavailable")
	}
	runtime := s.runtime
	installation := runtime.installation
	applied := runtime.scopedConfig.Load()
	if installation == nil || installation.ID <= 0 || installation.BinarySHA256 == "" || applied == nil || runtime.exited.Load() || runtime.staged.Load() {
		return status.Error(codes.FailedPrecondition, "plugin runtime authority changed")
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	current, err := s.repository.GetByID(readCtx, installation.ID)
	if err != nil {
		return status.Error(codes.Unavailable, "plugin runtime authority unavailable")
	}
	latestApplied := runtime.scopedConfig.Load()
	if current == nil || current.ID != installation.ID || current.PluginKey != installation.PluginKey ||
		current.BinarySHA256 != installation.BinarySHA256 || current.ConfigRevision != applied.revision ||
		latestApplied == nil || latestApplied.revision != applied.revision || runtime.exited.Load() || runtime.staged.Load() ||
		!hasEnabledOpenAIBinding(current.Bindings) ||
		(current.State != PluginStateEnabled && current.State != PluginStateUpgrading) {
		return status.Error(codes.FailedPrecondition, "plugin runtime authority changed")
	}
	// Inactive/upgrading must remain usable by existing Forward receipts. The
	// plugin pauses background acquisition on Apply(false); do not block receipts.
	return nil
}

func (s *pluginRequestHostServiceServer) StateCompareAndSwap(ctx context.Context, req *pluginv1.StateCompareAndSwapRequest) (*pluginv1.StateCompareAndSwapResponse, error) {
	if err := s.checkRuntimeAuthority(ctx); err != nil {
		return nil, err
	}
	return s.HostServiceServer.StateCompareAndSwap(ctx, req)
}

func (s *pluginRequestHostServiceServer) AcquireLease(ctx context.Context, req *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	if err := s.checkRuntimeAuthority(ctx); err != nil {
		return nil, err
	}
	return s.HostServiceServer.AcquireLease(ctx, req)
}

func (s *pluginRequestHostServiceServer) RenewLease(ctx context.Context, req *pluginv1.LeaseRequest) (*pluginv1.LeaseResponse, error) {
	if err := s.checkRuntimeAuthority(ctx); err != nil {
		return nil, err
	}
	return s.HostServiceServer.RenewLease(ctx, req)
}

func (s *pluginRequestHostServiceServer) ResolveOutboundIdentity(ctx context.Context, req *pluginv1.ResolveOutboundIdentityRequest) (*pluginv1.ResolveOutboundIdentityResponse, error) {
	if s == nil || s.runtime == nil || s.runtime.installation == nil || s.HostServiceServer == nil {
		return nil, status.Error(codes.Unavailable, "plugin runtime authority unavailable")
	}
	// Legacy directory consumers have no scoped Apply revision; preserve their
	// existing capability checks. Scoped workers cannot acquire credentials until
	// both enabled state and binding are committed by Enable.
	if pluginRequiresFeature(s.runtime.installation.Manifest, "scoped-routing.v1") {
		if err := s.checkRuntimeAuthority(ctx); err != nil {
			return nil, err
		}
	}
	return s.HostServiceServer.ResolveOutboundIdentity(ctx, req)
}
