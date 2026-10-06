---
title: OpenAI Sol 分布可信探针
description: 单次短题、持久化 128 次滑动窗口及独立行为匹配展示
updated: 2026-10-06
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
- 系列绑定 Key、账号绑定、端点、凭据指纹、模型映射、协议、请求合同和基准版本；变化后重新累计，展示只读取匹配的当前系列。
- 样本沿用 35 天观测保留边界；除了 Claim/Load 时回收，也在既有观测周期清理事务内删除闲置系列的过期答案。清理使用 revision CAS，不能覆盖并发 Claim/Finish；序号、系列身份与题序继续保留，不伪造完整窗口。旧 SQLite 单测缺少原生迁移表时兼容，生产 PostgreSQL 缺表必须报错。

按默认约 5 分钟一次探针，首次完整窗口约需 10 小时 40 分钟。改变频率影响收集耗时，不改变 128 次配额。

## 判定、告警与展示

不足 128 次时返回 `collecting`。pending 尚未完成时继续显示采集中且不发布新的候选分数；只有完成或过期回收为无效后才判定完整窗口。满额后要求有效标点至少 39、国家至少 10、整数至少 29，总体至少 78；达不到要求返回 `insufficient`。

独立 Go 实现 predictive likelihood，比较 Sol、Astra、Terra、Luna 与 other，并使用对应授权基准的高档阈值。充分支持 Sol 返回 `match`，充分支持其他候选返回 `mismatch`；并列、阈值不足、样本不足或基准缺失都属于证据不足。

API 的 `confidence_distribution` 包含窗口进度、有效样本、三类配额/计数、各候选浮点匹配分数、最接近模型、起止时间、协议和基准版本。新分布不写入旧 Juice 24h／7d 成功率，也不把旧 Juice 观测计入新窗口。

列表采用灰色采集中、绿色 Sol 匹配、红色疑似其他模型、黄色证据不足。点击详情展示采样时间、有效样本、三类分布与候选行为匹配分数；设置中明确单次请求和完整窗口所需时间。分数不标为模型真实概率。

首次充分不匹配、以及后续 `match` 与 `mismatch` 切换时，以 `key_confidence_distribution_changed` 记录独立事件。告警与判定状态在同一数据库事务保存，防止状态提交后丢事件。持续同状态静默，证据不足不视为失真或恢复。事件只含系列 ID、状态、最接近模型、有效样本数与基准版本，不记录凭据或原始请求。

相邻窗口高度重叠，连续判定不是独立证据。行为匹配不能证明模型身份，上游开发模拟结果不作为生产准确率保证。

## 基准来源与发布

来源为 [chen-006/gpt56_api_detector](https://github.com/chen-006/gpt56_api_detector)，固定提交 `dc608d5c097abf96575d5b34f49952ddbd2c281`，使用 2026-10-03 的原生与 Chat 统计包。用户已确认本实现使用基准的既有商业授权；该确认不改变上游许可证，也不授予下游商业使用权。保留 Required Notice：Copyright 2026 chen-006 and contributors，以及 [基准许可声明](../../backend/internal/service/confidence_baselines/LICENSE.reference.txt)。

仅提取运行时所需拟合参数、原题合同、配额与阈值，运行时验证文件 SHA-256。来源包版本、内容指纹与离线 scorer 对照 fixture 保存在 [资产说明](../../backend/internal/service/confidence_baselines/README.md)。Go 实现独立，不分发参考项目 Python scorer。Chat 包复用原生采样和校准；协议隔离不代表 Chat 曾独立重新采样。

迁移 `288_upstream_confidence_distribution.sql` 是新增独立表，不回写旧账号或 Key 数据。接续 profile 261 创建 pending profile 262：版本仍为 `0.2.13-baiyu`、parent 261、仅新增 288。所有历史 profile、旧 migration 原字节与 checksum 保持不变；未知 263 拒绝。

发布属于后端／数据库混合变更，需正式 VM Gate、实际 PostgreSQL 升级及旧镜像兼容、恢复分类和签名证据。本地单元测试与登记不等于 VM Gate 或生产发布完成；新敏感 blob 未分类保持阻断，不新增恢复豁免。

维护入口：评分与状态在 `backend/internal/service/upstream_confidence_distribution*.go`，持久化在 `backend/internal/repository/upstream_confidence_distribution_repo.go`，请求与健康接入在既有 upstream probe/config 服务，UI 位于 `UpstreamHealthCell.vue`。最低测试覆盖滑动配额、失败占位、租约竞争、重启与系列隔离、35 天保留、离线 oracle 评分、告警事务回滚及去重、健康与调度分离、列表／详情／移动布局和 profile 262 执行闭包。
