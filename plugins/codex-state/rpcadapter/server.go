package rpcadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"sync"

	"github.com/baiyucraft/codex-state-plugin/core"
	pluginv1 "github.com/baiyucraft/codex-state-plugin/sdk/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var RequiredHostFeatures = []string{
	"scoped-routing.v1", "admission.v1", "resources.v1", "actions.v1",
	"state-cas.v1", "leases.v1", "oauth-like.v1",
	"request-completion.v1", "config-secrets.v1",
}

type Server struct {
	pluginv1.UnimplementedTransportPluginServer
	mu     sync.Mutex
	host   *Host
	engine *core.Engine
	broker *hcplugin.GRPCBroker
	conn   *grpc.ClientConn
}

func New() *Server {
	host := &Host{}
	return &Server{host: host, engine: core.New(host, core.HTTPProber{}, core.Options{})}
}
func (s *Server) SetHostBroker(broker *hcplugin.GRPCBroker) {
	s.mu.Lock()
	s.broker = broker
	s.mu.Unlock()
}
func (s *Server) Close(ctx context.Context) error {
	err := s.engine.Close(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return err
}

func (s *Server) GetInfo(context.Context, *pluginv1.GetInfoRequest) (*pluginv1.GetInfoResponse, error) {
	return &pluginv1.GetInfoResponse{PluginId: "baiyu.codex-state", PluginVersion: core.Version, ProtocolVersion: 1, TransportApiVersion: 1, Capabilities: []string{pluginv1.TransportPluginName}}, nil
}
func (s *Server) InitHostServices(ctx context.Context, r *pluginv1.InitHostServicesRequest) (*pluginv1.InitHostServicesResponse, error) {
	if r == nil || r.HostServiceApiVersion != 2 {
		return &pluginv1.InitHostServicesResponse{Message: "host service API 2 required"}, nil
	}
	features := map[string]bool{}
	for _, feature := range r.HostFeatures {
		features[feature] = true
	}
	for _, feature := range RequiredHostFeatures {
		if !features[feature] {
			return &pluginv1.InitHostServicesResponse{Message: "required host feature missing: " + feature}, nil
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.broker == nil {
		return &pluginv1.InitHostServicesResponse{Message: "host broker unavailable"}, nil
	}
	if ctx.Err() != nil {
		return nil, status.Error(codes.Canceled, "host initialization cancelled")
	}
	conn, err := s.broker.Dial(r.HostServiceId)
	if err != nil {
		return &pluginv1.InitHostServicesResponse{Message: "host broker dial failed"}, nil
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	s.conn = conn
	s.host.set(pluginv1.NewHostServiceClient(conn))
	return &pluginv1.InitHostServicesResponse{Ready: true}, nil
}
func (s *Server) Health(ctx context.Context, _ *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	raw, err := s.engine.StatusJSON(ctx)
	if err != nil {
		return &pluginv1.HealthResponse{Message: "status unavailable"}, nil
	}
	return &pluginv1.HealthResponse{Healthy: true, Message: "ready", StatusJson: string(raw)}, nil
}
func (s *Server) ValidateConfig(_ context.Context, r *pluginv1.ValidateConfigRequest) (*pluginv1.ValidateConfigResponse, error) {
	cfg, raw, err := core.Normalize(r.GetConfigJson())
	if err != nil {
		return &pluginv1.ValidateConfigResponse{Message: err.Error(), ScopedRouting: true}, nil
	}
	result := &pluginv1.ValidateConfigResponse{Valid: true, NormalizedConfigJson: raw, ScopedRouting: true}
	if cfg.Enabled {
		for _, account := range cfg.Accounts {
			target := &pluginv1.ManagedTarget{AccountId: account.AccountID}
			for _, model := range core.Models {
				if account.Models[model].Enabled {
					target.Models = append(target.Models, model)
				}
			}
			if len(target.Models) > 0 {
				result.ManagedTargets = append(result.ManagedTargets, target)
			}
		}
	}
	return result, nil
}
func (s *Server) ApplyConfig(_ context.Context, r *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
	if r == nil || r.ConfigRevision == 0 {
		return &pluginv1.ApplyConfigResponse{Message: "host configuration revision required"}, nil
	}
	cfg, err := core.Validate(r.ConfigJson)
	if err != nil {
		return &pluginv1.ApplyConfigResponse{Message: err.Error()}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.RuntimeActive && cfg.Enabled {
		if _, err = s.host.connection(nil); err != nil {
			return &pluginv1.ApplyConfigResponse{Message: "host services unavailable"}, nil
		}
	}
	revision := strconv.FormatUint(r.ConfigRevision, 10)
	oldRevision := s.engine.ConfigRevision()
	oldActive := s.engine.RuntimeActive()
	if oldRevision != "" {
		old, _ := strconv.ParseUint(oldRevision, 10, 64)
		if r.ConfigRevision < old {
			return &pluginv1.ApplyConfigResponse{Message: "stale configuration revision"}, nil
		}
	}
	s.host.configure(revision, r.RuntimeActive && cfg.Enabled)
	if err = s.engine.Apply(r.ConfigJson, revision, r.RuntimeActive); err != nil {
		s.host.configure(oldRevision, oldActive)
		return &pluginv1.ApplyConfigResponse{Message: err.Error()}, nil
	}
	return &pluginv1.ApplyConfigResponse{Applied: true}, nil
}
func (s *Server) TestConfig(ctx context.Context, r *pluginv1.TestConfigRequest) (*pluginv1.TestConfigResponse, error) {
	_, err := core.Validate(r.GetConfigJson())
	if err != nil {
		return &pluginv1.TestConfigResponse{Message: err.Error()}, nil
	}
	raw, _ := s.engine.StatusJSON(ctx)
	return &pluginv1.TestConfigResponse{Success: true, Message: "configuration valid; no upstream probe performed", StatusJson: string(raw)}, nil
}
func (s *Server) AdmitBatch(ctx context.Context, r *pluginv1.AdmitBatchRequest) (*pluginv1.AdmitBatchResponse, error) {
	if r == nil {
		return nil, status.Error(codes.InvalidArgument, "missing admission batch")
	}
	result := &pluginv1.AdmitBatchResponse{ConfigRevision: r.ConfigRevision}
	for _, candidate := range r.Candidates {
		if candidate == nil {
			continue
		}
		decision := &pluginv1.AdmissionDecision{AccountId: candidate.AccountId, OutboundModel: candidate.OutboundModel, IdentityRevision: candidate.IdentityRevision}
		if !s.engine.RuntimeActive() {
			decision.Reason = "runtime_inactive"
		} else if strconv.FormatUint(r.ConfigRevision, 10) != s.engine.ConfigRevision() {
			decision.Reason = "config_revision_mismatch"
		} else if err := s.engine.Admit(ctx, candidate.AccountId, candidate.OutboundModel, candidate.IdentityRevision); err != nil {
			decision.Reason = "state_unavailable"
		} else if !s.engine.RuntimeActive() {
			decision.Reason = "runtime_inactive"
		} else if strconv.FormatUint(r.ConfigRevision, 10) != s.engine.ConfigRevision() {
			decision.Reason = "config_revision_mismatch"
		} else {
			decision.Allowed = true
		}
		result.Decisions = append(result.Decisions, decision)
	}
	return result, nil
}
func (s *Server) RunAction(ctx context.Context, r *pluginv1.RunActionRequest) (*pluginv1.RunActionResponse, error) {
	if r == nil || strconv.FormatUint(r.ConfigRevision, 10) != s.engine.ConfigRevision() {
		return nil, status.Error(codes.FailedPrecondition, "configuration revision mismatch")
	}
	var payload core.ActionPayload
	decoder := json.NewDecoder(bytes.NewReader(r.PayloadJson))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&payload) != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid action payload")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, status.Error(codes.InvalidArgument, "invalid action payload")
	}
	result, err := s.engine.RunAction(ctx, core.Action{Name: r.Name, ActionID: r.ActionId, Payload: payload})
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, "action unavailable")
	}
	raw, _ := json.Marshal(result)
	return &pluginv1.RunActionResponse{ActionId: r.ActionId, Accepted: result.Accepted, Status: result.Status, ResultJson: raw}, nil
}
