// Historical three-tab account UI fixture, before the UI 13.5 dialog layout.
// It preserves the old export contract and explicit historical task buttons.
// Supply a matching historical build; current releases use verify_login_ui.cjs.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const crypto = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const root = path.resolve(__dirname, '..');
if (!process.env.LEGACY_LOGIN_UI_DIR) throw Error('Provide the matching historical three-tab UI in LEGACY_LOGIN_UI_DIR.');
const ui = path.resolve(process.env.LEGACY_LOGIN_UI_DIR);
const assets = ['index.html', 'style.css', 'client.js', 'app.js', 'view.js', 'login.js'];
const sourceHashes = () => Object.fromEntries(assets.map(name => [name, crypto.createHash('sha256').update(fs.readFileSync(path.join(ui, name))).digest('hex')]));
const source = sourceHashes();
const output = process.env.LOGIN_UI_OUTPUT || path.join(root, 'artifacts/account-focused-8.9.0/login-ui');
fs.mkdirSync(output, { recursive: true });
const calls = [], jobs = [], checks = [], screenshots = [], heldReads = new Set();
let unauthorizedNext = false, rejectNextAction = false, capabilitiesGate = null, qualityGate = null, nextJobsFailure = null, nextDetailRead = null;
const target = { auth_id: 'offline-codex', auth_index: 'offline', email: 'fixture@example.test', account_id: 'acct_fixture_complete', user_id: 'user_fixture_complete', masked_email: 'f***@example.test', identity_version: 'identity-fixture', can_update: true, quality_paused: true, manual_disabled: true };
const seed = (id, status, method = 'password_totp') => {
  const job = { id, revision: 1, method, intent: 'reauthorize', email: target.email, masked_email: target.masked_email, status, phase: status === 'waiting_browser' ? 'authorization' : 'review', candidate_id: `${id}:candidate`, created_at: '2026-11-16T00:00:00Z', ...(method === 'browser' ? { authorization_url: 'https://auth.openai.com/oauth/authorize?state=offline-state&code_challenge=offline-challenge' } : {}) };
  jobs.push(job); return job;
};
const candidate = seed('candidate-existing', 'awaiting_commit');
const json = (res, value, status = 200) => { res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' }); res.end(JSON.stringify(value)); };
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (!url.pathname.startsWith('/v0/management/')) {
    const name = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
    if (!assets.includes(name)) { res.writeHead(404); res.end(); return; }
    res.writeHead(200, { 'Content-Type': name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html' });
    res.end(fs.readFileSync(path.join(ui, name))); return;
  }
  let raw = ''; for await (const chunk of req) raw += chunk;
  const body = raw ? JSON.parse(raw) : null;
  calls.push({ path: url.pathname, query: url.search, method: req.method, body, key: req.headers['idempotency-key'] });
  if (req.headers.authorization !== 'Bearer offline-management-key' || unauthorizedNext) { unauthorizedNext = false; json(res, { code: 'unauthorized' }, 401); return; }
  if (url.pathname.startsWith('/v0/management/plugins/model-quality') && !url.pathname.startsWith('/v0/management/plugins/model-quality/login/')) {
    if (qualityGate) await qualityGate;
    const suffix = url.pathname.split('model-quality')[1];
    if (suffix === '/questions') json(res, { items: [{ id: 'custom', kind: 'custom', title: '自定义题目', prompt: '' }] });
    else if (suffix === '/targets') json(res, { items: [{ ...target, provider: 'codex', type: 'oauth', models: ['offline-model'], reasons: ['quality_paused'], effective_available: false }] });
    else json(res, { items: [], total: 0 });
    return;
  }
  const suffix = url.pathname.replace('/v0/management/plugins/model-quality/login', '');
  const job = jobs.find(item => item.id === (body?.job_id || url.searchParams.get('id')?.replace(':candidate', '')));
  if (req.method !== 'GET') assert(req.headers['idempotency-key'], 'Every login write carries an idempotency key');
  if (rejectNextAction && req.method !== 'GET') { rejectNextAction = false; json(res, { code: 'revision_conflict', message: 'DO_NOT_RENDER_SERVER_SECRET' }, 409); return; }
  if (suffix === '/capabilities') {
    if (capabilitiesGate) await capabilitiesGate;
    json(res, { available: true, automatic_commit: true, commit_mode: 'account_auto_commit', runtime: { engine: 'go_sobek_protocol', runtime_version: '2.1.0', schema_version: '2' }, methods: { browser: { available: true }, password_totp: { available: true } }, vault: { available: true, initialized: true, storage_mode: 'plaintext' } });
  } else if (suffix === '/targets') json(res, { items: [target] });
  else if (suffix === '/accounts') json(res, { items: [] });
  else if (suffix === '/accounts/settings') json(res, { revision: 1, interval_minutes: 15, inspection_enabled: false, auto_relogin_enabled: false, login_proxy_url: '' });
  else if (suffix === '/vault/status') json(res, { available: true, initialized: true, storage_mode: 'plaintext' });
  else if (suffix === '/jobs') {
    const failure = nextJobsFailure; nextJobsFailure = null;
    if (failure) { failure.received(); await failure.gate; json(res, { code: 'storage_unavailable' }, 503); }
    else json(res, { items: [...jobs].reverse() });
  } else if (suffix === '/detail') {
    const response = JSON.parse(JSON.stringify(job || {}));
    const held = nextDetailRead; nextDetailRead = null;
    if (held) { held.received(); await held.gate; }
    json(res, response);
  } else if (suffix === '/candidate') {
    assert(url.searchParams.get('id').endsWith(':candidate'));
    json(res, { id: `${job.id}:candidate`, revision: job.revision, job_id: job.id, email: job.email, account_id: 'acct_fixture_complete', user_id: 'user_fixture_complete', intent: job.intent, target_auth_id: target.auth_id, restrictions: ['quality_paused', 'manual_disabled', 'rules_stopped'] });
  } else if (suffix === '/commit') {
    assert.equal(body.candidate_id, `${job.id}:candidate`); assert.equal(body.revision, job.revision); job.status = 'committed'; job.revision++; json(res, job);
  } else if (suffix === '/callback') {
    assert(body.callback_url.startsWith('http://localhost:1455/auth/callback')); job.status = 'awaiting_commit'; job.revision++; json(res, job);
  } else if (suffix === '/cancel' || suffix === '/discard') {
    job.status = suffix === '/cancel' ? 'cancelled' : 'discarded'; job.revision++; json(res, job);
  } else json(res, { code: 'not_found' }, 404);
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  const page = await browser.newPage({ viewport: { width: 1280, height: 800 }, reducedMotion: 'reduce' });
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.route('**/*', route => new URL(route.request().url()).hostname === '127.0.0.1' ? route.continue() : route.abort());
  const tasks = async () => page.locator('[data-login-view=tasks]').click();
  const ready = async () => page.locator('#account-import-open:enabled').waitFor({ state: 'attached' });
  const settle = async () => page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
  const refresh = async () => { await page.locator('#login-refresh').evaluate(button => button.click()); await settle(); };
  const waitStatus = async text => { await tasks(); await page.locator('#login-job-detail h3').filter({ hasText: text }).waitFor(); };
  const select = async job => { await refresh(); await tasks(); await page.locator(`[data-login-job="${job.id}"]`).click(); };
  const capture = async name => { await settle(); assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false); await page.screenshot({ path: path.join(output, `${name}.png`), fullPage: true }); screenshots.push(`${name}.png`); };
  const connect = async () => { await page.locator('#login-connect').click(); await page.locator('#management-key').fill('offline-management-key'); await page.locator('#connection-form button[type=submit]').click(); await page.locator('body.connected').waitFor(); await ready(); };
  const writes = () => calls.filter(call => call.method !== 'GET');
  const cancelCalls = () => calls.filter(call => call.path.endsWith('/cancel')).length;
  try {
    const serverURL = `http://127.0.0.1:${server.address().port}/`;
    await page.goto(`${serverURL}#login`);
    assert.equal(await page.locator('[data-page]').count(), 3);
    assert.deepEqual(await page.locator('[data-login-view]').evaluateAll(nodes => nodes.map(node => node.dataset.loginView)), ['accounts', 'tasks', 'settings']);
    assert.equal(await page.locator('#login-view-temporary, #login-form, #login-start, #login-method, #login-credential-rows').count(), 0, 'Temporary login is removed rather than hidden');
    assert.equal(await page.locator('#connection-open').isVisible(), true);
    await connect();
    assert.equal(await page.locator('#login-engine, .runtime-info').count(), 0);
    assert.equal(await page.locator('[data-page=login]').getAttribute('aria-current'), 'page');
    assert.equal(await page.locator('#login-view-accounts').isVisible(), true);
    assert.deepEqual(writes(), []);
    checks.push('The independent login page has three retained views, a disconnected reconnect state and no temporary-login DOM; connection only reads existing state');
    await tasks(); await waitStatus('等待确认导入');
    const detail = await page.locator('#login-job-detail').innerText();
    for (const value of [target.email, 'acct_fixture_complete', 'user_fixture_complete', '质量暂停', '人工禁用', '检测规则']) assert(detail.includes(value), value);
    assert(!detail.includes('f***'));
    assert.equal(calls.filter(call => call.path.endsWith('/commit')).length, 0);
    await capture('candidate-1280');
    await page.locator('[data-login-action=commit]').click(); await waitStatus('已保存');
    assert.equal(candidate.status, 'committed');
    assert.match(await page.locator('#login-job-detail').innerText(), /相关检测规则保持停止/);
    assert.equal(calls.filter(call=>call.path.endsWith('/commit')).length,1,'Historical candidate uses a single explicit action');
    checks.push('Historical candidates retain complete identity and restrictions and submit with one explicit action without an extra confirmation, model calls or quality release');
    const enabledJob=seed('automatic-account-completed','committed');
    Object.assign(enabledJob,{account_id:'offline-account',auto_commit:true,enable_credential:true,credential_enabled:true});
    await select(enabledJob);await waitStatus('已保存并启用');
    assert.match(await page.locator('#login-job-detail').innerText(),/无需再次确认/);assert.equal(await page.locator('[data-login-action=commit]').count(),0);
    Object.assign(enabledJob,{status:'saved_not_adopted',credential_enabled:false,error_code:'credential_enable_conflict',revision:enabledJob.revision+1});
    await refresh();await page.locator('#login-job-detail').filter({hasText:'CPA 尚未确认启用'}).waitFor();
    assert.equal(await page.locator('[data-login-action=commit]').count(),0);assert.equal(calls.filter(call=>call.path.endsWith('/commit')).length,1);
    checks.push('Automatic account completion confirms saving and enablement without another action; an enablement conflict exposes the incomplete result without retrying a write');
    const discard = seed('discard-existing', 'awaiting_commit'); await select(discard); await waitStatus('等待确认导入');
    await page.locator('[data-login-action=discard]').click(); await waitStatus('已丢弃候选'); assert.equal(discard.status, 'discarded');
    checks.push('A pending candidate can be explicitly discarded without creating or committing a replacement task');
    const browserJob = seed('browser-existing', 'waiting_browser', 'browser'); await select(browserJob); await waitStatus('等待浏览器授权');
    assert.match(await page.locator('.login-auth-link').getAttribute('href'), /^https:\/\/auth\.openai\.com\/oauth\/authorize/);
    await page.locator('#login-callback').fill('http://localhost:1455/auth/callback?code=offline-code&state=offline-state');
    await refresh(); assert.match(await page.locator('#login-callback').inputValue(), /offline-code/);
    await page.locator('[data-login-action=callback]').click(); await waitStatus('等待确认导入');
    assert.equal(await page.locator('#login-callback').count(), 0);
    assert.equal(browserJob.status, 'awaiting_commit');
    await page.locator('[data-login-action=discard]').click(); await waitStatus('已丢弃候选');
    checks.push('A historical browser task still accepts its callback, preserves an unsent callback across polling and clears it after submission');
    const unsafe = seed('unsafe-existing', 'waiting_browser', 'browser'); unsafe.authorization_url = 'https://auth.openai.com@evil.example/oauth/authorize';
    await select(unsafe); await page.locator('#login-job-detail').filter({ hasText: '授权链接暂不可用' }).waitFor();
    assert.equal(await page.locator('.login-auth-link').count(), 0);
    rejectNextAction = true; await page.locator('[data-login-action=cancel]').click();
    await page.locator('#login-history-feedback').filter({ hasText: '任务版本已变化' }).waitFor();
    assert(!(await page.locator('body').innerText()).includes('DO_NOT_RENDER_SERVER_SECRET'));
    assert.equal(unsafe.status, 'waiting_browser');
    await page.locator('[data-login-action=cancel]').click(); await waitStatus('已取消');
    checks.push('Unsafe historical authorization URLs are rejected; fixed action-error copy omits server secrets and cancellation remains explicit');
    Object.assign(unsafe, { status: 'failed', error_code: 'runtime_failed', error_stage: 'authorization', error_reason: 'proxy_connection_failed' });
    await refresh(); await page.locator('#login-job-detail').filter({ hasText: '打开授权页：代理连接失败' }).waitFor();
    assert.match(await page.locator('#login-job-detail').innerText(), /代理地址、端口和认证资料/);
    Object.assign(unsafe, { status: 'interaction_required', error_code: 'interaction_required', error_stage: 'authorization', error_reason: 'unsupported_request' });
    await refresh(); await page.locator('#login-job-detail').filter({ hasText: '尚未适配' }).waitFor();
    assert(!(await page.locator('#login-job-detail').innerText()).includes('临时登录'));
    assert(!(await page.locator('#login-job-detail').innerText()).includes('请使用浏览器授权'));
    Object.assign(unsafe, { error_stage: '', error_reason: '' });
    await refresh(); await page.locator('#login-job-detail').filter({ hasText: '此历史记录不能判断' }).waitFor();
    checks.push('Diagnostics distinguish proxy failure and runtime incompatibility, keep missing historical evidence uncertain and never point to the removed login page');
    Object.assign(unsafe, { method: 'password_totp', status: 'running', phase: 'session', error_code: '', error_stage: '', error_reason: '' });
    await refresh(); await page.locator('#login-job-detail .field-note').filter({ hasText: '建立网页登录会话' }).waitFor();
    const progressWrites = writes().length;
    unsafe.phase = 'password'; unsafe.revision++;
    await page.locator('#login-job-detail .field-note').filter({ hasText: '验证账号密码' }).waitFor({ timeout: 8000 });
    for (const [phase, label] of [['runtime_start', '启动 Go 登录引擎'], ['browser_start', '启动登录环境'], ['email', '填写账号邮箱'], ['totp', '验证 2FA'], ['browser_verification', '处理网站验证'], ['oauth', '获取 Codex 授权'], ['consent', '确认授权'], ['workspace', '核对账号工作区'], ['callback', '接收授权回调']]) {
      unsafe.phase = phase; unsafe.revision++;
      await refresh(); await page.locator('#login-job-detail .field-note').filter({ hasText: label }).waitFor();
      assert.equal(await page.locator('[data-login-action=commit]').count(), 0);
    }
    assert.equal(writes().length, progressWrites);
    await capture('reference-progress-1280');
    checks.push('Live polling advances session, password, 2FA, website verification and OAuth phase copy without recreating tasks or exposing premature commit');
    for (const [stage, reason, label, advice] of [
      ['session', 'session_unconfirmed', '建立网页登录会话：未确认网页登录会话', '未继续获取 Codex 授权'],
      ['password', 'authentication_not_advanced', '验证密码：验证响应未确认流程前进', '未判定验证成功'],
      ['totp', 'totp_factor_missing', '验证 2FA：未找到可用的 TOTP 验证方式', '本次未提交 2FA 验证码'],
      ['oauth', 'sentinel_unavailable', '获取 Codex 授权：官方登录安全组件未就绪', '官方认证资源'],
      ['browser_verification', 'human_verification', '处理网站验证：需要完成人机验证', '此步骤需要人工处理'],
      ['browser_verification', 'browser_verification_timeout', '处理网站验证：网站自动检查超时', '尚未确认需要人工验证'],
      ['browser_verification', 'browser_verification_required', '处理网站验证：网站验证尚未完成', '官方接口返回了网站验证页面'],
    ]) {
      Object.assign(unsafe, { status: reason === 'browser_verification_timeout' ? 'failed' : 'interaction_required', phase: '', error_code: reason === 'browser_verification_timeout' ? 'runtime_timeout' : 'interaction_required', error_stage: stage, error_reason: reason, revision: unsafe.revision + 1 });
      await refresh(); await page.locator('#login-job-detail').filter({ hasText: label }).waitFor();
      assert((await page.locator('#login-job-detail').innerText()).includes(advice));
      assert.equal(await page.locator('[data-login-action=commit]').count(), 0);
    }
    checks.push('Reference-flow diagnostics distinguish unconfirmed session, unchanged authentication state, absent TOTP factor, unavailable Sentinel and manual website verification');
    const security = seed('security-existing','running');
    Object.assign(security,{action:'reset_totp',security_status:'running',reauthorize:true,phase:'security_login'});
    const securityWrites=writes().length;await select(security);
    for(const [phase,label] of [['security_login','登录账号安全会话'],['security_check','核对账号与 2FA 状态'],['totp_disable','移除旧 2FA'],['totp_enroll','登记新 2FA'],['totp_activate','激活新 2FA'],['totp_saved','新 2FA 已保存'],['logout_all','退出所有设备'],['reauthorization','重新授权 CPA']]){
      security.phase=phase;security.revision++;await refresh();await page.locator('#login-job-detail .field-note').filter({hasText:label}).waitFor();
    }
    Object.assign(security,{status:'security_uncertain',phase:'',error_code:'security_uncertain',error_stage:'totp_activate'});await refresh();await waitStatus('安全操作待核对');
    assert.match(await page.locator('#login-job-detail').innerText(),/未确认的修改不会自动重试/);assert.equal(await page.locator('[data-login-action=commit]').count(),0);
    Object.assign(security,{status:'failed',security_status:'complete',totp_reset:true,devices_logged_out:true,error_code:'exchange_failed',error_stage:'oauth'});await refresh();await waitStatus('后续步骤未完成');
    assert.match(await page.locator('#login-job-detail').innerText(),/新 2FA 已保存/);assert.match(await page.locator('#login-job-detail').innerText(),/旧密钥不会恢复/);
    Object.assign(security,{action:'logout_all',status:'succeeded',security_status:'complete',totp_reset:false,reauthorize:false,error_code:'',error_stage:''});await refresh();await waitStatus('安全操作已完成');
    assert.match(await page.locator('#login-job-detail').innerText(),/本次未选择重新授权 CPA/);assert.equal(writes().length,securityWrites);
    checks.push('Security task phases and independent outcomes distinguish saved TOTP, device logout, unresolved mutations and later OAuth failure without replaying any write');
    const downloadEvent = page.waitForEvent('download'); await page.locator('#login-export').click();
    const reportText = fs.readFileSync(await (await downloadEvent).path(), 'utf8');
    assert(reportText.includes(target.email));
    for (const secret of ['offline-code', 'offline-state', 'offline-management-key', 'authorization_url', 'credentials']) assert(!reportText.includes(secret));
    checks.push('Task export retains complete identity and safe diagnostic metadata while omitting OAuth and management secrets');
    for (const theme of ['light', 'white', 'dark']) for (const viewport of [{ width: 1280, height: 800 }, { width: 390, height: 844 }]) {
      await page.locator('#theme-toggle').selectOption(theme); await page.setViewportSize(viewport); await capture(`tasks-${theme}-${viewport.width}`);
    }
    checks.push('Retained task details fit all three themes on desktop and phone without document overflow');
    await page.setViewportSize({ width: 1280, height: 800 }); await page.locator('#theme-toggle').selectOption('light');
    const active = seed('background-existing', 'waiting_browser', 'browser'); await select(active); await waitStatus('等待浏览器授权');
    await page.locator('#login-callback').fill('http://localhost:1455/auth/callback?code=unsent&state=offline-state');
    const cancelsBefore = cancelCalls(); const writesBefore = writes().length;
    await page.locator('[data-page=manual]').click();
    assert.equal(cancelCalls(), cancelsBefore); assert.equal(writes().length, writesBefore);
    let releaseCapabilities;
    capabilitiesGate = new Promise(resolve => { releaseCapabilities = resolve; });
    heldReads.add(releaseCapabilities);
    await page.locator('[data-page=login]').click(); await page.locator('#login-workbench').waitFor();
    for (const selector of ['#account-import-open', '#account-inspect']) assert(await page.locator(selector).isDisabled());
    capabilitiesGate = null; releaseCapabilities(); heldReads.delete(releaseCapabilities); await ready(); await tasks();
    await page.locator(`[data-login-job="${active.id}"]`).waitFor(); await page.locator('#login-callback').waitFor();
    assert.equal(await page.locator('#login-callback').inputValue(), ''); assert.equal(active.status, 'waiting_browser');
    const sameTabReads = calls.filter(call => call.path.endsWith('/capabilities')).length;
    await page.locator('#login-callback').fill('http://localhost:1455/auth/callback?code=draft&state=offline-state');
    await page.locator('[data-page=login]').click();
    assert.equal(calls.filter(call => call.path.endsWith('/capabilities')).length, sameTabReads);
    assert.match(await page.locator('#login-callback').inputValue(), /draft/);
    checks.push('Navigation clears callback input without cancelling host jobs; reentry disables account controls until ready and clicking the current page preserves its draft');
    let releaseFailure, failureReceived;
    const receivedFailure = new Promise(resolve => { failureReceived = resolve; });
    nextJobsFailure = { gate: new Promise(resolve => { releaseFailure = resolve; }), received: failureReceived }; heldReads.add(releaseFailure);
    const failedResponse = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/model-quality/login/jobs') && response.status() === 503);
    await refresh(); await receivedFailure;
    await page.locator('[data-page=manual]').click(); await page.locator('[data-page=login]').click(); await ready();
    releaseFailure(); heldReads.delete(releaseFailure); await (await failedResponse).finished(); await settle();
    assert.equal(await page.locator('#login-notice').count(), 0);
    checks.push('A delayed read failure from an older visit cannot overwrite the current account workspace');
    let releaseDetail, detailReceived;
    const receivedDetail = new Promise(resolve => { detailReceived = resolve; });
    nextDetailRead = { gate: new Promise(resolve => { releaseDetail = resolve; }), received: detailReceived }; heldReads.add(releaseDetail);
    await tasks(); await page.locator(`[data-login-job="${candidate.id}"]`).click(); await receivedDetail;
    assert.equal(await page.locator('#login-job-detail [data-login-action]').count(), 0);
    await page.locator(`[data-login-job="${active.id}"]`).click(); await waitStatus('等待浏览器授权');
    releaseDetail(); heldReads.delete(releaseDetail); await settle();
    assert.match(await page.locator('#login-job-detail h3').innerText(), /等待浏览器授权/);
    checks.push('Task selection removes stale actions immediately and a late detail response cannot replace a newer selected task');
    await page.locator('#login-callback').fill('http://localhost:1455/auth/callback?code=clear-on-401&state=offline-state');
    unauthorizedNext = true; await refresh(); await page.locator('body:not(.connected)').waitFor();
    assert.equal(await page.locator('[data-page=login]').getAttribute('aria-current'), 'page');
    assert(await page.locator('#connection-open').isVisible());
    assert(await page.locator('#login-view-accounts').isVisible());
    assert(await page.locator('#account-import-open').isDisabled());
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
    assert.equal(await page.locator('[data-login-secret], #login-callback').evaluateAll(nodes => nodes.some(node => node.value)), false);
    await connect(); assert.equal(await page.locator('[data-page=login]').getAttribute('aria-current'), 'page');
    checks.push('401 clears account and callback data plus the persisted management key, keeps the page selected and offers a working reconnect');
    await page.locator('#connection-top').click(); await page.locator('#disconnect').click(); await page.locator('[data-page=manual]').click();
    await page.locator('#connection-open').click(); await page.locator('#management-key').fill('offline-management-key');
    let releaseQuality; qualityGate = new Promise(resolve => { releaseQuality = resolve; }); heldReads.add(releaseQuality);
    await page.locator('#connection-form button[type=submit]').click(); await page.evaluate(() => { location.hash = '#login'; });
    await page.locator('[data-page=login][aria-current=page]').waitFor();
    assert(await page.locator('#login-view-accounts').isVisible());
    assert.equal(await page.locator('#connection-open').innerText(), '未连接');
    qualityGate = null; releaseQuality(); heldReads.delete(releaseQuality); await page.locator('body.connected').waitFor(); await ready();
    checks.push('Navigating to login during an unfinished management connection opens the correct account workspace when it completes');
    const clientResult = await page.evaluate(async () => {
      const { ManagementClient } = await import('./client.js'); const client = new ManagementClient('/unused', () => {}); client.open('offline-management-key');
      const paths = ['https://evil.example/commit', '//evil.example/commit', '/bad/../commit', '/commit#x', '/commit?password=secret', '/unknown'];
      const rejected = await Promise.all(paths.map(async value => { try { await client.requestHostLogin(value, 'POST', {}, 'key'); return false; } catch { return true; } }));
      const obsoleteRoutes = [['/start', 'POST'], ['/vault/entry', 'PUT'], ['/vault/entry', 'DELETE']];
      const obsoleteRejected = await Promise.all(obsoleteRoutes.map(async ([route, method]) => { try { await client.requestHostLogin(route, method, {}, 'key'); return false; } catch { return true; } }));
      let missingKeyRejected = false; try { await client.requestHostLogin('/commit', 'POST', {}); } catch { missingKeyRejected = true; }
      client.clear(); return { rejected, obsoleteRejected, missingKeyRejected };
    });
    assert(clientResult.rejected.every(Boolean)); assert(clientResult.obsoleteRejected.every(Boolean)); assert(clientResult.missingKeyRejected);
    assert(!calls.some(call => /\/(start|vault\/entry|vault\/initialize)$/.test(call.path)));
    assert(!calls.some(call => /\/(runs|release)$/.test(call.path) && call.method !== 'GET'));
    assert.deepEqual(await page.evaluate(() => ({ local: Object.fromEntries(Object.entries(localStorage)), session: Object.fromEntries(Object.entries(sessionStorage)) })), { local: { 'model-quality:management-key:/v0/management/plugins/model-quality': 'offline-management-key' }, session: {} });
    checks.push('Strict endpoint and idempotency validation rejects removed task-creation and storage-edit routes; task review never invokes models or quality release');
    assert.deepEqual(errors, []); assert.deepEqual(sourceHashes(), source);
    fs.writeFileSync(path.join(output, 'report.json'), JSON.stringify({ status: 'passed', fixture_scope: 'Loopback-only synthetic retained tasks; no real credentials or remote model calls', checks, screenshots, source_sha256: source }, null, 2) + '\n');
    process.stdout.write(`Login task UI: ${checks.length} checks passed with offline fixtures.\n`);
  } finally { for (const release of heldReads) release(); await browser.close(); await new Promise(resolve => server.close(resolve)); }
})().catch(error => { process.stderr.write(`${error.stack}\n`); process.exitCode = 1; });
