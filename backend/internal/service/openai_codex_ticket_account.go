package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	apperrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const codexAccountTicketConfigKey = "codex_ticket_config"
const codexTicketMaxAttempts = 8
const codexTicketRetryCooldown = 5 * time.Minute

const (
	codexTicketPlanPro  = "pro"
	codexTicketPlanTeam = "team"
)

// This is a manual account setting, never inferred from subscription metadata.
func codexTicketTargetLength(plan string) int {
	switch plan {
	case "", codexTicketPlanPro:
		return 292
	case codexTicketPlanTeam:
		return 332
	default:
		return 0
	}
}

// This key is server managed and is never accepted through general account edits.
type codexAccountTicketConfig struct {
	Models   map[string]codexTicketModelConfig `json:"models,omitempty"`
	Revision string                            `json:"revision"`

	// Legacy projection retained for #7338 clients and persisted blobs. New
	// writes use Models; these fields are derived in memory when Models exists.
	TicketPlan string `json:"ticket_plan,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
	Model      string `json:"model,omitempty"`
	ProxyURL   string `json:"proxy_url,omitempty"`
}

type codexTicketModelConfig struct {
	TicketPlan string `json:"ticket_plan"`
	Enabled    bool   `json:"enabled"`
	Revision   string `json:"revision"`
}

type CodexTicketModelUpdate struct {
	TicketPlan string `json:"ticket_plan"`
	Enabled    bool   `json:"enabled"`
}

type CodexAccountTicketUpdate struct {
	Models     map[string]CodexTicketModelUpdate `json:"models,omitempty"`
	TicketPlan string                            `json:"ticket_plan"`
	Enabled    bool                              `json:"enabled"`
	ProxyURL   string                            `json:"proxy_url"`
	Model      string                            `json:"model"`
	ClearProxy bool                              `json:"clear_proxy"`
}

type CodexAccountTicketStatus struct {
	Models               map[string]CodexAccountTicketModelStatus `json:"models"`
	Watchdog             CodexTicketWatchdogStatus                `json:"watchdog"`
	TicketPlan           string                                   `json:"ticket_plan"`
	TargetLength         int                                      `json:"target_length"`
	Enabled              bool                                     `json:"enabled"`
	GlobalEnabled        bool                                     `json:"global_enabled"`
	Model                string                                   `json:"model"`
	ProxyConfigured      bool                                     `json:"proxy_configured"`
	ProxyDisplay         string                                   `json:"proxy_display"`
	FixedProxyConfigured bool                                     `json:"fixed_proxy_configured"`
	State                string                                   `json:"state"`
	TicketUsable         bool                                     `json:"ticket_usable"`
	Refreshing           bool                                     `json:"refreshing"`
	CapturedAt           *time.Time                               `json:"captured_at,omitempty"`
	RetryAfter           *time.Time                               `json:"retry_after,omitempty"`
	RemainingSeconds     int64                                    `json:"remaining_seconds"`
	ExpiresAt            *time.Time                               `json:"expires_at,omitempty"`
	LastError            string                                   `json:"last_error"`
	Attempts             int                                      `json:"attempts"`
}

type CodexAccountTicketSnapshotStatus struct {
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	IssuedAt         *time.Time `json:"issued_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	Version          uint64     `json:"version,omitempty"`
	Length           int        `json:"length,omitempty"`
}

type CodexAccountTicketModelStatus struct {
	Watchdog     CodexTicketWatchdogStatus         `json:"watchdog"`
	Model        string                            `json:"model"`
	TicketPlan   string                            `json:"ticket_plan"`
	TargetLength int                               `json:"target_length"`
	Enabled      bool                              `json:"enabled"`
	State        string                            `json:"state"`
	TicketUsable bool                              `json:"ticket_usable"`
	Refreshing   bool                              `json:"refreshing"`
	Strikes      int                               `json:"strikes"`
	Active       *CodexAccountTicketSnapshotStatus `json:"active,omitempty"`
	Ready        *CodexAccountTicketSnapshotStatus `json:"ready,omitempty"`
	RetryAfter   *time.Time                        `json:"retry_after,omitempty"`
	LastError    string                            `json:"last_error"`
	Attempts     int                               `json:"attempts"`
}

type codexAccountTicketJob struct {
	model            string
	revision         string
	fixedFingerprint string
	harvestProxyURL  string // Immutable global pool snapshot for this job; never returned to clients.
	cancel           context.CancelFunc
	done             chan struct{}
	running          bool
	attempts         int
	lastError        string
	retryAfter       time.Time
}

var codexTicketSupportedModels = []string{
	openAICodexTicketDefaultModel,
	openAICodexTicketDefaultSolModel,
	openAICodexTicketDefaultTerraModel,
}

func isSupportedCodexTicketModel(model string) bool {
	model = normalizeOpenAICodexTicketModel(model)
	for _, supported := range codexTicketSupportedModels {
		if model == supported {
			return true
		}
	}
	return false
}

func normalizeCodexTicketModelConfig(in codexTicketModelConfig, fallbackRevision string) codexTicketModelConfig {
	in.TicketPlan = strings.ToLower(strings.TrimSpace(in.TicketPlan))
	if in.TicketPlan == "" {
		in.TicketPlan = codexTicketPlanPro
	}
	if in.Revision == "" {
		in.Revision = fallbackRevision
	}
	if codexTicketTargetLength(in.TicketPlan) == 0 || in.Revision == "" {
		in.Enabled = false
	}
	return in
}

func (c codexAccountTicketConfig) modelConfig(model string) (codexTicketModelConfig, bool) {
	model = normalizeOpenAICodexTicketModel(model)
	if c.Models != nil {
		cfg, ok := c.Models[model]
		if !ok {
			return codexTicketModelConfig{}, false
		}
		return normalizeCodexTicketModelConfig(cfg, c.Revision), true
	}
	if c.Enabled && normalizeOpenAICodexTicketModel(c.Model) == model {
		return normalizeCodexTicketModelConfig(codexTicketModelConfig{Enabled: c.Enabled, TicketPlan: c.TicketPlan, Revision: c.Revision}, c.Revision), true
	}
	return codexTicketModelConfig{}, false
}

func (c codexAccountTicketConfig) enabledModels() []string {
	out := make([]string, 0, len(codexTicketSupportedModels))
	for _, model := range codexTicketSupportedModels {
		if cfg, ok := c.modelConfig(model); ok && cfg.Enabled {
			out = append(out, model)
		}
	}
	return out
}

func codexAccountTicketConfigOf(account *Account) codexAccountTicketConfig {
	out := codexAccountTicketConfig{Model: openAICodexTicketDefaultModel, TicketPlan: codexTicketPlanPro}
	if account == nil || account.Extra == nil {
		return out
	}
	raw, err := json.Marshal(account.Extra[codexAccountTicketConfigKey])
	if err != nil {
		return out
	}
	if err = json.Unmarshal(raw, &out); err != nil {
		return codexAccountTicketConfig{Model: openAICodexTicketDefaultModel, TicketPlan: codexTicketPlanPro}
	}
	if len(out.Models) > 0 {
		normalized := make(map[string]codexTicketModelConfig, len(out.Models))
		for model, cfg := range out.Models {
			model = normalizeOpenAICodexTicketModel(model)
			if !isSupportedCodexTicketModel(model) {
				continue
			}
			normalized[model] = normalizeCodexTicketModelConfig(cfg, out.Revision)
		}
		out.Models = normalized
		out.ProxyURL = ""
		for _, model := range codexTicketSupportedModels {
			if cfg, ok := out.Models[model]; ok {
				out.Model, out.TicketPlan, out.Enabled = model, cfg.TicketPlan, cfg.Enabled
				if cfg.Enabled {
					break
				}
			}
		}
		return out
	}
	if out.Model == "" {
		out.Model = openAICodexTicketDefaultModel
	}
	if out.TicketPlan == "" {
		out.TicketPlan = codexTicketPlanPro
	}
	if codexTicketTargetLength(out.TicketPlan) == 0 {
		out.Enabled = false
	}
	// An incomplete or imported legacy blob must never opt an account in.
	if out.Revision == "" {
		out.Enabled = false
	}
	return out
}

func codexAccountTicketEligible(account *Account) bool {
	return isOpenAICodexTicketAccount(account) && account.Status == StatusActive && account.Proxy != nil && account.ProxyID != nil
}

func codexTicketFixedProxyFingerprint(account *Account) string {
	if account == nil || account.Proxy == nil || account.ProxyID == nil {
		return ""
	}
	raw := fmt.Sprintf("%d\x00%d\x00%s\x00%v", account.ID, *account.ProxyID, account.Proxy.URL(), account.Credentials["chatgpt_account_id"])
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func (s *OpenAIGatewayService) codexTicketLiveAccount(ctx context.Context, account *Account) (*Account, error) {
	if account == nil {
		return nil, errors.New("account unavailable")
	}
	if s.accountRepo == nil {
		return account, nil
	}
	// Repository lookup prevents stale scheduler snapshots from re-enabling disabled tickets.
	live, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || live == nil {
		return nil, errors.New("account unavailable")
	}
	return live, nil
}
func (s *OpenAIGatewayService) codexTicketAccountByID(ctx context.Context, id int64) (*Account, error) {
	if s == nil || s.accountRepo == nil {
		return nil, apperrors.New(503, "CODEX_TICKET_UNAVAILABLE", "Ticket service is unavailable")
	}
	account, err := s.accountRepo.GetByID(ctx, id)
	if err != nil || account == nil {
		return nil, apperrors.New(404, "ACCOUNT_NOT_FOUND", "Account not found")
	}
	if !isOpenAICodexTicketAccount(account) {
		return nil, apperrors.BadRequest("CODEX_TICKET_ACCOUNT", "STATE tickets require a non-shadow OpenAI OAuth account")
	}
	return account, nil
}

func codexAccountTicketJobKey(id int64, model string) string {
	return fmt.Sprintf("%d\x00%s", id, normalizeOpenAICodexTicketModel(model))
}

func (s *OpenAIGatewayService) codexAccountTicketJobLocked(id int64, model string) *codexAccountTicketJob {
	if s.openaiCodexAccountModelJobs != nil {
		if job := s.openaiCodexAccountModelJobs[codexAccountTicketJobKey(id, model)]; job != nil {
			return job
		}
	}
	if normalizeOpenAICodexTicketModel(model) == openAICodexTicketDefaultModel && s.openaiCodexAccountJobs != nil {
		return s.openaiCodexAccountJobs[id]
	}
	return nil
}

func (s *OpenAIGatewayService) setCodexAccountTicketJobLocked(id int64, model string, job *codexAccountTicketJob) {
	if s.openaiCodexAccountModelJobs == nil {
		s.openaiCodexAccountModelJobs = make(map[string]*codexAccountTicketJob)
	}
	key := codexAccountTicketJobKey(id, model)
	if job == nil {
		delete(s.openaiCodexAccountModelJobs, key)
	} else {
		s.openaiCodexAccountModelJobs[key] = job
	}
	// Preserve the #7338 single-model field for old integrations and tests.
	if normalizeOpenAICodexTicketModel(model) == openAICodexTicketDefaultModel {
		if s.openaiCodexAccountJobs == nil {
			s.openaiCodexAccountJobs = make(map[int64]*codexAccountTicketJob)
		}
		if job == nil {
			delete(s.openaiCodexAccountJobs, id)
		} else {
			s.openaiCodexAccountJobs[id] = job
		}
	}
}

func codexTicketSnapshotStatus(ticket *openAICodexTicket, now time.Time) *CodexAccountTicketSnapshotStatus {
	if ticket == nil {
		return nil
	}
	issued, expires := ticket.IssuedAt, ticket.ExpiresAt
	remaining := int64(ticket.ExpiresAt.Sub(now) / time.Second)
	if remaining < 0 {
		remaining = 0
	}
	return &CodexAccountTicketSnapshotStatus{
		Ready:            true,
		RemainingSeconds: remaining,
		IssuedAt:         &issued,
		ExpiresAt:        &expires,
		Version:          ticket.Version,
		Length:           ticket.Length,
	}
}

func (s *OpenAIGatewayService) GetCodexAccountTicketStatus(ctx context.Context, id int64) (*CodexAccountTicketStatus, error) {
	account, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	ac := codexAccountTicketConfigOf(account)
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	poolConfigured := pool != "" && ValidateOpenAICodexTicketHarvestProxyURL(pool) == nil
	status := &CodexAccountTicketStatus{
		Models:               make(map[string]CodexAccountTicketModelStatus, len(codexTicketSupportedModels)),
		GlobalEnabled:        s.openAICodexTicketEnabledContext(ctx),
		ProxyConfigured:      poolConfigured,
		FixedProxyConfigured: account.Proxy != nil && account.ProxyID != nil,
		State:                "disabled",
	}
	if parsed, err := url.Parse(strings.ReplaceAll(pool, "{sid}", "%7Bsid%7D")); err == nil {
		status.ProxyDisplay = parsed.Host
	}
	now := time.Now()
	for _, model := range codexTicketSupportedModels {
		modelCfg, configured := ac.modelConfig(model)
		if !configured {
			modelCfg = codexTicketModelConfig{TicketPlan: codexTicketPlanPro}
		}
		modelStatus := CodexAccountTicketModelStatus{
			Model: model, TicketPlan: modelCfg.TicketPlan, TargetLength: codexTicketTargetLength(modelCfg.TicketPlan),
			Enabled: modelCfg.Enabled, State: "disabled",
		}
		modelStatus.Watchdog = codexTicketWatchdogStatusOfModel(account, model, modelCfg.Enabled && status.GlobalEnabled)
		if modelCfg.Enabled {
			modelStatus.State = "waiting"
			if !status.GlobalEnabled {
				modelStatus.State = "global_disabled"
			} else if !codexAccountTicketEligible(account) {
				modelStatus.State = "error"
				modelStatus.LastError = "Account must be active and have a fixed business proxy"
			} else {
				slot := s.lookupOpenAICodexTicketSlot(account, model)
				if slot != nil {
					modelStatus.Strikes = slot.Strikes
					modelStatus.Active = codexTicketSnapshotStatus(slot.Active, now)
					modelStatus.Ready = codexTicketSnapshotStatus(slot.Ready, now)
					modelStatus.TicketUsable = slot.Active != nil && slot.Active.validFor(account, ac, now)
					if modelStatus.TicketUsable {
						modelStatus.State = "ready"
					}
				}
				if !poolConfigured && !modelStatus.TicketUsable {
					modelStatus.State = "error"
					modelStatus.LastError = "Configure the global dynamic proxy pool in gateway settings"
				}
			}
		}
		s.openaiCodexAccountMu.Lock()
		if job := s.codexAccountTicketJobLocked(id, model); job != nil && job.revision == modelCfg.Revision && job.harvestProxyURL == pool && job.fixedFingerprint == codexTicketFixedProxyFingerprint(account) {
			modelStatus.Attempts = job.attempts
			modelStatus.LastError = job.lastError
			if job.running {
				modelStatus.State = "harvesting"
				modelStatus.Refreshing = modelStatus.TicketUsable
			} else if job.lastError != "" && modelStatus.State != "ready" {
				modelStatus.State = "error"
			}
			if !job.running && now.Before(job.retryAfter) {
				retryAfter := job.retryAfter
				modelStatus.RetryAfter = &retryAfter
			}
		}
		s.openaiCodexAccountMu.Unlock()
		status.Models[model] = modelStatus
	}
	// Populate the original #7338 fields from its selected/default model so old
	// admin clients keep working while new clients consume Models.
	legacyModel := ac.Model
	if legacyModel == "" {
		legacyModel = openAICodexTicketDefaultModel
	}
	legacy := status.Models[legacyModel]
	status.Model, status.TicketPlan, status.TargetLength = legacy.Model, legacy.TicketPlan, legacy.TargetLength
	status.Enabled, status.State, status.TicketUsable, status.Refreshing = legacy.Enabled, legacy.State, legacy.TicketUsable, legacy.Refreshing
	status.Watchdog, status.Attempts, status.LastError, status.RetryAfter = legacy.Watchdog, legacy.Attempts, legacy.LastError, legacy.RetryAfter
	if legacy.Active != nil {
		status.RemainingSeconds, status.ExpiresAt = legacy.Active.RemainingSeconds, legacy.Active.ExpiresAt
		if ticket := s.lookupOpenAICodexTicket(account, legacyModel); ticket != nil {
			captured := ticket.CapturedAt
			status.CapturedAt = &captured
		}
	}
	return status, nil
}

func (s *OpenAIGatewayService) ConfigureCodexAccountTicket(ctx context.Context, id int64, input CodexAccountTicketUpdate) (*CodexAccountTicketStatus, error) {
	s.openaiCodexAccountMu.Lock()
	account, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		s.openaiCodexAccountMu.Unlock()
		return nil, err
	}
	old := codexAccountTicketConfigOf(account)
	next := codexAccountTicketConfig{Models: make(map[string]codexTicketModelConfig), Revision: old.Revision}
	for _, model := range codexTicketSupportedModels {
		if cfg, ok := old.modelConfig(model); ok {
			next.Models[model] = cfg
		} else {
			next.Models[model] = codexTicketModelConfig{TicketPlan: codexTicketPlanPro}
		}
	}
	if len(input.Models) > 0 {
		for model := range input.Models {
			if !isSupportedCodexTicketModel(model) {
				s.openaiCodexAccountMu.Unlock()
				return nil, apperrors.BadRequest("CODEX_TICKET_MODEL", "STATE model must be gpt-6-astra, gpt-5.6-sol or gpt-5.6-terra")
			}
		}
		for _, model := range codexTicketSupportedModels {
			update, ok := input.Models[model]
			if !ok {
				next.Models[model] = codexTicketModelConfig{TicketPlan: codexTicketPlanPro}
				continue
			}
			plan := strings.ToLower(strings.TrimSpace(update.TicketPlan))
			if plan == "" {
				plan = codexTicketPlanPro
			}
			if codexTicketTargetLength(plan) == 0 {
				s.openaiCodexAccountMu.Unlock()
				return nil, apperrors.BadRequest("CODEX_TICKET_PLAN", "Ticket plan must be pro or team")
			}
			next.Models[model] = codexTicketModelConfig{Enabled: update.Enabled, TicketPlan: plan}
		}
	} else {
		model := normalizeOpenAICodexTicketModel(input.Model)
		if model == "" {
			model = old.Model
		}
		if model == "" {
			model = openAICodexTicketDefaultModel
		}
		if !isSupportedCodexTicketModel(model) {
			s.openaiCodexAccountMu.Unlock()
			return nil, apperrors.BadRequest("CODEX_TICKET_MODEL", "STATE model must be gpt-6-astra, gpt-5.6-sol or gpt-5.6-terra")
		}
		plan := strings.ToLower(strings.TrimSpace(input.TicketPlan))
		if input.TicketPlan != "" && plan == "" {
			s.openaiCodexAccountMu.Unlock()
			return nil, apperrors.BadRequest("CODEX_TICKET_PLAN", "Ticket plan must be pro or team")
		}
		if plan == "" {
			if cfg, ok := old.modelConfig(model); ok {
				plan = cfg.TicketPlan
			}
		}
		if plan == "" {
			plan = codexTicketPlanPro
		}
		if codexTicketTargetLength(plan) == 0 {
			s.openaiCodexAccountMu.Unlock()
			return nil, apperrors.BadRequest("CODEX_TICKET_PLAN", "Ticket plan must be pro or team")
		}
		next.Models[model] = codexTicketModelConfig{Enabled: input.Enabled, TicketPlan: plan}
	}
	if input.ClearProxy || strings.TrimSpace(input.ProxyURL) != "" {
		s.openaiCodexAccountMu.Unlock()
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_PROXY", "Configure the dynamic proxy pool in gateway settings, not per account")
	}
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	hasEnabled := false
	for _, cfg := range next.Models {
		hasEnabled = hasEnabled || cfg.Enabled
	}
	if hasEnabled && (pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil || account.Proxy == nil || account.ProxyID == nil) {
		s.openaiCodexAccountMu.Unlock()
		return nil, apperrors.BadRequest("CODEX_TICKET_PROXY_REQUIRED", "Configure the global dynamic proxy pool and this account's fixed business proxy first")
	}
	changedModels := make(map[string]bool)
	for _, model := range codexTicketSupportedModels {
		oldCfg, oldOK := old.modelConfig(model)
		newCfg := next.Models[model]
		changed := (oldOK && (oldCfg.Enabled != newCfg.Enabled || oldCfg.TicketPlan != newCfg.TicketPlan || oldCfg.Revision == "")) || (!oldOK && newCfg.Enabled)
		if changed {
			newCfg.Revision = uuid.NewString()
			changedModels[model] = true
		} else if oldOK {
			newCfg.Revision = oldCfg.Revision
		} else {
			newCfg.Revision = uuid.NewString()
		}
		next.Models[model] = newCfg
	}
	structuralUpgrade := old.Models == nil
	changed := len(changedModels) > 0 || structuralUpgrade || old.Revision == ""
	updates := map[string]any{}
	if changed {
		if len(changedModels) > 0 || old.Revision == "" {
			next.Revision = uuid.NewString()
		} else {
			next.Revision = old.Revision
		}
		next.Model = ""
		for _, model := range codexTicketSupportedModels {
			if next.Models[model].Enabled {
				next.Model, next.TicketPlan, next.Enabled = model, next.Models[model].TicketPlan, true
				break
			}
		}
		if next.Model == "" {
			next.Model, next.TicketPlan = openAICodexTicketDefaultModel, codexTicketPlanPro
		}
		updates[codexAccountTicketConfigKey] = next
		for model := range changedModels {
			updates[openAICodexTicketExtraKey(model)] = nil
			updates[codexTicketWatchdogExtraKeyForModel(model)] = nil
		}
		if err := s.accountRepo.UpdateExtra(ctx, id, updates); err != nil {
			s.openaiCodexAccountMu.Unlock()
			return nil, apperrors.New(500, "CODEX_TICKET_SAVE_FAILED", "Could not save account ticket settings")
		}
		for model := range changedModels {
			if job := s.codexAccountTicketJobLocked(id, model); job != nil && job.cancel != nil {
				job.cancel()
			}
			s.setCodexAccountTicketJobLocked(id, model, nil)
		}
		s.openaiCodexTickets.Range(func(key, value any) bool {
			for model := range changedModels {
				if key == openAICodexTicketKey(id, model) {
					s.openaiCodexTickets.Delete(key)
				}
			}
			return true
		})
	}
	s.openaiCodexAccountMu.Unlock()
	if changed {
		s.InvalidateAgentIdentityWSConnections(id)
	}
	if changed && s.openAICodexTicketEnabledContext(ctx) {
		for model := range changedModels {
			if next.Models[model].Enabled {
				s.startCodexAccountTicketModelJob(context.Background(), id, model, true)
			}
		}
	}
	return s.GetCodexAccountTicketStatus(ctx, id)
}

func (s *OpenAIGatewayService) HarvestCodexAccountTicket(ctx context.Context, id int64, requestedModel ...string) (*CodexAccountTicketStatus, error) {
	account, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_DISABLED", "Enable the gateway STATE master switch first")
	}
	ac := codexAccountTicketConfigOf(account)
	model := ""
	if len(requestedModel) > 0 {
		model = normalizeOpenAICodexTicketModel(requestedModel[0])
	}
	if model == "" {
		model = ac.Model
	}
	modelCfg, configured := ac.modelConfig(model)
	if !configured || !modelCfg.Enabled {
		return nil, apperrors.BadRequest("CODEX_TICKET_DISABLED", "Enable STATE tickets for the requested model first")
	}
	if !codexAccountTicketEligible(account) {
		return nil, apperrors.BadRequest("CODEX_TICKET_ACCOUNT_INACTIVE", "Account must be active and have a fixed business proxy")
	}
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil {
		return nil, apperrors.BadRequest("CODEX_TICKET_GLOBAL_PROXY", "Configure the global dynamic proxy pool in gateway settings")
	}
	s.startCodexAccountTicketModelJob(context.Background(), id, model, true)
	return s.GetCodexAccountTicketStatus(ctx, id)
}

func (s *OpenAIGatewayService) cancelCodexTicketJobsLocked() {
	seen := make(map[*codexAccountTicketJob]struct{})
	for _, job := range s.openaiCodexAccountModelJobs {
		seen[job] = struct{}{}
	}
	for _, job := range s.openaiCodexAccountJobs {
		seen[job] = struct{}{}
	}
	for job := range seen {
		if job.cancel != nil {
			job.cancel()
		}
	}
}
func (s *OpenAIGatewayService) cancelCodexTicketJobs() {
	s.openaiCodexAccountMu.Lock()
	defer s.openaiCodexAccountMu.Unlock()
	s.cancelCodexTicketJobsLocked()
}

func (s *OpenAIGatewayService) startCodexAccountTicketJob(ctx context.Context, id int64, manual bool) *codexAccountTicketJob {
	account, err := s.codexTicketAccountByID(ctx, id)
	if err != nil {
		return nil
	}
	model := codexAccountTicketConfigOf(account).Model
	if model == "" {
		model = openAICodexTicketDefaultModel
	}
	return s.startCodexAccountTicketModelJob(ctx, id, model, manual)
}

func (s *OpenAIGatewayService) startCodexAccountTicketModelJob(ctx context.Context, id int64, model string, manual bool) *codexAccountTicketJob {
	if s == nil || ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
		return nil
	}
	s.openaiCodexAccountMu.Lock()
	defer s.openaiCodexAccountMu.Unlock()
	if s.openaiCodexAccountStopping {
		return nil
	}
	account, err := s.codexTicketAccountByID(ctx, id)
	if err != nil || !codexAccountTicketEligible(account) {
		return nil
	}
	ac := codexAccountTicketConfigOf(account)
	model = normalizeOpenAICodexTicketModel(model)
	modelCfg, configured := ac.modelConfig(model)
	pool := s.openAICodexTicketHarvestProxyURLContext(ctx)
	if !configured || !modelCfg.Enabled || pool == "" || ValidateOpenAICodexTicketHarvestProxyURL(pool) != nil {
		return nil
	}
	if job := s.codexAccountTicketJobLocked(id, model); job != nil {
		if job.running && job.revision == modelCfg.Revision && job.fixedFingerprint == codexTicketFixedProxyFingerprint(account) && job.harvestProxyURL == pool {
			return job
		}
		if job.running && job.cancel != nil {
			job.cancel()
		}
		if !manual && job.revision == modelCfg.Revision && job.harvestProxyURL == pool && time.Now().Before(job.retryAfter) {
			return nil
		}
	}
	// Own a detached job context; the request that clicked Save may finish immediately.
	s.openaiCodexTicketLifecycleMu.Lock()
	parentCtx := s.openaiCodexTicketContext
	s.openaiCodexTicketLifecycleMu.Unlock()
	if parentCtx == nil {
		parentCtx = context.WithoutCancel(ctx)
	}
	jobCtx, cancel := context.WithCancel(parentCtx)
	job := &codexAccountTicketJob{model: model, revision: modelCfg.Revision, fixedFingerprint: codexTicketFixedProxyFingerprint(account), harvestProxyURL: pool, cancel: cancel, done: make(chan struct{}), running: true}
	s.setCodexAccountTicketJobLocked(id, model, job)
	s.openaiCodexAccountWG.Add(1)
	go func() {
		defer cancel()
		defer s.openaiCodexAccountWG.Done()
		defer close(job.done)
		s.runCodexAccountTicketJob(jobCtx, id, model, job)
	}()
	return job
}

func (s *OpenAIGatewayService) runCodexAccountTicketJob(ctx context.Context, id int64, model string, job *codexAccountTicketJob) {
	lastError := "Unable to obtain a verified STATE ticket"
	defer func() {
		s.openaiCodexAccountMu.Lock()
		defer s.openaiCodexAccountMu.Unlock()
		job.running = false
		job.retryAfter = time.Time{}
		if ctx.Err() == nil && lastError != "" {
			job.retryAfter = time.Now().Add(codexTicketRetryCooldown)
		}
		if ctx.Err() != nil {
			job.lastError = ""
		} else {
			job.lastError = lastError
		}
	}()
	timeout := time.Duration(s.openAICodexTicketConfig().HarvestAttemptTimeoutSeconds) * time.Second
	release, acquired := s.acquireCodexTicketHarvestLock(ctx, id, model, job.revision, job.fixedFingerprint)
	if !acquired {
		lastError = ""
		return
	}
	defer release()
	for attempt := 1; attempt <= codexTicketMaxAttempts; attempt++ {
		if ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) {
			return
		}
		account, err := s.codexTicketAccountByID(ctx, id)
		if err != nil {
			return
		}
		ac := codexAccountTicketConfigOf(account)
		modelCfg, configured := ac.modelConfig(model)
		if !codexAccountTicketEligible(account) || !configured || !modelCfg.Enabled || modelCfg.Revision != job.revision || codexTicketFixedProxyFingerprint(account) != job.fixedFingerprint || s.openAICodexTicketHarvestProxyURLContext(ctx) != job.harvestProxyURL {
			return
		}
		// Token helpers are permitted to update metadata, but not shared account maps.
		account.Extra = maps.Clone(account.Extra)
		account.Credentials = maps.Clone(account.Credentials)
		s.openaiCodexAccountMu.Lock()
		job.attempts = attempt
		s.openaiCodexAccountMu.Unlock()
		token, _, err := s.GetAccessToken(ctx, account)
		if err != nil || token == "" {
			lastError = "Account authentication failed"
			return
		}
		harvestProxy := freshCodexTicketProxyURL(job.harvestProxyURL)
		state, status, err := s.fireCodexAccountTicketProbe(ctx, account, token, model, harvestProxy, "", timeout)
		if reason := codexTicketProbeRejection(status); reason != "" {
			lastError = reason
			return
		}
		if err != nil || status != 200 || !validCodexTicketState(state) {
			lastError = "Harvest did not return a completed target-model response and valid STATE"
		} else if _, envelopeErr := parseCodexTicketEnvelope(state, modelCfg.TicketPlan, time.Now()); envelopeErr != nil {
			lastError = "STATE envelope does not match the selected plan or validity window"
		} else {
			if ctx.Err() != nil || !s.openAICodexTicketEnabledContext(ctx) || s.openAICodexTicketHarvestProxyURLContext(ctx) != job.harvestProxyURL {
				return
			}
			replayState, status, err := s.fireCodexAccountTicketProbe(ctx, account, token, model, account.Proxy.URL(), state, timeout)
			if reason := codexTicketProbeRejection(status); reason != "" {
				lastError = reason
				return
			}
			if err == nil && status == 200 && !isCodexTicketAbnormalEnvelope(replayState, time.Now()) {
				// Serialize against account opt-out/source changes; reread persistent values immediately before publication.
				s.openaiCodexAccountMu.Lock()
				live, readErr := s.codexTicketAccountByID(ctx, id)
				liveCfg := codexAccountTicketConfigOf(live)
				liveModelCfg, liveConfigured := liveCfg.modelConfig(model)
				if readErr == nil && ctx.Err() == nil && s.codexAccountTicketJobLocked(id, model) == job && s.openAICodexTicketEnabledContext(ctx) && s.openAICodexTicketHarvestProxyURLContext(ctx) == job.harvestProxyURL && codexAccountTicketEligible(live) && liveConfigured && liveModelCfg.Enabled && liveModelCfg.Revision == job.revision && codexTicketFixedProxyFingerprint(live) == job.fixedFingerprint {
					now := time.Now()
					envelope, envelopeErr := parseCodexTicketEnvelope(state, modelCfg.TicketPlan, now)
					if envelopeErr != nil {
						s.openaiCodexAccountMu.Unlock()
						return
					}
					ticket := &openAICodexTicket{AccountID: id, Model: model, State: state, Length: len(state), CapturedAt: now, IssuedAt: envelope.IssuedAt, ExpiresAt: envelope.ExpiresAt, Attempts: attempt, Verified: true, ConfigRevision: job.revision, FixedProxyFingerprint: job.fixedFingerprint, Fingerprint: envelope.Fingerprint}
					if saveErr := s.storeOpenAICodexTicket(ctx, live, ticket); saveErr == nil {
						lastError = ""
					} else {
						lastError = "Could not save verified STATE"
					}
				}
				s.openaiCodexAccountMu.Unlock()
				return
			}
			lastError = "STATE did not preserve the target model on this account's fixed proxy"
		}
		if attempt < codexTicketMaxAttempts {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

// Account authentication, access and rate-limit rejections end the entire round.
// Rotating harvest exits cannot resolve these reliably; retain any still-valid
// ticket and use the existing failure cooldown instead of spending more probes.
func codexTicketProbeRejection(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "Upstream rejected authentication (HTTP 401); acquisition paused for cooldown"
	case http.StatusForbidden:
		return "Upstream denied access (HTTP 403); acquisition paused for cooldown"
	case http.StatusTooManyRequests:
		return "Upstream rate limit (HTTP 429); acquisition paused for cooldown"
	default:
		return ""
	}
}

var codexTicketSIDPattern = regexp.MustCompile(`(?i)-sid-[^-]+(-t-[0-9]+)`)

func freshCodexTicketProxyURL(raw string) string {
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.User == nil {
		return raw
	}
	username := parsed.User.Username()
	sid := strings.ReplaceAll(uuid.NewString(), "-", "")[:20]
	if strings.Contains(username, "{sid}") {
		username = strings.ReplaceAll(username, "{sid}", sid)
	} else if strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".1024proxy.io") || strings.EqualFold(parsed.Hostname(), "1024proxy.io") {
		username = codexTicketSIDPattern.ReplaceAllString(username, "-sid-"+sid+"${1}")
	}
	if password, ok := parsed.User.Password(); ok {
		parsed.User = url.UserPassword(username, password)
	} else {
		parsed.User = url.User(username)
	}
	return parsed.String()
}

func validateCodexTicketCompletedModel(body io.Reader, model string) error {
	if body == nil {
		return errors.New("missing completion")
	}
	scanner := bufio.NewScanner(io.LimitReader(body, 2<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	eventType := ""
	var data strings.Builder
	validate := func() (bool, error) {
		raw := strings.TrimSpace(data.String())
		if raw == "" {
			return false, nil
		}
		if !gjson.Valid(raw) {
			return false, errors.New("invalid completion")
		}
		typ := gjson.Get(raw, "type").String()
		if typ == "" {
			typ = eventType
		}
		if typ == "response.failed" || typ == "response.incomplete" || typ == "error" {
			return false, errors.New("incomplete response")
		}
		if typ != "response.completed" {
			return false, nil
		}
		if gjson.Get(raw, "response.model").String() != model {
			return false, errors.New("returned model differs from requested model")
		}
		status := gjson.Get(raw, "response.status").String()
		if status != "" && status != "completed" {
			return false, errors.New("incomplete response")
		}
		return true, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			okay, err := validate()
			if err != nil || okay {
				return err
			}
			data.Reset()
			eventType = ""
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				_ = data.WriteByte('\n')
			}
			_, _ = data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if okay, err := validate(); err != nil || okay {
		return err
	}
	return errors.New("response did not complete")
}
