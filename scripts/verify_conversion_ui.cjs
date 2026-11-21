// Serve the real plugin UI inside a synthetic CPA iframe. Only the private fixture receives reads.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

function syntheticAccount(id, plan) {
  const accountID = `fixture-account-${id}`, userID = `fixture-user-${id}`, email = `${id}@example.test`;
  const claims = { exp: 2208988800, email, 'https://api.openai.com/auth': { chatgpt_account_id: accountID, chatgpt_user_id: userID, chatgpt_plan_type: plan } };
  const header = Buffer.from(JSON.stringify({ typ: 'JWT', alg: 'RS256' })).toString('base64url');
  const token = `${header}.${Buffer.from(JSON.stringify(claims)).toString('base64url')}.SYNTHETIC_ACCESS_CANARY`;
  return {
    name: `合成凭证 ${id}`, platform: 'openai', type: 'oauth', concurrency: 1, priority: 1, rate_multiplier: 1, extra: {},
    credentials: {
      type: 'codex', account_id: accountID, chatgpt_account_id: accountID, chatgpt_user_id: userID, email, plan_type: plan,
      access_token: token, id_token: token, refresh_token: `SYNTHETIC_REFRESH_CANARY_${id}`, last_refresh: '2026-01-01T00:00:00Z',
    },
  };
}
const accounts = [syntheticAccount('first', 'plus'), syntheticAccount('second', 'pro')];
accounts[1].status = 'inactive';
const exportJSON = entries => JSON.stringify({ type: 'sub2api-data', version: 1, exported_at: '2026-11-20T03:45:00Z', proxies: [], accounts: entries }, null, 2);
const source = exportJSON(accounts);
const mixedSource = exportJSON([accounts[0], { ...accounts[1], credentials: {} }]);
const duplicateSource = exportJSON([accounts[0], accounts[0], accounts[1]]);
const ready = accounts.map(({ name, credentials: c }, index) => ({
  index, name, email: c.email, account_id: c.account_id, plan_type: c.plan_type, filename: `codex-converted-fixture-${index}.json`,
  status: 'ready', warnings: index === 1 ? ['kept_disabled'] : [],
  credential: { type: 'codex', access_token: c.access_token, refresh_token: c.refresh_token, account_id: c.account_id, disabled: index === 1 },
}));
const invalid = { index: 1, name: accounts[1].name, status: 'invalid', error: 'conversion_missing_access_token', warnings: [] };

async function dropFiles(zone, files) {
  const consumed = await zone.evaluate((node, files) => {
    const dataTransfer = new DataTransfer();
    for (const { name, content, size } of files) {
      const bytes = size === undefined ? content : new Uint8Array(size);
      dataTransfer.items.add(new File([bytes], name, { type: 'application/json' }));
    }
    const drag = type => node.dispatchEvent(new DragEvent(type, { dataTransfer, bubbles: true, cancelable: true }));
    drag('dragenter');
    return [drag('dragover'), drag('drop')];
  }, files);
  assert.deepEqual(consumed, [false, false], 'The browser must allow dropping and prevent navigation to the file');
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await Promise.race([once(createInterface({ input: fixture.stdout }), 'line'), exit.then(() => { throw Error('Fixture unavailable'); })]);
    const info = JSON.parse(line), fixtureURL = new URL(info.embedded_url);
    assert.equal(info.fixture, 'synthetic-plugin-ui'); assert.equal(info.pid, fixture.pid);
    assert.equal(fixtureURL.hostname, '127.0.0.1'); assert.notEqual(fixtureURL.port, '8317');
    // Unlike localhost, an HTTP .test origin is not a secure context and exposes no crypto.randomUUID.
    const url = new URL(fixtureURL); url.hostname = 'conversion-fixture.test';
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, acceptDownloads: true, reducedMotion: 'reduce', serviceWorkers: 'block' });
    const errors = [], unexpected = [], previews = [], uploads = [], keys = [];
    let holdNextPreview, replyToUpload, failRefresh = false, expireOnRead = false;
    await context.route('**/*', async route => {
      const req = route.request(), requestURL = new URL(req.url());
      if (requestURL.origin !== url.origin) { unexpected.push('external'); return route.abort(); }
      const pathname = requestURL.pathname;
      if (req.method() === 'GET') {
        if (expireOnRead && pathname === '/v0/management/plugins/model-quality/login/accounts') {
          expireOnRead = false;
          return route.fulfill({ status: 401, json: { code: 'unauthorized' } });
        }
        if (failRefresh && pathname === '/v0/management/plugins/model-quality/login/accounts') { failRefresh = false; return route.abort('connectionfailed'); }
        const response = await route.fetch({ url: new URL(pathname + requestURL.search, fixtureURL).href, maxRedirects: 0 });
        return route.fulfill({ response });
      }
      const prefix = '/v0/management/plugins/model-quality/login/conversion/';
      if (req.method() !== 'POST' || !pathname.startsWith(prefix)) { unexpected.push(pathname); return route.abort(); }
      keys.push(req.headers()['idempotency-key']);
      const body = req.postDataJSON();
      if (pathname === prefix + 'preview') {
        previews.push(body.source);
        if (holdNextPreview) {
          const hold = holdNextPreview; holdNextPreview = null; return hold(route);
        }
        if (body.source === source) return route.fulfill({ json: { items: ready, ready: 2 } });
        if (body.source === mixedSource) return route.fulfill({ json: { items: [ready[0], invalid], ready: 1 } });
        if (body.source === duplicateSource) return route.fulfill({ json: { items: [
          ready[0], { ...invalid, error: 'conversion_duplicate_source', duplicate_of: 0 }, { ...ready[1], index: 2 },
        ], ready: 2 } });
        if (body.source === '{') return route.fulfill({ status: 400, json: { code: 'conversion_invalid_json' } });
      }
      if (pathname === prefix + 'upload' && replyToUpload) {
        const reply = replyToUpload; replyToUpload = null; uploads.push(body);
        return reply(route);
      }
      unexpected.push(pathname); return route.abort();
    });
    const page = await context.newPage(); page.setDefaultTimeout(8000);
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(url.href);
    const frame = page.frameLocator('#fixture-plugin'), dialog = frame.locator('#account-convert-dialog');
    const get = name => frame.locator('#account-convert-' + name);
    const select = index => dialog.locator(`[data-convert-select="${index}"]`);
    const row = index => dialog.locator(`.conversion-item:has([data-convert-select="${index}"])`);
    const idle = () => frame.locator('#account-convert-dialog[open][aria-busy="false"]').waitFor();
    const open = async () => { await get('open').click(); await dialog.waitFor(); };
    const close = async () => { await get('cancel').click(); await dialog.waitFor({ state: 'hidden' }); };
    const connect = async () => {
      await frame.locator('#login-connect').click(); await frame.locator('#management-key').fill(info.management_key);
      await frame.locator('#connection-form button[type=submit]').click(); await frame.locator('body.connected').waitFor();
    };
    const paste = async text => {
      const before = previews.length;
      await get('paste').locator('summary').click(); await get('source').fill(text);
      assert.equal(previews.length, before);
      await get('preview').click(); await get('results').waitFor(); await idle();
      assert.deepEqual(previews.slice(before), [text]);
    };
    const submit = async (body, reply) => {
      const before = uploads.length; replyToUpload = reply;
      await get('upload').click(); await idle();
      assert.deepEqual(uploads.slice(before), [body]);
    };
    const receipts = items => route => route.fulfill({ json: { items } });
    const assertLocked = async index => { assert(await select(index).isDisabled()); assert(!await select(index).isChecked()); };
    const holdFilePreview = async file => {
      const heldRoute = new Promise(resolve => { holdNextPreview = resolve; });
      const sent = page.waitForRequest(req => req.url().endsWith('/conversion/preview') && req.postDataJSON().source === file.content);
      await dropFiles(get('dropzone'), [file]);
      return { request: await sent, route: await heldRoute };
    };
    const finishOldPreview = async held => {
      const finished = page.waitForEvent('requestfinished', req => req === held.request);
      await held.route.fulfill({ json: { items: ready, ready: 2 } }); await finished;
      await dialog.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    };
    const assertEmpty = async () => {
      assert.equal(await get('file-name').textContent(), ''); assert.equal(await get('file').inputValue(), '');
      assert.equal(await get('source').inputValue(), ''); assert.equal(await get('list').textContent(), '');
      assert(await get('results').isHidden()); assert(await get('upload').isDisabled());
    };
    const { ui_version } = JSON.parse(await frame.locator('#build-info').textContent());
    const out = path.resolve(process.env.UI_OUTPUT || `artifacts/credential-conversion-${ui_version}/browser`);
    fs.mkdirSync(out, { recursive: true });
    const layouts = [];
    const checkLayout = async (stage, width, control) => {
      await page.setViewportSize({ width, height: width < 500 ? 700 : 900 });
      const size = await dialog.evaluate(n => ({
        dialog_width: n.clientWidth, scroll_width: n.scrollWidth, dialog_height: n.clientHeight, iframe_width: innerWidth,
      }));
      assert(size.scroll_width <= size.dialog_width + 1 && size.dialog_width <= size.iframe_width && size.iframe_width <= width, JSON.stringify(size));
      const reachable = await control.evaluate(n => {
        const r = n.getBoundingClientRect(); return n.contains(document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2));
      });
      assert(reachable, `${stage} action outside the iframe at ${width}px`);
      await dialog.screenshot({ path: path.join(out, `conversion-${stage}-${width}.png`) }); layouts.push({ stage, width, ...size });
    };
    const cryptoFeatures = await frame.locator('html').evaluate(() => [window.isSecureContext, typeof crypto.randomUUID, typeof crypto.getRandomValues]);
    assert.deepEqual(cryptoFeatures, [false, 'undefined', 'function']);
    await connect();

    await open(); await get('dropzone').waitFor(); assert(await get('preview').isDisabled());
    for (const width of [1440, 390, 320]) await checkLayout('input', width, get('file-open'));
    await page.setViewportSize({ width: 1440, height: 900 });
    const file = { name: 'two-synthetic-credentials.json', content: source };
    await dropFiles(get('dropzone'), [{ name: 'oversize.json', size: 2 * 1024 * 1024 + 1 }]);
    await get('feedback').filter({ hasText: /2\s*(?:MB|MiB)/i }).waitFor();
    await dropFiles(get('dropzone'), [file, { ...file, name: 'another.json' }]);
    await get('feedback').filter({ hasText: /一个|1 个|单个/ }).waitFor();
    assert.deepEqual(previews, []); assert.deepEqual(uploads, []);
    await dropFiles(get('dropzone'), [{ name: 'malformed.json', content: '{' }]);
    await get('feedback').filter({ hasText: /JSON/ }).waitFor(); await idle();
    assert(await get('results').isHidden()); assert.equal(uploads.length, 0);
    const beforeDrop = previews.length;
    await dropFiles(get('dropzone'), [file]); await get('results').waitFor(); await idle();
    assert.deepEqual(previews.slice(beforeDrop), [source]); assert.deepEqual(uploads, []);
    assert.equal(await get('source').inputValue(), ''); assert.equal(await get('file-name').textContent(), file.name);
    for (const [i, account] of accounts.entries()) {
      assert(await select(i).isChecked()); assert.match(await row(i).innerText(), new RegExp(account.credentials.email));
    }
    assert.doesNotMatch(await dialog.innerText(), /SYNTHETIC_(ACCESS|REFRESH)_CANARY/);
    await select(0).uncheck(); await select(1).uncheck(); assert(await get('upload').isDisabled());
    await select(1).check(); await select(0).check(); assert(await get('upload').isEnabled());
    const downloadEvent = page.waitForEvent('download'); await dialog.locator('[data-convert-download="1"]').click();
    const download = await downloadEvent; assert.equal(download.suggestedFilename(), ready[1].filename);
    assert.deepEqual(JSON.parse(fs.readFileSync(await download.path(), 'utf8')), ready[1].credential);
    for (const width of [1440, 1064, 620, 390, 320]) await checkLayout('result', width, get('upload'));
    await page.setViewportSize({ width: 1064, height: 800 });

    const pending = new Promise(resolve => { replyToUpload = resolve; });
    const send = page.waitForRequest(req => req.url().endsWith('/conversion/upload'));
    await get('upload').click(); await send; const held = await pending;
    assert.deepEqual(uploads, [{ source, indexes: [0, 1] }]);
    await page.keyboard.press('Escape'); assert(await dialog.evaluate(n => n.open));
    for (const name of ['upload', 'close', 'cancel', 'edit']) assert(await get(name).isDisabled());
    for (const i of [0, 1]) assert(await select(i).isDisabled());
    await get('upload').evaluate(n => n.click()); failRefresh = true;
    const refreshFailure = page.waitForEvent('requestfailed', request => new URL(request.url()).pathname === '/v0/management/plugins/model-quality/login/accounts');
    await held.fulfill({ json: { items: [{ index: 1, status: 'uploaded', enabled: false }, { index: 0, status: 'uploaded', enabled: true }] } });
    await idle(); await refreshFailure; assert.equal(uploads.length, 1);
    assert.match(await row(0).innerText(), /启用/); assert.match(await row(1).innerText(), /禁用/);
    await assertLocked(0); await assertLocked(1); assert(await get('upload').isDisabled());
    await close(); assert.equal(await get('source').inputValue(), ''); assert.equal(await get('list').textContent(), '');

    // File picker also previews automatically; select one row and explicitly retry only its known pre-write failure.
    await open();
    const chooserEvent = page.waitForEvent('filechooser'); await get('file-open').click();
    await (await chooserEvent).setFiles({ name: file.name, mimeType: 'application/json', buffer: Buffer.from(source) });
    await get('results').waitFor(); await idle(); assert.equal(previews.at(-1), source); assert.equal(uploads.length, 1);
    await select(0).uncheck();
    await submit({ source, indexes: [1] }, receipts([{ index: 1, status: 'failed', code: 'security_pending' }]));
    assert(await select(0).isEnabled()); assert(!await select(0).isChecked());
    assert(await select(1).isEnabled()); assert(!await select(1).isChecked()); assert(await get('upload').isDisabled());
    await select(1).check();
    await submit({ source, indexes: [1] }, receipts([{ index: 1, status: 'failed', code: 'duplicate_identity' }]));
    await assertLocked(1); assert(await select(0).isEnabled());
    await select(0).check(); await submit({ source, indexes: [0] }, receipts([]));
    await assertLocked(0); assert(await get('upload').isDisabled()); await close();

    // Manual input waits for Preview. An invalid companion cannot be uploaded, and a lost write is never replayed.
    await open(); await paste(mixedSource); assert(await select(0).isChecked()); await assertLocked(1);
    assert.match(await row(1).innerText(), /access_token/);
    await submit({ source: mixedSource, indexes: [0] }, route => route.abort('connectionfailed'));
    await assertLocked(0); assert(await get('upload').isDisabled());
    assert.match(await row(0).innerText(), /确认|核对/);
    await get('upload').evaluate(n => n.click()); assert.equal(uploads.length, 5);
    await close();

    await open(); await paste(duplicateSource); await assertLocked(1);
    assert.match(await row(1).innerText(), /第\s*1\s*条/);
    await submit({ source: duplicateSource, indexes: [0, 2] }, receipts([
      { index: 2, status: 'uploaded', enabled: false }, { index: 0, status: 'existing', code: 'conversion_already_present' },
    ]));
    assert.equal(await row(0).getAttribute('data-state'), 'existing'); assert.match(await row(0).innerText(), /已有|已存在/);
    assert.equal(await row(2).getAttribute('data-state'), 'uploaded'); assert.match(await row(2).innerText(), /禁用/);
    await assertLocked(0); await assertLocked(2); assert(await get('upload').isDisabled());
    await checkLayout('receipt', 1440, get('cancel'));
    await page.locator('#fixture-theme').selectOption('dark'); await frame.locator('html[data-theme="dark"]').waitFor();
    await checkLayout('receipt-dark', 390, get('cancel'));
    await page.locator('#fixture-theme').selectOption('light'); await page.setViewportSize({ width: 1064, height: 800 });
    await close(); assert(await get('open').evaluate(n => n === document.activeElement));

    // A delayed server response for the previous drop must not replace the current file's preview.
    await open();
    const delayed = await holdFilePreview(file);
    const replacement = { name: 'replacement.json', content: mixedSource };
    await dropFiles(get('dropzone'), [replacement]); await get('results').waitFor(); await idle();
    await finishOldPreview(delayed);
    assert.equal(await get('file-name').textContent(), replacement.name); await assertLocked(1);
    assert.match(await row(1).innerText(), /access_token/);
    await close(); await assertEmpty();
    await open(); const previousDialog = await holdFilePreview(file);
    await close(); await open(); await finishOldPreview(previousDialog);
    await assertEmpty(); assert(await get('dropzone').isVisible());

    // A real management 401 from an independent poll disconnects the plugin and cancels the in-flight preview.
    const beforeDisconnect = await holdFilePreview(file);
    const cancelled = page.waitForEvent('requestfailed', req => req === beforeDisconnect.request);
    expireOnRead = true;
    await frame.locator('body:not(.connected)').waitFor(); await dialog.waitFor({ state: 'hidden' });
    await beforeDisconnect.route.fulfill({ json: { items: ready, ready: 2 } }); await cancelled;
    await assertEmpty(); assert(await get('open').isDisabled());
    await connect(); await open(); await assertEmpty(); assert(await get('dropzone').isVisible()); await close();
    assert(keys.every(key => typeof key === 'string' && key.length > 0)); assert.equal(new Set(keys).size, keys.length);
    assert.deepEqual(unexpected, []); assert.deepEqual(errors, []);
    fs.writeFileSync(path.join(out, 'conversion.json'), JSON.stringify({
      ui_version, layouts, insecure_http: true, dropped_two_credentials: true, file_picker: true, manual_preview: true,
      invalid_files: true, superseded_preview_discarded: true, closed_preview_discarded: true, disconnected_preview_discarded: true,
      converted_download: true, exact_selected_indexes: true,
      per_item_receipts: true, upload_busy_guard: true, uncertain_not_replayed: true, closing_clears_material: true,
      previews: previews.length, simulated_upload_requests: uploads.length, real_writes: 0,
    }, null, 2) + '\n');
    console.log('Credential conversion passed in the HTTP fixture: two-account drag and drop, picker, paste, selected uploads and receipts.');
  } finally { if (browser) await browser.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
