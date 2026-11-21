# sub2api 智商测试：输出设置与展示对照

**结论：sub2api 的手动「题目测试」没有「输出上限」或 `Temperature` 输入框，也没有统一给所有上游套一项输出 token 限额。它不是无限制：单轮并行数、题目的输出格式，以及各上游分支实际发送的参数是三件不同的事。**

核查范围：账号菜单「智商测试 → 题目测试」中的糖果题和 HTML 题，不把同一弹窗的状态探针、ModelTrace、Ciallo、定时测试或质量规则混成同一执行路径。历史窗口会合并展示手动与定时结果，下文分别说明其来源。

2026-11-15 首次核对了 `ranxi2001/sub2api` 默认分支 `production` 的 `e90811e27bc442763a496027f935a7bb1fb4396b`，并与参考提交 `ccf997c73868083d96c0bec56e71c1a34d26c98a` 比较；当时核对范围内的题测界面、题库、输入参数和上游构造逻辑一致。该结论只适用于当时两个提交。

2026-11-21 通过 `git ls-remote` 确认远端 HEAD 为 [`ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af`](https://github.com/ranxi2001/sub2api/commit/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af)，只读取该提交的相关源码。下文原版行为和新增展示研究按本次核对结果更新；插件 UI 4.6.0 的旧对照单独保留为历史快照。本次没有运行参考项目的浏览器或真实账号测试。

## 原版界面和执行方式

| 设置 | 实际行为 |
| --- | --- |
| 账号 | 从某个账号进入。整轮请求固定使用这个账号和一个模型，不是按样本从账号池调度。 |
| 题目 | 默认是「糖果逻辑测试」，也可以选「鹈鹕骑自行车（HTML）」。两者题面均可自定义；HTML 题另外支持随机题库。同页另有 Ciallo，本文不把其独立执行组件算作 HTML 题测。 |
| 测试消息 | 自定义模式下会修剪首尾空白，再根据题型附加输出格式要求；编辑题面不会取消原题型的格式要求。模型收到的不只是文本框里的文字。 |
| 模型 | 可从账号/上游目录选择，也可手工输入 ID。打开时 `anthropic` 账号先选 `claude-opus-5-5`，其他先选 `gpt-6-astra`；目录加载后可能改用列表首项。能输入 ID 不代表上游支持该模型。 |
| 思考强度 | 根据账号和模型查询候选档与默认档。返回空列表时 UI 使用 `none`；查询失败时 UI 回退到低、中、高。表单的选择不保证每种上游协议都会发送这个字段。 |
| 并行数 | 默认 1。前端将数值取整并限制为 1–8，再同时发起 N 个独立的流式请求。`8` 是这个界面单轮的限制，不是题测接口接收的一项并行参数。 |

流程是：**冻结账号、模型、推理档与实发题面 → 每份回答单独 `POST /api/v1/admin/accounts/:id/pelican-test` → 逐个消费 SSE → 保存和比较结果。** 每个请求传 `model_id`、`prompt`、`prompt_mode`、可选的 `pelican_question` 和 `reasoning_effort`；前端还发 `mode: default`，后端题测请求结构不读取它。后端不从这个请求读取 `temperature`、`max_tokens` 或样本数。关闭弹窗会取消浏览器发出的这些请求。手动历史按账号保存在该浏览器的 `localStorage`，写入时最多保留 8 轮，与一轮最多 8 份是两个限制。

随机 HTML 题先从管理接口取一份带题号、版本、轮次、实发题面与评审项的快照，这一步不调用模型。同一浏览器标签页内，同一管理员切换账号时可复用该题；点换题会请求新轮次，不保证抽到不同题。后端按轮次确定题目，提交时重建实际提示词，校验题号及版本，而不是拿客户端改写的题面冒充题库原题。

## 上游有没有输出限制？

| 已核对的请求分支 | 输出 token 参数 | `temperature` | 思考强度 |
| --- | --- | --- | --- |
| OpenAI Responses 常规题测 | 不发送 `max_output_tokens`，由上游默认与模型限制决定 | 不发送 | 已识别的档位通过 `reasoning.effort` 发送；具体是否可用取决于上游 |
| 走 OpenAI 兼容 Chat Completions 的账号 | 该构造不发送 `max_tokens` 或 `max_completion_tokens` | 不发送 | 专用题测有非空、已识别的档位时追加 `reasoning_effort`；不能只查看基础 payload 构造函数就认定未发送 |
| 使用 `createPelicanClaudePayload` 的 Claude Messages 路径 | 固定 `max_tokens: 32000`；思考也可能消耗这个额度 | 基础请求仅在模型不拒绝温度参数时发送 `temperature: 1`，否则省略 | 支持所选档位才设置 `output_config.effort`，支持自适应思考时一并构造 `thinking` |

以上参数在 `ee260eb` 重新核对。Claude 的 `32000` 不是输入框默认值，也不是对所有平台的统一设置；源码明确为思考和完整 HTML 留出共同额度。同一个后端会按账号与协议分流，不能用弹窗是否提供某项设置推断实际请求。另一个细节是，OpenAI Responses 分支接受显式 `none`；这与完全省略 `reasoning.effort` 不同。题测没有专门的提示词字符输入上限，但服务端仍有全局请求体限制，上游也有自己的上下文、输出及限流边界。具体构造见下方当前提交源码定位，不将这些常规文本路径的结论推广到所有平台和媒体协议。

## 输出格式与“成功”的含义

- **糖果题**：提示词要求仅答一个整数；原题参考答案是 **21**。手动题测前端只要求收到服务端成功完成事件且文本非空，不验证它是不是整数或是不是 `21`。答 `20` 仍可能显示生成成功。原版定时测试对未改动的内置糖果题另有答案检查，不应当作手动题测的逻辑。
- **HTML/SVG 题**：提示词要求返回独立 HTML，不用 Markdown 围栏、外部依赖或额外解释。前端提取却会兼容代码围栏和裸 SVG，找不到 HTML/SVG 时标为失败；能提取只说明格式可预览，不代表图形内容正确或动画达标。预览使用沙箱 iframe 和限制资源、连接的 CSP，原始回答仍可查看；文本与作品分别可下载为 `.txt` 或 `.html`。脚本与导航的具体边界见下方展示研究。
- **执行和质量分开**：手动题测给人比较回答，不输出 IQ 分数，也不会因答错就认定账号永久“降智”。例如 Claude 报 `max_tokens` 或拒绝时是生成未完成，不是答错。答错不触发账号状态处置，但原版复用连接测试逻辑，部分上游 HTTP 错误仍可写入账号运行状态；这不是判题动作。

## 历史对照：本插件 UI 4.6.0

本节保留 2026-11-15 的插件实现快照，**不是当前版本的参数或预览规范**。例如思考强度选择、HTML 展示与响应大小计数都可能在后续版本调整；当前交付以 [UI 说明](UI_CN.md)、[变更记录](CHANGELOG_CN.md) 和相关源码为准。

| 项目 | CPA Model Quality 在 UI 4.6.0 时的行为 | 当时与原版的差别 |
| --- | --- | --- |
| 输出上限 | 移除手动输入框。新任务的表单不提交 `max_tokens`；服务端冻结 `min(2048, max_output_tokens)` 供请求与预算使用。 | 两边都不提供该输入框；本插件仍保留全局上限、预算预留与每份响应的大小保护。 |
| `Temperature` | 移除手动输入框，不再把 0 注入新任务。 | 不覆盖新任务的温度；同配置重跑仍使用原先明确设置的值，包括 0。 |
| 回答数量 | 同样默认 1、最多 8；同一任务内逐份执行，各自为独立请求。 | 原版是同轮并行发起，本插件的「回答数量」不是并发度。 |
| 模型 / 思考强度 | 模型栏可键入，但后端要求该模型在所选凭证的宿主模型列表；思考强度默认不显式提交，由宿主的模型/协议能力与配置处理。 | 不等于原版对手输模型直接尝试的行为；页面参数也不是上游最终生效参数。 |
| 题面与预览 | 修改内置题后会按自定义题保存，不沿用内置参考答案。预览剥离脚本。 | 原版修改题面时仍按原题型附加格式说明，其沙箱预览允许内联脚本。两边的 `生成完成` 都不是答对。 |
| 资源和暂停 | 默认还限制每份响应为 256 KiB，并执行全局调用和 token 预算；质量暂停的凭证禁止手动和自动复测。 | 这些是插件的资源保护和凭证准入，不是参考项目的题目设置，不应因界面对齐而删除。 |

## UI 4.6.0 时采用的对齐方式

- **移除整个“输出设置与说明”**，不增加新的说明面板或高级设置入口。宽屏采用主栏题面、右栏模型与两项核心选项；窄屏依次排列。题库本身的输出格式要求原样保留。
- **删除客户端覆盖，不是取消服务端限制。** 新任务的冻结详情仍记录实际提交给宿主的请求预算；宿主或上游的最终参数可能不同。
- **质量规则、凭证限制、证据判定和历史不变。** 不能将截断、上游拒绝或请求错误当成答错。

## 2026-11-21 展示研究：`ee260eb`

### 逐份完成、原文与 HTML 挂载

`consumeRun` 收到 `content` 事件时只追加该份回答的 `run.output`。它继续读取浏览器响应流，直到 EOF；必须已经收到成功的 `test_complete`，且回答非空，才提取 HTML、设置 `run.html` 并将该份状态改为 `success`。HTML 预览不是每来一个片段就重新装载。每份回答独立完成，所以一份可以先展示作品，其他份仍在生成；整轮 `running` 要等 `Promise.all` 中所有请求结束才清除。

这只是生成与提取成功：组件没有等待 iframe 的 `load` 或验证动画内容。前端计时从 `startOne` 发起前到流消费、提取结束，不能当成纯上游耗时。失败时已经收到的原文仍保留；即时卡片的 HTML 在成功提取前为空。卡片下方始终显示原文，只有没有原文时才以错误文字替代。

服务端各协议的完成条件也不能作为本插件的统一完整性规范。例如常规手动 Chat 路径遇到 `[DONE]` 可发送成功，或在已有非空 `finish_reason` 后以 EOF 完成；严格要求 `finish_reason == stop` 的检查属于该函数中的其他专用执行上下文。参考页面的成功标签不能证明所有截断和异常完成状态都已排除。

### HTML 提取与原文保留

`extractPelicanHtml` 先修剪空白，尝试取第一个代码围栏内容，再从最早出现的 `<!doctype html`、`<html` 或 `<svg` 起取正文；若有 `</html>`，去掉最后一个闭标签之后的文字。裸 SVG 被包装成居中、限制最大宽高的 HTML。它在模型文档之前插入 CSP，避免模型原有 `<head>` 之前的内容抢先解析。

该函数不是完整性校验器：不要求 HTML/SVG 闭合，不验证 DOM、脚本运行或图形是否符合题意。提取和包装只影响 `srcdoc` 预览，`run.output` 保留原始文本。即时页面可逐份下载，全部下载逐份创建文件；HTML 下载使用提取后的作品内容，文本题下载原文。

### 卡片、放大、滚动与键盘

| 位置 | 已确认的展示行为 |
| --- | --- |
| 手动即时结果 | 单列卡片，`xl` 宽度下两列；每卡 HTML 预览固定为 4:3，下面是 `max-h-48 overflow-auto` 的原文区域。此卡片有下载按钮，没有独立放大按钮。 |
| 历史入口 | 「历史」打开覆盖在题测弹窗上的 `PelicanRecordsDashboard`，默认限当前账号，也可进入全部账号历史。小卡片预览仍为 4:3；iframe 禁止指针交互，由覆盖整卡的按钮打开详情。 |
| 历史放大 | 点击卡片打开独立大预览层：外层覆盖视口，内容 `h-full`、最大宽度 `max-w-6xl`，头部保留账号、模型、时间和关闭按钮，iframe 占剩余高度。没有缩放倍率、拖拽缩放或 iframe 内容自动测高逻辑。 |
| 滚动 | 账号历史面板与全账号历史根容器有纵向滚动；文本详情独立 `overflow-auto`。HTML 文档仍在 iframe 自己的视口内排版。已读组件没有新增回答时自动滚到底的逻辑；`BaseDialog` 仅在打开时把主体 `scrollTop` 设为 0。 |
| 键盘与焦点 | 历史层打开时聚焦其根容器，关闭时还原先前焦点。其外层 Escape 处理先关大预览，再关账号历史窗口，并阻止事件传到下方题测弹窗。详情打开时未见独立的焦点转移或 Tab 焦点陷阱。 |

键盘结论有明确范围：源码只在外层文档处理 Escape，没有 iframe 内键盘事件桥接。依据浏览器文档事件边界，不能据此承诺焦点进入生成作品后 Escape 仍会到达外层；本次未做浏览器实测。

### 多回答和历史来源

手动一轮先冻结账号、模型、思考档、题型和实发题面，然后并行生成 1–8 份回答。等整轮结束后才写浏览器历史，每账号最多保留最近 8 轮；存储容量不足只忽略保存异常，不使当前结果消失。

历史面板把每份手动回答拆成单独卡片，并与服务端定时结果按时间合并。手动数据来自 `sub2api-pelican-test:<账号ID>` 的浏览器存储，不能描述成服务端持久历史；服务端结果按需读取正文。首批显示 24 卡，加载更多再增加 24 卡，正文并发读取最多 4 份，每 15 秒刷新历史。

历史会对原文重新执行 HTML 提取，即使该份原状态是失败；只要能提取出 HTML，卡片和大预览仍可显示。因此历史出现作品不等于该次生成完整成功，更不能作为质量处置依据。这个行为与即时失败时尚未设置 `run.html` 的路径不同。

### 预览沙箱差异与已核实边界

两个展示组件都使用 `sandbox="allow-scripts"` 和 `referrerpolicy="no-referrer"`，没有授予 `allow-same-origin`、表单、弹窗或顶层导航权限。注入的 CSP 允许内联脚本和样式，图片、媒体只允许 `data:`/`blob:`，字体只允许 `data:`；`connect-src`、`frame-src`、`object-src`、`base-uri`、`form-action` 均为 `'none'`。这不是删除脚本的净化策略，与上述插件 UI 4.6.0 的脚本剥离实现不同。

参考工具还提供了读取父页 nonce、清除模型 nonce 并只给无外部地址的内联脚本重新授权的辅助函数；本次读取的 `IQTestModal` 和 `PelicanRecordsDashboard` 只调用提取函数，没有调用这两个辅助函数。源码中的 `allow-scripts` 和内联 CSP 说明预览意图允许脚本，实际是否执行仍受父页 CSP 等浏览器条件约束，不能仅凭这些属性宣称所有 JS 动画都可运行。

提取器没有删除链接、meta refresh 或其他导航属性，也不限制脚本执行时间。本次确认的是上述沙箱属性与 CSP 指令，未证明所有浏览器导航、下载或资源耗尽场景均被阻止；不把源码注释中的“不能联网”扩展成已验证的完整安全保证。对照展示方式也不意味着应降低本插件的响应完整性、历史暂停或处置证据要求。

## 当前参考提交源码定位

以下链接固定在 `ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af`，不会随默认分支更新而漂移。

| 证据 | GitHub 源码 |
| --- | --- |
| 手动即时卡片、原文滚动、iframe 与下载按钮 | [IQTestModal.vue:239–269](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/admin/account/IQTestModal.vue#L239-L269) |
| 并行数与浏览器历史限制 | [IQTestModal.vue:524–547](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/admin/account/IQTestModal.vue#L524-L547) |
| SSE、逐份完成、计时与整轮冻结保存 | [IQTestModal.vue:617–732](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/admin/account/IQTestModal.vue#L617-L732) |
| HTML 提取、CSP 与 nonce 辅助函数 | [pelicanHtml.ts:1–44](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/utils/pelicanHtml.ts#L1-L44) |
| 历史卡片、覆盖按钮与大预览 | [PelicanRecordsDashboard.vue:1–65](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/admin/account/PelicanRecordsDashboard.vue#L1-L65) |
| 历史重提取、来源合并、按需加载和键盘边界 | [PelicanRecordsDashboard.vue:163–292](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/admin/account/PelicanRecordsDashboard.vue#L163-L292) |
| 外层弹窗的滚动复位与焦点处理 | [BaseDialog.vue:139–181](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/frontend/src/components/common/BaseDialog.vue#L139-L181) |
| 题测请求结构与独立入口 | [account_handler.go:1432–1544](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/handler/admin/account_handler.go#L1432-L1544) |
| Claude 32000、思考参数与 Responses 题面 | [pelican_test_service.go:12–16](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/pelican_test_service.go#L12-L16)、[88–153](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/pelican_test_service.go#L88-L153) |
| Claude 温度参数按模型能力省略 | [account_test_service.go:367–373](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/account_test_service.go#L367-L373) |
| Chat 题测追加思考档、OpenAI 基础 payload | [account_test_service.go:2478–2485](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/account_test_service.go#L2478-L2485)、[3166–3209](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/account_test_service.go#L3166-L3209) |
| 常规 Chat 与专用测试上下文的完成条件差异 | [account_test_service.go:3300–3388](https://github.com/ranxi2001/sub2api/blob/ee260eb01c83d30bfe1e95dc62b2fb4f23e1c4af/backend/internal/service/account_test_service.go#L3300-L3388) |

## 早期核对定位

以下保留早期参考提交 `ccf997c73868083d96c0bec56e71c1a34d26c98a` 的仓库相对路径和行号，只用于追溯 2026-11-15 的核对，不适用于上面的最新提交，也不表示当前工作区仍存在该参考 checkout：

| 证据 | 路径 |
| --- | --- |
| 表单、并行钳制、SSE 请求、同轮快照 | `frontend/src/components/admin/account/IQTestModal.vue:125`、`:519`、`:610`、`:684` |
| 题面/格式、随机轮次、HTML 提取和预览 CSP | `frontend/src/utils/intelligenceTest.ts:3`、`frontend/src/utils/pelicanQuestionRound.ts:5`、`frontend/src/utils/pelicanHtml.ts:23` |
| 管理路由、题测请求结构与处理 | `backend/internal/server/routes/admin.go:523`、`backend/internal/handler/admin/account_handler.go:1385`、`:1451` |
| 模型推理能力、随机题库、定时测试规则 | `backend/internal/service/account_test_reasoning.go:15`、`backend/internal/service/pelican_questions.go:34`、`backend/internal/service/pelican_scheduled.go:519` |
| OpenAI / Claude 参数 | `backend/internal/service/pelican_test_service.go:88`、`:141`、`backend/internal/service/account_test_service.go:332`、`:3129`、`:3157` |

本插件相关源码：[`index.html`](../plugin/ui/index.html) 手动表单；[`app.js`](../plugin/ui/app.js) 请求封装；[`admission.go`](../plugin/quality/admission.go) 手动默认；[`types.go`](../plugin/quality/types.go) 全局默认；[`execution.go`](../plugin/quality/execution.go) 样本执行与预算；[`host.go`](../plugin/quality/host.go) 宿主请求；[`view.js`](../plugin/ui/view.js) 预览。
