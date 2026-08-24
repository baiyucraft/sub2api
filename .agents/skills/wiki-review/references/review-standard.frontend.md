# Frontend Review 标准

必须与 `references/review-standard.md` 同时使用，仅适用于真实 frontend/UI diff。

## Blocking / P0

- 外部 JSON、URL、表单、storage、上传和环境变量必须有运行时边界，不能只靠 TypeScript。
- Promise rejection、请求取消、组件卸载、timer/subscription/listener 必须清理。
- Hooks 生命周期、旧闭包、受控状态、list key、optimistic rollback 必须正确。
- 表单约束、防重复提交、loading/empty/error/disabled/success/recovery 状态必须可见且可验证。
- 未消毒 HTML、未编码 URL、敏感 token/storage/logging 属于 blocking。
- 关键用户行为和失败态必须有 component/E2E/manual fallback evidence。

## Non-blocking / P1/P2

- 大列表/大图/高频事件、重复渲染或无意义 memoization 的性能风险。
- focus、keyboard、semantic label、错误关联等 accessibility 缺口。
- 组件过大、状态位置不当、样式污染或层级混乱。

## 工具与误报

- 可使用项目已有 component tests、浏览器自动化或其他证据，不因缺少某个特定 runner 机械判失败。
- 不机械要求所有函数 memoize，不因审美偏好阻塞。
