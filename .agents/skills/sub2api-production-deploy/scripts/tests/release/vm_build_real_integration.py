"""Opt-in real Docker/BuildKit contract test on the configured local VM.

The host Docker daemon is only inspected and used to export an already cached
Dockerfile frontend. All registry traffic and builds run without a default
route, inside new mount/network/PID namespaces and private Docker data roots.
No registry, builder, or other image is downloaded. Raw logs remain on the VM.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import http.client
import json
import os
import re
import shlex
import shutil
import subprocess
import sys
import tarfile
import time
from pathlib import Path
from typing import Any

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
SCENARIOS = (
    "original_success", "success", "tag_head_502", "frontend_missing", "tag_drift",
    "wrong_digest", "config_missing", "layer_truncated", "manifest_missing", "auth_error",
    "unsupported_builder", "unsupported_platform", "builder_drift", "context_drift",
    "daemon_drift", "platform_drift", "metadata_only_no_blobs",
)
COMMIT = "a" * 40
RELEASE = "274-aaaaaaaaaaaa-1-deadbeef"


def digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def execute(args: list[str], *, env: dict[str, str] | None = None,
            timeout: int = 90, log: Path | None = None) -> subprocess.CompletedProcess:
    # This function is used only by the remote Linux entry points.
    result = subprocess.run(args, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env, timeout=timeout)
    if log is not None:
        log.write_bytes(result.stdout + result.stderr)
        os.chmod(log, 0o600)
    return result


def host_snapshot() -> dict[str, str]:
    result = execute(["docker", "info", "--format", "{{json .ID}} {{json .OSType}} {{json .Architecture}}"])
    if result.returncode:
        raise RuntimeError("host_info")
    result2 = execute(["docker", "ps", "-aq", "--no-trunc"])
    if result2.returncode:
        raise RuntimeError("host_containers")
    config = Path("/etc/docker/daemon.json")
    return {"daemon_sha256": digest(result.stdout), "containers_sha256": digest(b"\n".join(sorted(result2.stdout.splitlines()))),
            "config_sha256": digest(config.read_bytes()) if config.exists() else "absent"}


def isolated_env(directory: Path) -> dict[str, str]:
    env = dict(os.environ)
    for key in list(env):
        if key.lower() in {"http_proxy", "https_proxy", "all_proxy", "no_proxy"} or key.startswith("DOCKER_"):
            env.pop(key, None)
    env.update(DOCKER_HOST="unix://" + str(directory / "docker.sock"), DOCKER_CONFIG=str(directory / "docker-config"),
               NO_PROXY="localhost,127.0.0.1", no_proxy="localhost,127.0.0.1")
    return env


class PrivateDaemon:
    def __init__(self, directory: Path, certificate: Path):
        self.directory = directory
        directory.mkdir(mode=0o700)
        (directory / "docker-config").mkdir(mode=0o700)
        self.env = isolated_env(directory)
        self.env["SSL_CERT_FILE"] = str(certificate)
        # containerd adds another socket suffix; keep AF_UNIX paths below 108 bytes.
        self.runtime = directory.parents[2] / ("r" + digest(str(directory).encode())[:8])
        self.runtime.mkdir(mode=0o700)
        self.env["DOCKER_HOST"] = "unix://" + str(self.runtime / "docker.sock")
        (directory / "daemon.json").write_text(json.dumps({"features": {"containerd-snapshotter": False}}))
        self.log = (directory / "dockerd.log").open("wb")
        args = ["dockerd", "--host=" + self.env["DOCKER_HOST"], "--data-root=" + str(directory / "data"),
                "--exec-root=" + str(self.runtime / "r"), "--pidfile=" + str(self.runtime / "dockerd.pid"),
                "--config-file=" + str(directory / "daemon.json"), "--bridge=none", "--iptables=false",
                "--ip6tables=false", "--ip-forward=false", "--ip-masq=false", "--userland-proxy=false",
                "--storage-driver=vfs", "--log-level=error"]
        self.process = subprocess.Popen(args, stdout=self.log, stderr=self.log, env=self.env)

    def ready(self) -> None:
        for _ in range(160):
            result = execute(["docker", "info", "--format", "{{json .}}"], env=self.env, timeout=3)
            if not result.returncode:
                doc = json.loads(result.stdout)
                if doc.get("OSType") != "linux" or doc.get("Architecture") not in ("amd64", "x86_64"):
                    raise RuntimeError("private_platform")
                return
            if self.process.poll() is not None:
                raise RuntimeError("private_daemon_start")
            time.sleep(0.25)
        raise RuntimeError("private_daemon_timeout")

    def close(self) -> bool:
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=5)
        self.log.close()
        stopped = self.process.poll() is not None
        if stopped:
            # Only the instance's exact checked root is eligible for cleanup.
            for name in ("data", "docker-config"):
                target = self.directory / name
                if target.exists():
                    if target.is_symlink() or target.resolve().parent != self.directory.resolve():
                        raise RuntimeError("private_cleanup_path")
                    shutil.rmtree(target)
            if self.runtime.is_symlink() or self.runtime.resolve().parent != self.directory.parents[2].resolve():
                raise RuntimeError("private_runtime_cleanup_path")
            shutil.rmtree(self.runtime)
        return stopped


def shell_prelude(root: Path, registry: str) -> str:
    return "\n".join([
        "set -Eeuo pipefail", "umask 077", "source " + shlex.quote(str(root / "helper.sh")),
        "VM_BUILD_IMAGES=(" + " ".join(shlex.quote(registry + "/fixture/" + role + ":v1") for role in ("node", "golang", "alpine", "postgres")) + ")",
        "VM_BUILD_FRONTEND=" + shlex.quote(registry + "/fixture/frontend:v1"),
    ]) + "\n"


def parse_result(path: Path) -> dict[str, str]:
    values = {}
    for line in path.read_text().splitlines():
        key, value = line.split("=", 1)
        if key not in {"preflight", "proof_use", "candidate", "failure_code", "verified_count"} or not re.fullmatch(r"[a-z0-9_]+", value):
            raise RuntimeError("fixture_result_shape")
        values[key] = value
    return values


def tiny_dockerfile() -> str:
    return "\n".join([
        "# syntax=docker/dockerfile:1.7", "ARG NODE_IMAGE", "ARG GOLANG_IMAGE", "ARG ALPINE_IMAGE", "ARG POSTGRES_IMAGE",
        "FROM ${NODE_IMAGE} AS node", "FROM ${GOLANG_IMAGE} AS golang", "FROM ${ALPINE_IMAGE} AS alpine",
        "FROM ${POSTGRES_IMAGE} AS postgres", "FROM scratch",
        "COPY --from=node /etc/os-release /node", "COPY --from=golang /etc/os-release /golang",
        "COPY --from=alpine /etc/os-release /alpine", "COPY --from=postgres /etc/os-release /postgres", "",
    ])


def control_tag_get(registry: Any, role: str, expected_digest: str) -> bool:
    # An actual protocol request proves the control state. It is completed
    # before the measured helper/candidate request interval begins.
    connection = http.client.HTTPConnection(registry.address, timeout=10)
    try:
        connection.request("GET", "/v2/fixture/" + role + "/manifests/v1")
        response = connection.getresponse()
        body = response.read()
        return response.status == 200 and response.getheader("Docker-Content-Digest") == expected_digest and "sha256:" + digest(body) == expected_digest
    finally:
        connection.close()


def candidate_markers(path: Path) -> dict[str, bytes]:
    import io
    markers = {}
    with tarfile.open(path) as archive:
        member = archive.extractfile("manifest.json")
        if member is None:
            raise RuntimeError("candidate_archive_manifest")
        manifests = json.load(member)
        if len(manifests) != 1:
            raise RuntimeError("candidate_archive_ambiguous")
        for layer in manifests[0]["Layers"]:
            stream = archive.extractfile(layer)
            if stream is None:
                raise RuntimeError("candidate_archive_layer")
            with tarfile.open(fileobj=io.BytesIO(stream.read()), mode="r:*") as inner:
                for entry in inner:
                    name = entry.name.removeprefix("./").removeprefix("/")
                    if name in ("node", "golang", "alpine", "postgres") and entry.isfile():
                        value = inner.extractfile(entry)
                        if value is not None:
                            markers[name] = value.read()
    return markers


def scenario(root: Path, data: Any, name: str) -> dict[str, Any]:
    from oci_registry_fixture import Registry, ROLES
    directory = root / "scenarios" / name
    directory.mkdir(mode=0o700)
    context = directory / "context"
    context.mkdir(mode=0o700)
    dockerfile = context / "Dockerfile"
    dockerfile.write_bytes((root / "Original.Dockerfile").read_bytes() if name == "original_success" else tiny_dockerfile().encode())
    proof_dir = directory / "proof"
    proof_dir.mkdir(mode=0o700)
    result: dict[str, Any] = {"scenario": name, "status": "failed", "candidate_invoked": False,
                              "application_compile": "not_checked", "fault_observed": False}
    if name == "metadata_only_no_blobs":
        data = copy.deepcopy(data)
        data.add_metadata_only_branches()
    registry = Registry(data, name, root / "registry.crt", root / "registry.key")
    registry.start()
    daemon = None
    second = None
    freeze_offset = 0
    try:
        daemon = PrivateDaemon(directory / "daemon", root / "registry.crt")
        daemon.ready()
        env = daemon.env
        if name == "tag_head_502":
            result["index_get200_verified"] = control_tag_get(registry, "golang", data.tags["golang"])
            if not result["index_get200_verified"]:
                raise RuntimeError("control_index_get")
        prelude = shell_prelude(root, registry.address)
        if name == "metadata_only_no_blobs":
            control = execute(["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}",
                               registry.address + "/fixture/golang:v1"], env=env, timeout=90,
                              log=directory / "formatted-negative-control.log")
            result["formatted_control_failed"] = control.returncode != 0
            result["formatted_control_blob_404"] = any(row["resource"] == "blob" and row["status"] == 404 for row in registry.trace)
            metadata_offset = len(registry.trace)
            commands = ["vm_build_lock_role " + shlex.quote(str(proof_dir)) + " " + role + " " +
                        shlex.quote(registry.address + "/fixture/" + role + ":v1") for role in (*ROLES, "frontend")]
            metadata = execute(["bash", "-c", prelude + "\n".join(commands)], env=env, timeout=180,
                               log=directory / "metadata-wrapper.log")
            result["metadata_process_exit"] = metadata.returncode
            result["metadata_blob_request_count"] = sum(row["resource"] == "blob" for row in registry.trace[metadata_offset:])
            result["metadata_locked_count"] = sum((proof_dir / (role + "-lock.json")).is_file() for role in (*ROLES, "frontend"))
            result["metadata_amd64_locks_verified"] = all((proof_dir / (role + "-lock.json")).is_file() and
                json.loads((proof_dir / (role + "-lock.json")).read_text())["manifest_digest"] == data.role_data[role]["manifest"]
                for role in (*ROLES, "frontend"))
            allowed_manifests = set(data.tags.values()) | {data.role_data[role]["manifest"] for role in (*ROLES, "frontend")}
            result["metadata_unselected_manifest_request_count"] = sum(row["resource"] == "manifest_digest" and
                row["digest"] not in allowed_manifests for row in registry.trace[metadata_offset:])
            result["no_solve_or_proof"] = not (proof_dir / "proof.json").exists() and not (proof_dir / "solve.log").exists()
            result["fault_observed"] = result["formatted_control_failed"] and result["formatted_control_blob_404"]
            result["status"] = "pass" if (result["fault_observed"] and metadata.returncode == 0 and
                result["metadata_blob_request_count"] == 0 and result["metadata_locked_count"] == 5 and result["metadata_amd64_locks_verified"] and
                result["metadata_unselected_manifest_request_count"] == 0 and result["no_solve_or_proof"]) else "failed"
            return result
        alteration = "VM_BUILD_BUILDER=fixture-unsupported\n" if name == "unsupported_builder" else "VM_BUILD_PLATFORM=linux/arm64\n" if name == "unsupported_platform" else ""
        output = directory / "preflight-result"
        script = prelude + alteration + "\n".join([
            "if vm_build_preflight " + shlex.quote(str(proof_dir)) + " " + RELEASE + " " + COMMIT + " " + shlex.quote(str(dockerfile)) + "; then",
            "printf 'preflight=pass\\n' > " + shlex.quote(str(output)), "else",
            "printf 'preflight=fail\\n' > " + shlex.quote(str(output)), "fi",
            "printf 'failure_code=%s\\nverified_count=%s\\n' \"$VM_BUILD_FAILURE_CODE\" \"$VM_BUILD_DEPENDENCY_VERIFIED_COUNT\" >> " + shlex.quote(str(output)),
        ])
        build = execute(["bash", "-c", script], env=env, timeout=180, log=directory / "preflight-wrapper.log")
        result["preflight_process_exit"] = build.returncode
        if build.returncode or not output.exists():
            raise RuntimeError("preflight_wrapper")
        values = parse_result(output)
        result.update(preflight=values["preflight"], failure_code=values["failure_code"], verified_count=int(values["verified_count"]))
        proof = proof_dir / "proof.json"
        if values["preflight"] == "pass":
            doc = json.loads(proof.read_text())
            result.update(proof_sha256=digest(proof.read_bytes()), helper_sha256=doc["helper_sha256"],
                          dockerfile_sha256=doc["dockerfile_sha256"], builder=doc["builder"], platform=doc["platform"],
                          dependency_solve_count=doc["dependency_solve_count"],
                          role_manifest_digests={role:doc["roles"][role]["manifest_digest"] for role in (*ROLES, "frontend")})
            result["materialized_markers_verified"] = all((proof_dir / "materialized" / role).read_bytes() == data.role_data[role]["marker"] for role in ROLES)
            result["all_base_layers_read"] = all(any(row["method"] == "GET" and row["resource"] == "blob" and row["status"] == 200 and row["digest"] == data.role_data[role]["layer"] and row["byte_count"] == len(data.blobs[data.role_data[role]["layer"]]) for row in registry.trace) for role in ROLES)
            result["frontend_layers_read"] = all(any(row["method"] == "GET" and row["resource"] == "blob" and row["status"] == 200 and row["digest"] == layer and row["byte_count"] == len(data.blobs[layer]) for row in registry.trace) for layer in data.role_data["frontend"]["layers"])
            registry.frozen = True
            if name == "tag_drift":
                result["tag_drift_to_b_verified"] = control_tag_get(registry, "golang", data.alternate_tags["golang"])
                if not result["tag_drift_to_b_verified"]:
                    raise RuntimeError("control_tag_drift")
            freeze_offset = len(registry.trace)
            alteration = ""
            if name == "builder_drift":
                alteration = "VM_BUILD_BUILDER=fixture-drift\n"
            elif name == "platform_drift":
                alteration = "VM_BUILD_PLATFORM=linux/arm64\n"
            elif name == "context_drift":
                created = execute(["docker", "context", "create", "fixture-drift", "--docker", "host=" + env["DOCKER_HOST"]], env=env, log=directory / "context-create.log")
                if created.returncode:
                    raise RuntimeError("context_create")
                env = dict(env, DOCKER_CONTEXT="fixture-drift")
            elif name == "daemon_drift":
                second = PrivateDaemon(directory / "second-daemon", root / "registry.crt")
                second.ready()
                env = second.env
            output2 = directory / "candidate-result"
            use = "vm_build_use_proof " + shlex.quote(str(proof)) + " " + result["proof_sha256"] + " " + RELEASE + " " + COMMIT + " " + shlex.quote(str(dockerfile))
            script = prelude + alteration + "cd " + shlex.quote(str(context)) + "\n"
            script += "if " + use + "; then\nprintf 'proof_use=pass\\n' > " + shlex.quote(str(output2)) + "\n"
            if name == "original_success":
                script += "printf 'candidate=not_applicable\\n' >> " + shlex.quote(str(output2)) + "\n"
            else:
                tag = "fixture-candidate:" + name.replace("_", "-")
                command = "vm_build_candidate " + shlex.quote(str(directory / "candidate.log")) + " --build-arg COMMIT=" + COMMIT + " --build-arg VERSION=fixture --build-arg DATE=2026-10-19T00:00:00Z -t " + tag + " ."
                script += "if " + command + "; then\nprintf 'candidate=pass\\n' >> " + shlex.quote(str(output2)) + "\nelse\nprintf 'candidate=fail\\n' >> " + shlex.quote(str(output2)) + "\nfi\n"
            script += "else\nprintf 'proof_use=fail\\ncandidate=blocked\\n' > " + shlex.quote(str(output2)) + "\nfi\nprintf 'failure_code=%s\\n' \"$VM_BUILD_FAILURE_CODE\" >> " + shlex.quote(str(output2)) + "\n"
            candidate = execute(["bash", "-c", script], env=env, timeout=180, log=directory / "candidate-wrapper.log")
            result["candidate_process_exit"] = candidate.returncode
            if candidate.returncode or not output2.exists():
                raise RuntimeError("candidate_wrapper")
            values2 = parse_result(output2)
            result.update(proof_use=values2["proof_use"], candidate=values2["candidate"], candidate_failure_code=values2["failure_code"])
            result["candidate_invoked"] = values2["candidate"] in ("pass", "fail")
            result["tag_requests_after_freeze"] = sum(row["resource"] == "manifest_tag" for row in registry.trace[freeze_offset:])
            if values2["candidate"] == "pass":
                saved = execute(["docker", "image", "save", "-o", str(directory / "candidate.tar"), tag], env=env, log=directory / "candidate-save.log")
                if saved.returncode:
                    raise RuntimeError("candidate_save")
                markers = candidate_markers(directory / "candidate.tar")
                result["candidate_markers_verified"] = set(markers) == set(ROLES) and all(markers[role] == data.role_data[role]["marker"] for role in ROLES)
                (directory / "candidate.tar").unlink()
        else:
            result["candidate"] = "blocked"
        trace = registry.trace
        if name in ("original_success", "success", "tag_drift"):
            expected_candidate = "not_applicable" if name == "original_success" else "pass"
            passed = result.get("preflight") == "pass" and result.get("candidate") == expected_candidate and result.get("materialized_markers_verified") and result.get("all_base_layers_read") and result.get("frontend_layers_read") and result.get("tag_requests_after_freeze") == 0
            if name != "original_success":
                passed = passed and result.get("candidate_markers_verified")
            result["fault_observed"] = name != "tag_drift" or result.get("tag_drift_to_b_verified", False)
            passed = passed and result["fault_observed"]
        elif name.endswith("_drift"):
            passed = result.get("preflight") == "pass" and result.get("proof_use") == "fail" and result.get("candidate") == "blocked"
            result["fault_observed"] = passed
        else:
            target = data.role_data["golang"]
            observed = {
                "tag_head_502": result.get("index_get200_verified", False) and any(row["method"] == "HEAD" and row["resource"] == "manifest_tag" and row["status"] == 502 for row in trace),
                "frontend_missing": any(row["resource"].startswith("manifest") and row["status"] == 404 for row in trace),
                "wrong_digest": any(row["method"] == "GET" and row["digest"] == target["manifest"] for row in trace),
                "config_missing": any(row["digest"] == target["config"] and row["status"] == 404 for row in trace),
                "layer_truncated": any(row["method"] == "GET" and row["digest"] == target["layer"] and 0 < row["byte_count"] < len(data.blobs[target["layer"]]) for row in trace),
                "manifest_missing": any(row["resource"] == "manifest_digest" and row["status"] == 404 for row in trace),
                "auth_error": any(row["status"] == 401 for row in trace),
                "unsupported_builder": True, "unsupported_platform": True,
            }.get(name, False)
            result["fault_observed"] = observed
            passed = result.get("preflight") == "fail" and result.get("candidate") == "blocked" and observed
        result["status"] = "pass" if passed else "failed"
    except Exception as error:
        result["error_type"] = type(error).__name__
        if isinstance(error, RuntimeError):
            result["reason"] = str(error)
    finally:
        second_stopped = second.close() if second else True
        first_stopped = daemon.close() if daemon else True
        result["private_daemon_stopped"] = second_stopped and first_stopped
        registry.close()
        registry.write_trace(directory / "registry-trace.jsonl")
        result["registry_request_count"] = len(registry.trace)
        result["registry_trace_sha256"] = digest((directory / "registry-trace.jsonl").read_bytes())
        (directory / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def remote_inner(root: Path, names: list[str]) -> dict[str, Any]:
    from oci_registry_fixture import FixtureData
    if execute(["mount", "--make-rprivate", "/"]).returncode or execute(["ip", "link", "set", "lo", "up"]).returncode:
        raise RuntimeError("namespace_setup")
    isolated = os.readlink("/proc/self/ns/net") != (root / "host-netns").read_text() and not execute(["ip", "route", "show", "default"]).stdout.strip()
    if not isolated:
        raise RuntimeError("network_isolation")
    (root / "scenarios").mkdir(mode=0o700)
    certificate = execute(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(root / "registry.key"),
                           "-out", str(root / "registry.crt"), "-days", "1", "-subj", "/CN=offline-build-fixture",
                           "-addext", "subjectAltName=IP:127.0.0.1"], log=root / "certificate.log")
    if certificate.returncode:
        raise RuntimeError("fixture_certificate")
    data = FixtureData(root / "frontend.tar")
    results = []
    for name in names:
        results.append(scenario(root, data, name))
        (root / "progress.json").write_text(json.dumps({"completed": len(results), "total": len(names), "last_scenario": name, "last_status": results[-1]["status"]}))
        if results[-1]["status"] != "pass":
            break
    projection = {"network_isolated": bool(isolated), "scenarios": results,
                  "status": "pass" if len(results) == len(names) and all(row["status"] == "pass" for row in results) else "failed"}
    (root / "inner-result.json").write_text(json.dumps(projection))
    return projection


def remote_host(root: Path, names: list[str]) -> dict[str, Any]:
    os.umask(0o077)
    before = host_snapshot()
    result: dict[str, Any] = {"schema": 1, "checked_at": int(time.time()), "host_snapshot_before": before,
                              "requested_scenarios": names, "status": "blocked", "application_compile": "not_checked"}
    try:
        if shutil.disk_usage(root).free < 8 * 1024 ** 3:
            raise RuntimeError("fixture_space")
        if any(shutil.which(name) is None for name in ("unshare", "dockerd", "docker", "ip", "mount", "timeout", "python3", "jq", "openssl")):
            raise RuntimeError("missing_tool")
        saved = execute(["docker", "image", "save", "-o", str(root / "frontend.tar"), "docker/dockerfile:1.7"], timeout=90, log=root / "frontend-save.log")
        if saved.returncode:
            raise RuntimeError("cached_frontend_export")
        os.chmod(root / "frontend.tar", 0o600)
        result["cached_frontend_archive_sha256"] = digest((root / "frontend.tar").read_bytes())
        (root / "host-netns").write_text(os.readlink("/proc/self/ns/net"))
        child = execute(["timeout", "--kill-after=10s", "1800s", "unshare", "--mount", "--net", "--pid", "--fork", "--mount-proc", "python3", __file__, "--inner", str(root), *names], timeout=1815, log=root / "namespace.log")
        result["namespace_exit_code"] = child.returncode
        if (root / "inner-result.json").exists():
            result.update(json.loads((root / "inner-result.json").read_text()))
        else:
            raise RuntimeError("namespace_run")
    except Exception as error:
        result["error_type"] = type(error).__name__
        if isinstance(error, RuntimeError):
            result["reason"] = str(error)
    finally:
        for name in ("registry.key", "frontend.tar"):
            target = root / name
            if target.exists():
                if target.is_symlink() or target.resolve().parent != root:
                    raise RuntimeError("fixture_material_cleanup_path")
                target.unlink()
        result["temporary_key_destroyed"] = not (root / "registry.key").exists()
        result["cached_export_destroyed"] = not (root / "frontend.tar").exists()
        after = host_snapshot()
        result["host_unchanged"] = before == after
        if not result["host_unchanged"]:
            result["status"] = "failed"
        (root / "projection.json").write_text(json.dumps(result, indent=2) + "\n")
    return result


def run(runner: Any, remote_temps: list[str], names: list[str] | None = None) -> dict[str, Any]:
    names = list(names or SCENARIOS)
    if not names or any(name not in SCENARIOS for name in names) or len(set(names)) != len(names):
        raise ValueError("invalid_fixture_scenario")
    runner.release_id = None
    runner.deployment_mode = None
    runner.event_log_path = None
    remote = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "vm-build-real-fixture")
    remote_temps.append(remote)
    assets = {"runner.py": Path(__file__), "oci_registry_fixture.py": Path(__file__).with_name("oci_registry_fixture.py"),
              "helper.sh": DEPLOY_ROOT / "release/vm-build-common.sh", "Original.Dockerfile": DEPLOY_ROOT.parents[3] / "Dockerfile"}
    helper_bytes = assets["helper.sh"].read_bytes()
    for name, path in assets.items():
        if name == "helper.sh":
            runner.upload("local_vm", helper_bytes, remote + "/" + name, 0o700)
        else:
            runner.upload_file("local_vm", path, remote + "/" + name, 0o700 if name.endswith((".py", ".sh")) else 0o600)
    command = "python3 " + shlex.quote(remote + "/runner.py") + " --remote " + shlex.quote(remote) + " " + " ".join(names)
    values = runner.run("local_vm", command, {"projection"}, timeout=1830).values
    result = json.loads(values["projection"])
    result["local_helper_sha256"] = digest(helper_bytes)
    result["helper_unchanged_during_run"] = helper_bytes == (DEPLOY_ROOT / "release/vm-build-common.sh").read_bytes()
    result["fixture_source_sha256"] = digest(Path(__file__).read_bytes())
    result["registry_source_sha256"] = digest(Path(__file__).with_name("oci_registry_fixture.py").read_bytes())
    result["remote_artifacts_retained"] = True
    return result


def main() -> int:
    if len(sys.argv) > 2 and sys.argv[1] in ("--remote", "--inner"):
        root = Path(sys.argv[2]).resolve()
        if not root.is_dir() or root.is_symlink() or root.parent != Path("/opt/sub2api-deploy/release-input") or not root.name.startswith("vm-build-real-fixture."):
            return 2
        os.umask(0o077)
        names = sys.argv[3:]
        if not names or any(name not in SCENARIOS for name in names):
            return 2
        if sys.argv[1] == "--inner":
            try:
                remote_inner(root, names)
            except Exception as error:
                result = {"status": "blocked", "error_type": type(error).__name__}
                if isinstance(error, RuntimeError):
                    result["reason"] = str(error)
                (root / "inner-result.json").write_text(json.dumps(result))
        else:
            print("projection=" + json.dumps(remote_host(root, names), separators=(",", ":")))
        return 0
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scenario", action="append", choices=SCENARIOS)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    sys.path.insert(0, str(DEPLOY_ROOT))
    from release.ssh import SSHRunner
    try:
        result = run(SSHRunner(), [], args.scenario)
    except Exception as error:
        result = {"status": "blocked", "error_type": type(error).__name__}
    if args.output:
        args.output.write_text(json.dumps(result, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(result, indent=2))
    return 0 if result.get("status") == "pass" else 1


if __name__ == "__main__":
    sys.exit(main())
