from __future__ import annotations

import hashlib
import re
import shlex

from .manifest import RELEASE_ID
from .paths import RELEASE_PACKAGE_ROOT, skill_asset_path
from .ssh import SSHRunner


BUILD_HELPER = RELEASE_PACKAGE_ROOT / "vm-build-common.sh"
BUILD_HELPER_ASSET = skill_asset_path("release/vm-build-common.sh")
BUILD_FAILURE_CODES = frozenset({
    "none", "build_registry_502", "build_registry_eof", "build_registry_auth",
    "build_registry_tls", "build_timeout", "build_space", "build_compile",
    "build_unknown", "build_registry_metadata_invalid", "build_dependency_contract",
    "build_preflight_unproven",
})
BUILD_DEPENDENCY_FIELDS = frozenset({
    "build_dependency_status", "build_failure_code", "build_dependency_count",
    "build_dependency_verified_count",
})


class VMCandidateBuildError(RuntimeError):
    """A fixed, allowlisted reason; never a registry response or credential."""

    def __init__(self, report: dict[str, str]):
        self.report = {
            "build_dependency_status": "failed", "build_dependency_count": "4",
            "build_dependency_verified_count": "0", **report,
        }
        super().__init__(f"VM candidate build dependency failed: {report['build_failure_code']}")


def ensure_vm_build_dependencies(runner: SSHRunner, manifest: dict) -> dict[str, str]:
    """Inspect four base-image descriptors once before preparing restore inputs.

    Root-only diagnostics remain in release-logs. Only our input directory is
    removed. This function neither starts a validator nor retries a build.
    """
    release_id = str(manifest.get("release_id", ""))
    checksum = (manifest.get("release_asset_sha256") or {}).get(BUILD_HELPER_ASSET)
    data = BUILD_HELPER.read_bytes()
    if (
        not RELEASE_ID.fullmatch(release_id)
        or not isinstance(checksum, str)
        or not re.fullmatch(r"[0-9a-f]{64}", checksum)
        or hashlib.sha256(data).hexdigest() != checksum
    ):
        raise VMCandidateBuildError({"build_failure_code": "build_dependency_contract"})
    try:
        runner.run(
            "local_vm",
            "set -Eeuo pipefail\n"
            "[[ $(id -u) == 0 ]]\n"
            "root=/opt/sub2api-deploy/release-input\n"
            "[[ ! -L $root ]]\n"
            "install -d -m 700 \"$root\"\n"
            "[[ -d $root && $(stat -c '%u:%g:%a' \"$root\") == 0:0:700 ]]\n"
            "printf 'input_root_ready=true\\n'\n",
            {"input_root_ready"},
        )
        remote_root = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "build-dependencies")
    except Exception:
        raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
    remote_helper = f"{remote_root}/vm-build-common.sh"
    log_root = f"/opt/sub2api-deploy/release-logs/{release_id}"
    log_dir = f"{log_root}/build-dependencies-early"
    report: dict[str, str] | None = None
    try:
        runner.upload("local_vm", data, remote_helper, 0o400)
        script = f'''set -Eeuo pipefail
umask 077
helper={shlex.quote(remote_helper)}
[[ -f $helper && ! -L $helper && $(stat -c '%u:%g:%a:%h' "$helper") == 0:0:400:1 ]]
[[ $(sha256sum "$helper" | awk '{{print $1}}') == {shlex.quote(checksum)} ]]
for directory in /opt/sub2api-deploy/release-logs {shlex.quote(log_root)}; do
  [[ ! -L $directory ]]
  if [[ ! -e $directory ]]; then install -d -o 0 -g 0 -m 700 "$directory"; fi
  [[ -d $directory && $(stat -c '%u:%g:%a' "$directory") == 0:0:700 ]]
done
log_dir={shlex.quote(log_dir)}
[[ ! -e $log_dir && ! -L $log_dir ]]
install -d -o 0 -g 0 -m 700 "$log_dir"
source "$helper"
if vm_build_preflight "$log_dir"; then build_dependency_status=verified; else build_dependency_status=failed; fi
printf 'build_dependency_status=%s\\nbuild_failure_code=%s\\nbuild_dependency_count=4\\nbuild_dependency_verified_count=%s\\n' "$build_dependency_status" "$VM_BUILD_FAILURE_CODE" "$VM_BUILD_DEPENDENCY_VERIFIED_COUNT"
'''
        report = runner.run("local_vm", script, BUILD_DEPENDENCY_FIELDS, timeout=480).values
        if (
            not isinstance(report, dict)
            or set(report) != BUILD_DEPENDENCY_FIELDS
            or not all(isinstance(value, str) for value in report.values())
            or report["build_dependency_status"] not in {"verified", "failed"}
            or report["build_failure_code"] not in BUILD_FAILURE_CODES
            or report["build_dependency_count"] != "4"
            or report["build_dependency_verified_count"] not in {"0", "1", "2", "3", "4"}
            or (report["build_dependency_status"] == "verified" and (
                report["build_failure_code"] != "none" or report["build_dependency_verified_count"] != "4"
            ))
            or (report["build_dependency_status"] == "failed" and report["build_failure_code"] == "none")
        ):
            raise VMCandidateBuildError({"build_failure_code": "build_dependency_contract"})
    except VMCandidateBuildError:
        raise
    except Exception:
        raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
    finally:
        # create_temp_dir registered exactly this private input; diagnostics live
        # outside it. Do not clean candidate/Gate/other releases.
        try:
            runner.run(
                "local_vm",
                f"rm -rf -- {shlex.quote(remote_root)} && printf 'input_removed=true\\n'",
                {"input_removed"},
            )
        except Exception:
            raise VMCandidateBuildError({"build_failure_code": "build_preflight_unproven"}) from None
    assert report is not None
    if report["build_dependency_status"] != "verified":
        raise VMCandidateBuildError(report)
    return report
