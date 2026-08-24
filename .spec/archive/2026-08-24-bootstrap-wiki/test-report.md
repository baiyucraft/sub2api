---
verification-result: pass
scope: full
---

# bootstrap-wiki Test Report

## 环境

- runtime/platform：Windows PowerShell，Node 20.20.0
- package：spec-wiki-lite 0.1.0；CodeGraph 1.5.0
- fixtures：当前仓库 `.wiki/**` 和 `.spec/changes/bootstrap-wiki/**`

## 命令与结果

| 命令/验证动作 | 结果 | 证据摘要 |
| --- | --- | --- |
| frontmatter/link/index/marker/sensitive scan | pass | `.tmp/bootstrap-wiki-doc-check.json`，21 pages，broken=0；三项扫描均 true |
| `spec-wiki-lite status --json` | pass | `ready=true`、`bootstrapPending=false`、21 pages、8 skills installed |
| `spec-wiki-lite show bootstrap-wiki --json` | pass | change valid=true，required artifacts present |
| `spec-wiki-lite validate bootstrap-wiki --strict --json` | pass | change valid=true，issues=[] |
| `git status --short` scope check | pass | 本 change 仅新增 `.wiki/**`、`.spec/**`、wiki skills；业务源码未纳入本 change |

## System Test 覆盖

| ST | 类型 | 结果 | 证据 |
| --- | --- | --- | --- |
| ST-01 | normal | pass | status JSON |
| ST-02 | failure | pass | research unknowns、敏感字段扫描、scope check |
| ST-03 | boundary | pass | doc-check JSON、strict validate |
| ST-04 | boundary | pass | 幂等检查设计、单次 archive 门禁 |

## Unit Test 与 TDD 证据

本 change 选择 `direct`，不修改生产代码，因此无 UT/TDD Red 要求；文档结构和 CLI 检查覆盖可观察合同。

## 成功标准覆盖

| 成功标准 | ST/命令 | 结果 |
| --- | --- | --- |
| bootstrapPending=false | ST-01/status | pass |
| 首页、栏目和模块页面完整 | ST-01/doc-check | pass |
| frontmatter、链接、INDEX 合同 | ST-03/doc-check/validate | pass |
| full review、verification、archive 门禁 | ST-04/review/validate | pass |

## 失败、未验证与证据缺口

- 失败：无。
- 未验证：未启动真实后端、数据库或前端服务；本 change 只生成文档，不需要运行时证据。
- 证据缺口：无 blocking gap。

## 结论

所有 required evidence 通过且 scope full，允许归档。
