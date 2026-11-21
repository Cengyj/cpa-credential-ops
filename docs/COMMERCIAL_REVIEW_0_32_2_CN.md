# 0.32.2 商用可靠性复核

工作区源码为插件 **0.32.2 / UI 14.0.2**，Go/Sobek 运行时版本为 2.2.1。开工时 HEAD 为 `d557de6`，已有 150 项工作区变动，对照上轮原生构建使用的 113 个不同输入，摘要全部一致。本机 8317 仍提供 **0.32.0 / UI 14.0.0**。本轮以浏览器夹具、断网 Linux arm64 原生 QA 库和未修改的官方 CPA 8.0.76 核实增量；本机仅做匿名 HTTP 读取。[开工基线](../artifacts/verification-commercial-rounds/baseline.json) · [实服资源核对](../artifacts/verification-commercial-rounds/installed-readonly.json)

## 第一轮　授权、查询和页面

| 可复现的问题 | 修复后可观察到的行为 |
| --- | --- |
| 同一授权修订收到业务 503，先前发出的健康探针稍后才返回 429 或 403。旧实现可能忽略更长的重试期限。 | 同一协调器合并限制原因及最晚截止时间，迟到的成功不能提前开放调用。外层 401 仍优先作废原授权；密钥轮换后按修订丢弃旧响应。普通状态 GET 仅返回快照。[修复前](../artifacts/verification-commercial-rounds/connection-red.log) · [竞态回归](../artifacts/verification-commercial-rounds/connection-race.log) |
| 业务请求收到 CPA 的 401 请求头时，调用方同时取消；原密钥有时继续处于 ready。CPA 在业务 403 的小 JSON 正文中给出的封禁时长也可能丢失。 | 即使调用方取消，已经收到的外层管理拒绝也生效。只有本地取消、没有宿主响应时保留授权。收到 403 响应头后即关闭相关准入，在锁外有界读取宿主给出的等待时间，调用方仍能读取原正文。内层模型 401 继续进入上游策略。[修复前](../artifacts/verification-commercial-rounds/outer-denial-red.log) · [三轮定向竞态](../artifacts/verification-commercial-rounds/connection-store-final-race.log) |
| 两个目录首页读取遇到同一条目重新授权，旧实现可向等待者签发已经失效的目录。 | 13 个并发首页读取共享一次底层扫描。扫描期间失效时共享一次重读；连续两次失效返回可重试的 503。已经取得快照标识的续页仍可读原目录。任务准入和外写前仍读取当前执行证据。[目录实现](../plugin/quality/directory_pages.go) · [修复前](../artifacts/verification-commercial-rounds/quality-directory-red.log) · [修复后](../artifacts/verification-commercial-rounds/quality-directory-race.log) |
| 账号历史隐藏时，在途任务列表、详情或日志索引响应仍会派生新的展示读取。 | 三个阻塞点原先分别追加 3、2、1 次 GET，修复后均为 0。页面重新显示后按原选中任务获取当前修订，保留筛选和展开状态。用户已明确发起的两页导出可完成剩余一页，四个场景业务写入均为 0。[修复前的详情读取](../artifacts/verification-commercial-rounds/ui-detail-red.log) · [浏览器逐请求记录](../artifacts/verification-commercial-rounds/ui-hidden/account-navigation.json) |

标签页依浏览器的页面可见性事件停读，嵌入页面另观察子文档与视口的几何交叉。Chromium 实测包括同源及跨源 iframe 的 `display:none` 和重新显示。仅在父页面设置透明度、`visibility:hidden` 或叠加遮挡物，子页面可能收不到几何或可见性变化。[跨源实测](../artifacts/verification-commercial-rounds/ui-cross-origin.json) · [可见性订阅](../plugin/ui/app.js) · [账号展示读取](../plugin/ui/login.js)

## 第二轮　持久化和诊断边界

旧版 0.32.1 会在已声明 schema 2 的库中重建丢失的固定桶。隔离官方 CPA 证实，删去幂等桶后仍可注册，规则及连接 GET 均返回 200，损坏库的文件摘要发生变化。现在启动先检查 schema 和七个固定桶。根已被清空但存在已提交事务、可判定和记录不符的规则命令桶或待处理索引，都会中止加载。新空库可建立，schema 1 仍走原迁移。已存在且长为零的 `quality.db` 在 bbolt 初始化之前被拒绝。在修复这一层之前，相同的文件会被初始化成 32,768 字节。[固定桶红测](../artifacts/verification-commercial-rounds/store-open-red.log) · [已用空根红测](../artifacts/verification-commercial-rounds/store-empty-red.log) · [零字节旧行为](../artifacts/verification-commercial-rounds/qa-quality-zero-old.json) · [最终缺桶实测](../artifacts/verification-commercial-rounds/final/qa-quality-store-new.json) · [最终截断实测](../artifacts/verification-commercial-rounds/final/qa-quality-zero-new.json)

规则创建和立即执行的回执桶、待处理索引按实际使用建立，标记与建桶同事务提交。旧库允许尚未使用过这两类命令，也可能只有创建收据。首次遇到有桶但没有标记的旧库，核对收据与索引后登记布局。已登记的桶再度丢失时拒绝启动，防止把曾受理的命令报告为 404。进程中断时尚未完成的派发仍按原收据转为 `interrupted`。标记记录的是桶的存在性，不能逐条证明每条历史内容完整。升级前若所有可核对的收据和索引均已丢失，新版无法推断过去发生过哪些操作。[命令收据](../plugin/quality/rule_commands.go) · [已完成命令误报未受理的红测](../artifacts/verification-commercial-rounds/quality-command-buckets-red.log) · [兼容和重启回归](../artifacts/verification-commercial-rounds/quality-command-buckets-race.log)

账号任务日志在一个只读事务中核对任务 ID、行内容和删除标记，再验证事件文字、保留窗口和游标。已删除返回 404，坏行返回 `503 storage_unavailable`，来源为 `plugin`，`outcome_unknown=false`。32 个并发读与删除竞争时，每次得到完整日志页或 404；单条坏日志不妨碍读取其他任务。历史 v0/v1 的有效事件、级别、分页与截断含义继续保留。[日志接口](../plugin/login/logs.go) · [错误复现](../artifacts/verification-commercial-rounds/account-logs-red.log) · [竞态与兼容](../artifacts/verification-commercial-rounds/account-logs-race.log)

事件本身完全正常时，另一份持久化字段也能进入导出。用合成任务把顶层 `ErrorReason` 改成非枚举文字，原详情、历史、原键查询及导出均会回显该文字。扩展到 14 类异常字段的红测记录了 71 次异常成功响应，其中 66 次含合成标记。现在对任务摘要的 14 个诊断字段、原命令的错误码及已保存响应单独校验，服务端返回有确定来源的存储错误。合法的历史阶段和值继续展示，账号行仍可访问，异常任务历史可单独诊断。Chromium 另验证原先已读的摘要遇到 503、事件仍读取成功时，导出保留可用数据并标记 `read_incomplete=true`；详情恢复后以及正常历史导出的值均为 false。[诊断投影](../plugin/login/job_diagnostics.go) · [旧接口回显复现](../artifacts/verification-commercial-rounds/ui-job-projection-red.log) · [服务回归](../artifacts/verification-commercial-rounds/ui-job-projection-race.log) · [浏览器导出红测](../artifacts/verification-commercial-rounds/ui-job-export-red.log) · [浏览器修复验证](../artifacts/verification-commercial-rounds/ui-job-export-green.log)

## 第三轮　官方宿主、数据卷和业务恢复

在没有外网的官方宿主中，使用相同的最终质量服务 QA 库分别进行缺桶、截断和冷恢复。损坏时宿主仍报告插件已配置和启用，但 `registered=false`、`effective_enabled=false`，两条插件路由返回 404。宿主日志给出具体注册错误。故障启动前后的损坏库大小及 SHA-256 一致。停服并还原同一个合成完好备份后，插件重新注册，两条路由恢复 200。每个夹具尝试 10 次 GET，实际收到七次 200、两次 404，另一次发生在宿主尚未接受连接时。无需任何管理写入。[缺桶实测](../artifacts/verification-commercial-rounds/final/qa-quality-store-new.json) · [截断实测](../artifacts/verification-commercial-rounds/final/qa-quality-zero-new.json)

在实际只读挂载及 64 KiB 满盘 tmpfs 上分别验证授权保存和清除。失败均返回 507，同时保留之前的授权状态和存储内容。普通状态 GET 在加载、重启后各进行三次，均未产生后台授权文件。两个阶段各核对十项静态资源，合计 40 个 200 及 20 个 304；公开资源下的业务路径返回 404，未经鉴权的管理路由返回 401。[存储故障](../artifacts/verification-commercial-rounds/final/connection-storage.json) · [宿主资源](../artifacts/verification-commercial-rounds/final/qa-assets.json) · [空卷命令查询](../artifacts/verification-commercial-rounds/final/qa-native-receipt-queries.json)

30 个质量处置阶段共发出 92 次隔离假模型调用，覆盖跨规则停用优先级、恢复归属和提供商整组许可。CPA 手工重新启用后可立即提交手动检测，新错误可再次触发原策略。在未知状态写入场景，两次重启后原 PATCH 累计仍为一次，源样本继续保留。[质量处置](../artifacts/verification-commercial-rounds/final/qa-quality.json) · [重新准入](../artifacts/verification-commercial-rounds/final/qa-manual.json) · [未知写入](../artifacts/verification-commercial-rounds/final/qa-unknown.json)

按真实时钟运行 338.239 秒的同卷容器重建夹具中，错过两分钟后，首次页面查询前后台自行补检一次，补检产生的凭证写入为零。下一正常 Cron 按当前目录进行了两次新探测。四种官方质量场景均退出 0，使用同一个最终动态库。[按时序的补检计数](../artifacts/verification-commercial-rounds/final/qa-cron.json) · [四种场景与二进制摘要](../artifacts/verification-commercial-rounds/final/qa-integration.json)

完整 Chromium 导航回归通过 38 个场景。单独令 `/connection` 为 503 时，账号列表 GET 一次，仍可导入，后台授权写入零次。浏览器夹具统计 HTTP 请求；实例、调度器和健康检查协调器复用由原生生命周期用例核对。[浏览器逐场景记录](../artifacts/verification-commercial-rounds/final/navigation-frozen/account-navigation.json) · [原生实例计数](../plugin/cmd/model-quality/account_routes_test.go) · [热更新与关闭](../plugin/cmd/model-quality/lifecycle_test.go)

## 测量、备份与交付

旧库中有 4,096 条合成收据时，一次性布局核验中位数为 611.248 µs、约 297 KB 分配；已登记布局为 1.610 µs、1,268 B。每组 20 次、三组取中位数，包含事务开始及回滚，不含 fsync。目录 13 个首页请求共享一次扫描；一次实质失效需要两次扫描，其中第二次用于获得新证据。不同并发场景分别保留底层扫描与请求计数。[收据逐组基准](../artifacts/verification-commercial-rounds/quality-command-buckets-benchmark.log) · [历史性能基线](SERVICE_VERIFICATION_0_32_CN.md)

同机 Apple M6、darwin/arm64、Go 1.27.1，使用 2,000 个合成目标读取 20 页，三组各十次取中位数。0.32.1 与 0.32.2 均扫描一次，耗时分别为 2.015 和 2.018 ms，分配分别为 3,092,673 和 3,075,977 B。目录读注入 25 ms 延迟时，无关规则查询的等待分别为 1.2 和 1.1 µs。0.32.2 的两个 worker 完成 48 个合成任务，每个任务恰好一次假调用，三轮吞吐中位数 24.3 任务/秒，独立测试进程的峰值 RSS 中位数 21,905,408 B。0.32.0 同负载曾测得 10.9 任务/秒。此次桌面的 CPU 和磁盘由多个进程共享，未归因吞吐差异，也未测量长期容量。[逐版本对照、命令与原始测量](../artifacts/verification-commercial-rounds/final/performance.json)

升级前停 CPA，保留二进制、宿主配置和凭证目录、整个插件卷、私有授权及安全收据，核对权限和冷备摘要。坏库拒绝注册时，先保全现场、核实卷挂载和 `data_dir`，再从有来源的同一份冷备恢复并核对宿主注册状态、规则和连接。手工创建空桶或删除零字节文件会抹去判断依据。外部换入合法的新空库也可能与初装相同，历史连续性须由卷和备份证明。若升级后已有外写，先核对 CPA 和跨库收据，再决定整卷回滚，避免用旧卷授权第二次写入。[持久化与恢复](PERSISTENCE_CN.md) · [跨库重新授权](CREDENTIAL_REAUTH_RULES_CN.md)

完整功能的接口、数据来源、并发和重启行为沿用既有调用链与验收矩阵。本轮使用两种 QA 库。质量库采用宿主 OAuth 构建标签；账号查询库用默认账号入口与不可执行测试载荷。在该卷的首个账号请求前 3 秒，后台已完成合成账号巡检，修订由 1 变为 2，下次到期为 15 分钟后。重启仍读取同一次巡检。加载和重启各核对十个静态资源、六次匿名 401 和九次鉴权业务读取，任务与日志数量均保持一条。官方宿主源码清单中的 2,413 个文件逐一核对相符。质量 QA 构建使用 74 个生产输入，默认账号 QA 构建使用 110 个，两者并集 114 个。本轮相对 0.32.1 的原生构建输入改动 14 个文件、新增一个任务摘要校验文件。两种库均非商用安装包。0.32.2 完整构建包含第三方登录的无人值守反自动化挑战处理链路，本轮未构建、分发或部署该链路。本机旧安装的十项公开资源均与原清单相符，匿名鉴权探测为 401。未进行真实账号操作和真实模型调用。官方 CPA 仍缺少跨进程条件启停或删除的原子操作，SDK 也不能保证撤回已送达上游的调用。[逐功能调用链](SERVICE_LIFECYCLE_0_32_CN.md) · [验收矩阵](SERVICE_ACCEPTANCE_MATRIX_CN.md) · [最终质量库清单](../artifacts/verification-commercial-rounds/final/qa-native/qa-build.json) · [构建输入及变化](../artifacts/verification-commercial-rounds/source-audit.json) · [默认账号原生宿主实测](../artifacts/verification-commercial-rounds/final/account-projection-native.json) · [实服匿名复核](../artifacts/verification-commercial-rounds/installed-readonly.json)

Go 默认入口的竞态检查通过 500 个顶层用例，含子用例共 1,391 个，失败和跳过均为零。计数基于全模块运行与变更后质量包、账号包和默认入口复测，按包去重。默认 UI Go 包没有独立用例；另以宿主 OAuth 构建标签测试原生入口及 UI，显式提供 Node、Playwright 和 Chromium 后，浏览器用例通过。两种构建配置的 `go vet` 均通过。前端 56 个单测、题库展示检查及 Python 35 个测试通过。[Go 逐包来源与计数](../artifacts/verification-commercial-rounds/final/race-summary.json) · [标签构建浏览器实测](../artifacts/verification-commercial-rounds/final/host-oauth-browser-race.jsonl) · [前端单测和题库](../artifacts/verification-commercial-rounds/final/node-final.log) · [Python 验证](../artifacts/verification-commercial-rounds/final-python.log)

## 复现命令

使用工作区根目录，Go 模块位于 `plugin/`。标签构建的浏览器用例需要本机 Playwright 和 Chromium，可用 `PLAYWRIGHT_MODULE` 和 `CHROME_PATH` 指定安装路径。原生夹具需要本机已有的官方 CPA 和 Go 工具镜像、离线模块缓存。下面的 `OUT` 指向新建临时目录，各命令使用隔离数据。完整包发布和安装属于独立流程。

```sh
(cd plugin && AUTHLOGIN_TOTP_NODE="$(command -v node)" go test -race -count=1 ./...)
(cd plugin && go vet ./... && go vet -tags credential_ops_host_oauth ./...)
(cd plugin && HOST_OAUTH_UI_TEST_NODE="$(command -v node)" go test -race -count=1 -tags credential_ops_host_oauth ./cmd/model-quality ./ui)
node --test scripts/tests/*.test.cjs
node scripts/test_question_test.mjs
python3 -m unittest discover -s scripts/tests -p 'test_*.py'
OUT="$(mktemp -d)"
UI_OUTPUT="$OUT/navigation" node scripts/verify_account_startup.cjs
LOGIN_UI_OUTPUT="$OUT/history" node scripts/verify_login_ui.cjs
node scripts/verify_cross_origin_visibility.cjs
python3 scripts/verify_host_oauth_qa.py build --output-dir "$OUT"
python3 scripts/verify_host_oauth_qa.py fixtures --output-dir "$OUT"
python3 scripts/smoke_host_oauth_resources.py --output-dir "$OUT"
python3 scripts/smoke_host_oauth_resources.py --output-dir "$OUT" --receipt-queries
python3 scripts/smoke_connection_storage.py "$OUT/qa-native/model-quality.so" "$OUT/connection-storage.json"
python3 scripts/smoke_quality_store_integrity.py --native-dir "$OUT/qa-native" --output "$OUT/missing-bucket.json"
python3 scripts/smoke_quality_store_integrity.py --zero-byte --native-dir "$OUT/qa-native" --output "$OUT/zero-byte.json"
python3 scripts/smoke_native_account_queries.py "$OUT/native-account-queries.json"
```
