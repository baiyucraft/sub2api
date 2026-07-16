package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	UpstreamProviderSub2API = AccountUpstreamProviderSub2API
	UpstreamProviderNewAPI  = AccountUpstreamProviderNewAPI
	UpstreamProviderOther   = AccountUpstreamProviderOther

	UpstreamAuthModeUserLogin   = AccountSub2APIRateSyncAdapterUserLogin
	UpstreamAuthModeManualJWT   = AccountSub2APIRateSyncAdapterManualJWT
	UpstreamAuthModeCookie      = "cookie"
	UpstreamAuthModeAccessToken = "access_token"
)

var (
	ErrUpstreamConfigNotFound = infraerrors.NotFound("UPSTREAM_CONFIG_NOT_FOUND", "upstream config not found")
	ErrUpstreamKeyNotFound    = infraerrors.NotFound("UPSTREAM_KEY_NOT_FOUND", "upstream key not found")
)

const (
	UpstreamKeyPlatformSourceLegacy     = "legacy"
	UpstreamKeyPlatformSourceAuto       = "auto"
	UpstreamKeyPlatformSourceManual     = "manual"
	UpstreamKeyPlatformSourceUnassigned = "unassigned"

	UpstreamKeyPlatformDetectionLegacy     = "legacy"
	UpstreamKeyPlatformDetectionDetected   = "detected"
	UpstreamKeyPlatformDetectionUnresolved = "unresolved"
	UpstreamKeyPlatformDetectionAmbiguous  = "ambiguous"
	UpstreamKeyPlatformDetectionConflict   = "conflict"
)

type UpstreamConfig struct {
	ID                      int64
	Name                    string
	Provider                string
	SiteURL                 string
	APIURL                  *string
	ClearAPIURL             bool
	Sub2APINotInCNConfirmed bool
	AuthMode                string
	Credentials             map[string]any
	Extra                   map[string]any
	ProxyID                 *int64
	ClearProxy              bool
	RechargeRate            float64
	BalanceToCNYRate        *float64
	ClearBalanceToCNYRate   bool
	Status                  string
	LastError               *string
	LastCheckedAt           *time.Time
	LastSuccessAt           *time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time

	Keys []*UpstreamKey
}

func (c *UpstreamConfig) EffectiveAPIURL() string {
	if c == nil {
		return ""
	}
	if c.APIURL != nil && strings.TrimSpace(*c.APIURL) != "" {
		return strings.TrimSpace(*c.APIURL)
	}
	return strings.TrimSpace(c.SiteURL)
}

type UpstreamKey struct {
	ID                      int64
	UpstreamConfigID        int64
	Name                    string
	Key                     string
	KeyHash                 string
	RemoteKeyID             *int64
	UpstreamGroupID         *int64
	UpstreamGroupName       string
	Platform                *string
	PlatformSource          string
	DetectedPlatform        *string
	PlatformDetectionStatus string
	PlatformDetectedAt      *time.Time
	BoundAccountCount       int
	RateMultiplier          *float64
	SourceRateMultiplier    *float64
	Status                  string
	LastSeenAt              *time.Time
	MissingCount            int
	MissingSince            *time.Time
	Extra                   map[string]any
	CreatedAt               time.Time
	UpdatedAt               time.Time
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
	UpsertKey(ctx context.Context, key *UpstreamKey) error
	UpdateKey(ctx context.Context, key *UpstreamKey) error
	DeleteKey(ctx context.Context, id int64) error
	UpdateKeyPlatform(ctx context.Context, upstreamConfigID, keyID int64, platform string, expectedUpdatedAt time.Time, disableBoundAccounts bool) (*UpstreamKey, error)
	RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error
	SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

const MaxUpstreamActualRate = 999999.9999

// NormalizeUpstreamActualRate is the single authority for persisted upstream cost rates.
func NormalizeUpstreamActualRate(sourceRate, rechargeRate float64) (float64, error) {
	if sourceRate < 0 || math.IsNaN(sourceRate) || math.IsInf(sourceRate, 0) {
		return 0, infraerrors.BadRequest("UPSTREAM_KEY_RATE_INVALID", "upstream source rate multiplier is invalid")
	}
	if rechargeRate == 0 {
		rechargeRate = 1
	}
	if rechargeRate < 0 || rechargeRate > 100 || math.IsNaN(rechargeRate) || math.IsInf(rechargeRate, 0) {
		return 0, infraerrors.BadRequest("UPSTREAM_RECHARGE_RATE_INVALID", "recharge_rate must be greater than 0 and at most 100")
	}
	actual := decimal.NewFromFloat(sourceRate).
		Mul(decimal.NewFromFloat(rechargeRate)).
		Round(4).
		InexactFloat64()
	if actual < 0 || actual > MaxUpstreamActualRate || math.IsNaN(actual) || math.IsInf(actual, 0) {
		return 0, infraerrors.BadRequest("UPSTREAM_ACTUAL_RATE_INVALID", "upstream actual rate multiplier is out of range")
	}
	return actual, nil
}

type UpdateUpstreamKeyPlatformRequest struct {
	Platform             string
	ExpectedUpdatedAt    time.Time
	DisableBoundAccounts bool
}

type UpstreamConfigService struct {
	repo        UpstreamConfigRepository
	proxyRepo   ProxyRepository
	accountRepo AccountRepository
	syncLocks   sync.Map
}

type UpstreamConfigSyncResult struct {
	RunID               int64    `json:"run_id,omitempty"`
	ConfigID            int64    `json:"config_id"`
	Name                string   `json:"name"`
	Provider            string   `json:"provider,omitempty"`
	Success             bool     `json:"success"`
	Status              string   `json:"status,omitempty"`
	Stage               string   `json:"stage,omitempty"`
	ErrorCode           string   `json:"error_code,omitempty"`
	Retryable           bool     `json:"retryable,omitempty"`
	KeyCount            int      `json:"key_count"`
	FallbackKeyCount    int      `json:"fallback_key_count,omitempty"`
	UnresolvedKeyCount  int      `json:"unresolved_key_count,omitempty"`
	UpdatedAccountCount int      `json:"updated_account_count"`
	MissingKeyCount     int      `json:"missing_key_count,omitempty"`
	StaleKeyCount       int      `json:"stale_key_count,omitempty"`
	DeletedKeyCount     int      `json:"deleted_key_count,omitempty"`
	RestoredKeyCount    int      `json:"restored_key_count,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
	DurationMS          int64    `json:"duration_ms,omitempty"`
	Error               string   `json:"error,omitempty"`
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
	ApplySyncSnapshot(ctx context.Context, configID, runID int64, keys []UpstreamKey, extraUpdates map[string]any, checkedAt time.Time, complete bool) ([]UpstreamKey, UpstreamKeyReconcileResult, int, error)
}

type upstreamMaskedKeyFallbackRepository interface {
	ListKeysForMaskedFallback(ctx context.Context, upstreamConfigID int64, remoteKeyIDs []int64) ([]UpstreamKey, error)
}

type UpstreamKeyReconcileResult struct {
	Missing  int
	Stale    int
	Deleted  int
	Restored int
}

const UpstreamKeyStatusStale = "stale"

type upstreamConfigSyncLockRepository interface {
	WithUpstreamConfigSyncLock(ctx context.Context, configID int64, fn func(context.Context) error) error
}

type upstreamAccountNameBackfillRepository interface {
	PreviewAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error)
	ApplyAccountNameBackfill(ctx context.Context) ([]UpstreamAccountNameBackfillItem, error)
}

type upstreamProviderSnapshot struct {
	Keys               []UpstreamKey
	KeysComplete       bool
	RefreshedTokens    *sub2APIRefreshData
	ExtraUpdates       map[string]any
	Partial            bool
	Warnings           []string
	FallbackKeyCount   int
	UnresolvedKeyCount int
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

func (s *UpstreamConfigService) readUpstreamSettings(ctx context.Context) (*UpstreamSettings, error) {
	reader, ok := s.repo.(UpstreamSettingsReader)
	if !ok {
		return nil, infraerrors.ServiceUnavailable("UPSTREAM_SETTINGS_UNAVAILABLE", "upstream compliance settings are unavailable")
	}
	settings, err := reader.GetUpstreamSettings(ctx)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("UPSTREAM_SETTINGS_UNAVAILABLE", "failed to read upstream compliance settings")
	}
	if settings == nil {
		settings = &UpstreamSettings{}
	}
	return settings, nil
}

func (s *UpstreamConfigService) applySub2APIComplianceSettings(ctx context.Context, cfg *UpstreamConfig, settings *UpstreamSettings) error {
	if cfg == nil || cfg.Provider != UpstreamProviderSub2API || cfg.AuthMode != UpstreamAuthModeUserLogin {
		return nil
	}
	if settings == nil {
		var err error
		settings, err = s.readUpstreamSettings(ctx)
		if err != nil {
			return err
		}
	}
	cfg.Sub2APINotInCNConfirmed = settings.Sub2APINotInCNConfirmed
	return nil
}

func (s *UpstreamConfigService) List(ctx context.Context, params pagination.PaginationParams, provider, status, search string) ([]UpstreamConfig, *pagination.PaginationResult, error) {
	return s.repo.List(ctx, params, provider, status, search)
}

func (s *UpstreamConfigService) GetByID(ctx context.Context, id int64) (*UpstreamConfig, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *UpstreamConfigService) Create(ctx context.Context, config *UpstreamConfig) (*UpstreamConfig, error) {
	if config != nil {
		config.Provider = normalizeUpstreamProvider(config.Provider)
		config.AuthMode = normalizeUpstreamAuthMode(config.AuthMode)
		pruneUpstreamProviderCredentials(config.Credentials, config.Provider, config.AuthMode)
	}
	if err := normalizeAndValidateUpstreamConfig(config, true); err != nil {
		return nil, err
	}
	if err := s.repo.Create(ctx, config); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, config.ID)
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
	current.SiteURL = upstreamFirstNonEmpty(patch.SiteURL, current.SiteURL)
	if patch.ClearAPIURL {
		current.APIURL = nil
	} else if patch.APIURL != nil {
		current.APIURL = patch.APIURL
	}
	current.AuthMode = normalizeUpstreamAuthMode(upstreamFirstNonEmpty(patch.AuthMode, current.AuthMode))
	if patch.ClearProxy {
		current.ProxyID = nil
	} else if patch.ProxyID != nil {
		if *patch.ProxyID == 0 {
			current.ProxyID = nil
		} else {
			current.ProxyID = patch.ProxyID
		}
	}
	if patch.Status != "" {
		current.Status = patch.Status
	}
	if patch.RechargeRate > 0 {
		current.RechargeRate = patch.RechargeRate
	}
	if patch.ClearBalanceToCNYRate {
		current.BalanceToCNYRate = nil
	} else if patch.BalanceToCNYRate != nil {
		if *patch.BalanceToCNYRate == 0 {
			current.BalanceToCNYRate = nil
		} else {
			current.BalanceToCNYRate = patch.BalanceToCNYRate
		}
	}
	current.Credentials = mergePreservingUpstreamSecrets(current.Credentials, patch.Credentials)
	pruneUpstreamProviderCredentials(current.Credentials, current.Provider, current.AuthMode)
	if patch.Extra != nil {
		current.Extra = patch.Extra
	}
	if err := normalizeAndValidateUpstreamConfig(current, false); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, current); err != nil {
		return nil, err
	}
	return s.GetByID(ctx, id)
}

// pruneUpstreamProviderCredentials removes credentials that no longer belong to
// the selected provider or authentication mode. This prevents stale secrets
// from remaining encrypted in a config after a provider switch.
func pruneUpstreamProviderCredentials(credentials map[string]any, provider, authMode string) {
	if credentials == nil {
		return
	}
	if provider != UpstreamProviderSub2API {
		for _, key := range []string{
			AccountCredentialSub2APILoginEmail,
			AccountCredentialSub2APILoginPassword,
			AccountCredentialSub2APIAccessToken,
			AccountCredentialSub2APIRefreshToken,
			AccountCredentialSub2APITokenExpiresAt,
		} {
			delete(credentials, key)
		}
	}
	if provider != UpstreamProviderNewAPI {
		for _, key := range []string{
			AccountCredentialNewAPILoginUsername,
			AccountCredentialNewAPILoginPassword,
			AccountCredentialNewAPICookie,
			AccountCredentialNewAPIAccessToken,
			AccountCredentialNewAPIUserID,
		} {
			delete(credentials, key)
		}
	} else {
		pruneNewAPIAuthenticationCredentials(credentials, authMode)
	}
	if provider == UpstreamProviderSub2API {
		pruneSub2APIAuthenticationCredentials(credentials, authMode)
	}
}

func pruneSub2APIAuthenticationCredentials(credentials map[string]any, authMode string) {
	if credentials == nil {
		return
	}
	if authMode == UpstreamAuthModeManualJWT {
		delete(credentials, AccountCredentialSub2APILoginEmail)
		delete(credentials, AccountCredentialSub2APILoginPassword)
		return
	}
	delete(credentials, AccountCredentialSub2APIAccessToken)
	delete(credentials, AccountCredentialSub2APIRefreshToken)
	delete(credentials, AccountCredentialSub2APITokenExpiresAt)
}

func pruneNewAPIAuthenticationCredentials(credentials map[string]any, authMode string) {
	if credentials == nil {
		return
	}
	switch authMode {
	case UpstreamAuthModeCookie:
		delete(credentials, AccountCredentialNewAPILoginUsername)
		delete(credentials, AccountCredentialNewAPIAccessToken)
		delete(credentials, AccountCredentialNewAPILoginPassword)
	case UpstreamAuthModeAccessToken:
		delete(credentials, AccountCredentialNewAPILoginUsername)
		delete(credentials, AccountCredentialNewAPICookie)
		delete(credentials, AccountCredentialNewAPILoginPassword)
	default:
		delete(credentials, AccountCredentialNewAPICookie)
		delete(credentials, AccountCredentialNewAPIAccessToken)
		delete(credentials, AccountCredentialNewAPIUserID)
	}
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
	if _, err := s.repo.GetByID(ctx, upstreamConfigID); err != nil {
		return nil, err
	}
	keys, err := s.repo.ListKeys(ctx, upstreamConfigID)
	if err != nil {
		return nil, err
	}
	return keys, nil
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

func (s *UpstreamConfigService) DeleteKey(ctx context.Context, id int64) error {
	return s.repo.DeleteKey(ctx, id)
}

func (s *UpstreamConfigService) UpdateKeyPlatform(ctx context.Context, upstreamConfigID, keyID int64, req UpdateUpstreamKeyPlatformRequest) (*UpstreamKey, error) {
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if !isAssignableUpstreamKeyPlatform(platform) {
		return nil, infraerrors.BadRequest("UPSTREAM_KEY_PLATFORM_INVALID", "upstream key platform is invalid")
	}
	if req.ExpectedUpdatedAt.IsZero() {
		return nil, infraerrors.BadRequest("UPSTREAM_KEY_EXPECTED_UPDATED_AT_REQUIRED", "expected_updated_at is required")
	}
	return s.repo.UpdateKeyPlatform(ctx, upstreamConfigID, keyID, platform, req.ExpectedUpdatedAt.UTC(), req.DisableBoundAccounts)
}

func (s *UpstreamConfigService) Test(ctx context.Context, id int64) error {
	cfg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if cfg == nil {
		return ErrUpstreamConfigNotFound
	}
	if err := s.applySub2APIComplianceSettings(ctx, cfg, nil); err != nil {
		return err
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
	settings, err := s.readUpstreamSettings(ctx)
	if err != nil && cfg.Provider == UpstreamProviderSub2API && cfg.AuthMode == UpstreamAuthModeUserLogin {
		return nil, UpstreamConfigSyncResult{}, err
	}
	runID, err := s.beginSyncRun(ctx, UpstreamSyncTriggerManualSingle, 1)
	if err != nil {
		return nil, UpstreamConfigSyncResult{}, err
	}
	startedAt := time.Now().UTC()
	keys, result, syncErr := s.syncProviderConfig(ctx, cfg, runID, settings)
	if auditErr := s.persistSyncResult(ctx, startedAt, result); auditErr != nil {
		result.Success = false
		result.Status = UpstreamSyncStatusFailed
		result.Stage = "persist"
		result.ErrorCode = "database"
		result.Retryable = true
		result.Error = logredact.RedactText(auditErr.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")
		if syncErr == nil {
			syncErr = auditErr
		}
	}
	if auditErr := s.finishSyncRun(ctx, runID, []UpstreamConfigSyncResult{result}); auditErr != nil && syncErr == nil {
		syncErr = auditErr
	}
	return keys, result, syncErr
}

func (s *UpstreamConfigService) SyncActiveSub2APIConfigs(ctx context.Context) []UpstreamConfigSyncResult {
	results, err := s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API}, UpstreamSyncTriggerScheduled)
	return scheduledSyncResults(results, err)
}

func (s *UpstreamConfigService) SyncActiveUpstreamConfigs(ctx context.Context) []UpstreamConfigSyncResult {
	results, err := s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API, UpstreamProviderNewAPI}, UpstreamSyncTriggerScheduled)
	return scheduledSyncResults(results, err)
}

func (s *UpstreamConfigService) SyncActiveUpstreamConfigsManual(ctx context.Context) (int64, []UpstreamConfigSyncResult, error) {
	results, err := s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API, UpstreamProviderNewAPI}, UpstreamSyncTriggerManualBatch)
	var runID int64
	if len(results) > 0 {
		runID = results[0].RunID
	}
	return runID, results, err
}

func (s *UpstreamConfigService) syncActiveUpstreamConfigs(ctx context.Context, providers []string, trigger string) ([]UpstreamConfigSyncResult, error) {
	configs, listErr := s.listActiveUpstreamConfigs(ctx, providers)
	if listErr != nil {
		return nil, listErr
	}
	runID, err := s.beginSyncRun(ctx, trigger, len(configs))
	if err != nil {
		return nil, err
	}
	settings, settingsErr := s.readUpstreamSettings(ctx)
	results := make([]UpstreamConfigSyncResult, 0, len(configs))
	for i := range configs {
		cfg := configs[i]
		startedAt := time.Now().UTC()
		var err error
		var result UpstreamConfigSyncResult
		if settingsErr != nil && cfg.Provider == UpstreamProviderSub2API && cfg.AuthMode == UpstreamAuthModeUserLogin {
			err = settingsErr
			result = UpstreamConfigSyncResult{RunID: runID, ConfigID: cfg.ID, Name: cfg.Name, Provider: cfg.Provider, Status: UpstreamSyncStatusFailed}
		} else {
			_, result, err = s.syncProviderConfig(ctx, &cfg, runID, settings)
		}
		if err != nil {
			result.Success = false
			if adapter, ok := upstreamProviderAdapterFor(cfg.Provider); ok {
				result.Error = adapter.SanitizeError(err, cfg.Credentials)
			} else {
				result.Error = logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")
			}
		}
		if err := s.persistSyncResult(ctx, startedAt, result); err != nil {
			result.Success = false
			result.Status = UpstreamSyncStatusFailed
			result.Stage = "persist"
			result.ErrorCode = "database"
			result.Retryable = true
			result.Error = logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")
			results = append(results, result)
			_ = s.finishSyncRun(ctx, runID, results)
			return results, err
		}
		results = append(results, result)
	}
	if err := s.finishSyncRun(ctx, runID, results); err != nil {
		return results, err
	}
	return results, nil
}

func scheduledSyncResults(results []UpstreamConfigSyncResult, err error) []UpstreamConfigSyncResult {
	if err == nil {
		return results
	}
	return append(results, UpstreamConfigSyncResult{Status: UpstreamSyncStatusFailed, Error: logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")})
}

func (s *UpstreamConfigService) syncProviderConfig(ctx context.Context, cfg *UpstreamConfig, runID int64, settings *UpstreamSettings) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
	if cfg != nil && cfg.ID > 0 {
		unlock := s.lockUpstreamConfigSync(cfg.ID)
		defer unlock()
		if locker, ok := s.repo.(upstreamConfigSyncLockRepository); ok {
			var keys []UpstreamKey
			var result UpstreamConfigSyncResult
			var syncErr error
			lockErr := locker.WithUpstreamConfigSyncLock(ctx, cfg.ID, func(lockCtx context.Context) error {
				keys, result, syncErr = s.syncProviderConfigLocked(lockCtx, cfg, runID, settings)
				return nil
			})
			if lockErr != nil {
				result = UpstreamConfigSyncResult{RunID: runID, ConfigID: cfg.ID, Name: cfg.Name, Provider: cfg.Provider, Status: UpstreamSyncStatusFailed}
				result.Error = logredact.RedactText(lockErr.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")
				result.Stage, result.ErrorCode, result.Retryable = classifyUpstreamSyncFailure(lockErr, "auth")
				return nil, result, lockErr
			}
			return keys, result, syncErr
		}
	}
	return s.syncProviderConfigLocked(ctx, cfg, runID, settings)
}

func (s *UpstreamConfigService) syncProviderConfigLocked(ctx context.Context, cfg *UpstreamConfig, runID int64, settings *UpstreamSettings) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
	if cfg != nil && cfg.ID > 0 {
		latest, err := s.repo.GetByID(ctx, cfg.ID)
		if err != nil {
			result := UpstreamConfigSyncResult{RunID: runID, ConfigID: cfg.ID, Name: cfg.Name, Provider: cfg.Provider, Status: UpstreamSyncStatusFailed, Error: logredact.RedactText(err.Error(), "password", "api_key", "jwt", "authorization", "refresh_token", "access_token")}
			return nil, result, err
		}
		cfg = latest
	}
	if err := s.applySub2APIComplianceSettings(ctx, cfg, settings); err != nil {
		result := UpstreamConfigSyncResult{RunID: runID, Status: UpstreamSyncStatusFailed}
		if cfg != nil {
			result.ConfigID, result.Name, result.Provider = cfg.ID, cfg.Name, cfg.Provider
		}
		result.Error = logredact.RedactText(err.Error())
		return nil, result, err
	}
	result := UpstreamConfigSyncResult{RunID: runID, Status: UpstreamSyncStatusFailed}
	if cfg != nil {
		result.ConfigID = cfg.ID
		result.Name = cfg.Name
		result.Provider = cfg.Provider
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
		result.Stage, result.ErrorCode, result.Retryable = classifyUpstreamSyncFailure(err, "proxy")
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
		result.Stage, result.ErrorCode, result.Retryable = classifyUpstreamSyncFailure(err, "auth")
		return nil, result, upstreamProviderSyncError(cfg.Provider, safeErr)
	}
	if snapshot == nil {
		err := fmt.Errorf("upstream provider returned no snapshot")
		result.Error = adapter.SanitizeError(err, cfg.Credentials)
		return nil, result, err
	}
	s.resolveMaskedSnapshotKeys(ctx, cfg, snapshot)
	if cfg.Provider == UpstreamProviderSub2API {
		applySub2APIBillingRates(ctx, cfg, proxyURL, snapshot)
		if err := s.preserveMissingSub2APIRates(ctx, cfg, snapshot); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
	}
	keys := snapshot.Keys
	snapshot.ExtraUpdates = normalizeProviderBalanceExtra(cfg, snapshot.ExtraUpdates)
	result.Warnings = append(result.Warnings, snapshot.Warnings...)
	result.FallbackKeyCount = snapshot.FallbackKeyCount
	result.UnresolvedKeyCount = snapshot.UnresolvedKeyCount
	for i := range keys {
		if keys[i].SourceRateMultiplier != nil {
			actualRate, rateErr := NormalizeUpstreamActualRate(*keys[i].SourceRateMultiplier, cfg.RechargeRate)
			if rateErr != nil {
				result.Error = adapter.SanitizeError(rateErr, cfg.Credentials)
				return nil, result, rateErr
			}
			keys[i].RateMultiplier = &actualRate
		} else {
			keys[i].RateMultiplier = nil
		}
		if err := normalizeAndValidateUpstreamKey(&keys[i]); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
	}
	result.KeyCount = len(keys)
	if atomicRepo, ok := s.repo.(upstreamConfigAtomicSyncRepository); ok {
		complete := snapshot.KeysComplete && snapshot.UnresolvedKeyCount == 0
		localKeys, reconciled, updated, applyErr := atomicRepo.ApplySyncSnapshot(ctx, cfg.ID, runID, keys, snapshot.ExtraUpdates, time.Now().UTC(), complete)
		if applyErr != nil {
			result.Error = adapter.SanitizeError(applyErr, cfg.Credentials)
			result.Stage, result.ErrorCode, result.Retryable = classifyUpstreamSyncFailure(applyErr, "persist")
			_ = s.repo.RecordCheckResult(ctx, cfg.ID, false, result.Error)
			return nil, result, applyErr
		}
		result.UpdatedAccountCount = updated
		result.MissingKeyCount = reconciled.Missing
		result.StaleKeyCount = reconciled.Stale
		result.DeletedKeyCount = reconciled.Deleted
		result.RestoredKeyCount = reconciled.Restored
		result.Success = true
		result.Status = UpstreamSyncStatusSucceeded
		if snapshot.Partial || len(snapshot.Warnings) > 0 || snapshot.UnresolvedKeyCount > 0 {
			result.Status = UpstreamSyncStatusPartial
		}
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
	result.Status = UpstreamSyncStatusSucceeded
	if snapshot.Partial || len(snapshot.Warnings) > 0 || snapshot.UnresolvedKeyCount > 0 {
		result.Status = UpstreamSyncStatusPartial
	}
	_ = s.repo.RecordCheckResult(ctx, cfg.ID, true, "")
	return localKeys, result, nil
}

func applySub2APIBillingRates(ctx context.Context, cfg *UpstreamConfig, proxyURL string, snapshot *upstreamProviderSnapshot) {
	if cfg == nil || snapshot == nil || len(snapshot.Keys) == 0 {
		return
	}
	billingTarget := sub2APISyncTarget{billingRootURL: cfg.EffectiveAPIURL(), proxyURL: proxyURL}
	billingRates, billingWarnings := (&Sub2APIUpstreamRateSyncService{}).fetchSub2APIKeyBillingRates(ctx, billingTarget, snapshot.Keys)
	for i := range snapshot.Keys {
		if snapshot.Keys[i].RemoteKeyID == nil {
			continue
		}
		if rate, ok := billingRates[*snapshot.Keys[i].RemoteKeyID]; ok {
			snapshot.Keys[i].SourceRateMultiplier = &rate
		}
	}
	snapshot.Warnings = append(snapshot.Warnings, billingWarnings...)
}

func (s *UpstreamConfigService) preserveMissingSub2APIRates(ctx context.Context, cfg *UpstreamConfig, snapshot *upstreamProviderSnapshot) error {
	missingRemoteIDs := make([]int64, 0)
	for i := range snapshot.Keys {
		if snapshot.Keys[i].SourceRateMultiplier == nil && snapshot.Keys[i].RemoteKeyID != nil {
			missingRemoteIDs = append(missingRemoteIDs, *snapshot.Keys[i].RemoteKeyID)
		}
	}
	if len(missingRemoteIDs) == 0 {
		return nil
	}
	var (
		existing []UpstreamKey
		err      error
	)
	if fallbackRepo, ok := s.repo.(upstreamMaskedKeyFallbackRepository); ok {
		existing, err = fallbackRepo.ListKeysForMaskedFallback(ctx, cfg.ID, missingRemoteIDs)
	} else {
		existing, err = s.repo.ListKeys(ctx, cfg.ID)
	}
	if err != nil {
		return fmt.Errorf("load previous sub2api source rates: %w", err)
	}
	byRemoteID := make(map[int64]float64, len(existing))
	for i := range existing {
		key := existing[i]
		if key.RemoteKeyID != nil && key.SourceRateMultiplier != nil {
			byRemoteID[*key.RemoteKeyID] = *key.SourceRateMultiplier
		}
	}
	for i := range snapshot.Keys {
		key := &snapshot.Keys[i]
		if key.SourceRateMultiplier != nil || key.RemoteKeyID == nil {
			continue
		}
		if rate, ok := byRemoteID[*key.RemoteKeyID]; ok {
			key.SourceRateMultiplier = &rate
			snapshot.Partial = true
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("key %d: retained previous source rate", *key.RemoteKeyID))
			continue
		}
		return fmt.Errorf("api key %d has no valid rate multiplier", *key.RemoteKeyID)
	}
	return nil
}

func (s *UpstreamConfigService) resolveMaskedSnapshotKeys(ctx context.Context, cfg *UpstreamConfig, snapshot *upstreamProviderSnapshot) {
	if s == nil || cfg == nil || snapshot == nil || len(snapshot.Keys) == 0 {
		return
	}
	remoteKeyIDs := make([]int64, 0, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		if (strings.TrimSpace(key.Key) == "" || isMaskedUpstreamKey(key.Key)) && key.RemoteKeyID != nil {
			remoteKeyIDs = append(remoteKeyIDs, *key.RemoteKeyID)
		}
	}
	var (
		existing []UpstreamKey
		err      error
	)
	if fallbackRepo, ok := s.repo.(upstreamMaskedKeyFallbackRepository); ok {
		existing, err = fallbackRepo.ListKeysForMaskedFallback(ctx, cfg.ID, remoteKeyIDs)
	} else {
		existing, err = s.repo.ListKeys(ctx, cfg.ID)
	}
	if err != nil {
		snapshot.Partial = true
		snapshot.KeysComplete = false
		snapshot.Warnings = append(snapshot.Warnings, "failed to load local keys for masked-key fallback")
		return
	}
	byRemoteID := make(map[int64]UpstreamKey, len(existing))
	for _, key := range existing {
		if key.RemoteKeyID != nil && strings.TrimSpace(key.Key) != "" {
			byRemoteID[*key.RemoteKeyID] = key
		}
	}
	resolved := make([]UpstreamKey, 0, len(snapshot.Keys))
	for _, key := range snapshot.Keys {
		if strings.TrimSpace(key.Key) != "" && !isMaskedUpstreamKey(key.Key) {
			resolved = append(resolved, key)
			continue
		}
		if key.RemoteKeyID != nil {
			if old, ok := byRemoteID[*key.RemoteKeyID]; ok {
				key.Key = old.Key
				key.KeyHash = old.KeyHash
				resolved = append(resolved, key)
				snapshot.FallbackKeyCount++
				snapshot.Partial = true
				continue
			}
		}
		snapshot.UnresolvedKeyCount++
		snapshot.Partial = true
		snapshot.KeysComplete = false
	}
	if snapshot.UnresolvedKeyCount > 0 {
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("%d masked upstream keys could not be resolved", snapshot.UnresolvedKeyCount))
	}
	snapshot.Keys = resolved
}

func sub2APIProfileExtraUpdates(cfg *UpstreamConfig, profile *sub2APIProfile, profileErr error) (map[string]any, string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if profile == nil && profileErr == nil {
		profileErr = fmt.Errorf("sub2api profile returned null data")
	}
	if profileErr != nil {
		updates := map[string]any{
			"sub2api_balance_last_error":    sanitizeStandaloneSub2APIError(profileErr, cfg.Credentials),
			"sub2api_balance_last_error_at": now,
		}
		concurrencyUpdates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderSub2API, nil, profileErr)
		for key, value := range concurrencyUpdates {
			updates[key] = value
		}
		return updates, warning
	}
	updates := normalizeProviderBalanceExtra(cfg, map[string]any{
		"sub2api_balance":               profile.Balance,
		"sub2api_total_recharged":       profile.TotalRecharged,
		"sub2api_user_email":            strings.TrimSpace(profile.Email),
		"sub2api_user_id":               profile.ID,
		"sub2api_balance_synced_at":     now,
		"sub2api_balance_last_error":    "",
		"sub2api_balance_last_error_at": "",
	})
	concurrencyUpdates, warning := upstreamConcurrencySnapshotUpdates(cfg, UpstreamProviderSub2API, profile.Concurrency, nil)
	for key, value := range concurrencyUpdates {
		updates[key] = value
	}
	return updates, warning
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
	config.SiteURL = strings.TrimSpace(config.SiteURL)
	if config.SiteURL == "" {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_SITE_URL_REQUIRED", "upstream config site url is required")
	}
	var err error
	config.SiteURL, err = normalizeUpstreamConfigURL(config.SiteURL)
	if err != nil {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_SITE_URL_INVALID", "upstream config site url is invalid")
	}
	if config.APIURL != nil {
		trimmed := strings.TrimSpace(*config.APIURL)
		if trimmed == "" {
			config.APIURL = nil
		} else {
			normalized, normalizeErr := normalizeUpstreamConfigURL(trimmed)
			if normalizeErr != nil {
				return infraerrors.BadRequest("UPSTREAM_CONFIG_API_URL_INVALID", "upstream config api url is invalid")
			}
			if normalized == config.SiteURL {
				config.APIURL = nil
			} else {
				config.APIURL = &normalized
			}
		}
	}
	if config.RechargeRate == 0 {
		config.RechargeRate = 1
	}
	if config.Status == "" {
		config.Status = StatusActive
	}
	if config.Name == "" {
		return infraerrors.BadRequest("UPSTREAM_CONFIG_NAME_REQUIRED", "upstream config name is required")
	}
	if config.RechargeRate <= 0 || config.RechargeRate > 100 || math.IsNaN(config.RechargeRate) || math.IsInf(config.RechargeRate, 0) {
		return infraerrors.BadRequest("UPSTREAM_RECHARGE_RATE_INVALID", "recharge_rate must be greater than 0 and at most 100")
	}
	if config.BalanceToCNYRate != nil && (*config.BalanceToCNYRate <= 0 || math.IsNaN(*config.BalanceToCNYRate) || math.IsInf(*config.BalanceToCNYRate, 0)) {
		return infraerrors.BadRequest("UPSTREAM_CNY_RATE_INVALID", "balance_to_cny_rate must be a positive finite number")
	}
	if config.Credentials == nil {
		config.Credentials = map[string]any{}
	}
	if config.Extra == nil {
		config.Extra = map[string]any{}
	}
	if err := validateUpstreamProviderAuthMode(config.Provider, config.AuthMode); err != nil {
		return err
	}
	if adapter, ok := upstreamProviderAdapterFor(config.Provider); ok {
		return adapter.ValidateConfig(config, requireSecrets)
	}
	if config.Provider != UpstreamProviderSub2API && config.Provider != UpstreamProviderNewAPI {
		return nil
	}
	return nil
}

func validateUpstreamProviderAuthMode(provider, authMode string) error {
	valid := false
	switch provider {
	case UpstreamProviderSub2API:
		valid = authMode == UpstreamAuthModeUserLogin || authMode == UpstreamAuthModeManualJWT
	case UpstreamProviderNewAPI:
		valid = authMode == UpstreamAuthModeUserLogin || authMode == UpstreamAuthModeCookie || authMode == UpstreamAuthModeAccessToken
	default:
		return nil
	}
	if !valid {
		return infraerrors.BadRequest("UPSTREAM_AUTH_MODE_INVALID", "upstream auth mode is not supported by the selected provider")
	}
	return nil
}

func normalizeUpstreamConfigURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", fmt.Errorf("invalid upstream url")
	}
	return trimmed, nil
}

func normalizeAndValidateUpstreamKey(key *UpstreamKey) error {
	if key == nil {
		return infraerrors.BadRequest("UPSTREAM_KEY_REQUIRED", "upstream key is required")
	}
	key.Name = strings.TrimSpace(key.Name)
	key.Key = strings.TrimSpace(key.Key)
	if key.Platform != nil {
		platform := strings.ToLower(strings.TrimSpace(*key.Platform))
		if platform == "" {
			key.Platform = nil
		} else {
			if !isAssignableUpstreamKeyPlatform(platform) {
				return infraerrors.BadRequest("UPSTREAM_KEY_PLATFORM_INVALID", "upstream key platform is invalid")
			}
			key.Platform = &platform
		}
	}
	if key.PlatformSource == "" {
		if key.Platform == nil {
			key.PlatformSource = UpstreamKeyPlatformSourceUnassigned
		} else {
			key.PlatformSource = UpstreamKeyPlatformSourceLegacy
		}
	}
	if key.PlatformDetectionStatus == "" {
		key.PlatformDetectionStatus = UpstreamKeyPlatformDetectionLegacy
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
	if key.SourceRateMultiplier != nil && (*key.SourceRateMultiplier < 0 || math.IsNaN(*key.SourceRateMultiplier) || math.IsInf(*key.SourceRateMultiplier, 0)) {
		return infraerrors.BadRequest("UPSTREAM_KEY_RATE_INVALID", "upstream source rate multiplier is invalid")
	}
	return nil
}

func isAssignableUpstreamKeyPlatform(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case PlatformAnthropic, PlatformOpenAI, PlatformGemini, PlatformGrok:
		return true
	default:
		return false
	}
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
	case UpstreamAuthModeCookie:
		return UpstreamAuthModeCookie
	case UpstreamAuthModeAccessToken:
		return UpstreamAuthModeAccessToken
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
		KeysComplete:    snapshot.KeysComplete,
		RefreshedTokens: snapshot.RefreshedTokens,
		Warnings:        append([]string(nil), snapshot.Warnings...),
	}
	if err == nil && includeProfile {
		var warning string
		out.ExtraUpdates, warning = sub2APIProfileExtraUpdates(cfg, snapshot.Profile, snapshot.ProfileErr)
		if warning != "" {
			out.Warnings = append(out.Warnings, warning)
		}
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
