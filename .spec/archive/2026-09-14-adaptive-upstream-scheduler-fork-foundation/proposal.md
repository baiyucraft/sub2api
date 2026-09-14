# adaptive-upstream-scheduler-fork-foundation

## 问题

现有 fork 调度能力已经部分接口化：TTFT 配置与展示、健康证据、共享并发缓存、账号 RPM 都有可复用接口。但 TTFT 具体状态仍由 OpenAIGatewayService 持有，健康写入依赖全局注册并分散于业务/生命周期路径，优先池及倍率分层仍有逻辑嵌在官方 selector 中。仅新增 service 文件或给整个官方网关包一层接口，不能控制未来合并官方代码的冲突面。

原六个自适应策略 child 如果直接依赖这些具体实现，会继续扩大官方文件补丁，甚至重复触发 TTFT 试放、健康更新或并发副作用。因此需要先建立一个可独立验证的兼容底座。

2026-09-13 用户已确认正式增加第一份 change 并同步后六份设计；随后授权在当前工作区实现第一份兼容底座。本 change 仍不修复旧行为、不执行 VM Gate、生产部署或线上配置写入。

## 目标

- 建立现有健康、TTFT、并发/RPM、优先池/倍率和上游重试策略的窄内部接口；优先复用已有合同。
- 将 fork 自有状态、纯比较/分池逻辑及配置装配与官方执行职责分开，后续策略不直接依赖官方对象、全局 registry 或具体旧服务。
- 保留现有健康状态源、持久化顺序和失败回退；通过明确协调入口管理原健康命令和生命周期变更，不增加第二个写入者。
- 明确只读数据与有状态操作的区别，保持原有调用次数、试放节奏、RPM 扣次、槽位归属与释放。
- 默认完整委托 legacy 实现，以当前 fork 行为为基线；不关闭已有保护，不替换官方选择器。
- 为后六个 child 提供单向依赖的底座和可审计的官方函数级接入名单。

## 非目标

- 不重做官方 Select、Top-K、随机选择、粘性绑定、重试循环、协议、计费或硬资格。
- 不新增错误分类、统一 Outcome/UsageFacts、attempt budget、共享故障域、分布式半开、新缓存容量账本、TTFT 算法、mode/control_epoch 或灰度策略。
- 不修改 TTFT 阈值、probe/traffic 状态机、探针协议/节奏或账号级 RPM 维度。
- 不把现有质量分和展示缓存率接入调度，不改 SQL 聚合口径或用户计费。
- 不重构所有 fork 业务模块；活动、支付、邮件、前端、Observer 和 provider 同步协议不在本 change 中。
- 不新增数据库迁移、设置键、公开 API、Account 必填依赖或凭据字段；不运行 VM Gate、部署或写线上配置。
- 不把调研中探针可能覆盖业务暂停的风险静默修正为新语义；行为修复需独立声明并验证。

## 成功标准

- SC-01：独立底座不 import service、handler、repository、Ent、Gin、Redis 实现或后续 adaptivescheduler；后续消费者只依赖值对象/窄契约。
- SC-02：同候选、配置、时钟、随机源与输入轨迹下，兼容适配前后的选号、分层、随机消耗、调用次数和副作用一致；不得在生产链路双跑 Select 验证。
- SC-03：TTFT 观测、有状态排除与管理端视图共享一个 legacy 实例；试放、恢复、TTL/LRU、配置发布、upstream-only 和已有 sticky 特例保持原行为。
- SC-04：健康证据、手动恢复、观察设置和归档/恢复通过明确入口更新同一 registry；原锁、持久化失败回退、outbox 与账号投影不变，不额外清除官方阻断。
- SC-05：并发沿用账号/upstream target 互斥规则、原 Acquired/ReleaseFunc、等待和复核流程；取消、失败或重复结束只释放原资源一次。
- SC-06：RPM 预读不扣次、最终准入按原时机原子扣次；普通账号、默认重试码及可选接口缺失/存储失败时的回退不变。
- SC-07：未注入 adaptive 能力时完整走当前 fork legacy；不存在把“新调度关闭”解释为关闭原健康/TTFT/并发的情况。
- SC-08：官方变更限定到函数级名单，fork 状态/纯规则不继续嵌入官方执行主体；无公开接口/数据库/前端变更，并能映射专项回归证据。

这些是后续实现的验收标准，本轮文档 strict validate 通过不等于上述行为已经实现或验证。

## 影响范围

- `backend/internal/forkscheduling/`：拟新增的能力值对象、窄契约、legacy 状态与纯规则；不依赖官方业务层。
- `backend/internal/service/fork_scheduling_bridge*.go` 及相关 fork provider：类型转换、旧入口兼容、依赖注入与配置发布。
- 现有 `openai_ttft_guard*`、`upstream_health*`、`upstream_scheduler_concurrency`、`preferred_account_pool`、`pool_mode_retry_settings`：按职责机械迁移或适配，不更改算法。
- 官方 scheduler/gateway/Account/concurrency/helper：仅 parent 与 design 明确列出的 fork 接入点；不整文件替换。
- fork `upstream_config_repo.go`：生命周期成功后的 registry 命令适配，原 SQL/事务/outbox 不变。
- `.spec/changes/adaptive-upstream-scheduler*/`：依赖、边界、设计与后续验证依据；本轮不生成 tasks/cases。
- 实现审查后才更新 `.wiki/03-模块指南/05-分叉扩展与兼容性.md` 及扩展 catalog，不把设计当已实现事实。

## 交付形态

single-change

本 child 是 parent `adaptive-upstream-scheduler` 的第一项，order=1、dependsOn=[]。它只交付旧能力的边界与行为等价适配，可单独 review、验证和回滚。原六个 child ID 保持不变，顺序后移并显式依赖它；原策略间依赖不变。前置不消费后续事件或预算，避免环依赖。

## 风险

- 以函数名判断只读会误用现有接口：TTFT exclusions 推进试放序列，批量负载读取可能清理 Redis 过期成员。
- 将 TTFT、健康、展示各自构造一个 runtime 会出现状态分叉；全局兼容入口与注入接口必须委托同一个实例。
- 提取比较规则可能改变排序稳定性、随机消耗或满载回退顺序，必须保留基线轨迹。
- 生命周期状态更新若移过事务提交边界，会导致数据库与内存状态不一致；本 change 不增加新的事务机制掩盖该问题。
- Probe/Traffic 来源覆盖已有静态风险，结构重构不等于来源仲裁已正确；必须保留风险说明，不把缺陷固化为正确产品合同。
- 官方接口与 fork 专属逻辑交织，catalog 路径不构成整文件所有权或修改授权。

## 参考资料

- 来源：[前置调研与 CodeGraph 证据](research/codegraph.md)
  - 目标落点：接口缺口、状态 ownership、调用效果和回归边界。
  - 采用方式：direct project evidence；不依赖未提交的临时报告才能理解方案。
- 来源：[parent split](../adaptive-upstream-scheduler/split.md)
  - 目标落点：七个 child 的顺序、单向依赖和官方函数级接入边界。
  - 采用方式：shared internal contract。
- 来源：本地官方 `98d86915becae9fe9491a91ffc6defd5235c8d2b` 与 fork `9449571f7e6b03d93c29775ba9cf9d1e892dd2c5`。
  - 目标落点：区分官方 Select/反馈/阻断与 fork 状态/纯规则。
  - 采用方式：compatibility baseline；保留官方实现，仅机械迁移 fork 部分。
- 来源：fork 扩展 catalog、Wiki 分叉兼容性与代码结构约定。
  - 目标落点：既有功能不变量、依赖隔离、未来审计登记。
  - 采用方式：reuse existing contracts；不将目录登记当运行态解耦证明。
