package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"
)

// DailyActivityService contains the server-owned rules for the user activity page.
// Amounts and eligibility are deliberately not accepted from clients.
type DailyActivityService struct {
	db           *sql.DB
	settings     *SettingService
	billingCache interface {
		InvalidateUserBalance(context.Context, int64) error
	}
}

// DailyActivityConfig is persisted as one JSON system setting. Amounts are in
// the site's currency units; the service validates them before persistence.
type DailyActivityConfig struct {
	Enabled                   bool    `json:"enabled"`
	DailyGiftThreshold        float64 `json:"daily_gift_threshold"`
	DailyGiftMinReward        float64 `json:"daily_gift_min_reward"`
	DailyGiftMaxReward        float64 `json:"daily_gift_max_reward"`
	RechargeDrawThreshold     float64 `json:"recharge_draw_threshold"`
	RechargeDrawMinReward     float64 `json:"recharge_draw_min_reward"`
	RechargeDrawMaxReward     float64 `json:"recharge_draw_max_reward"`
	ConsumptionDrawThreshold  float64 `json:"consumption_draw_threshold"`
	ConsumptionDrawMinReward  float64 `json:"consumption_draw_min_reward"`
	ConsumptionDrawMaxReward  float64 `json:"consumption_draw_max_reward"`
	InviteQualificationAmount float64 `json:"invite_qualification_amount"`
	InviteDrawRequiredCount   int64   `json:"invite_draw_required_count"`
	InviteDrawMinReward       float64 `json:"invite_draw_min_reward"`
	InviteDrawMaxReward       float64 `json:"invite_draw_max_reward"`
}

func DefaultDailyActivityConfig() DailyActivityConfig {
	return DailyActivityConfig{Enabled: true, DailyGiftThreshold: 10, DailyGiftMinReward: 0, DailyGiftMaxReward: .5,
		RechargeDrawThreshold: 50, RechargeDrawMinReward: .5, RechargeDrawMaxReward: 1,
		ConsumptionDrawThreshold: 50, ConsumptionDrawMinReward: .5, ConsumptionDrawMaxReward: 1,
		InviteQualificationAmount: 10, InviteDrawRequiredCount: 5, InviteDrawMinReward: 5, InviteDrawMaxReward: 10}
}

func ValidateDailyActivityConfig(c DailyActivityConfig) error {
	for _, threshold := range []float64{c.DailyGiftThreshold, c.RechargeDrawThreshold, c.ConsumptionDrawThreshold, c.InviteQualificationAmount} {
		if threshold <= 0 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
			return errors.New("activity thresholds must be finite and greater than zero")
		}
		if threshold > 10000000 {
			return errors.New("activity threshold is too large")
		}
	}
	if c.InviteDrawRequiredCount <= 0 {
		return errors.New("invite draw required count must be greater than zero")
	}
	if c.InviteDrawRequiredCount > 1000000 {
		return errors.New("invite draw required count is too large")
	}
	for _, p := range [][2]float64{{c.DailyGiftMinReward, c.DailyGiftMaxReward}, {c.RechargeDrawMinReward, c.RechargeDrawMaxReward}, {c.ConsumptionDrawMinReward, c.ConsumptionDrawMaxReward}, {c.InviteDrawMinReward, c.InviteDrawMaxReward}} {
		if p[0] < 0 || p[1] < p[0] || math.IsNaN(p[0]) || math.IsNaN(p[1]) || math.IsInf(p[0], 0) || math.IsInf(p[1], 0) {
			return errors.New("activity reward range is invalid")
		}
		if p[1] > 100000 {
			return errors.New("activity reward is too large")
		}
	}
	return nil
}

func NewDailyActivityService(db *sql.DB, settings *SettingService) *DailyActivityService {
	return &DailyActivityService{db: db, settings: settings}
}

// SetBillingCache attaches the shared balance cache invalidator. Activity
// rewards are committed in the same SQL transaction as their ledger row; the
// cache is invalidated only after that transaction succeeds.
func (s *DailyActivityService) SetBillingCache(cache interface {
	InvalidateUserBalance(context.Context, int64) error
}) {
	if s != nil {
		s.billingCache = cache
	}
}

func (s *DailyActivityService) invalidateBalanceCache(ctx context.Context, userID int64) {
	if s == nil || s.billingCache == nil {
		return
	}
	if err := s.billingCache.InvalidateUserBalance(ctx, userID); err != nil {
		slog.Warn("invalidate activity reward balance cache failed", "user_id", userID, "error", err)
	}
}

func (s *DailyActivityService) config(ctx context.Context) DailyActivityConfig {
	cfg := DefaultDailyActivityConfig()
	if s != nil && s.settings != nil {
		if all, err := s.settings.GetAllSettings(ctx); err == nil && all != nil && ValidateDailyActivityConfig(all.DailyActivityConfig) == nil {
			cfg = all.DailyActivityConfig
		}
	}
	return cfg
}

func (s *DailyActivityService) startedAt(ctx context.Context) time.Time {
	if s != nil && s.settings != nil && s.settings.settingRepo != nil {
		if raw, err := s.settings.settingRepo.GetValue(ctx, SettingKeyDailyActivityStartedAt); err == nil {
			if epoch, parseErr := strconv.ParseFloat(strings.TrimSpace(raw), 64); parseErr == nil && epoch > 0 {
				seconds, fraction := math.Modf(epoch)
				return time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC()
			}
		}
	}
	return time.Unix(0, 0).UTC()
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

type activityQueryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s *DailyActivityService) rechargeAmount(ctx context.Context, q activityQueryRower, userID int64, start, end time.Time) (float64, error) {
	rechargeSQL := fmt.Sprintf(`SELECT COALESCE(SUM(amount),0) FROM (%s) recharge`, s.activityRechargeSourcesSQL("$1", "$2", "$3"))
	var amount float64
	if err := q.QueryRowContext(ctx, rechargeSQL, userID, start, end).Scan(&amount); err != nil {
		return 0, err
	}
	return amount, nil
}

// activityRechargeSourcesSQL returns the monetary events which have the same
// qualification semantics as the invitation rebate flow. Payment orders are
// included for both balance and subscription purchases. A successful payment
// fulfillment also creates and redeems a balance code, so those codes are
// excluded by recharge_code to avoid counting the same payment twice.
//
// Administrator additions are persisted as explicit activity events at the
// moment the existing affiliate switch accepts them. This avoids confusing an
// administrator's "set balance" operation with an actual recharge.
//
// The expression/placeholder arguments are internal fixed SQL fragments (never
// client input).
func (s *DailyActivityService) activityRechargeSourcesSQL(userExpr, startExpr, endExpr string) string {
	upperBound := func(column string) string {
		if strings.TrimSpace(endExpr) == "" {
			return ""
		}
		return " AND " + column + " < " + endExpr
	}
	parts := []string{
		fmt.Sprintf(`SELECT po.amount AS amount, po.paid_at AS event_at
			FROM payment_orders po
			WHERE po.user_id=%s AND po.order_type IN ('balance','subscription')
			  AND po.status='COMPLETED' AND po.paid_at IS NOT NULL
			  AND po.paid_at >= %s%s`, userExpr, startExpr, upperBound("po.paid_at")),
		fmt.Sprintf(`SELECT rc.value AS amount, rc.used_at AS event_at
			FROM redeem_codes rc
			WHERE rc.used_by=%s AND rc.type='balance' AND rc.status='used'
			  AND rc.value > 0 AND rc.used_at IS NOT NULL
			  AND rc.used_at >= %s%s
			  AND NOT EXISTS (
				  SELECT 1 FROM payment_orders po
				  WHERE po.recharge_code=rc.code
				    AND po.order_type IN ('balance','subscription')
			  )`, userExpr, startExpr, upperBound("rc.used_at")),
		fmt.Sprintf(`SELECT are.amount AS amount, are.occurred_at AS event_at
			FROM activity_recharge_events are
			WHERE are.user_id=%s AND are.source_type='admin_balance_add'
			  AND are.amount > 0 AND are.occurred_at >= %s%s`, userExpr, startExpr, upperBound("are.occurred_at")),
	}
	return strings.Join(parts, " UNION ALL ")
}

// RecordAdminRecharge records only the administrator "add" events already
// accepted by the invitation-rebate switch. sourceKey is generated once per
// balance adjustment and provides retry-safe deduplication.
func (s *DailyActivityService) RecordAdminRecharge(ctx context.Context, userID int64, amount float64, sourceKey string, occurredAt time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("activity database unavailable")
	}
	if userID <= 0 || amount <= 0 || strings.TrimSpace(sourceKey) == "" {
		return nil
	}
	if s.settings == nil || s.settings.settingRepo == nil || !s.settings.IsAffiliateAdminRechargeEnabled(ctx) {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO activity_recharge_events(user_id,source_type,source_key,amount,occurred_at)
		VALUES($1,'admin_balance_add',$2,$3,$4)
		ON CONFLICT (source_type,source_key) DO NOTHING`, userID, sourceKey, amount, occurredAt)
	return err
}

const (
	activityDailyGift    = "daily_gift"
	activityRechargeDraw = "recharge_draw"
	activitySpendDraw    = "spend_draw"
	activityInviteDraw   = "invite_draw"
	activityRuleVersion  = "v1"
)

func normalizeActivityType(value string, allowGift bool) (string, error) {
	switch strings.TrimSpace(value) {
	case "recharge", activityRechargeDraw:
		return activityRechargeDraw, nil
	case "consumption", "spend", activitySpendDraw:
		return activitySpendDraw, nil
	case "invite", activityInviteDraw:
		return activityInviteDraw, nil
	case activityDailyGift:
		if allowGift {
			return activityDailyGift, nil
		}
	case "":
		if allowGift {
			return "", nil
		}
	}
	return "", fmt.Errorf("unsupported activity type")
}

type DailyActivitySummary struct {
	Enabled      bool              `json:"enabled"`
	Balance      float64           `json:"balance"`
	ActivityDate string            `json:"activity_date"`
	Timezone     string            `json:"timezone"`
	NextResetAt  string            `json:"next_reset_at"`
	DailyGift    DailyGiftProgress `json:"daily_gift"`
	Recharge     ActivityProgress  `json:"recharge"`
	Consumption  ActivityProgress  `json:"consumption"`
	Invite       InviteProgress    `json:"invite"`
}

type ActivityProgress struct {
	Amount         float64 `json:"amount"`
	Threshold      float64 `json:"threshold"`
	AvailableDraws int64   `json:"available_draws"`
	LifetimeDraws  int64   `json:"lifetime_draws,omitempty"`
	RewardMin      float64 `json:"reward_min"`
	RewardMax      float64 `json:"reward_max"`
}
type DailyGiftProgress struct {
	Eligible  bool    `json:"eligible"`
	Claimed   bool    `json:"claimed"`
	Amount    float64 `json:"amount"`
	Threshold float64 `json:"threshold"`
	RewardMin float64 `json:"reward_min"`
	RewardMax float64 `json:"reward_max"`
}
type InviteProgress struct {
	QualifiedCount      int64   `json:"qualified_count"`
	RequiredCount       int64   `json:"required_count"`
	AvailableDraws      int64   `json:"available_draws"`
	LifetimeCount       int64   `json:"lifetime_count,omitempty"`
	QualificationAmount float64 `json:"qualification_amount"`
	RewardMin           float64 `json:"reward_min"`
	RewardMax           float64 `json:"reward_max"`
}

type DailyActivityReward struct {
	ID           int64     `json:"id"`
	ActivityType string    `json:"type"`
	Amount       float64   `json:"amount"`
	PeriodDate   *string   `json:"period_date,omitempty"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
}

type DailyActivityRewardsPage struct {
	Items    []DailyActivityReward `json:"items"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"page_size"`
}

var (
	ErrActivityDisabled = errors.New("daily activities are disabled")
	ErrActivityNotReady = errors.New("activity requirement not met")
	ErrActivityNoCredit = errors.New("no available draw credit")
)

func activityDay(now time.Time) (time.Time, string) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*60*60)
	}
	local := now.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return day, day.Format("2006-01-02")
}

func activityPeriod(day time.Time) (time.Time, time.Time) {
	return day.UTC(), day.AddDate(0, 0, 1).UTC()
}

func (s *DailyActivityService) activityWindow(ctx context.Context, now time.Time) (time.Time, time.Time, string) {
	day, date := activityDay(now)
	start, end := activityPeriod(day)
	return laterTime(start, s.startedAt(ctx)), end, date
}

func (s *DailyActivityService) Summary(ctx context.Context, userID int64, now time.Time) (*DailyActivitySummary, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("activity database unavailable")
	}
	cfg := s.config(ctx)
	if !cfg.Enabled {
		_, date := activityDay(now)
		return &DailyActivitySummary{Enabled: false, ActivityDate: date, Timezone: "Asia/Shanghai"}, nil
	}
	day, date := activityDay(now)
	start, end := activityPeriod(day)
	start = laterTime(start, s.startedAt(ctx))
	if err := s.SyncInvitationMilestones(ctx, userID); err != nil {
		return nil, err
	}
	if err := s.SyncCredits(ctx, userID, now); err != nil {
		return nil, err
	}
	var recharge, spend float64
	var balance float64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(balance,0) FROM users WHERE id=$1 AND deleted_at IS NULL`, userID).Scan(&balance); err != nil {
		return nil, err
	}
	rechargeAmount, err := s.rechargeAmount(ctx, s.db, userID, start, end)
	if err != nil {
		return nil, err
	}
	recharge = rechargeAmount
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(GREATEST(actual_cost,0)),0) FROM usage_logs WHERE user_id=$1 AND created_at >= $2 AND created_at < $3`, userID, start, end).Scan(&spend); err != nil {
		return nil, err
	}
	var qualified, inviteCredits, rechargeCredits, spendCredits int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_invitation_milestones WHERE inviter_id=$1`, userID).Scan(&qualified); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND activity_type=$2 AND consumed_at IS NULL`, userID, activityInviteDraw).Scan(&inviteCredits); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND activity_type=$2 AND consumed_at IS NULL`, userID, activityRechargeDraw).Scan(&rechargeCredits); err != nil {
		return nil, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND activity_type=$2 AND consumed_at IS NULL`, userID, activitySpendDraw).Scan(&spendCredits); err != nil {
		return nil, err
	}
	var claimed int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_reward_records WHERE user_id=$1 AND activity_type=$2 AND period_date=$3 AND status='credited'`, userID, activityDailyGift, date).Scan(&claimed); err != nil {
		return nil, err
	}
	next := day.AddDate(0, 0, 1).Format(time.RFC3339)
	return &DailyActivitySummary{Enabled: cfg.Enabled, Balance: balance, ActivityDate: date, Timezone: "Asia/Shanghai", NextResetAt: next,
		DailyGift:   DailyGiftProgress{Eligible: recharge >= cfg.DailyGiftThreshold && claimed == 0, Claimed: claimed > 0, Threshold: cfg.DailyGiftThreshold, RewardMin: cfg.DailyGiftMinReward, RewardMax: cfg.DailyGiftMaxReward},
		Recharge:    ActivityProgress{Amount: recharge, Threshold: cfg.RechargeDrawThreshold, AvailableDraws: rechargeCredits, RewardMin: cfg.RechargeDrawMinReward, RewardMax: cfg.RechargeDrawMaxReward},
		Consumption: ActivityProgress{Amount: spend, Threshold: cfg.ConsumptionDrawThreshold, AvailableDraws: spendCredits, RewardMin: cfg.ConsumptionDrawMinReward, RewardMax: cfg.ConsumptionDrawMaxReward},
		Invite:      InviteProgress{QualifiedCount: qualified, RequiredCount: cfg.InviteDrawRequiredCount, AvailableDraws: inviteCredits, LifetimeCount: qualified, QualificationAmount: cfg.InviteQualificationAmount, RewardMin: cfg.InviteDrawMinReward, RewardMax: cfg.InviteDrawMaxReward}}, nil
}

// SyncInvitationMilestones derives one permanent qualification per invitee from
// successful qualifying recharges. It is idempotent and intentionally independent
// from affiliate rebate settlement.
func (s *DailyActivityService) SyncInvitationMilestones(ctx context.Context, inviterID int64) error {
	cfg := s.config(ctx)
	rechargeSQL := s.activityRechargeSourcesSQL("ua.user_id", "$3", "")
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO activity_invitation_milestones(inviter_id, invitee_id, qualifying_amount, qualifying_order_id)
SELECT ua.inviter_id, ua.user_id, q.total_amount, q.order_id
FROM user_affiliates ua
JOIN LATERAL (
	  SELECT COALESCE(SUM(amount),0) AS total_amount, NULL::bigint AS order_id
	  FROM (%s) recharge_events
	) q ON q.total_amount >= $2
	WHERE ua.inviter_id=$1

	ON CONFLICT (inviter_id, invitee_id) DO NOTHING`, rechargeSQL), inviterID, cfg.InviteQualificationAmount, s.startedAt(ctx))
	return err
}

// SyncInvitationMilestoneForInvitee is the payment-settlement fast path. The
// broader inviter sync remains as a self-healing fallback when opening summary.
func (s *DailyActivityService) SyncInvitationMilestoneForInvitee(ctx context.Context, inviteeID, orderID int64) error {
	cfg := s.config(ctx)
	if !cfg.Enabled {
		return nil
	}
	rechargeSQL := s.activityRechargeSourcesSQL("ua.user_id", "$4", "")
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO activity_invitation_milestones(inviter_id, invitee_id, qualifying_amount, qualifying_order_id)
SELECT ua.inviter_id, ua.user_id, totals.total_amount, $2
FROM user_affiliates ua
JOIN LATERAL (
  SELECT COALESCE(SUM(amount),0) AS total_amount
  FROM (%s) recharge_events
) totals ON totals.total_amount >= $3
WHERE ua.user_id=$1 AND ua.inviter_id IS NOT NULL
ON CONFLICT (inviter_id, invitee_id) DO NOTHING`, rechargeSQL), inviteeID, orderID, cfg.InviteQualificationAmount, s.startedAt(ctx))
	return err
}

// SyncCredits materializes currently earned credits using unique sequence indexes.
func (s *DailyActivityService) SyncCredits(ctx context.Context, userID int64, now time.Time) error {
	cfg := s.config(ctx)
	if !cfg.Enabled {
		return ErrActivityDisabled
	}
	day, _ := activityDay(now)
	start, end := activityPeriod(day)
	start = laterTime(start, s.startedAt(ctx))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recharge, spend, invite int64
	rechargeSQL := fmt.Sprintf(`SELECT FLOOR(COALESCE(SUM(amount),0)/$4) FROM (%s) recharge`, s.activityRechargeSourcesSQL("$1", "$2", "$3"))
	if err = tx.QueryRowContext(ctx, rechargeSQL, userID, start, end, cfg.RechargeDrawThreshold).Scan(&recharge); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT FLOOR(COALESCE(SUM(GREATEST(actual_cost,0)),0)/$4) FROM usage_logs WHERE user_id=$1 AND created_at >= $2 AND created_at < $3`, userID, start, end, cfg.ConsumptionDrawThreshold).Scan(&spend); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT FLOOR(COUNT(*)/$2) FROM activity_invitation_milestones WHERE inviter_id=$1`, userID, cfg.InviteDrawRequiredCount).Scan(&invite); err != nil {
		return err
	}
	for typ, total := range map[string]int64{activityRechargeDraw: recharge, activitySpendDraw: spend, activityInviteDraw: invite} {
		var current int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(credit_index)+1,0) FROM activity_draw_credits WHERE user_id=$1 AND activity_type=$2`, userID, typ).Scan(&current); err != nil {
			return err
		}
		for current < total {
			if _, err = tx.ExecContext(ctx, `INSERT INTO activity_draw_credits(user_id,activity_type,credit_index,source) VALUES($1,$2,$3,'threshold') ON CONFLICT DO NOTHING`, userID, typ, current); err != nil {
				return err
			}
			current++
		}
	}
	return tx.Commit()
}

func randomCents(min, max int64) (int64, error) {
	if min < 0 || max < min {
		return 0, errors.New("invalid reward range")
	}
	span := max - min + 1
	if span <= 0 {
		return 0, errors.New("invalid reward range")
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	var n uint64
	for _, v := range b {
		n = (n << 8) | uint64(v)
	}
	return min + int64(n%uint64(span)), nil
}

func hashActivityKey(key string) *string {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	h := sha256.Sum256([]byte(key))
	value := hex.EncodeToString(h[:])
	return &value
}

func (s *DailyActivityService) OpenDailyGift(ctx context.Context, userID int64, idempotencyKey string, now time.Time) (*DailyActivityReward, error) {
	cfg := s.config(ctx)
	if !cfg.Enabled {
		return nil, ErrActivityDisabled
	}
	day, date := activityDay(now)
	start, end := activityPeriod(day)
	start = laterTime(start, s.startedAt(ctx))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var recharge float64
	if recharge, err = s.rechargeAmount(ctx, tx, userID, start, end); err != nil {
		return nil, err
	}
	if recharge < cfg.DailyGiftThreshold {
		return nil, ErrActivityNotReady
	}
	keyHash := hashActivityKey(idempotencyKey)
	if keyHash != nil {
		var existing DailyActivityReward
		var period sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records WHERE user_id=$1 AND idempotency_key_hash=$2`, userID, *keyHash).Scan(&existing.ID, &existing.ActivityType, &existing.Amount, &period, &existing.Source, &existing.CreatedAt)
		if err == nil {
			if period.Valid {
				existing.PeriodDate = &period.String
			}
			return &existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	cents, err := randomCents(int64(math.Round(cfg.DailyGiftMinReward*100)), int64(math.Round(cfg.DailyGiftMaxReward*100)))
	if err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO activity_reward_records(user_id,activity_type,amount,period_date,source,idempotency_key_hash,rule_version) VALUES($1,$2,$3,$4,'daily_recharge',$5,$6) ON CONFLICT(user_id,activity_type,period_date) WHERE activity_type='daily_gift' AND status <> 'failed' DO NOTHING RETURNING id`, userID, activityDailyGift, float64(cents)/100, date, keyHash, activityRuleVersion).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrActivityNotReady
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, float64(cents)/100, userID); err != nil {
		return nil, err
	}
	var reward DailyActivityReward
	var period sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records WHERE id=$1`, id).Scan(&reward.ID, &reward.ActivityType, &reward.Amount, &period, &reward.Source, &reward.CreatedAt); err != nil {
		return nil, err
	}
	if period.Valid {
		reward.PeriodDate = &period.String
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateBalanceCache(ctx, userID)
	return &reward, nil
}

func (s *DailyActivityService) Draw(ctx context.Context, userID int64, activityType string, count int, idempotencyKey string, now time.Time) ([]DailyActivityReward, error) {
	cfg := s.config(ctx)
	if !cfg.Enabled {
		return nil, ErrActivityDisabled
	}
	if count < 1 || count > 50 {
		return nil, fmt.Errorf("draw count must be between 1 and 50")
	}
	activityType, err := normalizeActivityType(activityType, false)
	if err != nil {
		return nil, err
	}
	if err := s.SyncCredits(ctx, userID, now); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keyHash := hashActivityKey(idempotencyKey)
	var requestID int64
	if keyHash != nil {
		err = tx.QueryRowContext(ctx, `INSERT INTO activity_draw_requests(user_id,activity_type,idempotency_key_hash,count) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,activity_type,idempotency_key_hash) DO NOTHING RETURNING id`, userID, activityType, *keyHash, count).Scan(&requestID)
		if errors.Is(err, sql.ErrNoRows) {
			var existingCount int
			if err = tx.QueryRowContext(ctx, `SELECT id,count FROM activity_draw_requests WHERE user_id=$1 AND activity_type=$2 AND idempotency_key_hash=$3`, userID, activityType, *keyHash).Scan(&requestID, &existingCount); err != nil {
				return nil, err
			}
			if existingCount != count {
				return nil, fmt.Errorf("idempotency key was already used with a different draw count")
			}
			rows, queryErr := tx.QueryContext(ctx, `SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records WHERE user_id=$1 AND metadata->>'draw_request_id'=$2 ORDER BY id`, userID, fmt.Sprint(requestID))
			if queryErr != nil {
				return nil, queryErr
			}
			defer rows.Close()
			var existing []DailyActivityReward
			for rows.Next() {
				var reward DailyActivityReward
				var period sql.NullString
				if scanErr := rows.Scan(&reward.ID, &reward.ActivityType, &reward.Amount, &period, &reward.Source, &reward.CreatedAt); scanErr != nil {
					return nil, scanErr
				}
				if period.Valid {
					reward.PeriodDate = &period.String
				}
				existing = append(existing, reward)
			}
			if err = rows.Err(); err != nil {
				return nil, err
			}
			if len(existing) == 0 {
				return nil, fmt.Errorf("idempotent draw result is unavailable")
			}
			return existing, nil
		}
		if err != nil {
			return nil, err
		}
	}
	// Lock credits in order, then mark them consumed inside the same transaction.
	rows, err := tx.QueryContext(ctx, `SELECT id FROM activity_draw_credits WHERE user_id=$1 AND activity_type=$2 AND consumed_at IS NULL ORDER BY credit_index FOR UPDATE SKIP LOCKED LIMIT $3`, userID, activityType, count)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return nil, ErrActivityNoCredit
	}
	if len(ids) < count {
		return nil, ErrActivityNoCredit
	}
	results := make([]DailyActivityReward, 0, count)
	var totalReward float64
	for i := 0; i < count; i++ {
		var minC, maxC int64
		source := activityType
		switch activityType {
		case activityInviteDraw:
			minC, maxC = int64(math.Round(cfg.InviteDrawMinReward*100)), int64(math.Round(cfg.InviteDrawMaxReward*100))
		case activityRechargeDraw:
			minC, maxC = int64(math.Round(cfg.RechargeDrawMinReward*100)), int64(math.Round(cfg.RechargeDrawMaxReward*100))
		default:
			minC, maxC = int64(math.Round(cfg.ConsumptionDrawMinReward*100)), int64(math.Round(cfg.ConsumptionDrawMaxReward*100))
		}
		cents, e := randomCents(minC, maxC)
		if e != nil {
			return nil, e
		}
		totalReward += float64(cents) / 100
		var rewardID int64
		metadata := `{}`
		if keyHash != nil {
			metadata = fmt.Sprintf(`{"draw_request_id":%d}`, requestID)
		}
		if err = tx.QueryRowContext(ctx, `INSERT INTO activity_reward_records(user_id,activity_type,amount,source,idempotency_key_hash,rule_version,metadata) VALUES($1,$2,$3,$4,NULL,$5,$6::jsonb) RETURNING id`, userID, activityType, float64(cents)/100, source, activityRuleVersion, metadata).Scan(&rewardID); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE activity_draw_credits SET consumed_at=NOW() WHERE id=$1 AND consumed_at IS NULL`, ids[i]); err != nil {
			return nil, err
		}
		var reward DailyActivityReward
		if err = tx.QueryRowContext(ctx, `SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records WHERE id=$1`, rewardID).Scan(&reward.ID, &reward.ActivityType, &reward.Amount, &sql.NullString{}, &reward.Source, &reward.CreatedAt); err != nil {
			return nil, err
		}
		results = append(results, reward)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, totalReward, userID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	s.invalidateBalanceCache(ctx, userID)
	return results, nil
}

func (s *DailyActivityService) Rewards(ctx context.Context, userID int64, page, pageSize int, activityType string) (*DailyActivityRewardsPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	offset := (page - 1) * pageSize
	activityType, err := normalizeActivityType(activityType, true)
	if err != nil {
		return nil, err
	}
	whereType := ""
	args := []any{userID}
	if activityType != "" {
		whereType = " AND activity_type=$2"
		args = append(args, activityType)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_reward_records WHERE user_id=$1 AND status='credited'`+whereType, args...).Scan(&total); err != nil {
		return nil, err
	}
	args = append(args, pageSize, offset)
	limitPos, offsetPos := 2, 3
	if activityType != "" {
		limitPos, offsetPos = 3, 4
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT id,activity_type,amount,period_date::text,source,created_at FROM activity_reward_records WHERE user_id=$1 AND status='credited'%s ORDER BY created_at DESC,id DESC LIMIT $%d OFFSET $%d`, whereType, limitPos, offsetPos), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DailyActivityReward, 0, pageSize)
	for rows.Next() {
		var r DailyActivityReward
		var p sql.NullString
		if err = rows.Scan(&r.ID, &r.ActivityType, &r.Amount, &p, &r.Source, &r.CreatedAt); err != nil {
			return nil, err
		}
		if p.Valid {
			r.PeriodDate = &p.String
		}
		items = append(items, r)
	}
	return &DailyActivityRewardsPage{Items: items, Total: total, Page: page, PageSize: pageSize}, rows.Err()
}
