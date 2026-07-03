package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type adminSub2APIRateSyncAccountRepo struct {
	AccountRepository
	nextID   int64
	accounts map[int64]*Account
}

func newAdminSub2APIRateSyncAccountRepo(accounts ...*Account) *adminSub2APIRateSyncAccountRepo {
	repo := &adminSub2APIRateSyncAccountRepo{
		accounts: make(map[int64]*Account),
	}
	for _, account := range accounts {
		cp := cloneAccountForSub2APIRateSync(account)
		repo.accounts[cp.ID] = cp
		if cp.ID > repo.nextID {
			repo.nextID = cp.ID
		}
	}
	return repo
}

func (r *adminSub2APIRateSyncAccountRepo) Create(_ context.Context, account *Account) error {
	if r.accounts == nil {
		r.accounts = make(map[int64]*Account)
	}
	r.nextID++
	account.ID = r.nextID
	r.accounts[account.ID] = cloneAccountForSub2APIRateSync(account)
	return nil
}

func (r *adminSub2APIRateSyncAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	account, ok := r.accounts[id]
	if !ok {
		return nil, ErrAccountNotFound
	}
	return cloneAccountForSub2APIRateSync(account), nil
}

func (r *adminSub2APIRateSyncAccountRepo) Update(_ context.Context, account *Account) error {
	if _, ok := r.accounts[account.ID]; !ok {
		return ErrAccountNotFound
	}
	r.accounts[account.ID] = cloneAccountForSub2APIRateSync(account)
	return nil
}

func (r *adminSub2APIRateSyncAccountRepo) BindGroups(_ context.Context, _ int64, _ []int64) error {
	return nil
}

func (r *adminSub2APIRateSyncAccountRepo) ListShadowsByParent(_ context.Context, _ int64) ([]*Account, error) {
	return nil, nil
}

type adminSub2APIRateSyncTrigger struct {
	calls chan *Account
	err   error
}

func (t *adminSub2APIRateSyncTrigger) SyncAccountNow(ctx context.Context, account *Account) error {
	if t.calls != nil {
		select {
		case t.calls <- cloneAccountForSub2APIRateSync(account):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return t.err
}

func requireSub2APIRateSyncCall(t *testing.T, calls <-chan *Account) *Account {
	t.Helper()
	select {
	case account := <-calls:
		return account
	case <-time.After(time.Second):
		t.Fatal("expected Sub2API rate sync trigger")
		return nil
	}
}

func requireNoSub2APIRateSyncCall(t *testing.T, calls <-chan *Account) {
	t.Helper()
	select {
	case account := <-calls:
		t.Fatalf("unexpected Sub2API rate sync trigger for account %d", account.ID)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAdminServiceCreateAccountTriggersSub2APIRateSync(t *testing.T) {
	calls := make(chan *Account, 1)
	svc := &adminServiceImpl{
		accountRepo:     newAdminSub2APIRateSyncAccountRepo(),
		sub2APIRateSync: &adminSub2APIRateSyncTrigger{calls: calls},
	}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "sub2api-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          sub2APITestCredentials(),
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.NotZero(t, account.ID)
	triggered := requireSub2APIRateSyncCall(t, calls)
	require.Equal(t, account.ID, triggered.ID)
	require.True(t, triggered.IsSub2APIUpstream())
}

func TestAdminServiceUpdateAccountTriggersSub2APIRateSync(t *testing.T) {
	accountID := int64(77)
	repo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:          accountID,
		Name:        "upstream",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: sub2APITestCredentials(),
		Extra:       map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderOther},
	})
	calls := make(chan *Account, 1)
	svc := &adminServiceImpl{
		accountRepo:     repo,
		sub2APIRateSync: &adminSub2APIRateSyncTrigger{calls: calls},
	}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
	})

	require.NoError(t, err)
	require.True(t, updated.IsSub2APIUpstream())
	triggered := requireSub2APIRateSyncCall(t, calls)
	require.Equal(t, accountID, triggered.ID)
	require.True(t, triggered.IsSub2APIUpstream())
}

func TestAdminServiceSub2APIRateSyncSkipsOtherProviders(t *testing.T) {
	calls := make(chan *Account, 1)
	svc := &adminServiceImpl{
		accountRepo:     newAdminSub2APIRateSyncAccountRepo(),
		sub2APIRateSync: &adminSub2APIRateSyncTrigger{calls: calls},
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "newapi-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"base_url": "https://upstream.example/v1", "api_key": "sk-upstream"},
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderNewAPI},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	requireNoSub2APIRateSyncCall(t, calls)
}

func TestAdminServiceSub2APIRateSyncFailureDoesNotBlockSave(t *testing.T) {
	calls := make(chan *Account, 1)
	svc := &adminServiceImpl{
		accountRepo: newAdminSub2APIRateSyncAccountRepo(),
		sub2APIRateSync: &adminSub2APIRateSyncTrigger{
			calls: calls,
			err:   errors.New("upstream unavailable"),
		},
	}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "sub2api-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          sub2APITestCredentials(),
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.NotNil(t, account)
	requireSub2APIRateSyncCall(t, calls)
}

func TestAdminServiceSub2APIRequiresLoginCredentials(t *testing.T) {
	svc := &adminServiceImpl{
		accountRepo: newAdminSub2APIRateSyncAccountRepo(),
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "sub2api-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"base_url": "https://upstream.example/v1", "api_key": "sk-upstream", AccountCredentialSub2APILoginEmail: "user@example.com"},
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
		SkipDefaultGroupBind: true,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "password")
}

func TestAdminServiceSub2APIManualJWTRequiresTokenButNotLoginCredentials(t *testing.T) {
	svc := &adminServiceImpl{
		accountRepo: newAdminSub2APIRateSyncAccountRepo(),
	}

	_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "sub2api-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"base_url": "https://upstream.example/v1", "api_key": "sk-upstream"},
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API, AccountSub2APIRateSyncAdapterKey: AccountSub2APIRateSyncAdapterManualJWT},
		SkipDefaultGroupBind: true,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "access token")

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "sub2api-upstream",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"base_url": "https://upstream.example/v1", "api_key": "sk-upstream", AccountCredentialSub2APIAccessToken: "jwt-secret"},
		Extra:                map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API, AccountSub2APIRateSyncAdapterKey: AccountSub2APIRateSyncAdapterManualJWT},
		SkipDefaultGroupBind: true,
	})

	require.NoError(t, err)
	require.Equal(t, AccountSub2APIRateSyncAdapterManualJWT, account.Sub2APIRateSyncAdapter())
	require.Equal(t, "jwt-secret", account.GetCredential(AccountCredentialSub2APIAccessToken))
	require.Empty(t, account.GetCredential(AccountCredentialSub2APILoginEmail))
	require.Empty(t, account.GetCredential(AccountCredentialSub2APILoginPassword))
}

func TestAdminServiceSub2APIManualJWTEditKeepsSavedToken(t *testing.T) {
	accountID := int64(89)
	repo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:       accountID,
		Name:     "upstream",
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"base_url":                          "https://upstream.example/v1",
			"api_key":                           "sk-upstream",
			AccountCredentialSub2APIAccessToken: "jwt-secret",
		},
		Extra: map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API, AccountSub2APIRateSyncAdapterKey: AccountSub2APIRateSyncAdapterManualJWT},
	})
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{"base_url": "https://upstream.example/api/v1"},
		Extra:       map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API, AccountSub2APIRateSyncAdapterKey: AccountSub2APIRateSyncAdapterManualJWT},
	})

	require.NoError(t, err)
	require.Equal(t, "jwt-secret", updated.GetCredential(AccountCredentialSub2APIAccessToken))
	require.Equal(t, AccountSub2APIRateSyncAdapterManualJWT, updated.Sub2APIRateSyncAdapter())
}

func TestAdminServiceSwitchingAwayFromSub2APIClearsLoginCredentials(t *testing.T) {
	accountID := int64(88)
	repo := newAdminSub2APIRateSyncAccountRepo(&Account{
		ID:          accountID,
		Name:        "upstream",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: sub2APITestCredentials(),
		Extra:       map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderSub2API},
	})
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Extra: map[string]any{AccountUpstreamProviderKey: AccountUpstreamProviderOther},
	})

	require.NoError(t, err)
	require.Equal(t, AccountUpstreamProviderOther, updated.UpstreamProvider())
	require.Empty(t, updated.GetCredential(AccountCredentialSub2APILoginEmail))
	require.Empty(t, updated.GetCredential(AccountCredentialSub2APILoginPassword))
	require.Empty(t, updated.GetCredential(AccountCredentialSub2APIAccessToken))
}

func sub2APITestCredentials() map[string]any {
	return map[string]any{
		"base_url":                            "https://upstream.example/v1",
		"api_key":                             "sk-upstream",
		AccountCredentialSub2APILoginEmail:    "user@example.com",
		AccountCredentialSub2APILoginPassword: "secret-password",
	}
}
