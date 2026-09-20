package service

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
)

// OpenAIGatewayService implements service.PluginAccountDirectory for the OpenAI
// OAuth outbound transport capability. The directory is intentionally scoped to
// OpenAI OAuth-like, non-shadow accounts regardless of the requested filter, so a
// plugin can never enumerate or resolve credentials outside that set. The host
// additionally only wires this directory into plugins whose manifest declares the
// matching capability (see PluginManager.buildHostServices).

// ListPluginAccounts returns the ids of active OpenAI OAuth-like accounts.
func (s *OpenAIGatewayService) ListPluginAccounts(ctx context.Context, platform, accountType string) ([]int64, error) {
	if s == nil || s.accountRepo == nil {
		return nil, nil
	}
	if p := strings.TrimSpace(platform); p != "" && p != PlatformOpenAI {
		return nil, nil
	}
	accountType = strings.TrimSpace(accountType)
	if accountType != "" && accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken {
		return nil, nil
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(accounts))
	for i := range accounts {
		account := accounts[i]
		if eligiblePluginAccount(&account) && (accountType == "" || account.Type == accountType) {
			ids = append(ids, account.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

// ResolvePluginOutboundIdentity resolves the access token plus the outbound
// identity headers and proxy the host would attach to a live request for the
// account. It returns (nil, nil) for out-of-scope accounts or when no token can
// be resolved.
func (s *OpenAIGatewayService) ResolvePluginOutboundIdentity(ctx context.Context, accountID int64) (*PluginOutboundIdentity, error) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return nil, nil
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		if errors.Is(err, ErrAccountNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if !eligiblePluginAccount(account) || account.ID != accountID {
		return nil, nil
	}
	identityRevision := PluginAccountIdentityRevision(account)
	egresses := []PluginOutboundEgress{}
	representatives, err := s.OpenAIProxyGroupRepresentatives(ctx, account)
	if err != nil && !errors.Is(err, ErrOpenAIProxyGroupNoEgress) {
		return nil, errors.New("plugin egress unavailable")
	}
	for _, representative := range representatives {
		if representative.Proxy != nil {
			egresses = append(egresses, PluginOutboundEgress{ProxyID: representative.Proxy.ID, ProxyURL: representative.Proxy.URL()})
		}
	}
	token, _, err := s.GetAccessToken(ctx, account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, nil
	}
	headers := http.Header{}
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(ctx, s.accountRepo, headers, account); err != nil {
		return nil, err
	}
	ensureCodexIdentityHeaders(headers)
	enforceCodexIdentityHeaders(headers)
	proxyURL := ""
	if len(egresses) > 0 {
		proxyURL = egresses[0].ProxyURL
	}
	return &PluginOutboundIdentity{
		AccountID:        account.ID,
		Platform:         account.Platform,
		AccountType:      account.Type,
		ProxyURL:         proxyURL,
		Token:            token,
		Headers:          headers,
		IdentityRevision: identityRevision,
		Egresses:         egresses,
	}, nil
}
