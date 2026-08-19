package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	AccountUpstreamModelSyncExtraKey             = "upstream_model_sync"
	AccountUpstreamModelSyncForceRefreshExtraKey = "upstream_model_sync_force_refresh"
	AccountNewAPIModelLimitsEnabledExtraKey      = "newapi_model_limits_enabled"
	AccountNewAPIModelLimitsExtraKey             = "newapi_model_limits"
	AccountNewAPIModelLimitsInvalidExtraKey      = "newapi_model_limits_invalid"

	UpstreamModelSyncStatusAvailable   = "available"
	UpstreamModelSyncStatusStale       = "stale"
	UpstreamModelSyncStatusError       = "error"
	UpstreamModelSyncStatusUnsupported = "unsupported"

	UpstreamModelSyncSourceNewAPI = "provider_model_limits"
	UpstreamModelSyncSourceLive   = "live_models"

	UpstreamModelSyncConcurrency = 4
)

const (
	UpstreamModelSyncFreshDuration   = 30 * time.Minute
	UpstreamModelSyncEnforceDuration = 24 * time.Hour
	UpstreamModelSyncAccountTimeout  = 15 * time.Second
)

// UpstreamModelSyncState is deliberately safe to persist and expose. It does
// not contain upstream URLs, credentials, response bodies, or raw errors.
type UpstreamModelSyncState struct {
	Status        string
	Source        string
	LastAttemptAt *time.Time
	LastSuccessAt *time.Time
	FreshUntil    *time.Time
	EnforceUntil  *time.Time
	ModelCount    int
	FailureKind   string
	ErrorCode     string
	Checksum      string
	AutoMapping   map[string]string
}

type upstreamModelSyncPersister interface {
	PersistUpstreamModelSync(ctx context.Context, accountID int64, mapping map[string]string, state map[string]any) error
}

type UpstreamModelSyncResult struct {
	Models    []string
	Updated   bool
	Skipped   bool
	Attempted bool
}

type UpstreamModelSyncStats struct {
	Attempted int
	Updated   int
	Failed    int
	Skipped   int
}

func (a *Account) upstreamModelSyncState() UpstreamModelSyncState {
	var state UpstreamModelSyncState
	if a == nil || a.Extra == nil {
		return state
	}
	raw, ok := a.Extra[AccountUpstreamModelSyncExtraKey]
	if !ok {
		return state
	}
	value := jsonObject(raw)
	state.Status = upstreamSyncStringValue(value["status"])
	state.Source = upstreamSyncStringValue(value["source"])
	state.LastAttemptAt = timeValue(value["last_attempt_at"])
	state.LastSuccessAt = timeValue(value["last_success_at"])
	state.FreshUntil = timeValue(value["fresh_until"])
	state.EnforceUntil = timeValue(value["enforce_until"])
	state.ModelCount = intValue(value["model_count"])
	state.FailureKind = upstreamSyncStringValue(value["failure_kind"])
	state.ErrorCode = upstreamSyncStringValue(value["error_code"])
	state.Checksum = upstreamSyncStringValue(value["checksum"])
	state.AutoMapping = stringMappingValue(value["auto_mapping"])
	return state
}

func (a *Account) upstreamModelSyncFresh(now time.Time) bool {
	state := a.upstreamModelSyncState()
	if state.Status != UpstreamModelSyncStatusAvailable {
		return false
	}
	if state.FreshUntil != nil {
		return !now.After(*state.FreshUntil)
	}
	return state.LastSuccessAt != nil && !now.After(state.LastSuccessAt.Add(UpstreamModelSyncFreshDuration))
}

func (a *Account) automaticModelMappingEnforced(now time.Time) bool {
	if a == nil || a.UpstreamLifecycleOwner != AccountUpstreamLifecycleOwnerSyncManaged {
		return true
	}
	state := a.upstreamModelSyncState()
	if state.EnforceUntil != nil {
		return !now.After(*state.EnforceUntil)
	}
	return state.LastSuccessAt != nil && !now.After(state.LastSuccessAt.Add(UpstreamModelSyncEnforceDuration))
}

func (a *Account) schedulableModelMapping(now time.Time) map[string]string {
	if !a.automaticModelMappingEnforced(now) {
		return nil
	}
	return a.GetModelMapping()
}

func (s *AccountTestService) SyncUpstreamAccountModels(ctx context.Context, account *Account, key *UpstreamKey, force bool) (UpstreamModelSyncResult, error) {
	if s == nil {
		return UpstreamModelSyncResult{}, newUpstreamModelSyncConfigError("Account test service is not configured", nil)
	}
	if account == nil {
		return UpstreamModelSyncResult{}, newUpstreamModelSyncConfigError("Account is required", nil)
	}
	if account.ID > 0 {
		flightKey := fmt.Sprintf("account:%d:force:%t", account.ID, force)
		value, err, _ := s.upstreamModelSyncSF.Do(flightKey, func() (any, error) {
			unlock := s.lockUpstreamModelSyncAccount(account.ID)
			defer unlock()
			return s.syncUpstreamAccountModelsOnce(ctx, account, key, force)
		})
		if value == nil {
			return UpstreamModelSyncResult{}, err
		}
		return value.(UpstreamModelSyncResult), err
	}
	return s.syncUpstreamAccountModelsOnce(ctx, account, key, force)
}

func (s *AccountTestService) lockUpstreamModelSyncAccount(accountID int64) func() {
	value, _ := s.upstreamModelSyncLocks.LoadOrStore(accountID, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *AccountTestService) syncUpstreamAccountModelsOnce(ctx context.Context, account *Account, key *UpstreamKey, force bool) (UpstreamModelSyncResult, error) {
	managed := account.UpstreamLifecycleOwner == AccountUpstreamLifecycleOwnerSyncManaged
	if managed && !force && account.upstreamModelSyncFresh(time.Now().UTC()) && !boolValue(account.Extra[AccountUpstreamModelSyncForceRefreshExtraKey]) {
		return UpstreamModelSyncResult{Models: sortedMappingKeys(account.GetModelMapping()), Skipped: true}, nil
	}

	attemptedAt := time.Now().UTC()
	requestCtx, cancel := context.WithTimeout(ctx, UpstreamModelSyncAccountTimeout)
	defer cancel()
	models, source, err := s.fetchModelsForUpstreamAccount(requestCtx, account, key)
	if err == nil {
		models = dedupeAndSortModelIDs(models)
		if len(models) == 0 {
			err = newUpstreamModelSyncUpstreamError("Upstream returned no supported models", nil)
		}
	}
	if !managed {
		return UpstreamModelSyncResult{Models: models, Attempted: true}, err
	}

	persister, ok := s.accountRepo.(upstreamModelSyncPersister)
	if !ok {
		return UpstreamModelSyncResult{Attempted: true}, newUpstreamModelSyncConfigError("Account repository cannot persist upstream model sync", nil)
	}
	previous := account.upstreamModelSyncState()
	if err != nil {
		failureKind := upstreamModelSyncFailureKind(err)
		status := UpstreamModelSyncStatusError
		if failureKind == string(UpstreamModelSyncErrorUnsupported) {
			status = UpstreamModelSyncStatusUnsupported
		} else if previous.LastSuccessAt != nil && (previous.EnforceUntil == nil || !attemptedAt.After(*previous.EnforceUntil)) {
			status = UpstreamModelSyncStatusStale
		}
		state := upstreamModelSyncStateMap(status, source, attemptedAt, previous, previous.ModelCount, previous.Checksum, failureKind, upstreamModelSyncErrorCode(err))
		if previous.AutoMapping != nil {
			state["auto_mapping"] = previous.AutoMapping
		}
		if persistErr := persister.PersistUpstreamModelSync(ctx, account.ID, nil, state); persistErr != nil {
			return UpstreamModelSyncResult{Attempted: true}, errors.Join(err, persistErr)
		}
		return UpstreamModelSyncResult{Attempted: true}, err
	}

	aliases := map[string]string{}
	if s.settingService != nil {
		var aliasErr error
		aliases, aliasErr = s.settingService.GetUpstreamModelAliasRules(ctx)
		if aliasErr != nil {
			return UpstreamModelSyncResult{Models: models, Attempted: true}, newUpstreamModelSyncConfigError("Invalid upstream model alias rules", aliasErr)
		}
	}
	mapping, autoMapping, mergeErr := MergeUpstreamModelMappings(models, aliases, account.GetModelMapping(), previous.AutoMapping)
	if mergeErr != nil {
		return UpstreamModelSyncResult{Models: models, Attempted: true}, newUpstreamModelSyncConfigError("Invalid upstream model alias rules", mergeErr)
	}
	state := upstreamModelSyncStateMap(UpstreamModelSyncStatusAvailable, source, attemptedAt, UpstreamModelSyncState{}, len(models), upstreamModelChecksum(models), "", "")
	if autoMapping != nil {
		state["auto_mapping"] = autoMapping
	}
	if err := persister.PersistUpstreamModelSync(ctx, account.ID, mapping, state); err != nil {
		return UpstreamModelSyncResult{Models: models, Attempted: true}, err
	}
	return UpstreamModelSyncResult{Models: models, Updated: true, Attempted: true}, nil
}

func (s *AccountTestService) fetchModelsForUpstreamAccount(ctx context.Context, account *Account, key *UpstreamKey) ([]string, string, error) {
	extra := account.Extra
	if key != nil && key.Extra != nil {
		extra = key.Extra
	}
	if account.UpstreamProvider() == AccountUpstreamProviderNewAPI || boolValue(extra[AccountNewAPIModelLimitsEnabledExtraKey]) {
		if boolValue(extra[AccountNewAPIModelLimitsEnabledExtraKey]) {
			models, ok := stringSliceValue(extra[AccountNewAPIModelLimitsExtraKey])
			if !ok || len(models) == 0 || boolValue(extra[AccountNewAPIModelLimitsInvalidExtraKey]) {
				return nil, UpstreamModelSyncSourceNewAPI, newUpstreamModelSyncUpstreamError("NewAPI model limits are enabled but invalid", nil)
			}
			return models, UpstreamModelSyncSourceNewAPI, nil
		}
	}
	models, err := s.FetchUpstreamSupportedModels(ctx, account)
	return models, UpstreamModelSyncSourceLive, err
}

func (s *UpstreamConfigService) syncManagedUpstreamAccountModels(ctx context.Context, keys []UpstreamKey, force bool) UpstreamModelSyncStats {
	stats := UpstreamModelSyncStats{}
	if s == nil || s.accountTestService == nil || s.accountRepo == nil {
		return stats
	}
	lister, ok := s.accountRepo.(upstreamAccountBindingLister)
	if !ok {
		return stats
	}
	type item struct {
		account Account
		key     UpstreamKey
		force   bool
	}
	items := make([]item, 0, len(keys))
	seen := make(map[int64]struct{})
	for i := range keys {
		key := keys[i]
		if key.ID <= 0 || !upstreamKeyIsActive(&key) {
			continue
		}
		accounts, err := lister.ListByUpstreamKeyID(ctx, key.ID)
		if err != nil {
			stats.Failed++
			continue
		}
		for j := range accounts {
			account := accounts[j]
			if account.ID <= 0 || account.UpstreamLifecycleOwner != AccountUpstreamLifecycleOwnerSyncManaged {
				stats.Skipped++
				continue
			}
			if _, duplicate := seen[account.ID]; duplicate {
				continue
			}
			seen[account.ID] = struct{}{}
			items = append(items, item{account: account, key: key, force: force || boolValue(account.Extra[AccountUpstreamModelSyncForceRefreshExtraKey])})
		}
	}
	if len(items) == 0 {
		return stats
	}

	jobs := make(chan item)
	results := make(chan UpstreamModelSyncResult, len(items))
	errs := make(chan error, len(items))
	workers := UpstreamModelSyncConcurrency
	if len(items) < workers {
		workers = len(items)
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for current := range jobs {
				result, err := s.accountTestService.SyncUpstreamAccountModels(ctx, &current.account, &current.key, current.force)
				results <- result
				errs <- err
			}
		}()
	}
	go func() {
		for _, current := range items {
			jobs <- current
		}
		close(jobs)
		wg.Wait()
		close(results)
		close(errs)
	}()

	for result := range results {
		if result.Attempted {
			stats.Attempted++
		}
		if result.Updated {
			stats.Updated++
		}
		if result.Skipped {
			stats.Skipped++
		}
	}
	for err := range errs {
		if err != nil {
			stats.Failed++
		}
	}
	return stats
}

func identityModelMapping(models []string) map[string]string {
	models = dedupeAndSortModelIDs(models)
	if len(models) == 0 {
		return nil
	}
	mapping := make(map[string]string, len(models))
	for _, model := range models {
		mapping[model] = model
	}
	return mapping
}

func upstreamModelSyncStateMap(status, source string, attemptedAt time.Time, previous UpstreamModelSyncState, modelCount int, checksum, failureKind, errorCode string) map[string]any {
	state := map[string]any{
		"status":          status,
		"source":          source,
		"last_attempt_at": attemptedAt.UTC().Format(time.RFC3339),
		"model_count":     modelCount,
	}
	if previous.LastSuccessAt != nil {
		state["last_success_at"] = previous.LastSuccessAt.UTC().Format(time.RFC3339)
	}
	if status == UpstreamModelSyncStatusAvailable {
		freshUntil := attemptedAt.Add(UpstreamModelSyncFreshDuration)
		enforceUntil := attemptedAt.Add(UpstreamModelSyncEnforceDuration)
		state["last_success_at"] = attemptedAt.UTC().Format(time.RFC3339)
		state["fresh_until"] = freshUntil.Format(time.RFC3339)
		state["enforce_until"] = enforceUntil.Format(time.RFC3339)
	} else {
		if previous.FreshUntil != nil {
			state["fresh_until"] = previous.FreshUntil.UTC().Format(time.RFC3339)
		}
		if previous.EnforceUntil != nil {
			state["enforce_until"] = previous.EnforceUntil.UTC().Format(time.RFC3339)
		}
	}
	if failureKind != "" {
		state["failure_kind"] = failureKind
	}
	if errorCode != "" {
		state["error_code"] = errorCode
	}
	if checksum != "" {
		state["checksum"] = checksum
	}
	return state
}

func upstreamModelSyncErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	var syncErr *UpstreamModelSyncError
	if errors.As(err, &syncErr) {
		switch syncErr.Kind {
		case UpstreamModelSyncErrorConfiguration:
			return "configuration"
		case UpstreamModelSyncErrorUnsupported:
			return "endpoint_unsupported"
		}
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"):
		return "timeout"
	case strings.Contains(lower, "401"), strings.Contains(lower, "403"), strings.Contains(lower, "unauthorized"), strings.Contains(lower, "forbidden"):
		return "auth_failed"
	case strings.Contains(lower, "invalid response"), strings.Contains(lower, "decode"), strings.Contains(lower, "json"), strings.Contains(lower, "returned no supported models"), strings.Contains(lower, "response too large"):
		return "invalid_response"
	default:
		return "upstream_error"
	}
}

func upstreamModelChecksum(models []string) string {
	models = dedupeAndSortModelIDs(models)
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\n", strings.Join(models, "\n"))))
	return fmt.Sprintf("%x", h[:])
}

func upstreamModelSyncFailureKind(err error) string {
	var syncErr *UpstreamModelSyncError
	if errors.As(err, &syncErr) {
		return string(syncErr.Kind)
	}
	return string(UpstreamModelSyncErrorUpstream)
}

func sortedMappingKeys(mapping map[string]string) []string {
	keys := make([]string, 0, len(mapping))
	for key := range mapping {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func jsonObject(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func upstreamSyncStringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

func timeValue(value any) *time.Time {
	text := upstreamSyncStringValue(value)
	if text == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil
	}
	return &parsed
}

func stringSliceValue(value any) ([]string, bool) {
	var values []string
	switch typed := value.(type) {
	case []string:
		values = append(values, typed...)
	case []any:
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			values = append(values, text)
		}
	default:
		return nil, false
	}
	values = dedupeAndSortModelIDs(values)
	return values, len(values) > 0
}

func stringMappingValue(value any) map[string]string {
	result := make(map[string]string)
	switch typed := value.(type) {
	case map[string]string:
		for key, item := range typed {
			if strings.TrimSpace(key) != "" && strings.TrimSpace(item) != "" {
				result[strings.TrimSpace(key)] = strings.TrimSpace(item)
			}
		}
	case map[string]any:
		for key, raw := range typed {
			item, ok := raw.(string)
			if ok && strings.TrimSpace(key) != "" && strings.TrimSpace(item) != "" {
				result[strings.TrimSpace(key)] = strings.TrimSpace(item)
			}
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func copyUpstreamModelSyncSourceMetadata(dst, src map[string]any) {
	if dst == nil || src == nil {
		return
	}
	for _, key := range []string{
		AccountNewAPIModelLimitsEnabledExtraKey,
		AccountNewAPIModelLimitsExtraKey,
		AccountNewAPIModelLimitsInvalidExtraKey,
	} {
		if value, ok := src[key]; ok {
			dst[key] = value
		} else {
			delete(dst, key)
		}
	}
}
