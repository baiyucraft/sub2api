package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	lcodexPublicSettingsPath = "/api/v1/settings/public"
	lcodexLoginPath          = "/auth/login"
	lcodexRefreshPath        = "/auth/refresh"
	lcodexKeysPath           = "/keys"
	lcodexGroupsPath         = "/groups/available"
	lcodexGroupRatesPath     = "/groups/rates"
	lcodexProfilePath        = "/user/profile"
	lcodexKeyPageSize        = 100
	lcodexMaxKeyPages        = 10000
)

var reLCodexAPIKey = regexp.MustCompile(`(?i)\bsk-[a-z0-9_-]{8,}\b`)

type lcodexUpstreamProviderAdapter struct{}

type lcodexSession struct {
	rootURL        string
	client         *http.Client
	accessToken    string
	refreshToken   string
	refreshAttempt bool
}

type lcodexTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

type lcodexPublicSettings struct {
	APIBaseURL string `json:"api_base_url"`
}

type lcodexGroupRow struct {
	ID                   int64           `json:"id"`
	Name                 string          `json:"name"`
	Platform             string          `json:"platform"`
	RateMultiplier       json.RawMessage `json:"rate_multiplier"`
	AllowImageGeneration json.RawMessage `json:"allow_image_generation"`
}

type lcodexKeyGroupRow struct {
	ID             int64           `json:"id"`
	Name           string          `json:"name"`
	Platform       string          `json:"platform"`
	RateMultiplier json.RawMessage `json:"rate_multiplier"`
}

type lcodexKeyRow struct {
	ID      int64              `json:"id"`
	Key     string             `json:"key"`
	Name    string             `json:"name"`
	GroupID *int64             `json:"group_id"`
	Group   *lcodexKeyGroupRow `json:"group"`
	Status  string             `json:"status"`
}

type lcodexKeyPage struct {
	Items    []lcodexKeyRow `json:"items"`
	Total    int64          `json:"total"`
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Pages    int            `json:"pages"`
}

type lcodexProfile struct {
	ID             int64           `json:"id"`
	Email          string          `json:"email"`
	CreditBalance  json.RawMessage `json:"credit_balance"`
	MaxConcurrency json.RawMessage `json:"max_concurrency"`
	Disabled       bool            `json:"disabled"`
}

type lcodexGroupInfo struct {
	ID                 int64
	Name               string
	Platform           string
	DefaultRate        *float64
	ImageCapability    *bool
	ImageCapabilityBad bool
}

func (lcodexUpstreamProviderAdapter) Provider() string { return UpstreamProviderLCodex }

func (lcodexUpstreamProviderAdapter) ValidateConfig(config *UpstreamConfig, requireSecrets bool) error {
	if config == nil || config.AuthMode != UpstreamAuthModeUserLogin {
		return infraBadRequest("UPSTREAM_AUTH_MODE_INVALID", "lcodex supports user login only")
	}
	if strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialLCodexLoginIdentifier)) == "" {
		return infraBadRequest("UPSTREAM_LCODEX_LOGIN_IDENTIFIER_REQUIRED", "lcodex login identifier is required")
	}
	if requireSecrets && strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialLCodexLoginPassword)) == "" {
		return infraBadRequest("UPSTREAM_LCODEX_LOGIN_PASSWORD_REQUIRED", "lcodex login password is required")
	}
	return nil
}

func (a lcodexUpstreamProviderAdapter) Test(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error {
	_, err := a.SyncSnapshot(ctx, cfg, proxyURL, false)
	return err
}

func (a lcodexUpstreamProviderAdapter) SyncSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*upstreamProviderSnapshot, error) {
	if cfg == nil {
		return nil, fmt.Errorf("missing lcodex upstream config")
	}
	siteURL, err := normalizeLCodexRootURL(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	client, err := newLCodexHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	var discoveredAPIURL *string
	rootURL := siteURL
	if cfg.APIURL != nil && strings.TrimSpace(*cfg.APIURL) != "" {
		rootURL, err = normalizeLCodexRootURL(*cfg.APIURL)
		if err != nil {
			return nil, fmt.Errorf("invalid lcodex api url: %w", err)
		}
	} else {
		discovered, discoverErr := a.discoverAPIURL(ctx, client, siteURL)
		if discoverErr != nil {
			return nil, discoverErr
		}
		rootURL = discovered
		discoveredAPIURL = &discovered
	}
	session, err := a.login(ctx, client, rootURL, cfg.Credentials)
	if err != nil {
		return nil, err
	}
	groups, groupWarnings, err := a.fetchGroups(ctx, session)
	if err != nil {
		return nil, err
	}
	dedicatedRates, rateWarnings, ratesErr := a.fetchGroupRates(ctx, session)
	warnings := append([]string{}, groupWarnings...)
	partial := len(groupWarnings) > 0
	if ratesErr != nil {
		partial = true
		warnings = appendLCodexWarning(warnings, "lcodex dedicated group rates unavailable; using group defaults")
	} else {
		warnings = append(warnings, rateWarnings...)
		partial = partial || len(rateWarnings) > 0
	}
	rows, err := a.fetchKeys(ctx, session)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	keys := make([]UpstreamKey, 0, len(rows))
	for _, row := range rows {
		key, buildWarnings, buildErr := lcodexUpstreamKey(cfg.ID, row, groups, dedicatedRates, now)
		if buildErr != nil {
			return nil, buildErr
		}
		warnings = append(warnings, buildWarnings...)
		partial = partial || len(buildWarnings) > 0
		keys = append(keys, key)
	}
	extraUpdates := map[string]any{}
	if includeProfile {
		profile, profileErr := a.fetchProfile(ctx, session)
		updates, warning := lcodexProfileExtraUpdates(cfg, profile, profileErr)
		for key, value := range updates {
			extraUpdates[key] = value
		}
		if profileErr != nil {
			partial = true
			warnings = appendLCodexWarning(warnings, "lcodex profile snapshot unavailable")
		}
		if warning != "" {
			partial = true
			warnings = appendLCodexWarning(warnings, warning)
		}
	}
	warnings = uniqueLCodexWarnings(warnings)
	extraUpdates["upstream_provider_snapshot_partial"] = partial
	extraUpdates["upstream_provider_snapshot_warnings"] = warnings
	return &upstreamProviderSnapshot{
		Keys: keys, KeysComplete: true, ExtraUpdates: extraUpdates,
		Partial: partial, Warnings: warnings, DiscoveredAPIURL: discoveredAPIURL,
	}, nil
}

func (a lcodexUpstreamProviderAdapter) SanitizeError(err error, credentials map[string]any) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, key := range []string{AccountCredentialLCodexLoginIdentifier, AccountCredentialLCodexLoginPassword} {
		if value := strings.TrimSpace(stringCredential(credentials, key)); value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	text = reLCodexAPIKey.ReplaceAllString(text, "sk-***")
	return logredact.RedactText(text, "password", "api_key", "jwt", "authorization", "bearer", "token", "access_token", "refresh_token")
}

func (a lcodexUpstreamProviderAdapter) discoverAPIURL(ctx context.Context, client *http.Client, rootURL string) (string, error) {
	endpoint, err := buildLCodexURL(rootURL, lcodexPublicSettingsPath, nil)
	if err != nil {
		return "", err
	}
	var settings lcodexPublicSettings
	status, err := doLCodexJSON(ctx, client, http.MethodGet, endpoint, "", nil, &settings)
	if err != nil {
		return "", fmt.Errorf("lcodex public settings request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return "", fmt.Errorf("lcodex public settings returned status %d", status)
	}
	apiURL, err := normalizeUpstreamConfigURL(settings.APIBaseURL)
	if err != nil || strings.TrimSpace(settings.APIBaseURL) == "" {
		return "", fmt.Errorf("lcodex public settings returned invalid api_base_url")
	}
	return apiURL, nil
}

func (a lcodexUpstreamProviderAdapter) login(ctx context.Context, client *http.Client, rootURL string, credentials map[string]any) (*lcodexSession, error) {
	endpoint, err := buildLCodexURL(rootURL, lcodexLoginPath, nil)
	if err != nil {
		return nil, err
	}
	var tokens lcodexTokenResponse
	status, err := doLCodexJSON(ctx, client, http.MethodPost, endpoint, "", map[string]string{
		"email":    strings.TrimSpace(stringCredential(credentials, AccountCredentialLCodexLoginIdentifier)),
		"password": strings.TrimSpace(stringCredential(credentials, AccountCredentialLCodexLoginPassword)),
	}, &tokens)
	if err != nil {
		return nil, fmt.Errorf("lcodex login request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("lcodex login returned status %d", status)
	}
	if strings.TrimSpace(tokens.AccessToken) == "" {
		return nil, fmt.Errorf("lcodex login returned no access token")
	}
	return &lcodexSession{rootURL: rootURL, client: client, accessToken: strings.TrimSpace(tokens.AccessToken), refreshToken: strings.TrimSpace(tokens.RefreshToken)}, nil
}

func (a lcodexUpstreamProviderAdapter) fetchGroups(ctx context.Context, session *lcodexSession) (map[int64]lcodexGroupInfo, []string, error) {
	endpoint, err := buildLCodexURL(session.rootURL, lcodexGroupsPath, nil)
	if err != nil {
		return nil, nil, err
	}
	var raw json.RawMessage
	status, err := session.doJSON(ctx, http.MethodGet, endpoint, nil, &raw)
	if err != nil {
		return nil, nil, fmt.Errorf("lcodex list groups failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, nil, fmt.Errorf("lcodex list groups returned status %d", status)
	}
	rows, err := decodeLCodexGroups(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("lcodex list groups returned incompatible response: %w", err)
	}
	out := make(map[int64]lcodexGroupInfo, len(rows))
	warnings := []string{}
	for _, row := range rows {
		if row.ID <= 0 {
			return nil, nil, fmt.Errorf("lcodex group id must be positive")
		}
		if _, exists := out[row.ID]; exists {
			return nil, nil, fmt.Errorf("lcodex groups returned duplicate id %d", row.ID)
		}
		defaultRate, ratePresent, rateErr := parseLCodexOptionalNonNegativeFloat(row.RateMultiplier)
		if rateErr != nil {
			warnings = appendLCodexWarning(warnings, "lcodex group rate is invalid and was ignored")
			defaultRate, ratePresent = 0, false
		}
		imageValue, imagePresent, imageErr := parseLCodexOptionalBool(row.AllowImageGeneration)
		if imageErr != nil {
			warnings = appendLCodexWarning(warnings, "lcodex image capability is invalid and was ignored")
		}
		info := lcodexGroupInfo{ID: row.ID, Name: normalizeUpstreamDisplayName(row.Name, 100), Platform: strings.ToLower(strings.TrimSpace(row.Platform)), ImageCapabilityBad: imageErr != nil}
		if ratePresent {
			info.DefaultRate = &defaultRate
		}
		if imagePresent && imageErr == nil {
			info.ImageCapability = &imageValue
		}
		out[row.ID] = info
	}
	return out, warnings, nil
}

func (a lcodexUpstreamProviderAdapter) fetchGroupRates(ctx context.Context, session *lcodexSession) (map[int64]float64, []string, error) {
	endpoint, err := buildLCodexURL(session.rootURL, lcodexGroupRatesPath, nil)
	if err != nil {
		return nil, nil, err
	}
	var raw map[string]json.RawMessage
	status, err := session.doJSON(ctx, http.MethodGet, endpoint, nil, &raw)
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status >= 300 {
		return nil, nil, fmt.Errorf("lcodex group rates returned status %d", status)
	}
	out := make(map[int64]float64, len(raw))
	warnings := []string{}
	for rawID, rawValue := range raw {
		id, parseErr := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		value, present, valueErr := parseLCodexOptionalNonNegativeFloat(rawValue)
		if parseErr != nil || id <= 0 || valueErr != nil || !present {
			warnings = appendLCodexWarning(warnings, "lcodex dedicated group rate is invalid and was ignored")
			continue
		}
		out[id] = value
	}
	return out, warnings, nil
}

func (a lcodexUpstreamProviderAdapter) fetchKeys(ctx context.Context, session *lcodexSession) ([]lcodexKeyRow, error) {
	seen := make(map[int64]struct{})
	rows := make([]lcodexKeyRow, 0)
	var expectedTotal int64 = -1
	for page := 1; page <= lcodexMaxKeyPages; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "page_size": {strconv.Itoa(lcodexKeyPageSize)}, "sort_by": {"created_at"}, "sort_order": {"desc"}}
		endpoint, err := buildLCodexURL(session.rootURL, lcodexKeysPath, query)
		if err != nil {
			return nil, err
		}
		var payload lcodexKeyPage
		status, err := session.doJSON(ctx, http.MethodGet, endpoint, nil, &payload)
		if err != nil {
			return nil, fmt.Errorf("lcodex list keys page %d failed: %w", page, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("lcodex list keys page %d returned status %d", page, status)
		}
		if payload.Total < 0 || (payload.Page != 0 && payload.Page != page) || (payload.PageSize != 0 && payload.PageSize != lcodexKeyPageSize) {
			return nil, fmt.Errorf("lcodex keys page %d returned incompatible pagination metadata", page)
		}
		if expectedTotal < 0 {
			expectedTotal = payload.Total
		} else if payload.Total != expectedTotal {
			return nil, fmt.Errorf("lcodex keys total changed during pagination")
		}
		for _, row := range payload.Items {
			if row.ID <= 0 {
				return nil, fmt.Errorf("lcodex key id must be positive")
			}
			if _, exists := seen[row.ID]; exists {
				return nil, fmt.Errorf("lcodex keys returned duplicate id %d", row.ID)
			}
			seen[row.ID] = struct{}{}
			rows = append(rows, row)
		}
		if int64(len(rows)) > expectedTotal {
			return nil, fmt.Errorf("lcodex keys returned more items than total")
		}
		if int64(len(rows)) == expectedTotal {
			return rows, nil
		}
		if len(payload.Items) == 0 || (payload.Pages > 0 && page >= payload.Pages) {
			return nil, fmt.Errorf("lcodex keys pagination ended before total was reached")
		}
	}
	return nil, fmt.Errorf("lcodex keys pagination exceeded %d pages", lcodexMaxKeyPages)
}

func (a lcodexUpstreamProviderAdapter) fetchProfile(ctx context.Context, session *lcodexSession) (*lcodexProfile, error) {
	endpoint, err := buildLCodexURL(session.rootURL, lcodexProfilePath, nil)
	if err != nil {
		return nil, err
	}
	var profile lcodexProfile
	status, err := session.doJSON(ctx, http.MethodGet, endpoint, nil, &profile)
	if err != nil {
		return nil, fmt.Errorf("lcodex profile request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("lcodex profile returned status %d", status)
	}
	if _, present, parseErr := parseLCodexOptionalNonNegativeFloat(profile.CreditBalance); parseErr != nil || !present {
		return nil, fmt.Errorf("lcodex profile returned invalid credit balance")
	}
	return &profile, nil
}

func (s *lcodexSession) doJSON(ctx context.Context, method, endpoint string, body, out any) (int, error) {
	status, err := doLCodexJSON(ctx, s.client, method, endpoint, s.accessToken, body, out)
	if err != nil || status != http.StatusUnauthorized || s.refreshAttempt || strings.TrimSpace(s.refreshToken) == "" {
		return status, err
	}
	s.refreshAttempt = true
	if err := s.refresh(ctx); err != nil {
		return status, err
	}
	return doLCodexJSON(ctx, s.client, method, endpoint, s.accessToken, body, out)
}

func (s *lcodexSession) refresh(ctx context.Context) error {
	endpoint, err := buildLCodexURL(s.rootURL, lcodexRefreshPath, nil)
	if err != nil {
		return err
	}
	var tokens lcodexTokenResponse
	status, err := doLCodexJSON(ctx, s.client, http.MethodPost, endpoint, "", map[string]string{"refresh_token": s.refreshToken}, &tokens)
	if err != nil {
		return fmt.Errorf("lcodex refresh request failed: %w", err)
	}
	if status < 200 || status >= 300 || strings.TrimSpace(tokens.AccessToken) == "" {
		return fmt.Errorf("lcodex refresh returned status %d without access token", status)
	}
	s.accessToken = strings.TrimSpace(tokens.AccessToken)
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		s.refreshToken = strings.TrimSpace(tokens.RefreshToken)
	}
	return nil
}

func lcodexUpstreamKey(configID int64, row lcodexKeyRow, groups map[int64]lcodexGroupInfo, dedicatedRates map[int64]float64, now time.Time) (UpstreamKey, []string, error) {
	secret := strings.TrimSpace(row.Key)
	if secret == "" || isMaskedUpstreamKey(secret) {
		return UpstreamKey{}, nil, fmt.Errorf("lcodex key %d did not include a complete key", row.ID)
	}
	groupID := row.GroupID
	if groupID == nil && row.Group != nil && row.Group.ID > 0 {
		value := row.Group.ID
		groupID = &value
	}
	groupName, platform := "", ""
	var sourceRate *float64
	extra := map[string]any{}
	warnings := []string{}
	if groupID != nil {
		if rate, ok := dedicatedRates[*groupID]; ok {
			value := rate
			sourceRate = &value
		}
		if group, ok := groups[*groupID]; ok {
			groupName, platform = group.Name, group.Platform
			if sourceRate == nil && group.DefaultRate != nil {
				value := *group.DefaultRate
				sourceRate = &value
			}
			if group.ImageCapability != nil {
				status := UpstreamKeyImagePricingStatusDisabled
				if *group.ImageCapability {
					status = UpstreamKeyImagePricingStatusPartial
				}
				extra[LCodexImageCapabilitySnapshotExtraKey] = lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{
					Version: lcodexImageCapabilitySnapshotVersion, Status: status,
					AllowImageGeneration: *group.ImageCapability, ObservedAt: &now,
				})
			}
			if group.ImageCapabilityBad {
				warnings = appendLCodexWarning(warnings, "lcodex image capability is invalid and was ignored")
			}
		}
	}
	if row.Group != nil {
		if groupName == "" {
			groupName = normalizeUpstreamDisplayName(row.Group.Name, 100)
		}
		if platform == "" {
			platform = strings.ToLower(strings.TrimSpace(row.Group.Platform))
		}
		if sourceRate == nil {
			if value, present, parseErr := parseLCodexOptionalNonNegativeFloat(row.Group.RateMultiplier); parseErr != nil {
				warnings = appendLCodexWarning(warnings, "lcodex key group rate is invalid and was ignored")
			} else if present {
				sourceRate = &value
			}
		}
	}
	if sourceRate == nil {
		warnings = appendLCodexWarning(warnings, fmt.Sprintf("lcodex key %d has no valid rate multiplier", row.ID))
	}
	var platformPtr *string
	platformSource, detectionStatus := UpstreamKeyPlatformSourceUnassigned, UpstreamKeyPlatformDetectionUnresolved
	if isAssignableUpstreamKeyPlatform(platform) {
		platformValue := strings.ToLower(strings.TrimSpace(platform))
		platformPtr = &platformValue
		platformSource, detectionStatus = UpstreamKeyPlatformSourceAuto, UpstreamKeyPlatformDetectionDetected
	}
	status := StatusActive
	switch strings.ToLower(strings.TrimSpace(row.Status)) {
	case "disabled", "inactive", "revoked", "expired":
		status = StatusDisabled
	}
	return UpstreamKey{
		UpstreamConfigID: configID, Name: normalizeUpstreamDisplayName(row.Name, 100), Key: secret, KeyHash: HashUpstreamKey(secret),
		RemoteKeyID: &row.ID, UpstreamGroupID: groupID, UpstreamGroupName: groupName,
		Platform: platformPtr, PlatformSource: platformSource, DetectedPlatform: platformPtr,
		PlatformDetectionStatus: detectionStatus, PlatformDetectedAt: &now,
		SourceRateMultiplier: sourceRate, Status: status, LastSeenAt: &now, Extra: extra,
	}, warnings, nil
}

func lcodexProfileExtraUpdates(cfg *UpstreamConfig, profile *lcodexProfile, profileErr error) (map[string]any, string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if profileErr != nil || profile == nil {
		if profileErr == nil {
			profileErr = fmt.Errorf("lcodex profile returned null data")
		}
		updates := map[string]any{
			"upstream_provider_snapshot_last_error":    lcodexUpstreamProviderAdapter{}.SanitizeError(profileErr, cfg.Credentials),
			"upstream_provider_snapshot_last_error_at": now,
		}
		concurrency, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderLCodex, nil, profileErr)
		for key, value := range concurrency {
			updates[key] = value
		}
		return updates, warning
	}
	balance, _, _ := parseLCodexOptionalNonNegativeFloat(profile.CreditBalance)
	updates := map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"version": 1, "provider": UpstreamProviderLCodex, "synced_at": now,
			"user_id": profile.ID, "email": strings.TrimSpace(profile.Email), "disabled": profile.Disabled,
			"balance_amount": balance, "base_balance_amount": balance, "currency": "USD", "currency_symbol": "$", "unit": "currency",
		},
		"upstream_provider_snapshot_last_error":    "",
		"upstream_provider_snapshot_last_error_at": "",
	}
	concurrency, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderLCodex, profile.MaxConcurrency, nil)
	for key, value := range concurrency {
		updates[key] = value
	}
	return updates, warning
}

func (s *UpstreamConfigService) mergeLCodexImageCapabilitySnapshots(ctx context.Context, cfg *UpstreamConfig, snapshot *upstreamProviderSnapshot) {
	if cfg == nil || snapshot == nil || len(snapshot.Keys) == 0 {
		return
	}
	existing, err := s.repo.ListKeys(ctx, cfg.ID)
	if err != nil {
		existing = nil
	}
	byRemoteID := make(map[int64]UpstreamKey, len(existing))
	for _, key := range existing {
		if key.RemoteKeyID != nil {
			byRemoteID[*key.RemoteKeyID] = key
		}
	}
	for i := range snapshot.Keys {
		key := &snapshot.Keys[i]
		if _, ok := parseLCodexImageCapabilitySnapshot(key.Extra); ok {
			continue
		}
		if key.Extra == nil {
			key.Extra = map[string]any{}
		}
		if key.RemoteKeyID != nil {
			if previous, exists := byRemoteID[*key.RemoteKeyID]; exists {
				if old, ok := parseLCodexImageCapabilitySnapshot(previous.Extra); ok && old.Status != UpstreamKeyImagePricingStatusUnavailable {
					old.Stale = true
					key.Extra[LCodexImageCapabilitySnapshotExtraKey] = lcodexImageCapabilitySnapshotMap(old)
					continue
				}
			}
		}
		key.Extra[LCodexImageCapabilitySnapshotExtraKey] = lcodexImageCapabilitySnapshotMap(lcodexImageCapabilitySnapshot{
			Version: lcodexImageCapabilitySnapshotVersion, Status: UpstreamKeyImagePricingStatusUnavailable,
		})
	}
}

func decodeLCodexGroups(raw json.RawMessage) ([]lcodexGroupRow, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("empty response")
	}
	if trimmed[0] != '[' {
		return nil, errors.New("expected an array")
	}
	var rows []lcodexGroupRow
	if err := json.Unmarshal(trimmed, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func parseLCodexOptionalNonNegativeFloat(raw json.RawMessage) (float64, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, true, errors.New("invalid non-negative number")
	}
	return value, true, nil
}

func parseLCodexOptionalBool(raw json.RawMessage) (bool, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return false, false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, true, err
	}
	return value, true, nil
}

func normalizeLCodexRootURL(raw string) (string, error) {
	return normalizeUpstreamConfigURL(raw)
}

func buildLCodexURL(rootURL, path string, query url.Values) (string, error) {
	rootURL, err := normalizeLCodexRootURL(rootURL)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(rootURL)
	if err != nil {
		return "", err
	}
	parsed.Path = path
	parsed.RawPath, parsed.RawQuery, parsed.Fragment = "", "", ""
	if query != nil {
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func newLCodexHTTPClient(rawProxyURL string) (*http.Client, error) {
	proxyURL, _, err := proxyurl.Parse(rawProxyURL)
	if err != nil {
		return nil, fmt.Errorf("invalid lcodex proxy: %w", err)
	}
	return httpclient.GetClient(httpclient.Options{ProxyURL: proxyURL, Timeout: 10 * time.Second})
}

func doLCodexJSON(ctx context.Context, client *http.Client, method, endpoint, bearerToken string, body, out any) (int, error) {
	if client == nil {
		return 0, errors.New("missing lcodex http client")
	}
	var requestBody []byte
	var err error
	if body != nil {
		requestBody, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "sub2api-lcodex-sync/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token := strings.TrimSpace(bearerToken); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || out == nil {
		return resp.StatusCode, nil
	}
	raw, err := readUpstreamResponseBodyLimited(resp.Body, defaultUpstreamResponseReadMaxBytes)
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return resp.StatusCode, err
	}
	return resp.StatusCode, nil
}

func appendLCodexWarning(existing []string, warning string) []string {
	if strings.TrimSpace(warning) == "" {
		return existing
	}
	for _, current := range existing {
		if current == warning {
			return existing
		}
	}
	return append(existing, warning)
}

func uniqueLCodexWarnings(warnings []string) []string {
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		out = appendLCodexWarning(out, warning)
	}
	return out
}
