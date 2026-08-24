# CodeGraph Explore Procedure

这是 explore 的阶段专用分析流程，不是通用工具清单。

1. 先调用 `codegraph_status`，记录索引可用性和时间。
2. 用 `codegraph_context` 建立任务相关区域、入口和符号地图。
3. 用 `codegraph_explore` 获取关键符号源码；CodeGraph 返回的源码视为已读取。
4. 已知起止点时直接用 `codegraph_trace` 获取完整调用链；不要用多轮 search 手工拼链。
5. 必要时用 `codegraph_callers` / `codegraph_callees` 核对 ownership。
6. 将结构化事实写入 `.spec/changes/<change-id>/research/codegraph.md`，包括影响、测试线索、事实/推断、未知项和降级原因。

MCP 不可用时使用等价 CLI；两者都不可用时定向读取源码和测试，并明确记录 fallback。不得保存大段原始输出。
