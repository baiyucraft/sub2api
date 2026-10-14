#!/usr/bin/env bash
set -Eeuo pipefail

# Real Docker Compose rendering only: no service, image build, volume, network,
# or production connection is created by this integration.
helper=${1:?helper path is required}
image=${2:?existing immutable local image ID is required}
[[ $(id -u) == 0 && $image =~ ^sha256:[0-9a-f]{64}$ ]]
[[ -f $helper && ! -L $helper ]]
source "$helper"
work=$(mktemp -d /tmp/sub2api-recovery-compose-test.XXXXXXXX)
chmod 700 "$work"
passed=0
cleanup() {
  local code=$?
  trap - EXIT
  [[ $work =~ ^/tmp/sub2api-recovery-compose-test\.[A-Za-z0-9]+$ && -d $work && ! -L $work ]]
  if ((code == 0)); then
    rm -rf -- "$work"
    printf 'compose_integration=pass\ncompose_cases=%s\nowned_staging_cleanup=true\n' "$passed"
  else
    printf 'compose_integration=fail\ncompose_cases=%s\nowned_staging_cleanup=false\n' "$passed"
  fi
  exit "$code"
}
trap cleanup EXIT
mkdir -m 700 "$work/source" "$work/snapshot" "$work/restored"
root="$work/source"; snap="$work/snapshot"; dest="$work/restored"
printf 'COMPOSE_FILE=docker-compose.yml:docker-compose.local.yml\nIMAGE=%s\n' "$image" > "$root/.env"
printf 'RECOVERY_TEST_VALUE=fixture-only\n' > "$root/app.env"
cat > "$root/docker-compose.yml" <<'COMPOSE'
services:
  sub2api:
    image: ${IMAGE}
    env_file:
      - path: app.env
        required: true
    volumes:
      - ./data:/app/data
    ports:
      - 127.0.0.1:18080:8080
COMPOSE
cat > "$root/docker-compose.local.yml" <<'COMPOSE'
services:
  sub2api:
    environment:
      SERVER_HOST: 0.0.0.0
      SERVER_PORT: '8080'
COMPOSE
chmod 600 "$root/"* "$root/.env"
# Host interpolation/selection inputs must not influence either rendering.
export COMPOSE_FILE=/must-not-be-used.yml IMAGE=invalid HOST_ENV_SECRET=not-to-be-used
recovery_compose_snapshot "$root" "$snap" >/dev/null
recovery_compose_restore_verify "$snap" "$dest" "$image" >/dev/null
[[ -f $dest/app.env && -f $dest/docker-compose.local.yml ]]
((passed+=1))
expect_fail() {
  if "$@" >/dev/null 2>&1; then return 1; fi
  ((passed+=1))
}
printf 'changed\n' >> "$dest/app.env"
expect_fail recovery_compose_verify_live "$snap" "$dest" "$image"
install -m 600 "$snap/app.env" "$dest/app.env"
expect_fail recovery_compose_verify_live "$snap" "$dest" "sha256:$(printf '%064d' 0)"
printf 'services: {}\n' > "$dest/docker-compose.release-active.yml"
chmod 600 "$dest/docker-compose.release-active.yml"
expect_fail recovery_compose_verify_live "$snap" "$dest" "$image"
rm -- "$dest/docker-compose.release-active.yml"
saved=$(<"$snap/.recovery-compose-files.sha256")
chmod 600 "$snap/.recovery-compose-files.sha256"
printf '%s\n%s\n' "$saved" "${saved%%$'\n'*}" > "$snap/.recovery-compose-files.sha256"
chmod 400 "$snap/.recovery-compose-files.sha256"
expect_fail recovery_compose_verify_live "$snap" "$dest" "$image"
chmod 600 "$snap/.recovery-compose-files.sha256"
printf '%s\n' "$saved" > "$snap/.recovery-compose-files.sha256"
chmod 400 "$snap/.recovery-compose-files.sha256"
rm -- "$dest/app.env"
ln -s "$snap/app.env" "$dest/app.env"
expect_fail recovery_compose_verify_live "$snap" "$dest" "$image"
rm -- "$dest/app.env"
install -m 600 "$snap/app.env" "$dest/app.env"
rm -- "$dest/no-release-active-override"
expect_fail recovery_compose_verify_live "$snap" "$dest" "$image"
install -m 600 "$snap/no-release-active-override" "$dest/no-release-active-override"
recovery_compose_verify_live "$snap" "$dest" "$image" >/dev/null
((passed+=1))
mkdir -m 700 "$work/unsupported"
install -m 600 "$root/docker-compose.yml" "$work/original.yml"
printf 'include: outside.yml\n' >> "$root/docker-compose.yml"
expect_fail recovery_compose_snapshot "$root" "$work/unsupported"
[[ -z $(find "$work/unsupported" -mindepth 1 -maxdepth 1 -print -quit) ]]
for key in '"include"' '"\x69nclude"'; do
  install -m 600 "$work/original.yml" "$root/docker-compose.yml"
  printf '%s: ../outside.yml\n' "$key" >> "$root/docker-compose.yml"
  expect_fail recovery_compose_snapshot "$root" "$work/unsupported"
  [[ -z $(find "$work/unsupported" -mindepth 1 -maxdepth 1 -print -quit) ]]
done
cat > "$root/docker-compose.yml" <<'COMPOSE'
services:
  sub2api:
    "extends":
      file: ../outside.yml
      service: sub2api
COMPOSE
expect_fail recovery_compose_snapshot "$root" "$work/unsupported"
[[ -z $(find "$work/unsupported" -mindepth 1 -maxdepth 1 -print -quit) ]]
# Invalid .env selectors fail even when called in an if/negative-test context.
printf 'COMPOSE_FILE=docker-compose.yml:docker-compose.yml\n' > "$root/.env"
expect_fail load_release_compose_files "$root"
printf 'COMPOSE_FILE=docker-compose.yml:\n' > "$root/.env"
expect_fail load_release_compose_files "$root"
