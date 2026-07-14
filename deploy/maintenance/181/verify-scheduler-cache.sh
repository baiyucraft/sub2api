#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${DEPLOY_DIR:-/opt/sub2api}
cd "$deploy_dir"

redis_password=$(awk -F= '$1=="REDIS_PASSWORD"{print substr($0,index($0,"=")+1)}' "$deploy_dir/.env")
redis_cli=(docker exec)
if [[ -n $redis_password ]]; then
  redis_cli+=(-e "REDISCLI_AUTH=$redis_password")
fi
redis_cli+=(sub2api-redis redis-cli --no-auth-warning --raw)

mapfile -t accounts < <(docker exec sub2api-postgres psql -X -A -F '|' -t -U sub2api -d sub2api -c "
  SELECT a.id, a.name
    FROM accounts a
    JOIN upstream_configs c ON c.id = a.upstream_config_id
   WHERE a.deleted_at IS NULL
     AND c.deleted_at IS NULL
     AND lower(trim(trailing '/' FROM c.site_url)) = 'https://www.codexapis.com'
     AND a.name IN ('刀哥-pro', '刀哥-plus')
     AND a.platform = 'openai'
     AND a.status = 'active'
     AND a.schedulable
   ORDER BY a.id")
[[ ${#accounts[@]} == 2 ]]

for account in "${accounts[@]}"; do
  IFS='|' read -r account_id account_name <<<"$account"
  cache_json=$("${redis_cli[@]}" GET "sched:v2:acc:$account_id")
  [[ -n $cache_json ]]
  jq -e --argjson id "$account_id" --arg name "$account_name" \
    '.ID == $id and .Name == $name and .Platform == "openai" and .Status == "active" and .Schedulable == true' \
    >/dev/null <<<"$cache_json"
done

printf 'scheduler_cache_accounts_verified=2\n'
