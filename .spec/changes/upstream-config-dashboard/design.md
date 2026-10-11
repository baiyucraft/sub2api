# 设计

后端通过上游配置仓储的只读聚合查询返回卡片和详情数据；真实流量与主动探针独立统计，无法完整配对的错误率标记为数据不足。前端复用现有 AppLayout、卡片和表格样式，新增看板卡片与右侧详情抽屉。

## 已确认的财务展示补充（2026-10-11）

用户确认将同一时间窗口的渠道收入、上游消费、估算毛利、毛利率并列展示，并要求金额数字与余额的记账含义一致。卡片和详情均使用现有 `revenue`、`upstream_cost`、`estimated_gross_profit`、`estimated_gross_profit_rate`，无需增加 API、迁移或计费逻辑。金额沿用余额的 `¥` 展示并保留两位小数，毛利率独立标注；亏损保留负号，未知值不能伪造为零，收入为零时毛利率不可用。

收入是窗口内用户实际扣费 `SUM(actual_cost)`，不是现金收款。上游消费沿用请求时成本与账号倍率快照；Sub2API 渠道余额与实际成本倍率均应用配置充值折算率，`CaptureUpstreamUsageSnapshot` 的 `CNY/1` 表示成本记账口径，不能仅凭模型价格或页面的美元标签引入市场汇率、站内销售倍率或重复折算。NewAPI 的既有余额归一规则保持原样。详情说明记账含义和“毛利率 = 毛利 ÷ 收入”。

### CodeGraph-derived design constraints

`codegraph explore "CaptureUpstreamUsageSnapshot ResolveUpstreamBalanceCNY"` 返回请求快照与余额相关符号，部分行号与当前文件有偏差，关键公式已定向核验当前源码；`codegraph impact UpstreamDashboardView` 仅返回该 Vue 组件。影响范围限于看板组件、中英文文案、其组件回归测试和长期合同。后端汇总、余额同步、余额告警、充值入口和调度不改变。验证复用看板/充值组件测试、locale completeness、typecheck、lint、前端 build 和后端既有看板/快照测试；回滚只需恢复本轮展示及文档差异。
