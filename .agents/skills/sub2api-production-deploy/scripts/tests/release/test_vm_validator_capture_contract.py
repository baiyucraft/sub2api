from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from test_vm_probe_ownership import VALIDATOR, bash_path, execute


@pytest.mark.parametrize(
    ("variable", "stdout_name", "exit_name", "expected_argv"),
    [
        (
            "plan_before",
            "plan-before.json",
            "plan-before.exit",
            ["run", "--rm", "--network", "none", "--env-file", "/fixture.env",
             "-v", "/snapshot:/input/production-snapshot.json:ro", "image", "/app/sub2api",
             "--migration-plan-snapshot-json", "/input/production-snapshot.json"],
        ),
        (
            "actual_plan",
            "plan-actual.json",
            "plan-actual.exit",
            ["run", "--rm", "--network=fixture", "--env-file", "/fixture.env",
             "-v", "/fixture:/app/data", "image", "/app/sub2api", "--migration-plan-json"],
        ),
    ],
)
def test_migration_plan_failures_are_persisted_before_assertion(
    tmp_path: Path, variable: str, stdout_name: str, exit_name: str, expected_argv: list[str]
) -> None:
    source = VALIDATOR.read_text(encoding="utf-8")
    # Locate the real capture by its result variable, then execute that exact
    # block and inspect Docker's argv, including its shared env-file authority.
    marker = "  if " + variable + "=$("
    assert source.count(marker) == 1
    start = source.index(marker)
    end = source.index('  [[ "$' + variable + '_exit" == 0', start)
    block = source[start:end]

    state = tmp_path / "state"
    state.mkdir()
    script = "set -Eeuo pipefail\ntrap 'exit 99' ERR\n"
    script += 'docker() { printf "%s\\0" "$@" > "$state_dir/docker.argv"; printf "partial-json"; return 23; }\n'
    script += (
        'state_dir="$1"; plan_snapshot=/snapshot; probe_network=fixture; '
        'probe_dir=/fixture; candidate_image_id=image; probe_connection_env=/fixture.env\n'
    )
    script += block + '\nprintf "persisted\\n"\n'
    result = execute(script, state.as_posix())
    assert result.returncode == 0 and result.stdout.strip() == "persisted"
    assert (state / stdout_name).read_text() == "partial-json"
    assert (state / exit_name).read_text().strip() == "23"
    assert (state / "docker.argv").read_bytes().split(b"\0") == [value.encode() for value in expected_argv] + [b""]


def test_validator_has_no_set_plus_e_capture_blocks() -> None:
    source = VALIDATOR.read_text(encoding="utf-8")
    assert "set +e" not in source
    assert source.index('printf \'%s\' "$actual_plan" > "$state_dir/plan-actual.json"') < source.index(
        '[[ "$actual_plan_exit" == 0 ]]'
    )


def test_shell_syntax() -> None:
    with VALIDATOR.open("rb") as source:
        result = subprocess.run([bash_path(), "-n"], stdin=source, capture_output=True)
    assert result.returncode == 0, result.stderr.decode(errors="replace")
