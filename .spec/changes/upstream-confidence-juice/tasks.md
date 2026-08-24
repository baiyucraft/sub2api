# upstream-confidence-juice 任务计划

implementation-mode: direct

## 任务总览

按探针合同、统计/清理、前端展示和验证四个能力块执行。

## 1. Juice 探针合同

- [x] 1.1 替换 OpenAI 算术挑战为 high Juice 模板池、归一化和分类。
- [x] 1.2 固定 effort/version，记录分类计数和 mixed/样本不足状态。
- [x] 1.3 补 service focused tests。
- [x] 1.4 固定可信度配置缺失/失败为 disabled，区分显式启用状态。

### CheckList

- [x] upstream high 指纹与模板一致
- [x] 异常不进入有效样本
- [x] service tests 通过

## 2. 统计与清理

- [x] 2.1 改 repository 聚合过滤和百分比分母。
- [x] 2.2 增加幂等旧可信度字段清理迁移/入口。
- [x] 2.3 补 repository/migration tests。

### CheckList

- [x] 24h/7d 仅统计有效返回
- [x] 普通健康字段保留
- [x] 清理范围与事务边界明确

## 3. 前端展示

- [x] 3.1 非 OpenAI 隐藏可信度。
- [x] 3.2 健康和可信度徽标改为同行布局。
- [x] 3.3 补 Vitest DOM assertions。

### CheckList

- [x] 无可信度占位
- [x] OpenAI 同行显示
- [x] 前端测试通过

## 4. 验证与数据清理

- [x] 4.1 运行 focused/aggregate Go、Vitest、typecheck/build。
- [x] 4.2 strict validate、diff 和敏感字段检查。
- [x] 4.3 修正后台 due 调度与真实流量抑制关系，加入发送前二次 freshness 检查；手动探针保持强制。
- [ ] 4.4 生产/目标数据库先只读计数，再执行幂等清理并复核。

### CheckList

- [x] 所有 ST 有证据
- [ ] 清理前后 allowlisted count 已记录
- [x] 工作区无未声明变更

## 用例到任务映射

| ST | 任务 | 证据 |
| --- | --- | --- |
| ST-01/ST-03 | 1 | Go tests |
| ST-02/ST-05 | 2/4 | repository tests/SQL output |
| ST-04 | 3 | Vitest |
| 全部 | 4 | aggregate/validate |

## 执行顺序

先实现并测试 service，再 repository/清理，再前端，最后聚合验证和授权数据清理。

## 暂缓事项

- 不实现其它平台可信度检测和多 effort 指纹。

## implementation 证据

- 配置状态与触发 focused：`go test ./internal/service -run 'TestGetUpstreamConfidenceProbeSettingsState|TestNormalizeUpstreamConfidenceProbeSettings|TestUpstreamConfigServiceListDueHealthProbeKeys|TestUpstreamConfigServiceProbeDueKeyRechecksTrafficFreshness|TestUpstreamHealthProbeRunner' -count=1`，通过。
- service aggregate：`go test -p 2 -parallel 2 ./internal/service -count=1`，通过。
- Go aggregate：`go test -p 2 -parallel 2 ./... -count=1`，通过。
- 前端：`pnpm vitest run`（282 files/1935 tests）、`pnpm typecheck`、`pnpm build`，通过。
- 收口：`git diff --check`、`spec-wiki-lite validate upstream-confidence-juice --strict --json`，通过。
- 4.4 生产/目标数据库清理仍未执行，保持未勾选；本轮不做生产写入。
