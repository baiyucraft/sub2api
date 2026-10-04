//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/typesafe"
	"github.com/stretchr/testify/require"
)

func TestAdminTypeSafeMergeRejectsNonAPIKeyCredentialsBeforeWrite(t *testing.T) {
	for _, accountType := range []string{"", AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run("create/"+accountType, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
				Name: "TypeSafe", Platform: PlatformTypeSafe, Type: accountType,
				SkipDefaultGroupBind: true,
			})
			require.EqualError(t, err, "typesafe accounts only support apikey credentials")
			require.Nil(t, repo.createAccount)
		})
	}
	for _, accountType := range []string{AccountTypeOAuth, AccountTypeSetupToken} {
		t.Run("update/"+accountType, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
				7: {ID: 7, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey},
			}}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Type: accountType})
			require.EqualError(t, err, "typesafe accounts only support apikey credentials")
			require.Empty(t, repo.updatedAccounts)
			require.Equal(t, AccountTypeAPIKey, repo.getByIDAccounts[7].Type)
		})
	}
}

func TestAdminTypeSafeMergeCreatePreservesPreferredGroupsAndStripsPrivateState(t *testing.T) {
	repo := &accountRepoStubForBulkUpdate{createID: 7}
	groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{
		10: {ID: 10, Platform: PlatformTypeSafe},
		20: {ID: 20, Platform: PlatformComposite},
	}}
	svc := &adminServiceImpl{accountRepo: repo, accountDuplicateRepo: repo, groupRepo: groups}
	preferred := []int64{20}
	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name: "TypeSafe", Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"},
		GroupIDs:    []int64{10, 20}, PreferredGroupIDs: &preferred,
		SkipDefaultGroupBind: true, SkipMixedChannelCheck: true,
		Extra: map[string]any{
			"codex_turn_ticket:custom":   "forged",
			UpstreamBillingProbeExtraKey: "forged",
			"custom":                     true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, []int64{10, 20}, repo.bindGroupsByAccount[account.ID])
	require.Equal(t, []int64{20}, repo.preferredByAccount[account.ID])
	require.Equal(t, 1, repo.atomicCreateCalls)
	require.NotContains(t, account.Extra, "codex_turn_ticket:custom")
	require.NotContains(t, account.Extra, UpstreamBillingProbeExtraKey)
	require.Equal(t, true, account.Extra["custom"])
}

func TestAdminTypeSafeMergeUpdatePreservesCredentialsAndPrivateState(t *testing.T) {
	for _, accountType := range []string{"", AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
				7: {
					ID: 7, Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
					Credentials: map[string]any{"api_key": "test-existing-key"},
					Extra:       map[string]any{"codex_turn_ticket:custom": "private-state"},
				},
			}}
			svc := &adminServiceImpl{accountRepo: repo}
			updated, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{
				Type: accountType, Credentials: map[string]any{"model_mapping": map[string]any{"jev-public": typesafe.JevLatestModel}},
				Extra: map[string]any{"codex_turn_ticket:custom": "forged", "custom": true},
			})
			require.NoError(t, err)
			require.Len(t, repo.updatedAccounts, 1)
			require.Equal(t, "test-existing-key", updated.Credentials["api_key"])
			require.Equal(t, "private-state", updated.Extra["codex_turn_ticket:custom"])
			require.Equal(t, true, updated.Extra["custom"])
		})
	}
}

func TestAdminTypeSafeMergeKeepsUpstreamIdentityReadOnly(t *testing.T) {
	configID, keyID := int64(1), int64(2)
	for _, accountType := range []string{"", AccountTypeAPIKey} {
		t.Run(accountType, func(t *testing.T) {
			repo := &accountRepoStubForBulkUpdate{getByIDAccounts: map[int64]*Account{
				7: {
					ID: 7, Name: "derived", Platform: PlatformTypeSafe, Type: AccountTypeAPIKey,
					UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
				},
			}}
			svc := &adminServiceImpl{accountRepo: repo}
			_, err := svc.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Name: "renamed", Type: accountType})
			requireApplicationErrorReason(t, err, "UPSTREAM_ACCOUNT_DERIVED_FIELDS_READ_ONLY")
			require.Empty(t, repo.updatedAccounts)
			require.Equal(t, "derived", repo.getByIDAccounts[7].Name)
		})
	}
}

func TestAdminTypeSafeMergeProxyGroupsRemainOpenAIOAuthOnly(t *testing.T) {
	ctx := context.Background()
	client := newAdminProxyBindingTestClient(t)
	binding, err := client.ProxyBinding.Create().SetBindingType(proxyBindingTypeProxyIPGroup).SetProxyIPGroupID(12).Save(ctx)
	require.NoError(t, err)
	groups := newProxyIPGroupRepoStub()
	groups.groups[12] = ProxyIPGroup{ID: 12, BindingID: binding.ID}
	svc := &adminServiceImpl{entClient: client, proxyIPGroupRepo: groups}

	for _, tc := range []struct {
		platform string
		typ      string
		allowed  bool
	}{
		{PlatformOpenAI, AccountTypeOAuth, true},
		{PlatformOpenAI, AccountTypeSetupToken, true},
		{PlatformOpenAI, AccountTypeAPIKey, false},
		{PlatformTypeSafe, AccountTypeAPIKey, false},
		{PlatformAnthropic, AccountTypeOAuth, false},
	} {
		t.Run(tc.platform+"/"+tc.typ, func(t *testing.T) {
			account := &Account{Platform: tc.platform, Type: tc.typ, ProxyID: &binding.ID}
			err := svc.validateOpenAIProxyGroupBinding(ctx, account)
			if !tc.allowed {
				requireApplicationErrorReason(t, err, "PROXY_IP_GROUP_ACCOUNT_TYPE_UNSUPPORTED")
				return
			}
			require.NoError(t, err)
			require.Equal(t, binding.ID, *account.ProxyID)
			require.Equal(t, int64(12), *account.ProxyIPGroupID)
		})
	}
}

func TestAdminTypeSafeMergeCompositeCandidatesRequireAccountMapping(t *testing.T) {
	require.Equal(t, []string{typesafe.JevLatestModel}, defaultModelsListCandidateIDs(PlatformTypeSafe))
	require.NotContains(t, defaultModelsListCandidateIDs(PlatformComposite), typesafe.JevLatestModel)

	repo := &accountRepoStubForCompositeModelsList{accounts: []Account{
		{Platform: PlatformTypeSafe, Type: AccountTypeAPIKey, Credentials: map[string]any{
			"model_mapping": map[string]any{typesafe.JevLatestModel: typesafe.JevLatestModel, "jev-public": typesafe.JevLatestModel},
		}},
	}}
	groups := &groupRepoStubForAdmin{getByIDByID: map[int64]*Group{99: {ID: 99, Platform: PlatformComposite}}}
	svc := &adminServiceImpl{accountRepo: repo, groupRepo: groups}
	candidates, err := svc.GetGroupModelsListCandidates(context.Background(), 99, "")
	require.NoError(t, err)
	require.Contains(t, candidates, typesafe.JevLatestModel)
	require.Contains(t, candidates, "jev-public")
	require.NotContains(t, defaultModelsListCandidateIDs(PlatformComposite), typesafe.JevLatestModel)

	repo.accounts = nil
	candidates, err = svc.GetGroupModelsListCandidates(context.Background(), 99, "")
	require.NoError(t, err)
	require.NotContains(t, candidates, typesafe.JevLatestModel)
}

func TestAdminTypeSafeMergeCompositeRoutesAcceptTypeSafe(t *testing.T) {
	route, err := compositeRouteFromInput(99, CompositeRouteInput{
		PublicModel: "jev-public", TargetPlatform: PlatformTypeSafe, UpstreamModel: typesafe.JevLatestModel,
	})
	require.NoError(t, err)
	require.Equal(t, PlatformTypeSafe, route.TargetPlatform)
	for _, model := range []string{"jev-latest", "jev-next", "typesafe/jev-latest", "jev/jev-latest"} {
		platform, ok := DetectModelPlatform(model)
		require.True(t, ok, model)
		require.Equal(t, PlatformTypeSafe, platform)
	}
}
