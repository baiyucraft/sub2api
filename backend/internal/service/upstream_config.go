package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	UpstreamProviderSub2API = AccountUpstreamProviderSub2API
	UpstreamProviderNewAPI  = AccountUpstreamProviderNewAPI
	UpstreamProviderOther   = AccountUpstreamProviderOther

	UpstreamAuthModeUserLogin = AccountSub2APIRateSyncAdapterUserLogin
	UpstreamAuthModeManualJWT = AccountSub2APIRateSyncAdapterManualJWT
)

var (
	ErrUpstreamConfigNotFound = infraerrors.NotFound("UPSTREAM_CONFIG_NOT_FOUND", "upstream config not found")
	ErrUpstreamKeyNotFound    = infraerrors.NotFound("UPSTREAM_KEY_NOT_FOUND", "upstream key not found")
)

type UpstreamConfig struct {
	ID            int64
	Name          string
	Provider      string
	BaseURL       string
	AuthMode      string
	Credentials   map[string]any
	Extra         map[string]any
	ProxyID       *int64
	Status        string
	LastError     *string
	LastCheckedAt *time.Time
	LastSuccessAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time

	Keys []*UpstreamKey
}

type UpstreamKey struct {
	ID                int64
	UpstreamConfigID  int64
	Name              string
	Key               string
	KeyHash           string
	RemoteKeyID       *int64
	UpstreamGroupID   *int64
	UpstreamGroupName string
	Platform          string
	RateMultiplier    *float64
	Status            string
	LastSeenAt        *time.Time
	Extra             map[string]any
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type UpstreamConfigRepository interface {
	List(ctx context.Context, params pagination.PaginationParams, provider, status, search string) ([]UpstreamConfig, *pagination.PaginationResult, error)
	GetByID(ctx context.Context, id int64) (*UpstreamConfig, error)
	Create(ctx context.Context, config *UpstreamConfig) error
	Update(ctx context.Context, config *UpstreamConfig) error
	Delete(ctx context.Context, id int64) error
	CountAccounts(ctx context.Context, id int64) (int64, error)
	ListKeys(ctx context.Context, upstreamConfigID int64) ([]UpstreamKey, error)
	GetKeyByID(ctx context.Context, id int64) (*UpstreamKey, error)
	CreateKey(ctx context.Context, key *UpstreamKey) error
	UpsertKey(ctx context.Context, key *UpstreamKey) error
	UpdateKey(ctx context.Context, key *UpstreamKey) error
	DeleteKey(ctx context.Context, id int64) error
	RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error
	SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type UpstreamConfigService struct {
	repo        UpstreamConfigRepository
	proxyRepo   ProxyRepository
	accountRepo AccountRepository
	syncLocks   sync.Map
}

type UpstreamConfigSyncResult struct {
	ConfigID            int64  `json:"config_id"`
	Name                string `json:"name"`
	Success             bool   `json:"success"`
	KeyCount            int    `json:"key_count"`
	UpdatedAccountCount int    `json:"updated_account_count"`
	Error               string `json:"error,omitempty"`
}

type UpstreamAccountNameBackfillItem struct {
	AccountID        int64  `json:"account_id"`
	OldName          string `json:"old_name"`
	NewName          string `json:"new_name,omitempty"`
	UpstreamConfigID *int64 `json:"upstream_config_id,omitempty"`
	UpstreamKeyID    *int64 `json:"upstream_key_id,omitempty"`
	SkipReason       string `json:"skip_reason,omitempty"`
}

type upstreamConfigAtomicSyncRepository interface {
	ApplySyncSnapshot(ctx context.Context, configID int64, keys []UpstreamKey, extraUpdates map[string]any, checkedAt time.Time) ([]UpstreamKey, int, error)
}

type upstreamAccountNameBackfillRepository interface {
	PreviewAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error)
	ApplyAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error)
}

type upstreamProviderSnapshot struct {
	Keys            []UpstreamKey
	RefreshedTokens *sub2APIRefreshData
	ExtraUpdates    map[string]any
}

type upstreamProviderAdapter interface {
	Provider() string
	ValidateConfig(config *UpstreamConfig, requireSecrets bool) error
	Test(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error
	SyncSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*upstreamProviderSnapshot, error)
	SanitizeError(err error, credentials map[string]any) string
}

func NewUpstreamConfigService(repo UpstreamConfigRepository, proxyRepo ProxyRepository, accountRepo AccountRepository) *UpstreamConfigService {
	return &UpstreamConfigService{repo: repo, proxyRepo: proxyRepo, accountRepo: accountRepo}
}

func (s *UpstreamConfigService) List(ctx context.Context, params pagination.PaginationParams, provider, status, search string) ([]UpstreamConfig, *pagination.PaginationResult, error) {
	return s.repo.List(ctx, params, provider, status, search)
}

func (s *UpstreamConfigService) GetByID(ctx context.Context, id int64) (*UpstreamConfig, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *UpstreamConfigService) Create(ctx context.Context, config *UpstreamConfig) (*UpstreamConfig, error) {
	if err := normalizeAndValidateUpstreamConfig(config, true); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, config); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, config.ID)
}

func (s *UpstreamConfigService) Update(ctx context.Context, id int64, patch *UpstreamConfig) (*UpstreamConfig, error) {
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrUpstreamConfigNotFound
	}
	current.Name = upstreamFirstNonEmpty(patch.Name, current.Name)
	current.Provider = normalizeUpstreamProvider(upstreamFirstNonEmpty(patch.Provider, current.Provider))
	current.BaseURL = upstreamFirstNonEmpty(patch.BaseURL, current.BaseURL)
	current.AuthMode = normalizeUpstreamAuthMode(upstreamFirstNonEmpty(patch.AuthMode, current.AuthMode))
	if patch.ProxyID != nil {
		if *patch.ProxyID == 0 {
			current.ProxyID = nil
		} else {
			current.ProxyID = patch.ProxyID
		}
	}
	if patch.Status != "" {
		current.Status = patch.Status
	}
	current.Credentials = mergePreservingUpstreamSecrets(current.Credentials, patch.Credentials)
	if patch.Extra != nil {
		current.Extra = patch.Extra
	}
	if err := normalizeAndValidateUpstreamConfig(current, false); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, current); err != nil {
		return nil, err
	}
	return s.repo.GetByID(ctx, id)
}

func (s *UpstreamConfigService) Delete(ctx context.Context, id int64) error {
	count, err := s.repo.CountAccounts(ctx, id)
	if err != nil {
		return err
	}
	if count > 0 {
		return infraerrors.New(http.StatusBadRequest, "UPSTREAM_CONFIG_IN_USE", "upstream config is used by accounts")
	}
	return s.repo.Delete(ctx, id)
}

func (s *UpstreamConfigService) ListKeys(ctx context.Context, upstreamConfigID int64) ([]UpstreamKey, error) {
	return s.repo.ListKeys(ctx, upstreamConfigID)
}

func (s *UpstreamConfigService) PreviewAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error) {
	repo, ok := s.repo.(upstreamAccountNameBackfillRepository)
	if !ok {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "UPSTREAM_ACCOUNT_NAME_BACKFILL_UNAVAILABLE", "upstream account name backfill is unavailable")
	}
	return repo.PreviewAccountNameBackfill(ctx)
}

func (s *UpstreamConfigService) ApplyAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error) {
	repo, ok := s.repo.(upstreamAccountNameBackfillRepository)
	if !ok {
		return nil, infraerrors.New(http.StatusServiceUnavailable, "UPSTREAM_ACCOUNT_NAME_BACKFILL_UNAVAILABLE", "upstream account name backfill is unavailable")
	}
	return repo.ApplyAccountNameBackfill(ctx)
}

func (s *UpstreamConfigService) CreateKey(ctx context.Context, upstreamConfigID int64, key *UpstreamKey) (*UpstreamKey, error) {
	if key == nil {
		return nil, infraerrors.BadRequest("UPSTREAM_KEY_REQUIRED", "upstream key is required")
	}
	key.UpstreamConfigID = upstreamConfigID
	if err := normalizeAndValidateUpstreamKey(key); err != nil {
		return nil, err
	}
	if err := s.repo.CreateKey(ctx, key); err != nil {
		return nil, err
	}
	return s.repo.GetKeyByID(ctx, key.ID)
}

func (s *UpstreamConfigService) DeleteKey(ctx context.Context, id int64) error {
	return s.repo.DeleteKey(ctx, id)
}

func (s *UpstreamConfigService) Test(ctx context.Context, id int64) error {
	cfg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if cfg == nil {
		return ErrUpstreamConfigNotFound
	}
	adapter, ok := upstreamProviderAdapterFor(cfg.Provider)
	if !ok {
		return upstreamProviderUnsupportedError(cfg.Provider)
	}
	proxyURL, err := s.resolveUpstreamConfigProxyURL(ctx, cfg)
	if err != nil {
		_ = s.repo.RecordCheckResult(ctx, id, false, adapter.SanitizeError(err, cfg.Credentials))
		return err
	}
	if cfg.Provider == UpstreamProviderSub2API {
		var snapshot *upstreamProviderSnapshot
		snapshot, err = adapter.SyncSnapshot(ctx, cfg, proxyURL, false)
		if snapshot != nil && snapshot.RefreshedTokens != nil {
			if saveErr := s.repo.SaveRefreshedTokens(ctx, cfg.ID, snapshot.RefreshedTokens.AccessToken, snapshot.RefreshedTokens.RefreshToken, snapshot.RefreshedTokens.ExpiresAt); saveErr != nil {
				err = saveErr
			}
		}
	} else {
		err = adapter.Test(ctx, cfg, proxyURL)
	}
	if err != nil {
		safeErr := adapter.SanitizeError(err, cfg.Credentials)
		_ = s.repo.RecordCheckResult(ctx, id, false, safeErr)
		return upstreamProviderSyncError(cfg.Provider, safeErr)
	}
	return s.repo.RecordCheckResult(ctx, id, true, "")
}

func (s *UpstreamConfigService) SyncKeys(ctx context.Context, id int64) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
	cfg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, UpstreamConfigSyncResult{}, err
	}
	if cfg == nil {
		return nil, UpstreamConfigSyncResult{}, ErrUpstreamConfigNotFound
	}
	return s.syncProviderConfig(ctx, cfg)
}

func (s *UpstreamConfigService) SyncActiveSub2APIConfigs(ctx context.Context) []UpstreamConfigSyncResult {
	return s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API})
}

func (s *UpstreamConfigService) SyncActiveUpstreamConfigs(ctx context.Context) []UpstreamConfigSyncResult {
	return s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API, UpstreamProviderNewAPI})
}

func (s *UpstreamConfigService) syncActiveUpstreamConfigs(ctx context.Context, providers []string) []UpstreamConfigSyncResult {
	configs, listErr := s.listActiveUpstreamConfigs(ctx, providers)
	if listErr != nil {
		return []UpstreamConfigSyncResult{{
			Success: false,
			Error:   logredact.RedactText(listErr.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token"),
		}}
	}
	results := make([]UpstreamConfigSyncResult, 0, len(configs))
	for i := range configs {
		cfg := configs[i]
		_, result, err := s.syncProviderConfig(ctx, &cfg)
		if err != nil {
			result.Success = false
			if adapter, ok := upstreamProviderAdapterFor(cfg.Provider); ok {
				result.Error = adapter.SanitizeError(err, cfg.Credentials)
			} else {
				result.Error = logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")
			}
		}
		results = append(results, result)
	}
	return results
}

func (s *UpstreamConfigService) syncProviderConfig(ctx context.Context, cfg *UpstreamConfig) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
	if cfg != nil && cfg.ID > 0 {
		unlock := s.lockUpstreamConfigSync(cfg.ID)
		defer unlock()
		latest, err := s.repo.GetByID(ctx, cfg.ID)
		if err != nil {
			result := UpstreamConfigSyncResult{ConfigID: cfg.ID, Name: cfg.Name, Error: logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")}
			return nil, result, err
		}
		cfg = latest
	}
	result := UpstreamConfigSyncResult{}
	if cfg != nil {
		result.ConfigID = cfg.ID
		result.Name = cfg.Name
	}
	if cfg == nil {
		err := fmt.Errorf("missing upstream config")
		result.Error = err.Error()
		return nil, result, err
	}
	adapter, ok := upstreamProviderAdapterFor(cfg.Provider)
	if !ok {
		err := upstreamProviderUnsupportedError(cfg.Provider)
		result.Error = err.Error()
		return nil, result, err
	}
	proxyURL, err := s.resolveUpstreamConfigProxyURL(ctx, cfg)
	if err != nil {
		_ = s.repo.RecordCheckResult(ctx, cfg.ID, false, adapter.SanitizeError(err, cfg.Credentials))
		result.Error = adapter.SanitizeError(err, cfg.Credentials)
		return nil, result, err
	}
	snapshot, err := adapter.SyncSnapshot(ctx, cfg, proxyURL, true)
	if snapshot != nil && snapshot.RefreshedTokens != nil {
		if saveErr := s.repo.SaveRefreshedTokens(ctx, cfg.ID, snapshot.RefreshedTokens.AccessToken, snapshot.RefreshedTokens.RefreshToken, snapshot.RefreshedTokens.ExpiresAt); saveErr != nil {
			result.Error = adapter.SanitizeError(saveErr, cfg.Credentials)
			return nil, result, saveErr
		}
	}
	if err != nil {
		safeErr := adapter.SanitizeError(err, cfg.Credentials)
		_ = s.repo.RecordCheckResult(ctx, cfg.ID, false, safeErr)
		result.Error = safeErr
		return nil, result, upstreamProviderSyncError(cfg.Provider, safeErr)
	}
	if snapshot == nil {
		err := fmt.Errorf("upstream provider returned no snapshot")
		result.Error = adapter.SanitizeError(err, cfg.Credentials)
		return nil, result, err
	}
	keys := snapshot.Keys
	for i := range keys {
		if err := normalizeAndValidateUpstreamKey(&keys[i]); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
	}
	result.KeyCount = len(keys)
	if atomicRepo, ok := s.repo.(upstreamConfigAtomicSyncRepository); ok {
		localKeys, updated, applyErr := atomicRepo.ApplySyncSnapshot(ctx, cfg.ID, keys, snapshot.ExtraUpdates, time.Now().UTC())
		if applyErr != nil {
			result.Error = adapter.SanitizeError(applyErr, cfg.Credentials)
			_ = s.repo.RecordCheckResult(ctx, cfg.ID, false, result.Error)
			return nil, result, applyErr
		}
		result.UpdatedAccountCount = updated
		result.Success = true
		return localKeys, result, nil
	}

	// Test doubles and legacy repositories retain the pre-transactional path.
	// Production repositories implement upstreamConfigAtomicSyncRepository.
	for i := range keys {
		if err := s.repo.UpsertKey(ctx, &keys[i]); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
	}
	localKeys, err := s.repo.ListKeys(ctx, cfg.ID)
	if err != nil {
		result.Error = adapter.SanitizeError(err, cfg.Credentials)
		return nil, result, err
	}
	updated, err := s.syncBoundAccountRates(ctx, cfg, localKeys)
	if err != nil {
		result.Error = adapter.SanitizeError(err, cfg.Credentials)
		return nil, result, err
	}
	result.UpdatedAccountCount = updated
	if len(snapshot.ExtraUpdates) > 0 {
		if err := s.repo.UpdateExtra(ctx, cfg.ID, snapshot.ExtraUpdates); err != nil {
			result.Error = logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token", "cookie", "session")
			_ = s.repo.RecordCheckResult(ctx, cfg.ID, false, result.Error)
			return nil, result, err
		}
	}
	result.Success = true
	_ = s.repo.RecordCheckResult(ctx, cfg.ID, true, "")
	return localKeys, result, nil
}

func sub2APIProfileExtraUpdates(cfg *UpstreamConfig, profile *sub2APIProfile, profileErr error) map[string]any {
	now := time.Now().UTC().Format(time.RFC3339)
	if profileErr != nil {
		return map[string]any{
			"sub2api_balance_last_error":    sanitizeStandaloneSub2APIError(profileErr, cfg.Credentials),
			"sub2api_balance_last_error_at": now,
		}
	}
	if profile == nil {
		return nil
	}
	return map[string]any{
		"sub2api_balance":               profile.Balance,
		"sub2api_total_recharged":       profile.TotalRecharged,
		"sub2api_user_email":            strings.TrimSpace(profile.Email),
		"sub2api_user_id":               profile.ID,
		"sub2api_balance_synced_at":     now,
		"sub2api_balance_last_error":    "",
		"sub2api_balance_last_error_at": "",
	}
}

func (s *UpstreamConfigService) lockUpstreamConfigSync(id int64) func() {
	if s == nil || id <= 0 {
		return func() {}
	}
	actual, _ := s.syncLocks.LoadOrStore(id, &sync.Mutex{})
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *UpstreamConfigService) listActiveUpstreamConfigs(ctx context.Context, providers []string) ([]UpstreamConfig, error) {
	const pageSize = 200
	out := make([]UpstreamConfig, 0)
	for _, provider := range providers {
		if _, ok := upstreamProviderAdapterFor(provider); !ok {
			continue
		}
		for page := 1; ; page++ {
			configs, result, err := s.repo.List(ctx, pagination.PaginationParams{
				Page:     page,
				PageSize: pageSize,
			}, provider, StatusActive, "")
			if err != nil {
				return nil, err
			}
			out = append(out, configs...)
			if result == nil || page >= result.Pages || len(configs) == 0 {
				break
			}
		}
	}
	return out, nil
}

func (s *UpstreamConfigService) syncBoundAccountRates(ctx context.Context, cfg *UpstreamConfig, keys []UpstreamKey) (int, error) {
	if s.accountRepo == nil || cfg == nil || cfg.ID <= 0 || len(keys) == 0 {
		return 0, nil
	}
	keyRates := make(map[int64]UpstreamKey, len(keys))
	for i := range keys {
		if keys[i].ID <= 0 || keys[i].RateMultiplier == nil {
			continue
		}
		keyRates[keys[i].ID] = keys[i]
	}
	if len(keyRates) == 0 {
		return 0, nil
	}

	accounts, err := s.listAccountsForUpstreamConfig(ctx, cfg.ID)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	updated := 0
	for i := range accounts {
		account := accounts[i]
		if account.UpstreamKeyID == nil {
			continue
		}
		key, ok := keyRates[*account.UpstreamKeyID]
		if !ok || key.RateMultiplier == nil {
			continue
		}
		multiplier := *key.RateMultiplier
		if multiplier < 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
			continue
		}
		priority := Sub2APIUpstreamPriority(multiplier)
		loadFactor := AutoUpstreamLoadFactor(priority, account.Concurrency)
		extra := map[string]any{
			"upstream_rate_sync_last_success_at": now,
			"upstream_rate_sync_last_error":      "",
			"upstream_provider":                  cfg.Provider,
			"upstream_platform":                  key.Platform,
			"upstream_rate_multiplier":           multiplier,
		}
		if key.UpstreamGroupID != nil {
			extra["upstream_group_id"] = *key.UpstreamGroupID
		}
		if strings.TrimSpace(key.UpstreamGroupName) != "" {
			extra["upstream_group_name"] = key.UpstreamGroupName
		}
		if cfg.Provider == UpstreamProviderSub2API {
			extra["sub2api_rate_sync_last_success_at"] = now
			extra["sub2api_rate_sync_last_error"] = ""
			extra["sub2api_upstream_platform"] = key.Platform
			extra["sub2api_upstream_rate_multiplier"] = multiplier
			if key.UpstreamGroupID != nil {
				extra["sub2api_upstream_group_id"] = *key.UpstreamGroupID
			}
			if strings.TrimSpace(key.UpstreamGroupName) != "" {
				extra["sub2api_upstream_group_name"] = key.UpstreamGroupName
			}
		}
		if _, err := s.accountRepo.BulkUpdate(ctx, []int64{account.ID}, AccountBulkUpdate{
			RateMultiplier: &multiplier,
			Priority:       &priority,
			LoadFactor:     &loadFactor,
			Extra:          extra,
		}); err != nil {
			return updated, fmt.Errorf("update bound account %d rate: %w", account.ID, err)
		}
		updated++
	}
	return updated, nil
}

func (s *UpstreamConfigService) listAccountsForUpstreamConfig(ctx context.Context, upstreamConfigID int64) ([]Account, error) {
	const pageSize = 500
	out := make([]Account, 0)
	for page := 1; ; page++ {
		accounts, result, err := s.accountRepo.ListWithFilters(ctx, pagination.PaginationParams{
			Page:     page,
			PageSize: pageSize,
		}, "", AccountTypeAPIKey, "", "", 0, "")
		if err != nil {
			return nil, err
		}
		for i := range accounts {
			if accounts[i].UpstreamConfigID != nil && *accounts[i].UpstreamConfigID == upstreamConfigID {
				out = append(out, accounts[i])
			}
		}
		if result == nil || page >= result.Pages || len(accounts) == 0 {
			return out, nil
		}
	}
}

func (s *UpstreamConfigService) resolveUpstreamConfigProxyURL(ctx context.Context, cfg *UpstreamConfig) (string, error) {
	if cfg == nil || cfg.ProxyID == nil {
		return "", nil
	}
	if s.proxyRepo == nil {
		return "", infraerrors.New(http.StatusServiceUnavailable, "UPSTREAM_PROXY_UNAVAILABLE", "upstream config proxy service is unavailable")
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *cfg.ProxyID)
	if err != nil {
		return "", err
	}
	if proxy == nil {
		return "", ErrProxyNotFound
	}
	return proxy.URL(), nil
}

func normalizeAndValidateUpstreamConfig(config *UpstreamConfig, requireSecrets bool) error {
	if config == nil {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_REQUIRED", "upstream config is required")
	}
	config.Name = strings.TrimSpace(config.Name)
	config.Provider = normalizeUpstreamProvider(config.Provider)
	config.AuthMode = normalizeUpstreamAuthMode(config.AuthMode)
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	if config.Status == "" {
		config.Status = StatusActive
	}
	if config.Name == "" {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_NAME_REQUIRED", "upstream config name is required")
	}
	if config.BaseURL == "" {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_BASE_URL_REQUIRED", "upstream config base url is required")
	}
	if config.Credentials == nil {
		config.Credentials = map[string]any{}
	}
	if config.Extra == nil {
		config.Extra = map[string]any{}
	}
	if adapter, ok := upstreamProviderAdapterFor(config.Provider); ok {
		return adapter.ValidateConfig(config, requireSecrets)
	}
	if config.Provider != UpstreamProviderSub2API && config.Provider != UpstreamProviderNewAPI {
		return nil
	}
	return nil
}

func normalizeAndValidateUpstreamKey(key *UpstreamKey) error {
	if key == nil {
		return infraerrors.BadRequest("UPSTREAM_KEY_REQUIRED", "upstream key is required")
	}
	key.Name = strings.TrimSpace(key.Name)
	key.Key = strings.TrimSpace(key.Key)
	key.Platform = strings.TrimSpace(key.Platform)
	if key.Platform == "" {
		key.Platform = PlatformOpenAI
	}
	if key.Status == "" {
		key.Status = StatusActive
	}
	if key.UpstreamConfigID <= 0 {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_ID_REQUIRED", "upstream config id is required")
	}
	if key.Key == "" {
		return infraerrors.BadRequest("UPSTREAM_KEY_SECRET_REQUIRED", "upstream key is required")
	}
	if key.KeyHash == "" {
		key.KeyHash = HashUpstreamKey(key.Key)
	}
	if key.RateMultiplier != nil && (*key.RateMultiplier < 0 || math.IsNaN(*key.RateMultiplier) || math.IsInf(*key.RateMultiplier, 0)) {
		return infraerrors.BadRequest("UPSTREAM_KEY_RATE_INVALID", "upstream key rate multiplier is invalid")
	}
	return nil
}

func normalizeUpstreamProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case UpstreamProviderSub2API:
		return UpstreamProviderSub2API
	case UpstreamProviderNewAPI:
		return UpstreamProviderNewAPI
	default:
		return UpstreamProviderOther
	}
}

func normalizeUpstreamAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case UpstreamAuthModeManualJWT:
		return UpstreamAuthModeManualJWT
	default:
		return UpstreamAuthModeUserLogin
	}
}

func upstreamProviderAdapterFor(provider string) (upstreamProviderAdapter, bool) {
	switch normalizeUpstreamProvider(provider) {
	case UpstreamProviderSub2API:
		return sub2APIUpstreamProviderAdapter{}, true
	case UpstreamProviderNewAPI:
		return newAPIUpstreamProviderAdapter{}, true
	default:
		return nil, false
	}
}

func upstreamProviderUnsupportedError(provider string) error {
	return infraerrors.BadRequest("UPSTREAM_PROVIDER_SYNC_UNSUPPORTED", fmt.Sprintf("automatic sync is not supported for %s upstream configs", normalizeUpstreamProvider(provider)))
}

func upstreamProviderSyncError(provider, safeErr string) error {
	safeErr = strings.TrimSpace(safeErr)
	if safeErr == "" {
		safeErr = "upstream sync failed"
	}
	return infraerrors.New(http.StatusBadGateway, "UPSTREAM_SYNC_FAILED", fmt.Sprintf("%s upstream sync failed: %s", normalizeUpstreamProvider(provider), safeErr))
}

type sub2APIUpstreamProviderAdapter struct{}

func (sub2APIUpstreamProviderAdapter) Provider() string { return UpstreamProviderSub2API }

func (sub2APIUpstreamProviderAdapter) ValidateConfig(config *UpstreamConfig, requireSecrets bool) error {
	if config.AuthMode == UpstreamAuthModeManualJWT {
		accessToken := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialSub2APIAccessToken))
		refreshToken := strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialSub2APIRefreshToken))
		if requireSecrets && accessToken == "" && refreshToken == "" {
			return infraerrors.BadRequest("UPSTREAM_TOKEN_REQUIRED", "sub2api access token or refresh token is required")
		}
		if !requireSecrets && accessToken == "" && refreshToken == "" {
			return infraerrors.BadRequest("UPSTREAM_TOKEN_REQUIRED", "sub2api access token or refresh token is required")
		}
		return nil
	}
	if strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialSub2APILoginEmail)) == "" {
		return infraerrors.BadRequest("UPSTREAM_LOGIN_EMAIL_REQUIRED", "sub2api login email is required")
	}
	if requireSecrets && strings.TrimSpace(stringCredential(config.Credentials, AccountCredentialSub2APILoginPassword)) == "" {
		return infraerrors.BadRequest("UPSTREAM_LOGIN_PASSWORD_REQUIRED", "sub2api login password is required")
	}
	return nil
}

func (sub2APIUpstreamProviderAdapter) Test(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error {
	return testSub2APIUpstreamConfig(ctx, cfg, proxyURL)
}

func (sub2APIUpstreamProviderAdapter) SyncSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*upstreamProviderSnapshot, error) {
	snapshot, err := syncSub2APIUpstreamSnapshot(ctx, cfg, proxyURL, includeProfile)
	if snapshot == nil {
		return nil, err
	}
	out := &upstreamProviderSnapshot{
		Keys:            snapshot.Keys,
		RefreshedTokens: snapshot.RefreshedTokens,
	}
	if err == nil && includeProfile {
		out.ExtraUpdates = sub2APIProfileExtraUpdates(cfg, snapshot.Profile, snapshot.ProfileErr)
	}
	return out, err
}

func (sub2APIUpstreamProviderAdapter) SanitizeError(err error, credentials map[string]any) string {
	return sanitizeStandaloneSub2APIError(err, credentials)
}

func HashUpstreamKey(key string) string {
	sum := md5.Sum([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

func upstreamFirstNonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func mergePreservingUpstreamSecrets(current, incoming map[string]any) map[string]any {
	out := make(map[string]any)
	for k, v := range current {
		out[k] = v
	}
	for k, v := range incoming {
		if strings.TrimSpace(upstreamString(v)) == "" || strings.Contains(upstreamString(v), "***") {
			continue
		}
		out[k] = v
	}
	return out
}

func stringCredential(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	return upstreamString(credentials[key])
}

func upstreamString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
