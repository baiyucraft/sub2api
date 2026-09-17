# `sub2新站` 优化能力差异分析与合并计划

> 日期：2026-09-17
> 目标仓库：`E:\project\!byAI\sub2api`
> 分析来源：`E:\project\!byAI\sub2api\.upstream\sub2新站`
> 当前阶段：只读分析与实施规划，尚未开始代码合并、数据库迁移或生产发布。

## 1. 文档目标

本文件用于分析 `sub2新站` 相对当前 Sub2API fork 的优化能力，并规划如何把其中确有价值、经过验证且不破坏当前主线能力的部分合并到当前仓库。

这里的“合并所有优化能力”不表示整文件覆盖或无条件复制，而是遵循以下原则：

1. 只迁移相对当前 fork 确有增益的能力。
2. 当前 fork 已有且实现相同的能力不重复迁移。
3. 新站中已知存在缺陷或落后于当前主线的实现不直接带入。
4. 以当前 `0.2.5-baiyu` 为主干，逐能力、逐调用链进行语义合并。
5. 保留当前 fork 的 RPM、探针、上游同步、平台扩展、调度和监控语义。
6. 每项能力必须有独立测试、回滚边界和可观测性，不能用“编译通过”替代行为验证。

## 2. 基线结论

### 2.1 两个新站目录的关系

已对以下目录执行逐文件 SHA-256 比较：

- `.upstream/sub2新站`
- `.upstream/sub2新站-1`

结果：除 `.codex-artifacts/juheai-astra-20260915/assets/speed-light.jpg` 外，业务代码、测试、迁移和前端源码完全一致。因此后续只以 `.upstream/sub2新站` 作为分析源，不再把 `sub2新站-1` 当作独立实现。

### 2.2 版本关系

| 代码基线 | 版本 |
|---|---|
| `sub2新站` | `0.2.4` |
| 当前官方主线基线 | `0.2.5` |
| 当前 fork | `0.2.5-baiyu` |

`sub2新站` 包含一批当前 fork 没有的账号保护、智能测试和动态流量能力，但它基于较旧主线。不能把新站的 OpenAI、WebSocket、Ent 或管理后台文件整块覆盖到当前仓库，否则会丢失当前版本的官方修复和 fork 扩展。

### 2.3 源码规模差异

| 范围 | 相同文件 | 内容不同 | 仅新站存在 | 仅当前 fork 存在 |
|---|---:|---:|---:|---:|
| `backend/internal` | 1776 | 528 | 289 | 430 |
| `backend/migrations` | 227 | 11 | 75 | 134 |
| `frontend/src` | 559 | 243 | 61 | 143 |

这些数字包含测试和历史演进文件，不能直接等同于需要移植的业务文件数。预计真正的有效新增/修改约为 3,000～6,000 行，但需要人工审查的关联代码可能超过 20,000 行。

## 3. 当前 fork 已有、无需重复迁移的能力

### 3.1 `X-Codex-Turn-State` 协议兼容

当前 fork 与新站的 `backend/internal/service/openai_codex_turn_state.go` 文件哈希一致，已经具备：

- 从 OpenAI 上游响应读取 `X-Codex-Turn-State`；
- 将该响应头显式返回下游客户端；
- 按 `API Key ID + 客户端原始 session ID` 记录状态来源账号；
- 同账号、同会话后续请求保留客户端回带值；
- failover 换账号时剥离由旧账号铸造的状态；
- 延迟提交响应头时，只有真正向客户端提交后才记录来源；
- HTTP、Responses、兼容 Messages、WebSocket 相关路径接入。

这部分不是新站独占能力，不需要再次合并。

### 3.2 当前 fork 已有的基础能力

- Codex 指纹模式与持久化种子；
- OpenAI Responses、Chat、Anthropic 兼容转发；
- WebSocket 会话状态与连接池；
- `previous_response_id`、session 和账号粘性；
- 账号级固定窗口 RPM 准入；
- 账号并发、sticky、熔断与失败切换；
- OpenAI 请求重试和上游错误归类；
- TLS 指纹 profile 基础设施；
- 当前主线后续加入的 WebSocket reader loop、ping/保活和相关修复。

## 4. 新站相对当前 fork 的主要优化能力

### 4.1 账号保护策略编排

新站提供完整的管理员保护策略系统，包括多套预设、策略预览、原子应用、一键还原、批量启用、适用性校验和受保护字段 ownership。

核心文件：

- `backend/internal/service/account_mode1_protection.go`
- `backend/internal/service/account_protection.go`
- `backend/internal/service/anti_degrade_strategies.go`
- `backend/internal/handler/admin/anti_degrade_handler.go`
- `frontend/src/components/account/EditAccountModal.vue`

合并价值：高。它能把身份、TLS、并发、代理和语义保护从散落配置提升为可审计的账号级合同。

### 4.2 HTTP、WSS 与账号测试的统一身份收敛

新站让 HTTP 请求头、请求体、原生 WebSocket 握手、后续 turn 和账号测试使用同一套 installation/session/thread 身份；同时处理字段别名，并同步修改 `client_metadata`、`x-codex-turn-metadata` 和必要的 `prompt_cache_key`。

需要迁移的是身份解析和应用的共享纯函数及各路径调用点，不能覆盖整个网关文件。

合并价值：高。这是解决握手头与请求体暴露不同客户端身份的核心能力。

### 4.3 Mode1 请求语义完整性守卫

新站保存转换前请求，并在真正发起上游请求前比较最终语义，覆盖 model、input、instructions、reasoning、tools、tool_choice、parallel_tool_calls、previous_response_id、token budget、session、functions 和 function_call 等字段。

发现不允许的损失时返回 `MODE1_LOSSY_TRANSFORM`。同时包含字符串 input、system/instructions、functions/tools、function_call/tool_choice 等语义等价规范化。

核心文件：

- `backend/internal/service/openai_mode1_integrity.go`
- `backend/internal/service/openai_mode1_semantics.go`

合并价值：高，但必须先建立当前 fork 的协议转换基线测试，避免合法转换误拦截。

### 4.4 保护模式下禁止破坏语义的自动恢复

保护模式禁止通过删除 encrypted reasoning 或其他重要字段进行自动恢复。该能力强调请求保真，但会降低部分请求的自动恢复成功率，应作为账号策略而非全局强制行为。

### 4.5 连接池按保护身份和传输配置隔离

新站扩展连接兼容键，纳入会话、线程、conversation ID、账号、代理、TLS 模板摘要和传输配置快照，避免不同身份共享底层连接或配置修改后继续复用旧连接。

合并价值：高，但必须在当前主线 WS reader/ping 实现上增量加入，禁止用新站旧版 `openai_ws_pool.go` 覆盖当前文件。

### 4.6 Node.js 24 风格 TLS 兼容传输

新站增加固定 TLS 模板，支持直连、HTTP/HTTPS CONNECT、SOCKS5/SOCKS5h、HTTP/WSS 共用策略，以及按账号、代理和模板摘要隔离连接池。

边界：该模板未证明与当前官方 Codex ClientHello 完全一致，也不能承诺防封、规避风控或提升模型质量。

合并价值：中高。应作为可选账号策略接入，默认不得改变现有账号传输。

### 4.7 动态账号流量控制

新站在固定 RPM 之外增加 Redis 滑动窗口、burst 令牌桶、动态并发、429/5xx 反馈降速、稳定恢复、HTTP/WS turn/测试共享控制器和流量等待。

核心文件：

- `backend/internal/service/account_traffic_service.go`
- `backend/internal/service/account_traffic_policy.go`
- `backend/internal/service/account_traffic_events.go`
- `backend/internal/service/account_traffic_ws.go`
- `backend/internal/repository/account_traffic_cache.go`

当前 fork 已有账号 RPM，必须统一而不是叠加两套计数器，最终只能有一个真实上游尝试前的原子准入点。

### 4.8 智能能力测试系统

新站新增鹈鹕 SVG、糖果推理、测试设置、批量入队、幂等、租约、取消、历史重评、答案与格式分离、SVG 安全预览、管理员面板、用户能力投影，以及与业务请求共享账号容量的完整模块。

核心范围：

- `backend/internal/service/intelligent_test_*.go`
- `backend/internal/repository/intelligent_test_*.go`
- `backend/internal/handler/admin/intelligent_test_handler.go`
- `backend/migrations/247_intelligent_tests.sql`
- `backend/migrations/248_intelligent_assessment_v2.sql`
- `frontend/src/views/admin/IntelligentTestsView.vue`
- `frontend/src/components/admin/intelligent-tests/`

合并价值：中高。模块相对独立，适合在账号保护和统一准入完成后单独合并。

### 4.9 保护感知的账号编辑和代理约束

新站增加受策略管理字段锁定、防止普通保存覆盖保护配置、防止固定出口账号切换随机代理、事务内代理模式复核、流量设置独立保存、有效并发展示，以及策略预览/应用/还原交互。

### 4.10 保护运行态和诊断信息

新站生成账号当前有效执行快照，包括真实身份模式、模型、prompt 摘要、TLS 模板、代理、并发/RPM、插件覆盖结果和保护状态，避免后台显示与真实执行配置不一致。

## 5. 关于“多地区 IP + 智商测试 + Turn State 轮换”的核对

用户提出的方法如下：

```text
多个地区 IP 发起智商测试
        ↓
挑选未降智结果
        ↓
保存响应头 X-Codex-Turn-State
        ↓
后续业务请求复用
        ↓
40～45 分钟有效期内轮换
```

### 5.1 当前实际实现

| 子能力 | 新站状态 |
|---|---|
| 鹈鹕/糖果能力测试 | 已实现 |
| 使用账号当前固定代理执行测试 | 已实现 |
| 同账号自动遍历多个地区代理 | 未实现 |
| 测试记录保存 proxy/IP/region | 未实现 |
| 捕获普通上游响应的 Turn State | 已实现 |
| 智能测试保存响应头 Turn State | 未实现 |
| 将测试评估结果与 Turn State 绑定 | 未实现 |
| 保存优质 Turn State 候选池 | 未实现 |
| 40～45 分钟随机 TTL | 未实现 |
| 有效期内轮换候选 | 未实现 |
| 跨账号复用 | 明确阻止 |

### 5.2 实际 Turn State TTL

新站默认：

- `sticky_session_ttl_seconds = 3600`
- `sticky_response_id_ttl_seconds = 3600`

即本地状态默认保留 60 分钟，不是 40～45 分钟，也没有 TTL 抖动和轮换任务。该 TTL 是本地粘性期限，不能证明 OpenAI 上游状态真实有效期。

### 5.3 当前测试与代理的关系

智能测试直接使用账号当前固定代理：

```text
账号配置代理 → 测试沿用该代理
账号没有代理 → 测试直连
```

它不会为一个账号自动遍历美国、德国、日本、新加坡等代理。新站的保护模型反而要求 Mode1 使用固定出口或直连，并阻止保护账号切到随机代理。

### 5.4 合并范围结论

现有 Turn State 透传和跨账号防污染已经在当前 fork 中存在，不需要从新站迁移。

“多地区测智商后挑选 Turn State”属于新的实验性能力，不能作为“从新站直接合并”的内容。若未来实施，必须单独设计并验证：

- Turn State 是否真的影响模型能力；
- 是否绑定账号、出口 IP、installation/session/thread 身份或模型；
- 跨 IP 回放是否安全；
- 真实有效期；
- 质量评估的统计显著性；
- 如何避免测试结论污染生产调度；
- 如何加密保存或只保存在短期缓存中；
- 如何在 failover 时禁止把旧账号状态带到新账号。

在没有实验数据前，不应把它描述为“防降智机制”。

## 6. 新站中不应直接合入的内容

### 6.1 旧版主线文件

新站基于 `0.2.4`。以下类型文件禁止整块覆盖：

- OpenAI gateway 大文件；
- OpenAI scheduler；
- WebSocket pool/forwarder；
- HTTP upstream；
- Ent 生成代码；
- Wire 生成代码；
- 账号管理大组件；
- 配置文件和完整迁移目录。

必须以当前 fork 文件为底稿逐块合并。

### 6.2 已知缺陷

新站审查材料已记录：

- 普通保存可能回写旧流量策略；
- 独立保存与页脚保存可能同时执行；
- 页脚保存可能丢弃未保存流量草稿；
- 切换保护策略可能覆盖未保存并发编辑；
- 隐藏的无效 RPM 仍可能阻止关闭功能；
- 旧状态刷新可能覆盖刚保存的新状态；
- 嵌套弹窗 Esc 和滚动锁存在问题；
- 只观察模式在配置变化后可能拒绝请求；
- 部分 WS 结束事件漏记上游 429；
- 调低 RPM 后 `Retry-After` 可能低估等待时间；
- Grok 实时语音 RPM 按连接而非 turn 计数；
- 非 mode1 策略的保存保护和还原语义不完全一致。

合并时必须先把这些问题转化为失败测试，不能把缺陷一并复制。

### 6.3 不可验证的宣传性结论

以下说法没有代码或真实对照实验支持，不作为验收标准：

- 一定防封号；
- 一定规避 OpenAI 风控；
- 一定不降智；
- TLS 模板等同官方 Codex 当前客户端；
- Turn State 能固定模型智力；
- HTTP 200 表示保护策略有效。

## 7. 当前 fork 必须保留的能力

合并期间不得回归：

- 账号级 RPM 及其 OpenAI/Responses/Chat/Images/WS 准入链；
- 上游账号探针最小输入 Token；
- 探针公平轮询和孤儿 Key 过滤；
- 上游账号生命周期、同步、绑定和归档；
- 普通账号与上游账号分组优先星标；
- 自适应国产平台协议；
- Kimi、智谱、DeepSeek、MiniMax 等平台扩展；
- OpenCode Go 及当前官方 `0.2.5` 能力；
- 当前 WS reader/ping/保活实现；
- 渠道状态 V1/V2 和渠道监控扩展；
- OAuth 复制、刷新、冷却和凭据语义；
- 上游账号 RPM、并发、健康、倍率和编辑白名单；
- 已撤回的“Key 模型级平台路由”不得复活；
- 一 Key 一物理账号的现有合同。

## 8. 推荐合并架构

```text
当前 0.2.5-baiyu 主干
        │
        ├── 保留现有 Turn State、RPM、WS、调度和 fork 扩展
        │
        ├── 新增账号保护领域模型
        │       ├── 策略预览
        │       ├── 原子应用/还原
        │       └── 字段 ownership
        │
        ├── 增量增强 OpenAI 执行链
        │       ├── HTTP/WSS 身份统一
        │       ├── 请求语义守卫
        │       ├── 连接池兼容键
        │       └── 可选 TLS 模板
        │
        ├── 扩展现有账号 RPM
        │       ├── burst
        │       ├── 动态并发
        │       └── 429/5xx 反馈
        │
        └── 最后加入智能测试与管理后台
```

## 9. 分阶段实施计划

### 阶段 0：冻结基线与差异清单

1. 记录当前 fork HEAD、官方 upstream SHA 和工作区状态。
2. 生成能力级文件清单，不使用整目录 diff 作为实施清单。
3. 为每项能力标记：当前已有、新站独有、双方不同、当前主线更新或不应合并。
4. 将 fork 新增长期合同登记到 `sub2api-fork-extension-audit` 和 `.wiki`。
5. 检查当前最新 migration/profile，迁移编号必须在实施时重新分配。

交付物：能力矩阵、文件矩阵、测试矩阵、迁移计划。

### 阶段 1：账号保护领域模型

1. 引入保护角色和策略 DTO。
2. 明确每个配置字段的 owner：普通编辑、保护策略、流量策略或系统派生。
3. 实现策略预览。
4. 实现事务内原子应用和还原。
5. 防止无标记旧快照覆盖新保护状态。
6. 先完成后端测试，不接前端按钮。

验收：应用失败不留下半开启状态；普通保存不能覆盖保护字段。

### 阶段 2：统一 Codex 身份

1. 抽取身份解析与改写纯函数。
2. 对齐 HTTP、透传 HTTP、HTTP→WS、原生 WS、账号测试。
3. 保证请求头与 `client_metadata` 一致。
4. 兼容字段别名。
5. 保留 API Key 隔离与账号种子生命周期。
6. 补充真实本地 HTTP/WSS 回显测试。

验收：同一请求在所有传输层暴露相同 installation/session/thread 身份。

### 阶段 3：请求语义完整性守卫

1. 为当前 fork 建立转换前/后的语义规范化模型。
2. 先加入 observe-only 模式，只记录差异。
3. 修复合法等价转换误报。
4. 再为指定保护策略开启 enforce 模式。
5. 明确 encrypted reasoning 自动恢复策略。

验收：合法请求不误拦截；真实字段丢失可稳定返回 `MODE1_LOSSY_TRANSFORM`。

### 阶段 4：连接池隔离与 TLS 传输

1. 在当前 WS pool 上增加身份/代理/TLS 兼容键。
2. 保留当前 reader loop、ping、保活和故障恢复。
3. 引入可选 Node.js 24 风格 TLS profile。
4. 支持直连和代理隧道。
5. 修改配置时只影响新连接，不强杀在途请求。
6. 不支持 ALPN 时明确报错。

验收：不同账号、代理、身份或 TLS 配置不会错误共享连接。

### 阶段 5：统一流量控制

1. 以当前账号 RPM 准入为唯一主入口。
2. 评估固定窗口升级为滑动窗口的兼容性和 Redis 成本。
3. 增加 burst 控制。
4. 增加动态并发状态机。
5. 429/5xx 驱动降速，成功窗口驱动恢复。
6. HTTP、Images、WS turn、兼容协议和账号测试统一调用。
7. Redis 故障保持现有 fail-open 合同。

验收：一次真实上游尝试只计数一次；失败切换各账号分别计数；并发槽不泄漏。

### 阶段 6：智能能力测试

1. 按当前 migration/profile 分配新迁移编号，不能复用新站的 `247/248`。
2. 引入测试设置、记录、队列和幂等请求表。
3. 接入鹈鹕与糖果评估器。
4. 测试执行使用只读账号快照。
5. 与业务请求共享账号并发和 RPM，但不改变账号健康状态。
6. 实现管理员列表、历史、取消、重评和公开能力投影。
7. 原始响应严格脱敏并限制长度。

验收：测试不会刷新 OAuth、切换代理、修改账号或污染生产调度。

### 阶段 7：管理后台

1. 增加保护策略预览、应用和还原入口。
2. 增加有效身份/TLS/代理/流量运行态。
3. 增加智能测试页面。
4. 将普通账号保存与保护/流量保存互斥。
5. 修复新站已知的草稿覆盖、并发保存、隐藏校验和嵌套弹窗问题。
6. 中英文文案明确“兼容策略”，不承诺模型质量。

验收：保存时无竞态；后台显示值与真实执行配置一致。

### 阶段 8：全量验证与文档沉淀

验证顺序：

1. fork 扩展审计；
2. migration/profile/version 合同；
3. 账号保护定向测试；
4. HTTP/WSS 身份一致性测试；
5. 请求语义守卫测试；
6. RPM、burst、动态并发和失败切换测试；
7. Responses、Chat、Messages、Images、WS 全路径测试；
8. 智能测试队列、评估和公开接口测试；
9. 前端定向测试；
10. 完整 Go 测试；
11. unit-tag 测试；
12. 完整 Vitest；
13. TypeScript 类型检查；
14. ESLint；
15. 前端生产构建；
16. `git diff --check`；
17. VM Gate。

实现完成后，将稳定合同整理到 `.wiki`，删除或归档一次性阶段材料。本文件保留为阶段性分析与迁移说明。

## 10. 测试重点

### 10.1 身份与连接

- HTTP、WSS、账号测试产生一致身份；
- 同账号同会话身份稳定；
- 不同 API Key 隔离；
- 不同账号、代理和 TLS 配置不复用连接；
- failover 后旧 Turn State 不进入新账号；
- 配置修改不影响已有在途连接；
- 连接池兼容键变化后新请求使用新连接。

### 10.2 语义完整性

- 字符串 input 与 message 数组等价；
- system 与 instructions 等价；
- functions/tools、function_call/tool_choice 等价；
- tools、reasoning、token budget 的真实丢失可检测；
- observe-only 永不拦截；
- enforce 只在指定账号生效；
- 禁止语义降级时不执行破坏性自动恢复。

### 10.3 流量控制

- RPM=0 时不限流；
- burst 独立生效；
- 动态并发可以降低并逐步恢复；
- HTTP、Images 与 WS turn 统一计数；
- 智能测试与业务请求共享容量；
- Redis 故障 fail-open；
- `Retry-After` 不低估真实等待窗口；
- failover 每个实际账号尝试分别计数；
- 最终准入失败不会泄漏并发槽。

### 10.4 智能测试

- 队列公平；
- 容量不足时延后而非发送上游；
- 取消只对未执行任务作确定性保证；
- 评估答案和格式分离；
- SVG 不允许脚本、外部资源和主动内容；
- 原始响应脱敏并受长度限制；
- 旧记录可以按新规则重评；
- 测试不改变账号配置、代理、OAuth 或调度状态；
- 显式模型不会被静默替换；
- 流响应必须出现真实终态，不能把 EOF 当作成功。

## 11. 发布边界

本计划完成前不得：

- 直接覆盖新站大文件；
- 使用新站旧 migration 编号；
- 修改生产数据库；
- 自动启用已有账号保护策略；
- 默认切换所有 OpenAI 账号 TLS；
- 把智能测试结果直接用于生产调度；
- 将 Turn State 当作模型质量凭证；
- 发布“防封”或“绝不降智”承诺；
- 覆盖当前工作区已有的无关改动。

正式发布应按能力分批，每批独立 commit、VM Gate、备份、迁移检查、灰度和回滚验证。

## 12. 验收标准

1. 新站相对当前 fork 的有效优化能力均有明确处理结论：已迁移、当前已有、拒绝迁移或延期研究。
2. 当前 fork 已有的 Turn State、RPM、探针、上游同步和平台能力不回归。
3. 新增保护策略应用和还原具备原子性。
4. HTTP、WSS 和账号测试身份一致。
5. 语义守卫不会误拦截已确认的合法等价转换。
6. 流量控制只有一套权威计数链。
7. 智能测试不修改账号配置或生产调度状态。
8. 新站已知缺陷均有回归测试或明确不迁移。
9. migration、profile 和 version 合同完整。
10. 完整后端、前端、构建和 VM Gate 通过。
11. 不直接发布生产，由后续单独发布计划执行。

## 13. 建议实施顺序

按风险和收益排序：

1. 账号保护领域模型与原子应用；
2. HTTP/WSS 身份统一；
3. 语义完整性 observe-only；
4. 连接池兼容键；
5. 可选 TLS 模板；
6. burst 和动态并发；
7. 语义完整性 enforce；
8. 智能能力测试；
9. 管理后台完整交互；
10. 另立研究项目验证“多地区 IP + Turn State + 质量轮换”。

不建议把最后一项与本轮迁移混在一起，因为新站没有现成实现，且该假设尚未被证明。

## 14. 初步能力处置矩阵

| 能力 | 当前 fork | 新站 | 处置建议 |
|---|---|---|---|
| Turn State 捕获与同会话复用 | 已有且核心文件相同 | 已有 | 保留当前实现 |
| 跨账号 Turn State 防污染 | 已有 | 已有 | 保留当前实现 |
| 账号保护策略编排 | 无完整系统 | 完整但有边界缺陷 | 修复后迁移 |
| HTTP/WSS 统一身份 | 部分具备 | 覆盖更完整 | 增量迁移 |
| Mode1 语义完整性守卫 | 无完整实现 | 已有 | 先 observe-only 后 enforce |
| 保护感知自动恢复 | 无策略化控制 | 已有 | 策略化迁移 |
| 连接池保护身份隔离 | 部分具备 | 更完整 | 在当前 WS 主线上增量迁移 |
| Node.js 24 TLS 模板 | 有 profile 基础 | 有固定兼容模板 | 可选迁移，不默认开启 |
| 账号 RPM | 已有固定窗口和统一准入 | 另有流量控制器 | 复用当前 RPM，不并存两套 |
| burst 令牌桶 | 无 | 有 | 迁移到当前 RPM 组件 |
| 动态并发 | 无完整反馈状态机 | 有 | 修复边界后迁移 |
| 429/5xx 流量反馈 | 部分调度逻辑 | 更系统化 | 与当前熔断整合 |
| 智能测试 | 无完整模块 | 完整 | 独立阶段迁移 |
| 多地区 IP 测试 | 无 | 无 | 不属于本轮直接迁移 |
| 智商结果绑定 Turn State | 无 | 无 | 单独研究 |
| 40～45 分钟 Turn State 轮换 | 无 | 无 | 单独研究 |
| 管理后台保护交互 | 无完整入口 | 有但存在竞态 | 重构后迁移 |

## 15. 下一步

正式实施前应先完成阶段 0，输出可执行的逐文件/逐符号任务列表，并再次让维护者确认范围。本文只定义差异、边界和总体路线，不授权直接修改业务代码、迁移数据库或发布生产。
