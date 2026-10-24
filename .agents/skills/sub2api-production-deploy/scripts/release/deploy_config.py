"""Local defaults for existing release commands; no execution or credentials."""
from __future__ import annotations

import json
from pathlib import Path
import re

from .atomic import read_replaceable_text
from .profiles import get_profile


_COMMAND_FIELDS = {
    "doctor": ("profile", "commit"),
    "recovery-review-scope": ("profile", "commit"),
    "vm-only-validate": ("profile", "commit"),
    "vm-validate": ("profile", "commit", "mode"),
    "deploy": ("profile", "commit", "mode"),
    "deploy-start": ("profile", "commit", "mode"),
    "deploy-follow": ("profile", "commit", "mode"),
}
_FIELDS = {"schema", "commit", "profile", "mode"}
_OPTIONS = {"--commit": "commit", "--profile": "profile", "--mode": "mode"}


def _unique_fields(pairs: list[tuple[str, object]]) -> dict:
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate deploy config field")
        result[key] = value
    return result


def _validate(field: str, value: object) -> None:
    if field == "commit":
        if not isinstance(value, str) or re.fullmatch(r"[0-9a-f]{40}", value) is None:
            raise ValueError("commit must be a complete 40-character lowercase SHA")
    elif field == "profile":
        if not isinstance(value, str):
            raise ValueError("profile must be a registered profile string")
        get_profile(value)
    elif field == "mode":
        if not isinstance(value, str) or value not in {"blue-green", "downtime"}:
            raise ValueError("mode must be blue-green or downtime")


def load_config(path: Path) -> dict:
    try:
        value = json.loads(read_replaceable_text(path, max_bytes=16384), object_pairs_hook=_unique_fields)
    except json.JSONDecodeError:
        raise ValueError("invalid deploy config JSON") from None
    if not isinstance(value, dict) or set(value) != _FIELDS:
        raise ValueError("deploy config requires exactly schema, commit, profile and mode")
    if type(value["schema"]) is not int or value["schema"] != 1:
        raise ValueError("unsupported deploy config schema")
    for field in ("commit", "profile", "mode"):
        _validate(field, value[field])
    return value


def prepare_arguments(arguments: list[str], workspace: Path) -> list[str]:
    """Fill only missing supported options; the original CLI still owns execution."""
    remaining, config_path, index = [], None, 0
    while index < len(arguments):
        argument = arguments[index]
        if argument == "--config" or argument.startswith("--config="):
            if config_path is not None:
                raise ValueError("duplicate --config")
            if argument == "--config":
                index += 1
                if index == len(arguments) or arguments[index].startswith("-"):
                    raise ValueError("--config requires a path")
                raw = arguments[index]
            else:
                raw = argument.partition("=")[2]
            if not raw:
                raise ValueError("--config requires a path")
            config_path = Path(raw)
        else:
            remaining.append(argument)
        index += 1
    if not remaining or "--help" in remaining or "-h" in remaining:
        return remaining
    fields = _COMMAND_FIELDS.get(remaining[0])
    if fields is None:
        if config_path is not None:
            raise ValueError("--config is not supported for this command")
        return remaining
    explicit, seen, index = {}, set(), 1
    while index < len(remaining):
        argument = remaining[index]
        option, separator, inline = argument.partition("=")
        if option.startswith("--"):
            if option in seen:
                raise ValueError(f"duplicate option: {option}")
            seen.add(option)
            if option not in _OPTIONS and any(name.startswith(option) for name in _OPTIONS):
                raise ValueError("use complete --commit, --profile and --mode option names")
        if option in _OPTIONS:
            if separator:
                value = inline
            else:
                index += 1
                if index == len(remaining) or remaining[index].startswith("-"):
                    raise ValueError(f"{option} requires a value")
                value = remaining[index]
            _validate(_OPTIONS[option], value)
            explicit[_OPTIONS[option]] = value
        index += 1
    selected = config_path if config_path is not None else workspace / ".release" / "deploy.local.json"
    if config_path is not None or selected.exists() or selected.is_symlink():
        config = load_config(selected)
        for field in fields:
            if field not in explicit:
                remaining.extend(["--" + field, config[field]])
    return remaining
