> **当前实例（2026-11-21 18:05，北京时间）**　8317 运行官方 CPA 8.0.76、完整 Linux arm64 插件 0.31.0、UI 13.8.0 及 Go/Sobek 2.2.1。停机备份后只替换动态库。六份实际资源与构建清单一致，校验标签相同时均返回 304。原有 195 份插件数据文件保全，账号及提交收据库逻辑内容一致，质量库仅新增一项与旧摘要精确匹配的宿主配置输入。真实账号及模型验收操作均为零。旧浏览器下次打开时，用其仍然有效的插件访问身份完成首次后台配置迁移。[版本记录](CHANGELOG_CN.md) · [部署证据](../artifacts/backend-connection-0.31.0/deployment.json)

> **历史实例（2026-11-17）：**8317 曾更新至官方 CPA + 插件 0.15.0 / UI 9.2.0 / Go＋Sobek 2.1.0，新增重置 2FA 与退出所有设备。2 个账号、9 条历史任务保留，详见 [账号安全与本机交付](OPENAI_ACCOUNT_SECURITY_CN.md)。

# 当前本机部署：官方 CPA + 插件 0.14.0

2026-11-17 02:33（北京时间）已在用户批准后完成切换。当前 UI 9.1.0、Go＋Sobek 2.0.1；账号列表一次点击 2FA 后自动保存并启用，无需另行确认。2 个账号、9 条登录记录及原凭证内容保留。当前数据路径、备份和核验见 [本机官方升级记录](LOCAL_OFFICIAL_UPGRADE_CN.md)。

下面保留旧补丁宿主部署历史，不代表当前运行版本。

> 2026-11-17：官方兼容版已改为独立插件，详见 [官方单插件方案](OFFICIAL_PLUGIN_CN.md)。本文保留补丁宿主的历史实现/部署记录；本机 8317 已按用户确认更新为官方 CPA + 插件 0.14.0，详见 [本机升级记录](LOCAL_OFFICIAL_UPGRADE_CN.md)。

# 完整 CPA 本地部署与插件安装

> 当前更新：插件 0.11.2 / UI 8.9.2。账号显示、两行操作与行内日志参照目标项目，说明见 [账号显示与日志](OPENAI_ACCOUNT_REFERENCE_CN.md)。

初次部署日期：2026-11-14；最近更新：2026-11-17。此实例运行完整 standalone CPA 管理后台，当前安装 model-quality **0.12.0 / UI 8.10.0**，登录运行时 **Go＋Sobek 2.0.0**。使用 8.0.67 归档源码加当前 quality v2 补丁，管理前端固定为 `d0126752201a35ff999bd2a1d4741215542258a8`；没有切换宿主上游基线。平台为 macOS arm64。

8317 已安装 OpenAI 账号管理、补丁宿主和 macOS arm64 本地登录运行包。在独立“OpenAI 登录”标签页中导入账号，从列表发起密码与 2FA 登录，查看候选后显式导入/更新。当前子页为账号管理、登录任务、代理与巡检；临时登录入口及表单已移除。新凭证默认禁用，质量暂停、人工禁用与冷却保留，相关规则保持停止。账号资料与管理密钥按用户要求明文持久化，具体位置见 [持久化说明](PERSISTENCE_CN.md)。

## 使用入口

- CPA 管理后台：http://127.0.0.1:8317/management.html
- CPA 插件管理：http://127.0.0.1:8317/management.html#/plugins
- CPA 内嵌插件：http://127.0.0.1:8317/management.html#/plugin-pages/model-quality/0
- 业务 API：http://127.0.0.1:8317/v1

管理密钥与业务 API Key 保存在本机私有文件：

```text
/Users/ceng/Downloads/CLIProxyAPI-Quality-Plugin/artifacts/local-cpa/LOGIN_CN.txt
```

## Go＋Sobek 2.0.0 原生引擎

2026-11-17 已同时更新宿主、插件 0.12.0 / UI 8.10.0 和原生运行包。能力接口实际返回 `go_sobek_protocol` / `2.0.0`，schema 2 直接执行 `bin/login-runtime`，没有 Node 或 Chrome 依赖。账号、代理、配置、凭证与质量业务状态的安装前后摘要一致，没有数据迁移。备份仅包含替换的宿主、插件和运行包，不重复备份账号秘密。

验证证据：`artifacts/login-go-2.0.0/`。备份：`artifacts/local-cpa/backups/login-go-2.0.0-20261117-003152`。此前失败账号已用新引擎完成真实密码、TOTP 和 OAuth 登录，生成等待确认导入的候选凭证；未提交凭证。具体结果见 [Go 引擎切换](OPENAI_LOGIN_GO_ENGINE_CN.md)。以下内容为历史版本记录。

## 登录运行时 1.2.2 参考流程补齐

2026-11-16 已安装运行时 1.2.2，插件 0.11.2 / UI 8.9.2 保持。补齐同一操作的双来源设备标识、已有网页会话的邮箱核验及 OAuth 续步、普通 JSON/HTTP 响应与网站验证的精确区分。78 项运行时测试通过，宿主全量测试、编译、原生加载、安装包验证和补丁复现通过。11 份记录匹配当前输入，其中4份插件/UI验证因源码未变复用。

安装前后账号、配置和业务状态摘要一致，未执行数据迁移或真实账号重试。备份：`artifacts/local-cpa/backups/login-reference-1.2.2-20261116-225659`；证据：`artifacts/login-reference-1.2.2/`。当前真实账号仍未完成官方人工验证；这次改动没有被描述为已通过该步骤。详细对照见 [目标项目登录实现](OPENAI_LOGIN_REFERENCE_ALIGNMENT_CN.md)。

## 登录运行时 1.2.1 网站验证请求修复

2026-11-16 22:20（Asia/Shanghai）已安装插件 0.11.2 / UI 8.9.2 与运行时 1.2.1。修复 ChatGPT 同源官方检查 POST 误拦截、空白过渡页面误判及自动检查超时分类。账号、代理、管理配置、凭证和质量业务状态在安装前后摘要一致；没有执行数据迁移。

运行时 66 项、登录 UI 16 项检查通过；宿主全量测试与编译、账号登录竞态、插件独立测试与共享库构建、原生加载、安装包离线验证和源码补丁复现均通过。12 份验证记录绑定当前源码。备份：`artifacts/local-cpa/backups/login-verification-1.2.1-20261116-222037`；证据：`artifacts/login-verification-1.2.1/`。

真实网站使用同一生效代理，修复后官方检查请求已收到 HTTP 200；页面实际显示“Verify you are human”复选框。这次观察未提交账号资料，尚未完成真实登录。详细原因和页面证据见 [验证修复](OPENAI_LOGIN_VERIFICATION_FIX_CN.md)。

## 登录运行时 1.2.0 参考项目流程与实时日志

按目标项目的网页登录、密码、TOTP、同会话 OAuth 顺序重整默认登录。网站验证、认证响应、身份与工作区都有明确成功判据，日志即时展示真实阶段。实现与边界见 [参考登录流程](OPENAI_LOGIN_REFERENCE_FLOW_CN.md)。

本机 8317 已安装宿主、插件 0.11.1 / UI 8.9.1 与运行包 1.2.0；更新前后账号、代理、设置、凭证和业务状态摘要一致。运行时 58 项、账号 UI 53 项、登录 UI 16 项、通用 UI 76 项检查通过，14 份最终验证记录与输入一致。备份：`artifacts/local-cpa/backups/login-reference-1.2.0-20261116-215900`；证据：`artifacts/login-reference-1.2.0/`。真实账号尝试结果以同目录 `real-login-result.json` 为准。

## 登录运行时 1.1.1 网站验证资源修复

本机 8317 已安装 1.1.1 运行包与相应宿主版本校验。修复官方授权页的浏览器验证请求、Cloudflare 脚本与子 frame 资源被本地拦截的问题，并区分自动检查等待与人工验证。插件 0.11.0 / UI 8.9.0 保持不变。

运行时 31 项离线测试、宿主全量测试与构建、插件测试与构建、原生生命周期、运行包宿主加载和补丁复现检查通过。配置、账号、代理、凭证与全部业务状态更新前后摘要一致。备份：`artifacts/local-cpa/backups/login-fix-1.1.1-20261116-213723`；证据：`artifacts/login-fix-1.1.1/`。真实授权页已能显示人工验证，但本次没有提交账号密码，也没有完成真实登录或上传凭证。详细根因及目标项目对照见 [登录诊断对照](OPENAI_LOGIN_CHALLENGE_ANALYSIS_CN.md)。

## UI 8.9.0 六列账号布局与入口清理

移除“登录出口”列与整块临时登录界面及相关事件、样式，重新分配六列宽度。保留账号导入、资料编辑、直接 2FA 登录、行内日志与代理设置；历史浏览器任务仍可在登录任务中完成已有回调或取消。

账号 UI 53 项、登录任务 UI 14 项、通用 UI 76 项检查通过，插件测试、共享库构建及原生加载与生命周期检查通过。7 份最终验证记录均与最终源码一致。本轮只更新插件共享库；配置、账号、代理、凭证、登录任务、规则、历史与质量状态的更新前后摘要一致，未执行真实 OpenAI 登录或模型调用。备份：`artifacts/local-cpa/backups/account-focused-8.9.0-20261116-211533`。证据：`artifacts/account-focused-8.9.0/`。

下文各版本记录描述当时的界面与能力，以本节和顶部当前说明为准。

## UI 8.8.0 七列账号布局与行内日志

移除到期时间列，重新分配邮箱、当前操作和登录出口宽度。日志将状态与阶段/原因分层显示，最新异常显示中文结果、真实耗时与处理建议，展开可查看完整安全 JSON。`unsupported_request` 明确为本地登录运行时尚未适配的请求，不据此判断密码、2FA 或账号异常。

本轮证据目录：`artifacts/account-refined-8.8.0/`。仅更新插件共享库，保留原有账号、代理、设置、历史与运行环境。

## UI 8.6.1 恢复五列

按用户明确要求保留账号、CPA状态、最近授权、登录出口与操作五列，撤回8.6.0合并及详情折叠。诊断、巡检和代理等信息直接显示，只调整列宽、间距与对齐。

44项账号、24项登录和76项通用UI检查通过；16项最终记录与源码一致。只更新插件共享库，原有账号、代理、设置、凭证、历史、质量状态摘要一致。备份：`artifacts/local-cpa/backups/account-columns-8.6.1-20261116-184209`。本轮证据：`artifacts/account-columns-8.6.1/`。

## UI 8.6.0 账号行布局

用语义账号列表替换五列表格：完整邮箱、CPA状态和最近授权聚合展示，代理及登录/资料操作在右侧。详情原位展开完整诊断与巡检信息，轮询保留展开状态和焦点。

账号45项、登录24项与通用UI76项检查通过，16项最终记录与源码一致；独立视觉复核ship。只更新插件共享库，宿主、登录运行包、配置、账号、凭证、历史与质量状态摘要一致。备份：`artifacts/local-cpa/backups/account-rows-8.6.0-20261116-183328`。证据：`artifacts/account-rows-8.6.0/`。未执行真实登录或模型调用。

## UI 8.5.1 账号目录精简

统计卡改为紧凑筛选，移除重复标题和表格外层卡片。桌面筛选、搜索、操作一行呈现；普通行约84px，手机登录/资料操作进入首屏。独立视觉复核ship，43项账号、24项登录和76项通用UI检查通过；16项最终记录与源码匹配。

本机只更新插件共享库。配置、账号、凭证、代理、历史和质量状态更新前后摘要一致；宿主与登录运行包保持原摘要。备份：`artifacts/local-cpa/backups/account-compact-8.5.1-20261116-181546`。本轮证据：`artifacts/account-compact-8.5.1/`。未执行真实登录或模型调用。

## UI 8.5.0 账号目录重构

2026-11-16已更新插件0.9.1 / UI8.5.0。账号概览、搜索工具栏、五列表格与并排行操作参考toSub2账号控制台重构；CPA状态和最近授权分开显示。单账号自然收拢，矮窗口压缩概览让出列表空间，手机保持完整值和44px操作。

16项最终验证与源码一致；账号43项、登录24项、通用UI76项检查通过，独立视觉复核为ship。仅替换插件共享库，宿主和登录运行包保持原摘要；配置、凭证、账号、任务、巡检设置及质量暂停状态的更新前后摘要一致。备份：`artifacts/local-cpa/backups/account-redesign-8.5.0-20261116-174751`。本轮证据位于`artifacts/account-redesign-8.5.0/`，布局说明见[账号目录重构](OPENAI_ACCOUNT_REDESIGN_CN.md)。未执行真实OpenAI登录或模型调用。

## UI 8.3.1 管理密钥持久化

2026-11-16 本机 8317 更新为插件 0.8.1 / UI 8.3.1。成功连接后保存插件自己的管理密钥，刷新、重新打开与浏览器历史恢复会自动重连；断开或对应密钥返回 401 时清除，临时网络失败保留。自动恢复不回写旧密钥，旧页面的鉴权失效也不会删除其他页面刚保存的新密钥。浏览器不允许本地存储时仍可手动连接，并提示刷新后需重新输入。

16 份最终验证记录通过且与最终输入一致，其中 6 份复用未更改宿主的既有记录；本轮重新执行插件测试/race/构建、原生生命周期和浏览器回归。通用 UI 76 项、登录 22 项、账号 36 项检查通过。实际安装页面已验证刷新自动连接、登录标签刷新恢复、断开清除及断开后刷新保持未连接，没有发起真实模型调用或账号登录。

仅更新插件共享库，配置、凭证、账号和业务状态摘要保持一致。备份：`artifacts/local-cpa/backups/management-key-8.3.1-20261116-160458`。证据位于 `artifacts/management-key-8.3.1/verification-summary.json`、`deployment-checks.json`、`installed-browser-checks.json`。

登录 CPA 后，在“插件管理”可看到 model-quality 已加载、已启用；侧边栏“插件页面”包含“Model Quality · 模型质量”。从该菜单打开的页面仍由 CPA 同源托管。插件不会读取父页面管理密钥，首次进入需在插件自己的“管理连接”中输入同一个管理密钥。连接成功后明文保存到此浏览器，刷新或重新打开插件时自动连接；点击“断开”或对应密钥失效会清除保存值。临时网络故障保留密钥，浏览器禁止存储时会提示。

初始没有上游凭证，也没有 mock 提供商。你可以在 CPA 后台配置 API Key 提供商，或通过 CPA 现有凭证/OAuth 功能添加自己的测试凭证，再到插件选择对应凭证与模型。自动调度和自动暂停默认关闭。插件 OpenAI 账号管理作为独立标签页提供密码/TOTP 登录，账号资料明文保存，无加密初始化步骤。实现边界见 [OPENAI_2FA_IMPLEMENTATION_CN.md](OPENAI_2FA_IMPLEMENTATION_CN.md)。

## UI 5.1.0 独立登录页

本轮源码把 OpenAI 登录提升为与智商测试、质量运维并列的第三个标签页，独立入口为插件页面内 `#login`。断开管理连接后仍保留登录页，重新连接会在当前页加载；切换页面会清空秘密输入，宿主已创建的登录任务继续运行。账号异常检查和自动重登属于后续规划，本轮没有启用自动登录行为。

UI 5.1.0 已更新到本机 8317，仅替换插件共享库。六份静态资源与源码 SHA-256 一致；配置、凭证文件与列表、暂停归属、规则、历史、设置、登录任务、资料库状态以及宿主和运行包摘要保持一致。备份位于 `artifacts/local-cpa/backups/login-tab-5.1.0-20261115-205026/`，本轮证据为 `artifacts/login-tab-5.1.0/deployment-checks.json`、`installed-browser-checks.json` 和 `verification-final/`。登录 UI 的 21 项离线检查、原有界面回归、CPA 内嵌检查、插件测试、共享库构建及原生生命周期均通过。

## 安装布局

所有运行文件固定在工作区的 `artifacts/local-cpa/`，不引用原 CPA 参考目录的运行数据：

```text
local-cpa/
  bin/cli-proxy-api              # Verified host with quality v2 patch
  config.yaml                   # Standalone configuration
  static/management.html        # Built CPA management frontend
  plugins/model-quality.dylib   # Installed native plugin
  auths/                        # Credentials and host quality state
  plugin-data/quality.db        # Plugin rules, history and audit
  kv.db                         # Host local KV state
  logs/                         # Host stdout/stderr
  local.cpa.model-quality.plist # Background service definition
  LOGIN_CN.txt                  # Local access credentials, mode 0600
  access-info.json               # Startup health-check credentials, mode 0600
  deployment.json               # Versions and artifact SHA-256
  deployment-checks.json        # Deployment verification
```

CPA 从 `plugins/model-quality.dylib` 按 C ABI 真实加载插件，通过 `/v8/management/plugins` 返回注册、启用状态及插件菜单。管理前端由已有固定源码使用 Bun 1.4.3、冻结 lockfile 构建，再安装单文件产物；未修改管理前端源码。

独立配置仅监听 `127.0.0.1:8317`，management remote-access 关闭；禁用面板自动升级和服务发现。启动使用 `--standalone --local-model`，不自动刷新远程模型目录。管理页面允许按需手动配置或刷新。

## 后台运行

当前以会话后台进程运行，具体原因见下方「macOS 后台启动权限与会话进程」。关闭浏览器不会停止宿主；退出系统登录或重启电脑后需要手动 start。保留的 launchd label 为 `local.cpa.model-quality`，当前不承担进程托管。

```sh
cd /Users/ceng/Downloads/CLIProxyAPI-Quality-Plugin
./scripts/local_cpa.sh status
./scripts/local_cpa.sh stop
./scripts/local_cpa.sh start
./scripts/local_cpa.sh restart
```

启动/重启会等待插件鉴权接口就绪。所有命令复用同一配置、端口、凭证目录和数据库，不生成新的测试环境、不清理数据。如果修改了管理密钥，需要同步更新私有 `access-info.json` 的 `management_key`，用于启动就绪检查；也更新自己的登录记录。

## 验证范围与维护

已验证管理前端构建、CPA 页面 200、未鉴权管理接口 401、model-quality 注册和有效启用、菜单注册、空凭证列表、默认开关关闭，以及停止后原目录重启。浏览器核验 CPA 登录、插件列表和内嵌插件页面。部署验证不调用任何上游模型。

原 `57788` mock 演示已停止，历史文件保留。日常使用当前 `8317` 完整 CPA 实例。需要离线专门验证时可以另行运行 `scripts/demo.py`，不要把 demo 状态当作此 CPA 实例的状态。

配置、私有登录文件、auths 和数据库均为运行数据，不能加入发布包或源码提交。升级前停止服务并备份数据，再替换二进制/插件；不要覆盖 config.yaml。质量暂停的卸载保留、人工释放和旧宿主回滚规则仍见 INSTALL_CN.md；不要删除数据库作为解除暂停或回滚步骤。


## macOS 后台启动权限与会话进程

本轮安装发现 LaunchAgent 在读取下载目录中的二进制时停在 macOS TCC 权限等待（`dyld __open`），且曾记录 `OS_REASON_CODESIGNING`。源码和离线测试均已通过；安装流程改为全新文件写入、fsync、签名校验及启动自检后原子替换，避免复用执行文件 inode。没有修改系统隐私权限。

当前 `artifacts/local-cpa/launch-mode` 为 `session`，由当前会话启动独立 CPA 后台进程；`scripts/local_cpa.sh start|stop|restart|status` 会识别并管理该进程，进程身份核对防止误杀。关闭浏览器不停止宿主巡检，显式重启仍恢复持久化设置；此启动方式**不保证用户退出登录或重启电脑后自动启动**。恢复 LaunchAgent 需由用户在 macOS 允许该后台程序访问下载目录，然后停用会话进程、移除 `launch-mode` 标记，再执行启动脚本。原 plist 保留，未更改系统权限。


## 插件0.5.0 / UI6.0.0 账号管理安装

已更新本机8317的宿主与插件共享库，登录运行包未更改。新增账号管理、加密批量导入、手动2FA授权、CPA状态巡检与显式启用的自动重新授权。巡检间隔默认15分钟，范围5–1440分钟；巡检、全局自动授权、账号自动授权默认关闭。首次登录仍须确认候选。

16份最终验证记录均通过并与当前源码/产物摘要一致，包括宿主测试/race/vet/编译、插件测试/race/共享库、源码基线/补丁复现、原生生命周期、原生界面及CPA外壳。账号UI14项、原登录UI21项离线检查通过。安装前后的配置、凭证文件和列表、质量暂停、规则、历史、设置、登录任务、资料库状态和整个登录运行包摘要一致。没有实际账号登录或模型调用。

安装备份：`artifacts/local-cpa/backups/account-manager-6.0.0-20261115-230301/`。最终证据：`artifacts/account-manager-6.0.0/deployment-checks.json`、`verification-summary.json`、`verification-final/`。本机当前启动方式及重启电脑后的限制见上一节。

## 插件 0.6.0 / UI 7.0.0 登录工作区重构

本机 8317 已于 2026-11-16 02:22:55（北京时间）更新宿主和插件共享库。新版提供可搜索分页的账号目录、右侧操作栏、完整账号资料查看、独立临时登录和任务详情。邮箱、密码、TOTP 密钥与身份 ID 按要求完整显示。

16 份最终检查记录均通过且与安装源码一致；账号 UI 21 项、登录 UI 21 项离线检查通过。独立视觉复核结论为 `ship`，实际 CPA 内嵌页面的桌面和手机验证通过。配置、凭证、规则、暂停、历史、登录任务、账号、巡检设置、资料库及登录运行包摘要在安装前后保持一致。巡检和自动授权仍关闭，间隔保留 15 分钟。未发起真实登录或真实模型调用。

备份：`artifacts/local-cpa/backups/login-redesign-7.0.0-20261116-022252/`。证据集中于 `artifacts/login-redesign-7.0.0/`，包括 `verification-summary.json`、`deployment-checks.json`、`installed-browser-checks.json` 和 `finish-review.json`。页面说明见 [OpenAI UI 重构](OPENAI_UI_REDESIGN_CN.md)。当前继续使用会话进程启动方式。

## toSub2 Go 参考改进

2026-11-16 12:07:17（北京时间）再次更新宿主，补上自动登录及授权码兑换开始前的身份、异常和许可复核，并把 TOTP 格式校验前移到资料输入边界。插件 0.6.0 / UI 7.0.0、共享库和登录运行包未变，原状态摘要保持。17 份验证记录通过，其中 7 份复用输入未变的插件与 UI 证据。详情见 [逻辑对照与验证](TOSUB2_GO_REFERENCE_CN.md)。备份：`artifacts/local-cpa/backups/tosub2-reference-7.0.0-20261116-120714/`。

## 当前安装：UI 8.0.0 内容重排

2026-11-16 12:52:46（北京时间），8317 更新至插件 0.6.1 / UI 8.0.0。登录页现在分为账号管理、临时登录、登录任务和巡检设置；目录全宽展示，导入和账号资料使用独立宽表单。桌面操作页脚可见，手机按顺序堆叠。

16 份最终检查通过，独立视觉复核为 `ship`。仅替换插件共享库，配置、凭证、账号、巡检设置与各业务状态摘要保持一致。备份：`artifacts/local-cpa/backups/login-content-8.0.0-20261116-125245`。证据位于 `artifacts/login-content-8.0.0/`，详见 [UI 8.0.0 内容布局](OPENAI_UI_CONTENT_CN.md)。启动方式继续为会话独立进程。

## 当前开发安装：明文保存 UI 8.1.0

2026-11-16 13:47:03（北京时间）本机 8317 已更新为插件 0.7.0 / UI 8.1.0。账号及选择保存的临时登录资料直接明文持久化，无加密初始化按钮或接口。按尚未上线处理，没有实现数据迁移；确认旧开发库无账号、登录任务及保存资料后，先完整备份再重新初始化登录开发库。

16 份最终验证通过；其他业务状态摘要一致。备份：`artifacts/local-cpa/backups/plaintext-storage-8.1.0-20261116-134700`。当前巡检与自动授权仍关闭，间隔为 15 分钟。数据的准确文件位置、读取/保存、重启和备份方式见 [持久化说明](PERSISTENCE_CN.md)，证据位于 `artifacts/plaintext-storage-8.1.0/`。


## 当前开发安装：账号列表与导入弹窗 UI 8.2.0

2026-11-16 本机 8317 更新为插件 0.7.1 / UI 8.2.0。参考 toSub2-go-port 的列表工具栏和导入弹窗，整合账号标题、带数量的筛选、搜索、表格及分页。批量导入在弹窗内完成，保留列表位置，并显示实时账号数量和局部错误。手机按账号纵向分组展示。

16 份验证记录与当前源码匹配：本轮新跑 10 份，6 份宿主检查因源码未变且完整校验一致而复用。账号 UI30 项、登录 UI22 项及通用 UI66 项检查通过；原生构建、生命周期和 CPA 内嵌验证通过。独立视觉复核结论为 `ship`。仅替换插件共享库，账号、凭证、配置、巡检设置与业务状态摘要保持一致，无加密或数据迁移。

备份：`artifacts/local-cpa/backups/account-layout-8.2.0-20261116-141632/`。证据：`artifacts/account-layout-8.2.0/`；布局说明见 [账号列表与导入弹窗](OPENAI_ACCOUNT_LAYOUT_CN.md)。启动继续使用会话进程。


## 当前开发安装：紧凑账号列表 UI 8.2.1

2026-11-16 本机8317更新为插件0.7.2 / UI8.2.1。根据单账号实际截图修正表格强制撑满高度的问题：少量账号随内容收紧，多账号保持局部滚动；状态与授权模式同行，活动提示精简，单页未筛选时隐藏重复分页。单账号场景在三种主题、宽屏和手机上单独验收。

16份最终验证记录匹配当前源码，其中10份本轮执行、6份复用源码未变的宿主记录；账号32项、登录22项、通用UI66项检查通过，独立视觉复核为`ship`。安装前后配置、账号、凭证、巡检与业务状态摘要一致，无数据迁移。备份：`artifacts/local-cpa/backups/account-compact-8.2.1-20261116-143602/`；证据：`artifacts/account-compact-8.2.1/`。


## 当前开发安装：账号原位操作 UI 8.3.0

2026-11-16 本机8317更新为插件0.8.0 / UI8.3.0。行内2FA登录直接提交、留在列表显示进度，不再自动打开任务页。账号资料使用原位弹窗，读取失败可在弹窗重试；保留已有凭证目标选择和显式候选确认。

16份验证记录匹配当前源码（本轮10份、复用未变宿主6份）；账号36项、登录22项、通用UI66项检查通过。独立行为审查无问题，视觉复核为ship，实际安装浏览器验证通过。配置、账号、凭证和巡检设置在安装前后摘要一致。备份：`artifacts/local-cpa/backups/account-actions-8.3.0-20261116-152250/`；证据：`artifacts/account-actions-8.3.0/`。详见 [账号操作说明](OPENAI_ACCOUNT_ACTIONS_CN.md)。
