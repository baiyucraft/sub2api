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

## Doctor 早期失败没有节点原始日志

- 现象：`deploy-start` 在 VM Gate 和生产阶段前以 `doctor.backup failed` 退出，只能看到远端 exit code，无法判断是目录、空间、DMIT 健康还是公网 IP 检查失败。
- 根因：原始日志包装只在生产 release 目录完成 staging 后启用；doctor、trust、临时目录和资产 staging 等更早的 SSH 命令仍只返回结构化 stdout，并隐藏远端 stderr。
- 证据：release `235-ada768cae453-1786735969-9712b65e` 在 `production_status=not_started` 时失败；拆分后的白名单检查确认只有备份机空间门禁失败，可用空间约 `4.90 GiB`，要求为 `5.00 GiB`。
- 修复：release 上下文中的所有 SSH 命令从第一条开始写节点本地 root-only `remote.raw.log`，同时保持 stdout allowlist 和 stderr 失败语义；使用稳定的独立日志根，避免预创建正式 release/Gate 目录。空间恢复只删除三个经过 inode/mtime/size 计划 SHA 绑定、无 release 引用且超过 7 天的 DR 临时目录，不触碰 daily、baseline、releases 或 verified 指针。
- 预防测试：四节点日志路径、目录 `0700`、文件 `0600`、root owner、单硬链接、命令 ID、stdout/stderr 双流、非 release 命令不落日志，以及备份机真实 smoke。
- 状态：代码、完整 release suite 与备份机日志 smoke 已通过，待新 full-SHA VM Gate 和生产停机发布验证。

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
- 状态：代码与完整 release suite 已通过，待新 full-SHA VM Gate 和下一次生产停机发布验证。

## 后台激活标记由 root 创建导致非 root 应用不可读

- 现象：停机候选容器和 `/health` 均正常，实例 Header 正确，但 `X-Sub2API-Background-Ready` 在写入激活标记后持续为 `false`，约 120 秒后进入协调恢复。
- 根因：镜像 entrypoint 会把 PID 1 降权为 `sub2api` 用户；发布脚本却在宿主机以 root 创建 `0600 root:root` 的 `.sub2api-active-instance`。非 root 应用无法读取标记，激活 watcher 只会继续轮询而不会改变 readiness。
- 证据：release `235-b21899c931cb-1786730845-4c28f2f0` 在 `candidate_headers_verified` 后失败；候选 HTTP 200，恢复后旧应用、Nginx、备份 timer 和 claim 均已对账。生产容器 PID 1 与 `/app/data` 均为 `1000:1000`，旧写法固定生成 root-owned `0600` 文件。
- 修复：共享 helper 明确拒绝 Docker userns/rootless，从目标容器 `/proc/1/status` 读取 PID 1 的 FSUID/FSGID，以该身份和 `0600`、单硬链接权限原子发布标记；首次切换、最终 Compose 接管和路由回滚全部复用。旧实例回滚必须先在 loopback 上确认实例 ID 与 `Background-Ready=true`，再恢复 Nginx 路由。Header 通过后的任意失败以及 Compose 启动失败也会在恢复前保存原始日志、失败行和结构化分类。
- 预防测试：验证 marker owner/mode/content、userns/rootless 拒绝、三条写入路径统一调用 helper、激活写入失败与 readiness 超时分类、Compose 启动及未处理的候选后启动错误在恢复前落 root-only 证据。
- 状态：代码与完整 release suite 已通过，待新 full-SHA VM Gate 和生产停机发布验证。

## Loopback 健康检查与生产绑定地址不一致

- 现象：候选进程保持 running、退出码为 0、无 OOM，应用日志明确显示已经监听 `127.0.0.1`，但 Docker 连续五次将容器判定为 unhealthy。
- 根因：生产候选显式绑定 IPv4 loopback，镜像 HEALTHCHECK 却访问 `localhost`；生产 Docker 环境中的 `localhost` 解析路径没有命中 IPv4 监听。
- 证据：candidate `sha256:6eb8dd24b84ec569171fc389e99d8e0b2b55e8450ae6faa51092f8a1de1de3d1` 在同一生产主机访问当前健康端点时，`localhost` 返回 exit 1，而 `127.0.0.1` 返回 exit 0；release `235-8f40f75ad81b-1786717077-274b236b` 随后完成协调恢复。
- 修复：Dockerfile HEALTHCHECK 固定使用 `127.0.0.1:${SERVER_PORT:-8080}`，与蓝绿和停机候选的 `SERVER_HOST=127.0.0.1` 合同一致。
- 预防测试：Dockerfile 合同测试拒绝恢复为 `localhost`；VM Gate 必须验证候选 Docker health、内部 `/health` 和实例 Header。
- 状态：修复中，需由新 full-SHA candidate 的 VM Gate 与生产停机发布验证。

## Compose 健康检查插值固定到旧槽位

- 现象：镜像 HEALTHCHECK 已改为 IPv4 且候选应用持续监听新槽位，但停机发布仍在 `candidate_started -> candidate_healthy` 失败；Docker health 连续失败，手工从同一镜像访问当前健康端点却成功。
- 根因：生产 Compose 自己定义了 healthcheck。Compose 在合并候选 override 前，先使用当前 `.env` 的 `SERVER_PORT` 展开基础 healthcheck，因此候选服务环境虽然是新槽位，容器 healthcheck 仍固定访问旧槽位。停机模式已停止旧应用后，该检查必然失败。
- 证据：release `235-a60e3895f1b1-1786720374-ab9a4857` 的候选监听 `18081` 达 184 秒且无 panic、fatal、OOM 或 bind 错误；镜像健康命令的 IPv4 对照通过，但生产 Compose 的解析结果仍指向旧槽位 `18080`。
- 修复：候选 override 显式写入绑定 `candidate_port` 的 IPv4 healthcheck；最终 active override 同样显式绑定最终槽位端口。Compose config 在启动容器前断言 healthcheck URL、服务监听端口和目标槽位完全一致。
- 预防测试：候选/最终 override 的端口绑定合同、Compose healthcheck 断言、候选失败时将 Docker health log 原样保存在生产机 root-only 日志。
- 状态：修复中，需通过完整 release suite、新 full-SHA Gate 和生产停机发布验证。

## 停机模式误复用蓝绿双槽状态机

- 现象：已选择简单停机更新，脚本仍创建相反端口的临时候选、切换 Nginx、等待旧槽排空、再回原端口重建；旧容器已经停止时，排空检查返回 `unknown`，导致候选健康也无法收口。
- 根因：`downtime` 只在前置阶段停止服务，后续仍复用了蓝绿的 candidate/expose/finalize 状态机；恢复判断又只检查当前 active 是否健康，没有核对运行镜像是否仍为 `pre-image-id`。
- 修复：停机模式固定使用原 `active_port` 和正式 `sub2api` 容器，迁移后直接 `compose up --force-recreate`，后台任务激活后只启动一次 Nginx；finalize 不排空、不二次切流。恢复判断同时核对运行镜像与 `pre-image-id`，清理器禁止删除正式容器。
- 预防测试：分别以 `18080/18081` 为 active 覆盖停机成功、迁移前后失败、健康失败、Nginx 启动失败和恢复；Shell/Python 阶段枚举必须一致。
- 状态：代码已修复，等待完整 VM Gate 和生产停机发布验证。

## Host 与 Bridge 混淆宿主槽位和容器端口

- 现象：Bridge Compose 的宿主端口为 `18080/18081`、容器内应用端口为 `8080`，但最终 override 或恢复脚本把宿主端口写入容器内 healthcheck，导致正确的 Bridge 配置在重建后变为 unhealthy。
- 根因：脚本在多个位置分别拼接监听环境、发布端口和 healthcheck，没有共享网络合同；预检还使用 `join | test` 子串判断，可能放行额外参数、`CMD-SHELL` 或错误 URL。
- 修复：新增共享 Compose/runtime 合同。Host 固定 `127.0.0.1:<slot>`，Bridge 固定容器内 `0.0.0.0:8080`、宿主 `127.0.0.1:<slot>` 和 healthcheck `127.0.0.1:8080`；完整命令数组精确比较，候选、finalize、resume-old 和 coordinated restore 全部复用。
- 预防测试：VM Gate 对 Host/Bridge 与两个槽位执行 JSON/runtime 合同，明确拒绝 `CMD-SHELL`、多余参数、错误路径和错误发布端口。
- 状态：代码已修复，等待完整 VM Gate 验证。

## 只读巡检硬编码默认活动端口

- 现象：正式应用和 Nginx 均健康，但只读巡检在活动槽为 `18081` 时仍固定请求 `18080`，产生内部健康假故障。
- 根因：发布、恢复和 doctor 已以 `/opt/sub2api/active-app` 为活动槽事实源，较早的独立只读巡检仍保留初始端口常量。
- 修复：只读巡检先验证 `active-app` 是普通文件且只有一个合法 `port` 字段，再仅允许 `18080/18081` 并请求对应 loopback 健康端点。
- 预防测试：拒绝硬编码 `127.0.0.1:18080/health`，验证动态端口命令、非法端口 fail-closed，并在脚本复查中统一搜索固定槽位 URL。
- 状态：已修复，等待下一次只读巡检与 VM Gate 验证。

## VM release unit 更新晚于 doctor

- 现象：仓库中的 `vm-validate` 更新后，`deploy-start` 在 VM doctor 阶段因 checksum 不一致停止；真正负责原子更新 validator 的 VM Gate 尚未开始，形成顺序死锁。
- 根因：编排先执行完整 local/VM doctor，随后才创建 VM Gate；`install_vm_validator` 位于 Gate 内部，无法在 doctor 前生效。
- 修复：先执行本地 Git、origin 和完整 SHA doctor，再使用已有 signer key 原子更新 validator、Gate signer、DR signer 同一版本单元；更新成功后才执行 VM、DMIT、备份机和生产 doctor。
- 预防测试：验证本地 doctor 失败时不写 VM；release unit 更新失败时不进入远端 doctor、Gate 或生产；成功路径严格保持 `local doctor -> release unit update -> VM/external doctor -> production bootstrap -> RackNerd doctor -> VM Gate`。
- 状态：已修复，等待新提交的实际 deploy-start 验证。

## VM fetch 只更新 FETCH_HEAD 未更新 origin/main

- 现象：VM 能通过 `ls-remote` 看到远端最新 `main`，但 Gate 在 `preflight` 立即退出；`/opt/sub2api-src` 的 `HEAD`、`origin/main` 和 `FETCH_HEAD` 仍停在上一次候选提交。
- 根因：validator 以 `origin/main` 作为发布事实源，但 VM 上该引用可能与远端发生非快进分叉；普通 fetch/refspec 会因拒绝更新而返回非零，原始 stderr 又不能混入结构化接口，最终只留下模糊的 `preflight` 失败。
- 证据：失败 release `237-de98543d0cb6-1786821227-4c0efd38` 的 `git ls-remote origin refs/heads/main` 已返回新 SHA，而跟踪引用仍为旧 SHA；受控分类结果为 `ref_update_rejected`。
- 修复：validator 仅对本地远端跟踪引用执行显式强制更新 `git fetch origin +main:refs/remotes/origin/main`，随后继续用完整 SHA 严格核对并 `reset --hard`；不修改远端分支、不放宽 origin、工作树、asset checksum 或 commit 合同。
- 预防测试：静态合同要求显式 refspec 和紧随其后的完整 SHA 断言；真实 VM Gate 必须证明源码工作树切到新 candidate 后才允许构建。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## VM migration-195 阶段过粗导致失败定位困难

- 现象：VM Gate 已完成候选构建和迁移，但失败阶段仍停留在 `migration_assertion_profile_195_fixture`，无法区分数据库 postflight、低水位拒绝还是 verified replay。
- 根因：多个 migration-195 断言连续执行，阶段 marker 只在整段开始处设置；任一中间断言失败都会丢失具体位置。
- 证据：release `237-548576818df5-1786821819-005f5ec3` 已生成 migration-195/232/233/234 状态文件，但没有进入后续 old-image 阶段，说明失败发生在 migration-195 后置断言区间。
- 修复：在 postflight DB、low-watermark 和 verified replay 前增加独立 stage marker，同时由 ERR trap 把实际 shell 行号写入 root-only `failure-line`；沿用现有 failure-category 映射，不放宽任何迁移或恢复断言。
- 预防测试：静态检查要求三个 marker、ERR trap 行号传递和 root-only `failure-line` 存在；Gate 失败时只需读取 `stage`、`failure-category` 和行号即可定位，无需回传原始日志。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## 新严格合同误要求旧生产实例预先满足

- 现象：新 Candidate 已通过 VM Gate，但生产在停机前的 preflight 退出；旧应用、Nginx、备份、空间和 migration 均正常。
- 根因：新版本将 healthcheck 固定为完整 IPv4 参数数组，preflight 却使用同一严格合同验证仍在运行的旧容器。旧容器使用精确的历史 `CMD wget -q --spider <IPv4 slot URL>`，因此在有机会升级前被拒绝；迁移前恢复脚本也可能重新加载该历史 Compose 后被严格断言阻断。
- 修复：仅 production preflight 的旧活动实例允许两种精确数组，禁止子串和任意参数；Candidate、最终实例及恢复后的 Compose 继续只允许新严格数组。`resume-old` 启动前主动生成规范化 active override。preflight 失败另存 root-only 阶段和行号，并进入结构化结果。
- 预防测试：Host/Bridge、两个槽位分别验证严格数组和历史数组；历史数组在默认严格模式必须失败，仅 `active_compat` 可通过；迁移前恢复必须先规范化再启动。
- 状态：代码已修复，等待新 full-SHA VM Gate 和生产停机发布验证。

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
