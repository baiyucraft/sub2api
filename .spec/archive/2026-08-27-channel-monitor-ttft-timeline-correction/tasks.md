# channel-monitor-ttft-timeline-correction 任务计划

implementation-mode: tdd

## 任务总览

按 TTFT 计时、时间线 SQL、读出归一化、前端展示、长期合同、全量验证和 VM Gate 七个能力块实施。每个产品代码修改前必须先取得对应目标行为的 Red 失败证据。

## 实现模式

tdd

先写失败测试并确认是目标行为缺失，再写最小实现；Green 后整理共享 helper 和回归覆盖。

## 1. 修正流式 TTFT 起点与正值语义

- [x] 1.1 Red: UT-01/UT-02 增加响应头前延迟、空事件和首字后断流 fixture，并确认失败原因符合预期（旧实现失败：TTFT 接近 0，setter 保存 0）
- [x] 1.2 Green: 在 `Do` 前记录请求起点，有首字时保存 `max(1, elapsed_ms)`
- [x] 1.3 Refactor: OpenAI/Grok/Anthropic/Gemini/兼容分支共用同一计时 helper，保持非流式 replace 为空

### CheckList

- [x] 失败测试已确认：`go test -tags=unit ./internal/service -run 'TestRunCheckForModel_TTFTIncludesResponseHeaderWait|TestSetMonitorTTFTClampsSubMillisecondToOne' -count=1 -v`（旧实现 Red）
- [x] 最小实现后测试通过
- [x] 重构后测试仍通过
- [x] checker focused tests 通过
- [x] 注释规范检查完成（新增导出 helper 已说明非正值边界；其余变更无复杂时序注释需求）

## 2. 修复时间线 SQL 投影与扫描

- [x] 2.1 Red: UT-03 增加六列投影/扫描契约测试并确认旧实现失败
- [x] 2.2 Green: 外层 SELECT 补齐 `ttft_ms`，与 Scan 严格同序
- [x] 2.3 Refactor: 保持批量查询、排序、limit 和 public interface 不变

### CheckList

- [x] 失败测试已确认：旧 SQL 未匹配 `ttft_ms` 投影，且扫描目标错位
- [x] 最小实现后测试通过
- [x] 重构后测试仍通过
- [x] repository focused tests 通过
- [x] 注释规范检查完成（SQL 投影修复保持原有查询职责与扫描顺序说明）

## 3. 统一非正 TTFT 读取/API 归一化

- [x] 3.1 Red: UT-04 覆盖 history/latest/timeline/立即探测/admin/user mapper 的 `nil/0/负数/正数`
- [x] 3.2 Green: 增加共享正值归一化 helper 并接入 repository 与 response mapper
- [x] 3.3 Refactor: 消除重复判断，不改变 JSON 字段和数据库行

### CheckList

- [x] 失败测试已确认
- [x] 最小实现后测试通过
- [x] 重构后测试仍通过
- [x] handler/service/repository focused tests 通过
- [x] 注释规范检查完成（NormalizeChannelMonitorTTFT 已说明非正值语义）

## 4. 恢复前端颜色与空值展示

- [x] 4.1 Red: UT-05 增加 `operational/degraded/error` 颜色和 `null/0` TTFT 展示测试
- [x] 4.2 Green: 对非正 TTFT 显示 `-`，保持总耗时和状态色映射
- [x] 4.3 Refactor: 管理列表、立即探测和用户卡片复用现有格式化口径

### CheckList

- [x] 失败测试已确认
- [x] 最小实现后测试通过
- [x] 重构后测试仍通过
- [x] 相关 Vitest/typecheck/lint 通过
- [x] 注释规范检查完成（前端仅复用格式化 helper，无新增复杂逻辑）

## 5. 锁定 V1 降级与回归边界

- [x] 5.1 Red/guard: UT-06 增加 TTFT 与总耗时独立的状态判定用例
- [x] 5.2 Green: 确认无需把 TTFT 接入判定；若测试暴露耦合，仅做最小解耦
- [x] 5.3 回归 replace 非流式、取消、重试/换号、V2 与可用率测试

### CheckList

- [x] 状态判定测试通过
- [x] replace/取消/重试/V2 回归通过
- [x] 未改变 settings、migration 或 endpoint
- [x] 注释规范检查完成（TTFT 与总耗时边界已在类型/测试中说明）

## 6. 更新长期合同并完成本地门禁

- [x] 6.1 更新 `.wiki/` 的 TTFT、总耗时、降级和时间线合同
- [x] 6.2 更新 fork extension catalog 的 description/invariants/required tests，不登记 `.wiki/.spec`
- [x] 6.3 运行 focused Go/Vitest、全量 Go、unit-tag Go、Vitest、typecheck、lint、build、release pytest、`git diff --check`
- [x] 6.4 运行 SpecWiki strict validate，按 full scope 执行 wiki-review 并生成 pass 报告
- [x] 6.5 单次调用 archive CLI，核验 dated archive 和长期 Wiki 状态（本次收口执行）

### CheckList

- [x] 长期合同已更新
- [x] 所有本地 required evidence 通过
- [x] review-result/verification-result 均为 full/pass（VM Gate 为后续发布门禁，真实 provider 保持 not_checked）
- [x] change 已由 CLI 单次归档（CLI 调用后核验）

## 7. 提交、fork audit 与 VM Gate

- [x] 7.1 创建 focused commit，确认工作区干净并取得完整 40 位 SHA（`9fe5d3db8bd6ee40e08cb1790b26c23c37032416`）
- [x] 7.2 以当前 `upstream/main` 完整 SHA 运行 fork extension audit，处理 blocker 后再确认干净提交（`7634e3c23b5b9afc588c37b170820f63f1d41bbb`，snapshot 通过）
- [x] 7.3 推送 `origin/main`，确认远端 SHA 一致（`9fe5d3db8bd6ee40e08cb1790b26c23c37032416`）
- [x] 7.4 按 profile 242 构建唯一 candidate，通过 VM `sub2api-dev:8211` Gate（release `242-9fe5d3db8bd6-1787783417-226d164a`）
- [x] 7.5 用隔离 SSE fixture/测试数据验证 TTFT、时间线颜色和总耗时阈值；真实 provider 标记 `not_checked`（Gate `vm_validate/verified`）
- [x] 7.6 核验生产未修改、VM 仅保留唯一开发容器和既有数据/回滚证据

### CheckList

- [x] commit/origin/audit target 均为完整 SHA
- [x] fork audit 无 blocker
- [x] VM Gate terminal evidence 通过
- [x] 生产未部署、未修改

## 用例到任务映射

| 系统测试用例 | 大 task | 小 task / 验证 |
| --- | --- | --- |
| ST-01 | 1 | 1.1-1.3 / UT-01 |
| ST-02 | 1 | 1.1-1.3 / UT-02 |
| ST-03 | 3、4 | 3.1-3.3、4.1-4.3 / UT-04、UT-05 |
| ST-04 | 2、4 | 2.1-2.3、4.1-4.3 / UT-03、UT-05 |
| ST-05 | 5 | 5.1-5.3 / UT-06 |
| ST-06 | 1、5 | focused regression |
| ST-07 | 7 | 7.1-7.6 / profile 242 Gate |

## 执行顺序

- 1 → 2 → 3 → 4 → 5 → 6 → 7。
- 每个 TDD task 的 Red 证据必须在对应产品代码修改前取得。
- fork audit 要求干净工作区，因此先创建本地 focused commit；若审计要求修复，形成后续 focused commit并重跑审计后再 push。
- VM Gate 只绑定最终推送的完整 SHA，不复用历史 candidate 或 Gate。

## 暂缓事项

- 生产部署、生产数据回填/清理、真实 provider 流式探测均不在本 change 内。
