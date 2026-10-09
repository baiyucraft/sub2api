"""Run candidate-build preserve checks against a unique, isolated Linux fixture.

Only the generated read-only VM proof runs remotely. Real release directories,
owner records, validators, containers, and production are never modified.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import io
import shlex
import sys
import tempfile
from pathlib import Path
from types import SimpleNamespace

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(Path(__file__).parent))
from release import supervisor
from release.ssh import SSHRunner
from test_vm_preserve_reconciliation import fixture


def capture_check() -> tuple[str, bytes]:
    with tempfile.TemporaryDirectory(prefix="vm-preserve-candidate-capture-") as local:
        patch = pytest.MonkeyPatch()
        try:
            case = fixture.__wrapped__(Path(local), patch)
            case.args.failed_stage = "candidate-build"
            manifest = (case.run_dir / "manifest.json").read_bytes()
            with contextlib.redirect_stdout(io.StringIO()):
                supervisor.reconcile_vm_preserve(case.args)
            return case.calls[0][1], manifest
        finally:
            patch.undo()


def fixture_shell(remote: str) -> tuple[str, int]:
    cases: list[str] = []

    def blocked_case(name: str, change: str, restore: str) -> None:
        cases.append(f"phase={shlex.quote(name)}\n{change}\nblocked\n{restore}\n")

    blocked_case("manifest_digest", 'printf "drift\\n" >> "$gate/manifest.json"',
                 'install -m 400 "$root/manifest.original" "$gate/manifest.json"')
    for stage in ("restore_probe", "migration_apply", "post_build_space"):
        blocked_case("stage_" + stage, f'printf "%s\\n" {stage} > "$gate/stage"',
                     'printf "candidate_build\\n" > "$gate/stage"')
    blocked_case("failure_category", 'printf "vm_v2_restore_probe\\n" > "$gate/failure-category"',
                 'printf "vm_v2_candidate_build\\n" > "$gate/failure-category"')
    for index, value in enumerate(("0", "-1", "not-a-line", "")):
        blocked_case(f"failure_line_{index}", f'printf "%s\\n" {shlex.quote(value)} > "$gate/failure-line"',
                     'printf "123\\n" > "$gate/failure-line"')
    for index, value in enumerate(("status=0 stage=candidate_build", "status=256 stage=candidate_build",
                                   "status=1 stage=restore_probe", "status=01 stage=candidate_build",
                                   "status=1 stage=candidate_build extra")):
        blocked_case(f"failure_detail_{index}", f'printf "%s\\n" {shlex.quote(value)} > "$gate/failure-detail"',
                     'printf "status=1 stage=candidate_build\\n" > "$gate/failure-detail"')

    absent_paths = ("output/gate.json", "output/gate.sig", "output/candidate.tar.gz", "output/SHA256SUMS",
                    "candidate.tar.gz", "probe-data", "probe-redis-data", "production-recovery", "plan-before.json")
    for index, path in enumerate(absent_paths):
        target = f'"$gate/{path}"'
        blocked_case(f"unexpected_artifact_{index}", f"install -m 600 /dev/null {target}", f"rm -- {target}")
        blocked_case(f"artifact_symlink_{index}", f'ln -s "$root/absent" {target}', f"rm -- {target}")

    files = (("$gate/manifest.json", "400"), ("$gate/stage", "600"),
             ("$gate/failure-category", "400"), ("$gate/failure-line", "400"),
             ("$gate/failure-detail", "400"), ("$gate/validator.stderr", "600"),
             ("$gate/logs/vm-validate.raw.log", "600"), ("$raw/vm-validate.raw.log", "600"))
    for index, (path, mode) in enumerate(files):
        target = f'"{path}"'
        blocked_case(f"file_mode_{index}", f"chmod 644 {target}", f"chmod {mode} {target}")
        blocked_case(f"file_owner_{index}", f"chown 65534:65534 {target}", f"chown root:root {target}")
        blocked_case(f"file_links_{index}", f'ln {target} "$root/hardlink"', 'rm -- "$root/hardlink"')
        blocked_case(f"file_symlink_{index}", f'mv -- {target} "$root/saved-file"\nln -s "$root/saved-file" {target}',
                     f'rm -- {target}\nmv -- "$root/saved-file" {target}')
    for index, path in enumerate(("$gate", "$gate/output", "$gate/logs", "$raw")):
        target = f'"{path}"'
        blocked_case(f"directory_mode_{index}", f"chmod 755 {target}", f"chmod 700 {target}")
        blocked_case(f"directory_owner_{index}", f"chown 65534:65534 {target}", f"chown root:root {target}")
        blocked_case(f"directory_symlink_{index}", f'mv -- {target} "$root/saved-dir"\nln -s "$root/saved-dir" {target}',
                     f'rm -- {target}\nmv -- "$root/saved-dir" {target}')
    blocked_case("log_mirror_drift", 'printf "drift\\n" >> "$raw/vm-validate.raw.log"',
                 'install -m 600 "$gate/logs/vm-validate.raw.log" "$raw/vm-validate.raw.log"')
    blocked_case("unit_lock_held", 'exec 7<>"$root/unit.lock"\nflock -n 7', 'flock -u 7\nexec 7>&-')
    blocked_case("gate_lock_held", 'exec 6<>"$root/gates/release.lock"\nflock -n 6', 'flock -u 6\nexec 6>&-')
    blocked_case("unit_lock_mode", 'chmod 644 "$root/unit.lock"', 'chmod 600 "$root/unit.lock"')
    blocked_case("unit_lock_owner", 'chown 65534:65534 "$root/unit.lock"', 'chown root:root "$root/unit.lock"')
    blocked_case("unit_lock_links", 'ln "$root/unit.lock" "$root/hardlink"', 'rm -- "$root/hardlink"')
    blocked_case("unit_lock_symlink", 'mv "$root/unit.lock" "$root/saved-lock"\nln -s "$root/saved-lock" "$root/unit.lock"',
                 'rm -- "$root/unit.lock"\nmv "$root/saved-lock" "$root/unit.lock"')
    blocked_case("gate_lock_symlink", 'mv "$root/gates/release.lock" "$root/saved-lock"\nln -s "$root/saved-lock" "$root/gates/release.lock"',
                 'rm -- "$root/gates/release.lock"\nmv "$root/saved-lock" "$root/gates/release.lock"')
    for index, process in enumerate(("bash /fixture/vm-space-clean.sh", "bash sub2api-vm-validate",
                                     "bash run-validator.sh", "docker build .", "docker buildx build .",
                                     "buildctl build", "bash /fixture/release-input/validator.fixture/bootstrap",
                                     "bash /fixture/.sub2api-release-unit.fixture/sub2api-sign-gate")):
        blocked_case(f"live_process_{index}", f"export FIXTURE_PROCESS={shlex.quote(process)}", "unset FIXTURE_PROCESS")
    blocked_case("dev_unhealthy", "export FIXTURE_DEV_HEALTH=unhealthy", "unset FIXTURE_DEV_HEALTH")
    blocked_case("boot_mismatch", 'cp "$root/check.sh" "$root/check.original"\nsed -i "s/$fixture_boot/ffffffff-ffff-ffff-ffff-ffffffffffff/g" "$root/check.sh"',
                 'mv -- "$root/check.original" "$root/check.sh"')

    setup = r'''set -Eeuo pipefail
root=__FIXTURE_ROOT__
test -d "$root" && test ! -L "$root"
test "$(realpath -e -- "$root")" = "$root"
test "$(stat -c '%U:%G:%a' "$root")" = root:root:700
case "$root" in /opt/sub2api-deploy/release-input/vm-preserve-candidate-test.*) ;; *) exit 80 ;; esac
mkdir -m 700 "$root/gates" "$root/logs" "$root/logs/264-failed" "$root/bin"
gate="$root/gates/264-failed"
raw="$root/logs/264-failed"
mkdir -m 700 "$gate" "$gate/output" "$gate/logs"
install -m 600 /dev/null "$root/unit.lock"
install -m 600 /dev/null "$root/gates/release.lock"
install -m 400 "$root/manifest.original" "$gate/manifest.json"
printf "candidate_build\n" > "$gate/stage"
chmod 600 "$gate/stage"
printf "vm_v2_candidate_build\n" > "$gate/failure-category"
printf "123\n" > "$gate/failure-line"
printf "status=1 stage=candidate_build\n" > "$gate/failure-detail"
chmod 400 "$gate/failure-category" "$gate/failure-line" "$gate/failure-detail"
printf "fixture raw log\n" > "$raw/vm-validate.raw.log"
chmod 600 "$raw/vm-validate.raw.log"
install -m 600 "$raw/vm-validate.raw.log" "$gate/logs/vm-validate.raw.log"
install -m 600 /dev/null "$gate/validator.stderr"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${FIXTURE_DEV_HEALTH:-healthy}"\n' > "$root/bin/docker"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${FIXTURE_PROCESS:-}"\n' > "$root/bin/ps"
chmod 700 "$root/bin/docker" "$root/bin/ps"
export PATH="$root/bin:$PATH"
fixture_boot=$(cat /proc/sys/kernel/random/boot_id)
checks=0
phase=initial
trap 'printf "vm_preserve_candidate_integration=failed\nchecks=%s\nfailure_phase=%s\n" "$checks" "$phase"; sed -n "/^fixture_failure_line=[0-9]*$/p" "$root/result"; exit 0' ERR
run_check() { bash "$root/check.sh" > "$root/result" 2>/dev/null; }
blocked() { if run_check; then return 81; fi; checks=$((checks+1)); }
passed() { run_check; grep -Fxq 'vm_preserve_preflight=verified' "$root/result"; checks=$((checks+1)); }
passed
'''.replace("__FIXTURE_ROOT__", shlex.quote(remote))
    finish = r'''phase=exit_status_255
printf "status=255 stage=candidate_build\n" > "$gate/failure-detail"
passed
printf "status=1 stage=candidate_build\n" > "$gate/failure-detail"
phase=final
passed
printf 'vm_preserve_candidate_integration=pass\nchecks=%s\nfailure_phase=none\nfixture_failure_line=0\n' "$checks"
'''
    return setup + "".join(cases) + finish, len(cases) + 3


def locked_fixture_script(check: str, manifest: bytes) -> str:
    """Create, execute, and remove the fixture within one real VM lock scope."""
    token = "__SUB2API_CANDIDATE_FIXTURE_ROOT__"
    check = check.replace("/opt/sub2api-deploy/release-gates", token + "/gates")
    check = check.replace("/opt/sub2api-deploy/release-logs", token + "/logs")
    check = check.replace("/usr/local/libexec/.sub2api-release-unit.lock", token + "/unit.lock")
    check = check.replace("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "__SUB2API_FIXTURE_BOOT__")
    check = check.replace("set -Eeuo pipefail", "set -Eeuo pipefail\ntrap 'printf \"fixture_failure_line=%s\\n\" \"$LINENO\"' ERR", 1)
    shell, expected_checks = fixture_shell(token)
    if expected_checks != 97:
        raise RuntimeError("candidate_fixture_contract_changed")
    script = r'''set -Eeuo pipefail
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$unit_lock" && test ! -L "$unit_lock"
test "$(stat -c '%U:%G:%a:%h' "$unit_lock")" = root:root:600:1
exec 8<>"$unit_lock"
test "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)" = root:root:600:1
flock -n 8
test -f /opt/sub2api-deploy/release-gates/release.lock
test ! -L /opt/sub2api-deploy/release-gates/release.lock
exec 9<>/opt/sub2api-deploy/release-gates/release.lock
flock -n 9
base=/opt/sub2api-deploy/release-input
test -d "$base" && test ! -L "$base"
test "$(realpath -e -- "$base")" = "$base"
root=$(mktemp -d "$base/vm-preserve-candidate-test.XXXXXXXX")
chmod 700 "$root"
cleanup_fixture() {
  test -d "$root" && test ! -L "$root"
  test "$(realpath -e -- "$root")" = "$root"
  test "$(stat -c '%U:%G:%a' "$root")" = root:root:700
  case "$root" in /opt/sub2api-deploy/release-input/vm-preserve-candidate-test.*) ;; *) return 80 ;; esac
  rm -rf -- "$root"
  test ! -e "$root" && test ! -L "$root"
  printf 'cleanup=pass\n'
}
trap cleanup_fixture EXIT
printf '%s' __CHECK_BASE64__ | base64 -d > "$root/check.sh"
printf '%s' __MANIFEST_BASE64__ | base64 -d > "$root/manifest.original"
printf '%s' __SHELL_BASE64__ | base64 -d > "$root/run-fixture.sh"
boot=$(cat /proc/sys/kernel/random/boot_id)
[[ $boot =~ ^[0-9a-f-]{36}$ ]]
sed -i "s|__SUB2API_CANDIDATE_FIXTURE_ROOT__|$root|g;s|__SUB2API_FIXTURE_BOOT__|$boot|g" "$root/check.sh"
sed -i "s|__SUB2API_CANDIDATE_FIXTURE_ROOT__|$root|g" "$root/run-fixture.sh"
chmod 700 "$root/check.sh" "$root/run-fixture.sh"
chmod 400 "$root/manifest.original"
bash "$root/run-fixture.sh"
cleanup_fixture
trap - EXIT
'''
    for placeholder, data in (("__CHECK_BASE64__", check.encode()), ("__MANIFEST_BASE64__", manifest), ("__SHELL_BASE64__", shell.encode())):
        script = script.replace(placeholder, shlex.quote(base64.b64encode(data).decode("ascii")))
    return script


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("release_id", help="Failed candidate-build release whose retained lease authorizes this audit")
    args = parser.parse_args()
    check, manifest = capture_check()
    script = locked_fixture_script(check, manifest)
    def audit_fixture(runner: SSHRunner, _identifier: str) -> dict[str, str]:
        return runner.run("local_vm", script,
                          {"vm_preserve_candidate_integration", "checks", "failure_phase", "fixture_failure_line", "cleanup"}, timeout=60).values
    # This is the sole execution entry: it holds the actual failed release and
    # VM identity locks, proves the real retained owner, and repeats complete
    # preflight after the fixture. Audit cannot commit or release that owner.
    supervisor.reconcile_vm_preserve(SimpleNamespace(release_id=args.release_id, failed_stage="candidate-build"),
                                     audit_fixture=audit_fixture, audit_only=True)


if __name__ == "__main__":
    main()
