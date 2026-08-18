"""Centralized process spawning for the release runner.

The release runner must not create transient Windows consoles.  Keep this
module small and dependency-free so every release subcommand can reuse the
same Windows/Linux behavior.
"""

from __future__ import annotations

import os
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

    options = {
        "stdin": subprocess.DEVNULL,
        "close_fds": True,
        **kwargs,
    }
    if os.name == "nt":
        options.update(_windows_options(new_process_group=True, existing_flags=int(options.pop("creationflags", 0) or 0)))
    else:
        options.setdefault("start_new_session", True)
    return subprocess.Popen(command, **options)
