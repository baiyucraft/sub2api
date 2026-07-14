#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source /opt/sub2api/releases/.active-release/assets/context.sh
verify_migrations_restored() {
  [[ -f $state_dir/pre-migrations.tsv && ! -L $state_dir/pre-migrations.tsv ]]
  diff -u "$state_dir/pre-migrations.tsv" <(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations ORDER BY filename") >/dev/null
}
if [[ -d $state_dir && ! -L $state_dir ]]; then
  if [[ -f $state_dir/recovery-point.age && -f $state_dir/recovery-point.age.sha256 ]]; then
    find "$state_dir" -mindepth 1 -maxdepth 1 \
      ! -name recovery-point.age ! -name recovery-point.age.sha256 ! -name pre-image-id \
      -exec rm -rf -- {} +
  elif [[ -f $state_dir/pre-image-id && -f $state_dir/SHA256SUMS ]]; then
    [[ ! -e $state_dir/recovery-point.age && ! -L $state_dir/recovery-point.age ]]
    [[ ! -e $state_dir/recovery-point.age.sha256 && ! -L $state_dir/recovery-point.age.sha256 ]]
    (cd "$state_dir" && sha256sum -c SHA256SUMS >/dev/null)
    verify_migrations_restored
    [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
    [[ $(systemctl is-active nginx) == active ]]
    [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
    rm -rf -- "$state_dir"
  else
    [[ ! -e $state_dir/pre-image-id && ! -L $state_dir/pre-image-id ]]
    [[ ! -e $state_dir/SHA256SUMS && ! -L $state_dir/SHA256SUMS ]]
    [[ -f $state_dir/restored.committed && ! -L $state_dir/restored.committed ]]
    verify_migrations_restored
    [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
    [[ $(systemctl is-active nginx) == active ]]
    [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
    rm -rf -- "$state_dir"
  fi
else
  [[ ! -e $state_dir && ! -L $state_dir ]]
  [[ $(docker inspect -f '{{.State.Health.Status}}' sub2api) == healthy ]]
  [[ $(systemctl is-active nginx) == active ]]
  [[ $(systemctl is-active sub2api-backup.service 2>/dev/null || true) != active ]]
  [[ $(systemctl is-enabled sub2api-backup.timer 2>/dev/null || true) == enabled ]]
fi
printf 'cleaned_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$active_claim/plaintext-cleaned"
chmod 400 "$active_claim/plaintext-cleaned"
printf 'plaintext_state_removed=true\n'
