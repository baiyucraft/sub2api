package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// PluginResources is the credential-free projection shared with administrative UI.
type PluginResources struct {
	Accounts []PluginResourceAccount `json:"accounts"`
	Groups   []PluginResourceGroup   `json:"groups"`
	Proxies  []PluginResourceProxy   `json:"proxies"`
}

type PluginResourceAccount struct {
	ID                       int64   `json:"id"`
	Name                     string  `json:"name"`
	Platform                 string  `json:"platform"`
	AccountType              string  `json:"account_type"`
	GroupIDs                 []int64 `json:"group_ids"`
	BusinessEgressConfigured bool    `json:"business_egress_configured"`
}

type PluginResourceGroup struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type PluginResourceProxy struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

// ResolvePluginProxy is only offered to the plugin process, never the UI bridge.
type PluginResourceDirectory interface {
	ListPluginResources(ctx context.Context) (*PluginResources, error)
	ResolvePluginProxy(ctx context.Context, id int64) (string, error)
}

func newPluginResources() *PluginResources {
	return &PluginResources{Accounts: []PluginResourceAccount{}, Groups: []PluginResourceGroup{}, Proxies: []PluginResourceProxy{}}
}

func eligiblePluginAccount(account *Account) bool {
	return account != nil && account.ID > 0 && account.Status == StatusActive && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// PluginAccountIdentityRevision is shared by admission and outbound resolution.
// Token refreshes and changes to proxy membership do not change this revision.
func PluginAccountIdentityRevision(account *Account) string {
	if account == nil {
		return ""
	}
	accountID := strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	userID := strings.TrimSpace(account.GetCredential("chatgpt_user_id"))
	unknownIdentity := ""
	if accountID == "" || userID == "" {
		// Without a complete provider identity, token rotation must invalidate
		// state rather than risk reusing it after an account credential swap.
		unknownIdentity = account.GetCredential("access_token")
	}
	raw := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", account.ID, account.Type, accountID, userID, unknownIdentity)
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *OpenAIGatewayService) ListPluginResources(ctx context.Context) (*PluginResources, error) {
	resources := newPluginResources()
	if s == nil || s.accountRepo == nil {
		return resources, nil
	}
	// Repository reads retain the normal soft-delete filter and hydrate live groups.
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("PLUGIN_RESOURCES_UNAVAILABLE", "plugin resources unavailable")
	}
	groups := make(map[int64]PluginResourceGroup)
	for i := range accounts {
		account := &accounts[i]
		if !eligiblePluginAccount(account) {
			continue
		}
		groupIDs := make([]int64, 0, len(account.Groups))
		seen := make(map[int64]bool)
		for _, group := range account.Groups {
			if group == nil || group.ID <= 0 || group.Status != StatusActive || seen[group.ID] {
				continue
			}
			seen[group.ID] = true
			groupIDs = append(groupIDs, group.ID)
			groups[group.ID] = PluginResourceGroup{ID: group.ID, Name: group.Name}
		}
		sort.Slice(groupIDs, func(i, j int) bool { return groupIDs[i] < groupIDs[j] })
		resources.Accounts = append(resources.Accounts, PluginResourceAccount{
			ID: account.ID, Name: account.Name, Platform: account.Platform, AccountType: account.Type,
			GroupIDs: groupIDs, BusinessEgressConfigured: s.pluginAccountHasUsableEgress(ctx, account),
		})
	}
	for _, group := range groups {
		resources.Groups = append(resources.Groups, group)
	}
	if s.proxyRepo != nil {
		proxies, err := s.proxyRepo.ListActive(ctx)
		if err != nil {
			return nil, infraerrors.ServiceUnavailable("PLUGIN_RESOURCES_UNAVAILABLE", "plugin resources unavailable")
		}
		for _, proxy := range usableProxyGroupMembers(proxies, time.Now()) {
			resources.Proxies = append(resources.Proxies, PluginResourceProxy{
				ID: proxy.ID, Name: proxy.Name, Protocol: proxy.Protocol, Host: proxy.Host, Port: proxy.Port,
			})
		}
	}
	sort.Slice(resources.Accounts, func(i, j int) bool { return resources.Accounts[i].ID < resources.Accounts[j].ID })
	sort.Slice(resources.Groups, func(i, j int) bool { return resources.Groups[i].ID < resources.Groups[j].ID })
	return resources, nil
}

func (s *OpenAIGatewayService) pluginAccountHasUsableEgress(ctx context.Context, account *Account) bool {
	if account.ProxyIPGroupID != nil {
		members, err := s.OpenAIProxyGroupRepresentatives(ctx, account)
		return err == nil && len(members) > 0
	}
	if account.ProxyID == nil {
		return false
	}
	proxy := account.Proxy
	if s.proxyRepo != nil {
		var err error
		proxy, err = s.proxyRepo.GetByID(ctx, *account.ProxyID)
		if err != nil {
			return false
		}
	}
	return proxy != nil && proxy.ID == *account.ProxyID && proxy.IsActive() && !proxy.IsExpired(time.Now())
}

func (s *OpenAIGatewayService) ResolvePluginProxy(ctx context.Context, id int64) (string, error) {
	if s == nil || s.proxyRepo == nil || id <= 0 {
		return "", nil
	}
	proxy, err := s.proxyRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, ErrProxyNotFound) {
			return "", nil
		}
		return "", infraerrors.ServiceUnavailable("PLUGIN_PROXY_UNAVAILABLE", "plugin proxy unavailable")
	}
	if proxy == nil || proxy.ID != id || !proxy.IsActive() || proxy.IsExpired(time.Now()) {
		return "", nil
	}
	return proxy.URL(), nil
}

// Resources only reads installation and directory data, including for disabled plugins.
func (m *PluginManager) Resources(ctx context.Context, id int64) (*PluginResources, error) {
	if m == nil || m.repo == nil {
		return nil, infraerrors.ServiceUnavailable("PLUGIN_RESOURCES_UNAVAILABLE", "plugin resources unavailable")
	}
	installation, err := m.repo.GetByID(ctx, id)
	if err != nil {
		return nil, pluginAdminOperationError(err)
	}
	if installation == nil {
		return nil, infraerrors.NotFound("PLUGIN_NOT_FOUND", "plugin not found")
	}
	m.mu.Lock()
	directory, ok := m.accountDirectory.(PluginResourceDirectory)
	m.mu.Unlock()
	if !ok {
		return nil, infraerrors.ServiceUnavailable("PLUGIN_RESOURCES_UNAVAILABLE", "plugin resources unavailable")
	}
	resources, err := directory.ListPluginResources(ctx)
	if err != nil {
		return nil, pluginAdminOperationError(err)
	}
	if resources == nil {
		return newPluginResources(), nil
	}
	return resources, nil
}
