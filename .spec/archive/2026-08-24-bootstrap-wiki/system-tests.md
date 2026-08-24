# bootstrap-wiki 系统测试

## 测试环境

- Windows PowerShell，Node 20.20.0，spec-wiki-lite 0.1.0，CodeGraph 1.5.0。
- Fixture：当前仓库 .wiki、.spec/changes/bootstrap-wiki 和 Git 工作区。
- 外部依赖：none，不启动后端、数据库或前端服务。

## ST-01 正常场景

- 前置：占位 Wiki 已存在。
- 操作：生成首页、栏目和模块页面，移除 marker，运行 status。
- 断言：bootstrapPending=false、无 blocking issue、每个目录有 INDEX.md。
- 证据：status JSON 与文件清单。

## ST-02 失败场景

- 前置：某模块事实无法从源码或配置确认。
- 操作：标记 unknown，不写入未核实能力、凭据或生产命令。
- 断言：Wiki fail-closed，业务代码无变更。
- 证据：research unknowns、diff 和敏感字段扫描。

## ST-03 边界场景

- 操作：解析 frontmatter、相对链接和目录 INDEX，运行 strict validate。
- 断言：所有页面有 frontmatter，链接存在且在 .wiki 内，CLI 通过。
- 证据：.tmp/bootstrap-wiki-doc-check.json 与 CLI 输出。

## ST-04 重复执行

- 操作：重复运行检查，不使用 --force；归档只调用一次。
- 断言：检查幂等，非占位内容保持不变，无重复页面。
- 证据：两次 diff/stat、checksum 和 archive 输出。

## 成功标准映射

| 成功标准 | ST | 证据 |
| --- | --- | --- |
| bootstrapPending=false | ST-01 | status JSON |
| 首页/栏目无 marker/占位符 | ST-01/ST-02 | 页面检查 |
| frontmatter/链接/INDEX 合同 | ST-03 | doc check JSON |
| strict/full/archive | ST-03/ST-04 | CLI、reports、archive path |

