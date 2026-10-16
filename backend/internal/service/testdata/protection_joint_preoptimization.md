# 联合调度冻结基线

`protection_joint_preoptimization.json` 来自**优化前真实 service 代码**运行 `TestJointProtectionMatchesPreOptimizationGolden` 的输出，不是根据当前实现手写的期望值。

- 原始 Git HEAD：`a45e09dc5b3dfdeeb0390325780fc6d6f2818823`。
- 开始实施时工作树差异 SHA256：`18cc09071d05229a72d8e8e17536651df98f5e7a8e216583b3105a5c1f803c29`，包括账号级 5 秒新鲜／30 秒可用修复。
- 完整旧源重建位置：`.tmp/protection-http-matrix/baseline/backend/`；来源记录为 `.tmp/protection-http-matrix/baseline-provenance.json`。不是仅 Git HEAD。
- 旧源输出和优化源输出 SHA256 均为 `8942a73a002b7c77106cb20ed762385a9e3a3966242c8c856f04090c5ad373ed`。
- 时钟：Go `testing/synctest` 提供固定起点及可控虚拟时钟，直接约束真实业务代码的 `time.Now`；业务源码没有注入测试开关。
- 场景随机源：`rand.NewSource(1017)`；真实调度加权随机源使用固定 session/model/group 派生的原始 seed。

覆盖 144 个独立调度场景：legacy/advanced、cache-first/TTFT-first、不同缓存率和 TTFT 观测、已有低缓存会话及驻留、外层优先级、目标有容量／最优账号满载／全部满载。逐场景记录缓存候选档位和保持／迁移意图、真实抢槽尝试顺序、最终账号及容量、迁移标记、终端准入后 sticky、事务中的自然名额。另对两个 Engine 共享事务存储执行 100 个自然会话的重复请求、后续换模型请求，共比较 200 个持久化 trial plan，检查首个身份和试用序号。

这不代替跨进程竞争、完成消费/清理并发、所有时间边界或完整 HTTP 性能矩阵；异常预算回退及发送前绑定行为使用独立永久回归验证。

复现旧源导出（PowerShell，从仓库根目录；需要保留的完整冻结旧源）：

```powershell
Copy-Item -LiteralPath backend/internal/service/protection_joint_oracle_test.go -Destination .tmp/protection-http-matrix/baseline/backend/internal/service/protection_joint_oracle_test.go
$env:GOTOOLCHAIN = 'go1.27.2'
$env:SUB2API_JOINT_ORACLE_OUTPUT = (Join-Path (Get-Location) '.tmp/protection-perf-results/joint-baseline-trace.json')
Push-Location .tmp/protection-http-matrix/baseline/backend
go test -tags unit ./internal/service -run '^TestJointProtectionMatchesPreOptimizationGolden$' -count=1
Pop-Location
Remove-Item Env:SUB2API_JOINT_ORACLE_OUTPUT
Get-FileHash .tmp/protection-perf-results/joint-baseline-trace.json -Algorithm SHA256
```

当前代码的永久验收（不会导出或更新 golden）：

```powershell
$env:GOTOOLCHAIN = 'go1.27.2'
Push-Location backend
go test -tags unit ./internal/service -run '^TestJointProtectionMatchesPreOptimizationGolden$' -count=1
Pop-Location
```

导出模式只写显式指定的外部输出路径。永久测试默认读取冻结旧源 golden 比较，不会因实际结果变化自动更新。
