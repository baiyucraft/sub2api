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

## 管理代理与账号绑定

原生代理目录使用既有接口，不新增兼容路径：

- `GET /api/v1/admin/proxies`
- `GET /api/v1/admin/proxies/all`
- `POST /api/v1/admin/accounts`
- `PUT /api/v1/admin/accounts/:id`
- `POST /api/v1/admin/accounts/data`（导入）

统一绑定合同下，真实代理和代理组都作为正数绑定项返回。响应中的 `binding_type` 区分 `proxy` 与 `proxy_ip_group`；账号创建、更新和导入统一提交 `proxy_id`，服务端按 `proxy_bindings.id` 解析后写入账号。代理组摘要可以包含用于管理 CRUD 的 `proxy_ip_group_id`，但该字段不是账号持久化绑定字段。

`proxy_ip_group_id` 和负数 `proxy_id` 仅作为旧客户端请求的过渡输入，服务端归一化后不得继续写入账号；`proxy_id=0` 表示清除绑定，更新时省略字段表示保持原绑定。代理组绑定只允许 OpenAI OAuth/Setup Token，代理组行不得进入真实代理测速、编辑、删除或出站解析。统一绑定迁移文件 `284_unified_proxy_bindings.sql` 已登记到当前 pending profile 257；在测试、Gate 和发布完成前不得据文档假定已部署。

