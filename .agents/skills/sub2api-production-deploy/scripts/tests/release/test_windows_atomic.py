"""Windows native I/O tests; intermediate error layouts are explicit models."""
from __future__ import annotations

from contextlib import contextmanager
import ctypes
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import atomic
from release.process import _windows_options

WINDOWS = pytest.mark.skipif(os.name != "nt", reason="requires real Windows native APIs")


def test_snapshot_reads_text_and_stream_hash_with_explicit_and_initial_bounds(tmp_path):
    path = tmp_path / "state"
    path.write_bytes("测试状态".encode())
    assert atomic.read_replaceable_text(path) == "测试状态"
    assert atomic.read_replaceable_bytes(path, path.stat().st_size) == path.read_bytes()
    assert atomic.hash_replaceable_file(path) == hashlib.sha256(path.read_bytes()).hexdigest()
    with pytest.raises(ValueError, match="exceeds"):
        atomic.read_replaceable_bytes(path, 1)
    for limit in (-1, True, 1.5):
        with pytest.raises(ValueError, match="invalid"):
            atomic.read_replaceable_bytes(path, limit)


@pytest.mark.parametrize("operation", [atomic.read_replaceable_bytes, atomic.hash_replaceable_file])
def test_initial_handle_size_is_a_growth_bound(tmp_path, operation):
    path = tmp_path / "state"
    path.write_bytes(b"old")
    original = atomic._regular_reader

    @contextmanager
    def growing(path, max_bytes):
        with original(path, max_bytes) as pair:
            with path.open("ab") as writer:
                writer.write(b"growth")
            yield pair

    with mock.patch.object(atomic, "_regular_reader", growing):
        with pytest.raises(ValueError, match="grew"):
            operation(path)


def test_regular_file_only_and_invalid_text(tmp_path):
    with pytest.raises((ValueError, OSError)):
        atomic.read_replaceable_bytes(tmp_path)
    path = tmp_path / "bad"
    path.write_bytes(b"\xff")
    with pytest.raises(UnicodeError):
        atomic.read_replaceable_text(path)
    with pytest.raises(ValueError, match="null"):
        atomic.read_replaceable_bytes(Path(str(path) + "\0suffix"))


def test_bounded_read_reassembles_short_system_reads(tmp_path):
    path = tmp_path / "state"
    path.write_bytes(b"complete")
    original = atomic._regular_reader

    class ShortReader:
        def __init__(self, stream):
            self.stream = stream

        def read(self, size):
            return self.stream.read(min(2, size))

    @contextmanager
    def short_reader(path, limit):
        with original(path, limit) as (stream, bound):
            yield ShortReader(stream), bound

    with mock.patch.object(atomic, "_regular_reader", short_reader):
        assert atomic.read_replaceable_bytes(path) == b"complete"


READER = r'''
import json,sys
from pathlib import Path
sys.path.insert(0,sys.argv[1])
from release import atomic
with atomic._windows_open(Path(sys.argv[2])) if sys.argv[3]=='shared' else open(sys.argv[2],'rb',buffering=0) as stream:
    first=stream.read(2)
    print('ready',flush=True)
    assert sys.stdin.readline().strip()=='continue'
    print(json.dumps((first+stream.read()).decode()),flush=True)
'''


@contextmanager
def reader_process(path, shared=True):
    child = subprocess.Popen([sys.executable, "-B", "-c", READER,
                              str(Path(atomic.__file__).resolve().parent.parent), str(path),
                              "shared" if shared else "ordinary"], stdin=subprocess.PIPE,
                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                             **_windows_options())
    try:
        assert child.stdout.readline().strip() == "ready"
        yield child
    finally:
        if child.poll() is None:
            child.kill()
            child.communicate()


def finish_reader(child):
    output, error = child.communicate("continue\n", timeout=15)
    assert child.returncode == 0, error
    return json.loads(output)


@WINDOWS
def test_real_multiprocess_reader_keeps_old_snapshot_after_successful_cleanup(tmp_path):
    path = tmp_path / "state"
    path.write_bytes(b"old-complete")
    with reader_process(path) as child:
        atomic.atomic_write(path, b"new-complete")
        assert atomic.read_replaceable_bytes(path) == b"new-complete"
        assert not list(tmp_path.glob(".state.backup.*"))
        assert finish_reader(child) == "old-complete"


@WINDOWS
def test_real_ordinary_reader_refuses_and_preserves_target_temporary_backup_state(tmp_path):
    path = tmp_path / "state"
    path.write_bytes(b"old-complete")
    with reader_process(path, shared=False) as child:
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(path, b"new-complete")
        assert caught.value.winerror in (5, 32, 33)
        assert caught.value.atomic_write_committed is None
        assert caught.value.atomic_write_state_uncertain is True
        assert path.read_bytes() == b"old-complete"
        assert not list(tmp_path.glob(".state.backup.*"))
        assert [p.read_bytes() for p in tmp_path.glob(".state.*")] == [b"new-complete"]
        assert finish_reader(child) == "old-complete"


@WINDOWS
def test_real_initial_create_and_competing_creator_never_overwrite(tmp_path):
    path = tmp_path / "state"
    atomic.atomic_write(path, b"created")
    assert path.read_bytes() == b"created"
    path.unlink()
    original = atomic._create_absent

    def competing(target, temporary):
        target.write_bytes(b"other-writer")
        original(target, temporary)

    with mock.patch.object(atomic, "_create_absent", competing):
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(path, b"new")
    assert caught.value.winerror in (80, 183)
    assert path.read_bytes() == b"other-writer"
    assert [p.read_bytes() for p in tmp_path.glob(".state.*")] == [b"new"]


@WINDOWS
@pytest.mark.parametrize("code", [1175, 1176, 1177, 5, 87])
def test_modeled_native_failure_layouts_preserve_all_available_objects(tmp_path, code):
    """Materialized layouts, NOT actual OS induction of 1175/1176/1177."""
    target = tmp_path / "state"
    target.write_bytes(b"old")
    seen = {}

    def modeled_replace(target, temporary, backup):
        if code == 1177:
            target.rename(backup)
        seen.update(temporary=temporary, backup=backup)
        raise ctypes.WinError(code)

    with mock.patch.object(atomic, "_replace_existing", modeled_replace):
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(target, b"new")
    assert caught.value.winerror == code
    assert caught.value.atomic_write_stage == "replace_existing"
    assert caught.value.atomic_write_committed is None
    assert caught.value.atomic_write_state_uncertain is True
    assert seen["temporary"].read_bytes() == b"new"
    assert (seen["backup"] if code == 1177 else target).read_bytes() == b"old"


@WINDOWS
def test_successful_write_cleanup_error_is_committed_and_not_retried(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    unlink = Path.unlink

    def fail_backup(path, *args, **kwargs):
        if ".backup." in path.name:
            raise ctypes.WinError(5)
        return unlink(path, *args, **kwargs)

    with mock.patch.object(Path, "unlink", fail_backup), mock.patch.object(atomic, "_replace_existing", wraps=atomic._replace_existing) as replace:
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(target, b"new")
    assert caught.value.atomic_write_committed is True
    assert caught.value.atomic_write_stage == "cleanup_backup"
    assert replace.call_count == 1
    assert target.read_bytes() == b"new"
    assert [p.read_bytes() for p in tmp_path.glob(".state.backup.*")] == [b"old"]


@WINDOWS
def test_native_error_survives_failed_metadata_diagnostics(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    native = ctypes.WinError(1177)

    def fail_replace(*args):
        raise native

    with mock.patch.object(atomic, "_replace_existing", fail_replace), mock.patch.object(Path, "exists", side_effect=ctypes.WinError(5)):
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(target, b"new")
    assert caught.value is native and caught.value.winerror == 1177
    assert caught.value.atomic_write_objects == {"target": None, "temporary": None, "backup": None}


@WINDOWS
def test_readonly_target_refuses_without_changing_permissions(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    target.chmod(0o400)
    try:
        with pytest.raises(OSError) as caught:
            atomic.atomic_write(target, b"new")
        assert caught.value.winerror == 5
        assert target.read_bytes() == b"old"
        assert target.stat().st_file_attributes & 1
        assert [p.read_bytes() for p in tmp_path.glob(".state.*")] == [b"new"]
    finally:
        target.chmod(0o600)


@WINDOWS
def test_backup_name_collision_never_overwrites_existing_object(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    backup = tmp_path / (".state.backup." + "a" * 32)
    backup.write_bytes(b"unrelated")
    with mock.patch.object(atomic.secrets, "token_hex", return_value="a" * 32), mock.patch.object(atomic, "_replace_existing") as replace:
        with pytest.raises(FileExistsError):
            atomic.atomic_write(target, b"new")
    replace.assert_not_called()
    assert backup.read_bytes() == b"unrelated" and target.read_bytes() == b"old"


@WINDOWS
def test_target_verification_failure_is_committed_and_preserves_backup(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    real_replace = atomic._replace_existing

    def corrupt_after_success(target, temporary, backup):
        real_replace(target, temporary, backup)
        target.write_bytes(b"unexpected")

    with mock.patch.object(atomic, "_replace_existing", corrupt_after_success):
        with pytest.raises(ValueError) as caught:
            atomic.atomic_write(target, b"new")
    assert caught.value.atomic_write_committed is True
    assert caught.value.atomic_write_stage == "verify_target"
    assert [p.read_bytes() for p in tmp_path.glob(".state.backup.*")] == [b"old"]


@WINDOWS
def test_open_handle_follows_old_snapshot_after_path_replacement(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"old")
    original = atomic._regular_reader

    @contextmanager
    def replacing(path, max_bytes):
        with original(path, max_bytes) as pair:
            with mock.patch.object(atomic, "_regular_reader", original):
                atomic.atomic_write(path, b"new")
            yield pair

    with mock.patch.object(atomic, "_regular_reader", replacing):
        assert atomic.read_replaceable_bytes(target) == b"old"
    assert atomic.read_replaceable_bytes(target) == b"new"


@WINDOWS
def test_windows_reparse_is_not_followed(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"state")
    link = tmp_path / "link"
    try:
        link.symlink_to(target)
    except OSError as error:
        pytest.skip(f"symlink unavailable: {error.winerror}")
    with pytest.raises((ValueError, OSError)):
        atomic.read_replaceable_bytes(link)


@WINDOWS
def test_crt_conversion_failure_closes_native_handle_once(tmp_path):
    import msvcrt
    target = tmp_path / "state"
    target.write_bytes(b"x")
    api = atomic._windows_api()
    close = api.CloseHandle
    with mock.patch.object(api, "CloseHandle", wraps=close) as closing, mock.patch.object(atomic, "_windows_api", return_value=api), mock.patch.object(msvcrt, "open_osfhandle", side_effect=OSError("conversion failed")):
        with pytest.raises(OSError, match="conversion failed"):
            atomic._windows_open(target)
    assert closing.call_count == 1


@WINDOWS
def test_fdopen_failure_closes_crt_descriptor_once(tmp_path):
    target = tmp_path / "state"
    target.write_bytes(b"x")
    with mock.patch.object(os, "fdopen", side_effect=OSError("fdopen failed")), mock.patch.object(os, "close", wraps=os.close) as closing:
        with pytest.raises(OSError, match="fdopen failed"):
            atomic._windows_open(target)
    assert closing.call_count == 1


@WINDOWS
def test_repeated_reads_writes_and_failures_do_not_leak_handles(tmp_path):
    from ctypes import wintypes
    api = ctypes.WinDLL("kernel32", use_last_error=True)
    api.GetCurrentProcess.restype = wintypes.HANDLE
    api.GetProcessHandleCount.argtypes = [wintypes.HANDLE, ctypes.POINTER(wintypes.DWORD)]
    api.GetProcessHandleCount.restype = wintypes.BOOL

    def count():
        value = wintypes.DWORD()
        assert api.GetProcessHandleCount(api.GetCurrentProcess(), ctypes.byref(value))
        return value.value

    target = tmp_path / "state"
    atomic.atomic_write(target, b"x")
    atomic.read_replaceable_bytes(target)
    before = count()
    for _ in range(50):
        atomic.atomic_write(target, b"y")
        assert atomic.read_replaceable_bytes(target) == b"y"
        with pytest.raises(ValueError):
            atomic.read_replaceable_bytes(target, 0)
    assert count() <= before + 2
