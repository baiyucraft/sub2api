package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	PluginActionMaxPayloadBytes = 256 * 1024
	pluginActionTimeout         = 30 * time.Second
	pluginActionMaxIDBytes      = 128
	pluginActionMaxNameBytes    = 128
	pluginActionMaxResultBytes  = 256 * 1024
)

// PluginActionResult embeds JSON as JSON, not protobuf's base64 byte encoding.
type PluginActionResult struct {
	ActionID string          `json:"action_id"`
	Accepted bool            `json:"accepted"`
	Status   string          `json:"status"`
	Result   json.RawMessage `json:"result"`
}

func pluginAdminOperationError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows), infraerrors.Code(err) == http.StatusNotFound:
		return infraerrors.NotFound("PLUGIN_NOT_FOUND", "plugin not found")
	case errors.Is(err, context.DeadlineExceeded), status.Code(err) == codes.DeadlineExceeded:
		return infraerrors.New(http.StatusGatewayTimeout, "PLUGIN_ACTION_TIMEOUT", "plugin operation timed out")
	case errors.Is(err, context.Canceled), status.Code(err) == codes.Canceled:
		return infraerrors.New(http.StatusRequestTimeout, "PLUGIN_ACTION_CANCELED", "plugin operation canceled")
	default:
		return infraerrors.ServiceUnavailable("PLUGIN_OPERATION_UNAVAILABLE", "plugin operation unavailable")
	}
}

func validatePluginActionRequest(request *pluginv1.RunActionRequest) error {
	if request == nil || !isValidPluginKVSegment(request.ActionId, pluginActionMaxIDBytes) || !isValidPluginKVSegment(request.Name, pluginActionMaxNameBytes) {
		return infraerrors.BadRequest("PLUGIN_ACTION_INVALID", "invalid action_id or name")
	}
	payload := bytes.TrimSpace(request.PayloadJson)
	if len(request.PayloadJson) > PluginActionMaxPayloadBytes || len(payload) == 0 || payload[0] != '{' || !json.Valid(payload) {
		return infraerrors.BadRequest("PLUGIN_ACTION_INVALID", "action payload must be a size-limited JSON object")
	}
	return nil
}

// RunAction never starts a runtime and leaves action idempotency to the engine.
func (m *PluginManager) RunAction(ctx context.Context, id int64, request *pluginv1.RunActionRequest) (*PluginActionResult, error) {
	if err := validatePluginActionRequest(request); err != nil {
		return nil, err
	}
	if m == nil || m.repo == nil {
		return nil, infraerrors.ServiceUnavailable("PLUGIN_ACTION_UNAVAILABLE", "plugin action unavailable")
	}
	// Lifecycle/configuration writes own operationMu; refuse contention so actions
	// cannot dispatch against a changing runtime or wait beyond their RPC budget.
	if !m.operationMu.TryLock() {
		return nil, infraerrors.Conflict("PLUGIN_OPERATION_BUSY", "plugin lifecycle operation in progress")
	}
	defer m.operationMu.Unlock()
	actionCtx, cancel := context.WithTimeout(ctx, pluginActionTimeout)
	defer cancel()
	installation, err := m.repo.GetByID(actionCtx, id)
	if err != nil {
		return nil, pluginAdminOperationError(err)
	}
	if installation == nil || installation.State != PluginStateEnabled {
		return nil, infraerrors.Conflict("PLUGIN_NOT_ENABLED", "plugin must be enabled")
	}
	m.mu.Lock()
	runtime := m.runtimes[id]
	if runtime == nil || runtime.api == nil || runtime.client == nil || runtime.client.Exited() || !runtime.beginRequest() {
		m.mu.Unlock()
		return nil, infraerrors.Conflict("PLUGIN_NOT_RUNNING", "plugin must be running")
	}
	m.mu.Unlock()
	defer runtime.finishRequest()
	// Pin the running package and applied configuration to the installation read.
	route := m.route.Load()
	if runtime.installation == nil || runtime.installation.BinarySHA256 != installation.BinarySHA256 || route == nil || route.pluginID != id || route.runtime != runtime || route.configRevision != installation.ConfigRevision {
		return nil, infraerrors.Conflict("PLUGIN_CONFIG_STALE", "plugin configuration is reconciling")
	}
	result, err := runtime.api.RunAction(actionCtx, &pluginv1.RunActionRequest{
		ActionId: request.ActionId, Name: request.Name,
		PayloadJson: append([]byte(nil), request.PayloadJson...), ConfigRevision: installation.ConfigRevision,
	})
	if err != nil {
		return nil, pluginAdminOperationError(err)
	}
	if result == nil || result.ActionId != request.ActionId || !isValidPluginKVSegment(result.Status, 64) || len(result.ResultJson) > pluginActionMaxResultBytes {
		return nil, infraerrors.New(http.StatusBadGateway, "PLUGIN_ACTION_INVALID_RESULT", "invalid plugin action result")
	}
	raw := bytes.TrimSpace(result.ResultJson)
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if raw[0] != '{' || !json.Valid(raw) {
		return nil, infraerrors.New(http.StatusBadGateway, "PLUGIN_ACTION_INVALID_RESULT", "invalid plugin action result")
	}
	return &PluginActionResult{ActionID: result.ActionId, Accepted: result.Accepted, Status: result.Status, Result: append(json.RawMessage(nil), raw...)}, nil
}
