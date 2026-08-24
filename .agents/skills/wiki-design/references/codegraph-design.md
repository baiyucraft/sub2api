# CodeGraph Design Procedure

这是 design 的阶段专用证据流程：

1. 读取同一 change 的 `research/codegraph.md`，避免重复全量扫描。
2. 对拟修改符号调用 `codegraph_impact`，确认影响半径。
3. 用 `codegraph_callers` / `codegraph_callees` 确认 ownership、接口边界和回滚影响。
4. 跨层流程使用 `codegraph_trace`；已有目标文件时用 `codegraph_affected` 找测试范围。
5. 把入口、调用路径、依赖边界、影响半径、测试、回滚、图证据与源码核验差异写入 design 模板专用章节。

MCP 不可用时使用 CLI 或定向源码核验，并记录降级和未确认风险；不得保存大段原始输出。
