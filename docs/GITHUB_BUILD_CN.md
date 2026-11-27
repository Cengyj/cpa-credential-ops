# GitHub Linux amd64 构建

推送 `main`、向 `main` 提交 Pull Request，或在 Actions 手动运行 **Build full Linux amd64 plugin**，都会构建完整原生插件。构建使用 `deploy/docker/Dockerfile` 的 `plugin-artifacts` 目标和 `linux/amd64` 平台，包含账号管理及内嵌 Go/Sobek 登录引擎。

工作流先运行 Python 构建工具测试、JavaScript 语法与前端回归，再取得固定的官方 CPA 8.0.76 源码，逐文件核对已有基线，生成仅含源码的 Docker 上下文。Docker 构建执行登录引擎测试、内嵌模式插件测试和 `go vet`，然后生成动态库、安装 ZIP、校验和与构建清单。

宿主源码固定为官方 `v8.0.76` 对应提交 `7df56074b252384d878ec4fca840552c6fb5236e`。2026-11-28 已核实，该提交归档中的 2,413 个记录文件与 `host-patches/official-8.0.76-baseline.json` 的 SHA256 全部一致；归档额外文件不进入构建上下文。`scripts/fetch_official_source.py` 只写入全部通过校验的源码，已有副本若不同则拒绝覆盖，保留原基线。

随后在禁止外网的临时官方 CPA 容器内核对插件注册、管理鉴权、十份静态资源、内嵌登录引擎、合成凭证转换以及重启。临时测试完成后删除容器，不使用现有 CPA、账号或凭证。

成功后的 Actions artifact 名称为 `credential-ops-linux-amd64-<commit>`，保留 30 天。解压 artifact 后可取得 `model-quality-v<version>-linux-amd64.zip`、`SHA256SUMS`、`build-manifest.json`、源码上下文清单及生命周期验证记录。`SHA256SUMS` 同时列出 ZIP 及其中的 `model-quality.so`，校验全部条目前先解压安装 ZIP。

此工作流产出当前分支的编译包。GitHub Release 与商店 `registry.json` 继续对应已正式发布的版本；正式发布时需使用成功验证的完整包及其真实 SHA256、大小更新商店条目。
