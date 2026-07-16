#!/usr/bin/env bash
set -Eeuo pipefail

backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
release_id=${RELEASE_ID:?RELEASE_ID is required}
transport_name=${TRANSPORT_ARTIFACT_NAME:?TRANSPORT_ARTIFACT_NAME is required}
artifact_sha=${ARTIFACT_SHA256:?ARTIFACT_SHA256 is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}
[[ $release_id =~ ^(182|187|191)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $transport_name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
[[ $artifact_sha =~ ^[0-9a-f]{64}$ ]]
[[ -d $backup_root && ! -L $backup_root ]]
exec 9>"$backup_root/.release-promotion.lock"
flock -n 9
mapfile -t sources < <(find "$backup_root" -path "$backup_root/releases" -prune -o -type f -name "$transport_name" -print)
[[ ${#sources[@]} == 1 ]]
source_artifact=${sources[0]}
source_checksum="$source_artifact.sha256"
[[ -f $source_checksum && ! -L $source_checksum ]]
grep -Eq "^${artifact_sha}[[:space:]]+\*?${transport_name}$" "$source_checksum"
[[ $(sha256sum "$source_artifact" | awk '{print $1}') == "$artifact_sha" ]]
free_bytes=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
profile=${release_id%%-*}
release_root="$backup_root/releases/$profile"
install -d -m 700 "$backup_root/releases" "$release_root"
target="$release_root/$release_id"
staging="$release_root/.$release_id.staging.$$"
if [[ -d $target && ! -L $target ]]; then
  [[ -f $target/artifact.tar.age && -f $target/artifact.tar.age.sha256 && -f $target/manifest && -f $target/bundle.sha256 ]]
  [[ $(find "$target" -mindepth 1 -maxdepth 1 -type f | wc -l) == 4 ]]
  (cd "$target" && sha256sum -c bundle.sha256 >/dev/null)
  [[ $(sha256sum "$target/artifact.tar.age" | awk '{print $1}') == "$artifact_sha" ]]
  grep -Fxq "release_id=$release_id" "$target/manifest"
  printf 'backup_promotion=verified\n'
  printf 'release_artifact=%s\n' "$release_id"
  printf 'release_sha256=%s\n' "$artifact_sha"
  printf 'release_free_bytes=%s\n' "$free_bytes"
  exit 0
fi
[[ ! -e $target && ! -L $target && ! -e $staging && ! -L $staging ]]
install -d -m 700 "$staging"
cleanup() { rm -rf "$staging"; }
trap cleanup EXIT
install -m 600 "$source_artifact" "$staging/artifact.tar.age"
printf '%s  artifact.tar.age\n' "$artifact_sha" > "$staging/artifact.tar.age.sha256"
printf 'release_id=%s\ntransport_artifact=%s\nsha256=%s\n' "$release_id" "$transport_name" "$artifact_sha" > "$staging/manifest"
(cd "$staging" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest > bundle.sha256 && sha256sum -c bundle.sha256 >/dev/null)
mv -T -- "$staging" "$target"
trap - EXIT
printf 'backup_promotion=verified\n'
printf 'release_artifact=%s\n' "$release_id"
printf 'release_sha256=%s\n' "$artifact_sha"
printf 'release_free_bytes=%s\n' "$free_bytes"
