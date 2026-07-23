# 变更分类与构建

## 目录

- [分类原则](#分类原则)
- [分类决策](#分类决策)
- [运维资产](#运维资产)
- [严格纯前端](#严格纯前端)
- [构建链改动](#构建链改动)
- [开发门禁改动](#开发门禁改动)
- [平台验证边界](#平台验证边界)
- [本地资源控制](#本地资源控制)
- [上游合并审计](#上游合并审计)
- [完整构建流水线](#完整构建流水线)
- [固定工作树与缓存](#固定工作树与缓存)
- [镜像身份与传输](#镜像身份与传输)

## 分类原则

分类只看最终 diff，不看分支名、提交来源、用户口述或“看起来只是前端”的文件名。无法确定时采用更严格的分类。远程写操作仍需先给出计划并获得用户确认。

应用产物类别必须保留生产旧容器，直到 candidate 构建、镜像身份检查、必要的 VM 验证和备份门禁完成。运维资产类别不生成 candidate，但必须完成本节定义的测试、确认和现场验证。

## 分类决策

```text
最终 diff
  |
  +-- 仅 skill 文档或固定字段只读巡检？ -> ops-readonly-assets
  |
  +-- 仅维护/备份/服务控制资产，且不进入应用构建或运行时？
  |       |
  |       +-- 是 -> ops-control-assets
  |       +-- 无法证明 -> dev-gated 或 build-chain
  |
  +-- 所有文件均在 frontend/ 且只含纯 UI？
  |       |
  |       +-- 是 -> frontend-direct
  |       |
  |       +-- 否 -> 是否触及构建链或依赖？
  |                   |
  |                   +-- 是 -> build-chain
  |                   +-- 否 -> dev-gated
  |
  +-- 分类无法证明 -> dev-gated
```

### 产物流向

```text
frontend-direct:
  本地前端检查 + Vite 代理 VM Gate smoke
    -> RackNerd 完整构建 -> RackNerd 生产切换

ops-readonly-assets:
  本地验证 -> review -> commit/push -> 固定字段只读巡检

ops-control-assets:
  本地/隔离验证 -> review -> 用户确认 -> 目标资产更新或维护操作 -> 现场验收

普通 dev-gated:
  RackNerd 完整构建 -> 按 image ID 传 VM -> sub2api-dev 验证
                     -> VM Gate 浏览器/接口验收 -> 使用同一 image ID 切换 RackNerd

build-chain:
  VM 完整构建 -> sub2api-dev 验证 -> 按 image ID 传 RackNerd
              -> VM Gate 浏览器/接口验收 -> RackNerd 使用同一 image ID 切换
```

禁止为了“重新构建”而破坏同一镜像身份；验证通过的镜像就是生产候选。同一 commit 重新构建也可能得到不同 image ID，same commit 不等于 same candidate；只有同一 release 的 Gate、归档和 image ID 三者同时匹配，才可称为复用 candidate。

## 运维资产

### `ops-readonly-assets`

只有以下条件全部成立才适用：

- 最终 diff 仅包含 skill 文档、reference、测试，或固定字段的只读巡检脚本。
- 脚本不接受任意远程命令，不写远端文件，不修改数据，不控制服务，不读取或输出 secret。
- 不进入 Dockerfile、Compose、应用二进制、systemd unit、生产安装流程或自动备份入口。
- 对最终 diff 执行引用检查、脚本测试、严格 review、skill 校验和 `git diff --check`。

该类别不构建镜像、不切换应用、不创建数据库恢复点。提交推送后只做适用的只读健康检查；镜像、VM、迁移和切换字段写 `not_applicable`，不能伪造 candidate。

### `ops-control-assets`

任何具备以下能力的资产至少属于本类别：

- 远程写文件、修改配置、数据或备份。
- stop/start/restart/mask/unmask systemd 或容器。
- 执行 migration、恢复、清理、晋升或生产维护动作。
- 位于 `deploy/` 且不能证明属于应用构建链，但可供人工操作生产。

`rg` 未发现运行时引用不能把控制脚本降级为只读资产。该类别只有在证明不进入应用构建、镜像或运行时后，才可免应用镜像构建和切换；仍必须：

1. 给出生产写操作方案并获得用户确认。
2. 执行 shell/Python 语法、故障注入、幂等和失败恢复测试。
3. 经过严格 reviewer 批准。
4. 按影响面执行备份、恢复点、停写和回滚门禁。
5. 更新目标资产时记录旧文件 checksum、目标 checksum 和恢复步骤。
6. 现场验证受影响服务、备份和双路径健康。

`cleanup-production` 是本类别的固定维护入口：它不安装运行时资产、不切换应用，但会删除精确计划内的旧 image 和执行容量有界 BuildKit GC，因此必须先 dry-run，并以同一 `plan_sha256` 绑定 apply；不能替换成 `image prune` 或 `system prune`。

涉及 Dockerfile、Compose、build/install 脚本、当前自动化入口或应用 runtime 的变更，不属于纯运维资产，必须回到 `build-chain` 或 `dev-gated`。任何不确定情况从严处理。

## 严格纯前端

只有以下条件全部成立，才允许 `frontend-direct`：

- 每个变更文件都在 `frontend/` 下。
- 变更只涉及 UI 实现、样式、静态资源或严格前端测试。
- 不涉及 API client、请求/响应序列化、共享 API 类型或生成契约。
- 不涉及登录、OAuth、JWT、refresh token、session 或运行时配置语义。
- 不涉及 `package.json`、`pnpm-lock.yaml`、workspace 或 package manager 配置。
- 不涉及 Vite、TypeScript、代码生成、Docker、构建脚本或工具链。
- 不包含难以审计的 upstream merge、大量生成文件或异常大删除。

即使是纯前端，也必须：

1. 在 RackNerd `/opt/sub2api-src` 用完整根 `Dockerfile` 构建。
2. 执行类型检查和相关前端测试。
3. 记录 `candidate_image_id`、`pre_switch_image_id` 和可用的 `older_fallback_image_id`。
4. 在生产切换前用本地 Vite 加载页面，但将 `VITE_DEV_PROXY_TARGET` 指向 VM Gate；验证登录、变更页面、静态资源、关键 API 和浏览器 console。
5. 切换后再次验证生产页面；本地 Vite 不得启动或依赖本地后端。

`docs/legal/`、根目录配置或任何前端构建依赖变化都不满足“所有文件均在 `frontend/`”的条件。

## 构建链改动

以下任一变化均属于 `build-chain`，必须先在 VM 构建和验证：

- 根 `Dockerfile`、`deploy/Dockerfile`、镜像 stage、基础镜像参数。
- build script、编译器、工具链、workspace 或安装脚本。
- `backend/go.mod`、`backend/go.sum`。
- `frontend/package.json`、`frontend/pnpm-lock.yaml`。
- pnpm、Go、Node、包管理器版本或依赖安装逻辑。

VM 构建规则：

- 使用 VM `/opt/sub2api-src` 固定工作树。
- 构建完成后必须在 `sub2api-dev` 完成完整验证。
- 从 VM 导出已经验证的 `candidate_image_id`，传到 RackNerd。
- 若 profile 已接入 `deploy/release.py`，使用 `python deploy/release.py deploy-start --profile <profile> --commit <full SHA>`，再用 `status/wait/verify-result` 收口；候选只在 VM 构建，RackNerd 只验签、导入和核对 image ID。
- RackNerd 不得重新构建同一个候选。
- VM 空间或依赖无法安全满足时停止，不得退回 RackNerd 未验证构建。
- 构建链在 VM 至少执行五次空间门禁：构建前、构建后、导出前、导入前、导入后；同时检查 Docker Root Dir、containerd、`/tmp` 和传输临时目录。
- 构建峰值必须包含镜像层、解压空间、压缩归档、缓存增长、开发数据库恢复副本、migration 临时空间和旧镜像回滚预留。任一空间不足时停止，禁止自动重建或改到 RackNerd 构建。

## 开发门禁改动

以下改动属于 `dev-gated`：

- `backend/`、数据库行为、migration、配置语义或共享契约。
- 非严格纯前端的 fork/upstream 同步。
- 前后端混合改动。
- 分类无法确定的改动。

不触及构建链的普通 `dev-gated` 可以在 RackNerd 构建一次，生产继续运行旧镜像，然后按确切 image ID 传给 VM 验证。验证通过后，生产只使用 RackNerd 已构建的同一镜像。

开发联调阶段直接使用 VM Gate 的 candidate 服务验证前后端契约。允许在本机执行 Go、TypeScript、Vitest 等静态/unit 门禁，但禁止启动本地后端来替代 VM Gate；前端页面也不应指向一个不存在的本地 API。

### 浏览器服务目标检查

开始浏览器 smoke 前必须记录以下脱敏事实：

- 页面地址和 API 代理目标属于本机 Vite + VM Gate，或两者都属于 VM Gate；
- 页面显示的版本与 candidate/version evidence 一致；
- 关键 API 至少有一次真实成功响应，且响应不是前端缓存或空状态兜底。

本机 `3000` 页面在没有后端（常见为 `8080` 未监听）时可能保留登录状态、展示旧版本号或把接口失败渲染为空列表。此时只能记录为“前端壳 smoke”，必须切换到 VM Gate 重新验收，不能报告功能通过。

## 平台验证边界

本地主机缺少目标平台工具时，不得把“命令无法执行”写成通过，也不得临时改用语义不同的 shell。特别是 Windows 本地没有 `bash` 时：

- Go、TypeScript、Vitest 等可跨平台检查继续在本地执行。
- `bash -n`、Caddy/Nginx shell 测试、Linux 权限和容器运行时测试必须在受控 Linux 环境执行；需要 VM Gate 的发布优先放入同一 candidate 的 VM 验证阶段。
- 报告分别记录 `local=not_checked (tool unavailable)` 和 `vm=pass|fail`，不能用 VM 结果伪装成本地执行结果。
- 若 Linux-only 检查没有进入自动 Gate，必须在提交或生产切换前显式执行并保存结构化证据；无法执行则停止，不得因脚本来自 upstream 而跳过。

## 本地资源控制

本地开发机默认使用低峰值模式，避免 Go 编译、测试和前端门禁同时抢占 CPU 与内存：

- 开发阶段只运行受影响包，保留 Go 测试缓存，不加 `-count=1`：

  ```text
  go test -p 2 -parallel 2 ./internal/<affected-package>/...
  ```

- 最终 Go 全量门禁在 `backend/` 目录执行一次：

  ```text
  go test -p 2 -parallel 2 ./... -count=1
  go test -tags=unit -p 2 -parallel 2 ./... -count=1
  ```

- 全量 Go、unit-tag、Vitest、typecheck 和前端生产构建必须串行执行，不使用并行工具调用同时启动。定向轻量检查可以并行，但不得与上述重量级门禁重叠。
- reviewer 或测试产生修复后，先用缓存运行受影响包；工作树稳定后再执行最终全量门禁。最终全量发现问题并修复时仍须重新跑完整门禁，不能用资源限制跳过验证。
- `-p 2` 限制并行包数，`-parallel 2` 限制包内并行测试；它们不降低测试覆盖。正式 Linux server 仍由 VM 根 `Dockerfile` 构建，本地不额外生成生产二进制。

CI、VM 和生产构建不得继承本地 `GOFLAGS`、`GOMAXPROCS` 或进程优先级设置。本规则只约束本地命令参数，不修改用户级或系统级 Go 配置。

## 上游合并审计

fork/upstream merge 的 reviewer 发现必须先归类，再决定是否阻断：

```text
发现
  +-- 合并冲突或融合代码新引入的回归 -> 修复后重跑门禁
  +-- 合并前 fork 与目标 upstream 均存在 -> 既有风险，按当前严重度决定是否阻断
  +-- 明确计划要求的行为             -> 用需求、实现和测试三方证据确认
  +-- 无法证明来源或影响              -> 从严视为当前阻断项
```

- “不是本次引入”不能自动放行 P0/P1；若风险会破坏本次迁移、数据、鉴权、调度或回滚，仍必须阻断。
- “官方也这样实现”不能替代 fork 行为验证；冲突文件要同时覆盖 fork 定制和官方新增能力。
- 明确设计行为不能仅凭口头解释排除，应有计划条目、代码路径和回归测试。例如复制上游账号是否保留绑定、是否默认可调度，必须按产品语义验证。
- 非阻断的既有技术债只在报告中简述影响和兜底，不混入大规模官方 merge；需要修复时另开小范围变更和并发/故障测试。
- 最终提交前再次核验固定 upstream SHA、merge parents、无冲突、生成文件幂等、迁移/依赖是否按计划变化，以及工作区无未暂存修复。

### 合并提交门禁

fork/upstream merge 在创建提交前必须完成以下检查：

1. 用 `git rev-list -n 1 <tag>` 解析目标 tag，并断言它等于固定的 upstream commit。该写法在 PowerShell 中也稳定；不要依赖可能被 shell 解释的 `^{}` 表达。
2. 合并期间记录 `MERGE_HEAD`；提交后用 `git show -s --format=%P HEAD` 断言第二父严格等于目标 upstream commit。
3. 除普通 `go test ./...` 外，先用 `rg -n '^//go:build'` 识别适用的 build-tag 测试。fork/upstream merge 涉及后端或测试接口桩时，在 `backend/` 目录执行完整 unit-tag 包集：

   ```text
   go test -tags=unit -p 2 -parallel 2 ./... -count=1
   ```

   普通全量测试不会自动包含 `//go:build unit` 文件，不能用它替代该门禁。
4. Ent/Wire 等生成文件先生成并暂存，再执行第二次相同生成命令；第二次生成后 `git diff --name-only` 和 `git ls-files --others --exclude-standard` 都必须为空，避免漏掉生成器新增的未跟踪文件。
5. 测试或 reviewer 产生任何后续修复后必须重新执行 `git add -A`。提交前同时满足：无 unmerged path、`git diff --name-only` 为空、`git ls-files --others --exclude-standard` 为空、`git diff --cached --check` 通过。
6. 分别核验 migration 和依赖差异。已发布 migration 不得漂移；新增 migration 必须符合重编号计划；计划声明无依赖变化时，`go.mod/go.sum/package.json/pnpm-lock.yaml` 的差异必须为空。

提交后再次执行父提交、目标 tag、migration 集合和工作区 clean 检查，再允许 push。不得复用修复前的暂存区或测试结论。

## 完整构建流水线

所有应用产物类别，包括 `frontend-direct`，都必须经过仓库根 `Dockerfile` 的完整多阶段流水线：

```text
frontend source
  -> pnpm install --frozen-lockfile（BuildKit cache）
  -> pnpm build
  -> dist 嵌入 backend/internal/web/dist
  -> Go release build（module/build cache）
  -> PostgreSQL client + runtime image
```

当前根 `Dockerfile` 使用固定缓存标识：

- `sub2api-pnpm-store`
- `sub2api-go-mod`
- `sub2api-go-build`

`deploy/docker-compose.dev.yml` 的 build context 指向仓库根，并使用根 `Dockerfile`。`deploy/Dockerfile` 与根 Dockerfile 的差异本身属于构建链改动，不能在不分类的情况下切换。

禁止：

- 只构建前端 layer。
- 跳过 Go 后端 release build。
- 把本地 `frontend/dist` 直接复制到生产。
- 用不同构建主机重新生成“等价”镜像来代替传输已验证镜像。

## 固定工作树与缓存

固定路径以 [architecture-and-current-state.md](architecture-and-current-state.md) 为唯一事实源。构建任务必须先读取该文档，再使用其中定义的 RackNerd/VM 源码、部署和 `data-dev` 路径；本文件不单独维护第二份路径表。

构建前确认：

- `.sub2api-deploy-worktree` marker 存在。
- `origin` 是用户 fork。
- 除 marker 外没有 tracked/untracked 改动。
- 已选择完整 40 位 commit SHA。
- 不创建每个 commit 一个新源码目录。
- 复用实际构建主机的 BuildKit 和依赖缓存。

发布流程禁止执行 `docker system prune`、缺少缓存上限或保留量的 builder prune，禁止删除数据库、Redis、data、backup 或仍被容器引用的 image。VM 空间在白名单对象清理后仍不足时，唯一允许的缓存回收是版本化清理器执行一次 `--all`、`max-used-space=1gb`、`reserved-space=1gb` 的容量有界 BuildKit GC；`--all` 不得脱离后两项边界单独使用。

## 镜像身份与传输

使用唯一 full-SHA tag，例如：

```text
sub2api:baiyu-<base-version>-<full-commit-sha>
```

构建后记录：

- 完整 commit SHA。
- full-SHA tag。
- `candidate_image_id=sha256:...`。
- `docker image inspect` size。
- 构建时间、应用版本和构建主机。

跨机传输必须：

1. 按 image ID 执行 `docker save`，不能依赖浮动 tag。
2. 对准确的 gzip 压缩字节计算 SHA-256。
3. 目标端校验传输 SHA-256。

传输前后还要核验临时归档所在文件系统和 Docker/containerd 空间；不得把 Docker Root Dir 的可用量当作 `/tmp` 或 containerd 空间的替代证明。
4. 捕获 `docker load` 后的 loaded image ID。
5. 断言 loaded ID 等于 `candidate_image_id`。
6. 在目标端显式执行 `docker tag <candidate_image_id> <full-sha-tag>`。
7. 最终断言源端和目标端 full-SHA tag 都指向同一 image ID。

导入前目标 Docker Root Dir 可用空间至少为镜像 inspect size 加 `2 GiB`；导入后至少保留 `2 GiB`。使用 `/tmp` 中间文件时，单独检查其所在文件系统。空间不足时立即停止，只能清理过期 `/tmp` artifact 或已证明无容器引用的单个旧 Sub2API image。
