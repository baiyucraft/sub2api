---
review-result: pass
scope: full
---

# adaptive-upstream-scheduler-fork-foundation Review Report

## Review 范围

- artifacts：proposal、research、design、system-tests、unit-tests、tasks、meta
- implementation diff：当前工作树中 `backend/internal/forkscheduling/`、`backend/internal/service/fork_scheduling_bridge.go` 及已登记的 service 接入点
- selected standards：general / Go
- exclusions：未审查 parent 及后六个 child 的实现；未执行 VM Gate、生产部署或线上配置写入

## Findings

| 优先级 | 位置 | 问题 | 影响 | 修复/回退阶段 |
| --- | --- | --- | --- | --- |
| P2 | 仓库既有并发风险 | 完整 `go test -race ./internal/service -count=1` 的首个竞态位于未修改的 `content_moderation.go:1534` 与 `content_moderation_cyber_test.go:264` | 不影响 foundation-scoped race 证据，但完整 service race 仍不能作为全仓库无竞态证明 | 由 content moderation 独立 change 修复；本 change 不扩大范围 |

除上述环境证据缺口外，当前 diff 未发现可复现的 P0/P1 correctness、ownership、路径安全或资源释放缺陷。

## Artifact 一致性

| Artifact / success criterion | 实现与证据 | 结果 |
| --- | --- | --- |
| SC-01、SC-08 | `go list -deps ./internal/forkscheduling/...` 无 service、handler、repository、Ent、Gin、Redis 或 adaptive scheduler 依赖；bridge 位于 service | pass |
| SC-02 | legacy 比较/分区规则接入 service active selector；完整 Go 回归通过 | pass |
| SC-03 | 单 Guard runtime 测试及现有 TTFT 回归通过 | pass |
| SC-04 | HealthEvidence/生命周期写入通过 service owner；owner 捕获与无 owner 只读测试通过 | pass |
| SC-05 | target、容量 bridge、handler 与完整 Go 回归通过 | pass |
| SC-06 | RPM reader/limiter 与 pool retry legacy 测试、完整 Go 回归通过 | pass |
| SC-07 | 未注入 adaptive 能力时 legacy runtime 保持可用；runtime 装配测试通过 | pass |

## 安全、Ownership 与回滚

- path/input safety：本 change 无新文件路径、命令输入、HTTP API 或用户可控执行入口。
- 用户内容保护：窄契约只复制有界账号/候选/健康值，不传递凭据、请求正文、Cookie、Token 或完整错误正文。
- 失败原子性/rollback：健康 traffic evidence 通过 `UpstreamConfigService` 的 key lock、持久化节流、失败回滚和账号投影同步；无 owner 的 runtime 不直接修改 Registry。
- 官方隔离：官方 selector、handler、failover、协议、usage/billing 和数据库 owner 保持原职责；fork 纯分区、比较和兼容值位于 `forkscheduling`/bridge。

## 残余风险

- foundation-scoped race 已在当前 Windows 环境取得证据；完整 service race 的既有竞态已定位到 content moderation，未触及本 change 文件。
- 全局 `go vet ./...` 仍有两个不在本 change 修改范围的既有告警：`admin_service_stub_test.go` 锁复制、`upstream_auth_session.go` 自赋值。
- `codegraph affected` 返回的测试集合较宽，不能替代已执行的专项和完整 Go 测试；已用 impact 结果交叉核对关键健康、分池、倍率和 bridge 符号。

## 结论

当前实现已完成功能、边界、依赖隔离和 foundation-scoped 并发回归审查；完整 service race 与全局 vet 的既有问题已明确定位且不属于本 change，未发现本 change 的 blocking finding，可以进入 verification。
