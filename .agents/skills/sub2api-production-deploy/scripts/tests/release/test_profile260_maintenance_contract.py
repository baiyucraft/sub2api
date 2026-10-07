from __future__ import annotations

import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path


MAINTENANCE_ROOT = Path(__file__).resolve().parents[2] / "maintenance"
EARLY_PROFILES = (
    "182", "187", "191", "192", "194", "195", "197", "198", "199",
    "202", "206", "207", "208", "209", "210", "212", "213", "215",
)
RELEASE_PROFILES = EARLY_PROFILES + tuple(str(value) for value in range(232, 265))
ID_GUARDS = {
    "release/context.sh": "release_dir",
    "release/prepare.sh": "release_id",
    "release/promote-backup.sh": "release_id",
    "181/mask-backup-units.sh": "base",
    "181/restore-backup-units.sh": "base",
}
PROFILE_GUARDS = {
    "release/prepare.sh": RELEASE_PROFILES,
    "release/migration-195-assert.sh": EARLY_PROFILES[5:] + tuple(str(value) for value in range(232, 265)),
    "release/migration-232-assert.sh": tuple(str(value) for value in range(232, 265)),
    "release/migration-233-assert.sh": tuple(str(value) for value in range(233, 265)),
    "release/migration-234-assert.sh": tuple(str(value) for value in range(235, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-235-assert.sh": tuple(str(value) for value in range(237, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-236-assert.sh": tuple(str(value) for value in range(237, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-237-assert.sh": tuple(str(value) for value in range(238, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-238-assert.sh": tuple(str(value) for value in range(239, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-239-assert.sh": tuple(str(value) for value in range(239, 265)),
    "release/migration-240-assert.sh": tuple(str(value) for value in range(240, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-241-assert.sh": tuple(str(value) for value in range(240, 246)) + ("258", "259", "260", "261", "262", "263", "264"),
    "release/migration-242-assert.sh": tuple(str(value) for value in range(241, 265)),
    "release/migration-243-assert.sh": tuple(str(value) for value in range(241, 265)),
    "release/migration-244-assert.sh": tuple(str(value) for value in range(241, 265)),
    "release/migration-245-assert.sh": tuple(str(value) for value in range(241, 265)),
    "release/migration-254-assert.sh": tuple(str(value) for value in range(242, 265)),
    "release/migration-285-assert.sh": ("260", "261", "262", "263", "264"),
}


class Profile260MaintenanceContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.bash = shutil.which("bash")
        if cls.bash is None and os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git" / "bin" / "bash.exe"
            cls.bash = str(candidate) if candidate.is_file() else None
        if cls.bash is None:
            raise unittest.SkipTest("bash is unavailable")

    def guards(self, relative_path: str, variable: str) -> list[str]:
        text = (MAINTENANCE_ROOT / relative_path).read_text(encoding="utf-8")
        guards = []
        for line in text.splitlines():
            if line.lstrip().startswith("#"):
                continue
            for match in re.finditer(r"\[\[.*?\]\]", line):
                expression = match.group()
                if re.search(rf"\${variable}\b", expression) and re.search(r"\b(?:258|260|261|262|263|264)\b", expression):
                    self.assertNotIn("$(", expression)
                    guards.append(expression)
        self.assertTrue(guards, f"no executable {variable} guard in {relative_path}")
        return guards

    def check_cases(self, expression: str, variable: str, cases: list[tuple[str, bool]]) -> None:
        # Execute only the extracted predicates, never the production scripts.
        command = (
            "set -u\nwhile (( $# > 0 )); do\n"
            f"  {variable}=$1\n"
            "  expected=$2\n  shift 2\n  actual=1\n"
            f"  if {expression}; then actual=0; fi\n"
            "  if [[ $actual != $expected ]]; then\n"
            f"    printf 'guard mismatch: %s expected exit %s\\n' \"${variable}\" \"$expected\" >&2\n"
            "    exit 1\n  fi\ndone\n"
        )
        arguments = [item for value, accepted in cases for item in (value, "0" if accepted else "1")]
        environment = os.environ.copy()
        environment.pop("BASH_ENV", None)
        completed = subprocess.run(
            [self.bash, "--noprofile", "--norc", "-c", command, "maintenance-guard", *arguments],
            env=environment,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)

    def id_value(self, variable: str, release_id: str) -> str:
        if variable == "release_dir":
            return f"/opt/sub2api/releases/{release_id}"
        return release_id

    def test_release_id_guards_accept_260_and_all_historical_profiles(self) -> None:
        for relative_path, variable in ID_GUARDS.items():
            cases = [(self.id_value(variable, f"{profile}-aaaaaaaaaaaa-1-aaaaaaaa"), True) for profile in RELEASE_PROFILES]
            if variable == "base":
                cases.append(("release181-state-20260930T120000Z", True))
            for guard in self.guards(relative_path, variable):
                with self.subTest(script=relative_path):
                    self.check_cases(guard, variable, cases)

    def test_release_id_guards_reject_unknown_profiles_and_malformed_paths(self) -> None:
        invalid_ids = (
            "265-aaaaaaaaaaaa-1-aaaaaaaa",
            "0259-aaaaaaaaaaaa-1-aaaaaaaa",
            "259-AAAAAAAAAAAA-1-aaaaaaaa",
            "259-aaaaaaaaaaa-1-aaaaaaaa",
            "259-aaaaaaaaaaaa-x-aaaaaaaa",
            "259-aaaaaaaaaaaa-1-aaaaaaa",
            "259-aaaaaaaaaaaa-1-aaaaaaaa/../outside",
            "259-aaaaaaaaaaaa-1-aaaaaaaa/",
        )
        for relative_path, variable in ID_GUARDS.items():
            cases = [(self.id_value(variable, value), False) for value in invalid_ids]
            if variable == "release_dir":
                cases.append(("/tmp/259-aaaaaaaaaaaa-1-aaaaaaaa", False))
            for guard in self.guards(relative_path, variable):
                with self.subTest(script=relative_path):
                    self.check_cases(guard, variable, cases)

    def test_profile_guards_preserve_history_and_reject_unknown_profiles(self) -> None:
        for relative_path, profiles in PROFILE_GUARDS.items():
            cases = [(profile, True) for profile in profiles]
            cases += [(value, False) for value in ("265", "0259", "2590", "259;true")]
            for guard in self.guards(relative_path, "profile"):
                with self.subTest(script=relative_path):
                    self.check_cases(guard, "profile", cases)

    def test_profile_260_uses_inherited_precise_data_plan_branches(self) -> None:
        guards = self.guards("release/migration-195-assert.sh", "release_profile")
        self.assertEqual(len(guards), 2)
        cases = [(str(value), True) for value in range(240, 265)]
        cases += [("239", False), ("265", False), ("0259", False)]
        for guard in guards:
            with self.subTest(guard=guard):
                self.check_cases(guard, "release_profile", cases)

    def test_all_maintenance_profile_allowlists_are_covered(self) -> None:
        covered = set(ID_GUARDS) | set(PROFILE_GUARDS)
        found = set()
        for path in MAINTENANCE_ROOT.rglob("*.sh"):
            text = path.read_text(encoding="utf-8")
            for line in text.splitlines():
                if line.lstrip().startswith("#"):
                    continue
                profile_guard = line.lstrip().startswith("[[ $profile ==")
                id_guard = "=~" in line and "-[0-9a-f]{12}-" in line
                if profile_guard or id_guard:
                    found.add(path.relative_to(MAINTENANCE_ROOT).as_posix())
        self.assertEqual(found, covered)


if __name__ == "__main__":
    unittest.main()
