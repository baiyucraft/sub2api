from __future__ import annotations

import argparse
import json
import shlex
import secrets
import time
from pathlib import Path

from .atomic import atomic_write, canonical_json
from .gate import verify_gate
from .manifest import sha256_file
from .profiles import get_profile
from .ssh import SSHRunner


DEPLOY_ROOT = Path(__file__).resolve().parents[1]
TRUSTED_KEY = DEPLOY_ROOT / "release" / "trust" / "vm-gate-ed25519.pub"
MAINTENANCE_ROOT = DEPLOY_ROOT / "maintenance" / "release"
UNIT_ROOT = DEPLOY_ROOT / "maintenance" / "181"
CANARY_FIELDS = {"route_health", "streaming", "curl_exit", "http_code", "canary_status"}
CANARY_RETRY_DELAYS = (5, 15)


def quoted_env(values: dict[str, str | int]) -> str:
    return " ".join(f"{key}={shlex.quote(str(value))}" for key, value in values.items())


def emit_progress(message: str) -> None:
    try:
        print(message, flush=True)
    except BrokenPipeError:
        pass


class ProductionRelease:
    def __init__(self, gate_dir: Path, profile_name: str) -> None:
        self.gate_dir = gate_dir
        self.profile = get_profile(profile_name)
        self.document = verify_gate(gate_dir, TRUSTED_KEY, profile_name)
        self.manifest = self.document["manifest"]
        self.evidence = self.document["evidence"]
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
        self.mask_intent = False
        self.backup_values: dict[str, str] | None = None
        self.result_path = gate_dir / "production-result.json"
        self.result: dict[str, object] = {"release_id": self.release_id, "status": "running", "stage": "init", "history": []}
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
        return self.runner.run(host, script, allowed, timeout=timeout).values

    def run_remote_with_input(self, host: str, script: str, allowed: set[str], data: bytes, timeout: int = 300) -> dict[str, str]:
        return self.runner.run_with_input(host, script, allowed, data, timeout=timeout).values

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
        self.stage("production_preflight")
        env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "MINIMUM_FREE_BYTES": self.profile["minimum_rack_free_bytes"],
            }
        )
        values = self.run_remote(
            "racknerd",
            f"{env} {self.active_assets}/preflight.sh",
            {"preflight", "pre_switch_image_id", "free_bytes", "migration_status"},
        )
        self.stage("production_preflight_verified", values)

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
                "backup_units_masked", "writes_frozen", "state_dir", "pre_switch_image_id", "compose_sha256",
                "artifact", "transport_artifact", "artifact_size", "artifact_sha256", "no_restart_path_proven",
                "local_restore_point_ready",
            },
            timeout=2400,
        )
        self.frozen = True
        self.units_masked = True
        self.backup_values = values
        if values.get("local_restore_point_ready") != "true":
            raise RuntimeError("local coordinated restore point is not ready")
        if values["state_dir"] != self.state_dir:
            raise RuntimeError("freeze state directory differs from release state")
        external_env = quoted_env({"PUBLIC_DOMAIN": self.profile["public_domain"]})
        for host in ("dmit", "backup"):
            script = (MAINTENANCE_ROOT / "external-freeze-check.sh").read_bytes()
            base = "/root" if host == "dmit" else "/srv/sub2api-backups"
            temp_dir = self.runner.create_temp_dir(host, base, "release-check")
            remote = f"{temp_dir}/external-freeze-check.sh"
            self.runner.upload(host, script, remote, 0o700)
            try:
                self.run_remote(host, f"{external_env} {remote}", {"public_health_blocked"}, timeout=30)
            finally:
                self.run_remote(host, f"rm -rf {temp_dir} && printf 'cleanup=true\\n'", {"cleanup"})
        self.stage("writes_frozen", values)

    def backup(self) -> None:
        self.stage("backup", timeout=600)
        if self.backup_values is None:
            raise RuntimeError("freeze and backup stage did not return evidence")
        values = self.backup_values
        promotion_script = MAINTENANCE_ROOT / "promote-backup.sh"
        temp_dir = self.runner.create_temp_dir("backup", "/srv/sub2api-backups", "release-promote")
        remote = f"{temp_dir}/promote-backup.sh"
        self.runner.upload_file("backup", promotion_script, remote, 0o700)
        try:
            promote_env = quoted_env(
                {
                    "RELEASE_ID": self.release_id,
                    "TRANSPORT_ARTIFACT_NAME": values["transport_artifact"],
                    "ARTIFACT_SHA256": values["artifact_sha256"],
                    "MINIMUM_FREE_BYTES": self.profile["minimum_backup_free_bytes"],
                }
            )
            try:
                promoted = self.run_remote(
                    "backup",
                    f"{promote_env} {remote}",
                    {"backup_promotion", "release_artifact", "release_sha256", "release_free_bytes"},
                    timeout=600,
                )
            except BaseException:
                promoted = self.run_remote(
                    "backup",
                    f"{promote_env} {remote}",
                    {"backup_promotion", "release_artifact", "release_sha256", "release_free_bytes"},
                    timeout=600,
                )
        finally:
            self.run_remote("backup", f"rm -rf {temp_dir} && printf 'cleanup=true\\n'", {"cleanup"})
        if promoted["release_sha256"] != values["artifact_sha256"]:
            raise RuntimeError("promoted recovery point checksum differs from RackNerd")
        self.stage("backup_verified", {**values, **promoted})

    def switch(self) -> None:
        self.stage("migration_and_switch", timeout=1200)
        self.migration_started = True
        env = quoted_env({"RELEASE_DIR": self.release_dir})
        values = self.run_remote(
            "racknerd",
            f"{env} {self.active_assets}/switch.sh",
            {
                "migration_verified", "running_image_id", "internal_health", "public_traffic_enabled",
                "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
            },
            timeout=1200,
        )
        self.stage("candidate_internal_verified", values)

    def verify_and_finalize(self) -> None:
        self.stage("public_route_verification", timeout=3300)
        expose_env = quoted_env({"RELEASE_DIR": self.release_dir})
        self.public_exposed = True
        self.run_remote("racknerd", f"{expose_env} {self.active_assets}/expose.sh", {"public_traffic_enabled"})
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
        final = self.run_remote(
            "racknerd",
            f"{finalize_env} {self.active_assets}/finalize.sh",
            {
                "auto_sync_enabled", "running_image_id", "final_health", "final_logs",
                "prompt_audit_disabled", "prompt_audit_jobs", "prompt_audit_events",
            },
            timeout=600,
        )
        external_final = self.run_remote(
            "backup",
            f"test $(curl -sS --resolve {self.profile['public_domain']}:443:{self.profile['dmit_public_ip']} --max-time 15 -o /dev/null -w '%{{http_code}}' https://{self.profile['public_domain']}/health) = 200 && printf 'dmit_final_health=pass\\n'",
            {"dmit_final_health"},
        )
        restore_env = quoted_env({"STATE_ROOT": "/opt/sub2api/backups/release-state", "STATE_DIR": self.state_dir})
        self.run_remote("racknerd", f"{restore_env} {self.active_assets}/restore-backup-units.sh", {"backup_units_restored"})
        self.units_masked = False
        consume_env = quoted_env({"RELEASE_DIR": self.release_dir})
        cleaned = self.run_remote(
            "racknerd",
            f"{consume_env} {self.active_assets}/cleanup-state.sh",
            {"plaintext_state_removed"},
        )
        consumed = self.run_remote("racknerd", f"{consume_env} {self.active_assets}/consume.sh", {"gate_consumed"})
        self.result["status"] = "verified"
        self.stage("production_verified", {**verified, **direct, **dmit, **attribution, **final, **external_final, **consumed, **cleaned})

    def recover(self) -> None:
        self.stage("recovery_started")
        if not self.migration_started and not self.frozen:
            remote_frozen = self.remote_writes_frozen()
            if remote_frozen is None:
                raise RuntimeError("remote write-freeze state is unknown")
            self.frozen = remote_frozen
        if self.migration_started:
            env = quoted_env({"RELEASE_DIR": self.release_dir})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.active_assets}/restore.sh",
                {"coordinated_restore", "restored_image_id", "application_health"},
                timeout=2400,
            )
        elif self.frozen:
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
            unit_values = self.run_remote("racknerd", f"{restore_env} {self.active_assets}/restore-backup-units.sh", {"backup_units_restored"})
            values.update(unit_values)
            self.units_masked = False
        cleaned = self.run_remote(
            "racknerd",
            f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.active_assets}/cleanup-state.sh",
            {"plaintext_state_removed"},
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
                f"test -f {self.release_dir}/.recovered/marker && test -f {self.release_dir}/.recovered/plaintext-cleaned && test ! -e /opt/sub2api/releases/.active-release && test $(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api) = healthy && test $(systemctl is-enabled sub2api-backup.timer) = enabled && printf 'release_claim_reconciled=true\\n'",
                {"release_claim_reconciled"},
            )
        values.update(reconciled)
        self.result["status"] = "recovered"
        self.stage("recovered", values)

    def remote_writes_frozen(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                f"if test -f {self.state_dir}/pre-image-id && test -f {self.state_dir}/SHA256SUMS && test $(docker inspect -f '{{{{.State.Status}}}}' sub2api) != running && test $(systemctl is-active nginx 2>/dev/null || true) != active; then printf 'writes_frozen=true\\n'; else printf 'writes_frozen=false\\n'; fi",
                {"writes_frozen"},
            )
        except BaseException:
            return None
        return values.get("writes_frozen") == "true"

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

    def emergency_close(self) -> None:
        self.run_remote(
            "racknerd",
            f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.active_assets}/emergency-close.sh",
            {"public_traffic_closed"},
        )

    def remote_gate_consumed(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                f"test -f {self.release_dir}/.consumed/marker && test -f {self.release_dir}/.consumed/plaintext-cleaned && test ! -e /opt/sub2api/releases/.active-release && grep -Fxq 'candidate_image_id={self.image_id}' {self.release_dir}/.consumed/marker && test $(docker inspect -f '{{{{.Image}}}}' sub2api) = {self.image_id} && test $(docker inspect -f '{{{{.State.Health.Status}}}}' sub2api) = healthy && test $(systemctl is-enabled sub2api-backup.timer) = enabled && printf 'gate_consumed=true\\n'",
                {"gate_consumed"},
            )
        except BaseException:
            return None
        return values.get("gate_consumed") == "true"

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
            self.backup()
            self.switch()
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
                public_close = "not_required"
                if self.public_exposed:
                    try:
                        self.emergency_close()
                        public_close = "closed"
                    except BaseException:
                        public_close = "unknown"
                self.stage("gate_consumption_status_unknown", {"public_close": public_close})
                raise
            if gate_consumed:
                self.result["status"] = "verified"
                self.stage("production_verified_after_reconciliation", {"gate_consumed": "true"})
                return
            if self.public_exposed:
                try:
                    self.emergency_close()
                finally:
                    self.result["status"] = "blocked_reconciliation"
                    self.stage("public_exposure_requires_reconciliation")
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
