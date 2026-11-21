// Verify manual request controls with synthetic targets and receipts only.
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
    const info = JSON.parse(line), { origin, hostname, port } = new URL(info.url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' });
    page.setDefaultTimeout(6500);
    const out = process.env.UI_OUTPUT, report = [], posted = [], runs = [], errors = [];
    if (out) await fs.mkdir(out, { recursive: true });
    const capture = name => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const model = 'synthetic-model', prefix = '/v0/management/plugins/model-quality';
    const provider = id => ({ auth_id: id, auth_index: 'index-' + id, provider: 'openai-compatible-fixture', provider_name: '合成供应商',
      is_provider: true, type: 'config_api_key', models: [model], runtime_status: 'registered', effective_available: true });
    const definitions = [
      ['registered', { levels: ['low', 'medium', 'high'] }, ['', 'low', 'medium', 'high']],
      ['pascal', { Levels: ['minimal', 'high', 'max'], ZeroAllowed: true, DynamicAllowed: true }, ['', 'none', 'minimal', 'high', 'max', 'auto']],
      ['budget', { min: 128, max: 8192, zero_allowed: true, dynamic_allowed: true, levels: [] }, ['', 'none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max', 'auto']],
      ['empty-support', {}, ['', 'low', 'medium', 'high', 'xhigh', 'max']],
      ['no-thinking', null, ['']],
      ['future-level', { levels: ['future-effort'] }, ['']],
      ['future-level-with-budget', { levels: ['future-effort'], min: 128, max: 8192 }, ['']],
    ];
    let targets = [
      { auth_id: 'account', auth_index: 'account-index', provider: 'codex', email: 'fixture@example.test', models: [model], runtime_status: 'active', effective_available: true },
      provider('unknown'),
      ...definitions.map(([id, metadata]) => ({ ...provider(id), reasoning: { [model]: metadata } })),
    ];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      assert.equal(url.origin, origin);
      const api = url.pathname.slice(prefix.length);
      if (api === '/targets' && request.method() === 'GET') return route.fulfill({ json: { items: targets, total: targets.length, offset: 0 } });
      if (api === '/runs' && request.method() === 'POST') {
        const config = request.postDataJSON(), target = targets.find(target => target.auth_id === config.auth_id);
        assert(target); posted.push(config);
        const run = { id: 'manual-' + posted.length, source: 'manual_test', status: 'completed', created_at: '2026-11-21T08:00:00Z',
          config: { ...config, subject_auth_index: target.auth_index, ...(target.is_provider ? { provider_name: target.provider_name } : {}) }, samples: [] };
        runs.unshift(run); return route.fulfill({ status: 202, json: { run_id: run.id } });
      }
      assert.equal(request.method(), 'GET', 'No credential, rule or account mutation is authorized by this fixture');
      if (api === '/runs/detail') {
        const id = url.searchParams.get('id'), key = url.searchParams.get('idempotency_key');
        const run = runs.find(run => id ? run.id === id : run.config.idempotency_key === key);
        return run ? route.fulfill({ json: run }) : route.fulfill({ status: 404, json: { error: 'not_found' } });
      }
      if (api === '/runs' && url.searchParams.get('source') === 'manual_test') return route.fulfill({ json: { items: runs, total: runs.length, offset: 0 } });
      return route.continue();
    });
    await page.goto(info.url); await page.click('#connection-open'); await page.fill('#management-key', info.management_key);
    await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
    await page.click('[data-page=manual]'); await page.waitForSelector('#target:enabled');
    assert.equal(await page.locator('#manual-kind-credential,#manual-kind-provider,#manual-provider').count(), 0);
    assert(await page.locator('#reasoning').isDisabled());
    assert.deepEqual(await page.locator('#target optgroup').evaluateAll(nodes => nodes.map(node => node.label)), ['OAuth 账户', 'AI 提供商 · 单条路由']);
    assert.equal(await page.locator('#reasoning').evaluate(node => node.tagName), 'SELECT');
    const choices = () => page.locator('#reasoning option').evaluateAll(nodes => nodes.filter(node => !node.disabled).map(node => node.value));
    const effort = () => page.locator('#reasoning').inputValue();
    const select = id => page.selectOption('#target', id);
    const choose = async value => { if (await effort() !== value) await page.selectOption('#reasoning', value); };
    const ready = () => page.waitForSelector('#run-submit:enabled');
    const refresh = async () => { await page.click('#refresh'); await page.waitForSelector('#refresh:not([aria-busy=true])'); };
    const submit = async expected => {
      const before = posted.length;
      await page.click('#run-submit'); await page.waitForFunction(() => document.querySelector('#manual-result-summary').textContent && !document.querySelector('#run-submit').disabled);
      assert.equal(posted.length, before + 1);
      if (expected) assert.equal(posted.at(-1).parameters.reasoning_effort, expected);
      else assert(!Object.hasOwn(posted.at(-1).parameters, 'reasoning_effort'));
      for (const key of ['target_group', 'authorized_groups', 'provider_name', 'subject_auth_index', 'managed_probe']) assert(!Object.hasOwn(posted.at(-1), key));
      return posted.at(-1);
    };
    await select('unknown');
    assert.equal(await effort(), '');
    assert.deepEqual(await choices(), ['', 'low', 'medium', 'high', 'xhigh', 'max']);
    assert.match(await page.locator('#reasoning-note').innerText(), /实际支持取决于 CPA 转换和上游/);
    await ready(); await submit('');
    await choose('high'); await submit('high');
    await capture('unknown-provider-request');
    report.push({ kind: 'unknown-provider', requestLevels: await choices(), defaultUnspecified: true, explicitHighSubmitted: true });
    for (const [id, metadata, expected] of definitions) {
      await choose(''); await select(id);
      assert.deepEqual(await choices(), expected, id);
      assert.equal(await effort(), '');
      assert.equal(await page.locator('#reasoning').isDisabled(), expected.length === 1);
      if (id === 'budget') {
        assert.match(await page.locator('#reasoning-note').innerText(), /预算范围调整/);
        await choose('max'); await submit('max'); await capture('budget-request-levels');
      }
      if (id === 'empty-support') assert.match(await page.locator('#reasoning-note').innerText(), /未声明思考档位或预算限制/);
      if (id === 'no-thinking') assert.match(await page.locator('#reasoning-note').innerText(), /未提供思考参数/);
      report.push({ kind: id, requestLevels: expected });
    }
    await select('account'); assert.deepEqual(await choices(), ['']);
    assert.match(await page.locator('#reasoning-note').innerText(), /思考能力未知/);
    for (const previous of ['none', 'minimal', 'auto']) {
      await select('pascal'); await choose(previous); await select('unknown');
      assert.equal(await effort(), previous);
      assert(await page.locator('#reasoning option[value="' + previous + '"]').isEnabled());
      await submit(previous);
    }
    await choose(''); await select('registered'); await choose('low');
    await page.locator('#reasoning').focus(); await choose('medium');
    assert.equal(await effort(), 'medium', 'The native reasoning selection must update the current request');
    await choose('high');
    targets = targets.map(target => target.auth_id === 'registered' ? { ...target, reasoning: { [model]: { levels: ['low'] } } } : target);
    await refresh();
    assert.deepEqual(await page.locator('#reasoning').evaluate(node => ({ value: node.value, disabled: node.selectedOptions[0]?.disabled, unavailable: node.dataset.unavailable })),
      { value: 'high', disabled: true, unavailable: 'true' });
    const beforeBlocked = posted.length;
    await page.locator('#run-form').evaluate(form => form.requestSubmit());
    assert.equal(await page.evaluate(() => document.activeElement.id), 'reasoning');
    assert.match(await page.locator('#reasoning-error').innerText(), /重新选择/);
    assert.equal(posted.length, beforeBlocked);
    await choose('low'); await ready();
    assert.equal(await page.locator('#reasoning-error').count(), 0);
    assert.equal(await page.locator('#reasoning').getAttribute('aria-invalid'), null);
    await select('unknown'); await choose('high');
    targets = targets.map(target => target.auth_id === 'unknown' ? { ...target, auth_index: 'replacement-index' } : target);
    const refreshed = page.waitForResponse(response => new URL(response.url()).pathname === prefix + '/targets');
    await page.click('#run-submit'); await (await refreshed).finished(); await page.waitForTimeout(100);
    assert.equal(posted.length, beforeBlocked, 'The preflight must not silently redirect a selected provider to a replacement index');
    assert.match(await page.locator('#target option:checked').innerText(), /原绑定待核实/);
    assert(await page.locator('#run-submit').isDisabled());
    await select('unknown'); await ready(); await submit('high');
    assert.equal(runs[0].config.subject_auth_index, 'replacement-index');
    report.push({ kind: 'refresh-and-identity', staleEffortPreserved: true, errorFocus: 'reasoning', staleIndexPosts: 0, explicitReselectionSubmitted: true });
    assert.deepEqual(errors, []);
    if (out) await fs.writeFile(path.join(out, 'manual-controls.json'), JSON.stringify({ synthetic: true, report, posted: posted.map(({ auth_id, parameters }) => ({ auth_id, parameters })), errors }, null, 2) + '\n');
    console.log(JSON.stringify({ scenarios: report.length, posts: posted.length, errors }));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
