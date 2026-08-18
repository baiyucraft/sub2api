"""Human-facing Chinese release observer.

The observer is deliberately read-only.  It attaches to one existing release
directory and never starts a second runner or reads raw production logs.
"""

from __future__ import annotations

import argparse
import time
from datetime import datetime
from typing import Any

from .supervisor import _read_json, _run_dir, start, status_view, verified_result_view


_STAGE_TEXT = {
    "init": "正在初始化发布状态",
    "vm_validate": "正在执行 VM 验证和候选构建",
    "production_release": "正在执行生产发布",
    "production_preflight": "正在执行生产预检",
    "backup": "正在创建生产备份",
    "backup_verified": "生产备份已通过",
    "migration_preflight": "正在执行迁移预检",
    "migration_and_switch": "正在执行迁移并切换应用",
    "candidate_started": "候选应用已启动，正在健康检查",
    "candidate_healthy": "候选应用健康检查通过",
    "public_route_verification": "正在验收公网链路",
    "nginx_reloaded": "Nginx 已重新加载",
    "downtime_finalizing": "正在完成停机发布收口",
    "production_verified": "生产发布已验真",
    "production_verified_after_reconciliation": "协调恢复后生产发布已验真",
}

_STATUS_TEXT = {
    "starting": "发布任务正在启动",
    "waiting_for_lock": "正在等待发布锁",
    "running": "发布任务运行中",
    "verified": "发布任务已完成",
    "recovered": "发布失败后已恢复旧版本",
    "failed": "发布任务失败",
    "blocked_reconciliation": "发布状态不明，已阻断等待协调",
}


def _now() -> str:
    return datetime.now().astimezone().strftime("%H:%M:%S")


def _failure_hint(identifier: str) -> str:
    """Return only a stable, allowlisted failure code for human display."""

    try:
        production = _read_json(_run_dir(identifier) / "gate" / "production-result.json") or {}
    except (OSError, RuntimeError):
        return "unknown"
    history = production.get("history")
    if not isinstance(history, list):
        return "unknown"
    keys = (
        "failure_code",
        "switch_failure_code",
        "init_failure_code",
        "migration_195_failure_code",
        "migration_233_failure_code",
        "gate_failure_category",
    )
    for event in reversed(history):
        if not isinstance(event, dict) or not isinstance(event.get("evidence"), dict):
            continue
        evidence = event["evidence"]
        for key in keys:
            value = evidence.get(key)
            if isinstance(value, str) and value and value not in {"none", "absent", "unknown"}:
                return value
    return "unknown"


def _status_fingerprint(value: dict[str, Any]) -> tuple[Any, ...]:
    return (
        value.get("runner_status"),
        value.get("vm_stage"),
        value.get("vm_status"),
        value.get("production_stage"),
        value.get("production_status"),
    )


def _print_change(identifier: str, value: dict[str, Any]) -> None:
    runner_status = str(value.get("runner_status") or "unknown")
    vm_stage = str(value.get("vm_stage") or "not_started")
    production_stage = str(value.get("production_stage") or "not_started")
    stage = production_stage if production_stage not in {"not_started", "init"} else vm_stage
    stage_text = _STAGE_TEXT.get(stage, _STATUS_TEXT.get(runner_status, "发布状态更新"))
    status_text = _STATUS_TEXT.get(runner_status)
    if runner_status in {"failed", "blocked_reconciliation"}:
        print(f"[{_now()}] {stage_text}；{status_text}（{_failure_hint(identifier)}）", flush=True)
    else:
        print(f"[{_now()}] {stage_text}；{status_text or '状态已更新'}", flush=True)


def follow(args: argparse.Namespace) -> int:
    heartbeat = max(10, int(args.heartbeat))
    last_fingerprint: tuple[Any, ...] | None = None
    last_output = time.monotonic()
    print(f"[{_now()}] 已连接发布任务 {args.release_id}，只读跟踪中", flush=True)
    try:
        while True:
            try:
                value = status_view(args.release_id)
            except (OSError, RuntimeError):
                print(f"[{_now()}] 暂时无法读取发布状态；后台 runner 未被终止，请稍后重新执行 follow {args.release_id}", flush=True)
                return 1
            fingerprint = _status_fingerprint(value)
            if fingerprint != last_fingerprint:
                _print_change(args.release_id, value)
                last_fingerprint = fingerprint
                last_output = time.monotonic()
            elif time.monotonic() - last_output >= heartbeat and value.get("runner_alive"):
                print(f"[{_now()}] 发布仍在进行，当前阶段：{value.get('production_stage') or value.get('vm_stage') or '初始化'}", flush=True)
                last_output = time.monotonic()
            if not value.get("runner_alive"):
                status = value.get("runner_status")
                if status == "verified":
                    try:
                        result = verified_result_view(args.release_id)
                    except Exception:
                        print(f"[{_now()}] runner 已结束，但最终验真未通过（unknown）", flush=True)
                        return 1
                    print(f"[{_now()}] 发布已验真，运行镜像与候选镜像一致：{result['running_image_id']}", flush=True)
                    return 0
                if status == "recovered":
                    print(f"[{_now()}] 发布未完成，旧版本已恢复；请保留当前 release 证据进行复盘", flush=True)
                    return 1
                if status in {"failed", "blocked_reconciliation"}:
                    print(f"[{_now()}] 发布已停止，失败码：{_failure_hint(args.release_id)}；禁止重复启动 release", flush=True)
                    return 1
            time.sleep(2)
    except KeyboardInterrupt:
        print(f"[{_now()}] 已停止观察，后台 runner 未被终止；可重新执行 follow {args.release_id}", flush=True)
        return 130


def deploy_follow(args: argparse.Namespace) -> int:
    try:
        identifier = start(args, announce=False)
    except (OSError, RuntimeError, ValueError):
        print(f"[{_now()}] 发布任务启动失败；请先检查 doctor 和现有 release 状态（unknown）", flush=True)
        return 1
    args.release_id = identifier
    print(f"[{_now()}] 发布任务已启动：{identifier}", flush=True)
    return follow(args)
