# upstream-confidence-juice

## 问题

当前 OpenAI 可信度探针使用随机加法题和服务端加权分，不能验证 GPT-5.6 Juice 指纹；统计还会把异常/无返回记录混入 24 小时和 7 天聚合，非 OpenAI 账号可能显示 `可信 -- / --`，可信度徽标也未与健康徽标同行。旧 v1 数据已不可信。

## 目标

- 迁移 `.upstream/gpt56_api_detector` 的 Juice high effort 探针和分类规则。
- 只统计有效返回，异常和无返回不进入分母；保留 24h/7d 命中百分比。
- 非 OpenAI 不展示可信度；可信度徽标与健康徽标同行。
- 提供幂等旧可信度数据清理，保留普通健康证据。
- 明确后台探针触发关系：显式启用的 OpenAI Juice 探针独立运行；其它情况由近期真实健康流量抑制普通后台探针。
- 后台入队后发送前再次检查 freshness；管理员手动探针始终强制执行。

## 非目标

- 不实现 low/medium/xhigh/max 多档 Juice 统计。
- 不为 Anthropic、Gemini 或其它平台新增可信度检测。
- 不删除普通健康状态、HTTP、TTFT、时间和历史观测。
- 不改变真实流量证据口径；普通请求级 4xx 仍不作为抑制证据。

## 成功标准

- high Juice 模板、归一化和 Sol/Terra/Luna 分类与 upstream detector 一致。
- 网络/HTTP/超时/流中断/空响应不影响有效样本分母。
- 有返回的错误值可形成失败或 mixed 证据。
- 非 OpenAI DOM 不含可信度占位，OpenAI 徽标与健康徽标同一行。
- 旧可信度字段清理后计数为 0，普通健康历史仍可查询。

## 影响范围

- `backend/internal/service`：探针请求、high Juice 分类和结果状态。
- `backend/internal/repository` / `backend/migrations`：有效样本聚合、版本过滤和旧字段清理。
- `frontend/src/components/account`：平台条件和同行徽标布局。
- `backend/internal/service/*_test.go`、`backend/internal/repository/*_test.go`、`frontend/src/components/account/__tests__`：验证。

## 交付形态

single-change

本 change 可独立 review、验证和归档；不改变其它平台探针合同。

## 风险

- Juice 指纹是行为证据，不是模型身份概率；UI 文案必须避免过度解释。
- 清理是数据写入，必须先输出只读聚合计数并限定字段。
- 新旧版本混存会污染统计，查询必须固定版本和 high effort。

## 参考资料

- 来源：`.upstream/gpt56_api_detector/gpt56_vnext/juice.py`、`catalog.py`、`baselines/runtime_catalog.json`
  - 目标落点：OpenAI 健康探针
  - 采用方式：direct migration
- 来源：`backend/internal/service/upstream_health_probe_client.go`、`backend/internal/repository/upstream_health_observation_repo.go`
  - 目标落点：探针、持久化、聚合
  - 采用方式：rewrite
- 来源：`frontend/src/components/account/UpstreamHealthCell.vue`
  - 目标落点：健康/可信度徽标
  - 采用方式：rewrite
