//go:build integration

package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This fixture only accepts an explicitly isolated database and creates its
// own schema. Embedded production migrations make the cross-compiled test
// binary independent of its launch directory and of external SQL files.
func newActivityRewardCostFixture(t *testing.T) (*sql.DB, *DailyActivityService) {
	t.Helper()
	dsn := os.Getenv("ACTIVITY_COST_TEST_DSN")
	if dsn == "" {
		t.Skip("ACTIVITY_COST_TEST_DSN is required for isolated PostgreSQL validation")
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid ACTIVITY_COST_TEST_DSN URL")
	}
	require.True(t, parsed.Scheme == "postgres" || parsed.Scheme == "postgresql", "integration DSN must be a PostgreSQL URL")
	adminDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDB.Close() })
	var database string
	err = adminDB.QueryRow(`SELECT current_database()`).Scan(&database)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(database, "codex_activity_cost_"), "refusing non-isolated database")
	schema := fmt.Sprintf("activity_cost_%d", time.Now().UnixNano())
	_, err = adminDB.Exec(`CREATE SCHEMA ` + schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := adminDB.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		if cleanupErr != nil {
			t.Errorf("clean isolated activity schema: %v", cleanupErr)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("postgres", parsed.String())
	require.NoError(t, err)
	db.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
CREATE TABLE users (id BIGSERIAL PRIMARY KEY, balance NUMERIC(20,8) NOT NULL DEFAULT 0, deleted_at TIMESTAMPTZ, updated_at TIMESTAMPTZ DEFAULT NOW());
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TIMESTAMPTZ DEFAULT NOW());
CREATE TABLE payment_orders (user_id BIGINT, amount NUMERIC(20,8), order_type TEXT, status TEXT, paid_at TIMESTAMPTZ, recharge_code TEXT);
CREATE TABLE redeem_codes (used_by BIGINT, type TEXT, status TEXT, value NUMERIC(20,8), used_at TIMESTAMPTZ, code TEXT);
CREATE TABLE usage_logs (user_id BIGINT, actual_cost NUMERIC(20,8), created_at TIMESTAMPTZ);
CREATE TABLE user_affiliates (user_id BIGINT PRIMARY KEY, aff_rebate_rate_percent NUMERIC);`)
	require.NoError(t, err)
	for _, name := range []string{"260_daily_activity_rewards.sql", "261_daily_activity_recharge_events.sql", "262_extra_cost_entries.sql", "289_activity_reward_costs.sql"} {
		migration, readErr := migrations.FS.ReadFile(name)
		require.NoError(t, readErr)
		_, err = db.Exec(string(migration))
		require.NoError(t, err, name)
	}
	cfg := DefaultDailyActivityConfig()
	cfg.DailyGiftMinReward, cfg.DailyGiftMaxReward = .12, .12
	cfg.RechargeDrawMinReward, cfg.RechargeDrawMaxReward = .23, .23
	cfg.ConsumptionDrawMinReward, cfg.ConsumptionDrawMaxReward = .34, .34
	cfg.InviteDrawMinReward, cfg.InviteDrawMaxReward = .45, .45
	setActivityIntegrationConfig(t, db, cfg)
	_, err = db.Exec(`UPDATE settings SET value=$1 WHERE key=$2`, strconv.FormatInt(time.Now().Add(-7*24*time.Hour).Unix(), 10), SettingKeyDailyActivityStartedAt)
	require.NoError(t, err)
	settings := &SettingService{settingRepo: &activityIntegrationSettings{db: db}, cfg: &config.Config{}}
	return db, NewDailyActivityService(db, settings)
}

type activityIntegrationSettings struct {
	SettingRepository
	db *sql.DB
}

func (r *activityIntegrationSettings) GetValue(ctx context.Context, key string) (string, error) {
	var value string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&value)
	return value, err
}

func (r *activityIntegrationSettings) GetAll(ctx context.Context) (map[string]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key,value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		values[key] = value
	}
	return values, rows.Err()
}

func setActivityIntegrationConfig(t *testing.T, db *sql.DB, cfg DailyActivityConfig) {
	t.Helper()
	encoded, err := json.Marshal(cfg)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO settings(key,value) VALUES($1,$2) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value`, SettingKeyDailyActivityConfig, string(encoded))
	require.NoError(t, err)
}

func activityIntegrationUser(t *testing.T, db *sql.DB, typ string, credits int) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&id))
	if typ == activityDailyGift {
		_, err := db.Exec(`INSERT INTO payment_orders(user_id,amount,order_type,status,paid_at) VALUES($1,20,'balance','COMPLETED',NOW())`, id)
		require.NoError(t, err)
	} else {
		for i := 0; i < credits; i++ {
			_, err := db.Exec(`INSERT INTO activity_draw_credits(user_id,activity_type,credit_index,source) VALUES($1,$2,$3,'historical')`, id, typ, i)
			require.NoError(t, err)
		}
	}
	return id
}

func activityIntegrationCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.QueryRow(query, args...).Scan(&count))
	return count
}

func assertActivityIntegrationLedger(t *testing.T, db *sql.DB, userID int64, expectedCount int64) {
	t.Helper()
	require.Equal(t, expectedCount, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_reward_records WHERE user_id=$1`, userID))
	require.Equal(t, expectedCount, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM extra_cost_entries WHERE related_user_id=$1`, userID))
	require.Zero(t, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_reward_records r LEFT JOIN extra_cost_entries c ON c.activity_reward_id=r.id WHERE r.user_id=$1 AND (c.id IS NULL OR c.amount IS DISTINCT FROM r.amount OR c.related_user_id IS DISTINCT FROM r.user_id OR c.activity_type IS DISTINCT FROM r.activity_type OR c.created_at IS DISTINCT FROM r.created_at OR c.cost_date IS DISTINCT FROM (r.created_at AT TIME ZONE 'Asia/Shanghai')::date OR c.created_by IS NOT NULL OR c.reversal_of IS NOT NULL OR c.category <> 'activity_reward' OR c.rule_version <> 'activity-reward-cost-v1' OR c.idempotency_key <> 'activity-reward:' || r.id::text)`, userID))
	var equal bool
	require.NoError(t, db.QueryRow(`SELECT balance=(SELECT COALESCE(SUM(amount),0) FROM activity_reward_records WHERE user_id=$1) FROM users WHERE id=$1`, userID).Scan(&equal))
	require.True(t, equal, "balance, award and cost ledgers must commit together")
}

func TestActivityRewardCostsPostgres(t *testing.T) {
	t.Run("four_reward_types_batch_and_replay", func(t *testing.T) {
		db, svc := newActivityRewardCostFixture(t)
		for _, typ := range []string{activityDailyGift, activityRechargeDraw, activitySpendDraw, activityInviteDraw} {
			userID := activityIntegrationUser(t, db, typ, 2)
			if typ == activityDailyGift {
				first, err := svc.OpenDailyGift(context.Background(), userID, "gift", time.Now())
				require.NoError(t, err)
				replay, err := svc.OpenDailyGift(context.Background(), userID, "gift", time.Now())
				require.NoError(t, err)
				require.Equal(t, first.ID, replay.ID)
				assertActivityIntegrationLedger(t, db, userID, 1)
			} else {
				first, err := svc.Draw(context.Background(), userID, typ, 2, "batch", time.Now())
				require.NoError(t, err)
				replay, err := svc.Draw(context.Background(), userID, typ, 2, "batch", time.Now())
				require.NoError(t, err)
				require.Equal(t, first, replay)
				assertActivityIntegrationLedger(t, db, userID, 2)
				require.Equal(t, int64(2), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND consumed_at IS NOT NULL`, userID))
			}
		}
	})
	t.Run("concurrent_same_idempotency", func(t *testing.T) {
		for _, typ := range []string{activityDailyGift, activityRechargeDraw} {
			t.Run(typ, func(t *testing.T) {
				db, svc := newActivityRewardCostFixture(t)
				userID := activityIntegrationUser(t, db, typ, 2)
				var wg sync.WaitGroup
				errors := make(chan error, 8)
				results := make(chan []DailyActivityReward, 8)
				start := make(chan struct{})
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						var rewards []DailyActivityReward
						var err error
						if typ == activityDailyGift {
							var gift *DailyActivityReward
							gift, err = svc.OpenDailyGift(context.Background(), userID, "concurrent", time.Now())
							if err == nil {
								rewards = []DailyActivityReward{*gift}
							}
						} else {
							rewards, err = svc.Draw(context.Background(), userID, typ, 2, "concurrent", time.Now())
						}
						errors <- err
						results <- rewards
					}()
				}
				close(start)
				wg.Wait()
				close(errors)
				close(results)
				for err := range errors {
					require.NoError(t, err)
				}
				var first []DailyActivityReward
				for rewards := range results {
					if first == nil {
						first = rewards
					} else {
						require.Equal(t, first, rewards)
					}
				}
				expected := int64(2)
				if typ == activityDailyGift {
					expected = 1
				}
				assertActivityIntegrationLedger(t, db, userID, expected)
				if typ != activityDailyGift {
					require.Equal(t, int64(1), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_draw_requests WHERE user_id=$1`, userID))
				}
			})
		}
	})
	t.Run("cost_failure_rolls_back_balance_and_credits", func(t *testing.T) {
		db, svc := newActivityRewardCostFixture(t)
		_, err := db.Exec(`CREATE FUNCTION reject_activity_cost() RETURNS TRIGGER LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic cost failure'; END; $$; CREATE TRIGGER reject_activity_cost BEFORE INSERT ON extra_cost_entries FOR EACH ROW EXECUTE FUNCTION reject_activity_cost();`)
		require.NoError(t, err)
		for _, typ := range []string{activityDailyGift, activityRechargeDraw, activitySpendDraw, activityInviteDraw} {
			userID := activityIntegrationUser(t, db, typ, 2)
			if typ == activityDailyGift {
				_, err = svc.OpenDailyGift(context.Background(), userID, "failure", time.Now())
			} else {
				_, err = svc.Draw(context.Background(), userID, typ, 2, "failure", time.Now())
			}
			require.ErrorContains(t, err, "synthetic cost failure")
			assertActivityIntegrationLedger(t, db, userID, 0)
			require.Zero(t, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND consumed_at IS NOT NULL`, userID))
			require.Zero(t, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_draw_requests WHERE user_id=$1`, userID))
		}
	})
	t.Run("zero_rewards_remain_in_both_ledgers", func(t *testing.T) {
		db, svc := newActivityRewardCostFixture(t)
		cfg := DefaultDailyActivityConfig()
		cfg.DailyGiftMinReward, cfg.DailyGiftMaxReward = 0, 0
		cfg.RechargeDrawMinReward, cfg.RechargeDrawMaxReward = 0, 0
		cfg.ConsumptionDrawMinReward, cfg.ConsumptionDrawMaxReward = 0, 0
		cfg.InviteDrawMinReward, cfg.InviteDrawMaxReward = 0, 0
		setActivityIntegrationConfig(t, db, cfg)
		for _, typ := range []string{activityDailyGift, activityRechargeDraw, activitySpendDraw, activityInviteDraw} {
			userID := activityIntegrationUser(t, db, typ, 1)
			if typ == activityDailyGift {
				reward, err := svc.OpenDailyGift(context.Background(), userID, "zero", time.Now())
				require.NoError(t, err)
				require.Zero(t, reward.Amount)
			} else {
				rewards, err := svc.Draw(context.Background(), userID, typ, 1, "zero", time.Now())
				require.NoError(t, err)
				require.Zero(t, rewards[0].Amount)
			}
			assertActivityIntegrationLedger(t, db, userID, 1)
		}
	})
	t.Run("historical_backfill_dates_precision_old_writer_conflicts_and_manual_preservation", testActivityCostHistoricalBackfill)
	t.Run("soft_deleted_recipient_rolls_back_live_awards", func(t *testing.T) {
		db, svc := newActivityRewardCostFixture(t)
		for _, typ := range []string{activityDailyGift, activityRechargeDraw} {
			userID := activityIntegrationUser(t, db, typ, 1)
			_, err := db.Exec(`UPDATE users SET deleted_at=NOW() WHERE id=$1`, userID)
			require.NoError(t, err)
			if typ == activityDailyGift {
				_, err = svc.OpenDailyGift(context.Background(), userID, "deleted", time.Now())
			} else {
				_, err = svc.Draw(context.Background(), userID, typ, 1, "deleted", time.Now())
			}
			require.ErrorIs(t, err, ErrUserNotFound)
			assertActivityIntegrationLedger(t, db, userID, 0)
			require.Zero(t, activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_draw_credits WHERE user_id=$1 AND consumed_at IS NOT NULL`, userID))
		}
	})
	t.Run("live_replay_rejects_source_snapshot_conflict", func(t *testing.T) {
		db, svc := newActivityRewardCostFixture(t)
		userID := activityIntegrationUser(t, db, activityRechargeDraw, 1)
		rewards, err := svc.Draw(context.Background(), userID, activityRechargeDraw, 1, "conflict", time.Now())
		require.NoError(t, err)
		_, err = db.Exec(`UPDATE extra_cost_entries SET amount=amount+1 WHERE activity_reward_id=$1`, rewards[0].ID)
		require.NoError(t, err)
		_, err = svc.Draw(context.Background(), userID, activityRechargeDraw, 1, "conflict", time.Now())
		require.ErrorIs(t, err, ErrExtraCostIdempotencyConflict)
		require.Equal(t, int64(1), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM activity_reward_records WHERE user_id=$1`, userID))
		var balance string
		require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, userID).Scan(&balance))
		require.Equal(t, "0.23000000", balance)
	})
}

func testActivityCostHistoricalBackfill(t *testing.T) {
	db, _ := newActivityRewardCostFixture(t)
	userID := activityIntegrationUser(t, db, activityRechargeDraw, 0)
	_, err := db.Exec(`INSERT INTO extra_cost_entries(cost_date,amount,category,notes,created_at,idempotency_key) VALUES('2026-10-02',12.34000000,'account','manual preserved','2026-10-02T08:00:00Z','manual-original')`)
	require.NoError(t, err)
	var manualBefore string
	require.NoError(t, db.QueryRow(`SELECT row_to_json(c)::text FROM extra_cost_entries c WHERE idempotency_key='manual-original'`).Scan(&manualBefore))
	for i, typ := range []string{activityDailyGift, activityRechargeDraw, activitySpendDraw, activityInviteDraw} {
		amount := "0.12345678"
		if i == 1 {
			amount = "0"
		}
		_, err = db.Exec(`INSERT INTO activity_reward_records(user_id,activity_type,amount,period_date,created_at) VALUES($1,$2,$3,'2026-01-01',$4)`, userID, typ, amount, time.Date(2026, 10, 6, 15, 59+i, 59, 0, time.UTC))
		require.NoError(t, err)
	}
	_, err = db.Exec(`INSERT INTO activity_reward_records(user_id,activity_type,amount,status,created_at) VALUES($1,'daily_gift',8,'failed','2026-10-02T08:00:00Z')`, userID)
	require.NoError(t, err)
	count, err := BackfillActivityRewardCosts(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, int64(4), count)
	count, err = BackfillActivityRewardCosts(context.Background(), db)
	require.NoError(t, err)
	require.Zero(t, count)
	require.Equal(t, int64(1), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM extra_cost_entries WHERE related_user_id=$1 AND cost_date='2026-10-06'`, userID))
	require.Equal(t, int64(3), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM extra_cost_entries WHERE related_user_id=$1 AND cost_date='2026-10-07'`, userID))
	var preciseTotal string
	require.NoError(t, db.QueryRow(`SELECT SUM(amount)::text FROM extra_cost_entries WHERE related_user_id=$1`, userID).Scan(&preciseTotal))
	require.Equal(t, "0.37037034", preciseTotal)
	_, err = db.Exec(`INSERT INTO activity_reward_records(user_id,activity_type,amount,created_at) VALUES($1,'recharge_draw',0.90000000,'2026-10-07T08:00:00Z')`, userID)
	require.NoError(t, err)
	count, err = BackfillActivityRewardCosts(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), count, "old writer tail must be backfilled once")
	_, err = db.Exec(`UPDATE extra_cost_entries SET related_user_id=99999 WHERE activity_reward_id=(SELECT MIN(id) FROM activity_reward_records WHERE user_id=$1)`, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO activity_reward_records(user_id,activity_type,amount,created_at) VALUES($1,'recharge_draw',1,'2026-10-07T09:00:00Z')`, userID)
	require.NoError(t, err)
	_, err = BackfillActivityRewardCosts(context.Background(), db)
	require.ErrorContains(t, err, "audit snapshot conflict")
	require.Equal(t, int64(5), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM extra_cost_entries WHERE category='activity_reward'`), "failed backfill must roll back new costs too")
	_, err = db.Exec(`UPDATE extra_cost_entries SET related_user_id=$1 WHERE related_user_id=99999`, userID)
	require.NoError(t, err)
	count, err = BackfillActivityRewardCosts(context.Background(), db)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	var manualAfter string
	require.NoError(t, db.QueryRow(`SELECT row_to_json(c)::text FROM extra_cost_entries c WHERE idempotency_key='manual-original'`).Scan(&manualAfter))
	require.Equal(t, manualBefore, manualAfter)
	var balance string
	require.NoError(t, db.QueryRow(`SELECT balance::text FROM users WHERE id=$1`, userID).Scan(&balance))
	require.Equal(t, "0.00000000", balance, "backfill must not credit balance")
	_, err = db.Exec(`DELETE FROM users WHERE id=$1`, userID)
	require.NoError(t, err)
	require.Equal(t, int64(6), activityIntegrationCount(t, db, `SELECT COUNT(*) FROM extra_cost_entries WHERE related_user_id=$1`, userID), "audit snapshots survive source deletion")
}
