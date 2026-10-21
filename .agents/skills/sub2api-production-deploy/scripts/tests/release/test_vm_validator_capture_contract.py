from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from test_vm_probe_ownership import VALIDATOR, bash_path, execute


@pytest.mark.parametrize(
    ("command", "stdout_name", "exit_name"),
    [
        (
            'plan_before=$(docker run --rm -v "$plan_snapshot:/input/production-snapshot.json:ro"',
            "plan-before.json",
            "plan-before.exit",
        ),
        (
            'actual_plan=$(docker run --rm --network="$probe_network" -v "$probe_dir:/app/data" "$candidate_image_id" /app/sub2api --migration-plan-json',
            "plan-actual.json",
            "plan-actual.exit",
        ),
    ],
)
def test_migration_plan_failures_are_persisted_before_assertion(
    tmp_path: Path, command: str, stdout_name: str, exit_name: str
) -> None:
    source = VALIDATOR.read_text(encoding="utf-8")
    start = source.index("  if " + command)
    end = source.index("  [[ \"$" + ("plan_before_exit" if "plan-before" in stdout_name else "actual_plan_exit") + '" == 0', start)
    block = source[start:end]

    state = tmp_path / "state"
    state.mkdir()
    script = "set -Eeuo pipefail\ntrap 'exit 99' ERR\ndocker() { printf 'partial-json'; return 23; }\n"
    script += (
        'state_dir="$1"; plan_snapshot=/snapshot; probe_network=fixture; '
        'probe_dir=/fixture; candidate_image_id=image\n'
    )
    script += block + '\nprintf "persisted\\n"\n'
    result = execute(script, state.as_posix())
    assert result.returncode == 0 and result.stdout.strip() == "persisted"
    assert (state / stdout_name).read_text() == "partial-json"
    assert (state / exit_name).read_text().strip() == "23"


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
