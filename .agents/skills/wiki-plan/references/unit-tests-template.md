# <change-id> TDD 单元测试

## UT-01 <行为>

- Test：`<test-file>`
- Modify：`<production-file>`
- 映射：ST-<id> / success criterion / safety boundary
- Red：<目标行为缺失时的预期失败；基础设施失败不算>
- Green：<最小完整实现>
- Refactor：<保持覆盖的结构优化>

## UT-02 <失败或边界>

- Test：`<test-file>`
- Modify：`<production-file>`
- 输入：<invalid/boundary>
- 断言：<错误、回滚、幂等或资源释放>

## 覆盖边界

- 不测试仅属于实现细节且不影响 observable contract 的内容。
- 外部依赖使用 fixture/mock，避免连接真实生产系统。
