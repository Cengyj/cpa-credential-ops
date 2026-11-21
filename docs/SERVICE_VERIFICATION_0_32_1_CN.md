# 后台服务与命令回执复核

工作区源码版本为插件 **0.32.1 / UI 14.0.1**。本次从已交付的 **0.32.0 / UI 14.0.0** 继续复核，最初对照版本为 **0.31.0 / UI 13.8.0**。本机 8317 的安装版本仍为 0.32.0，运行时 2.2.1。此次官方 CPA 集成采用独立临时卷、Linux arm64 质量服务原生库及宿主 OAuth 构建标签。该库只用于隔离验收。完整协议登录运行时包含无人值守处理第三方访问挑战的路径，0.32.1 完整包未构建和部署。官方宿主的 2,413 个基线文件逐一核对通过。原工作区改动及原交付证据保留。[原验收与性能基线](SERVICE_VERIFICATION_0_32_CN.md) · [官方源码核对](../artifacts/verification-0.32.1/host-integrity.json)

## 源码、数据流及验收范围

官方 CPA 鉴权后，插件使用管理请求读取本地业务状态或受理命令。浏览器身份、后台授权、每项查询的新鲜度、未决命令与表单草稿分别处理。账号库和质量库保存受理结果，后台管理密钥仅写入私有管理记录。逐功能调用链表列出入口、权威数据、外部依赖、并发和重启行为。验收索引按原任务编号区分服务测试、浏览器和原生宿主实测，含第八节全部 12 项和第九节全部 19 项。[服务调用链](SERVICE_LIFECYCLE_0_32_CN.md) · [需求与证据索引](SERVICE_ACCEPTANCE_MATRIX_CN.md)

| 复核场景 | 源码行为与测试证据 |
| --- | --- |
| 首屏和导航 | Chromium 中单独让 `/connection` 返回 503，账号 GET 一次，账号导入可用，配置写入零次。34 个导航场景含错误密钥限流、刷新、iframe 重建、多标签轮换、迟到 401 和跨标签退出。后台实例计数来自原生生命周期测试，浏览器夹具只记录 HTTP 请求。[导航记录](../artifacts/verification-0.32.1/navigation/account-navigation.json) · [实例生命周期](../plugin/cmd/model-quality/lifecycle_test.go) |
| 后台授权存储 | 旧版管理 401 发生在私有目录不可写时，内存停用旧密钥，但目录恢复可写后，仍未保存失效标记。当前协调器按原修订仅重试写入无密钥标记；更换密钥优先，旧重试不能覆盖新修订。定向竞态运行三次，确认本地重试产生网络请求零次。[修复前](../artifacts/verification-0.32.1/connection-disk-repair-red.log) · [修复后](../artifacts/verification-0.32.1/connection-disk-repair.log) |
| 真只读挂载与空间耗尽 | 在官方 CPA 临时卷中分别使用只读 bind 和 64 KiB tmpfs。首次保存或清除已保存授权失败均返回 507，修订、内存状态及已装文件摘要一致。恢复可写后显式保存成功，清空 tmpfs 空间后可显式清除。0.32.0 完整包和本轮隔离原生库均通过；磁盘仍不可写时断电可能保留旧记录，重启会先做只读验证。[旧包实测](../artifacts/verification-0.32.1/connection-storage-baseline.json) · [当前实测](../artifacts/verification-0.32.1/connection-storage.json) |
| 原始命令回执 | 旧版没有按原键核对账号任务的路由，三个入口均复现 404。当前首次任务事务关联指纹、任务和账号，按原 ID 查询；历史缺少关联返回待核对。缺少当前执行证据不授权重发。同一 SDK 目录故障原来报插件存储错误，现为 503、`source=sdk`。普通账号读写、登录提交收据、未知外写保护沿用既有存储。[失败记录](../artifacts/verification-0.32.1/account-job-command-red.log) · [按键核对与 SDK 来源](../artifacts/verification-0.32.1/account-job-command.log) |
| 页面恢复及取消 | 关闭最后一页后，从原键核对手动测试、账号、规则和批次；查询与自动重放分别计数。收到精简取消 ACK 后，按原批次 ID 读取完整进度，显示已完成的上传和并列回执。未受理、已清理及普通路由 404 区分处理。非安全 HTTP 环境下同草稿可共享原键，不同草稿分别受理。[账号六场景](../artifacts/verification-0.32.1/ui/account-commands.json) · [规则、巡检和转换](../artifacts/verification-0.32.1/ui/command-recovery.json) · [手动测试](../artifacts/verification-0.32.1/ui/manual/receipts.json) |
| 活动链接和历史导出 | 同一任务的详情成功撤回授权 URL 或给出无效 URL 后，原页面仍可重绘旧链接。现在由当前详情决定能否导航，候选信息读取慢也不会延长旧链接的显示。一次 503 保留最后已读状态。同一等待任务的回调草稿保留，切页或浏览器鉴权失效时清除。历史页能按完整邮箱搜索，导出的诊断只含白名单字段。旧 UI 的完整邮箱导出断言保存在单独的历史脚本中。Chromium 实测业务 POST 和后台配置写入均为零。[撤回前复现](../artifacts/verification-0.32.1/ui/historical-job-navigation-red.log) · [旧导出断言](../artifacts/verification-0.32.1/ui/historical-job-export-red.log) · [当前页面实测](../artifacts/verification-0.32.1/ui/historical-jobs/report.json) |
| 原生账号只读路由 | 使用默认账号管理入口和真实静态资源，在临时卷写入一个合成账号、一条已取消历史任务及其原键收据。加载和同卷重启后，每阶段各有 6 次匿名 GET 返回 401，9 次正式业务 GET 核对账号、历史、详情、日志及原键，此外单独核对能力接口和 10 项静态资源。详情展示已核实的完整邮箱，命令收据脱敏，未受理的键返回 404。查询前后任务及日志各保持 1 条，普通 GET 未创建后台授权文件。隔离库嵌入的是无法执行的测试文本，能力接口确认协议登录及安全操作均不可用。报告记录独立库和源码摘要。[假卷种子](../scripts/fixtures/native_account_query_seed_test.go) · [官方 CPA 结果](../artifacts/verification-0.32.1/native-account-queries.json) |
| 混合范围补检 | 原先某个成员正在重新授权时，其余成员能执行，但这个成员在本周期的等待丢失。现在将到期时刻、已受理对象、待重新授权对象所依赖的条目、待取得的目录和新任务同事务提交。未核实的提供商目录不会阻塞已取得当轮执行与处置证据的 OAuth 分支。就绪后只补尚未受理的对象。手动派发、重启、错收据、身份冲突、撤销定时许可及暂停有专门回归。30 次等待 Tick 新增目录读取、模型调用和外部写入均为零。[调度回归](../plugin/quality/partial_schedule_test.go) |
| 质量归属与外部写入 | 答错处置、上游错误停用、原规则恢复许可、已核实 CPA 手工启用及跨规则优先级独立判断。隔离官方 CPA 上，另一条规则可检测答错停用对象但不能取得其归属。有新上游错误时原规则停止复检，已停用对象额外状态写入零次。最终库通过 30 个质量阶段，包含 92 次假模型调用。另用相同摘要的重建库验证未知写入，两次重启后累计仍只有一次 PATCH。[最终质量处置](../artifacts/verification-0.32.1/qa-final-quality.json) · [最终手动重新准入](../artifacts/verification-0.32.1/qa-final-manual.json) · [未知写入](../artifacts/verification-0.32.1/qa-repro/qa-unknown.json) |
| 无浏览器同卷重建 | 最终隔离原生库按真实时钟运行约 340 秒。错过两分钟后重建容器，首次页面查询前完成一次新的假模型检测，凭证写入零次。下个正常 Cron 根据当前目录发出两次探测。整个场景没有使用真实账号。[同卷实测](../artifacts/verification-0.32.1/qa-final-cron.json) · [最终库集成清单](../artifacts/verification-0.32.1/qa-final-integration.json) |
| 无浏览器账号巡检 | 在账号只读验收的独立卷预置一条已到期的巡检许可，关闭全局和账号自动重登。后台在首个账号请求之前 3 秒已将未绑定账号核实为 `not_linked`，账号修订从 1 增至 2，下一次巡检在 15 分钟后。容器重启后首次读取取得相同的巡检时间、下次到期和修订，登录任务新增零条。首个账号请求使用同容器 HTTP Date 提供时间上界。[原生巡检与重启记录](../artifacts/verification-0.32.1/native-account-queries.json) |

## 兼容、恢复和回滚

原有 bbolt 库、授权文件和安全操作记录沿用原位置。账号命令新增可选字段，老记录只有可核实的关联才展示原任务。质量旧待补记录只含到期时间和已受理对象，按原有目录等待语义读取。服务恢复时依当前规则和身份取得全新快照。启动及查询不会拿旧任务重发外部写入。跨账号库、质量库和 CPA 的收据独立，提交前后分别有故障窗口。[持久化](PERSISTENCE_CN.md) · [跨库核对](CREDENTIAL_REAUTH_RULES_CN.md)

备份应在 CPA 停止时涵盖宿主配置、凭证目录、插件库以及整个插件卷，保留私有权限。旧二进制无法解释新增分支等待字段。回滚前先核对已受理命令、待补周期和未确认写入；有升级后业务活动时，应保全当前卷和外部确认记录。0.32.0 本机交付保全原有 196 份数据文件，三个 bbolt 库逻辑摘要一致。本轮未停服、未操作该卷。物理断电和长期生产负载未实测；官方 CPA 仍无跨进程条件启停或删除事务，公开 SDK 不能保证撤回已送出的调用。[停机备份与实服报告](../artifacts/verification-0.32.0/deployment.json)

## 测量和复现

同机使用 2,000 个假对象、每组 20 页、每次跑 10 组，三次取中位数。下表第三列来自原交付，第四列为当前源码复测。慢目录夹具人工注入 25 ms 延迟。B/op 只统计 Go 分配；进程常驻内存和 worker 吞吐的独立基线见原报告。[原始测量及规模](SERVICE_VERIFICATION_0_32_CN.md) · [本次逐轮测量](../artifacts/verification-0.32.1/performance.log)

| 负载 | 0.31.0 | 0.32.0 | 0.32.1 |
| --- | ---: | ---: | ---: |
| 读 20 页触发全目录读取次数 | 20 | 1 | 1 |
| 读 20 页耗时 | 5.905 ms | 2.176 ms | 2.015 ms |
| 读 20 页分配 | 22.59 MB | 3.09 MB | 3.09 MB |
| 慢目录期间无关质量查询等待 | 40.015 ms | 0.3 µs | 1.2 µs |

以同一台机器和合成服务对照 Git 基线和本次前端，每版每工作区新开五个浏览器上下文。账号、手动测试、质量运维首屏的正式 GET 数依次为 7→7、9→4、9→8，可操作时间中位数为 72→67、73→59、63→64 毫秒。一次跨页中断的目录读取只访问了 offset 0。[逐次浏览器样本](../artifacts/verification-0.32.1/ui/workspace-benchmark.json) · [独立资源和迟到响应](../artifacts/verification-0.32.1/ui/partial/reads.json)

账号原命令回执使用同一 bbolt 读快照返回已落盘的任务状态。此前，持有登录执行锁时回执 GET 可超过一秒，持久状态仍为 `committing` 时还可能显示内存中的 `succeeded`。回归覆盖任务 ID 不匹配、资料损坏、数据库已关闭和内存领先于磁盘。[回执失败复现](../artifacts/verification-0.32.1/login-command-snapshot-red.log) · [按原键读取回执](../artifacts/verification-0.32.1/login-command-snapshot-final.log)

在慢 SDK 持有登录锁时，正式详情 GET 现在从持久记录读取终态、墓碑和不存在的任务。仍需补全邮箱时只读取关联账号或核实身份一致的 CPA 条目。耗时的 SDK 读取后复查删除及状态变化，关闭服务时等待读取退出。活动浏览器会话的 URL 继续检查当前所有者。这个改动保留登录执行原有的并发限制。[六个旧版阻塞复现](../artifacts/verification-0.32.1/job-detail-red.log) · [详情与关闭回归](../plugin/login/job_detail_test.go)

活动详情还有一个跨锁的存储失败窗口。GET 已经读到 `committing` 后，另一个 goroutine 更新内存并写入只读数据库，旧 GET 随后误报 200 和 `succeeded`。现在 GET 取得会话锁后复核服务故障，返回 503，库中仍为 `committing`。定向竞态回归连续三次通过。[误报复现](../artifacts/verification-0.32.1/job-detail-live-red.log) · [修复验证](../artifacts/verification-0.32.1/job-detail-live-final.log)

同机使用相同的慢 SDK 和两个详情查询场景，三组各五轮取中位数。不存在任务的查询等待从 38.734 ms 降到 12.283 µs，现存终态从 38.746 ms 降到 110.825 µs。每轮准入均调用 5 次 SDK GET。历史详情场景整轮分配从约 299 KB 增至 315 KB，含查询的整轮耗时从约 46.3 ms 增至 50.0 ms。改进发生在无关查询的响应时间。另一个 2,048 条历史指纹的入场基线仍单独保留。[相同负载修复前](../artifacts/verification-0.32.1/job-detail-bench-before.log) · [修复后](../artifacts/verification-0.32.1/job-detail-bench-after.log) · [原始入场负载](../artifacts/verification-0.32.1/account-admission-before.log)

浏览器进程完全退出再以相同的临时配置目录启动，仍能查询原任务，自动业务 POST 和配置写入均为零。若浏览器禁用持久存储，跨进程找回本地引用的能力取决于浏览器，后端任务及幂等保护仍按自身记录工作。浏览器夹具使用 Chromium。[进程重开](../artifacts/verification-0.32.1/ui/browser-restart.json)

本次 Go 竞态覆盖 483 个顶层用例，含子用例共 1,310 个，失败及跳过 0 个。逐包计数采用全模块运行及冻结源码上的质量包、账号四个包和原生入口复测。前端单测 56 项、Python 35 项及题库展示检查通过。Go vet 分别检查普通入口和宿主 OAuth 入口。账号、规则、手动测试和转换夹具使用合成数据。[逐包计数和来源](../artifacts/verification-0.32.1/race-summary.json) · [前端单测](../artifacts/verification-0.32.1/ui/final-node-unit.log) · [安装器和过滤检查](../artifacts/verification-0.32.1/python.log)

最终隔离 QA 库采用 Linux arm64，SHA-256 为 `3bba19688091e3cf8f45c8f991fa79596e36d6f3a2ab01b366e921359c883638`。官方宿主加载及重启阶段各校验 10 项静态资源，合计 40 个内容响应及 20 个 304。六次普通状态 GET 没有创建授权材料。真实本机另以匿名请求核实旧安装的十个资源，各得一次 200 和一次 304，匿名业务接口返回 401。[隔离库清单](../artifacts/verification-0.32.1/qa-native/qa-build.json) · [隔离宿主资源](../artifacts/verification-0.32.1/qa-assets.json) · [本机只读复核](../artifacts/verification-0.32.1/installed-readonly.json)

在工作区根目录执行以下验证命令。磁盘夹具使用临时容器、随机测试密钥和 64 KiB 内存文件系统。质量原生库使用宿主 OAuth 标签。账号查询夹具独立编译默认账号入口，载入不可执行的占位文本，实际 SHA-256 为 `b9157ee77303421085702680d5a13e4ce7f7d7ccfa9074cf66344783e2902db3`。两种库使用的源码、卷和测试结果分别记录。[质量库构建清单](../artifacts/verification-0.32.1/qa-native/qa-build.json) · [账号库及输入清单](../artifacts/verification-0.32.1/native-account-queries.json)

```sh
(cd plugin && AUTHLOGIN_TOTP_NODE="$(command -v node)" go test -count=1 -race ./... && go vet ./...)
node --test scripts/tests/*.test.cjs
node scripts/test_question_test.mjs
node scripts/verify_account_startup.cjs
node scripts/verify_account_command_recovery.cjs
node scripts/verify_command_recovery.cjs
node scripts/verify_run_receipts.cjs
node scripts/verify_browser_reopen.cjs
node scripts/verify_login_ui.cjs
python3 -m unittest discover -s scripts/tests -p 'test_*.py'
QA="$(mktemp -d)"
python3 scripts/verify_host_oauth_qa.py build --output-dir "$QA"
python3 scripts/verify_host_oauth_qa.py fixtures --output-dir "$QA"
python3 scripts/smoke_host_oauth_resources.py --output-dir "$QA"
python3 scripts/smoke_host_oauth_resources.py --output-dir "$QA" --receipt-queries
python3 scripts/smoke_native_account_queries.py "$QA/native-account-queries.json"
python3 scripts/smoke_connection_storage.py "$QA/qa-native/model-quality.so" "$QA/connection-storage.json"
```
