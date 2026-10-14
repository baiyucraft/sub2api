from __future__ import annotations

import unittest

import test_recovery_file_contract as file_contract


class RecoveryPhaseFailClosedTest(unittest.TestCase):
    def setUp(self) -> None:
        self.fixture = file_contract.RecoveryFileContractTest()
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)
        source = (file_contract.ROOT / "maintenance/release/restore.sh").read_text(encoding="utf-8")
        nginx = source[source.index("restore_nginx_recovery() {"):source.index("fail_closed() {")]
        phases = source[source.index("apply_compose_recovery() {"):
                        source.index('docker cp "$recovery/redis/dump.rdb"')]
        self.fixture.functions.write_text(self.fixture.functions.read_text() + nginx + phases, encoding="utf-8")

    def rejects(self, setup: str, command: str) -> None:
        self.fixture.rejects(setup, command)

    def test_application_failed_cd_never_starts_compose_or_commits_checkpoint(self) -> None:
        checkpoint = (file_contract.ROOT / "maintenance/release/recovery-checkpoint.sh").read_text()
        self.fixture.functions.write_text(self.fixture.functions.read_text() + checkpoint)
        setup = r'''
deploy_dir="$RF_BASE/missing"
load_release_compose_files() { release_compose_args=(); }
docker() { printf 'called\n' > "$RF_BASE/docker-called"; printf 'healthy\n'; }
chown() { :; }
mkdir -p "$RF_BASE/checkpoints"
RECOVERY_CHECKPOINT_ROOT="$RF_BASE/checkpoints"
RECOVERY_CHECKPOINT_IDENTITY=bound-test-identity
recovery_checkpoint_assert_directory() { [[ -d $1 && ! -L $1 ]]; }
verify_success() { return 0; }
'''
        for mode in ("plain", "if", "or"):
            with self.subTest(mode=mode):
                for name in ("compose_restored.started", "compose_restored.done"):
                    marker = self.fixture.base / "checkpoints" / name
                    if marker.exists():
                        marker.chmod(0o600)
                        marker.unlink()
                result = self.fixture.execute(setup, 'recovery_checkpoint_run compose_restored verify_success apply_application_recovery', mode)
                self.assertNotEqual(result.returncode, 0, result.stderr)
        self.assertFalse((self.fixture.base / "docker-called").exists())
        self.assertFalse((self.fixture.base / "checkpoints/compose_restored.done").exists())

    def test_application_rejects_load_or_docker_partial_success_failure(self) -> None:
        for fault in ("load", "compose", "inspect"):
            with self.subTest(fault=fault):
                setup = r'''
deploy_dir="$RF_BASE"
load_release_compose_files() { release_compose_args=(); [[ $FAULT != load ]]; }
docker() {
  if [[ $1 == compose ]]; then [[ $FAULT != compose ]]; return; fi
  printf 'healthy\n'; [[ $FAULT != inspect ]]
}
''' + "\nFAULT=" + fault
                self.rejects(setup, "apply_application_recovery")

    def test_compose_inventory_failure_never_installs(self) -> None:
        self.rejects(r'''
recovery="$RF_BASE"; deploy_dir="$RF_BASE"
_recovery_compose_inventory() { RECOVERY_COMPOSE_FILES=(source); return 29; }
install() { printf 'called\n' > "$RF_BASE/install-called"; }
''', "apply_compose_recovery")
        self.assertFalse((self.fixture.base / "install-called").exists())

    def test_nginx_invalid_snapshot_stops_before_any_replace(self) -> None:
        self.rejects(r'''
recovery="$RF_BASE/missing"
replace_from_snapshot() { printf 'called\n' > "$RF_BASE/replace-called"; }
''', "restore_nginx_recovery")
        self.assertFalse((self.fixture.base / "replace-called").exists())

    def test_nginx_restore_or_verify_or_checkpoint_failure_never_starts_nginx(self) -> None:
        for fault in ("restore", "verify", "checkpoint"):
            with self.subTest(fault=fault):
                setup = r'''
recovery="$RF_BASE"
mkdir -p "$RF_BASE/config/nginx"
printf 'fixture\n' > "$RF_BASE/config/nginx/nginx-T.txt"
restore_nginx_recovery() { [[ $FAULT != restore ]]; }
verify_nginx_snapshot() { [[ $FAULT != verify ]]; }
recovery_checkpoint_store() { [[ $FAULT != checkpoint ]]; }
systemctl() { printf 'called\n' > "$RF_BASE/nginx-start-called"; }
''' + "\nFAULT=" + fault
                self.rejects(setup, "apply_nginx_recovery")
        self.assertFalse((self.fixture.base / "nginx-start-called").exists())

    def test_postgres_never_overwrites_data_until_container_absence_is_proven(self) -> None:
        for fault in ("nginx_stop", "candidate_stop", "still_present", "docker_list"):
            with self.subTest(fault=fault):
                setup = r'''
candidate_container=candidate; active_container=active
deploy_dir="$RF_BASE"
mkdir -p "$RF_BASE/data"
systemctl() { [[ $FAULT != nginx_stop ]]; }
docker() {
  case "$1" in
    inspect) printf 'exited\n';;
    stop) [[ $FAULT != candidate_stop ]];;
    rm) :;;
    ps) [[ $FAULT != docker_list ]] || return 29; printf 'candidate\n';;
    *) return 30;;
  esac
}
find() { printf 'called\n' > "$RF_BASE/data-delete-called"; }
''' + "\nFAULT=" + fault
                self.rejects(setup, "apply_postgres_recovery")
        self.assertFalse((self.fixture.base / "data-delete-called").exists())

    def test_redis_stop_and_runtime_state_are_verified_before_data_write(self) -> None:
        for fault in ("stop", "running", "inspect"):
            with self.subTest(fault=fault):
                setup = r'''
mkdir -p "$RF_BASE/redis-data"
docker() {
  case "$1" in
    stop) [[ $FAULT != stop ]];;
    inspect)
      if [[ $* == *'.State.Status'* ]]; then
        if [[ $FAULT == running ]]; then printf 'running\n'; else printf 'exited\n'; fi
        [[ $FAULT != inspect ]]; return
      elif [[ $* == *'.Mounts'* ]]; then printf '%s\n' "$RF_BASE/redis-data"
      else printf '{}\n'; fi;;
    *) return 29;;
  esac
}
jq() { printf 'no\n'; }
find() { printf 'called\n' > "$RF_BASE/redis-delete-called"; }
''' + "\nFAULT=" + fault
                self.rejects(setup, "apply_redis_recovery")
        self.assertFalse((self.fixture.base / "redis-delete-called").exists())

    def test_valid_application_phase_returns_success(self) -> None:
        result = self.fixture.execute(r'''
deploy_dir="$RF_BASE"
load_release_compose_files() { release_compose_args=(); }
docker() { if [[ $1 == inspect ]]; then printf 'healthy\n'; fi; }
''', "apply_application_recovery", "if")
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
