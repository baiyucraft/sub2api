"""Owned Linux process groups for the isolated matrix; standard library only."""
from __future__ import annotations

import argparse
import contextlib
import fcntl
import json
import os
import pathlib
import signal
import subprocess
import sys
import time


def atomic_json(path, data):
    temporary = path.with_name(path.name + '.new')
    with temporary.open('x') as stream:
        os.chmod(temporary, 0o600)
        json.dump(data, stream, indent=2)
        stream.write('\n')
    temporary.replace(path)


def process_stat(pid):
    try:
        fields = pathlib.Path(f'/proc/{pid}/stat').read_text().rsplit(') ', 1)[1].split()
        return {'pid': int(pid), 'state': fields[0], 'pgid': int(fields[2]),
                'sid': int(fields[3]), 'start_ticks': int(fields[19])}
    except (FileNotFoundError, ProcessLookupError):
        return None


def group_members(pgid):
    members = []
    for path in pathlib.Path('/proc').iterdir():
        if path.name.isdigit():
            info = process_stat(int(path.name))
            if info and info['pgid'] == pgid:
                members.append(info)
    return members


def verify_members(owner, members):
    if owner['pid'] <= 1 or owner['pid'] != owner['pgid'] or owner['sid'] != owner['pid']:
        raise RuntimeError('invalid_owned_group_identity')
    for member in members:
        if member['sid'] != owner['sid'] or member['start_ticks'] < owner['start_ticks']:
            raise RuntimeError('owned_group_identity_mismatch')
        if member['pid'] == owner['pid'] and member['start_ticks'] != owner['start_ticks']:
            raise RuntimeError('owned_leader_reused')
        if member['state'] == 'Z':
            continue
        try:
            environment = pathlib.Path(f"/proc/{member['pid']}/environ").read_bytes().split(b'\0')
            command = pathlib.Path(f"/proc/{member['pid']}/cmdline").read_bytes().split(b'\0')
        except (FileNotFoundError, ProcessLookupError):
            continue
        if ('MATRIX_JOB_TOKEN=' + owner['token']).encode() not in environment:
            raise RuntimeError('owned_process_token_mismatch')
        if member['pid'] == owner['pid'] and os.fsencode(owner['supervisor']) not in command:
            raise RuntimeError('owned_supervisor_command_mismatch')


@contextlib.contextmanager
def ownership_lock(root):
    with (root / 'jobs.lock').open('a') as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        yield


def checked_root(raw):
    path = pathlib.Path(raw)
    if not path.is_absolute() or path.is_symlink() or not path.is_dir():
        raise RuntimeError('invalid_owned_root')
    if path.parent != pathlib.Path('/tmp') or not path.name.startswith('codex-upstream-table-matrix'):
        raise RuntimeError('unexpected_owned_root')
    return path


def execute(root, cell):
    if not cell or pathlib.Path(cell).name != cell:
        raise RuntimeError('invalid_cell_identity')
    output = root / cell
    if output.is_symlink() or not output.is_dir():
        raise RuntimeError('invalid_owned_cell')
    token = os.environ['MATRIX_JOB_TOKEN']
    if os.getpgrp() != os.getpid() or os.getsid(0) != os.getpid():
        os.setsid()
    with ownership_lock(root):
        if (root / 'stop.requested').exists():
            raise RuntimeError('owned_task_already_stopping')
        owner = {**process_stat(os.getpid()), 'token': token,
                 'root': str(root), 'cell': cell, 'supervisor': str(pathlib.Path(__file__).resolve())}
        atomic_json(output / 'job.owner.json', owner)
    with (output / 'job.input.json').open() as stream:
        job = json.load(stream)
    (output / 'job.input.json').unlink()
    children = []
    code = 1
    def terminate(signum, frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    try:
        with (output / 'cpu_sampler.log').open('w') as sampler_log, (output / 'run.log').open('w') as log:
            sampler = subprocess.Popen([sys.executable, str(root / 'pg_cpu_sample.py'), str(output), job['cpu_file']],
                                       stdout=sampler_log, stderr=subprocess.STDOUT)
            children.append(sampler)
            test = subprocess.Popen(job['command'], env={**os.environ, **job['environment']},
                                    stdout=log, stderr=subprocess.STDOUT)
            children.append(test)
            code = test.wait()
            (output / 'process.finished').touch()
            sampler.wait(timeout=10)
    finally:
        # Stop handlers before reaping: a second group signal must not abort
        # the bounded wait and leave descendants after the leader has exited.
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        (output / 'process.finished').touch()
        for child in reversed(children):
            if child.poll() is None:
                child.terminate()
        for child in reversed(children):
            try:
                child.wait(timeout=5)
            except subprocess.TimeoutExpired:
                child.kill()
                child.wait(timeout=5)
        remaining = group_members(owner['pgid'])
        atomic_json(output / 'job.finished.json', {'exit_code': code, 'children_reaped': True,
                    'owner_start_ticks': owner['start_ticks'],
                    'group_drained': all(member['pid'] == owner['pid'] for member in remaining)})
    print(f'exit_code={code}', flush=True)


def stop(root, token):
    result = {'owned_processes_stopped': False, 'groups': [], 'errors': []}
    # Late starters cannot publish an owner after this barrier. Registrations
    # that already passed it are included before any resource is removed.
    with ownership_lock(root):
        (root / 'stop.requested').touch(mode=0o600)
        paths = sorted(root.glob('*/job.owner.json'))
        for path in paths:
            try:
                if path.is_symlink() or path.parent.is_symlink():
                    raise RuntimeError('invalid_owned_marker')
                owner = json.loads(path.read_text())
                if (owner['token'] != token or owner['root'] != str(root)
                        or owner['cell'] != path.parent.name
                        or owner['supervisor'] != str(pathlib.Path(__file__).resolve())):
                    raise RuntimeError('owned_marker_identity_mismatch')
                info = {'cell': owner['cell'], 'pid': owner['pid'], 'stopped': False}
                result['groups'].append(info)
                finished_path = path.with_name('job.finished.json')
                finished = json.loads(finished_path.read_text()) if finished_path.is_file() and not finished_path.is_symlink() else {}
                leader = process_stat(owner['pid'])
                if (finished.get('group_drained') is True and finished.get('owner_start_ticks') == owner['start_ticks']
                        and (leader is None or leader['start_ticks'] != owner['start_ticks'])):
                    # The supervisor observed no descendants before exiting.
                    # Its numerical PID/PGID may now name an unrelated group.
                    info.update(stopped=True, terminal_identity_retired=True)
                    continue
                members = group_members(owner['pgid'])
                verify_members(owner, members)
                for signum, seconds in ((signal.SIGTERM, 10), (signal.SIGKILL, 5)):
                    if not members:
                        break
                    verify_members(owner, members)
                    try:
                        os.killpg(owner['pgid'], signum)
                    except ProcessLookupError:
                        pass
                    deadline = time.monotonic() + seconds
                    while members and time.monotonic() < deadline:
                        time.sleep(.1)
                        members = group_members(owner['pgid'])
                        verify_members(owner, members)
                info['stopped'] = not members
                if members:
                    raise RuntimeError('owned_process_group_remains')
            except Exception as error:
                result['errors'].append({'cell': path.parent.name, 'error_type': type(error).__name__,
                                         'reason': str(error) if isinstance(error, RuntimeError) else 'ownership_check_failed'})
    result['owned_processes_stopped'] = not result['errors'] and all(item['stopped'] for item in result['groups'])
    atomic_json(root / 'process-cleanup.json', result)
    if not result['owned_processes_stopped']:
        raise RuntimeError('owned_process_cleanup_incomplete')
    print('owned_processes_stopped=true', flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('mode', choices=('execute', 'stop'))
    parser.add_argument('root')
    parser.add_argument('identity')
    args = parser.parse_args()
    root = checked_root(args.root)
    if args.mode == 'execute':
        execute(root, args.identity)
    else:
        stop(root, args.identity)


if __name__ == '__main__':
    main()
