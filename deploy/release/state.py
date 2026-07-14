from __future__ import annotations

import json
import os
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json


TERMINAL_STATES = {"verified", "failed", "recovered", "blocked_reconciliation"}


@dataclass
class RunState:
    path: Path
    value: dict[str, Any]

    @classmethod
    def create(cls, path: Path, release_id: str) -> "RunState":
        state = cls(path, {"schema": 1, "release_id": release_id, "status": "not_started", "stage": "init", "history": []})
        state.save()
        return state

    @classmethod
    def load(cls, path: Path) -> "RunState":
        return cls(path, json.loads(path.read_text(encoding="utf-8")))

    def transition(self, stage: str, status: str, evidence: dict[str, Any] | None = None) -> None:
        if self.value["status"] in TERMINAL_STATES and status == "running":
            raise RuntimeError("terminal release state cannot be resumed without reconciliation")
        event = {"stage": stage, "status": status, "at": int(time.time())}
        if evidence:
            event["evidence"] = evidence
        self.value["stage"] = stage
        self.value["status"] = status
        self.value["history"].append(event)
        self.save()

    def save(self) -> None:
        atomic_write(self.path, canonical_json(self.value) + b"\n")


class RunLock:
    def __init__(self, path: Path):
        self.path = path
        self.descriptor: int | None = None

    def __enter__(self) -> "RunLock":
        self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        try:
            self.descriptor = os.open(self.path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        except FileExistsError as error:
            raise RuntimeError(f"release lock exists and requires manual reconciliation: {self.path}") from error
        os.write(self.descriptor, f"pid={os.getpid()}\n".encode())
        os.fsync(self.descriptor)
        return self

    def __exit__(self, exc_type, exc, traceback) -> None:
        if self.descriptor is not None:
            os.close(self.descriptor)
        if exc_type is None:
            self.path.unlink()
