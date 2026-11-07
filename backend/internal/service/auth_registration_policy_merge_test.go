//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestAuthService_OAuthCreationPreservesRegistrationEmailPolicy(t *testing.T) {
	paths := []struct {
		name   string
		create func(*AuthService, string) (*User, error)
	}{
		{"legacy", func(s *AuthService, email string) (*User, error) {
			_, user, err := s.LoginOrRegisterOAuth(context.Background(), email, "new-user")
			return user, err
		}},
		{"token_pair", func(s *AuthService, email string) (*User, error) {
			_, user, err := s.LoginOrRegisterOAuthWithTokenPair(context.Background(), email, "new-user", "", "", "oidc")
			return user, err
		}},
		{"verified_email_create", func(s *AuthService, email string) (*User, error) {
			return s.createEmailOAuthUser(context.Background(), email, "new-user", "github", "", "")
		}},
	}
	cases := []struct {
		name             string
		email            string
		whitelist        string
		quotaEnabled     string
		blacklist        string
		domainCount      int
		domainLimitErr   error
		wantErr          error
		wantDomainCreate int
	}{
		{name: "first_non_whitelist_account", wantDomainCreate: 1},
		{name: "second_non_whitelist_account", domainCount: 1, wantErr: ErrEmailDomainRegistrationLimit},
		{name: "quota_disabled", quotaEnabled: "false", wantErr: ErrEmailSuffixNotAllowed},
		{name: "blacklist_before_quota", blacklist: `["new@custom.example"]`, wantErr: ErrEmailBlocked},
		{name: "atomic_domain_limit_race", domainLimitErr: ErrEmailDomainRegistrationLimit, wantErr: ErrEmailDomainRegistrationLimit, wantDomainCreate: 1},
		{name: "gmail_alias", email: "n.ew@gmail.com", whitelist: `["@gmail.com"]`, wantErr: ErrGmailAliasNotAllowed},
	}
	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					email, whitelist, quotaEnabled := tc.email, tc.whitelist, tc.quotaEnabled
					if email == "" {
						email = "new@custom.example"
					}
					if whitelist == "" {
						whitelist = `["@example.com"]`
					}
					if quotaEnabled == "" {
						quotaEnabled = "true"
					}
					repo := &userRepoStub{
						nextID:         91,
						domainCounts:   map[string]int{"custom.example": tc.domainCount},
						domainLimitErr: tc.domainLimitErr,
					}
					svc := newAuthService(repo, map[string]string{
						SettingKeyRegistrationEnabled:                 "true",
						SettingKeyRegistrationEmailSuffixWhitelist:    whitelist,
						SettingKeyRegistrationEmailDomainQuotaEnabled: quotaEnabled,
						SettingKeyRegistrationEmailBlacklist:          tc.blacklist,
					}, nil, nil)
					svc.refreshTokenCache = &refreshTokenCacheStub{}
					user, err := path.create(svc, email)
					if tc.wantErr != nil {
						require.ErrorIs(t, err, tc.wantErr)
						require.Nil(t, user)
						require.Empty(t, repo.created)
					} else {
						require.NoError(t, err)
						require.Equal(t, int64(91), user.ID)
						require.Len(t, repo.created, 1)
						require.Equal(t, 1, repo.guardedCreates)
					}
					require.Equal(t, tc.wantDomainCreate, repo.domainLimitedCreates)
				})
			}
		})
	}
}

func TestAuthService_OAuthTokenPairTransactionPreservesPolicyErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"domain_limit", ErrEmailDomainRegistrationLimit},
		{"suffix_not_allowed", ErrEmailSuffixNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			mock.ExpectBegin()
			mock.ExpectRollback()

			guardErr := fmt.Errorf("atomic email guard: %w", tc.err)
			repo := &userRepoStub{
				domainCounts:   map[string]int{"custom.example": 0},
				domainLimitErr: guardErr,
			}
			redeemRepo := &redeemCodeRepoStub{codesByCode: map[string]*RedeemCode{
				"transaction-invite": {ID: 17, Code: "transaction-invite", Type: RedeemTypeInvitation, Status: StatusUnused},
			}}
			svc := newAuthService(repo, map[string]string{
				SettingKeyRegistrationEnabled:                 "true",
				SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
				SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
				SettingKeyInvitationCodeEnabled:               "true",
			}, nil, nil)
			svc.entClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			svc.redeemRepo = redeemRepo
			svc.refreshTokenCache = &refreshTokenCacheStub{}

			tokens, user, err := svc.LoginOrRegisterOAuthWithTokenPair(context.Background(), "new@custom.example", "new-user", "transaction-invite", "", "oidc")
			require.ErrorIs(t, err, tc.err)
			require.Same(t, guardErr, err, "transactional creation must return the original guard error")
			require.Nil(t, tokens)
			require.Nil(t, user)
			require.Equal(t, 1, repo.domainLimitedCreates)
			require.Empty(t, repo.created)
			require.Empty(t, redeemRepo.useCalls, "failed creation must not consume the invitation")
			require.Equal(t, StatusUnused, redeemRepo.codesByCode["transaction-invite"].Status)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAuthService_EmailBindingPolicyRemainsStrictWithDomainQuota(t *testing.T) {
	svc := newAuthService(&userRepoStub{}, map[string]string{
		SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil, nil)

	require.ErrorIs(t, svc.validateRegistrationEmailPolicy(context.Background(), "bind@custom.example"), ErrEmailSuffixNotAllowed)
	require.NoError(t, svc.validateNewRegistrationEmailPolicy(context.Background(), "first@custom.example"))
}
