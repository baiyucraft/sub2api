#!/usr/bin/env bash
# Source-only guard for fault injection in a fully offline disposable Guest.
# The runner owns the root filesystem and creates these proof files only after
# checking the launch configuration. A marker alone never authorizes injection.

recovery_drill_assert_isolated() {
  local bundle_sha=${1:?helper bundle SHA-256 is required}
  [[ $bundle_sha =~ ^[0-9a-f]{64}$ && ${RECOVERY_DRILL_MODE:-} == 1 ]] || return 1
  [[ -z ${DOCKER_HOST:-} && -z ${DOCKER_CONTEXT:-} && -z ${DOCKER_CONFIG:-} && -z ${DOCKER_TLS_VERIFY:-} && -z ${DOCKER_CERT_PATH:-} ]] || return 1
  local marker=/run/sub2api-recovery-drill-isolated
  local proof=/run/sub2api-recovery-drill-host.json
  [[ $(id -u) == 0 ]] || return 1
  [[ -f $marker && ! -L $marker && $(stat -c '%u:%g:%a:%h' "$marker") == 0:0:600:1 ]] || return 1
  [[ -f $proof && ! -L $proof && $(stat -c '%u:%g:%a:%h' "$proof") == 0:0:600:1 ]] || return 1
  [[ $(cat -- "$marker") == "$bundle_sha" ]] || return 1
  [[ $(systemd-detect-virt --container) == systemd-nspawn ]] || return 1
  [[ -S /run/docker.sock && ! -L /run/docker.sock ]] || return 1
  [[ $(docker context show) == default ]] || return 1
  [[ $(docker context inspect default --format '{{.Endpoints.docker.Host}}') == unix:///var/run/docker.sock ]] || return 1
  [[ $(docker info --format '{{.DockerRootDir}}') == /var/lib/docker ]] || return 1
  local engine_id
  engine_id=$(docker info --format '{{.ID}}') || return 1
  [[ $engine_id =~ ^[A-Za-z0-9:-]{12,128}$ ]] || return 1
  RECOVERY_GUEST_ENGINE_ID="$engine_id" python3 - "$proof" "$bundle_sha" <<'PY'
import json
import os
from pathlib import Path
import re
import subprocess
import sys


def require(condition):
    if not condition:
        raise SystemExit("recovery drill isolation proof rejected")


def routes(family):
    result = subprocess.run(["ip", "-j", family, "route", "show", "default"],
                            capture_output=True, text=True, check=True, timeout=10)
    value = json.loads(result.stdout)
    require(isinstance(value, list))
    return value


proof = json.loads(Path(sys.argv[1]).read_text(encoding="utf-8"))
require(set(proof) == {"schema", "backend", "helper_bundle_sha256", "host_namespaces", "host_docker_engine_id"})
require(type(proof["schema"]) is int and proof["schema"] == 1 and proof["backend"] == "nspawn-offline-private-users")
require(proof["helper_bundle_sha256"] == sys.argv[2])
namespaces = ("mnt", "pid", "net", "user", "cgroup", "uts", "ipc")
require(isinstance(proof["host_namespaces"], dict) and set(proof["host_namespaces"]) == set(namespaces))
for kind in namespaces:
    expected = proof["host_namespaces"][kind]
    require(isinstance(expected, str) and re.fullmatch(re.escape(kind) + r":\[[1-9][0-9]*\]", expected) is not None)
    require(os.readlink("/proc/self/ns/" + kind) != expected)
    require(os.readlink("/proc/self/ns/" + kind) == os.readlink("/proc/1/ns/" + kind))
engine = proof["host_docker_engine_id"]
require(isinstance(engine, str) and re.fullmatch(r"[A-Za-z0-9:-]{12,128}", engine) is not None)
require(engine != os.environ["RECOVERY_GUEST_ENGINE_ID"])
# Root must be mapped away from host UID 0. This prevents Guest CAP_SYS_ADMIN
# from becoming host CAP_SYS_ADMIN even when Docker needs nested namespaces.
uid_map = [tuple(int(field) for field in line.split()) for line in Path("/proc/self/uid_map").read_text().splitlines()]
require(len(uid_map) == 1 and uid_map[0][0] == 0 and uid_map[0][1] >= 65536 and uid_map[0][2] >= 65536)
require(Path("/sys/fs/cgroup/cgroup.controllers").is_file())
require(routes("-4") == [] and routes("-6") == [])
# No host-facing veth, physical NIC, tunnel or wireless interface is allowed.
# Docker's bridges and internal veths remain inside this private net namespace.
interfaces = [entry.name for entry in Path("/sys/class/net").iterdir()]
require("lo" in interfaces)
require(all(re.fullmatch(r"lo|docker0|br-[0-9a-f]{12}|veth[0-9a-f]+", item) for item in interfaces))
PY
}
