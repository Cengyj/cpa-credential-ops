// Exercise concurrent UI state with private fake management responses only.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

function gate() {
  let release;
  return { promise: new Promise(resolve => { release = resolve; }), release: (...args) => release(...args) };
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const info = JSON.parse(line), origin = new URL(info.url).origin;
    assert.equal(new URL(origin).hostname, '127.0.0.1');
    assert.notEqual(new URL(origin).port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const out = process.env.UI_OUTPUT, report = {};
    if (out) await fs.mkdir(out, { recursive: true });
    const capture = (page, name) => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const baseRule = { id: 'saved-rule', revision: 1, name: 'Saved rule', target_scope: 'all_enabled', object_types: ['oauth'],
      enabled: true, timer_active: false, action: 'observe_only', models: ['fixture-reasoner'], samples: 1,
      question_id: 'logic-candy', judge: { mode: 'deterministic' }, parameters: { reasoning_effort: 'high' },
      max_results: 100, cron: '*/30 * * * *' };
    async function scenario(name, interact, initialRules = []) {
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
      const page = await context.newPage();
      page.setDefaultTimeout(6000);
      await page.clock.install();
      const control = { rules: structuredClone(initialRules), mode: 'success', ruleReads: 0, targetReads: 0, commits: 0 };
      const writes = [], errors = [];
      page.on('pageerror', error => errors.push(error.message));
      await context.route('**/*', async route => {
        const request = route.request(), url = new URL(request.url());
        assert.equal(url.origin, origin);
        const api = url.pathname.slice('/v0/management/plugins/model-quality'.length);
        if (api === '/rules' && request.method() === 'GET') {
          control.ruleReads++;
          if (control.failRules) return route.fulfill({ status: 503, json: { error: 'Synthetic list failure' } });
          if (control.hideRules) return route.fulfill({ json: { items: [], total: 0, offset: 0 } });
          return route.fulfill({ json: { items: control.rules, total: control.rules.length, offset: 0 } });
        }
        if (api === '/targets' && request.method() === 'GET') {
          control.targetReads++;
          if (control.failTargets) return route.fulfill({ status: 503, json: { error: 'Synthetic target failure' } });
        }
        if (request.method() !== 'GET') {
          assert.equal(api, '/rules');
          assert(['POST', 'PUT'].includes(request.method()));
          const body = request.postDataJSON(), method = request.method(), mode = control.mode;
          writes.push({ method, body });
          if (mode === 'hold') {
            control.entered.release();
            await control.hold.promise;
            return route.fulfill({ status: 400, json: { error: 'Synthetic validation rejection' } });
          }
          if (mode === '400' || mode === '409') return route.fulfill({ status: Number(mode), json: { error: 'Synthetic validation rejection' } });
          const existing = control.rules.find(rule => rule.id === body.id);
          if (method === 'PUT' && existing?.revision !== body.revision) return route.fulfill({ status: 409, json: { error: 'Synthetic stale revision' } });
          const saved = { ...body, id: body.id || 'created-' + writes.length, revision: (existing?.revision || 0) + 1,
            enabled: true, timer_active: body.activation === 'start' };
          if (existing) control.rules[control.rules.indexOf(existing)] = saved;
          else control.rules.push(saved);
          control.commits++;
          if (mode === 'drop') return route.abort('connectionfailed');
          if (mode === '408' || mode === '503') return route.fulfill({ status: Number(mode), json: { error: 'Synthetic uncertain receipt' } });
          if (mode === 'malformed') return route.fulfill({ status: 200, contentType: 'application/json', body: '{' });
          if (mode === 'empty') return route.fulfill({ json: {} });
          return route.fulfill({ json: saved });
        }
        return route.continue();
      });
      try {
        await page.goto(info.url);
        await page.click('#connection-open');
        await page.fill('#management-key', info.management_key);
        await page.click('#connection-form button[type=submit]');
        await page.waitForSelector('body.connected');
        await page.click('[data-page=operations]');
        await page.waitForSelector('#rule-new:enabled');
        const save = () => page.click('#rule-form button[type=submit]');
        const create = async () => {
          await page.click('#rule-new');
          await page.fill('#rule-name', 'Concurrent draft');
          await page.check('#rule-timer');
        };
        const tick = async (milliseconds = 5100) => {
          const read = page.waitForResponse(response => new URL(response.url()).pathname.endsWith('/targets'));
          await page.clock.fastForward(milliseconds);
          await (await read).finished();
          await page.waitForTimeout(80);
        };
        report[name] = await interact({ page, control, writes, save, create, tick });
        assert.deepEqual(errors, [], name);
      } finally { await context.close(); }
    }

    await scenario('saving_poll_keeps_controls_locked', async ({ page, control, writes, save, create, tick }) => {
      await create();
      control.mode = 'hold'; control.entered = gate(); control.hold = gate();
      await save(); await control.entered.promise;
      await tick();
      const unlocked = await page.locator('#rule-form').evaluate(form => [...form.querySelectorAll('input,select,textarea,button')].filter(node => !node.disabled).map(node => node.id || node.name));
      assert.deepEqual(unlocked, []);
      assert.equal(await page.locator('#rule-name').inputValue(), 'Concurrent draft');
      await capture(page, 'saving-poll-locked');
      control.hold.release();
      await page.waitForSelector('#rule-form button[type=submit]:enabled');
      assert(await page.locator('[name=rule-action][value=disable_credential]').isEnabled());
      assert.equal(writes.length, 1);
      return { pendingPollUnlockedControls: unlocked, validationFailureAllowsEditing: true, writes: writes.length };
    });

    for (const mode of ['drop', '408', '503', 'malformed', 'empty']) {
      await scenario('unknown_create_' + mode, async ({ page, control, writes, save, create, tick }) => {
        await create(); control.mode = mode;
        const readsBefore = control.ruleReads;
        await save();
        await page.locator('#rule-dialog .dialog-message').filter({ hasText: '保存结果未确认' }).waitFor();
        await page.waitForFunction(() => !document.querySelector('#rule-name').disabled);
        assert.equal(await page.locator('#rule-name').inputValue(), 'Concurrent draft');
        assert(await page.locator('#rule-timer').isChecked());
        assert(await page.locator('#rule-form button[type=submit]').isDisabled());
        await page.locator('#rule-form').evaluate(form => form.requestSubmit());
        await tick();
        assert(await page.locator('#rule-form button[type=submit]').isDisabled());
        assert.match(await page.locator('#rule-dialog .dialog-message').innerText(), /保存结果未确认/);
        assert.equal(writes.length, 1);
        assert.equal(control.commits, 1);
        assert(control.ruleReads > readsBefore);
        if (mode === 'drop') await capture(page, 'unknown-create-draft-preserved');
        await page.click('#rule-form .editor-footer [data-close]');
        await page.click('#action-confirm');
        await page.waitForSelector('#rule-dialog', { state: 'hidden' });
        await page.waitForSelector('[data-rule-edit="created-1"]');
        return { writes: writes.length, committed: control.commits, draftPreserved: true, retryBlocked: true, listRefreshed: true };
      });
    }

    await scenario('unknown_create_failed_list', async ({ page, control, writes, save, create }) => {
      await create(); control.mode = 'drop'; control.failRules = true;
      await save();
      await page.locator('#rule-dialog .dialog-message').filter({ hasText: '规则列表读取失败' }).waitFor();
      assert(await page.locator('#rule-form button[type=submit]').isDisabled());
      await page.locator('#rule-form').evaluate(form => form.requestSubmit());
      assert.equal(writes.length, 1);
      return { writes: writes.length, unavailableListDoesNotAuthorizeRetry: true };
    });

    await scenario('unknown_create_empty_list', async ({ page, control, writes, save, create, tick }) => {
      await create(); control.mode = 'drop'; control.hideRules = true;
      await save();
      await page.locator('#rule-dialog .dialog-message').filter({ hasText: '保存结果未确认' }).waitFor();
      await tick();
      assert(await page.locator('#rule-form button[type=submit]').isDisabled());
      await page.locator('#rule-form').evaluate(form => form.requestSubmit());
      assert.equal(writes.length, 1);
      return { writes: writes.length, emptyListDoesNotAuthorizeRetry: true };
    });

    await scenario('unsaved_draft_survives_poll_and_read_failure', async ({ page, control, writes, tick }) => {
      await page.click('[data-rule-edit=saved-rule]');
      await page.fill('#rule-name', 'Unsaved local title');
      await page.selectOption('#rule-question', 'custom');
      await page.fill('#rule-prompt', 'Preserve this question\n第二行');
      await page.fill('#rule-models', 'new-unsaved-model');
      await page.check('#rule-timer');
      const snapshot = () => page.evaluate(() => ({
        name: document.querySelector('#rule-name').value, prompt: document.querySelector('#rule-prompt').value,
        models: [...document.querySelectorAll('#rule-model-selection code')].map(node => node.textContent),
        pendingModel: document.querySelector('#rule-models').value, judge: document.querySelector('#rule-judge-mode').value,
        timer: document.querySelector('#rule-timer').checked, revision: document.querySelector('#rule-revision').value,
      }));
      const before = await snapshot();
      control.rules[0].revision = 2; control.rules[0].name = 'Other session title';
      control.failTargets = true;
      await tick(); assert.deepEqual(await snapshot(), before);
      control.failTargets = false;
      await tick(12500); assert.deepEqual(await snapshot(), before);
      assert.match(await page.locator('#rule-dialog .editor-dirty').innerText(), /未保存/);
      assert.equal(writes.length, 0);
      return { draftUnchanged: true, localRevision: before.revision, serverRevision: 2, writes: writes.length };
    }, [baseRule]);

    for (const mode of ['400', '409']) {
      await scenario('known_create_rejection_' + mode, async ({ page, control, writes, save, create }) => {
        await create(); control.mode = mode;
        await save(); await page.waitForSelector('#rule-form button[type=submit]:enabled');
        assert.equal(control.commits, 0);
        await page.fill('#rule-name', 'Corrected draft'); control.mode = 'success';
        await save(); await page.waitForSelector('#rule-dialog', { state: 'hidden' });
        assert.equal(writes.length, 2); assert.equal(control.commits, 1);
        assert.equal(control.rules[0].name, 'Corrected draft');
        return { writes: writes.length, committed: control.commits, correctedSaveSucceeded: true };
      });
    }

    await scenario('unknown_edit_keeps_revision_protection', async ({ page, control, writes, save }) => {
      await page.click('[data-rule-edit=saved-rule]'); await page.fill('#rule-name', 'Updated draft');
      control.mode = 'drop'; await save(); await page.waitForSelector('#rule-form button[type=submit]:enabled');
      assert.equal(control.rules[0].revision, 2);
      control.mode = 'success'; await save(); await page.waitForSelector('#rule-form button[type=submit]:enabled');
      await page.locator('#rule-dialog .dialog-message').filter({ hasText: 'stale revision' }).waitFor();
      assert.equal(writes.length, 2); assert.equal(control.commits, 1);
      assert.deepEqual(writes.map(write => write.method), ['PUT', 'PUT']);
      assert.deepEqual(writes.map(write => write.body.revision), [1, 1]);
      return { writes: writes.length, committed: control.commits, frozenRevision: 1, staleRetryRejected: true };
    }, [baseRule]);

    if (out) await fs.writeFile(path.join(out, 'quality-concurrency.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
