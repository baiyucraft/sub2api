from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from release.manifest import profile_contract_digest
from release.paths import WORKSPACE
from release.profiles import CURRENT_RELEASE_PROFILE, get_profile, get_release_profile, parse_profile_contract
from release.recovery_gate import classify


def contract(*profiles: dict[str, object]) -> str:
    return json.dumps({"schema": 1, "profiles": list(profiles)}, separators=(",", ":")) + "\n"


def profile(name: str, parent: str, version: str, migrations: list[str] | None = None) -> dict[str, object]:
    return {
        "profile": name,
        "parent": parent,
        "version": version,
        "new_migrations": migrations if migrations is not None else [],
    }


class ProfileContractTest(unittest.TestCase):
    def test_workspace_selects_latest_configured_profile(self) -> None:
        raw = (WORKSPACE / ".release/contract.json").read_bytes()
        entries = json.loads(raw)["profiles"]
        self.assertTrue(entries)
        latest = entries[-1]
        self.assertEqual(CURRENT_RELEASE_PROFILE, latest["profile"])
        current = get_release_profile(latest["profile"])
        self.assertEqual(current["version"], (WORKSPACE / "backend/cmd/server/VERSION").read_text().strip())
        self.assertEqual(current["parent"], latest["parent"])
        self.assertEqual(current["new_migrations"], latest["new_migrations"])
        with self.assertRaisesRegex(ValueError, "historical"):
            get_release_profile(latest["parent"])
        self.assertEqual(get_profile(latest["parent"])["name"], latest["parent"])

    def test_parses_contiguous_additive_profiles(self) -> None:
        raw = contract(
            profile("287", "286", "0.2.24-baiyu", ["314_example.sql"]),
            profile("288", "287", "0.2.25-baiyu", ["315_example.sql", "316_another.sql"]),
        )

        parsed = parse_profile_contract(raw.encode())

        self.assertEqual(list(parsed), ["287", "288"])
        self.assertEqual(parsed["287"]["parent"], "286")
        self.assertEqual(parsed["288"]["new_migrations"], ["315_example.sql", "316_another.sql"])

    def test_rejects_invalid_profile_contracts(self) -> None:
        valid = profile("287", "286", "0.2.24-baiyu", ["314_example.sql"])
        invalid = (
            {**valid, "profile": "288"},
            {**valid, "parent": "285"},
            {**valid, "version": "0.2-baiyu"},
            {**valid, "new_migrations": ["314_example.sql", "314_other.sql"]},
            {**valid, "extra": True},
        )
        for entry in invalid:
            with self.subTest(entry=entry), self.assertRaises(ValueError):
                parse_profile_contract(contract(entry).encode())

        with self.assertRaisesRegex(ValueError, "duplicate profile contract field"):
            parse_profile_contract(b'{"schema":1,"schema":1,"profiles":[]}')
        with self.assertRaisesRegex(ValueError, "too large"):
            parse_profile_contract(b" " * (128 * 1024 + 1))


class ProfileContractRecoveryGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git("init")
        self.git("config", "user.email", "release-tests@example.invalid")
        self.git("config", "user.name", "Release Tests")
        (self.root / "README.md").write_text("baseline\n", encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-m", "baseline")
        self.base = self.git("rev-parse", "HEAD")

    def tearDown(self) -> None:
        self.temp.cleanup()

    def git(self, *args: str) -> str:
        return subprocess.check_output(
            ["git", *args], cwd=self.root, text=True, stderr=subprocess.DEVNULL
        ).strip()

    def commit_contract(self, raw: str) -> str:
        path = self.root / ".release/contract.json"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(raw, encoding="utf-8")
        self.git("add", "-A")
        self.git("commit", "-m", "update release contract")
        return self.git("rev-parse", "HEAD")

    def test_appending_profiles_is_fast_and_preserves_the_parent_chain(self) -> None:
        first_entry = profile("287", "286", "0.2.24-baiyu")
        second_entry = profile("288", "287", "0.2.25-baiyu")
        first_raw = contract(first_entry)
        second_raw = contract(first_entry, second_entry)
        first = self.commit_contract(first_raw)
        second = self.commit_contract(second_raw)

        self.assertEqual(classify(self.root, self.base, first)["mode"], "fast")
        report = classify(self.root, first, second)
        self.assertEqual(report["mode"], "fast")
        self.assertEqual(report["reason_codes"], ["ordinary_change"])
        self.assertEqual(self.git("diff", "--name-only", first, second), ".release/contract.json")

        first_profile, second_profile = parse_profile_contract(second_raw.encode()).values()
        with patch("release.manifest.workspace_root", return_value=self.root):
            self.assertEqual(profile_contract_digest(first, first_profile), hashlib.sha256(first_raw.encode()).hexdigest())
            self.assertEqual(profile_contract_digest(second, second_profile), hashlib.sha256(second_raw.encode()).hexdigest())
            with self.assertRaisesRegex(RuntimeError, "does not match"):
                profile_contract_digest(second, first_profile)

    def test_rewriting_or_removing_a_published_profile_is_blocked(self) -> None:
        original = contract(profile("287", "286", "0.2.24-baiyu"))
        first = self.commit_contract(original)
        rewritten = self.commit_contract(contract(profile("287", "286", "0.2.25-baiyu")))
        with self.assertRaisesRegex(RuntimeError, "cannot be rewritten"):
            classify(self.root, first, rewritten)

        self.git("rm", ".release/contract.json")
        self.git("commit", "-m", "remove release contract")
        removed = self.git("rev-parse", "HEAD")
        with self.assertRaisesRegex(RuntimeError, "cannot be removed"):
            classify(self.root, first, removed)


if __name__ == "__main__":
    unittest.main()
