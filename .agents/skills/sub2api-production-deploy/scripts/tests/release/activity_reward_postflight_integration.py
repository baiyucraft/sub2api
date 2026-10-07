"""Linux shell fault injection in a VM-only synthetic workspace, no production I/O."""
import shlex
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.ssh import SSHRunner


def main():
    runner = SSHRunner()
    remote = runner.create_temp_dir('local_vm', '/opt/sub2api-deploy/release-input', 'reward-postflight-test')
    raw_root = '/opt/sub2api/releases/' + Path(remote).name
    try:
        runner.upload_file('local_vm', ROOT / 'maintenance/release/activity-reward-cost-postflight.sh', remote + '/postflight.sh', 0o700)
        runner.upload_file('local_vm', ROOT / 'release/backfill-activity-reward-costs.sh', remote + '/backfill-activity-reward-costs.sh', 0o700)
        script = 'set -Eeuo pipefail\nremote=' + shlex.quote(remote) + '\nraw_root=' + shlex.quote(raw_root) + '\n' + r'''
[[ ! -e $raw_root && ! -L $raw_root ]]
install -d -m 700 "$raw_root/logs"
trap 'rm -f -- "$raw_root/logs/production.raw.log"; rmdir -- "$raw_root/logs" "$raw_root"' EXIT
install -m 600 /dev/null "$raw_root/logs/production.raw.log"
cat > "$remote/case.sh" <<'CASE'
set -Eeuo pipefail
release_profile=264
deployment_mode=downtime
active_container=sub2api
assets_dir=$remote
state_dir=$remote/$scenario
mkdir -m 700 "$state_dir"
candidate_compose_args=(-f synthetic)
SUB2API_RELEASE_RAW_LOG=$raw_root/logs/production.raw.log
docker() {
  case "$1" in
    ps)
      [[ $scenario != ps_failure ]] || return 71
      printf 'synthetic-postgres\n'
      ;;
    inspect)
      if [[ $2 == -f ]]; then printf 'false\n'; return; fi
      [[ $scenario != inspect_failure ]] || return 72
      if [[ $scenario == extra_app ]]; then printf '[{"Config":{"Cmd":["/app/sub2api"],"Labels":{}}}]\n'
      elif [[ $scenario == entrypoint_app ]]; then printf '[{"Config":{"Entrypoint":["/app/sub2api"],"Cmd":[],"Labels":{}}}]\n'
      elif [[ $scenario == path_app ]]; then printf '[{"Path":"/app/sub2api","Config":{"Cmd":[],"Labels":{}}}]\n'
      elif [[ $scenario == shell_app ]]; then printf '[{"Config":{"Cmd":["sh","-c","exec /app/sub2api --serve"],"Labels":{}}}]\n'
      elif [[ $scenario == compose_app ]]; then printf '[{"Config":{"Cmd":[],"Labels":{"com.docker.compose.service":"sub2api"}}}]\n'
      elif [[ $scenario == invalid_inspect ]]; then printf '[{}]\n'
      else printf '[{"Config":{"Cmd":["postgres"],"Labels":{}}}]\n'; fi
      ;;
    exec)
      sql=$(cat)
      [[ $scenario != database_failure ]] || return 73
      mode=check
      if [[ $sql == *'SELECT public.backfill_activity_reward_costs()'* ]]; then mode=apply; fi
      printf '%s\n' "$mode" >> "$state_dir/order"
      verified=true missing=0 orphan=0 inserted=0
      if [[ $scenario == missing && $mode == check && ! -e $state_dir/applied ]]; then verified=false missing=1; fi
      if [[ $scenario == orphan ]]; then verified=false orphan=1; fi
      if [[ $mode == apply ]]; then touch "$state_dir/applied"; [[ $scenario != missing ]] || inserted=1; fi
      printf '{"verified":%s,"missing_count":%s,"orphan_count":%s,"mismatch_count":0,"inserted_rows":%s,"daily":[]}\n' "$verified" "$missing" "$orphan" "$inserted"
      ;;
    compose)
      [[ $* == *'--clear-dashboard-cache'* && $* == *'--no-deps'* && $* == *'--rm'* ]]
      printf 'cache\n' >> "$state_dir/order"
      [[ $scenario != cache_failure ]] || return 74
      if [[ $scenario == cache_invalid ]]; then printf '{"dashboard_cache_cleared":false}\n'
      else printf '{"dashboard_cache_cleared":true}\n'; fi
      ;;
    *) return 75 ;;
  esac
}
source "$assets_dir/postflight.sh"
sub2api_activity_reward_cost_postflight
CASE
chmod 700 "$remote/case.sh"
for scenario in success missing extra_app entrypoint_app path_app shell_app compose_app invalid_inspect ps_failure inspect_failure database_failure orphan cache_failure cache_invalid; do
  status=0
  remote="$remote" raw_root="$raw_root" scenario="$scenario" bash "$remote/case.sh" > "$remote/$scenario.out" 2> "$remote/$scenario.err" || status=$?
  if [[ $scenario == success || $scenario == missing ]]; then
    [[ $status == 0 ]]
    [[ $(paste -sd, "$remote/$scenario/order") == check,apply,check,cache ]]
    jq -e '.verified==true and .missing_count==0' "$remote/$scenario/activity-reward-cost-after.json" >/dev/null
    jq -e '.dashboard_cache_cleared==true' "$remote/$scenario/dashboard-cache-refresh.json" >/dev/null
    for result in "$remote/$scenario"/*.json; do [[ $(stat -c '%U:%G:%a:%h' "$result") == root:root:600:1 ]]; done
  else
    [[ $status != 0 && ! -e $remote/$scenario/dashboard-cache-refresh.json ]]
    if [[ $scenario == *_app || $scenario == invalid_inspect || $scenario == ps_failure || $scenario == inspect_failure || $scenario == database_failure ]]; then
      [[ ! -e $remote/$scenario/order ]]
    elif [[ $scenario == orphan ]]; then
      [[ $(cat "$remote/$scenario/order") == check ]]
    fi
  fi
done
printf 'reward_cost_postflight_integration=pass\nchecks=14\n'
'''
        print(runner.run('local_vm', script, {'reward_cost_postflight_integration', 'checks'}, timeout=120).values)
    finally:
        quoted = shlex.quote(remote)
        runner.run('local_vm', f'test -d {quoted} && test ! -L {quoted} && test "$(realpath -e -- {quoted})" = {quoted} && rm -rf -- {quoted} && printf "cleanup=pass\\n"', {'cleanup'})


if __name__ == '__main__':
    main()
