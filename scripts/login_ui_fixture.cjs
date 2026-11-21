#!/usr/bin/env node
// Serve the real UI against synthetic, in-memory APIs for manual CUA verification.
// This fixture has no upstream client, browser automation, or persistent account store.
// Quality APIs are read-only; all displayed answers are authored synthetic records.
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');

const root = path.resolve(__dirname, '..');
const apiPrefix = '/v0/management/plugins/model-quality';
const loginPrefix = `${apiPrefix}/login`;
const managementKey = 'offline-key';
const controlKey = 'offline-control';
const portIndex = process.argv.indexOf('--port');
const requestedPort = portIndex < 0 ? 0 : Number(process.argv[portIndex + 1]);
if (!Number.isInteger(requestedPort) || requestedPort < 0 || requestedPort > 65535) throw new Error('Invalid --port');
const staticFiles = new Set(require('../plugin/ui/public_assets.json'));
let accounts, jobs, credentials, logs, recoveries, settings, sequence, injections, calls, receipts, connection;
let qualityTargets, questions, qualityRuns, qualityRules, qualityActions, qualityAudits, qualitySettings;
const timers = new Set();
const active = new Set(['queued', 'running', 'waiting_browser', 'exchanging', 'validating', 'committing', 'cancelling']);
const clone = value => JSON.parse(JSON.stringify(value));
const now = () => new Date().toISOString();
const planTypes = new Set(['free', 'go', 'plus', 'pro', 'prolite', 'business_premium', 'team', 'enterprise', 'edu', 'unknown']);
function planType(value) {
  let plan = String(value || '').trim().toLowerCase().replace(/^chatgpt_/, '');
  plan = ({ business: 'team', self_serve_business: 'team', 'pro-lite': 'prolite', pro_lite: 'prolite', self_serve_business_prolite: 'business_premium', education: 'edu' })[plan] || plan;
  return planTypes.has(plan) ? plan : 'unknown';
}
function excludedPlans(value) {
  if (!Array.isArray(value) || value.length > 32 || value.some(item => typeof item !== 'string' || planType(item) === 'unknown' && item.trim().toLowerCase() !== 'unknown')) return null;
  return [...new Set(value.map(planType))];
}
function uploadExcluded(job) {
  const account = accounts.find(item => item.id === job.account_id);
  const known = [planType(job.plan_type), planType(account?.plan_type)].filter(plan => plan !== 'unknown');
  return (known.length ? known : ['unknown']).some(plan => settings.cpa_upload_excluded_plans.includes(plan));
}

function seed() {
  sequence = 500;
  connection = { revision: 1, state: 'ready', configured: true, can_migrate: false, source: 'saved' };
  injections = []; calls = []; receipts = new Map();
  credentials = new Map(); logs = new Map(); recoveries = new Map();
  settings = { revision: 1, inspection_enabled: false, auto_relogin_enabled: false, interval_minutes: 15, login_proxy_url: 'socks5h://host.docker.internal:1080', cpa_upload_excluded_plans: [] };
  const account = (id, label, extra = {}) => ({
    id, revision: 1, email: `${id}@example.test`, masked_email: 'synthetic@example.test', label: `[离线演示] ${label}`,
    proxy_url: '', auth_id: '', identity_version: '', health_status: 'not_linked', health_reason: 'login_required',
    last_checked_at: '', next_check_at: '', last_job_id: '', last_job_status: '', last_login_at: '',
    last_job_auto_commit: false, last_job_credential_enabled: false, auto_relogin: false,
    attention_required: false, security_pending: false, consecutive_failures: 0, plan_type: 'unknown', ...extra,
  });
  accounts = [
    account('fixture-new', '尚未授权'),
    account('fixture-failed', '代理失败待处理', { plan_type: 'free', last_job_id: 'fixture-job-failed', last_job_status: 'failed', last_job_error_code: 'network_error', last_job_error_stage: 'session', last_job_error_reason: 'proxy_connection_failed', attention_required: true, proxy_url: 'http://fixture-user:fixture-password@127.0.0.1:7890' }),
    account('fixture-active', '正在验证 2FA', { plan_type: 'plus', last_job_id: 'fixture-job-active', last_job_status: 'running', last_job_auto_commit: true }),
    account('fixture-enabled', '已保存并启用', { plan_type: 'pro', auth_id: 'fixture-cpa-enabled.json', identity_version: 'fixture-identity-enabled', health_status: 'healthy', health_reason: 'no_auth_failure_observed', last_job_id: 'fixture-job-enabled', last_job_status: 'committed', last_job_auto_commit: true, last_job_credential_enabled: true, last_login_at: '2026-11-17T01:20:00Z', last_checked_at: '2026-11-17T01:25:00Z', proxy_url: 'direct' }),
    account('fixture-uncertain', '2FA 结果待核对', { plan_type: 'business_premium', auth_id: 'fixture-cpa-uncertain.json', identity_version: 'fixture-identity-uncertain', health_status: 'unknown', health_reason: 'security_pending', last_job_id: 'fixture-job-uncertain', last_job_status: 'security_uncertain', last_job_action: 'reset_totp', last_job_security_status: 'unknown', security_pending: true, attention_required: true }),
    account('fixture-history', '历史任务超过全局 200 条', { plan_type: 'team', auth_id: 'fixture-cpa-history.json', identity_version: 'fixture-identity-history', health_status: 'not_checked', health_reason: 'awaiting_inspection', last_job_id: 'fixture-job-old', last_job_status: 'failed', last_job_error_code: 'runtime_timeout', attention_required: true, effective_proxy_url: 'http://host.docker.internal:8899', proxy_source: 'credential' }),
  ];
  for (const item of accounts) credentials.set(item.id, { email: item.email, password: 'SYNTHETIC-NOT-A-REAL-PASSWORD', totp_secret: 'JBSWY3DPEHPK3PXP' });
  const job = (id, accountID, status, extra = {}) => ({ id, revision: 1, account_id: accountID, email: `${accountID}@example.test`, plan_type: accounts.find(item => item.id === accountID)?.plan_type || 'unknown', method: 'password_totp', intent: 'import', automatic: false, status, phase: status === 'running' ? 'totp' : 'complete', auto_commit: true, enable_credential: true, credential_enabled: status === 'committed', created_at: '2026-11-17T01:20:00Z', updated_at: '2026-11-17T01:20:30Z', ...extra });
  jobs = [
    job('fixture-job-active', 'fixture-active', 'running', { created_at: '2026-11-17T02:00:00Z', updated_at: '2026-11-17T02:00:20Z' }),
    job('fixture-job-uncertain', 'fixture-uncertain', 'security_uncertain', { action: 'reset_totp', intent: 'reauthorize', phase: 'totp_activate', security_status: 'unknown', error_code: 'security_outcome_unknown', error_stage: 'totp_activate', reauthorize: true, logout_all_devices: true, totp_reset: false, devices_logged_out: false }),
    job('fixture-job-failed', 'fixture-failed', 'failed', { phase: 'session', error_code: 'network_error', error_stage: 'session', error_reason: 'proxy_connection_failed' }),
    job('fixture-job-enabled', 'fixture-enabled', 'committed', { action: 'reset_totp', intent: 'reauthorize', target_auth_id: 'fixture-cpa-enabled.json', reauthorize: true, logout_all_devices: true, totp_reset: true, devices_logged_out: true, security_status: 'complete' }),
    ...Array.from({ length: 205 }, (_, i) => job(`fixture-background-${i}`, 'fixture-enabled', 'committed', { created_at: new Date(Date.UTC(2026, 10, 15, 0, 0, i)).toISOString() })),
    job('fixture-job-old', 'fixture-history', 'failed', { created_at: '2026-10-01T00:00:00Z', error_code: 'runtime_timeout', error_stage: 'session' }),
  ];
  recoveries.set('fixture-uncertain', { job_id: 'fixture-job-uncertain', can_complete: false, activation_confirmed: false, new_totp_secret: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ' });
  for (const item of jobs) logs.set(item.id, { job_id: item.id, legacy: false, entries: [
    { id: 1, time: item.created_at, level: 'info', status: 'queued', phase: 'queued' },
    { id: 2, time: item.updated_at, level: ['failed', 'security_uncertain'].includes(item.status) ? 'error' : 'info', status: item.status, phase: item.phase, ...(item.error_code ? { error_code: item.error_code } : {}), ...(item.error_stage ? { error_stage: item.error_stage } : {}), ...(item.error_reason ? { error_reason: item.error_reason } : {}) },
  ] });
  // Rich event histories exercise diagnostics without contacting any upstream.
  for (const id of ['fixture-job-failed', 'fixture-job-enabled', 'fixture-job-uncertain']) {
    const item = jobs.find(job => job.id === id);
    const phases = id === 'fixture-job-failed' ? ['queued','authorization','runtime_start','session']
      : id === 'fixture-job-uncertain' ? ['queued','security_login','security_check','totp_disable','totp_enroll','totp_activate']
      : ['queued','security_login','password','totp','security_check','totp_disable','totp_enroll','totp_activate','totp_saved','logout_all','reauthorization','workspace','token_exchange','saving'];
    const entries = phases.map((phase,index) => ({ id: index + 1, time: new Date(Date.parse(item.created_at) + index * 1250 + index * index * 270).toISOString(), level: 'info', status: index ? 'running' : 'queued', phase }));
    // Fixed request labels model diagnostic metadata without any network requests.
    const requestTime = Date.parse(entries.at(-1).time) + 550;
    entries.push({ id: entries.length + 1, time: new Date(requestTime).toISOString(), level: 'info', status: 'running', phase: id === 'fixture-job-failed' ? 'session' : 'password', event: 'request', request_kind: 'password_verify', method: 'POST', http_status: 200, duration_ms: 824, failed: false });
    if (id === 'fixture-job-failed') entries.push({ id: entries.length + 1, time: new Date(requestTime + 1100).toISOString(), level: 'warn', status: 'running', phase: 'session', event: 'request', request_kind: 'auth_session', method: 'GET', http_status: 502, duration_ms: 1090, failed: false });
    if (id === 'fixture-job-uncertain') entries.push({ id: entries.length + 1, time: new Date(requestTime + 1100).toISOString(), level: 'error', status: 'running', phase: 'totp_activate', event: 'request', request_kind: 'totp_activate', method: 'POST', duration_ms: 1090, failed: true, error_reason: 'network_timeout', runtime_code: 'transport_error' });
    entries.push({ id: entries.length + 1, time: new Date(Date.parse(entries.at(-1).time) + 2270).toISOString(), level: item.status === 'committed' ? 'info' : 'error', status: item.status, phase: '', error_code: item.error_code, error_stage: item.error_stage, error_reason: item.error_reason, ...(item.status === 'committed' ? { event: 'state', totp_reset: true, devices_logged_out: true, credential_enabled: true, security_status: 'complete' } : {}) });
    item.updated_at = entries.at(-1).time;
    logs.set(id, { job_id: id, legacy: false, truncated: false, first_id: 1, last_id: entries.length, retained_count: entries.length, has_more: false, next_cursor: entries.length, entries });
  }
  accounts.find(account => account.id === 'fixture-new').label = '';
  const old = logs.get('fixture-job-old'); old.legacy = true; old.truncated = true;
  old.entries.forEach((entry,index) => { entry.id = 21 + index; });
  seedQuality();
}

function seedQuality() {
  const target = (id, label, extra = {}) => ({
    auth_id: id, auth_index: label, provider: 'openai', type: 'config_api_key',
    models: ['fixture-reasoner', 'fixture-visual'],
    reasoning: { 'fixture-reasoner': { zero_allowed: true, levels: ['none', 'low', 'medium', 'high', 'max'] }, 'fixture-visual': null },
    identity_version: `synthetic-${id}`, payload_config_version: 'fixture-v1',
    runtime_status: 'active', manual_disabled: false, quality_paused: false, effective_available: true,
    can_pause: false, can_retest: false, reasons: [],
    unsupported_reason: 'official_host_quality_pause_unavailable', ...extra,
  });
  qualityTargets = [
    target('fixture-quality-primary', '[离线演示] 主测试凭证'),
    target('fixture-quality-disabled', '[离线演示] 人工禁用凭证', { runtime_status: 'disabled', manual_disabled: true, effective_available: false, reasons: ['operator_disabled'] }),
    target('fixture-quality-cooldown', '[离线演示] 原生冷却凭证', { effective_available: false, reasons: ['native_cooldown'] }),
    target('fixture-quality-legacy', '[离线演示] 历史归属待核对', { retained_pause: true, effective_available: false, reasons: ['retained_pause_unverified'] }),
  ];
  questions = [
    { id: 'custom', title: '自定义题目', kind: 'custom', prompt: '', version: 'custom' },
    { id: 'logic-candy', title: '糖果逻辑题', kind: 'logic', prompt: '甲、乙、丙分别有不同数量的糖果。甲比乙多 2 颗，乙比丙多 2 颗，三人共有 21 颗。请逐步说明每人有多少颗糖果，并核对总数。', version: 'fixture-1' },
    { id: 'visual-pelican-html', title: '鹈鹕自行车 HTML', kind: 'html', prompt: '创建一个独立 HTML 页面，用内联 SVG 绘制骑自行车的鹈鹕。使用柔和背景，展示完整车轮与车架，不加载任何外部资源。', version: 'fixture-1' },
    { id: 'random-linear', title: '可复现随机约束题', kind: 'random', prompt: '根据冻结的整数约束解方程，并解释推导。此处为离线展示题面，不会生成或执行实际模型请求。', version: 'fixture-1' },
  ];
  const config = (questionID = 'logic-candy', model = 'fixture-reasoner', extra = {}) => ({
    auth_id: 'fixture-quality-primary', provider: 'openai', models: [model], question_id: questionID,
    question: clone(questions.find(question => question.id === questionID)), samples: 1,
    parameters: { max_tokens: 2048, stream: true, reasoning_effort: 'medium' },
    judge: { mode: 'manual', template_version: '' }, identity_version: 'synthetic-fixture-quality-primary',
    payload_config_version: 'fixture-v1', fingerprint: 'fixture-frozen-conditions', ...extra,
  });
  const sample = (output, index = 0, extra = {}) => ({
    model: 'fixture-reasoner', index, execution_status: 'completed', quality_verdict: 'unknown', output,
    reason: 'manual assessment required', total_ms: 2680 + index * 430, ttft_ms: 320 + index * 75,
    usage: { input_tokens: 96, output_tokens: 248 + index * 24, total_tokens: 344 + index * 24 },
    judge_usage: null, comparable: false, response_model: 'fixture-reasoner',
    evidence: { verified: false, requested_model: 'fixture-reasoner', resolved_model: '', auth_id: 'fixture-quality-primary',
      payload_fingerprint: '', payload_config_version: 'fixture-v1', conditions_changed: false,
      path: 'official-sdk', reason: '官方接口未提供已验证的最终载荷证据；此记录仅为离线界面样例。' }, ...extra,
  });
  const answer = '可以先把三人的糖果数量写成同一个未知数。\n\n设丙有 x 颗，乙就有 x + 2 颗，甲有 x + 4 颗。\n\n根据总数条件：\nx + (x + 2) + (x + 4) = 21\n3x + 6 = 21\n3x = 15\nx = 5\n\n所以丙有 5 颗，乙有 7 颗，甲有 9 颗。\n\n最后核对：9 − 7 = 2，7 − 5 = 2，三个人的数量互不相同；9 + 7 + 5 = 21，符合全部条件。\n\n答案：甲 9 颗，乙 7 颗，丙 5 颗。\n\n这是一份预先编写的合成回答，用于检查段落、公式、复制与完整阅读体验；没有调用真实模型。';
  const alternate = '三个人的数量构成公差为 2 的等差数列，所以中间的乙有 21 ÷ 3 = 7 颗。\n\n甲：7 + 2 = 9 颗。\n丙：7 − 2 = 5 颗。\n\n检验总数：9 + 7 + 5 = 21。\n\n两种推导得到相同结果。本段为离线合成样例，不构成真实模型的质量或性能证据。';
  const visual = '<!doctype html>\n<html lang="zh-CN">\n<meta charset="utf-8">\n<title>离线 SVG 回答样例</title>\n<style>body{margin:0;display:grid;place-items:center;min-height:100vh;background:#f5f0e6;font-family:system-ui;color:#3b443f}svg{width:min(100%,560px)}text{font-size:18px}</style>\n<svg viewBox="0 0 560 340" xmlns="http://www.w3.org/2000/svg" role="img" aria-label="骑车鹈鹕的合成示意图">\n<path d="M20 285H540" stroke="#b7b4a3" stroke-width="3"/>\n<g fill="none" stroke="#344d47" stroke-width="8"><circle cx="155" cy="235" r="62"/><circle cx="390" cy="235" r="62"/><path d="M155 235L230 140L285 235H155L220 170H360L390 235M285 235L355 135H390"/></g>\n<ellipse cx="290" cy="120" rx="70" ry="40" fill="#fffdf5" stroke="#8d9e95" stroke-width="3"/>\n<path d="M335 113Q345 45 380 57L388 89" fill="#fffdf5" stroke="#8d9e95" stroke-width="3"/>\n<path d="M380 72L458 91L385 96Z" fill="#e6a958"/><circle cx="375" cy="67" r="4" fill="#344d47"/>\n<path d="M255 135L282 187L308 187" fill="none" stroke="#dca663" stroke-width="8"/>\n<text x="280" y="325" text-anchor="middle">离线样例 · 没有执行真实模型请求</text>\n</svg>\n</html>';
  const run = (id, created, frozen, samples, extra = {}) => ({
    id, status: 'completed', source: 'manual_test', created_at: created,
    completed_at: new Date(Date.parse(created) + 6000).toISOString(), config: frozen, samples,
    action_status: 'not_applicable', cancel_requested: false, ...extra,
  });
  qualityRuns = [
    run('fixture-run-correct', '2026-11-17T03:45:00Z', config(), [
      sample('甲 9 颗、乙 7 颗、丙 5 颗。相邻各差 2 颗，总数为 21。', 0, { review: 'correct' }),
    ]),
    run('fixture-run-incorrect', '2026-11-17T03:30:00Z', config('custom', 'fixture-reasoner', {
      question: { id: 'custom', kind: 'custom', title: '乘法应用题', prompt: '每盒有 5 支铅笔，7 盒共有多少支？', answer: '35 支' },
    }), [sample('5 × 7 = 30，所以共有 30 支铅笔。', 0, { answer_match: 'incorrect' })]),
    run('fixture-run-complete', '2026-11-17T03:15:00Z', config('logic-candy', 'fixture-reasoner', { samples: 2 }), [sample(answer), sample(alternate, 1)]),
    run('fixture-run-visual', '2026-11-17T02:50:00Z', config('visual-pelican-html', 'fixture-visual', { parameters: { max_tokens: 2048, stream: true } }), [sample(visual, 0, { model: 'fixture-visual', response_model: 'fixture-visual', total_ms: 3480, evidence: { verified: false, requested_model: 'fixture-visual', auth_id: 'fixture-quality-primary', path: 'official-sdk', conditions_changed: false } })]),
    run('fixture-run-error', '2026-11-17T02:30:00Z', config(), [sample('', 0, { execution_status: 'error', reason: '离线错误样例：网络连接超时，不作为回答质量失败。', error: 'synthetic transport timeout', total_ms: 15000, ttft_ms: null, usage: null })], { status: 'failed', error: '离线错误样例：请求未取得完整回答。' }),
    ...Array.from({ length: 9 }, (_, index) => {
      const id = `fixture-run-history-${index + 1}`, created = new Date(Date.UTC(2026, 10, 16, 18 - index, 10)).toISOString();
      if (index === 0) return run(id, created, config('logic-candy', 'fixture-reasoner', { samples: 2 }), [sample(answer, 1, { answer_match: 'correct' })], { pruned_samples: 1, pruned_sample_slots: [{ model: 'fixture-reasoner', index: 0 }] });
      if (index === 1) return run(id, created, config('logic-candy', 'fixture-reasoner', { samples: 2 }), [sample(answer, 0, { review: 'correct' }), sample(alternate, 1)]);
      if (index === 2) return run(id, created, config(), [sample('', 0, { review: 'correct' })]);
      return run(id, created, config(), [sample(index % 2 ? alternate : answer)]);
    }),
    run('fixture-run-observation', '2026-11-17T03:05:00Z', config('logic-candy', 'fixture-reasoner', { rule_id: 'fixture-rule-observe', rule_revision: 2, judge: { mode: 'deterministic', template_version: 'fixture-1' } }), [sample('21', 0, { reason: '官方观察模式：未取得已验证的可比较证据，不推进质量失败计数。' })], { source: 'scheduled_probe', action_status: 'observe_only' }),
    run('fixture-run-observation-error', '2026-11-16T22:05:00Z', config('logic-candy', 'fixture-reasoner', { rule_id: 'fixture-rule-paused', rule_revision: 1 }), [sample('', 0, { execution_status: 'error', error: 'synthetic rate limit', reason: '离线限流样例，不计为答错。', usage: null, ttft_ms: null })], { status: 'failed', source: 'scheduled_probe', action_status: 'observe_only', error: '离线限流样例：未取得回答。' }),
  ];
  const rule = (id, name, extra = {}) => ({
    ...config(), id, name, revision: 2, status: 'detecting', enabled: true, action: 'observe_only',
    scheduling_enabled: false, automatic_pause: false, interval_seconds: 3600, timezone: 'Asia/Shanghai',
    fail_threshold: 3, fail_count: 0, retention_days: 30, next_run: '0001-01-01T00:00:00Z', ...extra,
  });
  qualityRules = [
    rule('fixture-rule-observe', '[离线演示] 推理模型观察', { last_run: 'fixture-run-observation' }),
    rule('fixture-rule-paused', '[离线演示] 已停止的周期观察', { enabled: false, status: 'paused', scheduling_enabled: true, interval_seconds: 21600, last_run: 'fixture-run-observation-error' }),
    rule('fixture-rule-retained', '[离线演示] 历史暂停归属待核对', { auth_id: 'fixture-quality-legacy', enabled: false, status: 'unsupported', action: 'pause_credential', action_id: 'fixture-action-retained', action_revision: 1, action_rule_revision: 1, blocked_auth_id: 'fixture-quality-legacy' }),
  ];
  // Official hosts can retain historical ownership without claiming pause/release authority.
  qualityActions = [{ status: 'retained_unverified', auth_id: 'fixture-quality-legacy', owner_plugin: 'model-quality',
    rule_id: 'fixture-rule-retained', rule_revision: 1, action_id: 'fixture-action-retained', action_revision: 1,
    evidence_run_id: '', identity_version: 'synthetic-fixture-quality-legacy', created_at: '2026-10-30T08:10:00Z',
    reason: '[离线演示] 历史暂停归属已保留但未经当前宿主验证。官方接口不支持质量暂停或解除；本记录不证明当前凭证已被暂停。' }];
  qualityAudits = [
    { id: 'fixture-audit-observe', time: '2026-11-17T03:05:06Z', kind: 'observation_completed', rule_id: 'fixture-rule-observe', run_id: 'fixture-run-observation', detail: '[离线演示] 观察记录已保存，质量结论未知；未暂停凭证。' },
    { id: 'fixture-audit-settings', time: '2026-11-17T02:00:00Z', kind: 'settings_saved', detail: '[离线演示] 全局调度关闭，自动质量暂停不可用；保存设置未执行模型。' },
    { id: 'fixture-audit-rule', time: '2026-11-16T21:00:00Z', kind: 'rule_saved', rule_id: 'fixture-rule-paused', detail: '[离线演示] 观察规则保持停止，等待明确手动操作。' },
    { id: 'fixture-audit-retained', time: '2026-11-15T08:00:00Z', kind: 'legacy_ownership_retained', rule_id: 'fixture-rule-retained', action_id: 'fixture-action-retained', detail: '[离线演示] 保留历史归属，未自动清理、重放或使用人工禁用模拟质量暂停。' },
  ];
  qualitySettings = { data_dir: 'synthetic-memory-only', workers: 2, queue: 32, max_models: 8, max_samples: 32,
    max_output_tokens: 4096, max_response_bytes: 262144, daily_calls: 200, daily_tokens: 1000000,
    retention_days: 30, max_storage_bytes: 134217728, scheduling_enabled: false, automatic_pause: false,
    capabilities: { mode: 'official_observation', credential_management: true, management_session: true, quality_pause: false, manual_release: false, verified_evidence: false, stream_close: true } };
}
seed();

function pageOf(values, url) {
  const requestedOffset = Number(url.searchParams.get('offset') || 0);
  const requestedLimit = Number(url.searchParams.get('limit') || 50);
  const offset = Number.isSafeInteger(requestedOffset) ? Math.max(0, requestedOffset) : 0;
  const limit = Number.isSafeInteger(requestedLimit) ? Math.min(200, Math.max(1, requestedLimit)) : 50;
  return { items: values.slice(offset, offset + limit), total: values.length, offset, limit, has_more: offset + limit < values.length };
}

function manualRunForDisplay(run, summary) {
  if (run.source !== 'manual_test') return run;
  const sample = run.samples.find(item => item.output);
  let preview;
  if (sample) {
    preview = { model: sample.model, index: sample.index };
    if (run.id === 'fixture-run-visual') preview.kind = 'html';
    else {
      const text = sample.output.replace(/\s+/gu, ' ').trim();
      preview.text = text.slice(0, 96);
      preview.truncated = preview.text.length < text.length;
    }
  }
  if (!summary) return { ...run, answer_preview: preview };
  const { id, source, created_at, status, config, credential_email, error, pruned_samples } = run;
  const samples = run.samples.map(({ model, index, execution_status, quality_verdict, review, answer_match, error }) =>
    ({ model, index, execution_status, quality_verdict, review, answer_match, error }));
  return { id, source, created_at, status, credential_email, error, pruned_samples, samples, answer_preview: preview,
    config: { auth_id: config.auth_id, question_id: config.question_id, question: { id: config.question.id, title: config.question.title },
      models: config.models, samples: config.samples, parameters: { reasoning_effort: config.parameters.reasoning_effort } } };
}

function qualityMetrics() {
  const samples = qualityRuns.flatMap(run => run.samples);
  const completed = samples.filter(sample => sample.execution_status === 'completed');
  return { correct: 0, incorrect: 0, judged: 0, unknown: samples.length, planned: qualityRuns.reduce((n, run) => n + run.config.samples * run.config.models.length, 0), quality_pass_rate: null, coverage: 0,
    known_tokens: samples.reduce((total, sample) => total + (sample.usage?.total_tokens || 0), 0),
    unknown_usage: samples.filter(sample => !sample.usage).length, dropped_usage_events: 0,
    error_types: { error: samples.length - completed.length }, capabilities: qualitySettings.capabilities };
}

function hostNavigationPreview() {
  return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>CLI Proxy API Management Center</title>
<style>body{margin:0;display:grid;grid-template-columns:125px minmax(0,1fr);height:100dvh;font:14px system-ui}nav{padding:12px}nav a{display:block;margin-bottom:8px}iframe{display:block;width:100%;height:100%;border:0}</style></head>
<body><nav aria-label="CPA 导航"><a href="#/auth-files">认证文件</a><a href="#/plugin-pages/model-quality/0">Credential Ops</a></nav><main id="fixture-host-view"></main>
<script>
const container = document.getElementById('fixture-host-view');
function renderRoute() {
  if (location.hash === '#/auth-files') { container.textContent = '认证文件'; return; }
  const frame = document.createElement('iframe');
  frame.id = 'fixture-plugin'; frame.title = 'Credential Ops'; frame.src = '/v0/resource/plugins/model-quality/index.html';
  container.replaceChildren(frame);
}
addEventListener('hashchange', renderRoute); renderRoute();
</script></body></html>`;
}

function embeddedPreview(url) {
  const theme = ['light', 'white', 'dark'].includes(url.searchParams.get('theme')) ? url.searchParams.get('theme') : 'light';
  const page = ['manual', 'operations', 'login'].includes(url.searchParams.get('page')) ? url.searchParams.get('page') : 'login';
  const demo = url.searchParams.get('demo') === '1' ? `<script>localStorage.setItem('model-quality:management-key:${apiPrefix}','${managementKey}')</script>` : '';
  return `<!doctype html><html lang="zh-CN"${theme === 'light' ? '' : ` data-theme="${theme}"`}><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>CLI Proxy API Management Center</title>${demo}
<style>html{--bg:#f2f0ea;--fg:#34352f;--line:#d4d2ca;color-scheme:light}html[data-theme=white]{--bg:#fff;--fg:#252625;--line:#ddd}html[data-theme=dark]{--bg:#232420;--fg:#eeeee7;--line:#484940;color-scheme:dark}*{box-sizing:border-box}body{margin:0;height:100dvh;display:grid;grid-template-rows:auto minmax(0,1fr);background:var(--bg);color:var(--fg);font:13px system-ui}header{display:flex;align-items:center;gap:12px;flex-wrap:wrap;padding:8px 12px;border-bottom:1px solid var(--line)}header strong{margin-right:auto}label{display:flex;gap:6px;align-items:center}select{min-height:32px;padding:3px 6px;border:1px solid var(--line);border-radius:5px;background:var(--bg);color:var(--fg)}iframe{width:100%;height:100%;border:0;min-height:0}small{opacity:.75}</style></head>
<body><header><strong>CPA 插件离线预览</strong><small>合成数据 · 演示密钥 offline-key</small><label>主题<select id="fixture-theme">${['light', 'white', 'dark'].map(value => `<option value="${value}"${value === theme ? ' selected' : ''}>${{ light: '浅色', white: '纯白', dark: '深色' }[value]}</option>`).join('')}</select></label><label>工作区<select id="fixture-page">${['login', 'manual', 'operations'].map(value => `<option value="${value}"${value === page ? ' selected' : ''}>${{ login: '账号管理', manual: '智商测试', operations: '质量观察' }[value]}</option>`).join('')}</select></label></header><iframe id="fixture-plugin" title="Model Quality 离线插件" src="/v0/resource/plugins/model-quality/index.html#${page}"></iframe>
<script>const theme=document.getElementById('fixture-theme');const workspace=document.getElementById('fixture-page');theme.addEventListener('change',()=>{if(theme.value==='light')document.documentElement.removeAttribute('data-theme');else document.documentElement.setAttribute('data-theme',theme.value);const url=new URL(location.href);url.searchParams.set('theme',theme.value);history.replaceState(null,'',url)});workspace.addEventListener('change',()=>{document.getElementById('fixture-plugin').contentWindow.location.hash=workspace.value;const url=new URL(location.href);url.searchParams.set('page',workspace.value);history.replaceState(null,'',url)});</script></body></html>`;
}

const canDeleteJob = job => ['succeeded', 'committed', 'failed', 'cancelled', 'discarded', 'expired', 'interrupted', 'interaction_required', 'conflict'].includes(job.status);
const jobView = job => ({ ...job, can_delete: canDeleteJob(job) });
function accountView(item) {
  const last = jobs.find(job => job.id === item.last_job_id);
  return { ...item,
    effective_proxy_url: item.effective_proxy_url || item.proxy_url || settings.login_proxy_url || 'direct',
    proxy_source: item.proxy_source || (item.proxy_url ? 'account' : settings.login_proxy_url ? 'login_default' : 'direct'),
    ...(last ? { last_job_created_at: last.created_at, last_job_updated_at: last.updated_at, last_job_phase: last.phase, last_job_automatic: last.automatic, last_job_intent: last.intent, last_job_reauthorize: !!last.reauthorize, last_job_logout_all_devices: !!last.logout_all_devices } : {}),
  };
}
const targetFor = item => ({ auth_id: item.auth_id, email: item.email, identity_version: item.identity_version, can_update: !item.security_pending, manual_disabled: false, quality_paused: false });
function send(res, data, status = 200, delay = 0) {
  const payload = JSON.stringify(data);
  const finish = () => {
    if (res.destroyed || res.writableEnded) return;
    res.writeHead(status, { 'Content-Type': 'application/json; charset=utf-8', 'Cache-Control': 'no-store' });
    res.end(payload);
  };
  if (!delay) return finish();
  const timer = setTimeout(() => { timers.delete(timer); finish(); }, delay);
  timers.add(timer);
}
async function readBody(req) {
  let raw = '';
  for await (const chunk of req) { raw += chunk; if (raw.length > 300000) throw new Error('body_too_large'); }
  return raw ? JSON.parse(raw) : {};
}
function updateAccountFromJob(item) {
  const account = accounts.find(account => account.id === item.account_id);
  if (!account) return;
  Object.assign(account, { revision: account.revision + 1, last_job_id: item.id, last_job_status: item.status,
    last_job_error_code: item.error_code || '', last_job_error_stage: item.error_stage || '', last_job_error_reason: item.error_reason || '',
    last_job_auto_commit: !!item.auto_commit, last_job_credential_enabled: !!item.credential_enabled,
    last_job_action: item.action || '', last_job_security_status: item.security_status || '', last_job_totp_reset: !!item.totp_reset, last_job_devices_logged_out: !!item.devices_logged_out,
    attention_required: ['failed', 'security_uncertain', 'awaiting_commit'].includes(item.status), security_pending: item.status === 'security_uncertain' });
  if (item.status === 'committed') Object.assign(account, { plan_type: planType(item.plan_type), auth_id: account.auth_id || `fixture-${account.id}.json`, last_login_at: now(), health_status: 'not_checked', health_reason: 'awaiting_inspection' });
}

function awaitUploadReview(item) {
  Object.assign(item, { status: 'awaiting_commit', phase: 'review', candidate_id: `${item.id}:candidate`, credential_enabled: false,
    error_code: uploadExcluded(item) ? 'cpa_plan_excluded' : '', expires_at: new Date(Date.now() + 5 * 60000).toISOString() });
}

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://127.0.0.1');
    const body = req.method === 'GET' ? {} : await readBody(req);
    if (url.pathname.startsWith('/__fixture/')) {
      if (req.headers['x-fixture-key'] !== controlKey) return send(res, { error: 'fixture_control_unauthorized' }, 401);
      if (url.pathname === '/__fixture/state' && req.method === 'GET') return send(res, { synthetic: true, account_ids: accounts.map(item => item.id), job_count: jobs.length, quality: { target_ids: qualityTargets.map(item => item.auth_id), run_ids: qualityRuns.map(item => item.id), rule_ids: qualityRules.map(item => item.id), mode: qualitySettings.capabilities.mode, execution_enabled: false }, pending_injections: injections, calls });
      if (url.pathname === '/__fixture/stop' && req.method === 'POST') { send(res, { stopped: true }); return setImmediate(stop); }
      if (url.pathname !== '/__fixture/control' || req.method !== 'POST') return send(res, { error: 'not_found' }, 404);
      if (body.action === 'reset') { seed(); return send(res, { reset: true }); }
      if (body.action === 'inject') {
        if (!['failure', 'malformed', 'delay'].includes(body.kind) || typeof body.route !== 'string' || !body.route.startsWith('/')) return send(res, { error: 'invalid_injection' }, 400);
        const item = { route: body.route, kind: body.kind, count: Math.min(10, Math.max(1, Number(body.count) || 1)), status: Number(body.status) === 401 ? 401 : 503, delay_ms: Math.min(30000, Math.max(0, Number(body.delay_ms) || 2500)) };
        injections.push(item); return send(res, { injected: item });
      }
      if (body.action === 'job') {
        const item = jobs.find(job => job.id === body.id);
        if (!item || !['running', 'committed', 'failed', 'cancelled', 'succeeded', 'security_uncertain', 'awaiting_commit'].includes(body.status)) return send(res, { error: 'invalid_job' }, 400);
        Object.assign(item, { status: body.status, phase: body.phase || 'complete', revision: item.revision + 1, updated_at: now(), credential_enabled: body.status === 'committed' });
        if (Object.hasOwn(body, 'plan_type')) item.plan_type = planType(body.plan_type);
        if (body.status === 'awaiting_commit' || body.status === 'committed' && uploadExcluded(item)) awaitUploadReview(item);
        for (const field of ['totp_reset', 'devices_logged_out', 'reauthorize']) if (typeof body[field] === 'boolean') item[field] = body[field];
        updateAccountFromJob(item); return send(res, item);
      }
      return send(res, { error: 'invalid_control_action' }, 400);
    }
    if (!url.pathname.startsWith('/v0/management/')) {
      if (['/embedded.html', '/management.html'].includes(url.pathname) && req.method === 'GET') {
        res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Content-Security-Policy': "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-src 'self'; object-src 'none'; base-uri 'self'" });
        return res.end(url.pathname === '/management.html' ? hostNavigationPreview() : embeddedPreview(url));
      }
      const name = url.pathname === '/' ? 'index.html' : url.pathname.startsWith('/v0/resource/plugins/model-quality/') ? url.pathname.slice('/v0/resource/plugins/model-quality/'.length) : url.pathname.slice(1);
      if (req.method !== 'GET' || !staticFiles.has(name)) return send(res, { error: 'not_found' }, 404);
      res.writeHead(200, { 'Content-Type': name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html; charset=utf-8', 'Cache-Control': 'no-store', 'Content-Security-Policy': "connect-src 'self'; object-src 'none'; base-uri 'self'" });
      return res.end(fs.readFileSync(path.join(root, 'plugin/ui', name)));
    }
    if (req.headers.authorization !== `Bearer ${managementKey}`) return send(res, { code: 'unauthorized' }, 401);
    if (!url.pathname.startsWith(apiPrefix + '/')) return send(res, { code: 'not_found' }, 404);
    const route = url.pathname.startsWith(loginPrefix) ? url.pathname.slice(loginPrefix.length) : url.pathname.slice(apiPrefix.length);
    calls.push({ method: req.method, route, query: Object.fromEntries(url.searchParams), at: now() });
    if (calls.length > 1000) calls.shift();
    let delay = 0;
    if (req.method === 'GET') {
      const injection = injections.find(item => item.route === route && item.count > 0);
      if (injection) {
        injection.count--;
        if (injection.kind === 'failure') return send(res, { code: 'storage_unavailable' }, injection.status);
        if (injection.kind === 'malformed') return send(res, route === '/accounts/settings' ? { revision: 'invalid', inspection_enabled: 'invalid' } : { items: [{ id: 'malformed-fixture-record' }] });
        delay = injection.delay_ms;
      }
    }
    const reply = (data, status = 200) => send(res, data, status, delay);
    if (!url.pathname.startsWith(loginPrefix + '/')) {
      if (route === '/connection') {
        if (req.method === 'GET') return reply(connection);
        if (body.revision !== connection.revision) return reply({ code: 'connection_conflict' }, 409);
        if (req.method === 'PUT' && ['configure', 'migrate'].includes(body.mode)) {
          if (body.mode === 'migrate' && !connection.can_migrate) return reply({ code: 'connection_conflict' }, 409);
          connection = { revision: connection.revision + 1, state: 'ready', configured: true, can_migrate: false, source: 'saved' };
        } else if (req.method === 'DELETE') {
          connection = { revision: connection.revision + 1, state: 'unconfigured', configured: false, can_migrate: false };
        } else return reply({ code: 'invalid_request' }, 400);
        return reply(connection);
      }
      if (req.method === 'POST' && route === '/rules/scope-preview') {
        const selected = qualityTargets.filter(t => body.object_types?.includes('oauth') &&
          (body.target_scope === 'single' ? t.auth_id === body.auth_id : body.target_scope === 'all_credentials' || !t.manual_disabled));
        const blocked = selected.filter(t => t.retained_pause);
        return reply({verified:true,candidates:{oauth:selected.length,provider:0},count:selected.length-blocked.length,
          skipped:blocked.length,recovery_count:0,skip_details:blocked.map(t => ({auth_id:t.auth_id,code:'quality_paused',reason:'历史独立质量暂停。'}))});
      }
      if (req.method !== 'GET') return reply({ code: 'fixture_model_execution_disabled', error: '离线视觉 fixture 禁止执行模型、规则及质量管理写入。请从历史记录查看预置合成回答。' }, 409);
      if (route === '/targets') return reply(pageOf(qualityTargets, url));
      if (route === '/questions') return reply(pageOf(questions, url));
      if (route === '/rules/question-preview') {
        const q = questions.find(q => q.kind === 'logic' && q.id === url.searchParams.get('id'));
        return q ? reply({...q, answer:'合成参考答案'}) : reply({code:'not_found'}, 404);
      }
      if (route === '/settings') return reply(qualitySettings);
      if (route === '/runs') {
        const rows = qualityRuns.filter(run => (!url.searchParams.get('source') || run.source === url.searchParams.get('source')) && (!url.searchParams.get('rule_id') || run.config.rule_id === url.searchParams.get('rule_id')) && (!url.searchParams.get('scope_batch_id') || run.config.scope_batch_id === url.searchParams.get('scope_batch_id'))).sort((a, b) => b.created_at.localeCompare(a.created_at));
        const result = pageOf(rows, url);
        result.items = result.items.map(row => manualRunForDisplay(row, url.searchParams.get('view') === 'summary'));
        return reply(result);
      }
      if (route === '/runs/detail') {
        const run = qualityRuns.find(run => run.id === url.searchParams.get('id'));
        return run ? reply(manualRunForDisplay(run, false)) : reply({ code: 'not_found' }, 404);
      }
      if (route === '/rules') return reply(pageOf(qualityRules, url));
      if (route === '/actions') return reply(pageOf(qualityActions, url));
      if (route === '/metrics') return reply(qualityMetrics());
      if (route === '/history') return reply(pageOf(qualityAudits, url));
      if (route === '/presets') return reply(pageOf([], url));
      return reply({ code: 'not_found' }, 404);
    }
    if (req.method !== 'GET') {
      const key = req.headers['idempotency-key'];
      if (!key) return reply({ code: 'idempotency_key_required' }, 400);
      const signature = JSON.stringify([req.method, route, body]);
      if (receipts.has(key)) {
        const stored = receipts.get(key);
        return stored.signature === signature ? reply(stored.data, stored.status) : reply({ code: 'idempotency_conflict' }, 409);
      }
      const original = reply;
      // Fixture mutations are not retried automatically. Store their final response below.
      res.fixtureReceipt = (data, status = 200) => { receipts.set(key, { signature, data: clone(data), status }); return original(data, status); };
    }
    const respond = req.method === 'GET' ? reply : res.fixtureReceipt;
    const account = accounts.find(item => item.id === (body.id || url.searchParams.get('id')));
    const job = jobs.find(item => item.id === (body.job_id || url.searchParams.get('id')));
    if (route === '/capabilities') return respond({ available: true, credential_conversion: true, automatic_commit: true, commit_mode: 'account_auto_commit', security_actions: { reset_totp: true, logout_all: true }, methods: { browser: { available: true }, password_totp: { available: true } }, runtime: { engine: 'go_sobek_protocol', runtime_version: '2.1.1', schema_version: '2' }, vault: { available: true, initialized: true, storage_mode: 'plaintext' } });
    if (route === '/vault/status') return respond({ available: true, initialized: true, storage_mode: 'plaintext' });
    if (route === '/targets') return respond({ items: accounts.filter(item => item.auth_id).map(targetFor) });
    if (route === '/accounts') return respond({ items: accounts.map(accountView) });
    if (route === '/accounts/detail') return account ? respond({ account: accountView(account), credentials: credentials.get(account.id), security_recovery: recoveries.get(account.id) }) : respond({ code: 'not_found' }, 404);
    if (route === '/accounts/settings') {
      if (req.method === 'PUT') {
        if (body.revision !== settings.revision) return respond({ code: 'revision_conflict' }, 409);
        const exclusions = Object.hasOwn(body, 'cpa_upload_excluded_plans') ? excludedPlans(body.cpa_upload_excluded_plans) : settings.cpa_upload_excluded_plans;
        if (exclusions === null) return respond({ code: 'invalid_request' }, 400);
        settings = { ...settings, ...body, cpa_upload_excluded_plans: exclusions, revision: settings.revision + 1 };
      }
      return respond(settings);
    }
    if (route === '/jobs') {
      if (req.method === 'DELETE') {
        if (!job) return respond({code: 'not_found'}, 404);
        if (!canDeleteJob(job) || body.revision !== job.revision) return respond({code: 'conflict'}, 409);
        jobs = jobs.filter(item => item !== job);
        logs.delete(job.id);
        const owner = accounts.find(item => item.last_job_id === job.id);
        if (owner) {
          const previous = jobs.filter(item => item.account_id === owner.id).sort((a, b) => b.created_at.localeCompare(a.created_at))[0];
          Object.assign(owner, {
            last_job_id: previous?.id || '', last_job_status: previous?.status || '',
            last_job_error_code: previous?.error_code || '', last_job_error_reason: previous?.error_reason || '',
            last_job_error_stage: previous?.error_stage || '', last_job_action: previous?.action || '',
          });
        }
        return respond({deleted: true});
      }
      const accountID = url.searchParams.get('account_id');
      const limit = Math.min(200, Math.max(1, Number(url.searchParams.get('limit')) || 200));
      const scoped = jobs.filter(item => !accountID || item.account_id === accountID).sort((a, b) => b.created_at.localeCompare(a.created_at));
      return respond({ items: scoped.slice(0, limit).map(jobView), has_more: scoped.length > limit });
    }
    if (route === '/detail') return job ? respond(jobView(job)) : respond({ code: 'not_found' }, 404);
    if (route === '/candidate') {
      const item = jobs.find(item => item.candidate_id === url.searchParams.get('id') || item.id === url.searchParams.get('id'));
      if (!item || item.status !== 'awaiting_commit') return respond({ code: 'not_found' }, 404);
      return respond({ id: item.candidate_id, job_id: item.id, revision: item.revision, email: item.email, account_id: `synthetic-${item.account_id}`, user_id: 'synthetic-user', plan_type: item.plan_type,
        target_auth_id: accounts.find(account => account.id === item.account_id)?.auth_id || '', intent: item.intent, restrictions: uploadExcluded(item) ? ['cpa_plan_excluded'] : [] });
    }
    if (route === '/logs') return job ? respond(logs.get(job.id) || { job_id: job.id, entries: [], legacy: false }) : respond({ code: 'not_found' }, 404);
    if (route === '/accounts/import') {
      if (!Array.isArray(body.entries) || !body.entries.length || body.entries.length > 100 || body.entries.some(item => !String(item.email).endsWith('@example.test'))) return respond({ code: 'invalid_credentials' }, 400);
      const seen = new Set(accounts.map(item => item.email.toLowerCase()));
      for (const item of body.entries) { if (seen.has(item.email.toLowerCase())) return respond({ code: 'duplicate_account' }, 409); seen.add(item.email.toLowerCase()); }
      const imported = body.entries.map(entry => ({ id: `fixture-import-${++sequence}`, revision: 1, email: entry.email, plan_type: 'unknown', label: '[离线演示] 新导入', proxy_url: body.proxy_url || '', auth_id: '', health_status: 'not_linked', health_reason: 'login_required', auto_relogin: false, attention_required: false, security_pending: false, last_job_id: '', last_job_status: '' }));
      imported.forEach((item, index) => { accounts.push(item); credentials.set(item.id, clone(body.entries[index])); });
      return respond({ items: imported.map(accountView) });
    }
    if (route === '/accounts/inspect') {
      const selected = accounts.filter(item => !body.id || item.id === body.id);
      selected.forEach(item => { item.last_checked_at = now(); item.revision++; });
      return respond({ items: selected.map(accountView) });
    }
    if (route === '/accounts/entry') {
      if (!account || account.revision !== body.revision) return respond({ code: 'revision_conflict' }, 409);
      if (req.method === 'DELETE') { accounts = accounts.filter(item => item.id !== account.id); credentials.delete(account.id); return respond({ deleted: true }); }
      if (body.credentials && body.credentials.email !== account.email) return respond({ code: 'identity_conflict' }, 409);
      Object.assign(account, { label: body.label || '', proxy_url: body.proxy_url || '', auto_relogin: !!body.auto_relogin, revision: account.revision + 1 });
      if (body.credentials) credentials.set(account.id, clone(body.credentials));
      return respond(accountView(account));
    }
    if (route === '/accounts/start' || route === '/accounts/security') {
      if (!account || account.revision !== body.revision) return respond({ code: 'revision_conflict' }, 409);
      if (account.security_pending || active.has(account.last_job_status)) return respond({ code: 'account_busy' }, 409);
      const item = { id: `fixture-job-${++sequence}`, revision: 1, account_id: account.id, email: account.email, plan_type: account.plan_type, method: 'password_totp', intent: account.auth_id ? 'reauthorize' : 'import', automatic: false, status: 'running', phase: route.endsWith('/security') ? 'security_login' : 'totp', auto_commit: body.upload_to_cpa ?? (route.endsWith('/start') || !!body.reauthorize), enable_credential: true, credential_enabled: false, action: body.action || '', reauthorize: !!body.reauthorize, logout_all_devices: !!body.logout_all_devices, created_at: now(), updated_at: now() };
      jobs.push(item); updateAccountFromJob(item);
      logs.set(item.id, { job_id: item.id, legacy: false, entries: [{ id: 1, time: item.created_at, level: 'info', status: item.status, phase: item.phase }] });
      return respond(item);
    }
    if (route === '/commit' || route === '/discard') {
      if (!job || job.revision !== body.revision) return respond({ code: 'revision_conflict' }, 409);
      if (job.status !== 'awaiting_commit' || route === '/commit' && body.candidate_id !== job.candidate_id) return respond({ code: 'conflict' }, 409);
      if (route === '/commit' && Date.parse(job.expires_at) <= Date.now()) return respond({ code: 'expired' }, 409);
      if (route === '/commit' && uploadExcluded(job)) return respond({ code: 'cpa_plan_excluded' }, 409);
      Object.assign(job, { status: route === '/commit' ? 'committed' : 'discarded', phase: 'complete', error_code: '', revision: job.revision + 1, updated_at: now(), credential_enabled: route === '/commit' });
      updateAccountFromJob(job);
      return respond(job);
    }
    if (route === '/cancel') {
      if (!job || job.revision !== body.revision) return respond({ code: 'revision_conflict' }, 409);
      if (!['queued', 'running', 'waiting_browser', 'exchanging', 'validating'].includes(job.status)) return respond({ code: 'conflict' }, 409);
      Object.assign(job, { status: 'cancelled', phase: 'cancelled', revision: job.revision + 1, updated_at: now() }); updateAccountFromJob(job); return respond(job);
    }
    return respond({ code: 'not_found' }, 404);
  } catch (error) { send(res, { code: error instanceof SyntaxError ? 'invalid_request' : 'fixture_error' }, 400); }
});

function stop() {
  for (const timer of timers) clearTimeout(timer);
  timers.clear();
  server.close(() => process.exit(0));
  server.closeAllConnections();
}
process.on('SIGTERM', stop);
process.on('SIGINT', stop);
server.listen(requestedPort, '127.0.0.1', () => {
  const port = server.address().port;
  console.log(JSON.stringify({ fixture: 'synthetic-plugin-ui', pid: process.pid, url: `http://127.0.0.1:${port}/#login`, manual_url: `http://127.0.0.1:${port}/#manual`, operations_url: `http://127.0.0.1:${port}/#operations`, embedded_url: `http://127.0.0.1:${port}/embedded.html`, management_key: managementKey, control_key: controlKey, control_url: `http://127.0.0.1:${port}/__fixture/control`, stop_url: `http://127.0.0.1:${port}/__fixture/stop`, state: 'memory-only', model_execution: 'disabled', account_ids: accounts.map(item => item.id), quality_run_ids: qualityRuns.map(item => item.id) }));
});
