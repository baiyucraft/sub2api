---
title: OpenAI OAuth 账号代理组
description: 一账号多代理、两级并发、会话粘性、负载均衡与账号级 STATE 合同
updated: 2026-09-22
owner: project
---

# OpenAI OAuth 账号代理组

本页记录 `openai-oauth-proxy-groups` 的长期维护合同。设计参考 [Go1c/sub2api PR #423](https://github.com/Go1c/sub2api/pull/423)，代码按当前 `0.2.7-baiyu` 的账号、调度、并发和 Codex STATE 边界独立适配；该 PR 不是当前 upstream 正式事实，后续不能据此把本扩展误归为 upstream。

## 数据与适用范围

迁移 `279_proxy_ip_groups.sql` 新增代理组、成员关系和 `accounts.proxy_ip_group_id`。账号的 `proxy_id` 与 `proxy_ip_group_id` 互斥，一个账号最多绑定一个代理组，一个代理可以加入多个组。代理组的 `per_ip_concurrency` 默认 `10`，合法范围 `1–1000`。

代理组只允许 OpenAI OAuth 和 Setup Token 账号使用。停用、过期或删除的代理不参与出站；使用中的代理组不得删除。空组或没有可用成员时禁止直连，返回 HTTP 503，稳定业务错误码为 `openai_proxy_group_no_egress`。账号响应只返回组名、成员数量、代理标签和并发摘要，不返回密码、完整 URL、STATE 或内部指纹。

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

账号 `concurrency` 是组内全部出口共享的总闸；组级 `per_ip_concurrency` 是每个 `account_id + proxy_id` 的附加上限。Redis 成员槽键为 `concurrency:account-proxy:{accountID}:{proxyID}`，标签名称只用于展示，不能进入并发或调度身份。

可靠 `SessionHash` 使用 `账号 ID + SessionHash` 绑定成员，TTL 对齐现有会话粘性。同一会话保留首次可用代理；并发新请求通过 Redis Lua 原子 claim 决定首次绑定的唯一赢家，后续竞争者只续期且必须服从已有绑定。失效绑定使用“代理 ID 匹配才删除”的比较删除，避免迟到请求删掉其他请求刚建立的新绑定。绑定成员未满时不因其他成员更空闲而迁移；绑定成员仅并发满、失效或移出组时才重新选择。新会话批量读取组内成员实时并发，按当前占用最少优先选择；相同负载使用 `SessionHash` 稳定打散，无 `SessionHash` 的管理请求使用进程内轮转起点。并发快照失败时回退到现有成员顺序尝试，但最终槽位准入仍由 Redis 原子操作决定。没有可靠 SessionHash 时按请求选择且不写绑定。WebSocket 在握手阶段固定出口，连接建立后不切换。

代理组绑定或成员并发 Redis 能力不可用时必须 fail-closed，普通单代理账号不受影响。所有成员均满载时先释放账号总槽，再把结果作为请求级容量失败交给现有账号容量换号；该过程不修改账号优先级、分组优先池、健康或上游错误 failover 语义。

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
- migration 279 的原始文件 SHA-256 登记在 `migration_contracts`，当前 pending profile 254 同时包含 migration 278 和 279。
- 最低回归必须覆盖代理组 CRUD、账号类型与互斥校验、会话粘性、两级并发、容量换号、Redis fail-closed、各 OpenAI HTTP 入口和 WS 握手、账号级 STATE、导入废弃字段及脱敏展示。
- upstream 后续出现相同功能时，按数据结构、调度边界、STATE 所有权和失败语义逐项判断完整或部分覆盖，不能只按 PR 标题删除本扩展。
