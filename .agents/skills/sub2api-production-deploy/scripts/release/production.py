from __future__ import annotations

import argparse
import hashlib
import json
import shlex
import secrets
import time
from pathlib import Path

from .atomic import atomic_write, canonical_json
from .gate import verify_gate
from .manifest import sha256_file
from .migration_planner import HOOK_REGISTRY, plan_migrations
from .production_snapshot import decode_snapshot, snapshot_script, snapshot_sha256
from .paths import MAINTENANCE_ROOT, TRUSTED_VM_PUBLIC_KEY, UNIT_ROOT
from .profiles import get_profile, get_release_profile
from .ssh import SSHRunner


TRUSTED_KEY = TRUSTED_VM_PUBLIC_KEY
CANARY_FIELDS = {"route_health", "streaming", "curl_exit", "http_code", "canary_status"}
CANARY_RETRY_DELAYS = (5, 15)
# The backup upload endpoint acknowledges receipt before the artifact is
# guaranteed to be visible to the restricted promotion account.  Keep the
# retry window bounded, but long enough to cover that eventual-consistency
# window without retrying the whole release.
BACKUP_PROMOTION_RETRY_DELAYS = (5, 15, 30, 60, 120)
BACKUP_PROMOTION_STAGING_RETRY_DELAYS = (5, 15, 30)
BACKUP_RESULT_RECONCILE_RETRY_DELAYS = (2, 5, 10, 20)
BACKUP_GENERATION_UPLOAD_RETRY_DELAYS = (5, 15)


class BackupGenerationFailure(RuntimeError):
    def __init__(self, message: str, failure: dict[str, str]) -> None:
        super().__init__(message)
        self.failure = failure


BACKUP_FIELDS = {
    "artifact", "transport_artifact", "artifact_size", "artifact_sha256", "traffic_preserved",
    "redis_backup_mode", "no_restart_path_proven", "local_restore_point_ready",
}


def quoted_env(values: dict[str, str | int]) -> str:
    return " ".join(f"{key}={shlex.quote(str(value))}" for key, value in values.items())


def gate_consumption_probe_script(
    release_dir: str,
    release_id: str,
    image_id: str,
    active_claim: str = "/opt/sub2api/releases/.active-release",
    active_slot: str = "/opt/sub2api/active-app",
) -> str:
    consumed = f"{release_dir}/.consumed"
    recovered = f"{release_dir}/.recovered"
    return f"""active={shlex.quote(active_claim)}
consumed={shlex.quote(consumed)}
recovered={shlex.quote(recovered)}
slot={shlex.quote(active_slot)}
slot_container=$(sed -n 's/^container=//p' \"$slot\" 2>/dev/null || true)
slot_image=$(sed -n 's/^image_id=//p' \"$slot\" 2>/dev/null || true)
slot_release=$(sed -n 's/^release_id=//p' \"$slot\" 2>/dev/null || true)
slot_port=$(sed -n 's/^port=//p' \"$slot\" 2>/dev/null || true)
slot_instance=$(sed -n 's/^instance_id=//p' \"$slot\" 2>/dev/null || true)
expected_instance={shlex.quote(f'{release_id}-active')}
health_headers=$(mktemp /tmp/sub2api-consumed-probe.XXXXXX)
trap 'rm -f \"$health_headers\"' EXIT
managed_upstream=${{NGINX_MANAGED_UPSTREAM:-/etc/nginx/conf.d/sub2api-release-upstream.conf}}
slot_valid=false
if test -f \"$slot\" && test ! -L \"$slot\" && test \"$slot_container\" = sub2api && {{ test \"$slot_port\" = 18080 || test \"$slot_port\" = 18081; }} && test \"$slot_instance\" = \"$expected_instance\" && test \"$slot_image\" = {shlex.quote(image_id)} && test \"$slot_release\" = {shlex.quote(release_id)} && test \"$(docker inspect -f '{{{{.Image}}}}' \"$slot_container\" 2>/dev/null)\" = {shlex.quote(image_id)} && test \"$(docker inspect -f '{{{{.State.Health.Status}}}}' \"$slot_container\" 2>/dev/null)\" = healthy && curl -sS -D \"$health_headers\" -o /dev/null \"http://127.0.0.1:$slot_port/health\" && grep -Eiq \"^x-sub2api-instance:[[:space:]]*$expected_instance\\r?$\" \"$health_headers\" && grep -Eiq '^x-sub2api-background-ready:[[:space:]]*true\\r?$' \"$health_headers\" && grep -Fq \"server 127.0.0.1:$slot_port;\" \"$managed_upstream\" && test \"$(systemctl is-active nginx 2>/dev/null)\" = active; then
  slot_valid=true
fi
compose_valid=false
if test -d \"$consumed\" && test ! -L \"$consumed\" && (export ACTIVE_CLAIM=\"$consumed\" RELEASE_DIR={shlex.quote(release_dir)}; source \"$consumed/assets/context.sh\"; assert_final_compose_closure \"${{DEPLOY_DIR:-/opt/sub2api}}\" \"$slot_port\") >/dev/null 2>&1; then
  compose_valid=true
fi
if test -d \"$consumed\" && test ! -L \"$consumed\" && test -f \"$consumed/marker\" && test ! -L \"$consumed/marker\" && test ! -e \"$recovered\" && test ! -L \"$recovered\" && test ! -e \"$active\" && test ! -L \"$active\" && grep -Fxq {shlex.quote(f'release_id={release_id}')} \"$consumed/marker\" && grep -Fxq {shlex.quote(f'candidate_image_id={image_id}')} \"$consumed/marker\" && test \"$slot_valid\" = true && test \"$compose_valid\" = true && test \"$(systemctl is-enabled sub2api-backup.timer 2>/dev/null)\" = enabled; then
  printf 'gate_consumed=true\\n'
elif test ! -e \"$consumed\" && test ! -L \"$consumed\" && test ! -e \"$recovered\" && test ! -L \"$recovered\" && test -d \"$active\" && test ! -L \"$active\" && test -f \"$active/release_id\" && test ! -L \"$active/release_id\" && test -f \"$active/gate.json\" && test ! -L \"$active/gate.json\" && test -f \"$active/CLAIM_SHA256SUMS\" && test ! -L \"$active/CLAIM_SHA256SUMS\" && grep -Fxq {shlex.quote(f'release_id={release_id}')} \"$active/release_id\" && (cd \"$active\" && sha256sum -c CLAIM_SHA256SUMS >/dev/null 2>&1) && test \"$(jq -er '.manifest.release_id' \"$active/gate.json\" 2>/dev/null)\" = {shlex.quote(release_id)} && test \"$(jq -er '.evidence.candidate_image_id' \"$active/gate.json\" 2>/dev/null)\" = {shlex.quote(image_id)}; then
  printf 'gate_consumed=false\\n'
else
  printf 'gate_consumed=unknown\\n'
fi"""


def emit_progress(message: str) -> None:
    try:
        print(message, flush=True)
    except BrokenPipeError:
        pass


class ProductionRelease:
    def __init__(self, gate_dir: Path, profile_name: str) -> None:
        self.gate_dir = gate_dir
        self.profile = get_release_profile(profile_name)
        self.document = verify_gate(gate_dir, TRUSTED_KEY, profile_name, accepted_schemas=frozenset({2}))
        self.manifest = self.document["manifest"]
        self.evidence = self.document["evidence"]
        self.migration_plan: dict[str, object] | None = None
        self.deployment_mode = str(self.manifest.get("deployment_mode", ""))
        if self.deployment_mode not in {"blue-green", "downtime"}:
            raise RuntimeError("signed Gate deployment mode is invalid")
        self.release_id = self.manifest["release_id"]
        self.image_id = self.evidence["candidate_image_id"]
        self.commit = self.manifest["commit_sha"]
        self.tag = f"sub2api:baiyu-{self.profile['version']}-{self.commit}"
        self.release_dir = f"/opt/sub2api/releases/{self.release_id}"
        self.active_assets = "/opt/sub2api/releases/.active-release/assets"
        self.state_dir = f"/opt/sub2api/backups/release-state/{self.release_id}"
        self.runner = SSHRunner()
        self.migration_started = False
        self.frozen = False
        self.units_masked = False
        self.claimed = False
        self.public_exposed = False
        self.route_switched = False
        self.route_switch_attempted = False
        self.mask_intent = False
        self.backup_values: dict[str, str] | None = None
        self._remote_raw_logging_ready = False
        self._remote_log_sequence = 0
        self.migration_status: str | None = None
        self.migration_195_status: str | None = None
        self.migration_196_status: str | None = None
        self.migration_197_status: str | None = None
        self.migration_198_status: str | None = None
        self.migration_199_status: str | None = None
        self.migration_200_status: str | None = None
        self.migration_201_status: str | None = None
        self.migration_202_status: str | None = None
        self.migration_203_status: str | None = None
        self.migration_204_status: str | None = None
        self.migration_205_status: str | None = None
        self.migration_206_status: str | None = None
        self.migration_208_status: str | None = None
        self.migration_209_status: str | None = None
        self.migration_211_status: str | None = None
        self.migration_212_status: str | None = None
        self.migration_214_status: str | None = None
        self.migration_215_status: str | None = None
        self.migration_216_status: str | None = None
        self.migration_217_status: str | None = None
        self.migration_218_status: str | None = None
        self.migration_219_status: str | None = None
        self.migration_220_status: str | None = None
        self.migration_221_status: str | None = None
        self.migration_222_status: str | None = None
        self.migration_223_status: str | None = None
        self.migration_224_status: str | None = None
        self.migration_225_status: str | None = None
        self.migration_226_status: str | None = None
        self.migration_227_status: str | None = None
        self.migration_228_status: str | None = None
        self.migration_229_status: str | None = None
        self.migration_230_status: str | None = None
        self.migration_231_status: str | None = None
        self.migration_232_status: str | None = None
        self.migration_233_status: str | None = None
        self.migration_234_status: str | None = None
        self.migration_235_status: str | None = None
        self.migration_236_status: str | None = None
        self.migration_237_status: str | None = None
        self.migration_238_status: str | None = None
        self.migration_239_status: str | None = None
        self.migration_240_status: str | None = None
        self.migration_241_status: str | None = None
        self.migration_242_status: str | None = None
        self.migration_243_status: str | None = None
        self.migration_244_status: str | None = None
        self.migration_245_status: str | None = None
        self.result_path = gate_dir / "production-result.json"
        self.result: dict[str, object] = {
            "release_id": self.release_id,
            "deployment_mode": self.deployment_mode,
            "status": "running",
            "stage": "init",
            "history": [],
        }
        self._save_result()

    def _save_result(self) -> None:
        atomic_write(self.result_path, canonical_json(self.result) + b"\n", 0o600)

    def stage(self, name: str, evidence: dict[str, str] | None = None, timeout: int | None = None) -> None:
        now = int(time.time())
        self.result["stage"] = name
        event: dict[str, object] = {"stage": name, "at": now}
        if timeout is not None:
            event["deadline_at"] = now + timeout
        if evidence:
            event["evidence"] = evidence
        history = self.result["history"]
        assert isinstance(history, list)
        history.append(event)
        self._save_result()
        emit_progress(f"release_id={self.release_id} stage={name} status={self.result['status']}")

    def run_remote(self, host: str, script: str, allowed: set[str], timeout: int = 300) -> dict[str, str]:
        if host == "racknerd" and self._remote_raw_logging_ready:
            script = self._wrap_remote_logging(script)
        return self.runner.run(host, script, allowed, timeout=timeout).values

    def restore_backup_units(self, restore_env: str) -> dict[str, str]:
        """Restore backup units while retaining benign stderr in root-only logs."""

        stderr_file = f"{self.release_dir}/restore-backup-units.stderr"
        script = f"""set -Eeuo pipefail
err_file={shlex.quote(stderr_file)}
if test -e "$err_file"; then
  test -f "$err_file" && test ! -L "$err_file"
else
  install -m 600 /dev/null "$err_file"
fi
chmod 600 "$err_file"
{restore_env} {shlex.quote(self.active_assets)}/restore-backup-units.sh 2>>"$err_file"
printf 'backup_units_restored=true\\n'
"""
        return self.run_remote("racknerd", script, {"backup_units_restored"})

    def run_remote_with_input(self, host: str, script: str, allowed: set[str], data: bytes, timeout: int = 300) -> dict[str, str]:
        if host == "racknerd" and self._remote_raw_logging_ready:
            script = self._wrap_remote_logging(script)
        return self.runner.run_with_input(host, script, allowed, data, timeout=timeout).values

    def _wrap_remote_logging(self, script: str) -> str:
        self._remote_log_sequence += 1
        stage = str(self.result.get("stage", "unknown"))
        if not stage or any(character not in "abcdefghijklmnopqrstuvwxyz0123456789_" for character in stage):
            raise RuntimeError("release stage is not safe for remote log metadata")
        log_dir = f"{self.release_dir}/logs"
        log_file = f"{log_dir}/production.raw.log"
        sequence = self._remote_log_sequence
        return f"""set -Eeuo pipefail
umask 077
test -d {shlex.quote(self.release_dir)} && test ! -L {shlex.quote(self.release_dir)}
if test -e {shlex.quote(log_dir)}; then
  test -d {shlex.quote(log_dir)} && test ! -L {shlex.quote(log_dir)}
else
  install -d -m 700 {shlex.quote(log_dir)}
fi
test "$(stat -c '%U:%G:%a' {shlex.quote(log_dir)})" = root:root:700
touch {shlex.quote(log_file)}
test -f {shlex.quote(log_file)} && test ! -L {shlex.quote(log_file)}
chmod 600 {shlex.quote(log_file)}
test "$(stat -c '%U:%G:%a:%h' {shlex.quote(log_file)})" = root:root:600:1
stdout_tmp=$(mktemp {shlex.quote(log_dir + '/.stdout.XXXXXX')})
stderr_tmp=$(mktemp {shlex.quote(log_dir + '/.stderr.XXXXXX')})
cleanup_remote_log_capture() {{ rm -f "$stdout_tmp" "$stderr_tmp"; }}
trap cleanup_remote_log_capture EXIT
set +e
SUB2API_RELEASE_RAW_LOG={shlex.quote(log_file)} bash -lc {shlex.quote(script)} >"$stdout_tmp" 2>"$stderr_tmp"
code=$?
set -e
if [[ $code -eq 0 && -s $stderr_tmp ]]; then
  code=97
fi
{{
  printf '\n[%s] sequence={sequence} stage={stage} stream=stdout\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  cat "$stdout_tmp"
  printf '\n[%s] sequence={sequence} stage={stage} stream=stderr exit=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$code"
  cat "$stderr_tmp"
}} >> {shlex.quote(log_file)}
cat "$stdout_tmp"
exit "$code"
"""

    def upload_assets(self) -> None:
        self.stage("stage_assets")
        trust_sha = sha256_file(TRUSTED_KEY)
        trust = self.run_remote(
            "racknerd",
            f"test -f /opt/sub2api-release-trust/vm-gate-ed25519.pub && test $(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}') = {trust_sha} && printf 'trust_key_verified=true\\n'",
            {"trust_key_verified"},
        )
        stage_dir = self.runner.create_temp_dir("racknerd", "/opt/sub2api/releases", "release-stage")
        files: dict[str, Path] = {
            "gate.json": self.gate_dir / "gate.json",
            "gate.sig": self.gate_dir / "gate.sig",
            "candidate.tar.gz": self.gate_dir / "candidate.tar.gz",
        }
        for path in sorted(MAINTENANCE_ROOT.glob("*")):
            if path.is_file():
                files[f"assets/{path.name}"] = path
        for name in ("mask-backup-units.sh", "restore-backup-units.sh"):
            files[f"assets/{name}"] = UNIT_ROOT / name
        self.run_remote("racknerd", f"install -d -m 700 {shlex.quote(stage_dir + '/assets')} && printf 'asset_directory_created=true\\n'", {"asset_directory_created"})
        checksum_lines: list[str] = []
        for relative, path in files.items():
            remote = f"{stage_dir}/{relative}"
            mode = 0o700 if path.suffix == ".sh" else 0o400
            self.runner.upload_file("racknerd", path, remote, mode)
            checksum_lines.append(f"{sha256_file(path)}  {relative}")
        checksum_document = ("\n".join(checksum_lines) + "\n").encode()
        self.runner.upload("racknerd", checksum_document, f"{stage_dir}/ASSET_SHA256SUMS", 0o400)
        self.run_remote(
            "racknerd",
            f"test ! -e {shlex.quote(self.release_dir)} && (cd {shlex.quote(stage_dir)} && sha256sum -c ASSET_SHA256SUMS >/dev/null) && mv -T -- {shlex.quote(stage_dir)} {shlex.quote(self.release_dir)} && printf 'release_directory_created=true\\n'",
            {"release_directory_created"},
        )
        self._remote_raw_logging_ready = True
        env = quoted_env({"RELEASE_ID": self.release_id, "RELEASE_DIR": self.release_dir})
        prepared = self.run_remote(
            "racknerd",
            f"{env} {self.release_dir}/assets/prepare.sh",
            {"prepared", "candidate_image_id", "candidate_archive_sha256", "trust_key_sha256"},
            timeout=1800,
        )
        if prepared["candidate_image_id"] != self.image_id or prepared["candidate_archive_sha256"] != self.evidence["candidate_archive_sha256"]:
            raise RuntimeError("RackNerd loaded candidate identity differs from signed Gate")
        self.claimed = True
        self.stage("stage_assets_verified", {"candidate_image_id": self.image_id, **trust})

    def preflight(self) -> None:
        if self.manifest.get("schema") == 2:
            self.preflight_v2()
            return
        self.stage("production_preflight")
        env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "MINIMUM_FREE_BYTES": self.profile["minimum_rack_free_bytes"],
            }
        )
        allowed = {
                "preflight",
                "pre_switch_image_id",
                "active_container",
                "active_port",
                "free_bytes",
                "migration_status",
                "migration_195_status",
                "migration_196_status",
                "migration_197_status",
                "migration_198_status",
                "migration_199_status",
                "migration_200_status",
                "migration_201_status",
                "migration_202_status",
                "migration_203_status",
                "migration_204_status",
                "migration_205_status",
                "migration_206_status",
                "migration_208_status",
                "migration_209_status",
                "migration_211_status",
                "migration_212_status",
                "migration_214_status",
                "migration_215_status",
                "migration_216_status",
                "migration_217_status",
                "migration_218_status",
                "migration_219_status",
                "migration_220_status",
                "migration_221_status",
                "migration_222_status",
                "migration_223_status",
                "migration_224_status",
                "migration_225_status",
                "migration_226_status",
                "migration_227_status",
                "migration_228_status",
                "migration_229_status",
                "migration_230_status",
                "migration_231_status",
                "migration_232_status",
                "migration_233_status",
                "migration_234_status",
                "migration_235_status",
                "migration_236_status",
                "migration_237_status",
                "migration_238_status",
                "migration_239_status",
                "migration_240_status",
                "migration_241_status",
                "migration_242_status",
                "migration_243_status",
                "migration_244_status",
                "migration_245_status",
            }
        try:
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/preflight.sh",
                allowed,
            )
        except BaseException:
            try:
                failure = self.run_remote(
                    "racknerd",
                    f"test -f {self.release_dir}/preflight-failure && test ! -L {self.release_dir}/preflight-failure && "
                    f"test \"$(stat -c '%U:%G:%a:%h' {self.release_dir}/preflight-failure)\" = root:root:600:1 && "
                    f"phase=$(sed -n 's/^preflight_failure_phase=//p' {self.release_dir}/preflight-failure); "
                    f"line=$(sed -n 's/^preflight_failure_line=//p' {self.release_dir}/preflight-failure); "
                    "case \"$phase\" in identity|active_runtime|backup_contract|migration_contract|capacity|compose_contract|completed) ;; *) exit 1 ;; esac; "
                    "case \"$line\" in ''|*[!0-9]*) exit 1 ;; esac; "
                    "printf 'preflight_failure_phase=%s\\npreflight_failure_line=%s\\n' \"$phase\" \"$line\"",
                    {"preflight_failure_phase", "preflight_failure_line"},
                )
            except BaseException:
                failure = {"preflight_failure_phase": "unknown", "preflight_failure_line": "0"}
            self.stage("production_preflight_failed", failure)
            raise
        self.migration_status = values["migration_status"]
        self.migration_195_status = values["migration_195_status"]
        self.migration_196_status = values["migration_196_status"]
        self.migration_197_status = values["migration_197_status"]
        self.migration_198_status = values["migration_198_status"]
        self.migration_199_status = values["migration_199_status"]
        self.migration_200_status = values["migration_200_status"]
        self.migration_201_status = values["migration_201_status"]
        self.migration_202_status = values["migration_202_status"]
        self.migration_203_status = values["migration_203_status"]
        self.migration_204_status = values["migration_204_status"]
        self.migration_205_status = values["migration_205_status"]
        self.migration_206_status = values["migration_206_status"]
        self.migration_208_status = values["migration_208_status"]
        self.migration_209_status = values["migration_209_status"]
        self.migration_211_status = values["migration_211_status"]
        self.migration_212_status = values["migration_212_status"]
        self.migration_214_status = values["migration_214_status"]
        self.migration_215_status = values["migration_215_status"]
        self.migration_216_status = values["migration_216_status"]
        self.migration_217_status = values["migration_217_status"]
        self.migration_218_status = values["migration_218_status"]
        self.migration_219_status = values["migration_219_status"]
        self.migration_220_status = values["migration_220_status"]
        self.migration_221_status = values["migration_221_status"]
        self.migration_222_status = values["migration_222_status"]
        self.migration_223_status = values["migration_223_status"]
        self.migration_224_status = values["migration_224_status"]
        self.migration_225_status = values["migration_225_status"]
        self.migration_226_status = values["migration_226_status"]
        self.migration_227_status = values["migration_227_status"]
        self.migration_228_status = values["migration_228_status"]
        self.migration_229_status = values["migration_229_status"]
        self.migration_230_status = values["migration_230_status"]
        self.migration_231_status = values["migration_231_status"]
        self.migration_232_status = values["migration_232_status"]
        self.migration_233_status = values["migration_233_status"]
        self.migration_234_status = values["migration_234_status"]
        self.migration_235_status = values["migration_235_status"]
        self.migration_236_status = values["migration_236_status"]
        self.migration_237_status = values["migration_237_status"]
        self.migration_238_status = values["migration_238_status"]
        self.migration_239_status = values["migration_239_status"]
        self.migration_240_status = values["migration_240_status"]
        self.migration_241_status = values["migration_241_status"]
        self.migration_242_status = values["migration_242_status"]
        self.migration_243_status = values["migration_243_status"]
        self.migration_244_status = values["migration_244_status"]
        self.migration_245_status = values["migration_245_status"]
        self.stage("production_preflight_verified", values)

    def preflight_v2(self) -> None:
        self.stage("production_preflight_v2")
        values = self.run_remote("racknerd", snapshot_script(), {"snapshot_b64"})
        snapshot = decode_snapshot(values["snapshot_b64"])
        signed_image = self.evidence.get("production_current_image_id")
        if signed_image and snapshot.get("current_image_id") != signed_image:
            raise RuntimeError("production current image differs from signed Gate")
        if snapshot_sha256(snapshot) != self.evidence.get("production_snapshot_sha256"):
            raise RuntimeError("production snapshot differs from signed Gate")
        plan = plan_migrations(self.manifest["migration_catalog"], snapshot["schema_migrations"])
        if plan["conflicts"]:
            raise RuntimeError("production migration checksum conflict")
        if not plan["existing_checksums_verified"]:
            raise RuntimeError("production existing migration checksums are not verified")
        signed = self.evidence.get("migration_evidence", {}).get("pending", [])
        actual = [{"filename": item["filename"], "checksum": item["checksum"]} for item in plan["pending"]]
        expected = [{"filename": item.get("filename"), "checksum": item.get("checksum")} for item in signed]
        if actual != expected:
            raise RuntimeError("production pending migration plan differs from signed Gate")
        remote_env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "MINIMUM_FREE_BYTES": self.profile["minimum_rack_free_bytes"],
            }
        )
        remote_values = self.run_remote(
            "racknerd",
            f"{remote_env} {self.active_assets}/preflight.sh",
            {
                "preflight",
                "active_container",
                "active_port",
                "pre_switch_image_id",
                "free_bytes",
                "migration_status",
                "migration_pending_count",
            },
        )
        if remote_values.get("preflight") != "pass":
            raise RuntimeError("production v2 preflight failed")
        if remote_values.get("pre_switch_image_id") != snapshot.get("current_image_id"):
            raise RuntimeError("production current image changed during preflight")
        if remote_values.get("migration_pending_count") != str(len(actual)):
            raise RuntimeError("production pending count differs from signed Gate")
        if remote_values.get("migration_status") != ("verified" if not actual else "absent"):
            raise RuntimeError("production migration status differs from signed Gate")
        self.production_snapshot = snapshot
        self.migration_plan = plan
        self.migration_status = "pending" if actual else "verified"
        self.stage("production_preflight_v2_verified", {
            "production_current_image_id": snapshot.get("current_image_id", ""),
            "pending_count": str(len(actual)),
            "existing_count": str(len(plan["existing"])),
            "catalog_sha256": plan["catalog_sha256"],
        })

    def run_route_canary(
        self,
        host: str,
        script: str,
        route_name: str,
        route_ip: str,
        api_key: bytes,
        phase: str,
    ) -> dict[str, str]:
        last: dict[str, str] | None = None
        attempt_user_agents: list[str] = []
        for attempt in range(1, len(CANARY_RETRY_DELAYS) + 2):
            marker = f"{self.release_id}-{phase}-{route_name}-{attempt}-{secrets.token_hex(4)}"
            user_agent = f"sub2api-release-{marker}-{route_name}"
            attempt_user_agents.append(user_agent)
            env = quoted_env(
                {
                    "PUBLIC_DOMAIN": self.profile["public_domain"],
                    "ROUTE_IP": route_ip,
                    "ROUTE_NAME": route_name,
                    "MARKER": marker,
                }
            )
            values = self.run_remote_with_input(host, f"{env} {script}", CANARY_FIELDS, api_key + b"\n", timeout=180)
            last = values
            self.stage(
                f"{phase}_{route_name}_canary_attempt",
                {
                    "attempt": str(attempt),
                    "marker": marker,
                    "canary_status": values["canary_status"],
                    "curl_exit": values["curl_exit"],
                    "http_code": values["http_code"],
                    "route_health": values["route_health"],
                    "streaming": values["streaming"],
                },
            )
            if values["canary_status"] == "pass":
                return {
                    **values,
                    "marker": marker,
                    "user_agent": user_agent,
                    "attempt_user_agents": ",".join(attempt_user_agents),
                }
            if values["canary_status"] != "retryable":
                raise RuntimeError(f"{phase} {route_name} canary failed without retry")
            if attempt <= len(CANARY_RETRY_DELAYS):
                time.sleep(CANARY_RETRY_DELAYS[attempt - 1])
        assert last is not None
        raise RuntimeError(
            f"{phase} {route_name} canary exhausted retries "
            f"(curl_exit={last['curl_exit']}, http_code={last['http_code']})"
        )

    def verify_streaming_routes(self, phase: str) -> tuple[dict[str, str], dict[str, str]]:
        self.stage(f"{phase}_streaming_preflight", timeout=1500)
        canary_key = self.runner.read_canary_key()
        route_script = self.active_assets + "/route-canary.sh"
        direct = self.run_route_canary(
            "racknerd", route_script, "direct", self.profile["rack_public_ip"], canary_key, phase
        )
        backup_temp = self.runner.create_temp_dir("backup", "/srv/sub2api-backups", "route-canary")
        backup_script = f"{backup_temp}/route-canary.sh"
        self.runner.upload_file("backup", MAINTENANCE_ROOT / "route-canary.sh", backup_script, 0o700)
        try:
            dmit = self.run_route_canary(
                "backup", backup_script, "dmit", self.profile["dmit_public_ip"], canary_key, phase
            )
        finally:
            self.run_remote("backup", f"rm -rf {backup_temp} && printf 'cleanup=true\\n'", {"cleanup"})
        self.stage(
            f"{phase}_streaming_verified",
            {"direct_attempt": direct["marker"].rsplit("-", 2)[-2], "dmit_attempt": dmit["marker"].rsplit("-", 2)[-2]},
        )
        return direct, dmit

    def freeze(self) -> None:
        self.stage("freeze", timeout=2400)
        self.mask_intent = True
        freeze_env = quoted_env({"RELEASE_DIR": self.release_dir})
        values = self.run_remote(
            "racknerd",
            f"{freeze_env} {self.active_assets}/freeze-backup.sh",
            {
                "backup_units_masked", "traffic_preserved", "release_state_captured", "outbox_checkpoint",
                "state_dir", "pre_switch_image_id", "compose_sha256",
            },
            timeout=2400,
        )
        self.frozen = True
        self.units_masked = True
        if values["state_dir"] != self.state_dir:
            raise RuntimeError("freeze state directory differs from release state")
        if values.get("traffic_preserved") != "true":
            raise RuntimeError("freeze did not preserve production traffic")
        self.stage("release_state_captured", values)

    def _generate_backup(self, attempt: int) -> dict[str, str]:
        attempt_id = f"{self.release_id}-{attempt}"
        backup_env = quoted_env({"RELEASE_DIR": self.release_dir, "BACKUP_ATTEMPT_ID": attempt_id})
        try:
            return self.run_remote(
                "racknerd",
                f"RELEASE_LOCK_HELD=false {backup_env} {self.active_assets}/backup.sh",
                BACKUP_FIELDS,
                timeout=2400,
            )
        except BaseException as backup_error:
            reconciled = False
            for delay in (0, *BACKUP_RESULT_RECONCILE_RETRY_DELAYS):
                if delay:
                    time.sleep(delay)
                try:
                    values = self.run_remote(
                        "racknerd",
                        f"set -Eeuo pipefail; state={shlex.quote(self.state_dir)}; "
                        "test -f \"$state/backup-result\" && test ! -L \"$state/backup-result\" && "
                        "test -f \"$state/backup-result.sha256\" && test ! -L \"$state/backup-result.sha256\" && "
                        "test \"$(stat -c '%U:%G:%a' \"$state/backup-result\")\" = root:root:400 && "
                        "test \"$(stat -c '%U:%G:%a' \"$state/backup-result.sha256\")\" = root:root:400 && "
                        "(cd \"$state\" && sha256sum -c backup-result.sha256 >/dev/null) && "
                        "test \"$(grep -c '^[a-z_][a-z0-9_]*=' \"$state/backup-result\")\" = 8 && "
                        "cat \"$state/backup-result\"",
                        BACKUP_FIELDS,
                    )
                    self.stage("backup_result_reconciled", {"backup_result_reconciled": "true"})
                    reconciled = True
                    break
                except BaseException:
                    try:
                        failure = self.run_remote(
                            "racknerd",
                            f"set -Eeuo pipefail; state={shlex.quote(self.state_dir)}; "
                            "if test -f \"$state/backup-failure\" && test ! -L \"$state/backup-failure\" && "
                            f"test \"$(sed -n 's/^attempt_id=//p' \"$state/backup-failure\")\" = {shlex.quote(attempt_id)}; then "
                            "stage=$(sed -n 's/^stage=//p' \"$state/backup-failure\"); "
                            "code=$(sed -n 's/^exit_code=//p' \"$state/backup-failure\"); "
                            "printf 'backup_failure_stage=%s\\nbackup_failure_exit_code=%s\\n' \"$stage\" \"$code\"; "
                            "else printf 'backup_failure_stage=absent\\nbackup_failure_exit_code=absent\\n'; fi",
                            {"backup_failure_stage", "backup_failure_exit_code"},
                        )
                    except BaseException:
                        continue
                    if failure["backup_failure_stage"] != "absent":
                        raise BackupGenerationFailure(
                            f"production backup failed at stage={failure['backup_failure_stage']} "
                            f"exit_code={failure['backup_failure_exit_code']}",
                            failure,
                        ) from backup_error
            if not reconciled:
                raise backup_error
        return values

    def backup(self) -> None:
        self.stage("backup", timeout=600)
        values: dict[str, str] | None = None
        generation_delays = (0, *BACKUP_GENERATION_UPLOAD_RETRY_DELAYS)
        for attempt, delay in enumerate(generation_delays, start=1):
            if delay:
                time.sleep(delay)
            try:
                values = self._generate_backup(attempt)
                break
            except BackupGenerationFailure as error:
                failure = error.failure
                evidence = {
                    "backup_failure_stage": str(failure.get("backup_failure_stage", "unknown")),
                    "backup_failure_exit_code": str(failure.get("backup_failure_exit_code", "unknown")),
                    "backup_generation_attempt": str(attempt),
                }
                self.stage("backup_generation_failed", evidence)
                if evidence["backup_failure_stage"] != "upload" or attempt == len(generation_delays):
                    raise
        if values is None:
            raise RuntimeError("production backup did not produce a result")
        self.backup_values = values
        if values.get("local_restore_point_ready") != "true":
            raise RuntimeError("local coordinated restore point is not ready")
        promotion_script = MAINTENANCE_ROOT / "promote-backup.sh"
        temp_dir: str | None = None
        staging_error: BaseException | None = None
        for delay in (0, *BACKUP_PROMOTION_STAGING_RETRY_DELAYS):
            if delay:
                time.sleep(delay)
            try:
                if temp_dir is None:
                    temp_dir = self.runner.create_temp_dir("backup", "/srv/sub2api-backups", "release-promote")
                self.runner.upload_file("backup", promotion_script, f"{temp_dir}/promote-backup.sh", 0o700)
                staging_error = None
                break
            except BaseException as error:
                staging_error = error
        if staging_error is not None or temp_dir is None:
            assert staging_error is not None
            raise staging_error
        remote = f"{temp_dir}/promote-backup.sh"
        try:
            promote_env = quoted_env(
                {
                    "RELEASE_ID": self.release_id,
                    "TRANSPORT_ARTIFACT_NAME": values["transport_artifact"],
                    "ARTIFACT_SHA256": values["artifact_sha256"],
                    "MINIMUM_FREE_BYTES": self.profile["minimum_backup_free_bytes"],
                }
            )
            promoted = None
            last_error: BaseException | None = None
            for attempt, delay in enumerate((0, *BACKUP_PROMOTION_RETRY_DELAYS), start=1):
                if delay:
                    time.sleep(delay)
                try:
                    promoted = self.run_remote(
                        "backup",
                        f"{promote_env} {remote}",
                        {"backup_promotion", "release_artifact", "release_sha256", "release_free_bytes"},
                        timeout=600,
                    )
                    break
                except BaseException as error:
                    last_error = error
            if promoted is None:
                assert last_error is not None
                raise last_error
        finally:
            self.run_remote("backup", f"rm -rf {temp_dir} && printf 'cleanup=true\\n'", {"cleanup"})
        if promoted["release_sha256"] != values["artifact_sha256"]:
            raise RuntimeError("promoted recovery point checksum differs from RackNerd")
        self.stage("backup_verified", {**values, **promoted})

    def migration_preflight(self) -> None:
        if self.manifest.get("schema") == 2:
            self.migration_preflight_v2()
            return
        if self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            return
        assertion_context = f"{self.state_dir}/migration-preflight-context.sh"
        profile_name = self.profile["name"]
        context_values = self.run_remote(
            "racknerd",
            f"set -Eeuo pipefail; state={shlex.quote(self.state_dir)}; "
            "test -d \"$state\" && test ! -L \"$state\"; "
            f"tmp=\"$state/.migration-preflight-context.tmp.$$\"; "
            f"printf 'profile=%q\\nrelease_profile=%q\\nstate_dir=%q\\n' "
            f"{shlex.quote(profile_name)} {shlex.quote(profile_name)} \"$state\" >\"$tmp\"; "
            "chmod 600 \"$tmp\"; mv -T -- \"$tmp\" \"$state/migration-preflight-context.sh\"; "
            "test \"$(stat -c '%U:%G:%a:%h' \"$state/migration-preflight-context.sh\")\" = root:root:600:1; "
            "printf 'migration_preflight_context=verified\\n'",
            {"migration_preflight_context"},
        )
        self.stage("migration_preflight_context_verified", context_values)

        def assertion_env(status: str) -> str:
            return quoted_env(
                {
                    "RELEASE_DIR": self.release_dir,
                    "MIGRATION_STATUS": status,
                    "ASSERT_CONTEXT_FILE": assertion_context,
                }
            )

        self.stage("migration_195_preflight")
        if self.migration_195_status not in {"absent", "verified"}:
            raise RuntimeError("migration 195 preflight status is unknown")
        env = assertion_env(self.migration_195_status)
        try:
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-195-assert.sh preflight",
                {
                    "migration_195_affected", "migration_195_recomputed", "migration_195_preserved",
                    "migration_195_skipped", "migration_195_unproven", "migration_195_conflict",
                    "migration_195_unexpected", "migration_195_data_plan_sha256",
                },
            )
        except BaseException:
            try:
                failure = self.run_remote(
                    "racknerd",
                    f"file={shlex.quote(self.state_dir + '/migration-195-failure')}; "
                    "if test -f \"$file\" && test ! -L \"$file\" && "
                    "test \"$(stat -c '%U:%G:%a:%h' \"$file\")\" = root:root:600:1; then "
                    "phase=$(sed -n 's/^migration_195_failure_phase=//p' \"$file\"); "
                    "code=$(sed -n 's/^migration_195_failure_code=//p' \"$file\"); "
                    "else phase=preflight; code=context_or_state; fi; "
                    "case \"$phase\" in preflight|bind|postflight_db|postflight_runtime) ;; *) exit 1 ;; esac; "
                    "case \"$code\" in config_file|timezone_value|unproven_rate|account_binding_mismatch|outbox_query|outbox_value|outbox_watermark|data_plan_query|account_ids_query|data_plan_hash|account_binding_conflict|unexpected_data|recovery_point|data_plan_missing|plan_write|plan_missing|affected_missing|timezone_file|data_plan_mismatch|account_ids_file|account_ids_hash|status_file|status_value|outbox_baseline_file|source_rate_state|postflight_shape|context_or_state) ;; *) exit 1 ;; esac; "
                    "printf 'migration_195_failure_phase=%s\\nmigration_195_failure_code=%s\\n' \"$phase\" \"$code\"",
                    {"migration_195_failure_phase", "migration_195_failure_code"},
                )
            except BaseException:
                failure = {
                    "migration_195_failure_phase": "preflight",
                    "migration_195_failure_code": "unknown",
                }
            self.stage("migration_195_preflight_failed", failure)
            raise
        self.stage("migration_195_preflight_verified", values)
        if self.profile["name"] in {"232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            if self.migration_232_status not in {"absent", "verified"}:
                raise RuntimeError("migration 232 preflight status is unknown")
            self.stage("migration_232_preflight")
            env = assertion_env(self.migration_232_status)
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-232-assert.sh preflight",
                {
                    "migration_232_affected",
                    "migration_232_upstream_bound",
                    "migration_232_lcodex_bound",
                    "migration_232_data_plan_sha256",
                },
            )
            self.stage("migration_232_preflight_verified", values)
        if self.profile["name"] in {"233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            if self.migration_233_status not in {"absent", "verified"}:
                raise RuntimeError("migration 233 preflight status is unknown")
            self.stage("migration_233_preflight")
            env = assertion_env(self.migration_233_status)
            try:
                values = self.run_remote(
                    "racknerd",
                    f"{env} {self.active_assets}/migration-233-assert.sh preflight",
                    {
                        "migration_233_duplicate_keys",
                        "migration_233_index_verified",
                        "migration_233_table_state",
                        "migration_233_columns_verified",
                        "migration_233_health_index_verified",
                        "migration_233_privileges_verified",
                        "migration_233_trigger_verified",
                        "migration_233_preflight",
                    },
                )
            except BaseException:
                try:
                    failure = self.run_remote(
                        "racknerd",
                        f"test -f {self.release_dir}/migration-233-failure && test ! -L {self.release_dir}/migration-233-failure && "
                        f"test \"$(stat -c '%U:%G:%a:%h' {self.release_dir}/migration-233-failure)\" = root:root:600:1 && "
                        f"code=$(sed -n 's/^migration_233_failure_code=//p' {self.release_dir}/migration-233-failure); "
                        "case \"$code\" in duplicate_keys|table_or_columns|index|privilege|trigger|context_or_state|permission|unknown) ;; *) exit 1 ;; esac; "
                        "printf 'migration_233_failure_code=%s\\n' \"$code\"",
                        {"migration_233_failure_code"},
                    )
                except BaseException:
                    failure = {"migration_233_failure_code": "unknown"}
                self.stage("migration_233_preflight_failed", failure)
                raise
            self.stage("migration_233_preflight_verified", values)
        if self.profile["name"] in {"235", "236", "237", "238", "239", "240", "241"} and self.migration_234_status not in {"absent", "verified"}:
            raise RuntimeError("migration 234 preflight status is unknown")
        if self.profile["name"] in {"235", "236", "237", "238", "239", "240", "241"}:
            self.stage("migration_234_preflight")
            env = assertion_env(self.migration_234_status)
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-234-assert.sh preflight",
                {"migration_234_schema_state", "migration_234_schema_verified", "migration_234_preflight"},
            )
            self.stage("migration_234_preflight_verified", values)
        if self.profile["name"] in {"237", "238", "239", "240", "241"}:
            for number, status, script_name, fields in (
                (235, self.migration_235_status, "migration-235-assert.sh", {"migration_235_schema_state", "migration_235_preflight"}),
                (236, self.migration_236_status, "migration-236-assert.sh", {"migration_236_schema_state", "migration_236_preflight"}),
            ):
                if status not in {"absent", "verified"}:
                    raise RuntimeError(f"migration {number} preflight status is unknown")
                self.stage(f"migration_{number}_preflight")
                env = assertion_env(status)
                values = self.run_remote("racknerd", f"{env} {self.active_assets}/{script_name} preflight", fields)
                self.stage(f"migration_{number}_preflight_verified", values)
        if self.profile["name"] in {"238", "239", "240", "241"}:
            if self.migration_237_status not in {"absent", "verified"}:
                raise RuntimeError("migration 237 preflight status is unknown")
            self.stage("migration_237_preflight")
            env = assertion_env(self.migration_237_status)
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-237-assert.sh preflight",
                {"migration_237_schema_state", "migration_237_preflight", "migration_237_postflight"},
            )
            self.stage("migration_237_preflight_verified", values)
        if self.profile["name"] in {"239", "240", "241"}:
            if self.migration_238_status not in {"absent", "verified"}:
                raise RuntimeError("migration 238 preflight status is unknown")
            self.stage("migration_238_preflight")
            env = assertion_env(self.migration_238_status)
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-238-assert.sh preflight",
                {"migration_238_schema_state", "migration_238_preflight", "migration_238_postflight"},
            )
            self.stage("migration_238_preflight_verified", values)
            if self.migration_239_status not in {"absent", "verified"}:
                raise RuntimeError("migration 239 preflight status is unknown")
            self.stage("migration_239_preflight")
            env = assertion_env(self.migration_239_status)
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-239-assert.sh preflight",
                {"migration_239_affected", "migration_239_data_plan_sha256", "migration_239_preflight"},
            )
            self.stage("migration_239_preflight_verified", values)
            for number, status, script_name, fields in (
                (240, self.migration_240_status, "migration-240-assert.sh", {"migration_240_schema_state", "migration_240_preflight", "migration_240_postflight"}),
                (241, self.migration_241_status, "migration-241-assert.sh", {"migration_241_schema_state", "migration_241_preflight", "migration_241_postflight"}),
                (242, self.migration_242_status, "migration-242-assert.sh", {"migration_242_schema_state", "migration_242_preflight", "migration_242_postflight"}),
                (243, self.migration_243_status, "migration-243-assert.sh", {"migration_243_schema_state", "migration_243_preflight", "migration_243_postflight"}),
                (244, self.migration_244_status, "migration-244-assert.sh", {"migration_244_schema_state", "migration_244_preflight", "migration_244_postflight"}),
                (245, self.migration_245_status, "migration-245-assert.sh", {"migration_245_schema_state", "migration_245_preflight", "migration_245_postflight"}),
            ):
                if status not in {"absent", "verified"}:
                    raise RuntimeError(f"migration {number} preflight status is unknown")
                self.stage(f"migration_{number}_preflight")
                env = assertion_env(status)
                values = self.run_remote("racknerd", f"{env} {self.active_assets}/{script_name} preflight", fields)
                self.stage(f"migration_{number}_preflight_verified", values)

    def migration_preflight_v2(self) -> None:
        pending = self.evidence.get("migration_evidence", {}).get("pending", [])
        for item in pending:
            hook = HOOK_REGISTRY.get(str(item["filename"]))
            if not hook:
                continue
            self.stage(f"migration_hook_preflight_{item['filename'].split('.')[0]}")
            env = quoted_env({"RELEASE_DIR": self.release_dir, "MIGRATION_STATUS": "absent"})
            phases = hook.get("preflight", ("preflight",))
            for phase in phases:
                self.run_remote("racknerd", f"{env} {self.active_assets}/{hook['script']} {phase} >/dev/null && printf 'hook_verified=true\\n'", {"hook_verified"})

    def bind_migration_plan(self) -> None:
        if self.manifest.get("schema") == 2:
            self.bind_migration_plan_v2()
            return
        if self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            return
        self.stage("migration_195_bind_recovery_point")
        env = quoted_env({"RELEASE_DIR": self.release_dir, "DEPLOYMENT_MODE": self.deployment_mode})
        values = self.run_remote(
            "racknerd",
            f"{env} {self.active_assets}/migration-195-assert.sh bind",
            {"migration_195_plan_sha256", "migration_195_recovery_sha256"},
        )
        self.stage("migration_195_plan_bound", values)
        if self.profile["name"] in {"232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            self.stage("migration_232_bind_recovery_point")
            env = quoted_env({"RELEASE_DIR": self.release_dir, "MIGRATION_STATUS": self.migration_232_status})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-232-assert.sh bind",
                {"migration_232_plan_sha256", "migration_232_recovery_sha256"},
            )
            self.stage("migration_232_plan_bound", values)
        if self.profile["name"] in {"239", "240", "241"}:
            self.stage("migration_239_bind_recovery_point")
            env = quoted_env({"RELEASE_DIR": self.release_dir, "MIGRATION_STATUS": self.migration_239_status})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/migration-239-assert.sh bind",
                {"migration_239_plan_sha256", "migration_239_recovery_sha256"},
            )
            self.stage("migration_239_plan_bound", values)

    def bind_migration_plan_v2(self) -> None:
        pending = self.evidence.get("migration_evidence", {}).get("pending", [])
        for item in pending:
            hook = HOOK_REGISTRY.get(str(item["filename"]))
            if not hook:
                continue
            self.stage(f"migration_hook_bind_{item['filename'].split('.')[0]}")
            env = quoted_env({"RELEASE_DIR": self.release_dir, "MIGRATION_STATUS": "absent", "DEPLOYMENT_MODE": self.deployment_mode})
            for phase in hook.get("bind", ("bind",)):
                self.run_remote("racknerd", f"{env} {self.active_assets}/{hook['script']} {phase} >/dev/null && printf 'hook_bound=true\\n'", {"hook_bound"})

    def postflight_migration_hooks_v2(self) -> None:
        pending = self.evidence.get("migration_evidence", {}).get("pending", [])
        for item in pending:
            hook = HOOK_REGISTRY.get(str(item["filename"]))
            if not hook:
                continue
            self.stage(f"migration_hook_postflight_{item['filename'].split('.')[0]}")
            env = quoted_env({"RELEASE_DIR": self.release_dir, "MIGRATION_STATUS": "verified"})
            for phase in hook.get("postflight", ("postflight",)):
                self.run_remote("racknerd", f"{env} {self.active_assets}/{hook['script']} {phase} >/dev/null && printf 'hook_verified=true\\n'", {"hook_verified"})

    def switch(self) -> None:
        self.stage("migration_and_switch", timeout=1200)
        self.migration_started = True
        env = quoted_env({"RELEASE_DIR": self.release_dir, "DEPLOYMENT_MODE": self.deployment_mode})
        allowed = {
            "migration_verified", "running_image_id", "internal_health", "public_traffic_enabled",
            "candidate_container", "candidate_port", "active_container", "active_port",
            "background_activation",
            "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
        }
        if getattr(self, "profile", {}).get("name") in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({
                "migration_195_affected", "migration_195_unproven",
                "migration_195_plan_sha256", "migration_195_database_postflight", "migration_195_postflight",
                "migration_195_recompute_mismatch", "migration_195_outbox_consumed",
                "migration_195_account_mismatch", "migration_195_snapshot_missing", "migration_195_outbox_missing",
                "migration_195_constraint_missing", "migration_195_trigger_missing",
            })
        if getattr(self, "profile", {}).get("name") in {"198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("managed_monitor_key_names_verified")
        if getattr(self, "profile", {}).get("name") in {"199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("reasoning_effort_policy_verified")
        if getattr(self, "profile", {}).get("name") in {"202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({
                "alipay_mobile_precreate_migration_verified",
                "group_auth_cache_image_generation_verified",
                "composite_model_routes_verified",
            })
        if getattr(self, "profile", {}).get("name") in {"206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({
                "session_id_columns_verified",
                "live_request_type_verified",
                "group_allow_live_verified",
                "email_alias_index_verified",
            })
        if getattr(self, "profile", {}).get("name") in {"208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("passkey_schema_verified")
        if getattr(self, "profile", {}).get("name") in {"209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("user_usage_aggregation_schema_verified")
        if getattr(self, "profile", {}).get("name") in {"212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("group_profit_control_schema_verified")
            allowed.add("group_profit_auth_cache_trigger_verified")
        if getattr(self, "profile", {}).get("name") in {"215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.add("usage_log_upstream_model_columns_verified")
            allowed.add("usage_log_upstream_model_mismatch_index_verified")
        if getattr(self, "profile", {}).get("name") in {"232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({
                "migration_232_backup_rows",
                "migration_232_remaining_rows",
                "migration_232_bound_sha256",
                "migration_232_postflight",
                "channel_monitor_v2_schema_verified",
                "channel_monitor_v2_defaults_verified",
                "group_media_pricing_schema_verified",
                "group_media_auth_cache_trigger_verified",
            })
        if getattr(self, "profile", {}).get("name") in {"233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({
                "migration_233_duplicate_keys",
                "migration_233_index_verified",
                "migration_233_table_state",
                "migration_233_columns_verified",
                "migration_233_health_index_verified",
                "migration_233_privileges_verified",
                "migration_233_trigger_verified",
                "migration_233_postflight",
            })
        if getattr(self, "profile", {}).get("name") in {"235", "236", "237", "238", "239", "240", "241"}:
            allowed.update({"migration_234_schema_state", "migration_234_schema_verified", "migration_234_postflight"})
        if getattr(self, "profile", {}).get("name") in {"237", "238", "239", "240", "241"}:
            allowed.update({
                "migration_235_schema_state", "migration_235_schema_verified", "migration_235_postflight",
                "migration_236_schema_state", "migration_236_schema_verified", "migration_236_postflight",
            })
        if getattr(self, "profile", {}).get("name") in {"238", "239", "240", "241"}:
            allowed.update({
                "migration_237_schema_state",
                "migration_237_schema_verified",
                "migration_237_preflight",
                "migration_237_postflight",
            })
        if getattr(self, "profile", {}).get("name") in {"239", "240", "241"}:
            allowed.update({
                "migration_238_schema_state",
                "migration_238_schema_verified",
                "migration_238_preflight",
                "migration_238_postflight",
                "migration_239_backup_rows",
                "migration_239_remaining_rows",
                "migration_239_constraint_verified",
                "migration_239_postflight",
            })
        if getattr(self, "profile", {}).get("name") in {"240", "241"}:
            allowed.update({
                "migration_240_schema_state",
                "migration_240_schema_verified",
                "migration_240_preflight",
                "migration_240_postflight",
                "migration_241_schema_state",
                "migration_241_schema_verified",
                "migration_241_preflight",
                "migration_241_postflight",
            })
        if getattr(self, "profile", {}).get("name") == "241":
            for number in (242, 243, 244, 245):
                allowed.update({
                    f"migration_{number}_schema_state",
                    f"migration_{number}_schema_verified",
                    f"migration_{number}_preflight",
                    f"migration_{number}_postflight",
                })
        try:
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/switch.sh",
                allowed,
                timeout=1200,
            )
        except BaseException as error:
            message = str(error)
            failure_reason = "unknown"
            if "returned an undeclared field:" in message:
                failure_reason = "undeclared_field"
            elif "omitted required fields:" in message:
                failure_reason = "missing_field"
            elif "returned unexpected stderr" in message:
                failure_reason = "unexpected_stderr"
            elif "stage failed with exit code" in message:
                failure_reason = "remote_exit"
            try:
                failure = self.run_remote(
                    "racknerd",
                    f"set -Eeuo pipefail; marker={shlex.quote(self.state_dir + '/switch-stage')}; "
                    "test -f \"$marker\" && test ! -L \"$marker\" && "
                    "stage=$(cat \"$marker\"); "
                    "[[ $stage =~ ^(initialized|downtime_stopped|migration_started|migration_completed|schema_verified|migration_committed|downtime_compose_prepared|candidate_started|candidate_healthy|candidate_network_verified|candidate_port_verified|candidate_probe_started|candidate_http_verified|candidate_headers_verified|background_activated|active_health_verified|prompt_audit_verified|runtime_verified)$ ]]; "
                    "http_code=unknown; curl_exit=unknown; "
                    f"code_file={shlex.quote(self.state_dir + '/candidate-http.code')}; "
                    f"exit_file={shlex.quote(self.state_dir + '/candidate-curl.exit')}; "
                    f"failure_file={shlex.quote(self.state_dir + '/candidate-failure')}; "
                    f"init_failure_file={shlex.quote(self.state_dir + '/switch-init-failure')}; "
                    "if test -f \"$code_file\" && test ! -L \"$code_file\"; then value=$(cat \"$code_file\"); [[ $value =~ ^[0-9]{3}$ ]]; http_code=$value; fi; "
                    "if test -f \"$exit_file\" && test ! -L \"$exit_file\"; then value=$(cat \"$exit_file\"); [[ $value =~ ^[0-9]{1,3}$ ]]; curl_exit=$value; fi; "
                    "candidate_failure_kind=unknown; candidate_state=unknown; candidate_health=unknown; candidate_exit_code=unknown; candidate_original_exit_code=unknown; "
                    "candidate_oom_killed=unknown; candidate_restart_count=unknown; candidate_health_log_entries=unknown; "
                    "candidate_log_capture=unknown; candidate_failure_line=unknown; "
                    "if test -f \"$failure_file\" && test ! -L \"$failure_file\"; then "
                    "test \"$(stat -c '%U:%G:%a:%h' \"$failure_file\")\" = root:root:600:1; "
                    "test \"$(grep -c '^candidate_' \"$failure_file\")\" = 10; "
                    "candidate_failure_kind=$(sed -n 's/^candidate_failure_kind=//p' \"$failure_file\"); "
                    "candidate_state=$(sed -n 's/^candidate_state=//p' \"$failure_file\"); "
                    "candidate_health=$(sed -n 's/^candidate_health=//p' \"$failure_file\"); "
                    "candidate_exit_code=$(sed -n 's/^candidate_exit_code=//p' \"$failure_file\"); "
                    "candidate_original_exit_code=$(sed -n 's/^candidate_original_exit_code=//p' \"$failure_file\"); "
                    "candidate_oom_killed=$(sed -n 's/^candidate_oom_killed=//p' \"$failure_file\"); "
                    "candidate_restart_count=$(sed -n 's/^candidate_restart_count=//p' \"$failure_file\"); "
                    "candidate_health_log_entries=$(sed -n 's/^candidate_health_log_entries=//p' \"$failure_file\"); "
                    "candidate_log_capture=$(sed -n 's/^candidate_log_capture=//p' \"$failure_file\"); "
                    "candidate_failure_line=$(sed -n 's/^candidate_failure_line=//p' \"$failure_file\"); "
                    "case \"$candidate_failure_kind\" in container_missing|container_exited|inspect_failed|health_unhealthy|health_timeout|runtime_contract_mismatch|post_start_contract_failure|activation_marker_write_failed|background_activation_timeout) ;; *) exit 1 ;; esac; "
                    "case \"$candidate_state\" in created|running|paused|restarting|removing|exited|dead|missing|unknown) ;; *) exit 1 ;; esac; "
                    "case \"$candidate_health\" in starting|healthy|unhealthy|none|missing|unknown) ;; *) exit 1 ;; esac; "
                    "[[ $candidate_exit_code == unknown || $candidate_exit_code =~ ^[0-9]+$ ]]; "
                    "[[ $candidate_original_exit_code == unknown || $candidate_original_exit_code =~ ^[0-9]+$ ]]; "
                    "[[ $candidate_oom_killed == true || $candidate_oom_killed == false || $candidate_oom_killed == unknown ]]; "
                    "[[ $candidate_restart_count == unknown || $candidate_restart_count =~ ^[0-9]+$ ]]; "
                    "[[ $candidate_health_log_entries == unknown || $candidate_health_log_entries =~ ^[0-9]+$ ]]; "
                    "case \"$candidate_log_capture\" in saved|unavailable|not_configured|unknown) ;; *) exit 1 ;; esac; "
                    "[[ $candidate_failure_line == unknown || $candidate_failure_line =~ ^[0-9]+$ ]]; fi; "
                    "init_failure_stage=absent; init_failure_substage=absent; init_failure_code=absent; init_failure_line=absent; "
                    "if test -f \"$init_failure_file\" && test ! -L \"$init_failure_file\"; then "
                    "test \"$(stat -c '%U:%G:%a:%h' \"$init_failure_file\")\" = root:root:600:1; "
                    "test \"$(grep -c '^switch_failure_' \"$init_failure_file\")\" = 4; "
                    "init_failure_stage=$(sed -n 's/^switch_failure_stage=//p' \"$init_failure_file\"); "
                    "init_failure_substage=$(sed -n 's/^switch_failure_substage=//p' \"$init_failure_file\"); "
                    "init_failure_code=$(sed -n 's/^switch_failure_code=//p' \"$init_failure_file\"); "
                    "init_failure_line=$(sed -n 's/^switch_failure_line=//p' \"$init_failure_file\"); "
                    "case \"$init_failure_stage\" in initialized|unknown) ;; *) exit 1 ;; esac; "
                    "case \"$init_failure_substage\" in initial_contract|context_source|unknown) ;; *) exit 1 ;; esac; "
                    "case \"$init_failure_code\" in context_source|initial_contract|unknown) ;; *) exit 1 ;; esac; "
                    "[[ $init_failure_line == unknown || $init_failure_line =~ ^[0-9]+$ ]]; fi; "
                    "printf 'switch_failure_stage=%s\ncandidate_http_code=%s\ncandidate_curl_exit=%s\ncandidate_failure_kind=%s\ncandidate_state=%s\ncandidate_health=%s\ncandidate_exit_code=%s\ncandidate_original_exit_code=%s\ncandidate_oom_killed=%s\ncandidate_restart_count=%s\ncandidate_health_log_entries=%s\ncandidate_log_capture=%s\ncandidate_failure_line=%s\ninit_failure_stage=%s\ninit_failure_substage=%s\ninit_failure_code=%s\ninit_failure_line=%s\n' "
                    "\"$stage\" \"$http_code\" \"$curl_exit\" \"$candidate_failure_kind\" \"$candidate_state\" \"$candidate_health\" \"$candidate_exit_code\" \"$candidate_original_exit_code\" \"$candidate_oom_killed\" \"$candidate_restart_count\" \"$candidate_health_log_entries\" \"$candidate_log_capture\" \"$candidate_failure_line\" \"$init_failure_stage\" \"$init_failure_substage\" \"$init_failure_code\" \"$init_failure_line\"",
                        {
                        "switch_failure_stage", "candidate_http_code", "candidate_curl_exit",
                        "candidate_failure_kind", "candidate_state", "candidate_health", "candidate_exit_code", "candidate_original_exit_code",
                        "candidate_oom_killed", "candidate_restart_count", "candidate_health_log_entries",
                        "candidate_log_capture", "candidate_failure_line", "init_failure_stage", "init_failure_substage",
                        "init_failure_code", "init_failure_line",
                    },
                )
            except BaseException:
                self.stage("migration_switch_failed", {
                    "switch_failure_stage": "unknown", "switch_failure_reason": failure_reason,
                    "candidate_failure_kind": "unknown", "candidate_state": "unknown", "candidate_health": "unknown",
                    "candidate_exit_code": "unknown", "candidate_original_exit_code": "unknown", "candidate_oom_killed": "unknown",
                    "candidate_restart_count": "unknown", "candidate_health_log_entries": "unknown",
                    "candidate_log_capture": "unknown", "candidate_failure_line": "unknown",
                    "switch_failure_substage": "unknown", "switch_failure_code": "unknown", "switch_failure_line": "unknown",
                    "migration_195_failure_phase": "unknown", "migration_195_failure_code": "none",
                })
            else:
                failure["switch_failure_reason"] = failure_reason
                try:
                    migration_failure = self.run_remote(
                        "racknerd",
                        f"""set -Eeuo pipefail
state={shlex.quote(self.state_dir)}
switch_substage=unknown
switch_code=unknown
switch_line=unknown
m195_phase=unknown
m195_code=none
switch_file=\"$state/migration-switch-failure\"
m195_file=\"$state/migration-195-failure\"
if test -f \"$switch_file\" && test ! -L \"$switch_file\"; then
  test \"$(stat -c '%U:%G:%a:%h' \"$switch_file\")\" = root:root:600:1
  test \"$(grep -c '^switch_failure_' \"$switch_file\")\" = 4
  switch_stage=$(sed -n 's/^switch_failure_stage=//p' \"$switch_file\")
  switch_substage=$(sed -n 's/^switch_failure_substage=//p' \"$switch_file\")
  switch_code=$(sed -n 's/^switch_failure_code=//p' \"$switch_file\")
  switch_line=$(sed -n 's/^switch_failure_line=//p' \"$switch_file\")
  case \"$switch_stage\" in migration_completed|schema_verified) ;; *) exit 1 ;; esac
  case \"$switch_substage:$switch_code\" in
    migration_record_verification:migration_record_checksum|schema_contract_assertion:schema_assertion|schema_stage_marker:schema_stage_marker|migration_container_identity:migration_container|migration_container_exit:migration_container|migration_marker_prepare:migration_marker|migration_marker_manifest:migration_marker|migration_marker_publish:migration_marker|migration_195_postflight:migration_195_postflight) ;;
    *) exit 1 ;;
  esac
  [[ $switch_line =~ ^[0-9]+$ ]]
fi
if test -f \"$m195_file\" && test ! -L \"$m195_file\"; then
  test \"$(stat -c '%U:%G:%a:%h' \"$m195_file\")\" = root:root:600:1
  test \"$(grep -c '^migration_195_failure_' \"$m195_file\")\" = 2
  m195_phase=$(sed -n 's/^migration_195_failure_phase=//p' \"$m195_file\")
  m195_code=$(sed -n 's/^migration_195_failure_code=//p' \"$m195_file\")
  case \"$m195_phase\" in preflight|bind|postflight_db|postflight_runtime) ;; *) exit 1 ;; esac
  case \"$m195_code\" in config_file|timezone_value|unproven_rate|account_binding_mismatch|outbox_query|outbox_value|outbox_watermark|data_plan_query|account_ids_query|data_plan_hash|account_binding_conflict|unexpected_data|recovery_point|data_plan_missing|plan_write|plan_missing|affected_missing|timezone_file|data_plan_mismatch|account_ids_file|account_ids_hash|status_file|status_value|outbox_baseline_file|source_rate_state|postflight_shape) ;; *) exit 1 ;; esac
fi
printf 'switch_failure_substage=%s\\nswitch_failure_code=%s\\nswitch_failure_line=%s\\nmigration_195_failure_phase=%s\\nmigration_195_failure_code=%s\\n' \"$switch_substage\" \"$switch_code\" \"$switch_line\" \"$m195_phase\" \"$m195_code\"
""",
                        {"switch_failure_substage", "switch_failure_code", "switch_failure_line", "migration_195_failure_phase", "migration_195_failure_code"},
                    )
                    failure.update(migration_failure)
                except BaseException:
                    failure.update({
                        "switch_failure_substage": "unknown", "switch_failure_code": "unknown", "switch_failure_line": "unknown",
                        "migration_195_failure_phase": "unknown", "migration_195_failure_code": "none",
                    })
                self.stage("migration_switch_failed", failure)
            raise
        self.stage("candidate_started", {
            "candidate_container": values["candidate_container"],
            "candidate_port": values["candidate_port"],
            "active_container": values["active_container"],
            "active_port": values["active_port"],
        })
        self.stage("candidate_healthy", values)

    def verify_and_finalize(self) -> None:
        deployment_mode = getattr(self, "deployment_mode", "blue-green")
        self.stage("public_route_verification", timeout=3300)
        expose_env = quoted_env({
            "RELEASE_DIR": self.release_dir,
            "DEPLOYMENT_MODE": deployment_mode,
            "PUBLIC_DOMAIN": self.profile["public_domain"],
            "DIRECT_IP": self.profile["rack_public_ip"],
        })
        self.route_switch_attempted = True
        exposed = self.run_remote(
            "racknerd",
            f"{expose_env} {self.active_assets}/expose.sh",
            {"public_traffic_enabled", "nginx_reload", "new_active_container", "new_active_port", "previous_container", "previous_port"},
        )
        self.public_exposed = True
        self.route_switched = True
        self.stage("nginx_reloaded", exposed)
        verify_env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "PUBLIC_DOMAIN": self.profile["public_domain"],
                "DIRECT_IP": self.profile["rack_public_ip"],
            }
        )
        verified = self.run_remote(
            "racknerd",
            f"{verify_env} {self.active_assets}/verify.sh",
            {
                "direct_health", "underscore_header_path", "two_mib_reached_app", "startup_logs",
                "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
            },
            timeout=600,
        )
        direct, dmit = self.verify_streaming_routes("post_switch")
        direct_agent = direct["user_agent"]
        dmit_agent = dmit["user_agent"]
        direct_agents = direct["attempt_user_agents"].split(",")
        dmit_agents = dmit["attempt_user_agents"].split(",")
        all_agents = direct_agents + dmit_agents
        agent_sql = ",".join("'" + agent.replace("'", "''") + "'" for agent in all_agents)
        direct_case = "|".join(shlex.quote(agent) for agent in direct_agents)
        dmit_case = "|".join(shlex.quote(agent) for agent in dmit_agents)
        backup_identity = self.run_remote(
            "backup",
            "public_ip=$(curl -fsS --max-time 15 https://api.ipify.org); [[ $public_ip =~ ^[0-9a-fA-F:.]+$ ]] && printf 'backup_public_ip=%s\\n' \"$public_ip\"",
            {"backup_public_ip"},
        )["backup_public_ip"]
        expected_direct_ip = self.profile["rack_public_ip"]
        usage_script = f"""
set -Eeuo pipefail
expected_direct_agent={shlex.quote(direct_agent)}
expected_dmit_agent={shlex.quote(dmit_agent)}
for _ in $(seq 1 30); do
  mapfile -t rows < <(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c {shlex.quote("SELECT user_agent, COALESCE(ip_address,''), api_key_id, COALESCE(inbound_endpoint,'') FROM usage_logs WHERE created_at > NOW() - INTERVAL '15 minutes' AND user_agent IN (" + agent_sql + ") ORDER BY user_agent")})
  found_direct=false
  found_dmit=false
  for row in "${{rows[@]}}"; do
    [[ ${{row%%|*}} == {shlex.quote(direct_agent)} ]] && found_direct=true
    [[ ${{row%%|*}} == {shlex.quote(dmit_agent)} ]] && found_dmit=true
  done
  [[ $found_direct == true && $found_dmit == true ]] && break
  sleep 1
done
[[ ${{#rows[@]}} -ge 2 && ${{#rows[@]}} -le {len(all_agents)} ]]
declare -A seen=()
for row in "${{rows[@]}}"; do
  IFS='|' read -r agent ip api_key endpoint <<<"$row"
  [[ -z ${{seen[$agent]+x}} ]]
  seen["$agent"]=1
  [[ $api_key == {int(self.profile.get('canary_api_key_id', 2))} ]]
  [[ $endpoint == /v1/responses ]]
  case "$agent" in
    {direct_case}) [[ $ip == {shlex.quote(expected_direct_ip)} ]] ;;
    {dmit_case}) [[ $ip == {shlex.quote(backup_identity)} ]] ;;
    *) exit 1 ;;
  esac
done
[[ -n ${{seen[$expected_direct_agent]+x}} ]]
[[ -n ${{seen[$expected_dmit_agent]+x}} ]]
printf 'canary_usage_recorded=true\nreal_client_ip=pass\ncanary_usage_records=%s\n' "${{#rows[@]}}"
"""
        attribution = self.run_remote(
            "racknerd", usage_script, {"canary_usage_recorded", "real_client_ip", "canary_usage_records"}, timeout=90
        )
        self.stage("split_route_verified", {"direct_path": direct["route_health"], "dmit_path": dmit["route_health"], **attribution})
        finalize_env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "PUBLIC_DOMAIN": self.profile["public_domain"],
                "DIRECT_IP": self.profile["rack_public_ip"],
            }
        )
        finalize_stage = "downtime_finalizing" if deployment_mode == "downtime" else "old_slot_draining"
        self.stage(finalize_stage, timeout=7500)
        try:
            final = self.run_remote(
                "racknerd",
                f"{finalize_env} {self.active_assets}/finalize.sh",
                {
                    "auto_sync_enabled", "running_image_id", "final_health", "final_logs",
                    "background_activation", "compose_managed", "old_container", "old_port",
                    "drain_status", "drain_connections", "candidate_drain_connections",
                    "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
                },
                timeout=7500,
            )
        except BaseException:
            try:
                failure = self.run_remote(
                    "racknerd",
                    f"set -Eeuo pipefail; state={shlex.quote(self.state_dir)}; "
                    "test -f \"$state/finalize-failure\" && test ! -L \"$state/finalize-failure\" && "
                    "test \"$(stat -c '%U:%G:%a' \"$state/finalize-failure\")\" = root:root:600 && "
                    "phase=$(sed -n 's/^finalize_failure_phase=//p' \"$state/finalize-failure\"); "
                    "line=$(sed -n 's/^finalize_failure_line=//p' \"$state/finalize-failure\"); "
                    "test \"$(grep -c '^finalize_failure_' \"$state/finalize-failure\")\" = 2 && "
                    "case \"$phase\" in preflight|downtime_preflight|downtime_log_gate|old_slot_drain|old_slot_remove|compose_prepare|final_container_start|final_instance_readiness|background_activation|final_route|candidate_drain|final_log_gate) ;; *) exit 1 ;; esac; "
                    "case \"$line\" in ''|*[!0-9]*) exit 1 ;; esac; "
                    "printf 'finalize_failure_phase=%s\\nfinalize_failure_line=%s\\n' \"$phase\" \"$line\"",
                    {"finalize_failure_phase", "finalize_failure_line"},
                )
            except BaseException:
                failure = {"finalize_failure_phase": "unknown", "finalize_failure_line": "0"}
            self.stage("finalize_failed", failure)
            raise
        if final["drain_status"] not in {"drained", "not_applicable"}:
            raise RuntimeError(f"old application slot did not drain: {final['drain_status']}")
        completed_stage = "downtime_finalized" if deployment_mode == "downtime" else "old_slot_drained"
        self.stage(completed_stage, final)
        external_final = self.run_remote(
            "backup",
            f"test $(curl -sS --resolve {self.profile['public_domain']}:443:{self.profile['dmit_public_ip']} --max-time 15 -o /dev/null -w '%{{http_code}}' https://{self.profile['public_domain']}/health) = 200 && printf 'dmit_final_health=pass\\n'",
            {"dmit_final_health"},
        )
        restore_env = quoted_env({"STATE_ROOT": "/opt/sub2api/backups/release-state", "STATE_DIR": self.state_dir})
        backup_units = self.restore_backup_units(restore_env)
        self.units_masked = False
        self.stage("post_switch_services_restored", {**external_final, **backup_units})
        consume_env = quoted_env({"RELEASE_DIR": self.release_dir})
        consumed = self.run_remote("racknerd", f"{consume_env} {self.active_assets}/consume.sh", {"gate_consumed"})
        consumed_assets = f"{self.release_dir}/.consumed/assets"
        cleaned = self.run_remote(
            "racknerd",
            f"{consume_env} ACTIVE_CLAIM={self.release_dir}/.consumed {consumed_assets}/cleanup-state.sh",
            {"plaintext_state_removed", "state_cleanup"},
        )
        slot_cleanup = self.run_remote(
            "racknerd",
            f"{consume_env} ACTIVE_CLAIM={self.release_dir}/.consumed {consumed_assets}/cleanup-slots.sh",
            {"candidate_removed", "previous_removed"},
        )
        self.result["status"] = "verified"
        route_evidence = {
            "direct_route_health": direct["route_health"],
            "direct_streaming": direct["streaming"],
            "dmit_route_health": dmit["route_health"],
            "dmit_streaming": dmit["streaming"],
        }
        self.stage("production_verified", {**verified, **route_evidence, **attribution, **final, **external_final, **backup_units, **consumed, **slot_cleanup, **cleaned})

    def recover(self) -> None:
        self.stage("recovery_started")
        recovery_needed = self.remote_pre_switch_recovery_needed()
        if recovery_needed is None:
            raise RuntimeError("old application slot state is unknown")
        migration_committed = self.migration_started
        if self.migration_started and getattr(self, "profile", {}).get("name") in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            migration_committed = self.remote_migration_committed()
            if migration_committed is None:
                raise RuntimeError("migration 195 committed state is unknown")
        if recovery_needed and migration_committed:
            env = quoted_env({"RELEASE_DIR": self.release_dir})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/restore.sh",
                {"coordinated_restore", "restored_image_id", "application_health"},
                timeout=2400,
            )
        elif recovery_needed:
            env = quoted_env({"RELEASE_DIR": self.release_dir})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/resume-old.sh",
                {"old_application_resumed", "running_image_id"},
                timeout=600,
            )
        else:
            values = {}
        if self.mask_intent:
            masked = self.remote_units_masked()
            if masked is None:
                raise RuntimeError("backup unit mask state is unknown")
            self.units_masked = masked
        if self.units_masked:
            restore_env = quoted_env({"STATE_ROOT": "/opt/sub2api/backups/release-state", "STATE_DIR": self.state_dir})
            unit_values = self.restore_backup_units(restore_env)
            values.update(unit_values)
            self.units_masked = False
        cleaned = self.run_remote(
            "racknerd",
            f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.active_assets}/cleanup-state.sh",
            {"plaintext_state_removed", "state_cleanup"},
        )
        values.update(cleaned)
        try:
            reconciled = self.run_remote(
                "racknerd",
                f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.active_assets}/reconcile.sh",
                {"release_claim_reconciled"},
            )
        except BaseException:
            reconciled = self.run_remote(
                "racknerd",
                f"slot=/opt/sub2api/active-app; active_container=$(sed -n 's/^container=//p' \"$slot\" 2>/dev/null || true); test -f {self.release_dir}/.recovered/marker && test -f {self.release_dir}/.recovered/plaintext-cleaned && test ! -e /opt/sub2api/releases/.active-release && test -n \"$active_container\" && test \"$(docker inspect -f '{{{{.State.Health.Status}}}}' \"$active_container\")\" = healthy && test $(systemctl is-enabled sub2api-backup.timer) = enabled && printf 'release_claim_reconciled=true\\n'",
                {"release_claim_reconciled"},
            )
        values.update(reconciled)
        self.result["status"] = "recovered"
        self.stage("recovered", values)

    def remote_migration_committed(self) -> bool | None:
        if self.manifest.get("schema") == 2:
            pending = self.evidence.get("migration_evidence", {}).get("pending", [])
            checks = " ".join(shlex.quote(str(item["filename"]) + "|" + str(item["checksum"])) for item in pending)
            pending_digest = hashlib.sha256(canonical_json(pending)).hexdigest()
            marker_lines = "\n".join(
                f"grep -Fxq {shlex.quote('migration=' + str(item['filename']) + ' checksum=' + str(item['checksum']))} \"$marker\""
                for item in pending
            ) or ":"
            script = f'''set -Eeuo pipefail
marker={shlex.quote(self.state_dir + "/migration-committed")}
all_verified=true
for item in {checks}; do filename=${{item%%|*}}; checksum=${{item#*|}}; row=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT checksum FROM schema_migrations WHERE filename='$filename'"); test "$row" = "$checksum" || all_verified=false; done
marker_verified=false
if test -f "$marker" && test ! -L "$marker" && test "$(stat -c '%U:%G:%a:%h' "$marker")" = root:root:600:1; then
  if grep -Fxq {shlex.quote('plan_sha256=' + pending_digest)} "$marker" && \
     grep -Fxq {shlex.quote('catalog_sha256=' + str(self.manifest['catalog_sha256']))} "$marker" && \
     grep -Fxq {shlex.quote('production_snapshot_sha256=' + str(self.evidence['production_snapshot_sha256']))} "$marker"; then
    {marker_lines}
    marker_verified=true
  fi
fi
if test "$all_verified" = true && test "$marker_verified" = true; then printf 'migration_committed=true\\n'; elif test ! -e "$marker" && test -z "$(docker ps -aq -f name=^sub2api-migrate-{self.release_id}$)"; then printf 'migration_committed=false\\n'; else printf 'migration_committed=unknown\\n'; fi'''
            try:
                value = self.run_remote("racknerd", script, {"migration_committed"})["migration_committed"]
            except BaseException:
                return None
            return {"true": True, "false": False}.get(value)
        if self.profile["name"] not in {"195", "197", "198", "199", "202", "206", "207", "208", "209", "210", "212", "213", "215", "232", "233", "234", "235", "236", "237", "238", "239", "240", "241"}:
            return self.migration_started
        status_by_migration = {
            "195_upstream_scheduling_monitor_rates.sql": self.migration_195_status,
            "196_ops_ingress_reject_aggregates.sql": self.migration_196_status,
            "197_auth_cache_invalidation_outbox.sql": self.migration_197_status,
            "198_normalize_managed_monitor_key_names.sql": self.migration_198_status,
            "199_group_reasoning_effort_policy.sql": self.migration_199_status,
            "200_alipay_mobile_precreate_deep_link.sql": self.migration_200_status,
            "201_group_auth_cache_image_generation.sql": self.migration_201_status,
            "202_composite_model_routes.sql": self.migration_202_status,
            "203_add_usage_log_session_id.sql": self.migration_203_status,
            "204_allow_live_usage_request_type.sql": self.migration_204_status,
            "205_add_group_allow_live.sql": self.migration_205_status,
            "206_add_users_email_alias_dedup_index_notx.sql": self.migration_206_status,
            "208_passkey_credentials.sql": self.migration_208_status,
            "209_user_usage_aggregation.sql": self.migration_209_status,
            "211_group_profit_control.sql": self.migration_211_status,
            "212_group_profit_control_auth_cache_invalidation.sql": self.migration_212_status,
            "214_add_usage_log_upstream_response_model.sql": self.migration_214_status,
            "215_add_usage_log_upstream_model_mismatch_index_notx.sql": self.migration_215_status,
            "216_channel_monitor_v2.sql": self.migration_216_status,
            "217_channel_monitor_mode.sql": self.migration_217_status,
            "218_channel_monitor_v2_ignored_error_categories.sql": self.migration_218_status,
            "219_channel_monitor_v2_seed_popular_models.sql": self.migration_219_status,
            "220_channel_monitor_v2_health_thresholds.sql": self.migration_220_status,
            "221_channel_monitor_v2_fixed_rollups.sql": self.migration_221_status,
            "222_channel_monitor_v2_rollup_permissions.sql": self.migration_222_status,
            "223_channel_monitor_v2_refresh_5m.sql": self.migration_223_status,
            "224_channel_monitor_v2_full_table_permissions.sql": self.migration_224_status,
            "225_channel_monitor_v2_default_ignore_and_cache.sql": self.migration_225_status,
            "226_channel_monitor_hide_throughput.sql": self.migration_226_status,
            "227_channel_monitor_v2_reset_factory_cache_thresholds.sql": self.migration_227_status,
            "228_channel_monitor_v2_privacy_defaults.sql": self.migration_228_status,
            "229_group_video_model_prices.sql": self.migration_229_status,
            "230_group_audio_voice_pricing.sql": self.migration_230_status,
            "231_group_search_price_per_1k.sql": self.migration_231_status,
            "232_clear_non_grok_video_generation_config.sql": self.migration_232_status,
            "233_upstream_management.sql": self.migration_233_status,
            "234_group_model_pricing.sql": self.migration_234_status,
            "235_group_usage_daily_rollups.sql": self.migration_235_status,
            "236_group_usage_rollup_timezone.sql": self.migration_236_status,
            "237_image_cost_routing.sql": self.migration_237_status,
            "238_upstream_account_lifecycle.sql": self.migration_238_status,
            "239_reconcile_non_grok_video_pricing.sql": self.migration_239_status,
            "240_upstream_observation_preference.sql": self.migration_240_status,
            "241_precise_upstream_effective_rate.sql": self.migration_241_status,
            "242_user_platform_quotas_add_cn_providers.sql": self.migration_242_status,
            "243_backfill_codex_fingerprint_seed.sql": self.migration_243_status,
            "244_channel_model_time_pricing.sql": self.migration_244_status,
            "245_channel_monitor_quota_mode.sql": self.migration_245_status,
        }
        pending = [migration for migration in self.manifest["migrations"] if status_by_migration.get(migration) == "absent"]
        pending_words = " ".join(shlex.quote(migration) for migration in pending)
        script = f"""
set -Eeuo pipefail
marker={self.state_dir}/migration-committed
plan=$(cat {self.state_dir}/migration-195-plan.sha256 2>/dev/null || true)
gate={self.release_dir}/gate.json
container=sub2api-migrate-{self.release_id}
pending_migrations=({pending_words})
expected_manifest=$(jq -cS '.manifest.migration_sha256' "$gate" | sha256sum | awk '{{print $1}}')
marker_manifest=$(sed -n 's/^migration_manifest_sha256=//p' "$marker" 2>/dev/null || true)
all_verified=true
while IFS=$'\t' read -r migration checksum; do
  row=$(docker exec sub2api-postgres psql -X -A -t -F '|' -U sub2api -d sub2api -c "SELECT filename,checksum FROM schema_migrations WHERE filename='$migration'")
  if test "$row" != "$migration|$checksum" || ! grep -Fxq "migration=$migration checksum=$checksum" "$marker" 2>/dev/null; then
    all_verified=false
  fi
done < <(jq -r '.manifest.migration_sha256 | to_entries[] | [.key,.value] | @tsv' "$gate")
pending_recorded=false
for migration in "${{pending_migrations[@]}}"; do
  row=$(docker exec sub2api-postgres psql -X -A -t -U sub2api -d sub2api -c "SELECT checksum FROM schema_migrations WHERE filename='$migration'")
  if test -n "$row"; then
    pending_recorded=true
  fi
done
if test -f "$marker" && test ! -L "$marker" && test -n "$plan" && grep -Fxq "plan_sha256=$plan" "$marker" && test "$marker_manifest" = "$expected_manifest" && test "$all_verified" = true; then
  printf 'migration_committed=true\n'
elif test ! -e "$marker" && test ! -L "$marker" && test "$pending_recorded" = false && test -z "$(docker ps -aq -f name=^${{container}}$)"; then
  printf 'migration_committed=false\n'
else
  printf 'migration_committed=unknown\n'
fi
"""
        try:
            value = self.run_remote("racknerd", script, {"migration_committed"})["migration_committed"]
        except BaseException:
            return None
        if value == "true":
            return True
        if value == "false":
            return False
        return None

    def remote_pre_switch_recovery_needed(self) -> bool | None:
        managed_upstream = "/etc/nginx/conf.d/sub2api-release-upstream.conf"
        route_intent = f"{self.state_dir}/route-switch-intent"
        route_switched = f"{self.state_dir}/route-switched"
        script = f"""
set -Eeuo pipefail
slot=/opt/sub2api/active-app
managed_upstream={managed_upstream}
active_container=$(sed -n 's/^container=//p' "$slot" 2>/dev/null || true)
active_port=$(sed -n 's/^port=//p' "$slot" 2>/dev/null || true)
slot_valid=false
if test -f "$slot" && test ! -L "$slot" && test "$(grep -c '^container=' "$slot")" = 1 && test "$(grep -c '^port=' "$slot")" = 1 && test -n "$active_container" && {{ test "$active_port" = 18080 || test "$active_port" = 18081; }}; then
  slot_valid=true
fi
app_status=$(docker inspect -f '{{{{.State.Status}}}}' "$active_container" 2>/dev/null || true)
active_image=$(docker inspect -f '{{{{.Image}}}}' "$active_container" 2>/dev/null || true)
pre_image=$(cat {self.state_dir}/pre-image-id 2>/dev/null || true)
nginx_status=$(systemctl is-active nginx 2>/dev/null || true)
upstream_port=$(sed -nE 's/^[[:space:]]*server[[:space:]]+127[.]0[.]0[.]1:(18080|18081);[[:space:]]*$/\1/p' "$managed_upstream" 2>/dev/null || true)
upstream_valid=false
if test -f "$managed_upstream" && test ! -L "$managed_upstream" && test "$(printf '%s\n' "$upstream_port" | grep -c .)" = 1 && test "$upstream_port" = "$active_port"; then
  upstream_valid=true
fi
route_marker_valid=true
if test -e {route_intent} || test -L {route_intent}; then
  route_marker_valid=false
  if test -f {route_intent} && test ! -L {route_intent} && test -f {route_switched} && test ! -L {route_switched}; then
    switched_port=$(sed -n 's/^route_port=//p' {route_switched})
    if test "$(grep -c '^route_port=' {route_switched})" = 1 && test "$switched_port" = "$active_port"; then
      route_marker_valid=true
    fi
  fi
elif test -e {route_switched} || test -L {route_switched}; then
  route_marker_valid=false
fi
if test -f {self.state_dir}/pre-image-id && test -f {self.state_dir}/SHA256SUMS && {{ test "$slot_valid" != true || test "$app_status" != running || test "$active_image" != "$pre_image" || test "$nginx_status" != active || test "$upstream_valid" != true || test "$route_marker_valid" != true; }}; then
  printf 'recovery_needed=true\n'
else
  printf 'recovery_needed=false\n'
fi
"""
        try:
            values = self.run_remote(
                "racknerd",
                script,
                {"recovery_needed"},
            )
        except BaseException:
            return None
        return values.get("recovery_needed") == "true"

    def remote_units_masked(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                f"if test -f {self.state_dir}/masked.committed && test $(systemctl is-enabled sub2api-backup.service) = masked && test $(systemctl is-enabled sub2api-backup.timer) = masked; then printf 'units_masked=true\\n'; else printf 'units_masked=false\\n'; fi",
                {"units_masked"},
            )
        except BaseException:
            return None
        return values.get("units_masked") == "true"

    def rollback_route(self) -> dict[str, str]:
        claim = "/opt/sub2api/releases/.active-release"
        consumed = f"{self.release_dir}/.consumed"
        values = self.run_remote(
            "racknerd",
            f"claim={claim}; if test ! -d \"$claim\" && test -d {consumed}; then claim={consumed}; fi; {quoted_env({'RELEASE_DIR': self.release_dir, 'PUBLIC_DOMAIN': self.profile['public_domain'], 'DIRECT_IP': self.profile['rack_public_ip']})} ACTIVE_CLAIM=\"$claim\" \"$claim/assets/rollback-route.sh\"",
            {"route_rollback", "nginx_reload", "active_container", "active_port", "candidate_preserved"},
        )
        self.route_switched = False
        self.public_exposed = False
        return values

    def emergency_close(self) -> None:
        # Backward-compatible method name: blue/green recovery restores the
        # old route and keeps Nginx serving instead of closing public traffic.
        self.rollback_route()

    def remote_gate_consumed(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                gate_consumption_probe_script(self.release_dir, self.release_id, self.image_id),
                {"gate_consumed"},
            )
        except BaseException:
            return None
        consumed = values.get("gate_consumed")
        if consumed == "true":
            return True
        if consumed == "false":
            return False
        return None

    def finish_consumed_cleanup(self) -> dict[str, str]:
        consume_env = quoted_env({"RELEASE_DIR": self.release_dir})
        consumed = f"{self.release_dir}/.consumed"
        assets = f"{consumed}/assets"
        cleaned = self.run_remote(
            "racknerd",
            f"{consume_env} ACTIVE_CLAIM={consumed} {assets}/cleanup-state.sh",
            {"plaintext_state_removed", "state_cleanup"},
        )
        slots = self.run_remote(
            "racknerd",
            f"{consume_env} ACTIVE_CLAIM={consumed} {assets}/cleanup-slots.sh",
            {"candidate_removed", "previous_removed"},
        )
        return {**cleaned, **slots}

    def remote_gate_claimed(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                f"if test -d /opt/sub2api/releases/.active-release && test ! -L /opt/sub2api/releases/.active-release && test -f /opt/sub2api/releases/.active-release/release_id && test -f /opt/sub2api/releases/.active-release/gate.json && grep -Fxq 'release_id={self.release_id}' /opt/sub2api/releases/.active-release/release_id; then printf 'gate_claimed=true\\n'; else printf 'gate_claimed=false\\n'; fi",
                {"gate_claimed"},
            )
        except BaseException:
            return None
        return values.get("gate_claimed") == "true"

    def remote_active_claim_exists(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                "if test -e /opt/sub2api/releases/.active-release || test -L /opt/sub2api/releases/.active-release; then printf 'active_claim=true\\n'; else printf 'active_claim=false\\n'; fi",
                {"active_claim"},
            )
        except BaseException:
            return None
        return values.get("active_claim") == "true"

    def execute(self) -> None:
        try:
            self.upload_assets()
            self.preflight()
            self.verify_streaming_routes("pre_switch")
            self.freeze()
            self.migration_preflight()
            self.backup()
            self.bind_migration_plan()
            self.switch()
            if getattr(self, "manifest", {}).get("schema") == 2:
                self.postflight_migration_hooks_v2()
            self.verify_and_finalize()
        except BaseException:
            if not self.claimed:
                claimed = self.remote_gate_claimed()
                if claimed is None:
                    self.result["status"] = "blocked_reconciliation"
                    self.stage("remote_claim_status_unknown")
                    raise
                self.claimed = claimed
                if not self.claimed:
                    active_claim = self.remote_active_claim_exists()
                    if active_claim is None:
                        self.result["status"] = "blocked_reconciliation"
                        self.stage("active_claim_status_unknown")
                        raise
                    if active_claim:
                        self.result["status"] = "blocked_reconciliation"
                        self.stage("incomplete_remote_claim")
                        raise
                    self.result["status"] = "failed"
                    self.stage("failed_before_claim")
                    raise
            gate_consumed = self.remote_gate_consumed()
            if gate_consumed is None:
                self.result["status"] = "blocked_reconciliation"
                # Consumption is the irreversible commit point.  If its
                # outcome cannot be proven, a route rollback could undo a
                # release that has already been committed and leave the
                # database/application pair inconsistent.  Freeze the
                # decision at reconciliation instead; a later status/recovery
                # pass will either finish cleanup or perform the pre-consume
                # rollback once the marker is known to be absent.
                self.stage("gate_consumption_status_unknown", {"route_rollback": "deferred_reconciliation"})
                raise
            if gate_consumed:
                try:
                    cleanup = self.finish_consumed_cleanup()
                except BaseException:
                    self.result["status"] = "blocked_reconciliation"
                    self.stage("consumed_cleanup_requires_reconciliation", {"gate_consumed": "true"})
                    raise
                self.result["status"] = "verified"
                self.stage("production_verified_after_reconciliation", {"gate_consumed": "true", **cleanup})
                return
            if getattr(self, "deployment_mode", "blue-green") == "downtime" and (self.migration_started or self.frozen):
                try:
                    self.recover()
                except BaseException:
                    self.result["status"] = "blocked_reconciliation"
                    self.stage("blocked_reconciliation")
                raise
            if getattr(self, "route_switch_attempted", False) or self.public_exposed:
                rollback: dict[str, str] | None = None
                try:
                    rollback = self.rollback_route()
                    self.stage("route_restored", rollback)
                except BaseException:
                    self.result["status"] = "blocked_reconciliation"
                    self.stage("route_rollback_requires_reconciliation")
                else:
                    self.result["status"] = "blocked_reconciliation"
                raise
            try:
                self.recover()
            except BaseException:
                self.result["status"] = "blocked_reconciliation"
                self.stage("blocked_reconciliation")
            raise
        self.result["status"] = "verified"
        self._save_result()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--gate", required=True)
    parser.add_argument("--profile", required=True)
    args = parser.parse_args()
    ProductionRelease(Path(args.gate).resolve(), args.profile).execute()


if __name__ == "__main__":
    main()
