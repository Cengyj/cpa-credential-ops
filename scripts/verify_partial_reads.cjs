// Exercise read failures through the same static page and management routes as CPA.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

function gate() {
  let release;
  const promise = new Promise(resolve => { release = resolve; });
  return { promise, release };
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exited = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line);
    const { origin, hostname, port } = new URL(url);
    assert.equal(hostname, '127.0.0.1');
    assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const out = process.env.UI_OUTPUT, results = {};
    if (out) await fs.mkdir(out, { recursive: true });
    const capture = (page, name) => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const status = page => page.evaluate(() => ({
      connected: document.body.classList.contains('connected'),
      saved: !!localStorage.getItem('model-quality:management-key:/v0/management/plugins/model-quality'),
      header: document.querySelector('#data-state').textContent,
      warning: document.querySelector('#data-state').classList.contains('stale'),
      credentialNote: document.querySelector('#target-summary').textContent,
      canSubmit: !document.querySelector('#run-submit').disabled,
      metrics: document.querySelector('.metric-value').textContent,
      metricsNote: document.querySelector('#metrics-read-note:not([hidden])')?.textContent || '',
      historyNote: document.querySelector('#history-read-note:not([hidden])')?.textContent || '',
      rulesNote: document.querySelector('#rules-read-note:not([hidden])')?.textContent || '',
      canRunRule: !!document.querySelector('[data-rule-run]:enabled'),
      questionNote: document.querySelector('#question-read-note:not([hidden])')?.textContent || '',
      historyText: document.querySelector('#history-list .empty-line')?.textContent || '',
      historyTotal: document.querySelector('#history-total').textContent,
      canClearHistory: !document.querySelector('#history-clear-all').disabled,
      historyPage: document.querySelector('#history-page').textContent,
      firstHistory: document.querySelector('#history-list [data-run]')?.dataset.run || '',
      qualityRows: document.querySelectorAll('#operations-history [data-run]').length,
      overflow: document.documentElement.scrollWidth > innerWidth,
    }));
    async function scenario(name, interact, { width = 1440, height = 900, theme = 'light', embedded = false } = {}) {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: 'reduce' });
      try {
        const outer = await context.newPage(), control = {}, writes = [], errors = [];
        outer.setDefaultTimeout(6000);
        outer.on('pageerror', error => errors.push(error.message));
        await context.route('**/*', async route => {
          const request = route.request(), u = new URL(request.url());
          assert.equal(u.origin, origin);
          if (request.method() !== 'GET') writes.push(request.method() + ' ' + u.pathname);
          const resource = u.searchParams.get('source') || u.pathname.split('/').pop();
          if (resource === 'metrics' && control.holdMetrics) {
            control.metricsEntered?.release();
            await control.holdMetrics.promise;
          }
          if (control.split && u.pathname === '/v0/management/plugins/model-quality/targets') {
            const offset = Number(u.searchParams.get('offset'));
            control.offsets.push(offset);
            if (!offset) { control.firstEntered.release(); await control.split.promise; }
            return route.fulfill({json:{total:2,snapshot:'two',items:[{auth_id:'partition-' + offset}]}});
          }
          if (resource === control.failure) return route.fulfill({ status: control.code || 503, json: { error: '离线读取故障' } });
          if (resource === control.invalid) return route.fulfill({ status: 200, contentType: 'application/json', body: 'null' });
          if (resource === 'metrics') return route.fulfill({ json: {
            quality_pass_rate: control.recovered ? 0.5 : 0.75, correct: control.recovered ? 4 : 6, judged: 8, planned: 10,
            unknown: 2, incorrect: control.recovered ? 4 : 2, coverage: 0.8, known_tokens: 120, unknown_usage: 0, dropped_events: 0,
          } });
          return route.continue();
        });
        await outer.goto(embedded ? origin + '/embedded.html?theme=' + theme + '&page=manual' : url);
        let page = outer;
        if (embedded) {
          await outer.waitForSelector('#fixture-plugin');
          page = outer.frames().find(frame => frame !== outer.mainFrame());
        } else await page.selectOption('#theme-toggle', theme);
        const settle = async () => {
          await page.waitForFunction(() => !document.querySelector('#refresh[aria-busy=true], #connection-form [aria-busy=true]'));
          await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        };
        const connect = async () => {
          await page.click('#connection-open');
          await page.fill('#management-key', management_key);
          await page.click('#connection-form button[type=submit]');
          await settle();
        };
        const choose = async () => {
          await page.click('[data-page=manual]');
          await page.selectOption('#target', 'fixture-quality-primary');
        };
        const refresh = async () => { await page.click('#refresh'); await settle(); };
        const historyClick = async selector => {
          const response = outer.waitForResponse(response => new URL(response.url()).searchParams.get('source') === 'manual_test');
          await page.click(selector);
          await (await response).finished(); await settle();
        };
        results[name] = await interact({ page, outer, control, settle, connect, choose, refresh, historyClick });
        assert.deepEqual(writes, [], name);
        assert.deepEqual(errors, [], name);
      } finally { await context.close(); }
    }
    await scenario('initial_history_failure', async ({ page, outer, control, connect, choose, refresh, historyClick }) => {
      control.failure = 'manual_test';
      await page.click('[data-page=manual]');
      await connect();
      if ((await status(page)).connected) { await choose(); await historyClick('[data-manual-view=history]'); }
      else await page.click('[data-manual-view=history]');
      const failed = await status(page);
      await capture(outer, 'connection-history');
      control.failure = '';
      if (failed.connected) await refresh(); else await connect();
      return { failed, recovered: await status(page) };
    });
    await scenario('metrics_failure', async ({ page, outer, control, connect, choose, refresh }) => {
      await connect(); await choose();
      await page.click('[data-page=operations]');
      await refresh();
      control.failure = 'metrics';
      await refresh();
      const failed = await status(page);
      await capture(outer, 'metrics');
      control.failure = ''; control.invalid = 'metrics';
      await refresh();
      const malformed = await status(page);
      control.invalid = ''; control.recovered = true;
      await refresh();
      return { failed, malformed, recovered: await status(page) };
    }, { width: 390, height: 844, theme: 'dark' });
    await scenario('history_pagination', async ({ page, outer, control, connect, choose, historyClick }) => {
      await connect(); await choose();
      await historyClick('[data-manual-view=history]');
      const initial = await status(page);
      control.failure = 'manual_test';
      await historyClick('#history-next');
      const failed = await status(page);
      await capture(outer, 'history-pagination');
      control.failure = '';
      if (await page.locator('#history-next').isEnabled()) {
        await historyClick('#history-next');
      }
      return { initial, failed, recovered: await status(page) };
    }, { width: 1440, height: 640, theme: 'white' });
    await scenario('catalog_failure', async ({ page, outer, control, connect, choose, refresh }) => {
      await connect();
      const connected = await status(page);
      await choose();
      control.failure = 'targets';
      await refresh();
      const failed = await status(page);
      await capture(outer, 'catalog');
      control.failure = '';
      await refresh();
      const recovered = await status(page);
      control.failure = 'manual_test'; control.code = 401;
      await refresh();
      return { connected, failed, recovered, unauthorized: await status(page) };
    }, { width: 320, height: 720, theme: 'white', embedded: true });
    await scenario('catalog_with_slow_metrics', async ({ page, outer, control, connect, choose, settle }) => {
      await connect(); await choose();
      await page.click('[data-page=operations]');
      control.holdMetrics = gate(); control.failure = 'targets';
      const failure = outer.waitForResponse(response => new URL(response.url()).pathname.endsWith('/targets') && response.status() === 503);
      let failed;
      try {
        await page.click('#refresh');
        await (await failure).finished();
        await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
        failed = await status(page);
      } finally { control.holdMetrics.release(); }
      await settle();
      return { failed };
    });
    await scenario('rules_and_questions', async ({ page, control, connect, choose, refresh }) => {
      await connect(); await choose();
      await page.click('[data-page=operations]'); await refresh();
      const initial = await status(page);
      control.failure = 'rules'; await refresh();
      const staleRules = await status(page);
      control.failure = 'questions'; await refresh();
      const staleQuestions = await status(page);
      control.failure = ''; await refresh();
      return { initial, staleRules, staleQuestions, recovered: await status(page) };
    });
    await scenario('late_metrics_on_manual', async ({ page, outer, control, connect, choose, settle }) => {
      await connect(); await choose(); await settle();
      control.metricsEntered = gate(); control.holdMetrics = gate();
      await page.click('[data-page=operations]'); await control.metricsEntered.promise;
      await page.click('[data-page=manual]');
      control.failure = 'metrics';
      const failed = outer.waitForResponse(r => r.url().includes('/metrics?') && r.status() === 503);
      control.holdMetrics.release(); await (await failed).finished(); await settle();
      const manual = await status(page);
      assert(!manual.warning && manual.canSubmit);
      return {manual,oldMetricsAffectedCurrentWorkspace:false};
    });
    await scenario('paged_directory_leaving', async ({ page, outer, control, connect, choose, settle }) => {
      await connect(); await choose(); await settle();
      control.split = gate(); control.firstEntered = gate(); control.offsets = [];
      await page.click('#refresh'); await control.firstEntered.promise;
      await page.click('[data-page=login]');
      const response = outer.waitForResponse(r => new URL(r.url()).pathname === '/v0/management/plugins/model-quality/targets');
      control.split.release(); await (await response).finished(); await settle();
      assert.deepEqual(control.offsets,[0]);
      return {requestedOffsets:[...control.offsets],displayReadAfterLeaving:0};
    });
    if (out) await fs.writeFile(path.join(out, 'reads.json'), JSON.stringify(results, null, 2) + '\n');
    if (process.argv.includes('--observe')) return;
    const initial = results.initial_history_failure;
    assert(initial.failed.connected && initial.failed.saved && initial.failed.canSubmit);
    assert(initial.failed.warning);
    assert.equal(initial.failed.qualityRows, 0, 'Opening manual history must not fetch operations data');
    assert.match(initial.failed.historyText, /读取失败/);
    assert.equal(initial.failed.historyTotal, '');
    assert(!initial.failed.canClearHistory && initial.recovered.canClearHistory);
    assert(initial.recovered.firstHistory && !initial.recovered.warning);
    const metrics = results.metrics_failure;
    assert(metrics.failed.warning && metrics.failed.canSubmit);
    assert.equal(metrics.failed.metrics, '75.0%');
    assert.match(metrics.failed.metricsNote, /上次|过期/);
    assert(metrics.malformed.warning && metrics.malformed.metricsNote && metrics.malformed.canSubmit);
    assert(!metrics.recovered.metricsNote && !metrics.recovered.warning);
    assert.equal(metrics.recovered.metrics, '50.0%');
    const history = results.history_pagination;
    assert.equal(history.initial.firstHistory, history.failed.firstHistory);
    assert.equal(history.failed.historyPage, '第 1 页');
    assert.match(history.failed.historyNote, /读取失败/);
    assert(!history.failed.canClearHistory && history.recovered.canClearHistory);
    assert.equal(history.recovered.historyPage, '第 2 页');
    assert.notEqual(history.failed.firstHistory, history.recovered.firstHistory);
    const catalog = results.catalog_failure;
    assert(catalog.connected.firstHistory && catalog.connected.canClearHistory, 'A direct manual workspace connection enables history cleanup immediately');
    assert(catalog.failed.connected && catalog.failed.canSubmit, 'The previous complete selection can request a fresh admission read');
    assert.match(catalog.failed.credentialNote, /未确认|读取失败/);
    assert(!catalog.recovered.warning && catalog.recovered.canSubmit);
    assert(!catalog.unauthorized.connected && !catalog.unauthorized.saved);
    assert(results.catalog_with_slow_metrics.failed.canSubmit, 'The saved selection can recheck the directory while unrelated metrics are pending');
    const rules = results.rules_and_questions;
    assert(rules.initial.canRunRule && !rules.staleRules.canRunRule && rules.staleRules.canSubmit && rules.staleRules.rulesNote);
    assert(rules.staleQuestions.canRunRule && !rules.staleQuestions.canSubmit && rules.staleQuestions.questionNote);
    assert(rules.recovered.canRunRule && rules.recovered.canSubmit);
    assert(!Object.values(results).some(scenario => Object.values(scenario).some(snapshot => snapshot.overflow)));
    console.log('Partial history, metrics and credential reads, pagination retries, recovery and 401 invalidation passed using only fixture GETs.');
  } finally {
    await browser?.close();
    fixture.kill(); await exited;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
