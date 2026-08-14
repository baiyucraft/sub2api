from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path


LOGGING_ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(LOGGING_ROOT))

from release_logging import (  # noqa: E402
    REDACTED,
    EventContext,
    JSONLEventLogger,
    LogQuery,
    ReleaseLogRecord,
    build_retention_plan,
    query_events,
    redact_text,
    redact_value,
    verify_retention_plan,
)


UTC = timezone.utc


class RedactionTest(unittest.TestCase):
    def test_recursive_redaction_preserves_correlation_ids(self) -> None:
        value = {
            "api_key": "sk-secretsecretsecret",
            "headers": {"Authorization": "Bearer hidden"},
            "upstream_key_id": 42,
            "nested": [{"password": "hidden", "command_id": "cmd-1"}],
        }
        redacted = redact_value(value)
        self.assertEqual(redacted["api_key"], REDACTED)
        self.assertEqual(redacted["headers"], REDACTED)
        self.assertEqual(redacted["upstream_key_id"], 42)
        self.assertEqual(redacted["nested"][0]["password"], REDACTED)
        self.assertEqual(redacted["nested"][0]["command_id"], "cmd-1")

    def test_inline_redaction_covers_auth_url_jwt_and_private_key(self) -> None:
        source = (
            "Authorization: Bearer abc.def-123 password=hunter2 "
            "postgres://alice:secret@db/service "
            "eyJabcdefgh.ijklmnop.qrstuvwx "
            "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"
        )
        redacted = redact_text(source)
        for secret in ("abc.def-123", "hunter2", "alice", "secret", "eyJabcdefgh"):
            self.assertNotIn(secret, redacted)
        self.assertIn(REDACTED, redacted)


class EventLoggerTest(unittest.TestCase):
    def test_emit_writes_complete_redacted_jsonl_contract(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "logs" / "events.jsonl"
            logger = JSONLEventLogger(path, EventContext("235-abcdef", "blue-green", "local"))
            emitted = logger.emit(
                stage="doctor",
                script="release.py",
                event="command_finished",
                message="api_key=sk-secretsecretsecret completed",
                command_id="cmd-1",
                attempt=2,
                stream="stderr",
                level="warn",
                exit_code=97,
                details={"authorization": "Bearer hidden", "account_id": 12},
                timestamp=datetime(2026, 8, 14, 1, 2, 3, tzinfo=UTC),
            )
            stored = json.loads(path.read_text(encoding="utf-8"))
            self.assertEqual(stored, emitted)
            self.assertEqual(stored["timestamp"], "2026-08-14T01:02:03.000Z")
            self.assertEqual(stored["deployment_mode"], "blue-green")
            self.assertEqual(stored["details"]["authorization"], REDACTED)
            self.assertEqual(stored["details"]["account_id"], 12)
            self.assertNotIn("sk-secretsecretsecret", path.read_text(encoding="utf-8"))
            if os.name != "nt":
                self.assertEqual(path.stat().st_mode & 0o777, 0o600)
                self.assertEqual(path.parent.stat().st_mode & 0o777, 0o700)

    def test_emit_appends_and_generates_command_id(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "events.jsonl"
            logger = JSONLEventLogger(path, EventContext("release-1", "downtime", "vm"))
            first = logger.emit(stage="gate", script="validate.sh", event="started", message="start")
            second = logger.emit(stage="gate", script="validate.sh", event="finished", message="done", exit_code=0)
            self.assertNotEqual(first["command_id"], second["command_id"])
            self.assertEqual(len(path.read_text(encoding="utf-8").splitlines()), 2)

    @unittest.skipIf(os.name == "nt", "symlink creation requires elevated Windows privileges")
    def test_symlink_log_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "target"
            target.write_text("", encoding="utf-8")
            link = root / "events.jsonl"
            link.symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, "single-link"):
                JSONLEventLogger(link, EventContext("release-1", "blue-green", "local"))

    def test_invalid_contract_values_are_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "deployment_mode"):
            EventContext("release-1", "rolling", "local")  # type: ignore[arg-type]
        with tempfile.TemporaryDirectory() as directory:
            logger = JSONLEventLogger(Path(directory) / "events.jsonl", EventContext("release-1", "blue-green", "local"))
            with self.assertRaisesRegex(ValueError, "attempt"):
                logger.emit(stage="gate", script="release.py", event="started", message="x", attempt=0)


class QueryTest(unittest.TestCase):
    def test_query_filters_sorts_tails_and_reports_malformed_lines(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first_path = root / "first.jsonl"
            second_path = root / "second.jsonl"
            local = JSONLEventLogger(first_path, EventContext("release-1", "blue-green", "local"))
            vm = JSONLEventLogger(second_path, EventContext("release-1", "blue-green", "vm"))
            local.emit(stage="doctor", script="release.py", event="old", message="old", level="info", timestamp=datetime(2026, 8, 13, 0, tzinfo=UTC))
            vm.emit(stage="gate", script="vm.sh", event="middle", message="middle", level="error", timestamp=datetime(2026, 8, 14, 10, tzinfo=UTC))
            local.emit(stage="gate", script="release.py", event="new", message="token=hidden", level="error", timestamp=datetime(2026, 8, 14, 11, tzinfo=UTC))
            with first_path.open("a", encoding="utf-8") as stream:
                stream.write("not-json\n")

            result = query_events(
                [first_path, second_path],
                LogQuery(stage="gate", level="error", since=timedelta(hours=6), tail=2),
                now=datetime(2026, 8, 14, 12, tzinfo=UTC),
            )
            self.assertEqual([event["event"] for event in result.events], ["middle", "new"])
            self.assertEqual(len(result.issues), 1)
            self.assertNotIn("hidden", json.dumps(result.events))

    def test_query_rejects_symlink_and_missing_file_without_aborting(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            missing = Path(directory) / "missing.jsonl"
            result = query_events([missing])
            self.assertEqual(result.events, [])
            self.assertEqual(result.issues[0].line, 0)

    def test_query_rejects_multiple_hard_links(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / "events.jsonl"
            source.write_text("{}\n", encoding="utf-8")
            alias = root / "alias.jsonl"
            os.link(source, alias)
            result = query_events([source])
            self.assertEqual(result.events, [])
            self.assertIn("hard links", result.issues[0].error)


class RetentionPlanTest(unittest.TestCase):
    def record(self, release_id: str, age_days: int, status: str = "verified", **kwargs: bool) -> ReleaseLogRecord:
        return ReleaseLogRecord(
            release_id=release_id,
            path=Path("/logs") / release_id,
            created_at=datetime(2026, 8, 14, tzinfo=UTC) - timedelta(days=age_days),
            status=status,
            **kwargs,
        )

    def test_plan_deletes_only_expired_success_outside_recent_set(self) -> None:
        records = [self.record(f"recent-{index}", index) for index in range(10)]
        records.extend(
            [
                self.record("old-success", 120),
                self.record("old-failed", 121, "failed"),
                self.record("old-current", 122, current_baseline=True),
                self.record("old-recovery", 123, has_recovery_point=True),
                self.record("old-reconcile", 124, has_reconciliation_evidence=True),
                self.record("old-running", 125, "running"),
            ]
        )
        plan = build_retention_plan(records, now=datetime(2026, 8, 14, tzinfo=UTC))
        self.assertEqual([item["release_id"] for item in plan.delete], ["old-success"])
        reasons = {item["release_id"]: item["reason"] for item in plan.retain}
        self.assertEqual(reasons["old-failed"], "failure_or_recovery_evidence")
        self.assertEqual(reasons["old-current"], "current_baseline")
        self.assertEqual(reasons["old-reconcile"], "reconciliation_evidence")
        self.assertTrue(verify_retention_plan(plan, plan.plan_sha256))

    def test_plan_checksum_is_deterministic_and_detects_tampering(self) -> None:
        now = datetime(2026, 8, 14, tzinfo=UTC)
        records = [self.record("old", 100), self.record("new", 1)]
        first = build_retention_plan(records, now=now, keep_recent=0)
        second = build_retention_plan(reversed(records), now=now, keep_recent=0)
        self.assertEqual(first.plan_sha256, second.plan_sha256)
        tampered = first.document()
        tampered["delete"] = []
        self.assertFalse(verify_retention_plan(tampered, first.plan_sha256))

    def test_duplicate_release_id_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "duplicate"):
            build_retention_plan([self.record("same", 100), self.record("same", 101)], now=datetime(2026, 8, 14, tzinfo=UTC))

    def test_naive_plan_time_is_rejected(self) -> None:
        with self.assertRaisesRegex(ValueError, "timezone-aware"):
            build_retention_plan([], now=datetime(2026, 8, 14))


if __name__ == "__main__":
    unittest.main()
