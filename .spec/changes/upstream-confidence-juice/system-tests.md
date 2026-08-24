# upstream-confidence-juice 系统测试

## 测试环境

- runtime/platform：Windows PowerShell，Go、Node/pnpm、spec-wiki-lite。
- fixture/data：HTTP stub、SQLite/Postgres repository fixture、Vue mount fixture。
- 外部依赖：测试 mock；生产清理单独执行只读计数和事务 SQL。

## ST-01 Juice 正常返回

- 类型：normal
- 前置：OpenAI API key，high effort，stub 返回 high Juice 数字。
- 操作：执行一次 OpenAI Responses probe。
- 断言：分类 current_success，score=100，版本为 openai-juice-high-v1。

## ST-02 异常不计平均

- 类型：failure
- 前置：有效返回、HTTP 401、超时、流中断混合历史。
- 操作：查询 24h/7d 聚合。
- 断言：异常记录不进入分母；有效返回仍按成功/失败分类。

## ST-03 mixed 与样本不足

- 类型：boundary
- 前置：high 返回命中其它型号或未知数字；另一个 key 无有效返回。
- 操作：执行 probe 和 API 查询。
- 断言：mixed 降级；无样本返回 null 且 UI 不显示可信度。

## ST-04 平台和布局

- 类型：normal
- 前置：OpenAI、Anthropic、Gemini 三种 account fixture。
- 操作：挂载健康单元格。
- 断言：仅 OpenAI 有可信度；健康与可信度徽标同一行。

## ST-05 旧数据清理

- 类型：failure
- 前置：包含 v1 可信度字段和普通健康字段的观测。
- 操作：记录 allowlisted count，执行清理两次。
- 断言：可信度字段清零，普通健康字段保留，第二次影响行数为 0。

## ST-06 后台触发模式

- 类型：normal/failure/boundary
- 前置：OpenAI 显式 enabled=true、OpenAI disabled/缺失配置、非 OpenAI；分别设置近期 LastEvidenceAt 和过期 LastProbeAt。
- 操作：运行 due 列表与后台 due probe；另行调用管理员 ProbeKey。
- 断言：仅显式启用 OpenAI 忽略真实流量 freshness；其它情况被抑制；后台二次检查发现新真实流量不发送请求；手动探针仍执行；配置读取失败按未启用处理。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| high Juice 分类一致 | ST-01/ST-03 | Go tests |
| 异常不进分母 | ST-02 | repository tests |
| 非 OpenAI 隐藏且同行布局 | ST-04 | Vitest DOM assertions |
| 清理幂等并保留普通证据 | ST-05 | SQL/test output |
| 后台探针触发关系正确 | ST-06 | service/runner tests |
