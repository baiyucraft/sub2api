# <change-id> 系统测试

## 测试环境

- runtime/platform：<版本>
- fixture/data：<可重复数据>
- 外部依赖：<mock / local / none>

## ST-01 <正常场景>

- 类型：normal
- 前置：<状态>
- 操作：<用户或系统动作>
- 断言：<可观察结果>
- 证据：<command/output/path>

## ST-02 <失败场景>

- 类型：failure
- 前置：<无效输入或故障>
- 操作：<动作>
- 断言：<fail-closed、错误和零副作用>
- 证据：<command/output/path>

## ST-03 <边界场景>

- 类型：boundary
- 前置：<重复、空值、最大值、冲突或平台边界>
- 操作：<动作>
- 断言：<稳定行为>
- 证据：<command/output/path>

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| <criterion> | ST-01 | <evidence> |
