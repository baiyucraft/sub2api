# channel-monitor-streaming 任务计划

implementation-mode: direct

## 任务总览

按主动探针协议、TTFT 数据契约、上游账号路径、前端展示和验证拆分；每个 task 完成后运行局部检查。

## 1. 渠道监控流式探针

### direct 模式

- [x] 1.1 修改 checker adapter/body/request，默认启用 SSE 并保留 replace 非流式兼容。
- [x] 1.2 增加 provider 事件解析、TTFT、终端和中断状态。
- [x] 1.3 添加 checker/service/runner focused tests。

### CheckList

- [x] SSE 首字和终端覆盖
- [x] 中断、空流、取消覆盖
- [x] 局部 Go tests 通过

## 2. TTFT 数据契约

### direct 模式

- [x] 2.1 新增幂等 migration 和 Ent 字段。
- [x] 2.2 贯通 service/repository/handler/API 类型与 latest/timeline。
- [x] 2.3 添加旧 NULL 兼容测试并重新生成 Ent。

### CheckList

- [x] migration checksum/profile 合同更新
- [x] API 可空字段正确
- [x] 旧数据不回填

## 3. 上游账号探针

### direct 模式

- [x] 3.1 Grok 改 SSE。
- [x] 3.2 Antigravity 主动测试改流式解析，不改变普通业务路径。
- [x] 3.3 回归 Juice、TTFT、错误率和取消测试。

### CheckList

- [x] Grok/Antigravity 无整包主动探针路径
- [x] 完整响应与可信度降级分离
- [x] focused tests 通过

## 4. 前端与文档

### direct 模式

- [x] 4.1 更新 admin/user API 类型与监控组件展示首字/总耗时。
- [x] 4.2 更新 `.wiki/` 与 fork audit 登记。
- [x] 4.3 补 Vitest 与文档校验。

### CheckList

- [x] 新旧 TTFT 展示正确
- [x] V2 页面/统计不变
- [x] Wiki/audit 有长期记录

## 5. 验证与 VM Gate

### direct 模式

- [x] 5.1 运行 focused、全量 Go、Vitest、typecheck、lint、build。
- [x] 5.2 运行 strict validate、audit、diff-check。
- [x] 5.3 生成同一完整 SHA candidate 并通过 VM Gate；不部署生产。

### CheckList

- [x] 所有 ST 有证据
- [x] migration/生成文件无漂移
- [x] VM Gate 通过，生产 not_checked

## 执行证据

- Go focused：`go test -tags=unit ./internal/service -run 'TestRunCheckForModel|TestRunUpstreamHealthProbe...' -count=1` 通过。
- Go aggregate：`go test -p 2 -parallel 2 ./... -count=1` 与 `go test -tags=unit -p 2 -parallel 2 ./... -count=1` 通过。
- Frontend：`pnpm test:run`（291 files / 2031 tests）、`pnpm typecheck`、`pnpm lint:check`、`pnpm build` 通过。
- Ent：`go generate ./ent` 重复生成无漂移。
- Migration：`channel_monitor_streaming_ttft_migration_test.go` 通过；历史 `ttft_ms` nullable、不回填。
- 发布边界：VM Gate 使用 profile 242 health-only；真实 provider 流式请求不发送，记为 `not_checked`；生产未修改、未部署。

## 用例到任务映射

| 系统测试用例 | 大 task | 小 task / 验证 |
| --- | --- | --- |
| ST-01/ST-02/ST-03 | 1/3 | 1.1-1.3/3.1-3.3 |
| ST-04 | 2/4 | 2.1-2.3/4.1-4.3 |
| 全部 | 5 | 5.1-5.3 |

## 执行顺序

1. checker 与 parser；2. TTFT schema/repository；3. upstream Grok/Antigravity；4. frontend/docs；5. aggregate/VM Gate。

## 暂缓事项

- 不实现监控页面 SSE/WebSocket 推送。
- 不发送真实模型请求；VM/生产流式能力按发布门禁记录 `not_checked`。
