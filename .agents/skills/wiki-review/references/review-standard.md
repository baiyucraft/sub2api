# Review 通用标准

## 适用范围

所有 SpecWiki Lite change 的 full/partial review。必须同时读取 proposal、design、system tests、tasks、metadata、diff 和测试证据。

## Blocking / P0

- 安全：注入、路径逃逸、命令执行、权限绕过、敏感信息泄露或危险反序列化。
- 正确性：空值/边界/错误分支、状态迁移、幂等、部分失败后不一致或资源未释放。
- Artifact drift：实现偏离 goals/non-goals/success criteria、design 或 task/test mapping。
- 接口/兼容：CLI、schema、路径、错误、公共输出或 ownership 与已确认合同冲突。
- 测试缺口：关键失败、安全、迁移、回滚或成功标准无可信证据。

## Non-blocking / P1/P2

- 明显性能退化、无界加载或重复 IO。
- 职责混杂、重复逻辑、隐式状态或难以测试的耦合。
- 可观测性、错误上下文或兼容说明不足但不阻断当前正确性。

## 建议工具化

formatter、lint、type checker、static analyzer、coverage、dependency scanner 和 diff check 用于稳定机械问题；人工 review 聚焦行为、安全、契约和证据。

## 非目标与误报

- 不因个人命名/格式偏好给 blocking。
- 不审查未参与本 change 的历史债务或第三方源码。
- 不把“未运行某个非必需工具”本身当缺陷；判断它是否造成证据缺口。
