from __future__ import annotations

import argparse
import json
import shlex
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


def quoted_env(values: dict[str, str | int]) -> str:
    return " ".join(f"{key}={shlex.quote(str(value))}" for key, value in values.items())


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

    def stage(self, name: str, evidence: dict[str, str] | None = None) -> None:
        self.result["stage"] = name
        event: dict[str, object] = {"stage": name}
        if evidence:
            event["evidence"] = evidence
        history = self.result["history"]
        assert isinstance(history, list)
        history.append(event)
        self._save_result()

    def run_remote(self, host: str, script: str, allowed: set[str], timeout: int = 300) -> dict[str, str]:
        return self.runner.run(host, script, allowed, timeout=timeout).values

    def upload_assets(self) -> None:
        self.stage("stage_assets")
        trust_sha = sha256_file(TRUSTED_KEY)
        trust = self.run_remote(
            "racknerd",
            f"test -f /opt/sub2api-release-trust/vm-gate-ed25519.pub && test $(sha256sum /opt/sub2api-release-trust/vm-gate-ed25519.pub | awk '{{print $1}}') = {trust_sha} && printf 'trust_key_verified=true\\n'",
            {"trust_key_verified"},
        )
        self.run_remote(
            "racknerd",
            f"test ! -e {shlex.quote(self.release_dir)} && install -d -m 700 {shlex.quote(self.release_dir)} && printf 'release_directory_created=true\\n'",
            {"release_directory_created"},
        )
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
        self.run_remote("racknerd", f"install -d -m 700 {shlex.quote(self.release_dir + '/assets')} && printf 'asset_directory_created=true\\n'", {"asset_directory_created"})
        checksum_lines: list[str] = []
        for relative, path in files.items():
            remote = f"{self.release_dir}/{relative}"
            mode = 0o700 if path.suffix == ".sh" else 0o400
            self.runner.upload_file("racknerd", path, remote, mode)
            checksum_lines.append(f"{sha256_file(path)}  {relative}")
        checksum_document = ("\n".join(checksum_lines) + "\n").encode()
        self.runner.upload("racknerd", checksum_document, f"{self.release_dir}/ASSET_SHA256SUMS", 0o400)
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
            f"{env} {self.release_dir}/assets/preflight.sh",
            {"preflight", "pre_switch_image_id", "free_bytes", "migration_absent"},
        )
        self.stage("production_preflight_verified", values)

    def freeze(self) -> None:
        self.stage("freeze")
        self.mask_intent = True
        freeze_env = quoted_env({"RELEASE_DIR": self.release_dir})
        self.frozen = True
        values = self.run_remote(
            "racknerd",
            f"{freeze_env} {self.release_dir}/assets/freeze-backup.sh",
            {
                "backup_units_masked", "writes_frozen", "state_dir", "pre_switch_image_id", "compose_sha256",
                "artifact", "transport_artifact", "artifact_size", "artifact_sha256", "no_restart_path_proven",
            },
            timeout=2400,
        )
        self.units_masked = True
        self.backup_values = values
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
        self.stage("backup")
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
        self.stage("migration_and_switch")
        self.migration_started = True
        env = quoted_env({"RELEASE_DIR": self.release_dir})
        values = self.run_remote(
            "racknerd",
            f"{env} {self.release_dir}/assets/switch.sh",
            {"migration_verified", "running_image_id", "internal_health", "public_traffic_enabled"},
            timeout=1200,
        )
        self.stage("candidate_internal_verified", values)

    def verify_and_finalize(self) -> None:
        expose_env = quoted_env({"RELEASE_DIR": self.release_dir})
        self.public_exposed = True
        self.run_remote("racknerd", f"{expose_env} {self.release_dir}/assets/expose.sh", {"public_traffic_enabled"})
        verify_env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "PUBLIC_DOMAIN": self.profile["public_domain"],
                "DIRECT_IP": self.profile["rack_public_ip"],
                "DMIT_IP": self.profile["dmit_public_ip"],
            }
        )
        verified = self.run_remote(
            "racknerd",
            f"{verify_env} {self.release_dir}/assets/verify.sh",
            {"direct_health", "dmit_health", "streaming", "real_client_ip", "underscore_header_path", "two_mib_reached_app", "startup_logs", "canary_usage_recorded"},
            timeout=600,
        )
        finalize_env = quoted_env(
            {
                "RELEASE_DIR": self.release_dir,
                "PUBLIC_DOMAIN": self.profile["public_domain"],
                "DIRECT_IP": self.profile["rack_public_ip"],
                "DMIT_IP": self.profile["dmit_public_ip"],
            }
        )
        final = self.run_remote(
            "racknerd",
            f"{finalize_env} {self.release_dir}/assets/finalize.sh",
            {"auto_sync_enabled", "running_image_id", "final_health", "final_logs"},
            timeout=600,
        )
        restore_env = quoted_env({"STATE_ROOT": "/opt/sub2api/backups/release-state", "STATE_DIR": self.state_dir})
        self.run_remote("racknerd", f"{restore_env} {self.release_dir}/assets/restore-backup-units.sh", {"backup_units_restored"})
        self.units_masked = False
        consume_env = quoted_env({"RELEASE_DIR": self.release_dir})
        cleaned = self.run_remote(
            "racknerd",
            f"{consume_env} {self.release_dir}/assets/cleanup-state.sh",
            {"plaintext_state_removed"},
        )
        consumed = self.run_remote("racknerd", f"{consume_env} {self.release_dir}/assets/consume.sh", {"gate_consumed"})
        self.stage("production_verified", {**verified, **final, **consumed, **cleaned})

    def recover(self) -> None:
        self.stage("recovery_started")
        if self.migration_started:
            env = quoted_env({"RELEASE_DIR": self.release_dir})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.release_dir}/assets/restore.sh",
                {"coordinated_restore", "restored_image_id", "application_health"},
                timeout=2400,
            )
        elif self.frozen:
            env = quoted_env({"RELEASE_DIR": self.release_dir})
            values = self.run_remote(
                "racknerd",
                f"{env} {self.release_dir}/assets/resume-old.sh",
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
            unit_values = self.run_remote("racknerd", f"{restore_env} {self.release_dir}/assets/restore-backup-units.sh", {"backup_units_restored"})
            values.update(unit_values)
            self.units_masked = False
        cleaned = self.run_remote(
            "racknerd",
            f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.release_dir}/assets/cleanup-state.sh",
            {"plaintext_state_removed"},
        )
        values.update(cleaned)
        try:
            reconciled = self.run_remote(
                "racknerd",
                f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.release_dir}/assets/reconcile.sh",
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
            f"{quoted_env({'RELEASE_DIR': self.release_dir})} {self.release_dir}/assets/emergency-close.sh",
            {"public_traffic_closed"},
        )

    def remote_gate_consumed(self) -> bool:
        try:
            values = self.run_remote(
                "racknerd",
                f"test -f {self.release_dir}/.consumed/marker && test -f {self.release_dir}/.consumed/plaintext-cleaned && test ! -e /opt/sub2api/releases/.active-release && grep -Fxq 'candidate_image_id={self.image_id}' {self.release_dir}/.consumed/marker && test $(docker inspect -f '{{{{.Image}}}}' sub2api) = {self.image_id} && test $(systemctl is-enabled sub2api-backup.timer) = enabled && printf 'gate_consumed=true\\n'",
                {"gate_consumed"},
            )
        except BaseException:
            return False
        return values.get("gate_consumed") == "true"

    def remote_gate_claimed(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                f"test -d {self.release_dir}/.claimed && test -f /opt/sub2api/releases/.active-release/release_id && grep -Fxq 'release_id={self.release_id}' /opt/sub2api/releases/.active-release/release_id && printf 'gate_claimed=true\\n'",
                {"gate_claimed"},
            )
        except BaseException:
            return None
        return values.get("gate_claimed") == "true"

    def remote_active_claim_exists(self) -> bool | None:
        try:
            values = self.run_remote(
                "racknerd",
                "test -d /opt/sub2api/releases/.active-release && printf 'active_claim=true\\n'",
                {"active_claim"},
            )
        except BaseException:
            return None
        return values.get("active_claim") == "true"

    def execute(self) -> None:
        try:
            self.upload_assets()
            self.preflight()
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
            if self.remote_gate_consumed():
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
