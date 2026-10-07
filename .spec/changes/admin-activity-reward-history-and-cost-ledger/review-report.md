---
review-result: pass
scope: full
---

# 用户领奖与成本审查报告

## 范围与标准

审查本 change 的 proposal、design、ST/UT、tasks、metadata、全部实现 diff 与真实证据。采用 general、Go、frontend、Python review standards；包含 SQL 迁移和 Shell 发布合同。root 与前端代理独立交叉核验历史、成本和缓存入口，成本代理核验事务及真实 PostgreSQL 测试，发布代理核验 profile 全执行链与历史不可变边界。

范围为本地实现、验证、治理和版本化发布合同。生产执行、正式候选镜像与签名 Gate 不在当前授权范围；它们仍是发布前的必需门禁，不由合成数据库测试替代。

## Findings 与处理

| 位置 | 发现 | 处理及证据 | 状态 |
| --- | --- | --- | --- |
| UserBalanceHistoryModal / UsageView | 弹窗保持打开时换用户、用户详情响应乱序可能显示旧用户流水 | 监听 show/user.id/initialType；详情请求版本、关闭/卸载失效；4 个 Red 用例转 Green | resolved |
| OpenDailyGift | 同键并发两次首次查询为空，日唯一冲突后原先误报未达标 | 冲突后重查同键并校验成本后提交；不同键继续拒绝重复日领奖；sqlmock Red/Green 与 VM 8 并发通过 | resolved |
| 发奖余额更新 | UPDATE 0 行原先可能提交无余额入账的奖励/成本 | RowsAffected 必须为 1；软删除回滚，失败不清缓存；unit/VM 均通过 | resolved |
| Draw 机会游标 | 原先未检查 rows.Err，可能吞掉游标错误 | 检查错误、关闭游标并回滚；Red/Green 通过 | resolved |
| 历史 DTO 投影 | 新模型须避免扩大旧用户/分组投影 | 使用原 shallow mapper，旧字段保留，新增字段显式映射 | resolved |
| Release 旧 fixtures 与文档 | 新 current 264 后旧测试上界和部分说明仍为 263 | 历史 profile 全字典与基线相同；当前 264、未知 265；修正 current fixture 与文档，最终 aggregate 763 pass / 3 既有环境 skip | resolved |

## 合同一致性

- 历史：独立服务读模型，旧响应字段保留；credited 全历史累计包含零额且独立于筛选分页。各来源提供 offset+limit 再混排，排序按发生时间倒序、来源升序、原始 ID 倒序；前端组合键避免跨表 ID 冲突。充值沿用原口径。
- 事务：金额从 PostgreSQL NUMERIC 原值直接复制；奖励、机会、余额与成本同事务，来源冲突和任何写失败回滚。幂等重放只修补/校验成本，不重复增加余额或扣机会。
- 历史：289 新迁移，旧 SQL/hash 不改；共享锁覆盖来源事实集合，重复补录只追加缺项并验证完整快照。原创建时间/上海成本日保留，非 credited 排除，人工流水完整。
- 自动成本：奖励唯一关联与用户/类型审计快照，无级联删除；服务、仓储及 HTTP 边界拒绝手工自动成本或保留幂等键，自动行不能冲正。
- 缓存：在线发奖只有成功提交后失效余额、Dashboard 和管理员统计查询缓存。SQL 历史补账在应用启动前运行；生产尾部补账后须按独立授权发布合同主动重建进程内缓存和清理受控 Redis snapshot，再复核统计，不能以 TTL 等待冒充清理。
- 发布：连续 profile 264 继承 263，版本仍为 0.2.14-baiyu，仅追加 289；全部祖先/profile/schema/签名/VM/生产/备份/清理入口支持 264，未知 265 拒绝。没有新增恢复分类豁免，也没有改写旧 Gate 或签名证据。
- 治理：长期 Wiki 只沉淀稳定接口、账本、日期、幂等和发布操作合同；fork audit 记录奖励/成本/profile 差异，共享 DTO/wire/API/i18n 路径复用既有基础扩展登记。

## 安全、ownership 与回滚

本次 VM 连接从 `.ssh.local` 读取，仅作为输入；日志输出采用断言结果和数量/金额白名单，无凭据、完整环境或 DSN。集成程序强制隔离数据库前缀，自建随机 schema，远端临时资源和数据库清理完成，原开发实例保留。补账 CLI 默认只读 check，apply 必须显式声明旧实例排空；生产执行单独授权，不自动进入 release runner。

新增关联为无级联的审计快照；数据库升级追加兼容结构，不能通过人工删除/冲正自动成本作为回滚。生产回滚和旧镜像兼容性仍服从现有数据库与恢复门禁。

## 残余风险与结论

无未解决的本次 correctness finding。最终后端全量、前端 3195 测试与质量/构建门禁、release 763 测试、真实 VM 数据库和补账 CLI 已通过，本 change strict validate 与差异检查通过，完整范围审查结论为 pass。未来生产门禁、三项既有 Windows 环境 skip 和既有 Wiki 坏链接详见 test-report；它们没有被写成已完成或通过。
