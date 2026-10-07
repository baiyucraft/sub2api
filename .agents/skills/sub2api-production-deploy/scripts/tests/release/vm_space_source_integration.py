"""Run the VM cleaner's source guards in an isolated Linux fault-injection fixture."""
from __future__ import annotations

import shlex
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))
from release.ssh import SSHRunner  # noqa: E402


def main() -> None:
    runner = SSHRunner()
    remote = runner.create_temp_dir("local_vm", "/opt/sub2api-deploy/release-input", "vm-space-source-test")
    # The production script is unchanged apart from paths and a shortened real
    # timeout for the hanging Git fixture. No Docker or source-repository I/O
    # reaches the actual VM service.
    cleaner = (ROOT / "release/vm-space-clean.sh").read_text(encoding="utf-8")
    cleaner = cleaner.replace("gate_root=/opt/sub2api-deploy/release-gates", 'gate_root="$FIXTURE_GATE_ROOT"')
    cleaner = cleaner.replace("source_dir=/opt/sub2api-src", 'source_dir="$FIXTURE_SOURCE"')
    cleaner = cleaner.replace("--kill-after=10s 300s", "--kill-after=0.1s 0.1s")
    try:
        runner.upload("local_vm", cleaner.encode(), f"{remote}/cleaner.sh", 0o700)
        script = "set -Eeuo pipefail\nremote=" + shlex.quote(remote) + "\n" + r'''
mkdir -m 700 "$remote/bin"
cat > "$remote/bin/git" <<'GIT'
#!/usr/bin/env bash
set -Eeuo pipefail
[[ $1 == -C && $2 == "$FIXTURE_SOURCE" ]]
printf 'git:%s\n' "$3" >> "$FIXTURE_LOG"
case "$3" in
  cat-file) [[ -e $FIXTURE_STATE/target-present ]];;
  fetch)
    [[ ${GIT_TERMINAL_PROMPT:-} == 0 && $4 == origin && $5 == main ]]
    case "$FIXTURE_SCENARIO" in
      fetch_failure|explicit_fetch_failure) exit 42;;
      fetch_timeout) sleep 2;;
      fetch_kill) trap '' TERM; sleep 2;;
      still_missing) exit 0;;
      fetch_success) touch "$FIXTURE_STATE/target-present";;
      *) exit 43;;
    esac;;
  rev-list) exit 0;;
  *) exit 44;;
esac
GIT
cat > "$remote/bin/docker" <<'DOCKER'
#!/usr/bin/env bash
set -Eeuo pipefail
printf 'docker:%s\n' "$*" >> "$FIXTURE_LOG"
case "$1" in
  info) exit 0;;
  exec) printf '1024\n';;
  inspect) printf 'sha256:%064d\n' 1;;
  image)
    if [[ $2 == inspect ]]; then printf '1024\n'; fi;;
  ps|buildx) exit 0;;
  *) exit 45;;
esac
DOCKER
cat > "$remote/bin/df" <<'DF'
#!/usr/bin/env bash
printf 'Filesystem 1-blocks Used Available Use%% Mounted\nfixture 214748364800 107374182400 107374182400 50%% /\n'
DF
cat > "$remote/bin/ps" <<'PS'
#!/usr/bin/env bash
exit 0
PS
chmod 700 "$remote/bin/"*
export PATH="$remote/bin:$PATH"
checks=0
for scenario in missing_source symlink_source fetch_failure explicit_fetch_failure fetch_timeout fetch_kill still_missing fetch_success present; do
  for mode in dry-run apply; do
    export FIXTURE_SCENARIO=$scenario FIXTURE_STATE="$remote/$scenario-$mode"
    mkdir -m 700 "$FIXTURE_STATE"
    export FIXTURE_GATE_ROOT="$FIXTURE_STATE/gates" FIXTURE_SOURCE="$FIXTURE_STATE/source" FIXTURE_LOG="$FIXTURE_STATE/calls"
    mkdir -m 700 "$FIXTURE_GATE_ROOT"
    : > "$FIXTURE_LOG"
    if [[ $scenario == symlink_source ]]; then
      mkdir "$FIXTURE_STATE/real-source"
      ln -s "$FIXTURE_STATE/real-source" "$FIXTURE_SOURCE"
    elif [[ $scenario != missing_source ]]; then
      mkdir "$FIXTURE_SOURCE"
    fi
    [[ $scenario != present ]] || touch "$FIXTURE_STATE/target-present"
    args=("$mode" aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa)
    if [[ $scenario == explicit_fetch_failure ]]; then
      args+=(0.1.172-baiyu bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb "sha256:$(printf '%064d' 2)")
    fi
    status=0
    "$remote/cleaner.sh" "${args[@]}" > "$FIXTURE_STATE/report" 2> "$FIXTURE_STATE/stderr" || status=$?
    if [[ $scenario == fetch_success || $scenario == present ]]; then
      [[ $status == 0 ]]
      grep -Fxq 'space_status=sufficient' "$FIXTURE_STATE/report"
      grep -Fxq "cleanup_mode=$mode" "$FIXTURE_STATE/report"
      grep -q '^docker:ps ' "$FIXTURE_LOG"
      if [[ $scenario == present ]]; then ! grep -q '^git:fetch$' "$FIXTURE_LOG"; fi
    else
      [[ $status != 0 && ! -s $FIXTURE_STATE/report ]]
      ! grep -Eq '^docker:(ps|image|exec|buildx|rm)' "$FIXTURE_LOG"
      case "$scenario" in
        missing_source|symlink_source) ! grep -q '^git:' "$FIXTURE_LOG";;
        fetch_failure|explicit_fetch_failure) [[ $status == 42 ]];;
        fetch_timeout) [[ $status == 124 || $status == 137 ]];;
        fetch_kill) [[ $status == 137 ]];;
        still_missing) [[ $(grep -c '^git:cat-file$' "$FIXTURE_LOG") == 2 ]];;
      esac
    fi
    checks=$((checks + 1))
  done
done
printf 'vm_space_source_integration=pass\nchecks=%s\n' "$checks"
'''
        result = runner.run("local_vm", script, {"vm_space_source_integration", "checks"}, timeout=120)
        print(result.values)
    finally:
        runner.run("local_vm", f"rm -rf -- {shlex.quote(remote)} && printf 'fixture_removed=true\\n'", {"fixture_removed"})


if __name__ == "__main__":
    main()
