# channel-monitor-shutdown-guard 任务计划

implementation-mode: tdd

## 任务总览

按 runner 生命周期、应用停机顺序、用户端刷新和文档门禁推进；每项产品代码修改前先取得对应 Red 证据。

## 1. Runner 生命周期保护

- [x] 1.1 Red：UT-001/UT-003 验证停机期间仍可提交探测或写入取消历史。
- [x] 1.2 Green：为 runner 增加非阻塞 `Quiesce`，取消任务并阻止新提交，保持 `Stop` 最终 drain。
- [x] 1.3 Green：StartChecked 的存量任务增加 5 秒首探测缓冲，CRUD Schedule 保持立即探测。
- [x] 1.4 Refactor：补充幂等、竞态和 context 注释，运行 runner focused tests。

### CheckList

- [x] Red 原因符合目标行为缺失
- [x] Quiesce/启动缓冲/取消持久化测试通过
- [x] Stop、Unschedule 和 Schedule replacement 回归通过
- [x] 注释与竞态边界检查完成

## 2. 应用停机钩子

- [x] 2.1 Red：wire 初次生成暴露两个无名 `func()` 字段无法区分的问题。
- [x] 2.2 Green：wire 注入 `Application.PrepareShutdown`，main 在 Server.Shutdown 前调用。
- [x] 2.3 Refactor：同步 wire_gen，确保 cleanup 顺序与资源关闭不变。

### CheckList

- [x] 停机回调先于 HTTP shutdown
- [x] Cleanup 仍最终等待 runner
- [x] 生成代码无漂移

## 3. 用户端 V1 刷新保护

- [x] 3.1 Red：UT-004 验证 silent reload 失败会产生红色提示或改变卡片数据。
- [x] 3.2 Green：自动刷新失败静默保留旧 items；手动刷新保留数据并提示错误。
- [x] 3.3 Refactor：补充 Abort 竞态和状态颜色断言。

### CheckList

- [x] 自动/手动刷新测试通过
- [x] 真实业务状态计算不变
- [x] V2 页面未受影响

## 4. 文档与局部门禁

- [x] 4.1 更新 `.wiki/` 渠道监控生命周期和刷新失败边界。
- [x] 4.2 更新 fork extension audit 登记，不登记 `.wiki/.spec` 或技能路径。
- [x] 4.3 执行 focused Go/Vitest、typecheck、lint、`git diff --check` 和 strict validate。

### CheckList

- [x] 长期文档与实现一致
- [x] 局部验证通过
- [x] 未执行 VM Gate、生产部署或真实模型请求
- [x] staged 路径无嵌套生成副本

## 用例到任务映射

| 系统测试 | 大任务 | UT/验证 |
| --- | --- | --- |
| ST-001 | 1、2 | UT-001、UT-003 |
| ST-002 | 1 | UT-002 |
| ST-003 | 3 | UT-004 |
| ST-004 | 1、3 | UT-005 |

## 执行顺序

1 → 2 → 3 → 4。每个 TDD task 按 Red → Green → Refactor 记录证据。

## 暂缓事项

全量门禁、VM Gate、生产部署、历史数据回填和真实 provider 请求。

## implementation 证据

- Red：实现前 runner focused 测试因 `Quiesce` 缺失编译失败；前端测试确认自动刷新失败会调用 `showError`。
- Green：`go test -tags=unit ./internal/service -run 'ChannelMonitor|Monitor' -count=1` 通过。
- Green：`go test ./cmd/server -run 'TestNonexistent' -count=0` 通过。
- Green：`pnpm test:run -- src/views/user/__tests__/ChannelStatusV1View.spec.ts src/components/user/__tests__/MonitorCardGrid.spec.ts src/views/user/__tests__/ChannelStatusView.mode.spec.ts` 通过（5 tests）。
- Green：`go generate ./cmd/server` 通过，确认 wire_gen 可重生成。
