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
install -m 400 "$release_dir/gate.json" "$active_claim/gate.json"
install -m 400 "$release_dir/gate.sig" "$active_claim/gate.sig"
mv -T -- "$release_dir/candidate.tar.gz" "$active_claim/candidate.tar.gz"
install -d -m 700 "$active_claim/assets"
for path in "$release_dir"/assets/*; do
  [[ -f $path && ! -L $path ]]
  install -m 500 "$path" "$active_claim/assets/$(basename -- "$path")"
done
(cd "$active_claim" && find gate.json gate.sig candidate.tar.gz assets -type f -print0 | sort -z | xargs -0 sha256sum > CLAIM_SHA256SUMS && sha256sum -c CLAIM_SHA256SUMS >/dev/null)
chmod 400 "$active_claim/CLAIM_SHA256SUMS"
openssl pkeyutl -verify -pubin -inkey "$trust_key" -rawin \
  -in "$active_claim/gate.json" -sigfile "$active_claim/gate.sig" >/dev/null
gate="$active_claim/gate.json"
[[ $(jq -er '.manifest.release_id' "$gate") == "$release_id" ]]
[[ $(jq -er '.manifest.profile' "$gate") == 182 ]]
[[ $(jq -er '.manifest.origin' "$gate") == https://github.com/baiyucraft/sub2api.git ]]
[[ $(jq -er '.manifest.vm_identity' "$gate") == sub2api-dev ]]
[[ $(jq -er '.evidence.integration_verified' "$gate") == true ]]
[[ $(jq -er '.evidence.vm_restore_verified' "$gate") == true ]]
[[ $(jq -er '.manifest.expires_at' "$gate") -ge $(date +%s) ]]
for path in "$active_claim"/assets/*; do
  [[ -f $path && ! -L $path ]]
  name=$(basename -- "$path")
  case "$name" in
    mask-backup-units.sh|restore-backup-units.sh) source="deploy/maintenance/181/$name" ;;
    *) source="deploy/maintenance/release/$name" ;;
  esac
  expected=$(jq -er --arg source "$source" '.manifest.release_asset_sha256[$source]' "$gate")
  [[ $(sha256sum "$path" | awk '{print $1}') == "$expected" ]]
done
while IFS=$'\t' read -r source expected; do
  case "$source" in
    deploy/maintenance/release/*) name=${source##*/} ;;
    deploy/maintenance/181/mask-backup-units.sh) name=mask-backup-units.sh ;;
    deploy/maintenance/181/restore-backup-units.sh) name=restore-backup-units.sh ;;
    *) continue ;;
  esac
  [[ -f $active_claim/assets/$name && ! -L $active_claim/assets/$name ]]
  [[ $(sha256sum "$active_claim/assets/$name" | awk '{print $1}') == "$expected" ]]
done < <(jq -r '.manifest.release_asset_sha256 | to_entries[] | [.key,.value] | @tsv' "$gate")
candidate_image_id=$(jq -er '.evidence.candidate_image_id' "$gate")
candidate_sha=$(jq -er '.evidence.candidate_archive_sha256' "$gate")
commit=$(jq -er '.manifest.commit_sha' "$gate")
candidate_tag="sub2api:baiyu-0.1.153-baiyu-$commit"
[[ $candidate_image_id =~ ^sha256:[0-9a-f]{64}$ ]]
[[ $candidate_sha =~ ^[0-9a-f]{64}$ ]]
[[ $(sha256sum "$active_claim/candidate.tar.gz" | awk '{print $1}') == "$candidate_sha" ]]
gzip -t "$active_claim/candidate.tar.gz"
gzip -dc "$active_claim/candidate.tar.gz" | docker load >/dev/null
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
