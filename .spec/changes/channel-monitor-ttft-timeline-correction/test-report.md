---
verification-result: pass
scope: full
---

# channel-monitor-ttft-timeline-correction Test Report

## 环境

- runtime/platform：Windows 本地 Go/Node 工具链
- package/tarball：当前工作区；前端 production build 已完成
- fixtures：httptest SSE 延迟/断流 fixture、repository SQL mock、admin/user mapper fixture、Vitest 组件 fixture
- 外部依赖：不访问真实 provider、生产数据库或生产 API

## 命令与结果

| 命令/验证动作 | 结果 | 证据摘要 |
| --- | --- | --- |
| `go test -p 2 -parallel 2 ./... -count=1` | pass | 全部 Go packages 通过 |
| `go test -tags=unit -p 2 -parallel 2 ./... -count=1` | pass | 全部 unit-tag packages 通过 |
| focused Go service/repository/handler tests | pass | TTFT 起点、正值归一、timeline 投影/mapper 通过 |
| `pnpm test:run` | pass | 292 test files / 2037 tests |
| `pnpm typecheck` | pass | vue-tsc 无错误 |
| `pnpm lint:check` | pass | ESLint 无错误 |
| `pnpm build` | pass | Vite production build 完成 |
| `python -m pytest .agents/skills/sub2api-production-deploy/scripts/tests/release` | pass | 337 passed, 1 skipped |
| `git diff --check` | pass | 无空白错误 |
| `spec-wiki-lite validate channel-monitor-ttft-timeline-correction --strict --json` | pass | strict validate `valid=true` |

## System Test 覆盖

| ST | 类型 | 结果 | 证据 |
| --- | --- | --- | --- |
| ST-01 | normal | pass | 延迟响应头 SSE fixture 与 checker focused test |
| ST-02 | failure | pass | 首字后断流保留 TTFT、结果失败的 checker 回归 |
| ST-03 | boundary | pass | repository/admin/user mapper 与前端 `null/0` 归一测试 |
| ST-04 | normal | pass | 六列 SQL/Scan 契约测试与 timeline 三状态 Vitest |
| ST-05 | boundary | pass | retry/degradation 既有测试确认 TTFT 不参与判定 |
| ST-06 | regression | pass | replace、取消、V2 与全量 Go 回归 |
| ST-07 | boundary | not_checked | VM Gate 属于提交后的发布门禁；不发送真实模型请求，后续以隔离 fixture 验证 |

## Unit Test 与 TDD 证据

| UT / suite | Red | Green/Refactor | 结果 |
| --- | --- | --- | --- |
| UT-01 请求起点与正值 TTFT | 旧实现漏计响应头等待/可保存 0ms 的 Red 已取得 | focused checker tests 与 Go 全量通过 | pass |
| UT-02 空事件与断流 | 既有断流 fixture 暴露首字边界 | checker focused tests 通过 | pass |
| UT-03 timeline 六列投影 | 旧外层 SELECT 漏 `ttft_ms` 的 Red 已取得 | repository contract test 通过 | pass |
| UT-04 非正值归一 | 旧 mapper 会输出 0/负数的 Red 已取得 | service/repository/handler tests 通过 | pass |
| UT-05 颜色与空值展示 | 旧 UI 缺少 `0 -> -` 断言的 Red 已取得 | Vitest、typecheck、build 通过 | pass |
| UT-06 降级解耦 | guard 测试确认状态函数不读取 TTFT | retry/policy 全量回归通过 | pass |

## 成功标准覆盖

| 成功标准 | ST/UT/命令 | 结果 |
| --- | --- | --- |
| TTFT 从请求开始计时且有首字为正 | ST-01/ST-02、UT-01/UT-02 | pass |
| timeline 恢复真实状态、耗时和颜色 | ST-04、UT-03/UT-05 | pass |
| 非正 TTFT 统一为空且不改历史 | ST-03、UT-04 | pass |
| 降级继续按整轮总耗时/重试/换号 | ST-05、UT-06 | pass |
| replace/V2/API 兼容与本地门禁 | ST-06、命令表 | pass |
| VM/生产边界 | ST-07、发布 skill | not_checked（发布阶段执行；生产未修改） |

## 失败、未验证与证据缺口

- 失败：无。
- 未验证：真实 provider 流式能力和提交后的 VM Gate 尚未在本地报告阶段执行；这是发布阶段独立门禁，不访问生产、不发送真实模型请求。
- 证据缺口：无本地代码验证阻塞；VM Gate 将补充候选镜像、迁移/健康和隔离 fixture 证据。

## 结论

本地 required evidence 全部通过，verification scope 为 full；真实 provider 与 VM Gate 按发布边界明确标记 `not_checked`，不被伪装为线上能力验证。
