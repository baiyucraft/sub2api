from __future__ import annotations

import re
import shutil
import subprocess
import unittest
from pathlib import Path


RELEASE_ROOT = Path(__file__).resolve().parents[2] / "release"
HISTORICAL_DR_PROFILES = (
    195, 199, 202, 206, 207, 208, 209, 210, 212, 213, 215,
    *range(232, 259),
)
SCRIPTS = (
    "vm-validate.sh", "sign-gate.sh", "sign-dr-evidence.sh",
    "bootstrap_vm_signer.sh", "bootstrap_backup_dr_assets.sh", "promote-dr-baseline.sh",
)


def source(name: str) -> str:
    return (RELEASE_ROOT / name).read_text(encoding="utf-8")


def regex_contracts() -> list[tuple[str, str, str, str]]:
    contracts = []
    for name in SCRIPTS:
        for line in source(name).splitlines():
            match = re.fullmatch(r'\[\[ "?\$([a-z_]+)"? =~ (.+) \]\]', line.strip())
            if match and match[1] in {"release_id", "gate", "evidence", "drill_id"}:
                contracts.append((name, match[1], match[2], line.strip()))
    return contracts


def identities(profile: str) -> dict[str, str]:
    release = f"{profile}-aaaaaaaaaaaa-1-aaaaaaaa"
    drill = f"dr-{profile}-20260930T120000Z"
    return {
        "release_id": release,
        "drill_id": drill,
        "gate": f"/opt/sub2api-deploy/release-gates/{release}/output/gate.json",
        "evidence": f"/opt/sub2api-deploy/dr-evidence/{release}/{drill}/evidence.json",
    }


def fragment(name: str, start: str, end: str) -> str:
    script = source(name)
    begin = script.index(start)
    return script[begin:script.index(end, begin)]


def bash_path() -> str | None:
    found = shutil.which("bash")
    if found:
        return found
    git = shutil.which("git")
    if git:
        candidate = Path(git).resolve().parents[1] / "bin" / "bash.exe"
        if candidate.is_file():
            return str(candidate)
    return None


class Profile259ReleaseContractTest(unittest.TestCase):
    def test_every_release_and_dr_path_accepts_259_and_258_but_rejects_260(self) -> None:
        contracts = regex_contracts()
        self.assertEqual(
            [name for name, *_ in contracts],
            ["vm-validate.sh", "vm-validate.sh", "sign-gate.sh", "sign-dr-evidence.sh",
             "promote-dr-baseline.sh", "promote-dr-baseline.sh"],
        )
        for name, variable, expression, _ in contracts:
            expression = expression.replace("$evidence_root", "/opt/sub2api-deploy/dr-evidence")
            for profile, allowed in (("258", True), ("259", True), ("260", False)):
                with self.subTest(script=name, variable=variable, profile=profile):
                    value = identities(profile)[variable]
                    self.assertEqual(re.fullmatch(expression, value) is not None, allowed)
                    self.assertIsNone(re.fullmatch(expression, value + "/extra"))

    def test_both_bootstraps_preserve_history_and_include_259_exactly_once(self) -> None:
        for name, variable in (
            ("bootstrap_vm_signer.sh", "selftest_profile"),
            ("bootstrap_backup_dr_assets.sh", "profile"),
        ):
            with self.subTest(script=name):
                loop = re.search(rf"^for {variable} in ([0-9 ]+); do$", source(name), re.MULTILINE)
                self.assertIsNotNone(loop)
                profiles = tuple(map(int, loop[1].split()))
                self.assertEqual(profiles, (*HISTORICAL_DR_PROFILES, 259))
                self.assertNotIn(260, profiles)

    def test_signer_and_bootstrap_route_259_and_258_to_gate_v2(self) -> None:
        for name, variable in (
            ("sign-gate.sh", "gate_profile"),
            ("bootstrap_vm_signer.sh", "selftest_profile"),
        ):
            with self.subTest(script=name):
                condition = next(line for line in source(name).splitlines() if f"if [[ ${variable} == 242" in line)
                profiles = tuple(map(int, re.findall(rf"\${variable} == ([0-9]+)", condition)))
                self.assertEqual(profiles, tuple(range(242, 260)))


class Profile259BashContractTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.bash = bash_path()
        if cls.bash is None:
            raise unittest.SkipTest("Bash is required for isolated shell contract execution")

    def run_contract(self, body: str, *args: str) -> subprocess.CompletedProcess[str]:
        # Execute only profile checks; full helpers require Linux root, locks and trust assets.
        return subprocess.run(
            [self.bash, "--noprofile", "--norc", "-e", "-u", "-o", "pipefail", "-c", body, "contract", *args],
            capture_output=True, text=True, timeout=10,
        )

    def test_all_six_scripts_have_valid_bash_syntax(self) -> None:
        for name in SCRIPTS:
            with self.subTest(script=name):
                result = subprocess.run(
                    [self.bash, "--noprofile", "--norc", "-n"],
                    input=source(name), capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_real_bash_path_guards_accept_259_and_258_and_reject_260(self) -> None:
        for name, variable, _, assertion in regex_contracts():
            for profile, allowed in (("258", True), ("259", True), ("260", False)):
                with self.subTest(script=name, variable=variable, profile=profile):
                    result = self.run_contract(
                        f'{variable}=$1\nevidence_root=/opt/sub2api-deploy/dr-evidence\n{assertion}',
                        identities(profile)[variable],
                    )
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_vm_v2_and_legacy_profile_guards_remain_closed(self) -> None:
        guards = [
            line.strip() for line in source("vm-validate.sh").splitlines()
            if line.strip().startswith("[[ ") and " || " in line
            and ("$profile ==" in line or '"$profile" ==' in line)
        ]
        self.assertEqual(len(guards), 2)
        for assertion in guards:
            for profile, allowed in (("258", True), ("259", True), ("260", False)):
                with self.subTest(assertion=assertion, profile=profile):
                    result = self.run_contract("profile=$1\n" + assertion, profile)
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_vm_version_parent_and_empty_migrations_contract(self) -> None:
        checks = fragment("vm-validate.sh", '  if [[ "$profile" == 254 ]]; then', "  recovery_gate_mode=")
        jq_stub = """
profile=$1
version=$2
parent=$3
new_migrations=$4
manifest=unused
jq() {
  case "$2" in
    .parent_profile) printf '%s\\n' "$parent" ;;
    '.new_migrations == []') [[ $new_migrations == empty ]] ;;
    *) return 1 ;;
  esac
}
"""
        cases = (
            ("258", "0.2.10-baiyu", "257", "empty", True),
            ("259", "0.2.11-baiyu", "258", "empty", True),
            ("259", "0.2.10-baiyu", "258", "empty", False),
            ("259", "0.2.11-baiyu", "257", "empty", False),
            ("259", "0.2.11-baiyu", "258", "nonempty", False),
            ("258", "0.2.11-baiyu", "257", "empty", False),
            ("258", "0.2.10-baiyu", "258", "empty", False),
            ("258", "0.2.10-baiyu", "257", "nonempty", False),
            ("260", "0.2.11-baiyu", "258", "empty", False),
        )
        for *args, allowed in cases:
            with self.subTest(args=args):
                result = self.run_contract(jq_stub + checks, *args)
                self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_gate_signer_requires_v2_schema_version_and_matching_profile(self) -> None:
        checks = fragment("sign-gate.sh", "gate_schema=$(jq", "expected_signature=")
        jq_stub = """
gate_profile=$1
schema=$2
gate_version=$3
profile_id=$4
gate=unused
jq() {
  case "$2" in
    .manifest.schema) printf '%s\\n' "$schema" ;;
    .gate_version) printf '%s\\n' "$gate_version" ;;
    .profile_id) printf '%s\\n' "$profile_id" ;;
    *) return 1 ;;
  esac
}
"""
        for profile in ("258", "259"):
            for schema, version, identity, allowed in (
                ("2", "2", profile, True), ("1", "2", profile, False),
                ("2", "1", profile, False), ("2", "2", "260", False),
            ):
                with self.subTest(profile=profile, schema=schema, version=version, identity=identity):
                    result = self.run_contract(jq_stub + checks, profile, schema, version, identity)
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_dr_signer_and_promoter_reject_mismatched_release_and_drill_profiles(self) -> None:
        signer = fragment("sign-dr-evidence.sh", "[[ $evidence =~", "expected_signature=")
        promoter = fragment("promote-dr-baseline.sh", "[[ $release_id =~", "if [[ $test_mode ==")
        for release_profile, drill_profile, allowed in (
            ("258", "258", True), ("259", "259", True),
            ("259", "258", False), ("258", "259", False),
            ("259", "260", False), ("260", "259", False), ("260", "260", False),
        ):
            release = identities(release_profile)["release_id"]
            drill = identities(drill_profile)["drill_id"]
            with self.subTest(release=release_profile, drill=drill_profile):
                evidence = f"/opt/sub2api-deploy/dr-evidence/{release}/{drill}/evidence.json"
                signed = self.run_contract("evidence_root=/opt/sub2api-deploy/dr-evidence\nevidence=$1\n" + signer, evidence)
                promoted = self.run_contract("release_id=$1\ndrill_id=$2\n" + promoter, release, drill)
                self.assertEqual(signed.returncode == 0, allowed, signed.stderr)
                self.assertEqual(promoted.returncode == 0, allowed, promoted.stderr)


if __name__ == "__main__":
    unittest.main()
