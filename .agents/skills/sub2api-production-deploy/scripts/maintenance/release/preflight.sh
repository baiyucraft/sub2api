#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-10737418240}
preflight_phase=identity
failure_line=0
preflight_failure_file="$release_dir/preflight-failure"
candidate_plan_tmp=
candidate_override_tmp=
candidate_plan_stderr=
record_preflight_result() {
  local code=$?
  trap - ERR EXIT
  [[ -z $candidate_plan_tmp ]] || rm -f -- "$candidate_plan_tmp"
  [[ -z $candidate_override_tmp ]] || rm -f -- "$candidate_override_tmp"
  [[ -z $candidate_plan_stderr ]] || rm -f -- "$candidate_plan_stderr"
  if [[ $code -eq 0 ]]; then
    rm -f "$preflight_failure_file"
  else
    local failure_tmp="$preflight_failure_file.tmp.$$"
    printf 'preflight_failure_phase=%s\npreflight_failure_line=%s\n' "$preflight_phase" "$failure_line" > "$failure_tmp"
    chmod 600 "$failure_tmp"
    mv -T -- "$failure_tmp" "$preflight_failure_file"
    printf 'preflight_failure_phase=%s preflight_failure_line=%s\n' "$preflight_phase" "$failure_line" >&2
  fi
  exit "$code"
}
trap 'failure_line=$LINENO' ERR
trap record_preflight_result EXIT
preflight_phase=context
source /opt/sub2api/releases/.active-release/assets/context.sh
[[ ! -e $release_dir/.consumed ]]
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
cd "$deploy_dir"
preflight_phase=active_runtime
[[ -f docker-compose.yml && -f .env ]]
load_release_compose_files "$deploy_dir"
[[ -f $active_slot_file && ! -L $active_slot_file ]]
active_container=$(sed -n 's/^container=//p' "$active_slot_file")
active_port=$(sed -n 's/^port=//p' "$active_slot_file")
active_image=$(sed -n 's/^image_id=//p' "$active_slot_file")
[[ $active_container =~ ^[A-Za-z0-9_.-]{1,100}$ ]]
[[ $active_port == 18080 || $active_port == 18081 ]]
[[ $active_image =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $(docker inspect -f '{{.State.Status}}' "$active_container") == running ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$active_container") == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-postgres) == healthy ]]
[[ $(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' sub2api-redis) == healthy ]]
[[ $(systemctl is-active nginx) == active ]]
preflight_phase=backup_contract
[[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
[[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
backup_exec=$(systemctl show sub2api-backup.service -p ExecStart --value)
backup_path=$(sed -n 's/.*path=\([^ ;}]*\).*/\1/p' <<<"$backup_exec" | head -n1)
[[ -f $backup_path && ! -L $backup_path ]]
grep -Fq '/run/lock/sub2api-backup-global.lock' "$backup_path"
preflight_phase=migration_contract
manifest_schema=$(jq -er '.manifest.schema' "$active_claim/gate.json")
if [[ $manifest_schema == 2 ]]; then
  [[ $(jq -er '.gate_version' "$active_claim/gate.json") == 2 ]]
  [[ $(jq -er '.profile_id' "$active_claim/gate.json") == "$(jq -er '.manifest.profile' "$active_claim/gate.json")" ]]
  [[ "$active_image" == "$(jq -er '.evidence.production_current_image_id' "$active_claim/gate.json")" ]]
  snapshot_rows=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COALESCE(json_agg(json_build_object('filename',filename,'checksum',checksum) ORDER BY filename),'[]'::json) FROM schema_migrations" | tr -d '\r\n')
  printf '%s' "$snapshot_rows" | jq -e 'type == "array"' >/dev/null
  current_snapshot_sha=$(printf '%s' "$(jq -cSn --arg image "$active_image" --argjson rows "$snapshot_rows" '{current_image_id:$image,schema_migrations:$rows}')" | sha256sum | awk '{print $1}')
  [[ "$current_snapshot_sha" == "$(jq -er '.evidence.production_snapshot_sha256' "$active_claim/gate.json")" ]]
  [[ $(jq -er '.evidence.catalog_sha256' "$active_claim/gate.json") == "$(jq -er '.manifest.catalog_sha256' "$active_claim/gate.json")" ]]
  [[ $(jq -er '.evidence.checksum_policy_sha256' "$active_claim/gate.json") == "$(jq -er '.manifest.checksum_policy_sha256' "$active_claim/gate.json")" ]]
  preflight_phase=capacity
  free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
  (( free_bytes >= minimum_free_bytes ))
  preflight_phase=compose_contract
  compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
  rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
  [[ -n $rendered_image ]]
  pre_image_id=$(docker inspect -f '{{.Image}}' "$active_container")
  [[ $active_image == "$pre_image_id" ]]
  [[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$pre_image_id" ]]
  jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
  compose_network_mode=$(sub2api_compose_network_mode "$compose_json" "$active_port")
  assert_sub2api_healthcheck_contract "$compose_json" "$compose_network_mode" "$active_port" active_compat
  assert_sub2api_runtime_contract "$active_container" "$pre_image_id" "$compose_network_mode" "$active_port" active_compat
  candidate_plan_tmp=$(mktemp "$release_dir/production-candidate-plan.XXXXXX")
  candidate_override_tmp=$(mktemp "$release_dir/production-candidate-compose.XXXXXX")
  candidate_plan_stderr=$(mktemp "$release_dir/production-candidate-plan-stderr.XXXXXX")
  {
    printf 'services:\n  sub2api:\n    image: %s\n' "$candidate_image_id"
    if [[ $compose_network_mode == host ]]; then
      printf '    environment:\n      SERVER_HOST: 127.0.0.1\n      SERVER_PORT: "%s"\n' "$active_port"
    else
      printf '    environment:\n      SERVER_HOST: 0.0.0.0\n      SERVER_PORT: "8080"\n'
    fi
  } > "$candidate_override_tmp"
  chmod 600 "$candidate_plan_tmp" "$candidate_override_tmp" "$candidate_plan_stderr"
  candidate_compose_args=("${release_compose_args[@]}" -f "$candidate_override_tmp")
  docker compose "${candidate_compose_args[@]}" run --rm --no-deps sub2api /app/sub2api --migration-plan-json > "$candidate_plan_tmp" 2> "$candidate_plan_stderr"
  jq -e 'type == "object" and (.conflicts|length)==0 and (.unknown|length)==0 and .existing_checksums_verified==true' "$candidate_plan_tmp" >/dev/null
  candidate_pending_names=$(jq -r '.pending | map(.filename) | sort | join(",")' "$candidate_plan_tmp")
  candidate_pending_checksums=$(jq -r '[.pending | sort_by(.filename, .checksum)[] | .checksum] | join(",")' "$candidate_plan_tmp")
  candidate_catalog_sha256=$(jq -er '.catalog_sha256' "$candidate_plan_tmp")
  candidate_checksum_policy_sha256=$(jq -er '.checksum_policy_sha256' "$candidate_plan_tmp")
  candidate_plan_pending_count=$(jq -er '.pending | length' "$candidate_plan_tmp")
  printf 'candidate_pending_names=%s\n' "$candidate_pending_names"
  printf 'candidate_pending_checksums=%s\n' "$candidate_pending_checksums"
  printf 'candidate_catalog_sha256=%s\n' "$candidate_catalog_sha256"
  printf 'candidate_checksum_policy_sha256=%s\n' "$candidate_checksum_policy_sha256"
  printf 'candidate_plan_pending_count=%s\n' "$candidate_plan_pending_count"
  pending_count=$(jq -er '.evidence.migration_evidence.pending | length' "$active_claim/gate.json")
  preflight_phase=completed
  printf 'preflight=pass\n'
  printf 'active_container=%s\n' "$active_container"
  printf 'active_port=%s\n' "$active_port"
  printf 'pre_switch_image_id=%s\n' "$pre_image_id"
  printf 'free_bytes=%s\n' "$free_bytes"
  printf 'migration_status=%s\n' "$([[ $pending_count == 0 ]] && printf verified || printf absent)"
  printf 'migration_pending_count=%s\n' "$pending_count"
  exit 0
fi
migration_status=verified
migration_195_status=verified
migration_196_status=not_applicable
migration_197_status=not_applicable
migration_198_status=not_applicable
migration_199_status=not_applicable
migration_200_status=not_applicable
migration_201_status=not_applicable
migration_202_status=not_applicable
migration_203_status=not_applicable
migration_204_status=not_applicable
migration_205_status=not_applicable
migration_206_status=not_applicable
migration_208_status=not_applicable
migration_209_status=not_applicable
migration_211_status=not_applicable
migration_212_status=not_applicable
migration_214_status=not_applicable
migration_215_status=not_applicable
migration_216_status=not_applicable
migration_217_status=not_applicable
migration_218_status=not_applicable
migration_219_status=not_applicable
migration_220_status=not_applicable
migration_221_status=not_applicable
migration_222_status=not_applicable
migration_223_status=not_applicable
migration_224_status=not_applicable
migration_225_status=not_applicable
migration_226_status=not_applicable
migration_227_status=not_applicable
migration_228_status=not_applicable
migration_229_status=not_applicable
migration_230_status=not_applicable
migration_231_status=not_applicable
migration_232_status=not_applicable
migration_233_status=not_applicable
migration_234_status=not_applicable
migration_235_status=not_applicable
migration_236_status=not_applicable
migration_237_status=not_applicable
migration_238_status=not_applicable
migration_239_status=not_applicable
migration_240_status=not_applicable
migration_241_status=not_applicable
 migration_242_status=not_applicable
 migration_243_status=not_applicable
 migration_244_status=not_applicable
 migration_245_status=not_applicable
migration_242_status=not_applicable
migration_243_status=not_applicable
migration_244_status=not_applicable
migration_245_status=not_applicable
while IFS=$'\t' read -r migration migration_checksum; do
  case "$migration" in
    196_ops_ingress_reject_aggregates.sql) migration_196_status=verified ;;
    197_auth_cache_invalidation_outbox.sql) migration_197_status=verified ;;
    198_normalize_managed_monitor_key_names.sql) migration_198_status=verified ;;
    199_group_reasoning_effort_policy.sql) migration_199_status=verified ;;
    200_alipay_mobile_precreate_deep_link.sql) migration_200_status=verified ;;
    201_group_auth_cache_image_generation.sql) migration_201_status=verified ;;
    202_composite_model_routes.sql) migration_202_status=verified ;;
    203_add_usage_log_session_id.sql) migration_203_status=verified ;;
    204_allow_live_usage_request_type.sql) migration_204_status=verified ;;
    205_add_group_allow_live.sql) migration_205_status=verified ;;
    206_add_users_email_alias_dedup_index_notx.sql) migration_206_status=verified ;;
    208_passkey_credentials.sql) migration_208_status=verified ;;
    209_user_usage_aggregation.sql) migration_209_status=verified ;;
    211_group_profit_control.sql) migration_211_status=verified ;;
    212_group_profit_control_auth_cache_invalidation.sql) migration_212_status=verified ;;
    214_add_usage_log_upstream_response_model.sql) migration_214_status=verified ;;
    215_add_usage_log_upstream_model_mismatch_index_notx.sql) migration_215_status=verified ;;
    216_channel_monitor_v2.sql) migration_216_status=verified ;;
    217_channel_monitor_mode.sql) migration_217_status=verified ;;
    218_channel_monitor_v2_ignored_error_categories.sql) migration_218_status=verified ;;
    219_channel_monitor_v2_seed_popular_models.sql) migration_219_status=verified ;;
    220_channel_monitor_v2_health_thresholds.sql) migration_220_status=verified ;;
    221_channel_monitor_v2_fixed_rollups.sql) migration_221_status=verified ;;
    222_channel_monitor_v2_rollup_permissions.sql) migration_222_status=verified ;;
    223_channel_monitor_v2_refresh_5m.sql) migration_223_status=verified ;;
    224_channel_monitor_v2_full_table_permissions.sql) migration_224_status=verified ;;
    225_channel_monitor_v2_default_ignore_and_cache.sql) migration_225_status=verified ;;
    226_channel_monitor_hide_throughput.sql) migration_226_status=verified ;;
    227_channel_monitor_v2_reset_factory_cache_thresholds.sql) migration_227_status=verified ;;
    228_channel_monitor_v2_privacy_defaults.sql) migration_228_status=verified ;;
    229_group_video_model_prices.sql) migration_229_status=verified ;;
    230_group_audio_voice_pricing.sql) migration_230_status=verified ;;
    231_group_search_price_per_1k.sql) migration_231_status=verified ;;
    232_clear_non_grok_video_generation_config.sql) migration_232_status=verified ;;
    233_upstream_management.sql) migration_233_status=verified ;;
    234_group_model_pricing.sql) migration_234_status=verified ;;
    235_group_usage_daily_rollups.sql) migration_235_status=verified ;;
    236_group_usage_rollup_timezone.sql) migration_236_status=verified ;;
    237_image_cost_routing.sql) migration_237_status=verified ;;
    238_upstream_account_lifecycle.sql) migration_238_status=verified ;;
    239_reconcile_non_grok_video_pricing.sql) migration_239_status=verified ;;
    240_upstream_observation_preference.sql) migration_240_status=verified ;;
    241_precise_upstream_effective_rate.sql) migration_241_status=verified ;;
    242_user_platform_quotas_add_cn_providers.sql) migration_242_status=verified ;;
    243_backfill_codex_fingerprint_seed.sql) migration_243_status=verified ;;
    244_channel_model_time_pricing.sql) migration_244_status=verified ;;
    245_channel_monitor_quota_mode.sql) migration_245_status=verified ;;
    242_user_platform_quotas_add_cn_providers.sql) migration_242_status=verified ;;
    243_backfill_codex_fingerprint_seed.sql) migration_243_status=verified ;;
    244_channel_model_time_pricing.sql) migration_244_status=verified ;;
    245_channel_monitor_quota_mode.sql) migration_245_status=verified ;;
  esac
  migration_state=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='$migration'")
  if [[ -z $migration_state ]]; then
    migration_status=absent
    case "$migration" in
      195_upstream_scheduling_monitor_rates.sql) migration_195_status=absent ;;
      196_ops_ingress_reject_aggregates.sql) migration_196_status=absent ;;
      197_auth_cache_invalidation_outbox.sql) migration_197_status=absent ;;
      198_normalize_managed_monitor_key_names.sql) migration_198_status=absent ;;
      199_group_reasoning_effort_policy.sql) migration_199_status=absent ;;
      200_alipay_mobile_precreate_deep_link.sql) migration_200_status=absent ;;
      201_group_auth_cache_image_generation.sql) migration_201_status=absent ;;
      202_composite_model_routes.sql) migration_202_status=absent ;;
      203_add_usage_log_session_id.sql) migration_203_status=absent ;;
      204_allow_live_usage_request_type.sql) migration_204_status=absent ;;
      205_add_group_allow_live.sql) migration_205_status=absent ;;
      206_add_users_email_alias_dedup_index_notx.sql) migration_206_status=absent ;;
      208_passkey_credentials.sql) migration_208_status=absent ;;
      209_user_usage_aggregation.sql) migration_209_status=absent ;;
      211_group_profit_control.sql) migration_211_status=absent ;;
      212_group_profit_control_auth_cache_invalidation.sql) migration_212_status=absent ;;
      214_add_usage_log_upstream_response_model.sql) migration_214_status=absent ;;
      215_add_usage_log_upstream_model_mismatch_index_notx.sql) migration_215_status=absent ;;
      216_channel_monitor_v2.sql) migration_216_status=absent ;;
      217_channel_monitor_mode.sql) migration_217_status=absent ;;
      218_channel_monitor_v2_ignored_error_categories.sql) migration_218_status=absent ;;
      219_channel_monitor_v2_seed_popular_models.sql) migration_219_status=absent ;;
      220_channel_monitor_v2_health_thresholds.sql) migration_220_status=absent ;;
      221_channel_monitor_v2_fixed_rollups.sql) migration_221_status=absent ;;
      222_channel_monitor_v2_rollup_permissions.sql) migration_222_status=absent ;;
      223_channel_monitor_v2_refresh_5m.sql) migration_223_status=absent ;;
      224_channel_monitor_v2_full_table_permissions.sql) migration_224_status=absent ;;
      225_channel_monitor_v2_default_ignore_and_cache.sql) migration_225_status=absent ;;
      226_channel_monitor_hide_throughput.sql) migration_226_status=absent ;;
      227_channel_monitor_v2_reset_factory_cache_thresholds.sql) migration_227_status=absent ;;
      228_channel_monitor_v2_privacy_defaults.sql) migration_228_status=absent ;;
      229_group_video_model_prices.sql) migration_229_status=absent ;;
      230_group_audio_voice_pricing.sql) migration_230_status=absent ;;
      231_group_search_price_per_1k.sql) migration_231_status=absent ;;
      232_clear_non_grok_video_generation_config.sql) migration_232_status=absent ;;
      233_upstream_management.sql) migration_233_status=absent ;;
      234_group_model_pricing.sql) migration_234_status=absent ;;
      235_group_usage_daily_rollups.sql) migration_235_status=absent ;;
      236_group_usage_rollup_timezone.sql) migration_236_status=absent ;;
      237_image_cost_routing.sql) migration_237_status=absent ;;
      238_upstream_account_lifecycle.sql) migration_238_status=absent ;;
      239_reconcile_non_grok_video_pricing.sql) migration_239_status=absent ;;
      240_upstream_observation_preference.sql) migration_240_status=absent ;;
      241_precise_upstream_effective_rate.sql) migration_241_status=absent ;;
      242_user_platform_quotas_add_cn_providers.sql) migration_242_status=absent ;;
      243_backfill_codex_fingerprint_seed.sql) migration_243_status=absent ;;
      244_channel_model_time_pricing.sql) migration_244_status=absent ;;
      245_channel_monitor_quota_mode.sql) migration_245_status=absent ;;
      242_user_platform_quotas_add_cn_providers.sql) migration_242_status=absent ;;
      243_backfill_codex_fingerprint_seed.sql) migration_243_status=absent ;;
      244_channel_model_time_pricing.sql) migration_244_status=absent ;;
      245_channel_monitor_quota_mode.sql) migration_245_status=absent ;;
    esac
  else
    [[ $migration_state == "$migration|$migration_checksum" ]]
  fi
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$active_claim/gate.json")
preflight_phase=capacity
free_bytes=$(df -PB1 /var/lib/docker 2>/dev/null | awk 'NR==2{print $4}' || df -PB1 / | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
preflight_phase=compose_contract
compose_json=$(docker compose "${release_compose_args[@]}" config --format json)
rendered_image=$(jq -r '.services.sub2api.image // empty' <<<"$compose_json")
[[ -n $rendered_image ]]
pre_image_id=$(docker inspect -f '{{.Image}}' "$active_container")
[[ $active_image == "$pre_image_id" ]]
[[ $(docker image inspect -f '{{.Id}}' "$rendered_image") == "$pre_image_id" ]]
jq -e '.services.sub2api.volumes | any(.target == "/app/data" and (.type == "bind" or .type == "volume"))' <<<"$compose_json" >/dev/null
compose_network_mode=$(sub2api_compose_network_mode "$compose_json" "$active_port")
assert_sub2api_healthcheck_contract "$compose_json" "$compose_network_mode" "$active_port" active_compat
assert_sub2api_runtime_contract "$active_container" "$pre_image_id" "$compose_network_mode" "$active_port" active_compat
preflight_phase=completed
printf 'preflight=pass\n'
printf 'active_container=%s\n' "$active_container"
printf 'active_port=%s\n' "$active_port"
printf 'pre_switch_image_id=%s\n' "$pre_image_id"
printf 'free_bytes=%s\n' "$free_bytes"
printf 'migration_status=%s\n' "$migration_status"
printf 'migration_195_status=%s\n' "$migration_195_status"
printf 'migration_196_status=%s\n' "$migration_196_status"
printf 'migration_197_status=%s\n' "$migration_197_status"
printf 'migration_198_status=%s\n' "$migration_198_status"
printf 'migration_199_status=%s\n' "$migration_199_status"
printf 'migration_200_status=%s\n' "$migration_200_status"
printf 'migration_201_status=%s\n' "$migration_201_status"
printf 'migration_202_status=%s\n' "$migration_202_status"
printf 'migration_203_status=%s\n' "$migration_203_status"
printf 'migration_204_status=%s\n' "$migration_204_status"
printf 'migration_205_status=%s\n' "$migration_205_status"
printf 'migration_206_status=%s\n' "$migration_206_status"
printf 'migration_208_status=%s\n' "$migration_208_status"
printf 'migration_209_status=%s\n' "$migration_209_status"
printf 'migration_211_status=%s\n' "$migration_211_status"
printf 'migration_212_status=%s\n' "$migration_212_status"
printf 'migration_214_status=%s\n' "$migration_214_status"
printf 'migration_215_status=%s\n' "$migration_215_status"
printf 'migration_216_status=%s\n' "$migration_216_status"
printf 'migration_217_status=%s\n' "$migration_217_status"
printf 'migration_218_status=%s\n' "$migration_218_status"
printf 'migration_219_status=%s\n' "$migration_219_status"
printf 'migration_220_status=%s\n' "$migration_220_status"
printf 'migration_221_status=%s\n' "$migration_221_status"
printf 'migration_222_status=%s\n' "$migration_222_status"
printf 'migration_223_status=%s\n' "$migration_223_status"
printf 'migration_224_status=%s\n' "$migration_224_status"
printf 'migration_225_status=%s\n' "$migration_225_status"
printf 'migration_226_status=%s\n' "$migration_226_status"
printf 'migration_227_status=%s\n' "$migration_227_status"
printf 'migration_228_status=%s\n' "$migration_228_status"
printf 'migration_229_status=%s\n' "$migration_229_status"
printf 'migration_230_status=%s\n' "$migration_230_status"
printf 'migration_231_status=%s\n' "$migration_231_status"
printf 'migration_232_status=%s\n' "$migration_232_status"
printf 'migration_233_status=%s\n' "$migration_233_status"
printf 'migration_234_status=%s\n' "$migration_234_status"
printf 'migration_235_status=%s\n' "$migration_235_status"
printf 'migration_236_status=%s\n' "$migration_236_status"
printf 'migration_237_status=%s\n' "$migration_237_status"
printf 'migration_238_status=%s\n' "$migration_238_status"
printf 'migration_239_status=%s\n' "$migration_239_status"
printf 'migration_240_status=%s\n' "$migration_240_status"
printf 'migration_241_status=%s\n' "$migration_241_status"
printf 'migration_242_status=%s\n' "$migration_242_status"
printf 'migration_243_status=%s\n' "$migration_243_status"
printf 'migration_244_status=%s\n' "$migration_244_status"
printf 'migration_245_status=%s\n' "$migration_245_status"
printf 'migration_242_status=%s\n' "$migration_242_status"
printf 'migration_243_status=%s\n' "$migration_243_status"
printf 'migration_244_status=%s\n' "$migration_244_status"
printf 'migration_245_status=%s\n' "$migration_245_status"
