#!/usr/bin/env bash
set -Eeuo pipefail

release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
source "$release_dir/assets/context.sh"
[[ -d $state_dir && ! -L $state_dir ]]
[[ -f $state_dir/recovery-point.age && -f $state_dir/recovery-point.age.sha256 ]]
find "$state_dir" -mindepth 1 -maxdepth 1 \
  ! -name recovery-point.age ! -name recovery-point.age.sha256 ! -name pre-image-id \
  -exec rm -rf -- {} +
printf 'cleaned_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$release_dir/.claimed/plaintext-cleaned"
chmod 400 "$release_dir/.claimed/plaintext-cleaned"
printf 'plaintext_state_removed=true\n'
