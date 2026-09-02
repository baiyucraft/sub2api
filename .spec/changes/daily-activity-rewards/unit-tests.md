# daily-activity-rewards TDD 单元测试

## UT-01 活动规则与每日进度

- Test：`backend/internal/service/activity_rewards_test.go`
- Modify：活动规则/进度 service
- 映射：ST-01、ST-05 / 中国时区和 10/50 元门槛
- Red：未实现活动日归属、成功订单过滤或显式 0 倍率消费排除时失败。
- Green：以固定时钟和 fixture 计算 summary，区分每日进度与永久机会。
- Refactor：将门槛和规则版本读取集中到不可变配置快照。

## UT-02 奖励金额与安全输入

- Test：`backend/internal/service/activity_rewards_test.go`
- Modify：奖励随机、金额 Decimal/分单位转换和 draw request 校验
- 映射：ST-02、ST-06
- 输入：边界区间、count=0、负数、超上限、未知类型和伪造金额字段。
- 断言：奖励始终在后端区间内，非法请求无副作用；浮点边界不产生额外分。

## UT-03 机会消费与幂等

- Test：`backend/internal/service/activity_rewards_concurrency_test.go`
- Modify：机会锁定、幂等键和奖励入账事务
- 映射：ST-03
- 输入：并发同机会、重复幂等键、余额入账失败和重试。
- 断言：最多一个成功奖励；失败可安全重试；不会出现机会已扣但奖励重复入账。

## UT-04 邀请达标唯一性

- Test：`backend/internal/repository/activity_rewards_test.go`
- Modify：邀请 milestone repository 与机会生成
- 映射：ST-04、ST-08
- 输入：同一 invitee 多订单、多次 webhook、5/6 个不同 invitee。
- 断言：每个 invitee 只计一次；第 5 次生成一个机会；不写 AffiliateService 返利账本。

## UT-05 管理员配置校验

- Test：`backend/internal/handler/admin/activity_settings_handler_test.go`
- Modify：活动设置 DTO/handler/service
- 映射：ST-07
- 输入：合法区间、负数、NaN、最小值大于最大值、超硬上限和非整数次数。
- 断言：合法保存并审计；非法 fail closed 且旧配置保持。

## UT-06 前端服务端结果展示

- Test：`frontend/src/views/user/__tests__/DailyActivitiesView.spec.ts`
- Modify：`DailyActivitiesView.vue`、活动 API 类型和用户导航
- 映射：ST-01、ST-02、ST-06
- Red：页面不应自行生成奖励金额或绕过 summary 资格判断。
- Green：mock API 返回 summary/reward，页面正确展示进度、机会和错误态，动作后刷新。
- Refactor：抽取活动卡片和奖励列表，保持键盘/窄屏/暗色布局。

## 覆盖边界

- 数据库、时钟、随机源、余额入账和支付事件使用 fixture/mock，不连接生产系统。
- 不测试不影响可观察账务/安全契约的 CSS 实现细节。
