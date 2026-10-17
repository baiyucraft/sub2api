"""Run targeted race checks with an owned Linux toolchain/source/cache tree.

The toolchain module must first be downloaded using the project's Go command:
 go mod download -json golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64
Save that JSON as .tmp/upstream-table-linux-toolchain.json. No stack image,
existing database, or existing source checkout is changed by this runner.
"""
from __future__ import annotations

import hashlib
import io
import json
import os
import pathlib
import shlex
import sys
import tarfile
import time

ROOT = pathlib.Path(__file__).resolve().parents[6]
sys.path.insert(0, str(ROOT / '.agents/skills/sub2api-production-deploy/scripts'))
from release.process import run_hidden
from release.ssh import SSHRunner

EXPECTED_TOOLCHAIN = 'v0.0.1-go1.27.2.linux-amd64'
EXPECTED_TOOLCHAIN_SUM = 'h1:N6Xv/1OLzI0jVVkUwkrxHRZc/GCTARUo6idcngKwlDE='


def objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        yield value
        raw = raw.lstrip()[end:]


def main():
    output = ROOT / '.tmp/upstream-table-race'
    output.mkdir(exist_ok=True, parents=True)
    toolchain = json.loads((ROOT / '.tmp/upstream-table-linux-toolchain.json').read_text())
    if toolchain['Version'] != EXPECTED_TOOLCHAIN or toolchain['Sum'] != EXPECTED_TOOLCHAIN_SUM:
        raise RuntimeError('toolchain identity differs from the verified module')
    zip_path = pathlib.Path(toolchain['Zip'])
    archive = output / 'input.tar.gz'
    env = dict(os.environ, GOTOOLCHAIN='go1.27.2')
    paths = run_hidden(['git', 'ls-files', '--cached', '--others', '--exclude-standard', '-z', 'backend'], cwd=ROOT, capture_output=True, check=True).stdout.split(b'\0')
    modules = list(objects(run_hidden(['go', 'list', '-m', '-json', 'all'], cwd=ROOT / 'backend', env=env, capture_output=True, check=True, text=True).stdout))
    sources = {}
    dependency_files = {}
    with tarfile.open(archive, 'w:gz', compresslevel=1) as tar:
        for raw in paths:
            if not raw:
                continue
            relative = pathlib.Path(os.fsdecode(raw))
            source = ROOT / relative
            if not source.is_file():
                continue
            sources[str(relative).replace('\\', '/')] = hashlib.sha256(source.read_bytes()).hexdigest()
            tar.add(source, arcname='source/' + relative.as_posix(), recursive=False)
        # Only module download-cache files named by the resolved go.mod graph
        # are copied. No environment, VCS credentials or global config enters.
        for module in modules:
            go_mod = module.get('GoMod')
            if module.get('Main') or not go_mod or '/cache/download/' not in go_mod.replace('\\', '/'):
                continue
            path = pathlib.Path(go_mod)
            normalized = path.as_posix()
            suffix = normalized.split('/cache/download/', 1)[1]
            for item in path.parent.glob(path.stem + '.*'):
                if item.suffix not in {'.mod', '.info', '.zip', '.ziphash'}:
                    continue
                target = 'cache/mod/cache/download/' + suffix.rsplit('/', 1)[0] + '/' + item.name
                dependency_files[target] = hashlib.sha256(item.read_bytes()).hexdigest()
                tar.add(item, arcname=target, recursive=False)
    provenance = {'toolchain_module': EXPECTED_TOOLCHAIN, 'toolchain_sum': EXPECTED_TOOLCHAIN_SUM,
                  'toolchain_zip_sha256': hashlib.sha256(zip_path.read_bytes()).hexdigest(),
                  'input_sha256': hashlib.sha256(archive.read_bytes()).hexdigest(),
                  'source_sha256': sources, 'dependency_cache_sha256': dependency_files,
                  'scope': 'owned toolchain, source and caches on local_vm; no application/dependency stack changes'}
    (output / 'provenance.json').write_text(json.dumps(provenance, indent=2) + '\n')
    print(json.dumps({'prepared': True, 'source_files': len(sources), 'dependency_files': len(dependency_files), 'input_bytes': archive.stat().st_size, 'toolchain_zip_bytes': zip_path.stat().st_size}), flush=True)
    runner = SSHRunner()
    remote = runner.create_temp_dir('local_vm', '/tmp', 'codex-upstream-table-race')
    result = {'remote_owned_temp': remote, 'tests': {}, 'cleanup': {}}
    (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
    try:
        runner.upload_file('local_vm', archive, remote + '/input.tar.gz')
        runner.upload_file('local_vm', zip_path, remote + '/toolchain.zip')
        command = r'''set -eu
cd __ROOT__
test "$(sha256sum input.tar.gz | cut -d' ' -f1)" = __INPUT_SHA__
test "$(sha256sum toolchain.zip | cut -d' ' -f1)" = __GO_SHA__
tar -xzf input.tar.gz
python3 - <<'PY'
import os, pathlib, zipfile
root=pathlib.Path('toolchain').resolve()
with zipfile.ZipFile('toolchain.zip') as archive:
 for item in archive.infolist():
  path=(root/item.filename).resolve()
  if not path.is_relative_to(root): raise RuntimeError('toolchain archive path')
  archive.extract(item,root)
  if not item.is_dir(): path.chmod((item.external_attr >> 16) & 0o777 or 0o644)
PY
go_dir=__ROOT__/toolchain/golang.org/toolchain@__GO_MODULE__
export PATH="$go_dir/bin:$PATH" GOROOT="$go_dir" GOTOOLCHAIN=local GOMAXPROCS=2 GOMEMLIMIT=600MiB
export GOCACHE=__ROOT__/cache/build GOMODCACHE=__ROOT__/cache/mod CGO_ENABLED=1
export GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org
test "$(go version)" = 'go version go1.27.2 linux/amd64'
printf 'toolchain_verified=true\n'
cd source/backend
set +e
go test -race -p 1 -parallel 2 ./internal/service ./internal/repository ./internal/handler/admin -run 'Test(AccountList|AccountQuality|GetAccountProxyConcurrency|UpstreamHealthConfidences|ConfidenceCountBoundary|ConfidenceDistributionBatch|.*Quality.*|.*List.*)' -count=1 -timeout=10m > __ROOT__/race.log 2>&1
code=$?
printf 'race_exit_code=%s\n' "$code"
'''
        for key, value in {'__ROOT__': shlex.quote(remote), '__INPUT_SHA__': shlex.quote(provenance['input_sha256']), '__GO_SHA__': shlex.quote(provenance['toolchain_zip_sha256']), '__GO_MODULE__': EXPECTED_TOOLCHAIN}.items():
            command = command.replace(key, value)
        started = time.monotonic()
        print(json.dumps({'race_started': True}), flush=True)
        values = runner.run('local_vm', command, {'toolchain_verified', 'race_exit_code'}, timeout=1500).values
        result['tests'] = {'toolchain_verified': values['toolchain_verified'], 'exit_code': int(values['race_exit_code']), 'wall_seconds': time.monotonic() - started}
        runner.download_file('local_vm', remote + '/race.log', output / 'race.log')
    finally:
        command = 'set -eu; test -d ' + shlex.quote(remote) + ' && test ! -L ' + shlex.quote(remote) + ' && rm -r -- ' + shlex.quote(remote) + '; test ! -e ' + shlex.quote(remote) + "; printf 'owned_temp_absent=true\\n'"
        result['cleanup'] = runner.run('local_vm', command, {'owned_temp_absent'}, timeout=90).values
        (output / 'result.json').write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result), flush=True)
    if result['tests'].get('exit_code') != 0 or result['cleanup'] != {'owned_temp_absent': 'true'}:
        raise SystemExit(1)


if __name__ == '__main__':
    main()
