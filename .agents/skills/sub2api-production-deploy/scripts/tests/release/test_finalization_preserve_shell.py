"""Execute the actual read-only preserve preflight using inert local stubs."""
from __future__ import annotations

import importlib
import os
import shutil
import sys
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
vm = importlib.import_module("release.vm_lifecycle")
process = importlib.import_module("release.process")
BOOT_ID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"


def bash_path(path):
    value = path.as_posix()
    return f"/{value[0].lower()}{value[2:]}" if os.name == "nt" else value


@pytest.fixture
def shell_probe(tmp_path):
    candidate = Path("C:/Program Files/Git/bin/bash.exe")
    bash = str(candidate) if candidate.is_file() else shutil.which("bash")
    if not bash:
        pytest.skip("Bash is required for the inert preserve preflight fixture")
    gate = tmp_path / "gates"
    gate.mkdir()
    unit_lock = tmp_path / "unit.lock"
    gate_lock = gate / "release.lock"
    unit_lock.touch()
    gate_lock.touch()
    probe = mock.Mock()
    probe._running.return_value = True
    probe.ssh.run.return_value = SimpleNamespace(values={"vm_preserve_verified": "true"})
    vm._preserve_running_probe(probe, BOOT_ID)
    script = probe.ssh.run.call_args.args[1]
    script = script.replace("/usr/local/libexec/.sub2api-release-unit.lock", bash_path(unit_lock))
    script = script.replace("/opt/sub2api-deploy/release-gates", bash_path(gate))
    stubs = r'''
id() { test "$*" = '-u' && printf '%s\n' "$FIXTURE_UID"; }
cat() {
  test "$*" = /proc/sys/kernel/random/boot_id || return 90
  test "$QUERY_FAIL" != boot || return 17
  if [[ $BOOT_SECOND == false || ! -f $BOOT_COUNTER ]]; then
    printf '%s\n' "$FIXTURE_BOOT"
  else
    printf '11111111-2222-3333-4444-555555555555\n'
  fi
  printf x >> "$BOOT_COUNTER"
}
stat() {
  test "$QUERY_FAIL" != stat || return 17
  case "$2" in
    '%U:%G:%a') printf '%s\n' "$GATE_MODE" ;;
    '%U:%G:%a:%h')
      case "$3" in "$UNIT_LOCK") printf '%s\n' "$UNIT_MODE" ;; /proc/self/fd/8) printf '%s\n' "$FD_UNIT_MODE" ;;
        "$GATE_LOCK") printf '%s\n' "$LOCK_MODE" ;; /proc/self/fd/9) printf '%s\n' "$FD_LOCK_MODE" ;; *) return 90 ;; esac ;;
    *) return 90 ;;
  esac
}
flock() { test "$*" = '-xn 8' || test "$*" = '-xn 9' || return 90; test "$2" != "$LOCK_BUSY"; }
ps() { test "$*" = '-eo args=' || return 90; test "$QUERY_FAIL" != ps || return 17; printf '%s\n' "$PROCESSES"; }
awk() { test "$QUERY_FAIL" != awk || return 17; command awk "$@"; }
docker() {
  case "$1 $2" in
    'ps -a') test "$QUERY_FAIL" != containers || return 17; printf '%s\n' "$CONTAINERS" ;;
    'network ls') test "$QUERY_FAIL" != networks || return 17; printf '%s\n' "$NETWORKS" ;;
    'inspect -f')
      test "$4" = sub2api-dev || return 90
      test "$QUERY_FAIL" != inspect || return 17
      case "$3" in '{{.State.Running}}') printf '%s\n' "$DEV_RUNNING" ;;
        '{{.State.Health.Status}}') printf '%s\n' "$DEV_HEALTH" ;; *) return 90 ;; esac ;;
    *) printf 'MUTATION_ATTEMPT\n' >&2; return 90 ;;
  esac
}
curl() {
  test "$*" = '-sS --max-time 10 -o /dev/null -w %{http_code} http://127.0.0.1:8211/health' || return 90
  test "$QUERY_FAIL" != curl || return 17
  printf '%s' "$HTTP_STATUS"
}
shutdown() { printf 'MUTATION_ATTEMPT\n' >&2; return 90; }
rm() { printf 'MUTATION_ATTEMPT\n' >&2; return 90; }
systemctl() { printf 'MUTATION_ATTEMPT\n' >&2; return 90; }
'''

    def execute(**changes):
        counter = tmp_path / "boot-counter"
        counter.unlink(missing_ok=True)
        settings = {
            "FIXTURE_UID": "0", "FIXTURE_BOOT": BOOT_ID, "BOOT_SECOND": "false",
            "BOOT_COUNTER": bash_path(counter), "UNIT_LOCK": bash_path(unit_lock), "GATE_LOCK": bash_path(gate_lock),
            "UNIT_MODE": "root:root:600:1", "GATE_MODE": "root:root:700", "LOCK_MODE": "root:root:600:1",
            "FD_UNIT_MODE": "root:root:600:1", "FD_LOCK_MODE": "root:root:600:1",
            "LOCK_BUSY": "none", "QUERY_FAIL": "none", "PROCESSES": "idle-process",
            "CONTAINERS": "sub2api-dev\npostgres-dev\nredis-dev", "NETWORKS": "bridge\nsub2api-dev",
            "DEV_RUNNING": "true", "DEV_HEALTH": "healthy", "HTTP_STATUS": "200",
        }
        environment = {**os.environ, **settings, **changes}
        environment.pop("BASH_ENV", None)
        environment.pop("ENV", None)
        result = process.run_hidden([bash, "--noprofile", "--norc", "-s"], input=stubs + script,
                                    text=True, capture_output=True, env=environment, timeout=10)
        assert "MUTATION_ATTEMPT" not in result.stderr
        return result

    return SimpleNamespace(execute=execute, script=script, unit_lock=unit_lock, gate=gate, gate_lock=gate_lock)


def test_preserve_real_preflight_requires_idle_healthy_same_boot_without_mutations(shell_probe):
    result = shell_probe.execute()
    assert result.returncode == 0, result.stderr
    assert result.stdout == "vm_preserve_verified=true\n"
    assert shell_probe.execute(LOCK_MODE="root:root:644:1").returncode == 0
    assert shell_probe.script.count('cat /proc/sys/kernel/random/boot_id') == 2
    for prohibited in ("docker stop", "docker start", "docker rm", "shutdown -", "systemctl ", "rm -"):
        assert prohibited not in shell_probe.script
    assert shell_probe.script.index("flock -xn 8") < shell_probe.script.index("flock -xn 9") < shell_probe.script.index("processes=$")


@pytest.mark.parametrize("field,value", [
    ("FIXTURE_UID", "1000"), ("FIXTURE_BOOT", "11111111-2222-3333-4444-555555555555"), ("BOOT_SECOND", "true"),
    ("UNIT_MODE", "root:root:644:1"), ("UNIT_MODE", "root:root:600:2"),
    ("FD_UNIT_MODE", "root:root:644:1"), ("FD_LOCK_MODE", "root:root:666:1"),
    ("GATE_MODE", "root:root:755"), ("LOCK_MODE", "root:root:666:1"), ("LOCK_MODE", "user:root:600:1"),
    ("LOCK_BUSY", "8"), ("LOCK_BUSY", "9"), ("DEV_RUNNING", "false"), ("DEV_HEALTH", "unhealthy"), ("HTTP_STATUS", "503"),
    ("PROCESSES", "bash vm-space-clean.sh"), ("PROCESSES", "sub2api-vm-validate"), ("PROCESSES", "run-validator.sh"),
    ("PROCESSES", "docker build ."), ("PROCESSES", "docker buildx build ."), ("PROCESSES", "buildctl build"),
    ("CONTAINERS", "sub2api-probe-test"), ("CONTAINERS", "sub2api-v2-test"), ("CONTAINERS", "sub2api-preview-test"),
    ("NETWORKS", "sub2api-probe-test"), ("NETWORKS", "sub2api-v2-test"), ("NETWORKS", "sub2api-preview-test"),
])
def test_preserve_preflight_rejects_drift_busy_tasks_and_unhealthy_dev(shell_probe, field, value):
    result = shell_probe.execute(**{field: value})
    assert result.returncode != 0
    assert "vm_preserve_verified" not in result.stdout


@pytest.mark.parametrize("query", ["boot", "stat", "ps", "awk", "containers", "networks", "inspect", "curl"])
def test_preserve_preflight_rejects_failed_queries_even_with_empty_output(shell_probe, query):
    result = shell_probe.execute(QUERY_FAIL=query)
    assert result.returncode != 0
    assert "vm_preserve_verified" not in result.stdout


@pytest.mark.parametrize("target", ["unit_lock", "gate", "gate_lock"])
@pytest.mark.parametrize("kind", ["missing", "wrong_type"])
def test_preserve_preflight_checks_path_before_opening_descriptor(shell_probe, target, kind):
    path = getattr(shell_probe, target)
    if target == "gate":
        shell_probe.gate_lock.unlink()
        path.rmdir()
        if kind == "wrong_type":
            path.touch()
    else:
        path.unlink()
        if kind == "wrong_type":
            path.mkdir()
    result = shell_probe.execute()
    assert result.returncode != 0
    assert "vm_preserve_verified" not in result.stdout
