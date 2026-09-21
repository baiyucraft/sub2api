package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

const OpenAIProxyGroupNoEgressCode = "openai_proxy_group_no_egress"

var (
	ErrOpenAIProxyGroupNoEgress           = errors.New("openai proxy group has no usable egress")
	ErrOpenAIProxyGroupCapacityFull       = errors.New("openai proxy group capacity full")
	ErrOpenAIProxyGroupBindingUnavailable = errors.New("openai proxy group binding unavailable")
)

type OpenAIManagementEgressResolver interface {
	AcquireOpenAIManagementEgress(ctx context.Context, account *Account) (*Account, func(), error)
}

// SetOpenAIProxyGroupRepositories wires the narrow proxy-group dependencies
// without expanding NewOpenAIGatewayService's compatibility-sensitive
// constructor.
func (s *OpenAIGatewayService) SetOpenAIProxyGroupRepositories(groupRepo ProxyIPGroupRepository, proxyRepo ProxyRepository) {
	if s == nil {
		return
	}
	s.proxyIPGroupRepo = groupRepo
	s.proxyRepo = proxyRepo
	if s.openAITokenProvider != nil && s.openAITokenProvider.openAIOAuthService != nil {
		s.openAITokenProvider.openAIOAuthService.SetOpenAIManagementEgressResolver(s)
	}
}

func acquireOpenAIManagementEgress(ctx context.Context, resolver OpenAIManagementEgressResolver, account *Account) (*Account, func(), error) {
	if account == nil || account.ProxyIPGroupID == nil {
		return account, func() {}, nil
	}
	if resolver == nil {
		return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
	}
	return resolver.AcquireOpenAIManagementEgress(ctx, account)
}

func openAIManagementEgressError(err error) error {
	if err == nil {
		return nil
	}
	return infraerrors.New(http.StatusServiceUnavailable, OpenAIProxyGroupNoEgressCode, "OpenAI proxy group has no available egress").WithCause(err)
}

func supportsOpenAIProxyGroup(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike()
}

func cloneAccountWithProxy(account *Account, proxy Proxy) *Account {
	clone := *account
	proxyCopy := proxy
	proxyID := proxy.ID
	// Keep ProxyIPGroupID/ProxyIPGroup on the request-local clone for
	// attribution and diagnostics. Persisted accounts still enforce proxy_id
	// and proxy_ip_group_id mutual exclusion; this clone represents the
	// resolved member selected from that group.
	clone.ProxyID = &proxyID
	clone.Proxy = &proxyCopy
	return &clone
}

func usableProxyGroupMembers(proxies []Proxy, now time.Time) []Proxy {
	out := make([]Proxy, 0, len(proxies))
	for _, proxy := range proxies {
		if proxy.ID <= 0 || !proxy.IsActive() || proxy.IsExpired(now) {
			continue
		}
		out = append(out, proxy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func proxyByID(proxies []Proxy, proxyID int64) (Proxy, bool) {
	for _, proxy := range proxies {
		if proxy.ID == proxyID {
			return proxy, true
		}
	}
	return Proxy{}, false
}

func (s *OpenAIGatewayService) loadOpenAIProxyGroupMembers(ctx context.Context, account *Account) (*ProxyIPGroup, []Proxy, error) {
	if account == nil || account.ProxyIPGroupID == nil || *account.ProxyIPGroupID <= 0 {
		return nil, nil, nil
	}
	if !supportsOpenAIProxyGroup(account) {
		return nil, nil, fmt.Errorf("%w: account type does not support proxy groups", ErrOpenAIProxyGroupNoEgress)
	}
	if s == nil || s.proxyIPGroupRepo == nil || s.proxyRepo == nil {
		return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
	}
	group, err := s.proxyIPGroupRepo.GetByID(ctx, *account.ProxyIPGroupID)
	if err != nil || group == nil || group.ID <= 0 || group.PerIPConcurrency <= 0 {
		return nil, nil, fmt.Errorf("%w: proxy group unavailable", ErrOpenAIProxyGroupNoEgress)
	}
	if len(group.ProxyIDs) == 0 {
		return group, nil, ErrOpenAIProxyGroupNoEgress
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, group.ProxyIDs)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: list proxy group members: %v", ErrOpenAIProxyGroupBindingUnavailable, err)
	}
	usable := usableProxyGroupMembers(proxies, time.Now())
	if len(usable) == 0 {
		return group, nil, ErrOpenAIProxyGroupNoEgress
	}
	return group, usable, nil
}

// OpenAIProxyGroupRepresentatives returns stable request-local account clones
// for business-egress verification. It does not reserve concurrency slots.
func (s *OpenAIGatewayService) OpenAIProxyGroupRepresentatives(ctx context.Context, account *Account) ([]*Account, error) {
	if account == nil {
		return nil, ErrOpenAIProxyGroupNoEgress
	}
	if account.ProxyIPGroupID == nil {
		if account.ProxyID == nil || account.Proxy == nil || !account.Proxy.IsActive() || account.Proxy.IsExpired(time.Now()) {
			return nil, ErrOpenAIProxyGroupNoEgress
		}
		return []*Account{account}, nil
	}
	_, members, err := s.loadOpenAIProxyGroupMembers(ctx, account)
	if err != nil {
		return nil, err
	}
	out := make([]*Account, 0, len(members))
	for _, proxy := range members {
		out = append(out, cloneAccountWithProxy(account, proxy))
	}
	return out, nil
}

// AcquireOpenAIManagementEgress resolves a request-local proxy member for
// account management traffic and reserves its per-proxy concurrency slot. It
// intentionally does not create a session binding: quota, token refresh and
// account probes are independent management operations rather than user
// sessions.
func (s *OpenAIGatewayService) AcquireOpenAIManagementEgress(ctx context.Context, account *Account) (*Account, func(), error) {
	if account == nil || account.ProxyIPGroupID == nil {
		return account, func() {}, nil
	}
	group, members, err := s.loadOpenAIProxyGroupMembers(ctx, account)
	if err != nil {
		return nil, nil, err
	}
	if s == nil || s.concurrencyService == nil {
		return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
	}
	for _, proxy := range members {
		result, acquireErr := s.concurrencyService.AcquireAccountProxySlot(ctx, account.ID, proxy.ID, group.PerIPConcurrency)
		if acquireErr != nil {
			return nil, nil, fmt.Errorf("%w: acquire management proxy slot: %v", ErrOpenAIProxyGroupBindingUnavailable, acquireErr)
		}
		if result == nil || !result.Acquired {
			continue
		}
		return cloneAccountWithProxy(account, proxy), result.ReleaseFunc, nil
	}
	return nil, nil, ErrOpenAIProxyGroupCapacityFull
}

// AcquireOpenAIProxyGroupEgress resolves one proxy member and reserves its
// second-level slot. Single-proxy accounts are returned unchanged.
func (s *OpenAIGatewayService) AcquireOpenAIProxyGroupEgress(ctx context.Context, account *Account, sessionHash string) (*Account, func(), error) {
	if account == nil || account.ProxyIPGroupID == nil {
		return account, func() {}, nil
	}
	group, members, err := s.loadOpenAIProxyGroupMembers(ctx, account)
	if err != nil {
		return nil, nil, err
	}
	if s.cache == nil || s.concurrencyService == nil {
		return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
	}
	bindingCache, ok := s.cache.(OpenAIProxyGroupBindingCache)
	if !ok {
		return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
	}

	sessionHash = strings.TrimSpace(sessionHash)
	for attempt := 0; attempt < 3; attempt++ {
		boundProxyID := int64(0)
		if sessionHash != "" {
			boundID, bindErr := bindingCache.GetOpenAIProxyGroupBinding(ctx, account.ID, sessionHash)
			switch {
			case bindErr == nil && boundID > 0:
				boundProxyID = boundID
				if proxy, found := proxyByID(members, boundID); found {
					result, acquireErr := s.concurrencyService.AcquireAccountProxySlot(ctx, account.ID, proxy.ID, group.PerIPConcurrency)
					if acquireErr != nil {
						return nil, nil, fmt.Errorf("%w: acquire bound proxy slot: %v", ErrOpenAIProxyGroupBindingUnavailable, acquireErr)
					}
					if result != nil && result.Acquired {
						claimedID, refreshErr := bindingCache.ClaimOpenAIProxyGroupBinding(ctx, account.ID, sessionHash, proxy.ID, stickySessionTTL)
						if refreshErr != nil {
							result.ReleaseFunc()
							return nil, nil, fmt.Errorf("%w: refresh proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, refreshErr)
						}
						if claimedID == proxy.ID {
							return cloneAccountWithProxy(account, proxy), result.ReleaseFunc, nil
						}
						result.ReleaseFunc()
						continue
					}
					// The sticky member is locally full. Continue below and try
					// another member before declaring the whole proxy group full.
				}
				if _, found := proxyByID(members, boundID); !found {
					if deleteErr := bindingCache.DeleteOpenAIProxyGroupBindingIfMatch(ctx, account.ID, sessionHash, boundID); deleteErr != nil {
						return nil, nil, fmt.Errorf("%w: delete stale proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, deleteErr)
					}
					boundProxyID = 0
				}
			case errors.Is(bindErr, ErrOpenAIProxyGroupBindingNotFound):
				// New session; select the first member with available capacity below.
			case bindErr != nil:
				return nil, nil, fmt.Errorf("%w: read proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, bindErr)
			}
		}

		contention := false
		for _, proxy := range members {
			if proxy.ID == boundProxyID {
				// The sticky member was already attempted above and was locally full.
				continue
			}
			result, acquireErr := s.concurrencyService.AcquireAccountProxySlot(ctx, account.ID, proxy.ID, group.PerIPConcurrency)
			if acquireErr != nil {
				return nil, nil, fmt.Errorf("%w: acquire proxy slot: %v", ErrOpenAIProxyGroupBindingUnavailable, acquireErr)
			}
			if result == nil || !result.Acquired {
				continue
			}
			if sessionHash != "" {
				var claimedID int64
				var bindErr error
				if boundProxyID > 0 {
					var replaced bool
					replaced, bindErr = bindingCache.ReplaceOpenAIProxyGroupBindingIfMatch(ctx, account.ID, sessionHash, boundProxyID, proxy.ID, stickySessionTTL)
					if replaced {
						claimedID = proxy.ID
					}
				} else {
					claimedID, bindErr = bindingCache.ClaimOpenAIProxyGroupBinding(ctx, account.ID, sessionHash, proxy.ID, stickySessionTTL)
				}
				if bindErr != nil {
					result.ReleaseFunc()
					return nil, nil, fmt.Errorf("%w: write proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, bindErr)
				}
				if claimedID != proxy.ID {
					result.ReleaseFunc()
					contention = true
					break
				}
			}
			if boundProxyID > 0 && proxy.ID != boundProxyID {
				logger.FromContext(ctx).Debug("openai.proxy_group_member_capacity_overflow",
					zap.Int64("account_id", account.ID),
					zap.Int64("proxy_group_id", *account.ProxyIPGroupID),
					zap.Int64("bound_proxy_id", boundProxyID),
					zap.Int64("selected_proxy_id", proxy.ID),
					zap.Int("per_proxy_concurrency", group.PerIPConcurrency),
					zap.String("proxy_member_switch_reason", "local_capacity_full"),
				)
			}
			return cloneAccountWithProxy(account, proxy), result.ReleaseFunc, nil
		}
		if !contention {
			return nil, nil, ErrOpenAIProxyGroupCapacityFull
		}
	}
	return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
}
