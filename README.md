# Credential Ops

Credential Ops is an independent native plugin project for the official CLIProxyAPI server. It adds account login, credential management, model evaluation, and scheduled quality operations through CPA's public plugin interface. [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)

[中文说明](README_CN.md) · [Releases](https://github.com/Cengyj/cpa-credential-ops/releases) · [Credential policies](docs/QUALITY_CREDENTIAL_ACTIONS_CN.md) · [Error policies](docs/QUALITY_UPSTREAM_ERRORS_CN.md)

## Features

- OpenAI account management with an embedded Go/Sobek login runtime, 2FA login, credential conversion, and explicit account security actions.
- Manual model tests with frozen questions, multiple answers, history, exports, and isolated HTML previews.
- Scheduled credential checks with configurable scope and separate actions for wrong answers, HTTP 401/402, exhausted account quota, and unsupported models.
- Durable operation records, bounded concurrency and storage, and explicit handling of uncertain credential writes.

## Install or upgrade

Version **0.29.37** ships for **Linux amd64 (x86_64)** and requires glibc **2.34** or later. It includes UI **13.6.35**, the account management page, embedded Go/Sobek login runtime **2.2.1**, manual model tests, and scheduled credential checks for official CPA **8.0.76**.

For store installation, enable the plugin system and add `https://raw.githubusercontent.com/Cengyj/cpa-credential-ops/main/registry.json` to **Third-party plugin sources**. Save your settings, refresh the plugin store, then open **Credential Ops** and select **Install** or **Update**. CPA selects the package for its platform. Restart CPA if prompted.

1. Download the package and checksums from [the release page](https://github.com/Cengyj/cpa-credential-ops/releases/tag/v0.29.37).
2. Back up the existing plugin and persistent data while CPA is stopped.
3. For a fresh manual installation, extract `model-quality.so` to the configured plugin directory and enable it. For a manual upgrade, use the plugin's `path` from `GET /v8/management/plugins` to locate the current library. If it has a versioned filename, use the store update so CPA also updates the configured version. Restart CPA after a manual installation.
4. Refresh the authenticated management panel and open **Credential Ops**.

The 0.31.0 source build verifies the CPA management key once, saves it in the plugin’s private data volume and restores access on startup. Browser logout ends that browser’s access. [Connection lifecycle](docs/BACKGROUND_CONNECTION_CN.md)

The display name and repository have changed. The native plugin ID, library filename, configuration key, API paths, and data directory remain `model-quality` so existing installations retain their accounts, rules, and history. Do not install a second copy under a different library filename.

## Quality operation behavior

Rule scope selects the credentials to inspect. An enabled credential's error state or cooldown does not exclude it from an authorized quality check. A verified model catalog can establish that a requested model is unsupported; otherwise the configured model is checked through the applicable execution channel. Historical status text alone is not sufficient evidence for an error action.

After an error policy successfully disables a credential, that credential leaves subsequent quality operations. The plugin does not poll its state, retest it, repeat the action, or generate repeated skipped records. The original error and disposition remain available. Wrong-answer recovery is a separate opt-in policy.

No account action or scheduled test is enabled merely by installing the plugin. Official CPA has no atomic conditional credential status/delete API; the plugin checks snapshots before and after writes and preserves uncertain outcomes without replaying them.

## Development

The plugin is an independent Go module under `plugin/`. The embedded login runtime is under `login-runtime-go/`. Host source is kept in a separate, verified checkout and is not modified. See [build and installation instructions](docs/INSTALL_CN.md) and [the CPA 8.0.76 build notes](docs/DOCKER_8_0_76_CN.md).

Development verification uses synthetic credentials and fake upstreams. Local runtime data, credentials, databases, and generated artifacts are excluded from Git.

See [0.29.36 verification](docs/QUALITY_VERIFICATION_0_29_36_CN.md) for the current scope and error-policy checks. Historical documentation uses the former name, Model Quality.
