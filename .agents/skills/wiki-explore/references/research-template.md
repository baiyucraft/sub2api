# <topic> 调研记录

phase: exploration
service-boundary: scope-and-delivery-shape

## 调研问题

- <只写会影响 scope、ownership、risk 或 delivery shape 的问题>

## 证据

| 来源 | 观察事实 | 可信度 | 对当前 change 的影响 |
| --- | --- | --- | --- |
| `<path-or-command>` | <事实，不混入推断> | high / medium / low | <影响> |

## 结论

- <从证据得到的结论；推断需显式标记>

## Delivery Shape 影响

- 推荐：single-change / multi-change
- 原因：<独立验收边界、依赖和风险>

## 未知项与风险

- <不阻塞探索但需进入 proposal/design 的事项>

## Artifact 影响

- <应写入 split、proposal 或 metadata 的决定；无则写“无”>

## CodeGraph Evidence

- index: <available/unavailable, generated-at>
- analysis goal: <本次代码分析目标>

### Queries

| tool | query | result |
| --- | --- | --- |
| `codegraph_status` | <query> | <summary> |
| `codegraph_context` | <query> | <summary> |
| `codegraph_explore` / `codegraph_trace` | <query> | <summary> |

### Confirmed Facts

- <入口、符号、调用关系和 file:line>

### Impact And Test Leads

- impact: <影响范围与边界>
- affected tests: <测试线索>

### Facts vs Inferences

- confirmed: <CodeGraph 或源码确认的事实>
- inferred: <推断及依据>

### Unknowns And Fallback

- unavailable tool: <缺失工具或失败原因>
- fallback: <普通源码/测试读取>
- residual risk: <未验证影响>

只保存结构化摘要，不粘贴大段原始 CodeGraph 输出。
