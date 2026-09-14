# adaptive-upstream-scheduler-fork-foundation 任务计划

implementation-mode: tdd

## 任务总览

按“契约/纯 legacy 规则 -> service bridge -> 旧入口回归 -> 文档和质量门禁”推进。第一份 change 只收口当前 fork 能力，不实现错误归因、预算、故障域、缓存容量、TTFT 新算法或灰度策略。

## 1. 建立独立契约和值对象

- [x] 1.1 Red：新增契约测试并确认原代码缺少独立包，首次实现反馈编译红
- [x] 1.2 Green：在 `backend/internal/forkscheduling/contracts.go` 增加 Account/Candidate/Concurrency/TTFT/Health/RPM/Retry 窄值和接口
- [x] 1.3 Green：在 `backend/internal/forkscheduling/legacy/` 增加容量解析、target、优先池比较和 retry 归一化纯规则
- [x] 1.4 Refactor：确认契约包不接收 `service.Account`、官方 scheduler、Repository、Redis 或任意执行闭包

### CheckList

- [x] 契约/legacy 包独立编译
- [x] target、容量、优先池、倍率和 retry 测试通过
- [x] 无公开 HTTP API、数据库字段或迁移
- [x] import 边界检查完成

## 2. 接入 service 兼容 bridge

- [x] 2.1 Red：bridge 接入前 TTFT/健康/容量仍只能通过具体 service 类型访问
- [x] 2.2 Green：新增 `fork_scheduling_bridge.go`，映射 Account/TTFT/Health/RPM 到窄契约
- [x] 2.3 Green：OpenAI TTFT、健康调度、Account target、优先池排序和 pool retry 入口委托 legacy/bridge
- [x] 2.4 Refactor：保留原构造、全局兼容外观、可选依赖 fallback 和原调用副作用

### CheckList

- [x] TTFT Observer/Excluder/Reader 共享同一个 legacy Guard
- [x] 健康 Reader/Evidence/Lifecycle 不创建第二个 Registry
- [x] RPM 预读与最终原子准入分离
- [x] slot/lease、健康持久化和 handler 执行权未迁移

## 3. 兼容回归与副作用守护

- [x] 3.1 Red：首次 bridge 编译暴露字段和类型映射缺口，修复后重新运行定向测试
- [x] 3.2 Green：补齐 bridge、纯包和 legacy 等价测试
- [x] 3.3 Refactor：用 Unix 秒级时间比较恢复原优先池随机分组语义
- [x] 3.4 执行 service、handler、repository 和完整后端回归，记录最终证据

### CheckList

- [x] 定向纯包和 service 测试通过
- [x] service/handler/repository 全量回归通过
- [x] static/import 检查通过
- [x] foundation-scoped race 检查通过（Clang + `go test -race` 覆盖 `forkscheduling`、bridge、TTFT、健康、RPM、优先池和并发专项）
- [x] 完整 service race 已执行；首个竞态位于既有 `content_moderation.go` / `content_moderation_cyber_test.go`，不在本 change 修改范围，作为残余风险保留
- [x] 未新增后台 goroutine、文件、网络或生产配置写入

## 4. 变更文档与质量门禁

- [x] 4.1 生成 system-tests、unit-tests 和本 tasks，并同步 implementation metadata
- [x] 4.2 运行 SpecWiki strict validate、链接/格式检查和 change 范围代码质量命令
- [x] 4.3 review/verification 前不归档；长期 Wiki/catalog 已在 review 通过前完成更新

### CheckList

- [x] 所有成功标准有测试或验证映射
- [x] 文档和代码状态一致
- [x] 官方变更写集与 bridge 边界复核完成
- [x] 本 change 未执行 VM Gate、生产部署或线上写入

## 用例到任务映射

| 系统测试 | 大任务 | 小 task / UT |
| --- | --- | --- |
| ST-001 | 1、4 | 1.4、4.2 |
| ST-002 | 1、2、3 | UT-01、UT-05、3.4 |
| ST-003 | 1、2、3 | UT-02、UT-03、3.4 |
| ST-004 | 2、3 | UT-04、3.4 |
| ST-005 | 2、3 | UT-05、3.4 |
| ST-006 | 2、3、4 | 2.4、3.4、4.2 |

## 执行顺序

1. 先完成契约和值对象及其纯测试。
2. 再接入 service bridge 和旧入口委托。
3. 运行定向测试后，执行 service/handler/repository 与完整后端回归。
4. 完成静态边界、SpecWiki、格式和 review/verification 门禁后，才允许 archive；不在本 change 部署。

## 暂缓事项

- 后六个 adaptive scheduler child 的实现。
- 旧 traffic/probe 来源覆盖风险的行为修复。
- `.wiki/` 与 fork extension catalog 的长期页面更新，等待实现 review 通过。
- VM Gate、生产部署、数据库迁移和线上配置写入。

## implementation 证据

- Red：首次 service 编译发现 `extraValue` 被错误移出 service 适配层；随后发现 `CandidateView.Model` 和健康状态类型映射缺口，均在 Green 前修复。
- Green：`go test ./internal/forkscheduling/... ./internal/service -run 'TestResolveUpstreamSchedulerConcurrency|TestTryAcquireAccountRPM|TestGroupPreferred|TestPoolModeRetry|TestOpenAITTFTGuard|TestLegacy|TestForkScheduling' -count=1` 通过。
- Green：纯契约/legacy 测试、现有 service 定向 TTFT/健康/RPM/优先池回归通过；健康 owner 与纯分池/倍率接线补充测试通过。
- Green：`go test ./internal/service -count=1`、`go test ./internal/handler -count=1`、`go test ./internal/repository -count=1` 和完整 `go test ./... -count=1` 均通过。
- Green：`go vet ./internal/forkscheduling/...`、依赖隔离检查、CodeGraph impact/affected、change 文件尾随空白检查、`git diff --check` 和 `spec-wiki-lite validate adaptive-upstream-scheduler-fork-foundation --strict --json` 通过。
- Green：显式使用 LLVM Clang 执行 `go test -race ./internal/forkscheduling/... ./internal/service -run 'TestLegacy|TestForkScheduling|TestResolveUpstreamSchedulerConcurrency|TestTryAcquireAccountRPM|TestGroupPreferred|TestPoolModeRetry|TestOpenAITTFTGuard|TestUpstreamHealth' -count=1` 通过。
- 残余风险：完整 `go test -race ./internal/service -count=1` 首个竞态位于未修改的 `content_moderation.go:1534` 与 `content_moderation_cyber_test.go:264`；全局 `go vet ./...` 仍报告既有的 `admin_service_stub_test.go` 锁复制和 `upstream_auth_session.go` 自赋值，均不在本 change 修改范围。
