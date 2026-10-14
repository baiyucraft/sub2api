#!/usr/bin/env bash
set -Eeuo pipefail

# Independent full-review issuer; never delegates to the Gate signer and
# never creates, rotates or relaxes permissions on the existing trust key.
[[ $(id -u) == 0 ]]
[[ $# == 5 ]]
request=$1
classification=$2
receipt=$3
approval=$4
output=$5
for input in "$request" "$classification" "$receipt" "$approval"; do
  [[ -f $input && ! -L $input && $(stat -c '%u:%g:%a:%h' "$input") =~ ^0:0:(400|600):1$ ]]
  [[ $(realpath -e -- "$input") == "$input" ]]
done
[[ ! -e $output && ! -L $output ]]
[[ -f /opt/sub2api-release-signer/vm-gate-ed25519.pem && ! -L /opt/sub2api-release-signer/vm-gate-ed25519.pem ]]
[[ $(stat -c '%u:%g:%a:%h' /opt/sub2api-release-signer/vm-gate-ed25519.pem) == 0:0:600:1 ]]
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
cd -- "$script_dir/.."
unset PYTHONPATH PYTHONHOME PYTHONOPTIMIZE PYTHONSTARTUP
exec python3 -B -m release.full_recovery_authorization sign \
  --request "$request" --classification "$classification" --receipt "$receipt" \
  --approval "$approval" --output "$output"
