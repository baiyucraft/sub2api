# bootstrap-wiki 任务计划

implementation-mode: direct

## 任务总览

按首页、快速上手/开发、模块/对外方法、文档验证四个能力块执行；只修改 Wiki 和 change 证据。

## 1. 首页与索引

- [x] 重写 .wiki/INDEX.md 并移除 marker。
- [x] 更新四个栏目 INDEX。
- [x] 补齐 frontmatter、owner、updated。

### CheckList

- [x] 内容来自已核验事实
- [x] 链接检查通过
- [x] CLI status/validate 通过

## 2. 快速上手与开发指南

- [x] 记录本地/Docker 启动、setup、配置和运行时。
- [x] 记录后端/前端结构、测试、迁移和质量命令。
- [x] 标注 pnpm、PostgreSQL、Redis、Windows/CI 边界。

### CheckList

- [x] 命令与 README/DEV_GUIDE/Makefile 一致
- [x] 无凭据和生产 secrets
- [x] 失败/回滚边界已说明
- [x] 页面结构检查通过

## 3. 模块与对外方法

- [x] 记录 initializeApplication -> router -> routes 请求链路。
- [x] 记录前端 main/router/stores/api/views 边界。
- [x] 记录网关/上游、数据/迁移、运维和 API 认证分组。

### CheckList

- [x] 页面注明 owner 和关键入口
- [x] API 只列稳定分组
- [x] CodeGraph 与源码行号已核验
- [x] 链接/索引检查通过

## 4. 验证、评审与归档

- [x] 运行 frontmatter/链接/INDEX/marker/敏感字段检查。
- [x] 运行 status/show/validate 并记录证据。
- [x] 完成 full review、verification 和单次 archive。

### CheckList

- [x] ST-01 至 ST-04 有证据
- [x] diff 仅包含 Wiki/change 文档
- [x] review 无 blocking finding
- [x] strict validate 在 verification 通过

## 用例到任务映射

| ST | 任务 | 证据 |
| --- | --- | --- |
| ST-01 | 1/2/3/4 | status、页面清单 |
| ST-02 | 2/3/4 | unknowns、diff |
| ST-03 | 1/4 | doc-check、validate |
| ST-04 | 4 | 重复检查、archive |

## 执行顺序

页面 -> 文档检查 -> status/show/validate -> review/test report -> archive。
