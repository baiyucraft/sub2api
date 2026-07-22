# 发布 Runner 生命周期

生产发布必须由独立 runner 持续执行，调用端只负责启动、观察和验真。宿主工具的超时、断开 stdout 或会话关闭都不能终止 runner。

```text
完整 SHA
  -> deploy-start（预分配 release ID、manifest、runner.json）
  -> 独立 worker（doctor/bootstrap/VM Gate/production 全程持锁）
  -> status 或 wait（只读、超时不 kill）
      ├ verified/recovered -> verify-result
      ├ running             -> 继续 wait
      └ exited unverified   -> reconcile-inspect
                               ├ claim_only_recover -> 用户确认后 reconcile --mode recover
                               └ 其他 -> blocked，人工完成证据审计
```

## 标准命令

```text
python deploy/release.py deploy-start --profile <profile> --commit <完整40位SHA>
python deploy/release.py status <release_id>
python deploy/release.py wait <release_id> --timeout 900
python deploy/release.py verify-result <release_id>
```

`status` 只输出固定字段：release/profile/commit、runner 存活与退出码、VM/production 阶段、候选和运行镜像、claim 最终状态、更新时间。禁止输出完整 JSON、argv、日志、secret 或远端原始回包。PID 必须同时匹配记录的进程启动 token，防止 PID 重用。

`wait --timeout` 到期只返回 `still_running`，绝不杀进程、重启发布或并发执行第二个 `deploy`。成功不能由退出码、健康接口或 Gate 单项推出；`verify-result` 必须重新验签并核对 VM 状态、production-result、双链路、备份 units、claim 和 signed candidate image。

## 故障边界

`stage_assets_verified` 之后没有 `production_preflight`，且 runner 已退出时，归类为 caller/runner interruption。只有 active claim 精确匹配、没有 production state、旧应用 healthy、Nginx active、backup timer enabled 且没有危险阶段，才允许 claim-only recovery。任何状态不明、迁移或公开流量已开始，都保持 `blocked`，不得删除 marker 或手工编辑 JSON。

一次 release 只允许一个 worker、一个 active claim 和一个 candidate。`.release.lock` 是 OS 文件锁，不以锁文件是否存在判断是否持锁；锁从 doctor 开始一直保持到最终收口。
