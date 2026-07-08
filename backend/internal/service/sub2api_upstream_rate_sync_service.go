package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	sub2APILoginPath       = "/api/v1/auth/login"
	sub2APIRefreshPath     = "/api/v1/auth/refresh"
	sub2APIKeysPath        = "/api/v1/keys"
	sub2APIKeysFallback    = "/api/v1/api-keys"
	sub2APIGroupRatesPath  = "/api/v1/groups/rates"
	sub2APIKeysPageSize    = 100
	sub2APIMaxKeyListPages = 1000
)

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
	AccessToken string `json:"access_token"`
	Requires2FA bool   `json:"requires_2fa"`
}

type sub2APIRefreshData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type sub2APIKeyListData struct {
	Items    []sub2APIUpstreamKey `json:"items"`
	Total    int64                `json:"total"`
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
		ID             int64   `json:"id"`
		Name           string  `json:"name"`
		Platform       string  `json:"platform"`
		RateMultiplier float64 `json:"rate_multiplier"`
	} `json:"group"`
}

type sub2APIUserLoginSession struct {
	keys  []sub2APIUpstreamKey
	rates map[int64]float64
}

type sub2APISyncTarget struct {
	account      Account
	rootURL      string
	adapter      string
	email        string
	password     string
	accessToken  string
	refreshToken string
	apiKey       string
	proxyID      *int64
	proxyURL     string
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
	if err != nil {
		return s.recordSyncError(ctx, account, err)
	}
	if refreshedTokens != nil {
		if err := s.saveRefreshedSub2APITokens(ctx, account, *refreshedTokens); err != nil {
			return s.recordSyncError(ctx, account, err)
		}
	}
	return s.syncTargetWithSession(ctx, target, session)
}

func (s *Sub2APIUpstreamRateSyncService) runOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if s.upstreamConfigSvc != nil {
		results := s.upstreamConfigSvc.SyncActiveSub2APIConfigs(ctx)
		for i := range results {
			result := results[i]
			if !result.Success {
				log.Printf("[Sub2APIRateSync] sync upstream config failed: config=%d name=%q err=%s", result.ConfigID, result.Name, result.Error)
			}
		}
		return
	}

	accounts, _, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{
		Page:     1,
		PageSize: 10000,
	}, "", AccountTypeAPIKey, "", "", 0, "")
	if err != nil {
		log.Printf("[Sub2APIRateSync] list accounts failed: %v", err)
		return
	}

	proxyURLs, proxyErrors := s.resolveAccountProxyURLs(ctx, accounts)
	groups := make(map[string][]sub2APISyncTarget)
	for i := range accounts {
		if !accounts[i].IsSub2APIUpstream() {
			continue
		}
		if accounts[i].ProxyID != nil {
			if err := proxyErrors[*accounts[i].ProxyID]; err != nil {
				if recErr := s.recordSyncError(ctx, &accounts[i], err); recErr != nil {
					log.Printf("[Sub2APIRateSync] record account error failed: account=%d err=%v", accounts[i].ID, recErr)
				}
				continue
			}
		}
		target, err := s.newSub2APISyncTarget(ctx, accounts[i], proxyURLs[proxyIDValue(accounts[i].ProxyID)])
		if err != nil {
			if recErr := s.recordSyncError(ctx, &accounts[i], err); recErr != nil {
				log.Printf("[Sub2APIRateSync] record account error failed: account=%d err=%v", accounts[i].ID, recErr)
			}
			continue
		}
		groups[target.groupKey()] = append(groups[target.groupKey()], target)
	}

	jobs := make(chan []sub2APISyncTarget)
	var wg sync.WaitGroup
	workers := s.concurrency
	if workers <= 0 {
		workers = 5
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for targets := range jobs {
				if len(targets) == 0 {
					continue
				}
				session, refreshedTokens, err := s.fetchUserLoginSession(ctx, targets[0])
				if err != nil {
					for i := range targets {
						if recErr := s.recordSyncError(ctx, &targets[i].account, err); recErr != nil {
							log.Printf("[Sub2APIRateSync] record account error failed: account=%d err=%v", targets[i].account.ID, recErr)
						}
					}
					continue
				}
				skipIDs := map[int64]struct{}{}
				if refreshedTokens != nil {
					for i := range targets {
						if err := s.saveRefreshedSub2APITokens(ctx, &targets[i].account, *refreshedTokens); err != nil {
							skipIDs[targets[i].account.ID] = struct{}{}
							if recErr := s.recordSyncError(ctx, &targets[i].account, err); recErr != nil {
								log.Printf("[Sub2APIRateSync] record account error failed: account=%d err=%v", targets[i].account.ID, recErr)
							}
						}
					}
				}
				for i := range targets {
					if _, skip := skipIDs[targets[i].account.ID]; skip {
						continue
					}
					if err := s.syncTargetWithSession(ctx, targets[i], session); err != nil {
						log.Printf("[Sub2APIRateSync] sync account failed: account=%d err=%v", targets[i].account.ID, err)
					}
				}
			}
		}()
	}

	for _, targets := range groups {
		select {
		case jobs <- targets:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		}
	}
	close(jobs)
	wg.Wait()
}

func newSub2APISyncTarget(account Account, proxyURL string) (sub2APISyncTarget, error) {
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	email := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginEmail))
	password := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APILoginPassword))
	accessToken := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APIAccessToken))
	refreshToken := strings.TrimSpace(account.GetCredential(AccountCredentialSub2APIRefreshToken))
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
		if accessToken == "" {
			return sub2APISyncTarget{}, fmt.Errorf("missing sub2api access token")
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
		account:      account,
		rootURL:      rootURL,
		adapter:      adapter,
		email:        email,
		password:     password,
		accessToken:  accessToken,
		refreshToken: refreshToken,
		apiKey:       apiKey,
		proxyID:      account.ProxyID,
		proxyURL:     normalizedProxyURL,
	}, nil
}

func (s *Sub2APIUpstreamRateSyncService) newSub2APISyncTarget(ctx context.Context, account Account, proxyURL string) (sub2APISyncTarget, error) {
	if account.UpstreamConfigID == nil || account.UpstreamKeyID == nil {
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
	if account.Credentials == nil {
		account.Credentials = map[string]any{}
	}
	account.Credentials["base_url"] = cfg.BaseURL
	account.Credentials["api_key"] = key.Key
	for k, v := range cfg.Credentials {
		account.Credentials[k] = v
	}
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
		return nil, nil, err
	}
	return session, refreshed, nil
}

func testSub2APIUpstreamConfig(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error {
	_, _, err := syncSub2APIUpstreamKeys(ctx, cfg, proxyURL)
	return err
}

func syncSub2APIUpstreamKeys(ctx context.Context, cfg *UpstreamConfig, proxyURL string) ([]UpstreamKey, *sub2APIRefreshData, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("missing upstream config")
	}
	account := Account{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": cfg.BaseURL, "api_key": "sync-only"},
		Extra: map[string]any{
			AccountUpstreamProviderKey:       cfg.Provider,
			AccountSub2APIRateSyncAdapterKey: cfg.AuthMode,
		},
		ProxyID: cfg.ProxyID,
	}
	for k, v := range cfg.Credentials {
		account.Credentials[k] = v
	}
	target, err := newSub2APISyncTarget(account, proxyURL)
	if err != nil {
		return nil, nil, err
	}
	svc := &Sub2APIUpstreamRateSyncService{}
	session, refreshed, err := svc.fetchUserLoginSession(ctx, target)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	out := make([]UpstreamKey, 0, len(session.keys))
	for _, upstreamKey := range session.keys {
		key := strings.TrimSpace(upstreamKey.Key)
		if key == "" || strings.Contains(key, "*") {
			continue
		}
		rate, _, err := resolveSub2APIEffectiveRate(key, session)
		if err != nil {
			return nil, nil, err
		}
		name := normalizeUpstreamDisplayName(upstreamKey.Name, 100)
		groupID := effectiveSub2APIGroupID(upstreamKey)
		groupName := ""
		platform := PlatformOpenAI
		if upstreamKey.Group != nil {
			groupName = normalizeUpstreamDisplayName(upstreamKey.Group.Name, 100)
			if strings.TrimSpace(upstreamKey.Group.Platform) != "" {
				platform = strings.TrimSpace(upstreamKey.Group.Platform)
			}
		}
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
		})
	}
	return out, refreshed, nil
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
	return logredact.RedactText(text, "api_key", "jwt", "authorization")
}

func (s *Sub2APIUpstreamRateSyncService) fetchSessionWithToken(ctx context.Context, client *http.Client, rootURL, token string) (*sub2APIUserLoginSession, error) {
	keys, err := s.fetchSub2APIKeys(ctx, client, rootURL, token)
	if err != nil {
		return nil, err
	}
	rates, err := s.fetchSub2APIGroupRates(ctx, client, rootURL, token)
	if err != nil {
		return nil, err
	}
	return &sub2APIUserLoginSession{keys: keys, rates: rates}, nil
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
	status, err := s.doJSON(ctx, client, http.MethodPost, endpoint, "", map[string]string{
		"email":    target.email,
		"password": target.password,
	}, &payload)
	if err != nil {
		return "", fmt.Errorf("login request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("login returned status %d", status)
	}
	if payload.Code != 0 {
		return "", fmt.Errorf("login failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
	}
	if payload.Data.Requires2FA {
		return "", fmt.Errorf("sub2api login requires 2fa")
	}
	if strings.TrimSpace(payload.Data.AccessToken) == "" {
		return "", fmt.Errorf("sub2api login returned no access token")
	}
	return strings.TrimSpace(payload.Data.AccessToken), nil
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
	payload.Data.AccessToken = strings.TrimSpace(payload.Data.AccessToken)
	payload.Data.RefreshToken = strings.TrimSpace(payload.Data.RefreshToken)
	if payload.Data.AccessToken == "" {
		return nil, fmt.Errorf("refresh returned no access token")
	}
	if payload.Data.RefreshToken == "" {
		return nil, fmt.Errorf("refresh returned no refresh token")
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
		return s.upstreamConfigRepo.SaveRefreshedTokens(ctx, *account.UpstreamConfigID, strings.TrimSpace(tokens.AccessToken), strings.TrimSpace(tokens.RefreshToken))
	}
	credentialUpdates := map[string]any{
		AccountCredentialSub2APIAccessToken:  strings.TrimSpace(tokens.AccessToken),
		AccountCredentialSub2APIRefreshToken: strings.TrimSpace(tokens.RefreshToken),
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

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIKeys(ctx context.Context, client *http.Client, rootURL, token string) ([]sub2APIUpstreamKey, error) {
	keys, err := s.fetchSub2APIKeysFromPath(ctx, client, rootURL, token, sub2APIKeysPath)
	if errors.Is(err, errSub2APIEndpointNotFound) {
		return s.fetchSub2APIKeysFromPath(ctx, client, rootURL, token, sub2APIKeysFallback)
	}
	return keys, err
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIKeysFromPath(ctx context.Context, client *http.Client, rootURL, token, path string) ([]sub2APIUpstreamKey, error) {
	out := make([]sub2APIUpstreamKey, 0)
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
		out = append(out, payload.Data.Items...)

		pages := payload.Data.Pages
		if pages > 0 {
			if page >= pages {
				return out, nil
			}
			continue
		}
		if len(payload.Data.Items) < sub2APIKeysPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("api key list exceeded max pages")
}

func (s *Sub2APIUpstreamRateSyncService) fetchSub2APIGroupRates(ctx context.Context, client *http.Client, rootURL, token string) (map[int64]float64, error) {
	endpoint, err := buildSub2APIURL(rootURL, sub2APIGroupRatesPath)
	if err != nil {
		return nil, err
	}
	var payload sub2APIEnvelope[map[string]float64]
	status, err := s.doJSON(ctx, client, http.MethodGet, endpoint, token, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("list group rates failed: %w", err)
	}
	if status < 200 || status >= 300 {
		if status == http.StatusUnauthorized {
			return nil, errSub2APIAccessTokenMayBeStale
		}
		if status == http.StatusForbidden {
			return nil, fmt.Errorf("sub2api access token was rejected or blocked by upstream")
		}
		return nil, fmt.Errorf("list group rates returned status %d", status)
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("list group rates failed: code %d%s", payload.Code, safeEnvelopeReason(payload.Reason))
	}
	rates := make(map[int64]float64, len(payload.Data))
	for rawID, rate := range payload.Data {
		var groupID int64
		if _, err := fmt.Sscanf(rawID, "%d", &groupID); err != nil || groupID <= 0 {
			continue
		}
		rates[groupID] = rate
	}
	return rates, nil
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
	_, err = s.accountRepo.BulkUpdate(ctx, []int64{target.account.ID}, AccountBulkUpdate{
		RateMultiplier: &multiplier,
		Priority:       &priority,
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
	if groupID == nil || key.Group == nil {
		return 0, "", fmt.Errorf("matched api key is not bound to a group")
	}
	multiplier := key.Group.RateMultiplier
	if session.rates != nil {
		if userRate, ok := session.rates[*groupID]; ok {
			multiplier = userRate
		}
	}
	return multiplier, key.Group.Platform, nil
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
	req.Header.Set("User-Agent", "sub2api-rate-sync/1.0")
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
