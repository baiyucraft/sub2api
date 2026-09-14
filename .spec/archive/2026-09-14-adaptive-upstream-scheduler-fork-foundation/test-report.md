---
verification-result: pass
scope: full
---

# adaptive-upstream-scheduler-fork-foundation Test Report

## 环境

- runtime/platform：Windows；Go module 位于 `backend`
- fixtures：纯值对象、内存 TTFT Guard、内存健康 Registry、既有 service/handler/repository doubles
- 外部依赖：不连接生产数据库、Redis 或上游；未执行 VM Gate、生产部署或线上配置写入

## 命令与结果

| 命令/验证动作 | 结果 | 证据摘要 |
| --- | --- | --- |
| `go test ./internal/forkscheduling/... ./internal/service -run 'TestLegacy|TestForkScheduling|Preferred|PoolModeRetry|Concurrency|RPM' -count=1` | pass | 新增 bridge owner、纯分区、倍率和既有专项测试通过 |
| `go test ./internal/service -count=1` | pass | service 全量回归通过 |
| `go test ./internal/handler -count=1` | pass | handler 全量回归通过 |
| `go test ./internal/repository -count=1` | pass | repository 全量回归通过 |
| `go test ./... -count=1` | pass | backend 所有 Go package 通过 |
| `go vet ./internal/forkscheduling/...` | pass | 独立契约/legacy 包无 vet 告警 |
| `go list -deps ./internal/forkscheduling/...` | pass | 未发现禁止依赖 |
| `git diff --check` | pass | 无 diff whitespace 错误 |
| change 文件尾随空白检查 | pass | proposal/design/tasks/reports/source 无尾随空白 |
| `spec-wiki-lite validate adaptive-upstream-scheduler-fork-foundation --strict --json` | pass | metadata、artifact 和 links 校验通过 |
| `go test -race ./internal/forkscheduling/... ./internal/service -run 'TestLegacy|TestForkScheduling|TestResolveUpstreamSchedulerConcurrency|TestTryAcquireAccountRPM|TestGroupPreferred|TestPoolModeRetry|TestOpenAITTFTGuard|TestUpstreamHealth' -count=1` | pass | 显式使用 LLVM Clang；foundation 相关纯规则、bridge、TTFT、健康、RPM、优先池和并发专项无竞态 |
| `go test -race ./internal/service -count=1` | fail (pre-existing, out of scope) | `GORACE=halt_on_error=1` 首个竞态位于未修改的 `content_moderation.go:1534` 与 `content_moderation_cyber_test.go:264` |
| `go vet ./...` | fail (pre-existing) | 两个既有告警位于本 change 未修改文件，未纳入本 change 修复 |

## System Test 覆盖

| ST | 类型 | 结果 | 证据 |
| --- | --- | --- | --- |
| ST-001 | boundary | pass | import dependency check、pure package vet、CodeGraph impact |
| ST-002 | normal/boundary | pass | concurrency/RPM tests、完整 Go 回归 |
| ST-003 | normal/boundary | pass | legacy partition/rate tests、service scheduler tests |
| ST-004 | normal/failure | pass | `TestLegacyTTFTRuntimeSharesStateAcrossObserverReaderAndExcluder` 及 TTFT suite |
| ST-005 | normal/failure | pass | owner-backed health runtime tests及既有 health service suite |
| ST-006 | regression | pass | handler、repository 与 `go test ./...` |

## Unit Test 与 TDD 证据

| UT / suite | Red | Green/Refactor | 结果 |
| --- | --- | --- | --- |
| UT-01 | 初次无独立契约，出现编译缺口 | contracts/legacy target 与完整回归通过 | pass |
| UT-02 | 原规则直接读取 Account，无法隔离验证 | legacy 分区/比较接线及测试通过 | pass |
| UT-03 | 无独立 retry contract | normalize/policy bridge 与既有 retry tests 通过 | pass |
| UT-04 | TTFT 只能经 service 私有状态访问 | 同一 Guard runtime 测试通过 | pass |
| UT-05 | 健康 bridge 初版存在 Registry 旁路 | owner-backed bridge、无 owner 只读和 service owner 持久化测试通过 | pass |

## 成功标准覆盖

| 成功标准 | ST/UT/命令 | 结果 |
| --- | --- | --- |
| SC-01 | ST-001、依赖检查 | pass |
| SC-02 | ST-003、UT-02、完整 Go 回归 | pass |
| SC-03 | ST-004、UT-04 | pass |
| SC-04 | ST-005、UT-05 | pass |
| SC-05 | ST-002、ST-006 | pass |
| SC-06 | ST-002、ST-003、UT-01/03 | pass |
| SC-07 | ST-006、runtime 装配测试 | pass |
| SC-08 | ST-001、CodeGraph impact、diff review | pass |

## 失败、未验证与证据缺口

- 失败：全局 `go vet ./...` 有两个既有告警，均不在本 change 修改范围。
- foundation-scoped race 已验证通过。
- 完整 service race 仍受未修改的 content moderation 既有竞态影响；该失败不覆盖本 change 的代码路径，已作为残余风险记录。

## 结论

功能测试、普通回归、依赖边界和 foundation-scoped race 全部通过；完整 service race 与全局 vet 的既有问题均已定位到本 change 之外，本报告为 `verification-result: pass`、`scope: full`。
