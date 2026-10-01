from __future__ import annotations

import hashlib
import json
import re
import shutil
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock


RELEASE_ROOT = Path(__file__).resolve().parents[2] / "release"
sys.path.insert(0, str(RELEASE_ROOT.parent))

from release.manifest import validate_manifest_profile_contract
from release.migration_planner import CHECKSUM_COMPATIBILITY_RULES, catalog_sha256, checksum_policy_sha256, discover_migration_catalog, migration_checksum
from release.paths import WORKSPACE
from release.profiles import get_profile

HISTORICAL_DR_PROFILES = (
    195, 199, 202, 206, 207, 208, 209, 210, 212, 213, 215,
    *range(232, 260),
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
    drill = f"dr-{profile}-20261001T120000Z"
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


class Profile260ReleaseContractTest(unittest.TestCase):
    def test_profile_260_checksum_registration_matches_raw_sql_and_runtime_catalog(self) -> None:
        filename = "285_upstream_null_rate_lifecycle.sql"
        catalog_path = WORKSPACE / ".agents/skills/sub2api-fork-extension-audit/references/extensions.yaml"
        registration = json.loads(catalog_path.read_text(encoding="utf-8"))
        content = (WORKSPACE / "backend/migrations" / filename).read_bytes()
        self.assertEqual(
            registration["migration_contracts"][filename],
            "e20050102cb990b4cac50d62c46e6602649011755aafaab38cdc61937515385a",
        )
        self.assertEqual(registration["migration_contracts"][filename], hashlib.sha256(content).hexdigest())
        self.assertEqual(registration["current_profile"]["id"], "260")
        self.assertEqual(registration["current_profile"]["new_migrations"], [filename])
        runtime = next(item for item in discover_migration_catalog(WORKSPACE) if item["filename"] == filename)
        self.assertEqual(runtime["checksum"], migration_checksum(content))
        self.assertFalse(runtime["non_transactional"])
        self.assertNotIn(filename, CHECKSUM_COMPATIBILITY_RULES)
        for name, contract in (("259", registration["historical_profiles"]["259"]), ("260", registration["current_profile"])):
            profile = get_profile(name)
            for field in ("version", "parent", "gate_schema", "new_migrations", "release_policy"):
                self.assertEqual(contract[field], profile[field])

    def test_python_manifest_binds_current_and_historical_profile_contracts(self) -> None:
        catalog = discover_migration_catalog(WORKSPACE)
        for name in ("259", "260"):
            profile = get_profile(name)
            manifest = {
                "schema": 2,
                "release_asset_layout": "skill-v1",
                "deployment_mode": "blue-green",
                "release_id": identities(name)["release_id"],
                "profile": name,
                "version": profile["version"],
                "commit_sha": "a" * 40,
                "origin": profile["origin"],
                "vm_identity": profile["vm_identity"],
                "migration_catalog": catalog,
                "catalog_sha256": catalog_sha256(catalog),
                "checksum_policy_sha256": checksum_policy_sha256(),
                "parent_profile": profile["parent"],
                "new_migrations": profile["new_migrations"],
                "release_policy": profile["release_policy"],
            }
            with mock.patch("release.manifest.discover_migration_catalog", return_value=catalog):
                validate_manifest_profile_contract(manifest, profile)
                invalid = [
                    {"version": "0.2.10-baiyu"},
                    {"parent_profile": "257"},
                    {"profile": "261"},
                    {"release_id": identities("261")["release_id"]},
                    {"new_migrations": ["284_unified_proxy_bindings.sql"]},
                    {"new_migrations": [] if name == "260" else ["285_upstream_null_rate_lifecycle.sql"]},
                    {"new_migrations": ["285_upstream_null_rate_lifecycle.sql", "284_unified_proxy_bindings.sql"]},
                ]
                for fields in invalid:
                    with self.subTest(profile=name, fields=fields), self.assertRaises(RuntimeError):
                        validate_manifest_profile_contract({**manifest, **fields}, profile)

    def test_every_release_and_dr_path_accepts_260_and_259_but_rejects_261(self) -> None:
        contracts = regex_contracts()
        self.assertEqual(
            [name for name, *_ in contracts],
            ["vm-validate.sh", "vm-validate.sh", "sign-gate.sh", "sign-dr-evidence.sh",
             "promote-dr-baseline.sh", "promote-dr-baseline.sh"],
        )
        for name, variable, expression, _ in contracts:
            expression = expression.replace("$evidence_root", "/opt/sub2api-deploy/dr-evidence")
            for profile, allowed in (("259", True), ("260", True), ("261", False)):
                with self.subTest(script=name, variable=variable, profile=profile):
                    value = identities(profile)[variable]
                    self.assertEqual(re.fullmatch(expression, value) is not None, allowed)
                    self.assertIsNone(re.fullmatch(expression, value + "/extra"))

    def test_both_bootstraps_preserve_history_and_include_260_exactly_once(self) -> None:
        for name, variable in (
            ("bootstrap_vm_signer.sh", "selftest_profile"),
            ("bootstrap_backup_dr_assets.sh", "profile"),
        ):
            with self.subTest(script=name):
                loop = re.search(rf"^for {variable} in ([0-9 ]+); do$", source(name), re.MULTILINE)
                self.assertIsNotNone(loop)
                profiles = tuple(map(int, loop[1].split()))
                self.assertEqual(profiles, (*HISTORICAL_DR_PROFILES, 260))
                self.assertNotIn(261, profiles)

    def test_signer_and_bootstrap_route_260_and_259_to_gate_v2(self) -> None:
        for name, variable in (
            ("sign-gate.sh", "gate_profile"),
            ("bootstrap_vm_signer.sh", "selftest_profile"),
        ):
            with self.subTest(script=name):
                condition = next(line for line in source(name).splitlines() if f"if [[ ${variable} == 242" in line)
                profiles = tuple(map(int, re.findall(rf"\${variable} == ([0-9]+)", condition)))
                self.assertEqual(profiles, tuple(range(242, 261)))


class Profile260BashContractTest(unittest.TestCase):
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

    def test_release_helpers_have_valid_bash_syntax(self) -> None:
        for name in (*SCRIPTS, "production-space-clean.sh", "production-recovery-retention-clean.sh"):
            with self.subTest(script=name):
                result = subprocess.run(
                    [self.bash, "--noprofile", "--norc", "-n"],
                    input=source(name), capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_cleanup_shell_guards_accept_260_and_259_and_reject_261(self) -> None:
        space = source("production-space-clean.sh")
        guards = re.findall(r"\[\[ \$(?:release_id|candidate_release) =~ .+? \]\]", space)
        self.assertEqual(len(guards), 2)
        retention = source("production-recovery-retention-clean.sh")
        pattern = re.search(r"^release_id_pattern='([^']+)'$", retention, re.MULTILINE)[1]
        guards.append(f"[[ $release_id =~ {pattern} ]]")
        for guard in guards:
            variable = "candidate_release" if "$candidate_release" in guard else "release_id"
            body = (
                f"{variable}=$1\n"
                f"{guard}\n"
            )
            for profile, allowed in (("259", True), ("260", True), ("261", False), ("0260", False)):
                with self.subTest(guard=guard, profile=profile):
                    result = self.run_contract(body, identities(profile)["release_id"])
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_real_bash_path_guards_accept_260_and_259_and_reject_261(self) -> None:
        for name, variable, _, assertion in regex_contracts():
            for profile, allowed in (("259", True), ("260", True), ("261", False)):
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
            for profile, allowed in (("259", True), ("260", True), ("261", False)):
                with self.subTest(assertion=assertion, profile=profile):
                    result = self.run_contract("profile=$1\n" + assertion, profile)
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_vm_version_parent_and_exact_migrations_contract(self) -> None:
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
    '.new_migrations == ["285_upstream_null_rate_lifecycle.sql"]') [[ $new_migrations == null_rate ]] ;;
    *) return 1 ;;
  esac
}
"""
        cases = (
            ("258", "0.2.10-baiyu", "257", "empty", True),
            ("259", "0.2.11-baiyu", "258", "empty", True),
            ("259", "0.2.10-baiyu", "258", "empty", False),
            ("259", "0.2.11-baiyu", "257", "empty", False),
            ("259", "0.2.11-baiyu", "258", "null_rate", False),
            ("260", "0.2.11-baiyu", "259", "null_rate", True),
            ("260", "0.2.10-baiyu", "259", "null_rate", False),
            ("260", "0.2.11-baiyu", "258", "null_rate", False),
            ("260", "0.2.11-baiyu", "260", "null_rate", False),
            ("260", "0.2.11-baiyu", "259", "empty", False),
            ("260", "0.2.11-baiyu", "259", "proxy_binding", False),
            ("260", "0.2.11-baiyu", "259", "null_rate_and_proxy", False),
            ("261", "0.2.11-baiyu", "260", "null_rate", False),
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
        for profile in ("259", "260"):
            for schema, version, identity, allowed in (
                ("2", "2", profile, True), ("1", "2", profile, False),
                ("2", "1", profile, False), ("2", "2", "261", False),
            ):
                with self.subTest(profile=profile, schema=schema, version=version, identity=identity):
                    result = self.run_contract(jq_stub + checks, profile, schema, version, identity)
                    self.assertEqual(result.returncode == 0, allowed, result.stderr)

    def test_dr_signer_and_promoter_reject_mismatched_release_and_drill_profiles(self) -> None:
        signer = fragment("sign-dr-evidence.sh", "[[ $evidence =~", "expected_signature=")
        promoter = fragment("promote-dr-baseline.sh", "[[ $release_id =~", "if [[ $test_mode ==")
        for release_profile, drill_profile, allowed in (
            ("259", "259", True), ("260", "260", True),
            ("260", "259", False), ("259", "260", False),
            ("260", "261", False), ("261", "260", False), ("261", "261", False),
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
