# group-preferred-account-pool 实施任务

implementation-mode: tdd

- [x] UT-001：完成关系字段、DTO、快照序列化及旧快照兼容。
- [x] UT-002：完成优先账号池保存的去重、绑定校验、事务回滚和 scheduler outbox。
- [x] UT-003：完成 GET/PUT 管理接口及路由契约测试。
- [x] UT-004：核验解绑、换组、复制分组和复制账号不泄漏优先状态。
- [x] UT-005：统一优先池分割 helper，接入通用网关和 OpenAI 高级调度。
- [x] UT-006：覆盖硬准入、粘性、模型路由、并发回退、等待和重试。
- [x] UT-007：覆盖 OpenAI Top-K、成本/倍率隔离和调度审计字段。
- [x] UT-008：完成 GroupsView、API client、双语文案和 Vitest。
- [x] Gate：运行 Go、Vitest、typecheck、lint、build、diff-check，随后准备 VM Gate；不部署生产。


## 当前证据

- focused Go 普通/unit-tag、关系/API/调度/复制测试通过。
- Go 全量：go test -p 2 -parallel 2 ./... -count=1 通过。
- Go unit-tag 全量：go test -tags=unit -p 2 -parallel 2 ./... -count=1 通过。
- Vitest：294 files / 2045 tests 通过。
- pnpm typecheck、pnpm lint:check、pnpm build 通过。
- release pytest：337 passed, 1 skipped。
- Ent go generate ./ent 连续两次无漂移；git diff --check 通过。
- 生产未部署；VM Gate 待 focused commit/push 后执行。
