# 用户领奖记录与成本设计

## 方案概述

复用用户资金流水弹窗和额外成本统计链。活动奖励账本是领奖事实源；额外成本追加账本记录等额成本及来源快照。用户已确认整合现有流水、补齐全部历史、等额成本及 TDD。

## 接口与稳定合同

- GET /admin/users/:id/balance-history 支持 type=activity_reward；追加 total_rewarded，保持原 total_recharged 口径。
- 独立流水读模型保留既有字段，追加 record_source/source_id/activity_type/period_date；排序为发生时间倒序、来源升序、原始 ID 倒序。
- 默认列表包含兑换、返利转入和已入账活动奖励；同来源取足 offset+limit 后合并分页，不能拼接每源同一页。
- extra_cost_entries 增加 activity_reward_id（唯一）、related_user_id、activity_type；使用 category=activity_reward、rule_version=activity-reward-cost-v1 和 activity-reward:<id> 幂等键。
- 关联字段作为审计快照保存，用户或源记录删除不得级联删除成本。自动记录的 created_by 为空，手工创建/冲正 API 拒绝自动类别或记录。
- 成本 API 返回关联字段；活动类别只在流水筛选出现，不进入人工新增选项。

## Ownership 与数据/文件流

```text
奖励 + 机会消费 + 用户余额 + 成本追加（同 SQL 事务）
  -> commit -> 余额缓存和现有管理员统计缓存失效
领奖账本 -> 管理员只读流水/累计领奖
成本账本 -> 今日/累计成本、趋势、额外成本追溯
```

## 正常流程

1. 新增奖励后在同事务从权威奖励行写成本，使用 Decimal 原值和 Asia/Shanghai 实际领奖日期。
2. 批量抽奖每奖励一条成本，事务失败全部回滚；幂等请求返回既有结果。
3. 新迁移补齐所有 credited 历史记录；补录仅写缺失成本。
4. 发布切换后旧实例排空，再通过幂等补录入口及逐日对账覆盖旧实例尾部奖励。
5. 顶部显示总充值/累计领奖；快捷入口使用同弹窗 initialType=activity_reward。

## 失败、边界与回滚

- 成本写入失败、重复来源但金额/归属冲突：失败关闭，发奖事务回滚。
- 零奖励保留领奖和零成本记录；非 credited 奖励不计入。
- 实际 created_at 决定成本日期，不使用机会生成日、period_date 或补账当天。
- 人工成本既有 API 和冲正语义保留，自动记录不允许人工冲正。
- 新迁移为追加式兼容 schema，不修改旧 SQL/checksum。生产发布仍单独授权。

## 验证设计

- Service/sqlmock 测试验证账本查询、事务、回滚、幂等、缓存通知。
- Vue/Vitest 验证合计、筛选、快捷入口和复用弹窗竞态。
- VM 本地隔离 PostgreSQL 合成 fixture 验证迁移重放、逐日金额、唯一约束和人工账保护。
- 本机不启动后端作为联调服务；浏览器可用时只使用已验证 VM 服务，组件/API 测试为稳定证据。

## Wiki 与长期合同落点

- 用户活动与奖励模块、分叉扩展兼容性模块及 fork 审计 catalog。
- 发布 profile/migration 合同保持历史不可变，记录新 profile 及旧实例排空后补录操作。

## 回滚

保留旧 migration 和人工流水。VM 测试只使用合成隔离资源并清理本次测试资源；不切生产镜像，不恢复/删除生产数据。

## CodeGraph-derived design constraints

- entry points and call paths：UserHandler.GetBalanceHistory -> AdminService.GetUserBalanceHistory；OpenDailyGift/Draw -> SQL transaction；ExtraCostsDialog -> ExtraCostHandler。
- ownership and dependency boundaries：用户历史使用独立读模型，活动规则继续由 DailyActivityService 权威结算。
- impact radius：接口签名影响 service/admin_service.go、admin_user.go、handler 及 stub；前端两个弹窗、UsersView 和 UsageView 的共享弹窗调用入口。复核发现保持弹窗打开切换用户及用户详情乱序响应的竞态，采用请求版本与 props 组合监听最小修复，并增加 Red/Green 回归。
- affected tests：admin_balance_history_test、daily_activity tests、extra_cost tests、UserBalanceHistoryModal/ExtraCostsDialog tests。
- rollback boundary：新增 schema 与成本事实保留，旧应用尾部奖励须补录后对账。
- graph evidence vs source verification：CLI impact 已运行；动态调用及索引缺项用当前源码核验，不依赖图作为测试结论。
- unresolved items：无产品决策悬项；VM 可用性和资源边界需执行时核验。
