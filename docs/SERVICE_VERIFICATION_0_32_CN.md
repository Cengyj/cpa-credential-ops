# Credential Ops 0.32.0 验收与复现

基线为 `d557de6ef52d977a114602cafa4711c60729e565` 的插件 0.31.0、UI 13.8.0、Go/Sobek 2.2.1。本次版本为插件 0.32.0、UI 14.0.0、Go/Sobek 2.2.1。基线插件 7 个有测试的包和 46 个 Node 用例通过。宿主为官方 CPA 8.0.76，2,413 个基线文件逐一校验，网络隔离的宿主编译与全测命中了相同源码的 Docker 缓存层。[起始工作区](../artifacts/verification-0.32.0/baseline-status.txt) · [插件基线](../artifacts/verification-0.32.0/go-baseline.log) · [宿主检查](../artifacts/verification-0.32.0/host-check.log)

源码入口、依赖、权威存储、任务生命周期、回滚步骤和受限能力见[服务与命令契约](SERVICE_LIFECYCLE_0_32_CN.md)。0.31.0 已有私有授权、退避、后台巡检、worker、重新授权协调与补检；本轮在这些现有实现上修复了以下可复现问题。

## 复现与回归证据

| 场景 | 结果及证据 |
| --- | --- |
| 只有连接状态持续 503 | 旧首屏没有请求可用的账号接口。当前账号 GET 一次，列表与导入可用，后台配置写入 0 次。错误密钥首次只发 1 个被拒请求。34 个场景覆盖首次设置、三次 iframe 重建、切页、多标签轮换、迟到 401 和退出。页面隐藏 65 秒新增展示请求 0 次。[请求记录](../artifacts/verification-0.32.0/ui/account-navigation.json) |
| 健康探测格式错误、并发限流与关闭 | 无效响应有独立代码，429/403、网络、503 和当前管理密钥 401 保留各自语义，退避状态含下次尝试时间。30 次普通状态 GET 不触发新探针。关闭中断正在读的管理 HTTP 响应；宿主配置热更新沿用现有后台实例。旧请求不会覆盖新密钥。[状态红测](../artifacts/verification-0.32.0/connection-red.log) · [关闭红测](../artifacts/verification-0.32.0/transport-red.log) · [后台连接回归](../plugin/quality/management_connection_test.go) · [原生实例回归](../plugin/cmd/model-quality/lifecycle_test.go) |
| 资源读取失败与准入 | 统计、历史、目标目录故障分别有可见提示，保留已读结果和独立操作。精确目标可发起当前读取，CPA 手工启用后允许即时提交；身份不明、保留暂停和待核对写入继续阻止。原生夹具还发现：引擎已核实启用，目标首页仍返回旧停用来源。归属变更现在失效普通首页，旧分页仍一致。停用期假模型调用为 0，人工启用后的新错误可再次触发原策略。[资源隔离](../artifacts/verification-0.32.0/ui/partial-reads.json) · [原失败](../artifacts/verification-0.32.0/readmission.log) · [官方宿主复验](../artifacts/verification-0.32.0/delivery-readmission.json) |
| 页面失去命令响应、重复点击、删除及重启 | 四种写命令丢回执并关闭最后一个标签，新标签完成鉴权后找回原 ID，各 1 个 POST、1 次受理、0 次自动重放。双标签分别提交两项操作，关闭两个标签再打开后找回两个 ID，新增提交 0 次。另两个未受理夹具先 GET，再由用户以相同 ID、相同参数重投。重复创建只产生一条规则，12 路并发重投只派发一个批次；删除和同卷重启后还能查原规则、派发与批次。较早版本重复提交能创建两条规则，手动派发完成后还可能重复补检。[浏览器七场景](../artifacts/verification-0.32.0/ui/command-recovery.json) · [旧版六项失败](../artifacts/verification-0.32.0/quality-commands-and-reads-before.log) · [定向竞态](../artifacts/verification-0.32.0/quality-commands-and-reads.log) |
| 外部写入和本地收据之间中断 | 对账号普通命令，操作已执行但幂等终态落盘失败时，保留待核对标识并返回 `idempotency_unknown`。巡检与转换分队列受理。假 SDK 保存后、返回前对子进程发送 SIGKILL，重启后新增保存为 0。实际库达到容量上限时，受理与收尾的预留空间分开；补回能核实的确认结果不会重复上传。质量停用写入未知经历两次重启共 1 次 PATCH。[旧版错误分类](../artifacts/verification-0.32.0/account-receipt-red.log) · [账号崩溃与容量](../artifacts/verification-0.32.0/account-batch-fixtures.log) · [跨库收据缺失及损坏修复](../artifacts/verification-0.32.0/account-cross-store.log) · [质量不确定写入](../artifacts/verification-0.32.0/delivery-unknown.json) |
| 目录分页、慢 CPA 与规则竞争 | 13 个并发目录读者合并读取，快照到期以 409 提示重开首页。质量锁外完成可能缓慢的管理读，返回后复查相应规则、配置、归属、重新授权变化。慢提供商、暂停、取消、归属变化与重复提交有专门回归。[目录与并发回归](../plugin/quality/directory_pages_test.go) · [规则命令回归](../plugin/quality/rule_commands_test.go) |
| 账号、质量策略及历史保护 | 账号导入与编辑、代理、上传许可、2FA、巡检及自动重登、安全操作、转换均使用合成材料。多种质量题型、答错与上游错误独立、手动启用、跨规则归属、提供商组授权与恢复、未知及清理都有服务测试。另在官方 CPA 上跑了假 OAuth 与提供商的实际路由和写接口。清历史后保留六个已确认错误停用来源。[账号操作](../artifacts/verification-0.32.0/ui/dialogs/account-interactions.json) · [质量策略](../artifacts/verification-0.32.0/delivery-policy.json) · [上游错误](../artifacts/verification-0.32.0/delivery-upstream.json) · [提供商](../artifacts/verification-0.32.0/delivery-provider.json) · [历史](../artifacts/verification-0.32.0/delivery-history.json) |

浏览器夹具使用 Chromium、本地随机端口和内存 API。软件包集成用独立临时数据卷、`--network none` 的官方 CPA 和回环假上游。实例数量由原生生命周期测试核实，浏览器测试计数的是 HTTP 请求。原生生命周期补充测试循环三次打开同卷、访问账号/连接/设置/规则、热更新并关闭。每次循环 16 次读取和 4 次普通热更新只创建 1 个实例。关闭后推进 3 分钟，Go synctest 检查该循环创建的后台 goroutine 均已退出；同一用例在竞态模式下重复三轮。无浏览器的巡检测试通过重开账号库、推进时钟验证逾期只巡检一次，全程浏览器请求、管理 HTTP 请求及新建登录任务均为 0。[排空检查](../artifacts/verification-0.32.0/service-drain.log) · [巡检重启](../plugin/login/inspection_restart_test.go) · [后台重新授权与规则](CREDENTIAL_REAUTH_RULES_CN.md)

## 构建与测试计数

最终 Linux arm64 包从 2,781 个已核对的构建输入生成。解开安装 ZIP 只有 `model-quality.so`，内嵌完整登录运行时和 10 个 UI 资源。逐文件比较重新准备的构建上下文、工作区、构建清单及 ZIP 内的动态库。ZIP SHA-256 为 `44848c4eca14e90268bec4d7bf1fb0b0d2efed581e670f29f577c4e6a8abc1a1`。[安装包](../artifacts/verification-0.32.0/delivery/model-quality-v0.32.0-linux-arm64.zip) · [源码与摘要](../artifacts/verification-0.32.0/source-integrity.json) · [构建日志](../artifacts/verification-0.32.0/delivery-build.log)

插件通过 463 个顶层 Go 竞态测试，含子用例共 1,271 个，测试跳过 0 个。UI 包的 Go 默认构建没有测试，替代宿主 OAuth 标签另有验证。Go/Sobek 独立模块的竞态与 vet、Linux 内嵌标签的 go test/vet 均通过。前端 Node 54 项及题库展示检查通过。Python 安装器和私有源码过滤 35 项通过，包括意外改写既有授权文件会使部署核验失败的回归。[Go 计数与逐包结果](../artifacts/verification-0.32.0/race-summary.json) · [运行时](../artifacts/verification-0.32.0/runtime-race.log) · [替代宿主 UI](../artifacts/verification-0.32.0/host-oauth.log) · [Node](../artifacts/verification-0.32.0/delivery-node.log) · [安装器](../artifacts/verification-0.32.0/installation-and-source.log)

## 同机性能测量

Go 测量在 darwin/arm64、Apple M6 上进行，表中采用三次运行的中位数。分配量采用 B/op，另列独立测试进程的峰值常驻内存。浏览器每版每工作区使用五个新 context，计时到第一个可用控件，请求数统计到网络空闲。

| 负载 | 0.31.0 | 0.32.0 |
| --- | ---: | ---: |
| 账号、智商测试、运维首屏的管理 GET | 7 / 9 / 9 | 7 / 4 / 8 |
| 对应首屏可用中位数 | 86 / 74 / 63 ms | 75 / 58 / 66 ms |
| 2,000 对象，连续读取 20 页，每组 10 次 | 20 次全目录扫描，5.905 ms，22.59 MB | 1 次扫描，2.176 ms，3.09 MB |
| 25 ms 慢目录期间无关质量查询等待 | 40.015 ms | 0.3 µs |
| 5,000 行审计的热读首 20 条 | 1.815 ms，3.35 MB | 11.097 µs，约 8.6 kB |
| 5,000 个运行、各 4 个样本，统计摘要热读 | 821.639 µs，1,796 B | 1.609 µs，2,000 B |
| 64 个账号、2,048 个历史任务，列表 40 次 | 1.178 ms，1.686 MB | 0.163 ms，0.231 MB |
| 两个绑定账号的一次列表访问 | SDK 列表 4 次、按文件读 4 次 | SDK 列表 1 次、按文件读 2 次 |
| 每次 SDK 读取延迟 15 ms 时，账号设置锁等待 | 31.806 ms | 0.531 µs |
| 12 个假账号一批，巡检吞吐和每批分配 | 63.77 账号/s，约 901 kB | 60.97 账号/s，约 566 kB |
| 2 个实际 worker、4 个假目标、48 个任务、每次假调用 2 ms | 10.9 任务/s | 10.9 任务/s |
| 同一 48 任务夹具，三个独立进程的峰值 RSS 中位数 | 20.74 MB | 21.64 MB |

巡检新增持久受理与进度记录，合成吞吐约下降 4.4%。历史及统计的加速限于读事务修订相同的热读，提交新写事务后首次读取会重建索引或摘要。峰值 RSS 含整个 Go 测试进程；这些短时测量无法确定长期运行内存上界或生产容量。[浏览器逐次样本](../artifacts/verification-0.32.0/ui/workspace-benchmark.json) · [目录基线](../artifacts/verification-0.32.0/quality-load-before.log) · [目录结果](../artifacts/verification-0.32.0/quality-load-after.log) · [历史基线](../artifacts/verification-0.32.0/display-before.log) · [历史结果](../artifacts/verification-0.32.0/display-after.log) · [账号基线](../artifacts/verification-0.32.0/account-bench-before.log) · [账号结果](../artifacts/verification-0.32.0/account-bench-after.log) · [队列基线](../artifacts/verification-0.32.0/worker-before.log) · [队列结果](../artifacts/verification-0.32.0/worker-after.log) · [RSS 基线](../artifacts/verification-0.32.0/worker-rss-before.log) · [RSS 结果](../artifacts/verification-0.32.0/worker-rss-after.log)

## 复现命令

从工作区根目录运行。下列 Docker 命令使用新目录，可与既有验收产物共存。受理、崩溃、容量和竞争测试在临时库执行。

```sh
(cd plugin && AUTHLOGIN_TOTP_NODE="$(command -v node)" go test -count=1 -race ./... && go vet ./...)
(cd login-runtime-go && go test -race ./... && go vet ./...)
node --test scripts/tests/*.test.cjs
node scripts/test_question_test.mjs
python3 -m unittest discover -s scripts/tests -p 'test_*.py'
node scripts/verify_account_startup.cjs
node scripts/verify_command_recovery.cjs
node scripts/verify_operations_live.cjs
node scripts/benchmark_workspace_reads.cjs
(cd plugin && go test -run '^$' -bench 'Benchmark(DisplayDirectoryPages|QueryDuringSlowManualDirectory)' -benchmem -benchtime=10x -count=3 ./quality)
(cd plugin && go test -run '^$' -bench BenchmarkDisplayHistoryAndMetrics -benchmem -count=3 ./quality)
(cd plugin && go test -run '^TestOffline(WorkerThroughput|ReadContention)$' -v -count=3 ./quality)
OUT="$(mktemp -d)"
python3 scripts/prepare_official_context.py "$OUT/context"
docker build --platform linux/arm64 --target plugin-artifacts --output "type=local,dest=$OUT/package" "$OUT/context"
python3 scripts/smoke_ui_package.py "$OUT/package" "$OUT/lifecycle.json"
python3 scripts/smoke_ui_package.py "$OUT/package" "$OUT/scope.json" --quality-scope
python3 scripts/smoke_ui_package.py "$OUT/package" "$OUT/provider.json" --provider-targets
python3 scripts/smoke_credential_policy.py "$OUT/package" "$OUT/readmission.json" --manual-readmission
python3 scripts/smoke_credential_policy.py "$OUT/package" "$OUT/cron.json" --scheduled-addition
```

## 官方 CPA 与本机交付

同一 ZIP 通过 13 个隔离官方 CPA 场景。基础加载及异步转换、范围与多模型派发、提供商、跨规则处置、上游错误、重启后错误停用排除、目录缺项、手动重新准入、凭证替换、运行时错误、未知写入、历史清理和无页面调度均使用独立临时卷。错过两分钟后重新创建容器，首次页面查询前只补 1 次、写入 0 次；下一正常分钟按当时目录发出 2 次新探测。[原生加载与转换](../artifacts/verification-0.32.0/delivery-lifecycle.json) · [多模型范围](../artifacts/verification-0.32.0/delivery-scope.json) · [无页面调度](../artifacts/verification-0.32.0/delivery-cron.json) · [凭证替换](../artifacts/verification-0.32.0/delivery-replacements.json) · [运行时错误](../artifacts/verification-0.32.0/delivery-runtime.json)

两个历史脚本重启后看到规则可读就提交了需要管理通道的命令。实测两个场景的同一后台修订都会自行从 `waiting` 转为 `ready`。夹具在提交前以只读查询核实这项前提，保留原调用与处置次数断言。错误停用场景共 45 次假上游调用，持续受错误停用保护的对象后续调用为 0。目录已核实缺项后依规则分别只产生一次相应的停用或删除。无页面定时夹具独立运行，在补检完成前没有查询插件状态。[首次失败](../artifacts/verification-0.32.0/delivery-suspensions.red.log) · [停用复验](../artifacts/verification-0.32.0/delivery-suspensions.json) · [目录缺项](../artifacts/verification-0.32.0/delivery-scope-skips.json)

2026-11-21 19:20（北京时间）完成本机 8317 停服备份并启动原容器。原有 196 份插件数据文件均在，新增 0 个；非数据库文件摘要一致，账号、提交收据及质量库的 bbolt 逻辑摘要逐项一致。宿主镜像、挂载、配置与凭证通过比对。实服 10 个静态资源与清单逐一匹配，匿名业务访问返回 401。独立 Chromium 配置首次读取 10 个 200，重开 10 个 304，gzip 内容解压一致。本机验证的鉴权请求、模型及账号操作均为 0。私有停机备份路径在部署报告中；自动回滚只替换旧动态库，数据恢复须遵循整卷核对步骤。[部署报告](../artifacts/verification-0.32.0/deployment.json) · [实服缓存和压缩](../artifacts/verification-0.32.0/served-cache.json) · [备份与回滚](SERVICE_LIFECYCLE_0_32_CN.md)

## 验证边界与限制

官方 CPA 没有跨进程的条件启停或删除事务。插件锁、当轮身份复查及写后收据可以缩小竞争窗口，外部管理者仍可同时修改 CPA。SDK 无法证明运行时 token 的值，也不能强制撤回已送出的全部调用。提供商轮换导致公共 ID 改变、缺失旧收据或无法核实中途启停历史时，保留历史和待核对状态。业务验证全部使用合成凭证和假上游。本次存储测试涵盖损坏记录、权限、容量水位和进程被强杀，物理介质掉电、整机长时间耐久性及各下游服务的真实可用性未经实测。
