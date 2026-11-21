// Exercise operation receipts and retained handling with isolated fake management responses.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const baseRule = { id: 'retained-rule', revision: 1, name: '待核对规则', target_scope: 'all_enabled', object_types: ['oauth'],
  enabled: true, timer_active: false, action: 'observe_only', models: ['fixture-reasoner'], samples: 1,
  question_id: 'logic-candy', judge: { mode: 'deterministic' }, cron: '*/30 * * * *' };
const records = [
  { auth_id: 'file-one', action_id: 'file-action', state: 'attention', email: 'file@example.test' },
  { auth_id: 'route-one', action_id: 'shared-action', state: 'attention' },
  { auth_id: 'route-two', action_id: 'shared-action', state: 'attention' },
];
function gate() {
  let resolve;
  return { wait: new Promise(done => { resolve = done; }), release: () => resolve() };
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const info = JSON.parse(line), { origin, port, hostname } = new URL(info.url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const out = process.env.UI_OUTPUT, report = {};
    if (out) await fs.mkdir(out, { recursive: true });
    async function scenario(name, interact, initialRules = [baseRule], initialOrphans = []) {
      if (process.env.UI_SCENARIO && process.env.UI_SCENARIO !== name) return;
      const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
      const page = await context.newPage();
      page.setDefaultTimeout(6500);
      await page.clock.install();
      const control = { rules: structuredClone(initialRules), orphans: structuredClone(initialOrphans), acknowledgements: [],
        writes: [], failRules: false, failTargets: false, nextMetricsUnauthorized: false, ruleReads: 0 };
      const errors = [];
      page.on('pageerror', error => errors.push(error.message));
      await context.route('**/*', async route => {
        const request = route.request(), url = new URL(request.url());
        assert.equal(url.origin, origin);
        const api = url.pathname.slice('/v0/management/plugins/model-quality'.length);
        if (request.method() === 'GET') {
          if (api === '/rules') {
            control.ruleReads++;
            if (control.failRules) return route.fulfill({ status: 503, json: { error: 'Synthetic rule read failure' } });
            const offset = Number(url.searchParams.get('offset') || 0), limit = Number(url.searchParams.get('limit') || 100);
            return route.fulfill({ json: { items: control.rules.slice(offset, offset + limit), total: control.rules.length, offset, orphaned_handling: control.orphans } });
          }
          if (api === '/targets' && control.failTargets) return route.fulfill({ status: 503, json: { error: 'Synthetic directory failure' } });
          if (api === '/metrics') {
            if (control.metricsHold) {
              const held = control.metricsHold;
              control.metricsHold = null; held.entered.release(); await held.finish.wait;
            }
            if (control.nextMetricsUnauthorized) {
              control.nextMetricsUnauthorized = false;
              return route.fulfill({ status: 401, json: { error: 'Synthetic expired session' } });
            }
          }
          return route.continue();
        }
        const body = request.postDataJSON();
        control.writes.push({ api, method: request.method(), body });
        if (api === '/rules/timer' && request.method() === 'PUT') {
          if (control.timerSuccess) {
            const rule = control.rules.find(rule => rule.id === body.id);
            assert.equal(body.revision, rule.revision);
            Object.assign(rule, { scheduling_enabled: body.enabled, timer_active: body.enabled && rule.enabled, revision: rule.revision + 1 });
            return route.fulfill({ json: rule });
          }
          control.metricsHold = { entered: control.followupEntered, finish: control.followupFinish };
          return route.fulfill({ status: 409, json: { error: 'Synthetic stale rule' } });
        }
        if (api === '/rules/scope-preview' && request.method() === 'POST') return route.fulfill({ json: { verified: true, count: 1, skipped: 0, candidates: { oauth: 1, provider: 0 }, recovery_count: 0 } });
        if (api === '/rules/run' && request.method() === 'POST') {
          control.nextMetricsUnauthorized = true;
          return route.fulfill({ status: 503, json: { error: 'Synthetic unknown dispatch receipt' } });
        }
        if (api === '/rules' && request.method() === 'DELETE') {
          const deleted = control.rules.find(rule => rule.id === body.id && rule.revision === body.revision);
          assert(deleted);
          control.orphans.push(...deleted.handling.filter(record => record.state === 'attention'));
          control.rules = control.rules.filter(rule => rule !== deleted);
          return route.fulfill({ json: { ok: true } });
        }
        if (api === '/rules' && request.method() === 'PUT') {
          const rule = control.rules.find(rule => rule.id === body.id);
          assert.equal(body.revision, rule.revision);
          assert.equal(body.activation, 'keep', 'Unchanged stored scheduling must not start or stop a rule');
          Object.assign(rule, body, { revision: rule.revision + 1 });
          return route.fulfill({ json: rule });
        }
        if (api === '/rules/handling/acknowledge' && request.method() === 'POST') {
          control.acknowledgements.push(body);
          const source = body.rule_id ? control.rules.find(rule => rule.id === body.rule_id)?.handling : control.orphans;
          const found = source?.find(record => record.auth_id === body.auth_id && record.action_id === body.action_id);
          if (!found) return route.fulfill({ status: 409, json: { error: 'Synthetic changed action identity' } });
          if (control.ackHold) {
            const held = control.ackHold; control.ackHold = null;
            held.entered.release(); await held.finish.wait;
          }
          if (control.ackBlocked) return route.fulfill({ status: 409, json: { error: '对象当前仍停用，请先在 CPA 核对状态。' } });
          if (body.rule_id) control.rules.find(rule => rule.id === body.rule_id).handling = source.filter(record => record.action_id !== body.action_id);
          else control.orphans = source.filter(record => record.action_id !== body.action_id);
          if (control.expireAfterAcknowledgement) control.nextMetricsUnauthorized = true;
          return route.fulfill({ json: { ok: true } });
        }
        throw Error('Unexpected synthetic mutation: ' + request.method() + ' ' + api);
      });
      const connect = async () => {
        await page.click('#connection-open'); await page.fill('#management-key', info.management_key);
        await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
      };
      const refresh = async () => {
        await page.click('#refresh'); await page.waitForSelector('#refresh:not([aria-busy=true])');
      };
      const tick = async () => { await page.clock.fastForward(5100); await page.waitForTimeout(100); };
      const capture = async suffix => out && page.screenshot({ path: path.join(out, name + '-' + suffix + '.png'), animations: 'disabled' });
      try {
        await page.goto(info.url); await connect(); await page.click('[data-page=operations]');
        await page.waitForSelector('#rule-new:enabled');
        report[name] = await interact({ page, control, connect, refresh, tick, capture });
        assert.deepEqual(errors, []);
      } catch (error) {
        await capture('failed');
        throw error;
      } finally { control.metricsHold?.finish.release(); control.followupFinish?.release(); await context.close(); }
    }

    await scenario('timer_error_after_disconnect', async ({ page, control }) => {
      control.followupEntered = gate(); control.followupFinish = gate();
      await page.click('[data-rule-timer=retained-rule]'); await control.followupEntered.wait;
      await page.click('#connection-top'); await page.click('#disconnect');
      await page.waitForSelector('body:not(.connected)'); await page.waitForTimeout(100);
      assert.match(await page.locator('#notice').innerText(), /已清除管理密钥与页面数据/);
      assert.doesNotMatch(await page.locator('#notice').innerText(), /规则状态已变更/);
      control.followupFinish.release();
      return { writes: control.writes.length, staleErrorDiscarded: true };
    });

    await scenario('unknown_run_after_expiry', async ({ page, control }) => {
      await page.click('[data-rule-run=retained-rule]'); await page.click('#action-confirm');
      await page.waitForSelector('body:not(.connected)'); await page.waitForTimeout(100);
      assert.match(await page.locator('#notice').innerText(), /管理鉴权已失效/);
      assert.doesNotMatch(await page.locator('#notice').innerText(), /提交结果未确认/);
      assert.equal(control.writes.filter(write => write.api === '/rules/run').length, 1);
      return { dispatchWrites: 1, staleReceiptDiscarded: true };
    });

    await scenario('acknowledgement_after_expiry', async ({ page, control }) => {
      await page.click('[data-rule-select=retained-rule]'); await page.click('[data-rule-handling] > summary');
      await page.click('[data-handling-ack=file-action]'); control.expireAfterAcknowledgement = true;
      await page.click('#action-confirm'); await page.waitForSelector('body:not(.connected)'); await page.waitForTimeout(100);
      assert.match(await page.locator('#notice').innerText(), /管理鉴权已失效/);
      assert.doesNotMatch(await page.locator('#notice').innerText(), /处置结果已核对/);
      assert.equal(control.acknowledgements.length, 1);
      return { acknowledgements: 1, staleSuccessDiscarded: true };
    }, [{ ...baseRule, handling: [records[0]] }]);

    await scenario('deleted_rule_retains_handling', async ({ page, control, refresh, tick, capture }) => {
      await page.click('[data-rule-delete=retained-rule]'); await page.click('#action-confirm');
      await page.waitForSelector('#action-dialog', { state: 'hidden' });
      const panel = page.locator('#ops-orphaned-handling');
      await panel.waitFor({ state: 'visible' });
      assert.equal(control.rules.length, 0);
      assert.match(await panel.innerText(), /已删除规则的待核对处置.*3/s);
      await panel.locator('summary').click();
      await panel.locator('[data-handling-ack=file-action]').waitFor();
      control.failRules = true; await refresh();
      assert(await panel.locator('[data-handling-ack=file-action]').isDisabled());
      assert.match(await panel.innerText(), /读取失败/);
      control.failRules = false; control.failTargets = true; await refresh();
      assert(await panel.locator('[data-handling-ack=file-action]').isDisabled());
      control.failTargets = false; await refresh();
      const member = panel.locator('.quality-handling-records > div').filter({ hasText: 'route-two' });
      await member.locator('[data-handling-ack]').click(); await page.click('#action-cancel');
      assert.equal(control.acknowledgements.length, 0);
      await member.locator('[data-handling-ack]').click();
      control.ackBlocked = true; await page.click('#action-confirm');
      await page.locator('#action-error').getByText('对象当前仍停用', { exact: false }).waitFor();
      assert.deepEqual(control.acknowledgements[0], { rule_id: '', auth_id: 'route-two', action_id: 'shared-action' });
      assert.equal(control.orphans.length, 3);
      await page.click('#action-cancel'); control.ackBlocked = false;
      await tick(); await capture('retained');
      await member.locator('[data-handling-ack]').click();
      control.ackHold = { entered: gate(), finish: gate() }; const held = control.ackHold;
      await page.click('#action-confirm'); await held.entered.wait;
      await page.locator('#action-form').evaluate(form => { form.requestSubmit(); form.requestSubmit(); });
      await tick();
      assert.equal(control.acknowledgements.length, 2);
      assert(await page.locator('#action-confirm').isDisabled());
      held.finish.release(); await page.waitForSelector('#action-dialog', { state: 'hidden' });
      await member.waitFor({ state: 'detached' }); assert.equal(control.orphans.length, 1);
      await panel.locator('[data-handling-ack=file-action]').click(); await page.click('#action-confirm');
      await page.waitForSelector('#action-dialog', { state: 'hidden' }); await panel.waitFor({ state: 'hidden' });
      assert.deepEqual(control.acknowledgements.at(-1), { rule_id: '', auth_id: 'file-one', action_id: 'file-action' });
      assert.equal(control.writes.filter(write => write.api === '/rules').length, 1);
      assert.equal(control.writes.filter(write => write.api === '/rules/run' || write.api === '/rules/timer').length, 0);
      return { deletedRules: 1, acknowledgements: 3, blockedRetained: true, exactMember: true, duplicateBlocked: true, noCPAChanges: true };
    }, [{ ...baseRule, handling: records }]);

    await scenario('orphan_read_paginated_and_reconnect', async ({ page, control, connect, tick, capture }) => {
      const panel = page.locator('#ops-orphaned-handling'); await panel.waitFor({ state: 'visible' });
      assert.equal(await page.locator('#rules-list .quality-rule').count(), 101);
      await panel.locator('summary').click();
      await panel.locator('[data-handling-ack]').waitFor();
      assert.equal(await panel.locator('[data-handling-ack]').count(), 1);
      await tick(); assert.equal(await panel.locator('[data-handling-ack]').count(), 1);
      await page.setViewportSize({ width: 390, height: 844 });
      await panel.scrollIntoViewIfNeeded(); await capture('mobile');
      const dimensions = await page.evaluate(() => ({ width: document.documentElement.clientWidth, scroll: document.documentElement.scrollWidth }));
      assert(dimensions.scroll <= dimensions.width + 1);
      await page.click('#connection-top'); await page.click('#disconnect'); await panel.waitFor({ state: 'hidden' });
      control.orphans = []; await connect(); await page.waitForSelector('#rule-new:enabled');
      assert(await panel.isHidden()); assert.equal(control.acknowledgements.length, 0);
      return { rules: 101, orphanRecords: 1, noDuplicatesAcrossPages: true, disconnectClearsRecords: true, dimensions };
    }, Array.from({ length: 101 }, (_, index) => ({ ...baseRule, id: 'rule-' + index, name: 'Rule ' + index })), [records[0]]);

    for (const [name, patch] of [
      ['global_off', { scheduling_enabled: true, timer_active: false }],
      ['rule_paused', { scheduling_enabled: true, timer_active: false, enabled: false, status: 'paused' }],
      ['running', { scheduling_enabled: true, timer_active: true }],
      ['legacy_running', { timer_active: true }],
    ]) {
      await scenario('stored_timer_' + name, async ({ page, control, capture }) => {
        const toggle = page.locator('[data-rule-timer=retained-rule]');
        assert.equal(await toggle.getAttribute('aria-checked'), 'true', 'Stored timer authorization must remain visible even when inactive');
        if (!patch.timer_active) {
          assert.match(await toggle.innerText(), /定时未运行/);
          assert.match(await toggle.getAttribute('title'), /关闭已保存定时/);
        }
        await page.click('[data-rule-edit=retained-rule]');
        assert(await page.locator('#rule-timer').isChecked());
        assert.equal(await page.locator('#rule-timer').getAttribute('data-initial'), 'true');
        if (!patch.timer_active) assert.match(await page.locator('#rule-schedule-note').innerText(), /保留定时设置.*保存不会重新启动/);
        await page.fill('#rule-name', 'Renamed only');
        await page.click('#rule-form button[type=submit]');
        await page.waitForSelector('#rule-dialog', { state: 'hidden' });
        assert.equal(control.rules[0].enabled, patch.enabled ?? true);
        assert.equal(control.rules[0].timer_active, patch.timer_active);
        assert.equal(control.writes[0].body.activation, 'keep');
        await capture('preserved');
        control.timerSuccess = true;
        await toggle.click();
        await page.waitForFunction(() => document.querySelector('[data-rule-timer=retained-rule]').getAttribute('aria-checked') === 'false');
        const writes = control.writes.filter(write => write.api === '/rules/timer');
        assert.equal(writes.length, 1); assert.equal(writes[0].body.enabled, false);
        assert.equal(control.rules[0].scheduling_enabled, false);
        assert.equal(control.rules[0].enabled, patch.enabled ?? true);
        await page.click('[data-rule-edit=retained-rule]'); assert(!await page.locator('#rule-timer').isChecked());
        await page.click('#rule-form .editor-footer [data-close]');
        return { keptActivation: 'keep', storedPermissionRevoked: true, preservedEnabled: patch.enabled ?? true, timerWrites: 1 };
      }, [{ ...baseRule, ...patch }]);
    }

    if (out) await fs.writeFile(path.join(out, 'operations-sessions.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
