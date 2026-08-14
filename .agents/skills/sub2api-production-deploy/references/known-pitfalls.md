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
