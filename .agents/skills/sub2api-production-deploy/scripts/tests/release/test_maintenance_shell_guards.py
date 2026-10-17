"""Exercise production maintenance fragments with inert command failures only."""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[2]
MAINTENANCE = SCRIPTS / "maintenance/release"
BASH = shutil.which("bash")
if os.name == "nt" and Path("C:/Program Files/Git/bin/bash.exe").is_file():
    BASH = "C:/Program Files/Git/bin/bash.exe"


def function(source: str, name: str) -> str:
    found = re.search(r"^" + re.escape(name) + r"\(\) ([{(])\n", source, re.M)
    if not found:
        raise AssertionError("missing real function " + name)
    closing = "}" if found[1] == "{" else ")"
    end = re.search(r"^" + re.escape(closing) + r"\n", source[found.end():], re.M)
    if not end:
        raise AssertionError("missing function terminator " + name)
    return source[found.start():found.end() + end.end()]


@unittest.skipUnless(BASH, "Bash is required for inert maintenance fixtures")
class MaintenanceShellGuardsTest(unittest.TestCase):
    def shell(self, script: str, *args: str):
        return subprocess.run(
            [str(BASH), "--noprofile", "--norc", "-c", script, "fixture", *args],
            capture_output=True, text=True, timeout=20,
            env={k: v for k, v in os.environ.items() if k not in {"BASH_ENV", "ENV", "SHELLOPTS"}},
        )

    def compose(self, *names: str) -> str:
        source = (MAINTENANCE / "compose-contract.sh").read_text()
        return "\n".join(function(source, name) for name in names)

    def test_cleanup_migration_producer_requires_status_and_preserves_empty_inventory(self):
        actual = function((MAINTENANCE / "cleanup-state.sh").read_text(), "verify_migrations_restored")
        script = r'''set -Eeuo pipefail
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
state_dir=$work/state
mkdir "$state_dir" "$work/tmp"
export TMPDIR=$work/tmp
value=$1
status=$2
write_status=$3
if [[ -n $value ]]; then builtin printf '%s\n' "$value" > "$state_dir/pre-migrations.tsv"; else : > "$state_dir/pre-migrations.tsv"; fi
docker() { builtin printf '%s' "$value"; return "$status"; }
printf() { builtin printf "$@"; return "$write_status"; }
''' + actual + r'''
if verify_migrations_restored; then builtin printf 'receipt=true\n'; else builtin printf 'rejected=true\n'; fi
[[ -z $(command find "$TMPDIR" -mindepth 1 -print -quit) ]]
'''
        for value, status, write_status in (("", "0", "0"), ("migration|sha", "0", "0"),
                                            ("migration|sha", "17", "0"), ("migration|sha", "0", "17")):
            with self.subTest(value=value, status=status, write_status=write_status):
                result = self.shell(script, value, status, write_status)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("receipt=true" if status == write_status == "0" else "rejected=true", result.stdout)

    def test_cleanup_candidate_absence_requires_successful_names_inventory(self):
        source = (MAINTENANCE / "cleanup-slots.sh").read_text()
        actual = source[source.index("candidate_removed=false\n"):]
        script = r'''set -Eeuo pipefail
candidate_container=$1
inventory=${2//|/$'\n'}
status=$3
docker() { if [[ $1 == ps ]]; then builtin printf '%s' "$inventory"; return "$status"; fi; builtin printf 'mutation=%s\n' "$1" >&2; }
application_connection_count() { builtin printf '0\n'; }
''' + actual
        for candidate, inventory, status, expected in (
            ("candidate", "sub2api|candidate", "0", "true"),
            ("candidate", "sub2api|candidate-suffix", "0", "false"),
            ("sub2api", "sub2api", "0", "false"),
            ("candidate", "sub2api|candidate", "17", None),
            ("candidate", "", "17", None),
        ):
            with self.subTest(candidate=candidate, inventory=inventory, status=status):
                result = self.shell(script, candidate, inventory, status)
                if expected is None:
                    self.assertNotEqual(result.returncode, 0, result.stderr)
                    self.assertNotIn("candidate_removed=", result.stdout)
                    self.assertNotIn("mutation=", result.stderr)
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("candidate_removed=" + expected, result.stdout)
                    self.assertEqual("mutation=rm" in result.stderr, expected == "true")

    def test_required_captures_reject_correct_looking_stdout_and_nonzero(self):
        # Extract the real capture statements from every maintenance asset.
        # Each producer emits its expected-looking value before exit 17. The
        # subsequent receipt represents a mutation/attestation boundary.
        cases = []
        for path in sorted(MAINTENANCE.glob("*.sh")):
            for number, line in enumerate(path.read_text().splitlines(), 1):
                if re.match(r"^\s*_required_(?:arg_)?\d+=\$\(", line) and line.endswith((")", ") || return 1")) and "$(<" not in line:
                    cases.append((path.name, number, line.strip()))
        self.assertGreater(len(cases), 230)
        commands = ("docker", "systemctl", "sub2api_required_systemctl", "jq", "find", "stat", "id", "realpath",
                    "sha256sum", "awk", "sed", "grep", "wc", "curl", "date", "basename", "cat", "systemd-detect-virt",
                    "assert_sub2api_compose_closure", "application_connection_count", "query", "timeout", "printf",
                    "_recovery_compose_render_sha256", "release_compose_value_with_active_override", "sub2api_compose_network_mode")
        stubs = "\n".join(name + '() { builtin printf "%s\\n" "$fixture_value"; return "$fixture_status"; }' for name in commands)
        for filename, number, capture in cases:
            with self.subTest(file=filename, line=number):
                variables = set(re.findall(r"\$(?:\{)?([A-Za-z_][A-Za-z0-9_]*)", capture))
                variables -= {"PATH", "HOME", "BASH_SOURCE", "PWD", "SHELLOPTS", "EUID", "UID"}
                setup = "\n".join(name + "=fixture" for name in sorted(variables))
                script = 'set -Eeuo pipefail\nfixture_value=healthy\nfixture_status=$1\n' + setup + "\n" + stubs
                # Normal top-level call preserves errexit while explicit
                # returns are independently exercised below under if callers.
                invocation = "run_fixture fixture fixture fixture fixture fixture\n"
                script += "\nrun_fixture() {\n" + capture + '\nbuiltin printf "receipt=true\\n"\n}\n' + invocation
                failed = self.shell(script, "17")
                self.assertNotEqual(failed.returncode, 0, failed.stdout + failed.stderr)
                self.assertNotIn("receipt=true", failed.stdout)
                passed = self.shell(script, "0")
                self.assertEqual(passed.returncode, 0, passed.stderr)
                self.assertIn("receipt=true", passed.stdout)
                if capture.endswith(" || return 1"):
                    conditional = self.shell(script.removesuffix(invocation) + "if " + invocation.strip() + "; then :; fi\n", "17")
                    self.assertEqual(conditional.returncode, 0, conditional.stderr)
                    self.assertNotIn("receipt=true", conditional.stdout)

    def test_nul_inventory_publishes_only_complete_success_and_cleans_temporary(self):
        helper = self.compose("sub2api_capture_nul")
        script = r'''set -Eeuo pipefail
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
export TMPDIR=$work
values=(previous)
producer() { builtin printf 'one with spaces\0two\nlines\0'; return "$1"; }
''' + helper + r'''
if sub2api_capture_nul values producer "$1"; then
  [[ ${#values[@]} == 2 && ${values[0]} == 'one with spaces' && ${values[1]} == $'two\nlines' ]]
  printf 'receipt=true\n'
else
  [[ ${values[*]} == previous ]]
  printf 'rejected=true\n'
fi
[[ -z $(command find "$work" -mindepth 1 -print -quit) ]]
'''
        for status in ("0", "17"):
            with self.subTest(status=status):
                result = self.shell(script, status)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("receipt=true" if status == "0" else "rejected=true", result.stdout)
                if status != "0":
                    self.assertNotIn("receipt=true", result.stdout)
        empty = self.shell('set -Eeuo pipefail\n' + helper + '\nvalues=(previous)\nsub2api_capture_nul values true\n[[ ${#values[@]} == 0 ]]\n')
        self.assertEqual(empty.returncode, 0, empty.stderr)
        read_error = self.shell('set -Eeuo pipefail\n' + helper + r'''
values=(previous)
mapfile() { return 17; }
if sub2api_capture_nul values printf 'entry\0'; then printf 'receipt=true\n'; else [[ ${values[*]} == previous ]]; fi
''')
        self.assertEqual(read_error.returncode, 0, read_error.stderr)
        self.assertNotIn("receipt=true", read_error.stdout)

    def test_systemctl_known_nonzero_states_do_not_admit_query_errors(self):
        helper = self.compose("sub2api_required_systemctl")
        for operation, value, code, accepted in (
            ("is-active", "active", 0, True), ("is-active", "inactive", 3, True),
            ("is-active", "failed", 3, True), ("is-enabled", "masked", 1, True),
            ("is-enabled", "disabled", 1, True), ("is-enabled", "enabled", 0, True),
            ("is-active", "inactive", 17, False), ("is-active", "active", 17, False),
            ("is-enabled", "masked", 17, False), ("is-active", "", 4, False),
        ):
            with self.subTest(operation=operation, value=value, code=code):
                script = 'set -Eeuo pipefail\nsystemctl() { builtin printf "%s\\n" "$2"; return "$3"; }\n' + helper
                script += '\nif actual=$(sub2api_required_systemctl "$1" "$2" "$3"); then printf "receipt=%s\\n" "$actual"; fi\n'
                result = self.shell(script, operation, value, str(code))
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual("receipt=" in result.stdout, accepted)

    def test_all_real_http_readiness_conditions_require_transport_success(self):
        count = 0
        for filename in ("expose.sh", "switch.sh", "finalize.sh", "rollback-route.sh"):
            for line in (MAINTENANCE / filename).read_text().splitlines():
                if re.match(r"\s*if \w+=\$\(curl ", line) and "== 200 ]]" in line:
                    condition = line.strip().removeprefix("if ")
                    condition = condition[:condition.index("== 200 ]]") + len("== 200 ]]")]
                    count += 1
                    names = set(re.findall(r"\$(?:\{)?([A-Za-z_][A-Za-z0-9_]*)", condition))
                    setup = "\n".join(name + "=fixture" for name in names)
                    script = 'set -Eeuo pipefail\n' + setup + '\ncurl() { builtin printf 200; return "$fixture_status"; }\n'
                    script += '\nfixture_status=$1\nif ' + condition + '; then printf "receipt=true\\n"; fi\n'
                    for code in ("0", "17"):
                        with self.subTest(file=filename, condition=condition, status=code):
                            result = self.shell(script, code)
                            self.assertEqual(result.returncode, 0, result.stderr)
                            self.assertEqual("receipt=true" in result.stdout, code == "0")
        self.assertEqual(count, 7)

    def test_external_freeze_distinguishes_known_transport_failures(self):
        source = (MAINTENANCE / "external-freeze-check.sh").read_text()
        for code, status, accepted in (("503", "0", True), ("200", "0", False), ("200", "17", False),
                                       ("503", "17", False), ("000", "7", True), ("000", "28", True),
                                       ("000", "52", True), ("000", "56", True), ("000", "17", False),
                                       ("000", "0", False), ("600", "0", False), ("", "17", False)):
            with self.subTest(code=code, status=status):
                result = self.shell('PUBLIC_DOMAIN=fixture\nfixture_code=$1; fixture_status=$2\ncurl() { builtin printf "%s" "$fixture_code"; return "$fixture_status"; }\n' + source, code, status)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)
                self.assertEqual("public_health_blocked=true" in result.stdout, accepted)

    def test_conditional_rollback_copy_failure_cannot_publish_target(self):
        source = (MAINTENANCE / "rollback-nginx-ingress.sh").read_text()
        script = r'''set -Eeuo pipefail
work=$(mktemp -d); trap 'command rm -rf -- "$work"' EXIT
txn=$work; mkdir -p "$txn/files"; printf old > "$txn/files/site"
cp() { return 17; }
mv() { printf 'published=true\n'; }
''' + function(source, "restore_target") + r'''
validate_target() { return 0; }
if restore_target site "$work/target" present; then printf 'receipt=true\n'; fi
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("receipt=true", result.stdout)
        self.assertNotIn("published=true", result.stdout)

    def test_marker_metadata_failure_cannot_publish_even_with_correct_stdout(self):
        script = r'''set -Eeuo pipefail
work=$(mktemp -d); trap 'command rm -rf -- "$work"' EXIT
docker() {
  case "$*" in
    info*) printf '[]\n';;
    *Mounts*) printf '%s\n' "$work";;
    *Uid:*|*Gid:*) printf '0\n';;
  esac
}
chown() { return 0; }; chmod() { return 0; }
stat() { printf '0:0:600:1\n'; return 17; }
mv() { printf 'published=true\n'; }
''' + self.compose("write_release_activation_marker") + r'''
if write_release_activation_marker sub2api instance; then printf 'receipt=true\n'; fi
[[ $RELEASE_ACTIVATION_MARKER_FAILURE_REASON == marker_publish ]]
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("receipt=true", result.stdout)
        self.assertNotIn("published=true", result.stdout)

    def test_real_snapshot_document_failure_precedes_digest(self):
        for filename in ("preflight.sh", "switch.sh"):
            source = (MAINTENANCE / filename).read_text()
            lines = source.splitlines()
            index = next(i for i, line in enumerate(lines) if "snapshot_document=$(jq -cSn" in line)
            fragment = "\n".join(lines[index:index + 2])
            result = self.shell('set -Eeuo pipefail\nactive_image=fixture; current_image=fixture; production_current_commit_sha=fixture; snapshot_rows="[]"\njq() { printf "{}"; return 17; }\nsha256sum() { printf "digest_started=true\\n"; }\n' + fragment + '\nprintf "receipt=true\\n"\n')
            self.assertNotEqual(result.returncode, 0, result.stderr)
            self.assertNotIn("receipt=true", result.stdout)
            self.assertNotIn("digest_started=true", result.stdout)

    def test_migration_inventory_preserves_empty_success_and_rejects_partial_failure(self):
        for filename in ("context.sh", "switch.sh"):
            lines = (MAINTENANCE / filename).read_text().splitlines()
            for index, line in enumerate(lines):
                if "migration_inventory=$(jq -r " not in line:
                    continue
                fragment = "\n".join(lines[index:index + 3])
                for stdout, code in (("", "0"), ("300_upstream_health_history_index.sql", "0"), ("300_upstream_health_history_index.sql", "17")):
                    script = 'set -Eeuo pipefail\nactive_claim=fixture\nfixture_stdout=$1; fixture_code=$2\njq() { printf "%s" "$fixture_stdout"; return "$fixture_code"; }\nrun_fixture() {\n' + fragment + '\nprintf "receipt=true count=%s\\n" "${#migrations[@]}"\n}\nrun_fixture\n'
                    result = self.shell(script, stdout, code)
                    self.assertEqual(result.returncode == 0, code == "0", result.stderr)
                    self.assertEqual("receipt=true" in result.stdout, code == "0")

    def test_conditional_nginx_inventory_rejects_partial_find_and_grep_query_error(self):
        source = (MAINTENANCE / "nginx-ingress-contract.sh").read_text()
        script = r'''set -Eeuo pipefail
work=$(mktemp -d); trap 'command rm -rf -- "$work"' EXIT
mkdir "$work/sites-enabled"
printf 'proxy_pass http://sub2api_release_backend;\n' > "$work/sites-enabled/site.conf"
NGINX_MANAGED_PROXY_PATTERN='proxy_pass'
''' + self.compose("sub2api_capture_nul") + function(source, "find_managed_nginx_site").replace("/etc/nginx/sites-enabled", "$work/sites-enabled") + r'''
case_name=$1
find() { command find "$work/sites-enabled" -maxdepth 1 -type f -print0; [[ $case_name != find_error ]]; }
grep() { [[ $case_name != grep_error ]] || return 17; command grep "$@"; }
if site=$(find_managed_nginx_site); then printf 'receipt=true\n'; fi
'''
        for case in ("pass", "find_error", "grep_error"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual("receipt=true" in result.stdout, case == "pass")

    def test_conditional_checkpoint_apply_failure_keeps_started_without_done(self):
        source = (MAINTENANCE / "recovery-checkpoint.sh").read_text()
        script = r'''set -Eeuo pipefail
work=$(mktemp -d); trap 'command rm -rf -- "$work"' EXIT
RECOVERY_CHECKPOINT_ROOT=$work
RECOVERY_CHECKPOINT_PHASES=(compose_restored)
RECOVERY_CHECKPOINT_IDENTITY=identity
recovery_checkpoint_store() { builtin printf '%s\n' "$2" > "$work/$1"; }
apply_fixture() { return 17; }
verify_fixture() { printf 'verified=true\n'; }
''' + function(source, "recovery_checkpoint_run") + r'''
if recovery_checkpoint_run compose_restored verify_fixture apply_fixture; then printf 'receipt=true\n'; fi
[[ -f $work/compose_restored.started && ! -e $work/compose_restored.done ]]
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("receipt=true", result.stdout)
        self.assertNotIn("verified=true", result.stdout)

    def test_migration_record_query_preserves_sql_bytes_and_rejects_failed_producer(self):
        source = (MAINTENANCE / "migration-297-assert.sh").read_text()
        script = r'''set -Eeuo pipefail
catalog_checksum=fixture; filename=fixture
query() {
  [[ $1 != *'return 1'* ]] || exit 89
  builtin printf t
  return "$fixture_status"
}
fail() { exit 17; }
''' + function(source, "verify_migration_record") + '\nfixture_status=$1\nverify_migration_record\nbuiltin printf "receipt=true\\n"\n'
        for code in ("0", "17"):
            result = self.shell(script, code)
            self.assertEqual(result.returncode == 0, code == "0", result.stderr)
            self.assertEqual("receipt=true" in result.stdout, code == "0")


if __name__ == "__main__":
    unittest.main()
