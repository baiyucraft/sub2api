from __future__ import annotations

import os
from pathlib import Path
import shutil
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.process import run_hidden


class ComposeContractFailClosedTest(unittest.TestCase):
    """Exercise Bash condition contexts that disable errexit inside functions."""

    def setUp(self) -> None:
        self.bash = shutil.which("bash")
        if os.name == "nt":
            candidate = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git/bin/bash.exe"
            if candidate.is_file():
                self.bash = str(candidate)
        if not self.bash:
            self.skipTest("bash unavailable")
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.base = Path(self.temporary.name)
        context = (ROOT / "maintenance/release/context.sh").read_text(encoding="utf-8")
        # Source the actual assertion functions without production-only context setup.
        self.context = self.base / "context-assertions.sh"
        self.context.write_text(context[context.index("assert_http_header_equals() {"):
                                        context.index("assert_prompt_audit_disabled() {")], encoding="utf-8")
        self.helper = ROOT / "maintenance/release/compose-contract.sh"
        self.env = os.environ.copy()
        self.env.pop("BASH_ENV", None)
        self.env.pop("ENV", None)
        self.env.update(FC_BASE=str(self.base), FC_HELPER=str(self.helper), FC_CONTEXT=str(self.context))

    def execute(self, setup: str, command: str, mode: str):
        invocation = {
            "plain": command,
            "if": f"if {command}; then exit 0; else exit 17; fi",
            "or": f"{command} || exit 17",
        }[mode]
        script = r'''
set -Eeuo pipefail
if [[ ${OS:-} == Windows_NT ]]; then
  FC_BASE=$(cygpath -u "$FC_BASE")
  FC_HELPER=$(cygpath -u "$FC_HELPER")
  FC_CONTEXT=$(cygpath -u "$FC_CONTEXT")
fi
source "$FC_HELPER"
''' + setup + "\n" + invocation + "\n"
        return run_hidden([self.bash, "--noprofile", "--norc", "-c", script],
                          env=self.env, text=True, capture_output=True, check=False)

    def rejects(self, setup: str, command: str) -> None:
        for mode in ("plain", "if", "or"):
            with self.subTest(mode=mode):
                result = self.execute(setup, command, mode)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)

    def test_closure_rejects_every_failed_stage(self) -> None:
        setup = r'''
source "$FC_CONTEXT"
load_release_compose_files() { release_compose_args=(); [[ $FAULT != load ]]; }
docker() {
  if [[ $1 == compose ]]; then printf '{}\n'; [[ $FAULT != compose ]]; return; fi
  [[ $FAULT != image_command ]] || return 29
  if [[ $FAULT == image_mismatch ]]; then printf 'wrong\n'; else printf 'expected\n'; fi
}
jq() {
  if [[ $1 == -r ]]; then
    [[ $FAULT != image_value ]] || return 0
    printf 'image\n'; [[ $FAULT != image_parse ]]; return
  fi
  [[ $FAULT != final_assert ]]
}
sub2api_compose_network_mode() { printf 'bridge\n'; [[ $FAULT != network ]]; }
assert_sub2api_healthcheck_contract() { [[ $FAULT != health ]]; }
assert_sub2api_observer_storage_contract() { [[ $FAULT != observer ]]; }
'''
        faults = ("load", "compose", "image_value", "image_parse", "image_command", "image_mismatch",
                  "network", "health", "observer", "final_assert")
        for fault in faults:
            with self.subTest(fault=fault):
                self.rejects(setup + f"\nFAULT={fault}", 'assert_sub2api_compose_closure unused 18080 expected')
        self.rejects(setup + "\nFAULT=none", 'assert_sub2api_compose_closure unused 9999 expected')
        result = self.execute(setup + "\nFAULT=none", 'assert_sub2api_compose_closure unused 18080 expected', "if")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "bridge\n")

    def test_contract_rejects_invalid_port_and_policy_before_jq(self) -> None:
        setup = 'jq() { printf "bridge\\n"; }; docker() { printf "[]\\n"; }'
        for command in (
            'sub2api_healthcheck_url bridge 9999',
            'sub2api_compose_network_mode "{}" 9999',
            'assert_sub2api_healthcheck_contract "{}" bridge 18080 unknown',
            'assert_sub2api_healthcheck_contract "{}" bridge 9999',
            'assert_sub2api_runtime_contract app image bridge 18080 unknown',
            'assert_sub2api_runtime_contract app image bridge 9999',
        ):
            with self.subTest(command=command):
                self.rejects(setup, command)

    def test_runtime_rejects_inspect_output_when_command_fails(self) -> None:
        self.rejects('jq() { return 0; }; docker() { printf "[]\\n"; return 29; }',
                     'assert_sub2api_runtime_contract app image bridge 18080')

    def test_invalid_override_does_not_touch_output(self) -> None:
        for mode in ("plain", "if", "or"):
            output = self.base / "override.yml"
            output.write_text("original\n")
            result = self.execute('', 'write_release_active_override "$FC_BASE/override.yml" image instance 9999 bridge', mode)
            self.assertNotEqual(result.returncode, 0, result.stderr)
            self.assertEqual(output.read_text(), "original\n")

    def test_header_rejects_precondition_and_pipeline_failures(self) -> None:
        for source in ("", 'source "$FC_CONTEXT"'):
            with self.subTest(source=source):
                fixture = source + '\nprintf "X-Ready: valid\\r\\n" > "$FC_BASE/headers"\n'
                self.rejects(fixture, 'assert_http_header_equals "$FC_BASE/missing" X-Ready valid')
                self.rejects(fixture + '\nprintf "X Ready: valid\\n" > "$FC_BASE/headers"',
                             'assert_http_header_equals "$FC_BASE/headers" "X Ready" valid')
                self.rejects(fixture + '\nprintf "X-Ready: bad/value\\n" > "$FC_BASE/headers"',
                             'assert_http_header_equals "$FC_BASE/headers" X-Ready bad/value')
                self.rejects(fixture + '\nprintf "X-Ready: valid\\nX-Ready: valid\\n" > "$FC_BASE/headers"',
                             'assert_http_header_equals "$FC_BASE/headers" X-Ready valid')
                self.rejects(fixture + '\nprintf "X-Ready: valid\\nX-Ready:\\n" > "$FC_BASE/headers"',
                             'assert_http_header_equals "$FC_BASE/headers" X-Ready valid')
                self.rejects(fixture + '\ntr() { command tr "$@"; return 29; }',
                             'assert_http_header_equals "$FC_BASE/headers" X-Ready valid')
                result = self.execute(fixture, 'assert_http_header_equals "$FC_BASE/headers" x-ready valid', "if")
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_compose_file_read_failure_cannot_use_default_closure(self) -> None:
        fixture = r'''
printf 'COMPOSE_FILE=docker-compose.yml\n' > "$FC_BASE/.env"
printf 'services: {}\n' > "$FC_BASE/docker-compose.yml"
awk() { printf '0\n'; return 29; }
'''
        self.rejects(fixture, 'load_release_compose_files "$FC_BASE"')

    def test_active_override_requires_an_existing_base_closure(self) -> None:
        self.rejects('source "$FC_CONTEXT"; release_compose_files=(docker-compose.release-active.yml)',
                     'release_compose_value_with_active_override')


if __name__ == "__main__":
    unittest.main()
