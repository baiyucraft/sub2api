# 发布 Runner 生命周期

生产发布必须由独立 runner 持续执行，调用端只负责启动、观察和验真。宿主工具的超时、断开 stdout 或会话关闭都不能终止 runner。

## 单控制台观察规范

日常人工发布使用：

```text
deploy-follow --profile <profile> --commit <完整SHA> --mode downtime --lang zh-CN
```

该入口只创建一个隐藏 runner，并在当前控制台读取同一个 release 的结构化状态。阶段变化和长阶段心跳使用中文输出；机器 JSON、Gate 字段和稳定 failure code 保持英文。观察器关闭、超时或 Ctrl+C 后，使用 `follow <release_id>` 重新 attach，不能再次执行 `deploy` 或创建第二个 runner。

Windows 下 runner、Python、OpenSSL、Git Bash 和 Go 子进程统一走 `scripts/release/process.py`。不得混用 `DETACHED_PROCESS` 和 `CREATE_NO_WINDOW`，也不得在发布路径中直接调用裸 `subprocess.run/Popen/check_output`。

```text
完整 SHA
  -> deploy-start（预分配 release ID、manifest、runner.json）
  -> 独立 worker（doctor/bootstrap/VM Gate/production 全程持锁）
  -> status 或 wait（只读、超时不 kill）
      ├ verified            -> verify-result
      ├ recovered           -> verify-recovery-result
      ├ running             -> 继续 wait
      └ exited unverified   -> reconcile-inspect
                               ├ claim_only_recover -> 用户确认后 reconcile --mode recover
                               └ 其他 -> blocked，人工完成证据审计
```

## 标准命令

```text
python .agents/skills/sub2api-production-deploy/scripts/release.py deploy-start --profile <profile> --commit <完整40位SHA> --mode blue-green|downtime
python .agents/skills/sub2api-production-deploy/scripts/release.py status <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py wait <release_id> --timeout 900
python .agents/skills/sub2api-production-deploy/scripts/release.py verify-result <release_id>
python .agents/skills/sub2api-production-deploy/scripts/release.py verify-recovery-result <release_id>
```

两个验真入口禁止互相替代：`verify-result` 只验证 signed candidate 已成为生产运行版本；`verify-recovery-result` 只验证协调恢复已恢复恢复点绑定的旧版本、清除 claim、恢复入口与备份 units 并完成恢复状态收口。

`status` 只输出固定字段：release/profile/commit、runner 存活与退出码、VM/production 阶段、候选和运行镜像、claim 最终状态、更新时间。禁止输出完整 JSON、argv、日志、secret 或远端原始回包。PID 必须同时匹配记录的进程启动 token，防止 PID 重用。

生产 runner 在远端 release 目录的 `logs/production.raw.log` 保存完整 stdout/stderr，并在最终接管脚本退出时追加稳定容器与临时候选容器最近 15 分钟的启动日志。目录权限固定为 `0700`、文件权限固定为 `0600`，只保留在生产机，不进入 Gate、备份 bundle、本地 `.tmp` 或报告。失败时先使用 `finalize_failure_phase`、行号、退出码和白名单错误分类定位；只有确需深入时才在生产机本地对原始日志做脱敏检索，禁止整文件回传。

`wait --timeout` 到期只返回 `still_running`，绝不杀进程、重启发布或并发执行第二个 `deploy`。成功不能由退出码、健康接口或 Gate 单项推出；`verify-result` 必须重新验签并核对 VM 状态、production-result、双链路、备份 units、claim 和 signed candidate image。

蓝绿生产阶段会依次记录 `candidate_started`、`candidate_healthy`、`nginx_reloaded`、`old_slot_draining` 和 `old_slot_drained`。`old_slot_draining` 默认 deadline 为 3900 秒（含命令余量），实际连接排空上限为 3600 秒。该阶段长时间无控制台输出属于预期，必须用 `status`/`wait` 观察，不得启动第二个 runner。

路由已经切换但旧 slot 未排空时，失败恢复优先执行 `rollback-route.sh`：恢复 pre-release upstream、`nginx -t`、graceful reload、写回 active slot并删除未使用 candidate。只有进入协调数据恢复时才允许停止 Nginx、PostgreSQL/Redis 或稳定应用容器。

## 故障边界

`stage_assets_verified` 之后没有 `production_preflight`，且 runner 已退出时，归类为 caller/runner interruption。只有 active claim 精确匹配、没有 production state、旧应用 healthy、Nginx active、backup timer enabled 且没有危险阶段，才允许 claim-only recovery。任何状态不明、迁移或公开流量已开始，都保持 `blocked`，不得删除 marker 或手工编辑 JSON。

一次 release 只允许一个 worker、一个 active claim 和一个 candidate。`.release.lock` 是 OS 文件锁，不以锁文件是否存在判断是否持锁；锁从 doctor 开始一直保持到最终收口。

## 恢复 Runner 原子版本与断点续跑

恢复 runner 必须上传并使用当前 commit 的单一 helper bundle，至少包含 orchestrator、restore、cleanup 和 reconcile。bundle 以小写 SHA-256 绑定到 manifest、runner state 和最终报告；新版 supervisor 不得回退调用事故 release 目录内的旧 helper，旧资产也不得删除或覆盖。

恢复 runner 按 `postgres_restored`、`redis_restored`、`compose_restored`、`app_healthy`、`nginx_restored`、`backup_units_restored`、`claim_reconciled`、`state_cleanup` 顺序保存幂等 checkpoint。重连或进程中断后先复核现场再跳过已完成阶段；状态不一致时 fail-closed。业务已经恢复、只剩 cleanup 时，不得重新执行数据库或 Redis 恢复。

三层门禁时间预算为 `fast=0-2min`、`specialized=5-15min`、`full=20-60min`。这些值仅用于计划、状态心跳和最终报告，不缩短子阶段原有 timeout，也不授权调用端在预算到期后杀死 runner。
