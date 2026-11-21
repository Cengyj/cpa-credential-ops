> 官方插件新增 `GET /connection`、`PUT /connection` 和 `DELETE /connection`，前缀均为 `/v0/management/plugins/model-quality`。请求字段、配置修订、回执及错误码见[后台连接和恢复](BACKGROUND_CONNECTION_CN.md)。
>
> 当前官方 CPA：`GET /v0/management/plugins/model-quality/rules/question-preview?id=logic-candy` 在管理鉴权后只读返回固定题的 ID、版本、题面和参考答案；未知、随机、自定义或视觉题返回 404。一般 `/questions` 列表仍不含参考答案。规则范围、Cron、保留条数及官方能力限制以[质量观察说明](QUALITY_SCOPE_CN.md)为准；下文 v8 业务路由表是早期补丁宿主的历史记录。
>
> 官方接口 `GET /v0/management/plugins/model-quality/runs?view=summary` 按运行 ID 返回摘要。同一对象的不同轮次各占一条。`config.subject_auth_index` 和 `config.provider_name` 分别按当轮保存的值返回。准入跳过的原因在 `skip_detail` 中，目录预检见 `preflight_detail`，已申请取消的轮次返回 `cancel_requested: true`。
>
> 手动提交 `POST /v0/management/plugins/model-quality/runs` 可附 `expected_object_type`（`oauth` 或 `provider`）及 `expected_auth_index`。插件用服务端读到的 CPA 对象核对这些前置条件，类型或索引变化返回 409。旧客户端可省略这两个字段。同一幂等键和相同请求可再次取得原运行回执。规则用自己的固定对象绑定。
>
> 新建或明确改选固定规则时，可在规则保存或范围预览请求提供 `expected_target_object_type`，并提供 `auth_id`、`provider` 和 `target_auth_index`。来源或定位发生变化返回 409。服务器在读取当前目录后记录已核实的 `target_object_type`。老客户端省略预期来源时，以服务端受理时的目录为准。
>
> 摘要仅投影已记录的三种 `samples[].evidence.path` 值，即 `provider_config_probe`、`provider_native_probe` 和 `cpa_management_probe`。未保存或未识别的路径省略。样本执行状态、错误与答案判定分别保留。完整冻结配置和原始样本见鉴权接口 `GET /v0/management/plugins/model-quality/runs/detail?id=运行ID`。
>
> 0.15.0 新增鉴权账号安全操作和本地完成接口，见 [重置 2FA 与退出设备](OPENAI_ACCOUNT_SECURITY_CN.md)。

> 2026-11-17：官方兼容版已改为独立插件，详见 [官方单插件方案](OFFICIAL_PLUGIN_CN.md)。本文保留补丁宿主的历史实现/部署记录；本机 8317 已按用户确认更新为官方 CPA + 插件 0.14.0，详见 [本机升级记录](LOCAL_OFFICIAL_UPGRADE_CN.md)。

# 0.3.0 鉴权 API 与存储

业务前缀为 `/v8/management/plugins/model-quality`。公共 `/v0/resource/plugins/model-quality/{index.html,app.js,client.js,view.js,style.css}` 只返回静态 GET，无模型调用和状态变更。业务路由精确匹配，管理鉴权与 remote-access 策略由宿主执行；管理密钥仅存在页面内存，不保存到插件或回环请求。

## 当前规则

质量暂停后，主体、control、judge 三种角色都不能执行模型请求；自动或手动复测均不支持。旧复测/自动恢复配置不再生效。人工释放完成后，相关规则保持停止：没有剩余质量限制时为 paused，否则继续 awaiting_release。必须显式恢复规则才能产生新的检测。

## 路由

| 方法 | 路径 | 内容 |
|---|---|---|
| GET | /targets | 脱敏目标、身份、模型/reasoning、人工禁用/质量暂停/有效状态 |
| GET | /questions | 题目列表，不含参考答案 |
| GET / POST / DELETE | /presets | 保存/加载/删除配置，不执行 |
| GET / POST | /runs | 历史 / 异步提交；暂停依赖在入队前拒绝 |
| GET | /runs/detail?id= | 冻结配置、样本、control 样本和独立状态 |
| POST | /runs/cancel | `{id}`，幂等取消 |
| POST | /runs/review | `{id,sample_index,verdict,note}`，人工评审 |
| GET / POST / PUT / DELETE | /rules | 规则 CRUD；修改/删除检查 revision |
| POST | /rules/run | `{id}`，仅未暂停且版本有效的规则可运行 |
| POST | /rules/pause | `{id,revision,paused}`，停止/显式恢复检测规则 |
| GET | /actions | 宿主权威暂停动作 |
| POST | /actions/preview | `{rule_id,action_id,action_revision}`，人工释放预览 |
| POST | /actions/release | 相同动作绑定，人工 CAS 释放；始终停住相关规则 |
| GET | /history | 运维审计 |
| GET | /metrics | 质量率、覆盖率、来源尝试、用量、延迟与预算 |
| GET / PUT | /settings | 当前配置与更新 |

分页为 offset（默认0）、limit（默认50，最大100），返回 `{items,total,offset,limit}`。GET /runs 支持 source=manual_test或scheduled_probe，其他值返回400；相同时间以ID稳定排序，先分页后复制输出。创建运行返回202与 `{run_id}`，再鉴权轮询；不是 SSE。JSON body 最大128KiB，拒绝重复字段和未知字段。

错误结构 `{error:{code,message}}`；参数错误400，not_found为404，版本/幂等键冲突409，暂停状态冲突409，队列/预算429，不可用503，存储准入超限507，不支持能力422，幂等结果已过保留期410。对暂停凭证的新调用不能返回202后再消耗额度。

`pause_rule` 仅作为旧客户端兼容输入；false 不会继续启用检测。当前 UI 不提供“解除后继续自动检测”选项。

若人工释放已在宿主完成、随后剩余限制查询失败，返回503并明确“owned pause released; remaining restrictions await reconciliation”。此时相关规则继续停止，保留待对账标记；刷新动作和目标状态确认结果，不把错误响应解释为宿主必然未释放。

## 运行和规则配置

```json
{
  "auth_id":"offline-a", "provider":"offline", "models":["real-model"],
  "question_id":"logic-boxes", "samples":1,
  "parameters":{"max_tokens":1024,"stream":true},
  "judge":{"mode":"deterministic"}, "idempotency_key":"operator-job-001"
}
```

每模型 samples为1..8，总数还受 max_samples 限制；模型数、temperature、reasoning_effort、输出预算均服务端检查。custom 题可提供 prompt、review_standard，二者共同冻结；不得改内建题面后继承原答案。视觉/自定义题默认没有确定性 oracle。

直接提交人工评阅任务（`judge.mode=manual` 或不指定 `judge.mode`）时，若 `parameters.max_tokens` 缺省或为零，服务端冻结 `min(2048, max_output_tokens)`；非手动模式原有默认值 1024 不变。省略 `temperature` 不等于传入 0。显式 token 预算超出上限仍会被拒绝；规则、旧记录和显式同配置重跑不改写已有参数。

原生模型流式回调按解码后的 UTF-8 正文字节执行 `max_response_bytes` 限制。每个响应帧和整条流另设有限的接收额度，覆盖 JSON 包装及 SSE 元数据。回答超限、响应帧超限、累计传输超限均保留此前收到的正文并报告各自原因。上游明确返回 `finish_reason=length` 时，样本状态为 `incomplete`。

可选 control_auth_id/control_provider 与 seed 用于随机题有效对照。judge mode为 deterministic/manual/llm；LLM 需要独立 auth_id/provider/model。任何依赖被质量暂停都会阻止整个运行。答案仅提供给独立 judge，不发送给被测模型。

规则增加 name、action（observe_only或pause_credential）、enabled、scheduling_enabled、automatic_pause、interval_seconds或cron、timezone、fail_threshold、retention_days。默认失败门槛3，范围1..100；间隔60..2678400秒；保留1..365天。规则 status为 detecting/paused/awaiting_release，由服务端状态导出，不能由客户端修改来绕过限制。

官方宿主插件 0.25.0 的观察规则表单使用 `activation`：`keep` 保留既有授权（新建时仅手动），`start` 为本条规则授权定时检测，`stop` 撤销其定时检测。它与规则字段在同一次存储事务中提交，不修改实例级定时许可。响应中的 `timer_active` 表示该规则当前是否获准定时运行，并非是否存在匹配凭证。旧客户端不传 `activation` 时仍沿用旧字段。显式调用原实例级停止接口会一并撤销逐条定时授权。

automatic_recovery、pass_threshold、pass_count、retest_seconds 不再是新 API 的可配置能力。旧数据库/旧宿主 YAML 在迁移时忽略其运行意义；新 JSON 设置不接受这些已移除参数。action_id、归属、身份与计数由服务端维护，编辑仍保留既有动作归属并执行 revision 检查。

全局可配置开关仅 scheduling_enabled与automatic_pause。workers、queue、data_dir 修改需要宿主重配。各资源字段范围以 types.go为准；示例在 artifacts/config。

## 存储与迁移

宿主 auth-dir/quality-state.db保存独立身份、活动暂停和已释放墓碑，插件卸载后继续生效。插件 data_dir/quality.db为schema2的bbolt，保存meta/runs/rules/audit/usage/idempotency/presets。新目录0700、文件0600。

schema1升级保留历史与动作，不删除数据库，不发起复测，不自动释放。旧queued/running标记interrupted。已暂停主体及相关control/judge依赖规则停止；schema2规则不能因旧恢复开关、历史通过结果或到期计时器自动恢复。

跨库使用持久意图与host action_status对账；释放墓碑不会被当成“尚未暂停”而重放。人工释放和对账都不会恢复规则启用。UTC每日预算计入主体/control/judge调用，未知usage单独显示；被动生产仅保存脱敏事件，原始Source、凭证和生产全文不落盘。

审计或被动指标数据损坏/读取失败时返回503，不再伪装成空历史或零指标。缺失或非法的历史流量来源归入unknown，不能自动归入production。

每日预算按调用准入时的UTC日期记账，跨日完成仍更新原日，meta内budget:YYYY-MM-DD保留配置天数并保护在途日期。主动任务缺usage仍显示未知；被动ABI缺少零用量存在性标记，因此无法证实的0和旧事件未标记tokens_known时按unknown_usage统计，不伪造零成本。


## OpenAI 登录（0.4.0）

新增宿主鉴权前缀 `/v8/management/credential-logins`，不经过插件 Management RPC 传递秘密。接口包含 capabilities、targets、start、jobs、detail、candidate、callback、cancel、discard、commit 和 vault/status、vault/entry。写请求必须包含 Idempotency-Key；完整字段和限制见 [OPENAI_2FA_IMPLEMENTATION_CN.md](OPENAI_2FA_IMPLEMENTATION_CN.md)。公开静态 GET 新增 `login.js`，不承载业务操作。


## OpenAI 账号管理（0.5.0）

新增受保护的 `/v8/management/credential-logins/accounts` 列表及 `/accounts/import`、`/accounts/entry`、`/accounts/start`、`/accounts/inspect`、`/accounts/settings`。账号列表返回完整邮箱和身份字段，受鉴权保护的详情接口提供已保存的明文资料；所有写入使用原有幂等日志，修改和删除采用 revision。完整字段、请求限制、巡检证据和自动重登条件见 [账号管理与巡检](OPENAI_ACCOUNTS_CN.md)。

UI 8.4.0 对应宿主增加登录代理字段：`PUT /accounts/settings` 的 `login_proxy_url` 设置默认代理；`POST /accounts/import` 的 `proxy_url` 应用于本批账号；`PUT /accounts/entry` 的 `proxy_url` 修改账号覆盖；`POST /start` 的 `proxy_url` 仅用于本次临时登录。更新请求省略代理字段保留原配置，传空字符串恢复继承，`direct` 明确直连。账号响应包含 `proxy_url`、`effective_proxy_url` 和 `proxy_source`；最新失败任务以 `last_job_error_code`、`last_job_error_stage`、`last_job_error_reason` 展示，任务本身提供 `error_stage` 和 `error_reason`。诊断值使用有限枚举，不返回网页内容或回调秘密。代理优先级和支持范围见 [登录代理说明](OPENAI_LOGIN_PROXY_CN.md)。


## OpenAI完整账号视图（0.6.0）

新增受管理鉴权的 `GET /v8/management/credential-logins/accounts/detail?id=...`，返回 `{account,credentials:{email,password,totp_secret}}`，用于管理员明确打开账号资料。账号、任务、目标、候选和资料库状态新增完整 `email`；目标与候选提供完整 `account_id/user_id`。旧 masked 字段仅为兼容，新UI不使用。普通列表不携带密码/TOTP，任务报告带完整邮箱和身份信息。身份不能可信还原的历史任务返回空值。详情不缓存，也不落入幂等或普通任务记录。

TOTP 在导入、资料更新及登录请求的共享输入边界验证：支持有效 Base32 或 SHA-1／6 位／30 秒的 `otpauth://totp`，非法格式返回 `invalid_credentials`；错误导入不写入任何账号，错误更新保留旧资料。原始密钥文本和密码空白不因校验改变。执行边界改进见 [toSub2 Go 逻辑对照](TOSUB2_GO_REFERENCE_CN.md)。
