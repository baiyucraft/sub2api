#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
if [[ -d $state_dir && ! -L $state_dir ]]; then
  [[ -f $state_dir/recovery-point.age && -f $state_dir/recovery-point.age.sha256 ]]
  find "$state_dir" -mindepth 1 -maxdepth 1 \
    ! -name recovery-point.age ! -name recovery-point.age.sha256 ! -name pre-image-id \
    -exec rm -rf -- {} +
else
  [[ ! -e $state_dir && ! -L $state_dir ]]
  [[ $(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT COUNT(*) FROM schema_migrations WHERE filename='$migration'") == 0 ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
  [[ $(systemctl is-active nginx) == active ]]
  [[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
  [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
fi
printf 'cleaned_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/plaintext-cleaned"
chmod 400 "$active_claim/plaintext-cleaned"
printf 'plaintext_state_removed=true\n'
