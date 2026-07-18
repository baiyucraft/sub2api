package service

import (
	"context"
	"net/url"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	ChannelMonitorCredentialManual             = "manual"
	ChannelMonitorCredentialManagedLocal       = "managed_local"
	ManagedMonitorOwnerUserID            int64 = 1
)

var (
	ErrChannelMonitorManagedConfig = infraerrors.BadRequest(
		"CHANNEL_MONITOR_MANAGED_CONFIG_INVALID",
		"managed local monitor requires an HTTPS API base origin and an active bound group",
	)
	ErrChannelMonitorManagedUnsupported = infraerrors.BadRequest(
		"CHANNEL_MONITOR_MANAGED_UNAVAILABLE",
		"managed local monitor is not available",
	)
	ErrChannelMonitorManagedOwnerMissing = infraerrors.BadRequest(
		"CHANNEL_MONITOR_MANAGED_OWNER_MISSING",
		"managed local monitor owner user is not configured",
	)
	ErrChannelMonitorManagedGroupInactive = infraerrors.BadRequest(
		"CHANNEL_MONITOR_MANAGED_GROUP_INACTIVE",
		"managed local monitor group is not active",
	)
	ErrChannelMonitorManagedKeyInvalid = infraerrors.BadRequest(
		"CHANNEL_MONITOR_MANAGED_KEY_INVALID",
		"managed local monitor key binding is invalid",
	)
)

// ManagedMonitorRepository is implemented by the concrete repository without
// widening the legacy ChannelMonitorRepository test contract.
type ManagedMonitorRepository interface {
	CreateManaged(ctx context.Context, monitor *ChannelMonitor, plaintextKey string) error
	UpdateManaged(ctx context.Context, monitor *ChannelMonitor) (string, error)
	DeleteManaged(ctx context.Context, monitorID int64) (string, error)
}

// ManagedMonitorRuntimeRepository validates the binding immediately before a
// probe so disabled or soft-deleted groups never create real gateway traffic.
type ManagedMonitorRuntimeRepository interface {
	ValidateManagedRuntime(ctx context.Context, monitor *ChannelMonitor) error
}

type ManagedMonitorKeyService interface {
	GenerateKey() (string, error)
	InvalidateAuthCacheByKey(ctx context.Context, key string)
}

type ManagedMonitorSettings interface {
	GetAPIBaseURL(ctx context.Context) string
}

func validateManagedMonitorEndpoint(raw string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", ErrChannelMonitorManagedConfig
	}
	return value, nil
}
