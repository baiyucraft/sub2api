---
title: HTTP API 与认证
description: common、用户、管理员、网关和支付 API 分组
updated: 2026-08-24
owner: backend
---

# HTTP API 与认证

- common：健康、状态和 setup。
- `/api/v1/auth`、`/users`、`/model-plaza`：登录、用户和模型发现。
- `/api/v1/admin/**`：管理员面，使用 admin auth、审计、合规和按风险 step-up。
- gateway：API Key 模型调用、流式和兼容协议。
- payment：订阅、充值、支付回调和账单。

JWT middleware 保护用户/管理员 API，API Key middleware 保护网关；Axios client 负责 token refresh 和统一错误。查具体接口时先看 routes，再看 handler、DTO、service 和前端 API。

