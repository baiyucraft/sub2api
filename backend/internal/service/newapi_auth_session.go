package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	newAPIRefreshPath           = "/api/user/auth/refresh"
	newAPIRefreshCookieName     = "new_api_refresh"
	newAPIMaxSessionCookies     = 64
	newAPIMaxSessionCookieBytes = 128 << 10
)

var errNewAPIRefreshUnsupported = errors.New("newapi session has no refresh cookie")

// Retain cookie attributes separately: net/http's CookieJar.Cookies intentionally
// omits Path and expiry, which are required to restore a refresh cookie safely.
type newAPISavedCookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Path     string    `json:"path"`
	Domain   string    `json:"domain,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"http_only,omitempty"`
	Expires  time.Time `json:"expires,omitempty"`
}

type newAPISessionCookieJar struct {
	jar    http.CookieJar
	origin *url.URL
	mu     sync.Mutex
	saved  map[string]newAPISavedCookie
}

func newAPICookieJar(root string) (*newAPISessionCookieJar, error) {
	origin, err := url.Parse(root)
	if err != nil || origin.Host == "" {
		return nil, errors.New("invalid newapi cookie origin")
	}
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	return &newAPISessionCookieJar{jar: jar, origin: origin, saved: make(map[string]newAPISavedCookie)}, nil
}

func (j *newAPISessionCookieJar) Cookies(u *url.URL) []*http.Cookie {
	if !j.sameOrigin(u) {
		return nil
	}
	return j.jar.Cookies(u)
}

func (j *newAPISessionCookieJar) sameOrigin(u *url.URL) bool {
	return u != nil && strings.EqualFold(u.Scheme, j.origin.Scheme) && strings.EqualFold(u.Host, j.origin.Host)
}

func (j *newAPISessionCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if !j.sameOrigin(u) {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	for key, c := range j.saved {
		if !c.Expires.IsZero() && !c.Expires.After(now) {
			delete(j.saved, key)
		}
	}
	for _, c := range cookies {
		if c == nil || c.Valid() != nil || len(c.Name)+len(c.Value)+len(c.Path)+len(c.Domain) > 4<<10 {
			continue
		}
		domain := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		host := strings.ToLower(u.Hostname())
		suffix, _ := publicsuffix.PublicSuffix(domain)
		if domain != "" && (host != domain && !strings.HasSuffix(host, "."+domain) || suffix == domain && host != domain) {
			continue
		}
		path := c.Path
		if !strings.HasPrefix(path, "/") {
			path = "/"
			if last := strings.LastIndex(u.Path, "/"); last > 0 {
				path = u.Path[:last]
			}
		}
		effectiveDomain := domain
		if effectiveDomain == "" {
			effectiveDomain = host
		}
		key := effectiveDomain + "\x00" + path + "\x00" + c.Name
		expires := c.Expires
		if c.MaxAge > 0 {
			expires = now.Add(time.Duration(c.MaxAge) * time.Second)
		}
		if c.MaxAge < 0 || !expires.IsZero() && !expires.After(now) {
			delete(j.saved, key)
			j.jar.SetCookies(u, []*http.Cookie{c})
			continue
		}
		if _, exists := j.saved[key]; !exists && len(j.saved) >= newAPIMaxSessionCookies {
			continue
		}
		candidate := newAPISavedCookie{Name: c.Name, Value: c.Value, Path: path, Domain: c.Domain, Secure: c.Secure, HTTPOnly: c.HttpOnly, Expires: expires}
		previous, exists := j.saved[key]
		j.saved[key] = candidate
		encoded, err := json.Marshal(j.saved)
		if err != nil || len(encoded) > newAPIMaxSessionCookieBytes {
			if exists {
				j.saved[key] = previous
			} else {
				delete(j.saved, key)
			}
			continue
		}
		j.jar.SetCookies(u, []*http.Cookie{c})
	}
}

func (j *newAPISessionCookieJar) snapshot() []newAPISavedCookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]newAPISavedCookie, 0, len(j.saved))
	now := time.Now()
	for _, c := range j.saved {
		if c.Expires.IsZero() || c.Expires.After(now) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, k int) bool {
		return out[i].Domain+"\x00"+out[i].Path+"\x00"+out[i].Name < out[k].Domain+"\x00"+out[k].Path+"\x00"+out[k].Name
	})
	return out
}

func restoreNewAPICookies(session *newAPISession, raw any) error {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil || len(data) > newAPIMaxSessionCookieBytes {
		return errors.New("invalid newapi saved cookies")
	}
	var cookies []newAPISavedCookie
	if err := json.Unmarshal(data, &cookies); err != nil || len(cookies) > newAPIMaxSessionCookies {
		return errors.New("invalid newapi saved cookies")
	}
	root, _ := url.Parse(session.rootURL)
	for _, c := range cookies {
		if c.Name == "" || !strings.HasPrefix(c.Path, "/") {
			return errors.New("invalid newapi saved cookie")
		}
		session.client.Jar.SetCookies(root, []*http.Cookie{{Name: c.Name, Value: c.Value, Path: c.Path, Domain: c.Domain, Secure: c.Secure, HttpOnly: c.HTTPOnly, Expires: c.Expires}})
	}
	return nil
}

func setNewAPIAuthMetadata(session *newAPISession, data newAPILoginData) {
	session.expiresAt = sub2APIJWTExpiresAt(strings.TrimSpace(strings.TrimPrefix(newAPIBearerAuthorization(data.AccessToken), "Bearer ")))
	if data.AccessExpiresAt > 0 {
		expires := time.Unix(data.AccessExpiresAt, 0).UTC()
		session.expiresAt = &expires
	}
	if data.Session != nil && len(data.Session.SID) <= 128 {
		session.sessionID = data.Session.SID
	}
	if session.sessionID == "" {
		session.sessionID = newAPITokenSessionID(data.AccessToken)
	}
}

func newAPITokenSessionID(token string) string {
	if len(token) > 16<<10 {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(newAPIBearerAuthorization(token), "Bearer "), ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(raw) > 8<<10 {
		return ""
	}
	var claims struct {
		SID string `json:"sid"`
	}
	if json.Unmarshal(raw, &claims) != nil || len(claims.SID) > 128 {
		return ""
	}
	return claims.SID
}

func restrictNewAPIAuthRedirects(client *http.Client, rootURL string) {
	origin, _ := url.Parse(rootURL)
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !strings.EqualFold(req.URL.Scheme, origin.Scheme) || !strings.EqualFold(req.URL.Host, origin.Host) {
			return errors.New("newapi authentication cross-origin redirect refused")
		}
		if len(via) >= 10 {
			return errors.New("newapi authentication redirect limit reached")
		}
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
}

func (a newAPIUpstreamProviderAdapter) refreshSession(ctx context.Context, session *newAPISession) error {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIRefreshPath)
	if err != nil {
		return err
	}
	u, _ := url.Parse(endpoint)
	hasRefresh := false
	if session.client.Jar != nil {
		for _, c := range session.client.Jar.Cookies(u) {
			if c.Name == newAPIRefreshCookieName && c.Value != "" {
				hasRefresh = true
			}
		}
	}
	if !hasRefresh {
		return errNewAPIRefreshUnsupported
	}
	// Refresh is cookie-authenticated. Do not carry the expired Bearer or a
	// fixed Cookie header that could hide a rotated refresh cookie in the jar.
	client := *session.client
	if transport, ok := client.Transport.(newAPIAuthTransport); ok {
		client.Transport = transport.base
	}
	origin, _ := url.Parse(session.rootURL)
	headers := http.Header{"Origin": []string{origin.Scheme + "://" + origin.Host}}
	if session.sessionID != "" {
		headers.Set("X-Auth-Session", session.sessionID)
	}
	var payload newAPIEnvelope[newAPILoginData]
	status, err := a.doJSONWithHeaders(ctx, &client, http.MethodPost, endpoint, session.userID, map[string]any{}, &payload, headers)
	if err != nil {
		return fmt.Errorf("newapi refresh request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return newAPIStatusError("refresh", http.MethodPost, newAPIRefreshPath, status, payload.Code, payload.Message)
	}
	if !payload.Success || strings.TrimSpace(payload.Data.AccessToken) == "" {
		return errors.New("newapi refresh returned no usable access token")
	}
	if payload.Data.User != nil && payload.Data.User.ID != session.userID || payload.Data.ID != 0 && payload.Data.ID != session.userID {
		return errors.New("newapi refresh returned a different user")
	}
	if payload.Data.Session != nil && (payload.Data.Session.SID == "" || len(payload.Data.Session.SID) > 128) {
		return errors.New("newapi refresh returned an invalid session")
	}
	metadata := &newAPISession{}
	setNewAPIAuthMetadata(metadata, payload.Data)
	if session.sessionID != "" && metadata.sessionID != "" && metadata.sessionID != session.sessionID {
		return errors.New("newapi refresh returned a different session")
	}
	if metadata.expiresAt != nil && !metadata.expiresAt.After(time.Now().UTC()) {
		return errors.New("newapi refresh returned an expired access token")
	}
	base := session.client.Transport
	if transport, ok := base.(newAPIAuthTransport); ok {
		base = transport.base
	}
	session.client.Transport = newAPIAuthTransport{base: base, accessToken: newAPIBearerAuthorization(payload.Data.AccessToken)}
	setNewAPIAuthMetadata(session, payload.Data)
	return nil
}

type newAPIHTTPError struct {
	Operation string
	Method    string
	Path      string
	Status    int
	Code      string
	Message   string
}

func (e *newAPIHTTPError) Error() string {
	code := ""
	if e.Code != "" {
		code = " [" + e.Code + "]"
	}
	return fmt.Sprintf("newapi %s returned status %d%s%s", e.Operation, e.Status, code, safeNewAPIMessage(e.Message))
}

func newAPIStatusError(operation, method, path string, status int, rawCode any, message string) error {
	code, _ := rawCode.(string)
	// Error codes are untrusted input; only publish reviewed protocol constants.
	switch code {
	case "AUTH_SESSION_LIMIT", "AUTH_SESSION_ISSUANCE_LIMIT", "AUTH_SESSION_MISMATCH", "AUTH_REFRESH_RACE", "AUTH_TOKEN_EXPIRED", "AUTH_SESSION_REVOKED", "AUTH_UNAUTHORIZED", "AUTH_ORIGIN_FORBIDDEN", "AUTH_USER_DISABLED", "AUTH_INSUFFICIENT_PRIVILEGE", "AUTH_INTERNAL_ERROR":
	default:
		code = ""
	}
	return &newAPIHTTPError{Operation: operation, Method: method, Path: path, Status: status, Code: code, Message: strings.TrimPrefix(safeNewAPIMessage(message), ": ")}
}
