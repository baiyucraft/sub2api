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
		if sessionHash != "" {
			boundProxyID, bindErr := bindingCache.GetOpenAIProxyGroupBinding(ctx, account.ID, sessionHash)
			switch {
			case bindErr == nil && boundProxyID > 0:
				if proxy, found := proxyByID(members, boundProxyID); found {
					result, acquireErr := s.concurrencyService.AcquireAccountProxySlot(ctx, account.ID, proxy.ID, group.PerIPConcurrency)
					if acquireErr != nil {
						return nil, nil, fmt.Errorf("%w: acquire bound proxy slot: %v", ErrOpenAIProxyGroupBindingUnavailable, acquireErr)
					}
					if result == nil || !result.Acquired {
						return nil, nil, ErrOpenAIProxyGroupCapacityFull
					}
					claimedID, refreshErr := bindingCache.ClaimOpenAIProxyGroupBinding(ctx, account.ID, sessionHash, proxy.ID, stickySessionTTL)
					if refreshErr != nil {
						result.ReleaseFunc()
						return nil, nil, fmt.Errorf("%w: refresh proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, refreshErr)
					}
					if claimedID != proxy.ID {
						result.ReleaseFunc()
						continue
					}
					return cloneAccountWithProxy(account, proxy), result.ReleaseFunc, nil
				}
				if deleteErr := bindingCache.DeleteOpenAIProxyGroupBindingIfMatch(ctx, account.ID, sessionHash, boundProxyID); deleteErr != nil {
					return nil, nil, fmt.Errorf("%w: delete stale proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, deleteErr)
				}
				continue
			case errors.Is(bindErr, ErrOpenAIProxyGroupBindingNotFound):
				// New session; select the first member with available capacity below.
			case bindErr != nil:
				return nil, nil, fmt.Errorf("%w: read proxy binding: %v", ErrOpenAIProxyGroupBindingUnavailable, bindErr)
			}
		}

		contention := false
		for _, proxy := range members {
			result, acquireErr := s.concurrencyService.AcquireAccountProxySlot(ctx, account.ID, proxy.ID, group.PerIPConcurrency)
			if acquireErr != nil {
				return nil, nil, fmt.Errorf("%w: acquire proxy slot: %v", ErrOpenAIProxyGroupBindingUnavailable, acquireErr)
			}
			if result == nil || !result.Acquired {
				continue
			}
			if sessionHash != "" {
				claimedID, bindErr := bindingCache.ClaimOpenAIProxyGroupBinding(ctx, account.ID, sessionHash, proxy.ID, stickySessionTTL)
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
			return cloneAccountWithProxy(account, proxy), result.ReleaseFunc, nil
		}
		if !contention {
			return nil, nil, ErrOpenAIProxyGroupCapacityFull
		}
	}
	return nil, nil, ErrOpenAIProxyGroupBindingUnavailable
}
