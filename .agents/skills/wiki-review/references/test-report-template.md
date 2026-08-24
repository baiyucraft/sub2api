verification-result: pending
scope: partial

# <change-id> Test Report

## 环境

- runtime/platform：<版本>
- package/tarball：<版本或路径>
- fixtures：<测试数据>

## 命令与结果

| 命令/验证动作 | 结果 | 证据摘要 |
| --- | --- | --- |
| `<command>` | pass / fail / skipped | <output/path/reason> |

## System Test 覆盖

| ST | 类型 | 结果 | 证据 |
| --- | --- | --- | --- |
| ST-01 | normal/failure/boundary | pass/fail/skipped | <evidence> |

## Unit Test 与 TDD 证据

| UT / suite | Red | Green/Refactor | 结果 |
| --- | --- | --- | --- |
| UT-01 | <failure> | <passing command> | pass/fail |

## 成功标准覆盖

| 成功标准 | ST/UT/命令 | 结果 |
| --- | --- | --- |
| <criterion> | <evidence> | pass/fail/skipped |

## 失败、未验证与证据缺口

- 失败：<无 / 详情>
- 未验证：<无 / 原因与影响>
- 证据缺口：<无 / 回退阶段>

## 结论

- 所有 required evidence 通过且 scope full 时，首行改为 `verification-result: pass`、`scope: full`。
