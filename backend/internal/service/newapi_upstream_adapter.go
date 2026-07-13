package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	newAPILoginPath          = "/api/user/login"
	newAPITokensPath         = "/api/token/"
	newAPITokenBatchKeysPath = "/api/token/batch/keys"
	newAPIUserGroupsPath     = "/api/user/self/groups"
	newAPIUserProfilePath    = "/api/user/self"
	newAPIUserStatPath       = "/api/log/self/stat"
	newAPIStatusPath         = "/api/status"
	newAPIPricingPath        = "/api/pricing"
	newAPIKeysPageSize       = 100
	newAPIMaxKeyListPages    = 1000
	newAPIMaxRevealWorkers   = 5

	newAPIWarningInvalidRemoteKeyID = "newapi_token_list_invalid_remote_key_id"
	newAPIWarningTotalChanged       = "newapi_token_list_total_changed"
	newAPIWarningTotalMismatch      = "newapi_token_list_total_mismatch"
	newAPIWarningRepeatedPage       = "newapi_token_list_repeated_page"
	newAPIWarningNoProgress         = "newapi_token_list_no_progress"
	newAPIWarningPageLimit          = "newapi_token_list_page_limit"
	newAPIWarningRevealFailed       = "newapi_token_key_reveal_failed"
)

var reNewAPISecretKey = regexp.MustCompile(`\bsk-[0-9A-Za-z_-]{8,}\b`)

type newAPIUpstreamProviderAdapter struct{}

type newAPIEnvelope[T any] struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type newAPILoginData struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type newAPISession struct {
	rootURL string
	userID  int64
	client  *http.Client
}

type newAPIAuthTransport struct {
	base        http.RoundTripper
	cookie      string
	accessToken string
}

func (t newAPIAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	if t.cookie != "" {
		clone.Header.Set("Cookie", t.cookie)
	} else if t.accessToken != "" {
		clone.Header.Set("Authorization", t.accessToken)
	}
	return t.base.RoundTrip(clone)
}

type newAPIGroupInfo struct {
	Desc      string   `json:"desc"`
	Ratio     any      `json:"ratio"`
	Platforms []string `json:"-"`
}

type newAPIKeyListData struct {
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
	Items    []newAPIKeyRow `json:"items"`
}

type newAPIWireEnvelope struct {
	Success *bool           `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type newAPIKeyListWireData struct {
	Page     json.RawMessage `json:"page"`
	PageSize json.RawMessage `json:"page_size"`
	Total    json.RawMessage `json:"total"`
	Items    json.RawMessage `json:"items"`
}

type newAPIKeyFetchResult struct {
	Rows     []newAPIKeyRow
	Partial  bool
	Warnings []string
}

type newAPIRevealResult struct {
	Keys          map[int64]string
	Partial       bool
	Warnings      []string
	FallbackCount int
}

type newAPIPaginationMode struct {
	startPage int
	sizeParam string
}

type newAPIKeyRow struct {
	ID                 int64   `json:"id"`
	UserID             int64   `json:"user_id"`
	Key                string  `json:"key"`
	Status             int     `json:"status"`
	Name               string  `json:"name"`
	Group              string  `json:"group"`
	UsedQuota          float64 `json:"used_quota"`
	RemainQuota        float64 `json:"remain_quota"`
	UnlimitedQuota     bool    `json:"unlimited_quota"`
	ModelLimitsEnabled bool    `json:"model_limits_enabled"`
}

type newAPIBatchKeysData struct {
	Keys map[string]string `json:"keys"`
}

type newAPIPricingData struct {
	Success     bool              `json:"success"`
	UsableGroup map[string]string `json:"usable_group"`
	Data        []map[string]any  `json:"data"`
}

type newAPIUserProfile struct {
	ID           int64           `json:"id"`
	Email        string          `json:"email"`
	Username     string          `json:"username"`
	DisplayName  string          `json:"display_name"`
	Group        string          `json:"group"`
	Quota        float64         `json:"quota"`
	UsedQuota    float64         `json:"used_quota"`
	RequestCount float64         `json:"request_count"`
	Concurrency  json.RawMessage `json:"concurrency"`
}

type newAPIStatusData struct {
	QuotaDisplayType           string  `json:"quota_display_type"`
	QuotaPerUnit               float64 `json:"quota_per_unit"`
	USDExchangeRate            float64 `json:"usd_exchange_rate"`
	CustomCurrencySymbol       string  `json:"custom_currency_symbol"`
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
}

type newAPIUsageStat struct {
	Quota float64 `json:"quota"`
}

func (newAPIUpstreamProviderAdapter) Provider() string { return UpstreamProviderNewAPI }

func (newAPIUpstreamProviderAdapter) ValidateConfig(config *UpstreamConfig, requireSecrets bool) error {
	username := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPILoginUsername))
	password := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPILoginPassword))
	cookie := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPICookie))
	accessToken := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPIAccessToken))
	if cookie != "" && accessToken != "" {
		return infraBadRequest("UPSTREAM_NEWAPI_AUTH_CONFLICT", "newapi cookie and access token are mutually exclusive")
	}
	switch config.AuthMode {
	case UpstreamAuthModeCookie:
		if cookie == "" {
			return infraBadRequest("UPSTREAM_NEWAPI_COOKIE_REQUIRED", "newapi cookie is required")
		}
		if _, err := newAPIConfiguredUserID(config.Credentials); err != nil {
			return err
		}
	case UpstreamAuthModeAccessToken:
		if accessToken == "" {
			return infraBadRequest("UPSTREAM_NEWAPI_ACCESS_TOKEN_REQUIRED", "newapi access token is required")
		}
		if _, err := newAPIConfiguredUserID(config.Credentials); err != nil {
			return err
		}
	default:
		if username == "" {
			return infraBadRequest("UPSTREAM_NEWAPI_USERNAME_REQUIRED", "newapi login username is required")
		}
		if password == "" {
			return infraBadRequest("UPSTREAM_NEWAPI_PASSWORD_REQUIRED", "newapi login password is required")
		}
	}
	return nil
}

func (a newAPIUpstreamProviderAdapter) Test(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error {
	snapshot, err := a.SyncSnapshot(ctx, cfg, proxyURL, true)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return fmt.Errorf("newapi test returned no snapshot")
	}
	if raw, ok := snapshot.ExtraUpdates["upstream_provider_snapshot_last_error"]; ok {
		if text := strings.TrimSpace(upstreamString(raw)); text != "" {
			return fmt.Errorf("newapi profile check failed: %s", text)
		}
	}
	return nil
}

func (a newAPIUpstreamProviderAdapter) SyncSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*upstreamProviderSnapshot, error) {
	session, err := a.login(ctx, cfg, proxyURL)
	if err != nil {
		return nil, err
	}
	groups, err := a.fetchGroups(ctx, session)
	if err != nil {
		return nil, err
	}
	a.enrichGroupsFromPricing(ctx, session, groups)
	keyResult, err := a.fetchKeys(ctx, session)
	if err != nil {
		return nil, err
	}
	revealResult, err := a.fetchMaskedKeySecrets(ctx, session, keyResult.Rows)
	if err != nil {
		return nil, err
	}
	warnings := appendNewAPIWarnings(nil, keyResult.Warnings...)
	warnings = appendNewAPIWarnings(warnings, revealResult.Warnings...)
	partial := keyResult.Partial || revealResult.Partial
	groupsComplete := true
	for _, row := range keyResult.Rows {
		group := strings.TrimSpace(row.Group)
		if group == "" {
			continue
		}
		if _, exists := groups[group]; !exists {
			groupsComplete = false
			partial = true
			warnings = appendNewAPIWarnings(warnings, "newapi group snapshot does not cover all returned keys")
			break
		}
	}
	now := time.Now()
	keys := make([]UpstreamKey, 0, len(keyResult.Rows))
	unresolvedKeyCount := 0
	for _, row := range keyResult.Rows {
		key := strings.TrimSpace(row.Key)
		if isMaskedUpstreamKey(key) {
			key = strings.TrimSpace(revealResult.Keys[row.ID])
		}
		unresolved := key == "" || isMaskedUpstreamKey(key)
		if unresolved {
			key = ""
			unresolvedKeyCount++
		}
		group := strings.TrimSpace(row.Group)
		var rate *float64
		if group != "" {
			if info, ok := groups[group]; ok {
				if parsed, ok := parseNewAPIRatio(info.Ratio); ok {
					rate = &parsed
				}
			}
		}
		status := StatusDisabled
		if row.Status == 1 {
			status = StatusActive
		}
		extra := map[string]any{
			"newapi_user_id":              row.UserID,
			"newapi_group":                group,
			"newapi_used_quota":           row.UsedQuota,
			"newapi_remain_quota":         row.RemainQuota,
			"newapi_unlimited_quota":      row.UnlimitedQuota,
			"newapi_model_limits_enabled": row.ModelLimitsEnabled,
		}
		if info, ok := groups[group]; ok && strings.TrimSpace(info.Desc) != "" {
			extra["newapi_group_desc"] = strings.TrimSpace(info.Desc)
		}
		platform := PlatformOpenAI
		if info, ok := groups[group]; ok && len(info.Platforms) == 1 {
			platform = info.Platforms[0]
		}
		item := UpstreamKey{
			UpstreamConfigID:  cfg.ID,
			Name:              normalizeUpstreamDisplayName(row.Name, 100),
			Key:               key,
			KeyHash:           "",
			RemoteKeyID:       &row.ID,
			UpstreamGroupName: normalizeUpstreamDisplayName(group, 100),
			Platform:          platform,
			RateMultiplier:    rate,
			Status:            status,
			LastSeenAt:        &now,
			Extra:             extra,
		}
		if key != "" {
			item.KeyHash = HashUpstreamKey(key)
		}
		keys = append(keys, item)
	}
	extraUpdates := map[string]any{}
	if includeProfile {
		profile, profileErr := a.fetchProfile(ctx, session)
		status, statusErr := a.fetchStatus(ctx, session)
		todayUsage, todayUsageErr := a.fetchTodayUsage(ctx, session)
		if profileErr != nil {
			partial = true
			warnings = appendNewAPIWarnings(warnings, "newapi profile snapshot unavailable")
		}
		if statusErr != nil {
			partial = true
			warnings = appendNewAPIWarnings(warnings, "newapi currency status unavailable")
		}
		if todayUsageErr != nil {
			partial = true
			warnings = appendNewAPIWarnings(warnings, "newapi today usage snapshot unavailable")
		}
		profileUpdates, concurrencyWarning := newAPIProfileExtraUpdates(cfg, profile, profileErr, status, statusErr)
		warnings = appendNewAPIWarnings(warnings, concurrencyWarning)
		if concurrencyWarning != "" {
			partial = true
		}
		for key, value := range profileUpdates {
			extraUpdates[key] = value
		}
		if snapshot, ok := extraUpdates["upstream_provider_snapshot"].(map[string]any); ok {
			snapshot["groups"] = newAPIGroupSnapshot(groups)
			snapshot["groups_complete"] = groupsComplete
			if todayUsageErr == nil && todayUsage != nil && finiteNewAPINumber(todayUsage.Quota) {
				amounts := newAPIQuotaAmounts(todayUsage.Quota, 0, status)
				snapshot["today_used_quota"] = todayUsage.Quota
				snapshot["today_used_amount"] = amounts.BalanceAmount
				snapshot["base_today_used_amount"] = amounts.BaseBalanceAmount
			}
		}
	}
	extraUpdates["upstream_provider_snapshot_partial"] = partial
	extraUpdates["upstream_provider_snapshot_warnings"] = warnings
	return &upstreamProviderSnapshot{
		Keys: keys, KeysComplete: !keyResult.Partial && !revealResult.Partial && unresolvedKeyCount == 0,
		ExtraUpdates: extraUpdates, Partial: partial || unresolvedKeyCount > 0,
		Warnings: warnings, FallbackKeyCount: revealResult.FallbackCount,
	}, nil
}

func newAPIGroupSnapshot(groups map[string]newAPIGroupInfo) map[string]any {
	out := make(map[string]any, len(groups))
	for name, info := range groups {
		entry := map[string]any{"description": strings.TrimSpace(info.Desc)}
		if ratio, ok := parseNewAPIRatio(info.Ratio); ok {
			entry["ratio"] = ratio
		} else {
			entry["ratio_status"] = "unavailable"
		}
		if len(info.Platforms) > 0 {
			entry["platforms"] = append([]string(nil), info.Platforms...)
		}
		out[name] = entry
	}
	return out
}

func (newAPIUpstreamProviderAdapter) SanitizeError(err error, credentials map[string]any) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, key := range []string{
		AccountCredentialNewAPILoginUsername,
		AccountCredentialNewAPILoginPassword,
		AccountCredentialNewAPICookie,
		AccountCredentialNewAPIAccessToken,
	} {
		value := strings.TrimSpace(stringCredential(credentials, key))
		if value != "" {
			text = strings.ReplaceAll(text, value, "[REDACTED]")
		}
	}
	text = reNewAPISecretKey.ReplaceAllString(text, "sk-***")
	return logredact.RedactText(text, "api_key", "jwt", "authorization", "bearer", "token", "access_token", "refresh_token", "cookie", "session", "password")
}

func (a newAPIUpstreamProviderAdapter) login(ctx context.Context, cfg *UpstreamConfig, proxyURL string) (*newAPISession, error) {
	rootURL, err := normalizeSub2APIBaseURL(cfg.SiteURL)
	if err != nil {
		return nil, err
	}
	normalizedProxyURL, err := normalizeSub2APIProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	sharedClient, err := sub2APIHTTPClient(normalizedProxyURL)
	if err != nil {
		return nil, err
	}
	clientCopy := *sharedClient
	client := &clientCopy
	baseTransport := client.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	if cfg.AuthMode == UpstreamAuthModeCookie || cfg.AuthMode == UpstreamAuthModeAccessToken {
		userID, userErr := newAPIConfiguredUserID(cfg.Credentials)
		if userErr != nil {
			return nil, userErr
		}
		client.Transport = newAPIAuthTransport{
			base:        baseTransport,
			cookie:      strings.TrimSpace(stringCredential(cfg.Credentials, AccountCredentialNewAPICookie)),
			accessToken: strings.TrimSpace(stringCredential(cfg.Credentials, AccountCredentialNewAPIAccessToken)),
		}
		return &newAPISession{rootURL: rootURL, userID: userID, client: client}, nil
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client.Jar = jar

	endpoint, err := buildSub2APIURL(rootURL, newAPILoginPath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[newAPILoginData]
	status, err := a.doJSON(ctx, client, http.MethodPost, endpoint, 0, map[string]string{
		"username": strings.TrimSpace(stringCredential(cfg.Credentials, AccountCredentialNewAPILoginUsername)),
		"password": strings.TrimSpace(stringCredential(cfg.Credentials, AccountCredentialNewAPILoginPassword)),
	}, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi login request failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi login returned status %d", status)
	}
	if !payload.Success {
		return nil, fmt.Errorf("newapi login failed%s", safeNewAPIMessage(payload.Message))
	}
	if payload.Data.ID <= 0 {
		return nil, fmt.Errorf("newapi login returned no user id")
	}
	parsedRoot, err := url.Parse(rootURL)
	if err != nil {
		return nil, err
	}
	if len(client.Jar.Cookies(parsedRoot)) == 0 {
		return nil, fmt.Errorf("newapi login returned no session cookie")
	}
	return &newAPISession{rootURL: rootURL, userID: payload.Data.ID, client: client}, nil
}

func newAPIConfiguredUserID(credentials map[string]any) (int64, error) {
	raw := strings.TrimSpace(stringCredential(credentials, AccountCredentialNewAPIUserID))
	userID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || userID <= 0 {
		return 0, infraBadRequest("UPSTREAM_NEWAPI_USER_ID_REQUIRED", "newapi user id must be a positive integer")
	}
	return userID, nil
}

func (a newAPIUpstreamProviderAdapter) fetchGroups(ctx context.Context, session *newAPISession) (map[string]newAPIGroupInfo, error) {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIUserGroupsPath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[map[string]newAPIGroupInfo]
	status, err := a.doJSON(ctx, session.client, http.MethodGet, endpoint, session.userID, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi list groups failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi list groups returned status %d", status)
	}
	if !payload.Success {
		return nil, fmt.Errorf("newapi list groups failed%s", safeNewAPIMessage(payload.Message))
	}
	return payload.Data, nil
}

func (a newAPIUpstreamProviderAdapter) enrichGroupsFromPricing(ctx context.Context, session *newAPISession, groups map[string]newAPIGroupInfo) {
	if len(groups) == 0 {
		return
	}
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIPricingPath)
	if err != nil {
		return
	}
	var payload newAPIPricingData
	status, err := a.doJSON(ctx, session.client, http.MethodGet, endpoint, session.userID, nil, &payload)
	if err != nil || status < 200 || status >= 300 || !payload.Success {
		return
	}
	for group, desc := range payload.UsableGroup {
		info, ok := groups[group]
		if !ok || strings.TrimSpace(info.Desc) != "" {
			continue
		}
		if desc = strings.TrimSpace(desc); desc != "" {
			info.Desc = desc
			groups[group] = info
		}
	}
	platformsByGroup := map[string]map[string]struct{}{}
	for _, record := range payload.Data {
		platform := normalizeNewAPIPlatform(anyString(record["owner_by"]))
		if platform == "" {
			platform = normalizeNewAPIPlatform(anyString(record["ownerBy"]))
		}
		if platform == "" {
			platform = normalizeNewAPIPlatform(anyString(record["platform"]))
		}
		if platform == "" {
			platform = normalizeNewAPIPlatform(anyString(record["vendor"]))
		}
		if platform == "" {
			continue
		}
		for _, field := range []string{"enable_groups", "enable_group"} {
			for _, group := range newAPIStringList(record[field]) {
				if platformsByGroup[group] == nil {
					platformsByGroup[group] = map[string]struct{}{}
				}
				platformsByGroup[group][platform] = struct{}{}
			}
		}
	}
	for group, platformSet := range platformsByGroup {
		info, ok := groups[group]
		if !ok {
			continue
		}
		for platform := range platformSet {
			info.Platforms = append(info.Platforms, platform)
		}
		sort.Strings(info.Platforms)
		groups[group] = info
	}
}

func normalizeNewAPIPlatform(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.Contains(value, "anthropic"), strings.Contains(value, "claude"):
		return PlatformAnthropic
	case strings.Contains(value, "gemini"), strings.Contains(value, "google"):
		return PlatformGemini
	case strings.Contains(value, "openai"), strings.Contains(value, "gpt"):
		return PlatformOpenAI
	default:
		return ""
	}
}

func newAPIStringList(value any) []string {
	var raw []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			raw = append(raw, anyString(item))
		}
	case []string:
		raw = typed
	case string:
		raw = strings.Split(typed, ",")
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func (a newAPIUpstreamProviderAdapter) fetchKeys(ctx context.Context, session *newAPISession) (*newAPIKeyFetchResult, error) {
	result, fallback, err := a.fetchKeysWithMode(ctx, session, newAPIPaginationMode{startPage: 0, sizeParam: "size"}, true)
	if err != nil {
		return nil, err
	}
	if !fallback {
		return result, nil
	}
	result, _, err = a.fetchKeysWithMode(ctx, session, newAPIPaginationMode{startPage: 1, sizeParam: "page_size"}, false)
	return result, err
}

func (a newAPIUpstreamProviderAdapter) fetchKeysWithMode(
	ctx context.Context,
	session *newAPISession,
	mode newAPIPaginationMode,
	allowFirstPageFallback bool,
) (*newAPIKeyFetchResult, bool, error) {
	result := &newAPIKeyFetchResult{Rows: make([]newAPIKeyRow, 0), Warnings: []string{}}
	seenIDs := make(map[int64]struct{})
	seenPages := make(map[string]struct{})
	var rawSeen int64
	var totalHint int64
	for pageOffset := 0; pageOffset < newAPIMaxKeyListPages; pageOffset++ {
		page := mode.startPage + pageOffset
		endpoint, err := buildSub2APIURL(session.rootURL, newAPITokensPath)
		if err != nil {
			return nil, false, err
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, false, err
		}
		q := u.Query()
		q.Set("p", strconv.Itoa(page))
		q.Set(mode.sizeParam, strconv.Itoa(newAPIKeysPageSize))
		u.RawQuery = q.Encode()

		var payload newAPIWireEnvelope
		status, err := a.doJSON(ctx, session.client, http.MethodGet, u.String(), session.userID, nil, &payload)
		if err != nil {
			if allowFirstPageFallback && pageOffset == 0 && isNewAPIJSONDecodeError(err) {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("newapi list tokens failed: %w", err)
		}
		if allowFirstPageFallback && pageOffset == 0 && (status == http.StatusBadRequest || status == http.StatusNotFound) {
			return nil, true, nil
		}
		if status < 200 || status >= 300 {
			return nil, false, fmt.Errorf("newapi list tokens returned status %d", status)
		}
		if payload.Success == nil {
			if allowFirstPageFallback && pageOffset == 0 {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("newapi list tokens returned incompatible response")
		}
		if !*payload.Success {
			return nil, false, fmt.Errorf("newapi list tokens failed%s", safeNewAPIMessage(payload.Message))
		}
		items, total, totalPresent, err := parseNewAPIKeyListPage(payload.Data)
		if err != nil {
			if allowFirstPageFallback && pageOffset == 0 {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("newapi list tokens returned incompatible response")
		}

		if totalPresent && total > 0 {
			if totalHint == 0 {
				totalHint = total
			} else if total != totalHint {
				markNewAPIPartial(result, newAPIWarningTotalChanged)
			}
		}

		fingerprint := newAPIPageFingerprint(items)
		_, repeatedPage := seenPages[fingerprint]
		if len(items) > 0 {
			seenPages[fingerprint] = struct{}{}
		}
		newIDs := 0
		for _, row := range items {
			if row.ID <= 0 {
				markNewAPIPartial(result, newAPIWarningInvalidRemoteKeyID)
				continue
			}
			if _, exists := seenIDs[row.ID]; exists {
				continue
			}
			seenIDs[row.ID] = struct{}{}
			result.Rows = append(result.Rows, row)
			newIDs++
		}
		rawSeen += int64(len(items))

		if len(items) > 0 && repeatedPage {
			markNewAPIPartial(result, newAPIWarningRepeatedPage)
			return result, false, nil
		}
		if len(items) == newAPIKeysPageSize && newIDs == 0 {
			markNewAPIPartial(result, newAPIWarningNoProgress)
			return result, false, nil
		}

		normalStop := len(items) < newAPIKeysPageSize || (totalHint > 0 && rawSeen >= totalHint)
		if normalStop {
			if totalHint > 0 && (rawSeen < totalHint || int64(len(result.Rows)) < totalHint) {
				markNewAPIPartial(result, newAPIWarningTotalMismatch)
			}
			return result, false, nil
		}
		if pageOffset == newAPIMaxKeyListPages-1 {
			markNewAPIPartial(result, newAPIWarningPageLimit)
			return result, false, nil
		}
	}
	return result, false, nil
}

func (a newAPIUpstreamProviderAdapter) fetchMaskedKeySecrets(ctx context.Context, session *newAPISession, rows []newAPIKeyRow) (*newAPIRevealResult, error) {
	ids := make([]int64, 0)
	seenIDs := make(map[int64]struct{})
	for _, row := range rows {
		if row.ID <= 0 {
			continue
		}
		key := strings.TrimSpace(row.Key)
		if key != "" && isMaskedUpstreamKey(key) {
			if _, exists := seenIDs[row.ID]; !exists {
				seenIDs[row.ID] = struct{}{}
				ids = append(ids, row.ID)
			}
		}
	}
	result := &newAPIRevealResult{Keys: make(map[int64]string), Warnings: []string{}}
	if len(ids) == 0 {
		return result, nil
	}
	endpoint, err := buildSub2APIURL(session.rootURL, newAPITokenBatchKeysPath)
	if err != nil {
		return nil, err
	}
	var payload newAPIWireEnvelope
	status, err := a.doJSON(ctx, session.client, http.MethodPost, endpoint, session.userID, map[string]any{"ids": ids}, &payload)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("newapi fetch token keys request failed")
	}
	if status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
		return a.fetchIndividualKeySecrets(ctx, session, ids, result)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi fetch token keys returned status %d", status)
	}
	if payload.Success == nil {
		return nil, fmt.Errorf("newapi fetch token keys returned incompatible response")
	}
	if !*payload.Success {
		if isNewAPIBatchUnsupportedMessage(payload.Message) {
			return a.fetchIndividualKeySecrets(ctx, session, ids, result)
		}
		return nil, fmt.Errorf("newapi fetch token keys failed")
	}
	batchKeys, err := parseNewAPIBatchKeys(payload.Data)
	if err != nil {
		return nil, fmt.Errorf("newapi fetch token keys returned incompatible response")
	}
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	for rawID, key := range batchKeys {
		id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		if _, ok := wanted[id]; ok && isUsableNewAPIKey(key) {
			result.Keys[id] = strings.TrimSpace(key)
		}
	}
	unresolved := make([]int64, 0)
	for _, id := range ids {
		if _, ok := result.Keys[id]; !ok {
			unresolved = append(unresolved, id)
		}
	}
	if len(unresolved) == 0 {
		return result, nil
	}
	return a.fetchIndividualKeySecrets(ctx, session, unresolved, result)
}

func (a newAPIUpstreamProviderAdapter) fetchIndividualKeySecrets(
	ctx context.Context,
	session *newAPISession,
	ids []int64,
	result *newAPIRevealResult,
) (*newAPIRevealResult, error) {
	type revealItem struct {
		key string
		ok  bool
	}
	items := make([]revealItem, len(ids))
	jobs := make(chan int)
	workerCount := newAPIMaxRevealWorkers
	if len(ids) < workerCount {
		workerCount = len(ids)
	}
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer wg.Done()
			for index := range jobs {
				items[index].key, items[index].ok = a.fetchIndividualKeySecret(ctx, session, ids[index])
			}
		}()
	}
	for index := range ids {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for index, id := range ids {
		if items[index].ok {
			result.Keys[id] = items[index].key
			result.FallbackCount++
			continue
		}
		result.Partial = true
		result.Warnings = appendNewAPIWarnings(result.Warnings, fmt.Sprintf("%s remote_key_id=%d", newAPIWarningRevealFailed, id))
	}
	return result, nil
}

func (a newAPIUpstreamProviderAdapter) fetchIndividualKeySecret(ctx context.Context, session *newAPISession, id int64) (string, bool) {
	endpoint, err := buildSub2APIURL(session.rootURL, fmt.Sprintf("/api/token/%d/key", id))
	if err != nil {
		return "", false
	}
	var payload newAPIWireEnvelope
	status, err := a.doJSON(ctx, session.client, http.MethodGet, endpoint, session.userID, nil, &payload)
	if err != nil || status < 200 || status >= 300 || payload.Success == nil || !*payload.Success {
		return "", false
	}
	if !isJSONObject(payload.Data) {
		return "", false
	}
	var data struct {
		Key json.RawMessage `json:"key"`
	}
	if err := json.Unmarshal(payload.Data, &data); err != nil || len(data.Key) == 0 || isJSONNull(data.Key) {
		return "", false
	}
	var key string
	if err := json.Unmarshal(data.Key, &key); err != nil || !isUsableNewAPIKey(key) {
		return "", false
	}
	return strings.TrimSpace(key), true
}

func parseNewAPIKeyListPage(raw json.RawMessage) ([]newAPIKeyRow, int64, bool, error) {
	if !isJSONObject(raw) {
		return nil, 0, false, fmt.Errorf("data must be an object")
	}
	var data newAPIKeyListWireData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, 0, false, err
	}
	if _, _, err := parseOptionalNewAPIInteger(data.Page); err != nil {
		return nil, 0, false, fmt.Errorf("invalid page")
	}
	if _, _, err := parseOptionalNewAPIInteger(data.PageSize); err != nil {
		return nil, 0, false, fmt.Errorf("invalid page_size")
	}
	total, totalPresent, err := parseOptionalNewAPIInteger(data.Total)
	if err != nil {
		return nil, 0, false, fmt.Errorf("invalid total")
	}
	if len(data.Items) == 0 || isJSONNull(data.Items) {
		return nil, 0, false, fmt.Errorf("items must be an array")
	}
	var items []newAPIKeyRow
	if err := json.Unmarshal(data.Items, &items); err != nil || items == nil {
		return nil, 0, false, fmt.Errorf("items must be an array")
	}
	return items, total, totalPresent, nil
}

func parseNewAPIBatchKeys(raw json.RawMessage) (map[string]string, error) {
	if !isJSONObject(raw) {
		return nil, fmt.Errorf("data must be an object")
	}
	var data struct {
		Keys json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	if len(data.Keys) == 0 || isJSONNull(data.Keys) {
		return map[string]string{}, nil
	}
	var keys map[string]string
	if err := json.Unmarshal(data.Keys, &keys); err != nil || keys == nil {
		return nil, fmt.Errorf("keys must be an object of strings")
	}
	return keys, nil
}

func parseOptionalNewAPIInteger(raw json.RawMessage) (int64, bool, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, false, nil
	}
	if value, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return value, true, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || !finiteNewAPINumber(value) || math.Trunc(value) != value || value > math.MaxInt64 || value < math.MinInt64 {
		return 0, false, fmt.Errorf("not an integer")
	}
	return int64(value), true, nil
}

func newAPIPageFingerprint(items []newAPIKeyRow) string {
	var builder strings.Builder
	builder.Grow(len(items) * 12)
	for _, row := range items {
		builder.WriteString(strconv.FormatInt(row.ID, 10))
		builder.WriteByte(',')
	}
	return builder.String()
}

func markNewAPIPartial(result *newAPIKeyFetchResult, warning string) {
	result.Partial = true
	result.Warnings = appendNewAPIWarnings(result.Warnings, warning)
}

func appendNewAPIWarnings(existing []string, warnings ...string) []string {
	seen := make(map[string]struct{}, len(existing)+len(warnings))
	for _, warning := range existing {
		seen[warning] = struct{}{}
	}
	for _, warning := range warnings {
		if warning == "" {
			continue
		}
		if _, ok := seen[warning]; ok {
			continue
		}
		seen[warning] = struct{}{}
		existing = append(existing, warning)
	}
	return existing
}

func isUsableNewAPIKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && !isMaskedUpstreamKey(key)
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func isJSONNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

func isNewAPIJSONDecodeError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func isNewAPIBatchUnsupportedMessage(message string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(message)), " "))
	switch normalized {
	case "unsupported",
		"not supported",
		"not implemented",
		"method not allowed",
		"batch reveal unsupported",
		"batch reveal not supported",
		"batch reveal is not supported",
		"batch token key reveal unsupported",
		"batch token key reveal not supported",
		"batch token key reveal is not supported",
		"batch token keys unsupported",
		"batch token keys not supported",
		"batch token keys are not supported",
		"this endpoint is not supported",
		"不支持",
		"暂不支持",
		"未实现",
		"批量获取令牌密钥不支持",
		"不支持批量获取令牌密钥",
		"暂不支持批量获取令牌密钥":
		return true
	default:
		return false
	}
}

func (a newAPIUpstreamProviderAdapter) fetchProfile(ctx context.Context, session *newAPISession) (*newAPIUserProfile, error) {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIUserProfilePath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[*newAPIUserProfile]
	status, err := a.doJSON(ctx, session.client, http.MethodGet, endpoint, session.userID, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi get profile failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi get profile returned status %d", status)
	}
	if !payload.Success {
		return nil, fmt.Errorf("newapi get profile failed%s", safeNewAPIMessage(payload.Message))
	}
	if payload.Data == nil {
		return nil, fmt.Errorf("newapi get profile returned null data")
	}
	if !finiteNewAPINumber(payload.Data.Quota) || !finiteNewAPINumber(payload.Data.UsedQuota) {
		return nil, fmt.Errorf("newapi get profile returned invalid quota")
	}
	return payload.Data, nil
}

func (a newAPIUpstreamProviderAdapter) fetchStatus(ctx context.Context, session *newAPISession) (*newAPIStatusData, error) {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIStatusPath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[newAPIStatusData]
	status, err := a.doJSON(ctx, session.client, http.MethodGet, endpoint, session.userID, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi get status failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi get status returned status %d", status)
	}
	if !payload.Success {
		return nil, fmt.Errorf("newapi get status failed%s", safeNewAPIMessage(payload.Message))
	}
	return &payload.Data, nil
}

func (a newAPIUpstreamProviderAdapter) fetchTodayUsage(ctx context.Context, session *newAPISession) (*newAPIUsageStat, error) {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIUserStatPath)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("type", "0")
	q.Set("token_name", "")
	q.Set("model_name", "")
	q.Set("start_timestamp", strconv.FormatInt(start, 10))
	q.Set("end_timestamp", strconv.FormatInt(now.Unix(), 10))
	q.Set("group", "")
	u.RawQuery = q.Encode()
	var payload newAPIEnvelope[newAPIUsageStat]
	status, err := a.doJSON(ctx, session.client, http.MethodGet, u.String(), session.userID, nil, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi get today usage failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi get today usage returned status %d", status)
	}
	if !payload.Success || !finiteNewAPINumber(payload.Data.Quota) {
		return nil, fmt.Errorf("newapi get today usage returned incompatible response")
	}
	return &payload.Data, nil
}

func (newAPIUpstreamProviderAdapter) doJSON(ctx context.Context, client *http.Client, method, endpoint string, userID int64, body any, out any) (int, error) {
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
	req.Header.Set("User-Agent", "sub2api-newapi-sync/1.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if userID > 0 {
		req.Header.Set("New-Api-User", strconv.FormatInt(userID, 10))
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

func newAPIProfileExtraUpdates(cfg *UpstreamConfig, profile *newAPIUserProfile, profileErr error, status *newAPIStatusData, statusErr error) (map[string]any, string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if profile == nil && profileErr == nil {
		profileErr = fmt.Errorf("newapi profile returned null data")
	}
	if profileErr != nil {
		updates := map[string]any{
			"upstream_provider_snapshot_last_error":    newAPIUpstreamProviderAdapter{}.SanitizeError(profileErr, cfg.Credentials),
			"upstream_provider_snapshot_last_error_at": now,
		}
		concurrencyUpdates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, nil, profileErr)
		for key, value := range concurrencyUpdates {
			updates[key] = value
		}
		return updates, warning
	}
	amounts := newAPIQuotaAmounts(profile.Quota, profile.UsedQuota, status)
	lastError := ""
	lastErrorAt := ""
	if statusErr != nil {
		lastError = newAPIUpstreamProviderAdapter{}.SanitizeError(statusErr, cfg.Credentials)
		lastErrorAt = now
	}
	updates := map[string]any{
		"upstream_provider_snapshot": map[string]any{
			"version":                       1,
			"provider":                      UpstreamProviderNewAPI,
			"synced_at":                     now,
			"user_id":                       profile.ID,
			"email":                         strings.TrimSpace(profile.Email),
			"username":                      strings.TrimSpace(profile.Username),
			"display_name":                  strings.TrimSpace(profile.DisplayName),
			"group":                         strings.TrimSpace(profile.Group),
			"quota":                         profile.Quota,
			"quota_raw":                     profile.Quota,
			"used_quota":                    profile.UsedQuota,
			"used_quota_raw":                profile.UsedQuota,
			"remain_quota":                  profile.Quota,
			"remain_quota_raw":              profile.Quota,
			"total_quota":                   amounts.TotalRaw,
			"total_quota_raw":               amounts.TotalRaw,
			"balance_amount":                amounts.BalanceAmount,
			"used_amount":                   amounts.UsedAmount,
			"total_amount":                  amounts.TotalAmount,
			"base_balance_amount":           amounts.BaseBalanceAmount,
			"base_used_amount":              amounts.BaseUsedAmount,
			"base_total_amount":             amounts.BaseTotalAmount,
			"total_amount_semantics":        "derived_quota",
			"currency":                      amounts.Currency,
			"currency_symbol":               amounts.Symbol,
			"quota_display_type":            amounts.DisplayType,
			"quota_per_unit":                amounts.QuotaPerUnit,
			"usd_exchange_rate":             amounts.USDExchangeRate,
			"custom_currency_symbol":        amounts.CustomCurrencySymbol,
			"custom_currency_exchange_rate": amounts.CustomCurrencyExchangeRate,
			"request_count":                 profile.RequestCount,
			"unit":                          "currency",
		},
		"upstream_provider_snapshot_last_error":    lastError,
		"upstream_provider_snapshot_last_error_at": lastErrorAt,
	}
	concurrencyUpdates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderNewAPI, profile.Concurrency, nil)
	for key, value := range concurrencyUpdates {
		updates[key] = value
	}
	return updates, warning
}

type newAPIQuotaSnapshotAmounts struct {
	BaseBalanceAmount          float64
	BaseUsedAmount             float64
	BaseTotalAmount            float64
	BalanceAmount              float64
	UsedAmount                 float64
	TotalAmount                float64
	TotalRaw                   float64
	Currency                   string
	Symbol                     string
	DisplayType                string
	QuotaPerUnit               float64
	USDExchangeRate            float64
	CustomCurrencySymbol       string
	CustomCurrencyExchangeRate float64
}

func newAPIQuotaAmounts(balanceRaw, usedRaw float64, status *newAPIStatusData) newAPIQuotaSnapshotAmounts {
	quotaPerUnit := 500000.0
	displayType := "USD"
	usdRate := 0.0
	customRate := 1.0
	customSymbol := "¤"
	if status != nil {
		if finiteNewAPINumber(status.QuotaPerUnit) && status.QuotaPerUnit > 0 {
			quotaPerUnit = status.QuotaPerUnit
		}
		if strings.TrimSpace(status.QuotaDisplayType) != "" {
			displayType = strings.ToUpper(strings.TrimSpace(status.QuotaDisplayType))
		}
		if finiteNewAPINumber(status.USDExchangeRate) && status.USDExchangeRate > 0 {
			usdRate = status.USDExchangeRate
		}
		if finiteNewAPINumber(status.CustomCurrencyExchangeRate) && status.CustomCurrencyExchangeRate > 0 {
			customRate = status.CustomCurrencyExchangeRate
		}
		if strings.TrimSpace(status.CustomCurrencySymbol) != "" {
			customSymbol = strings.TrimSpace(status.CustomCurrencySymbol)
		}
	}
	rate := 1.0
	currency := "USD"
	symbol := "$"
	switch displayType {
	case "CNY":
		rate = usdRate
		currency = "CNY"
		symbol = "¥"
	case "CUSTOM":
		rate = customRate
		currency = "CUSTOM"
		symbol = customSymbol
	case "TOKENS":
		rate = quotaPerUnit
		currency = "TOKENS"
		symbol = ""
	default:
		displayType = "USD"
	}
	toAmount := func(raw float64) float64 {
		if displayType == "TOKENS" {
			return raw
		}
		return raw / quotaPerUnit * rate
	}
	totalRaw := balanceRaw + usedRaw
	toBaseAmount := func(raw float64) float64 { return raw / quotaPerUnit }
	return newAPIQuotaSnapshotAmounts{
		BaseBalanceAmount:          toBaseAmount(balanceRaw),
		BaseUsedAmount:             toBaseAmount(usedRaw),
		BaseTotalAmount:            toBaseAmount(totalRaw),
		BalanceAmount:              toAmount(balanceRaw),
		UsedAmount:                 toAmount(usedRaw),
		TotalAmount:                toAmount(totalRaw),
		TotalRaw:                   totalRaw,
		Currency:                   currency,
		Symbol:                     symbol,
		DisplayType:                displayType,
		QuotaPerUnit:               quotaPerUnit,
		USDExchangeRate:            usdRate,
		CustomCurrencySymbol:       customSymbol,
		CustomCurrencyExchangeRate: customRate,
	}
}

func parseNewAPIRatio(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if finiteNewAPINumber(v) && v >= 0 {
			return v, true
		}
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil && finiteNewAPINumber(parsed) && parsed >= 0 {
			return parsed, true
		}
	}
	return 0, false
}

func finiteNewAPINumber(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func isMaskedUpstreamKey(key string) bool {
	return strings.Contains(key, "*") || strings.Contains(key, "…") || strings.Contains(key, "...")
}

func safeNewAPIMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	return ": " + logredact.RedactText(message, "api_key", "jwt", "authorization", "bearer", "token", "access_token", "refresh_token", "cookie", "session", "password")
}

func infraBadRequest(code, message string) error {
	return infraerrors.BadRequest(code, message)
}
