"""Centralized process spawning for the release runner.

The release runner must not create transient Windows consoles.  Keep this
module small and dependency-free so every release subcommand can reuse the
same Windows/Linux behavior.
"""

from __future__ import annotations

import os
import ctypes
from pathlib import Path
import subprocess
from typing import Any, Sequence


def _windows_options(*, new_process_group: bool = False, existing_flags: int = 0) -> dict[str, Any]:
    if os.name != "nt":
        return {}
    flags = existing_flags | getattr(subprocess, "CREATE_NO_WINDOW", 0)
    if new_process_group:
        flags |= getattr(subprocess, "CREATE_NEW_PROCESS_GROUP", 0)
    options: dict[str, Any] = {"creationflags": flags}
    startupinfo_type = getattr(subprocess, "STARTUPINFO", None)
    if startupinfo_type is not None:
        startupinfo = startupinfo_type()
        startupinfo.dwFlags |= getattr(subprocess, "STARTF_USESHOWWINDOW", 0)
        startupinfo.wShowWindow = getattr(subprocess, "SW_HIDE", 0)
        options["startupinfo"] = startupinfo
    return options


def run_hidden(command: Sequence[str], **kwargs: Any) -> subprocess.CompletedProcess[Any]:
    """Run a release child process without opening a console on Windows."""

    options = dict(kwargs)
    options.update(_windows_options(existing_flags=int(options.pop("creationflags", 0) or 0)))
    return subprocess.run(command, **options)


def check_output_hidden(command: Sequence[str], **kwargs: Any) -> bytes | str:
    """Read command output without allowing a console window to appear."""

    options = dict(kwargs)
    options.update(_windows_options(existing_flags=int(options.pop("creationflags", 0) or 0)))
    return subprocess.check_output(command, **options)


def popen_detached_worker(command: Sequence[str], **kwargs: Any) -> subprocess.Popen[Any]:
    """Start the persistent worker with one process group and no console."""

    if "stdin" in kwargs:
        raise ValueError("detached workers cannot override stdin")
    options = {"close_fds": True, **kwargs}
    options["stdin"] = subprocess.DEVNULL
    if os.name == "nt":
        options.update(_windows_options(new_process_group=True, existing_flags=int(options.pop("creationflags", 0) or 0)))
    else:
        options.setdefault("start_new_session", True)
    return subprocess.Popen(command, **options)


def process_identity(pid: int) -> str | None:
    """Return a live process token; unknown observations fail closed."""
    if type(pid) is not int or pid <= 0:
        return None
    if os.name == "nt":
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        kernel.OpenProcess.argtypes = (ctypes.c_ulong, ctypes.c_int, ctypes.c_ulong)
        kernel.OpenProcess.restype = ctypes.c_void_p
        kernel.GetProcessTimes.argtypes = (ctypes.c_void_p,) + (ctypes.POINTER(ctypes.c_ulonglong),) * 4
        kernel.GetProcessTimes.restype = ctypes.c_int
        kernel.WaitForSingleObject.argtypes = (ctypes.c_void_p, ctypes.c_ulong)
        kernel.WaitForSingleObject.restype = ctypes.c_ulong
        kernel.CloseHandle.argtypes = (ctypes.c_void_p,)
        kernel.CloseHandle.restype = ctypes.c_int
        handle = kernel.OpenProcess(0x1000 | 0x100000, False, pid)
        if not handle:
            if ctypes.get_last_error() == 87:
                return None
            raise RuntimeError("worker identity query is uncertain")
        try:
            state = kernel.WaitForSingleObject(handle, 0)
            if state == 0:
                return None
            if state != 258:
                raise RuntimeError("worker identity query is uncertain")
            times = [ctypes.c_ulonglong() for _ in range(4)]
            if not kernel.GetProcessTimes(handle, *(ctypes.byref(value) for value in times)):
                raise RuntimeError("worker identity query is uncertain")
            state = kernel.WaitForSingleObject(handle, 0)
            if state == 0:
                return None
            if state != 258 or times[0].value <= 0:
                raise RuntimeError("worker identity query is uncertain")
            return f"win:{times[0].value}"
        finally:
            kernel.CloseHandle(handle)
    try:
        content = Path(f"/proc/{pid}/stat").read_text(encoding="ascii")
    except FileNotFoundError:
        return None
    except (OSError, UnicodeError):
        raise RuntimeError("worker identity query is uncertain") from None
    try:
        fields = content.rsplit(")", 1)[1].split()
        if fields[0] in {"Z", "X", "x"}:
            return None
        start = int(fields[19])
        boot = Path("/proc/sys/kernel/random/boot_id").read_text(encoding="ascii").strip()
        if start <= 0 or not boot:
            raise ValueError("invalid identity")
        return f"linux:{boot}:{start}"
    except (OSError, IndexError, UnicodeError, ValueError):
        raise RuntimeError("worker identity query is uncertain") from None
