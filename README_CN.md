# Credential Ops

Credential Ops 是面向官方 CLIProxyAPI 的独立原生插件项目，通过 CPA 的公开插件接口提供账号登录、凭证管理、智商测试与定时质量运维。[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)

[English](README.md) · [GitHub Releases](https://github.com/Cengyj/cpa-credential-ops/releases)

**从插件商店安装或升级（CPA 8.0.76）**。在「配置面板 → 可视化 → 高级与实验 → 插件」启用插件系统，将 `https://raw.githubusercontent.com/Cengyj/cpa-credential-ops/main/registry.json` 添加到「第三方插件源」并保存。在「插件 → 商店」刷新 **Credential Ops**，选择安装或更新。若 CPA 提示重启，按提示操作。

**GitHub 发布 Linux amd64（x86_64）完整包。** 包含插件账号管理、2FA 登录、智商测试、质量运维和内嵌 Go/Sobek 登录引擎，要求 glibc 2.34 或以上。[安装包与校验文件](https://github.com/Cengyj/cpa-credential-ops/releases/tag/v0.29.37)

**工作区源码为插件 0.32.2、UI 14.0.2，运行时版本为 2.2.1。** 启动时核对质量库和规则命令回执，后台按 CPA 给出的期限恢复管理请求。账号历史在页面隐藏后停止追加展示读取，返回时按原任务同步。本机 8317 实际提供插件 0.32.0、UI 14.0.0。[服务与命令契约](docs/SERVICE_LIFECYCLE_0_32_CN.md) · [三轮复核与恢复实测](docs/COMMERCIAL_REVIEW_0_32_2_CN.md)

从 CPA 侧栏返回时，同一标签页恢复上次打开的工作区。读取账号列表期间也可以切到智商测试或质量运维。固定供应商暂时缺席时，新运行记录会保留已核实的路由类型与索引。[管理连接与页面状态](docs/PERSISTENCE_CN.md) · [运维记录](docs/QUALITY_SCOPE_CN.md)

质量运维按执行轮次保留供应商历史，列表和详情均显示当轮路由及调用路径。供应商组处置在写入前复查授权成员，账户额度策略排除有明确请求数或 token 限流证据的 429。[版本记录](docs/CHANGELOG_CN.md) · [上游错误处理](docs/QUALITY_UPSTREAM_ERRORS_CN.md)

智商测试继续使用统一对象选择器和原有思考强度下拉框。HTML/SVG 回答完成后可直接查看、滚动和放大。[思考强度控件](docs/REASONING_SELECT_0_30_4_CN.md) · [供应商模型能力](docs/MANUAL_PROVIDER_REASONING_0_30_3_CN.md) · [长篇回答与中断片段](docs/UI_CN.md)

此前的质量运维、独立错误策略和恢复归属审查见 [0.30.2](docs/COMMERCIAL_AUDIT_0_30_2_CN.md)、[0.30.1](docs/COMMERCIAL_AUDIT_0_30_1_CN.md) 和 [0.30.0](docs/QUALITY_OPERATIONS_0_30_0_CN.md)。上方 GitHub 下载链接指向已发布包。

本地 8317 已于 2026-11-21 19:20（北京时间）更新到完整 Linux arm64 插件 0.32.0。原有 196 个插件数据文件全部保留，质量库、账号库、登录收据库的逻辑摘要一致。10 个实际页面资源与包内源码一致，浏览器重开全部返回 304。[验收与复现](docs/SERVICE_VERIFICATION_0_32_CN.md) · [部署报告](artifacts/verification-0.32.0/deployment.json)

在「质量运维 → 编辑规则 → 上游错误处理」中，可分别设置遇到 401、402、账户配额耗尽的 429、指定模型不支持的 400/404 时如何处理凭证。从 UI 13.6.59 起，在支持凭证处置的 CPA 上新建规则时，四项均预选「暂停凭证」，可逐项修改。编辑已有规则会显示原配置。确认处置后，同批其他凭证和后续任务继续执行。答错与上游错误策略独立，自定义、视觉和人工评审题可在支持的探测通道上处理明确上游错误。管理员在 CPA 启用后，正常刷新、测试提交及规则派发自动结束旧普通阻断，无额外确认阶段。

[0.29.36 范围与终止处置验证](docs/QUALITY_VERIFICATION_0_29_36_CN.md) · [上游错误规则、使用边界与验收](docs/QUALITY_UPSTREAM_ERRORS_CN.md) · [0.29.30 三轮审查](docs/QUALITY_VERIFICATION_0_29_30_CN.md) · [0.29.29 验证与性能对照](docs/QUALITY_VERIFICATION_0_29_29_CN.md)

账号管理继续沿用 [无提示卡片的行内登录反馈](docs/LOGIN_FEEDBACK_13_6_3_CN.md)、[五按钮分组工具栏](docs/ACCOUNT_TOOLBAR_13_6_2_CN.md)、[紧凑资料表单](docs/PROFILE_FORM_13_6_1_CN.md)与[历史/设置弹窗布局](docs/ACCOUNT_SURFACES_13_6_CN.md)。

智商测试保留 [等高双栏和固定底栏](docs/MANUAL_WORKSPACE_13_4_CN.md)，质量运维继续采用 [同高双栏工作台](docs/QUALITY_WORKSPACE_13_3_CN.md)；规则中的“答错后的处理”提供仅检测、暂停凭证、移除凭证，以及暂停后整轮通过自动恢复。策略使用 CPA 已有管理 API，不修改 CPA 源码。答错时默认仅记录结果。支持范围、管理连接、人工操作保护与并发边界见 [答错处理说明](docs/QUALITY_CREDENTIAL_ACTIONS_CN.md)。

新规则默认检测「OAuth 账户」中的「仅已启用」对象。每条规则可单独勾选 AI 提供商，范围可选全部、仅已启用或指定对象。下一轮按规则当前范围重新匹配；仍处于上游错误停用的对象退出所有答题和恢复，状态读取继续。全部范围可检测管理员或答错停用对象，本规则恢复关闭时仍可记录，他规则不能接管答错恢复归属。默认模型为 `gpt-6.2-sol`，每模型 1 份；`low / medium / high / xhigh / max` 五档直接可选，默认 `high`。旧规则可沿用 `minimal`，不会自动换档。运行记录保留当轮请求的档位，不代表上游实际生效档位。「检测周期（五段 Cron，服务端时区）」只有一个输入框，默认 `*/30 * * * *`。唯一的「启用定时检测」默认关闭；打开后保存，仅本条规则从下个周期开始。每条规则默认按凭证保留 100 条结果。打开、预览或保存规则不会立即调用模型。

[质量运维使用说明](docs/QUALITY_SCOPE_CN.md) · [sub2api 质量运维与 CPA 对照](docs/ACCOUNT_QUALITY_PARITY_CN.md) · [CPA 8.0.76 容器构建与部署](docs/DOCKER_8_0_76_CN.md)。

账号管理：[账号资料表单](docs/PROFILE_FORM_13_6_1_CN.md) · [历史记录与全局设置](docs/ACCOUNT_SURFACES_13_6_CN.md) · [转换凭证](docs/CREDENTIAL_CONVERSION_CN.md) · [重置确认框与流程](docs/UI_REWORK_12_8_0_CN.md) · [2FA 功能对齐说明](docs/RESET_2FA_REFERENCE_ALIGNMENT_CN.md)。

此前 8317 安装的是插件 0.17.3 / UI 11.18.0。该版的账号管理、登录任务、请求级诊断和完整排查报告在 12.1 中保留。见 [11.18 历史部署记录](docs/UI_REWORK_11_18_0_CN.md)。

账号列表沿用 **UI 11.7 的紧凑布局：**账号身份、CPA 状态与最近操作各占两行，完整信息保留在提示、资料与日志中。账号操作按两行三列排列，桌面按钮高 29px，手机上至少 44px。

“全局设置”保留 **CPA 上传排除套餐**（`cpa_upload_excluded_plans`），默认为空，适用于本插件所有 CPA 凭证上传和更新。套餐来自授权快照，未知明确显示为“未知套餐”，不会推定为 Free。功能来源与早期部署证据见 [UI 11.7.0](docs/UI_REWORK_11_7_0_CN.md)；早期布局作为历史保留。

插件 0.16.0 的登录与账号能力继续保留：读取失败保留列表并恢复、进行中筛选、导入前校验、实际代理来源、安全步骤与部分结果、账号独立历史，以及 SOCKS5H 远端 DNS。详见 [对照研究](docs/REFERENCE_LOGIN_20261117_CN.md) 和 [0.16.0 实现与验证](docs/LOGIN_EXPERIENCE_0_16_0_CN.md)。

**工作区选择修复已部署（0.17.1 / 运行时 2.1.2）：**未绑定账号按参考项目优先选择第一个组织工作区，否则选择第一个有效工作区；已有绑定始终精确匹配目标。覆盖网页登录与 OAuth，旧任务不会自动重试。用户需在账号列表手动发起新登录以使用修复。实现、离线验证及部署证据见 [工作区自动选择](docs/WORKSPACE_SELECTION_CN.md)；本轮未尝试真实登录。

**安全弹窗沿用 UI 11.12.0：**底部流程去掉“必选/执行/跳过”，只显示步骤名、箭头与视觉状态；原生选项和无障碍语义保留。重置弹窗为480×486px，默认选择及安全业务不变。见 [验证与截图](docs/UI_REWORK_11_12_0_CN.md)。

UI 13.6.47 将智商测试的设置与回答放进同一个双栏工作区，窄屏按页签切换。点击“开始测试”后，按钮显示加载环，旁边显示提交、排队或执行进度。[页面说明](docs/UI_CN.md)

**保留 UI 11.14.0 的设置精简：**全局设置中不再显示底部“服务状态”；上传排除保留30px高原生复选框胶囊。此前套餐配色与尺寸见 [历史记录](docs/UI_REWORK_11_14_0_CN.md)，当前套餐配色以 UI 11.16.0 为准。

**保留 UI 11.15.0 的紧凑排版：**套餐使用 CPA 的“套餐＋名称”胶囊；CPA 状态与最近操作采用统一高度的圆点徽章，三列保持两行对齐。桌面、390/320px窄屏和三套主题的布局证据见 [历史记录](docs/UI_REWORK_11_15_0_CN.md)，其中套餐配色已由本轮替代。

**沿用 UI 11.16.0 配色：**套餐按 toSub2 配色：Pro 银色，Pro Lite／Business Premium 金色，Plus 蓝、Go 绿、Team 紫；上传排除选项同步配色，保持现有22px胶囊和两行对齐。浅色／深色配色、原生构建和部署重启核验通过。见 [本轮记录](docs/UI_REWORK_11_16_0_CN.md)。

**保留 UI 11.17.0 的账号日志能力：**移除账号行“手动登录”兜底标记，操作日志增加任务摘要、历史任务选择、阶段／代码搜索、毫秒时间、事件间隔和排查报告导出；明确早期记录及截断，历史事件不混入最终安全结果。插件 0.17.2 增加日志保留／分页元数据，运行时仍为 2.1.2。离线测试、三主题与窄屏、原生构建、部署重启核验通过，见 [本轮记录](docs/UI_REWORK_11_17_0_CN.md)。

## 功能

- **智商测试**：在分组选择器中选择 CPA 账户或供应商单条路由，选择模型并从原有下拉框选择思考档位，输入所需的回答份数（默认 1 份），逐份生成；支持自定义题、测试记录、复制、下载和 HTML 预览。默认不附加思考参数；记录保存请求档位，实际效果取决于 CPA 转换和上游。打开页面或保存题目不会调用模型。
- **OpenAI 账号管理**：按 `账号----密码----2FA长期密钥` 导入，提供账号列表、资料编辑、行内日志、单账号/默认代理与直接 2FA 登录。2FA 使用目标项目适配的 Go＋Sobek 引擎，不走浏览器自动化。
- **转换凭证**：导入或粘贴 sub2api OpenAI / Codex OAuth JSON（最多 100 条、2 MiB），逐条核对转换结果、下载 CPA JSON，或一键上传到当前 CPA 凭证列表。保留禁用状态，遵守套餐排除策略；已有凭证不覆盖，不确定写入不重试。详见 [转换说明](docs/CREDENTIAL_CONVERSION_CN.md)。
- **账号安全**：账号行直接提供重置 2FA，“更多”提供退出所有设备，可选完成后重新授权并启用 CPA；重置后自动保存新密钥，未知结果不重复修改。
- **套餐与 CPA 上传策略**：账号显示授权套餐快照，可按套餐排除手动登录、自动重登及安全操作后授权的 CPA 写入。排除默认关闭；命中时保留当前会话的授权供核对，不上传、不自动补传；安全操作已确认的新 2FA 密钥仍保存。
- **账号巡检**：保存后可每 5–1440 分钟检查 CPA 已记录的认证状态，默认 15 分钟且关闭。自动重登需同时启用全局和账号许可，并有明确认证失效证据。从账号列表发起登录后，成功且未命中套餐排除时自动保存并启用 CPA 凭证；已授权的巡检重登沿用同一流程。进度和结果留在账号行，无需跳转或再次确认。
- **质量运维**：按本条规则选择的对象类型与范围检测账户、供应商路由或配置组，答错与上游错误分别处置，支持单列恢复复检、批次回执与可选定时。普通对象停用不改变规则启停，恢复使用本轮快照和原答错归属；供应商整组写入另需明确授权。输出上限不再作为建规则字段，既有规则和后台预算保留。官方 SDK 不提供独立质量暂停、归属释放和可信最终证据，不使用人工禁用来模拟。

账号密码、2FA、代理和设置按用户要求明文保存在插件数据目录。插件用 CPA 正式管理接口验证密钥，保存到插件专用数据卷，重启后自行恢复。当前浏览器单独保存页面访问身份。退出浏览器后，后台继续按原有许可执行。详见 [持久化说明](docs/PERSISTENCE_CN.md)。

## 安装与构建

推送 `main` 后，[GitHub Actions](https://github.com/Cengyj/cpa-credential-ops/actions/workflows/build.yml) 自动编译并验证 Linux amd64 完整插件，成功运行的 Artifacts 中可下载安装包。[构建流程与产物说明](docs/GITHUB_BUILD_CN.md)。

本次 CPA 8.0.76 容器安装与重建见 [Docker 部署](docs/DOCKER_8_0_76_CN.md)。其他既有官方 CPA 的原生构建流程见 [安装说明](docs/INSTALL_CN.md)：

```sh
./scripts/build.sh
./scripts/verify.sh
```

旧 8.0.67 原生构建流程校验 2,303 个基线文件；本次 8.0.76 Docker 构建校验 2,413 个基线文件，编译并内嵌原生登录引擎，输出单个动态库和符合官方安装器格式的 ZIP。SDK Go module 路径中的 `/v8` 是模块版本；插件自定义业务接口使用官方已有且受管理鉴权保护的 `/v0/management/plugins/model-quality/` 通道，两者不是一回事。

官方凭证保存接口没有跨进程原子比较更新能力。插件在写入前复查身份和原 JSON，保留未知策略字段，按本次账号登录的授权启用凭证，并在写入前重新检查最新套餐排除设置；写入后检查物理文件及运行时的启用状态；结果不确定时保留状态，不盲目重试。仅支持 standalone 本地文件凭证；不宣称支持 Home 或远端存储同步。详细边界见 [官方单插件方案](docs/OFFICIAL_PLUGIN_CN.md)。

## 文档与证据

- [质量运维逻辑、产品决策与场景验收（2026-11-21）](docs/QUALITY_OPERATIONS_LOGIC_CN.md) · [当前架构、源码定位与公开接口限制](docs/QUALITY_OPERATIONS_ARCHITECTURE_CN.md)。质量运维审查见 [0.30.2](docs/COMMERCIAL_AUDIT_0_30_2_CN.md)，当前手动控件、构建与本地交付见 [0.30.4](docs/REASONING_SELECT_0_30_4_CN.md)，不等同于已发布 GitHub 版本。

- [sub2api → CPA 凭证转换、使用与验证](docs/CREDENTIAL_CONVERSION_CN.md)

- [0.19.2 严格审查、修复与验证](docs/STRICT_AUDIT_0_19_2_CN.md)

- [重置 2FA、退出设备与恢复边界](docs/OPENAI_ACCOUNT_SECURITY_CN.md)
- [一键登录、上传与启用](docs/LOGIN_ONECLICK_CN.md)

- [安装、升级与卸载](docs/INSTALL_CN.md)
- [官方兼容架构、API 与能力边界](docs/OFFICIAL_PLUGIN_CN.md)
- [账号、管理密钥及凭证的持久化](docs/PERSISTENCE_CN.md)
- [单库打包与运行时生命周期](docs/PLUGIN_BUNDLE_CN.md)
- [Go＋Sobek 来源与此前引擎验证](docs/OPENAI_LOGIN_GO_ENGINE_CN.md)
- [质量运维工作台截图](artifacts/quality-model-card-0.27.1/installed-workspace-1440.png)（合成数据）
- [检测模型卡片截图](artifacts/quality-model-card-0.27.1/installed-model-card-1440.png)（合成数据）
- 规则编辑器：[桌面截图](artifacts/quality-model-card-0.27.1/installed-sheet-1440.png) · [手机截图](artifacts/quality-model-card-0.27.1/installed-sheet-390.png) · [三种宽度与三套主题的交互回归](artifacts/quality-model-card-0.27.1/browser/quality-scope.json) · [隔离 CPA 多模型与重启验证](artifacts/quality-model-card-0.27.1/native-smoke.json) · [8317 已安装界面验证](artifacts/quality-model-card-0.27.1/deployed-iframe.json)
- [账号删除回归验证](artifacts/account-delete-12.10.1/browser/account-delete.json) · [隔离宿主加载与重启](artifacts/account-delete-12.10.1/native-smoke.json) · [8317 管理页核验](artifacts/account-delete-12.10.1/deployed-iframe.json)
- [UI 12.10.0 构建摘要](artifacts/credential-conversion-0.20.0/plugin-package/build-manifest.json) · [文件校验值](artifacts/credential-conversion-0.20.0/plugin-package/SHA256SUMS)
- [UI 12.2.0 历史分栏设计](docs/UI_REWORK_12_2_0_CN.md)
- [UI 12.1.3 历史确认框](docs/UI_REWORK_12_1_3_CN.md)
- [UI 12.1.2 历史确认框](docs/UI_REWORK_12_1_2_CN.md)
- [UI 12.1.1 工作区改进记录](docs/UI_REWORK_12_1_1_CN.md)
- [UI 12.1.0 历史记录](docs/UI_REWORK_12_1_0_CN.md) · [容器升级记录](artifacts/ui-rework-12.1.0/deployment.json)
- [UI 11.18.0 历史验证](docs/UI_REWORK_11_18_0_CN.md) · [重启核验](artifacts/ui-rework-11.18.0/verification-after-restart.json) · [容器升级记录](artifacts/ui-rework-11.18.0/deployment.json)
- [UI 11.17.0 历史验证](docs/UI_REWORK_11_17_0_CN.md) · [重启核验](artifacts/ui-rework-11.17.0/verification-after-restart.json) · [容器升级记录](artifacts/ui-rework-11.17.0/deployment.json)
- [UI 11.17.0 构建摘要](artifacts/ui-rework-11.17.0/plugin-package/build-manifest.json) · [文件校验值](artifacts/ui-rework-11.17.0/plugin-package/SHA256SUMS)

历史部署记录（0.29.5）：8317 从插件 0.29.4 / UI 13.6.4 升级前已停服备份，只替换插件库并启动原 CPA 8.0.76 容器。六份已安装页面资源与 UI 13.6.5 安装包一致。当时的 2 个插件账号、7 条历史登录任务、1 次模型运行记录、1 条规则、1 份 CPA 凭证、设置及日志均保留；宿主镜像、配置与凭证文件未变。当前 0.30.2 的数据校验与部署证据另见 [审查记录](docs/COMMERCIAL_AUDIT_0_30_2_CN.md)。

历史原生实例曾保留 2 个账号、9 条任务，并完成 1 个旧绑定转换；它与当前容器是不同部署记录。见 [历史本机升级](docs/LOCAL_OFFICIAL_UPGRADE_CN.md)、[补丁宿主历史](docs/INSTALL_PATCHED_HOST_HISTORY_CN.md)。

源码在 `plugin/`；原生引擎在 `login-runtime-go/`；当前未修改的 8.0.76 SDK 副本在 `workspaces/CLIProxyAPI-official/`，使用 `host-patches/official-8.0.76-baseline.json` 校验，原有 8.0.67 基线另行保留。可通过 `scripts/prepare_official_context.py` 生成不含运行数据的 Docker 构建上下文。旧 `workspaces/CLIProxyAPI/` 与 `host-patches/model-quality.patch` 为历史产物，不是当前安装依赖。原始 `/Users/ceng/Downloads/CLIProxyAPI-8.0.76` 和历史 8.0.67 参考目录均保持只读。
