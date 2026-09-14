# adaptive-upstream-scheduler-shadow-rollout

## 问题

前置 fork 调度底座及错误归因、请求预算、故障域健康、缓存容量、TTFT/流量隔离五个策略 child 完成后，需要在不改变真实选号的前提下比较新旧调度决策。若直接切换，无法区分调度收益、样本分布变化和上游波动，也无法在重试放大、缓存预测或长请求排队恶化时快速恢复。

## 目标

- 建立新旧调度的旁路 shadow/counterfactual 决策记录。
- 支持按分组或受控比例灰度启用新决策。
- 建立可观察的成功率、TTFT、重试、成本、缓存预测和资源释放门禁。
- 支持自动停用新策略并恢复旧策略。

## 非目标

- 不重写账号选择器、错误归因、容量模型或 TTFT 模型。
- 不让 shadow 结果直接反馈真实调度或健康熔断。
- 不改变 API、计费、usage、协议转换和用户配置。
- 不在本 change 执行生产部署或线上配置写入。

## 成功标准

- 同一请求可关联旧决策、新决策、最终实际账号和结果，但不记录敏感凭据或请求正文。
- shadow 计算失败不影响请求；灰度开关关闭后立即回到旧策略。
- 灰度达到样本门槛后能输出新旧策略对比，且按 provider、model、workload 和 cache 状态分层。
- 任一回滚门禁触发后，新策略停止接收流量，已有请求按原生命周期完成。
- shadow 与真实流量、父账号、usage 和成本统计不会重复计数。

## 影响范围

- `backend/internal/service`：新增旁路决策与评估适配，不复制 scheduler。
- 配置/缓存：灰度开关、版本和短期评估状态通过抽象接口读取。
- dashboard/ops：增加脱敏聚合指标和回滚事件。
- 测试：增加端到端决策对比、失败隔离和回滚验证。

## 交付形态

single-change

这是 parent `adaptive-upstream-scheduler` 的第 7 个 child。整体为前置底座加原六个策略 child，共七个 child；本 child 首先依赖 `adaptive-upstream-scheduler-fork-foundation`，并保留对错误归因、请求预算、故障域健康、缓存容量和 TTFT/流量隔离五个策略 child 的依赖。它只负责验证和发布控制，只消费前置窄端口及策略稳定合同；前置底座不反向依赖评估、Outcome/UsageFacts 或 mode/epoch，其他策略仍消费归因提供的公共事件/事实及 parent 的模式版本合同。可独立关闭和回滚；disabled 默认执行当前 fork legacy 行为，不以 Noop 关闭原 TTFT、健康或并发保护。

## 风险

- shadow 共享真实凭据，若错误归并会造成样本重复或误判收益。
- 新旧策略候选集合不一致时，counterfactual 结果不能解释为真实可达结果，必须记录不可比原因。
- 灰度比例过高可能放大未知错误；开关读取失败必须 fail-closed 到旧策略。
- 指标延迟和小样本可能导致过早回滚或错误放量。

## 参考资料

- 来源：parent `split.md` 与 `research/scope-and-delivery.md`
  - 目标落点：调度旁路评估、灰度控制和运维看板
  - 采用方式：rewrite with existing scheduler boundaries
- 来源：Envoy outlier detection、LiteLLM Router、Portkey retry handler
  - 目标落点：候选对比、失败隔离、回滚门禁
  - 采用方式：inspiration only
