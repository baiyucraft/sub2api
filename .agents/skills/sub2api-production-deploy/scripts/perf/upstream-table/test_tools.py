"""Offline qualification checks; no database, VM, or production connections."""
from __future__ import annotations

import contextlib
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("table_comparison", pathlib.Path(__file__).with_name("compare.py"))
comparison = importlib.util.module_from_spec(spec)
spec.loader.exec_module(comparison)
runner_spec = importlib.util.spec_from_file_location("table_runner", pathlib.Path(__file__).with_name("run.py"))
runner = importlib.util.module_from_spec(runner_spec)
runner_spec.loader.exec_module(runner)


class ComparisonQualificationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="table-comparison-")
        self.addCleanup(self.temporary.cleanup)
        self.root = pathlib.Path(self.temporary.name)

    def fixture(self, name="runs", ready=True, seconds=85, scenario="normal", repetitions=3, samples=334):
        entries = []
        for version, latency in (("baseline", 100), ("optimized", 60)):
            for repetition in range(repetitions):
                directory = f"{name}-{version}-{repetition}"
                (self.root / directory).mkdir()
                (self.root / directory / "latency_samples.json").write_text(json.dumps({
                    route: [latency] * samples for route in ("admin_ms", "list_ms", "gateway_ms")}))
                entries.append(dict(cell=directory, version=version, repetition=repetition,
                    binary_sha256=version, artifacts_directory=directory, exit=0,
                    result=dict(accounts=1000, history=100000, page=100, admin_workers=20,
                        scenario=scenario, admin_errors=0, gateway_errors=0,
                        admin={"samples": samples}, list_http={"samples": samples}, gateway={"samples": samples}, initial_cache_active=ready,
                        initial_all_accounts_fresh=ready)))
        source = self.root / f"{name}.json"
        source.write_text(json.dumps(entries))
        source.with_name(source.stem + "-provenance.json").write_text(json.dumps(dict(
            fixture_sha256={"fixture": "fixed"}, measurement_seconds=seconds, readiness_seconds=120,
            admins=20, quality_filter="", page_number=1,
            tool_sha256={"prepare.py": "fixed", "run.py": "fixed", "pg_cpu_sample.py": "fixed", "remote_job.py": "fixed"},
            source_manifest_sha256={version: version for version in ("baseline", "optimized")},
            binaries={version: version for version in ("baseline", "optimized")})))
        return source

    def report(self, *sources):
        output = self.root / "report.json"
        with patch.object(comparison, "ARTIFACTS", self.root), patch("sys.argv", [
            "compare.py", *map(str, sources), "--output", str(output)]), contextlib.redirect_stdout(io.StringIO()):
            comparison.main()
        return json.loads(output.read_text())

    def test_p99_requires_1000_actual_samples(self):
        self.assertNotIn("p99_ms", comparison.distribution([1] * 999))
        self.assertEqual(1, comparison.distribution([1] * 1000)["p99_ms"])
        self.assertNotIn("p95_ms", comparison.distribution([]))

    def test_qualified_latency_does_not_claim_full_acceptance(self):
        report = self.report(self.fixture())
        self.assertTrue(report["all_supplied_cells_passed"])
        self.assertFalse(report["full_acceptance_complete"])

    def test_protection_must_be_ready(self):
        report = self.report(self.fixture(ready=False))
        self.assertIn("protection_not_ready_at_measurement_start", report["comparisons"][0]["issues"])
        self.assertFalse(report["all_supplied_cells_passed"])

    def test_missing_provenance_is_rejected(self):
        source = self.fixture()
        source.with_name(source.stem + "-provenance.json").unlink()
        self.assertIn("missing_or_inconsistent_provenance", self.report(source)["comparisons"][0]["issues"])

    def test_different_measurement_conditions_cannot_pool(self):
        report = self.report(self.fixture("first"), self.fixture("second", seconds=90))
        self.assertIn("mixed_measurement_conditions", report["comparisons"][0]["issues"])

    def test_required_provenance_cannot_be_consistently_absent(self):
        source = self.fixture()
        metadata_file = source.with_name(source.stem + "-provenance.json")
        original = json.loads(metadata_file.read_text())
        for missing in ("measurement_seconds", "readiness_seconds", "tool_sha256", "source_manifest_sha256"):
            with self.subTest(missing=missing):
                metadata_file.write_text(json.dumps({key: value for key, value in original.items() if key != missing}))
                self.assertIn("missing_or_inconsistent_provenance", self.report(source)["comparisons"][0]["issues"])

    def test_three_runs_are_required(self):
        report = self.report(self.fixture(repetitions=2))
        self.assertIn("three_runs_of_one_binary_required", report["comparisons"][0]["issues"])

    def test_duplicate_input_cannot_create_more_samples(self):
        source = self.fixture()
        with self.assertRaisesRegex(ValueError, "duplicate run"):
            self.report(source, source)

    def test_untyped_full_rejection_is_not_accepted(self):
        report = self.report(self.fixture(scenario="full"))
        self.assertIn("full_capacity_rejection_semantics_require_verification", report["comparisons"][0]["issues"])

    def test_only_verified_full_rejections_qualify(self):
        source = self.fixture(scenario="full")
        entries = json.loads(source.read_text())
        for entry in entries:
            entry["result"].update(gateway={"samples": 334}, gateway_errors=334,
                full_expected_rejections=334, full_unexpected_responses=0,
                full_invalid_json=0, upstream_requests=0)
        source.write_text(json.dumps(entries))
        self.assertTrue(self.report(source)["all_supplied_cells_passed"])
        entries[0]["result"]["full_unexpected_responses"] = 1
        source.write_text(json.dumps(entries))
        self.assertFalse(self.report(source)["all_supplied_cells_passed"])

    def test_missing_raw_run_cannot_count_as_third_repetition(self):
        source = self.fixture(samples=1000)
        entry = json.loads(source.read_text())[0]
        (self.root / entry["artifacts_directory"] / "latency_samples.json").unlink()
        report = self.report(source)
        self.assertIn("invalid_or_missing_raw_samples", report["comparisons"][0]["issues"])
        self.assertFalse(report["all_supplied_cells_passed"])

    def test_missing_or_mismatched_route_is_rejected(self):
        for fault in ("missing", "count", "invalid_document", "invalid_json"):
            with self.subTest(fault=fault):
                source = self.fixture(name=fault, samples=1000)
                entry = json.loads(source.read_text())[0]
                path = self.root / entry["artifacts_directory"] / "latency_samples.json"
                data = json.loads(path.read_text())
                if fault == "missing":
                    del data["list_ms"]
                elif fault == "count":
                    data["gateway_ms"].pop()
                elif fault == "invalid_document":
                    data = []
                path.write_text("{" if fault == "invalid_json" else json.dumps(data))
                report = self.report(source)
                self.assertIn("invalid_or_missing_raw_samples", report["comparisons"][0]["issues"])
                self.assertFalse(report["all_supplied_cells_passed"])

    def test_sample_values_must_be_finite_nonnegative_numbers(self):
        for index, value in enumerate((float("nan"), float("inf"), float("-inf"), -1, True, "1")):
            with self.subTest(value=value):
                source = self.fixture(name=f"value-{index}", samples=1000)
                entry = json.loads(source.read_text())[0]
                path = self.root / entry["artifacts_directory"] / "latency_samples.json"
                data = json.loads(path.read_text())
                data["admin_ms"][0] = value
                path.write_text(json.dumps(data))
                self.assertIn("invalid_or_missing_raw_samples", self.report(source)["comparisons"][0]["issues"])


class RunnerTimeoutTests(unittest.TestCase):
    def test_invalid_options_fail_before_connecting_to_vm(self):
        for options in (['--seconds', '0'], ['--readiness', '601'], ['--admins', '0'],
                        ['--repetitions', '0'], ['--page-number', '0'], ['--versions', 'unknown'],
                        ['--scenarios', 'unknown'], ['--cells', '0:100000:20']):
            with self.subTest(options=options), patch('sys.argv', ['run.py', *options]), \
                    patch.object(runner, 'SSHRunner') as ssh, contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as error:
                    runner.main()
                self.assertEqual(2, error.exception.code)
                ssh.assert_not_called()

    def test_version_order_alternates_by_repetition(self):
        self.assertEqual(['baseline', 'optimized'], runner.version_order(['baseline', 'optimized'], 0))
        self.assertEqual(['optimized', 'baseline'], runner.version_order(['baseline', 'optimized'], 1))
        self.assertEqual(['baseline', 'optimized'], runner.version_order(['baseline', 'optimized'], 2))
        self.assertEqual(['optimized'], runner.version_order(['optimized'], 1))

    def test_long_single_admin_measurement_gets_full_budget(self):
        budget = runner.measurement_timeouts(1700, 120)
        self.assertEqual(2540, budget["go_test_seconds"])
        self.assertEqual(2600, budget["ssh_seconds"])

    def test_timeout_bounds_are_explicit(self):
        self.assertEqual(722, runner.measurement_timeouts(1, 1)["go_test_seconds"])
        self.assertEqual(8580, runner.measurement_timeouts(7200, 600)["ssh_seconds"])
        for seconds, readiness in ((0, 120), (7201, 120), (85, 0), (85, 601), (True, 120), (85.0, 120)):
            with self.subTest(seconds=seconds, readiness=readiness), self.assertRaises(ValueError):
                runner.measurement_timeouts(seconds, readiness)


if __name__ == "__main__":
    unittest.main()
