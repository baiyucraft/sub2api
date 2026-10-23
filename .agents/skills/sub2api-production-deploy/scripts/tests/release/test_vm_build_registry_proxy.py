from __future__ import annotations

import re
import shlex
import shutil
import sys
from pathlib import Path

import pytest

DEPLOY_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(DEPLOY_ROOT))
from release.process import run_hidden

VALIDATOR = DEPLOY_ROOT / "release" / "vm-validate.sh"
VM_ONLY_VALIDATOR = DEPLOY_ROOT / "release" / "vm-only-validate.sh"
BUILD_HELPER = DEPLOY_ROOT / "release" / "vm-build-common.sh"
MIRROR = "docker.m.daocloud.io"
AUTH = "m.daocloud.io"
PROXY_NAMES = ("HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy")


def bash_path() -> str:
    bash = shutil.which("bash")
    if bash:
        return bash
    git = shutil.which("git")
    if git:
        bundled = Path(git).parent.parent / "bin" / "bash.exe"
        if bundled.is_file():
            return str(bundled)
    pytest.skip("real Bash is unavailable")


def build_source() -> tuple[str, list[tuple[str, str]]]:
    source = VALIDATOR.read_text(encoding="utf-8")
    # Match the complete build group and bind DATE to its adjacent required
    # capture without depending on the generated variable's numeric suffix.
    build_pattern = (
        r'(?m)^'
        r'[ \t]*(?P<capture>(?P<date_variable>[A-Za-z_]\w*)=\$\(date -u \+%Y-%m-%dT%H:%M:%SZ\))\n'
        r'[ \t]*(?P<command>vm_build_candidate "\$build_log" \\\n'
        r'(?:[ \t]+.*\\\n)*[ \t]+(?:--build-arg DATE="\$\{(?P=date_variable)\}" -t "\$tag" \.'
        r'|--build-arg DATE="\$\{(?P=date_variable)\}" \\\n[ \t]+-t "\$tag" \.))'
    )
    vm_only_source = VM_ONLY_VALIDATOR.read_text(encoding="utf-8")
    build_groups = [*re.finditer(build_pattern, source), *re.finditer(build_pattern, vm_only_source)]
    assert "set -Eeuo pipefail\n" in source
    commands = [(match['capture'], match['command']) for match in build_groups]
    assert len(commands) == 3, "Gate v2, legacy and VM-only must use the same versioned helper"
    assert "docker build --network=host" not in source
    assert "docker build --network=host" not in vm_only_source
    return BUILD_HELPER.read_text(encoding="utf-8"), commands


@pytest.mark.parametrize("entry", [0, 1, 2], ids=["v2", "legacy", "vm-only"])
@pytest.mark.parametrize(
    "upper,lower",
    [
        ("localhost,.upper.test", "127.0.0.1,.lower.test"),
        ("localhost,.upper.test", None),
        (None, "127.0.0.1,.lower.test"),
        (None, None),
        ("", ""),
        ("", "127.0.0.1,.lower.test"),
        (f"localhost,{MIRROR}", ".lower.test"),
        (f"localhost,{AUTH}", ".lower.test"),
        (f"localhost,{MIRROR},{AUTH}", ".lower.test"),
        (MIRROR, AUTH),
        (None, f".lower.test,{AUTH}"),
    ],
)
@pytest.mark.parametrize("exit_code", [0, 37], ids=["success", "failure"])
def test_real_bash_build_bypass_preserves_parent_and_all_proxies(entry, upper, lower, exit_code):
    helper, commands = build_source()
    date_capture, build_command = commands[entry]
    setup = ["unset NO_PROXY no_proxy " + " ".join(PROXY_NAMES)]
    for name, value in (("NO_PROXY", upper), ("no_proxy", lower)):
        if value is not None:
            setup.append(f"export {name}={shlex.quote(value)}")
    proxy_values = [f"http://fixture-{index}.test:8080" for index in range(len(PROXY_NAMES))]
    setup.extend(f"export {name}={shlex.quote(value)}" for name, value in zip(PROXY_NAMES, proxy_values))
    script = "set -Eeuo pipefail\n" + "\n".join(setup) + "\nsource " + shlex.quote(BUILD_HELPER.as_posix()) + r'''
emit_environment() {
  printf '%s\0' "${NO_PROXY-<unset>}" "${no_proxy-<unset>}" \
    "${HTTP_PROXY-<unset>}" "${http_proxy-<unset>}" \
    "${HTTPS_PROXY-<unset>}" "${https_proxy-<unset>}" \
    "${ALL_PROXY-<unset>}" "${all_proxy-<unset>}"
}
docker() {
  "$BASH" --noprofile --norc -c '
    printf "%s\0" "${NO_PROXY-<unset>}" "${no_proxy-<unset>}" \
      "${HTTP_PROXY-<unset>}" "${http_proxy-<unset>}" \
      "${HTTPS_PROXY-<unset>}" "${https_proxy-<unset>}" \
      "${ALL_PROXY-<unset>}" "${all_proxy-<unset>}"
    status=$1
    shift
    printf "%s\0" "$#" "$@"
    exit "$status"
  ' fixture-docker "$fixture_exit_code" "$@"
}
date() {
  [[ $# == 2 && $1 == -u && $2 == +%Y-%m-%dT%H:%M:%SZ ]] || return 97
  printf '%s' fixture-date
}
commit=fixture-commit
version=fixture-version
tag=fixture-tag
scratch=$(mktemp -d)
scratch=$(cd "$scratch" && pwd -P)
chmod 700 "$scratch"
trap 'rm -rf -- "$scratch"' EXIT
cd "$scratch"
printf '# syntax=docker/dockerfile:1.7\nFROM scratch\n' >Dockerfile
VM_BUILD_PROOF_PATH="$scratch/proof.json"
VM_BUILD_DOCKERFILE="$scratch/Dockerfile"
VM_BUILD_COMMIT=$commit
vm_build_use_proof() {
  VM_BUILD_FAILURE_CODE=none
  VM_BUILD_IMAGE_ARGS=(
    --build-arg "NODE_IMAGE=docker.io/library/node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    --build-arg "GOLANG_IMAGE=docker.io/library/golang@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    --build-arg "ALPINE_IMAGE=docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    --build-arg "POSTGRES_IMAGE=docker.io/library/postgres@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    --build-arg "BUILDKIT_SYNTAX=docker.io/docker/dockerfile@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  )
}
build_log="$scratch/build.log"
# Windows ACLs do not implement POSIX directory modes. Linux executions keep
# the actual production metadata check; these cases exercise proxy scope.
if [[ $OSTYPE == msys* ]]; then
  vm_build_private_directory() { [[ -d $1 && ! -L $1 ]]; }
fi
emit_environment
''' + f"fixture_exit_code={exit_code}\n{date_capture}\nif {build_command}; then result=0; else result=$?; fi\n" + r'''
cat "$build_log"
emit_environment
printf '%s\0' "$result"
'''
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-s"], input=script, capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    fields = result.stdout.rstrip("\0").split("\0")
    parent = [value if value is not None else "<unset>" for value in (upper, lower)] + proxy_values
    merged = ",".join(value for value in (upper, lower) if value)
    for host in (MIRROR, AUTH):
        if host not in merged.split(","):
            merged = ",".join(value for value in (merged, host) if value)
    assert fields[:8] == parent
    assert fields[8:16] == [merged, merged] + proxy_values
    assert not {"docker.io", "registry-1.docker.io", "auth.docker.io"} & set(fields[8].split(","))
    assert fields[8].split(",").count(MIRROR) == 1
    assert fields[8].split(",").count(AUTH) == 1
    argument_count = int(fields[16])
    arguments = fields[17:17 + argument_count]
    assert arguments[:9] == ["buildx", "build", "--builder", "default", "--platform", "linux/amd64", "--load", "--network=host", "--progress=plain"]
    for image in ("node", "golang", "alpine", "postgres"):
        assert any(f"=docker.io/library/{image}@sha256:" in argument for argument in arguments)
    assert any(argument.startswith("BUILDKIT_SYNTAX=docker.io/docker/dockerfile@sha256:") for argument in arguments)
    assert arguments[-3:] == ["-t", "fixture-tag", "."]
    assert arguments.count("DATE=fixture-date") == 1
    assert arguments[arguments.index("DATE=fixture-date") - 1] == "--build-arg"
    assert fields[17 + argument_count:25 + argument_count] == parent
    assert fields[-1] == str(exit_code)


def test_real_bash_validator_syntax():
    for path in (VALIDATOR, VM_ONLY_VALIDATOR, BUILD_HELPER):
        result = run_hidden([bash_path(), "--noprofile", "--norc", "-n"], input=path.read_text(encoding="utf-8"), capture_output=True, text=True, timeout=15)
        assert result.returncode == 0, result.stderr


def test_official_sources_are_exact_and_shared_by_python_and_shell():
    from release.vm_build import BUILD_DEPENDENCY_REFS
    expected = {
        "frontend": "docker.io/docker/dockerfile:1.7",
        "node": "docker.io/library/node:24-alpine",
        "golang": "docker.io/library/golang:1.27.3-alpine",
        "alpine": "docker.io/library/alpine:3.21",
        "postgres": "docker.io/library/postgres:18-alpine",
    }
    assert BUILD_DEPENDENCY_REFS == expected
    script = 'source ' + shlex.quote(BUILD_HELPER.as_posix()) + '; printf "%s\\n" "$VM_BUILD_FRONTEND" "${VM_BUILD_IMAGES[@]}"'
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-c", script], capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
    assert result.stdout.splitlines() == list(expected.values())
