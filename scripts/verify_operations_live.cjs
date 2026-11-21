// Verify scheduled activity updates through the embedded page using a synthetic management API.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line), { hostname, port, origin } = new URL(url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, timezoneId: 'Asia/Shanghai' });
    page.setDefaultTimeout(7500);
    await page.clock.install();
    const prefix = '/v0/management/plugins/model-quality', reads = [], writes = [], errors = [];
    const rule = { id: 'live-rule', name: '动态范围', target_scope: 'all_enabled', object_types: ['oauth'], enabled: true,
      timer_active: true, models: ['fixture-reasoner'], samples: 1, action: 'observe_only',
      last_scope_batch: 'batch-new', next_run_local: '2026-11-21T08:10:00+08:00',
      last_scope_dispatch: { matched: 1, skipped: 0, at: '2026-11-21T00:00:00Z' } };
    const run = (id, ruleID = rule.id, batch = 'batch-old') => ({
      id, rule_id: ruleID, source: 'scheduled_probe', status: 'completed', samples: [], created_at: '2026-11-21T00:00:00Z',
      config: { rule_id: ruleID, auth_id: id + '@example.test', scope_batch_id: batch, models: ['fixture-reasoner'], samples: 1, question_id: 'logic-candy', judge: { mode: 'deterministic' } },
    });
    const rows = [run('outside', 'other-rule'), ...Array.from({length: 21}, (_, i) => run('row-' + i))];
    let tokens = 10, hold, failActivity = false, failTargets = false;
    let target = { auth_id: 'live-target', auth_index: 'live-route', provider: 'openai', provider_name: '合成供应商', is_provider: true,
      object_type: 'provider', models: ['fixture-reasoner'], manual_disabled: true, runtime_status: 'disabled', effective_available: false,
      error_policy_block: { code: 'error_policy_disabled', reason: 'synthetic previous 401' } };
    page.on('pageerror', err => errors.push(err.message));
    await page.route(origin + prefix + '/**', async route => {
      const req = route.request(), u = new URL(req.url()), p = u.pathname.slice(prefix.length);
      if (req.method() !== 'GET') { writes.push(p); return route.fulfill({status:405,json:{error:'fixture_read_only'}}); }
      reads.push(p + u.search);
      if (p === '/targets') {
        if (failTargets) { failTargets = false; return route.fulfill({status:503,json:{error:'synthetic target failure'}}); }
        return route.fulfill({json:{items:[target],total:1}});
      }
      if (p === '/rules') return route.fulfill({json:{items:[rule],total:1}});
      if (p === '/metrics') return route.fulfill({json:{known_tokens:tokens,judged:0,planned:0}});
      if (p === '/runs/detail') {
        const found = rows.find(row => row.id === u.searchParams.get('id'));
        if (found) return route.fulfill({json:found});
      }
      if (p === '/runs' && u.searchParams.get('source') === 'scheduled_probe') {
        assert.equal(u.searchParams.get('view'), 'summary');
        const id = u.searchParams.get('rule_id'), batch = u.searchParams.get('scope_batch_id');
        const result = rows.filter(row => (!id || row.rule_id === id) && (!batch || row.config.scope_batch_id === batch));
        const offset = Number(u.searchParams.get('offset')), limit = Number(u.searchParams.get('limit'));
        const snapshot = JSON.stringify({ total: result.length, offset, items: result.slice(offset, offset + limit) });
        if (hold) { const gate = hold; hold = null; gate.enter(); await gate.wait; }
        if (failActivity) { failActivity = false; return route.fulfill({status:503,json:{error:'synthetic history failure'}}); }
        return route.fulfill({body:snapshot,contentType:'application/json'});
      }
      return route.continue();
    });

    await page.goto(origin + '/management.html#/plugin-pages/model-quality/0');
    const view = page.frameLocator('#fixture-plugin'), el = selector => view.locator(selector);
    const query = () => reads.filter(url => url.startsWith('/runs?') && url.includes('source=scheduled_probe'));
    const tick = async ms => { await page.clock.fastForward(ms); await page.waitForTimeout(80); };
    const displays = (selector, text) => el(selector).getByText(text, { exact: false }).first().waitFor();
    const connect = async () => {
      await el('#connection-open').click(); await el('#management-key').fill(management_key);
      await el('#connection-form button[type=submit]').click(); await el('body.connected').waitFor();
    };
    await connect();
    await el('[data-page=manual]').click();
    await el('#target').selectOption(target.auth_id);
    await el('#target-summary').getByText('目标供应商路由当前仍在 CPA 停用', {exact:false}).waitFor();
    assert(await el('#run-submit').isEnabled());
    await el('[data-page=operations]').click(); await displays('#ops-batch-summary', '1–20 / 22');
    const start = reads.length;
    await el('[data-run=row-0]').focus();
    rows.unshift(run('admitted-after-timer', rule.id, 'batch-new'));
    rows[0].status = 'running';
    target = {...target, manual_disabled:false, runtime_status:'registered', effective_available:true};
    tokens = 42; rule.next_run_local = '2026-11-21T08:15:00+08:00';
    await tick(5100);
    await el('[data-run=admitted-after-timer]').waitFor();
    await displays('#metrics', '42'); await displays('#rules-list', '08:15:00');
    assert(await el('[data-run=row-0]').evaluate(node => node === document.activeElement));
    const liveEndpoints = [...new Set(reads.slice(start).map(url => url.split('?')[0]))].sort();
    assert.deepEqual(liveEndpoints, ['/metrics', '/rules', '/runs', '/targets']);
    assert.equal(await el('#target-summary').textContent(), '通过 CPA 原生路由测试');
    assert(await el('#run-submit').isEnabled());
    await el('[data-rule-select=live-rule]').click();
    await displays('#ops-batch-summary', '1–20 / 22');
    await el('[data-skip-rule=live-rule] > summary').click();
    await el('#ops-batch-next').click(); await displays('#ops-batch-summary', '21–22 / 22');
    const offset = query().length;
    await tick(5100);
    assert.equal(query().length, offset + 1);
    assert.match(query().at(-1), /offset=20/); assert.match(query().at(-1), /rule_id=live-rule/);
    assert(await el('[data-skip-rule=live-rule]').evaluate(node => node.open));
    await el('[data-rule-batch=batch-new]').click(); await displays('#ops-batch-summary', '本批次 1–1 / 1');

    // A delayed read must not start a second poll or overwrite a more recent scope selection.
    let entered, release;
    const pending = new Promise(resolve => { entered = resolve; });
    hold = {enter:entered,wait:new Promise(resolve => { release=resolve; })};
    await tick(5100); await pending;
    const blocked = reads.length;
    await tick(10200); assert.equal(reads.length, blocked);
    await el('#ops-all-rules').click(); await displays('#ops-batch-summary', '1–20 / 23');
    release(); await page.waitForTimeout(100);
    assert.equal(await el('#ops-scope-label').textContent(), '全部规则');
    assert(await el('[data-run=outside]').isVisible());

    await el('[data-run=admitted-after-timer]').click(); await el('#quality-detail[open]').waitFor();
    await displays('#quality-detail-content', 'admitted-after-timer');
    rows[0].status = 'completed';
    await tick(5100);
    assert(await el('#quality-detail[open]').isVisible());
    await el('[data-close=quality-detail]').click();
    failActivity = true; await tick(5100);
    await el('#activity-read-note:not([hidden])').waitFor();
    assert(await el('[data-run=admitted-after-timer]').isVisible());
    await tick(12500); await el('#activity-read-note').waitFor({state:'hidden'});

    const invisible = async (hide, resume) => {
      await hide(); await page.waitForTimeout(120);
      const at = query().length; await tick(12500);
      assert.equal(query().length, at);
      await resume();
      await page.waitForFunction(() => document.querySelector('iframe')?.contentDocument?.querySelector('#ops-record-count'));
      await page.waitForTimeout(150);
      assert.equal(query().length, at + 1);
    };
    const visibility = hidden => el('html').evaluate((node, hidden) => {
      Object.defineProperty(document,'hidden',{configurable:true,value:hidden});
      document.dispatchEvent(new Event('visibilitychange'));
    }, hidden);
    await invisible(() => visibility(true), () => visibility(false));
    const frame = page.locator('#fixture-plugin');
    await invisible(() => frame.evaluate(n=>n.style.display='none'), () => frame.evaluate(n=>n.style.removeProperty('display')));
    await el('[data-page=manual]').click();
    const manual = query().length; await tick(12500); assert.equal(query().length, manual);
    await el('[data-page=operations]').click(); await el('[data-run=admitted-after-timer]').waitFor();
    await el('#connection-top').click(); await el('#disconnect').click();
    await el('body.connected').waitFor({state:'hidden'});
    const off = query().length; await tick(12500); assert.equal(query().length, off);
    rows.unshift(run('while-disconnected')); await connect(); await el('[data-run=while-disconnected]').waitFor();

    await page.getByRole('link',{name:'认证文件'}).click(); await frame.waitFor({state:'detached'});
    const destroyed = query().length; await tick(12500); assert.equal(query().length, destroyed);
    rows.unshift(run('host-return'));
    await page.getByRole('link',{name:'Credential Ops'}).click(); await el('body.connected').waitFor();
    await el('[data-page=operations]').click(); await el('[data-run=host-return]').waitFor();
    const resumed = query().length;
    await tick(5100); assert.equal(query().length, resumed + 1);
    failTargets = true; await tick(5100);
    assert(await el('#run-submit').isDisabled());
    assert.match(await el('#target-summary').textContent(), /读取失败/);
    assert.doesNotMatch(await el('#target-summary').textContent(), /停用|错误策略/);
    await tick(12500);
    assert.doesNotMatch(await el('#target-summary').textContent(), /读取失败/);
    assert.deepEqual(writes, []); assert.deepEqual(errors, []);
    const result = {liveEndpoints, scopeAndPagination:true, preservedFocus:true, preservedDetail:true, readsWhileHidden:0,
      noOverlappingPolls:true, recoveredAfterFailure:true, resumedAfterDisconnect:true, resumedAfterFrameRemoval:true, writes:writes.length};
    if (process.env.UI_OUTPUT) {
      await fs.mkdir(process.env.UI_OUTPUT,{recursive:true});
      await fs.writeFile(path.join(process.env.UI_OUTPUT,'operations-live.json'),JSON.stringify(result,null,2)+'\n');
    }
    console.log(JSON.stringify(result));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode=1; });
