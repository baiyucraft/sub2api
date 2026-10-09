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


def build_source() -> tuple[str, list[str]]:
    source = VALIDATOR.read_text(encoding="utf-8")
    helper = re.search(r"(?ms)^docker_build_with_registry_bypass\(\) \{\n.*?^\}", source)
    assert helper is not None
    commands = re.findall(
        r'(?m)^\s*(docker_build_with_registry_bypass --network=host --progress=plain \\\n'
        r'(?:[ \t]+.*\\\n)*[ \t]+--build-arg DATE=.*? -t "\$tag" \.) [^\n]*',
        source,
    )
    assert len(commands) == 2, "both Gate v2 and legacy builds must use the scoped helper"
    assert "docker build --network=host" not in source
    return helper.group(0), commands


@pytest.mark.parametrize("entry", [0, 1], ids=["v2", "legacy"])
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
    setup = ["unset NO_PROXY no_proxy " + " ".join(PROXY_NAMES)]
    for name, value in (("NO_PROXY", upper), ("no_proxy", lower)):
        if value is not None:
            setup.append(f"export {name}={shlex.quote(value)}")
    proxy_values = [f"http://fixture-{index}.test:8080" for index in range(len(PROXY_NAMES))]
    setup.extend(f"export {name}={shlex.quote(value)}" for name, value in zip(PROXY_NAMES, proxy_values))
    script = "set -Eeuo pipefail\n" + "\n".join(setup) + "\n" + helper + r'''
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
commit=fixture-commit
version=fixture-version
tag=fixture-tag
emit_environment
''' + f"fixture_exit_code={exit_code}\nif {commands[entry]}; then result=0; else result=$?; fi\n" + r'''
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
    assert fields[8].split(",").count(MIRROR) == 1
    assert fields[8].split(",").count(AUTH) == 1
    argument_count = int(fields[16])
    arguments = fields[17:17 + argument_count]
    assert arguments[:3] == ["build", "--network=host", "--progress=plain"]
    for image in ("node:24-alpine", "golang:1.27.2-alpine", "alpine:3.21", "postgres:18-alpine"):
        assert any(argument.endswith(f"=docker.m.daocloud.io/library/{image}") for argument in arguments)
    assert arguments[-3:] == ["-t", "fixture-tag", "."]
    assert fields[17 + argument_count:25 + argument_count] == parent
    assert fields[-1] == str(exit_code)


def test_real_bash_validator_syntax():
    result = run_hidden([bash_path(), "--noprofile", "--norc", "-n"], input=VALIDATOR.read_text(encoding="utf-8"), capture_output=True, text=True, timeout=15)
    assert result.returncode == 0, result.stderr
