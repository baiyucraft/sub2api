#!/usr/bin/env python3
"""Build a private, disposable connection closure; never print credentials."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import stat
import sys


DB_KEYS = ('host', 'port', 'user', 'password', 'dbname', 'sslmode')
REDIS_KEYS = ('host', 'port', 'username', 'password', 'db', 'enable_tls')
CONFIG_ENV = {'CONFIG_FILE': '/app/data/config.yaml', 'DATA_DIR': '/app/data'}


def ordinary_ancestors(path: Path) -> None:
    for parent in path.absolute().parents:
        if not stat.S_ISDIR(parent.lstat().st_mode):
            raise ValueError()


def scalar(value: str) -> str:
    value = value.strip()
    if value.startswith('"'):
        result, end = json.JSONDecoder().raw_decode(value)
        if not isinstance(result, str) or not re.fullmatch(r'\s*(?:#.*)?', value[end:]):
            raise ValueError()
        return result
    if value.startswith("'"):
        match = re.fullmatch(r"('(?:[^']|'')*')\s*(?:#.*)?", value)
        if not match:
            raise ValueError()
        return match[1][1:-1].replace("''", "'")
    value = re.split(r'\s+#', value, maxsplit=1)[0].strip()
    if (not value or value[0] in '*&!{|>[`' or value.lower() in ('null', '~')
            or re.search(r':\s', value) or any(c in value for c in '\r\n\x00')):
        raise ValueError()
    return value


def read_config(path: Path) -> tuple[list[str], dict[str, dict[str, tuple[int, str]]], dict[str, int]]:
    ordinary_ancestors(path)
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 4 * 1024 * 1024:
        raise ValueError()
    lines = path.read_text(encoding='utf-8').splitlines(keepends=True)
    if lines and not lines[-1].endswith('\n'):
        lines[-1] += '\n'
    sections: dict[str, dict[str, tuple[int, str]]] = {}
    ends: dict[str, int] = {}
    section = None
    indent = None
    for index, line in enumerate(lines):
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        if not line[0].isspace():
            if line.startswith(('---', '...', '%', '"', "'")):
                raise ValueError()
            if section:
                ends[section] = index
            section = None
            key = line.split(':', 1)[0].strip().strip('"\'').lower()
            if key == '<<' or key.startswith(('database.', 'redis.')):
                raise ValueError()
            if key in ('database', 'redis'):
                if key in sections or not re.fullmatch(key + r':\s*(?:#.*)?', line.rstrip()):
                    raise ValueError()
                section = key
                sections[key] = {}
                indent = None
            continue
        if section:
            line_indent = len(line) - len(line.lstrip(' '))
            if indent is None:
                indent = line_indent
            if '\t' in line or line_indent != indent or not indent:
                raise ValueError()
            match = re.fullmatch(r' +([a-z][a-z0-9_]*):[ \t]*(.*)', line.rstrip('\r\n'))
            if not match:
                raise ValueError()
            key, value = match.groups()
            if key in sections[section]:
                raise ValueError()
            sections[section][key] = (index, scalar(value))
    if section:
        ends[section] = len(lines)
    if set(sections) != {'database', 'redis'}:
        raise ValueError()
    return lines, sections, ends


def write_private(path: Path, value: str) -> None:
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w', encoding='utf-8', newline='\n') as output:
        output.write(value)


def prepare(vm_config: Path, probe_config: Path, environment: list[str], database: str, output: Path) -> None:
    if not re.fullmatch(r'sub2api_(?:v2|probe)_[A-Za-z0-9_]+', database):
        raise ValueError()
    # Source and destination must be different ordinary files in private trees.
    if (vm_config.resolve() == probe_config.resolve() or probe_config.parent.is_symlink()
            or output.parent.is_symlink()):
        raise ValueError()
    ordinary_ancestors(output)
    if os.name != 'nt' and stat.S_IMODE(output.parent.stat().st_mode) & 0o077:
        raise ValueError()
    _, vm, _ = read_config(vm_config)
    lines, probe, ends = read_config(probe_config)
    env: dict[str, str] = {}
    for item in environment:
        if not isinstance(item, str) or '=' not in item:
            raise ValueError()
        key, value = item.split('=', 1)
        if key.startswith(('DATABASE_', 'REDIS_')) or key in CONFIG_ENV:
            if key in env or any(c in value for c in '\n\r\x00'):
                raise ValueError()
            env[key] = value
    if any(env.get(key) and env[key] != expected for key, expected in CONFIG_ENV.items()):
        raise ValueError()
    # Viper AutomaticEnv ignores empty values; fall back to the VM file.
    effective = {}
    for key in DB_KEYS:
        effective[key] = env.get('DATABASE_' + key.upper()) or vm['database'].get(key, (0, None))[1]
        if effective[key] is None:
            raise ValueError()
    role = effective['user']
    if not re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', role):
        raise ValueError()
    if not re.fullmatch(r'[1-9][0-9]{0,4}', effective['port']) or int(effective['port']) > 65535:
        raise ValueError()
    if effective['dbname'] != 'sub2api_dev' or effective['sslmode'] not in ('disable', 'allow', 'prefer', 'require', 'verify-ca', 'verify-full'):
        raise ValueError()
    if not effective['host'] or any(any(c in value for c in '\r\n\x00') for value in effective.values()):
        raise ValueError()
    db = dict(effective, host='sub2api-postgres', port='5432', sslmode='disable', dbname=database)
    redis = dict(host='probe-redis', port='6379', username='', password='', db='0', enable_tls='false')
    replacements = {'database': db, 'redis': redis}
    insertions: dict[int, list[str]] = {}
    for section, values in replacements.items():
        if probe[section]:
            first_line = lines[next(iter(probe[section].values()))[0]]
            section_indent = len(first_line) - len(first_line.lstrip(' '))
        else:
            section_indent = 2
        for key, value in values.items():
            rendered_value = value if key in ('port', 'db', 'enable_tls') else json.dumps(value, ensure_ascii=False)
            rendered = f'{" " * section_indent}{key}: {rendered_value}\n'
            if key in probe[section]:
                lines[probe[section][key][0]] = rendered
            else:
                insertions.setdefault(ends[section], []).append(rendered)
    content = ''.join(''.join(insertions.get(i, [])) + line for i, line in enumerate(lines))
    content += ''.join(insertions.get(len(lines), []))
    output.mkdir(mode=0o700)
    connection = ''.join(f'{section.upper()}_{key.upper()}={value}\n' for section, values in replacements.items() for key, value in values.items())
    connection += ''.join(f'{key}={value}\n' for key, value in CONFIG_ENV.items())
    write_private(output / 'connection.env', connection)
    pg = dict(PGHOST=db['host'], PGPORT=db['port'], PGUSER=role,
              PGPASSWORD=effective['password'], PGDATABASE='sub2api_dev', PGSSLMODE=db['sslmode'], PGCONNECT_TIMEOUT='10')
    write_private(output / 'postgres.env', ''.join(f'{key}={value}\n' for key, value in pg.items()))
    write_private(output / 'role', role + '\n')
    temporary = probe_config.with_name('.probe-config.tmp')
    write_private(temporary, content)
    os.chmod(temporary, stat.S_IMODE(probe_config.stat().st_mode))
    os.replace(temporary, probe_config)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument('--vm-config', type=Path, required=True)
    parser.add_argument('--probe-config', type=Path, required=True)
    parser.add_argument('--database', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--environment-file', type=Path, required=True)
    args = parser.parse_args()
    try:
        ordinary_ancestors(args.environment_file)
        info = args.environment_file.lstat()
        if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 1024 * 1024
                or (os.name != 'nt' and stat.S_IMODE(info.st_mode) & 0o077)):
            raise ValueError()
        environment = json.loads(args.environment_file.read_text(encoding='utf-8'))
        if not isinstance(environment, list):
            raise ValueError()
        prepare(args.vm_config, args.probe_config, environment, args.database, args.output)
    except Exception:
        print('probe_config_identity', file=sys.stderr)
        return 1
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
