# 上游账号表本地浏览器验证

这些工具只访问 `127.0.0.1:4319`，静态提供本地生产构建并拦截所有 `/api/**` 请求。它们测量真实 Chromium 中的 Vue、JSON 解析、布局、绘制和交互，**不证明生产 API、数据库、网络或真实上游探测性能**。生产构建、浏览器功能和后端负载测量分别记录，未执行的对照保持未测。

本次交付采用代码正确、判断等价、刷新稳定、最终构建及浏览器功能要求。刷新50ms、滚动32ms、交互200ms等原性能目标未完整达成，保留为参考，不作为阻挡交付门槛；不再为指标追加长测和调优，也不声称浏览器性能全部达标。`smoke` 是最终构建的功能验证入口，本轮另做真实2分钟短稳定性检查；CPU、原生滚动与30分钟稳定性工具保留为可选测量，旧构建结果须保留身份，不能冒充本轮最终验证。

## 环境与运行

本轮工具版本：Node `22.23.2`、锁定 `pnpm 9.15.9`、`@playwright/cli 0.1.22`。浏览器的精确版本由每轮 `runtime-info.js` 输出的 `Browser.getVersion` 留证；更换版本需要重新对照，不能合并成绩。

从仓库根目录执行：

```powershell
Push-Location frontend
corepack pnpm@9.15.9 install --frozen-lockfile
corepack pnpm@9.15.9 build
Pop-Location
node .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/browser/serve.cjs
```

静态服务器占用当前终端。在另一个终端逐项运行，日志保留在 `.tmp`：

```powershell
$tool = '.agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/browser/run.ps1'
& $tool smoke > .tmp/upstream-browser-smoke.log
& $tool short-stability-start > .tmp/upstream-browser-short-stability-start.log
# 等待真实 2 分钟；期间不能关闭、导航或复用该浏览器。
& $tool stability-read > .tmp/upstream-browser-short-stability-result.log
& $tool close
```

以下是保留的可选性能与长稳定性入口，本次不要求追加执行：

```powershell
$tool = '.agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/browser/run.ps1'
& $tool refresh-cpu > .tmp/upstream-browser-refresh.log
& $tool native-scroll > .tmp/upstream-browser-scroll.log
& $tool stability-start > .tmp/upstream-browser-stability-start.log
# 等待真实 30 分钟；期间不能关闭、导航或复用该浏览器。
& $tool stability-read > .tmp/upstream-browser-stability-result.log
& $tool close
```

最后在服务器终端按 `Ctrl+C`。不要通过进程名批量关闭其他 Node/Chrome 进程。`stability-read` 读取同一会话的 `page.__stability`；短模式必须有 `finished: true`、实际时长达到 `120,000ms`，只证明2分钟短检查，不能称为30分钟稳定性。可选长模式则须达到 `1,800,000ms`；不能用中断、缩短时钟或较早构建的结果代替所报告的时长。需要复测 CPU 时，先关闭稳定性浏览器，再另起所选模式的检查。

## Fixture 与指标口径

- 固定 200 个账号，每个 Key 有 24 条健康记录，内联质量 v5 数据。账号列表支持分页、搜索、`include_quality`；`page.__upstreamFixture` 可注入延迟、列表失败和质量部分失败。其他 API 使用有界空响应，不触发远程探测。首次列表为 20 行；性能与稳定性脚本明确切到 100 行。
- `smoke.js` 检验未变化 DOM 复用、单行变化、质量失败保留、慢旧页不能覆盖新页、详情暂停与关闭补刷、移动端离屏绘制和独立质量请求为零，公开分页/页长/筛选/排序故障注入也须恢复且无未处理错误。查看所有 `checks`、错误数组及截图；任何 `false` 或非预期错误均不能算通过。注入503产生的预期console错误单独留证，不能静默删除未知错误，也不把预期失败伪报成完全没有错误。
- `refresh-cpu.js` 使用 1440×1000、100 行，标准 1 倍与 4 倍 CPU 降速分别输出。每档无变化、单行变化、全页变化各 30 个样本。主线程耗时从响应发送前的 CDP `TaskDuration` 到响应完成后两次 rAF；交互耗时从点击到两次 rAF。不能只报 Vue/JS 时间或删除解析和布局。30 个样本用于本地 P95，不能声称达到后端 1,000 样本 P99 验收。
- `native-scroll.js` 使用 CDP 原生 `Input.synthesizeScrollGesture`。每帧只采时间，不逐帧读布局或写 `scrollTop`。桌面和移动端、1 倍和 4 倍分别报告；`content-visibility` 减少布局与绘制，不代表移动 DOM 数量下降。
- `stability-start.js` 按真实分钟采集 DOM、监听器、堆、请求数和错误，30 分钟后执行 GC 并保存终态。堆比较须采用相同 GC 口径；临时 DOM/监听器波动不能直接认定泄漏，持续增长也不能被最终平均值掩盖。
- `short-stability-start.js` 使用真实120,000ms、每10秒采样，并由 `stability-read` 读取；最终结果须按短检查标注，不推断30分钟或更长期无泄漏。

正式计量只开一个浏览器，不与 Go 编译、压测、其他自动刷新页面或浏览器 CPU 测试并行；稳定性可以在 CPU 短测全部结束后与低干扰后台验证并行，但须注明负载。CPU 脚本默认不启用 Profiler/Tracing，结束或失败会恢复 CPU、断开观察器并释放会话；诊断 trace 保存在 `.tmp`，诊断负载不能混入正式成绩。

`run.ps1` 为每项短测创建并关闭独立 renderer，稳定性读数保留同一会话。手动直接复用脚本时也必须遵守此隔离约束。显示空白、状态误判或旧响应串页属于功能缺陷，仍须修复；性能原目标未达到则如实记录，不继续以指标阻挡交付，不能调整刷新频率、有效期或度量口径伪造通过。
