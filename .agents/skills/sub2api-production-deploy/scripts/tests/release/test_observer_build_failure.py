from pathlib import Path
import json
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))
from release import observer, supervisor


class ObserverBuildFailureTest(unittest.TestCase):
    def test_vm_and_early_failure_codes_are_visible_without_production_result(self):
        for name in ("vm-build-failure.json", "build-dependencies.json"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / name).write_text(json.dumps({"build_failure_code": "build_registry_502"}), encoding="utf-8")
                with mock.patch.object(supervisor, "RUN_ROOT", root), mock.patch.object(observer, "_run_dir", return_value=root):
                    self.assertEqual(observer._failure_hint("release"), "build_registry_502")

    def test_success_and_unrecognized_free_text_do_not_become_failure_hints(self):
        for code in ("none", "registry password=secret", "not_registered"):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / "build-dependencies.json").write_text(json.dumps({"build_failure_code": code}), encoding="utf-8")
                with mock.patch.object(supervisor, "RUN_ROOT", root), mock.patch.object(observer, "_run_dir", return_value=root):
                    self.assertEqual(observer._failure_hint("release"), "unknown")

    def test_candidate_failure_takes_precedence_over_successful_early_preflight(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "build-dependencies.json").write_text(json.dumps({"build_failure_code": "none"}), encoding="utf-8")
            (root / "vm-build-failure.json").write_text(json.dumps({"build_failure_code": "build_registry_eof"}), encoding="utf-8")
            with mock.patch.object(supervisor, "RUN_ROOT", root), mock.patch.object(observer, "_run_dir", return_value=root):
                self.assertEqual(observer._failure_hint("release"), "build_registry_eof")
