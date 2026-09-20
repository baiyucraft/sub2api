from __future__ import annotations

import json
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json


PLUGIN_TERMINAL_STATES = {"verified", "failed", "recovered", "blocked_reconciliation"}
PLUGIN_STAGES = (
    "initialized",
    "package_built",
    "local_verified",
    "awaiting_vm_authorization",
    "vm_gate_verified",
    "awaiting_production_authorization",
    "production_preflight_verified",
    "install_or_upgrade_started",
    "installation_committed",
    "instances_verified",
    "verified",
)
_NEXT_STAGE = {current: following for current, following in zip(PLUGIN_STAGES, PLUGIN_STAGES[1:])}
_SENSITIVE_KEY_PARTS = {
    "api_key",
    "authorization",
    "cookie",
    "credential",
    "jwt",
    "password",
    "private_key",
    "refresh_token",
    "secret",
    "token",
    "totp",
}


def _reject_sensitive(value: Any, path: str = "evidence") -> None:
    if isinstance(value, dict):
        for key, item in value.items():
            normalized = str(key).lower()
            if any(part in normalized for part in _SENSITIVE_KEY_PARTS):
                raise ValueError(f"sensitive field is not allowed in plugin state: {path}.{key}")
            _reject_sensitive(item, f"{path}.{key}")
    elif isinstance(value, list):
        for index, item in enumerate(value):
            _reject_sensitive(item, f"{path}[{index}]")


@dataclass
class PluginRunState:
    path: Path
    value: dict[str, Any]

    @classmethod
    def create(cls, path: Path, release_id: str, plugin_id: str, source_commit: str) -> "PluginRunState":
        value = {
            "schema": 1,
            "release_id": release_id,
            "plugin_id": plugin_id,
            "source_commit": source_commit,
            "stage": "initialized",
            "status": "running",
            "generation": 1,
            "history": [
                {"stage": "initialized", "status": "running", "at": int(time.time()), "generation": 1}
            ],
        }
        state = cls(path, value)
        state.save()
        return state

    @classmethod
    def load(cls, path: Path) -> "PluginRunState":
        raw = json.loads(path.read_text(encoding="utf-8"))
        if (
            not isinstance(raw, dict)
            or raw.get("schema") != 1
            or raw.get("stage") not in PLUGIN_STAGES
            or raw.get("status") not in {"running", *PLUGIN_TERMINAL_STATES}
            or not isinstance(raw.get("generation"), int)
            or int(raw.get("generation", 0)) < 1
            or not isinstance(raw.get("history"), list)
        ):
            raise RuntimeError("invalid plugin release state")
        _reject_sensitive(raw)
        return cls(path, raw)

    def transition(
        self,
        stage: str,
        status: str = "running",
        evidence: dict[str, Any] | None = None,
        *,
        expected_generation: int | None = None,
    ) -> None:
        if expected_generation is not None and expected_generation != self.value.get("generation"):
            raise RuntimeError("plugin release state generation changed")
        current_stage = str(self.value.get("stage"))
        current_status = str(self.value.get("status"))
        if current_status in PLUGIN_TERMINAL_STATES:
            raise RuntimeError("terminal plugin release state cannot be resumed")
        if status == "verified":
            if stage != "verified" or stage != _NEXT_STAGE.get(current_stage):
                raise RuntimeError("verified plugin release must use the verified stage")
        elif status != "running":
            raise RuntimeError("terminal plugin release failures must use fail()")
        elif stage != _NEXT_STAGE.get(current_stage):
            raise RuntimeError(f"illegal plugin release transition: {current_stage} -> {stage}")
        if evidence is not None:
            _reject_sensitive(evidence)
        generation = int(self.value.get("generation", 0)) + 1
        event: dict[str, Any] = {
            "stage": stage,
            "status": status,
            "at": int(time.time()),
            "generation": generation,
        }
        if evidence:
            event["evidence"] = evidence
        self.value["stage"] = stage
        self.value["status"] = status
        self.value["generation"] = generation
        self.value.setdefault("history", []).append(event)
        self.save()

    def fail(self, stage: str, *, blocked: bool = False, evidence: dict[str, Any] | None = None) -> None:
        if self.value.get("status") in PLUGIN_TERMINAL_STATES:
            raise RuntimeError("terminal plugin release state cannot be changed")
        if stage != self.value.get("stage"):
            raise RuntimeError("plugin release failure must retain the current stage")
        if evidence is not None:
            _reject_sensitive(evidence)
        status = "blocked_reconciliation" if blocked else "failed"
        generation = int(self.value.get("generation", 0)) + 1
        event: dict[str, Any] = {
            "stage": stage,
            "status": status,
            "at": int(time.time()),
            "generation": generation,
        }
        if evidence:
            event["evidence"] = evidence
        self.value.update({"stage": stage, "status": status, "generation": generation})
        self.value.setdefault("history", []).append(event)
        self.save()

    def save(self) -> None:
        _reject_sensitive(self.value)
        atomic_write(self.path, canonical_json(self.value) + b"\n", 0o600)
