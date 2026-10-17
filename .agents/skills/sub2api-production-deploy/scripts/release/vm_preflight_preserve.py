"""Read-only proof for the audited profile-273 migration preflight incident.

This is deliberately separate from the pre-validator and candidate-build proofs.
It never accepts a migration apply, production claim, or a different helper tree.
"""
from __future__ import annotations

import shlex


INCIDENT_COMMIT = "bc3354b95f9d2c7b140bf5ab8ad6358b24f75184"
INCIDENT_VALIDATOR = "ff1796eeb7799ae37f98eabe79c3ef288ba96cebdd2c52999ad04aa4da26c98a"
INCIDENT_ASSERTION = "b29e85a28f9aa7bad94e2442033ddb08d4e1568e703584e137eed390e2a021e2"
ASSERTION_PATH = ".agents/skills/sub2api-production-deploy/scripts/maintenance/release/migration-298-assert.sh"


def migration_preflight_checks(identifier: str, manifest_sha256: str, manifest: dict) -> str:
    if (manifest.get("commit_sha") != INCIDENT_COMMIT or manifest.get("profile") != "273"
        or manifest.get("vm_validator_sha256") != INCIDENT_VALIDATOR
        or (manifest.get("release_asset_sha256") or {}).get(ASSERTION_PATH) != INCIDENT_ASSERTION):
        raise RuntimeError("vm_preserve_migration_incident_unproven")
    # The validator and line are frozen by this source identity, independently
    # of the newer helper executing the read-only incident reconciliation.
    suffix = "".join(c for c in identifier if c.isalnum())
    probe_db = "sub2api_v2_" + suffix[:24]
    probe_suffix = suffix[:12]
    return f'''# Both descriptors are already locked by the supervisor.
required_capture_1=$(stat -c '%U:%G:%a:%h' /opt/sub2api-deploy/release-gates/release.lock) || exit $?
case "$required_capture_1" in root:root:600:1|root:root:644:1) ;; *) exit 1 ;; esac
required_capture_2=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/9) || exit $?
case "$required_capture_2" in root:root:600:1|root:root:644:1) ;; *) exit 1 ;; esac
required_capture_3=$(stat -c '%d:%i' /opt/sub2api-deploy/release-gates/release.lock) || exit $?
required_capture_4=$(stat -Lc '%d:%i' /proc/self/fd/9) || exit $?
test "$required_capture_3" = "$required_capture_4"
required_capture_5=$(stat -c '%U:%G:%a:%h' /usr/local/libexec/.sub2api-release-unit.lock) || exit $?
test "$required_capture_5" = root:root:600:1
required_capture_6=$(stat -Lc '%U:%G:%a:%h' /proc/self/fd/8) || exit $?
test "$required_capture_6" = root:root:600:1
required_capture_7=$(stat -c '%d:%i' /usr/local/libexec/.sub2api-release-unit.lock) || exit $?
required_capture_8=$(stat -Lc '%d:%i' /proc/self/fd/8) || exit $?
test "$required_capture_7" = "$required_capture_8"
process_count=$(python3 -B - <<'PROCESSES'
import os,re
from pathlib import Path
ancestors=set()
pid=os.getpid()
while pid and pid not in ancestors:
    ancestors.add(pid)
    try:
        pid=int((Path('/proc')/str(pid)/'stat').read_text().rsplit(')',1)[1].split()[1])
    except FileNotFoundError:
        break
count=0
for p in Path('/proc').iterdir():
    if not p.name.isdigit() or int(p.name) in ancestors:
        continue
    try:
        args=[v.decode(errors='replace') for v in (p/'cmdline').read_bytes().split(bytes([0])) if v]
    except FileNotFoundError:
        continue
    if not args:
        continue
    if (any(Path(a).name in ('vm-space-clean.sh','sub2api-vm-validate','run-validator.sh','buildctl') for a in args)
        or (Path(args[0]).name=='docker' and len(args)>1 and args[1] in ('build','buildx'))
        or any(re.search(r'release-input/[^ ]+/bootstrap|[.]sub2api-release-unit[.][^ ]*/sub2api-sign-(gate|dr-evidence)', a) for a in args)):
        count+=1
print(count)
PROCESSES
)
test "$process_count" = 0
gate_root=/opt/sub2api-deploy/release-gates/{identifier}
test -d "$gate_root"
test ! -L "$gate_root"
required_capture_9=$(realpath -e -- "$gate_root") || exit $?
test "$required_capture_9" = "$gate_root"
required_capture_10=$(stat -c '%U:%G:%a' "$gate_root") || exit $?
test "$required_capture_10" = root:root:700
test -d "$gate_root/output"
test ! -L "$gate_root/output"
required_capture_11=$(stat -c '%U:%G:%a' "$gate_root/output") || exit $?
test "$required_capture_11" = root:root:700
check_file() {{
  test -f "$1"
  test ! -L "$1"
  required_capture_12=$(stat -c '%U:%G:%a:%h' "$1") || exit $?
  test "$required_capture_12" = "root:root:$2:1"
}}
check_file "$gate_root/manifest.json" 400
required_capture_13=$(sha256sum "$gate_root/manifest.json" | awk '{{print $1}}') || exit $?
test "$required_capture_13" = {shlex.quote(manifest_sha256)}
check_file "$gate_root/stage" 600
for name in failure-category failure-line failure-detail migration-hook-filename migration-hook-phase migration-hook-status; do
  check_file "$gate_root/$name" 400
done
required_capture_14=$(cat "$gate_root/stage") || exit $?
test "$required_capture_14" = migration_plan_before
required_capture_15=$(cat "$gate_root/failure-category") || exit $?
test "$required_capture_15" = vm_v2_migration_plan_before
required_capture_16=$(cat "$gate_root/failure-line") || exit $?
test "$required_capture_16" = 612
required_capture_17=$(cat "$gate_root/failure-detail") || exit $?
test "$required_capture_17" = 'status=1 stage=migration_plan_before'
required_capture_18=$(cat "$gate_root/migration-hook-filename") || exit $?
test "$required_capture_18" = 298_group_scheduling_protection_policies.sql
required_capture_19=$(cat "$gate_root/migration-hook-phase") || exit $?
test "$required_capture_19" = preflight
required_capture_20=$(cat "$gate_root/migration-hook-status") || exit $?
test "$required_capture_20" = verified
check_file "$gate_root/migration-hook.stdout" 400
check_file "$gate_root/migration-hook.stderr" 400
required_capture_21=$(cat "$gate_root/migration-hook.stdout") || exit $?
test "$required_capture_21" = migration_298_failure_code=constraint_contract
test ! -s "$gate_root/migration-hook.stderr"
for name in plan-before.json plan-before.exit plan-actual.json plan-actual.exit; do
  check_file "$gate_root/$name" 400
done
required_capture_22=$(cat "$gate_root/plan-before.exit") || exit $?
test "$required_capture_22" = 0
required_capture_23=$(cat "$gate_root/plan-actual.exit") || exit $?
test "$required_capture_23" = 0
required_capture_24=$(jq -cS . "$gate_root/plan-before.json") || exit $?
required_capture_25=$(jq -cS . "$gate_root/plan-actual.json") || exit $?
test "$required_capture_24" = "$required_capture_25"
jq -e --slurpfile m "$gate_root/manifest.json" '
  type == "object" and .existing_checksums_verified == true and
  .conflicts == [] and .unknown == [] and
  .catalog_sha256 == $m[0].catalog_sha256 and
  .checksum_policy_sha256 == $m[0].checksum_policy_sha256 and
  ([.pending[].filename] == ["299_upstream_cache_guard_revisions.sql","300_upstream_health_history_index.sql"]) and
  ([.pending[] | {{filename,checksum}}] == [$m[0].migration_catalog[] |
    select(.filename == "299_upstream_cache_guard_revisions.sql" or .filename == "300_upstream_health_history_index.sql") | {{filename,checksum}}])
' "$gate_root/plan-before.json" >/dev/null
check_file "$gate_root/hook-results.json" 600
jq -e '. == {{"299_upstream_cache_guard_revisions.sql":{{"preflight":true}},"285_upstream_null_rate_lifecycle.sql":{{"preflight":true}},"297_upstream_cache_guard_account_scope.sql":{{"preflight":true}}}}' "$gate_root/hook-results.json" >/dev/null
for absent in output/gate.json output/gate.sig output/candidate.tar.gz output/SHA256SUMS candidate.tar.gz probe-data probe-redis-data production-recovery migration-plan-apply.json migrate-candidate.log plan-after.json old-image-health.log; do
  test ! -e "$gate_root/$absent"
  test ! -L "$gate_root/$absent"
done
test -d "$gate_root/logs"
test ! -L "$gate_root/logs"
required_capture_26=$(stat -c '%U:%G:%a' "$gate_root/logs") || exit $?
test "$required_capture_26" = root:root:700
check_file "$gate_root/logs/vm-validate.raw.log" 600
check_file "$gate_root/validator.stderr" 600
test -d "$raw_root"
test ! -L "$raw_root"
required_capture_27=$(realpath -e -- "$raw_root") || exit $?
test "$required_capture_27" = "$raw_root"
required_capture_28=$(stat -c '%U:%G:%a' "$raw_root") || exit $?
test "$required_capture_28" = root:root:700
check_file "$raw_root/vm-validate.raw.log" 600
required_capture_29=$(sha256sum "$raw_root/vm-validate.raw.log" | awk '{{print $1}}') || exit $?
required_capture_30=$(sha256sum "$gate_root/logs/vm-validate.raw.log" | awk '{{print $1}}') || exit $?
test "$required_capture_29" = "$required_capture_30"
names=$(docker ps -a --format '{{{{.Names}}}}')
for name in sub2api-v2-app-{probe_suffix} sub2api-v2-old-{probe_suffix} sub2api-v2-redis-{probe_suffix}; do
  if grep -Fxq -- "$name" <<<"$names"; then exit 1; fi
done
networks=$(docker network ls --format '{{{{.Name}}}}')
if grep -Fxq -- sub2api-v2-net-{probe_suffix} <<<"$networks"; then exit 1; fi
probe_count=$(docker exec sub2api-postgres sh -lc 'psql -X -q -A -t -v ON_ERROR_STOP=1 -U "${{POSTGRES_USER:-postgres}}" -d postgres -c "BEGIN READ ONLY; SELECT COUNT(*) FROM pg_database WHERE datname='"'"'{probe_db}'"'"'; COMMIT;"')
test "$probe_count" = 0
unset -f check_file
'''
