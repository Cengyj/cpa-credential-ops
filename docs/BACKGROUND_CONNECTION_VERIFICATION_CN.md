# 后台连接与启动恢复验收

下文保留 0.31.0 的原始记录。0.32.0 完整包实测了重启、无页面补检和本机部署；0.32.2 单独复核了并发失败、磁盘损坏、账号诊断与隔离宿主恢复。[完整包验收](SERVICE_VERIFICATION_0_32_CN.md) · [0.32.2 复核](COMMERCIAL_REVIEW_0_32_2_CN.md)

2026-11-21，在官方 CPA 8.0.76 上验证完整 Credential Ops 0.31.0、UI 13.8.0 和内嵌 Go/Sobek 2.2.1。使用临时账号库、假上游、假密钥和隔离容器。页面和后台连接的接口、存储和权限见[连接架构](BACKGROUND_CONNECTION_CN.md)，重新授权的收据边界见[条目与规则联动](CREDENTIAL_REAUTH_RULES_CN.md)。

## 回归与观测

| 修改前复现的问题 | 实测结果 |
| --- | --- |
| 账号和设置 GET 能把浏览器的密钥带入后台，重启靠重新打开页面取得授权 | 12 个并发迁移只保存 1 次，11 个得到冲突。之后 30 个状态 GET 产生 0 次回环探针。无浏览器重启从原目录取得授权。[后台连接测试](../artifacts/backend-connection-0.31.0/connection-tests.log) |
| 界面用连接状态兼任数据新鲜度，切页和失败回执容易触发重复初始化 | 已保存密钥的 4 次 iframe 初始化各读 1 次账号列表。总计 5 次连接状态 GET、1 次迁移 PUT、1 次明确清除 DELETE。页面隐藏 65 秒时展示请求为 0。新标签的变更优先于旧响应。[33 个浏览器场景](../artifacts/connection-ui-0.31.0/account-navigation.json) |
| 普通配置变化触发实例替换，旧配置摘要变化会清空逐规则许可 | 创建后一次预算热更新，实例启动 1 次、关闭 0 次。轮换管理密钥并重开质量库后，原规则归属和许可仍在，2 个新样本通过后只恢复 1 次。[热更新测试](../artifacts/backend-connection-0.31.0/connection-tests.log) · [轮换与归属](../artifacts/backend-connection-0.31.0/scheduler-rotation-race.log) |
| 旧调度在管理接口缺席时提前推进下一到期时间或生成空记录 | 假时钟停机 5 小时 3 分钟后产生 1 个补检批次，使用当前 2 个对象做 4 次探测。重复就绪、已有轮次、暂停、删除、修改、同刻正常到期均有回归。停机后才新开的全局许可不会授权补做此前的轮次。[调度竞态回归](../artifacts/backend-connection-0.31.0/scheduler-recovery-race.log) |
| 凭证保存前永久停掉依赖规则，或在临时读取失败时误判绑定变化 | 手动重登、自动重登和确认收据落盘后重启三个场景，均为首次保存 1 次、重新授权保存 1 次、解除等待 1 次。成功回调模型请求为 0，后续新轮次按新快照检查 2 个模型。缺失或矛盾的收据仍隔离。[任务和规则回归](../artifacts/backend-connection-0.31.0/targeted-results.log) |

单一后台恢复任务在超时、503、429、403 和成功构成的序列中共发起 5 次探针，关闭后新增请求为 0。失效的当前密钥停止使用；旧请求返回 401、上游模型返回 401、存储损坏和持久化失败均独立验证。另一组测试重开账号库后自动完成 1 次逾期巡检，浏览器读取、回环请求和登录任务均为 0。[连接和巡检记录](../artifacts/backend-connection-0.31.0/connection-tests.log)

最后复核还重现三个边界。合法的 8,192 字节假密钥经 JSON 转义后会超过旧读取上限。已启动的同密钥验证可能用迟到的成功覆盖更新的失效判定。并发 429 还可能推迟处理同一修订的健康探针 401。磁盘读写现在共用容量边界，失效立即推进修订，同一修订的确定失效优先于退避。三个场景各通过 10 轮竞态检查。[容量红测](../artifacts/backend-connection-0.31.0/key-length-red.log) · [迟到成功红测](../artifacts/backend-connection-0.31.0/key-invalidation-red.log) · [并发退避红测](../artifacts/backend-connection-0.31.0/health-denial-red.log) · [修复后结果](../artifacts/backend-connection-0.31.0/connection-edges-accepted.log)

即使把数据目录置于源码树内，Git、Docker 构建上下文和发布源码清单都会排除后台密钥记录、同目录的临时密钥文件及默认数据目录。以临时合成文件验证三种入口，连同安装器回归共 34 项通过。[源码过滤测试](../scripts/tests/test_private_source_inventory.py) · [构建与安装辅助工具回归](../artifacts/backend-connection-0.31.0/release-tools-final.log)

## 完整插件与既有功能

最终动态库、可安装 ZIP、嵌入的六份 UI 资源与构建清单逐项匹配。用于编译的 2,757 个文件与工作区源码匹配。插件全量 Go 测试通过 433 个顶层测试（含子用例为 1,239 个），全量竞态检查通过。另一个默认跳过的历史 JavaScript TOTP 校验器对照用例，在临时目录放入原校验器并显式指定 Node 后单独通过。发行插件运行内嵌 Go/Sobek。[源码与包摘要](../artifacts/backend-connection-0.31.0/delivery/source-integrity.json) · [Go 测试事件](../artifacts/backend-connection-0.31.0/go-accepted.jsonl) · [竞态检查](../artifacts/backend-connection-0.31.0/plugin-race-accepted.log) · [校验器对照](../artifacts/backend-connection-0.31.0/delivery/totp-parity.log)

| 覆盖范围 | 验收证据 |
| --- | --- |
| 官方宿主加载、重启、插件注册与静态资源，合成凭证转换和上传 | [完整原生生命周期](../artifacts/backend-connection-0.31.0/delivery/lifecycle.json) |
| 账号资料、代理与巡检，账号任务历史及转换，重置 2FA、退出设备、未知提交和本地完成 | [账号交互](../artifacts/connection-ui-0.31.0/dialogs/account-interactions.json) · [转换流程](../artifacts/connection-ui-0.31.0/convert/conversion.json) · [安全操作回执](../artifacts/connection-ui-0.31.0/security/security-submit.json) · [Go 账号服务测试](../artifacts/backend-connection-0.31.0/go-accepted.jsonl) |
| OAuth 文件与提供商的原生调用、定向探测、整组授权及恢复，答题判定与上游错误的独立处置 | [提供商](../artifacts/backend-connection-0.31.0/delivery/provider.json) · [跨规则处置](../artifacts/backend-connection-0.31.0/delivery/operations.json) · [上游错误](../artifacts/backend-connection-0.31.0/delivery/upstream.json) |
| 未确认停用经历两次重启，保留原 4 个样本，累计只提交 1 次 PATCH。CPA 人工启用后手动和定时路径按当前范围准入 | [未知收据](../artifacts/backend-connection-0.31.0/delivery/unknown.json) · [手动启用后的准入](../artifacts/backend-connection-0.31.0/delivery/readmission.json) |
| 质量范围、原规则恢复归属、历史筛选、日志、统计、分页、清理及清理后重启 | [范围及历史](../artifacts/backend-connection-0.31.0/delivery/scope.json) · [清理前后对照](../artifacts/backend-connection-0.31.0/delivery/history.json) · [页面清理回执](../artifacts/connection-ui-0.31.0/cleanup/cleanup-receipts.json) |

同一 ZIP 在 12 项隔离官方宿主场景中运行。真实时钟场景先确认已开启规则发现新加入的假凭证，再停止并删除容器，待错过两个分钟边界后以同一挂载重建。在第一次页面查询前，假上游已记录 1 次新补检，补检期间凭证写入为 0。下一个原定 Cron 时刻按当前目录检查 OAuth 文件和新增提供商各 1 次。该场景全程合计 5 次模拟调用、1 次有明确归属的停用写入、0 次对已停用凭证的调用。[同卷容器重建](../artifacts/backend-connection-0.31.0/delivery/cron.json) · [凭证停用、复检、恢复与删除](../artifacts/backend-connection-0.31.0/delivery/credential-policy.json) · [凭证替换](../artifacts/backend-connection-0.31.0/delivery/credential-changes.json) · [运行时错误证据](../artifacts/backend-connection-0.31.0/delivery/runtime-policy.json)

## 复现

在仓库根目录运行。Docker 场景使用本机已存在的官方 8.0.76 镜像和回环假上游，Cron 场景按真实分钟运行。将输出放到新的验收目录。

```sh
(cd plugin && go test -race ./...)
node --test scripts/tests/*.test.cjs
python3 scripts/verify_baseline.py --source workspaces/CLIProxyAPI-official
OUT=$(mktemp -d)
python3 scripts/prepare_official_context.py "$OUT/context"
docker build --platform linux/arm64 --target plugin-artifacts --output "type=local,dest=$OUT/package" "$OUT/context"
python3 scripts/smoke_ui_package.py "$OUT/package" "$OUT/lifecycle.json"
python3 scripts/smoke_ui_package.py "$OUT/package" "$OUT/provider.json" --provider-targets
python3 scripts/smoke_credential_policy.py "$OUT/package" "$OUT/operations.json" --quality-operations
python3 scripts/smoke_credential_policy.py "$OUT/package" "$OUT/cron.json" --scheduled-addition
```

构建器在 Linux arm64 上运行内嵌模式的 Go 测试、`go vet` 和登录引擎自测后才生成共享库。官方宿主的 2,413 个源码和构建文件与基线逐项核对，在隔离副本编译服务端并通过宿主全量 Go 测试。[构建日志](../artifacts/backend-connection-0.31.0/delivery/build.log) · [宿主检查](../artifacts/backend-connection-0.31.0/host-check.log) · [构建清单](../artifacts/backend-connection-0.31.0/delivery/arm64/build-manifest.json)

## 本机 8317

2026-11-21 18:05（北京时间），停机备份配置、凭证、插件及数据后，原子替换 Linux arm64 共享库。原宿主镜像、容器和挂载一致。六个公开资源的实际字节逐一匹配构建清单，带当前缓存标签读取均返回 304。管理页面可访问，未携带管理身份访问插件设置返回 401。[本机部署报告](../artifacts/backend-connection-0.31.0/deployment.json)

原有 195 份插件数据文件全部保留，非数据库文件内容变化为 0。三个业务数据库中，账号和提交收据逻辑内容一致；质量库只增加一项与旧 `config_input` 摘要精确匹配的宿主配置基线，其他变化为 0。原凭证目录和账号存储通过核对。实机验收鉴权管理请求、真实模型和账号操作均为 0。此次部署未代替管理员写入后台密钥；旧插件页面已保存的有效身份在下次访问时可自动迁移。首次后台恢复和无浏览器运行已在隔离容器中实测。
