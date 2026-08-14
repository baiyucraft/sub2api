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
        self.descriptor = os.open(self.path, os.O_CREAT | os.O_RDWR, 0o600)
        try:
            if os.name == "nt":
                import msvcrt

                os.lseek(self.descriptor, 0, os.SEEK_SET)
                if os.fstat(self.descriptor).st_size == 0:
                    os.write(self.descriptor, b"\0")
                os.lseek(self.descriptor, 0, os.SEEK_SET)
                msvcrt.locking(self.descriptor, msvcrt.LK_NBLCK, 1)
            else:
                import fcntl

                fcntl.flock(self.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except (OSError, BlockingIOError) as error:
            os.close(self.descriptor)
            self.descriptor = None
            raise RuntimeError("another release process is running") from error
        os.ftruncate(self.descriptor, 1)
        os.lseek(self.descriptor, 1, os.SEEK_SET)
        os.write(self.descriptor, f"pid={os.getpid()}\n".encode())
        os.fsync(self.descriptor)
        return self

    def __exit__(self, exc_type, exc, traceback) -> None:
        if self.descriptor is not None:
            if os.name == "nt":
                import msvcrt

                os.lseek(self.descriptor, 0, os.SEEK_SET)
                msvcrt.locking(self.descriptor, msvcrt.LK_UNLCK, 1)
            else:
                import fcntl

                fcntl.flock(self.descriptor, fcntl.LOCK_UN)
            os.close(self.descriptor)
