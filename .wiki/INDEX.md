---
title: Sub2API 项目 Wiki
description: Sub2API AI API 网关的开发、运行和接口知识入口
updated: 2026-08-24
owner: project
---

# Sub2API 项目 Wiki

Sub2API 是面向订阅配额分发和管理的 AI API 网关，负责 API Key 鉴权、上游账号与渠道调度、请求转发、用量计费、并发/速率限制和管理面板。

## SSOT 与边界

- 运行入口和命令：README、DEV_GUIDE、Makefile、backend/Makefile、frontend/package.json。
- 后端启动/依赖：backend/cmd/server/main.go、wire_gen.go。
- HTTP 路由：backend/internal/server/router.go、routes/**。
- 配置：backend/internal/config/config.go 与部署样例。
- 迁移：backend/internal/repository/migrations_runner.go、migration_plan.go、backend/migrations/*.sql。
- 前端：frontend/src/main.ts、router、api、views、components。
- 过程证据在 .spec/changes/**，长期知识在 .wiki/**；不要把凭据写入 Wiki。

## 按任务导航

- [快速上手](./01-快速上手/INDEX.md)
- [开发指南](./02-开发指南/INDEX.md)
- [模块指南](./03-模块指南/INDEX.md)
- [对外方法](./04-对外方法/INDEX.md)
- [文档约定](./00-文档约定/INDEX.md)

## 按模块导航

- [后端请求链路](./03-模块指南/01-后端请求链路.md)
- [前端管理面板](./03-模块指南/02-前端管理面板.md)
- [网关与上游](./03-模块指南/03-网关与上游.md)
- [数据与任务](./03-模块指南/04-数据与任务.md)
- [分叉扩展与兼容性](./03-模块指南/05-分叉扩展与兼容性.md)

## 维护入口

新成员从快速上手开始；修改代码前阅读开发指南；路由/API 变更核对对外方法；Wiki 变更遵循 SpecWiki Lite workflow 并通过 strict validate。
