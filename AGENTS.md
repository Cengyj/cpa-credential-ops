# Model Quality Plugin Workspace

This workspace is for a standalone CLIProxyAPI native plugin with manual model evaluation and credential quality operations.

## Local CPA updates

On 2026-11-20 the user authorized updating the plugin in the local CPA at
`http://127.0.0.1:8317/management.html#/plugin-pages/model-quality/0` after
plugin changes without asking again. Build the full plugin for the running
instance's architecture, preserve its data, and verify the assets served by
that instance. Follow any later request to defer building or deployment.

## GitHub release target

The user specified on 2026-11-20 that GitHub releases only require a Linux
amd64 (x86_64) build. Publish the full native plugin with the embedded Go/Sobek
login runtime and account management. Use the full `plugin-artifacts` Docker
target with `--platform linux/amd64`. The current CPA store registry should
list the full Linux amd64 package for the published version. Build additional
platforms only when explicitly requested.

## Current official-host compatibility decision

The user approved the official-host compatibility plan on 2026-11-17. Keep CPA
unmodified and distribute one installable native plugin. Use the official
authenticated `/v0/management/plugins/model-quality/` extension channel; this
explicit approval supersedes the earlier v8-only requirement below. Login,
account storage, inspection and the embedded Go/Sobek helper belong to the
plugin. Use only public SDK callbacks. Model tests and observation remain;
independent quality pause/release and automatic pause are unavailable on the
official host and must be rejected rather than emulated with operator disable.
Never silently clear historical pause ownership. The unchanged test checkout is
`workspaces/CLIProxyAPI-official/`; verify it against the source baseline without
editing or formatting it. The older patched checkout and patches are historical
artifacts, not requirements for this release.

On 2026-11-17 the user additionally requested one-action account login: a new
account-list 2FA login authorizes successful credential upload and enablement,
without another candidate confirmation or page navigation. Scheduled repair uses
its existing explicit global/account permissions, never reactivates a credential
that became disabled, and rechecks policy before saving. Historical pending jobs
are not automatically replayed. Identity conflicts, retained quality pauses and
uncertain writes still block; official callbacks still have no atomic CAS.

The user explicitly approved upgrading the existing local CPA on port 8317 while
preserving its two accounts on 2026-11-17. This includes a stopped-instance backup,
verified conversion of the one legacy account binding into plugin-owned storage,
and moving the old login runtime out of the credential scan directory. This is a
local deployment operation, not a real-account test. Do not start a real login,
call a model, or change existing credential contents as part of verification.

On 2026-11-17 the user requested reference-aligned reset-2FA and logout-all
account actions. These are explicit manual actions only. Preserve confirmed new
seeds before any subsequent logout/OAuth; unknown mutations stay quarantined and
must never be replayed on retries, restart, inspection, or local completion.
Deployment and routine verification do not authorize executing these operations
against real accounts. Keep the official host unchanged.

## Credential handling requested on 2026-11-19

The latest user request adds opt-in `disable_credential` and `delete_credential`
policies using the existing CPA v8 management APIs without changing the host.
These policies are distinct from historical independent `pause_credential`
ownership. Keep historical ownership and its restrictions intact. Default to
observation. Only complete, explicitly judged rounds may trigger handling;
network errors, refusal, truncation, cancellation and unknown answers do not.
An owned disabled credential can be probed through the existing management
API-call channel. Optional recovery requires every selected model and sample to
pass and the stored credential snapshot to remain unchanged. Never temporarily
enable a credential for testing. Since 2026-11-21, explicitly verified loopback management authorization
is saved to the plugin's private management directory for browser-free restart.
Ordinary authenticated reads never install a key. Keep the key out of quality
settings, source, public resources, task records, logs, and exports. Uncertain mutations are not replayed.
Document CPA's lack of atomic conditional status/delete operations. Verification
uses only isolated fake credentials and upstreams; deploying this plugin does not
authorize real-account actions or model calls.

## Task specification

Read `PROMPT_CN.md` for the complete implementation task and `docs/RESEARCH_CN.md` for verified source findings. The latest direct user instruction takes precedence over these files.

The product action is **credential-wide quality pause**, followed only by explicit manual release. Model-level results are evidence; the first release does not implement groups or per-model isolation.

## Paths and source boundaries

- Workspace: `/Users/ceng/Downloads/CLIProxyAPI-Quality-Plugin`
- Plugin module and embedded UI: `plugin/`
- Editable host source, prepared when implementation starts: `workspaces/CLIProxyAPI/`
- Reference checkout, prepared when needed: `workspaces/sub2api/`
- Host patches and base manifest: `host-patches/`
- Documentation: `docs/`
- Build artifacts: `artifacts/`
- Existing host source reference: `/Users/ceng/Downloads/CLIProxyAPI-8.0.67`

Treat the existing host source reference as read-only. Make required host changes in the workspace copy. Read its `AGENTS.md` before preparing or editing host code and preserve those requirements in the copy. The source reference may lack Git metadata; record its actual baseline rather than assuming a branch or commit. Copy only source and required build assets, preserving source fidelity and excluding credentials, environment secrets, live configuration, logs, databases, binaries, and plugin state. Do not replace the reference with a newer upstream checkout without documenting the difference.

The plugin is a separate Go module using the public `/v8` SDK. Do not import the host's `internal` packages from the plugin. Maintain host extensions and integration adapters inside the host copy.

## Product invariants

- Manual evaluation does not pause or resume credentials automatically.
- A quality pause blocks new business executions for the entire credential and has durable ownership.
- Operator disable, quality pause, native cooldown, and other owners remain separate conditions.
- Quality-paused credentials must never be retested, manually or automatically. No bypass grants or automatic recovery exist. Stop every dependent rule and task, including use as control or judge.
- Only complete, comparable, explicitly incorrect evidence can advance a quality-failure decision. Infrastructure errors and unknown results do not constitute quality failures.
- Manual release removes only the matching owned pause; it does not promise the credential is otherwise usable. All dependent rules stay stopped, stale counters reset, and explicit rule resume is required.
- Default to observation. Scheduling and automatic pause require separate configuration switches. Recovery switches and retest intervals are removed.

## Engineering and verification

Follow the source repository's Go, JSON/Base64, thinking-pipeline, final-payload-barrier, logging, and network-timeout rules. Keep new business APIs under authenticated v8 management paths; public resource GETs serve static content only. Do not add or alter deprecated v0 management endpoints.

Use offline fake upstreams for development. Never copy live credentials into this workspace or use real model accounts during routine tests.

Test the plugin module separately from the host module, build the native shared library, and verify loading and lifecycle behavior. Run the required host formatting, tests, and compile checks after Go changes. Keep source-only host patches and their baseline evidence reproducible.

Use English code comments. New Chinese documentation must use an explicit `_CN.md` suffix.
