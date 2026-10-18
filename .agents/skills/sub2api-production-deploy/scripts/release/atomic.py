from __future__ import annotations

import json
import os
import tempfile
import hashlib
import stat
import secrets
from contextlib import contextmanager
from pathlib import Path
from typing import Any


def canonical_json(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("utf-8")


def _windows_api():
    import ctypes
    from ctypes import wintypes

    api = ctypes.WinDLL("kernel32", use_last_error=True)
    api.CreateFileW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD,
                               wintypes.LPVOID, wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE]
    api.CreateFileW.restype = wintypes.HANDLE
    api.CloseHandle.argtypes = [wintypes.HANDLE]
    api.CloseHandle.restype = wintypes.BOOL
    api.ReplaceFileW.argtypes = [wintypes.LPCWSTR, wintypes.LPCWSTR, wintypes.LPCWSTR,
                                wintypes.DWORD, wintypes.LPVOID, wintypes.LPVOID]
    api.ReplaceFileW.restype = wintypes.BOOL
    api.MoveFileExW.argtypes = [wintypes.LPCWSTR, wintypes.LPCWSTR, wintypes.DWORD]
    api.MoveFileExW.restype = wintypes.BOOL
    return api


def _windows_open(path: Path):
    import ctypes
    import msvcrt

    api = _windows_api()
    native_path = str(path.absolute())
    if "\0" in native_path:
        raise ValueError("embedded null character in state path")
    # OPEN_REPARSE_POINT allows us to reject a final symlink/junction rather
    # than silently following it. Parent/root checks remain the caller's duty.
    handle = api.CreateFileW(native_path, 0x80000000, 7, None, 3, 0x00200080, None)
    if handle == ctypes.c_void_p(-1).value:
        raise ctypes.WinError(ctypes.get_last_error())
    try:
        descriptor = msvcrt.open_osfhandle(handle, os.O_RDONLY | os.O_BINARY | getattr(os, "O_NOINHERIT", 0))
    except BaseException:
        api.CloseHandle(handle)
        raise
    # Ownership has transferred to the CRT; never CloseHandle after this point.
    try:
        return os.fdopen(descriptor, "rb", buffering=0)
    except BaseException:
        os.close(descriptor)
        raise


@contextmanager
def _regular_reader(path: Path, max_bytes: int | None):
    if max_bytes is not None and (type(max_bytes) is not int or max_bytes < 0):
        raise ValueError("invalid state read limit")
    stream = _windows_open(path) if os.name == "nt" else path.open("rb", buffering=0)
    with stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
            raise ValueError("state read requires a regular non-reparse file")
        limit = info.st_size if max_bytes is None else max_bytes
        if info.st_size > limit:
            raise ValueError("state file exceeds read limit")
        yield stream, limit


def read_replaceable_bytes(path: Path, max_bytes: int | None = None) -> bytes:
    """Read one shared-delete snapshot, bounded by limit or its initial size."""
    with _regular_reader(path, max_bytes) as (stream, limit):
        chunks, size = [], 0
        while True:
            chunk = stream.read(min(65536, limit - size + 1))
            if not chunk:
                return b"".join(chunks)
            size += len(chunk)
            if size > limit:
                raise ValueError("state file grew beyond read limit")
            chunks.append(chunk)


def read_replaceable_text(path: Path, max_bytes: int | None = None, encoding: str = "utf-8") -> str:
    return read_replaceable_bytes(path, max_bytes).decode(encoding)


def _snapshot(path: Path, max_bytes: int | None = None) -> tuple[int, str]:
    with _regular_reader(path, max_bytes) as (stream, limit):
        digest, size = hashlib.sha256(), 0
        while True:
            chunk = stream.read(min(65536, limit - size + 1))
            if not chunk:
                return size, digest.hexdigest()
            size += len(chunk)
            if size > limit:
                raise ValueError("state file grew beyond read limit")
            digest.update(chunk)


def hash_replaceable_file(path: Path, max_bytes: int | None = None) -> str:
    return _snapshot(path, max_bytes)[1]


def _replace_existing(target: Path, temporary: Path, backup: Path) -> None:
    import ctypes

    if not _windows_api().ReplaceFileW(str(target.absolute()), str(temporary.absolute()),
                                     str(backup.absolute()), 0, None, None):
        raise ctypes.WinError(ctypes.get_last_error())


def _create_absent(target: Path, temporary: Path) -> None:
    import ctypes

    # No REPLACE_EXISTING: a competing creator must never be overwritten.
    if not _windows_api().MoveFileExW(str(temporary.absolute()), str(target.absolute()), 0):
        raise ctypes.WinError(ctypes.get_last_error())


def _windows_commit(target: Path, temporary: Path, data: bytes) -> None:
    backup = None
    stage, committed = "snapshot", False
    try:
        try:
            original = _snapshot(target)
        except FileNotFoundError:
            stage = "create_absent"
            _create_absent(target, temporary)
        else:
            # A direct sibling avoids a delete-pending old reader preventing
            # removal of a containing backup directory after successful unlink.
            backup = target.with_name(f".{target.name}.backup.{secrets.token_hex(16)}")
            if os.path.lexists(backup):
                raise FileExistsError("atomic backup name already exists")
            stage = "replace_existing"
            _replace_existing(target, temporary, backup)
        # API success is already a committed write even if verification or
        # cleanup subsequently fails. Callers must not retry it as a no-op.
        committed = True
        stage = "verify_target"
        if _snapshot(target, len(data)) != (len(data), hashlib.sha256(data).hexdigest()):
            raise RuntimeError("atomic replacement target verification failed")
        if backup is not None:
            stage = "verify_backup"
            if _snapshot(backup, original[0]) != original:
                raise RuntimeError("atomic replacement backup verification failed")
            stage = "cleanup_backup"
            backup.unlink()
    except BaseException as error:
        # Preserve all available objects on *any* failure, especially native
        # 1175/1176/1177 intermediate states. Do not delete, restore or retry.
        def present(path):
            try:
                return path.exists() if path is not None else False
            except OSError:
                return None

        # Native replacement errors may already have moved the old target.
        # A nonoverwriting create refused with EXISTS has a known no-commit.
        uncertain = not committed and (
            stage == "replace_existing" or
            (stage == "create_absent" and getattr(error, "winerror", None) not in {80, 183})
        )
        try:
            error.atomic_write_stage = stage
            error.atomic_write_committed = None if uncertain else committed
            error.atomic_write_state_uncertain = uncertain
            error.atomic_write_objects = {"target": present(target), "temporary": present(temporary),
                                          "backup": present(backup)}
        except BaseException:
            pass  # Diagnostic attachment must never replace the original error.
        raise


def atomic_write(path: Path, data: bytes, mode: int = 0o600) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    descriptor, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary_path = Path(temporary_name)
    descriptor_open = True
    try:
        # Windows maps POSIX write bits to ACL/read-only state. Applying 0400
        # here can make release evidence unreadable to the next process even
        # though it runs as the same user. The parent release directory owns
        # the Windows access boundary; strict file modes remain enforced on
        # POSIX hosts where they have the intended semantics.
        if os.name != "nt":
            if hasattr(os, "fchmod"):
                os.fchmod(descriptor, mode)
            else:
                os.chmod(temporary_path, mode)
        with os.fdopen(descriptor, "wb") as stream:
            descriptor_open = False
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        if os.name == "nt":
            _windows_commit(path, temporary_path, data)
        else:
            os.replace(temporary_path, path)
        if os.name != "nt":
            directory_descriptor = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(directory_descriptor)
            finally:
                os.close(directory_descriptor)
    except BaseException:
        if descriptor_open:
            os.close(descriptor)
        if os.name != "nt":
            temporary_path.unlink(missing_ok=True)
        raise
