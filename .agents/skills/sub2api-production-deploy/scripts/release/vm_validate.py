from __future__ import annotations

import argparse
import json
import shlex
import time
from pathlib import Path

from .gate import verify_gate
from .manifest import manifest_release_asset_layout, release_unit_relative_paths, validate_manifest_profile_contract
from .profiles import get_profile
from .bootstrap import install_vm_validator
from .paths import LAYOUT_SKILL_V1, RELEASE_PACKAGE_ROOT, TRUSTED_VM_PUBLIC_KEY
from .ssh import SSHRunner


TRUSTED_KEY = TRUSTED_VM_PUBLIC_KEY
SPACE_CLEANER = RELEASE_PACKAGE_ROOT / "vm-space-clean.sh"
SPACE_FIELDS = {
    "cleanup_mode",
    "space_status",
    "free_bytes",
    "required_bytes",
    "container_candidates",
    "container_candidate_logical_bytes",
    "image_candidates",
    "image_candidate_logical_bytes",
    "removed_containers",
    "removed_images",
    "build_cache_policy",
    "build_cache_records",
    "build_cache_gc_attempted",
}

VM_WORKER_FIELDS = {
    "worker_status",
    "worker_pid",
    "worker_token_match",
    "worker_exit_code",
    "gate_stage",
    "gate_failure_category",
    "gate_output_ready",
    "gate_signature_ready",
    "candidate_archive_ready",
    "raw_log_status",
    "raw_log_bytes",
}


class VMWorkerNotStartedError(RuntimeError):
    """The remote host repeatedly proved that no worker handshake exists."""


class VMWorkerStateUnknownError(RuntimeError):
    """The worker may still exist, so its input and evidence must be preserved."""


def _remote_vm_worker_script(
    *,
    remote_root: str,
    remote_manifest: str,
    remote_output: str,
    validator: str,
    release_id: str,
) -> str:
    """Create a detached VM validator wrapper.

    The wrapper owns its stdout/stderr and writes a small state protocol. This
    keeps a long Docker build independent from the lifetime of the SSH channel.
    Raw output remains on the VM and is never returned through the structured
    stdout interface.
    """

    quoted = {name: shlex.quote(value) for name, value in {
        "remote_root": remote_root,
        "remote_manifest": remote_manifest,
        "remote_output": remote_output,
        "validator": validator,
        "release_id": release_id,
    }.items()}
    worker_dir = f"{remote_root}/worker"
    wrapper = f"{worker_dir}/run-validator.sh"
    raw_root = f"/opt/sub2api-deploy/release-logs/{release_id}"
    raw_log = f"{raw_root}/vm-validate.raw.log"
    return f'''set -Eeuo pipefail
remote_root={quoted["remote_root"]}
worker_dir={shlex.quote(worker_dir)}
wrapper={shlex.quote(wrapper)}
raw_root={shlex.quote(raw_root)}
raw_log={shlex.quote(raw_log)}
validator={quoted["validator"]}
manifest={quoted["remote_manifest"]}
output={quoted["remote_output"]}
release_id={quoted["release_id"]}
install -d -o 0 -g 0 -m 700 "$worker_dir"
[[ -d "$worker_dir" && ! -L "$worker_dir" && $(stat -c '%u:%g:%a' "$worker_dir") == 0:0:700 ]]
command -v nohup >/dev/null
command -v setsid >/dev/null
if [[ -e "$raw_root" ]]; then
  [[ -d "$raw_root" && ! -L "$raw_root" && $(stat -c '%u:%g:%a' "$raw_root") == 0:0:700 ]]
else
  install -d -o 0 -g 0 -m 700 "$raw_root"
fi
if [[ -e "$raw_log" ]]; then
  [[ -f "$raw_log" && ! -L "$raw_log" && $(stat -c '%u:%g:%a:%h' "$raw_log") == 0:0:600:1 ]]
else
  : >"$raw_log"
  chown 0:0 "$raw_log"
  chmod 600 "$raw_log"
fi
cat >"$wrapper" <<'RUNNER'
#!/usr/bin/env bash
set -Eeuo pipefail
umask 077
worker_dir={shlex.quote(worker_dir)}
raw_log={shlex.quote(raw_log)}
validator={shlex.quote(validator)}
manifest={shlex.quote(remote_manifest)}
output={shlex.quote(remote_output)}
release_id={shlex.quote(release_id)}
state_write() {{
  local key=$1 value=$2
  local tmp="$worker_dir/$key.tmp"
  printf '%s\n' "$value" >"$tmp"
  chmod 600 "$tmp"
  mv -T -- "$tmp" "$worker_dir/$key"
}}
boot_id=$(tr -d '\r\n' < /proc/sys/kernel/random/boot_id)
start_token=$(awk '{{print $22}}' /proc/$$/stat)
state_write boot_id "$boot_id"
state_write start_token "$start_token"
state_write pid "$$"
state_write status running
set +e
"$validator" "$manifest" "$output" >>"$raw_log" 2>&1
exit_code=$?
set -e
gate_root=${{output%/output}}
if [[ -d "$gate_root" && ! -L "$gate_root" ]]; then
  gate_logs="$gate_root/logs"
  if [[ -e "$gate_logs" ]]; then
    [[ -d "$gate_logs" && ! -L "$gate_logs" && $(stat -c '%u:%g:%a' "$gate_logs") == 0:0:700 ]] || exit_code=98
  else
    install -d -o 0 -g 0 -m 700 "$gate_logs" || exit_code=98
  fi
  if [[ "$exit_code" != 98 ]] && ! install -o 0 -g 0 -m 600 "$raw_log" "$gate_logs/vm-validate.raw.log"; then
    [[ "$exit_code" != 0 ]] || exit_code=98
  fi
elif [[ "$exit_code" == 0 ]]; then
  exit_code=98
fi
state_write exit_code "$exit_code"
state_write status exited
exit "$exit_code"
RUNNER
chmod 700 "$wrapper"
[[ $(stat -c '%u:%g:%a:%h' "$wrapper") == 0:0:700:1 ]]
launch_state_tmp="$worker_dir/status.tmp"
printf 'launching\n' >"$launch_state_tmp"
chmod 600 "$launch_state_tmp"
mv -T -- "$launch_state_tmp" "$worker_dir/status"
nohup setsid "$wrapper" >>"$raw_log" 2>&1 </dev/null &
launcher_pid=$!
printf 'worker_started=true\n'
printf 'launcher_pid=%s\n' "$launcher_pid"
printf 'raw_log_ready=true\n'
'''


def _poll_vm_worker_script(
    *,
    remote_root: str,
    remote_output: str,
    release_id: str,
) -> str:
    worker_dir = f"{remote_root}/worker"
    raw_log = f"/opt/sub2api-deploy/release-logs/{release_id}/vm-validate.raw.log"
    return f'''set -Eeuo pipefail
worker_dir={shlex.quote(worker_dir)}
output={shlex.quote(remote_output)}
raw_log={shlex.quote(raw_log)}
state_contract=ok
read_state() {{
  local target=$1 path=$2 default=$3 state_value
  if [[ ! -e "$path" ]]; then printf -v "$target" '%s' "$default"; return; fi
  if [[ ! -f "$path" || -L "$path" || $(stat -c '%u:%g:%a:%h' "$path") != 0:0:600:1 ]]; then
    state_contract=invalid
    printf -v "$target" '%s' invalid
    return
  fi
  state_value=$(tr -d '\r\n' <"$path")
  printf -v "$target" '%s' "$state_value"
}}
read_optional() {{
  local path=$1 default=$2
  [[ -f "$path" && ! -L "$path" ]] && tr -d '\r\n' <"$path" || printf '%s' "$default"
}}
read_state pid "$worker_dir/pid" 0
read_state boot_id "$worker_dir/boot_id" absent
read_state start_token "$worker_dir/start_token" absent
read_state worker_status "$worker_dir/status" absent
read_state worker_exit_code "$worker_dir/exit_code" pending
if [[ "$state_contract" != ok ]]; then worker_status=invalid; fi
token_match=no
if [[ "$pid" =~ ^[0-9]+$ && "$pid" -gt 1 && -r "/proc/$pid/stat" && -r /proc/sys/kernel/random/boot_id ]]; then
  current_boot_id=$(tr -d '\r\n' < /proc/sys/kernel/random/boot_id)
  current_start_token=$(awk '{{print $22}}' "/proc/$pid/stat")
  if [[ "$current_boot_id" == "$boot_id" && "$current_start_token" == "$start_token" ]]; then token_match=yes; fi
fi
gate_root=${{output%/output}}
printf 'worker_status=%s\n' "$worker_status"
printf 'worker_pid=%s\n' "$pid"
printf 'worker_token_match=%s\n' "$token_match"
printf 'worker_exit_code=%s\n' "$worker_exit_code"
printf 'gate_stage=%s\n' "$(read_optional "$gate_root/stage" absent)"
printf 'gate_failure_category=%s\n' "$(read_optional "$gate_root/failure-category" absent)"
printf 'gate_output_ready=%s\n' "$(test -f "$output/gate.json" && test ! -L "$output/gate.json" && echo yes || echo no)"
printf 'gate_signature_ready=%s\n' "$(test -f "$output/gate.sig" && test ! -L "$output/gate.sig" && echo yes || echo no)"
printf 'candidate_archive_ready=%s\n' "$(test -f "$output/candidate.tar.gz" && test ! -L "$output/candidate.tar.gz" && echo yes || echo no)"
raw_root=${{raw_log%/*}}
if [[ -d "$raw_root" && ! -L "$raw_root" && $(stat -c '%u:%g:%a' "$raw_root") == 0:0:700 && -f "$raw_log" && ! -L "$raw_log" && $(stat -c '%u:%g:%a:%h' "$raw_log") == 0:0:600:1 ]]; then
  printf 'raw_log_status=ok\n'
  printf 'raw_log_bytes=%s\n' "$(stat -c '%s' "$raw_log")"
else
  printf 'raw_log_status=invalid\n'
  printf 'raw_log_bytes=0\n'
fi
'''


def _wait_for_vm_worker(
    runner: SSHRunner,
    *,
    remote_root: str,
    remote_output: str,
    release_id: str,
    timeout_seconds: int = 7200,
    poll_seconds: float = 3.0,
    max_stale_polls: int = 3,
    max_absent_polls: int = 20,
) -> dict[str, str]:
    """Poll a detached validator and tolerate transient SSH disconnects."""

    deadline = time.monotonic() + timeout_seconds
    last_error: Exception | None = None
    stale_polls = 0
    absent_polls = 0
    while time.monotonic() < deadline:
        try:
            result = runner.run(
                "local_vm",
                _poll_vm_worker_script(remote_root=remote_root, remote_output=remote_output, release_id=release_id),
                VM_WORKER_FIELDS,
                timeout=120,
            ).values
        except (OSError, RuntimeError, TimeoutError) as error:
            last_error = error
            time.sleep(poll_seconds)
            continue
        last_error = None
        status = result["worker_status"]
        if status == "exited":
            if result["worker_token_match"] == "no":
                if not result["worker_exit_code"].isdigit():
                    raise VMWorkerStateUnknownError("detached VM validator returned an invalid exit code")
                return result
            stale_polls += 1
            if stale_polls >= max_stale_polls:
                raise VMWorkerStateUnknownError("detached VM validator exited without releasing its process identity")
        elif status == "absent":
            absent_polls += 1
            if absent_polls >= max_absent_polls:
                raise VMWorkerNotStartedError("detached VM validator did not complete its startup handshake")
        elif status == "launching":
            absent_polls += 1
            if absent_polls >= max_absent_polls:
                raise VMWorkerStateUnknownError("detached VM validator did not advance beyond its launcher handshake")
        elif status == "running":
            absent_polls = 0
            if result["worker_token_match"] == "yes":
                stale_polls = 0
            else:
                stale_polls += 1
                if stale_polls >= max_stale_polls:
                    raise VMWorkerStateUnknownError("detached VM validator lost its process identity")
        else:
            raise VMWorkerStateUnknownError("remote VM validator returned an invalid worker status")
        time.sleep(poll_seconds)
    if last_error is not None:
        raise VMWorkerStateUnknownError("timed out waiting for detached VM validator") from last_error
    raise VMWorkerStateUnknownError("timed out waiting for detached VM validator")


def _validate_completed_vm_worker(worker: dict[str, str]) -> None:
    if worker["worker_exit_code"] != "0":
        raise RuntimeError("remote VM validator failed; inspect VM raw log and release evidence")
    if worker["raw_log_status"] != "ok":
        raise RuntimeError("detached VM validator raw log contract failed")
    if (
        worker["gate_output_ready"] != "yes"
        or worker["gate_signature_ready"] != "yes"
        or worker["candidate_archive_ready"] != "yes"
    ):
        raise RuntimeError("detached VM validator exited without complete Gate output")


def space_cleaner_checksum(manifest: dict[str, object]) -> str:
    layout = manifest_release_asset_layout(manifest)
    if layout != LAYOUT_SKILL_V1:
        raise RuntimeError("VM validation only accepts new skill-v1 manifests")
    release_assets = manifest.get("release_asset_sha256")
    if not isinstance(release_assets, dict):
        raise RuntimeError("manifest release asset checksums are missing")
    cleaner_key = release_unit_relative_paths(layout)["space_cleaner"]
    checksum = release_assets.get(cleaner_key)
    if not isinstance(checksum, str):
        raise RuntimeError("manifest VM space cleaner checksum is missing")
    return checksum


def ensure_vm_space(runner: SSHRunner, cleaner: str, manifest: dict[str, object]) -> dict[str, str]:
    arguments = [shlex.quote(str(manifest["commit_sha"]))]
    compatibility_fields = ("compatibility_version", "compatibility_commit", "compatibility_image_id")
    if any(field in manifest for field in compatibility_fields):
        if not all(field in manifest for field in compatibility_fields):
            raise RuntimeError("manifest compatibility identity is incomplete")
        arguments.extend(shlex.quote(str(manifest[field])) for field in compatibility_fields)
    argument_text = " ".join(arguments)
    command = f"{shlex.quote(cleaner)} dry-run {argument_text}"
    report = runner.run("local_vm", command, SPACE_FIELDS).values
    if report["space_status"] == "sufficient":
        return report
    if report["space_status"] != "insufficient":
        raise RuntimeError("VM space cleaner returned an invalid status")
    runner.run(
        "local_vm",
        f"{shlex.quote(cleaner)} apply {argument_text}",
        SPACE_FIELDS,
        timeout=600,
    )
    verified = runner.run("local_vm", command, SPACE_FIELDS).values
    if verified["space_status"] != "sufficient":
        raise RuntimeError("VM disk space remains insufficient after one allowlisted cleanup")
    return verified


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--manifest", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    manifest_path = Path(args.manifest)
    output = Path(args.output)
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    validate_manifest_profile_contract(manifest, get_profile(str(manifest.get("profile", ""))))
    space_cleaner_checksum(manifest)
    runner = SSHRunner()
    install_vm_validator(runner)
    runner.run(
        "local_vm",
        "install -d -m 700 /opt/sub2api-deploy/release-input && test $(stat -c '%u:%a' /opt/sub2api-deploy/release-input) = $(id -u):700 && printf 'input_root_ready=true\\n'",
        {"input_root_ready"},
    )
    remote_root = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "validation")
    remote_manifest = f"{remote_root}/manifest.json"
    remote_cleaner = f"{remote_root}/vm-space-clean.sh"
    remote_output = f"/opt/sub2api-deploy/release-gates/{manifest['release_id']}/output"
    runner.upload("local_vm", manifest_path.read_bytes(), remote_manifest, 0o400)
    runner.upload_file("local_vm", SPACE_CLEANER, remote_cleaner, 0o700)
    cleanup_remote_root = True
    try:
        cleaner_checksum = space_cleaner_checksum(manifest)
        runner.run(
            "local_vm",
            f"test $(sha256sum {shlex.quote(remote_cleaner)} | awk '{{print $1}}') = {shlex.quote(cleaner_checksum)} && printf 'space_cleaner_verified=true\\n'",
            {"space_cleaner_verified"},
        )
        ensure_vm_space(runner, remote_cleaner, manifest)
        # From this point onward a transport error cannot prove whether the
        # remote launcher ran. Preserve the input until repeated polls prove
        # that no handshake exists, and never launch a second worker.
        cleanup_remote_root = False
        launch_error: BaseException | None = None
        try:
            start_result = runner.run(
                "local_vm",
                f"for asset in /usr/local/libexec/sub2api-vm-validate /usr/local/libexec/sub2api-sign-gate /usr/local/libexec/sub2api-sign-dr-evidence; do test -f $asset && test ! -L $asset && test $(stat -c '%U:%G:%a' $asset) = root:root:700; done && test $(sha256sum /usr/local/libexec/sub2api-vm-validate | awk '{{print $1}}') = {manifest['vm_validator_sha256']} && test $(sha256sum /usr/local/libexec/sub2api-sign-gate | awk '{{print $1}}') = {manifest['vm_gate_signer_sha256']} && test $(sha256sum /usr/local/libexec/sub2api-sign-dr-evidence | awk '{{print $1}}') = {manifest['vm_dr_signer_sha256']} && "
                + _remote_vm_worker_script(
                    remote_root=remote_root,
                    remote_manifest=remote_manifest,
                    remote_output=remote_output,
                    validator="/usr/local/libexec/sub2api-vm-validate",
                    release_id=str(manifest["release_id"]),
                ),
                {"worker_started", "launcher_pid", "raw_log_ready"},
                timeout=120,
            )
            if (
                start_result.values["worker_started"] != "true"
                or start_result.values["raw_log_ready"] != "true"
                or not start_result.values["launcher_pid"].isdigit()
            ):
                launch_error = RuntimeError("detached VM validator failed its startup contract")
        except (OSError, RuntimeError, TimeoutError) as error:
            launch_error = error
        try:
            worker = _wait_for_vm_worker(
                runner,
                remote_root=remote_root,
                remote_output=remote_output,
                release_id=str(manifest["release_id"]),
            )
        except VMWorkerNotStartedError as error:
            cleanup_remote_root = True
            if launch_error is not None:
                raise VMWorkerNotStartedError(str(error)) from launch_error
            raise
        cleanup_remote_root = True
        _validate_completed_vm_worker(worker)
        download_dir = f"{remote_root}/download"
        runner.run(
            "local_vm",
            f"install -d -m 700 {download_dir} && for name in gate.json gate.sig candidate.tar.gz SHA256SUMS; do ln {remote_output}/$name {download_dir}/$name; done && printf 'download_ready=true\\n'",
            {"download_ready"},
        )
        output.mkdir(parents=True, exist_ok=True, mode=0o700)
        for name in ("gate.json", "gate.sig", "candidate.tar.gz", "SHA256SUMS"):
            runner.download_file("local_vm", f"{download_dir}/{name}", output / name)
    finally:
        if cleanup_remote_root:
            runner.run("local_vm", f"rm -rf {remote_root} && printf 'input_removed=true\\n'", {"input_removed"})
    verify_gate(output, TRUSTED_KEY, manifest["profile"])


if __name__ == "__main__":
    main()
