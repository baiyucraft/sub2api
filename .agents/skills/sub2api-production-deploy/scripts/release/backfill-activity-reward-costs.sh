#!/usr/bin/env bash
set -Eeuo pipefail
# Explicit, separately authorized catch-up. PostgreSQL connection comes from
# the operator's approved libpq service/environment; never print connection data.
mode=check
old_instances_drained=false
while (( $# )); do
  case "$1" in
    --mode) [[ $# -ge 2 ]] || exit 2; mode=$2; shift 2 ;;
    --old-instances-drained) old_instances_drained=true; shift ;;
    *) printf 'activity_reward_cost_status=blocked\nfailure_code=invalid_arguments\n' >&2; exit 2 ;;
  esac
done
[[ $mode == check || $mode == apply ]] || { printf 'activity_reward_cost_status=blocked\nfailure_code=invalid_mode\n' >&2; exit 2; }
if [[ $mode == apply && $old_instances_drained != true ]]; then
  printf 'activity_reward_cost_status=blocked\nfailure_code=old_instances_not_drained\n' >&2
  exit 2
fi
command -v psql >/dev/null || { printf 'activity_reward_cost_status=blocked\nfailure_code=psql_unavailable\n' >&2; exit 127; }
if ! output=$(
  {
    if [[ $mode == apply ]]; then
      printf "BEGIN;\nSET LOCAL lock_timeout = '30s';\nSET LOCAL statement_timeout = '5min';\nSELECT public.backfill_activity_reward_costs() AS inserted_rows \\gset\n"
    else
      printf "BEGIN TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY;\nSET LOCAL statement_timeout = '5min';\n\\set inserted_rows 0\n"
    fi
    cat <<'SQL'
WITH rewards AS (
  SELECT id, user_id, activity_type, amount, created_at,
         (created_at AT TIME ZONE 'Asia/Shanghai')::date AS cost_date
  FROM activity_reward_records WHERE status = 'credited'
), automatic_costs AS (
  SELECT * FROM extra_cost_entries
  WHERE category = 'activity_reward' OR activity_reward_id IS NOT NULL
), reward_days AS (
  SELECT cost_date, COUNT(*) AS reward_count, SUM(amount) AS reward_amount
  FROM rewards GROUP BY cost_date
), cost_days AS (
  SELECT cost_date, COUNT(*) AS cost_count, SUM(amount) AS cost_amount
  FROM automatic_costs GROUP BY cost_date
), days AS (
  SELECT COALESCE(r.cost_date, c.cost_date) AS cost_date,
         COALESCE(r.reward_count, 0) AS reward_count, COALESCE(c.cost_count, 0) AS cost_count,
         COALESCE(r.reward_amount, 0) AS reward_amount, COALESCE(c.cost_amount, 0) AS cost_amount,
         COALESCE(r.reward_count, 0) = COALESCE(c.cost_count, 0)
           AND COALESCE(r.reward_amount, 0) = COALESCE(c.cost_amount, 0) AS matched
  FROM reward_days r FULL JOIN cost_days c USING (cost_date)
), audit AS (
  SELECT (SELECT COUNT(*) FROM rewards r WHERE NOT EXISTS (
           SELECT 1 FROM automatic_costs c WHERE c.activity_reward_id = r.id)) AS missing_count,
         (SELECT COUNT(*) FROM automatic_costs c LEFT JOIN rewards r ON r.id = c.activity_reward_id
          WHERE r.id IS NULL) AS orphan_count,
         (SELECT COUNT(*) FROM automatic_costs c JOIN rewards r ON r.id = c.activity_reward_id
          WHERE c.amount IS DISTINCT FROM r.amount
             OR c.related_user_id IS DISTINCT FROM r.user_id
             OR c.activity_type IS DISTINCT FROM r.activity_type
             OR c.created_at IS DISTINCT FROM r.created_at
             OR c.cost_date IS DISTINCT FROM r.cost_date
             OR c.category IS DISTINCT FROM 'activity_reward'
             OR c.rule_version IS DISTINCT FROM 'activity-reward-cost-v1'
             OR c.idempotency_key IS DISTINCT FROM 'activity-reward:' || r.id::text
             OR c.created_by IS NOT NULL OR c.reversal_of IS NOT NULL) AS mismatch_count
)
SELECT jsonb_build_object(
  'inserted_rows', :inserted_rows,
  'reward_count', (SELECT COUNT(*) FROM rewards),
  'cost_count', (SELECT COUNT(*) FROM automatic_costs),
  'reward_amount', (SELECT COALESCE(SUM(amount), 0) FROM rewards),
  'cost_amount', (SELECT COALESCE(SUM(amount), 0) FROM automatic_costs),
  'missing_count', missing_count, 'orphan_count', orphan_count, 'mismatch_count', mismatch_count,
  'verified', missing_count = 0 AND orphan_count = 0 AND mismatch_count = 0
    AND NOT EXISTS (SELECT 1 FROM days WHERE NOT matched),
  'daily', (SELECT COALESCE(jsonb_agg(to_jsonb(days) ORDER BY cost_date), '[]'::jsonb) FROM days)
) FROM audit;
COMMIT;
SQL
  } | psql -X -qAt --no-password --set=ON_ERROR_STOP=1 2>/dev/null
); then
  printf 'activity_reward_cost_status=failed\nfailure_code=database_check_or_apply_failed\n' >&2
  exit 1
fi
printf '%s\n' "$output"
verified_pattern='"verified"[[:space:]]*:[[:space:]]*true([,}])'
if [[ ! $output =~ $verified_pattern ]]; then
  printf 'activity_reward_cost_status=failed\nfailure_code=audit_mismatch\n' >&2
  exit 1
fi
