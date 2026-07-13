# 变更分类与构建

## 目录

- [分类原则](#分类原则)
- [分类决策](#分类决策)
- [严格纯前端](#严格纯前端)
- [构建链改动](#构建链改动)
- [开发门禁改动](#开发门禁改动)
- [完整构建流水线](#完整构建流水线)
- [固定工作树与缓存](#固定工作树与缓存)
- [镜像身份与传输](#镜像身份与传输)

## 分类原则

分类只看最终 diff，不看分支名、提交来源、用户口述或“看起来只是前端”的文件名。无法确定时采用更严格的分类。远程写操作仍需先给出计划并获得用户确认。

所有类别都必须保留生产旧容器，直到 candidate 构建、镜像身份检查、必要的 VM 验证和备份门禁完成。

## 分类决策

```text
最终 diff
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
  RackNerd 完整构建 -> RackNerd 生产切换

普通 dev-gated:
  RackNerd 完整构建 -> 按 image ID 传 VM -> sub2api-dev 验证
                    -> 使用同一 image ID 切换 RackNerd

build-chain:
  VM 完整构建 -> sub2api-dev 验证 -> 按 image ID 传 RackNerd
              -> RackNerd 使用同一 image ID 切换
```

禁止为了“重新构建”而破坏同一镜像身份；验证通过的镜像就是生产候选。

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
4. 切换后验证登录、变更页面、静态资源和浏览器 console。

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
- RackNerd 不得重新构建同一个候选。
- VM 空间或依赖无法安全满足时停止，不得退回 RackNerd 未验证构建。

## 开发门禁改动

以下改动属于 `dev-gated`：

- `backend/`、数据库行为、migration、配置语义或共享契约。
- 非严格纯前端的 fork/upstream 同步。
- 前后端混合改动。
- 分类无法确定的改动。

不触及构建链的普通 `dev-gated` 可以在 RackNerd 构建一次，生产继续运行旧镜像，然后按确切 image ID 传给 VM 验证。验证通过后，生产只使用 RackNerd 已构建的同一镜像。

## 完整构建流水线

所有类别，包括 `frontend-direct`，都必须经过仓库根 `Dockerfile` 的完整多阶段流水线：

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

发布流程禁止执行 `docker builder prune`、`docker system prune`，禁止删除数据库、Redis、data、backup 或仍被容器引用的 image。

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
4. 捕获 `docker load` 后的 loaded image ID。
5. 断言 loaded ID 等于 `candidate_image_id`。
6. 在目标端显式执行 `docker tag <candidate_image_id> <full-sha-tag>`。
7. 最终断言源端和目标端 full-SHA tag 都指向同一 image ID。

导入前目标 Docker Root Dir 可用空间至少为镜像 inspect size 加 `2 GiB`；导入后至少保留 `2 GiB`。使用 `/tmp` 中间文件时，单独检查其所在文件系统。空间不足时立即停止，只能清理过期 `/tmp` artifact 或已证明无容器引用的单个旧 Sub2API image。
