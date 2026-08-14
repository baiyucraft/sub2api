import json
import os
from pathlib import Path
import shutil
import subprocess

import pytest


SCRIPT = Path(__file__).resolve().parents[2] / "windows" / "run-bash.ps1"
PWSH = shutil.which("pwsh") or shutil.which("powershell")
GIT_BASH = Path(r"C:\Program Files\Git\bin\bash.exe")


pytestmark = pytest.mark.skipif(
    os.name != "nt" or PWSH is None or not GIT_BASH.is_file(),
    reason="requires Windows, PowerShell, and Git Bash",
)


def run_wrapper(*arguments: str, cwd: Path | None = None, env: dict[str, str] | None = None):
    command = [
        PWSH,
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-File",
        str(SCRIPT),
        *arguments,
    ]
    merged_env = os.environ.copy()
    merged_env["SUB2API_BASH_EXE"] = str(GIT_BASH)
    if env:
        merged_env.update(env)
    return subprocess.run(
        command,
        cwd=cwd,
        env=merged_env,
        text=True,
        encoding="utf-8",
        capture_output=True,
        check=False,
    )


def write_shell_script(path: Path, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8", newline="\n")


def test_capabilities_report_local_and_vm_only_commands():
    result = run_wrapper("capabilities")

    assert result.returncode == 0, result.stderr
    payload = json.loads(result.stdout)
    assert payload["schema"] == "sub2api.windows-shell-capabilities/v1"
    assert Path(payload["bash_path"]).resolve() == GIT_BASH.resolve()
    assert payload["bash_version"] not in {"", "unknown"}
    assert payload["commands"]["sha256sum"] == "available"
    assert payload["commands"]["docker"] == "vm_required"
    assert payload["commands"]["systemctl"] == "vm_required"


def test_syntax_accepts_path_with_spaces_and_bang(tmp_path: Path):
    script = tmp_path / "workspace !" / "valid script.sh"
    write_shell_script(script, "#!/usr/bin/env bash\nprintf '%s\\n' ok\n")

    result = run_wrapper("syntax", str(script))

    assert result.returncode == 0, result.stderr
    assert result.stdout == ""


def test_syntax_preserves_bash_failure(tmp_path: Path):
    script = tmp_path / "invalid.sh"
    write_shell_script(script, "#!/usr/bin/env bash\nif then\n")

    result = run_wrapper("syntax", str(script))

    assert result.returncode == 2
    assert "syntax error" in result.stderr.lower()


def test_test_mode_preserves_arguments_streams_and_exit_code(tmp_path: Path):
    script = tmp_path / "runner !" / "stream test.sh"
    write_shell_script(
        script,
        "#!/usr/bin/env bash\n"
        "printf 'stdout:%s|%s\\n' \"$1\" \"$2\"\n"
        "printf 'stderr:%s\\n' \"$3\" >&2\n"
        "exit 23\n",
    )

    result = run_wrapper("test", str(script), "hello world", "bang!", "error detail")

    assert result.returncode == 23
    assert result.stdout == "stdout:hello world|bang!\n"
    assert result.stderr == "stderr:error detail\n"


def test_test_mode_converts_absolute_workspace_arguments(tmp_path: Path):
    workspace = tmp_path / "workspace !"
    script = workspace / "path test.sh"
    target = workspace / "folder with spaces" / "value.txt"
    target.parent.mkdir(parents=True)
    target.write_text("value", encoding="utf-8")
    write_shell_script(
        script,
        "#!/usr/bin/env bash\n"
        "case \"$1\" in\n"
        "  /*) printf 'posix:%s\\n' \"$1\" ;;\n"
        "  *) printf 'windows:%s\\n' \"$1\"; exit 9 ;;\n"
        "esac\n",
    )

    result = run_wrapper("test", str(script), str(target), cwd=workspace)

    assert result.returncode == 0, result.stderr
    assert result.stdout.startswith("posix:/")
    assert "folder with spaces/value.txt" in result.stdout


def test_command_mode_runs_only_local_allowlist():
    allowed = run_wrapper("command", "sha256sum", "--version")
    rejected = run_wrapper("command", "printf", "unexpected")
    vm_only = run_wrapper("command", "docker", "version")

    assert allowed.returncode == 0, allowed.stderr
    assert "sha256sum" in allowed.stdout.lower()
    assert rejected.returncode == 64
    assert "command_status=rejected" in rejected.stderr
    assert vm_only.returncode == 78
    assert "command_status=vm_required command=docker" in vm_only.stderr


def test_noninteractive_shell_does_not_load_bash_env(tmp_path: Path):
    startup = tmp_path / "startup.sh"
    script = tmp_path / "clean.sh"
    write_shell_script(startup, "printf 'unexpected-startup\\n' >&2\n")
    write_shell_script(script, "#!/usr/bin/env bash\nprintf 'clean\\n'\n")

    result = run_wrapper("test", str(script), env={"BASH_ENV": str(startup)})

    assert result.returncode == 0, result.stderr
    assert result.stdout == "clean\n"
    assert "unexpected-startup" not in result.stderr
