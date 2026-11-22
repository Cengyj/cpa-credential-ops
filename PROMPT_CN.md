# Model Quality 插件历史开发提示词：凭证暂停版

> **2026-11-22 已确认变更**　下文保留的每日调用次数预算属于历史规格。当前手动测试和质量运维累计调用次数用于统计，设置与历史格式见 [API 说明](docs/API_CN.md)。

> **2026-11-19 当前扩展：**用户要求通过 CPA 已有管理 API 增加答错后暂停/移除，以及暂停后整轮通过自动恢复；保持 CPA 源码不变。新策略与历史独立质量暂停分开保存，默认仅检测，未知写入不重放。当前实现及实际接口边界见 `docs/QUALITY_CREDENTIAL_ACTIONS_CN.md`，下文不一致处作为历史规格保留。

> **2026-11-17 当前用户决定：**保留官方 CPA，只安装插件。用户已明确同意使用官方鉴权 v0 插件通道，保留模型测试、账号管理、Go＋Sobek 2FA 和巡检；质量功能改为观察，官方接口不支持的独立暂停/释放不再启用。以下补丁宿主要求作为历史记录，当前实施以 AGENTS.md 和 docs/OFFICIAL_PLUGIN_CN.md 为准。

> **2026-11-13 最新用户决定，覆盖下文旧版复测/恢复要求：**“全部禁止复测，仅人工解除”。凭证质量暂停后，停止所有使用该凭证的检测任务和规则（含对照、judge）；禁止手动和自动复测，移除自动恢复与宿主绕过权限。仅管理员可以通过鉴权动作解除归属匹配的暂停；解除后所有相关规则仍停止，清空旧失败证据，必须显式恢复规则才重新检测。schema 1 升级至 2，保留暂停归属和历史，安全停用旧复测任务。下文保留初版任务原文供溯源；当前实现以此决定及 docs/CHANGELOG_CN.md 为准。

从下文“你是一名……”开始整段复制即可。此版本以独立开发目录为根，替代旧提示词中的模型隔离/分组处置方案。

---

你是一名熟悉 Go、C ABI 插件、LLM 代理、凭证调度、后台任务与前端开发的高级工程师。请在下面的独立工作区，参考 sub2api 实现一个可安装、可验证的 CLIProxyAPI 原生插件 model-quality，包含“手动智商测试”和“质量运维”。先核查源码，再持续完成实现、测试和交付，不要停在方案或静态页面。

一、工作目录、源码与交付边界

本次所有开发在 `/Users/ceng/Downloads/CLIProxyAPI-Quality-Plugin` 下进行。先读该目录的 AGENTS.md、docs/RESEARCH_CN.md，以及 CPA 参考源码的 AGENTS.md。

目录安排：

- `plugin/`：独立 Go module、C ABI 入口、业务代码和嵌入 UI。
- `workspaces/CLIProxyAPI/`：实施时准备的 CPA 源码工作副本。
- `workspaces/sub2api/`：实施时准备的参考仓库。
- `host-patches/`：宿主改动补丁、基线清单和应用说明。
- `docs/`：设计、接口、测试和运维文档。
- `artifacts/`：动态库、宿主构建产物及验证材料。

CPA 参考源码为 `/Users/ceng/Downloads/CLIProxyAPI-8.0.67`，作为只读参考；必要宿主改动只在本工作区副本进行。先检查源码是否有 Git 元数据。没有时记录版本、关键文件校验值和未修改基线，交付能对该基线复现的补丁；不要假装存在某个 Git 提交，也不要擅自换成最新上游版本。

工作副本只复制源码和必要构建资源，不复制 .env、生产 config.yaml、auths、token、日志、数据库、已编译程序和运行数据。保留 config.example.yaml 等示例文件。准备独立 mock 配置、临时数据目录和未占用端口。

参考仓库为 https://github.com/ranxi2001/sub2api ，已调研提交为 `ccf997c73868083d96c0bec56e71c1a34d26c98a`。记录实际参考提交，复用代码或题目时保留来源和适用许可。

插件使用当前 `/v8` 公共 SDK，不 import CPA 的 internal 包。宿主适配放在宿主副本。独立 module 的本地 replace/go.work 指向本工作区副本，核查依赖的 replace 不会自动传递等 Go module 问题；不得沿用示例中滞后的 /v7 路径。

本次是完整开发任务。普通实现选择自行推进，不反复询问。确有阻塞时提供文件证据、影响及具体解决方案。

二、产品目标与明确范围

一个插件、两个页面，共用执行、题目、判题、队列、存储和审计模块：

1. 手动智商测试：管理员指定凭证和模型，提交逻辑题、可视化题或自定义题，比较多样本输出，人工判断质量。
2. 质量运维：按规则定时或手动检测；确认质量异常后可暂停凭证，在保持业务暂停的前提下复测，满足条件后受控恢复。

CPA 当前没有可直接照搬的 sub2api 账号分组体系，本次动作明确为 `observe_only` 和 `pause_credential`，不新增分组，也不实现按模型隔离。模型结果是证据，处置单位是凭证：暂停影响该凭证所有模型的新业务执行。UI 必须清楚展示影响范围。

默认只观察。定时检测、自动暂停、自动恢复分别配置开关；安装插件、打开页面、保存题目或预设不会触发模型调用。手动测试的结果不直接触发自动处置。

首期支持 standalone CPA。Home 模式遵守其控制权；没有相应 Home 接口时明确不支持相关动作，不绕过 Home。BPS、Jev、ModelTrace、Codex 门票探针、自动删除账号/凭证、外部通知均不属于本次核心范围。

三、先核查现有能力，避免重复造轮子

CPA 重点阅读：

- `sdk/pluginapi/types.go`、`sdk/pluginabi/types.go`。
- `internal/pluginhost/management.go`、`host_callbacks.go`、`host_model_stream_callbacks.go`、`callback_contexts.go`、`auth_callbacks.go`。
- `sdk/api/handlers/model_execution.go` 及其测试。
- `internal/api/server_management_v8.go`、`internal/api/server_management.go`。
- `internal/api/handlers/management/auth_files_fields.go`、`auth_files_status_sync_test.go`。
- `sdk/cliproxy/auth/conductor_execution.go` 的 VerifyInference，以及选择、重试、会话复用路径。
- `sdk/cliproxy/usage/manager.go`、`internal/runtime/executor/helps/usage_helpers.go`。
- `examples/plugin/host-model-callback/`、`usage/`、`management-api/`、`request-lifecycle/`。

sub2api 重点阅读：

- `frontend/src/components/admin/account/IQTestModal.vue`。
- `frontend/src/utils/intelligenceTest.ts`、`pelicanHtml.ts`。
- `frontend/src/views/admin/AccountQualityView.vue`、`frontend/src/api/admin/accountQuality.ts`。
- `backend/internal/service/pelican_questions.go`、`pelican_test_service.go`、`pelican_scheduled.go`。
- `backend/internal/service/account_quality.go`、`quality_judge.go`、`quality_random_question.go`、`quality_shared_batch.go`。
- `backend/internal/repository/account_quality.go`、`account_quality_isolation.go`。
- `docs/account-quality-operations.md` 及相关测试。

已经确认、仍需按当前源码核实的事实：

- CPA 的 host.model 请求支持 auth_id 和 forced_provider，可指定真实凭证 ID；auth_id 不等于 auth_index。
- `PATCH /v8/management/credentials/status` 已存在，但不同凭证来源的更新和持久化不同。不能靠修改一个 JSON 的 disabled 字段统一处理所有凭证。
- 普通固定凭证执行和 VerifyInference 都会拒绝禁用凭证；VerifyInference 是最小推理验证，不是任意题目的复测接口。
- 现有 ABI 没有带 owner/revision 的质量暂停与恢复能力。
- 插件自定义管理路由仍有 v0 分发限制；公共 Resource 是未鉴权 GET；ManagementResponse 不是流式 writer。
- 首包前/非流式模型调用缺少完整 operation/cancel 能力；UsageRecord.Source 不能可靠标记测试流量。

四、凭证暂停的实现原则

必须实现真正的“停止该凭证接收新的业务执行”，不能只在插件页面标记已暂停。

优先将质量暂停与人工禁用分开保存，在宿主现有凭证可用性判断中统一生效：有效不可用条件包括人工禁用、质量暂停、原生冷却和其他现有限制。这样恢复只删除本规则的暂停，不需要猜测某个 disabled=true 是否是人工设置。

如通过现有状态管理服务实现，也必须提供同等的归属、版本和持久化保障。不要在插件里直接编辑 auth 文件、覆盖整个配置，或用全局 reset_cooldown 模拟恢复。不要让插件自己保存管理密钥并通过回环 HTTP 调用管理接口。

将必要状态服务或最小通用回调补充到宿主，复用现有管理器、存储、同步 hook 和调度缓存机制。名称按最终设计确定，不虚构当前已存在的 API。

逐类核查并返回动作能力：

- 文件型 OAuth/凭证。
- 配置式 API Key。
- 运行时凭证。
- 插件虚拟凭证及一个源文件展开的多个子凭证。
- Home 管理凭证。

不能安全精确暂停的类型，仍可展示可用的观察能力，并明确 can_pause/can_retest/unsupported_reason。若现有状态操作会影响同源多个子凭证，不能把它冒充单凭证动作；展示实际影响范围，首版可明确禁用这种扩大范围的自动动作。

核查暂停后的目标发现：部分 runtime-only 凭证在禁用后可能不再出现在普通列表。提供受保护的稳定 ID 查询和必要模型能力快照，区别“暂停导致列表隐藏”和“实际删除”，不能凭列表缺项删除规则、误恢复或重新创建凭证。

暂停确认生效后，普通选择、固定凭证请求、后续重试、会话粘性及 WebSocket 后续业务轮次/复用路径都要遵守限制。已提交给上游的业务执行可以自然结束，不因质量暂停强制中断；新的执行不能沿旧会话继续绕过。明确生效边界并测试竞态。

若质量暂停独立于原生 Disabled，接口和页面必须分别展示“人工禁用”“质量暂停”“有效可用状态”和原因；不要把两者混为一个开关，或声称原生 Disabled 已变化。

五、在暂停状态下复测与恢复

这是核心验收点：绝对不能采用“全局启用凭证 → 测试 → 再禁用”的方式。

为宿主模型执行添加受信任、作用域明确的质量复测能力，只允许被授权的插件任务在指定 auth_id 上绕过自己持有的质量暂停。所有权、规则、动作版本和插件实例由宿主核验，普通用户不能通过请求头、query 或 payload 自行获得此权限。

复测仍复用原有模型映射、协议转换、thinking、payload rules、代理和用量链路。不能为复测复制一套供应商 HTTP/OAuth 调用。不得轮询换号，也不得清空原生冷却。

人工禁用、其他 owner 暂停、需要重新登录、凭证删除/替换等条件仍然阻止复测或恢复。记录 blocked 及原因；不把这些情况计为答错。凭证被替换后，旧证据不能作用于新身份；正常 token 刷新与凭证身份替换应按现有 epoch/身份语义区分。

每次暂停至少绑定：auth_id、owner_plugin、rule_id、rule_revision、action_id、action_revision、evidence_run_id、凭证身份版本、原因及时间。操作幂等，必要时使用 compare-and-swap。核查 RegistrationEpoch/CredentialVersion/Generation 的实际生命周期，不把会因普通状态变化而改变的全局 Generation 当永久暂停版本，也不把进程内 epoch 直接当跨重启身份。

原本已人工禁用的凭证，插件不能认领为自己暂停；管理员在质量暂停期间增加人工禁用，后续检测通过也不能清除它。同样的布尔状态并不证明归属未变，不以 disabled 当前值或 UpdatedAt 作为唯一依据。

解除质量暂停仅意味着本规则释放限制，不保证凭证已恢复业务可用；页面继续显示剩余禁用、冷却和其他原因。自动恢复默认关闭，管理员可以预览并手动解除本规则暂停。人工解除流程说明是否继续自动规则，提供“暂停规则并解除本规则暂停”，避免旧失败计数或尚未完成任务立即再次暂停。

规则编辑保留已有动作的版本和归属，需要转移时显式审计并 CAS。规则暂停不自动解除凭证暂停。持有暂停的规则删除或更换目标前必须处理归属，不能产生无主暂停。

暂停须持久化；进程重启、插件停用或卸载不能静默放行。宿主保留受管理鉴权保护的查看和人工释放入口，使插件不可用时仍可处理其暂停。启动加载顺序保证暂停恢复后才放行对应凭证，崩溃造成动作状态不确定时进行幂等对账。

六、规则判定和误暂停保护

首版同一凭证只允许一条自动暂停规则，允许多个手动测试或纯观察规则。规则支持多个检测模型，但处置仍然是凭证级。

规则配置包括：目标、模型、题目版本、推理参数、每模型样本数、判题配置、周期、时区、预算、暂停门槛、复测周期、恢复门槛和保留期。

首版默认使用保守门槛：

- 单模型：全部计划样本完整且可判定后，出现 incorrect 才是该模型失败；全部 correct 才通过；缺样本、unknown、skipped 则本模型轮次不确定。
- 凭证暂停：所有选定检测模型在同一轮均明确失败，连续 3 个有效轮次后才可暂停。部分模型通过或证据不足时仅展示疑似异常，不扩大为整个凭证暂停。
- 凭证恢复：所有必选模型、全部计划样本连续 2 个完整轮次通过，且自动恢复已开启，才尝试解除本规则暂停。
- 阈值可配置并检查边界；未知轮次打断连续判定，不形成新动作。静置期只决定何时复测，时间到不会自动恢复。

以上是本插件建议默认值，不是对 sub2api 既有行为的陈述。配置界面说明：全失败指“所选检测模型”，不能据此声称该凭证所有可用模型均已验证。

分别保存 execution_status、quality_verdict、action_status，不用 HTTP 200 直接推导质量通过。正确、错误、无法判断分开统计，质量通过率只以可判定样本为分母，同时展示覆盖率。

网络错误、401/403、429、5xx、空输出、截断、流提前 EOF、拒答、取消和 judge 失败不算降智，不触发新的自动暂停，也不作为恢复证据。存在问题时可以形成运维记录，但不能混入答题质量阈值。

七、手动测试和判题引擎

从 host.auth.list/get_runtime/models 获取脱敏目标，模型 reasoning 能力另外查询已验证的模型注册表/能力接口，未知时明确标未知。展示 auth_id、auth_index、provider、类型和模型时避免泄漏路径、邮箱或其他不必要信息。

题型：逻辑题、HTML/SVG 可视化题、自定义 prompt。若使用糖果题，答案 21 必须绑定准确题面和版本，用户改题后不能继续沿用。随机推理题使用本地生成器和有界求解器，保存题族、版本、seed/实例和答案；答案不发给被测模型。

默认每轮 1 个样本，最多 8 个，服务端执行限制。冻结题面、答案/人工标准、模型映射、推理参数、输出预算、judge 配置/模板、规则版本和 payload 配置版本。各样本使用独立会话；不同凭证对比使用同题和兼容有效参数。

只通过 host.model.* 执行，固定 auth_id 和 provider。记录请求模型、解析模型、实际凭证、response_model、有效参数和用量；未知不伪造，response_model 不是身份认证。

尊重 payload rules。只读采集最终测试条件指纹或变更标记，不能证明实际题面/模型/参数与冻结判据一致时，标记证据不足；可以展示输出，但禁止据此自动暂停、恢复或作为有效对照。

手动页支持选择、执行、进度、取消、复制、同配置重跑、多样本比较、历史与脱敏 JSON 导出。展示执行状态、总耗时、可获得的 TTFT/token、原始回答、安全预览、人工评审和可选判题结果，不输出未经校准的通用 IQ 分数。

确定性判题使用题型专用严格解析；不能用“包含 21”等子串规则。无法解析或自相矛盾时 unknown。

可选 LLM judge 使用独立凭证/模型，排除被测凭证，严格限定尝试次数和调用预算。参考答案与候选答案作为不可信数据，与指令隔离；要求结构化 verdict=correct|incorrect|unknown 和 reason，拒绝重复字段、非法值、额外输出和超限内容。格式/调用错误均为 unknown，单独记录 judge 用量。

随机题自动处置需同批同题、同模型映射、同有效参数、同已知路径和 judge 条件下，存在其他凭证明确通过的有效对照；按凭证去重，不能用自己的别名充当对照。全体答错、缺对照、陈旧或窗口外结果只记录。启动/采集窗口只影响证据有效性，不转成上游网络 timeout。

HTML/SVG 原文转义展示，预览用 sandbox iframe 和严格 CSP，阻止外部网络、表单、弹窗、宿主页访问；允许脚本时不能同时开放 same-origin。限制响应和分片大小，不在宿主页直接执行模型代码。

八、界面与鉴权接口

手动测试页和质量运维页共用插件菜单入口。质量页至少提供：规则列表、凭证状态、立即执行、规则暂停/恢复、运营历史、暂停原因、复测进度、恢复冲突、动作预览及人工解除本规则暂停。

明确区分“暂停检测规则”和“暂停凭证”，使用不同按钮和说明。前者只停止后续检测；后者停止该凭证接收业务执行。人工解除质量暂停也不等于开启人工禁用。

公共 `/v0/resource/plugins/model-quality/...` 只返回静态外壳。目标、任务、规则、结果、指标和动作均走鉴权的 `/v8/management/plugins/model-quality/...`。复用 management 权限和 remote-access 策略，凭证不放 URL、HTML、日志或导出。独立页面输入管理密钥时仅存页面内存。

必要时补充独立的 v8 插件路由注册/分发能力，保留旧插件兼容；不增加或修改弃用 v0 管理端点。处理路由冲突和版本兼容，宿主能力不足时明确报告所需版本。

首版用 POST 创建异步任务返回 202/run_id，鉴权 GET 轮询，POST 取消。当前 ManagementResponse 是完整字节，不伪装成 SSE。路由为精确匹配，不假设支持 :id/*。

可采用固定路径：GET /targets、/questions、/runs、/runs/detail、/rules、/metrics、/actions；POST /runs、/runs/cancel、/rules、/rules/run、/rules/pause、/actions/preview、/actions/release；PUT/DELETE /rules。ID 放 query/body，定义分页、幂等键、错误码、字段限制和版本冲突。

九、后台执行、持久化和观测

插件拥有有界队列、全局/每凭证并发和持久化；当前没有现成插件 cron/KV/job ABI。优先评估项目已有 bbolt，封装 Store 和迁移。若选择 SQLite，解释查询需求及 c-shared 跨平台代价。插件数据与宿主暂停权威状态各有明确所有者，跨存储动作使用幂等、对账或可靠事件保证一致，不假称跨库原子事务。

规则支持固定间隔和五段 Cron，UTC 存储、明确时区展示；默认时区可用 Asia/Shanghai，允许配置。后台执行独立于浏览器，保存规则与“立即执行”是不同动作。保存预设不创建规则或调用模型，批量修改返回逐项结果，不冒充原子事务。

任务先持久化再入队，通过幂等键、规则版本和单实例所有权避免重复执行。后台不能保存过期管理 RPC 的 host_callback_id。为模型执行补充按插件实例隔离的 operation/cancel，处理首包前、流中、非流式取消及重复关闭竞态；不要用固定网络超时替代。

重启后无法证明完成的 run 标为 interrupted，保留已完成样本，不无条件重发可能已经消耗额度的请求。租约到期不允许重复启动仍在执行的网络调用。reconfigure/quiesce/shutdown 幂等，停止接单、取消操作、关闭流、回收 worker，避免重复调度和旧实例写入。

配置最大模型数、总样本、输出、并发、队列、每日调用/token 预算和历史/存储上限。预算按已知用量执行，缺 usage 标未知，不伪造零成本。

主动任务和结果是首期重点。被动生产指标复用 UsagePlugin/必要生命周期事件，回调里不请求模型或重型写库；缓冲有界，丢事件有可见计数。可信来源区分 production、manual_test、scheduled_probe、judge、internal_other/unknown，经 context→usage→ABI 传递，不进入上游业务 payload，历史缺字段不能默认生产。

UsageRecord.Source 不能作为可靠来源标记，也不能原样落盘。usage 是尝试事件，不一定等于入站请求；区分重试尝试与最终请求，按真实事件语义关联和去重。展示质量率/覆盖率、错误类型、延迟、TTFT、token，缺失显示未知。默认不保存生产完整提示词/回答，不保存 APIKey、Cookie、access/refresh token 或原始凭证。

十、工程约束

- 遵守 CPA AGENTS.md。Go 版本跟随根 go.mod，插件独立 module 使用 /v8 公共 SDK。
- 使用 moego/ojson 和 moego/base64；JSON 写操作在 *ojson.Doc 上跨阶段进行，最后统一序列化，不引入 gjson/sjson/encoding/base64。
- 保持 canonical ThinkingConfig → provider applier 架构，不独立修改 translator，不复制 OAuth/供应商执行器。
- payload 配置是最后语义屏障：内建转换/注入/清理完成后应用一次用户规则，之后不恢复过滤字段或覆盖配置。新增来源和操作控制不改变业务 payload。
- 上游连接建立后不新增网络 timeout/deadline，不照搬 sub2api 的固定请求超时。只用明确取消、停机取消和资源约束；现有允许例外按 AGENTS 保持。
- executor 目录只放执行器和单测，辅助代码放 helps；logrus 脱敏日志、上下文错误、处理 defer 错误，不用 log.Fatal。
- 代码注释英文；中文文档用 _CN.md。时序/TTL/调度测试用 mock clock 或确定性同步，不靠 time.Sleep。
- 保持实现适度，不引入本任务不需要的分布式队列、账号分组或通用策略框架。

十一、实施顺序和验收

先简洁报告实际能力、缺口、文件边界、状态模型和接口，然后继续执行：

A. 准备独立源码副本和基线；实现最小宿主补强及兼容测试。
B. 完成插件构建、两页 UI、手动测试、质量规则、判题、持久化、后台执行和取消，使观察模式可用。
C. 完成凭证级暂停、保持暂停时复测、受控恢复、版本/所有权保护、重启/卸载行为及集成验证。

核心功能不能留 TODO。不要把“观察模式完成”描述为“自动暂停与恢复已完成”。遇到平台真实限制如实列明，不用临时启用凭证等方式绕过。

默认 fake upstream/mock host 离线验证，不使用真实账号额度。至少覆盖：

- 固定 auth 不换号；模型别名、thinking、payload 最后屏障；测试条件被改写时阻止动作。
- 流多分片/心跳/流内错误/EOF/空输出/截断；首包前、流中、非流式取消和竞态。
- 同轮参数一致、随机题可重现、标准答案正确、judge 输出严格校验、答案不泄漏。
- 401/429/网络失败/unknown 不暂停；部分模型正常不暂停整个凭证；缺对照、缺样本、旧批次不动作。
- 自动暂停后所有模型的新业务执行受阻；后续重试、粘性会话、固定请求和 WebSocket 后续轮次不能绕过。
- 暂停期间复测不全局启用；普通客户端不能伪造复测授权；人工禁用/其他 owner/原生冷却仍生效。
- 通过后只释放自身暂停；人工同值禁用、删除重建凭证、正常 token 刷新、旧 epoch 结果、规则版本变化均正确处理。
- 不同凭证存储类型、虚拟子凭证和状态同步 hook；错误不能造成假成功或扩大处置范围。
- 重复提交/tick、队列满、重启/热重载/卸载、跨存储不确定状态对账、无主暂停与宿主人工解除。
- 未授权 API、公共 GET 无副作用、生成 HTML 沙箱、敏感数据脱敏、事件去重与指标分母。

在各自模块运行 gofmt、相关测试、必要 race 测试和 go test ./...。在宿主副本按 AGENTS 执行 `go build -o test-output ./cmd/server && rm test-output`。插件独立 module 单独测试和实际 `go build -buildmode=c-shared`，根模块测试不会覆盖它；进行宿主加载、调用、暂停、复测、释放和停止的端到端 mock 验证。前端完成构建与交互验证。

十二、交付要求

交付插件源码、动态库、必要宿主补丁及准确基线、安装配置/API/存储说明、测试结果、页面演示、升级和回滚说明。明确需要原版 CPA 还是带补丁的宿主及最低能力；需要补丁时不能宣称对当前原版即插即用。

说明暂停的持久化、卸载后的人工解除和旧宿主回滚行为。旧版本不识别暂停状态可能造成放行，回滚前必须有可执行的处理方案，不能以删除数据库作为回滚步骤。

最终报告说明实际完成内容、验证平台、测试结果、仍有的限制。原始 CPA 参考目录保持不变，所有工作和可复现交付保存在本独立开发目录。
