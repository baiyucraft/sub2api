from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys
import tempfile
import unittest

import test_recovery_checkpoint as checkpoint_module


def fingerprint_functions() -> str:
    source = (checkpoint_module.ROOT / "maintenance/release/restore.sh").read_text(encoding="utf-8")
    return source[source.index("postgres_metadata() {"):source.index("assert_recovery_container_absent() {")]


class RecoveryPostgresFingerprintTest(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = checkpoint_module.RecoveryCheckpointTest()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        functions = fingerprint_functions()
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
sha256sum() {
  if [[ ${FAIL_SHA:-no} == yes ]]; then command cat >/dev/null; return 8; fi
  command sha256sum "$@"
}
awk() {
  if [[ ${FAIL_AWK:-no} == yes ]]; then command cat >/dev/null; return 9; fi
  command awk "$@"
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

    def test_hash_command_error_cannot_produce_accepted_digest(self) -> None:
        result = self.fixture.run_script(FAIL_SHA="yes")
        self.assertEqual(result.returncode, 44, result.stderr)
        self.assertNotIn("accepted=", result.stdout)

    def test_hash_parser_error_cannot_produce_accepted_digest(self) -> None:
        result = self.fixture.run_script(FAIL_AWK="yes")
        self.assertEqual(result.returncode, 44, result.stderr)
        self.assertNotIn("accepted=", result.stdout)


class RecoveryPostgresFingerprintIntegrationTest(unittest.TestCase):
    """Opt-in real SQL regression; owns a fresh loopback-only PostgreSQL cluster."""

    @classmethod
    def setUpClass(cls) -> None:
        configured = os.environ.get("SUB2API_TEST_POSTGRES_BIN")
        if not configured:
            raise unittest.SkipTest("set SUB2API_TEST_POSTGRES_BIN to run real PostgreSQL regression")
        binary_dir = Path(configured).resolve()
        suffix = ".exe" if os.name == "nt" else ""
        cls.binaries = {name: binary_dir / (name + suffix) for name in ("initdb", "pg_ctl")}
        missing = [name for name, path in cls.binaries.items() if not path.is_file()]
        if missing:
            raise AssertionError("configured PostgreSQL binaries missing: " + ", ".join(missing))
        try:
            import psycopg  # noqa: F401
        except ImportError as error:
            raise AssertionError("psycopg is required for the configured PostgreSQL regression") from error
        cls.bash = shutil.which("bash")
        if os.name == "nt":
            git_bash = Path(os.environ.get("ProgramFiles", r"C:\Program Files")) / "Git/bin/bash.exe"
            if git_bash.is_file():
                cls.bash = str(git_bash)
        if not cls.bash:
            raise AssertionError("Bash is required for the real PostgreSQL regression")
        cls.temporary = tempfile.TemporaryDirectory(prefix="sub2api-pg-fingerprint-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.base = Path(cls.temporary.name)
        cls.data = cls.base / "data"
        cls.log = cls.base / "postgres.log"
        # A psql transport adapter only: execute the actual Shell SQL unchanged,
        # stream real COPY bytes, and return a failing status on database errors.
        # No Docker daemon, pre-existing database or downloaded client is used.
        cls.adapter = cls.base / "psql_adapter.py"
        cls.adapter.write_text(r'''
import os
import sys
import json
import psycopg

args = sys.argv[1:]
query = args[args.index("-c") + 1]
database = args[args.index("-d") + 1] if "-d" in args else os.environ["PGDATABASE"]
try:
    with psycopg.connect(dbname=database, autocommit=True) as connection:
        with connection.cursor() as cursor:
            if query.startswith("COPY "):
                with cursor.copy(query) as copy:
                    for block in copy:
                        sys.stdout.buffer.write(block)
            else:
                cursor.execute(query)
                if cursor.description:
                    for row in cursor:
                        values = [json.dumps(value) if isinstance(value, (dict, list)) else str(value) for value in row]
                        sys.stdout.buffer.write(("|".join(values) + "\n").encode("utf-8"))
except psycopg.Error as error:
    print(str(error), file=sys.stderr)
    sys.exit(3)
''', encoding="utf-8")
        cls.env = {key: value for key, value in os.environ.items()
                   if not key.startswith("PG") and key not in ("BASH_ENV", "ENV")}
        # Never inherit a service, credential, host or database from the caller.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        cls.env.update(PGHOST="127.0.0.1", PGPORT=str(port), PGUSER="sub2api",
                       PGDATABASE="sub2api", PGCLIENTENCODING="UTF8",
                       PGOPTIONS="-c work_mem=64kB -c temp_file_limit=8MB",
                       LC_ALL="C", TEST_PYTHON=Path(sys.executable).as_posix(),
                       TEST_ADAPTER=cls.adapter.as_posix())
        initialized = cls.command([str(cls.binaries["initdb"]), "-D", str(cls.data),
                                   "-U", "sub2api", "--auth=trust", "--encoding=UTF8", "--locale=C"])
        if initialized.returncode:
            raise AssertionError("initdb failed: " + initialized.stderr + initialized.stdout)
        # Register before starting so a partially started server is stopped too.
        cls.addClassCleanup(cls.stop_cluster)
        options = f"-h 127.0.0.1 -p {port}"
        started = cls.command([str(cls.binaries["pg_ctl"]), "-D", str(cls.data), "-l", str(cls.log),
                               "-o", options, "-w", "-t", "30", "start"])
        if started.returncode:
            log = cls.log.read_text(encoding="utf-8", errors="replace") if cls.log.exists() else ""
            raise AssertionError("pg_ctl start failed: " + started.stderr + started.stdout + log)
        created = cls.command([sys.executable, str(cls.adapter),
                               "-d", "postgres", "-c", "CREATE DATABASE sub2api"])
        if created.returncode:
            raise AssertionError("create fixture database failed: " + created.stderr)

    @classmethod
    def command(cls, args, **kwargs):
        if Path(args[0]) == cls.binaries["pg_ctl"]:
            # Windows postgres can inherit pg_ctl pipe handles after pg_ctl
            # exits. A real file avoids communicate() waiting on daemon EOF.
            with (cls.base / "pg_ctl.log").open("a+b") as output:
                result = checkpoint_module.run_hidden(args, env=cls.env, stdout=output,
                                                       stderr=output, timeout=45)
            return subprocess.CompletedProcess(args, result.returncode, "", "")
        return checkpoint_module.run_hidden(args, env=cls.env, capture_output=True,
                                             text=True, encoding="utf-8", timeout=90, **kwargs)

    @classmethod
    def stop_cluster(cls) -> None:
        if (cls.data / "postmaster.pid").exists():
            stopped = cls.command([str(cls.binaries["pg_ctl"]), "-D", str(cls.data),
                                   "-m", "immediate", "-w", "-t", "30", "stop"])
            if stopped.returncode:
                raise AssertionError("owned PostgreSQL cluster did not stop: " + stopped.stderr)
        print("real_postgres_owned_cluster=stopped")

    def sql(self, query: str, *, check=True):
        result = self.command([sys.executable, str(self.adapter),
                               "-A", "-t", "-c", query])
        if check:
            self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def setUp(self) -> None:
        self.sql("DROP SCHEMA public CASCADE; CREATE SCHEMA public;")

    def fingerprint(self) -> str:
        script = "set -Eeuo pipefail\n" + fingerprint_functions() + r'''
docker() {
  [[ $1 == exec && $2 == sub2api-postgres && $3 == psql ]] || return 70
  shift 3
  "$TEST_PYTHON" "$TEST_ADAPTER" "$@"
}
# The real function must propagate SQL/hash failure even in a conditional.
if value=$(postgres_fingerprint); then printf '%s\n' "$value"; else exit 44; fi
'''
        result = self.command([self.bash, "--noprofile", "--norc", "-s"], input=script)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertRegex(result.stdout, r"\A[0-9a-f]{64}\n\Z")
        return result.stdout.strip()

    def expected_fingerprint(self, *tables: str) -> str:
        # Independent Python aggregation proves sorted row hashes preserve
        # duplicates, empty-table identity and the exact UTF-8 row bytes.
        material = bytearray()
        for table in sorted(tables):
            rows = self.sql(f'SELECT row_to_json(t)::text FROM "public"."{table}" t').stdout.splitlines()
            hashes = sorted(hashlib.sha256(row.encode("utf-8")).hexdigest() for row in rows)
            digest = hashlib.sha256("".join(value + "\n" for value in hashes).encode("ascii")).hexdigest()
            material.extend(f"public|{table}|{digest}\n".encode("ascii"))
        return hashlib.sha256(material).hexdigest()

    def test_real_rows_order_duplicates_changes_empty_tables_and_unicode(self) -> None:
        self.sql("""CREATE TABLE rows_fixture (n integer, value text);
CREATE TABLE empty_fixture (value text);
INSERT INTO rows_fixture VALUES (2, E'Unicode 中文 \\nline\\ttab\\\\slash'),
 (1, 'duplicate'), (1, 'duplicate'), (3, NULL);""")
        original = self.fingerprint()
        self.assertEqual(original, self.expected_fingerprint("rows_fixture", "empty_fixture"))
        self.sql("CREATE TEMP TABLE reordered AS SELECT * FROM rows_fixture ORDER BY n DESC; "
                 "TRUNCATE rows_fixture; INSERT INTO rows_fixture SELECT * FROM reordered;")
        self.assertEqual(self.fingerprint(), original)
        self.sql("DELETE FROM rows_fixture WHERE ctid IN "
                 "(SELECT ctid FROM rows_fixture WHERE value='duplicate' LIMIT 1)")
        single = self.fingerprint()
        self.assertNotEqual(single, original)
        self.assertEqual(single, self.expected_fingerprint("rows_fixture", "empty_fixture"))
        self.sql("UPDATE rows_fixture SET value='changed' WHERE n=1")
        changed = self.fingerprint()
        self.assertNotEqual(changed, single)
        self.assertEqual(changed, self.expected_fingerprint("rows_fixture", "empty_fixture"))
        self.sql("DROP TABLE empty_fixture")
        self.assertNotEqual(self.fingerprint(), changed)
        print("real_postgres_semantics=verified")

    def test_large_rows_old_sort_exceeds_temp_limit_new_function_succeeds(self) -> None:
        self.sql("CREATE TABLE wide_fixture AS SELECT g AS n, repeat(md5(g::text), 512) AS value "
                 "FROM generate_series(1,2048) g")
        old = self.sql('COPY (SELECT row_to_json(t)::text FROM "public"."wide_fixture" t '
                       'ORDER BY row_to_json(t)::text COLLATE "C") TO STDOUT', check=False)
        self.assertNotEqual(old.returncode, 0, "fixture must reproduce the old external-sort failure")
        self.assertIn("temporary file size exceeds temp_file_limit", old.stderr)
        query = ('SELECT encode(sha256(convert_to(row_to_json(t)::text,\'UTF8\')),\'hex\') '
                 'COLLATE "C" AS row_sha256 FROM "public"."wide_fixture" t ORDER BY row_sha256')
        plan = json.loads(self.sql("EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) " + query).stdout)[0]["Plan"]
        self.assertEqual(plan["Node Type"], "Sort")
        self.assertEqual(len(plan["Output"]), 1)
        self.assertIn("encode", plan["Output"][0])
        self.assertIn("sha256", plan["Output"][0])
        self.assertLessEqual(plan["Plan Width"], 128)
        if plan["Sort Space Type"] == "Disk":
            self.assertLess(plan["Sort Space Used"], 8 * 1024)
        print("real_postgres_sort_plan=" + json.dumps({key: plan[key] for key in
              ("Node Type", "Output", "Plan Width", "Sort Key", "Sort Space Used", "Sort Space Type")}, sort_keys=True))
        rows = self.sql("COPY (" + query + ") TO STDOUT").stdout
        self.assertEqual(len(rows.splitlines()), 2048)
        self.assertTrue(all(len(row) == 64 and all(c in "0123456789abcdef" for c in row)
                            for row in rows.splitlines()))
        table_digest = hashlib.sha256(rows.encode("ascii")).hexdigest()
        expected = hashlib.sha256(f"public|wide_fixture|{table_digest}\n".encode("ascii")).hexdigest()
        self.assertEqual(self.fingerprint(), expected)
        print("real_postgres_sort_regression=verified rows=2048 row_payload_bytes=16384 "
              "work_mem=64kB temp_file_limit=8MB old_query=blocked new_function=passed")


if __name__ == "__main__":
    unittest.main()
