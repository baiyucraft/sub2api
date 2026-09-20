---
title: Codex STATE 独立插件
description: 独立包、账号模型票据、严格准入和维护更新边界
updated: 2026-09-20
owner: project
---

# Codex STATE 独立插件

`baiyu.codex-state` 位于 `plugins/codex-state/`，独立版本从 `0.1.0` 开始。宿主仍为 `0.2.7-baiyu`。插件默认关闭，不读取或迁移旧 STATE 配置、票据，不自动采集真实账号。

## 维护归属

- 宿主只提供通用资源/身份/代理解析、`AdmitBatch`、流式 `Forward`、`RunAction`、加密持久状态、CAS 和租约；不包含模型名单、套餐块数、续期或打票业务。
- 插件拥有采集、实际模型复验、严格 envelope、active/ready、watchdog、三模型配置和管理 UI。`core.Engine` 负责生命周期，`core.Guard` 绑定账号、模型、配置代次和身份；RPC 适配器不把这些业务类型引入宿主。
- 在清单声明的 SDK 能力范围内，更新插件包即可更新业务；引入新的宿主能力必须先更新宿主。
- 当前仅绑定一个出站插件，不建立插件链。

## 来源

| 来源 | 固定基线 | 采用方式 |
| --- | --- | --- |
| Sub2API PR #7315 | head `3c2f05c957b4b93866318ec8695fc5a28fff70eb`；merge `49a39b6dc1abed30fd227611e8af1108bc427610` | 既有 STATE 基础；由 #7338 父历史带入 |
| Sub2API PR #7338 | head `09e112fb4997be666b10da48c3b5f65fb8d8b53b`；fork merge `a6be2645fffca32a2425d79f822902d7a5aedaff` | 保留已合入作者与提交历史；本次迁移业务所有权 |
| ccodex-sleep-state | `b18fabf9ad8e9d7af7d9d0306b623ba6091a39d6` / v0.4.0 | 严格 envelope、双票据和故障分类的设计参考；GPL，不复制实现或依赖 |
| sub2api-codex-turn-state | `c8e5483e06ab9de199c3848d9d46fccfc063f88a` / v0.1.6 | 插件生命周期、任务取消和被动状态设计参考 |
| sub2api-state-kit | 审阅源码 `9f2d20ba7db0f76a558ce2c28eefb20128eae562`；发布 v0.3.4 单独记录 | 包格式、界面、资源目录和动作设计参考；不把源码 SHA 冒充发布包身份 |

#7338 原始参考点 `26b22196bf68b372d0daad9381f686a3321068d4` 保留为历史来源。完整许可、采用机制、未采用行为与测试映射以插件 `sources.lock.json` 和 `THIRD_PARTY_NOTICES.md` 为准。

## 配置与状态

管理员在插件页集中管理账号/分组筛选、Astra/Sol/Terra 独立开关及 Pro/Team 套餐、动态采集代理、拨号代理、票据状态、冷却和手动动作。保存停用草稿、查询资源、Health、配置校验都不触发采集；插件启用后才启动已开启任务。

每个账号、实际出站模型、配置代次、ChatGPT 身份对应唯一逻辑票据槽。代理 ID、URL、代理组成员不进入票据所有权，更换出口不销毁票据。账号身份改变则隔离旧票据。配置 revision 和受管账号/模型范围由宿主一次提交，插件不能通过陈旧配置绕过新准入。

状态经宿主写入 PostgreSQL 加密表，插件没有数据库或 Redis 凭据。删除保留 tombstone 版本以防 CAS ABA；采集与变更使用数据库时钟租约和持锁代次；过期持有者不能覆盖新结果。旧 Redis KV API 不变，STATE 不使用它作为权威存储。

## 生命周期

- 严格 Base64 URL-safe envelope、版本、内部签发时间和密文块数校验；Pro 10 块、Team 12 块。格式正确仍须实际模型复验。
- 内部签发时间起一小时 TTL，未来时间最多容忍 30 秒，最后 30 秒不分配新请求，提前十分钟续期。
- 每轮最多八次采集，失败冷却五分钟。续期失败不能延长旧票有效期。
- 新票先进入 ready；active 失效、不可用或同版本连续两次异常才晋升，不覆盖仍健康的 active。
- watchdog 只观察完整成功响应。仅 2xx 头、EOF、不完整 SSE 都不算完成；正常目标模型响应清零 strikes。完成事件后的状态记账使用独立的有界收尾上下文。
- 迟到响应只能影响其实际使用的 active 版本；任务取消、配置变化、身份变化和租约代次共同防止旧任务覆盖。

## 客户端与出口

宿主先运行已有客户端 `x-codex-turn-state` 来源守卫：已知跨账号票据剥离，同账号及守卫允许保留的客户端值优先；插件仅在缺失时注入 active。正式请求不改响应正文、不自动重放。

动态代理负责采集，业务代理负责复验。单代理账号使用该代理；代理组按稳定可用成员顺序复验，任一成员成功即可发布账号级票据，组内共享，不创建每代理票据槽。正式转发必须使用宿主已经取得两级并发槽并选定的出口，插件不得更换出口。无可用业务出口时 strict 阻断。

## 准入、停用与升级

- 按渠道、账号和 compact 映射后的实际出站模型准入，普通候选、粘性和回退均受约束，发送前再次验证。
- 拒绝只排除当前请求的账号/模型，不扣上游错误重试预算，不改变账号健康、调度状态、计费或优先级。
- 未受管账号/模型不依赖插件在线。明确停用恢复普通请求并保留配置、状态。
- 插件崩溃、重启、升级保留受管范围并 strict；不得借用停用流程让维护窗口静默放行。
- 专用升级接口验证签名、SDK 和插件 ID，保留旧包及状态，跨实例停止新受管准入并排空在途，失败恢复旧包。请求守卫异常残留不会被假定已经结束：升级应中止，由管理员核实旧实例和请求后恢复维护。
- 配置切换或升级过程中，已可能发送到上游的业务请求不重放。

## 接口与秘密保护

- 管理入口使用 `/api/v1/admin/plugins/:id` 下 `config`、`status`、`resources`、`actions` 和 `upgrade`。
- `GET /api/v1/admin/plugins/:id/resources` 只返回脱敏目录；`POST /api/v1/admin/plugins/:id/actions` 执行显式幂等操作。
- `PUT /api/v1/admin/plugins/:id/config/secrets` 仅由宿主可信表单编辑签名清单声明的秘密字段。iframe 普通配置只收到空值和 configured 布尔标志，不接收代理凭据。
- `plugin.resources` 和 `plugin.action` Bridge 只开放当前插件的管理员操作；动作必须包含幂等 ID。
- UI/状态/日志不返回 STATE、OAuth token、完整代理凭据、请求正文或内部指纹。
- 内置 `codex-ticket` 专用 API、采集器、账号表单和全局网关票据表单退场。
- 历史 `accounts.extra` 票据字段仍做脱敏、导入剥离、普通编辑保留；不批量删除历史数据，也不自动迁移。

## WebSocket

账号存在任一受管模型时走 HTTP bridge，每轮按实际模型决定是否经过插件。已有原生连接遇到新启用受管模型时要求重连。原生上游 WS 票据隔离不在本期范围，不能由 bridge 测试推断已经支持。

## 后续更新

“更新此插件”先按 `plugins/codex-state/MAINTENANCE.md` 检查三个来源的 HEAD、release 和相对锁定 SHA 的差异，再选择性吸收，更新来源与回归映射。不得覆盖本地实现、清空历史来源或自动启用生产采集。

正式 VM Gate 要求干净、已提交且绑定完整 SHA 的源码。本次开发可产出本地测试和插件包，但不以旧 HEAD 或临时伪造身份替代本次 Gate。代理组独立合同见 [OpenAI OAuth 账号代理组](./09-OpenAI-OAuth账号代理组.md)。
