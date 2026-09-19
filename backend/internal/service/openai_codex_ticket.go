package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	openAICodexTicketExtraKeyPrefix    = "codex_turn_ticket:"
	openAICodexAstraMinVersion         = "0.153.4"
	openAICodexTicketDefaultModel      = "gpt-6-astra"
	openAICodexTicketDefaultSolModel   = "gpt-5.6-sol"
	openAICodexTicketDefaultTerraModel = "gpt-5.6-terra"
)

// ErrOpenAICodexTicketUnavailable indicates an opted-in account has no verified
// STATE for its configured model. Other accounts and models are unaffected.
var ErrOpenAICodexTicketUnavailable = errors.New("codex turn-state ticket unavailable")

type openAICodexTicket struct {
	AccountID             int64     `json:"account_id"`
	Model                 string    `json:"model"`
	State                 string    `json:"state"`
	Length                int       `json:"length"`
	CapturedAt            time.Time `json:"captured_at"`
	IssuedAt              time.Time `json:"issued_at"`
	ExpiresAt             time.Time `json:"expires_at"`
	Attempts              int       `json:"attempts"`
	Verified              bool      `json:"verified"`
	ConfigRevision        string    `json:"config_revision"`
	FixedProxyFingerprint string    `json:"fixed_proxy_fingerprint"`
	Fingerprint           string    `json:"fingerprint"`
	Version               uint64    `json:"version"`
}

type openAICodexTicketSlot struct {
	Active  *openAICodexTicket `json:"active,omitempty"`
	Ready   *openAICodexTicket `json:"ready,omitempty"`
	Strikes int                `json:"strikes"`
	Version uint64             `json:"version"`
}

func openAICodexTicketKey(accountID int64, model string) string {
	return fmt.Sprintf("%d\x00%s", accountID, strings.TrimSpace(model))
}

func openAICodexTicketExtraKey(model string) string {
	return openAICodexTicketExtraKeyPrefix + strings.TrimSpace(model)
}

func normalizeOpenAICodexTicketModel(model string) string {
	return strings.TrimSpace(model)
}

func extractOpenAICodexTicketModel(body []byte) string {
	return normalizeOpenAICodexTicketModel(gjson.GetBytes(body, "model").String())
}

func (s *OpenAIGatewayService) openAICodexTicketConfig() config.OpenAICodexTicketConfig {
	cfg := config.OpenAICodexTicketConfig{}
	if s != nil && s.cfg != nil {
		cfg = s.cfg.Gateway.OpenAICodexTicket
	}
	if cfg.TargetLength <= 0 {
		cfg.TargetLength = 292
	}
	if cfg.TTLSeconds <= 0 {
		cfg.TTLSeconds = 3600
	}
	if cfg.RefreshBeforeSeconds <= 0 {
		cfg.RefreshBeforeSeconds = 600
	}
	if cfg.HarvestProbeIntervalSeconds <= 0 {
		cfg.HarvestProbeIntervalSeconds = 6
	}
	if cfg.HarvestAttemptTimeoutSeconds <= 0 {
		cfg.HarvestAttemptTimeoutSeconds = 25
	}
	if len(cfg.Models) == 0 {
		cfg.Models = []string{openAICodexTicketDefaultModel, openAICodexTicketDefaultSolModel, openAICodexTicketDefaultTerraModel}
	}
	return cfg
}

// OpenAICodexTicketStatus 是给管理端看的门票摘要，不含 state blob。
type OpenAICodexTicketStatus struct {
	Model            string     `json:"model"`
	Length           int        `json:"length,omitempty"`
	Ready            bool       `json:"ready"`
	RemainingSeconds int64      `json:"remaining_seconds"`
	Blocked          bool       `json:"blocked"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

func OpenAICodexTicketStatuses(account *Account, cfg config.OpenAICodexTicketConfig, now time.Time) []OpenAICodexTicketStatus {
	ac := codexAccountTicketConfigOf(account)
	if !cfg.Enabled || !isOpenAICodexTicketAccount(account) {
		return nil
	}
	statuses := make([]OpenAICodexTicketStatus, 0, len(ac.enabledModels()))
	for _, model := range ac.enabledModels() {
		status := OpenAICodexTicketStatus{Model: model}
		slot := parseOpenAICodexTicketSlotFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
		if slot != nil && slot.Active.validFor(account, ac, now) {
			status.Ready = true
			status.Length = slot.Active.Length
			status.RemainingSeconds = int64(slot.Active.ExpiresAt.Sub(now) / time.Second)
			expiry := slot.Active.ExpiresAt
			status.ExpiresAt = &expiry
		}
		status.Blocked = !status.Ready
		statuses = append(statuses, status)
	}
	return statuses
}

func (s *OpenAIGatewayService) openAICodexTicketEnabled() bool {
	return s.openAICodexTicketEnabledContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketEnabledContext(ctx context.Context) bool {
	if s == nil {
		return false
	}
	fallback := s.cfg != nil && s.cfg.Gateway.OpenAICodexTicket.Enabled
	if s.settingService != nil {
		return s.settingService.GetOpenAICodexTicketEnabled(ctx, fallback)
	}
	return fallback
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURL() string {
	return s.openAICodexTicketHarvestProxyURLContext(context.Background())
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestProxyURLContext(ctx context.Context) string {
	if s.settingService != nil {
		if proxy := s.settingService.GetOpenAICodexTicketHarvestProxyURL(ctx); proxy != "" {
			return proxy
		}
	}
	return strings.TrimSpace(s.openAICodexTicketConfig().HarvestProxyURL)
}

func (t *openAICodexTicket) valid(now time.Time, _ int) bool {
	return t != nil && t.Verified && t.Length == len(t.State) &&
		t.AccountID > 0 && t.Model != "" && t.ConfigRevision != "" && t.FixedProxyFingerprint != "" &&
		!t.CapturedAt.IsZero() && !t.CapturedAt.After(now.Add(time.Minute))
}
func (t *openAICodexTicket) validFor(account *Account, ac codexAccountTicketConfig, now time.Time) bool {
	if account == nil || t == nil {
		return false
	}
	modelCfg, ok := ac.modelConfig(t.Model)
	if !ok || !modelCfg.Enabled || !t.valid(now, 0) || t.AccountID != account.ID ||
		t.ConfigRevision != modelCfg.Revision || t.FixedProxyFingerprint != codexTicketFixedProxyFingerprint(account) {
		return false
	}
	envelope, err := parseCodexTicketEnvelope(t.State, modelCfg.TicketPlan, now)
	if err != nil || !envelope.usableAt(now) {
		return false
	}
	if !t.IssuedAt.IsZero() && !t.IssuedAt.Equal(envelope.IssuedAt) {
		return false
	}
	if !t.ExpiresAt.IsZero() && !t.ExpiresAt.Equal(envelope.ExpiresAt) {
		return false
	}
	return t.Fingerprint == "" || t.Fingerprint == envelope.Fingerprint
}
func validCodexTicketState(state string) bool {
	now := time.Now()
	_, proErr := parseCodexTicketEnvelope(state, codexTicketPlanPro, now)
	_, teamErr := parseCodexTicketEnvelope(state, codexTicketPlanTeam, now)
	return proErr == nil || teamErr == nil
}

func (t *openAICodexTicket) needsRefresh(now time.Time, refreshBefore time.Duration) bool {
	if t == nil || t.ExpiresAt.IsZero() {
		return true
	}
	return !t.ExpiresAt.After(now.Add(refreshBefore))
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicket(account *Account, model string) *openAICodexTicket {
	slot := s.lookupOpenAICodexTicketSlot(account, model)
	if slot == nil {
		return nil
	}
	return slot.Active
}

func (s *OpenAIGatewayService) lookupOpenAICodexTicketSlot(account *Account, model string) *openAICodexTicketSlot {
	if s == nil || account == nil || account.ID <= 0 {
		return nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	ac := codexAccountTicketConfigOf(account)
	modelCfg, enabled := ac.modelConfig(model)
	if !enabled || !modelCfg.Enabled {
		return nil
	}
	now := time.Now()
	key := openAICodexTicketKey(account.ID, model)
	var memory *openAICodexTicketSlot
	if raw, ok := s.openaiCodexTickets.Load(key); ok {
		switch stored := raw.(type) {
		case *openAICodexTicketSlot:
			memory = stored
		case *openAICodexTicket:
			version := stored.Version
			if version == 0 {
				version = 1
			}
			memory = &openAICodexTicketSlot{Active: stored, Version: version}
		}
	}
	persisted := parseOpenAICodexTicketSlotFromAny(account.ID, model, account.Extra[openAICodexTicketExtraKey(model)])
	best := memory
	if persisted != nil && (best == nil || persisted.Version > best.Version) {
		best = persisted
	}
	if best == nil {
		s.openaiCodexTickets.Delete(key)
		return nil
	}
	best = cloneOpenAICodexTicketSlot(best)
	if s.codexTicketRejectedByWatchdog(best.Active) || !best.Active.validFor(account, ac, now) {
		best.Active = nil
	}
	if s.codexTicketRejectedByWatchdog(best.Ready) || !best.Ready.validFor(account, ac, now) {
		best.Ready = nil
	}
	if best.Active == nil && best.Ready != nil {
		best.Active, best.Ready = best.Ready, nil
		best.Strikes = 0
		best.Version++
		best.Active.Version = best.Version
		if s.accountRepo != nil {
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = s.accountRepo.UpdateExtra(writeCtx, account.ID, map[string]any{openAICodexTicketExtraKey(model): best})
			cancel()
		}
	}
	if best.Active != nil {
		s.openaiCodexTickets.Store(key, best)
		return best
	}
	s.openaiCodexTickets.Delete(key)
	return nil
}

func cloneOpenAICodexTicketSlot(slot *openAICodexTicketSlot) *openAICodexTicketSlot {
	if slot == nil {
		return nil
	}
	out := *slot
	if slot.Active != nil {
		active := *slot.Active
		out.Active = &active
	}
	if slot.Ready != nil {
		ready := *slot.Ready
		out.Ready = &ready
	}
	return &out
}

func parseOpenAICodexTicketFromAny(accountID int64, model string, raw any) *openAICodexTicket {
	slot := parseOpenAICodexTicketSlotFromAny(accountID, model, raw)
	if slot == nil {
		return nil
	}
	return slot.Active
}

func parseOpenAICodexTicketSlotFromAny(accountID int64, model string, raw any) *openAICodexTicketSlot {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var slot openAICodexTicketSlot
	if err := json.Unmarshal(b, &slot); err == nil && (slot.Active != nil || slot.Ready != nil || slot.Version > 0) {
		if slot.Active != nil && (slot.Active.AccountID != accountID || slot.Active.Model != model) {
			slot.Active = nil
		}
		if slot.Ready != nil && (slot.Ready.AccountID != accountID || slot.Ready.Model != model) {
			slot.Ready = nil
		}
		if slot.Active == nil && slot.Ready == nil {
			return nil
		}
		normalizePersistedCodexTicket(slot.Active)
		normalizePersistedCodexTicket(slot.Ready)
		return &slot
	}
	var ticket openAICodexTicket
	if err := json.Unmarshal(b, &ticket); err != nil {
		return nil
	}
	if ticket.AccountID != accountID || ticket.Model != model {
		return nil
	}
	ticket.State = strings.TrimSpace(ticket.State)
	if ticket.Length == 0 {
		ticket.Length = len(ticket.State)
	}
	if ticket.State == "" {
		return nil
	}
	if ticket.Version == 0 {
		ticket.Version = 1
	}
	normalizePersistedCodexTicket(&ticket)
	return &openAICodexTicketSlot{Active: &ticket, Version: ticket.Version}
}

func normalizePersistedCodexTicket(ticket *openAICodexTicket) {
	if ticket == nil {
		return
	}
	envelope, err := parseCodexTicketEnvelopeShape(ticket.State, time.Now())
	if err != nil {
		return
	}
	if ticket.IssuedAt.IsZero() {
		ticket.IssuedAt = envelope.IssuedAt
	}
	// #7338 persisted expiry from capture time. Upgrade legacy records to the
	// envelope's immutable issuance window during read compatibility.
	if ticket.ExpiresAt.IsZero() || ticket.Fingerprint == "" {
		ticket.ExpiresAt = envelope.ExpiresAt
	}
	if ticket.Fingerprint == "" {
		ticket.Fingerprint = envelope.Fingerprint
	}
}

// storeOpenAICodexTicket is called only while the per-account mutation lock is held.
func (s *OpenAIGatewayService) storeOpenAICodexTicket(ctx context.Context, account *Account, ticket *openAICodexTicket) error {
	if s == nil || account == nil || s.codexTicketRejectedByWatchdog(ticket) || !ticket.validFor(account, codexAccountTicketConfigOf(account), time.Now()) {
		return ErrOpenAICodexTicketUnavailable
	}
	key := openAICodexTicketKey(account.ID, ticket.Model)
	current := s.lookupOpenAICodexTicketSlot(account, ticket.Model)
	slot := cloneOpenAICodexTicketSlot(current)
	if slot == nil {
		slot = &openAICodexTicketSlot{}
	}
	slot.Version++
	ticket.Version = slot.Version
	if slot.Active == nil || !slot.Active.validFor(account, codexAccountTicketConfigOf(account), time.Now()) {
		slot.Active = ticket
		slot.Ready = nil
		slot.Strikes = 0
	} else if slot.Active.Fingerprint != ticket.Fingerprint {
		slot.Ready = ticket
	}
	if s.accountRepo != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.accountRepo.UpdateExtra(writeCtx, account.ID, map[string]any{openAICodexTicketExtraKey(ticket.Model): slot}); err != nil {
			return err
		}
	}
	s.openaiCodexTickets.Store(key, slot)
	return nil
}

// applyOpenAICodexTicket 在出站请求上覆盖 x-codex-turn-state。
// 请求路径只注入已捕获的有效门票，不现场打票；无票则返回
// ErrOpenAICodexTicketUnavailable。打票由后台 harvester 完成。
func (s *OpenAIGatewayService) applyOpenAICodexTicket(ctx context.Context, account *Account, model string, h http.Header) error {
	_, err := s.applyOpenAICodexTicketWithReceipt(ctx, account, model, h)
	return err
}

func (s *OpenAIGatewayService) applyOpenAICodexTicketWithReceipt(ctx context.Context, account *Account, model string, h http.Header) (*codexTicketReceipt, error) {
	if s == nil || h == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabledContext(ctx) {
		return nil, nil
	}
	// guardOpenAICodexTurnStateEcho already removed known cross-account STATE.
	// A remaining client STATE belongs to this account or has unknown provenance;
	// either way it has priority over a background ticket and is not watched.
	if strings.TrimSpace(h.Get(openAICodexTurnStateHeader)) != "" {
		return nil, nil
	}
	model = normalizeOpenAICodexTicketModel(model)
	live, err := s.codexTicketLiveAccount(ctx, account)
	if err != nil {
		if cfg, ok := codexAccountTicketConfigOf(account).modelConfig(model); ok && cfg.Enabled {
			return nil, ErrOpenAICodexTicketUnavailable
		}
		return nil, nil
	}
	ac := codexAccountTicketConfigOf(live)
	modelCfg, configured := ac.modelConfig(model)
	if !isOpenAICodexTicketAccount(live) || !configured || !modelCfg.Enabled {
		return nil, nil
	}
	// A scheduler snapshot with a different business proxy must be reselected.
	if codexTicketFixedProxyFingerprint(account) != codexTicketFixedProxyFingerprint(live) {
		return nil, ErrOpenAICodexTicketUnavailable
	}
	ticket := s.lookupOpenAICodexTicket(live, model)
	if ticket.validFor(live, ac, time.Now()) {
		h.Set(openAICodexTurnStateHeader, ticket.State)
		receipt := receiptForCodexTicket(ticket)
		return &receipt, nil
	}
	return nil, ErrOpenAICodexTicketUnavailable
}

// openAICodexTicketOutboundModel 预测本请求真正出站的模型名，也就是
// applyOpenAICodexTicket 注入时读到的 body.model。
//
// 调度门控与注入必须按同一个模型名判定门票。普通请求下二者同源：Forward 的
// upstreamModel 与本函数都走 resolveOpenAIAccountUpstreamModelForRequest，且
// Forward 会把 body.model 改写成该值后才注入。但 /responses/compact 例外——
// Forward 会把出站模型进一步改写为 compact 映射或 gateway.openai_compact_model
// （默认非空），此时若门控仍按客户端原始模型判定，就会把「实际出站是非门控
// 模型、根本不需要票」的 compact 请求整片误拦成不可调度。
func (s *OpenAIGatewayService) openAICodexTicketOutboundModel(account *Account, requestedModel string, requireCompact bool) string {
	model := strings.TrimSpace(requestedModel)
	if account == nil || model == "" {
		return model
	}
	if !account.IsOpenAI() {
		return canonicalOpenAIAccountSchedulingModel(account, model)
	}
	_, upstreamModel := resolveOpenAIForwardMappedModels(account, model, requireCompact)
	if requireCompact {
		// 与 Forward 同序：compact 兜底模型优先于普通/compact 映射结果。
		if compactModel := strings.TrimSpace(s.resolveOpenAICompactFallbackModel(account, model)); compactModel != "" {
			upstreamModel = compactModel
		}
	}
	if upstreamModel = strings.TrimSpace(upstreamModel); upstreamModel != "" {
		return upstreamModel
	}
	return model
}

// outboundModel 必须是真正会发给上游的模型名（openAICodexTicketOutboundModel），
// 不是客户端原始模型：注入侧读的是出站 body.model，两侧口径必须一致。
func (s *OpenAIGatewayService) openAICodexTicketBlocksAccount(account *Account, outboundModel string) bool {
	if s == nil || !isOpenAICodexTicketAccount(account) || !s.openAICodexTicketEnabled() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	live, err := s.codexTicketLiveAccount(ctx, account)
	if err != nil {
		ac := codexAccountTicketConfigOf(account)
		modelCfg, ok := ac.modelConfig(outboundModel)
		return ok && modelCfg.Enabled
	}
	ac := codexAccountTicketConfigOf(live)
	model := normalizeOpenAICodexTicketModel(outboundModel)
	modelCfg, configured := ac.modelConfig(model)
	if !configured || !modelCfg.Enabled || !isOpenAICodexTicketAccount(live) {
		return false
	}
	return !s.lookupOpenAICodexTicket(live, model).validFor(live, ac, time.Now())
}

func (s *OpenAIGatewayService) fireOpenAICodexTicketProbe(ctx context.Context, account *Account, token, model, proxyURL string, attemptTimeout time.Duration) (state string, status int, err error) {
	return s.fireCodexAccountTicketProbe(ctx, account, token, model, proxyURL, "", attemptTimeout)
}

func (s *OpenAIGatewayService) fireCodexAccountTicketProbe(ctx context.Context, account *Account, token, model, proxyURL, injectedState string, attemptTimeout time.Duration) (state string, status int, err error) {
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()

	body := []byte(`{"model":` + jsonString(model) + `,"store":false,"stream":true,"instructions":"Reply with exactly: pong","input":[{"role":"user","content":[{"type":"input_text","text":"ping"}]}]}`)
	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, chatgptCodexURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, err
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAIHarvest))
	req.Close = true
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("session_id", uuid.NewString())
	if err := resolveAndSetOpenAIChatGPTAccountHeaders(attemptCtx, s.accountRepo, req.Header, account); err != nil {
		return "", 0, err
	}
	applyOpenAICodexTicketHarvestIdentity(req.Header, model)
	if injectedState != "" {
		req.Header.Set(openAICodexTurnStateHeader, injectedState)
	}

	// Synthetic probes must use the dedicated no-reuse transport even when the
	// production account is bound to a plugin. This also avoids reading pluginManager
	// while handlers are still wiring it during gateway construction.
	var resp *http.Response
	if injectedState == "" && s.openAICodexTicketConfig().HarvestDialProxyURL != "" {
		var client *http.Client
		client, err = newCodexTicketChainedClient(proxyURL, s.openAICodexTicketConfig().HarvestDialProxyURL)
		if err == nil {
			defer client.CloseIdleConnections()
			resp, err = client.Do(req)
		}
	} else if s.httpUpstream != nil {
		resp, err = s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	} else {
		err = errors.New("ticket transport unavailable")
	}
	if err != nil {
		return "", 0, err
	}
	if resp == nil {
		return "", 0, errors.New("nil upstream response")
	}
	// A completed response with the requested actual model is required.
	defer func() {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, errors.New("upstream request was rejected")
	}
	if err := validateCodexTicketCompletedModel(resp.Body, model); err != nil {
		return "", resp.StatusCode, err
	}
	return extractOpenAICodexTurnState(resp.Header), resp.StatusCode, nil
}

func jsonString(v string) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `""`
	}
	return string(b)
}

func applyOpenAICodexTicketHarvestIdentity(h http.Header, model string) {
	ensureCodexIdentityHeaders(h)
	enforceCodexIdentityHeaders(h)
	version := strings.TrimSpace(h.Get("version"))
	if needsOpenAICodexAstraVersion(model) && (version == "" || CompareVersions(version, openAICodexAstraMinVersion) < 0) {
		h.Set("version", openAICodexAstraMinVersion)
		h.Set("user-agent", buildCodexCLIUserAgent(openAICodexAstraMinVersion))
		h.Set("originator", openai.CodexDefaultOriginator)
	}
}

func needsOpenAICodexAstraVersion(model string) bool {
	m := strings.ToLower(normalizeOpenAICodexTicketModel(model))
	return strings.Contains(m, "gpt-6") || strings.Contains(m, "astra")
}

func (s *OpenAIGatewayService) StartOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	defer s.openaiCodexTicketLifecycleMu.Unlock()
	if s.openaiCodexTicketStopped || s.openaiCodexTicketDone != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.openaiCodexTicketCancel = cancel
	s.openaiCodexTicketContext = ctx
	s.openaiCodexTicketDone = done
	go func() {
		defer close(done)
		s.openAICodexTicketHarvestLoop(ctx)
	}()
	logger.L().Info("openai_codex_ticket harvester started",
		zap.Int("ttl_seconds", 3600),
		zap.String("scope", "account_opt_in"),
		zap.Bool("completed_model_and_fixed_proxy_validation", true),
	)
}

func (s *OpenAIGatewayService) StopOpenAICodexTicketHarvester() {
	if s == nil {
		return
	}
	s.openaiCodexTicketLifecycleMu.Lock()
	s.openaiCodexTicketStopped = true
	cancel, done := s.openaiCodexTicketCancel, s.openaiCodexTicketDone
	s.openaiCodexTicketLifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	s.openaiCodexAccountMu.Lock()
	s.openaiCodexAccountStopping = true
	s.cancelCodexTicketJobsLocked()
	s.openaiCodexAccountMu.Unlock()
	s.openaiCodexAccountWG.Wait()
}

func (s *OpenAIGatewayService) openAICodexTicketHarvestLoop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.refreshOpenAICodexTickets(ctx)
			timer.Reset(time.Duration(s.openAICodexTicketConfig().HarvestProbeIntervalSeconds) * time.Second)
		}
	}
}

// The ticker only launches bounded jobs. A failed job cools down for five minutes.
func (s *OpenAIGatewayService) refreshOpenAICodexTickets(ctx context.Context) {
	if s == nil || s.accountRepo == nil || ctx.Err() != nil {
		return
	}
	if !s.openAICodexTicketEnabledContext(ctx) {
		s.cancelCodexTicketJobs()
		return
	}
	accounts, err := s.accountRepo.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return
	}
	for i := range accounts {
		account := &accounts[i]
		ac := codexAccountTicketConfigOf(account)
		if !codexAccountTicketEligible(account) {
			continue
		}
		for _, model := range ac.enabledModels() {
			ticket := s.lookupOpenAICodexTicket(account, model)
			if ticket.validFor(account, ac, time.Now()) && !ticket.needsRefresh(time.Now(), 10*time.Minute) {
				continue
			}
			s.startCodexAccountTicketModelJob(ctx, account.ID, model, false)
		}
	}
}

// IsOpenAICodexTicketExtraKey identifies server-managed ticket material.
func IsOpenAICodexTicketExtraKey(key string) bool {
	return key == codexTicketWatchdogExtraKey || key == codexAccountTicketConfigKey ||
		strings.HasPrefix(key, codexTicketWatchdogExtraKeyPrefix) || strings.HasPrefix(key, openAICodexTicketExtraKeyPrefix)
}

// MergeOpenAICodexTicketExtra preserves only persisted tickets, never summaries or
// blobs supplied by an account edit. The repository repeats this under the row
// lock so a concurrent harvest cannot be overwritten by a stale admin snapshot.
func MergeOpenAICodexTicketExtra(extra, current map[string]any) map[string]any {
	result := maps.Clone(extra)
	for key := range result {
		if IsOpenAICodexTicketExtraKey(key) {
			delete(result, key)
		}
	}
	for key, value := range current {
		if IsOpenAICodexTicketExtraKey(key) {
			if result == nil {
				result = make(map[string]any)
			}
			result[key] = value
		}
	}
	return result
}

// ValidateOpenAICodexTicketHarvestProxyURL validates only syntax, without making
// a network request or including credentials in validation errors.
func ValidateOpenAICodexTicketHarvestProxyURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.Hostname() == "" || parsed.Opaque != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("harvest proxy must be an HTTP(S) or SOCKS5(h) URL with a host and no path, query or fragment")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("harvest proxy scheme must be http, https, socks5 or socks5h")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("harvest proxy port must be between 1 and 65535")
		}
	}
	return nil
}

// MaskProxyURL never returns a stored proxy password, even for invalid legacy data.
func MaskProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || ValidateOpenAICodexTicketHarvestProxyURL(raw) != nil {
		return ""
	}
	parsed, _ := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if parsed.User != nil {
		if _, ok := parsed.User.Password(); ok {
			parsed.User = url.UserPassword(parsed.User.Username(), "***")
		}
	}
	return parsed.String()
}

// IsMaskedProxyURL recognizes the exact password placeholder emitted by the API.
func IsMaskedProxyURL(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	parsed, err := url.Parse(strings.ReplaceAll(raw, "{sid}", "%7Bsid%7D"))
	if err != nil || parsed.User == nil {
		return false
	}
	password, ok := parsed.User.Password()
	return ok && password == "***"
}

// Credential shadows do not own tickets. Keep their existing forwarding policy
// instead of imposing a gate for a key the harvester never populates.
func isOpenAICodexTicketAccount(account *Account) bool {
	return account != nil && account.IsOpenAIOAuthLike() && !account.IsShadow()
}

// IsOpenAICodexTicketPrivateExtraKey also covers the retired account-level proxy
// override, whose credentials may remain in older account records.
func IsOpenAICodexTicketPrivateExtraKey(key string) bool {
	return IsOpenAICodexTicketExtraKey(key) || key == "codex_harvest_proxy_url"
}

// RedactOpenAICodexTicketExtra strips ephemeral ticket material from exports
// without changing the source account or unrelated backup fields.
func RedactOpenAICodexTicketExtra(extra map[string]any) map[string]any {
	redacted := maps.Clone(extra)
	for key := range redacted {
		if IsOpenAICodexTicketPrivateExtraKey(key) {
			delete(redacted, key)
		}
	}
	return redacted
}
