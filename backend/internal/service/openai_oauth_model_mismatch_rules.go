package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	MaxOpenAIOAuthModelMismatchRules = 1000
	MaxOpenAIOAuthModelMismatchName  = 120
	OpenAIOAuthModelSyncExtraKey     = "openai_oauth_model_sync"
)

type OpenAIOAuthModelSyncSnapshot struct {
	Status    string   `json:"status"`
	Models    []string `json:"models"`
	UpdatedAt string   `json:"updated_at"`
	Error     string   `json:"error,omitempty"`
}

var openAIOAuthModelDisableLocks sync.Map

type OpenAIOAuthModelMismatchRule struct {
	Source string `json:"source"`
	Target string `json:"target"`
}

func NormalizeOpenAIOAuthModelMismatchRules(rules []OpenAIOAuthModelMismatchRule) ([]OpenAIOAuthModelMismatchRule, error) {
	if len(rules) > MaxOpenAIOAuthModelMismatchRules {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_MODEL_MISMATCH_RULES_TOO_MANY", fmt.Sprintf("rules cannot contain more than %d entries", MaxOpenAIOAuthModelMismatchRules))
	}
	seen := make(map[string]struct{}, len(rules))
	normalized := make([]OpenAIOAuthModelMismatchRule, 0, len(rules))
	for _, rule := range rules {
		rule.Source = strings.TrimSpace(rule.Source)
		rule.Target = strings.TrimSpace(rule.Target)
		if rule.Source == "" || rule.Target == "" {
			return nil, infraerrors.BadRequest("OPENAI_OAUTH_MODEL_MISMATCH_RULE_INVALID", "source and target are required")
		}
		if len([]rune(rule.Source)) > MaxOpenAIOAuthModelMismatchName || len([]rune(rule.Target)) > MaxOpenAIOAuthModelMismatchName {
			return nil, infraerrors.BadRequest("OPENAI_OAUTH_MODEL_MISMATCH_RULE_TOO_LONG", fmt.Sprintf("model names cannot exceed %d characters", MaxOpenAIOAuthModelMismatchName))
		}
		key := strings.ToLower(rule.Source)
		if _, ok := seen[key]; ok {
			return nil, infraerrors.BadRequest("OPENAI_OAUTH_MODEL_MISMATCH_RULE_DUPLICATE", "source must be unique")
		}
		seen[key] = struct{}{}
		normalized = append(normalized, rule)
	}
	return normalized, nil
}

func (s *SettingService) GetOpenAIOAuthModelMismatchRules(ctx context.Context) ([]OpenAIOAuthModelMismatchRule, error) {
	if s == nil || s.settingRepo == nil {
		return []OpenAIOAuthModelMismatchRule{}, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpenAIOAuthModelMismatchDisableRules)
	if err != nil {
		if errors.Is(err, ErrSettingNotFound) {
			return []OpenAIOAuthModelMismatchRule{}, nil
		}
		return nil, err
	}
	var rules []OpenAIOAuthModelMismatchRule
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("unmarshal OpenAI OAuth model mismatch rules: %w", err)
	}
	return NormalizeOpenAIOAuthModelMismatchRules(rules)
}

func (s *SettingService) SetOpenAIOAuthModelMismatchRules(ctx context.Context, rules []OpenAIOAuthModelMismatchRule) ([]OpenAIOAuthModelMismatchRule, error) {
	normalized, err := NormalizeOpenAIOAuthModelMismatchRules(rules)
	if err != nil {
		return nil, err
	}
	if s == nil || s.settingRepo == nil {
		return nil, infraerrors.ServiceUnavailable("OPENAI_OAUTH_MODEL_MISMATCH_RULES_UNAVAILABLE", "setting repository is unavailable")
	}
	raw, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	if err := s.settingRepo.Set(ctx, SettingKeyOpenAIOAuthModelMismatchDisableRules, string(raw)); err != nil {
		return nil, err
	}
	return normalized, nil
}

func MatchOpenAIOAuthModelMismatchRule(rules []OpenAIOAuthModelMismatchRule, source, target string) (OpenAIOAuthModelMismatchRule, bool) {
	source = strings.TrimSpace(source)
	target = strings.TrimSpace(target)
	if source == "" || target == "" || strings.EqualFold(source, target) {
		return OpenAIOAuthModelMismatchRule{}, false
	}
	for _, rule := range rules {
		if strings.EqualFold(strings.TrimSpace(rule.Source), source) && strings.EqualFold(strings.TrimSpace(rule.Target), target) {
			return rule, true
		}
	}
	return OpenAIOAuthModelMismatchRule{}, false
}

// MaybeAutoDisableOpenAIOAuthModel records an exact configured upstream
// response mapping. It is intentionally best-effort: billing/forwarding must
// never fail because an administrative model state update failed.
func MaybeAutoDisableOpenAIOAuthModel(ctx context.Context, settingService *SettingService, accountRepo AccountRepository, account *Account, requestedModel, sentModel, responseModel string, conflict bool) {
	maybeAutoDisableOpenAIOAuthModel(ctx, settingService, accountRepo, account, requestedModel, sentModel, responseModel, conflict)
}

// maybeAutoDisableOpenAIOAuthModel performs the idempotent persistence step
// and schedules the optional catalog refresh after the current response path
// has been allowed to complete.
func maybeAutoDisableOpenAIOAuthModel(ctx context.Context, settingService *SettingService, accountRepo AccountRepository, account *Account, requestedModel, sentModel, responseModel string, conflict bool) {
	if account == nil || !account.IsOpenAIOAuth() || accountRepo == nil || conflict {
		return
	}
	requestedModel = strings.TrimSpace(requestedModel)
	sentModel = strings.TrimSpace(sentModel)
	responseModel = strings.TrimSpace(responseModel)
	if requestedModel == "" || sentModel == "" || !strings.EqualFold(requestedModel, sentModel) || responseModel == "" {
		return
	}
	rules, err := settingService.GetOpenAIOAuthModelMismatchRules(ctx)
	if err != nil {
		slog.Warn("openai_oauth_model_mismatch_rules_load_failed", "account_id", account.ID, "error", err)
		return
	}
	rule, ok := MatchOpenAIOAuthModelMismatchRule(rules, requestedModel, responseModel)
	if !ok {
		return
	}
	lockValue, _ := openAIOAuthModelDisableLocks.LoadOrStore(account.ID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	current := account
	if fresh, err := accountRepo.GetByID(ctx, account.ID); err == nil && fresh != nil {
		current = fresh
	}
	if current.IsOpenAIOAuthModelAutoDisabled(requestedModel) {
		return
	}
	models := append([]string(nil), current.OpenAIOAuthAutoDisabledModels()...)
	models = append(models, requestedModel)
	if err := accountRepo.UpdateExtra(ctx, account.ID, map[string]any{OpenAIOAuthAutoDisabledModelsExtraKey: models}); err != nil {
		slog.Warn("openai_oauth_model_auto_disable_persist_failed", "account_id", account.ID, "requested_model", requestedModel, "response_model", responseModel, "error", err)
		return
	}
	if account.Extra == nil {
		account.Extra = map[string]any{}
	}
	account.Extra[OpenAIOAuthAutoDisabledModelsExtraKey] = models
	slog.Info("openai_oauth_model_auto_disabled", "account_id", account.ID, "requested_model", requestedModel, "response_model", responseModel, "matched_rule_source", rule.Source, "matched_rule_target", rule.Target, "model_sync_triggered", settingService != nil && settingService.openAIOAuthModelSyncSchedulerSnapshot() != nil)
	if scheduler := settingService.openAIOAuthModelSyncSchedulerSnapshot(); scheduler != nil {
		scheduler.ScheduleOpenAIOAuthModelSync(account.ID)
	}
}

func normalizeOpenAIOAuthAutoDisabledModels(raw any) []string {
	var values []any
	switch typed := raw.(type) {
	case []string:
		values = make([]any, len(typed))
		for i := range typed {
			values[i] = typed[i]
		}
	case []any:
		values = typed
	case nil:
		return []string{}
	default:
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		model, ok := value.(string)
		model = strings.TrimSpace(model)
		if !ok || model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, model)
	}
	return result
}

func validateOpenAIOAuthAutoDisabledModels(raw any) ([]string, error) {
	models := normalizeOpenAIOAuthAutoDisabledModels(raw)
	if raw != nil && models == nil {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_AUTO_DISABLED_MODELS_INVALID", "openai_oauth_auto_disabled_models must be an array of model names")
	}
	if len(models) > MaxOpenAIOAuthModelMismatchRules {
		return nil, infraerrors.BadRequest("OPENAI_OAUTH_AUTO_DISABLED_MODELS_TOO_MANY", fmt.Sprintf("openai_oauth_auto_disabled_models cannot contain more than %d entries", MaxOpenAIOAuthModelMismatchRules))
	}
	for _, model := range models {
		if len([]rune(model)) > MaxOpenAIOAuthModelMismatchName {
			return nil, infraerrors.BadRequest("OPENAI_OAUTH_AUTO_DISABLED_MODEL_TOO_LONG", fmt.Sprintf("model names cannot exceed %d characters", MaxOpenAIOAuthModelMismatchName))
		}
	}
	return models, nil
}

func normalizeOpenAIOAuthModelSyncSnapshot(raw any) *OpenAIOAuthModelSyncSnapshot {
	if raw == nil {
		return nil
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var snapshot OpenAIOAuthModelSyncSnapshot
	if err := json.Unmarshal(body, &snapshot); err != nil {
		return nil
	}
	snapshot.Status = strings.ToLower(strings.TrimSpace(snapshot.Status))
	seen := make(map[string]struct{}, len(snapshot.Models))
	models := make([]string, 0, len(snapshot.Models))
	for _, model := range snapshot.Models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		models = append(models, model)
	}
	snapshot.Models = models
	return &snapshot
}

func openAIOAuthModelSyncSnapshotForAccount(account *Account) *OpenAIOAuthModelSyncSnapshot {
	if account == nil || !account.IsOpenAIOAuth() || account.Extra == nil {
		return nil
	}
	return normalizeOpenAIOAuthModelSyncSnapshot(account.Extra[OpenAIOAuthModelSyncExtraKey])
}

func (a *Account) SetOpenAIOAuthModelSyncSnapshot(snapshot OpenAIOAuthModelSyncSnapshot) {
	if a == nil {
		return
	}
	if a.Extra == nil {
		a.Extra = make(map[string]any)
	}
	a.Extra[OpenAIOAuthModelSyncExtraKey] = snapshot
}

func openAIOAuthModelSyncSnapshotContains(snapshot *OpenAIOAuthModelSyncSnapshot, model string) bool {
	if snapshot == nil || snapshot.Status != "available" || len(snapshot.Models) == 0 {
		return false
	}
	for _, candidate := range snapshot.Models {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(model)) {
			return true
		}
	}
	return false
}

func newOpenAIOAuthModelSyncSnapshot(status string, models []string, err error) OpenAIOAuthModelSyncSnapshot {
	snapshot := OpenAIOAuthModelSyncSnapshot{Status: status, Models: append([]string(nil), models...), UpdatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err != nil {
		snapshot.Error = err.Error()
	}
	return snapshot
}
