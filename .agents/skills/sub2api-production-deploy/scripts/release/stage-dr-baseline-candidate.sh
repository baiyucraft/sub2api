#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

# Assemble a new version baseline candidate from a fresh postrelease capture
# and its actual signed Gate/candidate archive. This is not DR
# promotion: the old verified pointer remains untouched until a real restored,
# separately signed schema-1 DR evidence bundle passes the existing promoter.
release_id=${1:?release ID required}
input=${2:?private staging directory required}
expected_artifact=${3:?encrypted artifact SHA-256 required}
{
  required_query_1=$(id -u)
  [[ ${required_query_1} == 0 && $release_id =~ ^(271|272|273|274|275|276|277|278|279|280|281)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ && $expected_artifact =~ ^[0-9a-f]{64}$ ]]
}
[[ $input =~ ^/srv/sub2api-backups/dr-baseline-stage\.[A-Za-z0-9]{8}$ ]]
{
  required_query_2=$(realpath -e "$input")
  required_query_3=$(stat -c '%U:%G:%a' "$input")
  [[ -d $input && ! -L $input && ${required_query_2} == "$input" && ${required_query_3} == root:root:700 ]]
}
baseline_profile=${release_id%%-*}
root="/srv/sub2api-backups/releases/$baseline_profile"
trust=/opt/sub2api-dr-trust/vm-gate-ed25519.pub
verifier=/usr/local/libexec/sub2api-verify-dr-evidence
{
  required_query_4=$(sha256sum "$trust" | awk '{print $1}')
  [[ ${required_query_4} == ea0b628532f8d85d0e57921b5b010c7f00ef8b0f9701da2b0d4ea31105553e08 ]]
}
{
  required_query_5=$(stat -c '%U:%G:%a:%h' "$trust")
  [[ -f $trust && ! -L $trust && ${required_query_5} == root:root:644:1 ]]
}
{
  required_query_6=$(stat -c '%U:%G:%a:%h' "$verifier")
  [[ -f $verifier && ! -L $verifier && ${required_query_6} == root:root:700:1 ]]
}
{
  required_query_7=$(sha256sum "$verifier" | awk '{print $1}')
  [[ ${required_query_7} == f43b10ae4e9d52b970a9395487a3cf31ae2ccb0d869b5d0a5ee134fd0ca81d89 ]]
}
for directory in "$root"; do
  {
    required_query_8=$(realpath -e "$directory")
    required_query_9=$(stat -c '%U:%G:%a' "$directory")
    [[ -d $directory && ! -L $directory && ${required_query_8} == "$directory" && ${required_query_9} == root:root:700 ]]
  }
done
{
  required_query_10=$(find "$input" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort)
  [[ ${required_query_10} == $'artifact.tar.age\ncandidate.tar.gz\ncapture-receipt.json\ngate.json\ngate.sig' ]]
}
for file in artifact.tar.age capture-receipt.json gate.json gate.sig candidate.tar.gz; do {
                                                                                            required_query_11=$(stat -c '%U:%G:%a:%h' "$input/$file")
                                                                                            [[ -f $input/$file && ! -L $input/$file && ${required_query_11} == root:root:600:1 ]]
                                                                                          }; done
{
  required_query_12=$(sha256sum "$input/artifact.tar.age" | awk '{print $1}')
  [[ ${required_query_12} == "$expected_artifact" ]]
}
"$verifier" "$trust" "$input/gate.json" "$input/gate.sig" >/dev/null
gate_release=$(jq -er '.manifest.release_id' "$input/gate.json")
gate_profile=$(jq -er '.manifest.profile' "$input/gate.json")
gate_schema=$(jq -er '.manifest.schema' "$input/gate.json")
gate_commit=$(jq -er '.manifest.commit_sha' "$input/gate.json")
candidate_image=$(jq -er '.evidence.candidate_image_id' "$input/gate.json")
candidate_sha=$(jq -er '.evidence.candidate_archive_sha256' "$input/gate.json")
[[ $gate_release == "$release_id" && $gate_profile == "$baseline_profile" && $gate_schema == 2 && $gate_commit =~ ^[0-9a-f]{40}$ ]]
[[ $release_id == ${baseline_profile}-${gate_commit:0:12}-* && $candidate_image =~ ^sha256:[0-9a-f]{64}$ && $candidate_sha =~ ^[0-9a-f]{64}$ ]]
{
  required_query_13=$(sha256sum "$input/candidate.tar.gz" | awk '{print $1}')
  [[ ${required_query_13} == "$candidate_sha" ]]
}
{
  required_query_14=$(stat -c '%s' "$input/artifact.tar.age")
  jq -e --arg release "$release_id" --arg image "$candidate_image" --arg artifact "$expected_artifact" --argjson bytes "${required_query_14}" '
  .schema==1 and .kind=="encrypted-production-baseline-capture" and .capture_purpose=="postrelease" and
  .release_id==$release and .captured_image_id==$image and .pre_switch_image_id==$image and
  .artifact_sha256==$artifact and .artifact_bytes==$bytes and
  (.captured_at|test("^[0-9]{8}T[0-9]{6}Z$")) and .cross_store_writes_frozen==false
' "$input/capture-receipt.json"
} >/dev/null
artifact_name="sub2api-$(jq -er '.captured_at' "$input/capture-receipt.json").tar.age"
{
  required_query_15=$(df -PB1 "$root" | awk 'NR==2{print $4}')
  [[ ${required_query_15} -ge 5368709120 ]]
}

lock="$root/.promotion.lock"
if [[ ! -e $lock && ! -L $lock ]]; then (set -o noclobber; : > "$lock"); chmod 600 "$lock"; fi
{
  required_query_16=$(stat -c '%U:%G:%a:%h' "$lock")
  [[ -f $lock && ! -L $lock && ${required_query_16} == root:root:600:1 ]]
}
exec 9<>"$lock"
flock 9
for directory in "$root/candidates" "$root/promotion-input" "$root/verified-bundles"; do
  if [[ ! -e $directory && ! -L $directory ]]; then mkdir -m 700 "$directory"; fi
  {
    required_query_17=$(realpath -e "$directory")
    required_query_18=$(stat -c '%U:%G:%a' "$directory")
    [[ -d $directory && ! -L $directory && ${required_query_17} == "$directory" && ${required_query_18} == root:root:700 ]]
  }
done
staging=$(mktemp -d "$root/candidates/.staging-$release_id.XXXXXXXX")
pointer="$root/.candidate.$$.tmp"
trap 'rm -rf -- "$staging"; rm -f -- "$pointer"' EXIT
install -o root -g root -m 600 "$input/artifact.tar.age" "$staging/artifact.tar.age"
for file in gate.json gate.sig candidate.tar.gz; do install -o root -g root -m 600 "$input/$file" "$staging/$file"; done
printf 'release_id=%s\nstate=restore_pending\nartifact_name=%s\nartifact_sha256=%s\ncandidate_archive_sha256=%s\ncandidate_image_id=%s\n' "$release_id" "$artifact_name" "$expected_artifact" "$candidate_sha" "$candidate_image" > "$staging/manifest"
chmod 644 "$staging/manifest"
gate_output="/opt/sub2api-deploy/release-gates/$release_id/output"
{
  required_query_19=$(sha256sum "$staging/gate.json" | awk '{print $1}')
  required_query_20=$(sha256sum "$staging/gate.sig" | awk '{print $1}')
  printf '%s  %s/gate.json\n%s  %s/gate.sig\n%s  %s/candidate.tar.gz\n' "${required_query_19}" "$gate_output" "${required_query_20}" "$gate_output" "$candidate_sha" "$gate_output"
} > "$staging/SHA256SUMS"
(cd "$staging" && sha256sum artifact.tar.age candidate.tar.gz gate.json gate.sig manifest SHA256SUMS > bundle.sha256 && sha256sum -c bundle.sha256 >/dev/null)
chmod 644 "$staging/bundle.sha256"
for file in "$staging"/*; do sync -f "$file"; done
sync -f "$staging"
target="$root/candidates/$release_id"
if [[ -e $target || -L $target ]]; then
  {
    required_query_21=$(stat -c '%U:%G:%a' "$target") || exit $?
    [[ -d $target && ! -L $target && ${required_query_21} == root:root:700 ]]
  }
  {
    required_query_22=$(find "$target" -mindepth 1 -maxdepth 1 -printf '%f\n' | LC_ALL=C sort) || exit $?
    [[ ${required_query_22} == $'SHA256SUMS\nartifact.tar.age\nbundle.sha256\ncandidate.tar.gz\ngate.json\ngate.sig\nmanifest' ]]
  }
  for file in SHA256SUMS artifact.tar.age bundle.sha256 candidate.tar.gz gate.json gate.sig manifest; do
    {
      required_query_23=$(stat -c '%h' "$target/$file") || exit $?
      [[ -f $target/$file && ! -L $target/$file && ${required_query_23} == 1 ]]
    }
    cmp -s "$staging/$file" "$target/$file"
  done
else mv -T -- "$staging" "$target"; sync -f "$root/candidates"; fi
if [[ -e $root/candidate || -L $root/candidate ]]; then
  {
    required_query_24=$(readlink "$root/candidate") || exit $?
    [[ -L $root/candidate && ${required_query_24} =~ ^candidates/${baseline_profile}-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}$ ]]
  }
fi
ln -s "candidates/$release_id" "$pointer"
mv -T -- "$pointer" "$root/candidate"
sync -f "$root"
{
  required_query_25=$(sha256sum "$target/bundle.sha256" | awk '{print $1}')
  printf 'candidate_status=restore_pending\nrelease_id=%s\ncandidate_bundle_sha256=%s\n' "$release_id" "${required_query_25}"
}
