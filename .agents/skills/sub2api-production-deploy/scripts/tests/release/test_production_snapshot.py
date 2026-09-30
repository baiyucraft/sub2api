from __future__ import annotations

import json
import os
import shlex
import shutil
import subprocess
import sys
import unittest
from pathlib import Path


DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))

from release.atomic import canonical_json
from release.production_snapshot import snapshot_sha256
from release.production_snapshot import snapshot_script
from release.production_snapshot import runtime_commit_script
from release.paths import MAINTENANCE_ROOT


def integration_script() -> str:
    preflight = (MAINTENANCE_ROOT / "preflight.sh").read_text(encoding="utf-8")
    source = '  source "$active_claim/assets/runtime-identity.sh"\n'
    begin = preflight.index(source)
    end = preflight.index('  [[ $(jq -er \'.evidence.catalog_sha256\'', begin)
    fragment = preflight[begin:end].replace(source, runtime_commit_script() + "\n", 1)
    image = "sha256:" + "a" * 64
    commit = "b" * 40
    other = "c" * 40
    tag = "sub2api:baiyu-0.2.10-baiyu-" + commit
    other_tag = "sub2api:baiyu-0.2.10-baiyu-" + other
    rows = [{"filename": "241_example.sql", "checksum": "d" * 64}]
    drift = [{"filename": "241_example.sql", "checksum": "e" * 64}]
    expected = snapshot_sha256({
        "current_image_id": image,
        "production_current_commit_sha": commit,
        "schema_migrations": rows,
    })
    cases = (
        ("digest_unique", image, [tag], rows, True),
        ("missing", image, [], rows, False),
        ("ambiguous", image, [tag, other_tag], rows, False),
        ("tag_conflict", tag, [other_tag], rows, False),
        ("same_commit", tag, [tag, "sub2api:baiyu-0.2.11-baiyu-" + commit], rows, True),
        ("migration_snapshot_drift", image, [tag], drift, False),
    )
    script = r'''set -Eeuo pipefail
unset BASH_ENV ENV
docker() {
  [[ $# == 5 && $1 == image && $2 == inspect && $3 == -f ]] || return 90
  case "$4" in
    '{{json .RepoTags}}')
      [[ $5 == "$active_image" ]] || return 91
      printf '%s\n' "$fixture_tags"
      ;;
    '{{.Id}}')
      [[ $5 == "$image_ref" ]] || return 92
      printf '%s\n' "$active_image"
      ;;
    *) return 93 ;;
  esac
}
jq() {
  if [[ $# == 3 && $1 == -er && $2 == .evidence.production_snapshot_sha256 && $3 == "$active_claim/gate.json" ]]; then
    printf '%s\n' "$gate_expected"
  else
    command jq "$@"
  fi
}
export -f docker jq
active_claim=/mock-identity-claim
export active_claim active_image image_ref fixture_tags snapshot_rows gate_expected
'''
    script += "active_image=" + shlex.quote(image) + "\n"
    script += "gate_expected=" + shlex.quote(expected) + "\n"
    script += "preflight_fragment=" + shlex.quote(fragment) + "\n"
    for name, reference, tags, migrations, passes in cases:
        script += "image_ref=" + shlex.quote(reference) + "\n"
        script += "fixture_tags=" + shlex.quote(json.dumps(tags)) + "\n"
        script += "snapshot_rows=" + shlex.quote(json.dumps(migrations)) + "\n"
        # A separate shell preserves errexit even when its status is tested.
        script += 'if bash --noprofile --norc -e -u -o pipefail -c "$preflight_fragment"; then\n'
        script += "  actual=0\nelse\n  actual=$?\nfi\n"
        condition = '[[ $actual == 0 ]]' if passes else '[[ $actual != 0 ]]'
        script += condition + " || { printf '%s\\n' " + shlex.quote(name + " returned unexpected status") + " >&2; exit 1; }\n"
    return script + "printf 'identity_preflight_regression=pass\\n'\n"


class ProductionSnapshotTest(unittest.TestCase):
    def test_identity_preflight_executable_regression(self) -> None:
        bash = shutil.which("bash")
        if bash is None and os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git" / "bin" / "bash.exe"
            if candidate.is_file():
                bash = str(candidate)
        if bash is None:
            self.skipTest("Bash and jq are required for the identity preflight regression")
        environment = os.environ.copy()
        environment.pop("BASH_ENV", None)
        environment.pop("ENV", None)
        available = subprocess.run(
            [bash, "--noprofile", "--norc", "-c", "command -v jq >/dev/null"],
            env=environment, capture_output=True, text=True, timeout=10,
        )
        if available.returncode != 0:
            self.skipTest("Bash and jq are required for the identity preflight regression")
        completed = subprocess.run(
            [bash, "--noprofile", "--norc"], input=integration_script(),
            env=environment, capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stdout, "identity_preflight_regression=pass\n")

    def test_python_and_shell_preflight_share_signed_identity_helper(self) -> None:
        helper = (MAINTENANCE_ROOT / "runtime-identity.sh").read_text(encoding="utf-8")
        self.assertEqual(runtime_commit_script(), helper)
        self.assertIn(helper, snapshot_script())
        preflight = (MAINTENANCE_ROOT / "preflight.sh").read_text(encoding="utf-8")
        self.assertIn('source "$active_claim/assets/runtime-identity.sh"', preflight)
        self.assertIn('resolve_production_commit "$active_image" "$image_ref"', preflight)
        self.assertNotIn('image_ref =~ -([0-9a-f]{40})$', preflight)

    def test_snapshot_script_uses_application_role_and_portable_base64(self) -> None:
        script = snapshot_script()
        self.assertIn("-U sub2api -d sub2api", script)
        self.assertIn("base64 | tr -d", script)
        self.assertNotIn("POSTGRES_USER:-postgres", script)
        self.assertIn("all(.[]; type == \"object\"", script)

    def test_digest_started_container_resolves_only_unique_full_sha_release_tag(self) -> None:
        script = snapshot_script()
        self.assertIn("docker image inspect -f '{{json .RepoTags}}' \"$image\"", script)
        self.assertIn("^sub2api:baiyu-[0-9][0-9A-Za-z.-]*-[0-9a-f]{40}$", script)
        self.assertIn("| unique", script)
        self.assertIn("$(jq -r 'length' <<<\"$commits\") == 1", script)
        self.assertNotIn("git rev-parse main", script)

    def test_snapshot_digest_matches_canonical_persisted_document(self) -> None:
        snapshot = {
            "current_image_id": "sha256:" + "a" * 64,
            "schema_migrations": [
                {"filename": "241_example.sql", "checksum": "b" * 64},
                {"filename": "242_example.sql", "checksum": "c" * 64},
            ],
            "plan": {"pending": []},
        }

        persisted = json.loads(canonical_json(snapshot))

        self.assertEqual(snapshot_sha256(snapshot), snapshot_sha256(persisted))


if __name__ == "__main__":
    unittest.main()
