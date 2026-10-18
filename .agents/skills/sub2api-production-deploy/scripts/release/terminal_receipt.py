"""Exclusive, immutable success witness for new local worker generations."""

from __future__ import annotations

import hashlib
import os
import tempfile
from pathlib import Path

from .atomic import _create_absent, canonical_json
from .recovery_evidence import read_regular_bytes, strict_json
from .worker_errors import preserve_secondary


RECEIPT_NAME = "runner-terminal-receipt.json"
RECEIPT_SCHEMA = 1
_FIELDS = {"schema", "release_id", "terminal_status", "exit_code", "runner_sha256"}


def publish_terminal_receipt(run_dir: Path, runner: dict, runner_raw: bytes) -> None:
    # Explicit compatibility boundary: historical runner bytes are never
    # upgraded or given a retroactive success receipt.
    if "terminal_receipt_schema" not in runner:
        return
    if type(runner.get("terminal_receipt_schema")) is not int or runner["terminal_receipt_schema"] != RECEIPT_SCHEMA:
        raise RuntimeError("terminal_receipt_schema_invalid")
    if runner.get("status") not in {"verified", "recovered"}:
        return
    if (runner.get("release_id") != run_dir.name or type(runner.get("exit_code")) is not int
            or (runner["status"] == "verified" and runner["exit_code"] != 0)):
        raise RuntimeError("terminal_receipt_runner_invalid")
    if runner_raw != canonical_json(runner) + b"\n" or run_dir.is_symlink() or not run_dir.is_dir():
        raise RuntimeError("terminal_receipt_runner_invalid")
    receipt = {
        "schema": RECEIPT_SCHEMA, "release_id": run_dir.name, "terminal_status": runner["status"],
        "exit_code": runner["exit_code"], "runner_sha256": hashlib.sha256(runner_raw).hexdigest(),
    }
    descriptor, temporary_name = tempfile.mkstemp(prefix=".runner-terminal-receipt.", dir=run_dir)
    temporary = Path(temporary_name)
    # mkstemp is O_EXCL. Fully write, fsync and close before the last exclusive
    # publication. Failures preserve the staged witness; no final receipt exists.
    try:
        stream = os.fdopen(descriptor, "wb")
    except BaseException as error:
        try:
            os.close(descriptor)
        except BaseException as secondary:
            preserve_secondary(error, secondary, "close_receipt_descriptor")
        raise
    with stream:
        stream.write(canonical_json(receipt) + b"\n")
        stream.flush()
        os.fsync(stream.fileno())
    destination = run_dir / RECEIPT_NAME
    if os.name == "nt":
        # MoveFileExW with flags=0 never overwrites. No verification, cleanup,
        # logging or other operation which can raise follows native success.
        _create_absent(destination, temporary)
    else:
        os.link(temporary, destination)
        # A cleanup failure leaves two links. Readers reject that receipt and
        # report terminal_receipt_invalid instead of accepting worker success.
        temporary.unlink()


def terminal_receipt_status(run_dir: Path, runner: dict, runner_raw: bytes) -> str:
    if "terminal_receipt_schema" not in runner:
        return "legacy_schema_1"
    if type(runner.get("terminal_receipt_schema")) is not int or runner["terminal_receipt_schema"] != RECEIPT_SCHEMA:
        return "terminal_receipt_schema_invalid"
    if runner.get("status") not in {"verified", "recovered"}:
        return "not_required"
    if type(runner.get("exit_code")) is not int or (runner["status"] == "verified" and runner["exit_code"] != 0):
        return "terminal_receipt_runner_invalid"
    path = run_dir / RECEIPT_NAME
    try:
        if not path.exists() and not path.is_symlink():
            return "terminal_receipt_missing"
        if getattr(path.lstat(), "st_file_attributes", 0) & 0x400:
            return "terminal_receipt_invalid"
        receipt = strict_json(read_regular_bytes(path, maximum=2048))
    except (OSError, RuntimeError, ValueError):
        return "terminal_receipt_invalid"
    expected = {
        "schema": RECEIPT_SCHEMA, "release_id": run_dir.name, "terminal_status": runner.get("status"),
        "exit_code": runner.get("exit_code"), "runner_sha256": hashlib.sha256(runner_raw).hexdigest(),
    }
    if (set(receipt) != _FIELDS or type(receipt.get("schema")) is not int
            or type(receipt.get("exit_code")) is not int or receipt != expected
            or runner.get("release_id") != run_dir.name):
        return "terminal_receipt_mismatch"
    return "verified"


def require_terminal_receipt(run_dir: Path, runner: dict, runner_raw: bytes) -> None:
    status = terminal_receipt_status(run_dir, runner, runner_raw)
    if status not in {"legacy_schema_1", "not_required", "verified"}:
        raise RuntimeError(status)
