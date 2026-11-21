// Exercise the account page's first paint and connection lifecycle on a loopback fixture.
const assert = require('node:assert/strict');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const fs = require('node:fs/promises');

const api = '/v0/management/plugins/model-quality';
const alternateKey = 'alternate-key-for-isolated-browser-test';
const storedKey = `model-quality:management-key:${api}`;
const accountRow = '[data-account-row="fixture-enabled"]';
const hostPage = '/management.html#/plugin-pages/model-quality/0';

function nextRead(page, resource) {
  return page.waitForRequest(request => new URL(request.url()).pathname === api + resource);
}

function rememberKey(page, key) {
  return page.addInitScript(([name, value]) => {
    if (window.top === window) localStorage.setItem(name, value);
  }, [storedKey, key]);
}

async function checkShell(view) {
  await view.locator('#login-view-accounts').waitFor();
  assert(await view.locator('#account-import-open').isVisible());
  assert.equal(await view.locator('#login-disconnected, .login-connect-intro, .login-connect-guide').count(), 0);
  assert.equal(await view.locator('#page-login #login-connect').count(), 0);
  assert.doesNotMatch(await view.locator('#page-login').innerText(), /连接后可|连接管理接口|未连接/);
}

async function checkWaiting(view, restoring = false) {
  await checkShell(view);
  assert(await view.locator('#account-import-open').isDisabled());
  if (restoring) {
    assert(await view.getByRole('button', { name: /^(管理连接|已连接)$/ }).isVisible());
    assert(await view.locator('#account-import-empty').isHidden(), 'The empty directory invitation needs a confirmed empty account list');
  } else {
    assert(await view.locator('#account-empty').isVisible());
    assert(await view.locator('#account-import-empty').isDisabled());
  }
  assert.doesNotMatch(await view.locator('#login-view-accounts').innerText(), /0\s*个账号|全部状态\s*·\s*0/);
  assert.equal(await view.locator('[data-account-row]').count(), 0);
}

async function checkReturning(view) {
  await checkShell(view);
  assert(await view.locator(accountRow).isVisible());
  assert.equal(await view.locator('#connection-state').innerText(), '已连接');
  assert(await view.locator('#connection-top .connection-dot').isVisible());
  assert(await view.locator('#connection-dialog').isHidden());
}

async function checkSavedConnection(view) {
  assert.doesNotMatch(await view.locator('body').innerText(), /正在连接管理接口/);
}

async function checkNoCachedAccountData(page) {
  const stored = await page.evaluate(() => JSON.stringify([Object.entries(localStorage), Object.entries(sessionStorage)]));
  assert.doesNotMatch(stored, /fixture-enabled@example\.test|fixture-cpa-enabled\.json|fixture-quality-primary|fixture-run-visual/);
}

async function emptyAccountLayout(view, connectionLabel) {
  const pill = view.getByRole('button', { name: connectionLabel, exact: true });
  assert(await pill.evaluate(button => {
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) return false;
    return [button, button.querySelector('.connection-dot')].every(element => {
      const style = getComputedStyle(element);
      return style.animationName === 'none' && [style.animationDuration, style.transitionDuration, style.transitionDelay]
        .every(durations => durations.split(',').every(duration => parseFloat(duration) === 0));
    });
  }), 'The connection button and its status dot have no animation or transition with normal motion settings');
  const rects = { connection: await pill.boundingBox() };
  for (const selector of ['#login-workbench', '.account-toolbar',
    '#account-status-filter', '#account-search', '#account-import-open', '#account-convert-open',
    '#account-inspect', '#account-history-open', '#account-settings-open', '#refresh']) {
    rects[selector] = await view.locator(selector).boundingBox();
    assert(rects[selector], selector + ' must be visible in the account workspace');
  }
  return rects;
}

function changeVisibility(page, hidden) {
  // Headless Chromium keeps every tab visible. Deliver the browser event with the corresponding state.
  return page.evaluate(hidden => {
    Object.defineProperty(document, 'hidden', { configurable: true, value: hidden });
    Object.defineProperty(document, 'visibilityState', { configurable: true, value: hidden ? 'hidden' : 'visible' });
    document.dispatchEvent(new Event('visibilitychange'));
  }, hidden);
}

function changeFrameVisibility(page, hidden) {
  return page.evaluate(hidden => {
    const frame = document.getElementById('fixture-plugin');
    const child = frame.contentWindow;
    return new Promise((resolve, reject) => {
      const observer = new child.IntersectionObserver(([entry]) => {
        if (entry.isIntersecting === !hidden) {
          observer.disconnect();
          clearTimeout(timer);
          resolve(child.document.hidden);
        }
      });
      const timer = setTimeout(() => {
        observer.disconnect();
        reject(new Error('The iframe did not report its new intersection state'));
      }, 6000);
      observer.observe(child.document.documentElement);
      frame.style.display = hidden ? 'none' : '';
    });
  }, hidden);
}

async function checkPluginMotion(view) {
  const moving = await view.locator('html').evaluate(root => {
    if (matchMedia('(prefers-reduced-motion: reduce)').matches) throw new Error('Expected normal motion settings');
    const reported = [];
    for (const element of [root, ...root.querySelectorAll('*')]) {
      for (const pseudo of [null, '::before', '::after']) {
        const style = getComputedStyle(element, pseudo);
        const timed = style.transitionProperty !== 'none' && style.transitionDuration.split(',').some(time => parseFloat(time) > 0);
        if (timed || style.animationName !== 'none' || style.scrollBehavior === 'smooth') {
          reported.push((element.id || element.className || element.tagName) + (pseudo || ''));
        }
      }
    }
    return reported.slice(0, 10);
  });
  assert.deepEqual(moving, [], 'Plugin controls and pseudo-elements should update immediately');
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exited = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key: key } = JSON.parse(line);
    const { origin, hostname, port } = new URL(url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const report = {};

    async function scenario(name, test, { responses = {}, failures = {}, connection, ...options } = {}) {
      if (process.env.UI_SCENARIO && !name.includes(process.env.UI_SCENARIO)) return;
      const context = await browser.newContext(options);
      const errors = [], reads = [], writes = [], holds = new Map(), releases = [];
      try {
        const page = await context.newPage();
        page.setDefaultTimeout(6000);
        page.on('pageerror', error => errors.push(error.message));
        await context.route('**/*', async route => {
          const request = route.request(), url = new URL(request.url());
          const method = request.method(), resource = url.pathname.slice(api.length);
          if (url.origin !== origin || (method !== 'GET' && !(connection && resource === '/connection'))) {
            errors.push(`${method} ${url.pathname}`);
            return route.abort();
          }
          if (url.pathname.startsWith(api + '/')) {
            const auth = request.headers().authorization;
            const alternate = connection && auth === 'Bearer ' + alternateKey;
            const valid = auth === 'Bearer ' + key || alternate;
            if (!valid && auth !== 'Bearer expired-fixture-key') errors.push('Unexpected fixture authorization on ' + resource);
            if (method === 'GET') reads.push(resource);
            else writes.push({ method, ...request.postDataJSON() });
            if (holds.has(resource)) await holds.get(resource);
            const responsesForMethod = failures[method + ' ' + resource] || (method === 'GET' && failures[resource]);
            if (responsesForMethod?.length) {
              const reply = responsesForMethod.shift();
              return route.fulfill(typeof reply === 'number' ? { status: reply, json: { error: `Synthetic response ${reply}` } } : reply);
            }
            if (valid && connection && resource === '/connection') {
              if (method !== 'GET') {
                const body = request.postDataJSON();
                assert(!JSON.stringify(body).includes(key) && !JSON.stringify(body).includes(alternateKey));
                if (body.revision !== connection.revision || body.mode === 'migrate' && !connection.can_migrate) {
                  return route.fulfill({ status: 409, json: { error: { code: 'conflict', source: 'business' } } });
                }
                const configured = method === 'PUT';
                connection = { state: configured ? 'ready' : 'unconfigured', revision: body.revision + 1, configured, can_migrate: false };
                if (failures['receipt ' + resource]?.shift()) return route.abort('connectionfailed');
              }
              return route.fulfill({ json: connection });
            }
            if (valid && Object.hasOwn(responses, resource)) return route.fulfill({ json: responses[resource] });
            if (alternate) return route.continue({ headers: { ...request.headers(), authorization: 'Bearer ' + key } });
          }
          return route.continue();
        });
        const hold = (...resources) => {
          let release;
          const pending = new Promise(resolve => { release = resolve; });
          for (const resource of resources) holds.set(resource, pending);
          releases.push(release);
          return release;
        };
        const evidence = await test(page, hold, reads, writes);
        assert.deepEqual(errors, []);
        report[name] = { counts: Object.fromEntries([...new Set(reads)].sort().map(resource => [resource, reads.filter(r => r === resource).length])), ...evidence };
        console.log(`Passed: ${name}`);
      } finally {
        for (const release of releases) release();
        await context.close();
      }
    }

    await scenario('legacy migration runs once across iframe rebuilds and a backend clear', async (page, hold, reads, writes) => {
      await rememberKey(page, key);
      await page.goto(origin + hostPage);
      const frame = page.frameLocator('#fixture-plugin');
      await frame.locator('#account-import-open:enabled').waitFor();
      assert.deepEqual(writes, [{ method: 'PUT', mode: 'migrate', revision: 0 }], 'legacy browser key should configure a pristine backend once');
      for (let n = 0; n < 2; n++) {
        await page.getByRole('link', { name: '认证文件' }).click();
        await page.locator('#fixture-plugin').waitFor({ state: 'detached' });
        await page.getByRole('link', { name: 'Credential Ops' }).click();
        await frame.locator('#account-import-open:enabled').waitFor();
      }
      assert.equal(writes.length, 1);
      await frame.locator('#connection-top').click();
      await frame.locator('#connection-clear').click();
      await frame.locator('#action-confirm').click();
      await frame.locator('#connection-status').filter({ hasText: '后台未配置' }).waitFor();
      assert.equal(writes.length, 2);
      assert.deepEqual(writes[1], { method: 'DELETE', revision: 1 });
      await page.reload();
      await frame.locator('#account-import-open:enabled').waitFor();
      assert.equal(writes.length, 2);
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
      return { iframeRebuilds: 3, migrationWrites: 1, clearWrites: 1, reads: reads.length };
    }, { connection: { revision: 0, state: 'unconfigured', configured: false, can_migrate: true } });

    for (const initial of ['unconfigured', 'invalid']) await scenario('first connection with ' + initial + ' backend saves the key in a single action', async (page, hold, reads, writes) => {
      await page.goto(origin);
      await page.click('#connection-open'); await page.fill('#management-key', alternateKey);
      await page.click('#connection-submit');
      await page.locator('#connection-state').filter({ hasText: '已连接' }).waitFor();
      assert.deepEqual(writes, [{ method: 'PUT', revision: 7, mode: 'configure' }]);
      await page.reload(); await page.locator('#account-import-open:enabled').waitFor();
      await page.click('#connection-top'); await page.click('#disconnect');
      assert.equal(writes.length, 1);
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
      return { manualKeyEntries: 1, successfulConfigurationWrites: 1, logoutWrites: 0 };
    }, { connection: { state: initial, revision: 7, configured: initial === 'invalid', can_migrate: initial === 'unconfigured' } });

    await scenario('initial migration retries a known pre-write 503 after the iframe returns', async (page, hold, reads, writes) => {
      await page.clock.install();
      await rememberKey(page, key);
      await page.goto(origin + hostPage);
      const frame = page.frameLocator('#fixture-plugin');
      await frame.locator('#account-import-open:enabled').waitFor();
      await frame.locator('#notice').filter({ hasText: '后台连接未更新' }).waitFor();
      assert.equal(writes.length, 1);
      await page.getByRole('link', { name: '认证文件' }).click();
      await page.locator('#fixture-plugin').waitFor({ state: 'detached' });
      await page.clock.fastForward(3000);
      await page.getByRole('link', { name: 'Credential Ops' }).click();
      await frame.locator('#connection-state').filter({ hasText: '已连接' }).waitFor();
      assert.deepEqual(writes, Array(2).fill({ method: 'PUT', revision: 9, mode: 'migrate' }));
      await page.reload(); await frame.locator('#account-import-open:enabled').waitFor();
      assert.equal(writes.length, 2);
      return { prewriteRejections: 1, acceptedSetups: 1, retriesAfterSuccess: 0 };
    }, { connection: { state: 'unconfigured', revision: 9, configured: false, can_migrate: true },
      failures: { 'PUT /connection': [{ status: 503, json: { error: { code: 'management_network', source: 'backend_management' } } }] } });

    for (const [failure, text] of [['receipt', '写入结果待核对'], ['storage', '保存失败']]) {
      await scenario('initial migration ' + failure + ' is never replayed', async (page, hold, reads, writes) => {
        await rememberKey(page, key);
        await page.goto(origin);
        await page.locator('#notice').filter({ hasText: text }).waitFor();
        await page.locator('#account-import-open:enabled').waitFor();
        await page.reload(); await page.locator('#account-import-open:enabled').waitFor();
        assert.equal(writes.length, 1);
        assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
        assert.equal(await page.locator('#connection-state').innerText(), failure === 'storage' ? '后台未配置' : '已连接');
        return { writes: 1, replays: 0, accountsAvailable: true };
      }, { connection: { state: 'unconfigured', revision: 9, configured: false, can_migrate: true },
        failures: failure === 'receipt' ? { 'receipt /connection': [true] } : { 'PUT /connection': [507] } });
    }

    await scenario('rotating across tabs rejects a stale revision and ignores an old 401 after rotation', async (page, hold, reads, writes) => {
      await page.goto(origin + '/management.html#/auth-files');
      await page.evaluate(([name, value]) => localStorage.setItem(name, value), [storedKey, key]);
      await page.goto(origin); await page.locator('#account-import-open:enabled').waitFor();
      const other = await page.context().newPage();
      try {
        await other.goto(origin); await other.locator('#account-import-open:enabled').waitFor();
        await page.click('#connection-top'); await other.click('#connection-top');
        await other.click('#connection-clear'); await other.click('#action-confirm');
        await page.locator('#connection-status').filter({ hasText: '后台未配置' }).waitFor();
        await page.click('#connection-submit');
        await page.locator('#connection-status').filter({ hasText: '后台配置版本已变化' }).waitFor();
        assert.deepEqual(writes, [{ method: 'DELETE', revision: 10 }, { method: 'PUT', mode: 'configure', revision: 10 }]);
        await page.click('[data-close=connection-dialog]');
        let release, entered;
        const held = new Promise(resolve => { release = resolve; }), routed = new Promise(resolve => { entered = resolve; });
        await page.route('**/connection', async route => {
          entered(); await held;
          await route.fulfill({ status: 401, json: { error: 'Synthetic old key' } }).catch(() => {});
        }, { times: 1 });
        await page.click('#connection-top'); await routed;
        await other.fill('#management-key', alternateKey); await other.click('#connection-submit');
        await page.waitForFunction(([name, value]) => localStorage.getItem(name) === value, [storedKey, alternateKey]);
        release(); await page.locator('#account-import-open:enabled').waitFor();
        assert.equal(await page.locator('#connection-state').innerText(), '已连接');
        assert.equal(writes.length, 3);
        await other.click('#connection-top'); await other.click('#disconnect');
        await page.locator('body:not(.connected)').waitFor();
        assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
        assert.equal(writes.length, 3);
        return { configurationMutations: 2, staleCASRejections: 1, obsoleteUnauthorizedResponses: 1, logoutWrites: 0 };
      } finally { await other.close(); }
    }, { connection: { state: 'ready', revision: 10, configured: true, can_migrate: false } });

    await scenario('account list and controls work throughout a connection status outage', async (page, hold, reads, writes) => {
      await rememberKey(page, key);
      await page.goto(origin);
      await page.locator(accountRow).waitFor();
      await page.locator('#account-import-open:enabled').waitFor();
      await page.locator('#connection-state').filter({ hasText: '后台状态更新失败' }).waitFor();
      assert.equal(reads.filter(path => path === '/login/accounts').length, 1);
      assert.deepEqual(writes, []);
      await page.click('#account-import-open');
      await page.locator('#account-import-dialog[open]').waitFor();
      return { accountReads: 1, connectionWrites: 0, importAvailable: true };
    }, { failures: { '/connection': Array(50).fill(503) } });

    await scenario('a temporary page outage recovers on safe reads without another key', async (page, hold, reads, writes) => {
      await rememberKey(page, key);
      await page.clock.install();
      await page.goto(origin);
      await page.locator('#notice').filter({ hasText: '页面数据暂不可达' }).waitFor();
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
      await page.clock.fastForward(2600);
      await page.locator('#account-import-open:enabled').waitFor();
      await page.click('#account-import-open'); await page.fill('#account-import-text', 'local unsaved draft');
      await changeVisibility(page, true);
      const before = reads.length;
      await page.clock.fastForward(65000);
      assert.equal(reads.length, before);
      await changeVisibility(page, false);
      assert.equal(await page.locator('#account-import-text').inputValue(), 'local unsaved draft');
      assert.equal(writes.length, 0);
      return { hiddenReads: 0, writes: 0, preservedDraft: true, recoveredAccount: true };
    }, { failures: { '/connection': [503], '/login/accounts': [503] } });

    for (const [display, blocked] of [['tab', '/login/jobs'], ['iframe', '/login/detail']]) {
      const job = { id: 'fixture-job-active', account_id: 'fixture-active', email: 'fixture-active@example.test', revision: 1,
        created_at: '2026-11-17T02:00:00Z', method: 'password_totp', intent: 'import', status: 'awaiting_commit', candidate_id: 'fixture-candidate' };
      const candidate = { id: job.candidate_id, job_id: job.id, revision: 1, email: job.email, user_id: 'synthetic-before' };
      await scenario('hidden account history stops after ' + blocked + ' in ' + display, async (page, hold, reads, writes) => {
        await rememberKey(page, key);
        await page.clock.install();
        await page.goto(origin + (display === 'iframe' ? hostPage : '/#login'));
        const view = display === 'iframe' ? page.frameLocator('#fixture-plugin') : page;
        const change = display === 'iframe' ? changeFrameVisibility : changeVisibility;
        const detail = view.locator('#login-job-detail'), search = view.locator('#login-task-search');
        await view.locator('#account-history-open:enabled').click();
        await detail.getByText('synthetic-before', { exact: true }).waitFor();
        await detail.locator('[data-task-log-update]').filter({ hasText: '更新于' }).waitFor();
        await search.fill(job.email);
        await page.waitForLoadState('networkidle');

        const release = hold(blocked);
        const started = nextRead(page, blocked);
        const delivered = page.waitForResponse(response => new URL(response.url()).pathname === api + blocked);
        job.revision++;
        candidate.revision = job.revision;
        candidate.user_id = 'synthetic-after';
        await page.clock.fastForward(6500);
        await started;
        const documentHidden = await change(page, true);
        if (display === 'iframe') assert.equal(documentHidden, false, 'CSS hiding has its own visibility signal');
        const before = reads.length;
        release(); await (await delivered).finished();
        // The document may already have fired networkidle. Observe new requests
        // after the held response has reached the renderer.
        await page.waitForTimeout(500);
        const hiddenReads = reads.slice(before);
        assert.deepEqual(hiddenReads, [], 'Hidden follow-up requests after ' + blocked + ': ' + JSON.stringify(hiddenReads));

        const logs = page.waitForResponse(response => new URL(response.url()).pathname === api + '/login/logs');
        await change(page, false);
        await detail.getByText('synthetic-after', { exact: true }).waitFor({ timeout: 2500 });
        await (await logs).finished();
        await detail.locator('[data-task-log-update]').filter({ hasText: '更新于' }).waitFor();
        const resumed = reads.slice(before);
        for (const path of ['/login/detail', '/login/candidate', '/login/logs']) assert.equal(resumed.filter(read => read === path).length, 1);
        assert.equal(await search.inputValue(), job.email);
        assert.equal(await view.locator('[data-login-job]').count(), 1);
        assert.deepEqual(writes, []);
        return { blocked, display, hiddenReads, resumed, sameSelectionAndSearch: true, resumedCandidateRevision: candidate.revision, writes: 0 };
      }, { responses: { '/login/jobs': { items: [job], has_more: false }, '/login/detail': job, '/login/candidate': candidate } });
    }

    await scenario('hidden account log panel stops after account history in iframe', async (page, hold, reads, writes) => {
      await rememberKey(page, key);
      await page.goto(origin + hostPage);
      const view = page.frameLocator('#fixture-plugin');
      const toggle = view.locator('.account-logs-action[data-account-id="fixture-active"]');
      await toggle.waitFor();
      await page.waitForLoadState('networkidle');
      const release = hold('/login/jobs');
      const accountHistory = url => new URL(url).searchParams.get('account_id') === 'fixture-active';
      const started = page.waitForRequest(request => accountHistory(request.url()));
      const delivered = page.waitForResponse(response => accountHistory(response.url()));
      await toggle.click(); await started;
      const search = view.locator('[data-log-search]');
      await search.fill('running');
      assert.equal(await changeFrameVisibility(page, true), false);
      const before = reads.length;
      release(); await (await delivered).finished();
      await page.waitForTimeout(500);
      const hiddenReads = reads.slice(before);
      assert.deepEqual(hiddenReads, [], 'Hidden account log requests: ' + JSON.stringify(hiddenReads));
      await changeFrameVisibility(page, false);
      await view.locator('[data-log-update]').filter({ hasText: '更新于' }).waitFor({ timeout: 2500 });
      assert.match(await view.locator('.account-log-overview').innerText(), /fixture-job-active/);
      assert.equal(await search.inputValue(), 'running');
      assert.deepEqual(writes, []);
      const resumed = reads.slice(before);
      assert.equal(resumed.filter(read => read === '/login/logs').length, 1);
      return { hiddenReads, resumed, sameLogAndSearch: true, writes: 0 };
    });

    const exportPages = [];
    await scenario('hidden account tab finishes an explicit log export', async (page, hold, reads, writes) => {
      await rememberKey(page, key);
      await page.goto(origin + '/#login');
      await page.locator('#account-history-open:enabled').click();
      await page.locator('#login-job-detail .login-detail-identity').filter({ hasText: 'fixture-active@example.test' }).waitFor();
      await page.locator('[data-task-log-update]').filter({ hasText: '更新于' }).waitFor();
      // Two valid pages exercise the export's cursor read after hiding the tab.
      const id = 'fixture-job-active';
      for (const index of [1, 2]) exportPages.push({ json: { job_id: id, first_id: 1, last_id: 2, retained_count: 2,
        has_more: index === 1, next_cursor: index, entries: [{ id: index, status: 'running', phase: 'session' }] } });
      const release = hold('/login/logs'), started = nextRead(page, '/login/logs'), download = page.waitForEvent('download');
      await page.click('#login-export'); await started;
      await changeVisibility(page, true);
      const before = reads.length;
      release();
      const report = JSON.parse(await fs.readFile(await (await download).path(), 'utf8'));
      assert.equal(report.jobs[0].id, id);
      assert.deepEqual(report.jobs[0].events.map(event => event.id), [1, 2]);
      assert.equal(report.read_incomplete, false);
      assert.deepEqual(reads.slice(before), ['/login/logs']);
      assert.deepEqual(writes, []);
      return { requestedExportCursorReads: 1, exportedEvents: 2, displayPollsWhileHidden: 0, writes: 0 };
    }, { failures: { 'GET /login/logs': exportPages } });

    await scenario('account workspace in the first HTML paint', async page => {
      await page.goto(origin);
      await checkWaiting(page);
      assert(await page.getByRole('button', { name: '未连接', exact: true }).isVisible());
    }, { javaScriptEnabled: false });

    await scenario('account controls, connection dialog and quality tabs stay still', async page => {
      await page.goto(origin + hostPage);
      const view = page.frameLocator('#fixture-plugin');
      await checkWaiting(view);
      await view.getByRole('button', { name: '未连接', exact: true }).click();
      await checkPluginMotion(view);
      await view.locator('#management-key').fill(key);
      await view.locator('#connection-form button[type=submit]').click();
      await view.locator('#account-import-open:enabled').waitFor();
      await checkPluginMotion(view);
      for (const section of ['manual', 'operations']) {
        await view.locator(`[data-page="${section}"]`).click();
        await view.locator(`#page-${section}`).waitFor();
        await checkPluginMotion(view);
      }
    }, { reducedMotion: 'no-preference' });

    await scenario('embedded empty account layout stays in place during authentication', async (page, hold, reads) => {
      await page.goto(origin + hostPage);
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('body.embedded.host-themed').waitFor();
      await checkWaiting(view);
      const guest = await emptyAccountLayout(view, '未连接');
      assert.deepEqual(reads, []);

      await rememberKey(page, key);
      const release = hold('/login/accounts');
      const requested = nextRead(page, '/login/accounts');
      await page.reload();
      await requested;
      await checkWaiting(view, true);
      await emptyAccountLayout(view, '管理连接');
      release();
      await view.locator('#account-import-empty:enabled').waitFor();
      assert.equal(await view.locator('#account-status-filter option:checked').textContent(), '全部状态 · 0');
      assert.equal(reads.filter(path => path === '/login/accounts').length, 1);
      assert.equal(reads.filter(path => path === '/settings').length, 0);
      assert.deepEqual(await emptyAccountLayout(view, '已连接'), guest);
    }, {
      reducedMotion: 'no-preference',
      responses: { '/login/accounts': { items: [] }, '/login/jobs': { items: [] } },
    });

    for (const expired of [false, true]) {
      await scenario(expired ? 'expired key and manual reconnect' : 'first visit and manual connect', async (page, hold, reads) => {
        if (expired) await rememberKey(page, 'expired-fixture-key');
        const rejected = expired && page.waitForResponse(response => response.url().includes(api + '/') && response.status() === 401);
        await page.goto(expired ? origin : origin + hostPage);
        const view = expired ? page : page.frameLocator('#fixture-plugin');
        if (expired) {
          await rejected;
          await page.waitForFunction(name => localStorage.getItem(name) === null, storedKey);
        } else assert.deepEqual(reads, []);
        await checkWaiting(view);
        const pill = view.getByRole('button', { name: '未连接', exact: true });
        assert.equal(await view.locator('#connection-open:visible,#connection-top:visible').count(), 1);
        if (!expired) assert(await pill.evaluate(button => {
          const box = button.getBoundingClientRect();
          return box.x > innerWidth / 2 && box.top >= 0 && box.top < 100 && box.right <= innerWidth;
        }), 'The embedded guest connection pill is visible at the top right');
        await pill.click();
        await view.locator('#management-key').fill(key);
        await view.locator('#connection-form button[type=submit]').click();
        await view.locator(accountRow).waitFor();
        await view.locator('#account-import-open:enabled').waitFor();
        await checkReturning(view);
      });
    }

    await scenario('embedded account list loads independently of secondary reads', async (page, hold, reads) => {
      const releaseAccounts = hold('/login/accounts');
      const releaseSettings = hold('/login/accounts/settings');
      const releaseQuality = hold('/targets', '/questions', '/rules', '/metrics', '/runs');
      const accountsStarted = nextRead(page, '/login/accounts');
      await page.goto(origin + '/embedded.html?demo=1');
      const view = page.frameLocator('#fixture-plugin');
      await accountsStarted;
      await checkWaiting(view, true);
      await checkSavedConnection(view);
      releaseAccounts();
      await view.locator(accountRow).waitFor();
      await view.locator('#account-import-open:enabled').waitFor();
      assert(await view.locator('#account-settings-save').isDisabled(), 'Only submitting settings depends on their own revision');
      releaseSettings();
      await view.locator('#account-import-open:enabled').waitFor();
      assert.deepEqual(reads.filter(resource => !resource.startsWith('/login/')), ['/connection']);
      assert.equal(reads.filter(path => path === '/login/accounts').length, 1);
      releaseQuality();
      await view.locator('[data-page=manual]').click();
      await view.locator('#target option[value="fixture-quality-primary"]').waitFor({ state: 'attached' });
    });

    await scenario('plugin tab revisits and browser visibility during a slow poll', async (page, hold, reads) => {
      await rememberKey(page, key);
      await page.goto(origin);
      await page.locator('#account-import-open:enabled').waitFor();
      await page.click('[data-page=manual]');
      await page.locator('#target option[value="fixture-quality-primary"]').waitFor({ state: 'attached' });
      await page.click('[data-page=login]');
      await page.locator('#account-import-open:enabled').waitFor();

      const count = path => reads.filter(resource => resource === path).length;
      const previousSettings = count('/settings');
      const previousAccounts = count('/login/accounts');
      const release = hold('/login/accounts');
      await nextRead(page, '/login/accounts');
      for (const tab of ['manual', 'operations', 'manual']) {
        await page.click(`[data-page=${tab}]`);
        await page.click('[data-page=login]');
        await checkReturning(page);
      }
      for (const hidden of [true, false, true, false]) await changeVisibility(page, hidden);
      await checkReturning(page);
      assert.equal(count('/settings'), previousSettings + 1, 'Only the first operations visit needs handling settings');
      assert.equal(count('/login/accounts'), previousAccounts + 1, 'Tab and visibility changes share the pending account poll');
      release();
      await page.locator('#account-import-open:enabled').waitFor();
    });

    await scenario('CPA sidebar returns recreate the iframe, read accounts first and enforce expiry', async (page, hold, reads) => {
      await page.goto(origin + '/management.html#/auth-files');
      await page.evaluate(([storage, value]) => localStorage.setItem(storage, value), [storedKey, key]);
      await page.goto(origin + hostPage);
      const frame = page.frameLocator('#fixture-plugin');
      await frame.locator('#account-import-open:enabled').waitFor();
      const count = path => reads.filter(resource => resource === path).length;
      assert.equal(count('/settings'), 0);
      assert.equal(count('/login/accounts'), 1);
      const returns = [];
      const leavePlugin = async () => {
        await page.getByRole('link', { name: '认证文件' }).click();
        await page.locator('#fixture-plugin').waitFor({ state: 'detached' });
      };
      const returnToPlugin = () => page.getByRole('link', { name: 'Credential Ops' }).click();
      for (let index = 0; index < 2; index++) {
        await leavePlugin();
        const before = reads.length;
        const release = hold('/login/accounts');
        const requested = nextRead(page, '/login/accounts');
        await returnToPlugin();
        await requested;
        await checkWaiting(frame, true);
        assert.equal(reads.slice(before)[0], '/login/accounts');
        assert.equal(reads.slice(before).filter(resource => resource === '/login/accounts').length, 1);
        await checkSavedConnection(frame);
        release();
        await frame.locator('#account-import-open:enabled').waitFor();
        await checkReturning(frame);
        await checkNoCachedAccountData(page);
        const endpoints = reads.slice(before);
        assert.equal(endpoints.filter(path => path === '/login/accounts').length, 1);
        assert.equal(endpoints.filter(path => path === '/settings').length, 0);
        returns.push({ accounts: 1, settings: 0 });
      }

      await leavePlugin();
      await page.evaluate(name => localStorage.setItem(name, 'expired-fixture-key'), storedKey);
      const beforeExpiry = reads.length;
      const unauthorized = page.waitForResponse(response => response.url().includes(api + '/') && response.status() === 401);
      await returnToPlugin();
      assert.equal((await unauthorized).request().headers().authorization, 'Bearer expired-fixture-key');
      await page.waitForFunction(name => localStorage.getItem(name) === null, storedKey);
      await frame.getByRole('button', { name: '未连接', exact: true }).waitFor();
      await checkWaiting(frame);
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
      assert.deepEqual(reads.slice(beforeExpiry), ['/login/accounts'], 'Expired credentials never trigger a second validation endpoint');
      await leavePlugin();
      const afterExpiry = reads.length;
      await returnToPlugin();
      await frame.getByRole('button', { name: '未连接', exact: true }).waitFor();
      assert.deepEqual(reads.slice(afterExpiry), []);
      return { returns, expiredRead: '/login/accounts' };
    });

    await scenario('temporary account error preserves verified browser access and needs no quality authentication', async (page, hold, reads) => {
      const releaseAccounts = hold('/login/accounts');
      await rememberKey(page, key);
      const accountsRead = nextRead(page, '/login/accounts');
      await page.goto(origin + hostPage);
      await accountsRead;
      const frame = page.frameLocator('#fixture-plugin');
      await checkWaiting(frame, true);
      assert.equal(reads[0], '/login/accounts');
      releaseAccounts();
      await frame.locator('body.connected').waitFor();
      assert.equal(reads.filter(path => path === '/settings').length, 0);
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
      await frame.locator('#account-list-summary').filter({ hasText: '账号列表读取失败' }).waitFor();
      assert(await frame.locator('#account-import-empty').isHidden(), 'A failed list read cannot confirm there are no accounts');
      await frame.locator('#refresh').click();
      await frame.locator(accountRow).waitFor();
      return { primaryError: 503, settingsFallbackReads: 0, manualRefreshRecoveredAccounts: true };
    }, { failures: { '/login/accounts': [503] } });

    await scenario('commercial navigation from a slow account read to manual', async (page, hold, reads) => {
      await rememberKey(page, key);
      const release = hold('/login/accounts'), started = nextRead(page, '/login/accounts');
      await page.goto(origin + hostPage);
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('[data-page=manual]').click();
      await view.locator('body.connected').waitFor();
      await view.locator('#target option[value="fixture-quality-primary"]').waitFor({ state: 'attached' });
      assert.equal(reads.filter(name => name === '/login/accounts').length, 1);
      const denied = page.waitForResponse(response => new URL(response.url()).pathname === api + '/login/accounts' && response.status() === 403);
      release();
      await denied;
      await view.locator('#target').selectOption('fixture-quality-primary');
      assert(await view.locator('body.connected').count());
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
      assert.equal(await view.locator('[data-page][aria-current=page]').getAttribute('data-page'), 'manual');
      assert.equal(reads.filter(name => name === '/settings').length, 0);
      return { delayedAccountStatus: 403, lateResponseChangedSession: false };
    }, { failures: { '/login/accounts': [403] } });

    await scenario('commercial navigation to accounts while quality catalogs are pending', async (page, hold, reads) => {
      await rememberKey(page, key);
      const release = hold('/questions', '/targets'), started = nextRead(page, '/targets');
      await page.goto(origin + '/embedded.html?page=manual');
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('[data-page=login]').click();
      await view.locator('#account-import-open:enabled').waitFor();
      await checkReturning(view);
      const completed = page.waitForResponse(response => new URL(response.url()).pathname === api + '/targets');
      release();
      await completed;
      assert.equal(reads.filter(name => name === '/login/accounts').length, 1);
      assert(await view.locator(accountRow).isVisible());
      return { accountReads: 1, accountLoadedBeforeCatalogs: true };
    });

    await scenario('commercial navigation restores operations despite failed targets and slow metrics', async (page, hold, reads) => {
      await rememberKey(page, key);
      const release = hold('/metrics', '/runs'), started = nextRead(page, '/metrics');
      await page.goto(origin + '/embedded.html?page=operations');
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('body.connected').waitFor();
      await view.locator('[data-rule-select="fixture-rule-observe"]').waitFor({ state: 'attached' });
      assert(await view.locator('#data-state').evaluate(node => node.classList.contains('stale')));
      assert.equal(reads.filter(name => name === '/rules').length, 1);
      release();
      return { directoryStatus: 503, rulesVisibleBeforeMetricsAndHistory: true };
    }, { failures: { '/targets': [503] } });

    await scenario('commercial navigation opens rule history before either catalog arrives', async (page, hold, reads) => {
      await rememberKey(page, key);
      const targets = hold('/targets'), questions = hold('/questions');
      const started = nextRead(page, '/targets');
      await page.goto(origin + '/embedded.html?page=operations');
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('body.connected').waitFor();
      await view.locator('[data-rule-select="fixture-rule-observe"]').click();
      await view.locator('[data-rule-select="fixture-rule-observe"][aria-pressed=true]').waitFor();
      for (const selector of ['#rule-new', '[data-rule-edit="fixture-rule-observe"]', '[data-rule-run="fixture-rule-observe"]', '#run-submit']) {
        assert(await view.locator(selector).isDisabled(), selector + ' requires its current catalogs');
      }
      targets();
      await view.locator('#target option[value="fixture-quality-primary"]').waitFor({ state: 'attached' });
      assert(await view.locator('[data-rule-edit="fixture-rule-observe"]').isDisabled());
      questions();
      await view.locator('[data-rule-edit="fixture-rule-observe"]:enabled').waitFor();
      await view.locator('[data-rule-edit="fixture-rule-observe"]').click();
      await view.locator('#rule-dialog[open]').waitFor();
      assert(await view.locator('#rule-question').inputValue());
      assert.equal(reads.filter(resource => resource === '/questions').length, 1);
      return { ruleHistoryAvailableBeforeCatalogs: true, editorWaitedForBothCatalogs: true };
    });

    await scenario('commercial navigation shares pending data across rapid switches', async (page, hold, reads) => {
      await rememberKey(page, key);
      const accounts = hold('/login/accounts'), catalogs = hold('/targets', '/questions', '/rules');
      const started = nextRead(page, '/login/accounts');
      await page.goto(origin + hostPage);
      await started;
      const view = page.frameLocator('#fixture-plugin');
      const quality = nextRead(page, '/questions');
      await view.locator('[data-page=manual]').click();
      await quality;
      for (const section of ['login', 'operations', 'manual', 'login', 'operations']) await view.locator(`[data-page=${section}]`).click();
      catalogs();
      await view.locator('body.connected').waitFor();
      accounts();
      for (const path of ['/login/accounts', '/questions', '/rules', '/targets']) {
        assert.equal(reads.filter(resource => resource === path).length, 1, path + ' must not be duplicated');
      }
      return { repeatedSwitches: 5, duplicateCatalogReads: 0 };
    });

    for (const [body, reply] of [
      ['object', 403],
      ['plain', { status: 403, contentType: 'text/plain', body: 'Synthetic denial' }],
      ['null', { status: 403, contentType: 'application/json', body: 'null' }],
    ]) await scenario('commercial navigation on forbidden account admission ' + body, async (page, hold, reads) => {
      await rememberKey(page, key);
      await page.goto(origin + hostPage);
      const view = page.frameLocator('#fixture-plugin');
      await view.getByRole('button', { name: '已连接', exact: true }).waitFor();
      await view.locator('#account-list-summary').filter({ hasText: '403' }).waitFor();
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), key);
      assert.equal(reads[0], '/login/accounts');
      assert.equal(reads.includes('/settings'), false);
      assert.equal(await view.locator('[data-account-row]').count(), 0);
      return { forbiddenStatus: 403, secondaryValidationRequests: 0 };
    }, { failures: { '/login/accounts': [reply] } });

    await scenario('commercial navigation reports a rejected first key accurately', async page => {
      await page.goto(origin + hostPage);
      const view = page.frameLocator('#fixture-plugin');
      await view.getByRole('button', { name: '未连接', exact: true }).click();
      await view.locator('#management-key').fill(key);
      await view.locator('#connection-form button[type=submit]').click();
      const note = view.locator('#notice').filter({ hasText: '403' });
      await note.waitFor();
      assert.doesNotMatch(await note.innerText(), /已保存的密钥/);
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
      return { forbiddenStatus: 403, savedKey: false };
    }, { failures: { '/connection': [403] } });

    await scenario('commercial navigation invalidates the same key on a late 401', async (page, hold, reads) => {
      await rememberKey(page, key);
      const release = hold('/login/accounts'), started = nextRead(page, '/login/accounts');
      await page.goto(origin + hostPage);
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('[data-page=manual]').click();
      await view.locator('body.connected').waitFor();
      release();
      await view.getByRole('button', { name: '未连接', exact: true }).waitFor();
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
      assert.equal(await view.locator('#target option[value="fixture-quality-primary"]').count(), 0);
      assert.equal(reads.filter(name => name === '/login/accounts').length, 1);
      return { lateUnauthorizedStatus: 401, oldDataCleared: true };
    }, { failures: { '/login/accounts': [401] } });

    await scenario('commercial navigation keeps a pending disconnect effective', async (page, hold, reads) => {
      await rememberKey(page, key);
      const release = hold('/login/accounts'), started = nextRead(page, '/login/accounts');
      await page.goto(origin + hostPage);
      await started;
      const view = page.frameLocator('#fixture-plugin');
      await view.locator('#connection-open').click();
      await view.locator('#disconnect').click();
      release();
      await view.getByRole('button', { name: '未连接', exact: true }).waitFor();
      await view.locator('[data-page=manual]').click();
      await view.locator('[data-page=login]').click();
      assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
      assert.equal(await view.locator('[data-account-row]').count(), 0);
      assert.deepEqual(reads, ['/login/accounts', '/connection'].slice(0, reads.length));
      assert(reads.length <= 2);
      return { disconnectedBeforeReply: true };
    });

    for (const section of ['manual', 'operations']) {
      await scenario(`sidebar restores ${section} before statistics and history finish`, async (page, hold, reads) => {
        await page.goto(origin + '/management.html#/auth-files');
        await page.evaluate(([name, value]) => localStorage.setItem(name, value), [storedKey, key]);
        const frame = page.frameLocator('#fixture-plugin');
        await page.getByRole('link', { name: 'Credential Ops' }).click();
        await frame.locator('#account-import-open:enabled').waitFor();
        await frame.locator(`[data-page=${section}]`).click();
        await frame.locator(section === 'manual' ? '#target option[value="fixture-quality-primary"]' : '[data-rule-select="fixture-rule-observe"]').waitFor({ state: 'attached' });
        await page.getByRole('link', { name: '认证文件' }).click();
        await page.locator('#fixture-plugin').waitFor({ state: 'detached' });
        const before = reads.length;
        const release = hold('/metrics', '/runs');
        const viewRead = nextRead(page, section === 'manual' ? '/runs' : '/metrics');
        await page.getByRole('link', { name: 'Credential Ops' }).click();
        await viewRead;
        await frame.locator('body.connected').waitFor();
        assert.equal(await frame.locator('[data-page][aria-current=page]').getAttribute('data-page'), section);
        assert(await frame.locator(`#page-${section}`).isVisible());
        assert(await frame.locator('#page-login').isHidden());
        assert.equal(reads.slice(before).some(path => path.startsWith('/login/')), false);
        const whilePending = reads.slice(before);
        assert.equal(whilePending.filter(path => path === '/metrics').length, section === 'manual' ? 0 : 1);
        assert(whilePending.includes('/questions') && whilePending.includes('/targets') && whilePending.includes('/runs'));
        release();
        await frame.locator('#target option[value="fixture-quality-primary"]').waitFor({ state: 'attached' });
        await checkNoCachedAccountData(page);

        const fresh = await page.context().newPage();
        try {
          await fresh.goto(origin + hostPage);
          const freshFrame = fresh.frameLocator('#fixture-plugin');
          await freshFrame.locator('#account-import-open:enabled').waitFor();
          assert.equal(await freshFrame.locator('[data-page][aria-current=page]').getAttribute('data-page'), 'login');
          assert.equal(await frame.locator('[data-page][aria-current=page]').getAttribute('data-page'), section);
        } finally { await fresh.close(); }

        // This fixture creates a new iframe whose src carries an explicit, different workspace.
        const explicit = section === 'manual' ? 'operations' : 'manual';
        await page.goto(`${origin}/embedded.html?page=${explicit}`);
        await frame.locator('body.connected').waitFor();
        assert.equal(await frame.locator('[data-page][aria-current=page]').getAttribute('data-page'), explicit);
        await frame.locator('#connection-top').click();
        await frame.locator('#disconnect').click();
        await frame.getByRole('button', { name: '未连接', exact: true }).waitFor();
        assert.equal(await page.evaluate(name => localStorage.getItem(name), storedKey), null);
        const afterDisconnect = reads.length;
        await page.goto(origin + hostPage);
        await frame.getByRole('button', { name: '未连接', exact: true }).waitFor();
        assert.equal(await frame.locator('[data-page][aria-current=page]').getAttribute('data-page'), 'login');
        assert.equal(reads.length, afterDisconnect);
        return { restoredWorkspace: section, slowReads: section === 'manual' ? ['/runs'] : ['/metrics','/runs'], independentTab: 'login', explicitWorkspace: explicit };
      });
    }

    await scenario('persisted navigation restores with authenticated reads', async (page, hold) => {
      await rememberKey(page, key);
      await page.goto(origin);
      await page.locator('#account-import-open:enabled').waitFor();
      const release = hold('/login/accounts');
      const requested = nextRead(page, '/login/accounts');
      await page.evaluate(() => {
        dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
        dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
      });
      await requested;
      await checkShell(page);
      await checkSavedConnection(page);
      release();
      await page.locator(accountRow).waitFor();
    });

    for (const section of ['manual', 'operations']) {
      await scenario(`direct link to ${section}`, async (page, hold, reads) => {
        await rememberKey(page, key);
        await page.goto(`${origin}/#${section}`);
        await page.locator('body.connected').waitFor();
        assert(await page.locator(`#page-${section}`).isVisible());
        assert(await page.locator('#page-login').isHidden());
        assert.equal(reads.includes('/login/accounts'), false);
      });
    }
    if (!Object.keys(report).length) throw Error('No browser scenarios matched the selected filter');
    if (process.env.UI_OUTPUT) {
      await fs.mkdir(process.env.UI_OUTPUT, { recursive: true });
      await fs.writeFile(path.join(process.env.UI_OUTPUT, 'account-navigation.json'), JSON.stringify(report, null, 2) + '\n');
    }
  } finally {
    try { if (browser) await browser.close(); } finally { fixture.kill(); await exited; }
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
