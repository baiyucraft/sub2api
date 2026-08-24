---
title: SpecWiki Lite 工作流
description: Wiki change 的阶段、Skills 和归档边界
updated: 2026-07-29
owner: spec-wiki-lite
---

# SpecWiki Lite 工作流

> SpecWiki Lite managed baseline

```text
explore -> propose -> design -> plan -> apply -> review -> archive
```

- `.spec/changes/<change-id>/` 保存 active change artifact。
- `.spec/archive/YYYY-MM-DD-<change-id>/` 保存完成后的证据。
- `.agents/skills/wiki-*` 是 Codex 阶段入口。
- `spec-wiki-lite validate --strict` 是推进和归档前的确定性门禁。
- `.wiki` 只沉淀完成后仍有长期价值的知识，不保存实现过程副本。
