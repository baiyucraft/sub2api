#!/usr/bin/env bash
set -Eeuo pipefail

signer_dir=${SIGNER_DIR:-/opt/sub2api-release-signer}
validator_source=${VALIDATOR_SOURCE:?VALIDATOR_SOURCE is required}
validator_target=/usr/local/libexec/sub2api-vm-validate
signer_target=/usr/local/libexec/sub2api-sign-gate
private_key="$signer_dir/vm-gate-ed25519.pem"
public_key="$signer_dir/vm-gate-ed25519.pub"
[[ $(id -u) == 0 ]]
[[ -f $validator_source && ! -L $validator_source ]]
install -d -m 700 "$signer_dir"
install -d -m 755 /usr/local/libexec
install -o root -g root -m 700 "$validator_source" "$validator_target"
if [[ ! -e $private_key && ! -e $public_key ]]; then
  umask 077
  openssl genpkey -algorithm ED25519 -out "$private_key"
  openssl pkey -in "$private_key" -pubout -out "$public_key"
fi
[[ -f $private_key && ! -L $private_key && -f $public_key && ! -L $public_key ]]
chmod 600 "$private_key"
chmod 644 "$public_key"
helper_tmp=$(mktemp /usr/local/libexec/.sub2api-sign-gate.XXXXXX)
cat > "$helper_tmp" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
gate=${1:?gate path is required}
signature=${2:?signature path is required}
[[ $gate =~ ^/opt/sub2api-deploy/release-gates/182-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}/output/gate\.json$ ]]
expected_signature=${gate%/gate.json}/gate.sig
[[ $signature == "$expected_signature" ]]
[[ -f $gate && ! -L $gate && $(stat -c '%U:%G:%a' "$gate") == root:root:400 ]]
[[ ! -e $signature && ! -L $signature ]]
openssl pkeyutl -sign -inkey /opt/sub2api-release-signer/vm-gate-ed25519.pem -rawin -in "$gate" -out "$signature.tmp"
chmod 400 "$signature.tmp"
mv -T -- "$signature.tmp" "$signature"
EOF
chmod 700 "$helper_tmp"
chown root:root "$helper_tmp"
mv -T -- "$helper_tmp" "$signer_target"
openssl pkey -pubin -in "$public_key" -noout
printf 'signer_status=ready\n'
printf 'public_key_sha256=%s\n' "$(sha256sum "$public_key" | awk '{print $1}')"
printf 'validator_sha256=%s\n' "$(sha256sum "$validator_target" | awk '{print $1}')"
