package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type routingSettingRepo struct{ values map[string]string }

func (r *routingSettingRepo) Get(context.Context, string) (*Setting, error) { return nil, nil }
func (r *routingSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	return r.values[key], nil
}
func (r *routingSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}
func (r *routingSettingRepo) GetMultiple(_ context.Context, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		out[key] = r.values[key]
	}
	return out, nil
}
func (r *routingSettingRepo) SetMultiple(_ context.Context, values map[string]string) error {
	for k, v := range values {
		r.values[k] = v
	}
	return nil
}
func (r *routingSettingRepo) GetAll(context.Context) (map[string]string, error) { return r.values, nil }
func (r *routingSettingRepo) Delete(_ context.Context, key string) error {
	delete(r.values, key)
	return nil
}

func TestNormalizeSMTPRecipientDomains(t *testing.T) {
	got := normalizeSMTPRecipientDomains([]string{" QQ.COM ", "*.Example.com", "bad value", "*.*.com", ""})
	require.Equal(t, []string{"qq.com", "*.example.com"}, got)
	require.True(t, smtpRecipientDomainMatches("User@QQ.com", got))
	require.True(t, smtpRecipientDomainMatches("a@sub.example.com", got))
	require.False(t, smtpRecipientDomainMatches("example.com", got))
}

func TestSMTPRoutingSettingsUseSafeDefaultsAndConfiguredPasswordState(t *testing.T) {
	repo := &routingSettingRepo{values: map[string]string{SettingKeySMTPRecipientRoutingDomains: `["QQ.COM", "bad value"]`, SettingKeySMTPQQPassword: "secret", SettingKeySMTPQQHost: "smtp.qq.com"}}
	svc := NewEmailService(repo, nil)
	routing, err := svc.GetSMTPRoutingConfig(context.Background())
	require.NoError(t, err)
	require.False(t, routing.Enabled)
	require.Equal(t, []string{"qq.com"}, routing.Domains)
	require.Equal(t, 465, routing.QQ.Port)
	require.Equal(t, "secret", routing.QQ.Password)
}

func TestEmailServiceSelectsQQProfileForMatchedRecipient(t *testing.T) {
	repo := &routingSettingRepo{values: map[string]string{
		SettingKeySMTPHost: "primary.test", SettingKeySMTPPort: "587", SettingKeySMTPFrom: "noreply@example.com",
		SettingKeySMTPRecipientRoutingEnabled: "true", SettingKeySMTPRecipientRoutingDomains: `["qq.com"]`,
		SettingKeySMTPQQHost: "smtp.qq.com", SettingKeySMTPQQPort: "465", SettingKeySMTPQQUsername: "user@qq.com",
		SettingKeySMTPQQPassword: "auth-code", SettingKeySMTPQQFrom: "user@qq.com", SettingKeySMTPQQFromName: "QQ",
		SettingKeySMTPQQUseTLS: "true",
	}}
	svc := NewEmailService(repo, nil)
	var selected *SMTPConfig
	svc.sendEmailWithConfigFn = func(config *SMTPConfig, _, _, _ string) error { selected = config; return nil }
	require.NoError(t, svc.SendEmail(context.Background(), "person@qq.com", "subject", "body"))
	require.Equal(t, "smtp.qq.com", selected.Host)
	require.Equal(t, "user@qq.com", selected.From)
}

func TestEmailServiceFallsBackBeforeDataButNotAfterData(t *testing.T) {
	repo := &routingSettingRepo{values: map[string]string{
		SettingKeySMTPHost: "primary.test", SettingKeySMTPPort: "587", SettingKeySMTPFrom: "noreply@example.com",
		SettingKeySMTPRecipientRoutingEnabled: "true", SettingKeySMTPRecipientRoutingDomains: `["qq.com"]`,
		SettingKeySMTPQQHost: "qq.test", SettingKeySMTPQQPort: "465", SettingKeySMTPQQUsername: "u", SettingKeySMTPQQPassword: "p", SettingKeySMTPQQFrom: "u@qq.com",
	}}
	svc := NewEmailService(repo, nil)
	var calls []string
	svc.sendEmailWithConfigFn = func(config *SMTPConfig, _, _, _ string) error {
		calls = append(calls, config.Host)
		if len(calls) == 1 {
			return &smtpSendError{phase: smtpPhaseAuth, err: errors.New("auth")}
		}
		return nil
	}
	require.NoError(t, svc.SendEmail(context.Background(), "person@qq.com", "subject", "body"))
	require.Equal(t, []string{"qq.test", "primary.test"}, calls)

	calls = nil
	svc.sendEmailWithConfigFn = func(config *SMTPConfig, _, _, _ string) error {
		calls = append(calls, config.Host)
		return &smtpSendError{phase: smtpPhaseData, err: errors.New("uncertain")}
	}
	err := svc.SendEmail(context.Background(), "person@qq.com", "subject", "body")
	require.Error(t, err)
	require.Equal(t, []string{"qq.test"}, calls)
}
