package admin

import (
	"context"
	"fmt"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const maxRequestedAccountProxyIDs = 50

// resolveRequestedAccountProxyIDs normalizes the explicit ordered proxy list.
// A nil list preserves legacy proxy_id behavior; an explicit empty list means direct.
func resolveRequestedAccountProxyIDs(
	ctx context.Context,
	adminService service.AdminService,
	proxyIDs *[]int64,
	legacyProxyID *int64,
) (*[]int64, *int64, error) {
	if proxyIDs == nil {
		return nil, legacyProxyID, nil
	}

	ids := append([]int64(nil), (*proxyIDs)...)
	if len(ids) > maxRequestedAccountProxyIDs {
		return nil, nil, infraerrors.BadRequest(
			"ACCOUNT_PROXY_LIMIT_EXCEEDED",
			fmt.Sprintf("proxy_ids must contain at most %d proxies", maxRequestedAccountProxyIDs),
		)
	}
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, nil, infraerrors.BadRequest("INVALID_ACCOUNT_PROXY", fmt.Sprintf("proxy id %d is invalid", id))
		}
		if _, ok := seen[id]; ok {
			return nil, nil, infraerrors.BadRequest("DUPLICATE_ACCOUNT_PROXY", fmt.Sprintf("proxy id %d is duplicated", id))
		}
		seen[id] = struct{}{}
	}

	if len(ids) == 0 {
		return &ids, nil, nil
	}
	if adminService == nil {
		return nil, nil, infraerrors.InternalServer("ACCOUNT_PROXY_SERVICE_UNAVAILABLE", "account proxy service is unavailable")
	}
	proxies, err := adminService.GetProxiesByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	available := make(map[int64]struct{}, len(proxies))
	now := time.Now()
	for i := range proxies {
		if proxies[i].IsActive() && !proxies[i].IsExpired(now) {
			available[proxies[i].ID] = struct{}{}
		}
	}
	for _, id := range ids {
		if _, ok := available[id]; !ok {
			return nil, nil, infraerrors.BadRequest("INVALID_ACCOUNT_PROXY", fmt.Sprintf("proxy id %d is not available", id))
		}
	}

	first := ids[0]
	return &ids, &first, nil
}
