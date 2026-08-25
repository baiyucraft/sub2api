//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeRegistrationEmailSuffixWhitelist(t *testing.T) {
	got, err := NormalizeRegistrationEmailSuffixWhitelist([]string{"example.com", "@EXAMPLE.COM", " @foo.bar ", "*.EDU.CN"})
	require.NoError(t, err)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

func TestNormalizeRegistrationEmailSuffixWhitelist_Invalid(t *testing.T) {
	for _, item := range []string{"@invalid_domain", "*.", "*", "*.@", "*.foo"} {
		t.Run(item, func(t *testing.T) {
			_, err := NormalizeRegistrationEmailSuffixWhitelist([]string{item})
			require.Error(t, err)
		})
	}
}

func TestParseRegistrationEmailSuffixWhitelist(t *testing.T) {
	got := ParseRegistrationEmailSuffixWhitelist(`["example.com","@foo.bar","*.EDU.CN","@invalid_domain","*.foo"]`)
	require.Equal(t, []string{"@example.com", "@foo.bar", "*.edu.cn"}, got)
}

func TestIsRegistrationEmailSuffixAllowed(t *testing.T) {
	require.True(t, IsRegistrationEmailSuffixAllowed("user@example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@example.com.", []string{"@example.com"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@sub.example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@qq.com", []string{"@qq.com"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@sub.qq.com", []string{"@qq.com"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("student@cs.edu.cn", []string{"*.edu.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("student@edu.cn", []string{"*.edu.cn"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("student@foo.cn", []string{"*.edu.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@a.com", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@school.b.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@b.cn", []string{"@a.com", "*.b.cn"}))
	require.False(t, IsRegistrationEmailSuffixAllowed("user@c.cn", []string{"@a.com", "*.b.cn"}))
	require.True(t, IsRegistrationEmailSuffixAllowed("user@any.com", []string{}))
}

func TestRegistrationEmailQuotaRejectsMalformedDomainWhenWhitelistConfigured(t *testing.T) {
	repo := &userRepoStub{}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled:                 "true",
		SettingKeyRegistrationEmailSuffixWhitelist:    `["@example.com"]`,
		SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
	}, nil, nil)

	_, _, err := svc.Register(context.Background(), "malformed-email", "password")

	require.ErrorIs(t, err, ErrEmailSuffixNotAllowed)
	require.Empty(t, repo.created)
}

func TestIsRegistrationEmailSuffixLimited(t *testing.T) {
	require.False(t, IsRegistrationEmailSuffixLimited("user@custom.example", nil))
	require.False(t, IsRegistrationEmailSuffixLimited("user@example.com", []string{"@example.com"}))
	require.True(t, IsRegistrationEmailSuffixLimited("user@custom.example", []string{"@example.com"}))
}

func TestStrictGmailRegistrationPolicy(t *testing.T) {
	t.Run("activation follows explicit gmail family whitelist", func(t *testing.T) {
		require.False(t, IsStrictGmailRegistrationEnabled(nil))
		require.False(t, IsStrictGmailRegistrationEnabled([]string{"@qq.com"}))
		require.True(t, IsStrictGmailRegistrationEnabled([]string{"@gmail.com", "@qq.com"}))
		require.True(t, IsStrictGmailRegistrationEnabled([]string{"@googlemail.com"}))
	})

	for _, email := range []string{"abc123@gmail.com", "ABC123@gmail.com", "abc123@googlemail.com", "first.last+tag@qq.com"} {
		t.Run("accept_"+email, func(t *testing.T) {
			require.True(t, IsSimpleGmailRegistrationAddress(email))
		})
	}
	for _, email := range []string{"a.b.c@gmail.com", "abc+hello@gmail.com", "abc_name@gmail.com", "abc-name@gmail.com", "a.b@googlemail.com"} {
		t.Run("reject_"+email, func(t *testing.T) {
			require.False(t, IsSimpleGmailRegistrationAddress(email))
		})
	}
}

func TestRegistrationRejectsGmailAliasesOnlyWhenStrictPolicyEnabled(t *testing.T) {
	cases := []struct {
		name      string
		whitelist string
		email     string
		wantErr   bool
	}{
		{name: "empty whitelist keeps existing behavior", whitelist: `[]`, email: "a.b+tag@gmail.com"},
		{name: "qq only keeps existing behavior", whitelist: `["@qq.com"]`, email: "a.b+tag@gmail.com"},
		{name: "plain gmail", whitelist: `["@gmail.com","@qq.com"]`, email: "abc123@gmail.com"},
		{name: "gmail dots", whitelist: `["@gmail.com","@qq.com"]`, email: "a.b.c@gmail.com", wantErr: true},
		{name: "gmail plus", whitelist: `["@gmail.com","@qq.com"]`, email: "abc+hello@gmail.com", wantErr: true},
		{name: "googlemail strict family", whitelist: `["@googlemail.com","@qq.com"]`, email: "a.b@googlemail.com", wantErr: true},
		{name: "qq punctuation unaffected", whitelist: `["@gmail.com","@qq.com"]`, email: "first.last+tag@qq.com"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userRepoStub{}
			svc := newAuthService(repo, map[string]string{
				SettingKeyRegistrationEnabled:                 "true",
				SettingKeyRegistrationEmailSuffixWhitelist:    tc.whitelist,
				SettingKeyRegistrationEmailDomainQuotaEnabled: "true",
			}, nil, nil)

			_, _, err := svc.Register(context.Background(), tc.email, "password")
			if tc.wantErr {
				require.ErrorIs(t, err, ErrGmailAliasNotAllowed)
				require.Empty(t, repo.created)
				return
			}
			require.NoError(t, err)
			require.Len(t, repo.created, 1)
		})
	}
}

func TestRegistrationGmailPolicyCoversVerificationAndOAuthCreation(t *testing.T) {
	settings := map[string]string{
		SettingKeyRegistrationEnabled:              "true",
		SettingKeyRegistrationEmailSuffixWhitelist: `["@gmail.com","@qq.com"]`,
	}
	svc := newAuthService(&userRepoStub{}, settings, nil, nil)

	_, err := svc.SendVerifyCodeAsync(context.Background(), "a.b@gmail.com")
	require.ErrorIs(t, err, ErrGmailAliasNotAllowed)

	_, _, err = svc.LoginOrRegisterOAuth(context.Background(), "a.b@gmail.com", "alias")
	require.ErrorIs(t, err, ErrGmailAliasNotAllowed)

	_, err = svc.createEmailOAuthUser(context.Background(), "a.b@gmail.com", "alias", "google", "", "")
	require.ErrorIs(t, err, ErrGmailAliasNotAllowed)
}

func TestStrictGmailPolicyDoesNotBlockExistingOAuthLogin(t *testing.T) {
	existing := &User{ID: 42, Email: "a.b@gmail.com", Role: RoleUser, Status: StatusActive}
	repo := &userRepoStub{usersByEmail: map[string]*User{existing.Email: existing}}
	svc := newAuthService(repo, map[string]string{
		SettingKeyRegistrationEnabled:              "true",
		SettingKeyRegistrationEmailSuffixWhitelist: `["@gmail.com","@qq.com"]`,
	}, nil, nil)

	_, user, err := svc.LoginOrRegisterOAuth(context.Background(), existing.Email, "existing")
	require.NoError(t, err)
	require.Equal(t, existing.ID, user.ID)
}

func TestRegistrationEmailDomainUsesRegistrableDomain(t *testing.T) {
	require.Equal(t, "abc.com", RegistrationEmailDomain("user@abc.com"))
	require.Equal(t, "abc.com", RegistrationEmailDomain("user@abcd.abc.com"))
	require.Equal(t, "example.co.uk", RegistrationEmailDomain("user@team.example.co.uk"))
	require.Equal(t, "example.com", RegistrationEmailDomain("user@example.com."))
	require.Equal(t, "example.com", RegistrationEmailDomain("user@team.example.com."))
}
