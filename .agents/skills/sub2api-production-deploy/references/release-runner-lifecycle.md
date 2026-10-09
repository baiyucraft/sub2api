# 发布 Runner 生命周期

生产发布必须由独立 runner 持续执行，调用端只负责启动、观察和验真。宿主工具的超时、断开 stdout 或会话关闭都不能终止 runner。

## 可选 VM 自动启停

公共配置位于 `.ssh.local` 顶层，与 `servers` 并列；下例是字段合同，不是对本地连接资料的读取或修改：

```yaml
vm_lifecycle:
  enabled: true
  vmrun_path: 'C:/MProgram/VMware/vmrun.exe'
  vmx_path: 'D:/vmu/Ubuntu.vmx'
  startup_timeout_seconds: 180
  shutdown_timeout_seconds: 180
```

未配置 `vm_lifecycle` 时保持既有人工电源管理行为。配置存在时必须验证结构、boolean、路径和正整数 timeout；启用时可执行文件与目标 VMX 必须可解析且存在。配置错误、`vmrun list` 失败或电源身份不明均 fail-closed，不能当作未配置、猜测 VM 已关机或绕过电源检查直接连接。

应用与独立插件后台发布 worker 共用以下电源合同；各自仍使用原有 Gate、生产写授权和验真入口：

```text
本地输入/身份/配置检查 -> 发布全局锁 + 跨 checkout VM 身份锁
  -> 持久所有权检查 -> vmrun list（首次 VM SSH/API 连接之前）
      已运行 -> 记录本 release 未启动，始终保留 VM
      已停止 -> 持久记录启动意图 -> vmrun start <vmx> nogui
                -> 有界等待电源及 SSH 就绪 -> 记录本次启动所有权
  -> 原有 VM Gate 与生产发布
      失败 / blocked / recovered / 崩溃 -> 保留 VM 和所有权证据
      成功 -> signed 验真 -> postdoctor / 插件逐实例验证
           -> 清理本 release 的 VM 隔离临时任务
           -> 本次启动且所有权一致：SSH 正常 poweroff
           -> vmrun list 核实目标 VM 停止
```

启动使用 `nogui`，不得弹出 VMware 窗口。启动等待受 `startup_timeout_seconds` 限制；电源已运行不等于 SSH、Guest 或 Gate 已就绪。启动失败、超时或结果不确定时停止发布并保留已落地的所有权证据，不能重复启动或自动关机。

### VM 身份锁与持久所有权

- 仓库 `.release.lock` 之外还须以规范化后的 VMX 身份建立跨 checkout 的主机共享锁；同一 VM 的应用/插件发布不得因工作区不同而并发。锁覆盖电源检查、启动、Guest 使用和最终清理/关机核实，不以锁文件存在与否判断是否持锁。
- 所有权必须跨进程和 checkout 持久保存，绑定 VM 身份、release ID、完整 commit、worker 进程启动身份、初始电源状态和启动/清理进度。启动前先留下意图，以便崩溃后能识别未完成操作；不能仅用内存布尔值或“VM 现在运行”推断本次所有权。
- 原本运行的 VM 记录 `vm_started_by_release=false`，无论发布结果如何都不关机。本次确认启动才记录 `true`，关机前重新核对同一 VM、release 和 worker 所有权。
- 锁随崩溃释放不等于所有权可接管。遗留、冲突、不完整或身份不一致的所有权使后续发布停止；不得自动删除、覆盖、接管或通过新 release 代为关机。先只读核对现场，再走获批的人工恢复流程。
- 正常收口后须持久记录完成结果并结束本次活动所有权，保留审计证据；原本运行的 VM 以保留运行的结果收口，本次启动的 VM 必须先核实停止。明确完成的历史记录不作为待恢复所有权接管；失败或状态不明的记录不得标成完成来放行新发布。

### 成功与关机分别验收

应用关机前必须完成同 release 的 signed `verify-result`、post-deploy `doctor`；插件必须完成 signed `plugin-verify-result` 和所有目标实例的版本、binary SHA、Health、受管范围验收。任何必要验收未通过都保留 VM。清理只限本 release 的隔离容器/任务、临时模拟配置、凭据和一次性材料；保留 PostgreSQL、Redis、`data-dev`、持久展示、回滚 image、Gate、marker 和失败证据。

满足上述条件且确属本次启动时，worker 通过 SSH 请求 Guest 正常 `poweroff`，在 `shutdown_timeout_seconds` 内反复成功读取 `vmrun list`，确认目标规范化 VMX 不再列出才报告停止。不依赖 VMware Tools，不使用 hard stop/强制关机。SSH 因关机断开不能单独证明成功；`vmrun list` 失败、目标仍运行或关机超时均单独报告 cleanup 失败，保留所有权和诊断证据。

生产发布事实和 VM cleanup 结果必须分开：关机失败不能回滚已成功的应用/插件、重跑迁移、重新上传/升级或再次发布。恢复旧版本的 `recovered`、失败、blocked 和崩溃均不进入成功自动关机分支；即使存在部分成功证据，也先保留 VM。

### 只读入口与独立验证

普通 `doctor/status/wait/follow/verify-result` 和插件对应只读入口不自动开机、关机、接管所有权或执行 cleanup。VM 已停止时，普通 doctor 的 Guest 检查会失败，不能当作通过；重新验真使用已签名 Gate/结果和生产事实，不为读取结果而启动 VM。worker 的 postdoctor 必须在关机之前完成。

`deploy-follow` / `plugin-deploy-follow` 仅负责启动一次 worker 后观察；关闭观察器、超时或 Ctrl+C 不影响后台 worker、锁与所有权。独立 `vm-validate`、`vm-only-validate`、`vm-only-switch` 和用户展示不进入发布成功自动关机分支，不自动关闭 VM 或展示容器。

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

`status` 只输出固定字段：release/profile/commit、runner 存活与退出码、VM/production 阶段、候选和运行镜像、claim 最终状态、更新时间，以及 `vm_power_status`、`vm_started_by_release`、`vm_cleanup_status`。三个新增字段分别表示电源观察、本次启动归属和独立 cleanup 结果，不替代 production/plugin 成功证据；未知不能投影成已停止或归属本 release。禁止输出 VMX/可执行文件路径、完整所有权文件、完整 JSON、argv、日志、secret 或远端原始回包。PID 必须同时匹配记录的进程启动 token，防止 PID 重用。

生产 runner 在远端 release 目录的 `logs/production.raw.log` 保存完整 stdout/stderr，并在最终接管脚本退出时追加稳定容器与临时候选容器最近 15 分钟的启动日志。目录权限固定为 `0700`、文件权限固定为 `0600`，只保留在生产机，不进入 Gate、备份 bundle、本地 `.tmp` 或报告。失败时先使用 `finalize_failure_phase`、行号、退出码和白名单错误分类定位；只有确需深入时才在生产机本地对原始日志做脱敏检索，禁止整文件回传。

`wait --timeout` 到期只返回 `still_running`，绝不杀进程、重启发布或并发执行第二个 `deploy`。成功不能由退出码、健康接口或 Gate 单项推出；`verify-result` 必须重新验签并核对 VM 状态、production-result、双链路、备份 units、claim 和 signed candidate image。

蓝绿生产阶段会依次记录 `candidate_started`、`candidate_healthy`、`nginx_reloaded`、`old_slot_draining` 和 `old_slot_drained`。`old_slot_draining` 默认 deadline 为 3900 秒（含命令余量），实际连接排空上限为 3600 秒。该阶段长时间无控制台输出属于预期，必须用 `status`/`wait` 观察，不得启动第二个 runner。

路由已经切换但旧 slot 未排空时，失败恢复优先执行 `rollback-route.sh`：恢复 pre-release upstream、`nginx -t`、graceful reload、写回 active slot并删除未使用 candidate。只有进入协调数据恢复时才允许停止 Nginx、PostgreSQL/Redis 或稳定应用容器。

## 故障边界

候选构建失败的 Linux 故障注入通过版本化集成入口调用成对的 `audit_fixture`/`audit_only` 恢复审计 API。全程保留同一失败 release 的租约并持本地发布/VM 身份锁；远端持真实单元/Gate 锁后才创建唯一隔离 fixture，所有路径改写、容器及进程检查使用 stub。97 项检查及精确清理通过后，重新核对真实现场和原始本地证据；审计只返回 `audited`，不写 preserve 结果、lease、owner 或事件，不代表租约已释放。普通消费者的 `vm_guard` 拒绝保留租约规则保持不变，随后仍须用正式 CLI 重新审计收口。

VM Gate 的 `candidate_build` 已终止、生产尚未开始时，使用显式 `reconcile-vm-preserve <release_id> --failed-stage candidate-build`。默认入口仍只允许 validator 未启动。构建失败分支必须绑定本地与 VM 保留 manifest 的原始 SHA-256、严格验证构建阶段及 failure-category/line/detail、确认签名 Gate 与 candidate archive 不存在，并复核失败进程、同一 boot ID、VM 单元/Gate 锁、无在途 validator/构建、dev 健康、生产 release/claim 不存在及旧生产镜像一致。只接受原本运行且非本 release 启动的 VM。所有查询失败或任何后续阶段、身份及权限漂移均保留租约；收口仅写正式 preserve 证据并最后提交 released owner，保留失败状态、Gate 目录及原始日志，不清资源、不关机。恢复后使用新 commit、新 Gate、新 candidate 和新 release ID，不复用失败发布。

validator 尚未启动的失败使用独立 `reconcile-vm-preserve <release_id>`，不能调用依赖 Gate 的生产 reconciliation。此入口仅接受原本运行、未由 release 启动的 VM：失败 runner 已退出，manifest/runner/持久 owner 身份一致，本地无 Gate/生产 state，远端 Gate、生产 release 和 active claim 全部不存在，无 validator、构建或空间检查进程，VM boot ID/电源与 dev 健康可证明，生产 Nginx 和 backup timer 正常。入口在发布全局锁与跨 checkout VM 身份锁内重新核对，只把失败租约收口为 released/preserved，不改变失败状态、不删证据、不启动或关闭 VM。任何查询失败、状态漂移、已开始 validator/生产或本次启动 VM 都拒绝。owner 是最后提交标记，前序写失败保留原 owner，重新核对后才能收口。

`stage_assets_verified` 之后没有 `production_preflight`，且 runner 已退出时，归类为 caller/runner interruption。只有 active claim 精确匹配、没有 production state、旧应用 healthy、Nginx active、backup timer enabled 且没有危险阶段，才允许 claim-only recovery。任何状态不明、迁移或公开流量已开始，都保持 `blocked`，不得删除 marker 或手工编辑 JSON。

一次 release 只允许一个 worker、一个 active claim 和一个 candidate。`.release.lock` 是 OS 文件锁，不以锁文件是否存在判断是否持锁；worker 在本地检查后、首次 VM 连接前持锁，启用生命周期时同时取得跨 checkout VM 身份锁，保持到最终 cleanup/关机核实或失败收口。持久所有权不会因释放锁或 worker 退出而被自动接管。

## 恢复 Runner 原子版本与断点续跑

恢复 runner 必须上传并使用当前 commit 的单一 helper bundle，至少包含 orchestrator、restore、cleanup 和 reconcile。bundle 以小写 SHA-256 绑定到 manifest、runner state 和最终报告；新版 supervisor 不得回退调用事故 release 目录内的旧 helper，旧资产也不得删除或覆盖。

恢复 runner 按 `postgres_restored`、`redis_restored`、`compose_restored`、`app_healthy`、`nginx_restored`、`backup_units_restored`、`claim_reconciled`、`state_cleanup` 顺序保存幂等 checkpoint。重连或进程中断后先复核现场再跳过已完成阶段；状态不一致时 fail-closed。业务已经恢复、只剩 cleanup 时，不得重新执行数据库或 Redis 恢复。

三层门禁时间预算为 `fast=0-2min`、`specialized=5-15min`、`full=20-60min`。这些值仅用于计划、状态心跳和最终报告，不缩短子阶段原有 timeout，也不授权调用端在预算到期后杀死 runner。

失败租约恢复在VM上还须独占既有发布单元锁，以排除SSH断线后仍在途的安装和VM-only任务；默认分支存在validator启动原始日志时，即使Gate目录缺失和进程已退出也拒绝收口。显式构建失败分支另按上述严格证据验真。生产健康检查沿用Gate前入口策略，允许健康的needs_update，不要求尚未发生的生产策略升级；镜像身份和其余健康、备份、claim校验不放宽。
