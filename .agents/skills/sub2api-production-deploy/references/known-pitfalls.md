# 发布链已知踩坑与预防

本文只记录已在本 fork 发布链中复现或确认过的问题。每条记录都包含现象、根因、证据、修复、预防测试和状态；不写入凭据、完整环境、DSN 或原始远端日志。

## Windows Shell 与 Linux 能力边界

- 现象：在 Windows 直接运行 `.sh` 时出现路径、引号或命令不可用错误。
- 根因：PowerShell 与 Git Bash 的参数、路径和初始化语义不同；本机没有 `jq`、`docker`、`systemctl`、`flock` 等 Linux/容器能力。
- 证据：Git Bash `4.4.23(2)-release` 可执行；能力检测将缺少的 Linux 命令标记为 `vm_required`。
- 修复：统一使用 `scripts/windows/run-bash.ps1`，固定 `--noprofile --norc`、参数数组和 `cygpath`；Linux 服务、容器、权限、锁和 jq Gate 交给 VM。
- 预防测试：Git Bash 定位、空格与 `!` 路径、stdout/stderr、exit code、`bash -n` 以及缺少命令时的 `vm_required`。
- 状态：已修复。

## 远端 stderr 与原始日志

- 现象：远端命令失败后只看到不完整的错误摘要，重复执行可能造成二次写入风险。
- 根因：结构化 stdout 是机器接口，原始 stderr 不能直接混入 allowlist 或报告。
- 证据：SSH runner 对字段集合、marker、退出码和错误类型做校验；原始 stdout/stderr 只保存在目标节点 root-only release 日志目录。
- 修复：结构化事件写 JSONL；原始日志分别落盘，目录 `0700`、文件 `0600`，查询端只返回脱敏结构化记录，不自动回传原文。
- 预防测试：额外/缺失字段拒绝、敏感字段脱敏、symlink/多硬链接拒绝、远端状态不明时禁止重试。
- 状态：已修复。

## Release asset layout 迁移

- 现象：发布脚本从 `deploy/` 迁移到 skill 后，旧 Gate 或 VM 校验仍引用旧路径。
- 根因：资产 checksum、manifest 和 shell assertion 共同构成不可变合同，不能只移动文件。
- 证据：新 manifest 使用 `release_asset_layout=skill-v1`；缺失字段的历史 manifest 解释为 `deploy-v1`，历史 Gate 仅用于验真/恢复。
- 修复：manifest、validator、signer、DR verifier、prepare 和测试均按 layout 解析；新 candidate 禁止生成 `deploy-v1`。
- 预防测试：历史无字段兼容、新布局 allowlist、commit blob checksum、非法嵌套路径和 migration assertion 路径。
- 状态：收尾中，需通过完整 release suite 与 VM Gate 后关闭。

## 候选启动失败后的日志丢失

- 现象：停机发布在 `candidate_started` 后未达到 healthy，协调恢复删除候选容器后只能看到失败阶段，无法继续读取候选启动日志。
- 根因：生产 Docker 使用 `json-file` 日志驱动；容器删除会同时删除对应日志文件，而原始 release 日志此前只在最终接管阶段追加容器日志。
- 证据：release `235-5063cf9fd0c4-1786713743-0d64fa51` 在 `candidate_started -> candidate_healthy` 之间失败并完成协调恢复；候选无 OOM、端口占用或数据库/Redis 分类证据，但容器删除后日志不可恢复。
- 修复：候选健康等待失败时，在任何恢复或容器删除前，把候选最近 15 分钟日志追加到生产机 root-only `production.raw.log`，并原子保存状态、健康、退出码、OOM、重启次数、healthcheck 记录数、失败类型和脚本行号。
- 预防测试：候选提前退出立即失败、健康超时分类、root-only 原始日志追加、结构化字段白名单、协调恢复前证据已落盘。
- 状态：修复中，需通过 release suite、VM Gate 和下一次生产停机发布验证。

## Loopback 健康检查与生产绑定地址不一致

- 现象：候选进程保持 running、退出码为 0、无 OOM，应用日志明确显示已经监听 `127.0.0.1`，但 Docker 连续五次将容器判定为 unhealthy。
- 根因：生产候选显式绑定 IPv4 loopback，镜像 HEALTHCHECK 却访问 `localhost`；生产 Docker 环境中的 `localhost` 解析路径没有命中 IPv4 监听。
- 证据：candidate `sha256:6eb8dd24b84ec569171fc389e99d8e0b2b55e8450ae6faa51092f8a1de1de3d1` 在同一生产主机访问当前健康端点时，`localhost` 返回 exit 1，而 `127.0.0.1` 返回 exit 0；release `235-8f40f75ad81b-1786717077-274b236b` 随后完成协调恢复。
- 修复：Dockerfile HEALTHCHECK 固定使用 `127.0.0.1:${SERVER_PORT:-8080}`，与蓝绿和停机候选的 `SERVER_HOST=127.0.0.1` 合同一致。
- 预防测试：Dockerfile 合同测试拒绝恢复为 `localhost`；VM Gate 必须验证候选 Docker health、内部 `/health` 和实例 Header。
- 状态：修复中，需由新 full-SHA candidate 的 VM Gate 与生产停机发布验证。

## SSH 超时与重复 runner

- 现象：调用端超时后误以为 runner 未执行，重新启动第二个发布。
- 根因：前台工具生命周期短于独立 release worker；SSH 已开始执行后退出码不一定能说明远端是否提交。
- 证据：`.tmp/releases/<release_id>/runner.json`、结构化 `state.json`、远端 committed marker 和 active claim 是事实源。
- 修复：`deploy-start` 使用独立 worker；`status`/`wait` 只读观察；异常后先 reconciliation，不重复 `deploy`。
- 预防测试：PID 启动 token、单 runner/claim/candidate、marker 已提交但 stdout 解析失败、wait 超时不 kill。
- 状态：已修复。

## 生产部署模式

- 现象：未明确选择蓝绿或停机模式时，脚本行为与预期不一致。
- 根因：两种模式的连接排空、Nginx 控制和恢复边界不同。
- 证据：manifest、Gate、runner、状态和 production-result 均记录不可变 `deployment_mode`。
- 修复：交互终端显示“蓝绿无感切换/简单停机更新”；非交互未传 `--mode` 直接失败且不创建 release。
- 预防测试：两种模式的成功、失败、恢复与 reconciliation；部署开始后拒绝改变 mode。
- 状态：已实现，待 VM Gate 复验。

## 原始日志保留与清理

- 现象：失败日志需要长期追溯，但成功 release 不应无限占用磁盘。
- 根因：失败、恢复、当前基线和最近发布是证据，成功日志按保留期清理。
- 证据：retention plan 固定保护 active/current baseline/recovery/reconciliation 和最近 10 次 release。
- 修复：默认成功日志保留 90 天；清理采用 dry-run 生成 `plan_sha256`，apply 必须绑定同一 checksum。
- 预防测试：计划漂移拒绝、受保护 release 不删除、重复计划幂等和 `git diff --check`。
- 状态：CLI 已实现，待 VM Gate 复验。
