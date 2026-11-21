// Only rule configuration writes are fulfilled; no model or CPA credential receives a request.
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
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
    page.setDefaultTimeout(6500);
    const out = process.env.UI_OUTPUT;
    if (out) await fs.mkdir(out, { recursive: true });
    const shots = async name => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const errors = [], writes = [], acknowledgements = [], layouts = [], receipts = [];
    page.on('pageerror', error => errors.push(error.message));
    const policies = {
      unauthorized: 'disable_credential', payment_required: 'delete_credential', quota_exhausted: 'disable_credential', model_unsupported: 'delete_credential',
    }, codes = Object.keys(policies), neutral = Object.fromEntries(codes.map(code => [code, 'observe_only']));
    const defaultActions = Object.fromEntries(codes.map(code => [code, 'disable_credential']));
    const labels = ['上游 HTTP 401 认证失败', '上游 HTTP 402 付费要求', '上游 HTTP 429 配额耗尽', '上游 HTTP 404 指定模型不支持', '上游 HTTP 429（额度原因未确认）'];
    const old = { id: 'old', revision: 1, name: '旧观察规则', target_scope: 'all_enabled', enabled: true, timer_active: false, action: 'observe_only', models: ['fixture-reasoner'], samples: 1, question_id: 'logic-candy', judge: { mode: 'deterministic' }, cron: '*/30 * * * *' };
    const partial = { ...old, id: 'partial', name: '部分已配置', upstream_error_actions: { payment_required: 'delete_credential', quota_exhausted: 'observe_only' } };
    const rules = [old, partial], handling = [
      { action_id: 'known', auth_id: 'a', state: 'disabled', upstream_condition: 'unauthorized' },
      { action_id: 'removed', auth_id: 'b', state: 'deleted', upstream_condition: 'payment_required' },
      { action_id: 'attention', auth_id: 'c', state: 'attention', upstream_condition: 'quota_exhausted' },
      ...['route-one','route-two'].map(auth_id => ({ action_id:'group-action', auth_id, state:'attention', upstream_condition:'unauthorized' })),
    ];
    const runs = [...codes, 'rate_limit', 'management_401'].map((code, index) => {
      const condition = codes.includes(code) ? { upstream_condition: code } : {};
      const status = policies[code] === 'disable_credential' ? 'credential_suspended' : policies[code] === 'delete_credential' ? 'credential_removed' : 'observe_only';
      return { id: code, ...(index === 1 ? {} : condition), source: 'scheduled_probe', status: 'failed', action_status: index === 1 ? 'observe_only' : status, created_at: '2026-11-20T03:00:00Z',
        credential_email: 'synthetic@example.test', config: { rule_id: 'new', auth_id: 'fixture-quality-primary', samples: 1, models: ['fixture-reasoner'], question_id: 'logic-candy', judge: { mode: 'deterministic' } },
        samples: [{ model: 'fixture-reasoner', index: 0, execution_status: 'failed', quality_verdict: 'unknown', ...condition,
          error: code === 'management_401' ? 'management_http_401: CPA management request failed' : 'upstream HTTP ' + [401,402,429,404,429][index] }] };
    });
    const errorPauses = [...codes, 'catalog'].map((code, index) => ({ auth_id: 'paused-' + code, action_id: 'pause-' + index, state: 'disabled', ...(code === 'catalog' ? { catalog_missing_model: 'missing-pro' } : { upstream_condition: code }) }));
    const recovery = { ...old, id: 'recovery', name: '答错和错误策略并用', target_scope: 'all_credentials', action: 'disable_credential', auto_restore: true,
      upstream_error_actions: defaultActions,
      handling: [...errorPauses, { auth_id: 'quality', action_id: 'quality-action', state: 'disabled' }] };
    rules.push(recovery, { ...old, id: 'foreign', handling: [{ auth_id: 'foreign-disabled', action_id: 'foreign-action', state: 'disabled', upstream_condition: 'unauthorized' }] });
    const disabled = [...errorPauses.map(record => record.auth_id), 'quality', 'operator', 'foreign-disabled'].map(auth_id => ({ auth_id, auth_index: auth_id, email: auth_id + '@example.test', provider: 'codex', runtime_status: 'disabled', manual_disabled: true, reasons: ['operator_disabled'], models: [] }));
    const causes = ['上游返回 HTTP 401 未授权', '上游返回 HTTP 402 需要付款', '上游返回 HTTP 429，确认账户额度耗尽',
      '上游明确不支持所选模型（HTTP 400 或 404）', 'CPA 注册目录缺少所选模型'];
    const skipped = errorPauses.map((record, i) => ({ id: 'skip-' + i, created_at: runs[0].created_at,
      source: 'scheduled_probe', status: 'skipped', action_status: 'observe_only', samples: [],
      config: { ...runs[0].config, rule_id: recovery.id, auth_id: record.auth_id },
      skip_detail: { auth_id: record.auth_id, email: record.auth_id + '@example.test', code: 'error_policy_disabled',
        ...(record.catalog_missing_model ? { model: record.catalog_missing_model } : {}),
        reason: causes[i] + '，凭证已按错误策略停用。停用期间跳过检测，不会自动恢复。' } }));
    const providerRule = { ...recovery, id: 'provider-rule', name: '供应商答错暂停与恢复', revision: 7, object_types: ['provider'] };
    rules.push(providerRule);
    const providerRun = { id: 'provider-correct', created_at: runs[0].created_at, source: 'scheduled_probe', status: 'completed', action_status: 'observe_only',
      config: { ...runs[0].config, rule_id: providerRule.id, rule_revision: 7, auth_id: 'synthetic-provider', provider: 'codex',
        provider_name: 'Synthetic provider', subject_auth_index: 'synthetic-route', provider_observation: true,
        managed_probe: { provider_mechanism: 'excluded_models' } },
      samples: [{ model: 'fixture-reasoner', index: 0, execution_status: 'completed', quality_verdict: 'correct', output: '21', evidence: { path: 'provider_config_probe' } }] };
    const providerCases = [
      [providerRun, '本轮未处置'],
      [{ ...providerRun, id: 'provider-skipped', status: 'skipped', samples: [], skip_detail: { code: 'provider_unavailable', reason: 'Synthetic route unavailable' } }, '本轮未执行，未处置'],
      [{ ...providerRun, id: 'provider-legacy-skipped', status: 'skipped', action_status: undefined, samples: [], skip_detail: { code: 'error_policy_disabled', reason: 'Synthetic previous upstream error' } }, '保持暂停，本轮未改写'],
      [{ ...providerRun, id: 'provider-suspended', action_status: 'credential_suspended', credential_action: 'disable_credential' }, '已暂停'],
      [{ ...providerRun, id: 'provider-restored', action_status: 'credential_restored', credential_action: 'restore_credential' }, '整轮通过，已恢复'],
      [{ ...providerRun, id: 'provider-uncertain', action_status: 'credential_action_uncertain', credential_action: 'restore_credential' }, '恢复结果待核对'],
    ];
    const records = [...runs, ...skipped, ...providerCases.map(([run]) => run)];
    let settingsGate, capability = 'supported', emptyRules = true;
    await page.route('**/*', async route => {
      const req = route.request(), u = new URL(req.url()); assert.equal(u.origin, origin);
      const api = u.pathname.slice('/v0/management/plugins/model-quality'.length);
      if (req.method() === 'GET') {
        if (api === '/settings') {
          if (settingsGate) await settingsGate;
          if (capability === 'failed') return route.fulfill({ status:503, json:{error:{message:'synthetic settings failure'}} });
          if (capability === 'unsupported') return route.fulfill({ json:{capabilities:{mode:'official_observation',credential_management:false}} });
        }
        if (api === '/targets') {
          const current = { auth_id: 'enabled', auth_index: 'enabled', object_type: 'oauth', provider: 'codex', runtime_status: 'active', effective_available: true, models: ['fixture-reasoner'] };
          const unavailable = { ...current, auth_id: 'failing', auth_index: 'failing', runtime_status: 'error', effective_available: false, models: [] };
          const missing = { ...current, auth_id: 'free', auth_index: 'free', models: ['other-model'] };
          const items = [current, unavailable, missing, ...disabled];
          return route.fulfill({ json: { items, total: items.length, offset: 0 } });
        }
        if (api === '/rules') {
          const items = emptyRules ? [] : rules;
          return route.fulfill({ json: { items, total: items.length, offset: 0 } });
        }
        if (api === '/runs' && u.searchParams.get('source') === 'scheduled_probe') return route.fulfill({ json: { items: records, total: records.length, offset: 0 } });
        if (api === '/runs/detail' && records.some(r => r.id === u.searchParams.get('id'))) return route.fulfill({ json: records.find(r => r.id === u.searchParams.get('id')) });
        return route.continue();
      }
      if(api==='/rules/scope-preview' && req.method()==='POST'){
        const draft=req.postDataJSON(), all=draft.target_scope==='all_credentials';
        const recovery=draft.id==='recovery' && draft.auto_restore ? 1 : 0, candidates=3+recovery+(all?1:0);
        return route.fulfill({json:{verified:true,candidates:{oauth:candidates,provider:0},count:candidates,skipped:0,recovery_count:all?0:recovery,skip_details:[]}});
      }
      if (api === '/rules/handling/acknowledge' && req.method() === 'POST') {
        const data = req.postDataJSON();
        assert.equal(data.rule_id, 'new');
        assert.equal(data.action_id, 'group-action');
        assert.equal(data.auth_id, 'route-two', 'A shared group action must retain the clicked member identity');
        const rule = rules.find(rule => rule.id === data.rule_id);
        rule.handling = rule.handling.filter(record => record.action_id !== data.action_id);
        acknowledgements.push(data);
        return route.fulfill({ json:{ok:true} });
      }
      assert.equal(api, '/rules'); assert(['POST', 'PUT'].includes(req.method()));
      const data = req.postDataJSON(); writes.push(data);
      const current = rules.find(rule => rule.id === data.id);
      const saved = { ...data, id: data.id || (capability === 'supported' ? 'new' : capability), revision: (current?.revision || 0) + 1, enabled: true, timer_active: false, handling };
      if (current) rules[rules.indexOf(current)] = saved; else rules.push(saved);
      return route.fulfill({ json: saved });
    });
    const el = selector => page.locator(selector);
    const picker = code => el('#rule-upstream-' + code);
    const wrong = value => el('[name="rule-action"][value="' + value + '"]');
    const save = async () => { await page.click('#rule-form button[type=submit]'); await el('#rule-dialog').waitFor({ state: 'hidden' }); };
    const refresh = async () => { await page.click('#refresh'); await el('#refresh:not([aria-busy])').waitFor(); };
    await page.goto(url); await page.click('#connection-open'); await page.fill('#management-key', management_key);
    await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
    let releaseSettings;
    settingsGate = new Promise(resolve => { releaseSettings = resolve; });
    await page.click('[data-page=operations]');
    const firstRule = el('#rules-list [data-rule-new]');
    await firstRule.waitFor();
    assert(await firstRule.isDisabled()); assert(await el('#rule-new').isDisabled());
    releaseSettings(); settingsGate = null;
    await firstRule.click();
    for (const code of codes) assert.equal(await picker(code).inputValue(), 'disable_credential');
    await page.click('#rule-form .editor-footer [data-close]');
    emptyRules = false; await refresh(); await page.click('[data-rule-edit=old]');
    for (const code of codes) assert.equal(await picker(code).inputValue(), 'observe_only');
    await page.click('#rule-form .editor-footer [data-close]'); assert.equal(writes.length, 0);
    await page.click('#rule-new'); await page.fill('#rule-name', '上游错误验证');
    for (const [i, code] of codes.entries()) {
      assert.equal(await picker(code).inputValue(), 'disable_credential');
      assert.equal(await picker(code).locator('option').allTextContents().then(names => names.join('/')), '不处理/暂停凭证/移除凭证');
      assert.equal(await el('#rule-upstream-options label').nth(i).locator('select').getAttribute('id'), 'rule-upstream-' + code);
    }
    assert(await wrong('observe_only').isChecked()); assert(await el('#rule-auto-restore').isDisabled());
    await wrong('disable_credential').check(); await el('#rule-auto-restore').check();
    await page.selectOption('#rule-judge-mode', 'manual'); await page.click('#rule-form button[type=submit]');
    await el('#rule-judge-mode-error').waitFor(); assert.equal(writes.length, 0);
    await wrong('observe_only').check(); assert(await el('#rule-restore-option').isHidden());
    assert.match(await el('#rule-upstream-note').innerText(), /上游错误独立于答错处理/);
    for (const [width, height, theme] of [[1440, 900, 'light'], [1280, 620, 'white'], [390, 844, 'dark'], [320, 710, 'light']]) {
      await page.setViewportSize({ width, height });
      await page.evaluate(theme => { document.documentElement.dataset.theme = theme; }, theme);
      await el('.rule-upstream').evaluate(node => node.scrollIntoView({ block: 'center' }));
      const layout = await el('#rule-dialog').evaluate(dialog => {
        const box = dialog.getBoundingClientRect(), footer = dialog.querySelector('.editor-footer').getBoundingClientRect(), body = dialog.querySelector('.rule-form-scroll');
        return { overflow: body.scrollWidth > body.clientWidth + 1 || dialog.scrollWidth > dialog.clientWidth + 1, footerVisible: footer.top >= box.top && footer.bottom <= box.bottom + 1 };
      });
      assert(!layout.overflow && layout.footerVisible); layouts.push({ width, height, theme, ...layout }); await shots('upstream-editor-' + width);
    }
    await save();
    assert.deepEqual(writes[0].upstream_error_actions, defaultActions); assert.equal(writes[0].action, 'observe_only'); assert.equal(writes[0].auto_restore, false);
    assert.equal(writes[0].judge.mode, 'manual');
    assert.equal(writes[0].activation, 'keep'); assert.equal(rules.find(rule => rule.id === 'new').timer_active, false);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.click('[data-rule-edit=new]');
    for (const code of codes) {
      assert.equal(await picker(code).inputValue(), 'disable_credential');
      await picker(code).selectOption(policies[code]);
    }
    await save(); assert.deepEqual(writes[1].upstream_error_actions, policies);
    await page.click('[data-rule-run=new]');
    await el('#action-dialog').waitFor(); const confirmation = await el('#action-description').innerText();
    assert(confirmation.includes('401 认证失败时暂停凭证') && confirmation.includes('指定模型不支持时移除凭证'));
    await page.click('#action-cancel'); assert.equal(writes.length, 2);
    await page.click('[data-rule-edit=new]');
    for (const code of codes) assert.equal(await picker(code).inputValue(), policies[code]);
    for (const code of codes) await picker(code).selectOption('observe_only');
    await save(); assert.deepEqual(writes[2].upstream_error_actions, neutral);
    await page.click('[data-rule-edit=new]'); for (const code of codes) assert.equal(await picker(code).inputValue(), 'observe_only');
    await page.click('#rule-form .editor-footer [data-close]');
    assert.equal(await el('#rules-list [data-handling-ack],#rules-list .quality-handling-records').count(), 0);
    await el('[data-rule-select=new]').click();
    const handlingDetails = el('#ops-rule-context details[data-rule-handling]');
    await handlingDetails.locator('summary').click();
    const resolved = handlingDetails.locator('.quality-handling-records > div').first();
    assert.match(await resolved.innerText(), /401 认证失败.*已按错误规则停用，退出答题和恢复复检/);
    assert.match(await resolved.innerText(), /正常状态读取会自动结束旧关联/);
    assert(await handlingDetails.locator('[data-handling-ack=known]').isHidden()); assert(await handlingDetails.locator('[data-handling-ack=removed]').isHidden());
    assert(await handlingDetails.locator('[data-handling-ack=attention]').isVisible());
    const member = handlingDetails.locator('.quality-handling-records > div').filter({ hasText:'route-two' });
    await member.locator('[data-handling-ack]').click(); await el('#action-cancel').click();
    assert.deepEqual(acknowledgements, []);
    await member.locator('[data-handling-ack]').click(); await el('#action-confirm').click();
    await el('#action-dialog').waitFor({state:'hidden'});
    assert.equal(acknowledgements.length, 1);
    assert.equal(rules.find(rule => rule.id === 'new').enabled, true);
    await member.waitFor({state:'detached'});
    assert.equal(await handlingDetails.locator('[data-handling-ack="group-action"]').count(), 0);
    await el('#ops-all-rules').click(); assert(await handlingDetails.isHidden());
    for (const [i, run] of runs.entries()) {
      const result = el('[data-key="run:' + run.id + '"] .quality-result-cell');
      if (labels[i]) assert((await result.innerText()).includes(labels[i]));
      else assert.doesNotMatch(await result.innerText(), /上游 HTTP|配额耗尽/);
      await page.click('#operations-history [data-run="' + run.id + '"]');
      await el('#quality-detail-export:visible').waitFor();
      const facts = el('#quality-detail-content .quality-detail-facts'), reason = el('#quality-detail-content .sample-reason').first();
      assert.doesNotMatch(await facts.innerText(), /错误 1/);
      if (labels[i]) { assert((await facts.innerText()).includes(labels[i])); assert((await reason.innerText()).includes(labels[i])); }
      else { assert.doesNotMatch(await facts.innerText(), /上游响应|已移除|已暂停/); assert.match(await reason.innerText(), /management_http_401/); }
      const downloading = page.waitForEvent('download'); await page.click('#quality-detail-export');
      const file = await downloading, exported = JSON.parse(await fs.readFile(await file.path(), 'utf8'));
      assert.equal(exported.upstream_condition, run.upstream_condition); assert.equal(exported.samples[0].upstream_condition, run.samples[0].upstream_condition);
      receipts.push({ id: run.id, action: exported.action_status, condition: exported.upstream_condition, verdict: exported.samples[0].quality_verdict });
      if (i === 0) await shots('upstream-detail');
      await page.keyboard.press('Escape');
    }
    const providerHandling = [];
    assert.equal(await el('#operations-history').getByRole('columnheader', { name: '对象与规则', exact: true }).count(), 1);
    assert.equal(await el('#operations-history').getByRole('columnheader', { name: '本轮处置', exact: true }).count(), 1);
    for (const [run, label] of providerCases) {
      const row = el('[data-key="run:' + run.id + '"]');
      assert.equal(await row.locator('.quality-handling-cell').getAttribute('data-label'), '本轮处置');
      assert.equal(await row.locator('[data-handling]').innerText(), label);
      assert.doesNotMatch(await row.innerText(), /供应商仅观察/);
      await row.locator('[data-run]').click();
      const facts = el('#quality-detail-content .quality-detail-facts');
      await facts.locator('dt').getByText('本轮处置', { exact: true }).waitFor();
      await facts.getByText(label, { exact: true }).waitFor();
      assert.doesNotMatch(await facts.innerText(), /供应商仅观察/);
      if (run.id === 'provider-correct') {
        assert.match(await facts.innerText(), /正确/);
        await shots('provider-correct-no-action');
      }
      providerHandling.push({ id: run.id, action_status: run.action_status, label });
      await page.keyboard.press('Escape');
    }
    await page.click('[data-rule-run=recovery]');
    await el('#action-dialog').waitFor(); const allPreview = await el('#action-description').innerText();
    assert.match(allPreview, /OAuth 账户 5 个/); assert.match(allPreview, /恢复复检 0 个/); await page.click('#action-cancel');
    await page.click('[data-rule-edit=recovery]');
    assert.match(await el('#rule-restore-detail').innerText(), /人工停用/);
    assert.match(await el('#rule-upstream-note').innerText(), /退出后续运维检测/);
    await el('#rule-scope-note').filter({hasText:'OAuth 账户 ' + (3 + disabled.length) + ' 个'}).waitFor();
    await el('#rule-scope-all').check(); await el('#rule-scope-note').filter({hasText:'OAuth 账户 3 个'}).waitFor(); const enabledPreview = await el('#rule-scope-note').innerText();
    assert.doesNotMatch(enabledPreview, /恢复复检/);
    await el('#rule-scope-directory').check(); await el('#rule-restore-detail').scrollIntoViewIfNeeded();
    await shots('error-pause-recovery'); await page.click('#rule-form .editor-footer [data-close]');
    const skipEvidence = [];
    for (const run of skipped) {
      await page.click('#operations-history [data-run="' + run.id + '"]');
      const facts = el('#quality-detail-content .quality-detail-facts');
      await facts.getByText('已确认的错误策略暂停', { exact: true }).waitFor(); assert.match(await facts.innerText(), /本轮未重复答题/);
      await facts.getByText('保持暂停，本轮未改写', { exact: true }).waitFor();
      assert((await facts.innerText()).includes(run.skip_detail.reason)); assert.equal(await el('#quality-detail-content .sample').count(), 0);
      if (run.skip_detail.model) await facts.getByText(run.skip_detail.model, { exact: true }).waitFor();
      const downloading = page.waitForEvent('download'); await page.click('#quality-detail-export');
      const file = await downloading, exported = JSON.parse(await fs.readFile(await file.path(), 'utf8'));
      assert.equal(exported.status, 'skipped'); assert.deepEqual(exported.skip_detail, run.skip_detail);
      assert.deepEqual(exported.samples, []); skipEvidence.push(exported.skip_detail);
      if (run === skipped.at(-1)) await shots('error-pause-skip');
      await page.keyboard.press('Escape');
    }
    assert.equal(writes.length, 3);
    for (const legacy of [old, partial]) {
      const expected = { ...neutral, ...legacy.upstream_error_actions };
      await page.click('[data-rule-edit="' + legacy.id + '"]');
      for (const code of codes) assert.equal(await picker(code).inputValue(), expected[code]);
      await page.fill('#rule-name', legacy.name + '改名'); await save();
      assert.deepEqual(writes.at(-1).upstream_error_actions, expected);
    }
    for (const mode of ['unsupported', 'failed']) {
      capability = mode;
      await refresh(); await page.click('#rule-new'); await page.fill('#rule-name', '仅观察 ' + mode);
      for (const code of codes) {
        assert.equal(await picker(code).inputValue(), 'observe_only');
        const option = picker(code).locator('option[value=disable_credential]');
        assert(await option.evaluate(node => node.disabled), mode + ' / ' + code + ': ' + await el('#rule-upstream-note').textContent());
      }
      await save(); assert.deepEqual(writes.at(-1).upstream_error_actions, neutral);
    }
    assert.equal(writes.length, 7);
    capability = 'supported'; await refresh();
    for (const question of ['custom', 'visual-pelican-html']) {
      await page.click('[data-rule-edit=new]'); await el('#rule-question').selectOption(question);
      assert.equal(await el('#rule-judge-mode').inputValue(), 'manual');
      assert(await el('#rule-auto-restore').isDisabled());
      if (question === 'custom') await el('#rule-prompt').fill('Explain the synthetic fixture response.');
      for (const code of codes) await picker(code).selectOption('disable_credential');
      await save();
      assert.equal(writes.at(-1).question_id, question);
      assert.equal(writes.at(-1).judge.mode, 'manual');
      assert.equal(writes.at(-1).action, 'observe_only');
      assert.deepEqual(writes.at(-1).upstream_error_actions, defaultActions);
      assert.equal(writes.at(-1).auto_restore, false);
    }
    assert.equal(writes.length, 9); assert.deepEqual(errors, []);
    if (out) await fs.writeFile(path.join(out, 'upstream-ui.json'), JSON.stringify({ layouts, saved: writes, acknowledgements, results: receipts, providerHandling, allPreview, enabledPreview, skipEvidence, real_writes: 0 }, null, 2) + '\n');
    console.log('Upstream condition selects, round trips, scoped recovery, evidence, acknowledgement visibility and exports passed.');
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
