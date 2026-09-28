package service

import (
	"context"
	"errors"
	"fmt"
	"math"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrProxyBindingNotFound = infraerrors.NotFound("PROXY_BINDING_NOT_FOUND", "proxy binding not found")

type adminProxyBinding struct {
	id      int64
	kind    string
	groupID int64
}

// A retained registry entry for a deleted proxy or group is not a live binding.
func (s *adminServiceImpl) resolveAdminProxyBinding(ctx context.Context, id int64) (adminProxyBinding, error) {
	if id <= 0 {
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
	if s == nil || s.entClient == nil {
		return adminProxyBinding{}, errors.New("proxy binding registry is unavailable")
	}
	row, err := s.entClient.ProxyBinding.Get(ctx, id)
	if dbent.IsNotFound(err) {
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
	if err != nil {
		return adminProxyBinding{}, fmt.Errorf("get proxy binding: %w", err)
	}
	switch row.BindingType {
	case proxyBindingTypeProxy:
		if row.ProxyID == nil || *row.ProxyID != id || row.ProxyIPGroupID != nil || s.proxyRepo == nil {
			return adminProxyBinding{}, ErrProxyBindingNotFound
		}
		if _, err := s.proxyRepo.GetByID(ctx, *row.ProxyID); err != nil {
			return adminProxyBinding{}, err
		}
		return adminProxyBinding{id: id, kind: proxyBindingTypeProxy}, nil
	case proxyBindingTypeProxyIPGroup:
		if row.ProxyIPGroupID == nil || *row.ProxyIPGroupID <= 0 || row.ProxyID != nil || s.proxyIPGroupRepo == nil {
			return adminProxyBinding{}, ErrProxyBindingNotFound
		}
		group, err := s.proxyIPGroupRepo.GetByID(ctx, *row.ProxyIPGroupID)
		if err != nil {
			return adminProxyBinding{}, err
		}
		if group == nil || group.BindingID != id {
			return adminProxyBinding{}, ErrProxyBindingNotFound
		}
		return adminProxyBinding{id: id, kind: proxyBindingTypeProxyIPGroup, groupID: group.ID}, nil
	default:
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
}

func (s *adminServiceImpl) resolveAdminGroupBinding(ctx context.Context, groupID int64) (adminProxyBinding, error) {
	if groupID <= 0 || s == nil || s.proxyIPGroupRepo == nil {
		return adminProxyBinding{}, ErrProxyIPGroupNotFound
	}
	group, err := s.proxyIPGroupRepo.GetByID(ctx, groupID)
	if err != nil {
		return adminProxyBinding{}, err
	}
	if group == nil || group.BindingID <= 0 {
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
	binding, err := s.resolveAdminProxyBinding(ctx, group.BindingID)
	if err != nil {
		return adminProxyBinding{}, err
	}
	if binding.kind != proxyBindingTypeProxyIPGroup || binding.groupID != groupID {
		return adminProxyBinding{}, ErrProxyBindingNotFound
	}
	return binding, nil
}

// The legacy negative ID and group field are accepted only at the input boundary.
func normalizeAdminProxyBinding(proxyID, groupID **int64, byID, byGroup func(int64) (adminProxyBinding, error)) error {
	if proxyID == nil || groupID == nil {
		return nil
	}
	if *groupID != nil && **groupID < 0 {
		return ErrProxyIPGroupNotFound
	}
	var resolved *adminProxyBinding
	if *proxyID != nil && **proxyID != 0 {
		id := **proxyID
		if id == math.MinInt64 {
			return ErrProxyIPGroupNotFound
		}
		var binding adminProxyBinding
		var err error
		if id < 0 {
			binding, err = byGroup(-id)
		} else {
			binding, err = byID(id)
		}
		if err != nil {
			return err
		}
		resolved = &binding
	}
	if *groupID != nil && **groupID > 0 {
		group, err := byGroup(**groupID)
		if err != nil {
			return err
		}
		if resolved != nil && (resolved.kind != proxyBindingTypeProxyIPGroup || resolved.groupID != group.groupID) {
			return infraerrors.BadRequest("ACCOUNT_PROXY_BINDING_CONFLICT", "proxy_id and proxy_ip_group_id refer to different bindings")
		}
		resolved = &group
	} else if *groupID != nil && **groupID == 0 && *proxyID != nil && **proxyID < 0 {
		return infraerrors.BadRequest("ACCOUNT_PROXY_BINDING_CONFLICT", "legacy group binding conflicts with explicit clear")
	}
	if resolved != nil {
		id := resolved.id
		*proxyID = &id
	} else if *groupID != nil && *proxyID == nil {
		clearID := int64(0)
		*proxyID = &clearID
	}
	*groupID = nil
	return nil
}

func (s *adminServiceImpl) normalizeAdminProxyBinding(ctx context.Context, proxyID, groupID **int64) error {
	return normalizeAdminProxyBinding(proxyID, groupID,
		func(id int64) (adminProxyBinding, error) { return s.resolveAdminProxyBinding(ctx, id) },
		func(id int64) (adminProxyBinding, error) { return s.resolveAdminGroupBinding(ctx, id) },
	)
}
