# 后台服务验收与证据索引

编号沿用实施任务。起点为插件 0.31.0、UI 13.8.0。本轮源码为 0.32.1、UI 14.0.1。下表的“服务”指 Go 服务和适配器在临时数据库、假 SDK 或假上游上的测试。“浏览器”指 Chromium 合成夹具。“隔离宿主”指官方 CPA 8.0.76 在断网容器和临时卷中的运行。“历史完整包”指已交付的 0.32.0、UI 14.0.0、嵌入 Go/Sobek 2.2.1。[版本和核验范围](SERVICE_VERIFICATION_0_32_1_CN.md) · [历史交付](SERVICE_VERIFICATION_0_32_CN.md)

0.32.2 的增量复核使用新构建的隔离原生库和 Chromium 夹具，记录了管理拒绝同时取消、并发目录失效、命令索引损坏、零字节卷及账号日志的验证结果。[增量复核及复现命令](COMMERCIAL_REVIEW_0_32_2_CN.md)

## 一至七、十　架构与业务边界

| 原要求 | 可核实的边界 | 证据 |
| --- | --- | --- |
| I.1–4、8–9 | 浏览器恢复自己的访问身份并独立查询资源。切页、重建 iframe、关页及浏览器进程重启后，按已有命令 ID 读结果。普通管理 GET、配置热更新、实例关闭另有原生服务计数。HTTP 连接池由后台实例持有。 | [浏览器导航](../artifacts/verification-0.32.1/navigation/account-navigation.json) · [浏览器进程重开](../artifacts/verification-0.32.1/ui/browser-restart.json) · [实例创建与关闭计数](../plugin/cmd/model-quality/account_routes_test.go#L33) · [传输与超时](../plugin/quality/management_client.go#L47) |
| I.5–7、IV.1–11 | 私有记录通过修订控制设置、轮换、失效和清除。启动时异步验证，临时故障按原因退避。普通 GET 不触发验证，旧 401、模型 401 与当前管理 401 分开。只读磁盘、空间耗尽与失效标记修复均有实测。热更新复用实例，替换先排空旧回调。 | [连接状态与竞态](../plugin/quality/management_connection_test.go) · [失效标记落盘](../artifacts/verification-0.32.1/connection-disk-repair.log) · [只读挂载和 64 KiB 磁盘](../artifacts/verification-0.32.1/connection-storage.json) · [实例替换](../plugin/cmd/model-quality/lifecycle_test.go) |
| II、III | 原生入口、账号、质量、展示查询及前端资源独立分工。调用链表按入口列出鉴权、权威数据、外部依赖、并发、回执和重启行为，涵盖导入、代理、上传、登录、安全动作、转存、测试、质量、历史和热更新。官方宿主 2,413 个基线文件逐一核对。 | [初始工作区](../artifacts/verification-0.32.0/baseline-status.txt) · [功能调用链与数据位置](SERVICE_LIFECYCLE_0_32_CN.md) · [正式 API](API_CN.md) · [官方基线](../artifacts/verification-0.32.1/host-integrity.json) |
| V.A–E | 首屏通过本工作区的正式读请求建立访问身份。仅连接状态 503 时账号可读、可导入。目录、历史或统计失败分别显示；已读数据可以保留。提交时服务重读 CPA 状态。工作区读取、命令引用、账号转换、视图更新有独立模块。 | [首屏及认证](../artifacts/verification-0.32.1/navigation/account-navigation.json) · [独立读取和迟到响应](../artifacts/verification-0.32.1/ui/partial/reads.json) · [手动提交](../artifacts/verification-0.32.1/ui/manual/receipts.json) · [工作区读取](../plugin/ui/workspace.js) · [命令引用](../plugin/ui/commands.js) · [转换](../plugin/ui/conversion.js) · [渲染](../plugin/ui/markup.js) |
| VI.1–10 | 展示目录按快照稳定分页，相同读请求合并。执行及写入重新读取当前目录、规则和身份。慢质量目录读取移到锁外并复查。审计、统计、账号列表分别使用展示投影。已发送的 SDK 请求只能报告取消边界。 | [目录与竞争测试](../plugin/quality/directory_pages_test.go) · [本轮目录与锁测量](../artifacts/verification-0.32.1/performance.log) · [浏览器五轮样本](../artifacts/verification-0.32.1/ui/workspace-benchmark.json) · [历史内存、延迟及吞吐基线](SERVICE_VERIFICATION_0_32_CN.md) |
| VII | 新客户端为规则、手动测试、账号运行及批次保存原命令键。规则派发与子运行在同一事务内受理。账号命令按原键和已持久化的任务查询；批次取消确认与在途写入分别核对。旧客户端仍可省略规则创建、立即执行的命令键；同键去重依赖请求提供该键。 | [规则收据](../plugin/quality/rule_commands_test.go) · [手动测试清理后收据](../plugin/quality/lifecycle_test.go#L100) · [账号收据与只读磁盘](../plugin/login/job_commands_test.go#L61) · [批次丢回执及取消](../artifacts/verification-0.32.1/ui/command-recovery.json) |
| X | 账号、质量、跨库登录收据、后台授权及安全记录各有权威存储。写前意图和写后确认可分别中断；缺证据保留待核对。迁移沿用已有卷，回滚需要停服备份并核对升级后的外部操作。存储错误、SDK 错误和上游错误各有来源。 | [持久化与回滚](PERSISTENCE_CN.md) · [跨库恢复](CREDENTIAL_REAUTH_RULES_CN.md) · [缺失和损坏收据](../plugin/login/account_batch_repair_test.go#L15) · [SDK 来源验证](../artifacts/verification-0.32.1/account-job-command.log) · [构建过滤与安装器回归](../artifacts/verification-0.32.1/python.log) |

## 八　调度、重启和重新授权

| 编号 | 在什么条件下观察什么结果 | 证据 |
| --- | --- | --- |
| 1、3 | 同一条目重登成功后，仍有许可的规则进入可检测状态。等待保留启用意图；期间暂停、关闭或删除的决定优先。服务夹具分别运行手动、自动、写后跨库中断三种路径。 | [账号和质量服务联动](../plugin/login/credential_receipts_test.go#L33) · [用户意图与规则变更](../plugin/quality/login_coordination_test.go#L8) |
| 2、5 | 写前撤出以旧凭证为主体、对照或判题的任务；协调完成立即派发的模型调用为零。下一次正常派发采用新快照和证据。 | [依赖撤出与等待](../plugin/quality/credential_login_test.go#L60) · [确认后派发测试](../plugin/quality/login_coordination_test.go#L28) |
| 4、12 | 未知写入、运行时采用未确认、身份冲突各自保留状态。已写入而缺收据、旧定位被新条目占用和历史来源不足均不能建立成功或继承权限。 | [未知与身份冲突](../plugin/quality/login_coordination_test.go#L104) · [定位重用](../plugin/quality/login_coordination_test.go#L168) · [缺少原始收据](../plugin/login/credential_receipts_test.go#L196) · [历史来源核对](../plugin/quality/error_policy_disabled_test.go#L272) |
| 6、7 | 多个漏检周期只合并为一次新补检。重开原卷后重读规则、当前目录和范围；之前已经受理的中断子任务保留历史。隔离宿主在首个页面查询前完成补检，并回到正常周期。 | [服务重启与成员变更](../plugin/quality/background_recovery_test.go#L119) · [管理不可用期间的游标](../plugin/quality/background_recovery_test.go#L10) · [隔离宿主真实时钟](../artifacts/verification-0.32.1/qa-final-cron.json) |
| 8、9 | 手动运行撞上到期时刻、正常 Cron 与补检重合、重复就绪及再次重启共同去重。停机后新授予定时许可，仅从授权后计划执行。 | [手动运行消费到期时刻](../plugin/quality/rule_commands_test.go#L165) · [相遇与撤权](../plugin/quality/background_recovery_test.go#L64) · [新许可边界](../plugin/quality/background_recovery_test.go#L275) |
| 10 | 已就绪 OAuth 先运行，另一 OAuth 重登确认后补跑，提供商目录最后恢复时只受理剩余对象。三分支服务用例共 4 次文件假探测、2 次提供商假模型调用、0 次状态写入。全员等待期间连续 30 次 Tick 没有新增目录读取。 | [原失败](../artifacts/verification-0.32.1/partial-schedule-red.log) · [成员、重启、手动与撤权](../plugin/quality/partial_schedule_test.go) · [混合范围服务回归](../artifacts/verification-0.32.1/partial-mixed-branches-final.log) |
| 11 | 自动重登在巡检和写前分别复查全局许可、账号许可及当前 CPA 状态。人工停用继续生效；人工重登沿用明确的一次性授权。 | [人工与自动提交](../plugin/login/account_auto_test.go#L69) · [人工停用与撤权](../plugin/login/account_auto_test.go#L167) |

## 九　质量运维语义

| 编号 | 当前证据与处置 | 证据 |
| --- | --- | --- |
| 1–3 | 手动测试不写凭证。答错和上游错误各自选择处理；答错写入要求本轮完整、明确可判。错误、未知、拒答、截断、取消或歧义输出无法触发答错处置及恢复。 | [手动及不完整样本](../plugin/quality/credential_policy_test.go#L842) · [独立错误策略](../plugin/quality/upstream_policy_test.go#L8) · [歧义输出](../plugin/quality/commercial_evidence_test.go#L57) · [隔离宿主完整与歧义轮次](../artifacts/verification-0.32.1/qa-final-quality.json) |
| 4 | 自定义、视觉及人工评审题即使没有标准答案，仍可单独使用上游错误策略。人工评审结果不获得答错处置权限。 | [题型与授权矩阵](../plugin/quality/error_only_questions_test.go) · [隔离宿主的视觉和自定义题](../artifacts/verification-0.32.1/qa-final-quality.json) |
| 5、6、12 | 已确认的上游错误停用使对象退出所有规则及恢复分支，保存触发来源和证据。B 规则取得新错误时，A 规则原答错归属仍在；对象已停用时额外启停写入为零。 | [覆盖各范围和入队后复查](../plugin/quality/error_policy_disabled_test.go) · [跨规则及 OAuth 归属](../plugin/quality/credential_entry_lifecycle_test.go#L84) · [提供商整组](../plugin/quality/provider_entry_lifecycle_test.go#L84) · [隔离宿主跨规则及重启](../artifacts/verification-0.32.1/qa-final-quality.json) |
| 7、8 | 管理员在 CPA 启用后，直接提交手动测试可以读取当前状态；正常规则重新纳入该条目。取得新错误证据后可再次触发原策略。 | [当前状态准入](../plugin/quality/manual_error_policy_test.go#L71) · [隔离宿主重新启用与再次处置](../artifacts/verification-0.32.1/qa-final-manual.json) |
| 9–11 | 全部范围可记录人工停用或另一规则答错停用对象，保留原归属及既有保护。仅已启用范围可另外对原规则自己的答错停用对象执行获准的恢复复检。其它规则不能借用恢复权。 | [全部范围及当前状态](../plugin/quality/credential_policy_test.go#L569) · [仅启用范围的原规则恢复](../plugin/quality/credential_policy_test.go#L659) · [跨规则拒绝借权](../plugin/quality/credential_policy_test.go#L902) |
| 13、14 | 答错恢复依赖同一规则的停用选择和恢复开关，与普通检测共用原周期，分别保存恢复记录。所有所选模型、样本、组内路由须完整通过；写前复查本轮快照。 | [完整轮次及归属](../plugin/quality/credential_policy_test.go#L316) · [规则同周期](../plugin/quality/credential_policy_test.go#L659) · [全部路由](../plugin/quality/provider_disposition_test.go#L58) · [互相矛盾的路由证据](../plugin/quality/scope_dispatch_review_test.go#L352) |
| 15–17 | OAuth 和提供商使用各自的适配器。组选路与动态范围不自动授权整组写入。服务测试覆盖明确整组许可及停用状态下定向探测。提供商宽范围原生集成还保留 0.32.0 完整包报告。 | [提供商配置、整组与定向探测](../plugin/quality/provider_adapter_test.go#L302) · [写前组范围](../plugin/quality/scope_dispatch_review_test.go#L280) · [原生提供商历史完整包](../artifacts/verification-0.32.0/delivery-provider.json) |
| 18、19 | 已核实目录缺少模型才能使用该对象的模型不支持策略。目录或认证入口失败、不完整模型 ID、速率限制、临时错误和明确上游凭证错误分别判定。模型 401 不改变后台管理授权。 | [目录缺项和组证据](../plugin/quality/scope_dispatch_review_test.go#L169) · [不完整目录](../plugin/quality/commercial_provider_directory_test.go#L157) · [上游状态分类](../plugin/quality/upstream_error_test.go) · [管理与上游 401](../plugin/quality/management_connection_test.go#L225) |
| 归属与快照 | 同条目内容、token 或更新时间变化后保留原规则归属，每轮重新取得快照。两次读取间未观察到的启停不推测。提供商换密钥导致公开 ID 变化时，保留无法证明连续性的记录。官方模式继续保护历史独立暂停和未知写入。 | [文件归属与写前竞争](../plugin/quality/credential_entry_lifecycle_test.go#L12) · [提供商标识变化](../plugin/quality/provider_entry_lifecycle_test.go#L154) · [历史独立暂停](../plugin/quality/official_host_test.go#L146) · [隔离宿主未观察到的启停](../artifacts/verification-0.32.1/qa-final-quality.json) |

## 十一　运行证据与覆盖范围

| 验收组 | 验证条件及范围 | 证据 |
| --- | --- | --- |
| A 页面与授权 | Chromium 使用合成响应核对 34 个导航场景，涵盖仅连接状态失败、错误密钥、重建 iframe、跨标签轮换、迟到 401 与退出。浏览器完整重启查回原键，自动业务 POST 为零。实例创建数和普通读触发探针数来自独立 Go 测试。 | [浏览器导航](../artifacts/verification-0.32.1/navigation/account-navigation.json) · [浏览器重开](../artifacts/verification-0.32.1/ui/browser-restart.json) · [普通 GET 与探针](../plugin/quality/management_connection_test.go#L36) · [服务实例计数](../plugin/cmd/model-quality/account_routes_test.go#L33) |
| B 无浏览器运行 | 隔离官方宿主按真实时钟经历停机和同卷容器重建，首次页面请求前完成质量补检。在另一份临时卷中，默认账号入口按既有许可在首次账号请求前完成巡检。同卷重启后巡检修订维持为 2，没有重复检查。测试卷中的全局和账号自动重登均关闭。管理通道不可用时 SDK 独立观察仍执行。 | [隔离宿主补检](../artifacts/verification-0.32.1/qa-final-cron.json) · [隔离宿主无页面巡检](../artifacts/verification-0.32.1/native-account-queries.json) · [服务层无页面巡检](../plugin/login/inspection_restart_test.go#L9) · [SDK 独立执行](../plugin/quality/background_recovery_test.go#L169) |
| C 命令与崩溃 | 规则、账号、转换、巡检和手动测试覆盖丢回执、取消、刷新与重复提交。假 SDK 写后终止子进程；跨库提交收据丢失或损坏时保留核对。隔离官方宿主的未知写入经历两次重启仍只有一次 PATCH。 | [页面账号回执](../artifacts/verification-0.32.1/ui/account-commands.json) · [规则与批次](../artifacts/verification-0.32.1/ui/command-recovery.json) · [写后进程终止](../plugin/login/account_batch_crash_test.go#L98) · [跨库故障](../plugin/login/account_batch_repair_test.go#L15) · [隔离宿主未知写入](../artifacts/verification-0.32.1/qa-repro/qa-unknown.json) |
| D 账号与质量业务 | 账号导入编辑、代理、登录、自动重登、种子重置、退出设备及转换使用临时服务和合成材料。账号登录收据与质量协调见第八节。最终隔离质量库另验证人工重新准入、跨规则优先级和同条目内容变更。 | [账号界面夹具](../artifacts/verification-0.32.1/ui/account-commands.json) · [种子、退出与未知写入](../plugin/login/security_test.go) · [转换和幂等](../plugin/login/conversion_test.go) · [冻结库隔离场景](../artifacts/verification-0.32.1/qa-coverage.json) |
| E 并发与负载 | 合成负载测量 2,000 个目标的 20 页读取、慢目录锁等待、账号历史、worker 吞吐及 RSS。容量、队列、热更新和关闭单独验证。关闭测试在 Go 虚拟时钟环境内等待所属 goroutine 退出。长期实服压测和物理断电仍无实测。 | [本轮目录与延迟](../artifacts/verification-0.32.1/performance.log) · [历史吞吐和 RSS](SERVICE_VERIFICATION_0_32_CN.md) · [大目录和队列](../plugin/quality/scope_test.go#L122) · [容量边界](../plugin/login/account_batch_crash_test.go#L15) · [关闭后无活动](../plugin/cmd/model-quality/account_routes_test.go#L33) |

0.32.1 的宿主 OAuth 标签隔离库包含质量服务，官方宿主完成加载、资源及重启核验。嵌入式登录完整包的构建、本机停机备份和数据保全报告属于 0.32.0。8317 的本轮检查使用匿名请求核对该已安装版本的静态资源与业务鉴权。公开 SDK 无法保证撤回已提交的上游调用；CPA 文件状态及删除接口仍有跨进程并发窗口。[本轮竞态逐包报告](../artifacts/verification-0.32.1/race-summary.json) · [隔离原生库清单](../artifacts/verification-0.32.1/qa-native/qa-build.json) · [历史完整包本机部署](../artifacts/verification-0.32.0/deployment.json) · [本机只读报告](../artifacts/verification-0.32.1/installed-readonly.json)
