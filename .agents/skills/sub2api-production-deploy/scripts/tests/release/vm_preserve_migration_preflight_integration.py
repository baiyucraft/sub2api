"""Execute the generated migration-preflight proof in a private Linux fixture.

The fixture owns only a unique temporary directory. Docker replies and a proc
directory are isolated fixture inputs. Actual VM locks are held; actual retained
release reconciliation is a separate operation. No owner or Gate is committed.
"""
from __future__ import annotations

import argparse
import base64
import contextlib
import io
import json
import re
import shlex
import sys
import tempfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(Path(__file__).parent))
from release import supervisor
from release.ssh import SSHRunner
from test_vm_preflight_preserve import incident
from test_vm_preserve_reconciliation import fixture


def capture_check() -> tuple[str, bytes]:
    """Use the production generator, with locally constructed incident evidence."""
    with tempfile.TemporaryDirectory(prefix="vm-preserve-migration-capture-") as local:
        patch = pytest.MonkeyPatch()
        try:
            case = incident(fixture.__wrapped__(Path(local), patch))
            manifest_path = case.run_dir / "manifest.json"
            manifest = json.loads(manifest_path.read_bytes())
            manifest.update(catalog_sha256="c" * 64, checksum_policy_sha256="d" * 64,
                            migration_catalog=[
                                {"filename": "299_upstream_cache_guard_revisions.sql", "checksum": "e" * 64},
                                {"filename": "300_upstream_health_history_index.sql", "checksum": "f" * 64},
                            ])
            manifest_path.write_bytes(json.dumps(manifest).encode("utf-8"))
            with contextlib.redirect_stdout(io.StringIO()):
                supervisor.reconcile_vm_preserve(case.args)
            return case.calls[0][1], manifest_path.read_bytes()
        finally:
            patch.undo()


def fixture_shell(remote: str) -> tuple[str, int]:
    cases: list[str] = []

    def blocked(name: str, change: str, restore: str) -> None:
        cases.append(f"phase={shlex.quote(name)}\n{change}\nblocked\n{restore}\npassed\n")

    def text_case(name: str, path: str, value: str, original: str) -> None:
        restore = (f'printf "%s\\n" {shlex.quote(original)} > "$gate/{path}"'
                   if original else f': > "$gate/{path}"')
        blocked(name, f'printf "%s\\n" {shlex.quote(value)} > "$gate/{path}"', restore)

    blocked("manifest_digest", 'printf "drift\\n" >> "$gate/manifest.json"',
            'install -m 400 "$root/manifest.original" "$gate/manifest.json"')
    for index, value in enumerate(("candidate_build", "restore_probe", "migration_apply", "migration_plan_after", "")):
        text_case(f"stage_{index}", "stage", value, "migration_plan_before")
    text_case("failure_category", "failure-category", "vm_v2_migration_apply", "vm_v2_migration_plan_before")
    for index, value in enumerate(("0", "611", "613", "0612", "not-a-line", "")):
        text_case(f"failure_line_{index}", "failure-line", value, "612")
    for index, value in enumerate(("status=0 stage=migration_plan_before", "status=2 stage=migration_plan_before",
                                   "status=1 stage=migration_apply", "status=01 stage=migration_plan_before",
                                   "status=1 stage=migration_plan_before extra", "")):
        text_case(f"failure_detail_{index}", "failure-detail", value, "status=1 stage=migration_plan_before")
    for name, original, values in (
        ("migration-hook-filename", "298_group_scheduling_protection_policies.sql", ("299_upstream_cache_guard_revisions.sql", "")),
        ("migration-hook-phase", "preflight", ("apply", "postflight", "")),
        ("migration-hook-status", "verified", ("failed", "running", "")),
        ("migration-hook.stdout", "migration_298_failure_code=constraint_contract", ("migration_298_failure_code=none", "extra", "")),
        ("migration-hook.stderr", "", ("unexpected stderr",)),
        ("plan-before.exit", "0", ("1", "00", "")),
        ("plan-actual.exit", "0", ("1", "00", "")),
    ):
        for index, value in enumerate(values):
            text_case(name.replace("-", "_").replace(".", "_") + f"_{index}", name, value, original)

    plan_changes = (
        ".existing_checksums_verified=false", ".conflicts=[\"conflict\"]", ".unknown=[\"unknown\"]",
        '.catalog_sha256="bad"', '.checksum_policy_sha256="bad"', ".pending=[]",
        ".pending|=reverse", '.pending[0].filename="298_group_scheduling_protection_policies.sql"',
        '.pending[1].checksum="bad"', '.pending += [{"filename":"301_unknown.sql","checksum":"bad"}]',
        "del(.pending)", ".existing_checksums_verified=1",
    )
    for index, expression in enumerate(plan_changes):
        blocked(f"plan_contract_{index}",
                f'jq {shlex.quote(expression)} "$root/plan.original" > "$gate/plan-before.json"\n'
                'install -m 400 "$gate/plan-before.json" "$gate/plan-actual.json"',
                'restore_plans')
    blocked("plan_actual_drift", "jq '.extra=true' \"$root/plan.original\" > \"$gate/plan-actual.json\"", 'restore_plans')
    for index, value in enumerate(("{", "[]", "null")):
        blocked(f"plan_invalid_json_{index}", f'printf "%s\\n" {shlex.quote(value)} > "$gate/plan-before.json"', 'restore_plans')
    for index, expression in enumerate((
        'del(.["299_upstream_cache_guard_revisions.sql"])',
        '.["299_upstream_cache_guard_revisions.sql"].preflight=false',
        '.["299_upstream_cache_guard_revisions.sql"].apply=true',
        '.["298_group_scheduling_protection_policies.sql"]={"preflight":true}',
        '.["285_upstream_null_rate_lifecycle.sql"].preflight="true"',
    )):
        blocked(f"hook_results_{index}", f'jq {shlex.quote(expression)} "$root/hooks.original" > "$gate/hook-results.json"',
                'install -m 600 "$root/hooks.original" "$gate/hook-results.json"')

    absent_paths = ("output/gate.json", "output/gate.sig", "output/candidate.tar.gz", "output/SHA256SUMS",
                    "candidate.tar.gz", "probe-data", "probe-redis-data", "production-recovery", "migration-plan-apply.json",
                    "migrate-candidate.log", "plan-after.json", "old-image-health.log")
    for index, path in enumerate(absent_paths):
        target = f'"$gate/{path}"'
        blocked(f"unexpected_artifact_{index}", f"install -m 600 /dev/null {target}", f"rm -- {target}")
        blocked(f"artifact_symlink_{index}", f'ln -s "$root/missing" {target}', f"rm -- {target}")

    files = [("$gate/manifest.json", "400"), ("$gate/stage", "600"),
             ("$gate/hook-results.json", "600"), ("$gate/validator.stderr", "600"),
             ("$gate/logs/vm-validate.raw.log", "600"), ("$raw/vm-validate.raw.log", "600")]
    files += [("$gate/" + name, "400") for name in (
        "failure-category", "failure-line", "failure-detail", "migration-hook-filename", "migration-hook-phase",
        "migration-hook-status", "migration-hook.stdout", "migration-hook.stderr", "plan-before.json",
        "plan-before.exit", "plan-actual.json", "plan-actual.exit")]
    files += [("$root/unit.lock", "600"), ("$root/gates/release.lock", "600")]
    for index, (path, mode) in enumerate(files):
        target = f'"{path}"'
        bad_mode = "666" if path == "$root/gates/release.lock" else "644"
        blocked(f"file_mode_{index}", f"chmod {bad_mode} {target}", f"chmod {mode} {target}")
        blocked(f"file_owner_{index}", f"chown 65534:65534 {target}", f"chown root:root {target}")
        blocked(f"file_links_{index}", f'ln {target} "$root/hardlink"', 'rm -- "$root/hardlink"')
        blocked(f"file_symlink_{index}", f'mv -- {target} "$root/saved-file"\nln -s "$root/saved-file" {target}',
                f'rm -- {target}\nmv -- "$root/saved-file" {target}')
        blocked(f"file_missing_{index}", f'mv -- {target} "$root/saved-file"',
                f'mv -- "$root/saved-file" {target}')
    for index, path in enumerate(("$gate", "$gate/output", "$gate/logs", "$raw")):
        target = f'"{path}"'
        blocked(f"directory_mode_{index}", f"chmod 755 {target}", f"chmod 700 {target}")
        blocked(f"directory_owner_{index}", f"chown 65534:65534 {target}", f"chown root:root {target}")
        blocked(f"directory_symlink_{index}", f'mv -- {target} "$root/saved-dir"\nln -s "$root/saved-dir" {target}',
                f'rm -- {target}\nmv -- "$root/saved-dir" {target}')
        blocked(f"directory_regular_file_{index}", f'mv -- {target} "$root/saved-dir"\ninstall -m 700 /dev/null {target}',
                f'rm -- {target}\nmv -- "$root/saved-dir" {target}')
    blocked("log_mirror_drift", 'printf "drift\\n" >> "$raw/vm-validate.raw.log"',
            'install -m 600 "$gate/logs/vm-validate.raw.log" "$raw/vm-validate.raw.log"')
    for name, lock, fd in (("unit", "$root/unit.lock", "7"), ("gate", "$root/gates/release.lock", "6")):
        blocked(name + "_lock_held", f'exec {fd}<>"{lock}"\nflock -n {fd}', f"flock -u {fd}\nexec {fd}>&-")
    for index, process in enumerate((
        "bash /fixture/vm-space-clean.sh", "bash sub2api-vm-validate", "bash run-validator.sh",
        "docker build .", "docker buildx build .", "buildctl build",
        "bash /fixture/release-input/validator.fixture/bootstrap",
        "bash /fixture/.sub2api-release-unit.fixture/sub2api-sign-gate",
        "bash /fixture/.sub2api-release-unit.fixture/sub2api-sign-dr-evidence",
        "bash /fixture/awk/run-validator.sh",
    )):
        blocked(f"live_process_{index}", f"write_proc {shlex.quote(process)}", 'rm -- "$root/proc-fixture/2147480000/cmdline"')
    for index, value in enumerate(("sub2api-v2-app-264failed", "sub2api-v2-old-264failed", "sub2api-v2-redis-264failed")):
        blocked(f"probe_container_{index}", f"export FIXTURE_CONTAINERS={shlex.quote(value)}", "unset FIXTURE_CONTAINERS")
    blocked("probe_network", "export FIXTURE_NETWORKS=sub2api-v2-net-264failed", "unset FIXTURE_NETWORKS")
    for index, value in enumerate(("1", "2", "", "not-a-count")):
        blocked(f"probe_database_{index}", f"export FIXTURE_DB_COUNT={shlex.quote(value)}", "unset FIXTURE_DB_COUNT")
    for command in ("inspect", "ps", "network", "exec"):
        blocked("docker_error_" + command, f"export FIXTURE_DOCKER_FAILURE={command}", "unset FIXTURE_DOCKER_FAILURE")
    blocked("dev_unhealthy", "export FIXTURE_DEV_HEALTH=unhealthy", "unset FIXTURE_DEV_HEALTH")
    blocked("boot_mismatch", 'cp "$root/check.sh" "$root/check.original"\nsed -i "s/$fixture_boot/ffffffff-ffff-ffff-ffff-ffffffffffff/g" "$root/check.sh"',
            'mv -- "$root/check.original" "$root/check.sh"')

    setup = r'''set -Eeuo pipefail
root=__FIXTURE_ROOT__
test -d "$root" && test ! -L "$root"
test "$(realpath -e -- "$root")" = "$root"
test "$(stat -c '%U:%G:%a' "$root")" = root:root:700
case "$root" in /opt/sub2api-deploy/release-input/vm-preserve-migration-test.*) ;; *) exit 80 ;; esac
mkdir -m 700 "$root/gates" "$root/logs" "$root/logs/264-failed" "$root/bin"
mkdir -m 700 "$root/proc-fixture" "$root/proc-fixture/2147480000"
write_proc() {
  local -a args
  read -r -a args <<< "$1"
  printf '%s\0' "${args[@]}" > "$root/proc-fixture/2147480000/cmdline"
}
gate="$root/gates/264-failed"
raw="$root/logs/264-failed"
mkdir -m 700 "$gate" "$gate/output" "$gate/logs"
install -m 600 /dev/null "$root/unit.lock"
install -m 600 /dev/null "$root/gates/release.lock"
install -m 400 "$root/manifest.original" "$gate/manifest.json"
printf 'migration_plan_before\n' > "$gate/stage"
chmod 600 "$gate/stage"
printf 'vm_v2_migration_plan_before\n' > "$gate/failure-category"
printf '612\n' > "$gate/failure-line"
printf 'status=1 stage=migration_plan_before\n' > "$gate/failure-detail"
printf '298_group_scheduling_protection_policies.sql\n' > "$gate/migration-hook-filename"
printf 'preflight\n' > "$gate/migration-hook-phase"
printf 'verified\n' > "$gate/migration-hook-status"
printf 'migration_298_failure_code=constraint_contract\n' > "$gate/migration-hook.stdout"
: > "$gate/migration-hook.stderr"
printf '0\n' > "$gate/plan-before.exit"
printf '0\n' > "$gate/plan-actual.exit"
chmod 400 "$gate/failure-category" "$gate/failure-line" "$gate/failure-detail" "$gate"/migration-hook.* "$gate"/migration-hook-* "$gate"/plan-*.exit
jq '{existing_checksums_verified:true,conflicts:[],unknown:[],catalog_sha256:.catalog_sha256,checksum_policy_sha256:.checksum_policy_sha256,pending:.migration_catalog}' "$root/manifest.original" > "$root/plan.original"
restore_plans() {
  install -m 400 "$root/plan.original" "$gate/plan-before.json"
  install -m 400 "$root/plan.original" "$gate/plan-actual.json"
}
restore_plans
printf '%s\n' '{"299_upstream_cache_guard_revisions.sql":{"preflight":true},"285_upstream_null_rate_lifecycle.sql":{"preflight":true},"297_upstream_cache_guard_account_scope.sql":{"preflight":true}}' > "$root/hooks.original"
install -m 600 "$root/hooks.original" "$gate/hook-results.json"
printf 'fixture raw log\n' > "$raw/vm-validate.raw.log"
chmod 600 "$raw/vm-validate.raw.log"
install -m 600 "$raw/vm-validate.raw.log" "$gate/logs/vm-validate.raw.log"
install -m 600 /dev/null "$gate/validator.stderr"
cat > "$root/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -eu
[[ ${FIXTURE_DOCKER_FAILURE:-} != "$1" ]] || exit 70
case "$*" in
  'inspect -f {{.State.Health.Status}} sub2api-dev') printf '%s\n' "${FIXTURE_DEV_HEALTH:-healthy}" ;;
  'ps -a --format {{.Names}}') printf '%s\n' "${FIXTURE_CONTAINERS:-}" ;;
  'network ls --format {{.Name}}') printf '%s\n' "${FIXTURE_NETWORKS:-}" ;;
  'exec sub2api-postgres sh -lc '* )
    [[ $5 == *'BEGIN READ ONLY; SELECT COUNT(*) FROM pg_database WHERE datname='*'sub2api_v2_264failed'*'; COMMIT;'* ]] || exit 72
    printf '%s\n' "${FIXTURE_DB_COUNT-0}" ;;
  *) exit 71 ;;
esac
DOCKER
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "${FIXTURE_PROCESS:-}"\n' > "$root/bin/ps"
chmod 700 "$root/bin/docker" "$root/bin/ps"
export PATH="$root/bin:$PATH"
fixture_boot=$(cat /proc/sys/kernel/random/boot_id)
checks=0
phase=initial
fixture_failure() {
  local failure_line=0 line
  if [[ -f "$root/result" ]]; then
    while IFS= read -r line; do
      if [[ $line =~ ^fixture_failure_line=([0-9]+)$ ]]; then
        failure_line=${BASH_REMATCH[1]}
        break
      fi
    done < "$root/result"
  fi
  printf 'vm_preserve_migration_integration=failed\nchecks=%s\nfailure_phase=%s\nfixture_failure_line=%s\n' "$checks" "$phase" "$failure_line"
  exit 0
}
trap fixture_failure ERR
run_check() { bash "$root/check.sh" > "$root/result" 2>/dev/null; }
blocked() { if run_check; then return 81; fi; checks=$((checks+1)); }
passed() { run_check; grep -Fxq 'vm_preserve_preflight=verified' "$root/result"; checks=$((checks+1)); }
passed
'''.replace("__FIXTURE_ROOT__", shlex.quote(remote))
    finish = r'''phase=semantic_json_equality
jq -cS . "$root/plan.original" > "$gate/plan-actual.json"
passed
restore_plans
phase=unrelated_resources
export FIXTURE_CONTAINERS=sub2api-v2-app-unrelated
export FIXTURE_NETWORKS=sub2api-v2-net-unrelated
passed
unset FIXTURE_CONTAINERS FIXTURE_NETWORKS
phase=gate_lock_existing_mode
chmod 644 "$root/gates/release.lock"
passed
chmod 600 "$root/gates/release.lock"
phase=unrelated_process
write_proc 'awk /fixture/regular-file'
passed
rm -- "$root/proc-fixture/2147480000/cmdline"
phase=final
passed
printf 'vm_preserve_migration_integration=pass\nchecks=%s\nfailure_phase=none\nfixture_failure_line=0\n' "$checks"
'''
    return setup + "".join(cases) + finish, 2 * len(cases) + 6


EXPECTED_CHECKS = fixture_shell("placeholder")[1]


def locked_fixture_script(check: str, manifest: bytes) -> str:
    """Hold actual VM execution locks; mutate only the unique fixture subtree."""
    token = "__SUB2API_MIGRATION_FIXTURE_ROOT__"
    check = check.replace("/opt/sub2api-deploy/release-gates", token + "/gates")
    check = check.replace("/opt/sub2api-deploy/release-logs", token + "/logs")
    check = check.replace("/usr/local/libexec/.sub2api-release-unit.lock", token + "/unit.lock")
    if check.count("Path('/proc')") != 2:
        raise RuntimeError("migration_fixture_process_generator_changed")
    check = check.replace("Path('/proc')", "Path('" + token + "/proc-fixture')")
    check = check.replace("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "__SUB2API_FIXTURE_BOOT__")
    check = check.replace("set -Eeuo pipefail", "set -Eeuo pipefail\ntrap 'printf \"fixture_failure_line=%s\\n\" \"$LINENO\"' ERR", 1)
    shell, count = fixture_shell(token)
    assert count == EXPECTED_CHECKS
    script = r'''set -Eeuo pipefail
unit_lock=/usr/local/libexec/.sub2api-release-unit.lock
test -f "$unit_lock" && test ! -L "$unit_lock"
test "$(stat -c '%U:%G:%a:%h' "$unit_lock")" = root:root:600:1
exec 8<>"$unit_lock"
test "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8)" = root:root:600:1
flock -n 8
gate_lock=/opt/sub2api-deploy/release-gates/release.lock
test -f "$gate_lock" && test ! -L "$gate_lock"
case "$(stat -c '%U:%G:%a:%h' "$gate_lock")" in root:root:600:1|root:root:644:1) ;; *) exit 80 ;; esac
exec 9<>"$gate_lock"
case "$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9)" in root:root:600:1|root:root:644:1) ;; *) exit 80 ;; esac
flock -n 9
base=/opt/sub2api-deploy/release-input
test -d "$base" && test ! -L "$base"
test "$(realpath -e -- "$base")" = "$base"
root=$(mktemp -d "$base/vm-preserve-migration-test.XXXXXXXX")
chmod 700 "$root"
cleanup_fixture() {
  test -d "$root" && test ! -L "$root"
  test "$(realpath -e -- "$root")" = "$root"
  test "$(stat -c '%U:%G:%a' "$root")" = root:root:700
  case "$root" in /opt/sub2api-deploy/release-input/vm-preserve-migration-test.*) ;; *) return 80 ;; esac
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
sed -i "s|__SUB2API_MIGRATION_FIXTURE_ROOT__|$root|g;s|__SUB2API_FIXTURE_BOOT__|$boot|g" "$root/check.sh"
sed -i "s|__SUB2API_MIGRATION_FIXTURE_ROOT__|$root|g" "$root/run-fixture.sh"
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
    parser.parse_args()
    check, manifest = capture_check()
    script = locked_fixture_script(check, manifest)

    def audit_fixture(runner: SSHRunner) -> dict[str, str]:
        fields = {"vm_preserve_migration_integration", "checks", "failure_phase", "fixture_failure_line", "cleanup"}
        values = runner.run("local_vm", script, fields, timeout=120).values
        expected = {"vm_preserve_migration_integration": "pass", "checks": str(EXPECTED_CHECKS),
                    "failure_phase": "none", "fixture_failure_line": "0", "cleanup": "pass"}
        if values != expected:
            shell, _ = fixture_shell("placeholder")
            known = set(re.findall(r"^phase=([a-z0-9_]+)$", shell, re.MULTILINE))
            phase, line, count = (values.get(key, "") for key in ("failure_phase", "fixture_failure_line", "checks"))
            if (phase not in known or not re.fullmatch(r"[a-z0-9_]{1,64}", phase)
                    or not re.fullmatch(r"[0-9]{1,6}", line) or not re.fullmatch(r"[0-9]{1,3}", count)
                    or int(count) > EXPECTED_CHECKS):
                raise RuntimeError("migration_fixture_failure_diagnostic_unproven")
            print(f"migration_fixture_failure_phase={phase}\nmigration_fixture_failure_line={line}\nmigration_fixture_checks={count}", flush=True)
            raise RuntimeError("migration_fixture_failed")
        return values

    print(json.dumps(audit_fixture(SSHRunner()), sort_keys=True))


if __name__ == "__main__":
    main()
