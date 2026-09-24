package service

import (
	"math"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// normalizeAdminProxyBinding decodes the native proxy list's negative group ID
// before any account validation or persistence sees a proxy_id.
func normalizeAdminProxyBinding(proxyID, groupID **int64) error {
	if proxyID == nil || groupID == nil {
		return nil
	}
	if *proxyID != nil && **proxyID < 0 {
		if **proxyID == math.MinInt64 {
			return ErrProxyIPGroupNotFound
		}
		decoded := -**proxyID
		// When both compatibility fields are present, the explicit group field
		// must identify the same group. A zero value is an explicit clear value,
		// not an alias for the decoded virtual ID.
		if *groupID != nil && **groupID != decoded {
			return infraerrors.BadRequest("ACCOUNT_PROXY_BINDING_CONFLICT", "proxy_id and proxy_ip_group_id refer to different proxy groups")
		}
		*proxyID = nil
		*groupID = &decoded
	}
	if *proxyID != nil && **proxyID > 0 && *groupID != nil && **groupID > 0 {
		return infraerrors.BadRequest("ACCOUNT_PROXY_BINDING_CONFLICT", "proxy_id and proxy_ip_group_id are mutually exclusive")
	}
	return nil
}
