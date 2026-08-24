# bootstrap-wiki 设计方案

## 方案概述

以源码、配置、测试和仓库文档为 SSOT，重写正式首页、快速上手、开发指南、模块指南、对外方法。页面只记录长期边界和导航，不复制实现细节。

## 接口与稳定合同

- 首页固定为 `.wiki/INDEX.md`，不得含 bootstrap marker。
- 每个 Markdown 目录保留 `INDEX.md`，链接为仓库内相对路径。
- 后端入口/路由以 `backend/cmd/server`、`backend/internal/server` 为准；配置以 `backend/internal/config/config.go` 为准；迁移以 `backend/internal/repository/migrations_runner.go`、`migration_plan.go`、`backend/migrations/*.sql` 为准；前端以 `frontend/src/main.ts`、router、api、views 为准。
- 不写凭据、完整环境变量或未核实外部承诺。

## Ownership 与数据/文件流

```text
源码/配置/测试 -> CodeGraph + 源码核验 -> .spec research -> .wiki 长期页面
```

项目 owner 维护首页和跨模块导航；backend/frontend owner 分别维护各自入口和边界；SpecWiki Lite owner 维护工作流合同。

## 正常流程

1. 读取 research、proposal 和占位 Wiki。
2. 按入口、模块、命令和 SSOT 写页面。
3. 检查 frontmatter、链接、目录 INDEX、marker 和敏感字段。
4. 运行 status、show、strict validate，记录证据。

## 失败、边界与回滚

- 无法确认的事实标记 unknown，不写成事实。
- 只操作当前 active change，不处理 archive。
- 发现用户实质修改时保留内容并暂停该页。
- 所有目标固定在 `.wiki/**` 和当前 change，禁止路径逃逸。
- 回滚只恢复本 change 文档，不触碰业务源码。

## 验证设计

ST 覆盖正常 bootstrap、未知事实 fail-closed、链接/frontmatter/index 边界和重复执行保护；CLI seam 为 status/show/validate/archive。

## Wiki 与长期合同落点

首页、快速上手、开发指南、模块指南、对外方法五个入口及其子页面。

## CodeGraph-derived design constraints

- `initializeApplication -> runMainServer/main`；`ProvideRouter -> SetupRouter -> registerRoutes -> routes.Register*`。
- 后端依赖组装在 cmd/server，HTTP 在 internal/server/routes，配置在 internal/config，迁移在 internal/repository + migrations。
- `impact SetupRouter` 仅涉及 http.go 的 ProvideRouter/provider；`impact initializeApplication` 涉及 wire.go、main.go 和初始化调用点。
- CLI 无 context/trace 子命令，已用 status/explore/impact/callers/callees/affected，并核验具体文件行号。

