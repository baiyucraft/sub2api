---
title: 边界与 SSOT 规则
description: 长期 Wiki、change artifact、临时材料和代码事实的边界
updated: 2026-07-29
owner: spec-wiki-lite
---

# 边界与 SSOT 规则

> SpecWiki Lite managed baseline
>
> 本页由 package 管理。普通 update 保留本地修改，只有 `update --force` 才恢复内置版本。

| 类型 | 位置 | 职责 |
| --- | --- | --- |
| 长期知识 | `.wiki/**/*.md` | 稳定说明、导航、开发约定和公开契约 |
| 变更过程 | `.spec/changes/**` | proposal、design、tests、tasks 和报告 |
| 历史证据 | `.spec/archive/**` | 已完成 change 的只读归档 |
| 代码事实 | 源码、配置、测试 | 行为、字段、默认值和运行入口的最终事实 |
| 临时材料 | `.tmp/` 或项目约定目录 | 调试输出和一次性研究 |

- 同一事实只维护一个权威来源，其他页面写摘要并链接。
- 不把 change artifact 原文复制进 Wiki。
- 未确认假设和一次性验证结果不写成长期事实。
- 新增或移动页面后同步更新所在目录及上级 `INDEX.md`。
- 任何相对 `upstream/main` 的 fork 专属业务、数据、发布或运维差异，都必须在实现完成后沉淀到 `.wiki`；机器可读登记同步写入 `sub2api-fork-extension-audit/references/extensions.yaml`。
- `.spec/**`、`.wiki/**` 和 `.agents/skills/wiki-*` 是治理/知识资产，不登记为 fork 业务扩展路径；它们的长期规则只通过 Wiki 页面和 workflow 维护。
- 使用 `sub2api-fork-extension-audit` 或 `sub2api-production-deploy` 发现新的稳定合同时，先更新对应模块 Wiki，再进入 review/archive；过程证据仍留在 `.spec`、`.tmp` 或受控 release 目录。
