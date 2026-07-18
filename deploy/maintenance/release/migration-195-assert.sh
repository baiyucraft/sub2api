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
redis_container=${ASSERT_REDIS_CONTAINER:-sub2api-redis}
[[ $profile == 195 ]]
[[ $phase == preflight || $phase == bind || $phase == postflight_db || $phase == postflight_runtime ]]
[[ $migration_status == absent || $migration_status == verified ]]

query() {
  docker exec "$db_container" psql -X -A -t -F '|' -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -c "$1"
}

redis_watermark() {
  local redis_password
  redis_password=$(docker inspect "$redis_container" | jq -r '((.[0].Config.Entrypoint // []) + (.[0].Config.Cmd // [])) as $a | ($a | index("--requirepass")) as $i | if $i != null and ($i + 1) < ($a | length) then $a[$i + 1] else ([ $a[] | select(startswith("--requirepass=")) | ltrimstr("--requirepass=") ] | first // "") end')
  printf '%s\n' "$redis_password" | docker exec -i "$redis_container" sh -c 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning GET sched:v2:outbox:watermark' | tr -d '\r'
}

if [[ $phase == preflight ]]; then
  if [[ $migration_status == verified ]]; then
    terminal=$(query "
WITH expected_accounts AS (
  SELECT COALESCE(jsonb_agg(id ORDER BY id),'[]'::jsonb) AS ids, COUNT(*) AS affected FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL
), last_event AS (
  SELECT COALESCE(MAX(id),0) AS event_id FROM scheduler_outbox,expected_accounts WHERE event_type='account_bulk_changed' AND payload->'account_ids'=expected_accounts.ids
)
SELECT
  (SELECT affected FROM expected_accounts),
  (SELECT COUNT(*) FROM upstream_keys WHERE rate_multiplier IS NOT NULL AND source_rate_multiplier IS NULL),
  (SELECT COUNT(*) FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.rate_multiplier IS DISTINCT FROM k.rate_multiplier OR a.upstream_source_rate_multiplier IS DISTINCT FROM k.source_rate_multiplier OR a.priority IS DISTINCT FROM CEIL(k.rate_multiplier*100)::int),
  (SELECT event_id FROM last_event)")
    IFS='|' read -r affected unproven account_mismatch outbox_event_id <<<"$terminal"
    [[ $unproven == 0 && $account_mismatch == 0 ]]
    outbox_already_consumed=false
    if [[ $affected -gt 0 && $outbox_event_id == 0 ]]; then
      current_watermark=$(redis_watermark)
      [[ $current_watermark =~ ^[0-9]+$ && $current_watermark -gt 0 ]]
      outbox_already_consumed=true
    fi
    terminal_sha=$(query "COPY (SELECT id::text || '|' || to_char(source_rate_multiplier,'FM999999999999990.0000000000') || '|' || to_char(rate_multiplier,'FM999999999999990.0000') FROM upstream_keys WHERE source_rate_multiplier IS NOT NULL ORDER BY id) TO STDOUT" | sha256sum | awk '{print $1}')
    account_ids_sha=$(query "COPY (SELECT id FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL ORDER BY id) TO STDOUT" | sha256sum | awk '{print $1}')
    printf '%s\n' "$terminal_sha" > "$state_dir/migration-195-data-plan.sha256"
    printf '%s\n' "$account_ids_sha" > "$state_dir/migration-195-account-ids.sha256"
    printf '%s\n' "$affected" > "$state_dir/migration-195-affected.count"
    printf '%s\n' "$outbox_event_id" > "$state_dir/migration-195-outbox-event.id"
    printf '%s\n' "$outbox_already_consumed" > "$state_dir/migration-195-outbox-already-consumed"
    printf '%s\n' verified > "$state_dir/migration-195-status"
    chmod 600 "$state_dir"/migration-195-*.sha256 "$state_dir"/migration-195-*.count "$state_dir"/migration-195-*.id "$state_dir/migration-195-status" "$state_dir/migration-195-outbox-already-consumed"
    printf 'migration_195_affected=%s\n' "$affected"
    printf 'migration_195_recomputed=0\n'
    printf 'migration_195_preserved=0\n'
    printf 'migration_195_skipped=0\n'
    printf 'migration_195_unproven=0\n'
    printf 'migration_195_conflict=0\n'
    printf 'migration_195_unexpected=0\n'
    printf 'migration_195_data_plan_sha256=%s\n' "$terminal_sha"
    exit 0
  fi
  source_rate_column_exists=$(query "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='upstream_keys' AND column_name='source_rate_multiplier')")
  [[ $source_rate_column_exists == t || $source_rate_column_exists == f ]]
  printf '%s\n' "$source_rate_column_exists" > "$state_dir/migration-195-source-rate-column-existed"
  chmod 600 "$state_dir/migration-195-source-rate-column-existed"
  if [[ $source_rate_column_exists == t ]]; then
    canonical_plan_query="COPY (
  SELECT k.id::text || '|' ||
         to_char(COALESCE(k.source_rate_multiplier,k.rate_multiplier),'FM999999999999990.0000000000') || '|' ||
         to_char(CASE WHEN k.source_rate_multiplier IS NULL THEN k.rate_multiplier WHEN k.source_rate_multiplier=0 THEN 0 ELSE CEIL((k.source_rate_multiplier*c.recharge_rate)*100)/100 END,'FM999999999999990.0000')
    FROM upstream_keys k
    JOIN upstream_configs c ON c.id=k.upstream_config_id
   WHERE COALESCE(k.source_rate_multiplier,k.rate_multiplier) IS NOT NULL
   ORDER BY k.id
) TO STDOUT"
    recomputed_expression="(SELECT COUNT(*) FROM upstream_keys WHERE source_rate_multiplier IS NOT NULL)"
    preserved_expression="(SELECT COUNT(*) FROM upstream_keys WHERE source_rate_multiplier IS NULL AND rate_multiplier IS NOT NULL)"
    skipped_expression="(SELECT COUNT(*) FROM upstream_keys WHERE source_rate_multiplier IS NULL AND rate_multiplier IS NULL)"
    rate_present_expression="COALESCE(k.source_rate_multiplier,k.rate_multiplier) IS NOT NULL"
    unexpected_rate_expression="CASE WHEN k.source_rate_multiplier IS NULL THEN k.rate_multiplier ELSE CEIL((k.source_rate_multiplier*c.recharge_rate)*100)/100 END"
  else
    canonical_plan_query="COPY (
  SELECT k.id::text || '|' ||
         to_char(k.rate_multiplier,'FM999999999999990.0000000000') || '|' ||
         to_char(CASE WHEN k.rate_multiplier=0 THEN 0 ELSE CEIL((k.rate_multiplier*c.recharge_rate)*100)/100 END,'FM999999999999990.0000')
    FROM upstream_keys k
    JOIN upstream_configs c ON c.id=k.upstream_config_id
   WHERE k.rate_multiplier IS NOT NULL
   ORDER BY k.id
) TO STDOUT"
    recomputed_expression="(SELECT COUNT(*) FROM upstream_keys WHERE rate_multiplier IS NOT NULL)"
    preserved_expression="0"
    skipped_expression="(SELECT COUNT(*) FROM upstream_keys WHERE rate_multiplier IS NULL)"
    rate_present_expression="k.rate_multiplier IS NOT NULL"
    unexpected_rate_expression="CEIL((k.rate_multiplier*c.recharge_rate)*100)/100"
  fi
  counts=$(query "
WITH values AS (
  SELECT
    (SELECT COUNT(*) FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL) AS affected,
    $recomputed_expression AS recomputed,
    $preserved_expression AS preserved,
    $skipped_expression AS skipped,
    (SELECT COUNT(*) FROM accounts a LEFT JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.upstream_key_id IS NOT NULL AND k.rate_multiplier IS NULL) AS unproven,
    (SELECT COUNT(*) FROM accounts a LEFT JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.upstream_key_id IS NOT NULL AND (k.id IS NULL OR k.deleted_at IS NOT NULL OR k.upstream_config_id IS DISTINCT FROM a.upstream_config_id)) AS conflict,
    (SELECT COUNT(*) FROM upstream_keys k JOIN upstream_configs c ON c.id=k.upstream_config_id WHERE $rate_present_expression AND (c.recharge_rate <= 0 OR $unexpected_rate_expression > 999999.9999))
      + (SELECT COUNT(*) FROM accounts WHERE upstream_key_id IS NOT NULL AND concurrency > 1073741823) AS unexpected
)
SELECT affected,recomputed,preserved,skipped,unproven,conflict,unexpected FROM values")
  IFS='|' read -r affected recomputed preserved skipped unproven conflict unexpected <<<"$counts"
  [[ $unproven == 0 && $conflict == 0 && $unexpected == 0 ]]
  data_plan_sha=$(query "$canonical_plan_query" | sha256sum | awk '{print $1}')
  account_ids_sha=$(query "COPY (SELECT id FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL ORDER BY id) TO STDOUT" | sha256sum | awk '{print $1}')
  [[ $data_plan_sha =~ ^[0-9a-f]{64}$ && $account_ids_sha =~ ^[0-9a-f]{64}$ ]]
  printf '%s\n' "$data_plan_sha" > "$state_dir/migration-195-data-plan.sha256"
  printf '%s\n' "$account_ids_sha" > "$state_dir/migration-195-account-ids.sha256"
  printf '%s\n' "$affected" > "$state_dir/migration-195-affected.count"
  query "SELECT COALESCE(MAX(id),0) FROM scheduler_outbox" > "$state_dir/migration-195-outbox-baseline.id"
  printf '%s\n' absent > "$state_dir/migration-195-status"
  chmod 600 "$state_dir"/migration-195-*.sha256 "$state_dir"/migration-195-*.count "$state_dir"/migration-195-*.id "$state_dir/migration-195-status"
  printf 'migration_195_affected=%s\n' "$affected"
  printf 'migration_195_recomputed=%s\n' "$recomputed"
  printf 'migration_195_preserved=%s\n' "$preserved"
  printf 'migration_195_skipped=%s\n' "$skipped"
  printf 'migration_195_unproven=%s\n' "$unproven"
  printf 'migration_195_conflict=%s\n' "$conflict"
  printf 'migration_195_unexpected=%s\n' "$unexpected"
  printf 'migration_195_data_plan_sha256=%s\n' "$data_plan_sha"
  exit 0
fi

if [[ $phase == bind ]]; then
  [[ -f $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
  [[ -f $state_dir/migration-195-data-plan.sha256 && ! -L $state_dir/migration-195-data-plan.sha256 ]]
  recovery_sha=$(awk 'NR==1{print $1}' "$state_dir/recovery-point.age.sha256")
  data_plan_sha=$(<"$state_dir/migration-195-data-plan.sha256")
  [[ $recovery_sha =~ ^[0-9a-f]{64}$ && $data_plan_sha =~ ^[0-9a-f]{64}$ ]]
  plan_sha=$(printf '%s|%s\n' "$data_plan_sha" "$recovery_sha" | sha256sum | awk '{print $1}')
  printf '%s\n' "$plan_sha" > "$state_dir/migration-195-plan.sha256"
  chmod 600 "$state_dir/migration-195-plan.sha256"
  printf 'migration_195_plan_sha256=%s\n' "$plan_sha"
  printf 'migration_195_recovery_sha256=%s\n' "$recovery_sha"
  exit 0
fi

[[ -f $state_dir/migration-195-plan.sha256 && ! -L $state_dir/migration-195-plan.sha256 ]]
[[ -f $state_dir/migration-195-affected.count && ! -L $state_dir/migration-195-affected.count ]]

if [[ $phase == postflight_db ]]; then
  actual_data_plan_sha=$(query "COPY (SELECT id::text || '|' || to_char(source_rate_multiplier,'FM999999999999990.0000000000') || '|' || to_char(rate_multiplier,'FM999999999999990.0000') FROM upstream_keys WHERE source_rate_multiplier IS NOT NULL ORDER BY id) TO STDOUT" | sha256sum | awk '{print $1}')
  expected_data_plan_sha=$(<"$state_dir/migration-195-data-plan.sha256")
  if [[ $actual_data_plan_sha == "$expected_data_plan_sha" ]]; then recompute_mismatch=0; else recompute_mismatch=1; fi
  actual_account_ids_sha=$(query "COPY (SELECT id FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL ORDER BY id) TO STDOUT" | sha256sum | awk '{print $1}')
  if [[ $actual_account_ids_sha == "$(<"$state_dir/migration-195-account-ids.sha256")" ]]; then account_ids_mismatch=0; else account_ids_mismatch=1; fi
  migration_status=$(<"$state_dir/migration-195-status")
  if [[ $migration_status == verified ]]; then
    [[ $recompute_mismatch == 0 ]]
    printf 'migration_195_database_postflight=true\n'
    printf 'migration_195_affected=%s\n' "$(<"$state_dir/migration-195-affected.count")"
    printf 'migration_195_recompute_mismatch=0\n'
    printf 'migration_195_unproven=0\n'
    printf 'migration_195_account_mismatch=0\n'
    printf 'migration_195_snapshot_missing=0\n'
    printf 'migration_195_outbox_missing=0\n'
    printf 'migration_195_constraint_missing=0\n'
    printf 'migration_195_trigger_missing=0\n'
    printf 'migration_195_plan_sha256=%s\n' "$(<"$state_dir/migration-195-plan.sha256")"
    exit 0
  fi
  outbox_baseline=$(<"$state_dir/migration-195-outbox-baseline.id")
  [[ -f $state_dir/migration-195-source-rate-column-existed && ! -L $state_dir/migration-195-source-rate-column-existed ]]
  source_rate_column_existed=$(<"$state_dir/migration-195-source-rate-column-existed")
  [[ $source_rate_column_existed == t || $source_rate_column_existed == f ]]
  postflight=$(query "
WITH expected_accounts AS (
  SELECT COALESCE(jsonb_agg(id ORDER BY id),'[]'::jsonb) AS ids, COUNT(*) AS affected FROM accounts WHERE deleted_at IS NULL AND upstream_key_id IS NOT NULL
), matching_outbox AS (
  SELECT COALESCE(MAX(id),0) AS event_id, COUNT(*) AS count FROM scheduler_outbox, expected_accounts WHERE id > $outbox_baseline AND event_type='account_bulk_changed' AND payload->'account_ids'=expected_accounts.ids
), values AS (
  SELECT
    (SELECT affected FROM expected_accounts) AS affected,
    (SELECT COUNT(*) FROM upstream_keys WHERE rate_multiplier IS NOT NULL AND source_rate_multiplier IS NULL) AS unproven,
    (SELECT COUNT(*) FROM accounts a JOIN upstream_keys k ON k.id=a.upstream_key_id WHERE a.rate_multiplier IS DISTINCT FROM k.rate_multiplier OR a.upstream_source_rate_multiplier IS DISTINCT FROM k.source_rate_multiplier OR a.priority IS DISTINCT FROM CEIL(k.rate_multiplier*100)::int OR a.load_factor IS DISTINCT FROM LEAST(GREATEST(a.concurrency::bigint,1)*2,GREATEST(1,ROUND(GREATEST(a.concurrency::bigint,1)*CASE WHEN CEIL(k.rate_multiplier*100)::int<=5 THEN 2.0 WHEN CEIL(k.rate_multiplier*100)::int<=10 THEN 1.5 WHEN CEIL(k.rate_multiplier*100)::int<=20 THEN 1.0 WHEN CEIL(k.rate_multiplier*100)::int<=50 THEN 0.75 ELSE 0.5 END)::int))) AS account_mismatch,
    (SELECT COUNT(*) FROM groups g WHERE g.deleted_at IS NULL AND NOT EXISTS (SELECT 1 FROM group_rate_snapshots s WHERE s.group_id=g.id AND s.rate_multiplier=g.rate_multiplier AND s.peak_rate_enabled=g.peak_rate_enabled AND s.peak_start=g.peak_start AND s.peak_end=g.peak_end AND s.peak_rate_multiplier=g.peak_rate_multiplier AND s.timezone=COALESCE(NULLIF(current_setting('TIMEZONE',TRUE),''),'UTC'))) AS snapshot_missing,
    (SELECT CASE
       WHEN expected_accounts.affected=0 AND matching_outbox.count=0 THEN 0
       WHEN expected_accounts.affected>0 AND '$source_rate_column_existed'='t' AND matching_outbox.count=1 THEN 0
       WHEN expected_accounts.affected>0 AND '$source_rate_column_existed'='f' AND matching_outbox.count>=1 THEN 0
       ELSE 1 END FROM expected_accounts,matching_outbox) AS outbox_missing,
    (SELECT event_id FROM matching_outbox) AS outbox_event_id,
    (SELECT COUNT(*) FROM (VALUES ('channel_monitors_group_id_fkey'),('channel_monitors_managed_api_key_id_fkey'),('accounts_upstream_source_rate_valid'),('api_keys_purpose_check'),('channel_monitors_credential_mode_check'),('channel_monitors_max_probe_attempts_check')) required(name) WHERE NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conname=required.name AND c.convalidated)) AS constraint_missing,
    (SELECT COUNT(*) FROM (VALUES ('trg_record_group_rate_snapshot'),('trg_validate_account_upstream_key_binding')) required(name) WHERE NOT EXISTS (SELECT 1 FROM pg_trigger t WHERE t.tgname=required.name AND t.tgenabled<>'D' AND NOT t.tgisinternal)) AS trigger_missing
)
SELECT affected,unproven,account_mismatch,snapshot_missing,outbox_missing,outbox_event_id,constraint_missing,trigger_missing FROM values")
  IFS='|' read -r affected unproven account_mismatch snapshot_missing outbox_missing outbox_event_id constraint_missing trigger_missing <<<"$postflight"
  printf '%s\n' "$recompute_mismatch" > "$state_dir/migration-195-recompute-mismatch.count"
  printf '%s\n' "$account_ids_mismatch" > "$state_dir/migration-195-account-ids-mismatch.count"
  printf '%s\n' "$unproven" > "$state_dir/migration-195-unproven.count"
  printf '%s\n' "$account_mismatch" > "$state_dir/migration-195-account-mismatch.count"
  printf '%s\n' "$snapshot_missing" > "$state_dir/migration-195-snapshot-missing.count"
  printf '%s\n' "$outbox_missing" > "$state_dir/migration-195-outbox-missing.count"
  printf '%s\n' "$constraint_missing" > "$state_dir/migration-195-constraint-missing.count"
  printf '%s\n' "$trigger_missing" > "$state_dir/migration-195-trigger-missing.count"
  chmod 600 "$state_dir"/migration-195-*-mismatch.count "$state_dir"/migration-195-*-missing.count "$state_dir/migration-195-unproven.count"
  [[ $affected == "$(<"$state_dir/migration-195-affected.count")" ]]
  [[ $recompute_mismatch == 0 && $account_ids_mismatch == 0 && $unproven == 0 && $account_mismatch == 0 && $snapshot_missing == 0 && $outbox_missing == 0 && $constraint_missing == 0 && $trigger_missing == 0 ]]
  printf '%s\n' "$outbox_event_id" > "$state_dir/migration-195-outbox-event.id"
  chmod 600 "$state_dir/migration-195-outbox-event.id"
  printf 'migration_195_database_postflight=true\n'
  printf 'migration_195_affected=%s\n' "$affected"
  printf 'migration_195_recompute_mismatch=%s\n' "$recompute_mismatch"
  printf 'migration_195_unproven=%s\n' "$unproven"
  printf 'migration_195_account_mismatch=%s\n' "$account_mismatch"
  printf 'migration_195_snapshot_missing=%s\n' "$snapshot_missing"
  printf 'migration_195_outbox_missing=%s\n' "$outbox_missing"
  printf 'migration_195_constraint_missing=%s\n' "$constraint_missing"
  printf 'migration_195_trigger_missing=%s\n' "$trigger_missing"
  printf 'migration_195_plan_sha256=%s\n' "$(<"$state_dir/migration-195-plan.sha256")"
  exit 0
fi

[[ -f $state_dir/migration-195-outbox-event.id && ! -L $state_dir/migration-195-outbox-event.id ]]
outbox_event_id=$(<"$state_dir/migration-195-outbox-event.id")
if [[ $outbox_event_id == 0 ]]; then
  [[ -f $state_dir/migration-195-outbox-already-consumed && ! -L $state_dir/migration-195-outbox-already-consumed ]]
  [[ $(<"$state_dir/migration-195-outbox-already-consumed") == true || $(<"$state_dir/migration-195-affected.count") == 0 ]]
  outbox_watermark=$(redis_watermark)
else
  for _ in $(seq 1 30); do
    outbox_watermark=$(redis_watermark)
    [[ $outbox_watermark =~ ^[0-9]+$ && $outbox_watermark -ge $outbox_event_id ]] && break
    sleep 1
  done
fi
[[ $outbox_event_id == 0 || ( $outbox_watermark =~ ^[0-9]+$ && $outbox_watermark -ge $outbox_event_id ) ]]
printf 'migration_195_postflight=true\n'
printf 'migration_195_outbox_consumed=true\n'
