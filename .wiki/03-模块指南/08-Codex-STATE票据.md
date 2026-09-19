---
title: Codex STATE 票据
description: 账号级 STATE 采集、三模型隔离、生命周期和来源边界
updated: 2026-09-19
owner: project
---

# Codex STATE 票据

本页记录 `openai-codex-state-tickets` 的长期维护合同。它区分直接合入的 PR 基线与 fork 独立增强，避免后续同步 upstream 或参考项目时误删、重复实现或引入许可证污染。

## 来源与维护归属

| 来源 | 固定点 | 本项目采用方式 |
| --- | --- | --- |
| [Sub2API PR #7315](https://github.com/Wei-Shaw/sub2api/pull/7315) | head `3c2f05c957b4b93866318ec8695fc5a28fff70eb`；merge `49a39b6dc1abed30fd227611e8af1108bc427610` | 在既有 turn-state 转发与来源守卫上增加后台票据采集、存储、注入和模型门控基线；由 #7338 父历史带入，不重复合并 |
| [Sub2API PR #7338](https://github.com/Wei-Shaw/sub2api/pull/7338) | head `09e112fb4997be666b10da48c3b5f65fb8d8b53b`；fork merge `a6be2645fffca32a2425d79f822902d7a5aedaff` | 账号级配置、动态代理采集、固定代理复验、持久化和 watchdog；普通 merge 保留作者与父历史 |
| [`ccodex-sleep-state`](https://github.com/gylive/ccodex-sleep-state/tree/26b22196bf68b372d0daad9381f686a3321068d4) | `26b22196bf68b372d0daad9381f686a3321068d4` | #7338 原始设计参考点 |
| [`ccodex-sleep-state`](https://github.com/gylive/ccodex-sleep-state/commit/b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6) | `b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6` | 2026-09-19 设计复核点，用于理解严格 envelope、不可变快照和 active/ready 状态机 |

参考项目采用 GPL-3.0，本项目采用 LGPL-3.0-or-later。fork 只借鉴公开机制和状态机思想，在现有 Sub2API service/repository/scheduler 边界内独立实现；不得复制参考项目整文件、函数实现或引入其实现依赖。代码注释、Wiki 和审计登记必须持续保留两个参考 commit，便于后续比较其设计变化。

PR #7338 尚未成为当前官方正式版本的 upstream 能力，因此 `openai-codex-state-tickets` 归属 `fork`。未来若官方合入相关 PR，应按功能域逐项核对，不得仅凭 PR 号相同就删除本 fork 的多模型、状态机、锁和客户端优先级增强。

### 已审查但未采用的参考行为

- 不接管本机 Codex 配置、CCS/profile 或参考项目的本地 Web 面板。
- 不引入订阅导入、代理节点池、节点生命周期或独立随机出口管理。
- 不采用纯内存 STATE；本项目继续使用私有 `accounts.extra` 快照。
- 不因模型不匹配改写或拦截正式业务响应正文；watchdog 只观察完整响应。
- 不采用参考项目的 `on_demand/standby` 用户刷新策略、本地 API-key relay 或单用户服务模型。
- 不实现原生上游 WebSocket STATE 管理、账号暂停/恢复编排或本地配置恢复。

## 分层结构

```text
全局网关开关与共享采集代理
                ↓
账号级 OAuth/setup-token 显式启用
                ↓
账号 + 规范出站模型 + 配置 revision + 固定代理指纹
                ↓
active / ready / strikes / cooldown / immutable version
                ↓
客户端 STATE 优先级守卫 + strict 调度门控
```

全局开关只允许采集器运行，不自动启用任何账号。账号配置按 `gpt-6-astra`、`gpt-5.6-sol`、`gpt-5.6-terra` 三个规范出站模型隔离；模型映射后的实际出站模型决定票据域，不能按用户请求别名共用票据。

旧单模型 `{enabled, model, ticket_plan}` 只作为读取兼容格式。首次显式保存后转换为模型映射；升级不能自动开启新增模型。

## active / ready 生命周期

- `active` 是新请求可读取的不可变快照；请求完成后的观察只能回报它实际使用的版本。
- `ready` 是已经通过动态代理采集和固定业务代理复验的候选，不得在健康 active 仍可用时立即覆盖。
- active 过期、不可用或同版本连续两次异常后，才允许晋升 ready。
- 迟到旧响应、旧采集任务和旧配置 revision 不得作废或覆盖新版本。
- 续期失败保留仍有效的 active，但不得延长原签发时间对应的有效期。

票据必须执行严格 Base64 URL-safe envelope 解析，校验版本字节、内部签发时间、密文块数和时间窗口。Pro 预期 10 个密文块，Team 预期 12 个密文块；292/332 仅是运营观察长度，不是唯一合法性依据。票据从内部签发时间起有效一小时，未来签发时间最多容忍 30 秒时钟偏差，最后 30 秒不再分配给新请求，提前十分钟续期。

## 客户端 STATE 优先级

出站处理顺序固定为：

1. 客户端 STATE 已知由当前账号铸造时原样保留。
2. 客户端 STATE 已知来自其他账号时剥离，防止 failover 后跨账号回放。
3. 请求没有可用客户端 STATE 时，才注入当前账号和实际出站模型的 active。
4. 显式启用的账号/模型没有 active 时执行 strict 阻断，只排除该账号/模型组合。

strict 是调度硬门，不能被 TTFT Guard fail-open、成本回退或粘性恢复重新放入候选。它不修改账号全局 `schedulable`、健康状态或其它模型的可用性。后台采集失败不会重放正式业务请求。

## 采集、锁与观察

采集先使用共享动态代理获得候选，再使用账号固定业务代理复验实际模型和完整成功响应。HTTP 401、403、429 停止当轮；每轮最多八次尝试，失败冷却五分钟。

多实例通过现有 Redis leader lock 和 PostgreSQL advisory lock 协调。锁键包含账号、规范出站模型、配置 revision 和固定代理指纹。多实例部署中两个锁后端都不可用时跳过当轮，禁止无锁并发采集；单实例测试可使用明确的本地运行边界，但不能把它解释为生产多实例保证。

watchdog 只把完整成功响应转换为观察：实际模型不符或合法异常 STATE 对当前 active 版本累计 strike，正常匹配响应清零；连续两次异常才触发拒绝或 ready 晋升。观察不得修改响应字节，迟到版本不得污染当前状态，日志、管理 API 和审计不得输出票据正文、代理凭据或原始响应体。

## 数据和接口边界

- 私有状态继续保存在 `accounts.extra`，不新增数据库 migration。
- 普通账号编辑必须保留服务端管理字段；创建、导入、复制默认关闭且不复制票据。
- GET/PUT 保持 `/api/v1/admin/accounts/:id/codex-ticket`，手动采集保持 `/codex-ticket/harvest`；多模型采集必须明确 model。
- 列表只返回脱敏摘要；导出、审计和普通详情不得返回 STATE、动态代理凭据、内部 revision 或固定代理指纹。
- 数据库备份包含私有 Extra 时按密钥级数据保护。

## 传输边界

HTTP Responses、compact/compat 路径和 WebSocket-to-HTTP bridge 可以共享 HTTP 票据生命周期。原生上游 WebSocket 暂不支持 STATE 票据隔离：连接建立后已经绑定账号，不能安全地在连接中途更换账号或模型票据。

任何未来原生 WS 支持必须单独设计连接级快照、关闭和重连语义，并增加专项回归；不能因为 HTTP bridge 测试通过就宣称原生 WS 已支持。

## 后续 upstream 重核

官方版本或新 PR 涉及 STATE 时，按以下顺序重核：

1. 对比 PR #7315/#7338 的原始提交和官方最终实现。
2. 分别判断采集、持久化、三模型隔离、active/ready、strict、锁、客户端 STATE 优先级和 WS 边界是否被完整覆盖。
3. 官方完整覆盖的部分改归 upstream 维护；只覆盖一部分时保留 fork 扩展和专项测试。
4. `ccodex-sleep-state` 后续版本只用于设计复核，许可证边界不因实现相似而取消。

机器可读路径、符号和最低测试以 `.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml` 中的 `openai-codex-state-tickets` 为准。
