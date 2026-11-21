// Control the order of synthetic writes and reads through the real UI.
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
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line);
    const { origin } = new URL(url);
    assert.notEqual(new URL(url).port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const results = {};
    const out = process.env.UI_OUTPUT;
    if (out) await fs.mkdir(out, { recursive: true });
    const screenshot = (page, name) => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    async function scenario(name, interact) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
      const page = await context.newPage();
      page.setDefaultTimeout(6000);
      const tasks = new Map(), reads = [], posts = [], cancels = [], errors = [];
      const control = {};
      page.on('pageerror', e => errors.push(e.message));
      await context.route('**/*', async route => {
        const req = route.request(), u = new URL(req.url());
        assert.equal(u.origin, origin);
        const p = u.pathname;
        if (p.endsWith('/runs') && req.method() === 'POST') {
          const config = req.postDataJSON();
          posts.push(config.idempotency_key);
          if (control.blockReason) return route.fulfill({ status: 409, json: { code: 'blocked', error: control.blockReason } });
          if (control.dropBeforeAdmission) { control.dropBeforeAdmission = false; return route.abort('connectionfailed'); }
          let run = tasks.get(config.idempotency_key);
          if (!run) {
            run = { id: 'manual-' + (tasks.size + 1), source: 'manual_test', status: 'running', config, samples: [], created_at: '2026-11-20T02:00:00Z' };
            tasks.set(config.idempotency_key, run);
          }
          if (control.ack) await control.ack.promise;
          if (control.loseAck) { control.loseAck = false; return route.abort('connectionfailed'); }
          return route.fulfill({ status: 202, json: { run_id: run.id } });
        }
        if (p.endsWith('/runs/cancel') && req.method() === 'POST') {
          cancels.push(req.postDataJSON().id);
          return route.fulfill({ json: {} });
        }
        assert.equal(req.method(), 'GET');
        if (p.endsWith('/plugins/model-quality/targets')) {
          control.targetReads = (control.targetReads || 0) + 1;
          const held = control.targetHolds?.shift();
          if (control.targetState || held) {
            const data = await (await route.fetch()).json();
            const target = data.items.find(row => row.auth_id === 'fixture-quality-primary');
            Object.assign(target, control.targetState, held?.state);
            if (held) {
              held.entered.release();
              await held.wait.promise;
              if (held.status) return route.fulfill({ status: held.status, json: { error: 'Synthetic target read failure' } });
            }
            return route.fulfill({ json: data });
          }
        }
        if (p.endsWith('/runs/detail')) {
          const id = u.searchParams.get('id'), key = u.searchParams.get('idempotency_key');
          reads.push(id || 'by-receipt');
          if (control.hold?.id === id) {
            const { entered, wait } = control.hold;
            entered.release();
            await wait.promise;
          }
          if (control.otherActive === id && id) {
            const run = await (await route.fetch()).json();
            return route.fulfill({ json: { ...run, status: 'running' } });
          }
          if (key) {
            if (control.failReceiptReads) {
              control.failReceiptReads--;
              return route.fulfill({ status: 503, json: { error: { code: 'unavailable', message: 'Synthetic read failure' } } });
            }
            const found = tasks.get(key);
            return route.fulfill(found ? { json: found } : { status: 404, json: { error: { code: 'not_found', message: 'No synthetic run' } } });
          }
          const manual = [...tasks.values()].find(run => run.id === id);
          if (manual) return route.fulfill({ json: manual });
          if (id === 'fixture-run-observation' || id === 'fixture-run-observation-error') {
            const run = await (await route.fetch()).json();
            return route.fulfill({ json: { ...run, id, status: 'running', action_status: 'disable_pending' } });
          }
        }
        if (p.endsWith('/runs') && u.searchParams.get('source') === 'manual_test') {
          const history = await (await route.fetch()).json();
          return route.fulfill({ json: { ...history, items: [...tasks.values(), ...history.items].slice(0, 10), total: history.total + tasks.size } });
        }
        return route.continue();
      });
      try {
        await page.goto(url);
        await page.click('#login-connect');
        await page.fill('#management-key', management_key);
        await page.click('#connection-form button[type=submit]');
        await page.waitForSelector('body.connected');
        await page.click('[data-page=manual]');
        async function configure() {
          await page.selectOption('#target', 'fixture-quality-primary');
          await page.selectOption('#models', 'fixture-reasoner');
          await page.selectOption('#question', 'logic-candy');
        }
        await configure();
        results[name] = await interact({ page, tasks, reads, posts, cancels, control, configure });
        assert.deepEqual(errors, []);
      } finally { await context.close(); }
    }
    const selected = page => page.locator('#run-frozen-data .run-identifier').textContent({ timeout: 1500 }).catch(() => '');
    const submit = page => page.locator('#run-submit');
    const download = async (page, button) => {
      const pending = page.waitForEvent('download');
      await page.click(button);
      const file = await pending;
      return JSON.parse(await fs.readFile(await file.path(), 'utf8')).id;
    };
    await scenario('manual_error_policy', async ({ page, control, posts, tasks }) => {
      const history = { code: 'error_policy_disabled', model: 'former-model', reason: 'CPA 注册目录缺少所选模型' };
      const manualModel = 'test-model-9,manual-alias';
      const models = ['fixture-reasoner', ...Array.from({ length: 13 }, (_, i) => 'test-model-' + (i + 1))];
      models[9] = manualModel;
      const changeTarget = async state => {
        control.targetState = { models, ...state };
        await page.click('#refresh');
        await page.waitForSelector('#refresh:not([aria-busy="true"])');
      };
      const summary = page.locator('#target-summary');
      const setup = page.locator('#manual-setup');
      const policyText = /错误策略|质量运维|处置|上次.*停用|former-model|开始测试时将核对|CPA 注册目录缺少所选模型/;
      await changeTarget({});
      assert.deepEqual(new Set(await page.locator('#models > option').evaluateAll(options => options.map(o => o.value))), new Set(models));
      await page.selectOption('#models', manualModel);
      const healthySummary = await summary.textContent();
      assert.equal(healthySummary, '凭证可用');
      assert.equal(await page.locator('#model-note').textContent(), '');
      assert.doesNotMatch(await setup.innerText(), /可选\s*14\s*个模型/);
      await changeTarget({ credential_handling: 'disabled', error_policy_block: history, manual_disabled: true, runtime_status: 'disabled', effective_available: false });
      assert.equal(await summary.textContent(), '目标凭证当前仍在 CPA 停用，停用期间退出答题和恢复复检。');
      assert.doesNotMatch(await setup.innerText(), policyText);
      assert(await submit(page).isEnabled());
      await page.locator('#run-form').evaluate(form => form.requestSubmit());
      await page.waitForFunction(() => !document.querySelector('#run-submit').disabled);
      assert.equal(posts.length, 0);
      await changeTarget({ credential_handling: 'attention', error_policy_block: history });
      assert.equal(await summary.textContent(), '当前凭证状态尚未确认，无法开始智商测试。请刷新后重试。');
      assert.doesNotMatch(await setup.innerText(), policyText);
      assert(await submit(page).isDisabled());
      await changeTarget({ credential_handling: 'disabled', error_policy_block: history });
      assert.equal(await summary.textContent(), healthySummary);
      assert.equal(await page.locator('#models').inputValue(), manualModel);
      assert.doesNotMatch(await setup.innerText(), policyText);
      assert(await submit(page).isEnabled());
      await screenshot(page, 'manual-model-selection');
      const refusals = [
        ['blocked: credential already disabled by error policy', /刷新|不可用|停用/],
        ['blocked: CPA 注册目录缺少所选模型，CPA 当前仍停用此凭证。', /停用|禁用/],
        ['blocked: CPA 凭证身份核对失败，请刷新。', /凭证身份|状态/],
        ['blocked: 凭证存在未解除的处置记录，请在质量运维中核对。', /凭证.*状态|刷新/],
        ['blocked: 凭证的处置记录在测试提交后发生了变化，请重新核对。', /凭证.*状态|刷新/],
      ];
      for (const [message, expected] of refusals) {
        control.blockReason = message;
        const rejected = page.waitForResponse(r => r.url().endsWith('/runs') && r.request().method() === 'POST' && r.status() === 409);
        await submit(page).click();
        await (await rejected).finished();
        await page.waitForSelector('#run-submit:enabled');
        const notice = page.locator('#notice').filter({ hasText: expected });
        await notice.waitFor();
        assert.doesNotMatch(await notice.innerText(), /blocked|credential already disabled/);
        assert.doesNotMatch(await notice.innerText(), policyText);
      }
      assert.equal(posts.length, refusals.length);
      assert.equal(tasks.size, 0);
      control.blockReason = '';
      await submit(page).click();
      await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      assert.equal(posts.length, refusals.length + 1);
      assert.equal(tasks.size, 1);
      const submitted = tasks.get(posts.at(-1)).config;
      assert.equal(submitted.auth_id, 'fixture-quality-primary');
      assert.deepEqual(submitted.models, [manualModel]);
      assert.equal(submitted.judge.mode, 'manual');
      assert.equal(submitted.question_id, 'logic-candy');
      assert.doesNotMatch(JSON.stringify(submitted), /former-model|error_policy|credential_handling/);
      return { options: models.length, selectedModel: manualModel, submittedModels: submitted.models, accepted: tasks.size, rejected: posts.length - tasks.size };
    });
    await scenario('disabled_capability_refresh', async ({ page, control, posts, tasks }) => {
      await page.selectOption('#reasoning', 'high');
      const disabled = { manual_disabled: true, runtime_status: 'disabled', effective_available: false,
        credential_handling: 'disabled', models: [], reasoning: {} };
      const cacheDisabled = async () => {
        control.targetState = disabled;
        await page.click('#refresh');
        await page.waitForSelector('#refresh:not([aria-busy="true"])');
        assert.equal(await page.locator('#models').inputValue(), 'fixture-reasoner');
        assert.equal(await page.locator('#reasoning').inputValue(), 'high');
        assert(await submit(page).isEnabled(), 'A complete cached disabled plan must be able to read current capabilities');
        assert.equal(await page.locator('#model-note').innerText(), '当前缓存未提供此模型，开始测试时将刷新核对。');
      };
      const attempt = async () => {
        const before = control.targetReads;
        const refreshed = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/plugins/model-quality/targets'));
        await submit(page).click();
        await (await refreshed).finished();
        await page.waitForSelector('#run-submit:not([aria-busy="true"])');
        assert(control.targetReads > before, 'Submission must fetch the current target before validation');
      };
      await cacheDisabled();
      await screenshot(page, 'disabled-capability-ready');
      await attempt();
      assert.equal(posts.length, 0, 'A still-disabled target must not create a model task');
      control.targetState = { models: ['fixture-visual'], reasoning: {} };
      await attempt();
      assert.equal(posts.length, 0, 'A model absent from the current enabled directory must not create a model task');
      assert.equal(await page.locator('#model-note').innerText(), '原模型已从目录移除，请重新选择模型。');
      assert(await submit(page).isDisabled());
      await cacheDisabled();
      control.targetState = { reasoning: { 'fixture-reasoner': { levels: ['low'] } } };
      await attempt();
      assert.equal(posts.length, 0, 'A currently unsupported reasoning effort must not create a model task');
      assert(await submit(page).isDisabled());
      assert.match(await page.locator('#notice').innerText(), /思考强度.*支持范围/);
      await cacheDisabled();
      control.targetState = {};
      const before = control.targetReads;
      await submit(page).click();
      await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      assert(control.targetReads > before);
      assert.equal(posts.length, 1);
      assert.equal(tasks.size, 1);
      const submitted = tasks.get(posts[0]).config;
      assert.deepEqual(submitted.models, ['fixture-reasoner']);
      assert.equal(submitted.parameters.reasoning_effort, 'high');
      await screenshot(page, 'restored-capability-submitted');
      return { rejectedPosts: 0, accepted: tasks.size, model: submitted.models[0], effort: submitted.parameters.reasoning_effort };
    });
    for (const order of ['submit_first', 'visibility_first', 'latest_failure']) {
      await scenario('concurrent_target_reads_' + order, async ({ page, control, posts }) => {
        const disabled = { manual_disabled: true, runtime_status: 'disabled', effective_available: false };
        const first = { entered: gate(), wait: gate(), state: order === 'visibility_first' ? {} : disabled };
        const latest = { entered: gate(), wait: gate(), state: disabled, ...(order === 'latest_failure' ? { status: 503 } : {}) };
        control.targetHolds = [first, latest];
        await submit(page).click(); await first.entered.promise;
        await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')));
        await latest.entered.promise;
        const early = order === 'visibility_first' ? latest : first;
        const late = early === first ? latest : first;
        const response = page.waitForResponse(r => new URL(r.url()).pathname.endsWith('/plugins/model-quality/targets'));
        early.wait.release(); await (await response).finished();
        await page.waitForTimeout(80);
        assert.equal(posts.length, 0, 'A superseded preflight must not submit using old enabled state');
        late.wait.release();
        await page.waitForSelector('#run-submit:not([aria-busy="true"])');
        assert.equal(posts.length, 0);
        if (order === 'latest_failure') {
          assert(await submit(page).isDisabled());
          assert.match(await page.locator('#target-summary').innerText(), /读取失败/);
        } else assert.match(await page.locator('#target-summary').innerText(), /停用/);
        return { posts: posts.length, newestReadControlsAdmission: true, responseOrder: order };
      });
    }
    await scenario('target_read_session_switch', async ({ page, control, posts, configure }) => {
      const held = { entered: gate(), wait: gate(), state: { manual_disabled: true, runtime_status: 'disabled', effective_available: false } };
      control.targetHolds = [held];
      await submit(page).click(); await held.entered.promise;
      await page.click('#connection-top'); await page.click('#disconnect');
      await page.waitForSelector('body:not(.connected)');
      held.wait.release();
      await page.click('#connection-open'); await page.fill('#management-key', management_key);
      await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
      await configure(); await page.waitForSelector('#run-submit:enabled');
      assert.equal(posts.length, 0, 'An old preflight cannot submit into the new connection');
      assert.equal(await page.locator('#target-summary').innerText(), '凭证可用');
      return { posts: posts.length, oldSessionDiscarded: true, newConnectionReady: true };
    });
    await scenario('history_poll_after_selection', async ({ page, control }) => {
      control.otherActive = 'fixture-run-complete';
      await page.click('[data-manual-view=history]');
      await page.click('[data-run=fixture-run-complete]');
      await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('fixture-run-complete'));
      control.hold = { id: control.otherActive, entered: gate(), wait: gate() };
      await control.hold.entered.promise;
      await page.click('[data-manual-view=history]');
      await page.click('[data-run=fixture-run-visual]');
      await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('fixture-run-visual'));
      const late = page.waitForResponse(r => r.url().endsWith('/runs/detail?id=fixture-run-complete'));
      control.hold.wait.release();
      await (await late).finished();
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      return { afterLatePoll: await selected(page), exported: await download(page, '#run-export') };
    });
    await scenario('delayed_ack_and_dialog', async ({ page, reads, control, cancels, posts }) => {
      control.ack = gate();
      const receipt = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/runs'));
      await submit(page).click();
      await page.locator('#manual-config-summary').filter({ hasText: '提交中' }).waitFor();
      assert.equal(await submit(page).getAttribute('aria-busy'), 'true');
      await page.click('[data-manual-view=history]');
      await page.click('[data-run=fixture-run-complete]');
      await page.waitForFunction(() => document.querySelector('#run-frozen-data')?.textContent.includes('fixture-run-complete'));
      control.ack.release();
      await receipt;
      await page.locator('#manual-config-summary').filter({ hasText: '测试中' }).waitFor();
      const readAfterAck = await selected(page);
      await screenshot(page, 'slow-receipt');
      await page.click('[data-manual-view=history]');
      control.hold = { id: 'fixture-run-visual', entered: gate(), wait: gate() };
      await page.click('[data-run=fixture-run-visual]');
      await control.hold.entered.promise;
      const pollDuringHistoryRead = page.waitForRequest(r => r.url().endsWith('/runs/detail?id=manual-1'), { timeout: 3500 }).then(() => true, () => false);
      await page.click('[data-page=operations]');
      await page.click('#operations-history [data-run=fixture-run-observation]');
      await page.waitForSelector('#quality-detail-export:visible');
      const initialDownload = await download(page, '#quality-detail-export');
      const trackedDuringHistory = await pollDuringHistoryRead;
      await page.keyboard.press('Escape');
      await page.click('#operations-history [data-run=fixture-run-observation-error]');
      await page.waitForSelector('#quality-detail-export:visible');
      const flushed = page.waitForResponse(r => r.url().endsWith('/runs/detail?id=fixture-run-visual'));
      control.hold.wait.release();
      await flushed;
      await page.click('#quality-detail-cancel');
      const qualityDownload = await download(page, '#quality-detail-export');
      await page.keyboard.press('Escape');
      const trigger = await page.evaluate(() => document.activeElement?.dataset.run);
      await page.click('[data-page=manual]');
      const manualDownload = await download(page, '#run-export');
      return { posts: posts.length, readAfterAck, reads, trackedDuringHistory, initialDownload, qualityDownload, manualDownload, trigger, cancels };
    });
    await scenario('lost_receipt', async ({ page, reads, posts, tasks, control, configure }) => {
      control.loseAck = true;
      const lost = page.waitForEvent('requestfailed', { predicate: request => request.method() === 'POST' && request.url().endsWith('/runs') });
      await submit(page).click();
      await lost;
      await page.waitForFunction(() => !document.querySelector('#run-submit').disabled || document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      const recovered = await selected(page);
      await screenshot(page, 'lost-receipt');
      if (await submit(page).isEnabled()) await submit(page).click();
      await page.reload();
      await page.waitForSelector('body.connected');
      await page.click('[data-page=manual]');
      await configure();
      await page.waitForFunction(() => !document.querySelector('#run-submit').disabled || document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      const afterReconnect = await selected(page);
      return { posts: posts.length, distinctKeys: new Set(posts).size, admitted: tasks.size, recovered, afterReconnect, reads };
    });
    await scenario('missing_receipt', async ({ page, reads, posts, tasks, control }) => {
      control.dropBeforeAdmission = true;
      const lost = page.waitForEvent('requestfailed', { predicate: request => request.method() === 'POST' && request.url().endsWith('/runs') });
      await submit(page).click();
      await lost;
      await page.waitForSelector('#run-submit:enabled');
      const beforeRetry = [...reads];
      await submit(page).click();
      await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      return { beforeRetry, posts: posts.length, distinctKeys: new Set(posts).size, admitted: tasks.size };
    });
    await scenario('interrupted_receipt_read', async ({ page, reads, posts, tasks, control }) => {
      control.loseAck = true;
      control.failReceiptReads = 1;
      const failedRead = page.waitForResponse(response => response.status() === 503 && response.url().includes('idempotency_key='), { timeout: 3500 }).then(() => true, () => false);
      await submit(page).click();
      const checked = await failedRead;
      if (checked) {
        await page.locator('#manual-config-summary').filter({ hasText: '正在核对上次提交' }).waitFor();
        assert.equal(await submit(page).getAttribute('aria-busy'), 'true');
        assert(await submit(page).isDisabled());
        await page.locator('#run-form').evaluate(form => form.requestSubmit());
        await page.reload();
        await page.waitForSelector('body.connected');
        await page.click('[data-page=manual]');
        await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('manual-1'));
      }
      return { checked, reads, admitted: tasks.size, posts: posts.length, afterReconnect: await selected(page) };
    });
    if (out) await fs.writeFile(path.join(out, 'receipts.json'), JSON.stringify(results, null, 2) + '\n');
    const delayed = results.delayed_ack_and_dialog;
    assert.equal(delayed.posts, 1);
    assert.equal(delayed.readAfterAck, '任务 fixture-run-complete');
    assert(delayed.trackedDuringHistory, 'A slow history read must not pause the pending manual poll');
    assert.equal(delayed.initialDownload, 'fixture-run-observation');
    assert.equal(delayed.qualityDownload, 'fixture-run-observation-error');
    assert.equal(delayed.manualDownload, 'fixture-run-visual');
    assert.equal(delayed.trigger, 'fixture-run-observation-error');
    assert.equal(results.history_poll_after_selection.exported, 'fixture-run-visual');
    assert.deepEqual(delayed.cancels, ['fixture-run-observation-error']);
    assert.equal(results.lost_receipt.posts, 1);
    assert.equal(results.lost_receipt.admitted, 1);
    assert.equal(results.lost_receipt.recovered, '任务 manual-1');
    assert.equal(results.lost_receipt.afterReconnect, '任务 manual-1');
    assert(results.missing_receipt.beforeRetry.includes('by-receipt'));
    assert.equal(results.missing_receipt.posts, 2);
    assert.equal(results.missing_receipt.distinctKeys, 1);
    assert.equal(results.missing_receipt.admitted, 1);
    assert.equal(results.interrupted_receipt_read.posts, 1);
    assert.equal(results.interrupted_receipt_read.admitted, 1);
    assert.equal(results.interrupted_receipt_read.afterReconnect, '任务 manual-1');
    console.log('Delayed, lost and missing manual receipts, reconnect, reversed detail reads, dialog focus, cancellation and exports passed.');
  } finally {
    await browser?.close();
    fixture.kill();
    await exit;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
