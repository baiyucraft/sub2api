# 发布链已知踩坑与预防

本文只记录已在本 fork 发布链中复现或确认过的问题。每条记录都包含现象、根因、证据、修复、预防测试和状态；不写入凭据、完整环境、DSN 或原始远端日志。

## 近期发布复盘：先锁定身份，再判断状态

以下规则来自近期 `main` 合并、profile-242、VM 8211 和发布链复盘；它们是操作顺序约束，不是可选建议。

- **完整 SHA 只能现场生成**：发布前使用 `git rev-parse --verify HEAD^{commit}`，把同一个 40 位小写 SHA 传给 doctor、VM Gate、candidate、归档和最终验真。不要手工拼接、拆分参数、使用短 SHA、`latest`、`main` 或旧 Gate 记录。SHA 参数被拆开时，`release.py` 可能把后半段当成未知参数，不能据此判断发布逻辑本身失败。
- **静默不等于失败**：`deploy-start`、VM validator 或 SSH 观察器长时间没有输出时，先读取 `runner.json`、`state.json`、结构化事件、进程和 committed marker。只要 runner 仍在运行，就只能 `follow`/`status`/`wait`；禁止重新启动第二个 runner。调用端超时也不代表远端 runner 已退出。
- **VM Gate 与生产发布必须分开报告**：VM 的 `verified` 只证明当前 full-SHA candidate 在隔离的 `sub2api-dev`、本地 PostgreSQL/Redis 和 `data-dev` 上通过；没有生产 `verify-result`、生产切换和 post-deploy doctor，就只能报告“VM 通过、生产未变更”。没有真实业务流量时，streaming、usage attribution、模型能力统一记为 `not_checked`，不发送真实模型请求。
- **上游合并后重新生成版本身份**：官方 `VERSION` 变化时，fork 必须变为“官方版本 + `-baiyu`”，例如 `0.1.183-baiyu`；不能继续沿用旧 profile 版本，也不能重复追加后缀。版本、profile、migration catalog、candidate 和 Gate 必须作为同一 release 身份重新生成，不能复用旧 candidate 或旧 checksum。
- **Docker 125 先查镜像，不要追下游 JSON 错误**：VM 上运行容器引用的 image 不在本地 image store 时，后续 `docker run` 会以 125 失败，planner 输出为空，之后的 `jq`/JSON 解析错误只是次生症状。启动 Gate 前先逐一 `docker image inspect` 所需 current/compatibility image ID；若正在运行的 `sub2api-dev` 缺镜像，只能从匹配的历史 Gate 归档核对签名和 `candidate.tar.gz` SHA-256 后 `docker load`，再确认容器引用的 image ID 和 health，不得重启或盲目重跑。
- **迁移证据以数据库为准**：`schema_migrations(filename, checksum, applied_at)` 是当前迁移事实源。checksum 相同才可跳过，冲突立即停止；`absent`、`verified`、`unknown` 必须分开，部分 schema 不能伪装成已验证。历史 profile/Gate 证据只用于审计和恢复，不能驱动新 release 的 pending 分支；候选 catalog 缺少数据库中已存在的迁移时，先解决 catalog policy，不得压掉 `unknown`。
- **未跟踪的 Wiki/Spec/Skill 资产不是失败理由，也不能偷偷带入发布**：doctor 要求 clean worktree 时，先区分用户资产与本次代码。`.wiki/`、`.spec/`、`.agents/skills/wiki-*` 等未跟踪路径若不属于本次 release，可临时移到 worktree 外并原样恢复；禁止删除、加入提交或用忽略规则掩盖。恢复后重新核对 `git status`。
- **8211 只保留一个持久实例**：VM 展示固定使用 `sub2api-dev` 和 `8211`；不创建或保留 `sub2api-preview-*`、`18211`、`18220`、临时代理或第二套页面服务。candidate 切换必须先核对 image ID、`/health`、页面/API，再清理临时容器；release 目录清理只能使用有边界的版本化 cleaner，保留 active、candidate、旧镜像和回滚证据，禁止宽泛删除。
- **失败先 reconciliation，再决定是否重试**：SSH 超时、远端命令非零、迁移失败、健康检查失败或输出解析异常，都先核对 committed marker、migration 记录、active claim、容器/image、PG/Redis、Nginx 和备份状态。不能因为“可能没提交”就再次 deploy，也不能删除 `.active-release`、`.consumed`、`.recovered` 后重跑。
- **健康不是模型能力证明**：容器 health、应用 `/health`、Nginx/DMIT 路由和登录/API smoke 只能证明服务链路启动；不要把它们写成模型可用、账号可用或 upstream 探针通过。

预防测试至少覆盖：SHA 单值传递、静默 runner 重连、VM/生产状态分离、缺失 image 的 fail-closed、migration 状态三分法、未跟踪资产恢复、8211 单实例收口和重复 runner 拒绝。

## Windows Shell 与 Linux 能力边界

- 现象：在 Windows 直接运行 `.sh` 时出现路径、引号或命令不可用错误。
- 根因：PowerShell 与 Git Bash 的参数、路径和初始化语义不同；本机没有 `jq`、`docker`、`systemctl`、`flock` 等 Linux/容器能力。
- 证据：Git Bash `4.4.23(2)-release` 可执行；能力检测将缺少的 Linux 命令标记为 `vm_required`。
- 修复：统一使用 `scripts/windows/run-bash.ps1`，固定 `--noprofile --norc`、参数数组和 `cygpath`；Linux 服务、容器、权限、锁和 jq Gate 交给 VM。
- 预防测试：Git Bash 定位、空格与 `!` 路径、stdout/stderr、exit code、`bash -n` 以及缺少命令时的 `vm_required`。
- 状态：已修复。

## Windows 发布控制台闪烁与英文进度

- 现象：部署期间 Windows 控制台反复闪开、关闭，用户只能看到英文阶段字段；调用端多次执行 `status`/`wait` 后更明显。
- 根因：旧 runner 同时设置 `DETACHED_PROCESS` 与 `CREATE_NO_WINDOW`；runner 内部的 Python、OpenSSL、Git Bash、Go 子进程又各自直接创建进程；`wait` 只在结束或超时输出 JSON，没有持续的人类进度层。
- 证据：`supervisor.py` 的 worker flags 包含三个 Windows process flags；`cli.py`、Gate 验签、Git 读取、生产清理和 DR verifier 构建均存在直接子进程调用。
- 修复：所有发布运行子进程统一经 `scripts/release/process.py`，Windows 使用 `CREATE_NO_WINDOW`、隐藏 `STARTUPINFO` 和单独进程组；增加 `deploy-follow`/`follow` 单控制台中文观察器。机器协议仍保持英文，观察器不读取原始日志。
- 预防测试：Windows flags 不包含 `DETACHED_PROCESS`；child Python/OpenSSL/Git Bash 使用 no-window；观察器 Ctrl+C 不 kill runner、重连不创建第二 runner；历史 JSON 命令输出协议不漂移。
- 状态：已修复，等待下一次真实 Windows 发布观察。

## Windows Skill 校验的 UTF-8 环境

- 现象：Skill 内容本身正确，但直接运行 `quick_validate.py` 时出现 GBK 解码错误。
- 根因：Windows Python 按本地代码页读取中文 `SKILL.md`，而校验脚本未显式指定 UTF-8。
- 修复：在 Windows 运行 Skill 校验前设置 `PYTHONUTF8=1`，再执行 `python .../quick_validate.py <skill>`；不修改 Skill 内容来绕过编码问题。
- 预防测试：将 UTF-8 环境变量纳入 Skill validation 命令，并把原始解码异常与真正的 frontmatter 校验失败区分开。

## 单 runner 与观察器重连

- 现象：前台工具超时或窗口关闭后误以为发布失败，重新启动第二个 release。
- 根因：调用端生命周期短于 runner，控制台沉默不代表远端阶段没有推进。
- 证据：`.tmp/releases/<release_id>/runner.json`、`state.json`、结构化事件和 committed marker 才是发布事实源。
- 修复：默认用 `deploy-follow` 启动并观察；断线后只允许 `follow <release_id>`、`status` 或 reconciliation，禁止重复启动。
- 预防测试：wait timeout 不终止 worker、观察器断线后 attach 原 release、active claim/runner/candidate 唯一性和失败后 fail-closed。
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

## 新增 migration 的 preflight 错把 absent 当 verified

- 现象：profile 237 在候选迁移前执行 `migration-235-assert.sh` 时失败，实际数据库还没有 235/236 的表和列。
- 根因：新增迁移断言只检查目标 schema 已存在，未按 `MIGRATION_STATUS=absent` 与 `verified` 区分 preflight 合同；因此合法的“待执行”状态被误判为失败。
- 证据：release `237-9e20917feeab-1786823069-231ba3c2` 的 failure line 为 validator 第 583 行，对应 migration 235 preflight；前序 migration 195/232/233/234 已完成，候选 migration-only 尚未开始。
- 修复：235/236 preflight 在 `absent` 时要求目标对象数量为 0，在 `verified` 或 postflight 时要求完整 schema；部分存在仍 fail-closed。
- 预防测试：release core 静态合同覆盖 absent 分支和零对象断言；VM Gate 覆盖 absent→postflight verified 及部分 schema 拒绝。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## VM 候选迁移失败日志被清理

- 现象：`--migrate-only` 返回非零时只能看到 `migration_syntax` 等分类，无法确认具体迁移文件或数据库错误。
- 根因：失败 trap 为复用成功路径，分类后无条件删除 `migrate-candidate.log`；远端结构化 stdout 又只保留 allowlist 字段。
- 证据：release `237-d45e852cd3a7-1786823504-aa88a5ea` 的 failure line 为 validator 第 586 行，分类为 `migration_syntax`，但失败收口后候选迁移日志不存在。
- 修复：失败路径保留 `migrate-candidate.log` 为 root-only `0400`；成功路径仍在 post-migrate 检查后删除，避免成功 release 无限积累。
- 预防测试：静态检查失败 trap 对候选迁移日志执行权限收紧而非删除；日志查询只允许返回脱敏迁移编号和固定错误类别。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## Windows Shell 提示文本污染版本化源码

- 现象：migration 235 在 PostgreSQL 中报告 `syntax error`，错误 token 来自应用或 shell 文本，而不是迁移 SQL 关键字。
- 根因：早期在 PowerShell/Starship 环境中捕获命令输出并写文件时，把终端初始化错误一并写入了两个 SQL migration 和一个 Vue 文件的开头。
- 证据：release `237-f225e290e54b-1786823832-3da9d197` 明确命中 migration 235；版本化文件首字节为固定 Starship `[ERROR]` 文本，仓库扫描共发现 3 个污染文件。
- 修复：删除三个文件的固定污染前缀；不改 migration 235/236 的 SQL 语义。生成或编辑文件继续只用 `apply_patch` 或版本化脚本，禁止把带 shell 初始化输出的 stdout 直接重定向到源码。
- 预防测试：release core 扫描 `backend/migrations` 与 `frontend/src` 的版本化源码，发现固定 Starship 噪声立即失败；VM Gate 必须重新执行 migration 235/236。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## VM Gate 执行了新迁移 preflight 但未签入 evidence

- 现象：VM 端候选构建、迁移和运行验证全部完成，下载后的 Gate 却因缺少 migration 235 preflight evidence 被本地验签器拒绝。
- 根因：validator 调用了 migration 235/236 preflight，但只记录 status 与 postflight schema flags，遗漏两个 preflight 布尔字段；Gate verifier 已按 profile 237 合同要求这些字段。
- 证据：release `237-182e8414dacb-1786824449-7562443d` 在远端完成并生成 Gate 后，本地 `verify_gate` 报告缺少 migration 235 preflight evidence。
- 修复：初始化、成功置位、jq 参数和签名 evidence 同时加入 `migration_235_preflight_verified` 与 `migration_236_preflight_verified`。
- 预防测试：release core 检查两个字段的置位和 Gate JSON 映射；profile 237 Gate verifier 继续要求 status、preflight、schema 三类证据齐全。
- 状态：代码已修复，等待新 full-SHA VM Gate 验证。

## 生产 migration-233 preflight allowlist 漏掉成功字段

- 现象：生产 schema、索引、权限和触发器只读检查全部通过，但 release 在 `migration_233_preflight` 被判失败并进入恢复。
- 根因：`migration-233-assert.sh` 在已执行状态会返回 7 个结构化 schema evidence 字段；生产 `migration_preflight()` 的 SSH allowlist 只声明了 duplicate/table/preflight 三个字段，导致成功命令被当作 undeclared field 失败。
- 证据：release `237-32413cd11f05-1786825173-a7587bed` 的生产 migration 233/234/235/236 checksum 与 profile 237 一致，精确 SQL 断言均为通过；失败发生在停机前的 preflight，`migration_started=false`。
- 修复：生产 preflight allowlist 与 migration-233 assert 的完整成功输出保持一致；不放宽 stderr、退出码或迁移语义。
- 预防测试：production release 测试覆盖 8 个 migration-233 preflight 字段；后续未声明字段仍 fail-closed。
- 状态：代码已修复，等待新 full-SHA VM Gate 和生产停机发布验证。

## 生产 migration-234 preflight allowlist 漏掉 schema_verified

- 现象：新 release 的 migration 233 preflight 已通过，migration 234 preflight 随后失败并触发恢复；生产没有开始 migration。
- 根因：migration-234 assert 在 `verified` 状态输出 `migration_234_schema_verified=true`，生产 preflight allowlist 只有 schema_state 和 preflight 两项。
- 证据：release `237-6965621e0962-1786827315-76e0e117` 的 production-result 显示 migration_233_preflight_verified 成功，失败紧接着发生在 migration_234_preflight。
- 修复：将 schema_verified 纳入 migration-234 preflight allowlist；仍保持未声明字段 fail-closed。
- 预防测试：production release 测试覆盖 migration-234 三个 preflight 输出字段。
- 状态：代码已修复，等待新 full-SHA VM Gate 和生产停机发布验证。

## 停机发布恢复备份单元的 benign stderr 被判失败

- 历史 profile 现象：候选切换、迁移、当时的双链路 canary 和 downtime finalize 全部通过，随后 release 进入恢复；生产旧镜像最终健康，但 runner 报 exit 97。Gate v2（历史 profile 242-243、当前 profile 244）不执行 Canary。
- 根因：`restore-backup-units.sh` 返回结构化成功 stdout 的同时写出 benign stderr；SSH runner 的通用合同把任意非空 stderr 统一视为失败。
- 证据：release `237-8691759b2452-1786828578-37913f43` 的 history 已到 `downtime_finalized`，raw log 随后出现 `backup_units_restored=true` 和 `exit=97`。
- 修复：生产脚本通过共享 helper 将该命令 stderr 追加到 release root-only `restore-backup-units.stderr`，stdout 只返回 `backup_units_restored=true`；命令真正非零仍 fail-closed。
- 预防测试：production release 测试检查 stderr 捕获路径、结构化字段和不扩展 allowlist；恢复与正常收口都复用同一 helper。
- 状态：代码已修复，等待新 full-SHA VM Gate 和生产停机发布验证。

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

## VM 长构建绑定单个 SSH channel

- 现象：VM Gate 在 `candidate_build` 长时间运行时 SSH 被 reset，本地验证进程退出，但远端 Docker build 继续持有 release lock；直接重跑会形成状态不明或重复构建风险。
- 根因：validator 的完整生命周期由一次前台 SSH command 承载，调用端连接同时承担远端进程存活、stdout/stderr 传输和最终结构化结果返回。
- 证据：profile 239 release `239-a68eeab2790a-1786939986-9a81fc13` 的本地 SSH 已中断，远端阶段仍停留在 `candidate_build` 且构建进程继续存在；确认 30 分钟无推进后才按 manifest、PID、父子关系终止并由 trap 写入失败分类。
- 修复：VM validator 改为 `nohup + setsid` 独立 worker；worker 使用 boot ID、PID 与 `/proc` start token 建立进程身份，原始 stdout/stderr 仅写入 VM root-only 日志，本地只重连轮询结构化状态。启动 SSH 状态不明时不重复启动；只有连续证明握手不存在才清理 input，worker 运行或状态未知时保留 input 与证据。
- 补充：首版 detached wrapper 的 `state_write` 把 `key` 和依赖 `$key` 的 `tmp` 放在同一条 `local` 声明中，在 `set -u` 下会于状态握手前触发 `unbound variable`。局部变量必须分两步声明；launcher 先写 `launching`，并把 worker 的早期 stdout/stderr 直接追加到 root-only raw log，禁止重定向到 `/dev/null`。
- 预防测试：覆盖启动后首次 SSH reset、随后 running、最终 exited；PID token 丢失 fail-closed；非零退出、Gate 文件缺失、日志权限异常；状态未知不清理 input；worker 终态后才允许清理。
- 状态：代码已修复，等待新 full-SHA profile 239 VM Gate 验证。

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

## 上传前空间只贴着 5 GiB

- 现象：备份机 doctor 在发布开始前刚好通过，但生成发布恢复点后，三次上传都返回 `ERROR: less than 5 GiB free`，发布在 migration 前自动恢复。
- 根因：doctor 只验证当时可用空间达到 5 GiB；receiver 在新加密包落盘后再次要求仍保留 5 GiB。若发布前只高出几十 KiB，任何真实上传都会失败。
- 证据：失败 release 的 root-only 原始日志显示三个 upload attempt 均命中同一容量错误；备份目录已只剩最新 2 组 daily，而 `/var/log/journal` 占用约 3.9 GiB。
- 修复：保留 daily retention 的两组下限；新增版本化 `backup-host-space-clean.sh`，仅把 systemd journal 压到 1 GiB，并要求清理后至少保留 `5 GiB + 512 MiB` 上传余量。
- 预防测试：脚本必须 dry-run、绑定 `plan_sha256`、获取三把锁、拒绝 symlink/跨设备，不得包含 `rm -rf` 或遍历备份根；生产 doctor 通过后仍需验证上传余量。
- 补充：`journalctl --vacuum-size` 会把正常清理进度写到 stderr；必须把 stdout/stderr 追加到 root-only 原始日志，不能让通用 SSH 严格合同把已完成清理误判为失败并重跑。
- 状态：真实清理已完成并通过现场空间对账；等待修正日志捕获后的新 release 验证。

## migration 容器完成后的断言失败缺少结构化定位

- 现象：停机发布已完成 migrate-only，但随后只记录 `switch_failure_stage=migration_completed`、`switch_failure_substage=unknown`，无法区分 migration checksum 与 schema 合同断言失败。
- 根因：`switch.sh` 在 migration checksum 和全部 schema 断言之后才安装 `ERR` trap，且失败文件把 stage 硬编码为 `schema_verified`；断言提前退出时不会生成有效的结构化失败文件。
- 证据：profile 239 release `239-97edc418a80f-1786965869-6c2f2fdb` 在 `migration_completed` 后退出，协调恢复后确认 migration 未提交、旧镜像和双链路已恢复，root-only 原始日志仅允许做受控布尔检索。
- 修复：migration 容器成功返回后立即安装 trap；动态读取 `migration_completed` 或 `schema_verified`，并绑定 checksum、schema assertion、stage marker、migration container、migration marker、migration 195 postflight 六类固定 failure code。
- 预防测试：静态测试要求 trap 早于 migration checksum loop，失败文件固定 4 个 root-only 字段，Python parser 校验 stage、substage 与 failure code 的合法组合；任何未知组合继续 fail-closed。
- 状态：代码已修复，等待新 full-SHA Gate 和生产停机发布验证。

## 已执行 migration 232 后视频价格再次漂移

- 现象：profile 239 的 migrate-only 已完成，但 `migration-232-assert.sh postflight` 阻断切换；协调恢复后旧生产恢复健康。
- 根因：migration 232 只做一次性清理，没有数据库约束。后续管理写入重新给非 Grok/Composite 分组设置视频价格；migration 232 已记录为 verified，不会重跑 UPDATE，postflight 因 live remaining 非零而正确失败。
- 证据：release `239-618e91e8cc3a-1786969364-d4788e0c` 的结构化失败为 `schema_contract_assertion`、line 234；恢复后只读查询显示 migration 232 backup 表存在且 0 行，live remaining 为 1。
- 修复：pending profile 239 追加 migration 239，先写专用备份表，再清理漂移行并验证约束；preflight 记录受影响计数和规范化 hash，bind 绑定协调恢复点，postflight 核对备份、受保护平台、零残留和约束。
- 预防测试：VM probe DB 主动注入一条非 Grok 视频价格，要求 migration 239 清理并使数据库约束拒绝再次漂移；生产失败仍先协调恢复，不手工改写 migration 232 历史证据。
- 状态：代码已修复，等待新的 full-SHA VM Gate 和生产停机发布验证。

## 多个数据迁移在 VM Gate 中绑定了不同恢复点 checksum

- 现象：migration 239 已成功执行并清理夹具数据，但继承的 migration 232 postflight 在 bound checksum 校验处失败，Gate 分类仍显示 `migration_assertion_profile_232_channel_monitor_media`。
- 根因：VM Gate 先为 migration 232 写入并绑定 `recovery-point.age.sha256`，随后 migration 239 夹具又覆盖同一文件；migration 232 postflight 因恢复点 checksum 改变而正确阻断。
- 证据：失败 release `239-00ae1c06b3ca-1786975258-d402f23d` 的生产阶段为 `not_started`，VM failure line 为 1024；migration 232 状态 `verified/affected=0`，migration 239 状态 `absent/affected=1`，失败发生在共享恢复点被第二次写入后。
- 修复：profile 239 的 VM 夹具只验证既有 recovery checksum 文件并复用它完成 migration 239 bind；232 与 239 的数据计划绑定同一个协调恢复点，符合生产单 release 恢复合同。
- 预防测试：release core 测试截取 migration 239 夹具，要求存在 recovery 文件安全断言，并禁止再次 `printf` 覆盖；任何需要独立恢复点的迁移必须使用独立 state 文件和显式合同，不能复用共享文件名。
- 状态：代码已修复，等待新的完整 SHA Gate 验证。

## `release.py logs --node vm` 使用展示名连接 SSH

- 现象：VM Gate 失败后，`release.py logs <release_id> --node vm` 返回 `event log unavailable: KeyError`，只能人工投影本地事件和 VM failure marker。
- 根因：CLI 对外节点名是 `vm`，`.ssh.local` 与 `SSHRunner` 的连接键是 `local_vm`；日志查询把展示名直接交给连接器。
- 证据：同一失败 release 的 VM Gate 目录和 root-only 日志存在，直接使用 `local_vm` 可读取固定字段，但 `logs --node vm` 在连接配置索引前抛出 KeyError。
- 修复：日志查询保留输出标签 `vm`，连接时显式映射为 `local_vm`；RackNerd、DMIT 和 backup 名称不变。
- 预防测试：使用内存中的 VM JSONL 事件验证查询结果仍标记为 `vm`，并断言 SSH 调用使用 `local_vm`。
- 状态：代码已修复，等待随新发布资产验证。

## Profile 240 发布链补充约束

- migration preflight 只能使用冻结的最小上下文，不能继承完整 active-claim 环境；migration 事务统一由 migration runner 管理，断言脚本不能再嵌套拥有事务。
- profile、逐 migration 状态、Gate evidence 和签名 payload 必须成套生成和验证；签名输入缺失、初始化失败或发布中途修复后都必须生成新的完整 SHA、Gate、candidate 和 release ID。
- 精确倍率迁移必须允许 `sync_managed + key_missing` 的已归档绑定参与可证明重算；历史 usage 不回算。非 Grok/Composite 分组提交的空视频价格对象必须在 service 层归一化为空值，不能再次制造 migration 232 漂移。
- direct `release.py` 偶发 `WinError 10013` 目前只记录为待复现问题；不能把 `python -c import release.cli` 成功当作长期修复，脚本入口和模块入口都必须保留 Windows smoke。
- one-shot 备份 service 处于 `inactive` 不等于失败，doctor 必须结合 timer 是否启用、最近执行结果以及当前是否运行进行判定。
- 状态：规则已沉淀；涉及生产的条目仍以每次新 release 的结构化证据为准。

## Profile 242 health-only 合同与探针误用

- 现象：生产 Docker 升级被“账号池不可用”、Canary 凭据缺失或 upstream/model 探针失败阻塞；或者 Gate 通过了 `/health` 却被误报为模型能力已验证。
- 根因：历史 profile 的发布链曾把 API key、Canary、模型请求和能力探针混在候选健康或生产切换流程中；`probe_*` 命名又容易把隔离容器检查误解为 upstream 探针。
- 证据：profile 242 Gate v2 只允许容器 health、应用 `/health` 200、迁移/备份/恢复/镜像与数据库/Redis 隔离合同；Gate 的 `release_policy.canary_verified` 固定为 `not_checked`，生产 `streaming`、`usage_attribution` 和真实客户端 IP 按合同记录为 `not_checked`。现有回归测试禁止 `/api/v1/`、`/v1/`、`Authorization: Bearer`、Canary request、Canary key 和 profile 242 的 `canary_api_key_id`。
- 修复：升级路径不读取账号池或 Canary/API key，不发送模型/upstream 请求，不生成 usage attribution；direct/DMIT `/health` 只证明容器启动和路由可达。隔离数据库、Redis、候选容器和旧镜像检查可以继续使用 `probe_*` 变量，但必须保持本地隔离语义并在 Gate 结束清理。
- 预防测试：每次修改 Gate、production、doctor、profiles 或 release references 后，搜索上述凭据、路径和请求符号，并运行 profile 242 定向测试与完整 release suite；没有可用账号时仍不得阻塞 Docker 镜像升级。
- 状态：已修复并由 profile 242 VM Gate、停机生产 release、`verify-result` 和 post-deploy `doctor` 验证。

## 修复后只重跑局部阶段导致证据漂移

- 现象：修复了 validator、migration planner、Gate 或生产脚本后，继续复用旧 Gate/candidate/release；局部测试通过，但签名资产、snapshot checksum、运行镜像或远端 validator 仍对应旧版本。
- 根因：把一次失败当作单点代码问题，忽略发布链的 commit、origin、release asset、validator/signer、Gate、candidate archive、production snapshot、claim 和恢复证据是同一个不可变身份合同。
- 修复：每次修复先完成同类关联面审计，再生成新的完整 40 位 commit；使用同一 SHA 更新 validator/signer 单元，重新跑完整 Gate，生成新的 candidate archive/image 和唯一 release ID。旧 release 只保留为审计/恢复证据，不重签、不覆盖、不复用。
- 预防测试：检查完整 SHA 贯穿 manifest/Gate/runner/production-result；验证 candidate image 与线上运行 image 一致；失败、超时、SSH reset 或输出解析错误都先做结构化 reconciliation，禁止并发或重复 `deploy`。
- 状态：已固化为 skill 规则，并由 profile 242 release 的 `status`、`verify-result`、完整测试和 `doctor` 复核。

## 逐点试错修复造成重复 runner

- 现象：第一次修复只覆盖当前报错，随后每个新失败再追加一小段补丁并重复启动 runner；最终出现多个候选、旧 Gate 与新代码混用，问题定位和恢复边界都变得不可靠。
- 根因：没有在启动 Gate 前完成同类符号、旧 profile 分支、测试、配置和恢复路径的全量关联面审计，把同一根因拆成多轮局部试错。
- 修复：发现问题后先冻结当前候选，建立关联面清单并一次性合并同根因修复；完成静态审计、定向测试和完整 release suite 后，重新生成完整 SHA、Gate、candidate archive/image 和唯一 release。旧 release 不追加修复，只保留作审计或恢复证据。
- 预防测试：skill 契约必须要求“启动 runner 前完成关联面清单”和“一次性修复批次”；失败、超时、SSH reset 或解析错误只能触发 reconciliation，不能触发第二个 `deploy`。
- 状态：已固化为强制门禁。

## Profile 242 最近发布：观察、验真与清理边界

- **现象**：`deploy-start` 前台暂时无输出、未立即返回 release ID，或早期 `status` 显示 `claim_final_state=unknown`、`candidate_image_id=null`、生产尚未开始；随后有人误判失败并准备再次启动 runner。
- **根因**：发布 runner 独立于调用端运行，状态文件按阶段逐步落盘；前台工具超时、观察器断开和早期字段为空都不是终态。
- **修复**：只跟踪同一 release 的 `runner.json`、`state.json`、结构化事件、进程和 committed marker；使用 `status`/`wait`/`follow` 重连，禁止第二个 runner、第二个 claim 或第二个 candidate。
- **预防测试**：覆盖启动握手、长时间无输出、观察器超时、SSH 断开后重连、早期字段为空和最终退出；验证观察器关闭不会终止 worker。

- **生产成功判定必须成套**：不能只看 VM Gate、容器 `/health=200`、`production-result` 或 `verify-result` 任一单项。至少同时核对签名 Gate、`production-result.json` 为 `production_verified`（或 `production_verified_after_reconciliation`）且顶层 `status=verified`、`verify-result=verified`、运行 image 等于 candidate image，以及 post-deploy doctor 通过。
- **报告分层**：VM Gate、生产切换/`verify-result`、post-deploy doctor 和业务能力验证必须分开报告；`/health` 只证明容器启动与路由可达，不代表模型、账号或流式能力已验证。Gate v2 profile 242-243（历史）与 244（当前）均为 health-only，账号池、Canary、Bearer/API key、真实 upstream/model 请求和 usage attribution 均保持 `not_checked`。
- **空间清理边界**：空间不足时先 dry-run，再使用同一计划 checksum apply；只运行版本化 cleaner，最多一次，并保留 candidate、旧运行 image、Gate、恢复点和失败 release 证据。禁止 `docker system prune`、删卷、删 PostgreSQL/Redis、`data-dev` 或备份；BuildKit GC 必须同时具备明确的 `max-used-space` 与 `reserved-space`。
- **输出与身份安全**：doctor/status/follow 只输出白名单字段和脱敏摘要，不回显完整 snapshot、环境变量、原始日志、请求头或凭据。PowerShell 发布参数不要依赖易被解释的 `HEAD^{commit}` 文本，必须由现场 `git rev-parse` 生成并贯穿全流程的完整 40 位小写 SHA。
- **状态**：已由 release `242-a15fefb7a6a5-1787786100-53f9251e` 的 VM Gate、`verify-result` 和 post-deploy doctor 复核；真实模型/Provider 流式能力仍按合同记为 `not_checked`。
