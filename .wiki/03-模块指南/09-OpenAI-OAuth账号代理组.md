---
title: OpenAI OAuth 账号代理组
description: 一账号多代理、两级并发、会话粘性、负载均衡与账号级 STATE 合同
updated: 2026-09-28
owner: project
---

# OpenAI OAuth 账号代理组

本页记录 `openai-oauth-proxy-groups` 的长期维护合同。设计参考 [Go1c/sub2api PR #423](https://github.com/Go1c/sub2api/pull/423)，代码按当前 fork 的账号、调度、并发和 Codex STATE 边界独立适配；该 PR 不是当前 upstream 正式事实，后续不能据此把本扩展误归为 upstream。

## 数据与适用范围

`279_proxy_ip_groups.sql` 是不可变历史迁移：它新增代理组、成员关系和旧版 `accounts.proxy_ip_group_id`，并建立了账号单代理/代理组互斥语义。统一绑定迁移 `284_unified_proxy_bindings.sql` 已纳入当前 pending profile 257，其原始 SHA-256 为 `de0adde07c5dd930aa1cccf9e68d6c55958fd7ea16323d746019c7c97481afe3`；迁移和运行时代码仍需测试与发布验证。在 284 的测试和发布门禁完成前，不能把目标合同当作已部署行为。

目标合同为统一使用正数 `proxy_id`：真实代理和代理组共用一套只增不复用的绑定 ID，账号表只保存 `proxy_bindings.id`。统一绑定表区分 `binding_type=proxy` 与 `binding_type=proxy_ip_group`，并分别指向真实代理或代理组。代理组管理自己的主键仍保留；`proxy_ip_group_id` 只用于代理组目录/管理身份和旧请求兼容输入，不再作为账号持久化绑定字段。代理组的 `per_ip_concurrency` 默认 `10`，合法范围 `1–1000`。

284 的迁移必须在事务内为现有真实代理和代理组建立唯一绑定，迁移历史账号且不复用已删除绑定 ID；并发创建必须通过数据库共享序列或等价的持久化分配器，不能用“当前最大值 + 1”。历史 279 的 SQL、checksum、profile 和已应用记录均不可改写。

代理组只允许 OpenAI OAuth 和 Setup Token 账号使用。停用、过期或删除的代理不参与出站；使用中的代理组不得删除。空组或没有可用成员时禁止直连，返回 HTTP 503，稳定业务错误码为 `openai_proxy_group_no_egress`。账号响应只返回组名、成员数量、代理标签和并发摘要，不返回密码、完整 URL、STATE 或内部指纹。

## 管理端混合代理目录

沿用原生 `GET /api/v1/admin/proxies` 与 `GET /api/v1/admin/proxies/all`，不新增路由。统一绑定目标下，两个接口默认返回真实代理与代理组绑定行的混合目录；分页接口的 `total` 应计入代理组行，`/all?with_count=true` 仍保留计数分支。真实代理的管理员 DTO 原有凭据字段不得因混合目录而扩大到代理组行。

| 行类型 | `id` | `binding_type` | `proxy_ip_group_id` |
| --- | --- | --- | --- |
| 真实代理 | 正数真实代理 ID | `proxy` | `null` 或省略 |
| 代理组绑定行 | 正数且与真实代理共用递增绑定序列 | `proxy_ip_group` | 正数代理组管理 ID |

代理组行是绑定选择器，不是单个成员代理记录，不得承载成员代理的密码、完整 URL、STATE 或内部指纹，也不得传给真实代理详情、测试、编辑、删除和出站操作。新客户端必须依据 `binding_type` 分流；正数 `id` 不能再单凭数值判断为真实代理，必须先按绑定目录解析类型。负数代理组 ID 只保留为一次性旧请求输入兼容，原生代理列表不再返回负数。

### 账号创建/更新输入

统一合同下创建、更新和导入都提交 `proxy_id`。服务层解析绑定表后，真实代理和代理组都写入同一账号字段；`proxy_id = 0` 显式清除，更新请求省略字段则保持原绑定。旧客户端提交的 `proxy_ip_group_id` 或负数 `proxy_id` 只在请求归一化阶段转换为统一正数绑定 ID，不能继续持久化。旧字段与 `proxy_id` 指向不同绑定时拒绝，不能静默选择一方。代理组仍需通过存在性、OpenAI OAuth/Setup Token 类型和互斥校验。

兼容归一化只针对账号创建、更新和导入绑定，不自动扩展到 OAuth 授权阶段、真实代理 CRUD、批量代理操作或其它消费者。真实代理操作必须先确认解析结果为 `binding_type=proxy`，代理组绑定不得误入真实代理 CRUD。A 管理页可按 `binding_type` 过滤目录；B 客户端省略该参数即可获得混合目录。

## 出站与两级并发

```text
调度选中账号
    -> 获取账号总并发槽
    -> 单代理：沿用原出口
    -> 代理组：解析当前会话成员
    -> 获取 account_id + proxy_id 的成员槽
    -> 发起请求
    -> 按相反顺序释放两级槽
```

账号 `concurrency` 是组内全部出口共享的总闸；组级 `per_ip_concurrency` 是每个 `account_id + 成员真实代理 ID` 的附加上限。Redis 成员槽键为 `concurrency:account-proxy:{accountID}:{proxyID}`，此处 `proxyID` 始终是成员真实代理 ID，不是账号绑定 ID；标签名称只用于展示，不能进入并发或调度身份。

可靠 `SessionHash` 使用 `账号 ID + SessionHash` 绑定成员，TTL 对齐现有会话粘性。同一会话保留首次可用代理；并发新请求通过 Redis Lua 原子 claim 决定首次绑定的唯一赢家，后续竞争者只续期且必须服从已有绑定。失效绑定使用“代理 ID 匹配才删除”的比较删除，避免迟到请求删掉其他请求刚建立的新绑定。绑定成员未满时不因其他成员更空闲而迁移；绑定成员仅并发满、失效或移出组时才重新选择。新会话批量读取组内成员实时并发，按当前占用最少优先选择；相同负载使用 `SessionHash` 稳定打散，无 `SessionHash` 的管理请求使用进程内轮转起点。并发快照失败时回退到现有成员顺序尝试，但最终槽位准入仍由 Redis 原子操作决定。没有可靠 SessionHash 时按请求选择且不写绑定。WebSocket 在握手阶段固定出口，连接建立后不切换。

代理组绑定或成员并发 Redis 能力不可用时必须 fail-closed，普通单代理账号不受影响。所有成员均满载时先释放账号总槽，再把结果作为请求级容量失败交给现有账号容量换号；该过程不修改账号优先级、分组优先池、健康或上游错误 failover 语义。
OAuth 刷新、配额及刷新后的隐私设置同样必须解析组内真实出口；组出口不可用时不得退化为直连。流式连接熔断只按已选中的真实成员代理 ID 记账，账号保存的组绑定 ID 不能作为物理代理熔断键。

## 账号级 STATE

STATE 所有权固定为：

```text
account_id + canonical outbound model + config revision + ChatGPT identity
```

代理 ID、代理 URL、代理组 ID和成员集合不进入票据所有权或有效性。单代理账号使用该出口复验；代理组账号在动态采集代理取得候选后，按稳定成员顺序使用可用代表出口复验，任意一个代表成功即可发布该账号模型的 active/ready。组内全部成员复用同一票据，不保存每代理票据、strike 或兼容状态。

切换单代理、绑定代理组或调整成员不自动销毁票据；OAuth/Setup Token 对应的 ChatGPT 身份变化必须使旧票据失效。代理组无可用业务出口时，已启用模型保持 strict 阻断。完整票据生命周期见 [Codex STATE 票据](./08-Codex-STATE票据.md)。

## 导入与历史副本

管理员导入不再按代理复制账号。只有批次全部为 OpenAI OAuth/Setup Token 时才允许统一选择代理组；选择后忽略导入文件中的单代理绑定，每个源账号只创建一条记录。并发、倍率、优先级、指纹模式、账号分组和优先星标覆盖仍保留。

`copy_proxy_ids` 的新建语义已经删除，仅作为旧协议拒绝字段保留。旧客户端提交非空值时固定返回 `COPY_PROXY_IMPORT_DEPRECATED`，不得静默改成单条导入或继续复制。新导入不再写 `codex_import_replica_fingerprint_seed`；指纹模式覆盖仍保留，但每个新账号独立生成账号级 seed。历史字段不做 migration 清理、不能由管理请求覆盖，也不再参与设备、会话或线程身份派生；历史复制账号不自动归并、停用或删除，管理员需显式选择保留账号并建立代理组。

## 维护与回归

- 机器可读合同：`.agents/skills/sub2api-fork-extension-audit/references/extensions.yaml` 中的 `openai-oauth-proxy-groups`。
- migration 279 的原始文件 SHA-256 登记在 `migration_contracts`，历史 profile 254 对其的登记、checksum 和已应用记录不可修改。统一绑定迁移 `284_unified_proxy_bindings.sql` 已登记到当前 pending profile 257，原始字节 SHA-256 为 `de0adde07c5dd930aa1cccf9e68d6c55958fd7ea16323d746019c7c97481afe3`；修改 SQL 须重算，不得误称已发布。
- 历史 profile 256 保留原合同；由于已检查目录不足以证明它从未签名或发布，当前 pending profile 257（parent 256）承载 284。不得回写 254/255 或 279；迁移必须先移除旧 proxy_id FK 和互斥 CHECK，再回填组账号。
- 最低回归必须覆盖代理组 CRUD、账号类型与互斥校验、会话粘性、两级并发、容量换号、Redis fail-closed、各 OpenAI HTTP 入口和 WS 握手、账号级 STATE、导入废弃字段及脱敏展示。
- 新增统一绑定回归：两个原生列表默认返回真实/代理组绑定行及类型字段，分页 `total`、`with_count`、正数绑定解析、旧负数/旧 `proxy_ip_group_id` 请求归一、零值清空、类型/组存在性/互斥拒绝、删除后不复用 ID 和绑定行不进入真实代理操作。
- upstream 后续出现相同功能时，按数据结构、调度边界、STATE 所有权和失败语义逐项判断完整或部分覆盖，不能只按 PR 标题删除本扩展。
