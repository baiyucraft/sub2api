# daily-activity-rewards 任务计划

implementation-mode: tdd

## 任务总览

任务按“规则与数据 → 结算服务 → HTTP 合同 → 管理配置 → 前端 → 回归门禁”拆分，先建立后端可测试的权威结算，再接入页面。

## 实现模式

tdd

先为活动日、订单过滤、邀请唯一计数、机会幂等和输入拒绝建立 Red 用例，再完成最小实现并重构公共账务/配置能力。

## 1. 活动数据与规则快照

- [x] 1.1 Red：新增迁移/fixture 合同测试，覆盖奖励、机会、邀请达标和事件去重的唯一约束。
- [x] 1.2 Green：实现幂等活动表、规则版本和 `Asia/Shanghai` 活动日计算。
- [x] 1.3 Refactor：复用现有迁移 runner、金额类型和审计基础设施。

### CheckList

- [ ] 迁移可重复执行且不删除已有账务
- [ ] 活动规则版本保存到奖励记录
- [ ] 数据库唯一约束覆盖礼包、机会和 invitee 达标

## 2. 后端进度与结算

- [x] 2.1 Red：覆盖成功充值/消费、退款过滤、0 倍率消费排除和邀请 10/5 规则。
- [x] 2.2 Green：实现 summary、礼包、抽奖、奖励账本和统一余额入账事务。
- [x] 2.3 Refactor：抽取安全随机、Decimal/分单位、幂等和失败回滚逻辑。

### CheckList

- [ ] 奖励金额完全由后端配置和随机源决定
- [ ] 机会扣减与入账无重复发放
- [ ] 现有邀请返利账本没有被活动奖励写入

## 3. HTTP 与管理员配置

- [x] 3.1 Red：handler contract 覆盖伪造字段、他人 user ID、未知类型和非法 count。
- [x] 3.2 Green：接入四个用户接口和管理员系统设置配置。
- [x] 3.3 Refactor：统一错误码、规则版本、审计和分页响应结构。

### CheckList

- [ ] 用户 ID 只来自认证上下文
- [ ] 配置硬上限和非法值 fail closed
- [ ] API 文档/DTO 与前端类型一致

## 4. 用户页面与导航

- [x] 4.1 Red：页面测试覆盖入口、四类卡片、机会、进度、错误态和重复点击。
- [x] 4.2 Green：新增 `/activities/daily`、API client、活动页和邀请返利下方导航入口。
- [x] 4.3 Refactor：抽取卡片/历史列表，完成暗色、窄屏、键盘和 i18n。

### CheckList

- [ ] 前端不计算门槛、机会或奖励金额
- [ ] 操作完成后刷新服务端 summary
- [ ] 活动关闭时页面只读且不影响现有邀请返利

## 5. 验证与文档

- [x] 5.1 运行后端活动/支付/affiliate 回归测试与前端活动/导航 Vitest。
- [x] 5.2 运行 `pnpm typecheck`、`pnpm lint:check`、`git diff --check`。
- [x] 5.3 更新 `.wiki` 活动长期合同和 fork audit 登记，完成 strict validate/review/archive 门禁。

## 用例到任务映射

| 系统测试用例 | 大 task | 小 task / 验证 |
| --- | --- | --- |
| ST-01、ST-05 | 1、2、4 | 1.2 / 2.1 / 4.2 |
| ST-02、ST-03、ST-06 | 2、3 | 2.2 / 2.3 / 3.1 |
| ST-04、ST-08 | 1、2 | 1.2 / 2.1 / 2.2 |
| ST-07 | 3 | 3.2 / 3.3 |

## 执行顺序

1. 规则/迁移 fixture 与 Red 测试；
2. 进度、机会和结算 Green；
3. handler/API 与管理员设置；
4. 用户页面与 i18n；
5. 回归验证、Wiki、audit、review 和 archive。

## 暂缓事项

- 不接入外部活动页面接口，不发送真实奖励，不部署生产或操作 VM；待本 change 完整验证后另行安排发布。
