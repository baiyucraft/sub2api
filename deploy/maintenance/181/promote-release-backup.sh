#!/usr/bin/env bash
set -Eeuo pipefail

backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
transport_name=${TRANSPORT_ARTIFACT_NAME:?TRANSPORT_ARTIFACT_NAME is required}
release_name=${RELEASE_ARTIFACT_NAME:?RELEASE_ARTIFACT_NAME is required}
artifact_sha256=${ARTIFACT_SHA256:?ARTIFACT_SHA256 is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}

report_success() {
  set +e
  printf 'transport_artifact=%s\n' "$transport_name"
  printf 'release_artifact=%s\n' "$release_name"
  [[ -z ${release_stem:-} ]] || printf 'release_bundle=%s\n' "$release_stem"
  printf 'release_sha256=%s\n' "$artifact_sha256"
  [[ -z ${free_after:-} ]] || printf 'release_free_bytes=%s\n' "$free_after"
  [[ -z ${release_layout:-} ]] || printf 'release_layout=%s\n' "$release_layout"
  printf 'release_promotion=verified\n'
  exit 0
}

verify_bundle() {
  local bundle=$1 expected_manifest expected_bundle_checksum entry_count
  [[ -d $bundle && ! -L $bundle ]]
  entry_count=$(find "$bundle" -mindepth 1 -maxdepth 1 -print | wc -l)
  [[ $entry_count == 4 ]]
  for name in artifact.tar.age artifact.tar.age.sha256 manifest bundle.sha256; do
    [[ -f $bundle/$name && ! -L $bundle/$name ]]
  done
  expected_manifest=$(printf 'version=1\ntransport_artifact=%s\nrelease_artifact=%s\nsha256=%s' \
    "$transport_name" "$release_name" "$artifact_sha256")
  [[ $(<"$bundle/manifest") == "$expected_manifest" ]]
  [[ $(<"$bundle/artifact.tar.age.sha256") == "$artifact_sha256  artifact.tar.age" ]]
  expected_bundle_checksum=$(cd "$bundle" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest)
  [[ $(<"$bundle/bundle.sha256") == "$expected_bundle_checksum" ]]
  (cd "$bundle" && sha256sum -c bundle.sha256 >/dev/null)
  [[ $(sha256sum "$bundle/artifact.tar.age" | awk '{print $1}') == "$artifact_sha256" ]]
}

[[ $transport_name =~ ^sub2api-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
[[ $release_name =~ ^sub2api-release181-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
[[ ${transport_name#sub2api-} == ${release_name#sub2api-release181-} ]]
[[ $artifact_sha256 =~ ^[0-9a-f]{64}$ ]]
[[ -d $backup_root && -w $backup_root && ! -L $backup_root ]]
[[ $(realpath -e -- "$backup_root") == "$backup_root" ]]
exec 9>"$backup_root/.release181-promotion.lock"
flock -n 9

mapfile -t sources < <(find "$backup_root" -path "$backup_root/releases" -prune -o -type f -name "$transport_name" -print)
[[ ${#sources[@]} == 1 ]]
source_artifact=${sources[0]}
source_checksum="$source_artifact.sha256"
[[ -f $source_checksum && ! -L $source_checksum ]]
grep -Eq "^${artifact_sha256}[[:space:]]+\*?${transport_name}$" "$source_checksum"
[[ $(sha256sum "$source_artifact" | awk '{print $1}') == "$artifact_sha256" ]]

free_bytes=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
releases_root="$backup_root/releases"
if [[ -e $releases_root || -L $releases_root ]]; then
  [[ -d $releases_root && ! -L $releases_root ]]
else
  install -d -m 700 "$releases_root"
fi
[[ $(realpath -e -- "$releases_root") == "$releases_root" ]]
release_root="$releases_root/181"
if [[ -e $release_root || -L $release_root ]]; then
  [[ -d $release_root && ! -L $release_root ]]
else
  install -d -m 700 "$release_root"
fi
[[ $(realpath -e -- "$release_root") == "$release_root" ]]

# Releases promoted by the original script remain valid and are handled idempotently.
legacy_artifact="$release_root/$release_name"
legacy_checksum="$legacy_artifact.sha256"
if [[ -e $legacy_artifact || -L $legacy_artifact || -e $legacy_checksum || -L $legacy_checksum ]]; then
  [[ -f $legacy_artifact && ! -L $legacy_artifact ]]
  [[ -f $legacy_checksum && ! -L $legacy_checksum ]]
  grep -Eq "^${artifact_sha256}[[:space:]]+\*?${release_name}$" "$legacy_checksum"
  [[ $(sha256sum "$legacy_artifact" | awk '{print $1}') == "$artifact_sha256" ]]
  release_layout=legacy
  report_success
fi

release_stem=${release_name%.tar.age}
bundle_dir="$release_root/$release_stem"
staging_dir="$release_root/.$release_stem.staging.$$"
if [[ -e $bundle_dir || -L $bundle_dir ]]; then
  verify_bundle "$bundle_dir"
  report_success
fi
[[ ! -e $staging_dir && ! -L $staging_dir ]]

cleanup() {
  rm -f -- "$staging_dir/artifact.tar.age" "$staging_dir/artifact.tar.age.sha256" \
    "$staging_dir/manifest" "$staging_dir/bundle.sha256"
  rmdir -- "$staging_dir" 2>/dev/null || true
}
trap cleanup EXIT

install -d -m 700 "$staging_dir"
install -m 600 "$source_artifact" "$staging_dir/artifact.tar.age"
printf '%s  artifact.tar.age\n' "$artifact_sha256" > "$staging_dir/artifact.tar.age.sha256"
printf 'version=1\ntransport_artifact=%s\nrelease_artifact=%s\nsha256=%s\n' \
  "$transport_name" "$release_name" "$artifact_sha256" > "$staging_dir/manifest"
(cd "$staging_dir" && sha256sum artifact.tar.age artifact.tar.age.sha256 manifest > bundle.sha256)
verify_bundle "$staging_dir"

free_after=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_after >= minimum_free_bytes ))

# The release becomes visible as one directory. The exclusive lock and the
# target preflight above make replacement a hard failure without using -f.
mv -T -- "$staging_dir" "$bundle_dir"
trap - EXIT
report_success
