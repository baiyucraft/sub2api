# bootstrap-wiki

## 问题

当前 Wiki 只有占位页面，无法说明 Sub2API 的运行定位、后端/前端边界、配置、迁移、测试和开发路径。

## 目标

- 建立正式首页和四个栏目索引，移除 bootstrap marker。
- 记录后端启动/路由、前端入口/API、配置、数据库迁移、测试与部署边界。
- 内容来自当前源码、配置、测试和仓库文档，并标注 SSOT owner。

## 非目标

- 不修改业务源码、依赖、生产配置或 CodeGraph。
- 不虚构未核实功能，不记录凭据，不建立额外索引/缓存。

## 成功标准

- `spec-wiki-lite status --json` 返回 `wiki.bootstrapPending=false` 且无 Wiki issue。
- 首页包含一级目录、SSOT、按任务导航、按模块导航和文档约定。
- 四个栏目 INDEX 无未处理占位符，链接到已核实页面。
- 页面 frontmatter、相对链接和目录 INDEX 检查通过，strict validate、full review 和 archive 通过。

## 影响范围

- `.wiki/**`：正式项目知识和导航。
- `.spec/changes/bootstrap-wiki/**`：研究、设计、测试、任务和审查证据。

## 交付形态

single-change

## 风险

- 仓库规模大，宽泛查询可能命中生成代码；使用定向 CodeGraph 和源码核验。
- 命令和外部依赖随环境变化；页面记录仓库声明并标注前置条件。

## 参考资料

- 来源：`README*.md`、`DEV_GUIDE.md`、`Makefile`、`backend/**`、`frontend/**`、`deploy/**`
  - 目标落点：`.wiki/**`
  - 采用方式：rewrite

