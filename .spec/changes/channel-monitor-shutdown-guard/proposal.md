# channel-monitor-shutdown-guard

## 问题

停机更新期间，HTTP 服务停止接收请求后，渠道监控 runner 仍可能发起一次探测；托管本站 Key 的探测因此收到连接错误并写入 `error` 历史，用户端短暂看到红色状态。启动时 runner 又可能在 HTTP 监听前立即探测，产生同类假失败。

## 目标

- 停机前停止新的后台 V1 探测，并让取消中的 scheduled probe 不落历史、不更新 `last_checked_at`。
- 启动存量监控延迟首轮探测，避免服务尚未监听时产生失败记录。
- 用户端自动刷新失败时保留最近一次真实状态和颜色。

## 非目标

- 不改变真实上游错误、V1 状态枚举、探测重试、V2 监控或数据库结构。
- 不改变新建/编辑/启用监控后的立即探测语义。
- 不部署生产、不执行 VM Gate、不发送真实模型请求。

## 成功标准

- 收到停机信号后 runner 不再提交新的探测，已在途探测被取消且没有新增错误历史。
- 存量监控启动首探测在 5 秒缓冲后执行，CRUD 调度仍立即执行。
- 自动刷新请求失败不会清空卡片、改变状态颜色或弹出红色错误提示；手动刷新仍提示错误但保留数据。
- 相关 Go/Vitest、类型检查、Lint、差异检查和 SpecWiki strict validate 通过。

## 影响范围

- `backend/internal/service/channel_monitor_runner.go`：quiesce、启动缓冲和任务提交边界。
- `backend/cmd/server/main.go`、`backend/cmd/server/wire.go`、`backend/cmd/server/wire_gen.go`：停机前生命周期钩子。
- `frontend/src/views/user/ChannelStatusV1View.vue`：自动刷新失败处理；相关 runner/V1 测试和长期 Wiki。

## 交付形态

single-change

该修复可独立测试、回滚和归档，不依赖 SMTP 或其它未提交 change。

## 风险

- quiesce 必须非阻塞，否则可能延长进程退出；最终等待仍由现有 cleanup 负责。
- 启动缓冲不能影响 CRUD 的立即探测，也不能抑制真实运行期失败。
- 自动刷新错误不能被误判为渠道业务状态。

## 参考资料

- 来源：`ChannelMonitorRunner`、`RunCheck` 的现有取消持久化边界
  - 目标落点：停机取消和 scheduled probe 历史写入
  - 采用方式：direct migration
- 来源：`main.go` 的 `Server.Shutdown` 与 cleanup 顺序
  - 目标落点：停机前 runner quiesce
  - 采用方式：rewrite
- 来源：`ChannelStatusV1View.vue` 的 AbortController 和自动刷新路径
  - 目标落点：刷新失败保留真实状态
  - 采用方式：direct migration
