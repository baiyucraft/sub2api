# upstream-confidence-juice 设计方案

## 方案概述

OpenAI Responses 探针每次从 upstream detector 的 high 模板池选择一个恒等变形 Juice 任务，请求只要求返回最终数字；服务端按申报模型的 high 指纹分类。只对收到且完成解析的返回计入有效样本；异常请求单独保存健康结果但不进入可信度分母。

## 接口与稳定合同

- `confidence_prompt_version` 使用 `openai-juice-high-v1`。
- `requested_effort` 固定为 `high`，设置接口不再接受其它值。
- `confidence_score_24h/7d` 为 `current_success / valid_completed * 100`，无有效样本返回空值。
- `confidence_checks` 保存分类计数、有效样本数和 mixed/状态信息。
- 只有 `platform == openai` 且有效样本数大于 0 时 API/UI 暴露可信度徽标。

## Ownership 与数据/文件流

```text
upstream detector templates -> OpenAI probe request -> Juice classifier
  -> upstream_health_observations -> confidence aggregate API -> account cell UI
old confidence fields -> read-only count -> scoped SQL cleanup
```

后端 service 拥有分类与状态，repository 拥有聚合/清理，前端只消费平台条件和聚合字段。

## 正常流程

1. 生成 high Juice prompt，保留模板 id 和随机 nonce。
2. OpenAI Responses SSE 完成后拼接文本并归一化数字。
3. 按申报模型 high 指纹分类 current_success/mixed/unsuccessful。
4. 仅有效返回写入可信度计数；错误请求仍写普通探针观测。
5. 24h/7d 聚合只读取 OpenAI、probe、版本、high 且分类有效的观测。
6. UI 在健康徽标同一 flex 行显示可信度徽标。

## 后台触发与真实流量关系

- 可信度配置只有在数据库存在合法配置且 enabled=true 时才算启用；缺失、空值、读取失败或非法 JSON 均按未启用处理，默认值为 disabled。
- OpenAI 且显式启用时，后台 Juice 探针只按 LastProbeAt 和探针周期判断，不受 LastEvidenceAt 抑制。
- 未启用/未配置的 OpenAI、所有非 OpenAI 平台，使用 max(LastProbeAt, LastEvidenceAt) freshness 抑制普通后台探针。
- 后台专用 due 路径在健康锁内再次检查 freshness；管理员 ProbeKey 不经过该抑制。

## 失败、边界与回滚

- 无效 effort：后端归一化为 high，拒绝其它持久化值。
- 异常/无响应：不计可信度分母，保留 result/reason/HTTP/TTFT。
- 错误数字：计入 valid_completed，标为 unsuccessful 或 mixed。
- 无有效样本：分数为 null，UI 隐藏可信度徽标。
- 清理重复执行：使用单条条件 UPDATE/事务，第二次影响行数为 0。
- 清理失败：事务回滚，不删除普通健康字段。
- 回滚代码：恢复旧查询/展示即可；清理字段不可恢复，因此执行前必须记录 allowlisted 聚合。

## 验证设计

- service 单测覆盖 high 模板解析、三型号分类、异常排除、mixed 和样本不足。
- repository 单测覆盖版本/平台/effort/分类过滤、24h/7d 分母和清理 SQL。
- Vue 单测覆盖 OpenAI 同行徽标、非 OpenAI 隐藏和无样本隐藏。
- focused Go/Vitest/typecheck/build 与 `spec-wiki-lite validate --strict`。

## Wiki 与长期合同落点

- `.wiki/03-模块指南/03-网关与上游.md`：补充探针证据、统计和清理边界。

## 参考边界

- upstream detector：direct migration；不复制其运行时 UI 或网络层。
- 当前 Sub2API service/repository/frontend：rewrite。

## 回滚

- 代码回滚仅恢复本 change 文件；数据清理前输出备份式聚合记录，禁止自动恢复普通健康字段。

## CodeGraph-derived design constraints

- 入口路径：`runOpenAIUpstreamHealthProbe -> parseOpenAIUpstreamHealthStream -> classifyJuiceAnswer`。
- 聚合路径：`GetUpstreamHealthConfidence -> account handlers -> UpstreamHealthCell`。
- 影响半径包含 `upstream_health_probe_client_test.go`、repository tests、`UpstreamHealthCell.spec.ts`。
- CodeGraph impact 已对评分函数、聚合方法和 affected files 执行；源码核验确认当前 v1 算术题和垂直可信度块。
- 未解决项：生产连接信息不用于代码实现；清理由发布步骤在授权环境执行。
