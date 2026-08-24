# <change-id> 设计方案

## 方案概述

<核心设计、边界和为什么满足 proposal。>

## 接口与稳定合同

- <CLI/API/file/schema/path/field>

## Ownership 与数据/文件流

```text
<source of truth> -> <operation> -> <managed/user-owned output>
```

## 正常流程

1. <步骤与可观察结果>

## 失败、边界与回滚

- 无效输入：<fail-closed>
- 重复/并发：<幂等或冲突>
- 部分失败：<原子性与恢复>
- 用户内容：<保护规则>
- path safety：<containment/symlink 规则>

## 验证设计

- <测试 seam、输出、命令和成功标准映射入口>

## Wiki 与长期合同落点

- `<page>`：<更新内容与 SSOT>

## 参考边界

- 来源：<source>
- 目标落点：<target landing area>
- 采用方式：direct migration / rewrite / inspiration only

## 回滚

- <如何恢复且不破坏用户内容>

## CodeGraph-derived design constraints

- entry points and call paths: <入口与调用路径>
- ownership and dependency boundaries: <ownership 与依赖边界>
- impact radius: <影响半径>
- affected tests: <受影响测试>
- rollback boundary: <回滚边界>
- graph evidence vs source verification: <CodeGraph 证据与源码核验差异>
- unresolved items: <未确认项及处理>
