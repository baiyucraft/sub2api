package admin

import (
	"context"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestResolveRequestedAccountProxyIDsPreservesOrderAndSynchronizesLegacyID(t *testing.T) {
	svc := newStubAdminService()
	svc.proxies = []service.Proxy{
		{ID: 11, Status: service.StatusActive},
		{ID: 12, Status: service.StatusActive},
	}
	requested := []int64{12, 11}
	legacy := int64(99)

	proxyIDs, proxyID, err := resolveRequestedAccountProxyIDs(context.Background(), svc, &requested, &legacy)
	require.NoError(t, err)
	require.Equal(t, []int64{12, 11}, *proxyIDs)
	require.Equal(t, int64(12), *proxyID)
}

func TestResolveRequestedAccountProxyIDsExplicitEmptyMeansDirect(t *testing.T) {
	svc := newStubAdminService()
	requested := []int64{}
	legacy := int64(99)

	proxyIDs, proxyID, err := resolveRequestedAccountProxyIDs(context.Background(), svc, &requested, &legacy)
	require.NoError(t, err)
	require.NotNil(t, proxyIDs)
	require.Empty(t, *proxyIDs)
	require.Nil(t, proxyID)
}

func TestResolveRequestedAccountProxyIDsRejectsDuplicateAndUnavailable(t *testing.T) {
	svc := newStubAdminService()
	expiredAt := time.Now().Add(-time.Minute)
	svc.proxies = []service.Proxy{
		{ID: 11, Status: service.StatusActive},
		{ID: 12, Status: service.StatusDisabled},
		{ID: 13, Status: service.StatusActive, ExpiresAt: &expiredAt},
	}

	for _, tc := range []struct {
		name   string
		ids    []int64
		reason string
	}{
		{name: "duplicate", ids: []int64{11, 11}, reason: "DUPLICATE_ACCOUNT_PROXY"},
		{name: "non-positive", ids: []int64{0}, reason: "INVALID_ACCOUNT_PROXY"},
		{name: "disabled", ids: []int64{12}, reason: "INVALID_ACCOUNT_PROXY"},
		{name: "expired", ids: []int64{13}, reason: "INVALID_ACCOUNT_PROXY"},
		{name: "missing", ids: []int64{14}, reason: "INVALID_ACCOUNT_PROXY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveRequestedAccountProxyIDs(context.Background(), svc, &tc.ids, nil)
			require.Error(t, err)
			require.Equal(t, 400, infraerrors.Code(err))
			var appErr *infraerrors.ApplicationError
			require.ErrorAs(t, err, &appErr)
			require.Equal(t, tc.reason, appErr.Reason)
		})
	}
}
