"""Run real cleaner functions through their conditional/substitution callers.

Only temporary fixture files and shell command stubs are used. No Docker,
production path, release owner or remote connection is touched.
"""
from __future__ import annotations

import os
import re
import shutil
import subprocess
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[2]
BASH = shutil.which("bash")
if os.name == "nt" and Path("C:/Program Files/Git/bin/bash.exe").is_file():
    BASH = "C:/Program Files/Git/bin/bash.exe"


def function(source: str, name: str) -> str:
    match = re.search(r"^" + re.escape(name) + r"\(\) \{\n.*?^\}\n", source, re.M | re.S)
    if not match:
        raise AssertionError("missing real function " + name)
    return match.group()


@unittest.skipUnless(BASH, "Bash is required for shell execution fixtures")
class AdditionalCleanerErrexitTest(unittest.TestCase):
    def shell(self, script: str, case: str = "pass") -> subprocess.CompletedProcess[str]:
        result = subprocess.run(
            [str(BASH), "--noprofile", "--norc", "-c", script, "fixture", case],
            capture_output=True, text=True, timeout=15,
            env={key: value for key, value in os.environ.items() if key not in {"BASH_ENV", "ENV", "SHELLOPTS"}},
        )
        self.assertEqual(result.stderr, "", result.stderr)
        return result

    def vm_fixture(self, entry: str) -> str:
        source = (SCRIPTS / "release/vm-space-clean.sh").read_text(encoding="utf-8")
        self.assertIn(entry, source)
        names = ("is_protected_commit", "assert_no_active_build", "write_referenced_images",
                 "list_image_candidates", "remove_container_candidates", "remove_image_candidates")
        return r'''set -Eeuo pipefail
case_name=$1
work_dir=$(mktemp -d)
trap 'command rm -rf -- "$work_dir"' EXIT
fixture_image_id=sha256:''' + "f" * 64 + r'''
target_commit=''' + "b" * 40 + r'''
compat_commit=
container_id=''' + "c" * 64 + r'''
printf '%s\n' "$container_id" > "$work_dir/container-candidates"
docker() {
  if [[ $1 == ps ]]; then
    [[ $case_name != ps_error ]] || return 71
    if [[ -e $work_dir/container_removed ]]; then
      [[ $case_name != container_residual ]] || printf '%s\n' "$container_id"
    elif [[ $case_name == referenced || $case_name == inspect_error ]]; then
      printf '%s\n' "$container_id"
    fi
  elif [[ $1 == rm ]]; then
    printf 'container_rm\n' >> "$work_dir/events"
    [[ $case_name != rm_error ]] || return 72
    : > "$work_dir/container_removed"
  elif [[ $1 == inspect ]]; then
    case "$3" in
      '{{.Name}}') [[ $case_name != name_error ]] || return 73; printf '/sub2api-dev-pre-fixture\n' ;;
      '{{.State.Status}}') [[ $case_name != status_error ]] || return 73; printf 'exited\n' ;;
      '{{.Image}}') [[ $case_name != inspect_error ]] || return 73; printf '%s\n' "$fixture_image_id" ;;
      *) return 79 ;;
    esac
  elif [[ $1 == image && $2 == ls ]]; then
    [[ $case_name != list_error ]] || return 74
    if [[ " $* " == *' -a '* ]]; then
      [[ $case_name != after_error ]] || return 75
      [[ $case_name != image_residual ]] || printf '%s\n' "$fixture_image_id"
    else
      printf '%s\n' "$fixture_image_id"
    fi
  elif [[ $1 == image && $2 == inspect ]]; then
    if [[ $4 == *RepoTags* ]]; then
      [[ $case_name != tags_error ]] || return 76
      printf 'sub2api:fixture-%s\n' "''' + "a" * 40 + r'''"
    else
      printf '123\n'
    fi
  elif [[ $1 == image && $2 == rm ]]; then
    printf 'image_rm\n' >> "$work_dir/events"
    [[ $case_name != rm_error ]] || return 77
  else
    return 78
  fi
}
ps() {
  [[ $case_name != build_query_error ]] || return 70
  [[ $case_name != active_build ]] || printf 'docker docker build /tmp/contains-awk\n'
}
''' + "\n".join(function(source, name) for name in names) + r'''
set +e
( set -e
''' + entry + r'''
printf 'result=%s\n' "${removed_images:-${removed_containers:-}}"
)
code=$?
set -e
printf 'code=%s\nevents=' "$code"
if [[ -e $work_dir/events ]]; then tr '\n' ',' < "$work_dir/events"; fi
printf '\n'
'''

    def test_vm_image_formal_command_substitution_blocks_failed_proofs(self) -> None:
        script = self.vm_fixture("removed_images=$(remove_image_candidates)")
        for case in ("pass", "ps_error", "list_error", "tags_error", "build_query_error", "active_build",
                     "rm_error", "after_error", "image_residual", "referenced", "inspect_error"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                self.assertEqual(result.returncode, 0)
                values = dict(line.split("=", 1) for line in result.stdout.splitlines())
                if case in {"pass", "referenced"}:
                    self.assertEqual(values["code"], "0")
                    self.assertEqual(values["result"], "0" if case == "referenced" else "1")
                else:
                    self.assertNotEqual(values["code"], "0")
                if case in {"ps_error", "list_error", "tags_error", "build_query_error", "active_build", "referenced", "inspect_error"}:
                    self.assertEqual(values["events"], "")

    def test_vm_container_formal_command_substitution_checks_remove_and_readback(self) -> None:
        script = self.vm_fixture("removed_containers=$(remove_container_candidates)")
        for case in ("pass", "rm_error", "ps_error", "container_residual", "name_error", "status_error"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                values = dict(line.split("=", 1) for line in result.stdout.splitlines())
                self.assertEqual(result.returncode, 0)
                self.assertEqual(values["code"] == "0", case == "pass")
                if case == "pass":
                    self.assertEqual(values["result"], "1")
                if case in {"name_error", "status_error"}:
                    self.assertEqual(values["events"], "")

    def test_cleanup_state_formal_substitution_rejects_wrong_slot_before_read(self) -> None:
        source = (SCRIPTS / "maintenance/release/cleanup-state.sh").read_text(encoding="utf-8")
        actual = (function(source, "active_container_from_slot") + function(source, "assert_cleanup_application_state")).replace("/opt/sub2api/active-app", "$fixture_slot")
        entry = 'assert_cleanup_application_state'
        self.assertIn(entry, source)
        for case in ("pass", "missing", "directory", "health_error", "nginx_error", "timer_error"):
            with self.subTest(case=case):
                script = r'''set -Eeuo pipefail
work_dir=$(mktemp -d)
case_name=$1
trap 'if [[ -e $work_dir/events ]]; then cat "$work_dir/events"; fi; command rm -rf -- "$work_dir"' EXIT
fixture_slot="$work_dir/slot"
case $1 in
 pass|health_error|nginx_error|timer_error) printf 'container=fixture\n' > "$fixture_slot" ;;
 directory) mkdir "$fixture_slot" ;;
esac
sed() { printf 'read\n' >> "$work_dir/events"; printf 'fixture\n'; }
docker() { printf 'health\n' >> "$work_dir/events"; printf 'healthy\n'; [[ $case_name != health_error ]]; }
systemctl() {
 if [[ $1 == is-active ]]; then printf 'active\n'; [[ $case_name != nginx_error ]];
 else printf 'enabled\n'; [[ $case_name != timer_error ]]; fi
}
''' + actual + "\n( " + entry + r'''; printf 'would_delete_state\n' >> "$work_dir/events" )
'''
                result = self.shell(script, case)
                self.assertEqual(result.returncode == 0, case == "pass")
                expected = "read\nhealth\nwould_delete_state\n" if case == "pass" else ("read\nhealth\n" if case.endswith("_error") else "")
                self.assertEqual(result.stdout, expected)

    def test_backup_bundle_formal_or_continue_rejects_each_contract_failure(self) -> None:
        source = (SCRIPTS / "release/backup-release-retention-clean.sh").read_text(encoding="utf-8")
        self.assertIn('verify_bundle "$bundle" "$release_id" || continue', source)
        script = r'''set -Eeuo pipefail
case_name=$1
work_dir=$(mktemp -d)
trap 'command rm -rf -- "$work_dir"' EXIT
bundle="$work_dir/bundle"
mkdir "$bundle"
release_id=273-aaaaaaaaaaaa-1234567890-12345678
printf 'ciphertext fixture\n' > "$bundle/artifact.tar.age"
sha=$(sha256sum "$bundle/artifact.tar.age" | awk '{print $1}')
printf '%s  artifact.tar.age\n' "$sha" > "$bundle/artifact.tar.age.sha256"
printf 'release_id=%s\ntransport_artifact=sub2api-20261017T010101Z.tar.age\nsha256=%s\n' "$release_id" "$sha" > "$bundle/manifest"
if [[ $case_name == duplicate ]]; then printf 'release_id=%s\n' "$release_id" >> "$bundle/manifest"; fi
if [[ $case_name == transport ]]; then sed -i 's/sub2api-20261017T010101Z.tar.age/bad/' "$bundle/manifest"; fi
(cd "$bundle"; sha256sum artifact.tar.age artifact.tar.age.sha256 manifest > bundle.sha256)
release_device=$(stat -c '%d' "$bundle")
case $case_name in
  extra) : > "$bundle/unexpected" ;;
  hardlink) ln "$bundle/artifact.tar.age" "$work_dir/linked" ;;
  device) release_device=wrong ;;
  artifact) printf 'changed\n' >> "$bundle/artifact.tar.age" ;;
  checksum) printf 'changed\n' >> "$bundle/bundle.sha256" ;;
esac
sed() { [[ $case_name != sed_error ]] || return 61; command sed "$@"; }
find() { [[ $case_name != find_error ]] || return 62; command find "$@"; }
stat() { command stat "$@"; [[ $case_name != stat_output_error ]]; }
grep() { command grep "$@"; [[ $case_name != grep_output_error ]]; }
sha256sum() { command sha256sum "$@"; [[ $case_name != sha_output_error ]]; }
''' + function(source, "verify_bundle") + r'''
for item in fixture; do
  verify_bundle "$bundle" "$release_id" || continue
  printf 'selected=true\n'
done
'''
        for case in ("pass", "extra", "hardlink", "device", "duplicate", "transport", "artifact", "checksum", "sed_error", "find_error", "stat_output_error", "grep_output_error", "sha_output_error"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                self.assertEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "selected=true\n" if case == "pass" else "")

    def test_backup_protected_inventory_failure_never_uses_partial_output(self) -> None:
        source = (SCRIPTS / "release/backup-release-retention-clean.sh").read_text(encoding="utf-8")
        start = source.index('find "$release_root" -mindepth 1 -maxdepth 2 -type l')
        end = source.index('LC_ALL=C sort -u "$protected" -o "$protected"', start)
        actual = source[start:end]
        script = r'''set -Eeuo pipefail
case_name=$1
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
release_root="$work/releases"
mkdir -p "$release_root/bundle"
protected="$work/protected"
: > "$protected"
find() { printf '%s\0' "$work/pointer"; [[ $case_name != find_error ]]; }
realpath() { [[ $case_name != resolve_error ]] || return 64; printf '%s\n' "$release_root/bundle"; }
''' + actual + "\nprintf 'inventory_accepted=true\\n'\n"
        for case in ("pass", "find_error", "resolve_error"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                self.assertEqual(result.returncode == 0, case == "pass")
                self.assertEqual(result.stdout, "inventory_accepted=true\n" if case == "pass" else "")

    def test_cleanup_migration_read_error_cannot_match_expected_then_delete(self) -> None:
        source = (SCRIPTS / "maintenance/release/cleanup-state.sh").read_text(encoding="utf-8")
        actual = function(source, "verify_migrations_restored")
        script = r'''set -Eeuo pipefail
case_name=$1
state_dir=$(mktemp -d)
trap 'command rm -rf -- "$state_dir"' EXIT
printf 'fixture.sql|checksum\n' > "$state_dir/pre-migrations.tsv"
docker() {
  if [[ $case_name == mismatch ]]; then printf 'drift.sql|checksum\n'; else printf 'fixture.sql|checksum\n'; fi
  [[ $case_name != query_error ]]
}
''' + actual + "\nverify_migrations_restored >/dev/null\nprintf 'would_delete_state=true\\n'\n"
        for case in ("pass", "query_error", "mismatch"):
            with self.subTest(case=case):
                result = self.shell(script, case)
                self.assertEqual(result.returncode == 0, case == "pass")
                self.assertEqual(result.stdout, "would_delete_state=true\n" if case == "pass" else "")

    def test_required_metadata_and_receipt_commands_reject_output_with_error(self) -> None:
        vm = (SCRIPTS / "release/vm-space-clean.sh").read_text(encoding="utf-8")
        cleanup = (SCRIPTS / "maintenance/release/cleanup-state.sh").read_text(encoding="utf-8")
        retention = (SCRIPTS / "release/backup-release-retention-clean.sh").read_text(encoding="utf-8")
        fragments = [
            "compat_image_id=$(docker image inspect -f '{{.Id}}' \"$compat_tag\")\n  [[ $compat_image_id == \"$explicit_compat_image_id\" ]]",
            "cleaned_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)\nprintf 'cleaned_at=%s\\n' \"$cleaned_at\" > \"$active_claim/plaintext-cleaned\"",
            "observed=$(stat -c '%Y' \"$bundle\")\n  [[ $observed == \"$bundle_mtime\" ]]",
        ]
        for source, fragment in zip((vm, cleanup, retention), fragments):
            self.assertIn(fragment, source)
            for case in ("pass", "output_error"):
                with self.subTest(fragment=fragment.splitlines()[0], case=case):
                    script = r'''set -Eeuo pipefail
case_name=$1
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
active_claim=$work
bundle=$work
bundle_mtime=fixture
compat_tag=fixture
explicit_compat_image_id=fixture
docker() { printf 'fixture\n'; [[ $case_name != output_error ]]; }
date() { printf 'fixture\n'; [[ $case_name != output_error ]]; }
stat() { printf 'fixture\n'; [[ $case_name != output_error ]]; }
''' + fragment + "\nprintf 'accepted=true\\n'\n"
                    result = self.shell(script, case)
                    self.assertEqual(result.returncode == 0, case == "pass")
                    self.assertEqual(result.stdout, "accepted=true\n" if case == "pass" else "")

    @unittest.skipIf(os.name == "nt", "Real symlink fixture requires native POSIX; Git Bash ln may copy")
    def test_native_symlink_slot_and_bundle_are_rejected_at_formal_callers(self) -> None:
        cleanup = (SCRIPTS / "maintenance/release/cleanup-state.sh").read_text(encoding="utf-8")
        retention = (SCRIPTS / "release/backup-release-retention-clean.sh").read_text(encoding="utf-8")
        actual = function(cleanup, "active_container_from_slot").replace("/opt/sub2api/active-app", "$fixture_slot")
        script = r'''set -Eeuo pipefail
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
printf 'container=fixture\n' > "$work/target"
fixture_slot="$work/slot"
ln -s "$work/target" "$fixture_slot"
[[ -L $fixture_slot ]]
''' + actual + '\nactive_container=$(active_container_from_slot)\nprintf "unsafe_slot=true\\n"\n'
        result = self.shell(script)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        script = r'''set -Eeuo pipefail
work=$(mktemp -d)
trap 'command rm -rf -- "$work"' EXIT
mkdir "$work/target"
bundle="$work/bundle"
ln -s "$work/target" "$bundle"
[[ -L $bundle ]]
release_device=unused
''' + function(retention, "verify_bundle") + r'''
for item in fixture; do
  verify_bundle "$bundle" fixture || continue
  printf 'unsafe_bundle=true\n'
done
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
