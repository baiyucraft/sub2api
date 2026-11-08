from __future__ import annotations

import json
import sys
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

import pytest

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release.backup_maintenance import APPLY_FIELDS, PLAN_FIELDS, UPLOAD_RESERVE_BYTES, ensure_backup_capacity


CURRENT = "283-aaaaaaaaaaaa-1000-aaaaaaaa"
OLD = "282-bbbbbbbbbbbb-900-bbbbbbbb"
MINIMUM = 5 * 1024 ** 3
REQUIRED = MINIMUM + UPLOAD_RESERVE_BYTES


def plan():
    return {
        "cleanup_mode": "dry-run", "cleanup_status": "ready", "plan_sha256": "a" * 64,
        "candidate_count": "1", "candidate_bytes": str(2 * 1024 ** 3),
        "candidate_release_ids": OLD, "free_before": str(MINIMUM - 1), "keep_recent": "1",
        "keep_latest_profile_only": "true", "latest_profile": "283", "retention_days": "30",
    }


def applied():
    return {
        "cleanup_mode": "apply", "cleanup_status": "completed", "plan_sha256": "a" * 64,
        "deleted_count": "1", "deleted_bytes": str(2 * 1024 ** 3),
        "free_before": str(MINIMUM - 1), "free_after": str(REQUIRED + 100),
    }


def context(free=MINIMUM - 1):
    runner = mock.Mock()
    runner.assert_configuration_current = mock.Mock()
    runner.run.side_effect = [SimpleNamespace(values={"backup_free_bytes": str(free)}),
                              SimpleNamespace(values={"backup_free_bytes": str(REQUIRED + 50)})]
    runner.run_with_input.side_effect = [SimpleNamespace(values=plan()), SimpleNamespace(values=applied())]
    environment = SimpleNamespace(assert_current=mock.Mock(), deployment={"backup_upload": {"node": "synthetic-backup"}})
    return runner, environment


def call(tmp_path, runner, environment):
    return ensure_backup_capacity(runner, environment, MINIMUM, tmp_path, CURRENT)


def receipt(tmp_path):
    return json.loads((tmp_path / "backup-maintenance.json").read_text())


def test_capacity_above_reserve_skips_all_cleanup(tmp_path):
    runner, environment = context(REQUIRED)
    result = call(tmp_path, runner, environment)
    assert result["status"] == "not_needed"
    runner.run_with_input.assert_not_called()
    assert receipt(tmp_path)["required_free_bytes"] == REQUIRED


def test_low_space_applies_exact_plan_and_checks_capacity(tmp_path):
    runner, environment = context()
    intents = []
    def run(node, command, fields, source, **kwargs):
        intents.append(receipt(tmp_path)["status"] if (tmp_path / "backup-maintenance.json").exists() else "none")
        if "dry-run" in command:
            assert fields == PLAN_FIELDS
            return SimpleNamespace(values=plan())
        assert command == f"bash -s -- apply {'a' * 64} {OLD}"
        assert fields == APPLY_FIELDS
        return SimpleNamespace(values=applied())
    runner.run_with_input.side_effect = run
    result = call(tmp_path, runner, environment)
    assert result["status"] == "verified"
    assert intents == ["none", "applying"]
    assert runner.run.call_count == 2
    assert environment.assert_current.call_count == 4
    assert runner.assert_configuration_current.call_count == 4
    assert receipt(tmp_path)["apply"]["deleted_count"] == "1"


@pytest.mark.parametrize("field,value", [
    ("cleanup_status", "completed"), ("cleanup_mode", "apply"), ("plan_sha256", "z" * 64),
    ("candidate_count", "2"), ("candidate_bytes", "-1"), ("free_before", "nan"),
    ("keep_recent", "0"), ("keep_latest_profile_only", "false"), ("retention_days", "7"),
    ("latest_profile", "281"), ("candidate_release_ids", CURRENT),
    ("candidate_release_ids", OLD + "," + OLD), ("candidate_release_ids", OLD + ";touch /tmp/example"),
])
def test_invalid_plan_never_deletes(tmp_path, field, value):
    runner, environment = context()
    values = plan(); values[field] = value
    runner.run_with_input.side_effect = [SimpleNamespace(values=values)]
    with pytest.raises(RuntimeError):
        call(tmp_path, runner, environment)
    assert runner.run_with_input.call_count == 1
    assert receipt(tmp_path)["status"] == "blocked"


def test_empty_plan_and_insufficient_projected_space_do_not_delete(tmp_path):
    for empty in (True, False):
        runner, environment = context()
        values = plan()
        values.update(candidate_bytes="0" if empty else "1", candidate_count="0" if empty else "1",
                      candidate_release_ids="" if empty else OLD)
        runner.run_with_input.side_effect = [SimpleNamespace(values=values)]
        with pytest.raises(RuntimeError):
            call(tmp_path, runner, environment)
        assert runner.run_with_input.call_count == 1
        (tmp_path / "backup-maintenance.json").unlink()


@pytest.mark.parametrize("field,value", [
    ("cleanup_mode", "dry-run"), ("cleanup_status", "failed"), ("plan_sha256", "b" * 64),
    ("deleted_count", "0"), ("deleted_bytes", "1"), ("free_after", "1"),
])
def test_bad_apply_or_short_capacity_never_retries_or_succeeds(tmp_path, field, value):
    runner, environment = context()
    values = applied(); values[field] = value
    runner.run_with_input.side_effect = [SimpleNamespace(values=plan()), SimpleNamespace(values=values)]
    with pytest.raises(RuntimeError):
        call(tmp_path, runner, environment)
    assert runner.run_with_input.call_count == 2
    assert receipt(tmp_path)["status"] == "blocked"


def test_uncertain_apply_preserves_intent_and_does_not_retry(tmp_path):
    runner, environment = context()
    runner.run_with_input.side_effect = [SimpleNamespace(values=plan()), TimeoutError()]
    with pytest.raises(TimeoutError):
        call(tmp_path, runner, environment)
    result = receipt(tmp_path)
    assert result["status"] == "blocked" and "plan" in result and "apply" not in result
    assert runner.run_with_input.call_count == 2


def test_environment_drift_stops_before_apply(tmp_path):
    runner, environment = context()
    environment.assert_current.side_effect = [None, None, RuntimeError("fixture drift")]
    with pytest.raises(RuntimeError, match="fixture drift"):
        call(tmp_path, runner, environment)
    assert runner.run_with_input.call_count == 1
    assert receipt(tmp_path)["status"] == "blocked"


def test_post_cleanup_drift_blocks_success(tmp_path):
    runner, environment = context()
    runner.run.side_effect = [SimpleNamespace(values={"backup_free_bytes": str(MINIMUM - 1)}),
                              SimpleNamespace(values={"backup_free_bytes": "1"})]
    with pytest.raises(RuntimeError, match="drifted"):
        call(tmp_path, runner, environment)
    assert receipt(tmp_path)["status"] == "blocked"


def test_undeclared_field_in_plan_is_rejected(tmp_path):
    runner, environment = context()
    values = plan(); values["unexpected"] = "value"
    runner.run_with_input.side_effect = [SimpleNamespace(values=values)]
    with pytest.raises(RuntimeError, match="protocol"):
        call(tmp_path, runner, environment)
    assert runner.run_with_input.call_count == 1


def test_second_entrypoint_preserves_verified_plan_and_only_checks_capacity(tmp_path):
    runner, environment = context()
    first = call(tmp_path, runner, environment)
    runner2, environment2 = context(REQUIRED)
    second = call(tmp_path, runner2, environment2)
    assert second["status"] == "verified"
    assert second["plan"] == first["plan"] and second["apply"] == first["apply"]
    assert second["last_observed_free_bytes"] == REQUIRED
    runner2.run_with_input.assert_not_called()


@pytest.mark.parametrize("status", ["planned", "applying", "blocked"])
def test_prior_incomplete_receipt_blocks_any_second_cleanup(tmp_path, status):
    old = {"schema": 1, "release_id": CURRENT, "required_free_bytes": REQUIRED, "status": status,
           "plan": plan()}
    path = tmp_path / "backup-maintenance.json"
    path.write_text(json.dumps(old))
    runner, environment = context()
    with pytest.raises(RuntimeError, match="reconciliation"):
        call(tmp_path, runner, environment)
    runner.run.assert_not_called()
    runner.run_with_input.assert_not_called()
    assert receipt(tmp_path) == old


def test_not_needed_receipt_can_perform_first_cleanup_after_capacity_drift(tmp_path):
    runner, environment = context(REQUIRED)
    call(tmp_path, runner, environment)
    runner2, environment2 = context()
    assert call(tmp_path, runner2, environment2)["status"] == "verified"
    assert runner2.run_with_input.call_count == 2


def test_verified_cleanup_cannot_clean_more_after_capacity_drift(tmp_path):
    runner, environment = context()
    first = call(tmp_path, runner, environment)
    runner2, environment2 = context(1)
    with pytest.raises(RuntimeError, match="drifted"):
        call(tmp_path, runner2, environment2)
    assert receipt(tmp_path)["status"] == "blocked"
    assert receipt(tmp_path)["plan"] == first["plan"]
    runner2.run_with_input.assert_not_called()


@pytest.mark.parametrize("invalid", ["missing_apply", "wrong_digest", "boolean_schema"])
def test_corrupt_verified_receipt_is_not_reported_as_success(tmp_path, invalid):
    runner, environment = context()
    call(tmp_path, runner, environment)
    old = receipt(tmp_path)
    if invalid == "missing_apply":
        old.pop("apply")
    elif invalid == "wrong_digest":
        old["apply"]["plan_sha256"] = "b" * 64
    else:
        old["schema"] = True
    (tmp_path / "backup-maintenance.json").write_text(json.dumps(old))
    runner2, environment2 = context(REQUIRED)
    with pytest.raises(RuntimeError):
        call(tmp_path, runner2, environment2)
    runner2.run.assert_not_called()
    runner2.run_with_input.assert_not_called()
