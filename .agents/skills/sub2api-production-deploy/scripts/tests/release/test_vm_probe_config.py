"""Cross-environment connection identity; no Docker or network operations."""
from __future__ import annotations

import ast
import importlib.util
import json
import os
from pathlib import Path
import secrets

import pytest


HERE = Path(__file__).resolve().parent
HELPER = HERE.parents[1] / "release/vm-probe-config.py"


def module():
    spec = importlib.util.spec_from_file_location("vm_probe_config_fixture", HELPER)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


VM = """server:
  host: 127.0.0.1
database:
  host: sub2api-postgres
  port: 5432
  user: vm_app
  password: 'vm-secret:# =value'
  dbname: sub2api_dev
  sslmode: disable
  max_open_conns: 9
  max_idle_conns: 3
redis:
  host: sub2api-redis
  port: 6379
  username: vm_acl
  password: vm_redis_secret
  db: 0
  enable_tls: false
  pool_size: 9
  min_idle_conns: 2
timezone: Asia/Shanghai
"""

PRODUCTION = """server:
  host: 0.0.0.0
database:
  host: production.invalid
  port: 6543
  user: production_app
  password: production_secret
  dbname: production_db
  sslmode: verify-full
  max_open_conns: 17
  max_idle_conns: 3
redis:
  host: production-redis.invalid
  port: 6380
  username: production_acl
  password: production_redis_secret
  db: 5
  enable_tls: true
  pool_size: 23
  min_idle_conns: 2
jwt:
  secret: retained-business-secret
timezone: Asia/Shanghai
"""


def paths(tmp_path, vm=VM, production=PRODUCTION):
    source = tmp_path / "vm.yaml"
    source.write_text(vm, encoding="utf-8")
    probe = tmp_path / "probe.yaml"
    probe.write_text(production, encoding="utf-8")
    return source, probe, tmp_path / "private-connection"


def run(tmp_path, vm=VM, production=PRODUCTION, environment=()):
    source, probe, output = paths(tmp_path, vm, production)
    module().prepare(source, probe, list(environment), "sub2api_v2_configfixture", output)
    return source, probe, output


def env_file(path):
    lines = path.read_text(encoding="utf-8").splitlines()
    result = dict(line.split("=", 1) for line in lines)
    assert len(lines) == len(result), "no ambiguous duplicate environment authority"
    return result


def test_production_credentials_are_replaced_and_business_configuration_retained(tmp_path):
    source, probe, output = run(tmp_path)
    assert source.read_text(encoding="utf-8") == VM
    configured = probe.read_text(encoding="utf-8")
    assert "production.invalid" not in configured
    assert "production_secret" not in configured
    assert "production_redis_secret" not in configured
    assert "production_acl" not in configured
    assert "retained-business-secret" in configured
    assert "max_open_conns: 17" in configured and "pool_size: 23" in configured
    assert "timezone: Asia/Shanghai" in configured
    connection = env_file(output / "connection.env")
    assert connection == {
        "DATABASE_HOST": "sub2api-postgres", "DATABASE_PORT": "5432",
        "DATABASE_USER": "vm_app", "DATABASE_PASSWORD": "vm-secret:# =value",
        "DATABASE_DBNAME": "sub2api_v2_configfixture", "DATABASE_SSLMODE": "disable",
        "REDIS_HOST": "probe-redis", "REDIS_PORT": "6379", "REDIS_USERNAME": "",
        "REDIS_PASSWORD": "", "REDIS_DB": "0", "REDIS_ENABLE_TLS": "false",
        "CONFIG_FILE": "/app/data/config.yaml", "DATA_DIR": "/app/data",
    }
    check = env_file(output / "postgres.env")
    assert check["PGUSER"] == connection["DATABASE_USER"]
    assert check["PGPASSWORD"] == connection["DATABASE_PASSWORD"]
    assert check["PGDATABASE"] == "sub2api_dev"
    assert (output / "role").read_text(encoding="utf-8").strip() == "vm_app"
    if os.name != "nt":
        assert output.stat().st_mode & 0o777 == 0o700
        assert all(p.stat().st_mode & 0o777 == 0o600 for p in output.iterdir())


def test_effective_vm_environment_wins_without_copying_other_secrets(tmp_path):
    environment = ["DATABASE_USER=effective_app", "DATABASE_PASSWORD=env-secret=with#chars",
                   "DATABASE_HOST=vm-service", "DATABASE_PORT=5439", "DATABASE_SSLMODE=require",
                   "JWT_SECRET=irrelevant-secret", "REDIS_USERNAME=irrelevant-acl"]
    _, probe, output = run(tmp_path, environment=environment)
    connection = env_file(output / "connection.env")
    assert connection["DATABASE_USER"] == "effective_app"
    assert connection["DATABASE_PASSWORD"] == "env-secret=with#chars"
    assert connection["DATABASE_HOST"] == "sub2api-postgres"
    assert connection["REDIS_USERNAME"] == ""
    check = env_file(output / "postgres.env")
    assert check["PGHOST"] == "sub2api-postgres" and check["PGPORT"] == "5432"
    assert check["PGSSLMODE"] == "disable"
    assert "irrelevant-secret" not in probe.read_text(encoding="utf-8")
    assert all("irrelevant-secret" not in p.read_text(encoding="utf-8") for p in output.iterdir())


def test_empty_environment_follows_viper_fallback_but_probe_redis_is_actually_empty(tmp_path):
    _, probe, output = run(tmp_path, environment=["DATABASE_USER=", "DATABASE_PASSWORD=", "REDIS_PASSWORD="])
    connection = env_file(output / "connection.env")
    assert connection["DATABASE_USER"] == "vm_app"
    assert connection["DATABASE_PASSWORD"] == "vm-secret:# =value"
    assert connection["REDIS_PASSWORD"] == "" and connection["REDIS_USERNAME"] == ""
    assert "production_redis_secret" not in probe.read_text(encoding="utf-8")


@pytest.mark.parametrize("password", ["", "spaces # = quote' double\" slash\\", "中文密码", "dollar$backtick`"])
def test_scalar_password_roundtrip_to_yaml_and_unquoted_env_file(tmp_path, password):
    vm = VM.replace("'vm-secret:# =value'", json.dumps(password, ensure_ascii=False))
    _, probe, output = run(tmp_path, vm=vm)
    assert env_file(output / "connection.env")["DATABASE_PASSWORD"] == password
    assert env_file(output / "postgres.env")["PGPASSWORD"] == password
    assert json.dumps(password, ensure_ascii=False) in probe.read_text(encoding="utf-8")


@pytest.mark.parametrize("replacement", [
    "  user: first\n  user: second", "  user: *alias", "  user: &anchor vm_app",
    "  \"user\": vm_app", "  <<: *defaults\n  user: vm_app", "\tuser: vm_app",
    "  user: vm_app;malformed", "  user: [vm_app]", "  user: {value: vm_app}",
])
def test_ambiguous_vm_identity_is_rejected_before_mutating_probe(tmp_path, replacement):
    vm = VM.replace("  user: vm_app", replacement)
    source, probe, output = paths(tmp_path, vm)
    before = probe.read_bytes()
    with pytest.raises(Exception):
        module().prepare(source, probe, [], "sub2api_v2_configfixture", output)
    assert probe.read_bytes() == before


@pytest.mark.parametrize("environment", [
    ["DATABASE_USER=first", "DATABASE_USER=second"],
    ["DATABASE_PASSWORD=secret\ninjected=value"],
    ["DATABASE_PASSWORD=secret\rvalue"], ["DATABASE_PASSWORD=secret\x00value"],
    ["DATABASE_USER=invalid-role"], ["DATABASE_PORT=0"], ["DATABASE_PORT=65536"],
    ["DATABASE_SSLMODE=malformed"],
])
def test_invalid_or_duplicate_connection_environment_fails_closed(tmp_path, environment):
    source, probe, output = paths(tmp_path)
    before = probe.read_bytes()
    with pytest.raises(Exception):
        module().prepare(source, probe, environment, "sub2api_v2_configfixture", output)
    assert probe.read_bytes() == before


@pytest.mark.parametrize("database", ["production", "sub2api_dev", "sub2api_v2_x;injection", "sub2api_v2_"])
def test_only_disposable_probe_database_names_are_accepted(tmp_path, database):
    source, probe, output = paths(tmp_path)
    with pytest.raises(Exception):
        module().prepare(source, probe, [], database, output)
    assert probe.read_text(encoding="utf-8") == PRODUCTION


def test_existing_output_is_not_overwritten(tmp_path):
    source, probe, output = paths(tmp_path)
    output.mkdir()
    marker = output / "marker"
    marker.write_text("preserve", encoding="utf-8")
    with pytest.raises(Exception):
        module().prepare(source, probe, [], "sub2api_v2_configfixture", output)
    assert marker.read_text() == "preserve"
    assert probe.read_text(encoding="utf-8") == PRODUCTION


@pytest.mark.parametrize("mode", ["source", "fast", "specialized", "full"])
def test_real_fixture_config_has_valid_pool_pairs_before_and_after_normalization(tmp_path, mode):
    # Evaluate only the real fixture's configuration builder: never execute its
    # Docker/bootstrap body during this offline regression.
    fixture = (HERE / "vm_probe_config_integration.sh").read_text(encoding="utf-8")
    embedded = fixture.split("python3 -B - <<'PY'\n", 1)[1].rsplit("\nPY", 1)[0]
    tree = ast.parse(embedded)
    builders = [node for node in tree.body if isinstance(node, ast.FunctionDef) and node.name == "config"]
    assert len(builders) == 1
    namespace = {"json": json, "secrets": secrets}
    exec(compile(ast.Module(body=builders, type_ignores=[]), "real-fixture-config", "exec"), namespace)
    source = tmp_path / "vm.yaml"
    probe = tmp_path / mode / "config.yaml"
    namespace["config"](source, "fixture_vm", "synthetic-vm", "sub2api_dev")
    namespace["config"](probe, "fixture_prod", "synthetic-prod", "fixture_source")
    helper = module()
    _, before, _ = helper.read_config(probe)
    pairs = {section: {key: int(before[section][key][1]) for key in keys} for section, keys in (
        ("database", ("max_open_conns", "max_idle_conns")),
        ("redis", ("pool_size", "min_idle_conns")),
    )}
    assert pairs["database"]["max_open_conns"] > 0
    assert 0 <= pairs["database"]["max_idle_conns"] <= pairs["database"]["max_open_conns"]
    assert pairs["redis"]["pool_size"] > 0
    assert 0 <= pairs["redis"]["min_idle_conns"] <= pairs["redis"]["pool_size"]
    if mode != "source":
        helper.prepare(source, probe, [], "sub2api_v2_offline_" + mode, tmp_path / "connection")
        _, after, _ = helper.read_config(probe)
        for section, values in pairs.items():
            assert {key: int(after[section][key][1]) for key in values} == values


@pytest.mark.skipif(os.name == "nt", reason="Linux inode guard tested by explicit fixture")
@pytest.mark.parametrize("target", ["source", "probe", "output_parent"])
def test_symlinked_authority_or_destination_is_rejected(tmp_path, target):
    source, probe, output = paths(tmp_path)
    if target == "output_parent":
        actual = tmp_path / "actual"
        actual.mkdir()
        alias = tmp_path / "alias"
        alias.symlink_to(actual, target_is_directory=True)
        output = alias / "connection"
    else:
        original = source if target == "source" else probe
        saved = original.with_suffix(".saved")
        original.rename(saved)
        original.symlink_to(saved)
    with pytest.raises(Exception):
        module().prepare(source, probe, [], "sub2api_v2_configfixture", output)
