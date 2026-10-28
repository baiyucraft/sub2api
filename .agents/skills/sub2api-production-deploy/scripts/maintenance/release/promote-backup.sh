#!/usr/bin/env bash
set -Eeuo pipefail

backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
release_id=${RELEASE_ID:?RELEASE_ID is required}
transport_name=${TRANSPORT_ARTIFACT_NAME:?TRANSPORT_ARTIFACT_NAME is required}
artifact_sha=${ARTIFACT_SHA256:?ARTIFACT_SHA256 is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}
[[ $release_id =~ ^(182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275|276|277|278|279|280|281)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $transport_name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
[[ $artifact_sha =~ ^[0-9a-f]{64}$ ]]
[[ -d $backup_root && ! -L $backup_root ]]
_required_1=$(realpath -e -- "$backup_root")
[[ ${_required_1} == "$backup_root" ]]
lock_file="$backup_root/.release-promotion.lock"
[[ ! -L $lock_file ]]
exec 9>"$lock_file"
[[ -f $lock_file && ! -L $lock_file ]]
flock -n 9
source_inventory=$(find "$backup_root" -path "$backup_root/releases" -prune -o -type f -name "$transport_name" -print)
sources=()
if [[ -n $source_inventory ]]; then mapfile -t sources <<<"$source_inventory"; fi
[[ ${#sources[@]} == 1 ]]
source_artifact=${sources[0]}
source_checksum="$source_artifact.sha256"
[[ -f $source_checksum && ! -L $source_checksum ]]
grep -Eq "^${artifact_sha}[[:space:]]+\*?${transport_name}$" "$source_checksum"
_required_2=$(sha256sum "$source_artifact" | awk '{print $1}')
[[ ${_required_2} == "$artifact_sha" ]]
free_bytes=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
profile=${release_id%%-*}
release_root="$backup_root/releases/$profile"
for dir in "$backup_root/releases" "$release_root"; do
  if [[ -e $dir || -L $dir ]]; then
    [[ -d $dir && ! -L $dir ]]
  else
    install -d -m 700 "$dir"
  fi
  _required_3=$(realpath -e -- "$dir")
  [[ ${_required_3} == "$dir" ]]
done
target="$release_root/$release_id"
staging="$release_root/.$release_id.staging.$$"

verify_bundle() {
  local bundle=$1 expected_manifest expected_bundle_checksum entry_count
  [[ -d $bundle && ! -L $bundle ]] || return 1
  _required_4=$(realpath -e -- "$bundle") || return 1
  [[ ${_required_4} == "$bundle" ]] || return 1
  entry_count=$(find "$bundle" -mindepth 1 -maxdepth 1 -print | wc -l) || return 1
  [[ $entry_count == 4 ]] || return 1
  for name in artifact.tar.age artifact.tar.age.sha256 manifest bundle.sha256; do
    [[ -f $bundle/$name && ! -L $bundle/$name ]] || return 1
    _required_5=$(stat -c '%h' "$bundle/$name") || return 1
    [[ ${_required_5} == 1 ]] || return 1
  done
  expected_manifest=$(printf 'release_id=%s\ntransport_artifact=%s\nsha256=%s' \
    "$release_id" "$transport_name" "$artifact_sha") || return 1
  [[ $(<"$bundle/manifest") == "$expected_manifest" ]] || return 1
  [[ $(<"$bundle/artifact.tar.age.sha256") == "$artifact_sha  artifact.tar.age" ]] || return 1
  expected_bundle_checksum=$(cd "$bundle" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest) || return 1
  [[ $(<"$bundle/bundle.sha256") == "$expected_bundle_checksum" ]] || return 1
  (cd "$bundle" && sha256sum -c bundle.sha256 >/dev/null) || return 1
  _required_6=$(sha256sum "$bundle/artifact.tar.age" | awk '{print $1}') || return 1
  [[ ${_required_6} == "$artifact_sha" ]] || return 1
}

if [[ -d $target && ! -L $target ]]; then
  verify_bundle "$target"
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
(cd "$staging" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest > bundle.sha256)
verify_bundle "$staging"
mv -T -- "$staging" "$target"
trap - EXIT
printf 'backup_promotion=verified\n'
printf 'release_artifact=%s\n' "$release_id"
printf 'release_sha256=%s\n' "$artifact_sha"
printf 'release_free_bytes=%s\n' "$free_bytes"
