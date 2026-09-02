# daily-activity-rewards 设计方案

## 方案概述

新增独立活动域，拆分为活动配置、活动进度、永久机会、邀请达标、奖励账本五类职责。页面只消费 summary/rewards 接口；所有金额和资格由服务端从当前生效规则与已结算事件计算。

```
已结算充值/消费/邀请事件
        ↓ 去重与活动日归属
活动进度与永久机会
        ↓ 事务锁定 + 幂等
后端安全随机奖励 → 奖励账本 → 统一余额入账
        ↓
用户 summary / reward history
```

## 接口与数据流

用户接口：

- `GET /api/v1/user/activities/summary`：返回活动开关、Asia/Shanghai 活动日、进度、机会数、礼包领取状态和生效规则版本。
- `GET /api/v1/user/activities/rewards?page=&page_size=`：分页返回活动奖励记录和余额入账状态。
- `POST /api/v1/user/activities/daily-gift/open`：不接收金额；服务端校验今日门槛、唯一领取约束并发放奖励。
- `POST /api/v1/user/activities/draw`：仅接收 `activity_type` 与 `count`；类型只允许 recharge/consumption/invitation，数量受服务端上限约束。

所有用户接口从认证上下文取得 user ID。奖励金额、门槛、概率/区间、规则版本和邀请人关系不信任客户端输入。写接口要求幂等键或等价请求指纹，重复请求返回原结算结果。

管理员沿用现有系统设置入口，增加活动配置 DTO、后端硬上限和审计字段。配置保存生成规则版本；已经创建的奖励记录保留原规则版本，不因后续改配置重算。

## 规则与边界

- 每日礼包：每日成功充值累计达到 10 元，每个用户在中国时区活动日唯一领取一次，奖励区间 0～0.50 元。
- 充值抽奖：当日成功充值每满 50 元生成一次永久机会，奖励区间 0.50～1.00 元。
- 消费抽奖：当日实际用户消费每满 50 元生成一次永久机会；显式用户专属倍率 0 的请求不增加消费抽奖进度，奖励区间 0.50～1.00 元。
- 邀请达标：被邀请用户成功结算充值累计达到 10 元时，按 `(inviter_id, invitee_id)` 唯一计数一次；每满 5 个达标邀请生成一个永久机会，奖励区间 5～10 元。
- 失败、取消、退款和管理员赠送不计入充值门槛；同一邀请对象后续充值不重复计数。
- 统计窗口使用 `Asia/Shanghai` 的日边界；机会、邀请达标计数和奖励历史永久保留。

## 持久化与一致性

建议新增幂等、可审计的活动表（具体命名按现有迁移约定落地）：

- `activity_reward_records`：user、activity type、amount cents、source period/event、rule version、status、idempotency key、created/settled timestamps。
- `activity_draw_opportunities`：user、type、source event/period、sequence、used_at；唯一约束防止机会重复消费。
- `activity_invitation_milestones`：inviter、invitee、qualifying recharge evidence、qualified_at；`(inviter_id, invitee_id)` 唯一。
- `activity_progress_events`：充值/消费/每日礼包等事件去重键及归属日，避免 webhook、异步结算和重试重复累计。

抽奖事务先锁定可用机会，再生成奖励记录并通过现有余额服务入账；任一步失败整体回滚或进入可重试的明确 pending 状态。奖励金额使用整数分/Decimal，随机源使用后端安全随机源并记录规则版本与结算结果。

邀请达标事件挂在成功支付履约完成之后，不能仅由前端付款成功页触发。消费进度挂在最终成功计费结算之后，并复用现有 0 倍率语义。

## 前端信息架构

新增 `/activities/daily` 与 `DailyActivitiesView.vue`，导航紧接 `/affiliate`。页面展示服务端 summary：活动日、倒计时、充值/消费进度、邀请达标 `n / 5`、永久机会数和奖励历史。领取礼包、单抽、全部抽取均有 loading、防重复点击、错误态和成功刷新；不在前端生成金额或资格。

页面视觉复用项目布局/可访问性基础组件，但采用独立卡片、进度和奖励层级，避免复制参考页面样式。邀请卡仅展示独立达标抽奖进度与邀请链接，不把现有返利金额混入活动奖励。

## 兼容、回滚与安全

- 活动关闭时用户接口返回 disabled/只读 summary，写接口 fail closed，不影响支付和邀请返利。
- 新表迁移必须幂等；回滚应用可隐藏入口，已发放奖励和账务记录不删除。
- 日志只保留 user/activity/rule/status 等 allowlist 字段，不写 token、完整请求体或外部页面凭据。
- 不读取或复用外部活动页面接口；管理端配置修改必须记录审计日志。
