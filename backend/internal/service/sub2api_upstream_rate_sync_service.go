package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	sub2APILoginPath       = "/api/v1/auth/login"
	sub2APIRefreshPath     = "/api/v1/auth/refresh"
	sub2APIProfilePath     = "/api/v1/auth/me"
	sub2APIKeysPath        = "/api/v1/keys"
	sub2APIKeysFallback    = "/api/v1/api-keys"
	sub2APIAvailableGroups = "/api/v1/groups/available"
	sub2APIGroupRatesPath  = "/api/v1/groups/rates"
	sub2APIKeysPageSize    = 100
	sub2APIMaxKeyListPages = 1000

	sub2APIBrowserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
)

const sub2APITokenRefreshSkew = 60 * time.Second

var (
	errSub2APIEndpointNotFound      = errors.New("sub2api endpoint not found")
	errSub2APIAccessTokenMayBeStale = errors.New("sub2api access token may be expired; please update the manual JWT or provide a refresh token")
)

type Sub2APIUpstreamRateSyncService struct {
	accountRepo        AccountRepository
	upstreamConfigRepo UpstreamConfigRepository
	upstreamConfigSvc  *UpstreamConfigService
	proxyRepo          ProxyRepository
	interval           time.Duration
	concurrency        int
	stopCh             chan struct{}
	stopOnce           sync.Once
	wg                 sync.WaitGroup
}

type sub2APIEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"reason"`
	Data    T      `json:"data"`
}

type sub2APILoginData struct {
	AccessToken       string `json:"access_token"`
	AccessTokenCamel  string `json:"accessToken"`
	RefreshToken      string `json:"refresh_token"`
	RefreshTokenCamel string `json:"refreshToken"`
	TokenType         string `json:"token_type"`
	TokenTypeCamel    string `json:"tokenType"`
	ExpiresIn         any    `json:"expires_in"`
	ExpiresInCamel    any    `json:"expiresIn"`
	Requires2FA       bool   `json:"requires_2fa"`
	Requires2FACamel  bool   `json:"requires2FA"`
}

func (d sub2APILoginData) accessToken() string {
	if token := strings.TrimSpace(d.AccessToken); token != "" {
		return token
	}
	return strings.TrimSpace(d.AccessTokenCamel)
}

func (d sub2APILoginData) requires2FA() bool {
	return d.Requires2FA || d.Requires2FACamel
}

type sub2APIRefreshData struct {
	AccessToken       string     `json:"access_token"`
	AccessTokenCamel  string     `json:"accessToken"`
	RefreshToken      string     `json:"refresh_token"`
	RefreshTokenCamel string     `json:"refreshToken"`
	TokenType         string     `json:"token_type"`
	TokenTypeCamel    string     `json:"tokenType"`
	ExpiresIn         any        `json:"expires_in"`
	ExpiresInCamel    any        `json:"expiresIn"`
	ExpiresAt         *time.Time `json:"-"`
}

func (d *sub2APIRefreshData) normalize(fallbackRefreshToken string) {
	if d == nil {
		return
	}
	if strings.TrimSpace(d.AccessToken) == "" {
		d.AccessToken = strings.TrimSpace(d.AccessTokenCamel)
	} else {
		d.AccessToken = strings.TrimSpace(d.AccessToken)
	}
	if strings.TrimSpace(d.RefreshToken) == "" {
		d.RefreshToken = strings.TrimSpace(d.RefreshTokenCamel)
	}
	if strings.TrimSpace(d.RefreshToken) == "" {
		d.RefreshToken = strings.TrimSpace(fallbackRefreshToken)
	} else {
		d.RefreshToken = strings.TrimSpace(d.RefreshToken)
	}
	expiresIn, ok := sub2APINonNegativeNumber(d.ExpiresIn)
	if !ok || expiresIn <= 0 {
		expiresIn, ok = sub2APINonNegativeNumber(d.ExpiresInCamel)
	}
	if ok && expiresIn > 0 {
		expiresAt := time.Now().UTC().Add(time.Duration(expiresIn * float64(time.Second)))
		d.ExpiresAt = &expiresAt
	} else {
		d.ExpiresAt = sub2APIJWTExpiresAt(d.AccessToken)
	}
}

func sub2APIJWTExpiresAt(token string) *time.Time {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims struct {
		ExpiresAt json.Number `json:"exp"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&claims); err != nil {
		return nil
	}
	exp, err := claims.ExpiresAt.Int64()
	if err != nil || exp <= 0 {
		return nil
	}
	expiresAt := time.Unix(exp, 0).UTC()
	return &expiresAt
}

type sub2APIKeyListData struct {
	Items    []sub2APIUpstreamKey `json:"items"`
	Total    *int64               `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"page_size"`
	Pages    int                  `json:"pages"`
}

type sub2APIUpstreamKey struct {
	ID      int64  `json:"id"`
	Key     string `json:"key"`
	Name    string `json:"name"`
	GroupID *int64 `json:"group_id"`
	Group   *struct {
		ID             int64    `json:"id"`
		Name           string   `json:"name"`
		Platform       string   `json:"platform"`
		RateMultiplier *float64 `json:"rate_multiplier"`
	} `json:"group"`
}

type sub2APIUserLoginSession struct {
	keys         []sub2APIUpstreamKey
	keysComplete bool
	groupRates   map[int64]sub2APIGroupRateInfo
	accessToken  string
}

type sub2APIGroupRateInfo struct {
	ID                     int64
	Name                   string
	Platform               string
	DefaultMultiplier      *float64
	DedicatedMultiplier    *float64
	HasDedicatedMultiplier bool
}

type sub2APIProfile struct {
	ID             int64           `json:"id"`
	Email          string          `json:"email"`
	Balance        float64         `json:"balance"`
	TotalRecharged float64         `json:"total_recharged"`
	Concurrency    json.RawMessage `json:"concurrency"`
}

type sub2APIUpstreamSnapshot struct {
	Keys            []UpstreamKey
	KeysComplete    bool
	Profile         *sub2APIProfile
	ProfileErr      error
	RefreshedTokens *sub2APIRefreshData
}

type sub2APISyncTarget struct {
	account          Account
	rootURL          string
	adapter          string
	email            string
	password         string
	notInCNConfirmed bool
	accessToken      string
	refreshToken     string
	tokenExpiresAt   *time.Time
	apiKey           string
	proxyID          *int64
	proxyURL         string
}

func NewSub2APIUpstreamRateSyncService(accountRepo AccountRepository, proxyRepo ProxyRepository, interval time.Duration) *Sub2APIUpstreamRateSyncService {
	return &Sub2APIUpstreamRateSyncService{
		accountRepo: accountRepo,
		proxyRepo:   proxyRepo,
		interval:    interval,
		concurrency: 5,
		stopCh:      make(chan struct{}),
	}
}

func (s *Sub2APIUpstreamRateSyncService) SetUpstreamConfigRepository(repo UpstreamConfigRepository) {
	if s != nil {
		s.upstreamConfigRepo = repo
	}
}

func (s *Sub2APIUpstreamRateSyncService) SetUpstreamConfigService(svc *UpstreamConfigService) {
	if s != nil {
		s.upstreamConfigSvc = svc
	}
}

func (s *Sub2APIUpstreamRateSyncService) Start() {
	if s == nil || s.accountRepo == nil || s.interval <= 0 {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()

		s.runOnce()
		for {
			select {
			case <-ticker.C:
				s.runOnce()
			case <-s.stopCh:
				return
			}
		}
	}()
}

func (s *Sub2APIUpstreamRateSyncService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *Sub2APIUpstreamRateSyncService) SyncAccountNow(ctx context.Context, account *Account) error {
	if s == nil || s.accountRepo == nil || account == nil || !account.IsSub2APIUpstream() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	proxyURL, err := s.resolveAccountProxyURL(ctx, *account)
	if err != nil {
		return s.recordSyncError(ctx, account, err)
	}
	target, err := s.newSub2APISyncTarget(ctx, *account, proxyURL)
	if err != nil {
		return s.recordSyncError(ctx, account, err)
	}
	session, refreshedTokens, err := s.fetchUserLoginSession(ctx, target)
	if refreshedTokens != nil {
		if saveErr := s.saveRefreshedSub2APITokens(ctx, account, *refreshedTokens); saveErr != nil {
			return s.recordSyncError(ctx, account, saveErr)
		}
	}
	if err != nil {
		return s.recordSyncError(ctx, account, err)
	}
	return s.syncTargetWithSession(ctx, target, session)
}

func (s *Sub2APIUpstreamRateSyncService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if s.upstreamConfigSvc != nil {
		results := s.upstreamConfigSvc.SyncActiveUpstreamConfigs(ctx)
		for i := range results {
			result := results[i]
			if !result.Success {
				log.Printf("[UpstreamRateSync] sync upstream config failed: config=%d name=%q err=%s", result.ConfigID, result.Name, result.Error)
			}
		}
		return
	}

	log.Printf("[UpstreamRateSync] upstream config service is unavailable; skipping deprecated account-scan fallback")
}

func newSub2APISyncTarget(account Account, proxyURL string) (sub2APISyncTarget, error) {
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	email := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginEmail))
	password := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginPassword))
	accessToken := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APIAccessToken))
	refreshToken := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APIRefreshToken))
	tokenExpiresAt := account.GetCredentialAsTime(AccountCredentialSub2APITokenExpiresAt)
	if baseURL == "" {
		return sub2APISyncTarget{}, fmt.Errorf("missing base_url")
	}
	if apiKey == "" {
		return sub2APISyncTarget{}, fmt.Errorf("missing api_key")
	}
	rootURL, err := normalizeSub2APIBaseURL(baseURL)
	if err != nil {
		return sub2APISyncTarget{}, err
	}
	normalizedProxyURL, err := normalizeSub2APIProxyURL(proxyURL)
	if err != nil {
		return sub2APISyncTarget{}, err
	}
	adapter := account.Sub2APIRateSyncAdapter()
	if adapter == AccountSub2APIRateSyncAdapterManualJWT {
		if accessToken == "" && refreshToken == "" {
			return sub2APISyncTarget{}, fmt.Errorf("missing sub2api access token or refresh token")
		}
	} else {
		if email == "" {
			return sub2APISyncTarget{}, fmt.Errorf("missing sub2api login email")
		}
		if password == "" {
			return sub2APISyncTarget{}, fmt.Errorf("missing sub2api login password")
		}
	}
	return sub2APISyncTarget{
		account:          account,
		rootURL:          rootURL,
		adapter:          adapter,
		email:            email,
		password:         password,
		notInCNConfirmed: upstreamCredentialBool(account.Credentials[SettingKeyUpstreamSub2APINotInCNConfirmed]),
		accessToken:      accessToken,
		refreshToken:     refreshToken,
		tokenExpiresAt:   tokenExpiresAt,
		apiKey:           apiKey,
		proxyID:          account.ProxyID,
		proxyURL:         normalizedProxyURL,
	}, nil
}

func upstreamCredentialBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func (s *Sub2APIUpstreamRateSyncService) newSub2APISyncTarget(ctx context.Context, account Account, proxyURL string) (sub2APISyncTarget, error) {
	if account.UpstreamConfigID == nil || account.UpstreamKeyID == nil {
		if account.Sub2APIRateSyncAdapter() == AccountSub2APIRateSyncAdapterUserLogin {
			reader, ok := s.upstreamConfigRepo.(UpstreamSettingsReader)
			if !ok {
				return sub2APISyncTarget{}, fmt.Errorf("upstream compliance settings are unavailable")
			}
			settings, err := reader.GetUpstreamSettings(ctx)
			if err != nil {
				return sub2APISyncTarget{}, fmt.Errorf("failed to read upstream compliance settings")
			}
			if settings != nil {
				if account.Credentials == nil {
					account.Credentials = make(map[string]any)
				}
				account.Credentials[SettingKeyUpstreamSub2APINotInCNConfirmed] = settings.Sub2APINotInCNConfirmed
			}
		}
		return newSub2APISyncTarget(account, proxyURL)
	}
	if s == nil || s.upstreamConfigRepo == nil {
		return sub2APISyncTarget{}, fmt.Errorf("missing upstream config repository")
	}
	cfg, err := s.upstreamConfigRepo.GetByID(ctx, *account.UpstreamConfigID)
	if err != nil {
		return sub2APISyncTarget{}, err
	}
	key, err := s.upstreamConfigRepo.GetKeyByID(ctx, *account.UpstreamKeyID)
	if err != nil {
		return sub2APISyncTarget{}, err
	}
	if cfg == nil || key == nil || key.UpstreamConfigID != cfg.ID {
		return sub2APISyncTarget{}, fmt.Errorf("invalid upstream binding")
	}
	if cfg.Provider == UpstreamProviderSub2API && cfg.AuthMode == UpstreamAuthModeUserLogin {
		reader, ok := s.upstreamConfigRepo.(UpstreamSettingsReader)
		if !ok {
			return sub2APISyncTarget{}, fmt.Errorf("upstream compliance settings are unavailable")
		}
		settings, settingsErr := reader.GetUpstreamSettings(ctx)
		if settingsErr != nil {
			return sub2APISyncTarget{}, fmt.Errorf("failed to read upstream compliance settings")
		}
		if settings != nil {
			cfg.Sub2APINotInCNConfirmed = settings.Sub2APINotInCNConfirmed
		}
	}
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	account.Credentials["base_url"] = cfg.SiteURL
	account.Credentials["api_key"] = key.Key
	for k, v := range cfg.Credentials {
		account.Credentials[k] = v
	}
	account.Credentials[SettingKeyUpstreamSub2APINotInCNConfirmed] = cfg.Sub2APINotInCNConfirmed
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[AccountUpstreamProviderKey] = cfg.Provider
	account.Extra[AccountSub2APIRateSyncAdapterKey] = cfg.AuthMode
	if cfg.ProxyID != nil {
		account.ProxyID = cfg.ProxyID
	}
	return newSub2APISyncTarget(account, proxyURL)
}

func (s *Sub2APIUpstreamRateSyncService) fetchUserLoginSession(ctx context.Context, target sub2APISyncTarget) (*sub2APIUserLoginSession, *sub2APIRefreshData, error) {
	client, err := sub2APIHTTPClient(target.proxyURL)
	if err != nil {
		return nil, nil, err
	}
	token := target.accessToken
	if target.adapter != AccountSub2APIRateSyncAdapterManualJWT {
		token, err = s.loginSub2APIUser(ctx, client, target)
		if err != nil {
			return nil, nil, err
		}
		session, err := s.fetchSessionWithToken(ctx, client, target.rootURL, token)
		return session, nil, err
	}
	if token == "" && target.refreshToken != "" {
		refreshed, refreshErr := s.refreshSub2APIToken(ctx, client, target)
		if refreshErr != nil {
			return nil, nil, fmt.Errorf("refresh sub2api token failed: %w", refreshErr)
		}
		session, err := s.fetchSessionWithToken(ctx, client, target.rootURL, refreshed.AccessToken)
		if err != nil {
			return nil, refreshed, err
		}
		return session, refreshed, nil
	}
	if token != "" && target.refreshToken != "" && target.tokenExpiresAt != nil && time.Until(*target.tokenExpiresAt) <= sub2APITokenRefreshSkew {
		refreshed, refreshErr := s.refreshSub2APIToken(ctx, client, target)
		if refreshErr == nil {
			session, err := s.fetchSessionWithToken(ctx, client, target.rootURL, refreshed.AccessToken)
			if err == nil {
				return session, refreshed, nil
			}
			fallbackSession, fallbackErr := s.fetchSessionWithToken(ctx, client, target.rootURL, token)
			if fallbackErr == nil {
				return fallbackSession, refreshed, nil
			}
			return nil, refreshed, err
		}
		if refreshErr != nil {
			log.Printf("[Sub2APIRateSync] proactive refresh failed, falling back to access token: base_url=%s err=%v", target.rootURL, sanitizeSub2APISyncError(&target.account, refreshErr))
		}
	}
	session, err := s.fetchSessionWithToken(ctx, client, target.rootURL, token)
	if err == nil || !errors.Is(err, errSub2APIAccessTokenMayBeStale) {
		return session, nil, err
	}
	if target.refreshToken == "" {
		return nil, nil, err
	}
	refreshed, refreshErr := s.refreshSub2APIToken(ctx, client, target)
	if refreshErr != nil {
		return nil, nil, fmt.Errorf("refresh sub2api token failed: %w", refreshErr)
	}
	session, err = s.fetchSessionWithToken(ctx, client, target.rootURL, refreshed.AccessToken)
	if err != nil {
		return nil, refreshed, err
	}
	return session, refreshed, nil
}

func testSub2APIUpstreamConfig(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error {
	_, err := syncSub2APIUpstreamSnapshot(ctx, cfg, proxyURL, false)
	return err
}

func syncSub2APIUpstreamKeys(ctx context.Context, cfg *UpstreamConfig, proxyURL string) ([]UpstreamKey, *sub2APIRefreshData, error) {
	snapshot, err := syncSub2APIUpstreamSnapshot(ctx, cfg, proxyURL, false)
	if err != nil {
		return nil, nil, err
	}
	return snapshot.Keys, snapshot.RefreshedTokens, nil
}

func syncSub2APIUpstreamSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*sub2APIUpstreamSnapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing upstream config")
	}
	account := Account{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": cfg.SiteURL, "api_key": "sync-only"},
		Extra: map[string]any{
			AccountUpstreamProviderKey:       cfg.Provider,
			AccountSub2APIRateSyncAdapterKey: cfg.AuthMode,
		},
		ProxyID: cfg.ProxyID,
	}
	for k, v := range cfg.Credentials {
		account.Credentials[k] = v
	}
	account.Credentials[SettingKeyUpstreamSub2APINotInCNConfirmed] = cfg.Sub2APINotInCNConfirmed
	target, err := newSub2APISyncTarget(account, proxyURL)
	if err != nil {
		return nil, err
	}
	svc := &Sub2APIUpstreamRateSyncService{}
	session, refreshed, err := svc.fetchUserLoginSession(ctx, target)
	snapshot := &sub2APIUpstreamSnapshot{RefreshedTokens: refreshed}
	if err != nil {
		return snapshot, err
	}
	now := time.Now()
	out := make([]UpstreamKey, 0, len(session.keys))
	for _, upstreamKey := range session.keys {
		key := strings.TrimSpace(upstreamKey.Key)
		rate, err := resolveSub2APIKeyRate(upstreamKey, session.groupRates)
		if err != nil {
			return snapshot, err
		}
		name := normalizeUpstreamDisplayName(upstreamKey.Name, 100)
		groupID := effectiveSub2APIGroupID(upstreamKey)
		groupName := ""
		platform := PlatformOpenAI
		var groupInfo *sub2APIGroupRateInfo
		if groupID != nil && session.groupRates != nil {
			if info, ok := session.groupRates[*groupID]; ok {
				groupInfo = &info
				groupName = normalizeUpstreamDisplayName(info.Name, 100)
				if strings.TrimSpace(info.Platform) != "" {
					platform = strings.TrimSpace(info.Platform)
				}
			}
		}
		if upstreamKey.Group != nil {
			if groupName == "" {
				groupName = normalizeUpstreamDisplayName(upstreamKey.Group.Name, 100)
			}
			if platform == PlatformOpenAI && strings.TrimSpace(upstreamKey.Group.Platform) != "" {
				platform = strings.TrimSpace(upstreamKey.Group.Platform)
			}
			if upstreamKey.Group.RateMultiplier != nil {
				if groupInfo == nil {
					groupInfo = &sub2APIGroupRateInfo{}
					if groupID != nil {
						groupInfo.ID = *groupID
					}
				}
				if groupInfo.DefaultMultiplier == nil {
					defaultRate := *upstreamKey.Group.RateMultiplier
					groupInfo.DefaultMultiplier = &defaultRate
				}
			}
		}
		extra := sub2APIUpstreamKeyRateExtra(groupInfo)
		out = append(out, UpstreamKey{
			UpstreamConfigID:  cfg.ID,
			Name:              name,
			Key:               key,
			KeyHash:           HashUpstreamKey(key),
			RemoteKeyID:       &upstreamKey.ID,
			UpstreamGroupID:   groupID,
			UpstreamGroupName: groupName,
			Platform:          platform,
			RateMultiplier:    &rate,
			Status:            StatusActive,
			LastSeenAt:        &now,
			Extra:             extra,
		})
	}
	snapshot.Keys = out
	snapshot.KeysComplete = session.keysComplete
	if includeProfile {
		client, clientErr := sub2APIHTTPClient(target.proxyURL)
		if clientErr != nil {
			snapshot.ProfileErr = clientErr
		} else {
			snapshot.Profile, snapshot.ProfileErr = svc.fetchSub2APIProfile(ctx, client, target.rootURL, session.accessToken)
		}
	}
	return snapshot, nil
}

func normalizeUpstreamDisplayName(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if value == "" || maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func sanitizeStandaloneSub2APIError(err error, credentials map[string]any) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, key := range []string{
		AccountCredentialSub2APILoginPassword,
		AccountCredentialSub2APIAccessToken,
		AccountCredentialSub2APIRefreshToken,
	} {
		value := strings.TrimSpace(stringCredential(credentials, key))
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	return logredact.RedactText(text, "api_key", "jwt", "authorization", "bearer", "token", "access_token", "refresh_token")
}

func (s *Sub2APIUpstreamRateSyncService) fetchSessionWithToken(ctx context.Context, client *http.Client, rootURL, token string) (*sub2APIUserLoginSession, error) {
	keys, keysComplete, err := s.fetchSub2APIKeys(ctx, client, rootURL, token)
	if err != nil {
		return nil, err
	}
	groupRates, err := s.fetchSub2APIMergedGroupRates(ctx, client, rootURL, token)
	if err != nil {
		if errors.Is(err, errSub2APIAccessTokenMayBeStale) {
			return nil, err
		}
		log.Printf("[Sub2APIRateSync] group rate table unavailable, falling back to key group rate: base_url=%s err=%v", rootURL, logredact.RedactText(err.Error(), "api_key", "jwt", "authorization", "refresh_token", "access_token"))
	}
	return &sub2APIUserLoginSession{keys: keys, keysComplete: keysComplete, groupRates: groupRates, accessToken: strings.TrimSpace(token)}, nil
}

func (t sub2APISyncTarget) groupKey() string {
	proxyIdentity := fmt.Sprintf("proxy:%d:%s", proxyIDValue(t.proxyID), t.proxyURL)
	if t.adapter == AccountSub2APIRateSyncAdapterManualJWT {
		tokenMaterial := t.accessToken
		if t.refreshToken != "" {
			tokenMaterial = "refresh:" + t.refreshToken
		}
		sum := sha256.Sum256([]byte(tokenMaterial))
		return t.rootURL + "|" + t.adapter + "|" + proxyIdentity + "|" + hex.EncodeToString(sum[:8])
	}
	return t.rootURL + "|" + t.adapter + "|" + proxyIdentity + "|" + strings.ToLower(t.email)
}

func (s *Sub2APIUpstreamRateSyncService) loginSub2APIUser(ctx context.Context, client *http.Client, target sub2APISyncTarget) (string, error) {
	endpoint, err := buildSub2APIURL(target.rootURL, sub2APILoginPath)
	if err != nil {
		return "", err
	}
	var payload sub2APIEnvelope[sub2APILoginData]
	body := map[string]any{
		"email":    target.email,
		"password": target.password,
	}
	if target.notInCNConfirmed {
		body["not_in_cn_confirmed"] = true
	}
	status, err := s.doJSON(ctx, client, http.MethodPost, endpoint, "", body, &payload)
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("login returned status %d", status)
	}
	if payload.Code != 0 {
		return "", fmt.Errorf("login failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
	}
	if payload.Data.requires2FA() {
		return "", fmt.Errorf("sub2api login requires 2fa")
	}
	accessToken := payload.Data.accessToken()
	if accessToken == "" {
		return "", fmt.Errorf("sub2api login returned no access token")
	}
	return accessToken, nil
}

func (s *Sub2APIUpstreamRateSyncService) refreshSub2APIToken(ctx context.Context, client *http.Client, target sub2APISyncTarget) (*sub2APIRefreshData, error) {
	endpoint, err := buildSub2APIURL(target.rootURL, sub2APIRefreshPath)
	if err != nil {
		return nil, err
	}
	var payload sub2APIEnvelope[sub2APIRefreshData]
	status, err := s.doJSON(ctx, client, http.MethodPost, endpoint, "", map[string]string{
		"refresh_token": target.refreshToken,
	}, &payload)
	if err != nil {
		return nil, fmt.Errorf("refresh request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("refresh returned status %d", status)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("refresh failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
	}
	payload.Data.normalize(target.refreshToken)
	if payload.Data.AccessToken == "" {
		return nil, fmt.Errorf("refresh returned no access token")
	}
	return &payload.Data, nil
}

func (s *Sub2APIUpstreamRateSyncService) saveRefreshedSub2APITokens(ctx context.Context, account *Account, tokens sub2APIRefreshData) error {
	if account == nil {
		return fmt.Errorf("save refreshed sub2api token: missing account")
	}
	if strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
		return fmt.Errorf("save refreshed sub2api token: missing token")
	}
	if account.UpstreamConfigID != nil {
		if s.upstreamConfigRepo == nil {
			return fmt.Errorf("save refreshed sub2api token: missing upstream config repository")
		}
		return s.upstreamConfigRepo.SaveRefreshedTokens(ctx, *account.UpstreamConfigID, strings.TrimSpace(tokens.AccessToken), strings.TrimSpace(tokens.RefreshToken), tokens.ExpiresAt)
	}
	credentialUpdates := map[string]any{
		AccountCredentialSub2APIAccessToken:  strings.TrimSpace(tokens.AccessToken),
		AccountCredentialSub2APIRefreshToken: strings.TrimSpace(tokens.RefreshToken),
	}
	if tokens.ExpiresAt != nil {
		credentialUpdates[AccountCredentialSub2APITokenExpiresAt] = tokens.ExpiresAt.UTC().Format(time.RFC3339)
	} else {
		credentialUpdates[AccountCredentialSub2APITokenExpiresAt] = nil
	}
	if _, err := s.accountRepo.BulkUpdate(ctx, []int64{account.ID}, AccountBulkUpdate{Credentials: credentialUpdates}); err != nil {
		return fmt.Errorf("save refreshed sub2api token: %w", err)
	}
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	for k, v := range credentialUpdates {
		account.Credentials[k] = v
	}
	return nil
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIKeys(ctx context.Context, client *http.Client, rootURL, token string) ([]sub2APIUpstreamKey, bool, error) {
	keys, err := s.fetchSub2APIKeysFromPath(ctx, client, rootURL, token, sub2APIKeysPath)
	if errors.Is(err, errSub2APIEndpointNotFound) {
		keys, err = s.fetchSub2APIKeysFromPath(ctx, client, rootURL, token, sub2APIKeysFallback)
	}
	return keys, err == nil, err
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIKeysFromPath(ctx context.Context, client *http.Client, rootURL, token, path string) ([]sub2APIUpstreamKey, error) {
	out := make([]sub2APIUpstreamKey, 0)
	seenIDs := make(map[int64]struct{})
	var expectedTotal int64 = -1
	for page := 1; page <= sub2APIMaxKeyListPages; page++ {
		endpoint, err := buildSub2APIURL(rootURL, path)
		if err != nil {
			return nil, err
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("page", fmt.Sprintf("%d", page))
		q.Set("page_size", fmt.Sprintf("%d", sub2APIKeysPageSize))
		u.RawQuery = q.Encode()

		var payload sub2APIEnvelope[sub2APIKeyListData]
		status, err := s.doJSON(ctx, client, http.MethodGet, u.String(), token, nil, &payload)
		if err != nil {
			return nil, fmt.Errorf("list api keys failed: %w", err)
		}
		if status == http.StatusNotFound {
			return nil, errSub2APIEndpointNotFound
		}
		if status < 200 || status >= 300 {
			if path == sub2APIKeysPath || path == sub2APIKeysFallback {
				if status == http.StatusUnauthorized {
					return nil, errSub2APIAccessTokenMayBeStale
				}
				if status == http.StatusForbidden {
					return nil, fmt.Errorf("sub2api access token was rejected or blocked by upstream")
				}
			}
			return nil, fmt.Errorf("list api keys returned status %d", status)
		}
		if payload.Code != 0 {
			return nil, fmt.Errorf("list api keys failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
		}
		if payload.Data.Total != nil && *payload.Data.Total >= 0 {
			if expectedTotal >= 0 && *payload.Data.Total != expectedTotal {
				return nil, fmt.Errorf("api key list total changed across pages")
			}
			expectedTotal = *payload.Data.Total
		}
		newIDs := 0
		for _, item := range payload.Data.Items {
			if item.ID <= 0 {
				return nil, fmt.Errorf("api key list contains invalid key id")
			}
			if _, exists := seenIDs[item.ID]; exists {
				continue
			}
			seenIDs[item.ID] = struct{}{}
			out = append(out, item)
			newIDs++
		}
		if len(payload.Data.Items) > 0 && newIDs == 0 {
			return nil, fmt.Errorf("api key list pagination made no progress")
		}
		if expectedTotal >= 0 && int64(len(out)) >= expectedTotal {
			if int64(len(out)) != expectedTotal {
				return nil, fmt.Errorf("api key list unique count exceeds total")
			}
			return out, nil
		}

		pages := payload.Data.Pages
		if pages > 0 {
			if page >= pages {
				if expectedTotal >= 0 && int64(len(out)) != expectedTotal {
					return nil, fmt.Errorf("api key list incomplete: got %d of %d", len(out), expectedTotal)
				}
				return out, nil
			}
			continue
		}
		if len(payload.Data.Items) < sub2APIKeysPageSize {
			if expectedTotal >= 0 && int64(len(out)) != expectedTotal {
				return nil, fmt.Errorf("api key list incomplete: got %d of %d", len(out), expectedTotal)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("api key list exceeded max pages")
}

func resolveSub2APIKeyRate(key sub2APIUpstreamKey, rates map[int64]sub2APIGroupRateInfo) (float64, error) {
	groupID := effectiveSub2APIGroupID(key)
	if groupID != nil {
		if info, ok := rates[*groupID]; ok {
			if info.HasDedicatedMultiplier && info.DedicatedMultiplier != nil {
				return validateSub2APIRate(*info.DedicatedMultiplier)
			}
			if info.DefaultMultiplier != nil {
				return validateSub2APIRate(*info.DefaultMultiplier)
			}
		}
	}
	if key.Group != nil && key.Group.RateMultiplier != nil {
		return validateSub2APIRate(*key.Group.RateMultiplier)
	}
	return 0, fmt.Errorf("api key group has no valid rate multiplier")
}

func validateSub2APIRate(rate float64) (float64, error) {
	if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
		return 0, fmt.Errorf("invalid rate multiplier")
	}
	return rate, nil
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIMergedGroupRates(ctx context.Context, client *http.Client, rootURL, token string) (map[int64]sub2APIGroupRateInfo, error) {
	groups, availableErr := s.fetchSub2APIAvailableGroups(ctx, client, rootURL, token)
	if errors.Is(availableErr, errSub2APIAccessTokenMayBeStale) {
		return nil, availableErr
	}
	if availableErr != nil {
		log.Printf("[Sub2APIRateSync] /groups/available unavailable, using key group defaults: base_url=%s err=%v", rootURL, logredact.RedactText(availableErr.Error(), "api_key", "jwt", "authorization", "refresh_token", "access_token"))
		groups = make(map[int64]sub2APIGroupRateInfo)
	}
	rates, ratesErr := s.fetchSub2APIGroupRates(ctx, client, rootURL, token)
	if errors.Is(ratesErr, errSub2APIAccessTokenMayBeStale) {
		return nil, ratesErr
	}
	if ratesErr != nil {
		log.Printf("[Sub2APIRateSync] /groups/rates unavailable, using /groups/available defaults: base_url=%s err=%v", rootURL, logredact.RedactText(ratesErr.Error(), "api_key", "jwt", "authorization", "refresh_token", "access_token"))
		if availableErr != nil {
			return nil, fmt.Errorf("group rate endpoints unavailable: available: %v; rates: %v", availableErr, ratesErr)
		}
		return groups, nil
	}
	for groupID, rate := range rates {
		info, ok := groups[groupID]
		if !ok {
			info = sub2APIGroupRateInfo{ID: groupID}
		}
		rateCopy := rate
		info.DedicatedMultiplier = &rateCopy
		info.HasDedicatedMultiplier = true
		groups[groupID] = info
	}
	return groups, nil
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIAvailableGroups(ctx context.Context, client *http.Client, rootURL, token string) (map[int64]sub2APIGroupRateInfo, error) {
	endpoint, err := buildSub2APIURL(rootURL, sub2APIAvailableGroups)
	if err != nil {
		return nil, err
	}
	var payload any
	status, err := s.doJSON(ctx, client, http.MethodGet, endpoint, token, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("list available groups failed: %w", err)
	}
	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, errSub2APIAccessTokenMayBeStale
		}
		return nil, fmt.Errorf("list available groups returned status %d", status)
	}
	if code, reason, ok := sub2APIEnvelopeCode(payload); ok && code != 0 {
		return nil, fmt.Errorf("list available groups failed: code %d%s", code, safeEnvelopeReason(reason))
	}
	out := make(map[int64]sub2APIGroupRateInfo)
	for _, item := range sub2APIDataArray(payload) {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		groupID, ok := sub2APIInt64Field(record, "id", "group_id", "groupId")
		if !ok || groupID <= 0 {
			continue
		}
		name := strings.TrimSpace(sub2APIStringField(record, "name"))
		if name == "" {
			continue
		}
		var defaultRate *float64
		if rate, ok := sub2APINonNegativeNumberField(record, "rate_multiplier", "rateMultiplier", "multiplier", "rate"); ok {
			rateCopy := rate
			defaultRate = &rateCopy
		}
		out[groupID] = sub2APIGroupRateInfo{
			ID:                groupID,
			Name:              name,
			Platform:          strings.TrimSpace(sub2APIStringField(record, "platform")),
			DefaultMultiplier: defaultRate,
		}
	}
	return out, nil
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIGroupRates(ctx context.Context, client *http.Client, rootURL, token string) (map[int64]float64, error) {
	endpoint, err := buildSub2APIURL(rootURL, sub2APIGroupRatesPath)
	if err != nil {
		return nil, err
	}
	var payload any
	status, err := s.doJSON(ctx, client, http.MethodGet, endpoint, token, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("list group rates failed: %w", err)
	}
	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, errSub2APIAccessTokenMayBeStale
		}
		return nil, fmt.Errorf("list group rates returned status %d", status)
	}
	if code, reason, ok := sub2APIEnvelopeCode(payload); ok && code != 0 {
		return nil, fmt.Errorf("list group rates failed: code %d%s", code, safeEnvelopeReason(reason))
	}
	return parseSub2APIGroupRateOverrides(payload), nil
}

func parseSub2APIGroupRateOverrides(payload any) map[int64]float64 {
	out := make(map[int64]float64)
	data := sub2APIEnvelopeData(payload)
	switch typed := data.(type) {
	case map[string]any:
		for rawID, rawRate := range typed {
			groupID, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
			if err != nil || groupID <= 0 {
				continue
			}
			if rate, ok := sub2APINonNegativeNumber(rawRate); ok {
				out[groupID] = rate
			}
		}
	case []any:
		for _, item := range typed {
			record, ok := item.(map[string]any)
			if !ok {
				continue
			}
			groupID, ok := sub2APIInt64Field(record, "group_id", "groupId", "id")
			if !ok || groupID <= 0 {
				continue
			}
			rate, ok := sub2APINonNegativeNumberField(record, "rate_multiplier", "rateMultiplier", "multiplier", "rate")
			if !ok {
				continue
			}
			out[groupID] = rate
		}
	}
	return out
}

func sub2APIUpstreamKeyRateExtra(info *sub2APIGroupRateInfo) map[string]any {
	if info == nil {
		return map[string]any{}
	}
	extra := map[string]any{
		"has_dedicated_rate_multiplier": info.HasDedicatedMultiplier,
	}
	if info.DefaultMultiplier != nil {
		extra["default_rate_multiplier"] = *info.DefaultMultiplier
	}
	if info.DedicatedMultiplier != nil {
		extra["dedicated_rate_multiplier"] = *info.DedicatedMultiplier
	}
	return extra
}

func sub2APIEnvelopeData(payload any) any {
	if record, ok := payload.(map[string]any); ok {
		if data, exists := record["data"]; exists {
			return data
		}
	}
	return payload
}

func sub2APIDataArray(payload any) []any {
	data := sub2APIEnvelopeData(payload)
	switch typed := data.(type) {
	case []any:
		return typed
	case map[string]any:
		for _, key := range []string{"items", "list", "records", "groups"} {
			if items, ok := typed[key].([]any); ok {
				return items
			}
		}
	}
	return nil
}

func sub2APIEnvelopeCode(payload any) (int, string, bool) {
	record, ok := payload.(map[string]any)
	if !ok {
		return 0, "", false
	}
	raw, exists := record["code"]
	if !exists {
		return 0, "", false
	}
	code, ok := sub2APIInt(raw)
	if !ok {
		return 0, "", false
	}
	reason := sub2APIStringField(record, "reason", "message")
	return code, reason, true
}

func sub2APIInt64Field(record map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if out, ok := sub2APIInt64(value); ok {
				return out, true
			}
		}
	}
	return 0, false
}

func sub2APIStringField(record map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := record[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func sub2APINonNegativeNumberField(record map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, exists := record[key]; exists {
			if out, ok := sub2APINonNegativeNumber(value); ok {
				return out, true
			}
		}
	}
	return 0, false
}

func sub2APINonNegativeNumber(value any) (float64, bool) {
	out, ok := sub2APIFloat64(value)
	if !ok || out < 0 {
		return 0, false
	}
	return out, true
}

func sub2APIInt(value any) (int, bool) {
	out, ok := sub2APIInt64(value)
	if !ok {
		return 0, false
	}
	return int(out), true
}

func sub2APIInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return parsed, true
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func sub2APIFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		if !math.IsNaN(typed) && !math.IsInf(typed, 0) {
			return typed, true
		}
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed, true
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0) {
			return parsed, true
		}
	}
	return 0, false
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIProfile(ctx context.Context, client *http.Client, rootURL, token string) (*sub2APIProfile, error) {
	endpoint, err := buildSub2APIURL(rootURL, sub2APIProfilePath)
	if err != nil {
		return nil, err
	}
	var payload sub2APIEnvelope[*sub2APIProfile]
	status, err := s.doJSON(ctx, client, http.MethodGet, endpoint, token, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("get profile failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("get profile returned status %d", status)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("get profile failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
	}
	if payload.Data == nil {
		return nil, fmt.Errorf("get profile returned null data")
	}
	if math.IsNaN(payload.Data.Balance) || math.IsInf(payload.Data.Balance, 0) {
		return nil, fmt.Errorf("get profile returned invalid balance")
	}
	if math.IsNaN(payload.Data.TotalRecharged) || math.IsInf(payload.Data.TotalRecharged, 0) {
		return nil, fmt.Errorf("get profile returned invalid total_recharged")
	}
	return payload.Data, nil
}

func (s *Sub2APIUpstreamRateSyncService) syncTargetWithSession(ctx context.Context, target sub2APISyncTarget, session *sub2APIUserLoginSession) error {
	multiplier, platform, err := resolveSub2APIEffectiveRate(target.apiKey, session)
	if err != nil {
		return s.recordSyncError(ctx, &target.account, err)
	}
	if multiplier < 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		err := fmt.Errorf("invalid effective rate multiplier")
		return s.recordSyncError(ctx, &target.account, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	priority := Sub2APIUpstreamPriority(multiplier)
	loadFactor := AutoUpstreamLoadFactor(priority, target.account.Concurrency)
	_, err = s.accountRepo.BulkUpdate(ctx, []int64{target.account.ID}, AccountBulkUpdate{
		RateMultiplier: &multiplier,
		Priority:       &priority,
		LoadFactor:     &loadFactor,
		Extra: map[string]any{
			"sub2api_rate_sync_last_success_at": now,
			"sub2api_rate_sync_last_error":      "",
			"sub2api_upstream_platform":         platform,
			"sub2api_upstream_rate_multiplier":  multiplier,
		},
	})
	if err != nil {
		return fmt.Errorf("update account rate: %w", err)
	}
	return nil
}

func resolveSub2APIEffectiveRate(apiKey string, session *sub2APIUserLoginSession) (float64, string, error) {
	if session == nil {
		return 0, "", fmt.Errorf("missing sub2api login session")
	}
	var matches []sub2APIUpstreamKey
	sawHiddenKey := false
	for _, key := range session.keys {
		candidate := strings.TrimSpace(key.Key)
		if candidate == "" || strings.Contains(candidate, "***") {
			sawHiddenKey = true
			continue
		}
		if candidate == apiKey {
			matches = append(matches, key)
		}
	}
	if len(matches) == 0 {
		if sawHiddenKey {
			return 0, "", fmt.Errorf("api key list does not expose complete keys")
		}
		return 0, "", fmt.Errorf("api key not found in upstream user account")
	}
	if len(matches) > 1 {
		return 0, "", fmt.Errorf("multiple matching api keys found in upstream user account")
	}

	key := matches[0]
	groupID := effectiveSub2APIGroupID(key)
	if groupID == nil {
		return 0, "", fmt.Errorf("matched api key is not bound to a group")
	}
	platform := PlatformOpenAI
	if key.Group != nil && strings.TrimSpace(key.Group.Platform) != "" {
		platform = strings.TrimSpace(key.Group.Platform)
	}
	if session.groupRates != nil {
		if info, ok := session.groupRates[*groupID]; ok {
			if strings.TrimSpace(info.Platform) != "" {
				platform = strings.TrimSpace(info.Platform)
			}
			if info.HasDedicatedMultiplier && info.DedicatedMultiplier != nil {
				return *info.DedicatedMultiplier, platform, nil
			}
			if info.DefaultMultiplier != nil {
				return *info.DefaultMultiplier, platform, nil
			}
		}
	}
	if key.Group == nil {
		return 0, "", fmt.Errorf("matched api key group has no rate multiplier")
	}
	if key.Group.RateMultiplier == nil {
		return 0, "", fmt.Errorf("matched api key group has no rate multiplier")
	}
	return *key.Group.RateMultiplier, platform, nil
}

func effectiveSub2APIGroupID(key sub2APIUpstreamKey) *int64 {
	if key.GroupID != nil {
		return key.GroupID
	}
	if key.Group != nil && key.Group.ID > 0 {
		return &key.Group.ID
	}
	return nil
}

func (s *Sub2APIUpstreamRateSyncService) recordSyncError(ctx context.Context, account *Account, err error) error {
	if err == nil || account == nil {
		return err
	}
	message := sanitizeSub2APISyncError(account, err)
	updateErr := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{
		"sub2api_rate_sync_last_error":    message,
		"sub2api_rate_sync_last_error_at": time.Now().UTC().Format(time.RFC3339),
	})
	if updateErr != nil {
		return fmt.Errorf("%w; record error: %v", err, updateErr)
	}
	return err
}

func (s *Sub2APIUpstreamRateSyncService) doJSON(ctx context.Context, client *http.Client, method, endpoint, bearerToken string, body any, out any) (int, error) {
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", sub2APIBrowserUserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(bearerToken) != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}

	if client == nil {
		return 0, fmt.Errorf("missing sub2api http client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, nil
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func normalizeSub2APIBaseURL(baseURL string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", fmt.Errorf("parse base_url: %w", err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid base_url")
	}
	if parsed.User != nil {
		return "", fmt.Errorf("base_url must not contain user info")
	}
	path := stripSub2APIGatewayPath(parsed.Path)
	parsed.Path = strings.TrimRight(path, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func buildSub2APIURL(baseURL, apiPath string) (string, error) {
	root, err := normalizeSub2APIBaseURL(baseURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(root)
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + apiPath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func normalizeSub2APIProxyURL(raw string) (string, error) {
	trimmed, _, err := proxyurl.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid account proxy: %w", err)
	}
	return trimmed, nil
}

func sub2APIHTTPClient(proxyURL string) (*http.Client, error) {
	return httpclient.GetClient(httpclient.Options{
		ProxyURL: proxyURL,
		Timeout:  10 * time.Second,
	})
}

func (s *Sub2APIUpstreamRateSyncService) resolveAccountProxyURL(ctx context.Context, account Account) (string, error) {
	if account.ProxyID == nil {
		return "", nil
	}
	if account.Proxy != nil {
		return account.Proxy.URL(), nil
	}
	if s.proxyRepo == nil {
		return "", fmt.Errorf("account proxy %d is configured but proxy repository is unavailable", *account.ProxyID)
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
	if err != nil {
		return "", fmt.Errorf("resolve account proxy %d: %w", *account.ProxyID, err)
	}
	if proxy == nil {
		return "", fmt.Errorf("resolve account proxy %d: proxy not found", *account.ProxyID)
	}
	return proxy.URL(), nil
}

func (s *Sub2APIUpstreamRateSyncService) resolveAccountProxyURLs(ctx context.Context, accounts []Account) (map[int64]string, map[int64]error) {
	urls := map[int64]string{0: ""}
	errs := make(map[int64]error)
	needed := make(map[int64]struct{})
	for i := range accounts {
		if !accounts[i].IsSub2APIUpstream() || accounts[i].ProxyID == nil {
			continue
		}
		id := *accounts[i].ProxyID
		if accounts[i].Proxy != nil {
			urls[id] = accounts[i].Proxy.URL()
			continue
		}
		needed[id] = struct{}{}
	}
	if len(needed) == 0 {
		return urls, errs
	}
	if s.proxyRepo == nil {
		for id := range needed {
			errs[id] = fmt.Errorf("account proxy %d is configured but proxy repository is unavailable", id)
		}
		return urls, errs
	}
	ids := make([]int64, 0, len(needed))
	for id := range needed {
		ids = append(ids, id)
	}
	proxies, err := s.proxyRepo.ListByIDs(ctx, ids)
	if err != nil {
		for id := range needed {
			errs[id] = fmt.Errorf("resolve account proxy %d: %w", id, err)
		}
		return urls, errs
	}
	for i := range proxies {
		urls[proxies[i].ID] = proxies[i].URL()
		delete(needed, proxies[i].ID)
	}
	for id := range needed {
		errs[id] = fmt.Errorf("resolve account proxy %d: proxy not found", id)
	}
	return urls, errs
}

func proxyIDValue(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

func stripSub2APIGatewayPath(path string) string {
	out := strings.TrimRight(path, "/")
	lower := strings.ToLower(out)
	for _, suffix := range []string{"/antigravity/v1beta", "/antigravity/v1", "/antigravity", "/api/v1beta", "/api/v1", "/api", "/v1beta", "/v1"} {
		if lower == suffix || strings.HasSuffix(lower, suffix) {
			return out[:len(out)-len(suffix)]
		}
	}
	return out
}

func sanitizeSub2APISyncError(account *Account, err error) string {
	message := logredact.RedactText(err.Error())
	if account == nil {
		return message
	}
	for _, secret := range []string{
		account.GetCredential("api_key"),
		account.GetCredential(AccountCredentialSub2APILoginPassword),
		account.GetCredential(AccountCredentialSub2APIAccessToken),
		account.GetCredential(AccountCredentialSub2APIRefreshToken),
	} {
		secret = strings.TrimSpace(secret)
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[REDACTED]")
		}
	}
	email := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginEmail))
	if email != "" {
		message = strings.ReplaceAll(message, email, maskSub2APIEmail(email))
	}
	return message
}

func safeEnvelopeReason(reason string) string {
	reason = strings.TrimSpace(logredact.RedactText(reason))
	if reason == "" {
		return ""
	}
	return ": " + reason
}

func maskSub2APIEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 1 {
		return "***"
	}
	return email[:1] + "***" + email[at:]
}

func Sub2APIUpstreamPriority(rateMultiplier float64) int {
	return int(math.Round(rateMultiplier * 100))
}
