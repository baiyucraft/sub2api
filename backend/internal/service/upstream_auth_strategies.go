package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

type upstreamAuthContextKey struct{}

func withUpstreamAuthHandle(ctx context.Context, handle *UpstreamAuthHandle) context.Context {
	return context.WithValue(ctx, upstreamAuthContextKey{}, handle)
}

func upstreamAuthHandleFromContext(ctx context.Context) (*UpstreamAuthHandle, bool) {
	handle, ok := ctx.Value(upstreamAuthContextKey{}).(*UpstreamAuthHandle)
	return handle, ok && handle != nil
}

type newAPIAuthValue struct {
	Session     *newAPISession
	Cookie      string
	AccessToken string
	UserID      int64
}

type newAPIAuthStrategy struct{ adapter newAPIUpstreamProviderAdapter }

func upstreamAuthStrategyFor(cfg *UpstreamConfig, svc *UpstreamConfigService) UpstreamAuthStrategy {
	if cfg == nil {
		return nil
	}
	switch normalizeUpstreamProvider(cfg.Provider) {
	case UpstreamProviderNewAPI:
		return newAPIAuthStrategy{adapter: newAPIUpstreamProviderAdapter{}}
	case UpstreamProviderLCodex:
		return lcodexAuthStrategy{adapter: lcodexUpstreamProviderAdapter{}}
	case UpstreamProviderSub2API:
		return sub2APIAuthStrategy{service: &Sub2APIUpstreamRateSyncService{upstreamConfigRepo: svc.repo}}
	default:
		return nil
	}
}

func (s newAPIAuthStrategy) Fingerprint(cfg *UpstreamConfig) string {
	return UpstreamAuthCredentialFingerprint(cfg)
}
func (s newAPIAuthStrategy) CanLogin(cfg *UpstreamConfig) bool {
	return cfg != nil && cfg.AuthMode != UpstreamAuthModeCookie && cfg.AuthMode != UpstreamAuthModeAccessToken
}
func (s newAPIAuthStrategy) Seed(ctx context.Context, cfg *UpstreamConfig, proxy string) (*UpstreamAuthHandle, error) {
	if s.CanLogin(cfg) {
		return nil, nil
	}
	session, err := s.adapter.login(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	return newAPIHandle(session), nil
}
func (s newAPIAuthStrategy) Restore(ctx context.Context, cfg *UpstreamConfig, proxy string, secret *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error) {
	if secret == nil || secret.Provider != UpstreamProviderNewAPI {
		return nil, errors.New("invalid newapi session secret")
	}
	cookie, _ := secret.Data["cookie"].(string)
	token, _ := secret.Data["access_token"].(string)
	userID := int64FromAny(secret.Data["user_id"])
	if userID <= 0 {
		return nil, errors.New("newapi session has no user id")
	}
	session, err := newAPIAuthSession(ctx, cfg, proxy, userID, cookie, token)
	if err != nil {
		return nil, err
	}
	return newAPIHandle(session), nil
}
func (s newAPIAuthStrategy) Login(ctx context.Context, cfg *UpstreamConfig, proxy string) (*UpstreamAuthHandle, error) {
	session, err := s.adapter.login(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	handle := newAPIHandle(session)
	handle.Authenticated = true
	return handle, nil
}
func (s newAPIAuthStrategy) Refresh(context.Context, *UpstreamConfig, string, *UpstreamAuthHandle) (*UpstreamAuthHandle, error) {
	return nil, errors.New("newapi refresh is not supported")
}
func (s newAPIAuthStrategy) Serialize(handle *UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error) {
	value, ok := handle.Value.(newAPIAuthValue)
	if !ok || value.Session == nil {
		return nil, errors.New("invalid newapi auth handle")
	}
	return &UpstreamAuthSessionSecret{Provider: UpstreamProviderNewAPI, Data: map[string]any{"cookie": value.Cookie, "access_token": value.AccessToken, "user_id": value.UserID}}, nil
}
func (s newAPIAuthStrategy) ClassifyAuthError(err error) UpstreamAuthErrorCategory {
	return classifyHTTPAuthError(err)
}

func newAPIHandle(session *newAPISession) *UpstreamAuthHandle {
	if session == nil {
		return nil
	}
	cookie, token := "", ""
	if session.client != nil && session.client.Jar != nil {
		if u, err := url.Parse(session.rootURL); err == nil {
			parts := make([]string, 0, len(session.client.Jar.Cookies(u)))
			for _, item := range session.client.Jar.Cookies(u) {
				parts = append(parts, item.Name+"="+item.Value)
			}
			cookie = strings.Join(parts, "; ")
		}
	}
	if session.client != nil {
		if transport, ok := session.client.Transport.(newAPIAuthTransport); ok {
			if cookie == "" {
				cookie = strings.TrimSpace(transport.cookie)
			}
			token = transport.accessToken
		}
	}
	return &UpstreamAuthHandle{Value: newAPIAuthValue{Session: session, Cookie: cookie, AccessToken: token, UserID: session.userID}}
}

func newAPIAuthSession(_ context.Context, cfg *UpstreamConfig, proxy string, userID int64, cookie, token string) (*newAPISession, error) {
	rootURL, err := normalizeSub2APIBaseURL(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	normalizedProxyURL, err := normalizeSub2APIProxyURL(proxy)
	if err != nil {
		return nil, err
	}
	client, err := sub2APIHTTPClient(normalizedProxyURL)
	if err != nil {
		return nil, err
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	if cookie != "" {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return nil, jarErr
		}
		client.Jar = jar
		parsed, parseErr := url.Parse(rootURL)
		if parseErr != nil {
			return nil, parseErr
		}
		for _, part := range strings.Split(cookie, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 {
				client.Jar.SetCookies(parsed, []*http.Cookie{{Name: strings.TrimSpace(kv[0]), Value: strings.TrimSpace(kv[1])}})
			}
		}
	}
	if token != "" {
		client.Transport = newAPIAuthTransport{base: base, cookie: cookie, accessToken: newAPIBearerAuthorization(token)}
	}
	return &newAPISession{rootURL: rootURL, userID: userID, client: client}, nil
}

type lcodexAuthValue struct{ Session *lcodexSession }
type lcodexAuthStrategy struct{ adapter lcodexUpstreamProviderAdapter }

func (s lcodexAuthStrategy) Fingerprint(cfg *UpstreamConfig) string {
	return UpstreamAuthCredentialFingerprint(cfg)
}
func (s lcodexAuthStrategy) CanLogin(*UpstreamConfig) bool { return true }
func (s lcodexAuthStrategy) Seed(context.Context, *UpstreamConfig, string) (*UpstreamAuthHandle, error) {
	return nil, nil
}
func (s lcodexAuthStrategy) Restore(ctx context.Context, cfg *UpstreamConfig, proxy string, secret *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error) {
	if secret == nil || secret.Provider != UpstreamProviderLCodex {
		return nil, errors.New("invalid lcodex session secret")
	}
	access, _ := secret.Data["access_token"].(string)
	refresh, _ := secret.Data["refresh_token"].(string)
	client, err := newLCodexHTTPClient(proxy)
	if err != nil {
		return nil, err
	}
	root, err := normalizeLCodexRootURL(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	return &UpstreamAuthHandle{Value: lcodexAuthValue{Session: &lcodexSession{rootURL: root, client: client, accessToken: access, refreshToken: refresh, refreshAttempt: true}}}, nil
}
func (s lcodexAuthStrategy) Login(ctx context.Context, cfg *UpstreamConfig, proxy string) (*UpstreamAuthHandle, error) {
	root, err := normalizeLCodexRootURL(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	client, err := newLCodexHTTPClient(proxy)
	if err != nil {
		return nil, err
	}
	session, err := s.adapter.login(ctx, client, root, cfg.Credentials)
	if err != nil {
		return nil, err
	}
	session.refreshAttempt = true
	return &UpstreamAuthHandle{Value: lcodexAuthValue{Session: session}, Authenticated: true}, nil
}
func (s lcodexAuthStrategy) Refresh(ctx context.Context, _ *UpstreamConfig, _ string, handle *UpstreamAuthHandle) (*UpstreamAuthHandle, error) {
	value, ok := handle.Value.(lcodexAuthValue)
	if !ok || value.Session == nil {
		return nil, errors.New("invalid lcodex auth handle")
	}
	if err := value.Session.refresh(ctx); err != nil {
		return nil, err
	}
	return &UpstreamAuthHandle{Value: value, Refreshed: true}, nil
}
func (s lcodexAuthStrategy) Serialize(handle *UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error) {
	value, ok := handle.Value.(lcodexAuthValue)
	if !ok || value.Session == nil {
		return nil, errors.New("invalid lcodex auth handle")
	}
	return &UpstreamAuthSessionSecret{Provider: UpstreamProviderLCodex, Data: map[string]any{"access_token": value.Session.accessToken, "refresh_token": value.Session.refreshToken}}, nil
}
func (s lcodexAuthStrategy) ClassifyAuthError(err error) UpstreamAuthErrorCategory {
	return classifyHTTPAuthError(err)
}

type sub2APIAuthValue struct {
	Session      *sub2APIUserLoginSession
	AccessToken  string
	RefreshToken string
}
type sub2APIAuthStrategy struct {
	service *Sub2APIUpstreamRateSyncService
}

func (s sub2APIAuthStrategy) Fingerprint(cfg *UpstreamConfig) string {
	return UpstreamAuthCredentialFingerprint(cfg)
}
func (s sub2APIAuthStrategy) CanLogin(cfg *UpstreamConfig) bool {
	return cfg != nil && cfg.AuthMode != UpstreamAuthModeManualJWT
}
func (s sub2APIAuthStrategy) Seed(ctx context.Context, cfg *UpstreamConfig, proxy string) (*UpstreamAuthHandle, error) {
	if s.CanLogin(cfg) {
		return nil, nil
	}
	target, err := s.target(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	client, err := sub2APIHTTPClient(target.proxyURL)
	if err != nil {
		return nil, err
	}
	var refreshedTokens *sub2APIRefreshData
	if target.accessToken == "" && target.refreshToken != "" {
		refreshed, refreshErr := s.service.refreshSub2APIToken(ctx, client, target)
		if refreshErr != nil {
			return nil, refreshErr
		}
		refreshedTokens = refreshed
		target.accessToken, target.refreshToken, target.tokenExpiresAt = refreshed.AccessToken, refreshed.RefreshToken, refreshed.ExpiresAt
	}
	if target.accessToken == "" {
		return nil, errors.New("sub2api access token unavailable")
	}
	session, err := s.service.fetchSessionWithToken(ctx, client, target.rootURL, target.accessToken)
	if err != nil {
		return nil, err
	}
	return &UpstreamAuthHandle{Value: sub2APIAuthValue{Session: session, AccessToken: target.accessToken, RefreshToken: target.refreshToken}, ExpiresAt: target.tokenExpiresAt, Refreshed: refreshedTokens != nil}, nil
}
func (s sub2APIAuthStrategy) target(ctx context.Context, cfg *UpstreamConfig, proxy string) (sub2APISyncTarget, error) {
	account := Account{ID: cfg.ID, Name: cfg.Name, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": cfg.SiteURL, "api_key": "sync-only"}, Extra: map[string]any{AccountUpstreamProviderKey: cfg.Provider, AccountSub2APIRateSyncAdapterKey: cfg.AuthMode}, ProxyID: cfg.ProxyID}
	for k, v := range cfg.Credentials {
		account.Credentials[k] = v
	}
	account.Credentials[SettingKeyUpstreamSub2APINotInCNConfirmed] = cfg.Sub2APINotInCNConfirmed
	return s.service.newSub2APISyncTarget(ctx, account, proxy)
}
func (s sub2APIAuthStrategy) Restore(ctx context.Context, cfg *UpstreamConfig, proxy string, secret *UpstreamAuthSessionSecret) (*UpstreamAuthHandle, error) {
	if secret == nil || secret.Provider != UpstreamProviderSub2API {
		return nil, errors.New("invalid sub2api session secret")
	}
	token, _ := secret.Data["access_token"].(string)
	refresh, _ := secret.Data["refresh_token"].(string)
	target, err := s.target(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	// The persisted auth-session secret is authoritative during restore. For
	// user-login configs the current upstream config intentionally stores only
	// the email/password, so the refresh token is not present in target until
	// we copy it from the encrypted session secret.
	target.accessToken = strings.TrimSpace(token)
	target.refreshToken = strings.TrimSpace(refresh)
	client, err := sub2APIHTTPClient(target.proxyURL)
	if err != nil {
		return nil, err
	}
	session, err := s.service.fetchSessionWithToken(ctx, client, target.rootURL, token)
	refreshed := false
	if errors.Is(err, errSub2APIAccessTokenMayBeStale) && strings.TrimSpace(refresh) != "" {
		refreshedTokens, refreshErr := s.service.refreshSub2APIToken(ctx, client, target)
		if refreshErr != nil {
			return nil, fmt.Errorf("refresh sub2api token failed: %w", refreshErr)
		}
		token, refresh = refreshedTokens.AccessToken, refreshedTokens.RefreshToken
		target.tokenExpiresAt = refreshedTokens.ExpiresAt
		session, err = s.service.fetchSessionWithToken(ctx, client, target.rootURL, token)
		refreshed = true
	}
	if err != nil {
		return nil, err
	}
	handle := &UpstreamAuthHandle{Value: sub2APIAuthValue{Session: session, AccessToken: token, RefreshToken: refresh}, ExpiresAt: target.tokenExpiresAt, Refreshed: refreshed}
	handle.Authenticated = true
	return handle, nil
}
func (s sub2APIAuthStrategy) Login(ctx context.Context, cfg *UpstreamConfig, proxy string) (*UpstreamAuthHandle, error) {
	target, err := s.target(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	client, err := sub2APIHTTPClient(target.proxyURL)
	if err != nil {
		return nil, err
	}
	loginData, err := s.service.loginSub2APIUserTokens(ctx, client, target)
	if err != nil {
		return nil, err
	}
	session, err := s.service.fetchSessionWithToken(ctx, client, target.rootURL, loginData.AccessToken)
	if err != nil {
		return nil, err
	}
	return &UpstreamAuthHandle{Value: sub2APIAuthValue{Session: session, AccessToken: loginData.AccessToken, RefreshToken: loginData.RefreshToken}, ExpiresAt: loginData.ExpiresAt, Authenticated: true}, nil
}
func (s sub2APIAuthStrategy) Refresh(ctx context.Context, cfg *UpstreamConfig, proxy string, handle *UpstreamAuthHandle) (*UpstreamAuthHandle, error) {
	value, ok := handle.Value.(sub2APIAuthValue)
	if !ok {
		return nil, errors.New("invalid sub2api auth handle")
	}
	target, err := s.target(ctx, cfg, proxy)
	if err != nil {
		return nil, err
	}
	if value.RefreshToken == "" {
		return nil, errors.New("sub2api refresh token unavailable")
	}
	// For restored user-login sessions the current upstream config stores only
	// email/password; the usable refresh token lives in the encrypted session
	// handle. Pass that token into the refresh request target explicitly.
	target.accessToken = strings.TrimSpace(value.AccessToken)
	target.refreshToken = strings.TrimSpace(value.RefreshToken)
	client, err := sub2APIHTTPClient(target.proxyURL)
	if err != nil {
		return nil, err
	}
	refreshed, err := s.service.refreshSub2APIToken(ctx, client, target)
	if err != nil {
		return nil, err
	}
	session, err := s.service.fetchSessionWithToken(ctx, client, target.rootURL, refreshed.AccessToken)
	if err != nil {
		return nil, err
	}
	return &UpstreamAuthHandle{Value: sub2APIAuthValue{Session: session, AccessToken: refreshed.AccessToken, RefreshToken: refreshed.RefreshToken}, ExpiresAt: refreshed.ExpiresAt, Refreshed: true}, nil
}
func (s sub2APIAuthStrategy) Serialize(handle *UpstreamAuthHandle) (*UpstreamAuthSessionSecret, error) {
	value, ok := handle.Value.(sub2APIAuthValue)
	if !ok {
		return nil, errors.New("invalid sub2api auth handle")
	}
	return &UpstreamAuthSessionSecret{Provider: UpstreamProviderSub2API, Data: map[string]any{"access_token": value.AccessToken, "refresh_token": value.RefreshToken}}, nil
}
func (s sub2APIAuthStrategy) ClassifyAuthError(err error) UpstreamAuthErrorCategory {
	return classifyHTTPAuthError(err)
}

func classifyHTTPAuthError(err error) UpstreamAuthErrorCategory {
	if err == nil {
		return UpstreamAuthErrorUnknown
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "409") || strings.Contains(text, "session limit"):
		return UpstreamAuthErrorConflict
	case strings.Contains(text, "401") || strings.Contains(text, "unauthorized") || strings.Contains(text, "rejected") || strings.Contains(text, "expired") || strings.Contains(text, "stale"):
		return UpstreamAuthErrorUnauthorized
	case strings.Contains(text, "timeout") || strings.Contains(text, "connection"):
		return UpstreamAuthErrorTransport
	default:
		return UpstreamAuthErrorUnknown
	}
}

func int64FromAny(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case string:
		var n int64
		_, _ = fmt.Sscan(v, &n)
		return n
	default:
		return 0
	}
}
