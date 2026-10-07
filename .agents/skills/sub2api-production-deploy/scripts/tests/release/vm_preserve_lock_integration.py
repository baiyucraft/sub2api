"""Exercise the real generated VM-preserve checks in an isolated Linux fixture."""
from __future__ import annotations

import contextlib
import io
import shlex
import sys
import tempfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(Path(__file__).parent))
from release.ssh import SSHRunner
from release import supervisor
from test_vm_preserve_reconciliation import fixture


def main():
    with tempfile.TemporaryDirectory(prefix="vm-preserve-capture-") as local:
        patch = pytest.MonkeyPatch()
        try:
            case = fixture.__wrapped__(Path(local), patch)
            with contextlib.redirect_stdout(io.StringIO()):
                supervisor.reconcile_vm_preserve(case.args)
            check = case.calls[0][1]
        finally:
            patch.undo()
    runner = SSHRunner()
    remote = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "vm-preserve-test")
    try:
        boot = runner.run("local_vm", "printf 'boot=%s\\n' \"$(cat /proc/sys/kernel/random/boot_id)\"", {"boot"}).values["boot"]
        check = check.replace("/opt/sub2api-deploy/release-gates", remote + "/gates")
        check = check.replace("/opt/sub2api-deploy/release-logs", remote + "/logs")
        check = check.replace("/usr/local/libexec/.sub2api-release-unit.lock", remote + "/unit.lock")
        check = check.replace("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", boot)
        runner.upload("local_vm", check.encode(), remote + "/check.sh", 0o700)
        shell = "set -Eeuo pipefail\nroot=" + shlex.quote(remote) + "\n" + r'''
mkdir -m 700 "$root/gates" "$root/logs" "$root/logs/264-failed" "$root/bin"
install -m 600 /dev/null "$root/unit.lock"
install -m 600 /dev/null "$root/gates/release.lock"
printf '#!/usr/bin/env bash\nprintf "healthy\\n"\n' > "$root/bin/docker"
chmod 700 "$root/bin/docker"
export PATH="$root/bin:$PATH"
checks=0
run_check() { bash "$root/check.sh" > "$root/result" 2>/dev/null; }
blocked() { if run_check; then exit 81; fi; checks=$((checks+1)); }
run_check
grep -Fxq 'vm_preserve_preflight=verified' "$root/result"
checks=$((checks+1))
# A VM-only task or validator-unit installation uses this lock, even after
# the build command has exited and the process-name filters no longer match.
exec 7<>"$root/unit.lock"
flock -n 7
blocked
flock -u 7
exec 7>&-
exec 6<>"$root/gates/release.lock"
flock -n 6
blocked
flock -u 6
exec 6>&-
# Started-but-exited validators retain their launch log even without Gate.
install -m 600 /dev/null "$root/logs/264-failed/vm-validate.raw.log"
blocked
rm "$root/logs/264-failed/vm-validate.raw.log"
ln -s "$root/absent" "$root/logs/264-failed/vm-validate.raw.log"
blocked
rm "$root/logs/264-failed/vm-validate.raw.log"
chmod 644 "$root/unit.lock"
blocked
chmod 600 "$root/unit.lock"
mv "$root/unit.lock" "$root/real-unit.lock"
ln -s "$root/real-unit.lock" "$root/unit.lock"
blocked
rm "$root/unit.lock"
mv "$root/real-unit.lock" "$root/unit.lock"
mkdir -m 700 "$root/gates/264-failed"
blocked
rmdir "$root/gates/264-failed"
run_check
checks=$((checks+1))
printf 'vm_preserve_lock_integration=pass\nchecks=%s\n' "$checks"
'''
        result = runner.run("local_vm", shell, {"vm_preserve_lock_integration", "checks"}, timeout=60).values
        assert result == {"vm_preserve_lock_integration": "pass", "checks": "9"}
        print("vm_preserve_lock_integration=pass checks=9", flush=True)
    finally:
        runner.run("local_vm", "rm -rf -- " + shlex.quote(remote) + " && printf 'cleanup=pass\\n'", {"cleanup"})


if __name__ == "__main__":
    main()
