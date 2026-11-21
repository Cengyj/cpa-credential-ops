# 质量运维 0.30.0 实现与交付记录

日期：2026-11-21（Asia/Shanghai）。插件 0.30.0，UI 13.7.0，内嵌 Go/Sobek 2.2.1；官方 CPA 8.0.76。

本报告所列日志与 JSON 证据均在 `artifacts/quality-operations-0.30.0/`，入口见 [验证索引](../artifacts/quality-operations-0.30.0/verification-index.json)，安装包见 [完整 arm64 ZIP](../artifacts/quality-operations-0.30.0/full-plugin/model-quality-v0.30.0-linux-arm64.zip)。

本轮依据已确认的质量运维目标执行，保留工作区原有修改。答题质量处置、上游错误来源、当前 CPA 状态、答错恢复归属与本轮写入条件分别判断。普通停用不会停止规则，也不会在管理员启用后要求再次确认。历史独立质量暂停和未知写入保护继续保留。

## 实现结果

| 问题 | 当前行为 | 主要位置 |
| --- | --- | --- |
| 无标准答案阻止配置上游错误处理 | 自定义、视觉及人工评审题可单独设置错误策略；答错写入仍要求受支持的确定性判定 | `plugin/quality/rules.go`、`error_only_questions_test.go`、`plugin/ui/app.js` |
| 错误停用后永久排除，CPA 启用仍不可测试 | 刷新、手动提交、派发和执行复查读取当前 CPA；确认已启用后撤销旧普通归属、旧任务与计数，按当前范围重新选择 | `error_policy_disabled.go`、`admission.go`、`scope.go`、`transitions.go` |
| 普通停用连带关闭规则 | 取消受影响任务并按状态跳过；规则启用和定时许可保持。管理员已停止或无法证明停止来源的旧规则仍停止 | `credential_policy.go`、`rules.go`、`current_state_test.go` |
| 检测权限混同恢复、删除权限 | 全部范围可只读检测本规则或他规则答错停用对象；他规则不取得恢复或删除权限 | `scope_plan.go`、`credential_policy.go` |
| B 规则错误覆盖 A 规则答错归属 | 独立保存 A 的答错归属及 B 的触发规则、版本、运行、模型、条件和证据；已停用时不重复写 CPA；所有规则停止答题和恢复 | `credential_policy.go`、`queries.go`、`provider_disposition.go` |
| 凭证内容摘要被用作长期恢复身份 | 可确认同一条目时，轮间 token/账号内容变化不丢失归属；新轮冻结新快照，本轮变更使旧证据失效 | `management_client.go`、`credential_entry_lifecycle_test.go` |
| 供应商组成员及来源判断不一致 | 整组写入要求显式组授权、当前完整成员计划和全通过；未知组写入保护所有入口；成员换 ID 不猜测恢复继承 | `provider_disposition.go`、`provider_entry_lifecycle_test.go`、`manual_group_guard_test.go` |
| 临时目录遗漏被误认为凭证删除 | 文件删除需精确 CPA 文件 404；供应商旧 ID 消失但当前同组仍停用/未核实时保留旧错误来源；当前整组确认启用后旧来源不复活 | `error_policy_disabled.go`、`current_state_test.go` |
| 删除规则丢失有效错误来源 | 删除规则不启用对象；仍有效的错误来源和必要证据保留，重启后继续退出答题；已观察到启用或消失的记录不再生效 | `rules.go`、`rule_delete_handling_test.go` |
| 页面缓存与合法供应商状态不一致 | `active` 和合法供应商 `registered` 对齐；完整选择可先刷新当前状态，刷新后再决定能否提交；切换连接时终止旧提交 | `plugin/ui/app.js`、`view.js` |

普通智商测试仍不执行规则的停用、删除或恢复。质量运维“立即执行”使用当前规则策略。网络异常、拒答、截断、取消、缺项和未知不计为答错；整轮完整且全部可判定才允许答错处置。明确上游错误可提前处理。目录缺模型仍对整个对象生成目录结果，本轮不继续探测其他模型。

## 回归与原生验证

测试使用隔离假 CPA、假凭证、假上游；原生集成容器使用 `--network none`。真实账号模型调用、登录及测试性启停均为 0。

| 验收范围 | 证据与结果 |
| --- | --- |
| 插件、账号、登录与运行时兼容 | 完整 Docker 构建执行运行时 `go test ./...`、插件内嵌版本 `go test -tags loginbundle_embedded ./...`、`go vet`、原生运行时自检及 `c-shared` 构建，均通过 |
| 并发与生命周期 | `race.log` 覆盖 quality/login/cmd；最终后端 `race-final2-quality.log` 再验证质量模块，均通过 |
| 两条策略、跨规则与轮间快照 | `final-operations-native.json`：17 个阶段、40 次假模型调用、7 次预期插件写入；B 401 升级后 A/B 与重启均零调用，已停用对象零重复写入；账号内容替换与未观察到的启用再停用均按新完整轮次恢复 |
| 直接手动重新准入、删除规则与重启 | `final-readmission-native.json`：CPA 启用后不先刷新即可提交；新证据可再次停用；删除规则保留有效错误来源和证据；共 2 次假模型调用，停用期间手动调用为 0 |
| 普通答错、全部样本、异常、删除与重启 | `final-policy-native.json`：128 次假调用，完整恢复、拒答/截断/异常不写入、人工停用边界、精确删除、动态新增及重启不重放通过 |
| 单供应商路由 | `final-route-native.json`：只停用目标路由，另一路由保持可用；停用期间定向探测，全通过后一次条件配置写入恢复原排除模型继承，加载和重启通过 |
| 整供应商组 | 最终包 `final-provider-native.json`：9 项整组授权、完整通过、全部范围、412 不重试、外部启用、新证据再次停用与重启检查通过；6 次预期条件写入，凭证文件及无关配置保持；动态投影的 `auth_index` 单独核实，不混入持久配置比较 |
| 定时、错误排除与 CPA 启用 | `final-suspensions-native.json`：六种错误来源在普通执行、真实 Cron、其他范围及重启后持续退出答题；已启用对象重新纳入后续当前范围，健康对象继续执行 |
| 未知写入 | 最终包 `final-uncertain-native.json`：CPA 已完成 PATCH 但回复中断，连续两次启动仍保留未知状态，总 PATCH 仅 1 次、不重放 |
| 界面 | 题型、错误策略、手动供应商、实时状态、对象类型、范围、部分读取失败和回执测试通过；`ui/receipts/receipts.json` 验证缓存停用/模型/思考能力丢失时先刷新，仍停用或档位不支持零 POST，能力恢复仅一次提交；供应商三主题三宽度回归通过，两张状态截图已目视核对 |
| 部署与保全工具 | `deployment-script-tests.json`：24 项隔离回归通过，包括真实逻辑变化拒绝、已确认 KV/账号数据库的空事务、窄范围空定时变化、挂载错误、失败恢复原库及保留业务数据 |

新增 Go 行为回归覆盖：`TestErrorOnlyQuestionsExecuteWithoutStandardAnswers`、`TestAnswerHandlingStillRequiresSupportedAutomaticJudgment`、`TestExternalEnableCancelsStaleRecoveryWithoutChangingRulePermissions`、`TestRefreshPreservesAdministratorStoppedRuleAndReadFailure`、`TestAnswerOwnershipUsesCurrentEntryContentEachRound`、`TestOwnedRecoveryRejectsMutationInsideRoundButKeepsOwnership`、`TestCrossRuleErrorSourceRetainsAnswerOwnerWithoutRepeatedCPAWrite`、`TestOtherRuleAllScopeCannotDeleteAnswerOwnedDisabledEntry`、`TestProviderRecoveryUsesNewCompleteGroupSnapshot`、`TestProviderGroupExternalEnableReconciliationIsAtomic`、`TestProviderAllKeysRotatedRetainsErrorExclusionUntilCurrentGroupEnabled`、`TestManualProviderRespectsUncertainGroupWrite`、`TestTransientRuntimeOmissionPreservesFileErrorExclusion`、`TestObservedRemovalDoesNotGrantRecreatedEntryOldRecoveryAuthority`。

## 构建与本地安装

运行实例为 Docker `cli-proxy-api`，官方 CPA 8.0.76，Linux arm64。只构建此实例所需架构，使用完整 `plugin-artifacts` 目标，保留账号管理及内嵌 Go/Sobek。未构建额外平台，未推送、发布 GitHub 或改动商店条目。

最终安装包位于 `artifacts/quality-operations-0.30.0/full-plugin/`，构建日志为 `build-delivery.log`，库 SHA-256 为 `a82f6cbcb7144db2ea83756260f4d4386cb717f543d9e27768982f357229b079`。最终上下文中的 318 份插件/运行时源码与当前源码一致。较早完成的后端原生套件针对 `final2-linux_arm64`；`backend-source-parity.json` 证明该包与交付包的构建源仅 `plugin/ui/app.js` 不同，后端及运行时完全一致。最终包重新通过完整构建测试，并执行最终整组加载/重启与未知写入验证。

官方参考源码和 `workspaces/CLIProxyAPI-official/` 均按新增 `host-patches/official-8.0.76-baseline.json` 核验：2,413 份源码/构建文件完全一致。原 8.0.67 基线保留；并未把旧基线当作本次 8.0.76。官方宿主编译通过，源码未编辑或格式化。`scripts/prepare_official_context.py` 只复制校验过的宿主源码、插件与运行时源码，不复制凭证、配置、日志和数据库。

**本地更新已完成，最终核验时间为 2026-11-21 05:13:09 +08:00。** [部署报告](../artifacts/quality-operations-0.30.0/deployment-final.json) 的状态为 `passed`。仅替换 `model-quality.so` 并启动原 CPA 容器，原宿主镜像、挂载和命令保持。停服备份在 `/Users/ceng/.local/share/cpa-quality-backups/plugin-0.30.0-kp7wnmn5/`，逐文件 SHA-256 验证通过。

- 配置和凭证文件完全一致；凭证目录 18 份文件中的 5 份 JSON 保持原字节。唯一专门处理的宿主 `kv.db` 由实际配置确认，完整逻辑摘要一致。
- 原有 195 份插件数据文件全部保留，无新增文件、非数据库文件改写或数据恢复。账号、登录回执、质量状态三个数据库的桶、键、值和序列完整一致；此次最终校验窗口的空派发例外次数为 0。
- 实际服务的 `index.html`、`app.js`、`client.js`、`view.js`、`login.js`、`style.css` 全部与最终构建源 SHA-256 一致，缓存头均为 `no-store`；管理面板返回 200，未鉴权插件设置接口返回 401。
- 部署验证的管理鉴权请求、模型调用和账号操作均为 0。未启动真实登录，未测试性启停或修改真实凭证。

`scripts/update_local_plugin.py` 保留停止备份、路径/架构验证和仅库回滚；`scripts/inspect_private_store.go` 只读取工作区外的私有稳定副本，输出脱敏摘要。已确认的 bbolt 数据库按逻辑内容核对；质量库仅允许既有且有定时授权规则的下次执行时间变化和零匹配/零跳过审计追加，其余记录、权限和内容严格保留。任何失败只回滚旧插件库，不回滚业务数据。

首轮校验因将凭证目录中的 CPA `kv.db` 当作静态凭证文件而保护性回滚，原容器恢复运行。只读复核确认配置和 5 份凭证 JSON 完全一致，CPA KV、账号和登录回执数据库逻辑内容未变。原有定时规则仍按既有许可运行，质量库仅更新下次执行时间、追加空派发审计，没有新增模型运行或删除历史；这些变化另行核对，不称整库字节不变。原始报告为 `deployment.json`，复核为 `deployment-first-review.json`、`deployment-quality-diff.json`。校验脚本据此区分明确已知的数据库与凭证文件，并保留严格的配置、权限和既有记录检查。

## 公开接口限制

1. **供应商的任意换密钥不能保证恢复归属连续。** CPA `StableIDGenerator.Next` 及 `auth_index` 由可变内容派生，配置写入会剥离外部提供的 `auth_index`/`auth-index`（`internal/api/handlers/management/config_auth_index.go`，对应 `config_v8_auth_index_test.go`）；公开 SDK 没有条目 incarnation/UUID。对可确认同一 locator 的条目保留归属，无法唯一对应时保留旧记录，不以名称、位置或配置摘要猜测归属，不阻止另一个当前已启用对象。整组全部换钥匙但仍停用时，旧组错误来源不会被当成普通人工停用后恢复。
2. **完全未观察到的同路径删除再创建无法与同条目内容替换区分。** 已观察到的精确文件删除结束旧有效归属；仅修改时间或内容摘要不能证明删除或启用历史。
3. **CPA 文件状态与删除 API 无原子条件写入。** 插件采用本轮快照、写前复查和写后验证，但不能消除外部写入夹在复查与 mutation 之间的窗口。供应商配置使用宿主支持的条件 PUT；未知结果不自动重试。
4. **能力边界不伪装为质量错误。** 不支持的供应商删除、协议、思考档位和模型映射准确拒绝。管理探测不能证明完整业务最终负载；官方 SDK 不支持的独立质量暂停与释放仍不提供。历史独立暂停不会被普通 CPA 启用静默清除。
5. **历史规则不批量启动。** 无法证明停止由插件引起、且其后没有人工停止的记录保持停止。管理授权仅驻留后端进程内存，重启后需要正常面板连接供后续运维使用。

产品逻辑见 [逻辑与验收](QUALITY_OPERATIONS_LOGIC_CN.md)，源码关系见 [架构与能力](QUALITY_OPERATIONS_ARCHITECTURE_CN.md)，使用说明见 [检测范围](QUALITY_SCOPE_CN.md)、[上游错误](QUALITY_UPSTREAM_ERRORS_CN.md)、[答错处理](QUALITY_CREDENTIAL_ACTIONS_CN.md) 和 [恢复流程](QUALITY_RECOVERY_FLOWS_CN.md)。
