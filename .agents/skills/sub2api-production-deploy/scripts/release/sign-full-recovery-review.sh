#!/usr/bin/env bash
set -Eeuo pipefail

# Independent full-review issuer; never delegates to the Gate signer and
# never creates, rotates or relaxes permissions on the existing trust key.
{
  required_query_1=$(id -u)
  [[ ${required_query_1} == 0 ]]
}
[[ $# == 5 ]]
request=$1
classification=$2
receipt=$3
approval=$4
output=$5
for input in "$request" "$classification" "$receipt" "$approval"; do
  {
    required_query_2=$(stat -c '%u:%g:%a:%h' "$input")
    [[ -f $input && ! -L $input && ${required_query_2} =~ ^0:0:(400|600):1$ ]]
  }
  {
    required_query_3=$(realpath -e -- "$input")
    [[ ${required_query_3} == "$input" ]]
  }
done
[[ ! -e $output && ! -L $output ]]
[[ -f /opt/sub2api-release-signer/vm-gate-ed25519.pem && ! -L /opt/sub2api-release-signer/vm-gate-ed25519.pem ]]
{
  required_query_4=$(stat -c '%u:%g:%a:%h' /opt/sub2api-release-signer/vm-gate-ed25519.pem)
  [[ ${required_query_4} == 0:0:600:1 ]]
}
script_parent=$(dirname -- "${BASH_SOURCE[0]}")
script_dir=$(cd -- "$script_parent" && pwd -P)
cd -- "$script_dir/.."
unset PYTHONPATH PYTHONHOME PYTHONOPTIMIZE PYTHONSTARTUP
exec python3 -B -m release.full_recovery_authorization sign \
  --request "$request" --classification "$classification" --receipt "$receipt" \
  --approval "$approval" --output "$output"
