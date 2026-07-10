package service

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	upstreamAccountNameMaxRunes     = 100
	upstreamAccountConfigNameBudget = 49
	upstreamAccountKeyNameBudget    = 50
	upstreamAccountNameSeparator    = "-"
)

func trimUpstreamNameWhitespace(value string) string {
	return strings.TrimFunc(value, func(r rune) bool {
		switch {
		case r >= '\u0009' && r <= '\u000d':
			return true
		case r == '\u0020', r == '\u0085', r == '\u00a0', r == '\u1680',
			r == '\u2028', r == '\u2029', r == '\u202f', r == '\u205f', r == '\u3000':
			return true
		case r >= '\u2000' && r <= '\u200a':
			return true
		default:
			return false
		}
	})
}

func BuildUpstreamAccountName(configName, keyName string) (string, error) {
	configName = trimUpstreamNameWhitespace(configName)
	if configName == "" {
		return "", infraerrors.BadRequest("UPSTREAM_CONFIG_NAME_REQUIRED", "upstream config name is required")
	}
	keyName = trimUpstreamNameWhitespace(keyName)
	if keyName == "" {
		return "", infraerrors.BadRequest("UPSTREAM_KEY_NAME_REQUIRED", "upstream key name is required")
	}

	configRunes := []rune(configName)
	keyRunes := []rune(keyName)
	if len(configRunes)+len(keyRunes)+len([]rune(upstreamAccountNameSeparator)) <= upstreamAccountNameMaxRunes {
		return configName + upstreamAccountNameSeparator + keyName, nil
	}

	configBudget := upstreamAccountConfigNameBudget
	keyBudget := upstreamAccountKeyNameBudget
	if len(configRunes) < configBudget {
		keyBudget += configBudget - len(configRunes)
		configBudget = len(configRunes)
	}
	if len(keyRunes) < keyBudget {
		configBudget += keyBudget - len(keyRunes)
		keyBudget = len(keyRunes)
	}

	return string(configRunes[:configBudget]) + upstreamAccountNameSeparator + string(keyRunes[:keyBudget]), nil
}

func buildUpstreamAccountName(configName, keyName string) (string, error) {
	return BuildUpstreamAccountName(configName, keyName)
}

func (s *adminServiceImpl) scheduleSub2APIUpstreamRateSync(account *Account) {
	if s == nil || s.sub2APIRateSync == nil || account == nil || !account.IsSub2APIUpstream() {
		return
	}
	accountCopy := cloneAccountForSub2APIRateSync(account)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("sub2api_upstream_rate_sync_immediate_panic", "account_id", accountCopy.ID, "recover", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), sub2APIUpstreamImmediateSyncTimeout)
		defer cancel()
		if err := s.sub2APIRateSync.SyncAccountNow(ctx, accountCopy); err != nil {
			slog.Warn("sub2api_upstream_rate_sync_immediate_failed", "account_id", accountCopy.ID, "error", err)
		}
	}()
}

func cloneAccountForSub2APIRateSync(account *Account) *Account {
	if account == nil {
		return nil
	}
	cp := *account
	if account.Credentials != nil {
		cp.Credentials = make(map[string]any, len(account.Credentials))
		for k, v := range account.Credentials {
			cp.Credentials[k] = v
		}
	}
	if account.Extra != nil {
		cp.Extra = make(map[string]any, len(account.Extra))
		for k, v := range account.Extra {
			cp.Extra[k] = v
		}
	}
	return &cp
}

func (s *adminServiceImpl) normalizeUpstreamAccountInput(ctx context.Context, input *CreateAccountInput) error {
	if input == nil {
		return nil
	}
	if input.Type != AccountTypeUpstream && (input.UpstreamConfigID == nil || *input.UpstreamConfigID <= 0) {
		return nil
	}
	if input.UpstreamConfigID == nil || *input.UpstreamConfigID <= 0 || input.UpstreamKeyID == nil || *input.UpstreamKeyID <= 0 {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_ACCOUNT_BINDING_REQUIRED", "upstream config and key are required")
	}
	cfg, key, err := s.validateUpstreamAccountBinding(ctx, *input.UpstreamConfigID, *input.UpstreamKeyID)
	if err != nil {
		return err
	}
	autoName, err := buildUpstreamAccountName(cfg.Name, key.Name)
	if err != nil {
		return err
	}
	input.Name = autoName
	input.Type = AccountTypeAPIKey
	if strings.TrimSpace(input.Platform) == "" {
		input.Platform = key.Platform
	} else if strings.TrimSpace(key.Platform) != "" && !strings.EqualFold(strings.TrimSpace(input.Platform), strings.TrimSpace(key.Platform)) {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_KEY_PLATFORM_MISMATCH", "upstream key platform does not match account platform")
	}
	if input.Credentials == nil {
		input.Credentials = map[string]any{}
	}
	if input.Extra == nil {
		input.Extra = map[string]any{}
	}
	input.Extra[AccountUpstreamProviderKey] = cfg.Provider
	input.Extra[AccountSub2APIRateSyncAdapterKey] = cfg.AuthMode
	input.ProxyID = nil
	return nil
}

func (s *adminServiceImpl) normalizeUpstreamAccountUpdate(ctx context.Context, account *Account, input *UpdateAccountInput) error {
	if input == nil || account == nil {
		return nil
	}
	clearingBinding := input.Type != AccountTypeUpstream &&
		input.UpstreamConfigID != nil && *input.UpstreamConfigID <= 0 &&
		input.UpstreamKeyID != nil && *input.UpstreamKeyID <= 0
	if input.Type == AccountTypeUpstream {
		account.Type = AccountTypeAPIKey
	}
	cfgID := account.UpstreamConfigID
	keyID := account.UpstreamKeyID
	if input.UpstreamConfigID != nil {
		if *input.UpstreamConfigID <= 0 {
			cfgID = nil
		} else {
			cfgID = input.UpstreamConfigID
		}
	}
	if input.UpstreamKeyID != nil {
		if *input.UpstreamKeyID <= 0 {
			keyID = nil
		} else {
			keyID = input.UpstreamKeyID
		}
	}
	if cfgID == nil && keyID == nil {
		if clearingBinding {
			clearUpstreamAccountBinding(account, input)
			return nil
		}
		if input.Type == AccountTypeUpstream || input.UpstreamConfigID != nil || input.UpstreamKeyID != nil {
			return infraerrors.New(http.StatusBadRequest, "UPSTREAM_ACCOUNT_BINDING_REQUIRED", "upstream config and key are required")
		}
		return nil
	}
	if cfgID == nil || keyID == nil {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_ACCOUNT_BINDING_REQUIRED", "upstream config and key are required")
	}
	cfg, key, err := s.validateUpstreamAccountBinding(ctx, *cfgID, *keyID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(key.Platform) != "" && strings.TrimSpace(account.Platform) != "" && !strings.EqualFold(strings.TrimSpace(account.Platform), strings.TrimSpace(key.Platform)) {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_KEY_PLATFORM_MISMATCH", "upstream key platform does not match account platform")
	}
	autoName, err := buildUpstreamAccountName(cfg.Name, key.Name)
	if err != nil {
		return err
	}
	account.Name = autoName
	if input.Extra == nil {
		if account.Extra == nil {
			account.Extra = map[string]any{}
		}
		account.Extra[AccountUpstreamProviderKey] = cfg.Provider
		account.Extra[AccountSub2APIRateSyncAdapterKey] = cfg.AuthMode
	} else {
		input.Extra[AccountUpstreamProviderKey] = cfg.Provider
		input.Extra[AccountSub2APIRateSyncAdapterKey] = cfg.AuthMode
	}
	account.ProxyID = nil
	account.Proxy = nil
	input.ProxyID = nil
	return nil
}

func clearUpstreamAccountBinding(account *Account, input *UpdateAccountInput) {
	if account != nil && account.Extra != nil {
		delete(account.Extra, AccountUpstreamProviderKey)
		delete(account.Extra, AccountSub2APIRateSyncAdapterKey)
	}
	if input != nil && input.Extra != nil {
		delete(input.Extra, AccountUpstreamProviderKey)
		delete(input.Extra, AccountSub2APIRateSyncAdapterKey)
	}
}

func (s *adminServiceImpl) validateUpstreamAccountBinding(ctx context.Context, configID, keyID int64) (*UpstreamConfig, *UpstreamKey, error) {
	if s == nil || s.upstreamConfigRepo == nil {
		return nil, nil, infraerrors.New(http.StatusServiceUnavailable, "UPSTREAM_CONFIG_UNAVAILABLE", "upstream config service is unavailable")
	}
	cfg, err := s.upstreamConfigRepo.GetByID(ctx, configID)
	if err != nil {
		return nil, nil, err
	}
	key, err := s.upstreamConfigRepo.GetKeyByID(ctx, keyID)
	if err != nil {
		return nil, nil, err
	}
	if cfg == nil || key == nil || key.UpstreamConfigID != cfg.ID {
		return nil, nil, infraerrors.New(http.StatusBadRequest, "UPSTREAM_KEY_CONFIG_MISMATCH", "upstream key does not belong to the selected config")
	}
	return cfg, key, nil
}

func validateAndNormalizeSub2APIUpstreamCredentials(account *Account) error {
	if account == nil {
		return nil
	}
	if account.Type != AccountTypeAPIKey || account.UpstreamProvider() != AccountUpstreamProviderSub2API {
		if account.Extra != nil {
			delete(account.Extra, AccountSub2APIRateSyncAdapterKey)
		}
		if account.Credentials != nil {
			delete(account.Credentials, AccountCredentialSub2APILoginEmail)
			delete(account.Credentials, AccountCredentialSub2APILoginPassword)
			delete(account.Credentials, AccountCredentialSub2APIAccessToken)
			delete(account.Credentials, AccountCredentialSub2APIRefreshToken)
		}
		return nil
	}

	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	if account.UpstreamConfigID != nil && account.UpstreamKeyID != nil {
		account.Extra[AccountUpstreamProviderKey] = AccountUpstreamProviderSub2API
		return nil
	}
	adapter := account.Sub2APIRateSyncAdapter()
	switch adapter {
	case AccountSub2APIRateSyncAdapterManualJWT:
		token := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APIAccessToken))
		if token == "" || strings.Contains(token, "***") {
			return infraerrors.New(http.StatusBadRequest, "SUB2API_UPSTREAM_ACCESS_TOKEN_REQUIRED", "sub2api upstream access token is required")
		}
		delete(account.Credentials, AccountCredentialSub2APILoginEmail)
		delete(account.Credentials, AccountCredentialSub2APILoginPassword)
		account.Extra[AccountSub2APIRateSyncAdapterKey] = AccountSub2APIRateSyncAdapterManualJWT
	default:
		email := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginEmail))
		password := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginPassword))
		if email == "" || !strings.Contains(email, "@") || strings.Contains(email, "***") {
			return infraerrors.New(http.StatusBadRequest, "SUB2API_UPSTREAM_LOGIN_EMAIL_REQUIRED", "sub2api upstream login email is required")
		}
		if password == "" || strings.Contains(password, "***") {
			return infraerrors.New(http.StatusBadRequest, "SUB2API_UPSTREAM_LOGIN_PASSWORD_REQUIRED", "sub2api upstream login password is required")
		}
		delete(account.Credentials, AccountCredentialSub2APIAccessToken)
		delete(account.Credentials, AccountCredentialSub2APIRefreshToken)
		account.Extra[AccountSub2APIRateSyncAdapterKey] = AccountSub2APIRateSyncAdapterUserLogin
	}
	return nil
}
