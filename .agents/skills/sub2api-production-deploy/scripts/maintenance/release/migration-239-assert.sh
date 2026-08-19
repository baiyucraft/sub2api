#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source "${ASSERT_CONTEXT_FILE:-/opt/sub2api/releases/.active-release/assets/context.sh}"
[[ $profile == 239 || $profile == 240 || $profile == 241 || $profile == 242 ]] || exit 1
[[ $phase == preflight || $phase == bind || $phase == postflight ]] || exit 1
[[ $migration_status == absent || $migration_status == verified ]] || exit 1

query() {
  docker exec "${ASSERT_DB_CONTAINER:-sub2api-postgres}" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "${ASSERT_DB_USER:-sub2api}" -d "${ASSERT_DB_NAME:-sub2api}" -c "$1"
}

live_predicate="platform IS DISTINCT FROM 'grok' AND platform IS DISTINCT FROM 'composite' AND (video_price_480p IS NOT NULL OR video_price_720p IS NOT NULL OR video_price_1080p IS NOT NULL OR video_model_prices IS NOT NULL)"

canonical_live_hash() {
  query "COPY (
    SELECT id::text || '|' || COALESCE(platform,'<null>') || '|' ||
           COALESCE(video_price_480p::text,'<null>') || '|' ||
           COALESCE(video_price_720p::text,'<null>') || '|' ||
           COALESCE(video_price_1080p::text,'<null>') || '|' ||
           COALESCE(video_model_prices::text,'<null>')
      FROM groups WHERE $live_predicate ORDER BY id
  ) TO STDOUT" | sha256sum | awk '{print $1}'
}

canonical_backup_hash() {
  query "COPY (
    SELECT group_id::text || '|' || COALESCE(platform,'<null>') || '|' ||
           COALESCE(video_price_480p::text,'<null>') || '|' ||
           COALESCE(video_price_720p::text,'<null>') || '|' ||
           COALESCE(video_price_1080p::text,'<null>') || '|' ||
           COALESCE(video_model_prices::text,'<null>')
      FROM groups_video_price_backup_239 ORDER BY group_id
  ) TO STDOUT" | sha256sum | awk '{print $1}'
}

protected_hash() {
  query "COPY (
    SELECT id::text || '|' || platform || '|' ||
           COALESCE(video_price_480p::text,'<null>') || '|' ||
           COALESCE(video_price_720p::text,'<null>') || '|' ||
           COALESCE(video_price_1080p::text,'<null>') || '|' ||
           COALESCE(video_model_prices::text,'<null>')
      FROM groups WHERE platform IN ('grok','composite') ORDER BY id
  ) TO STDOUT" | sha256sum | awk '{print $1}'
}

if [[ $phase == preflight ]]; then
  backup_exists=$(query "SELECT to_regclass('public.groups_video_price_backup_239') IS NOT NULL")
  constraint_exists=$(query "SELECT COUNT(*) FROM pg_constraint WHERE conrelid='public.groups'::regclass AND conname='groups_video_pricing_platform_check'")
  if [[ $migration_status == absent ]]; then
    [[ $backup_exists == f && $constraint_exists == 0 ]]
    affected=$(query "SELECT COUNT(*) FROM groups WHERE $live_predicate")
    plan_sha256=$(canonical_live_hash)
  else
    [[ $backup_exists == t && $constraint_exists == 1 ]]
    affected=$(query "SELECT COUNT(*) FROM groups_video_price_backup_239")
    plan_sha256=$(canonical_backup_hash)
    [[ $(query "SELECT COUNT(*) FROM groups WHERE $live_predicate") == 0 ]]
  fi
  protected_sha256=$(protected_hash)
  [[ $affected =~ ^[0-9]+$ && $plan_sha256 =~ ^[0-9a-f]{64}$ && $protected_sha256 =~ ^[0-9a-f]{64}$ ]]
  printf '%s\n' "$migration_status" > "$state_dir/migration-239-status"
  printf '%s\n' "$affected" > "$state_dir/migration-239-affected.count"
  printf '%s\n' "$plan_sha256" > "$state_dir/migration-239-data-plan.sha256"
  printf '%s\n' "$protected_sha256" > "$state_dir/migration-239-protected.sha256"
  chmod 600 "$state_dir"/migration-239-*
  printf 'migration_239_affected=%s\n' "$affected"
  printf 'migration_239_data_plan_sha256=%s\n' "$plan_sha256"
  printf 'migration_239_preflight=pass\n'
  exit 0
fi

[[ -f $state_dir/migration-239-data-plan.sha256 && ! -L $state_dir/migration-239-data-plan.sha256 ]]
[[ -f $state_dir/migration-239-affected.count && ! -L $state_dir/migration-239-affected.count ]]

if [[ $phase == bind ]]; then
  [[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
  recovery_sha256=$(awk 'NR==1{print $1}' "$state_dir/recovery-point.age.sha256")
  [[ $recovery_sha256 =~ ^[0-9a-f]{64}$ ]]
  bound_sha256=$(printf '%s|%s\n' "$(<"$state_dir/migration-239-data-plan.sha256")" "$recovery_sha256" | sha256sum | awk '{print $1}')
  printf '%s\n' "$bound_sha256" > "$state_dir/migration-239-bound.sha256"
  chmod 600 "$state_dir/migration-239-bound.sha256"
  printf 'migration_239_plan_sha256=%s\n' "$(<"$state_dir/migration-239-data-plan.sha256")"
  printf 'migration_239_recovery_sha256=%s\n' "$recovery_sha256"
  exit 0
fi

expected_affected=$(<"$state_dir/migration-239-affected.count")
expected_plan=$(<"$state_dir/migration-239-data-plan.sha256")
expected_protected=$(<"$state_dir/migration-239-protected.sha256")
[[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
[[ -f $state_dir/migration-239-bound.sha256 && ! -L $state_dir/migration-239-bound.sha256 ]]
recovery_sha256=$(awk 'NR==1{print $1}' "$state_dir/recovery-point.age.sha256")
expected_bound=$(<"$state_dir/migration-239-bound.sha256")
actual_bound=$(printf '%s|%s\n' "$expected_plan" "$recovery_sha256" | sha256sum | awk '{print $1}')
backup_count=$(query "SELECT COUNT(*) FROM groups_video_price_backup_239")
backup_hash=$(canonical_backup_hash)
remaining=$(query "SELECT COUNT(*) FROM groups WHERE $live_predicate")
current_protected=$(protected_hash)
constraint_state=$(query "SELECT COUNT(*), BOOL_AND(convalidated), BOOL_AND(pg_get_constraintdef(oid) LIKE '%video_price_480p IS NULL%' AND pg_get_constraintdef(oid) LIKE '%video_model_prices IS NULL%') FROM pg_constraint WHERE conrelid='public.groups'::regclass AND conname='groups_video_pricing_platform_check'")
[[ $expected_affected =~ ^[0-9]+$ && $expected_plan =~ ^[0-9a-f]{64}$ && $expected_protected =~ ^[0-9a-f]{64}$ ]]
[[ $actual_bound == "$expected_bound" ]]
[[ $backup_count == "$expected_affected" ]]
[[ $backup_hash == "$expected_plan" ]]
[[ $remaining == 0 ]]
[[ $current_protected == "$expected_protected" ]]
[[ $constraint_state == '1|t|t' ]]
printf 'migration_239_backup_rows=%s\n' "$backup_count"
printf 'migration_239_remaining_rows=%s\n' "$remaining"
printf 'migration_239_constraint_verified=true\n'
printf 'migration_239_postflight=pass\n'
