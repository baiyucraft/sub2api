#!/usr/bin/env bash
set -Eeuo pipefail

phase=${1:?phase is required}
migration_status=${MIGRATION_STATUS:-absent}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
if [[ -n ${ASSERT_CONTEXT_FILE:-} ]]; then
  [[ -f $ASSERT_CONTEXT_FILE && ! -L $ASSERT_CONTEXT_FILE ]]
  source "$ASSERT_CONTEXT_FILE"
else
  source /opt/sub2api/releases/.active-release/assets/context.sh
fi
db_container=${ASSERT_DB_CONTAINER:-sub2api-postgres}
db_user=${ASSERT_DB_USER:-sub2api}
db_name=${ASSERT_DB_NAME:-sub2api}
[[ $profile == 232 || $profile == 233 || $profile == 234 || $profile == 235 || $profile == 236 || $profile == 237 || $profile == 238 || $profile == 239 || $profile == 240 || $profile == 241 || $profile == 242 ]]
[[ $phase == preflight || $phase == bind || $phase == postflight ]]
[[ $migration_status == absent || $migration_status == verified ]]

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

source_relation=groups
video_model_column_exists=$(query "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='groups' AND column_name='video_model_prices')")
[[ $video_model_column_exists == t || $video_model_column_exists == f ]]
video_model_select="NULL::jsonb"
video_model_predicate=""
if [[ $video_model_column_exists == t ]]; then
  video_model_select=video_model_prices
  video_model_predicate=" OR video_model_prices IS NOT NULL"
fi
live_source_predicate="platform IS DISTINCT FROM 'grok' AND platform IS DISTINCT FROM 'composite' AND (video_price_480p IS NOT NULL OR video_price_720p IS NOT NULL OR video_price_1080p IS NOT NULL${video_model_predicate})"
source_predicate="$live_source_predicate"
# Postflight must validate the immutable backup snapshot even when this run
# started with migration 232 absent. The live groups rows have already been
# cleared at that point, so hashing them would only work for a zero-row plan.
if [[ $migration_status == verified || $phase == postflight ]]; then
  [[ $(query "SELECT to_regclass('public.groups_video_price_backup_232') IS NOT NULL") == t ]]
  source_relation=groups_video_price_backup_232
  source_predicate=true
fi

canonical_source_hash() {
  if [[ $source_relation == groups ]]; then
    query "COPY (
      SELECT id::text || '|' || COALESCE(platform,'<null>') || '|' ||
             COALESCE(video_price_480p::text,'<null>') || '|' ||
             COALESCE(video_price_720p::text,'<null>') || '|' ||
             COALESCE(video_price_1080p::text,'<null>') || '|' ||
             COALESCE((${video_model_select})::text,'<null>')
        FROM groups WHERE $source_predicate ORDER BY id
    ) TO STDOUT" | sha256sum | awk '{print $1}'
  else
    query "COPY (
      SELECT group_id::text || '|' || COALESCE(platform,'<null>') || '|' ||
             COALESCE(video_price_480p::text,'<null>') || '|' ||
             COALESCE(video_price_720p::text,'<null>') || '|' ||
             COALESCE(video_price_1080p::text,'<null>') || '|' ||
             COALESCE(video_model_prices::text,'<null>')
        FROM groups_video_price_backup_232 ORDER BY group_id
    ) TO STDOUT" | sha256sum | awk '{print $1}'
  fi
}

protected_hash() {
  query "COPY (
    SELECT id::text || '|' || platform || '|' ||
           COALESCE(video_price_480p::text,'<null>') || '|' ||
           COALESCE(video_price_720p::text,'<null>') || '|' ||
           COALESCE(video_price_1080p::text,'<null>') || '|' ||
           COALESCE((${video_model_select})::text,'<null>')
      FROM groups
     WHERE platform IN ('grok','composite')
     ORDER BY id
  ) TO STDOUT" | sha256sum | awk '{print $1}'
}

if [[ $phase == preflight ]]; then
  if [[ $migration_status == absent ]]; then
    counts=$(query "WITH affected AS (
      SELECT id FROM groups WHERE $source_predicate
    )
    SELECT
      (SELECT COUNT(*) FROM affected),
      (SELECT COUNT(DISTINCT a.id) FROM affected g JOIN account_groups ag ON ag.group_id=g.id JOIN accounts a ON a.id=ag.account_id WHERE a.deleted_at IS NULL AND a.upstream_config_id IS NOT NULL),
      (SELECT COUNT(DISTINCT a.id) FROM affected g JOIN account_groups ag ON ag.group_id=g.id JOIN accounts a ON a.id=ag.account_id JOIN upstream_configs uc ON uc.id=a.upstream_config_id WHERE a.deleted_at IS NULL AND uc.provider='lcodex')")
  else
    counts=$(query "WITH affected AS (SELECT group_id AS id FROM groups_video_price_backup_232)
    SELECT
      (SELECT COUNT(*) FROM affected),
      (SELECT COUNT(DISTINCT a.id) FROM affected g JOIN account_groups ag ON ag.group_id=g.id JOIN accounts a ON a.id=ag.account_id WHERE a.deleted_at IS NULL AND a.upstream_config_id IS NOT NULL),
      (SELECT COUNT(DISTINCT a.id) FROM affected g JOIN account_groups ag ON ag.group_id=g.id JOIN accounts a ON a.id=ag.account_id JOIN upstream_configs uc ON uc.id=a.upstream_config_id WHERE a.deleted_at IS NULL AND uc.provider='lcodex')")
  fi
  IFS='|' read -r affected upstream_bound lcodex_bound <<<"$counts"
  [[ $affected =~ ^[0-9]+$ && $upstream_bound =~ ^[0-9]+$ && $lcodex_bound =~ ^[0-9]+$ ]]
  [[ $lcodex_bound -le $upstream_bound ]]
  plan_sha256=$(canonical_source_hash)
  protected_sha256=$(protected_hash)
  [[ $plan_sha256 =~ ^[0-9a-f]{64}$ && $protected_sha256 =~ ^[0-9a-f]{64}$ ]]
  printf '%s\n' "$affected" > "$state_dir/migration-232-affected.count"
  printf '%s\n' "$upstream_bound" > "$state_dir/migration-232-upstream-bound.count"
  printf '%s\n' "$lcodex_bound" > "$state_dir/migration-232-lcodex-bound.count"
  printf '%s\n' "$plan_sha256" > "$state_dir/migration-232-data-plan.sha256"
  printf '%s\n' "$protected_sha256" > "$state_dir/migration-232-protected.sha256"
  printf '%s\n' "$migration_status" > "$state_dir/migration-232-status"
  chmod 600 "$state_dir"/migration-232-*.count "$state_dir"/migration-232-*.sha256 "$state_dir/migration-232-status"
  printf 'migration_232_affected=%s\n' "$affected"
  printf 'migration_232_upstream_bound=%s\n' "$upstream_bound"
  printf 'migration_232_lcodex_bound=%s\n' "$lcodex_bound"
  printf 'migration_232_data_plan_sha256=%s\n' "$plan_sha256"
  exit 0
fi

[[ -f $state_dir/migration-232-data-plan.sha256 && ! -L $state_dir/migration-232-data-plan.sha256 ]]
[[ -f $state_dir/migration-232-affected.count && ! -L $state_dir/migration-232-affected.count ]]

if [[ $phase == bind ]]; then
  [[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
  recovery_sha256=$(awk 'NR==1{print $1}' "$state_dir/recovery-point.age.sha256")
  [[ $recovery_sha256 =~ ^[0-9a-f]{64}$ ]]
  bound_sha256=$(printf '%s|%s\n' "$(<"$state_dir/migration-232-data-plan.sha256")" "$recovery_sha256" | sha256sum | awk '{print $1}')
  printf '%s\n' "$bound_sha256" > "$state_dir/migration-232-bound.sha256"
  chmod 600 "$state_dir/migration-232-bound.sha256"
  printf 'migration_232_plan_sha256=%s\n' "$(<"$state_dir/migration-232-data-plan.sha256")"
  printf 'migration_232_recovery_sha256=%s\n' "$recovery_sha256"
  exit 0
fi

expected_affected=$(<"$state_dir/migration-232-affected.count")
expected_plan=$(<"$state_dir/migration-232-data-plan.sha256")
expected_protected=$(<"$state_dir/migration-232-protected.sha256")
[[ $expected_affected =~ ^[0-9]+$ && $expected_plan =~ ^[0-9a-f]{64}$ && $expected_protected =~ ^[0-9a-f]{64}$ ]]
[[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
[[ -f $state_dir/migration-232-bound.sha256 && ! -L $state_dir/migration-232-bound.sha256 ]]
recovery_sha256=$(awk 'NR==1{print $1}' "$state_dir/recovery-point.age.sha256")
expected_bound=$(<"$state_dir/migration-232-bound.sha256")
actual_bound=$(printf '%s|%s\n' "$expected_plan" "$recovery_sha256" | sha256sum | awk '{print $1}')
[[ $recovery_sha256 =~ ^[0-9a-f]{64}$ && $expected_bound =~ ^[0-9a-f]{64}$ && $actual_bound == "$expected_bound" ]]
remaining=$(query "SELECT COUNT(*) FROM groups WHERE $live_source_predicate")
backup_count=$(query "SELECT COUNT(*) FROM groups_video_price_backup_232")
backup_hash=$(canonical_source_hash)
current_protected=$(protected_hash)
trigger_definition=$(query "SELECT pg_get_functiondef('enqueue_group_auth_cache_invalidation()'::regprocedure)")
[[ $remaining == 0 ]]
[[ $backup_count == "$expected_affected" ]]
[[ $backup_hash == "$expected_plan" ]]
[[ $current_protected == "$expected_protected" ]]
for trigger_field in video_price_480p video_price_720p video_price_1080p video_model_prices web_search_price_per_call search_price_per_1k audio_realtime_price_per_min audio_tts_price_per_million_chars audio_stt_price_per_hour; do
  grep -Fq "OLD.${trigger_field} IS NOT DISTINCT FROM NEW.${trigger_field}" <<<"$trigger_definition"
done
printf 'migration_232_backup_rows=%s\n' "$backup_count"
printf 'migration_232_remaining_rows=%s\n' "$remaining"
printf 'migration_232_bound_sha256=%s\n' "$actual_bound"
printf 'migration_232_postflight=pass\n'
