#!/usr/bin/env bash
set -Eeuo pipefail

release_id=${RELEASE_ID:?RELEASE_ID is required}
release_dir=${RELEASE_DIR:?RELEASE_DIR is required}
trust_key=${TRUST_KEY:-/opt/sub2api-release-trust/vm-gate-ed25519.pub}
[[ $release_id =~ ^182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
[[ $release_dir == "/opt/sub2api/releases/$release_id" ]]
[[ -d $release_dir && ! -L $release_dir ]]
active_claim=/opt/sub2api/releases/.active-release
[[ ! -e $active_claim && ! -L $active_claim ]]
mkdir "$active_claim"
chmod 700 "$active_claim"
cleanup_claim() { rm -rf "$active_claim"; }
trap cleanup_claim ERR INT TERM
printf 'release_id=%s\n' "$release_id" > "$active_claim/release_id"
chmod 400 "$active_claim/release_id"
[[ -f $trust_key && ! -L $trust_key ]]
[[ -f $release_dir/gate.json && -f $release_dir/gate.sig && -f $release_dir/candidate.tar.gz ]]
[[ -f $release_dir/ASSET_SHA256SUMS ]]
(cd "$release_dir" && sha256sum -c ASSET_SHA256SUMS >/dev/null)
openssl pkeyutl -verify -pubin -inkey "$trust_key" -rawin \
  -in "$release_dir/gate.json" -sigfile "$release_dir/gate.sig" >/dev/null
[[ $(jq -er '.manifest.release_id' "$release_dir/gate.json") == "$release_id" ]]
[[ $(jq -er '.manifest.profile' "$release_dir/gate.json") == 182 ]]
[[ $(jq -er '.manifest.origin' "$release_dir/gate.json") == https://github.com/baiyucraft/sub2api.git ]]
[[ $(jq -er '.manifest.vm_identity' "$release_dir/gate.json") == sub2api-dev ]]
[[ $(jq -er '.evidence.integration_verified' "$release_dir/gate.json") == true ]]
[[ $(jq -er '.evidence.vm_restore_verified' "$release_dir/gate.json") == true ]]
[[ $(jq -er '.manifest.expires_at' "$release_dir/gate.json") -ge $(date +%s) ]]
for path in "$release_dir"/assets/*; do
  [[ -f $path && ! -L $path ]]
  name=$(basename -- "$path")
  case "$name" in
    mask-backup-units.sh|restore-backup-units.sh) source="deploy/maintenance/181/$name" ;;
    *) source="deploy/maintenance/release/$name" ;;
  esac
  expected=$(jq -er --arg source "$source" '.manifest.release_asset_sha256[$source]' "$release_dir/gate.json")
  [[ $(sha256sum "$path" | awk '{print $1}') == "$expected" ]]
done
while IFS=$'\t' read -r source expected; do
  case "$source" in
    deploy/maintenance/release/*) name=${source##*/} ;;
    deploy/maintenance/181/mask-backup-units.sh) name=mask-backup-units.sh ;;
    deploy/maintenance/181/restore-backup-units.sh) name=restore-backup-units.sh ;;
    *) continue ;;
  esac
  [[ -f $release_dir/assets/$name && ! -L $release_dir/assets/$name ]]
  [[ $(sha256sum "$release_dir/assets/$name" | awk '{print $1}') == "$expected" ]]
done < <(jq -r '.manifest.release_asset_sha256 | to_entries[] | [.key,.value] | @tsv' "$release_dir/gate.json")
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$release_dir/gate.json")
candidate_sha=$(jq -er '.evidence.candidate_archive_sha256' "$release_dir/gate.json")
commit=$(jq -er '.manifest.commit_sha' "$release_dir/gate.json")
candidate_tag="sub2api:baiyu-0.1.153-baiyu-$commit"
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $candidate_sha =~ ^[0-9a-f]{64}$ ]]
[[ $(sha256sum "$release_dir/candidate.tar.gz" | awk '{print $1}') == "$candidate_sha" ]]
gzip -t "$release_dir/candidate.tar.gz"
gzip -dc "$release_dir/candidate.tar.gz" | docker load >/dev/null
[[ $(docker image inspect -f '{{.Id}}' "$candidate_image_id") == "$candidate_image_id" ]]
docker tag "$candidate_image_id" "$candidate_tag"
[[ $(docker image inspect -f '{{.Id}}' "$candidate_tag") == "$candidate_image_id" ]]
marker="$release_dir/.prepared"
[[ ! -e $marker ]]
[[ ! -e $release_dir/.claimed && ! -e $release_dir/.consumed ]]
mkdir "$release_dir/.claimed"
chmod 700 "$release_dir/.claimed"
printf 'release_id=%s\ncandidate_image_id=%s\n' "$release_id" "$candidate_image_id" > "$marker.tmp"
mv -T -- "$marker.tmp" "$marker"
chmod 400 "$marker"
trap - ERR INT TERM
printf 'prepared=true\n'
printf 'candidate_image_id=%s\n' "$candidate_image_id"
printf 'candidate_archive_sha256=%s\n' "$candidate_sha"
printf 'trust_key_sha256=%s\n' "$(sha256sum "$trust_key" | awk '{print $1}')"
