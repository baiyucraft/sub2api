#!/usr/bin/env bash
set -Eeuo pipefail

backup_root=${BACKUP_ROOT:-/srv/sub2api-backups}
artifact_name=${ARTIFACT_NAME:?ARTIFACT_NAME is required}
artifact_sha256=${ARTIFACT_SHA256:?ARTIFACT_SHA256 is required}
minimum_free_bytes=${MINIMUM_FREE_BYTES:-5368709120}

[[ $artifact_name =~ ^sub2api-release181-[0-9]{8}T[0-9]{6}Z\.tar\.age$ ]]
[[ $artifact_sha256 =~ ^[0-9a-f]{64}$ ]]
[[ -d $backup_root && -w $backup_root ]]
exec 9>/run/lock/sub2api-backup-promotion.lock
flock -n 9

mapfile -t sources < <(find "$backup_root" -path "$backup_root/releases" -prune -o -type f -name "$artifact_name" -print)
[[ ${#sources[@]} == 1 ]]
source_artifact=${sources[0]}
source_checksum="$source_artifact.sha256"
[[ -f $source_checksum ]]
grep -Eq "^${artifact_sha256}[[:space:]]+\*?${artifact_name}$" "$source_checksum"
[[ $(sha256sum "$source_artifact" | awk '{print $1}') == "$artifact_sha256" ]]

free_bytes=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_bytes >= minimum_free_bytes ))
release_dir="$backup_root/releases/181"
install -d -m 700 "$release_dir"
[[ ! -e $release_dir/$artifact_name && ! -e $release_dir/$artifact_name.sha256 ]]
temp_artifact="$release_dir/.$artifact_name.tmp"
temp_checksum="$release_dir/.$artifact_name.sha256.tmp"
trap 'rm -f "$temp_artifact" "$temp_checksum"' EXIT

install -m 600 "$source_artifact" "$temp_artifact"
printf '%s  %s\n' "$artifact_sha256" "$artifact_name" > "$temp_checksum"
mv -f "$temp_artifact" "$release_dir/$artifact_name"
mv -f "$temp_checksum" "$release_dir/$artifact_name.sha256"
(cd "$release_dir" && sha256sum -c "$artifact_name.sha256" >/dev/null)
[[ $(sha256sum "$release_dir/$artifact_name" | awk '{print $1}') == "$artifact_sha256" ]]
free_after=$(df -PB1 "$backup_root" | awk 'NR==2{print $4}')
(( free_after >= minimum_free_bytes ))

printf 'release_artifact=%s\n' "$artifact_name"
printf 'release_sha256=%s\n' "$artifact_sha256"
printf 'release_free_bytes=%s\n' "$free_after"
printf 'release_promotion=verified\n'

trap - EXIT
