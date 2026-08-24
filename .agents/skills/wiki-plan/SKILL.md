---
name: wiki-plan
description: 把已接受的设计转换为 normal/failure/boundary 系统测试、可选 TDD 单元测试和可执行任务计划。
---

# Wiki Plan

只负责 `design`、`cases`、`tasks` 阶段。计划必须覆盖成功标准，并在生成 tasks 前确定一次实现模式：可选 `tdd` 或 `direct`，默认不替用户选择。

## 前置条件

- proposal 与 design 完整、一致且 strict validate 无 blocker。
- change 是可执行 standalone/child，不是 parent。
- 每个成功标准都有可观察结果。

## 输入

- proposal、design、metadata、research。
- 项目现有测试约定、命令、受影响文件和可用运行环境。
- `references/system-tests-template.md`、`references/unit-tests-template.md`、`references/tasks-template.md`。
- 涉及浏览器交互时读取 `references/browser-automation.md`。

## System Tests

- 使用稳定 `ST-*` id，覆盖正常、失败、边界和回归场景。
- 写清前置环境、测试数据、动作、断言、失败关闭和证据形式。
- 系统测试不等于只写 E2E；CLI、API、文件检查、集成测试或人工验证都可作为合适证据。

## TDD Unit Tests

- 只有选择 `tdd` 时才生成 `unit-tests.md`，使用 `UT-*`，明确 Test/Modify、Red 原因、Green 最小行为和 Refactor 守护。
- 选择 `direct` 时仍生成 system tests 和验证映射，但不要求先取得 Red 失败证据。
- 每个 UT 映射到 ST、成功标准或安全边界；不要只追求实现细节覆盖。

## Tasks

1. 如果当前请求没有明确模式，先询问：“本次选择 `tdd` 还是 `direct`？”；已有明确选择时不得重复询问。
2. 在 tasks 中写入稳定字段 `implementation-mode: tdd` 或 `implementation-mode: direct`。
3. `tdd` 按 Red → Green → Refactor 编排，并要求相关 Red 失败证据；`direct` 按 Implement → Verify → Refactor 编排，不要求 Red 证据。
4. `direct` 参考结构化实现清单：按能力块/验收目标拆分编号大 task，每个大 task 都有 `### CheckList`，小 task 以动词开头并指向具体模块、文件、接口、配置或数据流。
5. 每个 task 映射 ST/UT、成功标准与验证命令；两种模式都必须完成测试、review、verification 和 archive 门禁。

## 浏览器自动化

浏览器工具是可选证据手段。只有项目可启动、测试数据可控、权限和副作用可接受时采用。优先使用项目已有工具；不可用时记录 fallback reason，并用组件测试、API/CLI 断言或有步骤的手工证据覆盖。不得仅凭截图判 pass。

## 输出

- `system-tests.md`
- 选择 `tdd` 时输出 `unit-tests.md`；选择 `direct` 时可省略
- `tasks.md` 与合法 `implementation-mode`、任务总览、编号大 task、checklist、用例映射、执行顺序和暂缓事项
- planning 完成后 stage tasks，strict validate 通过

## 暂停条件

- 成功标准无法映射到证据。
- design 缺少接口、ownership、安全或回滚决定。
- 未获得实现授权。

## 下一阶段

tasks 完整、`implementation-mode` 合法且当前用户明确授权实现时使用 `wiki-apply`；缺失或非法 mode 必须回到本 Skill。
## CodeGraph 阶段动作

当目标文件或符号已知时按需调用 `codegraph_affected`，把受影响测试映射到 ST/UT 和 tasks。CodeGraph 只提供测试线索，不能替代测试设计、失败场景或验收断言；结果写入 tasks 的证据栏，不保存原始输出。
