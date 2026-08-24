# 通用浏览器自动化测试指南

浏览器自动化是可选验证方式，不是 SpecWiki Lite 内置 runner，也不要求特定框架。

## 采用条件

- 项目能在隔离环境稳定启动并有明确 base URL。
- 测试账号、fixture 和权限可控，不接触生产数据或不可逆外部状态。
- 已有项目工具可复用，或用户明确授权新增依赖。
- 操作可重复、可清理，失败不会留下破坏性副作用。

## 证据要求

- 记录环境、数据、前置状态、动作步骤和可机器判断的断言。
- 优先断言可见文本、URL、网络结果、DOM 状态、文件/API side effect，而不是时间等待。
- 覆盖 loading、empty、error、success、disabled、防重入和恢复路径（适用时）。
- screenshot、trace、video 只作辅助，不能作为某个 ST 或成功标准的唯一 pass 证据。

## 工具选择

- 优先使用仓库已有浏览器/E2E 工具。
- 可按项目选择 Playwright、WebDriver、Cypress、浏览器连接器或其他可靠方案。
- 不得声称 Lite 提供浏览器运行环境、测试数据或服务器启动器。

## Fallback

工具不可用、项目无法安全启动或权限不足时，记录 `fallback reason`，并用组件测试、API/CLI 断言、静态输出检查或有步骤的人工验证覆盖同一 ST/成功标准。
