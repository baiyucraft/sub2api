from __future__ import annotations

import json
import os
import stat
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from .atomic import atomic_write, canonical_json, read_replaceable_text


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
        return cls(path, json.loads(read_replaceable_text(path)))

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
    def __init__(self, path: Path, *, require_existing: bool = False):
        self.path = path
        self.descriptor: int | None = None
        self.require_existing = require_existing

    @staticmethod
    def _identity(info: os.stat_result) -> tuple[int, int]:
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1
                or getattr(info, "st_file_attributes", 0) & 0x400):
            raise RuntimeError("release_lock_file_unsafe")
        return info.st_dev, info.st_ino

    def _verify_existing(self, expected: tuple[int, int]) -> None:
        if (self._identity(self.path.lstat()) != expected
                or self._identity(os.fstat(self.descriptor)) != expected):
            raise RuntimeError("release_lock_identity_changed")

    def __enter__(self) -> "RunLock":
        expected = self._identity(self.path.lstat()) if self.require_existing else None
        if not self.require_existing:
            self.path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        flags = os.O_RDWR | (getattr(os, "O_NOFOLLOW", 0) if self.require_existing else os.O_CREAT)
        self.descriptor = os.open(self.path, flags, 0o600)
        try:
            if expected is not None:
                self._verify_existing(expected)
            try:
                if os.name == "nt":
                    import msvcrt

                    os.lseek(self.descriptor, 0, os.SEEK_SET)
                    if not self.require_existing and os.fstat(self.descriptor).st_size == 0:
                        os.write(self.descriptor, b"\0")
                    os.lseek(self.descriptor, 0, os.SEEK_SET)
                    msvcrt.locking(self.descriptor, msvcrt.LK_NBLCK, 1)
                else:
                    import fcntl

                    fcntl.flock(self.descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except (OSError, BlockingIOError) as error:
                raise RuntimeError("another release process is running") from error
            if expected is not None:
                self._verify_existing(expected)
            os.ftruncate(self.descriptor, 1)
            os.lseek(self.descriptor, 1, os.SEEK_SET)
            os.write(self.descriptor, f"pid={os.getpid()}\n".encode())
            os.fsync(self.descriptor)
            if expected is not None:
                self._verify_existing(expected)
        except BaseException:
            os.close(self.descriptor)
            self.descriptor = None
            raise
        return self

    def __exit__(self, exc_type, exc, traceback) -> None:
        if self.descriptor is not None:
            descriptor, self.descriptor = self.descriptor, None
            try:
                if os.name == "nt":
                    import msvcrt

                    os.lseek(descriptor, 0, os.SEEK_SET)
                    msvcrt.locking(descriptor, msvcrt.LK_UNLCK, 1)
                else:
                    import fcntl

                    fcntl.flock(descriptor, fcntl.LOCK_UN)
            finally:
                os.close(descriptor)
