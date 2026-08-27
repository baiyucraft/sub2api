---
review-result: pass
scope: full
---

# group-preferred-account-pool Review Report
---

## Review 范围

- artifacts：proposal、design、system-tests、unit-tests、tasks、meta、research/codegraph
- implementation diff：当前工作区相对 `2270dd4f59f0e2614eeede406b7dab01a448533f` 的本 change 文件；排除既有 `EmailVerifyView.vue` 与 `EmailVerifyView.spec.ts`
- selected standards：general、Go、frontend、migration/release-contract
- exclusions：注册邮件垃圾箱提示是既有用户改动，不属于本 change，未纳入提交与结论
---

## Findings

| 优先级 | 位置 | 问题 | 影响 | 修复/回退阶段 |
| --- | --- | --- | --- | --- |
| - | - | 无 blocking 或 non-blocking finding | - | - |
---

## Artifact 一致性

| Artifact / success criterion | 实现与证据 | 结果 |
| --- | --- | --- |
| 分组关系增加独立 `scheduler_preferred` 且默认 false | `backend/migrations/253_group_preferred_account_pool.sql`、Ent schema/generated code、service model | pass |
| GET/PUT 优先账号池严格限定当前分组并原子替换 | `admin_group.go`、`group_repo.go`、handler/routes、focused Go tests | pass |
| 解绑、换组、复制不泄漏优先状态 | `BindGroups` 保留现存关系标记、新关系默认 false；复制账号/分组测试 | pass |
| 合法粘性、模型/能力/健康/利润/并发硬准入优先于优先池 | OpenAI scheduler 与通用 gateway 分池代码、sticky focused tests | pass |
| 优先池忽略倍率/成本，普通池保持原算法 | preferred partition helpers、OpenAI compact/cost tests、gateway fallback test | pass |
| outbox 与 scheduler snapshot 及时刷新 | repository outbox payload、scheduler cache projection、migration/integration contracts | pass |
| GroupsView/API/双语文案和数量标记 | `GroupsView.vue`、admin groups API、locale、Vitest | pass |
| 不修改生产环境 | 本次仅本地代码、测试、Wiki 和 commit；VM Gate 仅开发环境 | pass |
---

## 安全、Ownership 与回滚

- path/input safety：账号 ID 正数校验、去重、当前分组绑定完整校验；非法输入 fail-closed。
- 用户内容保护：不新增敏感信息输出；前端只展示管理员已有账号元数据。
- 失败原子性/rollback：优先关系替换与 outbox 在同一 SQL 事务内；非法账号或 outbox 失败不保留部分关系更新；解绑最后一个分组后显式刷新账号快照。
- schema rollback：migration 253 幂等，仅新增列和局部索引，不删除业务数据。
---

## 残余风险

- VM Gate 的真实 upstream/model streaming 能力不在本 change 范围内；Gate 按 health/API/UI 与隔离测试证据执行，不发送真实模型请求。
---

## 结论

- full scope、required evidence 全部通过，无 blocking finding，允许进入 verification/archive。