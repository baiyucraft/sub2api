"""Inert native Bash checks for required-query and retry boundaries."""
from __future__ import annotations

import os
import re
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

RELEASE = Path(__file__).resolve().parents[2] / 'release'
BASH = shutil.which('bash')
if os.name == 'nt':
    candidate = Path('C:/Program Files/Git/bin/bash.exe')
    BASH = str(candidate) if candidate.is_file() else None


def function(name: str, source: str) -> str:
    start = source.index(name + '() {')
    end = source.index('\n}\n', start) + 3
    return source[start:end]


@unittest.skipUnless(BASH, 'native Bash is required')
class RemainingShellQueryTest(unittest.TestCase):
    def shell(self, script: str, *, setup=None) -> subprocess.CompletedProcess:
        with tempfile.TemporaryDirectory(prefix='sub2api-query-') as temporary:
            if setup:
                setup(Path(temporary))
            return subprocess.run([BASH, '-c', 'set -Eeuo pipefail\n' + script], cwd=temporary,
                                  text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)

    def test_redis_predicate_rejects_expected_stdout_from_failed_state_query(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        script = function('wait_for_redis_ready', source) + '''
write_redis_probe_diagnostics() { :; }
sleep() { :; }
docker() { printf running; return 70; }
if wait_for_redis_ready inert 1; then printf 'unsafe-pass'; else printf 'blocked'; fi
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'blocked')

    def test_redis_predicate_rejects_failed_ping_with_pong_stdout(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        script = function('wait_for_redis_ready', source) + '''
write_redis_probe_diagnostics() { :; }
sleep() { :; }
docker() {
  if [[ $* == *inspect* ]]; then printf running; return 0; fi
  if [[ $* == *PING* ]]; then printf PONG; return 70; fi
  printf 'loading:0\n'
}
if wait_for_redis_ready inert 1; then printf 'unsafe-pass'; else printf 'blocked'; fi
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'blocked')

    def test_redis_predicate_accepts_successful_running_pong(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        script = function('wait_for_redis_ready', source) + '''
docker() { if [[ $* == *inspect* ]]; then printf running; else printf PONG; fi; }
if wait_for_redis_ready inert 1; then printf ready; else printf blocked; fi
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'ready')

    def test_tag_inventory_failure_is_not_absence(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        script = function('image_id_for_tag', source) + '''
docker() { printf 'inert:tag sha256:%064d\n' 0; return 70; }
if image_id_for_tag inert:tag; then printf unsafe; else printf blocked; fi
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'blocked')

    def test_successful_tag_inventory_can_establish_absence(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        script = function('image_id_for_tag', source) + '''
docker() { :; }
value=$(image_id_for_tag inert:tag)
[[ -z $value ]]
printf absent
'''
        result = self.shell(script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'absent')

    def test_http_retry_requires_command_success_and_200(self):
        source = (RELEASE / 'vm-only-validate.sh').read_text(encoding='utf-8')
        line = next(line for line in source.splitlines() if 'if probe_http_status=$(curl ' in line)
        for status, expected in ((0, 'ready'), (70, 'retry')):
            with self.subTest(status=status):
                result = self.shell('candidate_port=1\ncurl() { printf 200; return ' + str(status) + '; }\n'
                                    + line + '\nprintf ready\nelse\nprintf retry\nfi\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, expected)

    def test_docker_retry_requires_command_success_and_healthy(self):
        source = (RELEASE / 'vm-only-switch.sh').read_text(encoding='utf-8')
        line = next(line for line in source.splitlines() if 'if probe_health=$(docker ' in line)
        for status, expected in ((0, 'ready'), (70, 'retry')):
            with self.subTest(status=status):
                result = self.shell('docker() { printf healthy; return ' + str(status) + '; }\n'
                                    + line + '\nprintf ready\nelse\nprintf retry\nfi\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, expected)

    def test_backup_idle_accepts_only_documented_status_content_pairs(self):
        source = (RELEASE / 'production-space-clean.sh').read_text(encoding='utf-8')
        start = source.index('  local backup_service_state backup_service_status=0')
        end = source.index('\n  required_value=', start)
        guard = source[start:end]
        for status, value, expected in ((3, 'inactive', True), (3, 'failed', True),
                                        (70, 'inactive', False), (0, 'active', False), (3, 'unknown', False)):
            with self.subTest(status=status, value=value):
                result = self.shell('systemctl() { printf ' + value + '; return ' + str(status) + '; }\n'
                                    'guard() {\n' + guard + '\n}\nguard\nprintf accepted\n')
                self.assertEqual(result.returncode == 0, expected, result.stderr)
                self.assertEqual(result.stdout == 'accepted', expected)

    def test_retention_grep_error_cannot_create_delete_candidate(self):
        source = (RELEASE / 'production-recovery-retention-clean.sh').read_text(encoding='utf-8')
        for grep_status, expected in ((0, 'protected'), (1, 'candidate'), (2, 'blocked')):
            with self.subTest(grep_status=grep_status):
                script = function('build_candidates', source) + '''
work_dir=$PWD;valid_records=$PWD/valid;protected_records=$PWD/protected;candidates=$PWD/candidates
cutoff_epoch=100;minimum_keep=0
printf 'inert\tconsumed\t1\t8\t1\n' > "$valid_records"
: > "$protected_records"; : > "$candidates"
grep() { return STATUS; }
if build_candidates; then
  if [[ -s $candidates ]]; then printf candidate; else printf protected; fi
else
  [[ ! -s $candidates ]]
  printf blocked
fi
'''.replace('STATUS', str(grep_status))
                result = self.shell(script)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, expected)

    def test_failed_protected_inventory_stops_before_a_complete_inventory_claim(self):
        source = (RELEASE / 'production-recovery-retention-clean.sh').read_text(encoding='utf-8')
        def setup(root):
            marker = root / 'releases' / 'inert' / '.consumed' / 'marker'
            marker.parent.mkdir(parents=True)
            marker.write_text('consumed_at=2026-10-17T00:00:00Z\n', encoding='utf-8')
        result = self.shell(function('build_protected_set', source) + '''
work_dir=$PWD;release_root=$PWD/releases;protected_records=$PWD/protected
: > "$protected_records"
sed() { printf '2026-10-17T00:00:00Z'; return 70; }
build_protected_set
printf unsafe
''', setup=setup)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('unsafe', result.stdout)

    def test_sourced_backfill_checks_psql_and_each_sql_producer(self):
        script_path = (RELEASE / 'backfill-activity-reward-costs.sh').as_posix()
        for failure in ('none', 'psql', 'printf', 'cat'):
            with self.subTest(failure=failure):
                script = '''
failure=FAILURE
psql() {
  while IFS= read -r line; do :; done
  builtin printf '{"verified":true}\n'
  if [[ $failure == psql ]]; then return 70; fi
}
printf() {
  if [[ $failure == printf && $1 == BEGIN* ]]; then return 70; fi
  builtin printf "$@"
}
cat() {
  if [[ $failure == cat ]]; then return 70; fi
  command cat "$@"
}
if source 'SCRIPT' --mode check; then builtin printf unsafe-or-success; fi
'''.replace('FAILURE', failure).replace('SCRIPT', script_path)
                result = self.shell(script)
                if failure == 'none':
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn('unsafe-or-success', result.stdout)
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertNotIn('unsafe-or-success', result.stdout)

    def test_signer_parent_query_failure_cannot_be_hidden_by_successful_cd(self):
        source = (RELEASE / 'sign-full-recovery-review.sh').read_text(encoding='utf-8')
        lines = '\n'.join(line for line in source.splitlines()
                          if line.startswith(('script_parent=', 'script_dir=')))
        for status in (0, 70):
            with self.subTest(status=status):
                result = self.shell('set +u\ntrap \'printf trap-hit\' ERR\n'
                                    'dirname() { printf "%s" "$PWD"; return ' + str(status) + '; }\n'
                                    + lines + '\nprintf sentinel\n')
                if status == 0:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, 'sentinel')
                else:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, 'trap-hit')

    def test_jq_predicate_preserves_false_and_aborts_errors_in_conditional_calls(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        pending = re.search(r'^  is_pending_v2\(\) \{.*?^  \}', source, re.M | re.S).group()
        for status, value, expected in ((0, 'true', 'pending'), (0, 'false', 'not-pending'),
                                        (17, 'true', None), (17, 'false', None), (0, 'null', None)):
            with self.subTest(status=status, value=value):
                result = self.shell(function('required_jq_predicate', source) + '\n' + pending + '\n'
                                    'plan_before=inert\njq() { printf ' + value + '; return ' + str(status) + '; }\n'
                                    'if is_pending_v2 inert; then printf pending; else printf not-pending; fi\n')
                if expected is None:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertEqual(result.stdout, '')
                else:
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, expected)

    def test_all_jq_inventories_complete_before_the_consumer(self):
        source = (RELEASE / 'vm-validate.sh').read_text(encoding='utf-8')
        inventories = [line for line in source.splitlines()
                       if line.lstrip().startswith('jq -r ') and '-inventory.tsv"' in line]
        self.assertEqual(len(inventories), 3)
        for line in inventories:
            for status in (0, 17):
                with self.subTest(line=line, status=status):
                    result = self.shell('state_dir=$PWD;manifest=inert\n'
                                        'jq() { printf "partial\\tentry\\n"; return ' + str(status) + '; }\n'
                                        + line + '\nprintf consumer-reached\n')
                    self.assertEqual(result.returncode == 0, status == 0, result.stderr)
                    self.assertEqual('consumer-reached' in result.stdout, status == 0)

    def test_archive_inventory_error_cannot_become_an_empty_successful_plan(self):
        source = (RELEASE / 'production-space-clean.sh').read_text(encoding='utf-8')
        for status in (0, 17):
            with self.subTest(status=status):
                result = self.shell(function('write_candidate_archive_records', source) + '\n'
                                    'work_dir=$PWD;release_root=$PWD;candidate_archive_records=$PWD/records\n'
                                    'find() { return ' + str(status) + '; }\n'
                                    'if write_candidate_archive_records; then printf ready; else printf blocked; fi\n')
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, 'ready' if status == 0 else 'blocked')

    def test_protected_image_grep_errors_block_candidates_and_deletion(self):
        for filename in ('production-space-clean.sh', 'vm-space-clean.sh'):
            source = (RELEASE / filename).read_text(encoding='utf-8')
            for entry in ('list_image_candidates', 'remove_image_candidates'):
                for status in (0, 1, 17):
                    with self.subTest(filename=filename, entry=entry, status=status):
                        script = '''
work_dir=$PWD
test_image=sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
test_tag=sub2api:inert-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
: > "$work_dir/mutations"
assert_release_idle() { :; }
assert_release_marker() { :; }
assert_services() { :; }
assert_no_active_build() { :; }
write_protected_images() { :; }
write_referenced_images() { :; }
is_protected_commit() { return 1; }
grep() { return STATUS; }
list_image_candidates() { printf '%s\n' "$test_image"; }
docker() {
  case "$*" in
    *'image rm'*) printf removed >> "$work_dir/mutations" ;;
    *'image ls -a'*) : ;;
    *'image ls'*) printf '%s\n' "$test_image" ;;
    *RepoTags*) printf '%s\n' "$test_tag" ;;
    *Size*) printf '8\n' ;;
    *) return 88 ;;
  esac
}
'''.replace('STATUS', str(status))
                        script += function(entry, source) + '\n'
                        script += 'if ' + entry + '; then printf accepted; else printf blocked; fi\n'
                        script += 'printf "\\nmutations="; command cat "$work_dir/mutations"\n'
                        result = self.shell(script)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        if status == 17:
                            self.assertIn('blocked', result.stdout)
                            self.assertNotIn('removed', result.stdout)
                        elif status == 0:
                            self.assertIn('accepted', result.stdout)
                            self.assertNotIn('removed', result.stdout)
                        else:
                            self.assertIn('accepted', result.stdout)
                            if entry == 'remove_image_candidates':
                                self.assertIn('removed', result.stdout)

    def test_port_inventory_error_cannot_establish_a_free_port(self):
        source = (RELEASE / 'vm-only-validate.sh').read_text(encoding='utf-8')
        start = source.index('listener_inventory=$(ss ')
        end = source.index('\ncleanup_candidate()', start)
        lines = source[start:end]
        for status in (0, 17):
            with self.subTest(status=status):
                result = self.shell('candidate_port=38121\nss() { return ' + str(status) + '; }\n'
                                    + lines + '\nprintf port-free\n')
                self.assertEqual(result.returncode == 0, status == 0, result.stderr)
                self.assertEqual('port-free' in result.stdout, status == 0)

    def test_backup_recent_and_protected_query_errors_stop_selection(self):
        source = (RELEASE / 'backup-release-retention-clean.sh').read_text(encoding='utf-8')
        start = source.index('  protected_match=0\n')
        end = source.index('  bundle_mtime=', start)
        guards = source[start:end]
        for failure in ('none', 'recent', 'protected'):
            with self.subTest(failure=failure):
                result = self.shell('bundle=inert;recent=recent;protected=protected;failure=' + failure + '\n'
                                    'grep() { if [[ $* == *"$failure"* && $failure != none ]]; then return 17; fi; return 1; }\n'
                                    'for item in inert; do\n' + guards + '\nprintf selected\ndone\n')
                self.assertEqual(result.returncode == 0, failure == 'none', result.stderr)
                self.assertEqual('selected' in result.stdout, failure == 'none')

    def test_failed_backup_protection_find_cannot_look_empty(self):
        source = (RELEASE / 'backup-release-retention-clean.sh').read_text(encoding='utf-8')
        line = next(line for line in source.splitlines() if 'protection_markers=$(find ' in line)
        result = self.shell('bundle=inert\nfind() { return 17; }\n' + line + '\nprintf selected\n')
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn('selected', result.stdout)

    def test_postdelete_query_error_cannot_report_success(self):
        source = (RELEASE / 'vm-space-clean.sh').read_text(encoding='utf-8')
        result = self.shell(function('remove_container_candidates', source) + '''
work_dir=$PWD
printf 'inert\n' > "$work_dir/container-candidates"
docker() {
  case "$*" in
    *Name*) printf '/sub2api-dev-pre-inert' ;;
    *Status*) printf exited ;;
    'rm inert') : > "$work_dir/mutation" ;;
    'ps -aq --no-trunc') : ;;
    *) return 88 ;;
  esac
}
grep() { return 17; }
if remove_container_candidates; then printf unsafe-success; else printf blocked; fi
[[ -f $work_dir/mutation ]]
''')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'blocked')


if __name__ == '__main__':
    unittest.main()
