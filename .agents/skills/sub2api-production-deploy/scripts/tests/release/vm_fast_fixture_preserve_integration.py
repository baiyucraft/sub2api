"""Run the identical read-only proof against owned Linux fixture resources.

--emit-script emits a standalone Python script for a controlled Linux host.
It creates only a private temporary directory; Docker and proc are fixtures.
It never calls reconciliation, alters an owner or accesses live release paths.
"""
from __future__ import annotations

import hashlib
import inspect
import json
import os
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.vm_fast_fixture_preserve import _vm_probe, _acquire_locks, ABSENT


def fixture_checks(probe, absent, acquire_locks):
    import fcntl
    import hashlib
    import os
    import subprocess
    import tempfile
    from pathlib import Path

    count = 0
    with tempfile.TemporaryDirectory(prefix="sub2api-fast-fixture-preserve-proof-") as temporary:
        base = Path(temporary)
        root, raw, proc = base / "gate", base / "raw", base / "proc"
        for path in (root, root / "output", root / "logs", raw, proc):
            path.mkdir(mode=0o700)
            path.chmod(0o700)
        expected = {}
        names = ("manifest.json", "stage", "failure-category", "failure-line", "failure-detail",
                 "plan-before.exit", "plan-actual.exit", "plan-before.json", "plan-actual.json",
                 "fixture-bootstrap-before.json", "fixture-bootstrap.log", "production-snapshot.json",
                 "candidate-identity", "candidate-cli-contract", "logs/vm-validate.raw.log", "validator.stderr")
        for name in names:
            path = root / name
            path.write_bytes(name.encode())
            path.chmod(0o600 if name in ("stage", "logs/vm-validate.raw.log", "validator.stderr", "fixture-bootstrap.log")
                       else 0o444 if name in ("fixture-bootstrap-before.json", "production-snapshot.json") else 0o400)
            expected[name] = hashlib.sha256(path.read_bytes()).hexdigest()
        (raw / "vm-validate.raw.log").write_bytes((root / "logs/vm-validate.raw.log").read_bytes())
        (raw / "vm-validate.raw.log").chmod(0o600)
        boot = base / "boot"
        boot.write_text("same-boot\n")
        replies = {"containers": "sub2api-dev\nsub2api-postgres\nsub2api-redis\n", "networks": "bridge\n",
                   "database": "0\n", "health": "healthy\n"}
        failure = None

        locks = (base / "unit.lock", base / "gate.lock")
        for lock in locks:
            lock.touch(mode=0o600)
            lock.chmod(0o600)
        specification = tuple((str(lock), (0o600, 0o644)) for lock in locks)

        def close_locks():
            for descriptor in acquire_locks(specification):
                os.close(descriptor)

        close_locks()
        count += 1
        for lock in locks:
            with lock.open("rb") as held:
                fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
                try:
                    acquire_locks(specification)
                except BlockingIOError:
                    count += 1
                else:
                    raise AssertionError("proof accepted a held lock")
            for mutation in ("mode", "symlink", "hardlink", "missing"):
                if mutation == "mode":
                    lock.chmod(0o666)
                elif mutation == "hardlink":
                    os.link(lock, base / "lock-alias")
                else:
                    lock.rename(base / "lock-saved")
                    if mutation == "symlink":
                        lock.symlink_to(base / "lock-saved")
                try:
                    acquire_locks(specification)
                except (OSError, RuntimeError):
                    count += 1
                else:
                    raise AssertionError("proof accepted an unsafe lock")
                if mutation == "mode":
                    lock.chmod(0o600)
                elif mutation == "hardlink":
                    (base / "lock-alias").unlink()
                else:
                    if mutation == "symlink":
                        lock.unlink()
                    (base / "lock-saved").rename(lock)
            close_locks()
        # The second-lock rejection must close the already acquired first lock.
        close_locks()
        count += 1

        def run(args):
            if failure:
                raise subprocess.CalledProcessError(1, args)
            key = {"ps": "containers", "network": "networks", "exec": "database", "inspect": "health"}[args[1]]
            return replies[key]

        def check():
            probe(str(root), str(raw), "same-boot", expected, absent, "sub2api_v2_fixture", "fixture",
                  run=run, proc_root=str(proc), boot_path=str(boot))

        def blocked():
            nonlocal count
            try:
                check()
            except (RuntimeError, OSError, subprocess.CalledProcessError):
                count += 1
                return
            raise AssertionError("proof accepted drift")

        check()
        count += 1
        for name in absent:
            path = root / name
            path.touch()
            blocked()
            path.unlink()
            path.symlink_to(base / "missing")
            blocked()
            path.unlink()
        for name in expected:
            path = root / name
            original, mode = path.read_bytes(), path.stat().st_mode & 0o777
            path.chmod(0o600)
            path.write_bytes(original + b"drift")
            path.chmod(mode)
            blocked()
            path.chmod(0o600)
            path.write_bytes(original)
            path.chmod(mode)
            path.chmod(0o666)
            blocked()
            path.chmod(mode)
            os.link(path, base / "extra-link")
            blocked()
            (base / "extra-link").unlink()
            path.rename(base / "saved")
            path.symlink_to(base / "saved")
            blocked()
            path.unlink()
            (base / "saved").rename(path)
        for path in (root, root / "output", root / "logs", raw):
            path.chmod(0o755)
            blocked()
            path.chmod(0o700)
        boot.write_text("other-boot\n")
        blocked()
        boot.write_text("same-boot\n")
        for key, bad in (("containers", "sub2api-v2-app-fixture\n"),
                         ("containers", "sub2api-preview-fixture\n"),
                         ("networks", "sub2api-v2-net-fixture\n"), ("networks", "sub2api-v2-net-other\n"),
                         ("database", "1\n"), ("database", ""), ("health", "unhealthy\n")):
            old = replies[key]
            replies[key] = bad
            blocked()
            replies[key] = old
        for args in (("bash", "vm-space-clean.sh"), ("bash", "sub2api-vm-validate"),
                     ("docker", "buildx"), ("buildctl",), ("bash", "run-validator.sh"), ("python3", "-m", "release.vm_validate"),
                     ("docker", "run", "candidate", "--migration-apply-plan-json"),
                     ("bash", "/tmp/release-input/owned/bootstrap-vm"),
                     ("bash", "/tmp/.sub2api-release-unit.fixture/sub2api-sign-gate")):
            process = proc / "987654321"
            process.mkdir()
            (process / "cmdline").write_bytes(b"\0".join(arg.encode() for arg in args) + b"\0")
            blocked()
            (process / "cmdline").unlink()
            process.rmdir()
        failure = True
        blocked()
        failure = None
        check()
        count += 1
    return count


def main():
    if "--emit-script" in sys.argv:
        print("import os,json\n" + inspect.getsource(_vm_probe) + "\n" + inspect.getsource(_acquire_locks)
              + "\n" + inspect.getsource(fixture_checks)
              + "\nif os.getuid() != 0: raise RuntimeError('Linux fixture requires root')\n"
              + "print(json.dumps({'vm_fast_fixture_preserve_integration':'pass','checks':fixture_checks(_vm_probe,"
              + repr(ABSENT) + ",_acquire_locks),'cleanup':'pass'}))")
        return
    if os.name != "posix" or os.getuid() != 0:
        raise SystemExit("Use --emit-script and execute in a controlled root Linux environment")
    print(json.dumps({"vm_fast_fixture_preserve_integration": "pass", "checks": fixture_checks(_vm_probe, ABSENT, _acquire_locks), "cleanup": "pass"}))


if __name__ == "__main__":
    main()
