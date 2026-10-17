# PostgreSQL / Redis 正确性与查询计划验证

本入口仅连接 `.ssh.local` 的 `local_vm`。每轮创建随机独占 PostgreSQL 数据库、角色、无持久化 Redis 容器及 `/tmp` 目录；最终删除并查询确认不存在。测试不启动应用，不修改持久开发数据库，不是签名 VM Gate 或旧镜像联调结果。

先在 `backend` 使用项目工具链编译：

```powershell
$env:GOTOOLCHAIN='go1.27.2'
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
go test -tags upstreamtablepg -p 2 -parallel 2 -c -o ../.tmp/upstream-table-pg-linux.test ./internal/repository
```

随后在仓库根运行：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/validate.py
```

查询计划额外要求先用同目录 `prepare.py --version baseline` 冻结 `8afa2ffe47800773f781279e945bf95d6c2e1067`，再运行：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/validate.py --explain --output .tmp/upstream-table-pg-explain
```

输出包含测试日志、二进制及源码摘要、迁移摘要、清理确认，以及可选的 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)`。SQL 从冻结基线及当前源文件提取，质量查询使用相同数据库事务和固定时间。

四项检查分别覆盖：

- 健康历史 24 条顺序、同时间戳 ID 规则、软删除、完整七日 confidence 与旧 Ent/Go 结果等价；另测 JSON / null、坏历史字段、纳秒与微秒边界、checks 全部不同的最坏形状。
- 账号与分组质量统计对照冻结旧 SQL，包括窗口边界、无 duration 的活动样本、零倍计费和重复错误去重。
- migration 300 双次执行、索引列与方向、旧列合同与旧 SQL 读写。此项仅证明合成 schema / SQL 兼容，不能代替在迁移后 schema 上启动旧应用镜像。
- 页内 proxy 并发 Redis pipeline 的去重、账号隔离和过期边界，与原单目标读取一致。

小规模查询计划用于解释节点和扫描量，不能单独宣称整体性能达标。计时受缓存、数据形状和同机负载影响；P95/P99、CPU、网关影响须由另一个同负载矩阵独立测量。race 也不是该二进制的测试能力：`CGO_ENABLED=0` 编译不包含 race detector。
