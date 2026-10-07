# 用户领奖与成本 TDD 单元测试

## UT-01 管理员混合历史

- Test：service/admin history 及 handler tests。
- Modify：独立历史读模型、AdminService/handler。
- 映射：ST-01 / SC-01。
- Red：新模型/总额缺失导致测试失败；记录准确编译或行为失败。
- Green：统一来源读取、稳定排序分页和独立合计。
- Refactor：共用 DTO/来源转换，不污染 RedeemCode 业务对象。

## UT-02 奖励成本事务

- Test：daily activity reward cost 和 extra cost tests。
- Modify：活动发奖、成本事务 helper、成本模型/仓储。
- 映射：ST-02/04。
- Red：发奖缺少成本、保护或缓存通知。
- Green：同事务 SQL、来源唯一、自动记录保护、commit 通知。
- Refactor：集中成本写入和审计映射。

## UT-03 UI 展示

- Test：UserBalanceHistoryModal、ExtraCostsDialog、用户快捷入口。
- Modify：API types/两个弹窗/UsersView/i18n。
- 映射：ST-01/04。
- Red：领奖合计/明细/筛选及自动成本保护尚不存在。
- Green：来源组合 key、领奖显示和同弹窗快捷入口。
- Refactor：复用活动类型翻译，保持旧请求竞态守护。

## UT-04 迁移与发布合同

- Test：migration unit/VM SQL fixtures、release profiles tests。
- Modify：新迁移、补录入口、profile/catalog。
- 映射：ST-03 / SC-03/05。
- Red：新 schema、补录或 profile 合同缺失。
- Green：保留历史配置、唯一审计关系、原日期补账。
- Refactor：源 SQL 和补录单一合同，未知后续 profile 继续拒绝。

## 覆盖边界

单元不连接生产；VM 数据合成。浏览器截图不能替代行为测试，未执行项如实记为未验证。
