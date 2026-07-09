package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
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
	newAPIStatusPath         = "/api/status"
	newAPIKeysPageSize       = 100
	newAPIMaxKeyListPages    = 1000
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

type newAPIGroupInfo struct {
	Desc  string `json:"desc"`
	Ratio any    `json:"ratio"`
}

type newAPIKeyListData struct {
	Page     int            `json:"page"`
	PageSize int            `json:"page_size"`
	Total    int            `json:"total"`
	Items    []newAPIKeyRow `json:"items"`
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

type newAPIUserProfile struct {
	ID           int64   `json:"id"`
	Email        string  `json:"email"`
	Username     string  `json:"username"`
	DisplayName  string  `json:"display_name"`
	Group        string  `json:"group"`
	Quota        float64 `json:"quota"`
	UsedQuota    float64 `json:"used_quota"`
	RequestCount float64 `json:"request_count"`
}

type newAPIStatusData struct {
	QuotaDisplayType           string  `json:"quota_display_type"`
	QuotaPerUnit               float64 `json:"quota_per_unit"`
	USDExchangeRate            float64 `json:"usd_exchange_rate"`
	CustomCurrencySymbol       string  `json:"custom_currency_symbol"`
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
}

func (newAPIUpstreamProviderAdapter) Provider() string { return UpstreamProviderNewAPI }

func (newAPIUpstreamProviderAdapter) ValidateConfig(config *UpstreamConfig, requireSecrets bool) error {
	username := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPILoginUsername))
	password := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialNewAPILoginPassword))
	if username == "" {
		return infraBadRequest("UPSTREAM_NEWAPI_USERNAME_REQUIRED", "newapi login username is required")
	}
	if requireSecrets && password == "" {
		return infraBadRequest("UPSTREAM_NEWAPI_PASSWORD_REQUIRED", "newapi login password is required")
	}
	if !requireSecrets && password == "" {
		return infraBadRequest("UPSTREAM_NEWAPI_PASSWORD_REQUIRED", "newapi login password is required")
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
	rows, err := a.fetchKeys(ctx, session)
	if err != nil {
		return nil, err
	}
	fullKeys, err := a.fetchMaskedKeySecrets(ctx, session, rows)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	keys := make([]UpstreamKey, 0, len(rows))
	for _, row := range rows {
		key := strings.TrimSpace(row.Key)
		if isMaskedUpstreamKey(key) {
			key = strings.TrimSpace(fullKeys[row.ID])
		}
		if key == "" || isMaskedUpstreamKey(key) {
			continue
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
		keys = append(keys, UpstreamKey{
			UpstreamConfigID:  cfg.ID,
			Name:              normalizeUpstreamDisplayName(row.Name, 100),
			Key:               key,
			KeyHash:           HashUpstreamKey(key),
			RemoteKeyID:       &row.ID,
			UpstreamGroupName: normalizeUpstreamDisplayName(group, 100),
			Platform:          PlatformOpenAI,
			RateMultiplier:    rate,
			Status:            status,
			LastSeenAt:        &now,
			Extra:             extra,
		})
	}
	out := &upstreamProviderSnapshot{Keys: keys}
	if includeProfile {
		profile, profileErr := a.fetchProfile(ctx, session)
		status, statusErr := a.fetchStatus(ctx, session)
		out.ExtraUpdates = newAPIProfileExtraUpdates(cfg, profile, profileErr, status, statusErr)
	}
	return out, nil
}

func (newAPIUpstreamProviderAdapter) SanitizeError(err error, credentials map[string]any) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	for _, key := range []string{
		AccountCredentialNewAPILoginUsername,
		AccountCredentialNewAPILoginPassword,
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
	rootURL, err := normalizeSub2APIBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	normalizedProxyURL, err := normalizeSub2APIProxyURL(proxyURL)
	if err != nil {
		return nil, err
	}
	client, err := sub2APIHTTPClient(normalizedProxyURL)
	if err != nil {
		return nil, err
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

func (a newAPIUpstreamProviderAdapter) fetchKeys(ctx context.Context, session *newAPISession) ([]newAPIKeyRow, error) {
	out := make([]newAPIKeyRow, 0)
	for page := 0; page < newAPIMaxKeyListPages; page++ {
		endpoint, err := buildSub2APIURL(session.rootURL, newAPITokensPath)
		if err != nil {
			return nil, err
		}
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		q := u.Query()
		q.Set("p", strconv.Itoa(page))
		q.Set("size", strconv.Itoa(newAPIKeysPageSize))
		u.RawQuery = q.Encode()

		var payload newAPIEnvelope[newAPIKeyListData]
		status, err := a.doJSON(ctx, session.client, http.MethodGet, u.String(), session.userID, nil, &payload)
		if err != nil {
			return nil, fmt.Errorf("newapi list tokens failed: %w", err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("newapi list tokens returned status %d", status)
		}
		if !payload.Success {
			return nil, fmt.Errorf("newapi list tokens failed%s", safeNewAPIMessage(payload.Message))
		}
		out = append(out, payload.Data.Items...)
		if payload.Data.Total > 0 && len(out) >= payload.Data.Total {
			return out, nil
		}
		if len(payload.Data.Items) < newAPIKeysPageSize {
			return out, nil
		}
	}
	return nil, fmt.Errorf("newapi token list exceeded max pages")
}

func (a newAPIUpstreamProviderAdapter) fetchMaskedKeySecrets(ctx context.Context, session *newAPISession, rows []newAPIKeyRow) (map[int64]string, error) {
	ids := make([]int64, 0)
	for _, row := range rows {
		if row.ID <= 0 {
			continue
		}
		key := strings.TrimSpace(row.Key)
		if key != "" && isMaskedUpstreamKey(key) {
			ids = append(ids, row.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	endpoint, err := buildSub2APIURL(session.rootURL, newAPITokenBatchKeysPath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[newAPIBatchKeysData]
	status, err := a.doJSON(ctx, session.client, http.MethodPost, endpoint, session.userID, map[string]any{"ids": ids}, &payload)
	if err != nil {
		return nil, fmt.Errorf("newapi fetch token keys failed: %w", err)
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("newapi fetch token keys returned status %d", status)
	}
	if !payload.Success {
		return nil, fmt.Errorf("newapi fetch token keys failed%s", safeNewAPIMessage(payload.Message))
	}
	out := make(map[int64]string, len(payload.Data.Keys))
	for rawID, key := range payload.Data.Keys {
		id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		out[id] = strings.TrimSpace(key)
	}
	return out, nil
}

func (a newAPIUpstreamProviderAdapter) fetchProfile(ctx context.Context, session *newAPISession) (*newAPIUserProfile, error) {
	endpoint, err := buildSub2APIURL(session.rootURL, newAPIUserProfilePath)
	if err != nil {
		return nil, err
	}
	var payload newAPIEnvelope[newAPIUserProfile]
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
	if !finiteNewAPINumber(payload.Data.Quota) || !finiteNewAPINumber(payload.Data.UsedQuota) {
		return nil, fmt.Errorf("newapi get profile returned invalid quota")
	}
	return &payload.Data, nil
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

func newAPIProfileExtraUpdates(cfg *UpstreamConfig, profile *newAPIUserProfile, profileErr error, status *newAPIStatusData, statusErr error) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	if profileErr != nil {
		return map[string]any{
			"upstream_provider_snapshot_last_error":    newAPIUpstreamProviderAdapter{}.SanitizeError(profileErr, cfg.Credentials),
			"upstream_provider_snapshot_last_error_at": now,
		}
	}
	if profile == nil {
		return nil
	}
	if statusErr != nil {
		return map[string]any{
			"upstream_provider_snapshot_last_error":    newAPIUpstreamProviderAdapter{}.SanitizeError(statusErr, cfg.Credentials),
			"upstream_provider_snapshot_last_error_at": now,
		}
	}
	amounts := newAPIQuotaAmounts(profile.Quota, profile.UsedQuota, status)
	return map[string]any{
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
		"upstream_provider_snapshot_last_error":    "",
		"upstream_provider_snapshot_last_error_at": "",
	}
}

type newAPIQuotaSnapshotAmounts struct {
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
	usdRate := 1.0
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
	return newAPIQuotaSnapshotAmounts{
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
