package service

import (
	"context"
	"database/sql"
	"errors"
)

// activityRewardCostSQL copies the authoritative NUMERIC amount and timestamp
// directly in PostgreSQL. A conflicting source must match the complete audit
// snapshot; no returned row means the caller must roll back the award.
const activityRewardCostSQL = `INSERT INTO extra_cost_entries
    (cost_date, amount, category, notes, created_by, created_at, reversal_of,
     idempotency_key, rule_version, activity_reward_id, related_user_id, activity_type)
SELECT (r.created_at AT TIME ZONE 'Asia/Shanghai')::date, r.amount,
       'activity_reward', '', NULL, r.created_at, NULL,
       'activity-reward:' || r.id::text, 'activity-reward-cost-v1', r.id, r.user_id, r.activity_type
FROM activity_reward_records r
WHERE r.id = $1 AND r.user_id = $2 AND r.status = 'credited'
ON CONFLICT (activity_reward_id) DO UPDATE
SET activity_reward_id = EXCLUDED.activity_reward_id
WHERE extra_cost_entries.amount = EXCLUDED.amount
  AND extra_cost_entries.related_user_id = EXCLUDED.related_user_id
  AND extra_cost_entries.activity_type = EXCLUDED.activity_type
  AND extra_cost_entries.cost_date = EXCLUDED.cost_date
  AND extra_cost_entries.created_at = EXCLUDED.created_at
  AND extra_cost_entries.category = EXCLUDED.category
  AND extra_cost_entries.rule_version = EXCLUDED.rule_version
  AND extra_cost_entries.idempotency_key = EXCLUDED.idempotency_key
  AND extra_cost_entries.created_by IS NULL
  AND extra_cost_entries.reversal_of IS NULL
RETURNING id`

func recordActivityRewardCost(ctx context.Context, tx *sql.Tx, rewardID, userID int64) error {
	var costID int64
	err := tx.QueryRowContext(ctx, activityRewardCostSQL, rewardID, userID).Scan(&costID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrExtraCostIdempotencyConflict
	}
	return err
}

// creditActivityRewardBalance requires a live recipient. An UPDATE which
// matched no row must not commit a reward and cost without crediting balance.
func creditActivityRewardBalance(ctx context.Context, tx *sql.Tx, userID int64, amount float64) error {
	result, err := tx.ExecContext(ctx, `UPDATE users SET balance=balance+$1,updated_at=NOW() WHERE id=$2 AND deleted_at IS NULL`, amount, userID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrUserNotFound
	}
	return nil
}

// BackfillActivityRewardCosts appends missing costs without updating balances.
// The migration-owned function also validates existing source snapshots and
// fails the transaction on a conflicting association. Operators run this again
// after old writers drain during a rolling release.
func BackfillActivityRewardCosts(ctx context.Context, db *sql.DB) (int64, error) {
	var inserted int64
	err := db.QueryRowContext(ctx, `SELECT backfill_activity_reward_costs()`).Scan(&inserted)
	return inserted, err
}
