package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
)

const shanghaiTimezone = "Asia/Shanghai"

type activityConfig struct {
	Enabled                   bool    `json:"enabled"`
	DailyGiftThreshold        float64 `json:"daily_gift_threshold"`
	RechargeDrawThreshold     float64 `json:"recharge_draw_threshold"`
	ConsumptionDrawThreshold  float64 `json:"consumption_draw_threshold"`
	InviteQualificationAmount float64 `json:"invite_qualification_amount"`
	InviteDrawRequiredCount   int64   `json:"invite_draw_required_count"`
}

type creditState struct {
	issued int64
	next   int64
	min    sql.NullInt64
}

type entitlementRow struct {
	userID int64

	targetRechargeAmount float64
	targetRechargeEarned int64
	rechargeEntitled     int64
	recharge             creditState

	targetSpendAmount float64
	targetSpendEarned int64
	spendEntitled     int64
	spend             creditState

	targetInviteCrossings int64
	inviteEntitled        int64
	invite                creditState

	giftClaimed bool
}

type milestoneRow struct {
	inviterID        int64
	inviteeID        int64
	qualifyingAmount float64
	orderID          sql.NullInt64
	qualifiedAt      time.Time
}

type plan struct {
	Mode                     string `json:"mode"`
	TargetDate               string `json:"target_date"`
	WindowStartUTC           string `json:"window_start_utc"`
	WindowEndUTC             string `json:"window_end_utc"`
	EffectiveStartUTC        string `json:"effective_start_utc"`
	ActivityStartedAtUTC     string `json:"activity_started_at_utc"`
	ActivityConfigUpdatedUTC string `json:"activity_config_updated_at_utc"`
	ActivityConfigSHA256     string `json:"activity_config_sha256"`
	ConfigUpdatedAfterTarget bool   `json:"config_updated_after_target"`

	UsersWithRecharge             int64   `json:"users_with_recharge"`
	RechargeAmountTotal           float64 `json:"recharge_amount_total"`
	RechargeEarnedDraws           int64   `json:"recharge_earned_draws"`
	RechargeEntitledThroughTarget int64   `json:"recharge_entitled_through_target"`
	RechargeAlreadyIssued         int64   `json:"recharge_already_issued"`
	RechargeToGrant               int64   `json:"recharge_to_grant"`

	UsersWithConsumption             int64   `json:"users_with_consumption"`
	ConsumptionAmountTotal           float64 `json:"consumption_amount_total"`
	ConsumptionEarnedDraws           int64   `json:"consumption_earned_draws"`
	ConsumptionEntitledThroughTarget int64   `json:"consumption_entitled_through_target"`
	ConsumptionAlreadyIssued         int64   `json:"consumption_already_issued"`
	ConsumptionToGrant               int64   `json:"consumption_to_grant"`

	InviteesCrossedThreshold     int64 `json:"invitees_crossed_threshold"`
	InvitationMilestonesToInsert int64 `json:"invitation_milestones_to_insert"`
	InviteEntitledThroughTarget  int64 `json:"invite_entitled_through_target"`
	InviteAlreadyIssued          int64 `json:"invite_already_issued"`
	InvitersEarningNewDraws      int64 `json:"inviters_earning_new_draws"`
	InviteToGrant                int64 `json:"invite_to_grant"`

	DailyGiftQualifiedUsers int64  `json:"daily_gift_qualified_users"`
	DailyGiftClaimedUsers   int64  `json:"daily_gift_claimed_users"`
	DailyGiftUnclaimedUsers int64  `json:"daily_gift_unclaimed_users"`
	UnexpectedCount         int64  `json:"unexpected_count"`
	PlanSHA256              string `json:"plan_sha256"`
}

type applyResult struct {
	Mode                         string `json:"mode"`
	TargetDate                   string `json:"target_date"`
	AppliedPlanSHA256            string `json:"applied_plan_sha256"`
	RechargeGranted              int64  `json:"recharge_granted"`
	ConsumptionGranted           int64  `json:"consumption_granted"`
	InviteGranted                int64  `json:"invite_granted"`
	InvitationMilestonesInserted int64  `json:"invitation_milestones_inserted"`
	PostcheckToGrant             int64  `json:"postcheck_to_grant"`
	PostcheckUnexpectedCount     int64  `json:"postcheck_unexpected_count"`
	PostcheckPlanSHA256          string `json:"postcheck_plan_sha256"`
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func main() {
	targetRaw := flag.String("target-date", "", "Asia/Shanghai activity date (YYYY-MM-DD)")
	apply := flag.Bool("apply", false, "insert missing permanent draw credits")
	expectedPlan := flag.String("plan-sha256", "", "required dry-run plan checksum for apply")
	flag.Parse()

	loc, err := time.LoadLocation(shanghaiTimezone)
	if err != nil {
		log.Fatalf("load activity timezone: %v", err)
	}
	targetDay, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(*targetRaw), loc)
	if err != nil {
		log.Fatalf("invalid --target-date: %v", err)
	}
	currentDay := time.Now().In(loc).Format("2006-01-02")
	if targetDay.Format("2006-01-02") >= currentDay {
		log.Fatal("target date must be a completed Asia/Shanghai activity day")
	}

	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	client, db, err := repository.InitEnt(cfg)
	if err != nil {
		log.Fatalf("initialize database: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if !*apply {
		built, _, _, err := buildPlan(ctx, db, targetDay)
		if err != nil {
			log.Fatalf("build dry-run plan: %v", err)
		}
		writeJSON(built)
		return
	}
	if len(strings.TrimSpace(*expectedPlan)) != sha256.Size*2 {
		log.Fatal("--plan-sha256 is required for apply")
	}
	result, err := applyPlan(ctx, db, targetDay, strings.ToLower(strings.TrimSpace(*expectedPlan)))
	if err != nil {
		log.Fatalf("apply backfill: %v", err)
	}
	writeJSON(result)
}

func writeJSON(value any) {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		log.Fatalf("encode result: %v", err)
	}
}

func loadActivitySettings(ctx context.Context, q queryer) (activityConfig, string, time.Time, time.Time, error) {
	var raw, startedRaw string
	var configUpdatedAt time.Time
	if err := q.QueryRowContext(ctx, `
SELECT COALESCE((SELECT value FROM settings WHERE key='daily_activity_config'), ''),
       COALESCE((SELECT updated_at FROM settings WHERE key='daily_activity_config'), TO_TIMESTAMP(0)),
       COALESCE((SELECT value FROM settings WHERE key='daily_activity_started_at'), '')`).Scan(&raw, &configUpdatedAt, &startedRaw); err != nil {
		return activityConfig{}, "", time.Time{}, time.Time{}, err
	}
	var cfg activityConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, "", time.Time{}, time.Time{}, fmt.Errorf("parse daily activity config: %w", err)
	}
	if !cfg.Enabled {
		return cfg, "", time.Time{}, time.Time{}, errors.New("daily activities are disabled")
	}
	if cfg.DailyGiftThreshold <= 0 || cfg.RechargeDrawThreshold <= 0 || cfg.ConsumptionDrawThreshold <= 0 || cfg.InviteQualificationAmount <= 0 || cfg.InviteDrawRequiredCount <= 0 {
		return cfg, "", time.Time{}, time.Time{}, errors.New("invalid daily activity thresholds")
	}
	for _, value := range []float64{cfg.DailyGiftThreshold, cfg.RechargeDrawThreshold, cfg.ConsumptionDrawThreshold, cfg.InviteQualificationAmount} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return cfg, "", time.Time{}, time.Time{}, errors.New("non-finite daily activity threshold")
		}
	}
	epoch, err := strconv.ParseFloat(strings.TrimSpace(startedRaw), 64)
	if err != nil || epoch <= 0 || math.IsNaN(epoch) || math.IsInf(epoch, 0) {
		return cfg, "", time.Time{}, time.Time{}, errors.New("daily activity start time unavailable")
	}
	seconds, fraction := math.Modf(epoch)
	startedAt := time.Unix(int64(seconds), int64(fraction*float64(time.Second))).UTC()
	digest := sha256.Sum256([]byte(raw))
	return cfg, hex.EncodeToString(digest[:]), startedAt, configUpdatedAt.UTC(), nil
}

func rechargeEventsSQL() string {
	return `
SELECT po.user_id, po.amount, po.paid_at AS event_at, 1::integer AS source_rank,
       po.id AS source_id, po.id AS qualifying_order_id
FROM payment_orders po
WHERE po.order_type IN ('balance','subscription')
  AND po.status='COMPLETED' AND po.paid_at IS NOT NULL
  AND po.paid_at >= $1 AND po.paid_at < $2
UNION ALL
SELECT rc.used_by AS user_id, rc.value AS amount, rc.used_at AS event_at, 2::integer AS source_rank,
       rc.id AS source_id, NULL::bigint AS qualifying_order_id
FROM redeem_codes rc
WHERE rc.used_by IS NOT NULL AND rc.type='balance' AND rc.status='used'
  AND rc.value > 0 AND rc.used_at IS NOT NULL
  AND rc.used_at >= $1 AND rc.used_at < $2
  AND NOT EXISTS (
    SELECT 1 FROM payment_orders po
    WHERE po.recharge_code=rc.code
      AND po.order_type IN ('balance','subscription')
  )
UNION ALL
SELECT are.user_id, are.amount, are.occurred_at AS event_at, 3::integer AS source_rank,
       are.id AS source_id, NULL::bigint AS qualifying_order_id
FROM activity_recharge_events are
WHERE are.source_type='admin_balance_add' AND are.amount > 0
  AND are.occurred_at >= $1 AND are.occurred_at < $2`
}

func entitlementSQL() string {
	return fmt.Sprintf(`
WITH recharge_events AS (%s),
recharge_daily AS (
  SELECT user_id, (event_at AT TIME ZONE 'Asia/Shanghai')::date AS activity_date, SUM(amount) AS amount
  FROM recharge_events GROUP BY user_id, activity_date
),
recharge_totals AS (
  SELECT user_id,
         COALESCE(SUM(amount) FILTER (WHERE activity_date=($3 AT TIME ZONE 'Asia/Shanghai')::date),0) AS target_amount,
         COALESCE(SUM(FLOOR(amount/$4)) FILTER (WHERE activity_date=($3 AT TIME ZONE 'Asia/Shanghai')::date),0)::bigint AS target_earned,
         COALESCE(SUM(FLOOR(amount/$4)),0)::bigint AS entitled
  FROM recharge_daily GROUP BY user_id
),
spend_daily AS (
  SELECT user_id, (created_at AT TIME ZONE 'Asia/Shanghai')::date AS activity_date,
         SUM(GREATEST(actual_cost,0)) AS amount
  FROM usage_logs
  WHERE created_at >= $1 AND created_at < $2
  GROUP BY user_id, activity_date
),
spend_totals AS (
  SELECT user_id,
         COALESCE(SUM(amount) FILTER (WHERE activity_date=($3 AT TIME ZONE 'Asia/Shanghai')::date),0) AS target_amount,
         COALESCE(SUM(FLOOR(amount/$5)) FILTER (WHERE activity_date=($3 AT TIME ZONE 'Asia/Shanghai')::date),0)::bigint AS target_earned,
         COALESCE(SUM(FLOOR(amount/$5)),0)::bigint AS entitled
  FROM spend_daily GROUP BY user_id
),
invitation_running AS (
  SELECT ua.inviter_id, ua.user_id AS invitee_id, re.event_at, re.source_rank, re.source_id,
         SUM(re.amount) OVER (
           PARTITION BY ua.user_id
           ORDER BY re.event_at, re.source_rank, re.source_id
           ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
         ) AS running_amount
  FROM user_affiliates ua
  JOIN recharge_events re ON re.user_id=ua.user_id
  WHERE ua.inviter_id IS NOT NULL
),
invitation_crossings AS (
  SELECT DISTINCT ON (invitee_id) inviter_id, invitee_id, event_at
  FROM invitation_running
  WHERE running_amount >= $6
  ORDER BY invitee_id, event_at, source_rank, source_id
),
invite_totals AS (
  SELECT inviter_id AS user_id,
         COUNT(*) FILTER (WHERE event_at >= $3 AND event_at < $2)::bigint AS target_crossings,
         FLOOR(COUNT(*)/$7)::bigint AS entitled
  FROM invitation_crossings GROUP BY inviter_id
),
credit_stats AS (
  SELECT user_id, activity_type, COUNT(*)::bigint AS issued,
         COALESCE(MAX(credit_index)+1,0)::bigint AS next_index,
         MIN(credit_index)::bigint AS min_index
  FROM activity_draw_credits GROUP BY user_id, activity_type
),
gift_claims AS (
  SELECT user_id, TRUE AS claimed
  FROM activity_reward_records
  WHERE activity_type='daily_gift'
    AND period_date=($3 AT TIME ZONE 'Asia/Shanghai')::date
    AND status='credited'
  GROUP BY user_id
),
affected_users AS (
  SELECT user_id FROM recharge_totals
  UNION SELECT user_id FROM spend_totals
  UNION SELECT user_id FROM invite_totals
  UNION SELECT user_id FROM credit_stats
)
SELECT u.user_id,
       COALESCE(rt.target_amount,0), COALESCE(rt.target_earned,0), COALESCE(rt.entitled,0),
       COALESCE(cr.issued,0), COALESCE(cr.next_index,0), cr.min_index,
       COALESCE(st.target_amount,0), COALESCE(st.target_earned,0), COALESCE(st.entitled,0),
       COALESCE(cs.issued,0), COALESCE(cs.next_index,0), cs.min_index,
       COALESCE(it.target_crossings,0), COALESCE(it.entitled,0),
       COALESCE(ci.issued,0), COALESCE(ci.next_index,0), ci.min_index,
       COALESCE(gc.claimed,FALSE)
FROM affected_users u
LEFT JOIN recharge_totals rt ON rt.user_id=u.user_id
LEFT JOIN spend_totals st ON st.user_id=u.user_id
LEFT JOIN invite_totals it ON it.user_id=u.user_id
LEFT JOIN credit_stats cr ON cr.user_id=u.user_id AND cr.activity_type='recharge_draw'
LEFT JOIN credit_stats cs ON cs.user_id=u.user_id AND cs.activity_type='spend_draw'
LEFT JOIN credit_stats ci ON ci.user_id=u.user_id AND ci.activity_type='invite_draw'
LEFT JOIN gift_claims gc ON gc.user_id=u.user_id
ORDER BY u.user_id`, rechargeEventsSQL())
}

func missingMilestonesSQL() string {
	return fmt.Sprintf(`
WITH recharge_events AS (%s),
invitation_running AS (
  SELECT ua.inviter_id, ua.user_id AS invitee_id, re.event_at, re.source_rank, re.source_id,
         re.qualifying_order_id,
         SUM(re.amount) OVER (
           PARTITION BY ua.user_id
           ORDER BY re.event_at, re.source_rank, re.source_id
           ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW
         ) AS running_amount
  FROM user_affiliates ua
  JOIN recharge_events re ON re.user_id=ua.user_id
  WHERE ua.inviter_id IS NOT NULL
),
crossings AS (
  SELECT DISTINCT ON (invitee_id) inviter_id, invitee_id, running_amount,
         qualifying_order_id, event_at
  FROM invitation_running
  WHERE running_amount >= $4
  ORDER BY invitee_id, event_at, source_rank, source_id
)
SELECT c.inviter_id, c.invitee_id, c.running_amount, c.qualifying_order_id, c.event_at
FROM crossings c
LEFT JOIN activity_invitation_milestones m
  ON m.inviter_id=c.inviter_id AND m.invitee_id=c.invitee_id
WHERE m.id IS NULL
ORDER BY c.inviter_id, c.invitee_id`, rechargeEventsSQL())
}

func buildPlan(ctx context.Context, q queryer, targetDay time.Time) (plan, []entitlementRow, []milestoneRow, error) {
	cfg, configHash, startedAt, configUpdatedAt, err := loadActivitySettings(ctx, q)
	if err != nil {
		return plan{}, nil, nil, err
	}
	targetStart := targetDay.UTC()
	targetEnd := targetDay.AddDate(0, 0, 1).UTC()
	if !startedAt.Before(targetEnd) {
		return plan{}, nil, nil, errors.New("daily activity started after target window")
	}

	rows, err := q.QueryContext(ctx, entitlementSQL(), startedAt, targetEnd, targetStart,
		cfg.RechargeDrawThreshold, cfg.ConsumptionDrawThreshold,
		cfg.InviteQualificationAmount, cfg.InviteDrawRequiredCount)
	if err != nil {
		return plan{}, nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	entitlements := make([]entitlementRow, 0)
	for rows.Next() {
		var item entitlementRow
		if err := rows.Scan(
			&item.userID,
			&item.targetRechargeAmount, &item.targetRechargeEarned, &item.rechargeEntitled,
			&item.recharge.issued, &item.recharge.next, &item.recharge.min,
			&item.targetSpendAmount, &item.targetSpendEarned, &item.spendEntitled,
			&item.spend.issued, &item.spend.next, &item.spend.min,
			&item.targetInviteCrossings, &item.inviteEntitled,
			&item.invite.issued, &item.invite.next, &item.invite.min,
			&item.giftClaimed,
		); err != nil {
			return plan{}, nil, nil, err
		}
		entitlements = append(entitlements, item)
	}
	if err := rows.Err(); err != nil {
		return plan{}, nil, nil, err
	}

	milestoneRows, err := q.QueryContext(ctx, missingMilestonesSQL(), startedAt, targetEnd, targetStart, cfg.InviteQualificationAmount)
	if err != nil {
		return plan{}, nil, nil, err
	}
	defer func() { _ = milestoneRows.Close() }()
	milestones := make([]milestoneRow, 0)
	for milestoneRows.Next() {
		var item milestoneRow
		if err := milestoneRows.Scan(&item.inviterID, &item.inviteeID, &item.qualifyingAmount, &item.orderID, &item.qualifiedAt); err != nil {
			return plan{}, nil, nil, err
		}
		milestones = append(milestones, item)
	}
	if err := milestoneRows.Err(); err != nil {
		return plan{}, nil, nil, err
	}

	built := summarize(targetDay, startedAt, configUpdatedAt, configHash, cfg, entitlements, milestones)
	return built, entitlements, milestones, nil
}

func summarize(targetDay, startedAt, configUpdatedAt time.Time, configHash string, cfg activityConfig, rows []entitlementRow, milestones []milestoneRow) plan {
	targetStart := targetDay.UTC()
	targetEnd := targetDay.AddDate(0, 0, 1).UTC()
	effectiveStart := startedAt
	if effectiveStart.Before(targetStart) {
		effectiveStart = targetStart
	}
	built := plan{
		Mode:                         "dry-run",
		TargetDate:                   targetDay.Format("2006-01-02"),
		WindowStartUTC:               targetStart.Format(time.RFC3339),
		WindowEndUTC:                 targetEnd.Format(time.RFC3339),
		EffectiveStartUTC:            effectiveStart.Format(time.RFC3339),
		ActivityStartedAtUTC:         startedAt.Format(time.RFC3339Nano),
		ActivityConfigUpdatedUTC:     configUpdatedAt.Format(time.RFC3339Nano),
		ActivityConfigSHA256:         configHash,
		ConfigUpdatedAfterTarget:     configUpdatedAt.After(targetEnd),
		InvitationMilestonesToInsert: int64(len(milestones)),
	}
	if built.ConfigUpdatedAfterTarget {
		built.UnexpectedCount++
	}
	for _, item := range rows {
		if item.targetRechargeAmount > 0 {
			built.UsersWithRecharge++
			built.RechargeAmountTotal += item.targetRechargeAmount
		}
		if item.targetSpendAmount > 0 {
			built.UsersWithConsumption++
			built.ConsumptionAmountTotal += item.targetSpendAmount
		}
		built.RechargeEarnedDraws += item.targetRechargeEarned
		built.RechargeEntitledThroughTarget += item.rechargeEntitled
		built.RechargeAlreadyIssued += item.recharge.issued
		built.RechargeToGrant += missing(item.rechargeEntitled, item.recharge.issued)
		built.ConsumptionEarnedDraws += item.targetSpendEarned
		built.ConsumptionEntitledThroughTarget += item.spendEntitled
		built.ConsumptionAlreadyIssued += item.spend.issued
		built.ConsumptionToGrant += missing(item.spendEntitled, item.spend.issued)
		built.InviteesCrossedThreshold += item.targetInviteCrossings
		built.InviteEntitledThroughTarget += item.inviteEntitled
		built.InviteAlreadyIssued += item.invite.issued
		inviteMissing := missing(item.inviteEntitled, item.invite.issued)
		built.InviteToGrant += inviteMissing
		if inviteMissing > 0 {
			built.InvitersEarningNewDraws++
		}
		built.UnexpectedCount += creditStateAnomalies(item.recharge) + creditStateAnomalies(item.spend) + creditStateAnomalies(item.invite)
		if item.targetRechargeAmount >= cfg.DailyGiftThreshold {
			built.DailyGiftQualifiedUsers++
			if item.giftClaimed {
				built.DailyGiftClaimedUsers++
			}
		}
	}
	built.DailyGiftUnclaimedUsers = built.DailyGiftQualifiedUsers - built.DailyGiftClaimedUsers
	built.PlanSHA256 = checksumPlan(built, rows, milestones)
	return built
}

func missing(entitled, issued int64) int64 {
	if entitled > issued {
		return entitled - issued
	}
	return 0
}

func creditStateAnomalies(state creditState) int64 {
	if state.issued == 0 {
		if state.next != 0 || state.min.Valid {
			return 1
		}
		return 0
	}
	if !state.min.Valid || state.min.Int64 != 0 || state.next != state.issued {
		return 1
	}
	return 0
}

func checksumPlan(built plan, rows []entitlementRow, milestones []milestoneRow) string {
	built.PlanSHA256 = ""
	encoded, _ := json.Marshal(built)
	hash := sha256.New()
	_, _ = hash.Write(encoded)
	rowEvidence := make([]string, 0, len(rows))
	for _, item := range rows {
		rowEvidence = append(rowEvidence, fmt.Sprintf(
			"%d|%.8f|%d|%d|%d|%d|%t|%d|%.8f|%d|%d|%d|%d|%t|%d|%d|%d|%d|%d|%t|%d|%t",
			item.userID,
			item.targetRechargeAmount, item.targetRechargeEarned, item.rechargeEntitled, item.recharge.issued, item.recharge.next, item.recharge.min.Valid, item.recharge.min.Int64,
			item.targetSpendAmount, item.targetSpendEarned, item.spendEntitled, item.spend.issued, item.spend.next, item.spend.min.Valid, item.spend.min.Int64,
			item.targetInviteCrossings, item.inviteEntitled, item.invite.issued, item.invite.next, item.invite.min.Valid, item.invite.min.Int64, item.giftClaimed,
		))
	}
	sort.Strings(rowEvidence)
	for _, value := range rowEvidence {
		_, _ = hash.Write([]byte("\nrow:" + value))
	}
	milestoneEvidence := make([]string, 0, len(milestones))
	for _, item := range milestones {
		orderID := int64(0)
		if item.orderID.Valid {
			orderID = item.orderID.Int64
		}
		milestoneEvidence = append(milestoneEvidence, fmt.Sprintf("%d|%d|%.8f|%d|%s", item.inviterID, item.inviteeID, item.qualifyingAmount, orderID, item.qualifiedAt.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(milestoneEvidence)
	for _, value := range milestoneEvidence {
		_, _ = hash.Write([]byte("\nmilestone:" + value))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func applyPlan(ctx context.Context, db *sql.DB, targetDay time.Time, expectedPlan string) (*applyResult, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE activity_draw_credits, activity_invitation_milestones IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, err
	}
	built, rows, milestones, err := buildPlan(ctx, tx, targetDay)
	if err != nil {
		return nil, err
	}
	if built.UnexpectedCount != 0 {
		return nil, fmt.Errorf("dry-run has %d unexpected condition(s)", built.UnexpectedCount)
	}
	if built.PlanSHA256 != expectedPlan {
		return nil, errors.New("plan checksum mismatch; run dry-run again")
	}

	var milestonesInserted int64
	for _, item := range milestones {
		result, err := tx.ExecContext(ctx, `
INSERT INTO activity_invitation_milestones(inviter_id,invitee_id,qualifying_amount,qualifying_order_id,qualified_at)
VALUES($1,$2,$3,$4,$5)
ON CONFLICT (inviter_id,invitee_id) DO NOTHING`, item.inviterID, item.inviteeID, item.qualifyingAmount, nullableInt64(item.orderID), item.qualifiedAt)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		milestonesInserted += affected
	}
	if milestonesInserted != int64(len(milestones)) {
		return nil, errors.New("invitation milestone plan drifted during apply")
	}

	source := "backfill:" + targetDay.Format("2006-01-02")
	var rechargeGranted, spendGranted, inviteGranted int64
	for _, item := range rows {
		for _, target := range []struct {
			activityType string
			count        int64
			next         int64
			granted      *int64
		}{
			{activityType: "recharge_draw", count: missing(item.rechargeEntitled, item.recharge.issued), next: item.recharge.next, granted: &rechargeGranted},
			{activityType: "spend_draw", count: missing(item.spendEntitled, item.spend.issued), next: item.spend.next, granted: &spendGranted},
			{activityType: "invite_draw", count: missing(item.inviteEntitled, item.invite.issued), next: item.invite.next, granted: &inviteGranted},
		} {
			for offset := int64(0); offset < target.count; offset++ {
				result, err := tx.ExecContext(ctx, `
INSERT INTO activity_draw_credits(user_id,activity_type,credit_index,source)
VALUES($1,$2,$3,$4)
ON CONFLICT (user_id,activity_type,credit_index) DO NOTHING`, item.userID, target.activityType, target.next+offset, source)
				if err != nil {
					return nil, err
				}
				affected, err := result.RowsAffected()
				if err != nil {
					return nil, err
				}
				if affected != 1 {
					return nil, errors.New("draw credit plan drifted during apply")
				}
				*target.granted = *target.granted + 1
			}
		}
	}

	postcheck, _, _, err := buildPlan(ctx, tx, targetDay)
	if err != nil {
		return nil, err
	}
	remaining := postcheck.RechargeToGrant + postcheck.ConsumptionToGrant + postcheck.InviteToGrant
	if remaining != 0 || postcheck.UnexpectedCount != 0 {
		return nil, fmt.Errorf("postcheck failed: remaining=%d unexpected=%d", remaining, postcheck.UnexpectedCount)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &applyResult{
		Mode:                         "apply",
		TargetDate:                   targetDay.Format("2006-01-02"),
		AppliedPlanSHA256:            expectedPlan,
		RechargeGranted:              rechargeGranted,
		ConsumptionGranted:           spendGranted,
		InviteGranted:                inviteGranted,
		InvitationMilestonesInserted: milestonesInserted,
		PostcheckToGrant:             remaining,
		PostcheckUnexpectedCount:     postcheck.UnexpectedCount,
		PostcheckPlanSHA256:          postcheck.PlanSHA256,
	}, nil
}

func nullableInt64(value sql.NullInt64) any {
	if value.Valid {
		return value.Int64
	}
	return nil
}
