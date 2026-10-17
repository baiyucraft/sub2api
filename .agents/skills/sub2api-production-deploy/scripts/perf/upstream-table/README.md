# 上游账号表对照与精确统计验证

这些工具只用于本任务的本地/开发VM验证，不是部署入口或签名Gate。`prepare.py` 在 `.tmp` 冻结源码并交叉编译；`run.py` 与 `validate.py` 使用 `.ssh.local` 的 `local_vm` 创建独占合成数据库/角色、独占Redis及临时目录，结束后核验删除。不要并行运行同一输出文件名，不要传入生产目标或生产数据。凭据只作为连接输入，报告不包含DSN、密码或用户请求正文。

本次最终交付以代码正确、判断等价、刷新稳定、最终构建和浏览器功能为要求；50ms/32ms/25%等原性能目标未完整达成，作为参考保留，不再阻挡交付或要求继续长矩阵。以下负载与长测工具保留为可复现能力，未运行的范围明确写未测；compare中的原阈值是性能子报告口径，不能冒充当前交付门禁，也不改写旧身份结果为最终构建成绩。浏览器功能入口见 [browser/README.md](browser/README.md)。

## 前置条件

- 从仓库根运行；Python需安装部署脚本使用的SSH依赖。现有Linux开发VM须有可用 `sub2api-postgres`、Docker网络、`redis:8-alpine` 镜像、`/usr/bin/time` 及Python标准库的 `fcntl`，支持读取本用户进程的 `/proc` 身份，容器数据边界由脚本确认。
- Go使用 `GOTOOLCHAIN=go1.27.2`，编译目标Linux amd64、CGO关闭。主机无需启动应用后端；真实handlers、repositories、gateway和保护runner在合成测试进程内运行。
- 基线固定 `8afa2ffe47800773f781279e945bf95d6c2e1067`。optimized从当前backend tracked及非忽略untracked源文件复制；修改源码/fixture后必须重建optimized，保存源码与二进制hash，不混用旧二进制与新源码结论。

## 冻结与编译

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/prepare.py --version baseline
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/prepare.py --version optimized
```

源码、`*-sources.json` 和 `*-matrix.test` 写入 `.tmp/upstream-table-perf`。每次prepare先核验绝对目标位于workspace和本任务生成目录，再重建该版本snapshot，避免删除/重命名的旧文件残留。baseline由固定commit重新archive，实验树额外注入 `baseline_quality_cache.go.txt`，将旧账号质量完成缓存TTL设为1纳秒（0会恢复旧30秒默认值），使相同刷新频率都重新计算；此适配只进入测试树，不改变产品基线文件。两边注入同份测试harness与保护观测helper。run逐版核对全部 `.go.txt` fixture（含helpers）及binary SHA，拒绝与源码清单不匹配的旧编译，provenance还保存Python工具hash、管理员数、重复次数、质量筛选及页号。性能报告必须同时披露该适配、源码/fixture/binary/tool SHA。

## HTTP矩阵

先验证工具可运行；smoke只证明链路与清理，不是性能验收：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/run.py --smoke --seconds 10 --repetitions 1 --admins 1 --result-file smoke.json --results-directory smoke
```

正式矩阵默认100/1000/10000账号×20/100行、10万历史会话、20管理员、每5秒刷新、85秒测量、3次重复。示例按管理员规模顺序运行（每次使用独立输出名）：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/run.py --admins 1 --repetitions 3 --result-file admins1.json --results-directory admins1
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/run.py --admins 5 --repetitions 3 --result-file admins5.json --results-directory admins5
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/run.py --admins 20 --repetitions 3 --result-file admins20.json --results-directory admins20
```

`--cells` 使用逗号分隔 `账号数:历史会话数:页长`，如 `--cells 1000:100000:100,10000:1000000:100`；账号数限1..10000、历史会话数0..1000000、页长1..100，拒绝重复cell。`--quality-filter` 使用产品原筛选串，`--page-number 2` 覆盖后续页。`--scenarios normal,slow,full` 分别正常、保护持久化慢依赖与实际容量满载。`slow` 当前40ms写触发器仅代表特定持久化退化，不等同于所有列表SQL或网络依赖退化。`--readiness 120` 是测量前保护就绪核验窗口，不能把未就绪测试说成保护有效情况下的对照。

`--seconds` 允许1..7200，`--readiness` 允许1..600；Go测试超时按测量秒数+readiness秒数+600秒初始化+120秒收口计算，SSH再加60秒，实际预算写入provenance。`--seconds 1700 --admins 1 --repetitions 3` 可用于获取单管理员的同cell P99样本，仍须以实得数量为准。管理员及重复次数各限1..100，页号须为正数；参数非法时在连接VM前拒绝。各repetition交替使用传入版本顺序及其反序，默认AB/BA/AB，provenance保存尝试/完成顺序，结果保存execution_index；已测旧固定AB顺序的独立报告保留其限制，不能声称已使用新交替规则。

harness每账号种200条健康历史、100条使用事实及10条错误事实，外加指定数量历史会话和1200条符合清理规则的旧事实；使用合成上游SSE。正常网关200必须读完响应，并核验合成SSE的stop、唯一usage（input100/output1/total101/cache90）及末尾 `[DONE]`，流内error、ReadAll/Copy失败或中断均不计成功；质量/列表传输错误同样失败，不在证据中保存响应正文。并发网关请求、完成消费、刷新与清理真实运行。管理员同一时间最多一个请求，按固定5秒发起槽位，超时/过载错过的槽位跳过，不突发追赶；必须对照实际成功核验次数及错误，不以较少刷新次数取得优势。baseline一轮为列表后独立质量请求，optimized为内联质量列表；`list_http`只比较列表阶段，`admin`比较整轮完成。

`remote_job.py` 将每cell的测试与CPU采样放入独占Linux进程组；先在本任务flock下原子登记PID/PGID/SID、启动时钟及归属nonce，再启动子进程。结束或SSH中断后，run使用独立SSH连接先写停止屏障，核验进程归属后按组发送TERM、必要时KILL并等待组空，禁止宽泛pkill。只有停止证据完整才删除本任务数据库、Redis与临时目录；无法确认归属、组仍存在或证据下载失败时保留资源并写失败清理JSON。已确认无后代且原supervisor已退出的历史身份退休，避免长矩阵PID复用导致误杀。远端连接输入读后删除，不下载含DSN的job输入及运行归属nonce文件；provenance必须包含remote_job工具hash。`*-process-cleanup.json` 与最终 `*-cleanup.json` 分别提供进程及资源清理证据，不能只凭SSH断开判断进程已退出。

每个cell目录保存运行日志、CPU/alloc/mutex/block pprof及JSON结果；总结果、provenance和资源清理证明写入 `.tmp/upstream-table-perf`。SQL driver calls/返回行与PG tuple visits总计含测量期整个测试应用负载；新增admin/gateway请求上下文SQL计数单独归因，admin按已记录刷新样本数归一化每轮量，错误次数另列并使验收不通过；异步保护runner和完成消费仍留在全局计数，不能把总量当作单页专用SQL。whole-process CPU及RSS包含初始化/就绪/测量，不能混称测量期CPU；另有 `measurement_process_cpu_seconds` 只覆盖fixture测量区间的Go进程user+system CPU。PG按当前合成DB记录temp_files/temp_bytes/deadlocks/blks_read增量，并每秒采样处于Lock等待的连接数/最大值及读取错误；连接数采样不是精确SQL锁等待时长，temp计数也不能独自归因为某条SQL排序溢出。PG累计统计异步更新，tuple/IO统计end后额外等1.1秒时runner仍在运行，属于近似观测，不能冒称精确扫描行。`pg_cpu_sample.py` 通过fixture的measurement.start/end marker和100ms轮询只读采集PG容器cgroup CPU，结果 `postgres_cpu.json` 同时记录CPU秒与实际观测wall_seconds；该指标包含整个PG容器的其他DB/原有开发栈背景活动，**不是独占合成DB CPU**，不支持cgroup或测量未开始/中断均明确status，不能当0。需做纯数据库CPU归因或精确锁/排序分析时另用隔离数据库/实例与受控SQL计划观测。记录保护就绪、刷新滞后、pending及清理积压，不能只看响应耗时。

## 配对汇总

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/compare.py .tmp/upstream-table-perf/admins1.json .tmp/upstream-table-perf/admins5.json .tmp/upstream-table-perf/admins20.json --output .tmp/upstream-table-perf/comparison.json
```

compare读取每次cell的 `latency_samples.json` 原始毫秒样本，只按相同账号数、历史会话量、页长、管理员数、场景、筛选和页号分组池化；不会平均已有P95/P99，也不拼接异质cell。每次run须有admin/list/gateway三路原始样本，与各自摘要计数逐项相符且全是有限非负数；缺文件、缺route、截断、非数字或NaN/Infinity均明确记录 `raw_sample_issues`，该run不池化并使cell不通过，即使其余两次已超过1000样本也不能充作三次合格重复。要求baseline/optimized各至少3次、各自单一binary及源码清单身份，拒绝重复输入；读取输入旁同名 `*-provenance.json`，核验binary、管理员数、筛选及页号，并要求fixture、测量秒数、readiness秒数以及prepare/run/PG CPU采样/remote_job工具hash一致。测量与readiness秒数必须为正数，四个工具hash及对应版本源码清单hash必须非空，不能用两边同时缺字段取得资格。测量开始时必须确认保护启用且全部账号快照新鲜；缺provenance、混合条件、未就绪、缺结果、错误、样本不足或阈值未达均明确标记。至少1000同cell有效样本才输出P99。

汇总明确属于**延迟子报告**，`full_acceptance_complete` 固定为 `false`；`all_supplied_cells_passed` 和每cell的 `status/issues` 只表示所提供cell的延迟资格及目标。保护刷新、pending、清理积压等原始观察保留在 `protection_and_cleanup_observations`，仍须单独审阅；资源清理证明、浏览器验收和完整矩阵另行核验。工具exit0只表示报告生成，不能据此宣称未跑矩阵或完整计划通过。

`full` 场景逐请求解析JSON，仅接受HTTP `429`、`error.type=rate_limit_error`、`error.code=gateway_queue_full` 及精确message `Too many pending requests, please retry later`，并确认没有实际请求上游。记录 `full_expected_rejections`、`full_unexpected_responses`、`full_invalid_json`、`full_response_signatures` 与有界异常摘要；其他响应均使fixture失败。compare要求gateway样本数大于零、全部属于已核验容量拒绝、异常及无效JSON计数为零、上游请求数为零；缺失或不符标记 `full_capacity_rejection_semantics_require_verification`，不能把任意非200或快速内部错误计作正确容量拒绝。修改fixture后必须重新冻结并编译两版，旧fixture结果不能与新结果混算。

离线工具资格测试不连接数据库或VM：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/test_tools.py
```

测试覆盖999/1000实际样本边界、至少3次、重复输入、缺raw/route及计数错配、非有限/负数/非法样本、保护未就绪、缺provenance、双方同时缺必填资格字段、混合测量条件、full场景拒绝语义、长测量超时预算及版本交替顺序。该命令验证工具逻辑，不能替代真实压测。只有1000账号/10万历史/100行/20管理员的normal单cell完成时，结论也仅覆盖其冻结的fixture、binary与工具身份；不能代表100/10000账号、其他管理员数、筛选、分页、slow/full、浏览器或完整验收。

fixture的传输校验纯测试可在Linux验证机上，对已核验身份的编译产物单独运行，不连接数据库：

```bash
./optimized-matrix.test -test.run '^TestMatrixResponseValidation$' -test.v
```

## 精确统计和迁移

```powershell
$env:GOTOOLCHAIN='go1.27.2'
$env:GOOS='linux'
$env:GOARCH='amd64'
$env:CGO_ENABLED='0'
go -C backend test -tags upstreamtablepg -c -o ../.tmp/upstream-table-pg-linux.test ./internal/repository
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/validate.py --explain
```

`validate.py` 默认执行健康固定随机事实/边界/异常JSON差分、质量single-pass对照旧SQL、300实际索引定义/幂等/旧SQL兼容、Redis全页批量读取。`--explain`依赖已冻结baseline源码，保存合成SQL计划；比较 `EXPLAIN ANALYZE` 前确认两边数据、统计与时间边界相同。默认结果 `.tmp/upstream-table-pg-validation`。race是否可执行由VM Go/gcc前提决定，CGO关闭的普通二进制不能作为race结果。

`race.py` 是独立的定向竞态检查入口。先用项目Go命令下载 `golang.org/toolchain@v0.0.1-go1.27.2.linux-amd64`，将其 `go mod download -json` 输出保存到 `.tmp/upstream-table-linux-toolchain.json`，再从仓库根运行：

```powershell
python .agents/skills/sub2api-production-deploy/scripts/perf/upstream-table/race.py
```

脚本核验固定toolchain版本与模块校验和，仅复制当前backend源文件及已解析依赖图对应的模块下载缓存，在开发VM独占临时目录展开并启用CGO与race。输出 `.tmp/upstream-table-race/provenance.json`、`race.log` 和 `result.json`，包括源码/依赖/toolchain归属及目录删除证据。它只执行service、repository、admin三个包中正则选中的质量/列表/健康等用例，**不是三个包全量race，也不是完整后端race**；需要VM预先具备gcc等CGO运行条件，不修改现有应用或数据库栈。普通Go测试中既有失败与本次定向race结果分开报告。

## 判断结果与范围

- 每项延迟保存样本数。P99只有至少1000个有效样本才具备验收资格；否则 `p99_eligible=false`，不输出可验收P99。默认20管理员×85秒单次约340个刷新样本，**单次不足以验收管理员P99**；三次相同cell理论约1020个，可池化原始样本，但错过槽位后仍须核对实得数。1/5管理员或实际不足1000时需延长相同负载测量，不能把多个异质场景拼成1000个。P95小样本同样需披露不确定性。
- 优化前后同机、同数据库/Redis配置、同数据量/种子与同负载各至少3次，记录网关成功量、刷新成功量和错误。重负载整轮P95目标下降25%；其余正常与网关P95/P99退化≤5%。不满足、未跑或依赖故障明确报告，不能用平均数掩盖尾延迟回退。
- smoke、合成负载及静态API fixture浏览器检查分别标注范围，不等同生产规模证明、真实页面+VM后端联调或签名Gate。本工具不自动给出全计划验收结论，也不提供生产部署。
- 最终浏览器功能使用生产构建与真实浏览器，附真实2分钟短稳定性检查，不能称为30分钟验证。可选性能测量的标准与4倍CPU降速分别记录，桌面/移动×20/100行×无变化/单行/全页变化及30分钟长测保留为可复现能力；若叠加网关，另测P95/P99及保护刷新滞后，未跑范围不冒报。
- 清理结果须全部为真；SSH/进程异常后先核验 `owned.json` 和对应cleanup证据，再处理本任务独占资源，不能扩大到现有开发/生产数据库、Redis或容器。
