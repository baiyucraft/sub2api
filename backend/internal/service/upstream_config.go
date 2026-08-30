package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/sync/singleflight"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
)

const (
	UpstreamProviderSub2API = AccountUpstreamProviderSub2API
	UpstreamProviderNewAPI  = AccountUpstreamProviderNewAPI
	UpstreamProviderLCodex  = AccountUpstreamProviderLCodex
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

var errUpstreamHealthEvidencePersistFailed = infraerrors.InternalServer("UPSTREAM_HEALTH_EVIDENCE_PERSIST_FAILED", "failed to save upstream health evidence")

const (
	upstreamProbeTempUnschedReasonPrefix = "upstream_probe:"
	// Keep the durable account exclusion long enough for the slowest configured
	// probe interval and recovery window. Successful probes clear it earlier.
	upstreamProbeTempUnschedulableDuration = 24 * time.Hour
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
	ID                                int64
	Name                              string
	Provider                          string
	SiteURL                           string
	APIURL                            *string
	ClearAPIURL                       bool
	Sub2APINotInCNConfirmed           bool
	AuthMode                          string
	Credentials                       map[string]any
	Extra                             map[string]any
	SchedulerConcurrencyOverride      *int
	ClearSchedulerConcurrencyOverride bool
	ProxyID                           *int64
	ClearProxy                        bool
	RechargeRate                      float64
	BalanceToCNYRate                  *float64
	ClearBalanceToCNYRate             bool
	SchedulingEnabled                 *bool
	Status                            string
	LastError                         *string
	LastCheckedAt                     *time.Time
	LastSuccessAt                     *time.Time
	CreatedAt                         time.Time
	UpdatedAt                         time.Time
	// Balance fields are derived from the latest persisted channel snapshot.
	// They remain nullable when the provider did not return a usable balance.
	BalanceCNY               *float64 `json:"balance_cny,omitempty"`
	BalanceAvailable         bool     `json:"balance_available"`
	BalanceLow               bool     `json:"balance_low"`
	BalanceThresholdCNY      *float64 `json:"balance_threshold_cny,omitempty"`
	BalanceUnavailableReason string   `json:"balance_unavailable_reason,omitempty"`

	Keys        []*UpstreamKey
	AuthSession *UpstreamAuthSessionStatus
}

type UpstreamManagementSettings struct {
	TTFTGuard            OpenAITTFTGuardSettings         `json:"ttft_guard"`
	ProbeModels          UpstreamProbeModels             `json:"probe_models"`
	ProbeIntervalSeconds int                             `json:"probe_interval_seconds"`
	ProbeGuard           UpstreamProbeGuardSettings      `json:"probe_guard"`
	ModelAliasRules      map[string]string               `json:"model_alias_rules"`
	ConfidenceProbe      UpstreamConfidenceProbeSettings `json:"confidence_probe"`
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
	BaseURL                 *string
	Description             string
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
	// ObservationEnabled is the durable administrator preference for active
	// health probing. Provider sync must preserve this value.
	ObservationEnabled bool
	// ObservationEnabledKnown distinguishes repository-loaded keys from
	// provider snapshots and test fixtures that predate the dedicated column.
	ObservationEnabledKnown bool
	Extra                   map[string]any
	ImagePricing            *UpstreamKeyImagePricing
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type UpstreamConfigRepository interface {
	List(ctx context.Context, params pagination.PaginationParams, filter UpstreamConfigListFilter) ([]UpstreamConfig, *pagination.PaginationResult, error)
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
	UpdateKeyBaseURL(ctx context.Context, upstreamConfigID, keyID int64, baseURL *string, expectedUpdatedAt time.Time) (*UpstreamKey, error)
	RecordCheckResult(ctx context.Context, id int64, success bool, safeErr string) error
	SaveRefreshedTokens(ctx context.Context, id int64, accessToken, refreshToken string, expiresAt *time.Time) error
	UpdateExtra(ctx context.Context, id int64, updates map[string]any) error
}

type UpstreamConfigListFilter struct {
	Provider             string
	Status               string
	Search               string
	BalanceSortAvailable bool
}

// UpstreamConfigDeleteOptions controls destructive operations on an upstream
// configuration. The default path deliberately keeps the historical
// "reject when accounts are bound" behavior.
type UpstreamConfigDeleteOptions struct {
	DeleteSyncManagedAccounts bool
}

// UpstreamConfigDeleteResult is returned after a successful delete. Counts are
// zero for the compatibility (non-cascade) path.
type UpstreamConfigDeleteResult struct {
	DeletedAccountCount int `json:"deleted_account_count"`
	DeletedKeyCount     int `json:"deleted_key_count"`
}

// UpstreamConfigBindingSummary is the redacted binding shape used by delete
// guards. It intentionally contains counts only; account/key credentials are
// never exposed to the handler.
type UpstreamConfigBindingSummary struct {
	BoundAccountCount       int
	ManualAccountCount      int
	MissingKeyAccountCount  int
	SyncManagedAccountCount int
}

// UpstreamConfigBindingSummaryRepository is optional for compatibility with
// lightweight repositories used by existing service tests.
type UpstreamConfigBindingSummaryRepository interface {
	GetBindingSummary(ctx context.Context, id int64) (UpstreamConfigBindingSummary, error)
}

// UpstreamConfigCascadeDeleteRepository is intentionally optional so existing
// lightweight repository test doubles do not need to grow a new method.
type UpstreamConfigCascadeDeleteRepository interface {
	DeleteWithOptions(ctx context.Context, id int64, options UpstreamConfigDeleteOptions) (UpstreamConfigDeleteResult, error)
}

const MaxUpstreamActualRate = 999999.9999

// NormalizeUpstreamActualRate is the single authority for persisted upstream cost rates.
//
// The returned value is the exact effective rate (source × recharge), rounded
// only to the ten decimal places supported by the persisted NUMERIC(20,10)
// columns.  It intentionally does not round up to two decimal places: values
// such as 0.045 must remain 0.045 for billing and scheduling.
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
		Round(10).
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

type UpdateUpstreamKeyBaseURLRequest struct {
	BaseURL           *string
	ClearBaseURL      bool
	ExpectedUpdatedAt time.Time
}

type UpstreamConfigService struct {
	repo               UpstreamConfigRepository
	proxyRepo          ProxyRepository
	accountRepo        AccountRepository
	accountTestService *AccountTestService
	accountProber      interface {
		RunUpstreamHealthProbe(ctx context.Context, account *Account, model string) (UpstreamHealthProbeResult, error)
	}
	openAIScheduleReporter     any
	settingService             *SettingService
	healthProbeIntervalSeconds atomic.Int64
	syncLocks                  sync.Map
	healthLocks                sync.Map
	healthPersistedAt          sync.Map
	healthProbeSF              singleflight.Group
	authSessionManager         UpstreamAuthSessionManager
	upstreamBalanceNotifier    UpstreamBalanceNotifier
}

// UpstreamBalanceNotifier sends an administrator alert for a newly opened
// low-balance incident. The incident opening time is used as the delivery
// deduplication key so repeated syncs in one incident cycle do not resend.
type UpstreamBalanceNotifier interface {
	NotifyUpstreamBalanceLow(ctx context.Context, config *UpstreamConfig, balance, threshold float64, observedAt, incidentOpenedAt time.Time)
}

type UpstreamConfigSyncResult struct {
	RunID                   int64    `json:"run_id,omitempty"`
	ConfigID                int64    `json:"config_id"`
	Name                    string   `json:"name"`
	Provider                string   `json:"provider,omitempty"`
	Success                 bool     `json:"success"`
	Status                  string   `json:"status,omitempty"`
	Stage                   string   `json:"stage,omitempty"`
	ErrorCode               string   `json:"error_code,omitempty"`
	Retryable               bool     `json:"retryable,omitempty"`
	KeyCount                int      `json:"key_count"`
	FallbackKeyCount        int      `json:"fallback_key_count,omitempty"`
	UnresolvedKeyCount      int      `json:"unresolved_key_count,omitempty"`
	UpdatedAccountCount     int      `json:"updated_account_count"`
	MissingKeyCount         int      `json:"missing_key_count,omitempty"`
	StaleKeyCount           int      `json:"stale_key_count,omitempty"`
	DeletedKeyCount         int      `json:"deleted_key_count,omitempty"`
	RestoredKeyCount        int      `json:"restored_key_count,omitempty"`
	ArchivedAccountCount    int      `json:"archived_account_count,omitempty"`
	RestoredAccountCount    int      `json:"restored_account_count,omitempty"`
	ModelSyncAttemptedCount int      `json:"model_sync_attempted,omitempty"`
	ModelSyncSucceededCount int      `json:"model_sync_succeeded,omitempty"`
	ModelSyncFailedCount    int      `json:"model_sync_failed,omitempty"`
	ModelSyncSkippedCount   int      `json:"model_sync_skipped,omitempty"`
	Warnings                []string `json:"warnings,omitempty"`
	DurationMS              int64    `json:"duration_ms,omitempty"`
	Error                   string   `json:"error,omitempty"`
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

type upstreamAccountBindingLister interface {
	ListByUpstreamKeyID(ctx context.Context, keyID int64) ([]Account, error)
}

type upstreamProbeModelUsageReader interface {
	ListRecentUpstreamProbeModels(ctx context.Context, since time.Time, limit int) (map[string][]string, error)
}

type upstreamMaskedKeyFallbackRepository interface {
	ListKeysForMaskedFallback(ctx context.Context, upstreamConfigID int64, remoteKeyIDs []int64) ([]UpstreamKey, error)
}

type upstreamKeyHealthRepository interface {
	PatchKeyHealth(ctx context.Context, keyID int64, health map[string]any) error
	ListAllKeysForHealth(ctx context.Context) ([]UpstreamKey, error)
}

type upstreamKeyHealthEventRepository interface {
	PatchKeyHealthWithEvent(ctx context.Context, keyID int64, health map[string]any, event *UpstreamEvent) error
}

type upstreamKeyHealthObservationRepository interface {
	PatchKeyHealthWithObservation(ctx context.Context, keyID int64, health map[string]any, event *UpstreamEvent, observation *UpstreamHealthObservation) error
}

type upstreamKeyHealthHistoryRepository interface {
	ListKeyHealthHistories(ctx context.Context, keyIDs []int64, limit int) (map[int64][]UpstreamHealthObservation, error)
}

type upstreamHealthProbeLockRepository interface {
	WithUpstreamHealthProbeLock(ctx context.Context, keyID int64, fn func(context.Context) error) (bool, error)
}

type UpstreamKeyReconcileResult struct {
	Missing              int
	Stale                int
	Deleted              int
	Restored             int
	ArchivedAccountCount int
	RestoredAccountCount int
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
	DiscoveredAPIURL   *string
}

type upstreamProviderAdapter interface {
	Provider() string
	ValidateConfig(config *UpstreamConfig, requireSecrets bool) error
	Test(ctx context.Context, cfg *UpstreamConfig, proxyURL string) error
	SyncSnapshot(ctx context.Context, cfg *UpstreamConfig, proxyURL string, includeProfile bool) (*upstreamProviderSnapshot, error)
	SanitizeError(err error, credentials map[string]any) string
}

func NewUpstreamConfigService(repo UpstreamConfigRepository, proxyRepo ProxyRepository, accountRepo AccountRepository) *UpstreamConfigService {
	svc := &UpstreamConfigService{repo: repo, proxyRepo: proxyRepo, accountRepo: accountRepo}
	svc.healthProbeIntervalSeconds.Store(DefaultUpstreamProbeIntervalSeconds)
	if healthRepo, ok := repo.(upstreamKeyHealthRepository); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		keys, err := healthRepo.ListAllKeysForHealth(ctx)
		cancel()
		if err != nil {
			slog.Warn("failed to hydrate upstream key health registry", "error", err)
		} else {
			for i := range keys {
				item, ok := upstreamHealthSnapshotFromExtra(keys[i].ID, keys[i].Extra)
				if !ok && !keys[i].ObservationEnabledKnown {
					// Provider snapshots and lightweight test repositories may not
					// carry the dedicated preference column or a durable Extra
					// snapshot. Do not erase an already hydrated runtime item.
					continue
				}
				if !ok {
					item = defaultUpstreamHealthSnapshot(keys[i].ID)
				}
				// The dedicated column is authoritative. Extra.health is retained
				// as the durable snapshot for backwards compatibility, but its
				// observation flag may be stale after provider synchronization.
				if keys[i].ObservationEnabledKnown {
					applyPersistedObservationPreference(&item, keys[i])
				}
				item = normalizeUpstreamHealthSnapshot(item)
				GlobalUpstreamHealthRegistry().Hydrate(item)
			}
		}
	}
	return svc
}

func (s *UpstreamConfigService) SetUpstreamAuthSessionManager(manager UpstreamAuthSessionManager) {
	s.authSessionManager = manager
}

func (s *UpstreamConfigService) GetAuthSessionStatus(ctx context.Context, id int64) (*UpstreamAuthSessionStatus, error) {
	cfg, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if s.authSessionManager == nil {
		return &UpstreamAuthSessionStatus{Provider: cfg.Provider, AuthMode: cfg.AuthMode}, nil
	}
	return s.authSessionManager.Status(ctx, id, cfg)
}
func (s *UpstreamConfigService) ClearAuthSession(ctx context.Context, id int64) error {
	if s.authSessionManager == nil {
		return nil
	}
	return s.authSessionManager.Clear(ctx, id)
}
func (s *UpstreamConfigService) ClearAuthSessionCooldown(ctx context.Context, id int64) error {
	if s.authSessionManager == nil {
		return nil
	}
	return s.authSessionManager.ClearCooldown(ctx, id)
}
func (s *UpstreamConfigService) ForceAuthSessionReauth(ctx context.Context, id int64) error {
	if s.authSessionManager == nil {
		return nil
	}
	return s.authSessionManager.ForceReauth(ctx, id)
}

func (s *UpstreamConfigService) SetHealthProbeDependencies(prober interface {
	RunUpstreamHealthProbe(ctx context.Context, account *Account, model string) (UpstreamHealthProbeResult, error)
}, settingService *SettingService) {
	if s == nil {
		return
	}
	s.accountProber = prober
	s.settingService = settingService
}

// SetUpstreamBalanceNotifier wires the optional administrator notification
// sink. Keeping it optional preserves lightweight service/repository tests.
func (s *UpstreamConfigService) SetUpstreamBalanceNotifier(notifier UpstreamBalanceNotifier) {
	if s != nil {
		s.upstreamBalanceNotifier = notifier
	}
}

// SetOpenAIScheduleReporter wires the OpenAI scheduler feedback sink used by
// upstream health probes. The dependency is optional for lightweight callers.
func (s *UpstreamConfigService) SetOpenAIScheduleReporter(reporter any) {
	if s != nil {
		s.openAIScheduleReporter = reporter
	}
}

func (s *UpstreamConfigService) reportOpenAIScheduleResult(account *Account, model string, success bool, firstTokenMs *int) {
	if s == nil || s.openAIScheduleReporter == nil || account == nil {
		return
	}
	switch reporter := s.openAIScheduleReporter.(type) {
	case interface {
		ReportOpenAIAccountScheduleResult(any, string, bool, *int, ...error) bool
	}:
		reporter.ReportOpenAIAccountScheduleResult(account, model, success, firstTokenMs)
	case interface {
		ReportOpenAIAccountScheduleResult(int64, string, bool, *int)
	}:
		reporter.ReportOpenAIAccountScheduleResult(account.ID, model, success, firstTokenMs)
	}
}

func (s *UpstreamConfigService) SetAccountTestService(accountTestService *AccountTestService) {
	if s != nil {
		s.accountTestService = accountTestService
	}
}

func (s *UpstreamConfigService) GetProbeModels(ctx context.Context) (UpstreamProbeModels, error) {
	if s == nil || s.settingService == nil {
		return DefaultUpstreamProbeModels(), nil
	}
	return s.settingService.GetUpstreamProbeModels(ctx)
}

func (s *UpstreamConfigService) GetProbePlatformCatalog() []UpstreamProbePlatform {
	return DefaultUpstreamProbePlatformCatalog()
}

func (s *UpstreamConfigService) GetManagementSettings(ctx context.Context) (UpstreamManagementSettings, error) {
	if s == nil || s.settingService == nil {
		return UpstreamManagementSettings{TTFTGuard: *DefaultOpenAITTFTGuardSettings(), ProbeModels: DefaultUpstreamProbeModels(), ProbeIntervalSeconds: DefaultUpstreamProbeIntervalSeconds, ProbeGuard: DefaultUpstreamProbeGuardSettings(), ModelAliasRules: map[string]string{}, ConfidenceProbe: DefaultUpstreamConfidenceProbeSettings()}, nil
	}
	ttft, err := s.settingService.GetOpenAITTFTGuardSettings(ctx)
	if err != nil {
		return UpstreamManagementSettings{}, err
	}
	models, err := s.settingService.GetUpstreamProbeModels(ctx)
	if err != nil {
		return UpstreamManagementSettings{}, err
	}
	interval, err := s.settingService.GetUpstreamProbeIntervalSeconds(ctx)
	if err != nil {
		return UpstreamManagementSettings{}, err
	}
	s.healthProbeIntervalSeconds.Store(int64(interval))
	guard, err := s.settingService.GetUpstreamProbeGuardSettings(ctx)
	if err != nil {
		return UpstreamManagementSettings{}, err
	}
	aliases, err := s.settingService.GetUpstreamModelAliasRules(ctx)
	if err != nil {
		return UpstreamManagementSettings{}, err
	}
	confidence, confidenceErr := s.settingService.GetUpstreamConfidenceProbeSettings(ctx)
	if confidenceErr != nil {
		return UpstreamManagementSettings{}, confidenceErr
	}
	return UpstreamManagementSettings{TTFTGuard: *ttft, ProbeModels: models, ProbeIntervalSeconds: interval, ProbeGuard: guard, ModelAliasRules: aliases, ConfidenceProbe: confidence}, nil
}

func (s *UpstreamConfigService) SetManagementSettings(ctx context.Context, settings UpstreamManagementSettings) error {
	if s == nil || s.settingService == nil {
		return infraerrors.ServiceUnavailable("UPSTREAM_MANAGEMENT_SETTINGS_UNAVAILABLE", "upstream management settings are unavailable")
	}
	if settings.ProbeIntervalSeconds == 0 {
		settings.ProbeIntervalSeconds = DefaultUpstreamProbeIntervalSeconds
	}
	if settings.ProbeGuard.SuspendAfterFailures == 0 && settings.ProbeGuard.RecoverySuccesses == 0 && settings.ProbeGuard.CustomErrorCodes == nil {
		settings.ProbeGuard = DefaultUpstreamProbeGuardSettings()
	}
	aliases, err := NormalizeUpstreamModelAliasRules(settings.ModelAliasRules)
	if err != nil {
		return err
	}
	if err := s.settingService.SetOpenAITTFTGuardProbeModelsIntervalAndGuardWithAliases(ctx, &settings.TTFTGuard, settings.ProbeModels, settings.ProbeIntervalSeconds, settings.ProbeGuard, aliases); err != nil {
		return err
	}
	if err := s.settingService.SetUpstreamConfidenceProbeSettings(ctx, settings.ConfidenceProbe); err != nil {
		return err
	}
	settings.ProbeGuard, _ = NormalizeUpstreamProbeGuardSettings(settings.ProbeGuard)
	transitions := GlobalUpstreamHealthRegistry().ReevaluateProbeGuard(settings.ProbeGuard, time.Now().UTC())
	if s.repo != nil {
		for _, transition := range transitions {
			if err := s.saveHealthTransition(ctx, transition.Current.KeyID, transition); err != nil {
				slog.Warn("failed to persist upstream probe guard re-evaluation", "key_id", transition.Current.KeyID, "error", err)
			}
		}
	}
	s.healthProbeIntervalSeconds.Store(int64(settings.ProbeIntervalSeconds))
	return nil
}

func (s *UpstreamConfigService) GetProbeModelCandidates(ctx context.Context) (map[string][]string, error) {
	models, err := s.GetProbeModels(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make(map[string]map[string]struct{})
	for _, platform := range DefaultUpstreamProbePlatformCatalog() {
		candidates[platform.ID] = make(map[string]struct{}, len(platform.Models)+1)
		for _, value := range platform.Models {
			if value = strings.TrimSpace(value); value != "" {
				candidates[platform.ID][value] = struct{}{}
			}
		}
	}
	for platform, value := range models.AsMap() {
		if bucket, ok := candidates[platform]; ok && strings.TrimSpace(value) != "" {
			bucket[strings.TrimSpace(value)] = struct{}{}
		}
	}
	if scoped, ok := s.accountRepo.(ScopedAccountLister); ok {
		accounts, _, listErr := scoped.ListWithFiltersScoped(ctx, pagination.PaginationParams{Page: 1, PageSize: 1000}, "", "", "", "", 0, "", AccountListScopeUpstream)
		if listErr != nil {
			return nil, listErr
		}
		for _, account := range accounts {
			bucket, exists := candidates[strings.ToLower(strings.TrimSpace(account.Platform))]
			if !exists {
				continue
			}
			for key, value := range account.schedulableModelMapping(time.Now().UTC()) {
				if strings.TrimSpace(key) != "" {
					bucket[key] = struct{}{}
				}
				if strings.TrimSpace(value) != "" {
					bucket[value] = struct{}{}
				}
			}
			if raw, ok := account.Credentials["model_whitelist"]; ok {
				for _, value := range stringSliceFromAny(raw) {
					if strings.TrimSpace(value) != "" {
						bucket[value] = struct{}{}
					}
				}
			}
		}
	}
	if usageReader, ok := s.accountRepo.(upstreamProbeModelUsageReader); ok {
		recent, usageErr := usageReader.ListRecentUpstreamProbeModels(ctx, time.Now().UTC().Add(-30*24*time.Hour), 500)
		if usageErr != nil {
			slog.Warn("failed to load recent upstream probe model candidates", "error", usageErr)
		} else {
			for platform, values := range recent {
				bucket, exists := candidates[strings.ToLower(strings.TrimSpace(platform))]
				if !exists {
					continue
				}
				for _, value := range values {
					if value = strings.TrimSpace(value); value != "" {
						bucket[value] = struct{}{}
					}
				}
			}
		}
	}
	result := make(map[string][]string, len(candidates))
	for platform, values := range candidates {
		items := make([]string, 0, len(values))
		for value := range values {
			items = append(items, value)
		}
		sort.Strings(items)
		result[platform] = items
	}
	return result, nil
}

func stringSliceFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func (s *UpstreamConfigService) SetProbeModels(ctx context.Context, models UpstreamProbeModels) error {
	if s == nil || s.settingService == nil {
		return infraerrors.ServiceUnavailable("UPSTREAM_PROBE_SETTINGS_UNAVAILABLE", "upstream probe settings are unavailable")
	}
	return s.settingService.SetUpstreamProbeModels(ctx, models)
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

func (s *UpstreamConfigService) List(ctx context.Context, params pagination.PaginationParams, filter UpstreamConfigListFilter) ([]UpstreamConfig, *pagination.PaginationResult, error) {
	configs, total, err := s.repo.List(ctx, params, filter)
	if err != nil {
		return nil, nil, err
	}
	for i := range configs {
		hydrateUpstreamConfigImagePricing(&configs[i])
		if s.authSessionManager != nil {
			configs[i].AuthSession, _ = s.GetAuthSessionStatus(ctx, configs[i].ID)
		}
	}
	return configs, total, nil
}

func (s *UpstreamConfigService) GetByID(ctx context.Context, id int64) (*UpstreamConfig, error) {
	config, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	hydrateUpstreamConfigImagePricing(config)
	if s.authSessionManager != nil {
		config.AuthSession, _ = s.GetAuthSessionStatus(ctx, config.ID)
	}
	return config, nil
}

func (s *UpstreamConfigService) Create(ctx context.Context, config *UpstreamConfig) (*UpstreamConfig, error) {
	if config != nil {
		config.Provider = normalizeUpstreamProvider(config.Provider)
		config.AuthMode = normalizeUpstreamAuthMode(config.AuthMode)
		if config.SchedulingEnabled == nil {
			enabled := true
			config.SchedulingEnabled = &enabled
		}
		pruneUpstreamProviderCredentials(config.Credentials, config.Provider, config.AuthMode)
		if err := applyUpstreamSchedulerConcurrencyOverride(config); err != nil {
			return nil, err
		}
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
	previousAuthFingerprint := UpstreamAuthCredentialFingerprint(current)
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
	if patch.SchedulingEnabled != nil {
		enabled := *patch.SchedulingEnabled
		current.SchedulingEnabled = &enabled
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
	previousOverride := extraValue(current.Extra, UpstreamSchedulerConcurrencyOverrideKey)
	if patch.Extra != nil {
		current.Extra = patch.Extra
	}
	if !patch.ClearSchedulerConcurrencyOverride && patch.SchedulerConcurrencyOverride == nil && previousOverride != nil {
		if current.Extra == nil {
			current.Extra = map[string]any{}
		}
		current.Extra[UpstreamSchedulerConcurrencyOverrideKey] = previousOverride
	}
	current.SchedulerConcurrencyOverride = patch.SchedulerConcurrencyOverride
	current.ClearSchedulerConcurrencyOverride = patch.ClearSchedulerConcurrencyOverride
	if err := applyUpstreamSchedulerConcurrencyOverride(current); err != nil {
		return nil, err
	}
	if err := normalizeAndValidateUpstreamConfig(current, false); err != nil {
		return nil, err
	}
	if err := s.repo.Update(ctx, current); err != nil {
		return nil, err
	}
	if s.authSessionManager != nil && previousAuthFingerprint != UpstreamAuthCredentialFingerprint(current) {
		if err := s.authSessionManager.Clear(ctx, id); err != nil {
			return nil, err
		}
	}
	return s.GetByID(ctx, id)
}

func applyUpstreamSchedulerConcurrencyOverride(config *UpstreamConfig) error {
	if config == nil {
		return nil
	}
	if config.Extra == nil {
		config.Extra = map[string]any{}
	}
	if config.ClearSchedulerConcurrencyOverride {
		delete(config.Extra, UpstreamSchedulerConcurrencyOverrideKey)
		return nil
	}
	if config.SchedulerConcurrencyOverride == nil {
		return nil
	}
	value := *config.SchedulerConcurrencyOverride
	if value < 1 || value > MaxUpstreamSchedulerConcurrency {
		return infraerrors.BadRequest("UPSTREAM_SCHEDULER_CONCURRENCY_INVALID", "scheduler_concurrency_override must be between 1 and 1000000")
	}
	config.Extra[UpstreamSchedulerConcurrencyOverrideKey] = value
	return nil
}

func (s *UpstreamConfigService) SetSchedulingEnabled(ctx context.Context, id int64, enabled bool) (*UpstreamConfig, error) {
	current, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, ErrUpstreamConfigNotFound
	}
	current.SchedulingEnabled = &enabled
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
	if provider != UpstreamProviderLCodex {
		delete(credentials, AccountCredentialLCodexLoginIdentifier)
		delete(credentials, AccountCredentialLCodexLoginPassword)
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
	_, err := s.DeleteWithOptions(ctx, id, UpstreamConfigDeleteOptions{})
	return err
}

func (s *UpstreamConfigService) DeleteWithOptions(ctx context.Context, id int64, options UpstreamConfigDeleteOptions) (UpstreamConfigDeleteResult, error) {
	if options.DeleteSyncManagedAccounts {
		cascadeRepo, ok := s.repo.(UpstreamConfigCascadeDeleteRepository)
		if !ok {
			return UpstreamConfigDeleteResult{}, infraerrors.InternalServer("UPSTREAM_CONFIG_CASCADE_DELETE_UNAVAILABLE", "upstream config cascade deletion is unavailable")
		}
		return cascadeRepo.DeleteWithOptions(ctx, id, options)
	}

	if summaryRepo, ok := s.repo.(UpstreamConfigBindingSummaryRepository); ok {
		summary, err := summaryRepo.GetBindingSummary(ctx, id)
		if err != nil {
			return UpstreamConfigDeleteResult{}, err
		}
		if summary.BoundAccountCount > 0 {
			return UpstreamConfigDeleteResult{}, upstreamConfigInUseError(summary)
		}
		return UpstreamConfigDeleteResult{}, s.repo.Delete(ctx, id)
	}
	count, err := s.repo.CountAccounts(ctx, id)
	if err != nil {
		return UpstreamConfigDeleteResult{}, err
	}
	if count > 0 {
		return UpstreamConfigDeleteResult{}, upstreamConfigInUseError(UpstreamConfigBindingSummary{
			BoundAccountCount:       int(count),
			SyncManagedAccountCount: int(count),
		})
	}
	return UpstreamConfigDeleteResult{}, s.repo.Delete(ctx, id)
}

func upstreamConfigInUseError(summary UpstreamConfigBindingSummary) error {
	return infraerrors.New(http.StatusBadRequest, "UPSTREAM_CONFIG_IN_USE", "upstream config is used by accounts").WithMetadata(map[string]string{
		"bound_account_count":        strconv.Itoa(summary.BoundAccountCount),
		"manual_account_count":       strconv.Itoa(summary.ManualAccountCount),
		"missing_key_account_count":  strconv.Itoa(summary.MissingKeyAccountCount),
		"sync_managed_account_count": strconv.Itoa(summary.SyncManagedAccountCount),
	})
}

func (s *UpstreamConfigService) ListKeys(ctx context.Context, upstreamConfigID int64) ([]UpstreamKey, error) {
	config, err := s.repo.GetByID(ctx, upstreamConfigID)
	if err != nil {
		return nil, err
	}
	keys, err := s.repo.ListKeys(ctx, upstreamConfigID)
	if err != nil {
		return nil, err
	}
	hydrateUpstreamKeysImagePricing(keys, config)
	return keys, nil
}

func (s *UpstreamConfigService) GetKeyByID(ctx context.Context, keyID int64) (*UpstreamKey, error) {
	return s.repo.GetKeyByID(ctx, keyID)
}

func (s *UpstreamConfigService) GetUpstreamHealthConfidence(ctx context.Context, keyID int64) (UpstreamHealthConfidenceSummary, error) {
	reader, ok := s.repo.(UpstreamHealthConfidenceReader)
	if !ok {
		return UpstreamHealthConfidenceSummary{}, nil
	}
	return reader.GetUpstreamHealthConfidence(ctx, keyID)
}

// GetKeyHealth returns the independent health snapshot for one key. The
// runtime registry is preferred, while the JSON extra field is used to
// rehydrate state after a process restart without changing the key schema.
func (s *UpstreamConfigService) GetKeyHealth(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	key, err := s.repo.GetKeyByID(ctx, keyID)
	if err != nil {
		return UpstreamHealthSnapshot{}, err
	}
	item := GlobalUpstreamHealthRegistry().Snapshot(keyID)
	if item.UpdatedAt.IsZero() && key != nil {
		if durable, ok := upstreamHealthSnapshotFromExtra(keyID, key.Extra); ok {
			item = GlobalUpstreamHealthRegistry().Hydrate(durable)
		}
	}
	if key != nil && key.ObservationEnabledKnown {
		// The dedicated preference column is authoritative across restarts and
		// provider refreshes. Keep the rest of the snapshot from the registry or
		// legacy Extra payload, but always apply this persisted toggle.
		applyPersistedObservationPreference(&item, *key)
		item = normalizeUpstreamHealthSnapshot(item)
	}
	item.KeyID = keyID
	return item, nil
}

func applyPersistedObservationPreference(item *UpstreamHealthSnapshot, key UpstreamKey) {
	if item == nil || !key.ObservationEnabledKnown {
		return
	}
	item.ObservationEnabled = key.ObservationEnabled
	if key.ObservationEnabled && item.Status == UpstreamHealthDisabled {
		// A pre-migration Extra snapshot may still carry the old disabled
		// status even though no explicit administrator disable event existed.
		// The dedicated column is authoritative, so resume observation in the
		// neutral state instead of leaving the key permanently skipped.
		item.Status = UpstreamHealthObserving
		item.Reason = "observation_enabled"
	}
}

func (s *UpstreamConfigService) saveKeyHealth(ctx context.Context, keyID int64, item UpstreamHealthSnapshot) error {
	return s.saveKeyHealthTransition(ctx, keyID, UpstreamHealthTransition{Current: item})
}

func (s *UpstreamConfigService) saveKeyHealthTransition(ctx context.Context, keyID int64, transition UpstreamHealthTransition) error {
	return s.saveKeyHealthTransitionWithObservation(ctx, keyID, transition, nil)
}

func (s *UpstreamConfigService) saveKeyHealthTransitionWithObservation(ctx context.Context, keyID int64, transition UpstreamHealthTransition, observation *UpstreamHealthObservation) error {
	health := upstreamHealthSnapshotMap(transition.Current)
	if repo, ok := s.repo.(upstreamKeyHealthObservationRepository); ok {
		return repo.PatchKeyHealthWithObservation(ctx, keyID, health, upstreamHealthTransitionEvent(transition), observation)
	}
	if repo, ok := s.repo.(upstreamKeyHealthEventRepository); ok {
		return repo.PatchKeyHealthWithEvent(ctx, keyID, health, upstreamHealthTransitionEvent(transition))
	}
	if repo, ok := s.repo.(upstreamKeyHealthRepository); ok {
		return repo.PatchKeyHealth(ctx, keyID, health)
	}
	key, err := s.repo.GetKeyByID(ctx, keyID)
	if err != nil {
		return err
	}
	if key.Extra == nil {
		key.Extra = map[string]any{}
	}
	key.Extra["health"] = health
	return s.repo.UpdateKey(ctx, key)
}

func upstreamHealthTransitionEvent(transition UpstreamHealthTransition) *UpstreamEvent {
	if !transition.StateChanged() {
		return nil
	}
	current := transition.Current
	severity := "info"
	switch current.Status {
	case UpstreamHealthSuspended:
		severity = "error"
	case UpstreamHealthDegraded, UpstreamHealthRecovering, UpstreamHealthDisabled:
		severity = "warning"
	}
	return &UpstreamEvent{
		Type:     "key_health_state_changed",
		Severity: severity,
		Message:  fmt.Sprintf("Upstream key health changed from %s to %s", transition.Previous.Status, current.Status),
		Payload: map[string]any{
			"previous_status":              string(transition.Previous.Status),
			"current_status":               string(current.Status),
			"previous_observation_enabled": transition.Previous.ObservationEnabled,
			"observation_enabled":          current.ObservationEnabled,
			"reason":                       current.Reason,
			"last_probe_status":            current.LastProbeStatus,
			"last_traffic_status":          current.LastTrafficStatus,
			"consecutive_failures":         current.ConsecutiveFails,
			"recovery_samples":             current.RecoverySamples,
			"recovery_samples_required":    current.RecoverySamplesRequired,
		},
		CreatedAt: current.UpdatedAt,
	}
}

func (s *UpstreamConfigService) withHealthKeyLock(keyID int64, fn func() error) error {
	if s == nil {
		return fn()
	}
	value, _ := s.healthLocks.LoadOrStore(keyID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}

func (s *UpstreamConfigService) saveHealthTransition(ctx context.Context, keyID int64, transition UpstreamHealthTransition) error {
	return s.saveHealthTransitionWithObservation(ctx, keyID, transition, nil)
}

func (s *UpstreamConfigService) saveHealthTransitionWithObservation(ctx context.Context, keyID int64, transition UpstreamHealthTransition, observation *UpstreamHealthObservation) error {
	if err := s.saveKeyHealthTransitionWithObservation(ctx, keyID, transition, observation); err != nil {
		GlobalUpstreamHealthRegistry().Hydrate(transition.Previous)
		var appErr *infraerrors.ApplicationError
		if errors.As(err, &appErr) {
			return err
		}
		return errUpstreamHealthEvidencePersistFailed.WithCause(err)
	}
	if err := s.syncProbeSchedulingState(ctx, keyID, transition.Current); err != nil {
		// Health evidence must remain fail-open: a transient account-repository
		// failure must not turn a successful probe into an internal-error result.
		slog.Warn("failed to synchronize probe temporary scheduling state", "key_id", keyID, "error", err)
	}
	return nil
}

func isProbeTempUnschedulableReason(reason string) bool {
	return strings.HasPrefix(strings.TrimSpace(reason), upstreamProbeTempUnschedReasonPrefix)
}

func probeTempUnschedulableReason(item UpstreamHealthSnapshot) string {
	class := strings.TrimSpace(item.LastFailureClass)
	if class == "" {
		class = "failure"
	}
	return upstreamProbeTempUnschedReasonPrefix + class
}

// syncProbeSchedulingState mirrors probe-owned health quarantine into the
// account's existing temporary-unschedulable field. It never overwrites or
// clears a block created by a human, quota, auth, proxy, or another policy.
func (s *UpstreamConfigService) syncProbeSchedulingState(ctx context.Context, keyID int64, item UpstreamHealthSnapshot) error {
	if s == nil || s.accountRepo == nil || keyID <= 0 {
		return nil
	}
	lister, ok := s.accountRepo.(upstreamAccountBindingLister)
	if !ok {
		return nil
	}
	accounts, err := lister.ListByUpstreamKeyID(ctx, keyID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	probeQuarantine := item.SuspensionSource == "probe" && (item.Status == UpstreamHealthSuspended || item.Status == UpstreamHealthRecovering)
	for i := range accounts {
		account := accounts[i]
		probeOwned := isProbeTempUnschedulableReason(account.TempUnschedulableReason)
		if probeQuarantine {
			// Never take over an active block owned by another subsystem.
			if account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now) && !probeOwned {
				continue
			}
			if probeOwned && account.TempUnschedulableUntil != nil && account.TempUnschedulableUntil.After(now) {
				continue
			}
			if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, now.Add(upstreamProbeTempUnschedulableDuration), probeTempUnschedulableReason(item)); err != nil {
				return err
			}
			continue
		}
		if probeOwned {
			if err := s.accountRepo.ClearTempUnschedulable(ctx, account.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ClearProbeSuspension is called by the existing admin recovery action. It
// persists the reset as an administrator observation and only affects a
// probe-owned suspension.
func (s *UpstreamConfigService) ClearProbeSuspension(ctx context.Context, keyID int64) error {
	if s == nil || keyID <= 0 {
		return nil
	}
	return s.withHealthKeyLock(keyID, func() error {
		transition, changed := GlobalUpstreamHealthRegistry().ResetProbeSuspension(keyID, time.Now().UTC())
		if !changed {
			return nil
		}
		observation := &UpstreamHealthObservation{
			UpstreamKeyID: keyID,
			ObservedAt:    transition.Current.UpdatedAt,
			State:         transition.Current.Status,
			Source:        "admin",
			Result:        "manual_recovery",
			Reason:        transition.Current.Reason,
		}
		return s.saveHealthTransitionWithObservation(ctx, keyID, transition, observation)
	})
}

func (s *UpstreamConfigService) ListUpstreamHealthHistories(ctx context.Context, keyIDs []int64, limit int) (map[int64][]UpstreamHealthObservation, error) {
	if limit <= 0 || limit > UpstreamHealthHistoryLimit {
		limit = UpstreamHealthHistoryLimit
	}
	repo, ok := s.repo.(upstreamKeyHealthHistoryRepository)
	if !ok {
		return map[int64][]UpstreamHealthObservation{}, nil
	}
	return repo.ListKeyHealthHistories(ctx, keyIDs, limit)
}

func upstreamHealthSnapshotMap(item UpstreamHealthSnapshot) map[string]any {
	out := map[string]any{
		"status":                    string(item.Status),
		"observation_enabled":       item.ObservationEnabled,
		"reason":                    item.Reason,
		"last_probe_status":         item.LastProbeStatus,
		"last_traffic_status":       item.LastTrafficStatus,
		"consecutive_failures":      item.ConsecutiveFails,
		"recovery_samples":          item.RecoverySamples,
		"recovery_samples_required": item.RecoverySamplesRequired,
		"last_failure_source":       item.LastFailureSource,
		"last_failure_class":        item.LastFailureClass,
		"suspension_source":         item.SuspensionSource,
	}
	if item.LastProbeAt != nil {
		out["last_probe_at"] = item.LastProbeAt.UTC().Format(time.RFC3339Nano)
	}
	if item.LastProbeTTFTMs != nil {
		out["last_probe_ttft_ms"] = *item.LastProbeTTFTMs
	}
	if item.LastEvidenceAt != nil {
		out["last_evidence_at"] = item.LastEvidenceAt.UTC().Format(time.RFC3339Nano)
	}
	if !item.UpdatedAt.IsZero() {
		out["updated_at"] = item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func upstreamHealthSnapshotFromExtra(keyID int64, extra map[string]any) (UpstreamHealthSnapshot, bool) {
	if extra == nil {
		return UpstreamHealthSnapshot{}, false
	}
	raw, ok := extra["health"].(map[string]any)
	if !ok {
		return UpstreamHealthSnapshot{}, false
	}
	item := defaultUpstreamHealthSnapshot(keyID)
	if value, ok := raw["status"].(string); ok {
		item.Status = UpstreamHealthStatus(strings.TrimSpace(value))
	}
	if value, ok := raw["observation_enabled"].(bool); ok {
		item.ObservationEnabled = value
	}
	if value, ok := raw["reason"].(string); ok {
		item.Reason = value
	}
	if value, ok := raw["last_probe_status"].(string); ok {
		item.LastProbeStatus = value
	}
	if value, exists := raw["last_probe_ttft_ms"]; exists {
		ttftMs := int64(upstreamHealthInt(value))
		if ttftMs >= 0 {
			item.LastProbeTTFTMs = &ttftMs
		}
	}
	if value, ok := raw["last_traffic_status"].(string); ok {
		item.LastTrafficStatus = value
	}
	if value, ok := raw["last_failure_source"].(string); ok {
		item.LastFailureSource = strings.TrimSpace(value)
	}
	if value, ok := raw["last_failure_class"].(string); ok {
		item.LastFailureClass = strings.TrimSpace(value)
	}
	if value, ok := raw["suspension_source"].(string); ok {
		item.SuspensionSource = strings.TrimSpace(value)
	}
	item.ConsecutiveFails = upstreamHealthInt(raw["consecutive_failures"])
	item.RecoverySamples = upstreamHealthInt(raw["recovery_samples"])
	if required := upstreamHealthInt(raw["recovery_samples_required"]); required > 0 {
		item.RecoverySamplesRequired = required
	}
	item.LastProbeAt = upstreamHealthTime(raw["last_probe_at"])
	item.LastEvidenceAt = upstreamHealthTime(raw["last_evidence_at"])
	if item.SuspensionSource == "" && (item.Status == UpstreamHealthSuspended || item.Status == UpstreamHealthRecovering) && item.LastProbeAt != nil && (item.LastEvidenceAt == nil || !item.LastProbeAt.Before(*item.LastEvidenceAt)) {
		item.SuspensionSource = "probe"
		if item.LastFailureSource == "" {
			item.LastFailureSource = "probe"
		}
		if item.LastFailureClass == "" {
			item.LastFailureClass = upstreamProbeFailureClass(item.LastProbeStatus, item.Reason)
		}
	}
	if updated := upstreamHealthTime(raw["updated_at"]); updated != nil {
		item.UpdatedAt = *updated
	}
	return normalizeUpstreamHealthSnapshot(item), true
}

func upstreamHealthInt(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	default:
		return 0
	}
}

func upstreamHealthTime(value any) *time.Time {
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil
	}
	parsed = parsed.UTC()
	return &parsed
}

func (s *UpstreamConfigService) SetKeyObservation(ctx context.Context, keyID int64, enabled bool) (UpstreamHealthSnapshot, error) {
	var item UpstreamHealthSnapshot
	var saveErr error
	err := s.withHealthKeyLock(keyID, func() error {
		transition := GlobalUpstreamHealthRegistry().SetObservationTransition(keyID, enabled, time.Now().UTC())
		item = transition.Current
		result := "disabled"
		if enabled {
			result = "enabled"
		}
		observation := &UpstreamHealthObservation{ObservedAt: item.UpdatedAt, State: item.Status, Source: "admin", Result: result, Reason: item.Reason}
		saveErr = s.saveHealthTransitionWithObservation(ctx, keyID, transition, observation)
		return saveErr
	})
	if err != nil {
		return UpstreamHealthSnapshot{}, err
	}
	return item, nil
}

func (s *UpstreamConfigService) ProbeKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	if keyID <= 0 {
		return UpstreamHealthSnapshot{}, ErrUpstreamKeyNotFound
	}
	result := s.healthProbeSF.DoChan(strconv.FormatInt(keyID, 10), func() (any, error) {
		return s.probeKeyOnce(ctx, keyID, false)
	})
	select {
	case output := <-result:
		if output.Err != nil {
			return UpstreamHealthSnapshot{}, output.Err
		}
		item, ok := output.Val.(UpstreamHealthSnapshot)
		if !ok {
			return UpstreamHealthSnapshot{}, infraerrors.ServiceUnavailable("UPSTREAM_KEY_PROBE_INVALID_RESULT", "upstream key probe returned an invalid result")
		}
		return item, nil
	case <-ctx.Done():
		return UpstreamHealthSnapshot{}, ctx.Err()
	}
}

// ProbeDueKey is the background-runner path. It performs a final freshness
// check immediately before dispatch; unlike ProbeKey, this path may be a
// harmless no-op when real traffic arrived after due-list construction.
func (s *UpstreamConfigService) ProbeDueKey(ctx context.Context, keyID int64) (UpstreamHealthSnapshot, error) {
	if keyID <= 0 {
		return UpstreamHealthSnapshot{}, ErrUpstreamKeyNotFound
	}
	result := s.healthProbeSF.DoChan("due:"+strconv.FormatInt(keyID, 10), func() (any, error) {
		return s.probeKeyOnce(ctx, keyID, true)
	})
	select {
	case output := <-result:
		if output.Err != nil {
			return UpstreamHealthSnapshot{}, output.Err
		}
		item, ok := output.Val.(UpstreamHealthSnapshot)
		if !ok {
			return UpstreamHealthSnapshot{}, infraerrors.ServiceUnavailable("UPSTREAM_KEY_PROBE_INVALID_RESULT", "upstream key probe returned an invalid result")
		}
		return item, nil
	case <-ctx.Done():
		return UpstreamHealthSnapshot{}, ctx.Err()
	}
}

func (s *UpstreamConfigService) probeKeyOnce(ctx context.Context, keyID int64, background bool) (UpstreamHealthSnapshot, error) {
	if locker, ok := s.repo.(upstreamHealthProbeLockRepository); ok {
		var item UpstreamHealthSnapshot
		acquired, err := locker.WithUpstreamHealthProbeLock(ctx, keyID, func(lockCtx context.Context) error {
			var probeErr error
			item, probeErr = s.probeKeyUnlocked(lockCtx, keyID, background)
			return probeErr
		})
		if err != nil {
			return UpstreamHealthSnapshot{}, err
		}
		if !acquired {
			return UpstreamHealthSnapshot{}, infraerrors.Conflict("UPSTREAM_KEY_PROBE_IN_PROGRESS", "upstream key probe is already in progress")
		}
		return item, nil
	}
	return s.probeKeyUnlocked(ctx, keyID, background)
}

func (s *UpstreamConfigService) probeKeyUnlocked(ctx context.Context, keyID int64, background bool) (UpstreamHealthSnapshot, error) {
	key, err := s.repo.GetKeyByID(ctx, keyID)
	if err != nil {
		return UpstreamHealthSnapshot{}, err
	}
	if key == nil {
		return UpstreamHealthSnapshot{}, ErrUpstreamKeyNotFound
	}
	lister, ok := s.accountRepo.(upstreamAccountBindingLister)
	if !ok || s.accountProber == nil {
		return UpstreamHealthSnapshot{}, infraerrors.ServiceUnavailable("UPSTREAM_KEY_PROBE_UNAVAILABLE", "upstream key probe is unavailable")
	}
	accounts, err := lister.ListByUpstreamKeyID(ctx, keyID)
	if err != nil {
		return UpstreamHealthSnapshot{}, err
	}
	if len(accounts) == 0 {
		return UpstreamHealthSnapshot{}, infraerrors.BadRequest("UPSTREAM_KEY_PROBE_ACCOUNT_REQUIRED", "configure the generated account before probing this key")
	}
	if len(accounts) > 1 {
		return UpstreamHealthSnapshot{}, infraerrors.Conflict("UPSTREAM_KEY_ACCOUNT_CONFLICT", "multiple accounts are bound to this upstream key")
	}
	account := accounts[0]
	models := DefaultUpstreamProbeModels()
	probeGuard := DefaultUpstreamProbeGuardSettings()
	if s.settingService != nil {
		if configured, loadErr := s.settingService.GetUpstreamProbeModels(ctx); loadErr == nil {
			models = configured
		}
		if configured, loadErr := s.settingService.GetUpstreamProbeGuardSettings(ctx); loadErr == nil {
			probeGuard = configured
		}
	}
	model := models.ModelFor(account.Platform)
	if model == "" || !UpstreamProbePlatformSupported(account.Platform) {
		return UpstreamHealthSnapshot{}, infraerrors.BadRequest("UPSTREAM_KEY_PROBE_PLATFORM_UNSUPPORTED", "upstream key platform does not support active probing")
	}
	// Scheduler feedback is reserved for the independent confidence mode: an
	// explicitly stored, enabled confidence configuration. Missing, disabled,
	// or invalid settings must not feed probe samples into the scheduler.
	confidenceIndependent := s.confidenceProbeIndependent(ctx)
	var result UpstreamHealthProbeResult
	var probeErr error
	if background {
		var current UpstreamHealthSnapshot
		var skip bool
		err := s.withHealthKeyLock(keyID, func() error {
			current = GlobalUpstreamHealthRegistry().Snapshot(keyID)
			cutoff := time.Now().UTC().Add(-s.effectiveHealthProbeInterval(ctx))
			if current.LastProbeAt != nil && current.LastProbeAt.After(cutoff) {
				skip = true
			}
			if !confidenceIndependent && current.LastEvidenceAt != nil && current.LastEvidenceAt.After(cutoff) {
				skip = true
			}
			if skip {
				return nil
			}
			probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			result, probeErr = s.accountProber.RunUpstreamHealthProbe(probeCtx, &account, model)
			return nil
		})
		if err != nil {
			return UpstreamHealthSnapshot{}, err
		}
		if skip {
			return current, nil
		}
	} else {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		result, probeErr = s.accountProber.RunUpstreamHealthProbe(probeCtx, &account, model)
	}
	probeRequestSucceeded := probeErr == nil
	if confidenceIndependent && s.openAIScheduleReporter != nil {
		var firstTokenMs *int
		if result.TTFTMs != nil && *result.TTFTMs > 0 {
			value := int(*result.TTFTMs)
			firstTokenMs = &value
		}
		s.reportOpenAIScheduleResult(&account, result.Model, probeRequestSucceeded, firstTokenMs)
	}
	if probeErr == nil && account.Platform == PlatformOpenAI && result.ConfidenceScore != nil && result.ConfidenceStatus != "current_success" {
		probeErr = infraerrors.New(http.StatusBadGateway, "UPSTREAM_KEY_PROBE_QUALITY_DEGRADED", "upstream Juice confidence classification is not current_success")
		result.Result = "quality_degraded"
		result.Reason = "probe_quality_degraded"
	}
	now := time.Now().UTC()
	var item UpstreamHealthSnapshot
	if err := s.withHealthKeyLock(keyID, func() error {
		registry := GlobalUpstreamHealthRegistry()
		var transition UpstreamHealthTransition
		if probeErr != nil {
			status := strings.TrimSpace(result.Result)
			if status == "" {
				status = upstreamProbeStatus(probeErr.Error())
			}
			reason := strings.TrimSpace(result.Reason)
			if reason == "" {
				reason = upstreamProbeReason(probeErr.Error())
			}
			transition = registry.RecordProbeFailureWithGuardTransition(keyID, status, reason, result.TTFTMs, now, probeGuard)
		} else {
			transition = registry.RecordProbeWithGuardSuccessTransition(keyID, "success", "probe_succeeded", result.TTFTMs, now, probeGuard)
		}
		item = transition.Current
		accountID := account.ID
		observation := &UpstreamHealthObservation{
			UpstreamConfigID: key.UpstreamConfigID, UpstreamKeyID: key.ID, AccountID: &accountID,
			Platform: account.Platform, Model: result.Model, Protocol: result.Protocol,
			ObservedAt: item.UpdatedAt, State: item.Status, Source: "probe", Result: item.LastProbeStatus, Reason: item.Reason,
			HTTPStatus: result.HTTPStatus, TTFTMs: result.TTFTMs, DurationMs: result.DurationMs,
			InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, OutputTPS: result.OutputTPS,
			ConfidenceScore: result.ConfidenceScore, ConfidencePromptVersion: result.ConfidencePromptVersion,
			RequestedEffort: result.RequestedEffort, ReasoningTokens: result.ReasoningTokens,
			ConfidenceChecks: result.ConfidenceChecks, ConfidenceStatus: result.ConfidenceStatus,
			ConfidenceEvidence: result.ConfidenceEvidence,
		}
		return s.saveHealthTransitionWithObservation(ctx, keyID, transition, observation)
	}); err != nil {
		return UpstreamHealthSnapshot{}, err
	}
	if item.LastProbeStatus != "success" {
		metadata := map[string]string{
			"probe_reason": strings.TrimSpace(result.Reason),
			"protocol":     strings.TrimSpace(result.Protocol),
			"model":        strings.TrimSpace(result.Model),
		}
		if result.HTTPStatus != nil {
			metadata["http_status"] = strconv.Itoa(*result.HTTPStatus)
		}
		for key, value := range metadata {
			if strings.TrimSpace(value) == "" {
				delete(metadata, key)
			}
		}
		return item, infraerrors.New(http.StatusBadGateway, "UPSTREAM_KEY_PROBE_FAILED", "upstream key probe failed").WithMetadata(metadata)
	}
	return item, nil
}

func upstreamProbeStatus(message string) string {
	lower := strings.ToLower(message)
	for _, code := range []string{"401", "403", "429", "529", "500", "502", "503", "504"} {
		if strings.Contains(lower, code) {
			return code
		}
	}
	return "transport_error"
}

func upstreamProbeReason(message string) string {
	switch upstreamProbeStatus(message) {
	case "401", "403":
		return "authentication_failed"
	case "429", "529":
		return "capacity_limited"
	case "500", "502", "503", "504":
		return "upstream_server_error"
	default:
		return "probe_transport_error"
	}
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
	key, err := s.repo.UpdateKeyPlatform(ctx, upstreamConfigID, keyID, platform, req.ExpectedUpdatedAt.UTC(), req.DisableBoundAccounts)
	if err != nil {
		return nil, err
	}
	config, err := s.repo.GetByID(ctx, upstreamConfigID)
	if err != nil {
		return nil, err
	}
	if config != nil && (config.Provider == UpstreamProviderSub2API || config.Provider == UpstreamProviderLCodex) {
		key.ImagePricing = deriveUpstreamKeyImagePricing(key, config)
	}
	return key, nil
}

func (s *UpstreamConfigService) UpdateKeyBaseURL(ctx context.Context, upstreamConfigID, keyID int64, req UpdateUpstreamKeyBaseURLRequest) (*UpstreamKey, error) {
	if req.ExpectedUpdatedAt.IsZero() {
		return nil, infraerrors.BadRequest("UPSTREAM_KEY_EXPECTED_UPDATED_AT_REQUIRED", "expected_updated_at is required")
	}
	var value *string
	if !req.ClearBaseURL {
		if req.BaseURL == nil || strings.TrimSpace(*req.BaseURL) == "" {
			return nil, infraerrors.BadRequest("UPSTREAM_KEY_BASE_URL_REQUIRED", "base_url is required")
		}
		normalized, err := normalizeUpstreamConfigURL(*req.BaseURL)
		if err != nil {
			return nil, infraerrors.BadRequest("UPSTREAM_KEY_BASE_URL_INVALID", "base_url must be a valid http or https URL")
		}
		value = &normalized
	}
	return s.repo.UpdateKeyBaseURL(ctx, upstreamConfigID, keyID, value, req.ExpectedUpdatedAt.UTC())
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
	var snapshot *upstreamProviderSnapshot
	if s.authSessionManager != nil {
		strategy := upstreamAuthStrategyFor(cfg, s)
		if strategy != nil {
			var authHandle *UpstreamAuthHandle
			authHandle, err = s.authSessionManager.Run(ctx, cfg, proxyURL, strategy, func(operationCtx context.Context, handle *UpstreamAuthHandle) error {
				authHandle = handle
				snapshot, err = adapter.SyncSnapshot(withUpstreamAuthHandle(operationCtx, handle), cfg, proxyURL, false)
				return err
			})
			if authHandle != nil && cfg.Provider == UpstreamProviderSub2API && authHandle.Refreshed {
				if value, ok := authHandle.Value.(sub2APIAuthValue); ok {
					if saveErr := s.repo.SaveRefreshedTokens(ctx, cfg.ID, value.AccessToken, value.RefreshToken, authHandle.ExpiresAt); saveErr != nil {
						err = saveErr
					}
				}
			}
		} else {
			err = adapter.Test(ctx, cfg, proxyURL)
		}
	} else if cfg.Provider == UpstreamProviderSub2API {
		snapshot, err = adapter.SyncSnapshot(ctx, cfg, proxyURL, false)
	} else {
		err = adapter.Test(ctx, cfg, proxyURL)
	}
	if snapshot != nil && snapshot.RefreshedTokens != nil {
		if saveErr := s.repo.SaveRefreshedTokens(ctx, cfg.ID, snapshot.RefreshedTokens.AccessToken, snapshot.RefreshedTokens.RefreshToken, snapshot.RefreshedTokens.ExpiresAt); saveErr != nil {
			err = saveErr
		}
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
	keys, result, syncErr := s.syncProviderConfig(ctx, cfg, runID, settings, true)
	if syncErr == nil {
		s.notifyUpstreamBalanceLow(ctx, cfg.ID)
	}
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
	results, err := s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API, UpstreamProviderNewAPI, UpstreamProviderLCodex}, UpstreamSyncTriggerScheduled)
	return scheduledSyncResults(results, err)
}

func (s *UpstreamConfigService) SyncActiveUpstreamConfigsManual(ctx context.Context) (int64, []UpstreamConfigSyncResult, error) {
	results, err := s.syncActiveUpstreamConfigs(ctx, []string{UpstreamProviderSub2API, UpstreamProviderNewAPI, UpstreamProviderLCodex}, UpstreamSyncTriggerManualBatch)
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
			_, result, err = s.syncProviderConfig(ctx, &cfg, runID, settings, trigger != UpstreamSyncTriggerScheduled)
			if err == nil {
				s.notifyUpstreamBalanceLow(ctx, cfg.ID)
			}
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

// notifyUpstreamBalanceLow projects an open balance_low incident into the
// administrator notification service after a successful sync transaction.
// Incident opening time is used for delivery deduplication across syncs.
func (s *UpstreamConfigService) notifyUpstreamBalanceLow(ctx context.Context, configID int64) {
	if s == nil || s.upstreamBalanceNotifier == nil || configID <= 0 {
		return
	}
	cfg, err := s.repo.GetByID(ctx, configID)
	if err != nil || cfg == nil {
		return
	}
	settings, err := s.readUpstreamSettings(ctx)
	if err != nil || settings == nil || settings.BalanceLowThresholdCNY <= 0 {
		return
	}
	balance, ok := finiteAnyFloat(cfg.Extra["balance_cny"])
	if !ok || balance >= settings.BalanceLowThresholdCNY {
		return
	}
	ops, ok := s.repo.(UpstreamOperationsRepository)
	if !ok {
		return
	}
	incidents, _, err := ops.ListUpstreamIncidents(ctx, configID, "open", 20, 0)
	if err != nil {
		return
	}
	for _, incident := range incidents {
		if incident.Type == "balance_low" && incident.Status == "open" {
			s.upstreamBalanceNotifier.NotifyUpstreamBalanceLow(ctx, cfg, balance, settings.BalanceLowThresholdCNY, time.Now().UTC(), incident.OpenedAt)
			return
		}
	}
}

func (s *UpstreamConfigService) syncProviderConfig(ctx context.Context, cfg *UpstreamConfig, runID int64, settings *UpstreamSettings, forceModelSync bool) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
	if cfg != nil && cfg.ID > 0 {
		unlock := s.lockUpstreamConfigSync(cfg.ID)
		defer unlock()
		if locker, ok := s.repo.(upstreamConfigSyncLockRepository); ok {
			var keys []UpstreamKey
			var result UpstreamConfigSyncResult
			var syncErr error
			lockErr := locker.WithUpstreamConfigSyncLock(ctx, cfg.ID, func(lockCtx context.Context) error {
				keys, result, syncErr = s.syncProviderConfigLocked(withUpstreamConfigSyncLockHeld(lockCtx), cfg, runID, settings, forceModelSync)
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
	return s.syncProviderConfigLocked(ctx, cfg, runID, settings, forceModelSync)
}

func (s *UpstreamConfigService) syncProviderConfigLocked(ctx context.Context, cfg *UpstreamConfig, runID int64, settings *UpstreamSettings, forceModelSync bool) ([]UpstreamKey, UpstreamConfigSyncResult, error) {
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
	var snapshot *upstreamProviderSnapshot
	var authHandle *UpstreamAuthHandle
	if s.authSessionManager != nil {
		strategy := upstreamAuthStrategyFor(cfg, s)
		if strategy != nil {
			authHandle, err = s.authSessionManager.Run(ctx, cfg, proxyURL, strategy, func(operationCtx context.Context, handle *UpstreamAuthHandle) error {
				snapshot, err = adapter.SyncSnapshot(withUpstreamAuthHandle(operationCtx, handle), cfg, proxyURL, true)
				return err
			})
		} else {
			snapshot, err = adapter.SyncSnapshot(ctx, cfg, proxyURL, true)
		}
	} else {
		snapshot, err = adapter.SyncSnapshot(ctx, cfg, proxyURL, true)
	}
	if authHandle != nil && cfg.Provider == UpstreamProviderSub2API {
		if value, ok := authHandle.Value.(sub2APIAuthValue); ok && authHandle.Refreshed {
			if saveErr := s.repo.SaveRefreshedTokens(ctx, cfg.ID, value.AccessToken, value.RefreshToken, authHandle.ExpiresAt); saveErr != nil {
				result.Error = adapter.SanitizeError(saveErr, cfg.Credentials)
				return nil, result, saveErr
			}
		}
	}
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
	if snapshot.DiscoveredAPIURL != nil && cfg.APIURL == nil {
		discovered := strings.TrimSpace(*snapshot.DiscoveredAPIURL)
		if discovered != "" {
			cfg.APIURL = &discovered
			if saveErr := s.repo.Update(ctx, cfg); saveErr != nil {
				result.Error = adapter.SanitizeError(saveErr, cfg.Credentials)
				result.Stage, result.ErrorCode, result.Retryable = classifyUpstreamSyncFailure(saveErr, "persist")
				return nil, result, saveErr
			}
		}
	}
	s.resolveMaskedSnapshotKeys(ctx, cfg, snapshot)
	if cfg.Provider == UpstreamProviderSub2API {
		applySub2APIBillingRates(ctx, cfg, proxyURL, snapshot)
		if err := s.preserveMissingProviderRates(ctx, cfg, snapshot); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
		s.mergeSub2APIImagePricingSnapshots(ctx, cfg, snapshot)
	} else if cfg.Provider == UpstreamProviderLCodex {
		if err := s.preserveMissingProviderRates(ctx, cfg, snapshot); err != nil {
			result.Error = adapter.SanitizeError(err, cfg.Credentials)
			return nil, result, err
		}
		s.mergeLCodexImageCapabilitySnapshots(ctx, cfg, snapshot)
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
		created, reconcileErr := s.reconcileUpstreamAccounts(ctx, cfg, localKeys)
		if reconcileErr != nil {
			result.Error = adapter.SanitizeError(reconcileErr, cfg.Credentials)
			return nil, result, reconcileErr
		}
		result.UpdatedAccountCount += created
		result.MissingKeyCount = reconciled.Missing
		result.StaleKeyCount = reconciled.Stale
		result.DeletedKeyCount = reconciled.Deleted
		result.RestoredKeyCount = reconciled.Restored
		result.ArchivedAccountCount = reconciled.ArchivedAccountCount
		result.RestoredAccountCount = reconciled.RestoredAccountCount
		result.Success = true
		result.Status = UpstreamSyncStatusSucceeded
		if snapshot.Partial || len(snapshot.Warnings) > 0 || snapshot.UnresolvedKeyCount > 0 {
			result.Status = UpstreamSyncStatusPartial
		}
		cfg.LastError = nil
		hydrateUpstreamKeysImagePricing(localKeys, cfg)
		modelStats := s.syncManagedUpstreamAccountModels(ctx, localKeys, forceModelSync)
		result.ModelSyncAttemptedCount = modelStats.Attempted
		result.ModelSyncSucceededCount = modelStats.Updated
		result.ModelSyncFailedCount = modelStats.Failed
		result.ModelSyncSkippedCount = modelStats.Skipped
		if modelStats.Failed > 0 {
			result.Status = UpstreamSyncStatusPartial
			result.Warnings = append(result.Warnings, "some upstream account model lists could not be refreshed")
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
	created, reconcileErr := s.reconcileUpstreamAccounts(ctx, cfg, localKeys)
	if reconcileErr != nil {
		result.Error = adapter.SanitizeError(reconcileErr, cfg.Credentials)
		return nil, result, reconcileErr
	}
	result.UpdatedAccountCount += created
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
	cfg.LastError = nil
	hydrateUpstreamKeysImagePricing(localKeys, cfg)
	modelStats := s.syncManagedUpstreamAccountModels(ctx, localKeys, forceModelSync)
	result.ModelSyncAttemptedCount = modelStats.Attempted
	result.ModelSyncSucceededCount = modelStats.Updated
	result.ModelSyncFailedCount = modelStats.Failed
	result.ModelSyncSkippedCount = modelStats.Skipped
	if modelStats.Failed > 0 {
		result.Status = UpstreamSyncStatusPartial
		result.Warnings = append(result.Warnings, "some upstream account model lists could not be refreshed")
	}
	return localKeys, result, nil
}

// reconcileUpstreamAccounts owns the one-key/one-derived-account lifecycle.
// Existing bound accounts are synchronized by the repository snapshot path;
// this function only fills missing accounts and deliberately leaves conflicts
// untouched for administrator repair.
func (s *UpstreamConfigService) reconcileUpstreamAccounts(ctx context.Context, cfg *UpstreamConfig, keys []UpstreamKey) (int, error) {
	lister, ok := s.accountRepo.(upstreamAccountBindingLister)
	if !ok || cfg == nil {
		return 0, nil
	}
	created := 0
	for i := range keys {
		key := &keys[i]
		if key.ID <= 0 || !upstreamKeyIsActive(key) || key.Platform == nil || strings.TrimSpace(*key.Platform) == "" || key.RateMultiplier == nil {
			continue
		}
		accounts, err := lister.ListByUpstreamKeyID(ctx, key.ID)
		if err != nil {
			return created, err
		}
		if len(accounts) != 0 {
			// len>1 is intentionally not repaired here; migration 233 and the UI
			// conflict state preserve the evidence for explicit administration.
			continue
		}
		name, err := BuildUpstreamAccountName(cfg.Name, key.Name)
		if err != nil {
			return created, err
		}
		platform := strings.ToLower(strings.TrimSpace(*key.Platform))
		const defaultUpstreamAccountConcurrency = 100
		concurrency := normalizeAccountConcurrency(platform, AccountTypeAPIKey, defaultUpstreamAccountConcurrency)
		priority := Sub2APIUpstreamPriority(*key.RateMultiplier)
		configID, keyID, rate := cfg.ID, key.ID, *key.RateMultiplier
		accountExtra := map[string]any{AccountUpstreamProviderKey: cfg.Provider, AccountSub2APIRateSyncAdapterKey: cfg.AuthMode}
		copyUpstreamModelSyncSourceMetadata(accountExtra, key.Extra)
		if description, ok := upstreamKeyDescription(key.Extra); ok {
			accountExtra[AccountUpstreamKeyDescriptionExtraKey] = description
			accountExtra[AccountUpstreamKeyDescriptionSyncedExtraKey] = true
		}
		credentials := map[string]any{"pool_mode": true}
		if key.BaseURL != nil && strings.TrimSpace(*key.BaseURL) != "" {
			credentials["base_url"] = strings.TrimSpace(*key.BaseURL)
		} else {
			credentials["base_url"] = cfg.EffectiveAPIURL()
		}
		account := &Account{
			Name: name, Platform: platform, Type: AccountTypeAPIKey,
			Credentials:      credentials,
			Extra:            accountExtra,
			UpstreamConfigID: &configID, UpstreamKeyID: &keyID,
			UpstreamLifecycleOwner: AccountUpstreamLifecycleOwnerSyncManaged,
			Concurrency:            concurrency, Priority: priority, RateMultiplier: &rate,
			Status: StatusActive, Schedulable: false,
		}
		if err := s.accountRepo.Create(ctx, account); err != nil {
			return created, err
		}
		GlobalUpstreamHealthRegistry().SetObservation(key.ID, key.ObservationEnabled, time.Now())
		created++
	}
	return created, nil
}

func (s *UpstreamConfigService) mergeSub2APIImagePricingSnapshots(ctx context.Context, cfg *UpstreamConfig, snapshot *upstreamProviderSnapshot) {
	if cfg == nil || snapshot == nil || len(snapshot.Keys) == 0 {
		return
	}
	remoteIDs := make([]int64, 0, len(snapshot.Keys))
	for i := range snapshot.Keys {
		if snapshot.Keys[i].RemoteKeyID != nil {
			remoteIDs = append(remoteIDs, *snapshot.Keys[i].RemoteKeyID)
		}
	}
	var (
		existing []UpstreamKey
		err      error
	)
	if fallbackRepo, ok := s.repo.(upstreamMaskedKeyFallbackRepository); ok {
		existing, err = fallbackRepo.ListKeysForMaskedFallback(ctx, cfg.ID, remoteIDs)
	} else {
		existing, err = s.repo.ListKeys(ctx, cfg.ID)
	}
	if err != nil {
		existing = nil
	}
	byRemoteID := make(map[int64]UpstreamKey, len(existing))
	for i := range existing {
		if existing[i].RemoteKeyID != nil {
			byRemoteID[*existing[i].RemoteKeyID] = existing[i]
		}
	}
	for i := range snapshot.Keys {
		key := &snapshot.Keys[i]
		incomingSnapshot, hasIncoming := parseSub2APIImagePricingSnapshot(key.Extra)
		mergedExtra := make(map[string]any, len(key.Extra)+1)
		var previousSnapshot sub2APIImagePricingSnapshot
		var hasPrevious bool
		if key.RemoteKeyID != nil {
			if previous, ok := byRemoteID[*key.RemoteKeyID]; ok {
				previousSnapshot, hasPrevious = parseSub2APIImagePricingSnapshot(previous.Extra)
			}
		}
		for extraKey, value := range key.Extra {
			mergedExtra[extraKey] = value
		}
		if hasIncoming && incomingSnapshot.Status != UpstreamKeyImagePricingStatusUnavailable {
			mergedExtra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(incomingSnapshot)
		} else if hasPrevious && previousSnapshot.Status != UpstreamKeyImagePricingStatusUnavailable {
			previousSnapshot.Stale = true
			mergedExtra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(previousSnapshot)
		} else if hasIncoming {
			incomingSnapshot.Stale = false
			mergedExtra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(incomingSnapshot)
		} else {
			mergedExtra[Sub2APIImagePricingSnapshotExtraKey] = sub2APIImagePricingSnapshotMap(newUnavailableSub2APIImagePricingSnapshot(false))
		}
		key.Extra = mergedExtra
	}
}

func upstreamKeyDescription(extra map[string]any) (string, bool) {
	if extra == nil {
		return "", false
	}
	for _, key := range []string{AccountUpstreamKeyDescriptionExtraKey, "description", "desc", "remark"} {
		if value, ok := extra[key].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" && len([]rune(value)) <= 512 {
				return value, true
			}
		}
	}
	return "", false
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

func (s *UpstreamConfigService) preserveMissingProviderRates(ctx context.Context, cfg *UpstreamConfig, snapshot *upstreamProviderSnapshot) error {
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
		return fmt.Errorf("load previous %s source rates: %w", normalizeUpstreamProvider(cfg.Provider), err)
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
			}, UpstreamConfigListFilter{Provider: provider, Status: StatusActive})
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
			if normalized == config.SiteURL && config.Provider != UpstreamProviderLCodex {
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
	case UpstreamProviderLCodex:
		valid = authMode == UpstreamAuthModeUserLogin
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
	if key.BaseURL != nil {
		if strings.TrimSpace(*key.BaseURL) == "" {
			key.BaseURL = nil
		} else {
			normalized, err := normalizeUpstreamConfigURL(*key.BaseURL)
			if err != nil {
				return infraerrors.BadRequest("UPSTREAM_KEY_BASE_URL_INVALID", "base_url is invalid")
			}
			key.BaseURL = &normalized
		}
	}
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
	return IsConcreteRequestPlatform(platform)
}

func normalizeUpstreamProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case UpstreamProviderSub2API:
		return UpstreamProviderSub2API
	case UpstreamProviderNewAPI:
		return UpstreamProviderNewAPI
	case UpstreamProviderLCodex:
		return UpstreamProviderLCodex
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
	case UpstreamProviderLCodex:
		return lcodexUpstreamProviderAdapter{}, true
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
		Keys:               snapshot.Keys,
		KeysComplete:       snapshot.KeysComplete,
		UnresolvedKeyCount: snapshot.UnresolvedKeyCount,
		RefreshedTokens:    snapshot.RefreshedTokens,
		Warnings:           append([]string(nil), snapshot.Warnings...),
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
