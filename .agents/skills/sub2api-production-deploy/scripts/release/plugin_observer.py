from __future__ import annotations

import argparse
import time

from .plugin_state import PLUGIN_TERMINAL_STATES
from .plugin_supervisor import start, status_view


_ZH_STAGE = {
    "initialized": "已初始化",
    "package_built": "双架构插件包已构建",
    "local_verified": "本地签名与内容校验通过",
    "awaiting_vm_authorization": "准备执行 VM8211 插件 Gate",
    "vm_gate_verified": "VM8211 插件 Gate 已通过",
    "awaiting_production_authorization": "准备执行生产安装或升级",
    "production_preflight_verified": "生产预检通过",
    "install_or_upgrade_started": "生产安装或升级已开始",
    "installation_committed": "生产安装记录已提交",
    "instances_verified": "实例恢复校验通过",
    "verified": "插件发布已验证",
}


def follow(args: argparse.Namespace) -> int:
    last_stage = None
    while True:
        view = status_view(args.release_id)
        stage = view.get("stage")
        if stage != last_stage:
            print(f"[{args.release_id}] {_ZH_STAGE.get(str(stage), str(stage))}", flush=True)
            last_stage = stage
        if view.get("status") in PLUGIN_TERMINAL_STATES:
            return 0 if view.get("status") == "verified" else 2
        time.sleep(max(1, int(getattr(args, "heartbeat", 5))))


def deploy_follow(args: argparse.Namespace) -> int:
    identifier = start(args, announce=False)
    print(f"plugin_release_id={identifier}", flush=True)
    return follow(
        argparse.Namespace(
            release_id=identifier,
            lang=getattr(args, "lang", "zh-CN"),
            heartbeat=getattr(args, "heartbeat", 5),
        )
    )
