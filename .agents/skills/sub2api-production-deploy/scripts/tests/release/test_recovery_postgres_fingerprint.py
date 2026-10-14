from __future__ import annotations

import unittest

import test_recovery_checkpoint as checkpoint_module


class RecoveryPostgresFingerprintTest(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = checkpoint_module.RecoveryCheckpointTest()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        source = (checkpoint_module.ROOT / "maintenance/release/restore.sh").read_text()
        functions = source[source.index("postgres_metadata() {"):source.index("apply_postgres_recovery() {")]
        self.fixture.script = self.fixture.script[:self.fixture.script.index("apply_fixture() {")] + functions + r'''
docker() {
  if [[ $* == *'SELECT schemaname'* ]]; then
    printf 'public|users\npublic|accounts\n'
    [[ ${FAIL_LIST:-no} != yes ]] || return 5
  elif [[ $* == *COPY* ]]; then
    printf 'row fixture\n'
    if [[ ${FAIL_COPY:-no} == yes && $* == *accounts* ]]; then return 6; fi
  else return 7; fi
}
# Calling inside an if deliberately disables Bash errexit. A failing psql
# command must still propagate through COPY's pipe and the final SHA pipe.
if fingerprint=$(postgres_fingerprint); then
  printf 'accepted=%s\n' "$fingerprint"
else
  exit 44
fi
'''

    def test_valid_actual_function_produces_a_digest(self) -> None:
        result = self.fixture.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertRegex(result.stdout, r"accepted=[0-9a-f]{64}")

    def test_partial_table_listing_error_cannot_produce_accepted_digest(self) -> None:
        result = self.fixture.run_script(FAIL_LIST="yes")
        self.assertEqual(result.returncode, 44, result.stderr)
        self.assertNotIn("accepted=", result.stdout)

    def test_second_table_copy_error_propagates_through_both_hash_pipes(self) -> None:
        result = self.fixture.run_script(FAIL_COPY="yes")
        self.assertEqual(result.returncode, 44, result.stderr)
        self.assertNotIn("accepted=", result.stdout)


if __name__ == "__main__":
    unittest.main()
