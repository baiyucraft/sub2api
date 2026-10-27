# 发布 Runner 生命周期

生产发布必须由独立 runner 持续执行，调用端只负责启动、观察和验真。宿主工具的超时、断开 stdout 或会话关闭都不能终止 runner。

## Windows 状态读取与原子发布

`runner.json`、应用和插件 state、可变 manifest、production result、VM lifecycle/owner 及其正式哈希、字节快照使用同一共享删除读取合同。读取旧句柄可以继续得到旧的完整文件，新打开的句柄得到新的完整文件；不得混合两个路径读取结果或用部分 JSON 更新状态。路径、链接、大小、schema、owner 和代际检查继续执行。

Windows 共享读取必须与原生安全替换配套；单独调整轮询间隔或只增加共享删除标志不能证明替换可用。已有文件替换须保留独占旧备份并核对提交结果，首次创建不得覆盖竞争出现的文件。正常观察不能阻塞自身发布写入；外部不兼容句柄、ACL 或只读拒绝仍应报错。

失败可能发生在提交前、提交后校验或清理阶段，报告必须明确区分。Windows 替换错误可能留下中间文件状态，不能沿用无条件删除临时文件的清理方式，也不能把非零退出解释为“目标未变化”。保留可用的临时文件、原目标和备份，禁止通用重试、强制权限修复或重新启动 release。后续检查从实际文件和结构化状态重建事实。

新 runner 使用独占发布的成功见证绑定 release ID 和 terminal runner 原字节 SHA-256；完成状态提交、必需日志和资源收尾后才可发布。status、wait 和 verify-result 必须一致校验，缺失、损坏或字节不匹配不能判成功。这样即使 verified JSON 已替换而备份校验或清理失败，也不会被观察端误认为完成。历史 schema 保持原合同，不回填旧文件。

迁移前置检查失败不属于构建失败。`reconcile-vm-preserve --failed-stage migration-preflight` 仅收口已独审冻结的 profile 273/迁移 298 preflight 事故：精确源身份、失败与迁移计划、无 apply/后续产物、probe 清理、同 boot/owner 和生产尚未开始均证明后，双重复验再释放本地租约。原 failed 记录不改，不恢复数据、不关闭 VM、不复用失败 release；未知阶段或源字节继续阻断。详细条件见 [release-doctor-and-recovery.md](release-doctor-and-recovery.md#vm-电源与-cleanup-恢复)。

另一个精确冻结的 `273-1396fc89fe73-1792295582-80609bce` 第 681 行计划比较事故由独立 `reconcile-vm-failed-preserve` 收口：仅此事故允许本 release 启动的 retained VM 显式保留运行，原四文件及远端 Gate 事故证据不改。当前已提交推送 helper、原 worker 退出、同 boot/owner、双远端锁、probe 已清理及生产尚未开始均重复证明后，先写独立 checks/result，最后释放 shared owner。`verify-vm-failed-preserve` 只读重验；原 `reconcile-vm-preserve` 不扩宽。下一次发布使用新 commit、new release ID、新 candidate 和新签名 Gate；修改 validator/helper 后旧 manifest/Gate 资产 checksum 不再代表新字节，不得复用失败发布或覆写其事故目录。

## 已消费发布的后置完成

生产已 `production_verified` 且 claim 已 consumed、原 worker 仅后置检查失败退出时，使用正式 `finalize-consumed <release_id>` 入口。它属于独立 `ops-control-assets` 收口，不重发应用、不重新消费 claim、不恢复数据；原 runner 非零退出及原 manifest、Gate、production result、VM 生命周期事故记录保持原样。

入口独占发布全局锁与同 VM 身份锁，独立核对原 PID/进程启动 token 已退出，重新验签原应用 Gate，要求原 production result verified、无 active claim、远端 consumed 且运行镜像等于 signed candidate。全部节点 doctor 必须通过：本地检查绑定当前已提交且推送的 helper 完整 SHA，远端检查绑定原应用完整 SHA。两种提交分别登记，旧 Gate 仅证明原应用。

`finalization-checks.json` 保存白名单后置检查、原事故文件 checksum、当前 helper 完整 SHA/资产 checksum 和 VM 收口的证据绑定；`finalization-result.json` 独立保存完成结果。默认 `finalize-consumed <release_id>` 保持 schema 1 的关机合同：仅接受原本停止、确由本 release 启动的受管 VM，retained 所有权精确匹配 release、应用 commit、VM 身份、原 worker 和同 boot。清理限本 release 的隔离任务，随后 SSH 正常 poweroff 并用 vmrun list 核实停止。

原本运行的 VM 必须显式使用 `finalize-consumed <release_id> --preserve-running-vm`。该分支的独立 `finalization-checks.json`、`finalization-result.json`、`vm-finalization.json` 均使用 schema 2、`mode=preserve-running`；旧关机分支三份记录保持 schema 1 且无 mode。保留分支只接受原生命周期 `initially_running=true`、`vm_started_by_release=false`、`vm_cleanup_status=retained_failure` 且 `lease_status=retained`。原失败 runner、manifest、signed Gate、production result 和生命周期文件字节不改；独立记录绑定这些原始 SHA256、应用与当前已提交推送 helper 的完整 SHA/资产、原进程、VM 身份和同 boot。

显式保留分支仍持全局发布与跨 checkout VM 身份锁，复核原 worker 已退出、原签名 Gate 及 production result、远端 consumed、无 active claim、运行 image 等于原 signed candidate、双路健康及全节点 doctor。仅清本 release 的隔离任务，保持 VM 运行；完成及验真都通过只读 probe 核对 VM 的双远端锁、同 boot、健康及无在途任务。独立 VM 终值为 status=verified、vm_power_status=running、vm_cleanup_status=preserved、shutdown_requested=false、vm_started_by_release=false；checks 与 VM 完成证据先写入并验真，shared owner 的 released/running/preserved 作为生命周期最后提交标记；随后写 finalization-result 汇总。它不启动、关停 VM，也不部署、迁移、重发应用、重消费 claim 或恢复数据。任何前序失败保留原 owner，不能把 partial 写成 completed。

成功后 `verify-result` 必须同时验证原签名应用证据、独立 finalization 记录与 terminal VM 收口证据，并只读复核当前生产事实；不能仅将原 failed runner 视为成功。再次执行成功收口入口只验真既有结果，不重复清理或关机。schema 2 保留分支的重放及验真仍执行包含 VM 的全节点 doctor；schema 1 默认关机分支保持停止 VM 的原验真合同。未知 schema/mode、跨模式重试、原 worker 活跃、原失败文件/owner/boot 漂移、镜像变化、未消费或 doctor 失败均拒绝。未配置生命周期或身份未知也拒绝。

新增收口 helper 的测试及现场结果不能冒充新应用 Gate，也不自动豁免未来发布中的恢复敏感 blob 分类。

现场验收沿用正式 consume/verify 合同：internal、direct 公网及异地到 DMIT 的 `/health` 都核对唯一实例响应头和 Background-Ready，并核对 managed upstream 端口。生产清理后可以保留加密恢复点，按固定五文件（及格式受限的可选 backup-failure）、原备份结果、baseline image、权限和 checksum 只读验证；不能要求整个目录不存在，也不能删除正常保留的恢复证据。

当前验真严格要求执行 checkout 的 helper 完整 SHA/资产等于收口记录；后续 HEAD 改动后验旧结果应回到对应 helper 提交，不能拿其他代码静默复用历史完成记录。status/follow 保留原 failed runner，并单列 finalization verified；VM cleanup 结果不覆盖事故退出码。

发布 Python 与 scripts 下 README 使用 `.gitattributes` 固定 LF，使工作区执行字节与 Git blob 一致；信任公钥仍使用既有 `-text` 原字节合同。不能通过忽略换行或放宽 checksum 处理 Windows 自动 CRLF 漂移。

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

跨 checkout 复用已有 VM 时，在本机 `.ssh.local` 的同一 `vm_lifecycle` 下设置 `shared_dir`，值为原共享目录的规范绝对物理路径；末级目录必须是该 VM 的 identity，且已有原 `lease.lock` 和身份匹配的 `owner.json`。配置目录及父链拒绝链接或 reparse；缺失、身份冲突或在途 owner 继续阻断，禁止新建目录、复制 owner 或重建锁来绕过。机器路径只保存在 `.ssh.local`，不写入应用部署参数。

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

## 本地部署参数

日常发布复用同一套已验证脚本；待发布代码提交后，在仓库根生成本机参数。下例解析当前提交的完整 SHA，并以无 BOM UTF-8 保存；此文件已被 Git 忽略：

```powershell
$commit = (git rev-parse --verify 'HEAD^{commit}').Trim()
$config = @{ schema = 1; commit = $commit; profile = '276'; mode = 'downtime' }
New-Item -ItemType Directory -Force '.release' | Out-Null
[IO.File]::WriteAllText((Join-Path $PWD '.release/deploy.local.json'), ($config | ConvertTo-Json), [Text.UTF8Encoding]::new($false))
```

配置只接受这四个字段：`schema` 为整数 `1`，`commit` 为完整 40 位小写 SHA，`profile` 为已登记字符串，`mode` 为 `blue-green` 或 `downtime`。当前 `276` 仍要求停机；配置不能改写 profile 的版本、父链、迁移或发布限制，也不能包含凭据、连接、命令或机器路径。重复键、未知字段和非法值在连接 SSH 前拒绝。

使用已准备好的发布 Python 解释器运行：

```text
python -I -S -B .agents/skills/sub2api-production-deploy/scripts/release.py doctor
python -I -S -B .agents/skills/sub2api-production-deploy/scripts/release.py deploy-follow --lang zh-CN
```

默认配置取入口所在仓库的 `.release/deploy.local.json`；也可显式传入 `--config <文件路径>`。显式完整的 `--commit/--profile/--mode` 优先于配置，文件不存在时仍可沿用完整 CLI 参数；显式指定的文件缺失或损坏则停止。配置必须整体有效，不能靠 CLI 覆盖掩盖坏字段；重复 CLI 参数及参数缩写也拒绝。

`doctor`、`recovery-review-scope`、`vm-only-validate` 读取 commit/profile；`vm-validate`、`deploy`、`deploy-start`、`deploy-follow` 再读取 mode，仍由原命令完成其各自门禁。配置不会安装依赖或修改脚本。Gate、candidate、生产快照、备份、迁移、恢复和生产验真继续按本 release 的完整身份生成与核验，脚本发生变化时仍执行原审阅和验证。

发布启动一次后使用 `follow <release_id>` 重连，最后执行 `verify-result <release_id>` 和原后置 doctor。`status/follow/wait/verify-result/verify-recovery-result` 与内部 worker 不读取默认配置；这些入口不接受 `--config`，因此修改本地下一次发布参数不改变既有 release 的身份，也不授权重跑失败 release。

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


### 进程存活身份与构建依赖前置合同

应用 runner、独立插件 runner 与 consumed 发布 finalization 统一使用 `release.process.process_identity`。Windows 显式声明完整 HANDLE 和 Win32 调用类型，使用同一句柄在读取创建时间前后执行 `WaitForSingleObject(handle, 0)`：已终止返回无存活身份，等待超时才允许返回原格式的创建时间 token。保留进程句柄不代表进程仍存活；真实退出码 259 也不能以 `STILL_ACTIVE` 数值推断存活。访问拒绝、等待失败或时间查询失败按未知阻断，取得的句柄最终关闭。Linux 的不存在 PID 和僵尸/退出状态没有存活身份，权限、格式和读取异常继续阻断。三入口不得各自维护弱化实现；finalization 保持既有未知身份错误合同。

依赖包括四个固定基础镜像 `docker.m.daocloud.io/library/node:24-alpine`、`docker.m.daocloud.io/library/golang:1.27.2-alpine`、`docker.m.daocloud.io/library/alpine:3.21`、`docker.m.daocloud.io/library/postgres:18-alpine`，以及原 Docker Hub frontend `docker.io/docker/dockerfile:1.7`。五角色各解析一次 tag descriptor，随后按 digest 读取并校验 index、唯一 `linux/amd64` 子 manifest 的原始字节摘要、长度、media type 及 config/layer 描述；不能只用 HTTPS、认证或 metadata 成功证明完整依赖可用。

正式发布在准备生产恢复输入前，使用实际 `default` builder、`docker` driver 和 `linux/amd64` 平台完成一次无 `RUN` 的 BuildKit solve：同 digest frontend 实际执行，四个基础镜像都向 scratch 导出 `/etc/os-release` 并核对物化结果。只读有界 proof 绑定 release、完整 commit、Dockerfile/helper SHA、五角色 digest、物化摘要和 builder/daemon/context 身份摘要，并一次绑定 manifest。Gate v2、历史 Gate v1 的新执行和 VM-only 三条入口均要求该 proof；新 manifest 的 `buildkit-dependencies-v1` marker 使签名 Gate 强制验证完整 proof，历史无 marker 的签名保持原字节可读。候选构建前只复核 proof 与当前身份，按冻结 digest 构建，不重复 solve 或解析 tag。检查和构建均不自动重试、不切源、不改正式 daemon/global 代理，仅在 Docker 子进程范围合并镜像站 NO_PROXY 条目；该 solve 不证明后续包下载或应用编译成功。

构建诊断只返回固定白名单状态、已验证数量和 `build_registry_502`、`build_registry_eof`、`build_registry_auth`、`build_registry_tls`、`build_timeout`、`build_space`、`build_compile`、`build_registry_metadata_invalid`、`build_dependency_contract`、`build_preflight_unproven` 或 `build_unknown` 等固定原因码。原始 metadata、stderr 和构建日志只保留于远端本 release 的私有目录，目录 `0700`、文件 `0600`；不得整体回显、下载到本地或输出代理值和认证响应。失败 release、原始日志和身份记录保持不变，后续仍按既有正式 reconciliation 与新发布合同执行。

诊断另含固定阶段、角色、已知 `HEAD`/`GET` 或 `unknown` 及 `retry_count=0`。validator 内的 proof 失败记录为 `build_dependencies`，不得误记成已尝试 candidate；使用显式 `reconcile-vm-preserve --failed-stage build-dependencies` 时，除原候选失败合同的锁、owner/boot、退出、manifest/raw-log、无 probe/签名/生产副作用证明外，还要求候选日志和身份文件从未创建。原本运行且非本 release 启动的 VM 才允许保留并释放租约；原 candidate-build 分支不放宽。


### 隔离 probe 的 PostgreSQL 所有权与失败证据

VM Gate 的 fast 克隆与 specialized/full 恢复统一由 `vm-probe-config.py` 生成临时 probe 连接闭包：VM 有效的 `DATABASE_*` 环境（非空环境优先，空值回落配置文件）是凭据权威，probe 配置只修改副本，生产配置与恢复包保持不变。创建隔离库前必须用当前 PostgreSQL 不可变 image、隔离 network 和 `postgres.env` 完成 `current_user|current_database` 认证预检，且数据库名为 `sub2api_dev`、角色为规范化应用角色；不再要求生产配置的 user 与 VM owner 相同，也拒绝别名、merge、重复键和无法确定的写法。随后统一使用 `pg_restore --no-owner --role <应用角色>` 建立隔离对象；数据库 owner 或 GRANT 不能替代表、序列和函数的 DDL 所有权。验证只操作本 release 独占的 `sub2api_v2_*`/`sub2api_probe_*` 库和 probe Redis，不改变源库、生产权限或生产恢复配置。probe 连接目录及 env 文件为 0700/0600 私有临时资产，成功或失败均清除，失败仅保留本 release 的私有有界诊断。

migration plan、fixture bootstrap 和恢复探针的非零退出必须先保存退出码与输出，再断言成功，避免 ERR trap 在证据落盘前退出。失败不得继续签名 Gate 或启动生产发布。真实 PostgreSQL 回归同时证明旧恢复方式会拒绝应用角色 DDL、两条新恢复来源均可执行 DDL，以及源库对象和权限不变。

### fast fixture 失败的专事故保留收口

`reconcile-vm-fast-fixture-preserve <release_id>` 与 `verify-vm-fast-fixture-preserve <release_id>` 只接受代码冻结的单一事故。必须校验原 manifest、runner、state、VM 生命周期及远端阶段文件的固定字节摘要；原 worker 已退出，VM 原本运行且 boot 未变，正式签名/生产发布未开始，隔离数据库、容器、网络和临时文件均不存在。VM 证明在既有双锁下只读执行，生产核对无 claim、旧镜像与健康，查询未知或失败一律阻断。

收口不修改原失败证据、不恢复数据、不删除资源、不关机、不重跑 Gate。当前已提交推送的 helper 身份独立绑定 checks/result，持久化后重新读回验真并复查现场，最后将 shared owner 置为 released；中断后只按既有独立记录续验，成功重放仍复核现场。helper 本身列入恢复敏感文件；任何后续字节、模式、删除或迁移必须重新审阅，不能套用到其他事故。新发布仍使用新的完整 commit、candidate 和签名 Gate。
