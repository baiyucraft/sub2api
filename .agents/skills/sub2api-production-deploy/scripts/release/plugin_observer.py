from __future__ import annotations

import argparse
import time

from .plugin_supervisor import start, status_view, terminal_exit


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
    "verified": "生产插件已验证",
}

_ZH_VM_POWER = {
    "checking": "正在核对本地 VM 状态",
    "starting": "本地 VM 正在启动",
    "waiting_for_ssh": "等待本地 VM SSH 就绪",
    "running": "本地 VM 已运行",
    "stopping": "本地 VM 正在正常关机",
    "stopped": "本地 VM 已关闭",
}
_ZH_VM_CLEANUP = {
    "pending": "等待本地 VM 收口",
    "failed": "本地 VM 关机失败，生产插件验证证据已保留",
    "preserved": "保留发布前已运行的本地 VM",
    "retained_failure": "发布失败，本地 VM 已保留",
    "shutdown_reply_uncertain": "本地 VM 关机回包不确定，等待电源状态验证",
}


def follow(args: argparse.Namespace) -> int:
    last_stage = None
    last_vm = (None, None)
    while True:
        view = status_view(args.release_id)
        stage = view.get("stage")
        if stage != last_stage:
            print(f"[{args.release_id}] {_ZH_STAGE.get(str(stage), str(stage))}", flush=True)
            last_stage = stage
        vm = (view.get("vm_power_status"), view.get("vm_cleanup_status"))
        if vm != last_vm:
            message = _ZH_VM_CLEANUP.get(vm[1]) or _ZH_VM_POWER.get(vm[0])
            if message:
                print(f"[{args.release_id}] {message}", flush=True)
            last_vm = vm
        result = terminal_exit(view)
        if result is not None:
            return result
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
