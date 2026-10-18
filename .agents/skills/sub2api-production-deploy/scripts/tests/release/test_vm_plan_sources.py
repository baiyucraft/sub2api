"""Local fixture checks; actual migration-source regressions run on inert Linux.

Run vm_plan_sources_integration.py explicitly for the lock-protected Linux
execution. No SSH or Docker command is performed by ordinary pytest discovery.
"""
from __future__ import annotations

import os
import shutil
import subprocess

import pytest

import vm_plan_sources_integration as integration


def test_remote_python_fixture_compiles():
    compile(integration.REMOTE, '<vm-plan-sources-remote>', 'exec')
    # This also builds the program from the current validator, rather than a
    # frozen copy of the shell that could pass after the validator changes.
    integration.script()


def test_generated_shell_has_valid_bash_syntax(tmp_path):
    bash = shutil.which('bash')
    if not bash and os.name == 'nt':
        for candidate in ('C:/Program Files/Git/bin/bash.exe', 'C:/Program Files/Git/usr/bin/bash.exe'):
            if os.path.isfile(candidate):
                bash = candidate
                break
    if not bash:
        pytest.skip('bash unavailable; execute the explicit Linux fixture')
    path = tmp_path / 'vm-plan-sources.sh'
    path.write_text(integration.script(), encoding='utf-8', newline='\n')
    with path.open('rb') as source:
        result = subprocess.run([bash, '-n'], stdin=source, capture_output=True)
    assert result.returncode == 0, 'generated Linux fixture has invalid shell syntax'
