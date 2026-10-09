---
title: OpenAI Sol 分布可信探针
description: 单次短题、持久化 128 次滑动窗口及独立行为匹配展示
updated: 2026-10-09
owner: project
---

# OpenAI Sol 分布可信探针

OpenAI API Key 账号的真实探针每次只发出一个请求，固定声明模型 `gpt-6.1-sol`。统计结果用于管理页面展示与独立告警。它不改变账号健康状态、调度权重、停用策略或可用性。网络、鉴权和协议错误继续进入现有健康处理。

## 请求与采样合同

每个 Key 检测系列只生成一次随机题序，包含标点 64 次、国家 16 次、17～83 整数 48 次，共 128 槽。始终循环同一排列；任何连续 128 次都保留原基准配额，不在新一轮重新洗牌。

| 项目 | 固定行为 |
| --- | --- |
| 模型 | `gpt-6.1-sol`，实际模型映射纳入系列身份 |
| 历史与 system | 空历史，`system="."` |
| 推理与输出 | `low`，最多 128 输出 token，流式响应 |
| Responses | 原生请求合同，`store=false`，独立原生基准 |
| Chat Completions | 对应兼容请求合同，独立 Chat 基准与采样系列 |
| 额外请求 | 不填充输入，不添加额外挑战，不即时重试 |

标点、国家和整数使用参考项目原题。请求失败、空回答、不完整响应与已消耗序号的崩溃尝试占据窗口位置，不向更早记录补成功样本。未发出的配置检查失败与调度跳过不消耗序号。

SSE 与兼容端 JSON 返回均保留原始文本和空白分段，再按原基准做原始长度检查、trim、Unicode casefold 和规范化长度检查。非空异常答案进入未知类别并作为有效样本；不修正数字，不合并国家别名。

## 持久化与窗口

新增 `upstream_confidence_distribution_states`，一 Key 一行，保存系列身份、固定题序、递增序号、最多 128 个样本、执行状态及最近充分判定。状态与旧 Key 健康 JSON 分离，避免观测更新覆盖正在执行的探针。

- 手动和定时执行共享按 Key 的锁。数据库 revision CAS 原子竞争 120 秒租约，开始发送前写入 pending 并消费序号。
- 完成时以系列 ID、租约 token、序号与题型验证所有权；过期或旧系列结果不得覆盖当前状态。
- 重启后从数据库恢复题序和窗口。过期 pending 变为 `lease_expired` 无效样本，不重发。
- 系列绑定带版本的有效请求身份：Key／账号绑定、最终端点、实际鉴权凭据、Sol 有效映射、协议、生效代理、稳定请求头、请求合同和基准版本；上述变化后重新累计。其他模型白名单和同步元数据不影响系列。
- 样本沿用 35 天观测保留边界；除了 Claim/Load 时回收，也在既有观测周期清理事务内删除闲置系列的过期答案。清理使用 revision CAS，不能覆盖并发 Claim/Finish；序号、系列身份与题序继续保留，不伪造完整窗口。旧 SQLite 单测缺少原生迁移表时兼容，生产 PostgreSQL 缺表必须报错。

按默认约 5 分钟一次探针，首次完整窗口约需 10 小时 40 分钟。改变频率影响收集耗时，不改变 128 次配额。

## 请求身份与旧窗口升级

展示与发送共用请求配置解析器；发送使用身份生成时选定的端点、鉴权、模型、代理和请求头快照，并应用持久化系列的自动客户端版本。身份使用内部 SHA-256 总摘要与分项摘要，不保存凭据、代理密码或原始请求头值，不向 API、事件或日志输出内部摘要与这些原值。

身份不包含整份 `Credentials`、无关模型映射、同步时间、费率、并发、调度参数、题型、序号或租约。系统逐请求生成的 `X-Codex-Window-ID` 不参与身份；管理员显式且生效的覆写仍参与。URL 与请求头通过同一规范化路径比较，未生效覆写不改变系列。

请求身份 v3 在每个 Responses 系列首次建立时固定系统生成的 Codex 客户端版本。系列后续发送的自动 `User-Agent` 版本片段与 `version` 头使用该版本，自动同步或默认客户端版本更新不会重新累计。只有这两个自动版本字段在身份摘要中归一化；自动 `User-Agent` 模板的其他内容仍参与身份。管理员显式且生效的账号请求头覆写保留实际值并参与摘要，发送时不被系列版本覆盖。Chat Completions 不应用此版本固定规则，继续使用独立身份与基准。

管理员全局固定客户端版本在任一自动版本字段仍生效时参与身份；合法固定版本的修改或清除会以 `headers_changed` 重新累计。若账号同时显式覆写 `User-Agent` 与 `version`，全局固定版本不影响实际请求，也不参与该系列身份。解析固定版本策略失败时返回安全配置错误，不领取槽；策略读取与自动头生成使用同一快照，避免缓存版本与身份不一致。

现有 `state_json` 保存身份版本、分项摘要、最近重置信息，以及公开格式的 SemVer 生成参数 `client_version`，不新增 SQL migration。此参数仅用于内部恢复与发送，不进入 API 分布、观测证据、重置事件或日志；不保存完整 `User-Agent` 或请求头原值。已有 v3 系列身份相等时保留原版本，不用当前自动同步版本覆盖。

旧状态仍可解码。v2 窗口只有在当前配置生成的完整 v2 指纹、协议和基准精确一致时，才能通过 revision CAS 原位升级为 v3；合同已经包含在完整指纹中。升级将当前可验证的自动客户端版本保存为系列版本，保留系列 ID、题序、递增序号、样本、pending、原租约和最近充分判定，不生成重置事件。完整 v2 指纹不匹配时使用 `legacy_identity_unverifiable` 保守重开，不猜测旧自动版本，不拼接历史不同系列。

无身份版本的旧窗口继续要求旧完整指纹、协议、合同及基准匹配，且新增身份维度的连续性可验证。旧指纹只覆盖代理 ID，未记录代理实际连接配置；运行时默认请求头来源也可能未被覆盖。这类配置无法证明连续性时同样保守重开，不把当前快照作为历史证据。

Claim 的顺序保持为恢复过期租约、有效 pending 则忙、比较／升级身份、领取新槽。身份变化不能覆盖有效的在途租约；崩溃后的 pending 按原租约过期为无效槽，不延长、不重发。CAS 冲突后重新读取并完整判断。

新版身份与旧版写入逻辑不能混用。上线先停止全部旧版探针调度及写入，等待旧在途请求完成或退出后再启用新版；尚存 pending 保留原租约。回滚旧版可能重新累计，但不改写历史观测。该约束适用于多实例和应用双槽排空阶段，不能把旧 HTTP 流量排空等同于已停止旧后台探针。

## 判定、告警与展示

不足 128 次时返回 `collecting`。pending 尚未完成时继续显示采集中且不发布新的候选分数；只有完成或过期回收为无效后才判定完整窗口。满额后要求有效标点至少 39、国家至少 10、整数至少 29，总体至少 78；达不到要求返回 `insufficient`。

独立 Go 实现 predictive likelihood，比较 Sol、Astra、Terra、Luna 与 other，并使用对应授权基准的高档阈值。充分支持 Sol 返回 `match`，充分支持其他候选返回 `mismatch`；并列、阈值不足、样本不足或基准缺失都属于证据不足。

API 的 `confidence_distribution` 包含窗口进度、有效样本、三类配额/计数、各候选浮点匹配分数、最接近模型、起止时间、协议和基准版本。新分布不写入旧 Juice 24h／7d 成功率，也不把旧 Juice 观测计入新窗口。

可选 `series_reset` 包含 `pending`、实际重置时间 `at`、原因枚举 `reasons` 和上一窗口槽数 `previous_attempted`（最多 128，含失败／pending，不是历史总尝试数）。配置身份不匹配时读取返回当前身份的空窗口与待重置原因，不创建系列、不发送请求、不写重置事件；直到下次符合发送条件的 Claim 才实际重开。列表保留紧凑进度，详情明确区分待重置与已重置；旧接口缺少字段时不显示该区域。

凭据缺失、端点无效、Sol 模型不支持等配置检查失败，返回当前配置的 `insufficient` 和安全原因枚举，清除缓存的旧匹配结果及 Juice 分数，不读旧窗口、不领取槽、不产生重置事件。配置解析器或采样读取不可用时同样显示证据不足，并仅记录 Key ID 与安全原因；不向响应或日志透出原始错误。手动探针与观察开关响应均保留 `confidence_distribution` 及其重置详情。

实际重置以 `key_confidence_distribution_reset` 保存普通 info 事件，与新系列和领取状态在同一事务提交。首次初始化、保留窗口的身份升级和单纯读取不产生此事件。原因仅使用 `binding_changed`、`protocol_changed`、`endpoint_changed`、`credential_changed`、`model_changed`、`proxy_changed`、`headers_changed`、`contract_changed`、`baseline_changed`、`legacy_identity_unverifiable`；事件仅包含安全的系列标识、时间、原因及上一窗口槽数。身份重置独立于模型失真告警，不影响健康或调度。

列表采用灰色采集中、绿色 Sol 匹配、红色疑似其他模型、黄色证据不足。点击详情展示采样时间、有效样本、三类分布与候选行为匹配分数；设置中明确单次请求和完整窗口所需时间。分数不标为模型真实概率。

首次充分不匹配、以及后续 `match` 与 `mismatch` 切换时，以 `key_confidence_distribution_changed` 记录独立事件。告警与判定状态在同一数据库事务保存，防止状态提交后丢事件。持续同状态静默，证据不足不视为失真或恢复。事件只含系列 ID、状态、最接近模型、有效样本数与基准版本，不记录凭据或原始请求。

相邻窗口高度重叠，连续判定不是独立证据。行为匹配不能证明模型身份，上游开发模拟结果不作为生产准确率保证。

## 基准来源与发布

来源为 [chen-006/gpt56_api_detector](https://github.com/chen-006/gpt56_api_detector)，固定提交 `dc608d5c097abf96575d5b34f49952ddbd2c281`，使用 2026-10-03 的原生与 Chat 统计包。用户已确认本实现使用基准的既有商业授权；该确认不改变上游许可证，也不授予下游商业使用权。保留 Required Notice：Copyright 2026 chen-006 and contributors，以及 [基准许可声明](../../backend/internal/service/confidence_baselines/LICENSE.reference.txt)。

仅提取运行时所需拟合参数、原题合同、配额与阈值，运行时验证文件 SHA-256。来源包版本、内容指纹与离线 scorer 对照 fixture 保存在 [资产说明](../../backend/internal/service/confidence_baselines/README.md)。Go 实现独立，不分发参考项目 Python scorer。Chat 包复用原生采样和校准；协议隔离不代表 Chat 曾独立重新采样。

迁移 `288_upstream_confidence_distribution.sql` 是新增独立表，不回写旧账号或 Key 数据。历史 profile 262 接续 261，版本为 `0.2.13-baiyu`，仅新增 288；历史 profile 263 接续 262，升级为 `0.2.14-baiyu`，无新增迁移。请求身份 v2 与 v3 修复只扩充现有 `state_json`，不新增迁移、不改历史 profile 或 migration 的原字节及 checksum。后续发布使用发布工具当前登记的 profile，不沿用文档中的历史版本判断。

发布属于后端／数据库混合变更，需正式 VM Gate、实际 PostgreSQL 升级及旧镜像兼容、恢复分类和签名证据。本地单元测试与登记不等于 VM Gate 或生产发布完成；新敏感 blob 未分类保持阻断，不新增恢复豁免。

维护入口：评分与状态在 `backend/internal/service/upstream_confidence_distribution*.go`，持久化在 `backend/internal/repository/upstream_confidence_distribution_repo.go`，请求与健康接入在既有 upstream probe/config 服务，UI 位于 `UpstreamHealthCell.vue`。最低测试覆盖滑动配额、失败占位、租约竞争、重启与系列隔离、自动客户端版本更新下的稳定累计与实际发送、管理员生效版本／请求头变更隔离、v2 精确升级与保守重开、35 天保留、离线 oracle 评分、告警事务回滚及去重、健康与调度分离、列表／详情／移动布局和 profile 262 执行闭包。

## 隔离 PostgreSQL 验证

除现有 Docker integration suite 外，可在独立的本机 PostgreSQL 测试集群上运行 `TestConfidenceDistributionNativePostgres`。从 `backend` 目录设置 `SUB2API_DISTRIBUTION_TEST_POSTGRES_DSN`，然后执行 `go test -tags distribution_postgres ./internal/repository -run '^TestConfidenceDistributionNativePostgres$' -count=1 -v`。此套件要求字面 loopback 地址、显式非默认端口和 `/postgres` 管理库，自动创建并删除独立临时数据库；配置缺失直接失败，不跳过。

该套件验证跨实例 Claim、CAS 升级、系列客户端版本的持久化与重启恢复、自动版本更新下的稳定滑动窗口、重置事件去重与失败回滚、租约恢复和旧结果隔离。它使用最小测试 schema，不替代正式 migration／VM Gate，也不能作为生产发布证据。
