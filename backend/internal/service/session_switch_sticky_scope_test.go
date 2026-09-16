package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionSwitchStickyScopeOnlyAppliesToSpeedFirstKeys(t *testing.T) {
	base := context.Background()

	cacheFirst := &APIKey{ID: 10, SchedulingMode: APIKeySchedulingModeCacheFirst}
	require.Zero(t, SessionSwitchStickyScopeAPIKeyID(WithSessionSwitchStickyScope(base, cacheFirst)))

	speedFirst := &APIKey{ID: 11, SchedulingMode: APIKeySchedulingModeSpeedFirst}
	scoped := WithSessionSwitchStickyScope(base, speedFirst)
	require.Equal(t, int64(11), SessionSwitchStickyScopeAPIKeyID(scoped))
	require.Zero(t, SessionSwitchStickyNamespaceAPIKeyID(scoped))
	require.Equal(t, int64(11), SessionSwitchStickyNamespaceAPIKeyID(WithSessionSwitchStickyOperation(scoped)))

	require.Zero(t, SessionSwitchStickyScopeAPIKeyID(WithSessionSwitchStickyScope(base, nil)))
	require.Zero(t, SessionSwitchStickyScopeAPIKeyID(WithSessionSwitchStickyScope(base, &APIKey{SchedulingMode: APIKeySchedulingModeSpeedFirst})))
}
