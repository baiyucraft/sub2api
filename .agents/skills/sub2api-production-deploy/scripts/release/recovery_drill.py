"""Disposable offline nspawn recovery drill; never restore in the host namespace.

Infrastructure supplies a clean, dedicated rootfs with synthetic recovery
fixtures and offline images. This entry point does not install host packages,
copy production state, generate trust keys, or declare missing cases passed.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
from typing import Any


BACKEND = "nspawn-offline-private-users"
SHA = re.compile(r"[0-9a-f]{64}")
RELEASE = re.compile(r"(?:182|187|191|192|194|195|197|198|199|202|206|207|208|209|210|212|213|215|232|233|234|235|236|237|238|239|240|241|242|243|244|245|246|247|248|249|250|251|252|253|254|255|256|257|258|259|260|261|262|263|264|265|266|267|268|269|270|271|272|273|274|275|276)-[0-9a-f]{12}-[0-9]+-[0-9a-f]{8}")
DRILL = re.compile(r"drill-[0-9a-f]{16}")
PHASES = ("compose_restored", "postgres_restored", "redis_restored", "app_healthy", "nginx_restored", "backup_units_restored", "state_cleanup", "claim_reconciled")
NAMESPACES = ("mnt", "pid", "net", "user", "cgroup", "uts", "ipc")
CONFIG_FIELDS = frozenset({"schema", "drill_id", "backend", "fixture_kind", "release_id", "recovery_helper_bundle_sha256", "pre_image_id", "recovery_point_sha256", "redis_health_budget_seconds"})


class DrillBlocked(RuntimeError):
    pass


def require(condition: bool, message: str) -> None:
    if not condition:
        raise DrillBlocked(message)


def canonical(value: Any) -> bytes:
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True) + "\n").encode()


def digest(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def safe_file(path: Path, *, private: bool = False) -> None:
    require(path.is_file() and not path.is_symlink(), "regular nonsymlink file required")
    info = path.stat()
    require(info.st_nlink == 1, "hard-linked drill file rejected")
    if private:
        require(info.st_uid == 0 and stat.S_IMODE(info.st_mode) in {0o400, 0o600}, "root-only drill file required")


def bundle_api():
    # The same verifier is used by production, staging and this standalone
    # Guest entry point. No second bundle hash or flattened-path algorithm.
    sys.dont_write_bytecode = True
    if __package__:
        from . import recovery_bundle
    else:
        import recovery_bundle
    return recovery_bundle


def verify_helper_bundle(directory: Path, expected: str) -> dict[str, Any]:
    require(SHA.fullmatch(expected) is not None, "invalid helper bundle identity")
    manifest = directory / "recovery-helper-bundle.json"
    safe_file(manifest)
    document = json.loads(manifest.read_text(encoding="utf-8"))
    api = bundle_api()
    require(api.recovery_bundle_sha256(document) == expected, "helper bundle identity differs")
    api.verify_recovery_bundle(directory, document)
    return document


def validate_config(document: dict[str, Any]) -> dict[str, Any]:
    require(isinstance(document, dict) and set(document) == CONFIG_FIELDS, "unknown or missing drill config fields")
    require(type(document["schema"]) is int and document["schema"] == 1, "unknown drill schema")
    require(document["backend"] == BACKEND, "unsupported isolation backend")
    require(document["fixture_kind"] == "synthetic-offline-complete-recovery", "real production fixtures are forbidden")
    require(isinstance(document["drill_id"], str) and DRILL.fullmatch(document["drill_id"]) is not None, "invalid drill identity")
    require(isinstance(document["release_id"], str) and RELEASE.fullmatch(document["release_id"]) is not None, "invalid fixture release identity")
    for key in ("recovery_helper_bundle_sha256", "recovery_point_sha256"):
        require(isinstance(document[key], str) and SHA.fullmatch(document[key]) is not None, "invalid drill digest")
    require(isinstance(document["pre_image_id"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", document["pre_image_id"]) is not None, "invalid old image identity")
    require(type(document["redis_health_budget_seconds"]) is int and 61 <= document["redis_health_budget_seconds"] <= 600, "invalid Redis health budget")
    return document


def guest_helper_directory(config: dict[str, Any]) -> Path:
    return Path("/opt/sub2api/releases") / config["release_id"] / "recovery-helpers" / config["recovery_helper_bundle_sha256"]


def probe() -> dict[str, Any]:
    linux = sys.platform.startswith("linux")
    tools = {name: bool(shutil.which(name)) for name in ("systemd-nspawn", "docker", "ip", "python3")}
    blockers = ["missing_" + name for name, present in tools.items() if not present]
    if not linux:
        blockers.append("linux_host_required")
    elif os.geteuid() != 0:
        blockers.append("root_host_required")
    if linux and not Path("/sys/fs/cgroup/cgroup.controllers").is_file():
        blockers.append("unified_cgroup_required")
    return {"schema": 1, "backend": BACKEND, "read_only": True, "tools": tools, "blockers": blockers, "status": "ready_for_plan" if not blockers else "blocked"}


def validate_root(rootfs: Path, config: dict[str, Any]) -> None:
    expected = Path("/var/lib/sub2api-recovery-drills") / config["drill_id"] / "rootfs"
    require(rootfs == expected and rootfs.resolve() == expected, "Guest root must be a dedicated owned drill path")
    require(rootfs.is_dir() and not rootfs.is_symlink(), "Guest root unavailable")
    for ancestor in (rootfs, rootfs.parent, rootfs.parent.parent):
        info = ancestor.stat()
        require(not ancestor.is_symlink() and info.st_uid == 0 and stat.S_IMODE(info.st_mode) == 0o700, "Guest ownership or private mode invalid")
    # No mount (especially a bind of production data, daemon socket or cgroup)
    # may already exist inside the prepared filesystem.
    for line in Path("/proc/self/mountinfo").read_text().splitlines():
        target = line.split(" ")[4].replace("\\040", " ").replace("\\134", "\\")
        require(not (target == str(rootfs) or target.startswith(str(rootfs) + "/")), "pre-existing Guest mount rejected")
    for relative in ("usr/lib/systemd/systemd", "usr/bin/dockerd", "usr/bin/docker", "usr/sbin/nginx", "usr/bin/python3", "usr/sbin/ip"):
        path = rootfs / relative
        require(path.is_file(), "pre-provisioned Guest runtime incomplete")
    for relative in ("etc", "etc/systemd", "etc/systemd/system", "opt"):
        path = rootfs / relative
        require(path.is_dir() and not path.is_symlink() and path.resolve() == path, "Guest provisioning path unsafe")
    deployment = rootfs / "opt/sub2api"
    require(deployment.is_dir() and not deployment.is_symlink(), "offline recovery fixture unavailable")
    for directory, subdirs, files in os.walk(deployment, followlinks=False):
        for name in subdirs + files:
            require(not (Path(directory) / name).is_symlink(), "fixture symlink rejected")
    assets = rootfs / guest_helper_directory(config).relative_to("/")
    verify_helper_bundle(assets, config["recovery_helper_bundle_sha256"])
    point = deployment / "backups/release-state" / config["release_id"] / "recovery-point.tar"
    safe_file(point, private=True)
    require(digest(point) == config["recovery_point_sha256"], "fixture recovery point identity differs")
    safe_file(deployment / "releases" / config["release_id"] / ".prepared", private=True)
    for name in ("gate.json", "gate.sig"):
        safe_file(deployment / "releases" / config["release_id"] / name, private=True)


def nspawn_command(rootfs: Path, config: dict[str, Any]) -> list[str]:
    return ["systemd-nspawn", "--directory=" + str(rootfs), "--machine=" + config["drill_id"], "--boot", "--private-network", "--private-users=pick", "--settings=no", "--register=no", "--link-journal=no", "--property=Delegate=yes"]


def plan(rootfs: Path, config: dict[str, Any]) -> dict[str, Any]:
    validate_config(config)
    result = probe()
    if result["blockers"]:
        return {**result, "drill_id": config["drill_id"], "required_resources": ["dedicated complete synthetic rootfs", "offline old/app/PG/Redis images", "same-byte formal recovery helper bundle", "systemd Docker nginx and private-user nested Docker support"]}
    validate_root(rootfs, config)
    return {"schema": 1, "status": "planned", "drill_id": config["drill_id"], "backend": BACKEND, "recovery_helper_bundle_sha256": config["recovery_helper_bundle_sha256"], "launch_argv": nspawn_command(rootfs, config), "network": "offline_private_namespace", "host_binds": [], "cases": ["redis_start_over_60_seconds", "ttl_natural_expiry", "redis_lazy_expiry_between_live_queries", "app_and_cleanup_interrupt_resume", "stale_ingress_reconciliation", "completed_data_checkpoint_reverification"]}


def write_private(path: Path, data: bytes) -> None:
    require(path.parent.is_dir() and not path.parent.is_symlink(), "private output parent unavailable")
    require(not path.exists() and not path.is_symlink(), "owned output already exists")
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(data)


def host_proof(config: dict[str, Any]) -> dict[str, Any]:
    result = subprocess.run(["docker", "info", "--format", "{{.ID}}"], capture_output=True, text=True, check=True, timeout=15, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"})
    return {"schema": 1, "backend": BACKEND, "helper_bundle_sha256": config["recovery_helper_bundle_sha256"], "host_namespaces": {name: os.readlink("/proc/1/ns/" + name) for name in NAMESPACES}, "host_docker_engine_id": result.stdout.strip()}


def execute_host(rootfs: Path, config: dict[str, Any], timeout: int) -> dict[str, Any]:
    prepared = plan(rootfs, config)
    require(prepared["status"] == "planned", "drill resources unavailable; no Guest launched")
    require(type(timeout) is int and 300 <= timeout <= 7200, "invalid Guest execution timeout")
    etc = rootfs / "etc"
    write_private(etc / "sub2api-recovery-drill.json", canonical(config))
    write_private(etc / "sub2api-recovery-drill-host.json", canonical(host_proof(config)))
    service = etc / "systemd/system/sub2api-recovery-drill.service"
    service_document = "[Unit]\nAfter=docker.service nginx.service\nRequires=docker.service\n[Service]\nType=oneshot\nEnvironment=PYTHONDONTWRITEBYTECODE=1\nExecStart=/usr/bin/python3 -B " + str(guest_helper_directory(config) / "recovery_drill.py") + " guest-run\nExecStopPost=/usr/bin/systemctl --no-block poweroff\nTimeoutStartSec=infinity\n[Install]\nWantedBy=multi-user.target\n"
    write_private(service, service_document.encode())
    wants = etc / "systemd/system/multi-user.target.wants"
    require(not wants.is_symlink(), "Guest systemd wants directory unsafe")
    wants.mkdir(mode=0o755, exist_ok=True)
    target = wants / service.name
    require(not target.exists() and not target.is_symlink(), "Guest service already registered")
    target.symlink_to("../" + service.name)
    evidence_root = rootfs / "opt/sub2api-recovery-drill"
    require(not evidence_root.exists() and not evidence_root.is_symlink(), "Guest evidence directory already exists")
    evidence_root.mkdir(mode=0o700)
    raw = rootfs.parent / "guest.raw.log"
    start = time.monotonic()
    timed_out = False
    with raw.open("xb") as stream:
        os.chmod(raw, 0o600)
        child = subprocess.Popen(nspawn_command(rootfs, config), stdin=subprocess.DEVNULL, stdout=stream, stderr=subprocess.STDOUT, env={"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "LANG": "C.UTF-8"})
        try:
            code = child.wait(timeout=timeout)
        except subprocess.TimeoutExpired:
            timed_out = True
            child.terminate()  # only the nspawn process owned by this run
            try:
                code = child.wait(timeout=60)
            except subprocess.TimeoutExpired:
                raise DrillBlocked("owned Guest did not shut down; evidence preserved")
    require(not timed_out and code == 0, "Guest launch/runtime failed; root-only evidence preserved")
    report_path = evidence_root / "result.json"
    safe_file(report_path)
    report = json.loads(report_path.read_text(encoding="utf-8"))
    verify_real_evidence(report, config)
    checkpoints = rootfs / "opt/sub2api/releases" / config["release_id"] / "recovery-checkpoints"
    require(report["checkpoint_sha256"] == {phase: digest(checkpoints / (phase + ".done")) for phase in PHASES}, "real checkpoint bytes differ from report")
    current_helpers = rootfs / guest_helper_directory(config).relative_to("/")
    verify_helper_bundle(current_helpers, config["recovery_helper_bundle_sha256"])
    return {**report, "host_elapsed_seconds": round(time.monotonic() - start, 3), "owned_guest_stopped": True, "evidence_preserved": True, "temporary_material_destroyed": "not_checked"}


def verify_real_evidence(report: dict[str, Any], config: dict[str, Any]) -> None:
    require(isinstance(report, dict) and type(report.get("schema")) is int and report.get("schema") == 1 and report.get("status") == "pass", "real Guest drill did not pass")
    for key in ("drill_id", "backend", "release_id", "pre_image_id", "recovery_point_sha256", "recovery_helper_bundle_sha256"):
        require(report.get(key) == config[key], "real drill evidence identity differs")
    for key in ("isolation_verified", "compose_restored", "postgres_restored", "redis_restored", "app_healthy", "nginx_restored", "backup_units_restored", "claim_reconciled", "state_cleanup", "completed_data_not_reapplied", "stale_ingress_reconciled", "ttl_natural_expiry", "redis_lazy_expiry_observed"):
        require(report.get(key) is True, "required real recovery case missing")
    require(report.get("interrupts") == ["app_healthy", "backup_units_restored"], "real interruption cases missing")
    delay = report.get("redis_health_wait_seconds")
    require(type(delay) in (int, float) and 60 < delay <= config["redis_health_budget_seconds"], "real delayed Redis startup case missing")
    hashes = report.get("checkpoint_sha256")
    require(isinstance(hashes, dict) and set(hashes) == set(PHASES), "checkpoint evidence missing")
    require(all(isinstance(value, str) and SHA.fullmatch(value) is not None for value in hashes.values()), "invalid checkpoint evidence digest")


def command_values(argv: list[str], env: dict[str, str], raw: Path) -> tuple[int, dict[str, str]]:
    with raw.open("ab") as stream:
        os.chmod(raw, 0o600)
        result = subprocess.run(argv, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=stream, timeout=1800)
        stream.write(result.stdout)
    values = {}
    for line in result.stdout.decode("utf-8", errors="replace").splitlines():
        key, separator, value = line.partition("=")
        if separator and re.fullmatch(r"[a-z_]{1,80}", key) and re.fullmatch(r"[A-Za-z0-9:._-]{1,160}", value):
            values[key] = value
    return result.returncode, values


def data_runtime_identity() -> tuple[str, str]:
    postgres = subprocess.run(["docker", "exec", "sub2api-postgres", "psql", "-XAt", "-U", "sub2api", "-d", "postgres", "-c", "SELECT oid FROM pg_database WHERE datname='sub2api'"], capture_output=True, text=True, check=True, timeout=15)
    configured = subprocess.run(["docker", "inspect", "--format", "{{json .Config.Entrypoint}}\n{{json .Config.Cmd}}", "sub2api-redis"], capture_output=True, text=True, check=True, timeout=15)
    args = []
    for line in configured.stdout.splitlines():
        part = json.loads(line)
        require(part is None or (isinstance(part, list) and all(isinstance(item, str) for item in part)), "Redis runtime arguments invalid")
        args.extend(part or [])
    password = ""
    for index, value in enumerate(args):
        if value == "--requirepass":
            require(index + 1 < len(args), "Redis authentication arguments invalid")
            password = args[index + 1]
        elif value.startswith("--requirepass="):
            password = value.partition("=")[2]
    require("\n" not in password and "\r" not in password, "Redis authentication input invalid")
    # Authentication stays on stdin; only the non-secret INFO server response
    # is captured. No password is placed in argv, reports or raw logs.
    redis = subprocess.run(["docker", "exec", "-i", "sub2api-redis", "sh", "-c", 'IFS= read -r REDISCLI_AUTH; export REDISCLI_AUTH; redis-cli --no-auth-warning INFO server'], input=password + "\n", capture_output=True, text=True, check=True, timeout=15)
    oid = postgres.stdout.strip()
    run_ids = [line.partition(":")[2].strip() for line in redis.stdout.splitlines() if line.startswith("run_id:")]
    require(oid.isdigit() and len(run_ids) == 1 and re.fullmatch(r"[0-9a-f]{40}", run_ids[0]) is not None, "live data runtime identity unavailable")
    return oid, run_ids[0]


def guest_environment(config: dict[str, Any]) -> dict[str, str]:
    helpers = guest_helper_directory(config)
    release = Path("/opt/sub2api/releases") / config["release_id"]
    env = {key: value for key, value in os.environ.items() if not key.startswith("DOCKER_") and not key.startswith("COMPOSE_") and not key.startswith("RECOVERY_")}
    env.update({"RELEASE_DIR": str(release), "RECOVERY_ASSETS_DIR": str(helpers), "RECOVERY_HELPER_BUNDLE_SHA256": config["recovery_helper_bundle_sha256"], "RECOVERY_DRILL_MODE": "1", "RECOVERY_HELPER_GATE": str(release / "gate.json"), "RECOVERY_HELPER_SIGNATURE": str(release / "gate.sig")})
    return env


def guest_run() -> dict[str, Any]:
    config_path = Path("/etc/sub2api-recovery-drill.json")
    safe_file(config_path, private=True)
    config = validate_config(json.loads(config_path.read_text(encoding="utf-8")))
    claim = Path("/opt/sub2api/releases/.active-release")
    helpers = guest_helper_directory(config)
    verify_helper_bundle(helpers, config["recovery_helper_bundle_sha256"])
    env = guest_environment(config)
    safe_file(Path(env["RECOVERY_HELPER_GATE"]), private=True)
    safe_file(Path(env["RECOVERY_HELPER_SIGNATURE"]), private=True)
    write_private(Path("/run/sub2api-recovery-drill-isolated"), (config["recovery_helper_bundle_sha256"] + "\n").encode())
    safe_file(Path("/etc/sub2api-recovery-drill-host.json"), private=True)
    write_private(Path("/run/sub2api-recovery-drill-host.json"), Path("/etc/sub2api-recovery-drill-host.json").read_bytes())
    output = Path("/opt/sub2api-recovery-drill")
    raw = output / "helpers.raw.log"
    guard = 'set -Eeuo pipefail; source "$1/recovery-isolation.sh"; recovery_drill_assert_isolated "$2"'
    code, _ = command_values(["bash", "-c", guard, "isolation", str(helpers), config["recovery_helper_bundle_sha256"]], env, raw)
    require(code == 0, "live Guest isolation proof rejected")
    state = Path("/opt/sub2api/backups/release-state") / config["release_id"]
    safe_file(state / "recovery-point.tar", private=True)
    require(digest(state / "recovery-point.tar") == config["recovery_point_sha256"], "live fixture recovery point differs")
    safe_file(state / "pre-image-id", private=True)
    require((state / "pre-image-id").read_text().strip() == config["pre_image_id"], "live fixture pre-image differs")
    checkpoints = Path(env["RELEASE_DIR"]) / "recovery-checkpoints"
    require(not checkpoints.exists(), "fixture already contains recovery checkpoints")
    env["RECOVERY_INTERRUPT_AFTER_PHASE"] = "app_healthy"
    code, first = command_values(["bash", str(helpers / "restore.sh")], env, raw)
    require(code == 79, "actual app checkpoint interruption missing")
    require(all((checkpoints / (phase + ".done")).is_file() for phase in PHASES[:4]), "interrupted recovery checkpoint prefix incomplete")
    data_before = data_runtime_identity()
    markers_before = {phase: digest(checkpoints / (phase + ".done")) for phase in PHASES[:4]}
    env.pop("RECOVERY_INTERRUPT_AFTER_PHASE")
    code, second = command_values(["bash", str(helpers / "restore.sh")], env, raw)
    require(code == 0 and second.get("coordinated_restore") == "verified" and second.get("restored_image_id") == config["pre_image_id"], "same-byte restore resume or old image identity failed")
    require(data_runtime_identity() == data_before, "completed data phase was reapplied during restore resume")
    require(markers_before == {phase: digest(checkpoints / (phase + ".done")) for phase in PHASES[:4]}, "completed checkpoint identity drifted")
    # A real stale ingress applied marker must coexist with the restored marker.
    ingress = state / "nginx-ingress-transaction"
    require((state / "nginx-recovery-restored").is_file() and (ingress / "applied").is_file(), "stale ingress fixture case absent")
    env["RECOVERY_INTERRUPT_AFTER_PHASE"] = "backup_units_restored"
    code, _ = command_values(["bash", str(helpers / "recovery-finish.sh")], env, raw)
    require(code == 79, "actual cleanup-before checkpoint interruption missing")
    require((checkpoints / "backup_units_restored.done").is_file(), "backup units checkpoint not persisted")
    env.pop("RECOVERY_INTERRUPT_AFTER_PHASE")
    code, _ = command_values(["bash", str(helpers / "recovery-finish.sh")], env, raw)
    require(code == 0, "same-byte finish resume failed")
    require(data_runtime_identity() == data_before, "completed data phase was reapplied during finish resume")
    require(all((checkpoints / (phase + ".done")).is_file() for phase in PHASES), "real recovery checkpoint chain incomplete")
    require(not claim.exists() and not (ingress / "applied").exists(), "stale ingress or active claim remains")
    delay = float(first.get("redis_restore_health_wait_seconds", "0"))
    backup_expiring = int(first.get("redis_backup_expiring", "0"))
    restored_expiring = int(first.get("redis_restored_expiring", "0"))
    require(backup_expiring > restored_expiring >= 0 and first.get("redis_ttl_reconciliation") == "verified", "actual TTL expiry reconciliation evidence absent")
    restored_dbsize = int(first.get("redis_restored_dbsize", "-1"))
    restored_keyspace = int(first.get("redis_restored_keyspace_keys", "-1"))
    require(restored_dbsize > restored_keyspace >= 0, "real Redis lazy expiration between live queries was not observed")
    report = {"schema": 1, "status": "pass", **{key: config[key] for key in ("drill_id", "backend", "release_id", "pre_image_id", "recovery_point_sha256", "recovery_helper_bundle_sha256")}, **{phase: True for phase in PHASES}, "isolation_verified": True, "completed_data_not_reapplied": True, "stale_ingress_reconciled": True, "ttl_natural_expiry": True, "redis_lazy_expiry_observed": True, "redis_restored_dbsize": restored_dbsize, "redis_restored_keyspace_keys": restored_keyspace, "interrupts": ["app_healthy", "backup_units_restored"], "redis_health_wait_seconds": delay, "postgres_oid": data_before[0], "redis_run_id_sha256": hashlib.sha256(data_before[1].encode()).hexdigest(), "checkpoint_sha256": {phase: digest(checkpoints / (phase + ".done")) for phase in PHASES}}
    verify_real_evidence(report, config)
    write_private(output / "result.json", canonical(report))
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("probe", "plan", "run", "guest-run"))
    parser.add_argument("--guest-root", type=Path)
    parser.add_argument("--config", type=Path)
    parser.add_argument("--report", type=Path, help="new root-only evidence file; never overwrites prior evidence")
    parser.add_argument("--timeout", type=int, default=3600)
    args = parser.parse_args()
    try:
        if args.command == "probe":
            report = probe()
        elif args.command == "guest-run":
            report = guest_run()
        else:
            require(args.guest_root is not None and args.config is not None, "Guest root and config required")
            safe_file(args.config, private=True)
            config = validate_config(json.loads(args.config.read_text(encoding="utf-8")))
            report = plan(args.guest_root, config) if args.command == "plan" else execute_host(args.guest_root, config, args.timeout)
        if args.report is not None:
            require(sys.platform.startswith("linux") and os.geteuid() == 0, "root-only evidence output requires Linux root")
            write_private(args.report, canonical(report))
        print(json.dumps(report, sort_keys=True))
        if report.get("status") == "blocked":
            raise SystemExit(2)
    except (RuntimeError, OSError, ValueError, ImportError, subprocess.SubprocessError) as error:
        # Diagnostics from subprocesses stay in root-only raw logs. Only our
        # bounded domain error is suitable for public status.
        reason = str(error) if isinstance(error, DrillBlocked) else type(error).__name__
        print(json.dumps({"schema": 1, "status": "blocked", "reason": reason}))
        raise SystemExit(2)


if __name__ == "__main__":
    main()
