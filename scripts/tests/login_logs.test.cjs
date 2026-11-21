const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../../plugin/ui/login.js'), 'utf8')
  .replace(/^import .*;$/gm, '').replace(/^export /gm, '');
const context = vm.createContext({ URL, Set, Map, Error });
const viewSource = fs.readFileSync(path.join(__dirname, '../../plugin/ui/view.js'), 'utf8');
const markup = fs.readFileSync(path.join(__dirname, '../../plugin/ui/markup.js'), 'utf8');
const viewHelpers = markup.slice(0, markup.indexOf('export const previewLimit'))
  + viewSource.slice(viewSource.indexOf('export const date'), viewSource.indexOf('export const duration'));
new vm.Script(viewHelpers.replace(/^export /gm, '') + source + '\nthis.logs = { logEntrySummary, filterLogEntries, logInterval, logTiming, logRetention, buildLogReport, logReportText, diagnosticAdvice, projectLogEntry, renderLogEntries, filterTaskJobs };').runInContext(context);
const { logEntrySummary, filterLogEntries, logInterval, logTiming, logRetention, buildLogReport, logReportText, diagnosticAdvice, projectLogEntry, renderLogEntries, filterTaskJobs } = context.logs;
const base = { id: 'offline-job', revision: 4, method: 'password_totp', intent: 'reauthorize', status: 'committed', credential_enabled: true, created_at: '2026-11-17T00:00:00.000Z' };
const event = (extra = {}) => ({ id: 1, time: base.created_at, level: 'info', status: 'queued', phase: 'queued', ...extra });
const plain = value => JSON.parse(JSON.stringify(value));

test('CPA status details never expand historical login errors', () => {
  for (const code of ['invalid_callback', 'credentials_rejected', 'totp_rejected', 'runtime_timeout', 'network_error', 'exchange_failed', 'rate_limited', 'queue_full', 'interaction_required', 'backoff', 'retry_exhausted']) {
    assert.equal(vm.runInContext(`healthReason(${JSON.stringify(code)})`, context), '', code);
  }
  assert.match(vm.runInContext('healthReason("manual_disabled")', context), /跳过自动重新授权/);
  assert.match(vm.runInContext('healthReason("identity_changed")', context), /绑定身份/);
  assert.match(vm.runInContext('healthReason("security_uncertain")', context), /安全操作待核对/);
});

test('historical events do not inherit final security and enablement results', () => {
  const final = { ...base, action: 'reset_totp', totp_reset: true, devices_logged_out: true };
  const changing = { ...final, totp_reset: false, devices_logged_out: false, credential_enabled: false };
  for (const entry of [event(), event({ status: 'running', phase: 'totp_activate' }), event({ status: 'failed', phase: 'logout_all' }), event({ status: 'committed', phase: '' })]) {
    assert.deepEqual(plain(logEntrySummary(entry, final)), plain(logEntrySummary(entry, changing)));
    assert.doesNotMatch(JSON.stringify(logEntrySummary(entry, final)), /已保存并启用|后续步骤未完成|新 2FA 已保存|已退出所有设备/);
  }
  assert.equal(logEntrySummary(event({ status: 'running', phase: 'totp_activate' }), final).title, '激活新 2FA');
});

test('error summaries preserve stopped stage and concrete diagnostic reason', () => {
  const summary = logEntrySummary(event({ level: 'error', status: 'failed', phase: '', error_code: 'network_error', error_stage: 'session', error_reason: 'proxy_dns_failed' }), base);
  assert.match(summary.title, /代理地址无法解析/);
  assert.match(summary.detail, /连接登录服务/);
});

test('callback failures show binding diagnostics without manual callback instructions', () => {
  for (const [reason, text] of [
    ['callback_url_invalid', /回调地址/],
    ['callback_parameters_invalid', /关键参数重复/],
    ['callback_state_mismatch', /state.*不匹配/],
    ['callback_code_invalid', /有效授权码/],
    ['callback_authorization_denied', /拒绝/],
  ]) {
    const summary = logEntrySummary(event({level:'error',status:'failed',error_code:'invalid_callback',error_stage:'callback',error_reason:reason}), base);
    assert.match(summary.title, text);
    assert.doesNotMatch(summary.title, /复制完整回调/);
  }
  const legacy = logEntrySummary(event({level:'error',status:'failed',error_code:'invalid_callback',error_stage:'callback'}), base);
  assert.match(legacy.title, /OAuth 回调校验未通过/);
  assert.doesNotMatch(legacy.title, /复制完整回调/);
});

test('search matches translated text and raw codes with intersecting terms', () => {
  const entries = [event(), event({ id: 2, status: 'running', phase: 'password' }), event({ id: 3, level: 'error', status: 'failed', error_stage: 'session', error_code: 'network_error', error_reason: 'proxy_dns_failed' })];
  assert.deepEqual(Array.from(filterLogEntries(entries, base, 'all', '代理地址 DNS'), item => item.id), [3]);
  assert.deepEqual(Array.from(filterLogEntries(entries, base, 'all', 'PASSWORD'), item => item.id), [2]);
  assert.equal(filterLogEntries(entries, base, 'all', '代理 password').length, 0);
  assert.deepEqual(Array.from(filterLogEntries(entries, base, 'errors', ''), item => item.id), [3]);
  assert.equal(filterLogEntries(entries, base, 'errors', 'password').length, 0);
});

test('legacy informational upload exclusions remain visible in abnormal filter', () => {
  const excluded = event({ status: 'awaiting_commit', phase: 'review', error_code: 'cpa_plan_excluded' });
  assert.equal(filterLogEntries([excluded], base, 'errors').length, 1);
});

test('event intervals never invent durations for invalid or reversed timestamps', () => {
  assert.equal(logInterval(base.created_at, '2026-11-17T00:00:00.125Z'), '125 ms');
  assert.equal(logInterval(base.created_at, '2026-11-17T00:00:02.500Z'), '2.50 s');
  assert.equal(logInterval(base.created_at, '2026-11-17T00:01:02.500Z'), '1m 2.5s');
  assert.equal(logInterval(base.created_at, base.created_at), '0 ms');
  assert.equal(logInterval('invalid', base.created_at), '');
  assert.equal(logInterval(base.created_at, '2026-11-16T00:00:00Z'), '');
  assert.equal(logTiming([]), '');
  assert.equal(logTiming([event()]), '');
});

test('legacy and truncated logs disclose independent completeness limitations', () => {
  assert.match(logRetention({ legacy: true, entries: [event()] }), /早期任务/);
  const retained = logRetention({ legacy: true, truncated: true, entries: [event({ id: 17 })] });
  assert.match(retained, /早期任务/);
  assert.match(retained, /#17/);
  assert.match(retained, /200/);
  assert.equal(logRetention({ legacy: false, truncated: false }), '');
});

test('reports omit secrets and unnecessary identities from jobs, nested extras and event extras', () => {
  const canary = 'PRIVATE-CANARY-DO-NOT-EXPORT';
  const job = { ...base, email: canary, masked_email: canary, account_id: canary, target_auth_id: canary, password: canary, totp_secret: canary, authorization_url: `https://example.invalid/?code=${canary}`, credentials: { password: canary }, runtime: { token: canary } };
  const entries = [event({ raw: { access_token: canary }, credentials: { totp_secret: canary }, authorization_url: canary }), event({ id: 2, time: '2026-11-17T00:00:03Z', status: 'committed', phase: '' })];
  const report = buildLogReport([job], new Map([[job.id, { entries, revision: job.revision }]]), { ui_version: '11.17.0', runtime_version: '2.1.2', filter: 'errors', query: 'nothing' });
  assert.doesNotMatch(JSON.stringify(report), new RegExp(canary));
  assert.doesNotMatch(logReportText(report), new RegExp(canary));
  assert.equal(report.jobs[0].events.length, 2, 'export remains complete when the display is filtered');
  assert.equal(report.jobs[0].id, job.id);
  assert.equal(report.jobs[0].credential_enabled, true);
  assert.equal(report.jobs[0].recorded_span, '3.00 s');
});

test('malformed nested values in allowlisted fields cannot bypass report projection', () => {
  const canary = 'NESTED-SECRET-CANARY';
  const job = { ...base, security_status: { token: canary }, automatic: [canary] };
  const entries = [event({ phase: { password: canary }, error_reason: { token: canary } })];
  const report = buildLogReport([job], new Map([[job.id, { entries, revision: job.revision }]]), { ui_version: '11.17.0', runtime_version: '2.1.2' });
  assert.doesNotMatch(JSON.stringify(report), new RegExp(canary));
  assert.doesNotMatch(logReportText(report), new RegExp(canary));
});

test('reports identify partial reads, missing logs and retained ranges without claiming total duration', () => {
  const entries = [event({ id: 10 }), event({ id: 11, time: '2026-11-17T00:00:01Z' })];
  const report = buildLogReport([base], new Map([[base.id, { entries, revision: base.revision, legacy: true, truncated: true }]]), { read_incomplete: true, history_limited: true });
  const job = report.jobs[0];
  assert.equal(report.read_incomplete, true);
  assert.equal(report.history_limited, true);
  assert.equal(job.first_event_id, 10);
  assert.equal(job.last_event_id, 11);
  assert.equal(job.legacy, true);
  assert.equal(job.truncated, true);
  assert.match(logReportText(report), /读取尚未完成/);
  assert.match(logReportText(report), /开头已截断/);
  const unloaded = buildLogReport([base], new Map()).jobs[0];
  assert.equal(unloaded.log_loaded, false);
  assert.equal(unloaded.recorded_span, null);
});

test('unknown writes and completed security actions take priority over network retry advice', () => {
  const network = { error_code: 'network_error', error_reason: 'proxy_dns_failed' };
  for (const status of ['commit_unknown', 'saved_not_adopted']) {
    assert.match(diagnosticAdvice({ ...network, status }), /勿重复登录或导入/);
  }
  assert.match(diagnosticAdvice({ ...network, status: 'security_uncertain', action: 'reset_totp' }), /未确认的修改不会自动重试/);
  assert.match(diagnosticAdvice({ ...network, status: 'failed', action: 'reset_totp', totp_reset: true }), /不要为了重新授权再次重置/);
  const acceptedLogout = diagnosticAdvice({ ...network, status: 'failed', action: 'logout_all', devices_logged_out: true });
  assert.match(acceptedLogout, /服务器已接受.*会话失效可能有延迟/);
  assert.match(acceptedLogout, /仅处理失败的后续授权/);
  assert.match(diagnosticAdvice({ ...network, status: 'failed' }), /代理服务器地址解析失败/);
});

test('request diagnostics distinguish an HTTP rejection from the final task outcome', () => {
  const request = event({ event: 'request', request_kind: 'password_verify', method: 'POST', http_status: 502, duration_ms: 1250, phase: 'password', status: 'running' });
  const summary = logEntrySummary(request, base);
  assert.equal(summary.title, '提交密码验证');
  assert.match(summary.detail, /HTTP 502（网关错误）/);
  assert.match(summary.detail, /验证账号密码/);
  assert.doesNotMatch(summary.title + summary.detail, /已保存|登录失败/);
  assert.equal(filterLogEntries([request], base, 'errors', 'POST 502 密码').length, 1);
  assert.equal(filterLogEntries([request], base, 'errors', '503').length, 0);
});

test('post-reset gateway timeout distinguishes saved changes, authorization and upload', () => {
  const job = { ...base, status: 'failed', action: 'reset_totp', security_status: 'complete', reauthorize: true, auto_commit: true, credential_enabled: false, totp_reset: true, logout_all_devices: true, devices_logged_out: true, error_code: 'runtime_failed', runtime_code: 'service_error', error_stage: 'session', http_status: 504 };
  const terminal = event({ event: 'state', status: 'failed', revision: job.revision, error_code: job.error_code, runtime_code: job.runtime_code, error_stage: job.error_stage });
  const cache = new Map([[job.id, { revision: job.revision, entries: [terminal] }]]);
  const text = logReportText(buildLogReport([job], cache));
  assert.match(text, /重新授权时，认证服务返回 HTTP 504（网关超时）/);
  assert.match(text, /新 2FA 已保存，退出设备请求已获接受/);
  assert.doesNotMatch(text, /检查插件安装|原因：未记录/);
  const progress = vm.runInContext(`securityProgress(${JSON.stringify(job)})`, context);
  assert.match(progress, /重登授权<\/span><strong[^>]*>失败/);
  assert.match(progress, /上传 CPA<\/span><strong[^>]*>未开始/);
  assert.equal(logEntrySummary(terminal, job).title, 'HTTP 504（网关超时）');
  assert.doesNotMatch(logEntrySummary({ ...terminal, revision: job.revision - 1 }, job).title, /504/);
});

test('transport failure remains abnormal even after successful HTTP headers', () => {
  const request = event({ event: 'request', request_kind: 'auth_session', http_status: 200, failed: true, error_reason: 'network_timeout', status: 'running', phase: 'session' });
  assert.match(logEntrySummary(request, base).detail, /超时/);
  assert.equal(filterLogEntries([request], base, 'errors').length, 1);
  assert.equal(filterLogEntries([event({ event: 'request', http_status: 200, failed: false })], base, 'errors').length, 0);
});

test('request projection keeps numeric diagnostics and omits arbitrary payloads and malformed fields', () => {
  const canary = 'PRIVATE-REQUEST-CANARY';
  const projected = projectLogEntry({
    ...event(), event: 'request', revision: 8, request_kind: 'totp_verify', method: 'POST', http_status: 200,
    duration_ms: 0, failed: false, totp_reset: false, devices_logged_out: true, credential_enabled: false,
    runtime_code: 'transport_error', url: `https://example.test/?token=${canary}`, headers: { authorization: canary }, body: canary,
    response: { token: canary }, password: canary, totp_secret: canary, raw: canary,
  });
  assert.equal(projected.duration_ms, 0);
  assert.equal(projected.failed, false);
  assert.equal(projected.devices_logged_out, true);
  assert.equal(projected.revision, 8);
  assert.equal(projected.runtime_code, 'transport_error');
  assert.doesNotMatch(JSON.stringify(projected), new RegExp(canary));
  const malformed = projectLogEntry({ id: -1, revision: 1.5, http_status: '200', duration_ms: NaN, failed: 'false', totp_reset: { secret: canary }, request_kind: [canary], runtime_code: { token: canary } });
  assert.deepEqual(plain(malformed), {});
});

test('expanded events preserve safe diagnostic facts and escape markup', () => {
  const entries = [event(), event({ id: 2, time: '2026-11-17T00:00:01.250Z', event: 'request', phase: 'password', status: 'running', request_kind: 'password_verify', method: 'POST', http_status: 502, duration_ms: 0, failed: false, totp_reset: false, runtime_code: '<img src=x onerror=alert(1)>', raw: { token: 'SHOULD-NOT-RENDER' } })];
  const html = renderLogEntries([entries[1]], entries, base, new Set(['offline-job:2']));
  assert.match(html, /data-log-key="offline-job:2" open/);
  assert.match(html, /\+1\.25 s/);
  assert.match(html, /HTTP 502/);
  assert.match(html, /0 ms/);
  assert.match(html, /2FA 已保存/);
  assert.match(html, /false/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<img|SHOULD-NOT-RENDER/);
});

test('export retains structured request fields and discloses capped request details', () => {
  const job = { ...base, runtime_code: 'transport_error', updated_at: '2026-11-17T00:00:04Z' };
  const entries = [event({ event: 'request', revision: 2, request_kind: 'auth_session', method: 'GET', http_status: 200, duration_ms: 740, failed: true, error_reason: 'network_timeout' }), event({ id: 2, event: 'request_log_limit', level: 'warn', time: '2026-11-17T00:00:04Z' })];
  const report = buildLogReport([job], new Map([[job.id, { entries, revision: job.revision, retained_count: 2 }]]), { query: 'does-not-match' });
  assert.equal(report.schema_version, 2);
  assert.equal(report.jobs[0].runtime_code, 'transport_error');
  assert.equal(report.jobs[0].events[0].duration_ms, 740);
  assert.equal(report.jobs[0].events[0].failed, true);
  assert.equal(report.jobs[0].events.length, 2);
  assert.equal(report.jobs[0].request_details_limited, true);
  assert.match(logReportText(report), /请求明细已达到记录上限/);
  assert.match(logRetention({ entries }), /128/);
});

test('reports mark a cached log revision as incomplete when polling advances the task', () => {
  const cached = { entries: [event({ revision: 3 })], revision: 3 };
  const report = buildLogReport([base], new Map([[base.id, cached]]));
  assert.equal(report.jobs[0].revision, 4);
  assert.equal(report.jobs[0].log_revision, 3);
  assert.equal(report.jobs[0].log_matches_job_revision, false);
  assert.equal(report.read_incomplete, true);
  assert.match(logReportText(report), /读取尚未完成/);
  const current = buildLogReport([base], new Map([[base.id, { ...cached, revision: 4 }]]));
  assert.equal(current.jobs[0].log_matches_job_revision, true);
  assert.equal(current.read_incomplete, false);
});

test('task search intersects terms while retaining separate active and attention filters', () => {
  const jobs = [
    { ...base, id: 'complete', email: 'alice@example.test' },
    { ...base, id: 'active', email: 'bob@example.test', status: 'running', phase: 'password' },
    { ...base, id: 'uncertain', email: 'alice@example.test', action: 'reset_totp', status: 'security_uncertain', error_code: 'security_outcome_unknown' },
    { ...base, id: 'failed', email: 'charlie@example.test', status: 'failed', error_code: 'network_error', runtime_code: 'transport_error' },
  ];
  assert.deepEqual(Array.from(filterTaskJobs(jobs, 'active'), job => job.id), ['active']);
  assert.deepEqual(Array.from(filterTaskJobs(jobs, 'issues'), job => job.id), ['uncertain', 'failed']);
  assert.deepEqual(Array.from(filterTaskJobs(jobs, 'issues', 'ALICE 2FA'), job => job.id), ['uncertain']);
  assert.deepEqual(Array.from(filterTaskJobs(jobs, 'all', 'transport_error'), job => job.id), ['failed']);
  assert.equal(filterTaskJobs(jobs, 'active', 'alice').length, 0);
});
