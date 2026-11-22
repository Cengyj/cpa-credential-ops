// Exercise frozen scope records without contacting a model or mutating a CPA credential.
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
    const { url, management_key } = JSON.parse(line), { origin, port, hostname } = new URL(url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, timezoneId: 'Asia/Shanghai', reducedMotion: 'reduce' });
    page.setDefaultTimeout(7000);
    const out = process.env.UI_OUTPUT;
    if (out) await fs.mkdir(out, { recursive: true });
    const stamp = '2026-11-20T02:35:00Z', email = 'duplicate@example.test';
    const rule = { id: 'rule', name: '目录检查', revision: 5, enabled: true, timer_active: false, target_scope: 'all_credentials', action: 'observe_only', upstream_error_actions: { model_unsupported: 'disable_credential' }, models: ['missing-pro'], samples: 2, question_id: 'logic-candy', judge: { mode: 'deterministic' } };
    const skip = (id, auth_id, code, reason, model) => ({ run_id: id, auth_id, email, code, reason, ...(model ? { model } : {}) });
    const catalog = skip('catalog', 'alpha-file.json', 'missing_model', '可信凭证模型目录未注册该模型。', 'missing-pro');
    const protectedSkip = skip('protected', 'beta-file.json', 'quality_paused', '凭证保留质量暂停，禁止检测。');
    rule.handling = ['disabled', 'deleted', 'attention'].map(state => ({ state, action_id: state, auth_id: catalog.auth_id, catalog_missing_model: catalog.model }));
    const membership = [
      { auth_id: 'single-runtime', runtime_status: 'error', effective_available: false, models: [] },
      { auth_id: 'manual-disabled', runtime_status: 'disabled', manual_disabled: true },
      { auth_id: 'retained', runtime_status: 'active', retained_pause: true },
      { auth_id: catalog.auth_id, runtime_status: 'active', effective_available: true, models: ['missing-pro'] },
    ].map(target => ({ auth_index: target.auth_id, provider: 'codex', ...target }));
    const singles = membership.map(target => ({ ...rule, id: target.auth_id, name: target.auth_id, target_scope: 'single', auth_id: target.auth_id, handling: [] }));
    const sample = { model: 'missing-pro', index: 0, execution_status: 'failed', quality_verdict: 'unknown', upstream_condition: 'model_unsupported', error: 'model_not_found' };
    const config = { models: rule.models, samples: 2, rule_id: 'rule', judge: rule.judge, question_id: rule.question_id, question: { title: '冻结题目', prompt: '离线测试题面' } };
    const base = { source: 'scheduled_probe', created_at: stamp, credential_email: email, samples: [], action_status: 'observe_only' };
    const legacyConfig = { ...config, scope_batch_id: 'earlier', auth_id: catalog.auth_id, catalog_missing_model: catalog.model };
    const legacy = (id, change) => ({ ...base, id, status: 'skipped', config: legacyConfig, skip_detail: { ...catalog, run_id: id }, ...change });
    const oldCases = [
      { row: legacy('old-queue-full', { block_reason: 'queue_full' }), state: '未执行', explanation: '队列容量不足，本轮未入队。' },
      { row: legacy('old-budget', { block_reason: 'budget_exhausted' }), state: '未执行', explanation: '当时的调用预算不足，记录未区分具体上限。' },
      { row: legacy('token-budget', { block_reason: 'token_budget_exhausted' }), state: '未执行', explanation: '当日 token 预算不足，未发起这次模型调用。' },
      { row: legacy('old-cancelled', { status: 'cancelled', cancel_requested: true, error: 'context canceled' }), state: '已取消', explanation: '测试已取消' },
      { row: legacy('old-action', { action_status: 'credential_suspended', credential_action: 'disable_credential' }), state: '已检测', explanation: catalog.reason },
    ];
    const rows = [
      { ...base, id: 'catalog', status: 'completed', config: { ...config, scope_batch_id: 'mixed', auth_id: catalog.auth_id, catalog_missing_model: catalog.model }, preflight_detail: catalog, action_status: 'credential_suspended', credential_action: 'disable_credential' },
      { ...base, id: 'protected', status: 'skipped', config: { ...config, scope_batch_id: 'mixed', auth_id: protectedSkip.auth_id }, skip_detail: protectedSkip },
      { ...base, id: 'http', status: 'failed', config: { ...config, scope_batch_id: 'older', auth_id: 'gamma-file.json', samples: 1 }, upstream_condition: 'model_unsupported', samples: [sample], action_status: 'credential_removed', credential_action: 'delete_credential' },
      { ...base, id: 'changed', status: 'skipped', config: { ...config, scope_batch_id: 'earlier', auth_id: catalog.auth_id, catalog_missing_model: catalog.model }, skip_detail: { ...catalog, run_id: 'changed' }, action_status: 'catalog_action_unconfirmed' },
      { ...base, id: 'read-failure', status: 'failed', config: { ...config, auth_id: catalog.auth_id }, preflight_detail: skip('read-failure', catalog.auth_id, 'snapshot_unavailable', '凭证文件快照读取失败。') },
      { ...base, id: 'identity', status: 'blocked', config: { ...config, auth_id: catalog.auth_id }, preflight_detail: skip('identity', catalog.auth_id, 'identity_conflict', '当前文件身份与凭证目录冲突。') },
      ...oldCases.map(entry => entry.row),
    ];
    const errors = [], writes = [], reads = [], exported = [], captures = [];
    let pending = false;
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const req = route.request(), u = new URL(req.url()); assert.equal(u.origin, origin);
      const api = u.pathname.slice('/v0/management/plugins/model-quality'.length);
      if (req.method() === 'GET') {
        if (api === '/targets') return route.fulfill({ json: { items: membership, total: membership.length, offset: 0 } });
        if (api === '/rules') return route.fulfill({ json: { items: [rule, ...singles], total: 1 + singles.length, offset: 0 } });
        if (api === '/runs' && u.searchParams.get('source') === 'scheduled_probe') {
          const batch = u.searchParams.get('scope_batch_id');
          const items = rows.filter(row => !batch || row.config.scope_batch_id === batch);
          return route.fulfill({ json: { items, total: items.length, offset: 0 } });
        }
        if (api === '/runs/detail') {
          const row = rows.find(row => row.id === u.searchParams.get('id'));
          if (row) {
            reads.push(row.id);
            return route.fulfill({ json: row.id === 'catalog' && pending ? { ...row, status: 'running', action_status: 'disable_pending' } : row });
          }
        }
        return route.continue();
      }
      if (api === '/rules/scope-preview' && req.method() === 'POST') return route.continue();
      assert.equal(req.method(), 'POST'); assert.equal(api, '/rules/run');
      writes.push(req.postDataJSON());
      if (writes.at(-1).id === 'single-runtime') return route.fulfill({ json: { run_id: 'read-failure', count: 1, skipped: 0 } });
      const id = writes.at(-1).id;
      if (id === catalog.auth_id) return route.fulfill({json:{count:0,skipped:0,reason:'对象已按错误规则暂停，退出运维检测。'}});
      if (id === 'manual-disabled' || id === 'retained') return route.fulfill({json:{count:0,skipped:1,skip_details:[{auth_id:id,
        code:id === 'retained' ? 'quality_paused' : 'manual_disabled',reason:id === 'retained' ? '历史质量暂停仍然生效。' : '指定对象已被人工停用。'}]}});
      assert.equal(writes.at(-1).id, 'rule');
      rule.last_scope_batch = 'mixed';
      rule.last_scope_dispatch = { at: stamp, matched: 1, skipped: 1, details: [protectedSkip] };
      return route.fulfill({ json: { count: 1, skipped: 1, scope_batch_id: 'mixed', skip_details: [protectedSkip] } });
    });
    const el = selector => page.locator(selector), content = el('#quality-detail-content');
    const shot = name => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const download = async () => {
      const wait = page.waitForEvent('download'); await page.click('#quality-detail-export');
      const file = await wait, doc = JSON.parse(await fs.readFile(await file.path(), 'utf8')); exported.push(doc); return doc;
    };
    await page.goto(url); await page.click('#connection-open'); await page.fill('#management-key', management_key);
    await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected'); await page.click('[data-page=operations]');
    await page.click('[data-rule-select=rule]');
    await el('#ops-rule-context [data-rule-handling] > summary').click();
    for (const [index, state] of ['disabled', 'deleted', 'attention'].entries()) {
      const record = el('#ops-rule-context .quality-handling-records > div').nth(index);
      assert.match(await record.innerText(), /CPA 注册目录缺少 missing-pro/); assert.doesNotMatch(await record.innerText(), /400\/404/);
      const action = record.locator('[data-handling-ack]');
      assert.equal(await action.isVisible(), state === 'attention');
      if (state !== 'attention') {
        assert.equal(await action.count(), 0, 'Confirmed handling reconciles from current CPA state without an acknowledgement step');
        assert.match(await record.innerText(), state === 'disabled'
          ? /已按错误规则停用，退出答题和恢复复检.*正常状态读取会自动结束旧关联/
          : /已移除凭证.*不会自动重建凭证/);
      }
    }
    await page.click('#ops-all-rules');
    await page.click('[data-rule-edit=rule]'); assert.match(await el('#rule-upstream-note').innerText(), /CPA 目录.*400\/404/);
    await page.click('#rule-form .editor-footer [data-close]');
    await page.click('[data-rule-run=rule]'); await page.click('#action-confirm'); await el('#action-dialog').waitFor({ state: 'hidden' });
    assert.equal(writes.length, 1); await el('#ops-batch-summary').filter({ hasText: '1–2 / 2' }).waitFor();
    assert.equal(await el('#operations-history [data-run]').count(), 2);
    assert.match(await el('#ops-dispatch-receipt').innerText(), /检测 1 个 · 未执行 1 个/);
    assert.equal(await el('#ops-dispatch-receipt [data-run=catalog]').count(), 0);
    await shot('mixed-batch');
    for (const [index, row] of rows.slice(0, 2).entries()) {
      const rendered = el('#operations-history tr').filter({ has: el('[data-run="' + row.id + '"]') });
      const text = await rendered.innerText();
      const detail = row.preflight_detail || row.skip_detail;
      assert(text.includes(row.config.auth_id) && text.includes(email));
      assert(text.includes(index ? detail.reason : '未请求上游'));
      assert(!text.includes('暂无判定') && !text.includes('400/404')); assert.equal(await rendered.locator('time').getAttribute('datetime'), stamp);
      const link = index ? el('#ops-dispatch-receipt [data-run="' + row.id + '"]') : rendered.locator('[data-run]');
      if (!index) assert.match(text, /已检测[\s\S]*CPA 目录未注册所选模型/);
      await link.click(); await content.getByText(detail.reason, { exact: true }).waitFor();
      assert.equal(await content.locator('.sample, [data-review-sample]').count(), 0);
      assert.match(await content.innerText(), index ? /未发起模型请求/ : /CPA 目录未注册所选模型，本次未请求上游/); assert.doesNotMatch(await content.innerText(), /样本缺失|上游响应|上游 HTTP/);
      const doc = await download(); assert.equal(doc.status, row.status); assert.deepEqual(doc.skip_detail, row.skip_detail); assert.deepEqual(doc.preflight_detail, row.preflight_detail); assert.deepEqual(doc.samples, []);
      assert(!doc.upstream_condition); assert.equal(doc.action_status, row.action_status);
      if (index === 0) for (const [width, theme] of [[1440,'light'],[390,'dark'],[320,'white']]) {
        await page.setViewportSize({ width, height: 840 }); await page.evaluate(theme => document.documentElement.dataset.theme = theme, theme);
        assert(await el('#quality-detail').evaluate(n => n.scrollWidth <= n.clientWidth && n.querySelector('.quality-detail-scroll').scrollWidth <= n.querySelector('.quality-detail-scroll').clientWidth));
        await shot('catalog-detail-' + width); captures.push({ width, theme });
      }
      await page.keyboard.press('Escape'); assert(await link.evaluate(n => n === document.activeElement));
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    pending = true; await page.click('#operations-history [data-run=catalog]');
    await content.getByText('正在暂停凭证').waitFor(); assert(await el('#quality-detail-cancel').isVisible());
    assert.equal(await content.locator('.sample').count(), 0); pending = false;
    await content.getByText('已暂停凭证', { exact: true }).waitFor(); await page.keyboard.press('Escape');
    await page.click('#ops-batch-clear'); await el('#operations-history [data-run=http]').click();
    await content.getByText('上游已确认指定模型不支持', { exact: true }).first().waitFor();
    assert.equal(await content.locator('.sample').count(), 1); const actual = await download(); assert.equal(actual.samples[0].upstream_condition, 'model_unsupported');
    await shot('upstream-probe'); await page.keyboard.press('Escape');
    await page.click('#operations-history [data-run=changed]');
    await content.getByText('目录或凭证资格未能再次确认，未执行处置').waitFor();
    await content.getByText('已检测', { exact: true }).waitFor();
    assert.equal((await download()).status, 'skipped');
    assert.doesNotMatch(await content.innerText(), /400\/404|已暂停凭证|处置结果待核对/); assert.equal(await content.locator('.sample').count(), 0);
    await shot('catalog-recheck'); await page.keyboard.press('Escape');
    for (const row of rows.slice(4, 6)) {
      await page.click('#operations-history [data-run="' + row.id + '"]');
      await content.locator('.quality-detail-facts').getByText('当前凭证状态检查', { exact: true }).waitFor();
      await content.locator('.quality-detail-byline').getByText('当前凭证状态检查', { exact: true }).waitFor();
      await content.getByText(row.preflight_detail.reason, { exact: true }).waitFor();
      assert.doesNotMatch(await content.innerText(), /模型不支持|400\/404|已暂停凭证|跳过原因/);
      assert.equal(await content.locator('.sample').count(), 0);
      const doc = await download(); assert.equal(doc.status, row.status); assert.deepEqual(doc.preflight_detail, row.preflight_detail);
      await page.keyboard.press('Escape');
    }
    for (const { row, state, explanation } of oldCases) {
      const rendered = el('#operations-history tr').filter({ has: el('[data-run="' + row.id + '"]') });
      const list = await rendered.innerText();
      await rendered.locator('[data-run]').click(); await content.locator('.quality-detail-facts').waitFor();
      if (row.id === 'old-queue-full') await shot('legacy-queue-full');
      const facts = await content.locator('.quality-detail-facts').innerText();
      for (const surface of [list, facts]) {
        assert(surface.includes(state), row.id + ' state: ' + surface);
        assert(surface.includes(surface === list && state === '已检测' ? '未请求上游' : explanation), row.id + ' reason: ' + surface);
        if (state !== '已检测') assert.doesNotMatch(surface, /已检测|CPA 目录未注册所选模型|已暂停凭证/);
      }
      assert.equal(await content.locator('.sample').count(), 0);
      assert.deepEqual(await download(), row); await page.keyboard.press('Escape');
    }
    for (const [id, message] of [['manual-disabled', '人工停用'], ['retained', '质量暂停'], [catalog.auth_id, '按错误规则暂停']]) {
      await page.click('[data-rule-run="' + id + '"]');
      await el('#ops-dispatch-receipt').filter({ hasText: message }).waitFor();
    }
    await page.click('[data-rule-run="single-runtime"]'); await content.locator('.quality-detail-facts').getByText('当前凭证状态检查', { exact: true }).waitFor();
    assert.deepEqual(writes.map(write => write.id), ['rule', 'manual-disabled', 'retained', catalog.auth_id, 'single-runtime']); await page.keyboard.press('Escape');
    assert.deepEqual(errors, []);
    if (out) await fs.writeFile(path.join(out, 'skipped-history-ui.json'), JSON.stringify({ writes, reads, exported, captures, modelCalls: 0, credentialWrites: 0 }, null, 2) + '\n');
    console.log('Verified current and legacy catalog checks, mixed-batch counts, protected skips, preflight failures, real HTTP evidence and original exports.');
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
