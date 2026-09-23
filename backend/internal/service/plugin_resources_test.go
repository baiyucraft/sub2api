package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type pluginResourceAccountRepo struct {
	AccountRepository
	accounts []Account
	err      error
}

func (r *pluginResourceAccountRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, r.err
}

func (r *pluginResourceAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	if r.err != nil {
		return nil, r.err
	}
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return &r.accounts[i], nil
		}
	}
	return nil, ErrAccountNotFound
}

type pluginResourceProxyRepo struct {
	proxyGroupProxyRepoStub
	err error
}

func (r *pluginResourceProxyRepo) ListActive(context.Context) ([]Proxy, error) {
	return r.proxies, r.err
}

func (r *pluginResourceProxyRepo) GetByID(_ context.Context, id int64) (*Proxy, error) {
	if r.err != nil {
		return nil, r.err
	}
	for i := range r.proxies {
		if r.proxies[i].ID == id {
			return &r.proxies[i], nil
		}
	}
	return nil, ErrProxyNotFound
}

func pluginTestAccount(id int64, accountType string) Account {
	return Account{ID: id, Name: "account", Platform: PlatformOpenAI, Type: accountType, Status: StatusActive,
		Credentials: map[string]any{"access_token": "secret-access-token", "refresh_token": "secret-refresh-token", "chatgpt_account_id": "chatgpt-a", "chatgpt_user_id": "chatgpt-u"}}
}

func TestPluginResourcesProjectionFiltersAndExcludesCredentials(t *testing.T) {
	proxyID := int64(7)
	parentID := int64(2)
	expired := time.Now().Add(-time.Hour)
	oauth := pluginTestAccount(2, AccountTypeOAuth)
	oauth.ProxyID = &proxyID
	oauth.Groups = []*Group{{ID: 9, Name: "nine", Status: StatusActive}, {ID: 3, Name: "three", Status: StatusActive}, {ID: 4, Status: "inactive"}, {ID: 9, Name: "nine", Status: StatusActive}, nil}
	oauth.GroupIDs = []int64{9, 3, 4, 1000}
	setup := pluginTestAccount(1, AccountTypeSetupToken)
	inactive := pluginTestAccount(3, AccountTypeOAuth)
	inactive.Status = "inactive"
	shadow := pluginTestAccount(4, AccountTypeOAuth)
	shadow.ParentAccountID = &parentID
	other := pluginTestAccount(5, AccountTypeOAuth)
	other.Platform = PlatformAnthropic
	proxies := &pluginResourceProxyRepo{proxyGroupProxyRepoStub: proxyGroupProxyRepoStub{proxies: []Proxy{
		{ID: 7, Name: "seven", Protocol: "http", Host: "proxy.example", Port: 8080, Username: "secret-proxy-user", Password: "secret-proxy-password", Status: StatusActive},
		{ID: 8, Status: "inactive"}, {ID: 9, Status: StatusActive, ExpiresAt: &expired},
	}}}
	gateway := &OpenAIGatewayService{accountRepo: &pluginResourceAccountRepo{accounts: []Account{oauth, setup, inactive, shadow, other, pluginTestAccount(6, AccountTypeAPIKey)}}, proxyRepo: proxies}
	resources, err := gateway.ListPluginResources(context.Background())
	require.NoError(t, err)
	require.Len(t, resources.Accounts, 2)
	require.Equal(t, int64(1), resources.Accounts[0].ID)
	require.Equal(t, AccountTypeSetupToken, resources.Accounts[0].AccountType)
	require.Empty(t, resources.Accounts[0].GroupIDs)
	require.False(t, resources.Accounts[0].BusinessEgressConfigured)
	require.Equal(t, []int64{3, 9}, resources.Accounts[1].GroupIDs)
	require.True(t, resources.Accounts[1].BusinessEgressConfigured)
	require.Equal(t, []PluginResourceGroup{{ID: 3, Name: "three"}, {ID: 9, Name: "nine"}}, resources.Groups)
	require.Len(t, resources.Proxies, 1)
	raw, err := json.Marshal(resources)
	require.NoError(t, err)
	for _, secret := range []string{"access_token", "refresh_token", "password", "username", "secret-", "chatgpt-a", "chatgpt-u", "proxy_url", "credentials"} {
		require.NotContains(t, string(raw), secret)
	}
	require.Contains(t, string(raw), `"group_ids":[]`)
	url, err := gateway.ResolvePluginProxy(context.Background(), proxyID)
	require.NoError(t, err)
	require.Equal(t, "http://secret-proxy-user:secret-proxy-password@proxy.example:8080", url)
	for _, id := range []int64{0, 8, 9, 999} {
		url, err := gateway.ResolvePluginProxy(context.Background(), id)
		require.NoError(t, err)
		require.Empty(t, url)
	}
}

func TestPluginAccountIdentityRevisionStableAcrossTokensAndEgress(t *testing.T) {
	account := pluginTestAccount(7, AccountTypeOAuth)
	revision := PluginAccountIdentityRevision(&account)
	require.Len(t, revision, 64)
	account.Credentials["access_token"] = "rotated-token"
	account.Credentials["refresh_token"] = "rotated-refresh"
	proxyID, groupID := int64(11), int64(12)
	account.ProxyID, account.ProxyIPGroupID = &proxyID, &groupID
	account.Proxy = &Proxy{Username: "different", Password: "credential"}
	require.Equal(t, revision, PluginAccountIdentityRevision(&account))
	for _, change := range []func(*Account){
		func(a *Account) { a.ID++ },
		func(a *Account) { a.Type = AccountTypeSetupToken },
		func(a *Account) { a.Credentials["chatgpt_account_id"] = "other-account" },
		func(a *Account) { a.Credentials["chatgpt_user_id"] = "other-user" },
	} {
		changed := pluginTestAccount(7, AccountTypeOAuth)
		change(&changed)
		require.NotEqual(t, revision, PluginAccountIdentityRevision(&changed))
	}
	require.Empty(t, PluginAccountIdentityRevision(nil))
	unknown := pluginTestAccount(7, AccountTypeSetupToken)
	delete(unknown.Credentials, "chatgpt_user_id")
	before := PluginAccountIdentityRevision(&unknown)
	unknown.Credentials["access_token"] = "different-identity-without-metadata"
	require.NotEqual(t, before, PluginAccountIdentityRevision(&unknown))
}

func TestPluginOutboundIdentityUsesLiveAccountAndSortedGroupEgress(t *testing.T) {
	groupID := int64(41)
	account := pluginTestAccount(7, AccountTypeSetupToken)
	account.ProxyIPGroupID = &groupID
	expired := time.Now().Add(-time.Hour)
	accountRepo := &pluginResourceAccountRepo{accounts: []Account{account}}
	groupRepo := &proxyGroupRepoStub{group: &ProxyIPGroup{ID: groupID, PerIPConcurrency: 1, ProxyIDs: []int64{4, 3, 2, 1}}}
	gateway := &OpenAIGatewayService{accountRepo: accountRepo, proxyIPGroupRepo: groupRepo,
		proxyRepo: &pluginResourceProxyRepo{proxyGroupProxyRepoStub: proxyGroupProxyRepoStub{proxies: []Proxy{
			{ID: 4, Status: StatusActive, ExpiresAt: &expired}, {ID: 3, Status: "inactive"},
			{ID: 2, Status: StatusActive, Protocol: "http", Host: "two", Port: 2}, {ID: 1, Status: StatusActive, Protocol: "http", Host: "one", Port: 1},
		}}}}
	scope := newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeSetupToken})
	identity, err := gateway.ResolvePluginOutboundIdentity(context.Background(), scope, 7)
	require.NoError(t, err)
	require.NotNil(t, identity)
	require.Equal(t, []PluginOutboundEgress{{ProxyID: 1, ProxyURL: "http://one:1"}, {ProxyID: 2, ProxyURL: "http://two:2"}}, identity.Egresses)
	require.Equal(t, "http://one:1", identity.ProxyURL)
	require.Equal(t, "secret-access-token", identity.Token)
	require.Equal(t, PluginAccountIdentityRevision(&account), identity.IdentityRevision)
	groupRepo.group.ProxyIDs = nil
	resources, err := gateway.ListPluginResources(context.Background())
	require.NoError(t, err)
	require.False(t, resources.Accounts[0].BusinessEgressConfigured)
	identity, err = gateway.ResolvePluginOutboundIdentity(context.Background(), scope, 7)
	require.NoError(t, err)
	require.Empty(t, identity.Egresses)
	require.Empty(t, identity.ProxyURL)
	accountRepo.accounts[0].ProxyIPGroupID = nil
	identity, err = gateway.ResolvePluginOutboundIdentity(context.Background(), scope, 7)
	require.NoError(t, err)
	require.Empty(t, identity.Egresses)
	require.Empty(t, identity.ProxyURL)
	accountRepo.accounts[0].Status = "inactive"
	identity, err = gateway.ResolvePluginOutboundIdentity(context.Background(), scope, 7)
	require.NoError(t, err)
	require.Nil(t, identity)
	identity, err = gateway.ResolvePluginOutboundIdentity(context.Background(), scope, 999)
	require.NoError(t, err)
	require.Nil(t, identity)
}

func TestPluginHostResourcesAndLegacyOAuthBoundary(t *testing.T) {
	gateway := &OpenAIGatewayService{accountRepo: &pluginResourceAccountRepo{accounts: []Account{pluginTestAccount(1, AccountTypeOAuth), pluginTestAccount(2, AccountTypeSetupToken)}}}
	server := newPluginHostServiceServer("test.plugin", nil, gateway, newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}))
	ctx := context.Background()
	list, err := server.ListAccounts(ctx, &pluginv1.ListAccountsRequest{})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, list.AccountIds)
	identity, err := server.ResolveOutboundIdentity(ctx, &pluginv1.ResolveOutboundIdentityRequest{AccountId: 2})
	require.NoError(t, err)
	require.False(t, identity.Found)
	server.allowSetupToken = true
	server.scope = newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}, pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeSetupToken})
	list, err = server.ListAccounts(ctx, &pluginv1.ListAccountsRequest{})
	require.NoError(t, err)
	require.Equal(t, []int64{1, 2}, list.AccountIds)
	identity, err = server.ResolveOutboundIdentity(ctx, &pluginv1.ResolveOutboundIdentityRequest{AccountId: 2})
	require.NoError(t, err)
	require.True(t, identity.Found)
	require.NotEmpty(t, identity.IdentityRevision)
	resources, err := server.ListResources(ctx, &pluginv1.ListResourcesRequest{})
	require.NoError(t, err)
	require.NotContains(t, string(resources.ResourcesJson), "secret-access-token")
	require.Contains(t, string(resources.ResourcesJson), `"proxies":[]`)
	_, err = server.ListResources(ctx, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = server.ResolveProxy(ctx, &pluginv1.ResolveProxyRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	server.resources = nil
	_, err = server.ListResources(ctx, &pluginv1.ListResourcesRequest{})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestPluginResourceErrorsDoNotExposeBackendSecrets(t *testing.T) {
	secretErr := errors.New("http://user:secret-password@proxy token=secret-token")
	repo := &pluginResourceAccountRepo{err: secretErr}
	gateway := &OpenAIGatewayService{accountRepo: repo, proxyRepo: &pluginResourceProxyRepo{err: secretErr}}
	server := newPluginHostServiceServer("test.plugin", nil, gateway, newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}))
	_, err := server.ListResources(context.Background(), &pluginv1.ListResourcesRequest{})
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, err.Error(), "secret")
	_, err = server.ResolveOutboundIdentity(context.Background(), &pluginv1.ResolveOutboundIdentityRequest{AccountId: 1})
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, err.Error(), "secret")
	_, err = server.ResolveProxy(context.Background(), &pluginv1.ResolveProxyRequest{ProxyId: 1})
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, err.Error(), "secret")
}

func TestPluginHostIdentityResponseIncludesRevisionAndAllEgresses(t *testing.T) {
	directory := &fakeAccountDirectory{identity: &PluginOutboundIdentity{
		AccountID: 7, Platform: PlatformOpenAI, AccountType: AccountTypeOAuth,
		IdentityRevision: "identity-revision", Token: "internal-only-token",
		Egresses: []PluginOutboundEgress{{ProxyID: 1, ProxyURL: "http://one:1"}, {ProxyID: 2, ProxyURL: "http://two:2"}},
	}}
	server := newPluginHostServiceServer("test.plugin", nil, directory, newPluginAccountScope(pluginAccountScopeEntry{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth}))
	response, err := server.ResolveOutboundIdentity(context.Background(), &pluginv1.ResolveOutboundIdentityRequest{AccountId: 7})
	require.NoError(t, err)
	require.Equal(t, directory.identity.IdentityRevision, response.IdentityRevision)
	require.Len(t, response.Egresses, 2)
	require.Equal(t, int64(1), response.Egresses[0].ProxyId)
	require.Equal(t, "http://one:1", response.Egresses[0].ProxyUrl)
	require.Equal(t, int64(2), response.Egresses[1].ProxyId)
	require.Equal(t, "http://two:2", response.Egresses[1].ProxyUrl)
}
