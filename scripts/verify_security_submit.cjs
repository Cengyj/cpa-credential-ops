// Exercise the confirmation form using local reads and intercepted synthetic writes.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const root = path.resolve(__dirname, '..');
const version = JSON.parse(fs.readFileSync(path.join(root, 'plugin/ui/index.html'), 'utf8').match(/id="build-info">(.*?)<\/script>/)[1]).ui_version;
const out = path.resolve(process.env.UI_OUTPUT || path.join(root, `artifacts/ui-rework-${version}/browser`));

async function run() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await Promise.race([once(createInterface({ input: fixture.stdout }), 'line'), exit.then(() => { throw Error('Fixture failed'); })]);
    const { url, management_key: key } = JSON.parse(line), { origin } = new URL(url);
    assert.equal(new URL(origin).hostname, '127.0.0.1');
    browser = await chromium.launch({ executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', headless: true });
    const context = await browser.newContext({ viewport: { width: 1064, height: 800 }, reducedMotion: 'reduce' });
    const writes = [], blocked = [], errors = [], cases = [];
    let capture;
    await context.route('**/*', route => {
      const request = route.request();
      if (new URL(request.url()).origin !== origin) { blocked.push('external'); return route.abort(); }
      if (request.method() === 'GET') return route.continue();
      if (new URL(request.url()).pathname.endsWith('/login/accounts/security') && request.method() === 'POST' && capture) {
        const notify = capture; capture = null;
        writes.push(request.postDataJSON()); notify({ route, request });
        return;
      }
      blocked.push(request.method()); return route.abort();
    });
    const page = await context.newPage(); page.setDefaultTimeout(7000);
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(origin);
    await page.locator('#login-connect').click(); await page.locator('#management-key').fill(key);
    await page.locator('#connection-form button[type=submit]').click();
    const row = page.locator('[data-account-row="fixture-enabled"]');
    const dialog = page.locator('#account-security-dialog');
    const get = name => dialog.locator(`#account-security-${name}`);
    const open = () => row.locator('[data-account-action="reset_totp"]').click();
    const settle = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    const receive = async () => {
      let timer;
      try {
        const arrival = new Promise(resolve => { capture = resolve; });
        await get('submit').click();
        return await Promise.race([arrival, new Promise((_, reject) => { timer = setTimeout(() => reject(Error('No security submission')), 7000); })]);
      } finally { clearTimeout(timer); }
    };
    for (const [logout, reauthorize] of [[false, true], [true, true], [true, false], [false, false]]) {
      await open();
      const previous = cases.at(-1) || { logout: false, reauthorize: false };
      assert.equal(await get('logout').isChecked(), previous.logout);
      assert.equal(await get('reauthorize').isChecked(), previous.reauthorize);
      await get('logout').setChecked(logout); await get('reauthorize').setChecked(reauthorize);
      assert.equal(writes.length, cases.length, 'Opening and editing must not submit');
      const { route, request } = await receive();
      assert.deepEqual(request.postDataJSON(), { id: 'fixture-enabled', revision: 1, action: 'reset_totp', logout_all_devices: logout, reauthorize, upload_to_cpa: false });
      assert(request.headers()['idempotency-key']);
      for (const name of ['submit', 'cancel', 'close', 'logout', 'reauthorize']) assert(await get(name).isDisabled());
      assert.equal(await get('submit').innerText(), '正在提交…');
      await page.keyboard.press('Escape'); assert(await dialog.evaluate(el => el.open));
      await get('form').evaluate(el => el.requestSubmit()); await settle();
      assert.equal(writes.length, cases.length + 1, 'Busy form must not submit twice');
      await route.fulfill({ status: 409, json: { code: 'conflict' } });
      await get('error').waitFor(); await dialog.locator('#account-security-cancel:enabled').waitFor();
      await get('cancel').click();
      cases.push({ action: 'reset_totp', logout, reauthorize });
    }
    await row.locator('[data-account-action="more"]').click(); await page.locator('#account-logout-all').click();
    await get('reauthorize').uncheck();
    const logout = await receive();
    assert.deepEqual(logout.request.postDataJSON(), { id: 'fixture-enabled', revision: 1, action: 'logout_all', logout_all_devices: false, reauthorize: false, upload_to_cpa: false });
    await logout.route.fulfill({ status: 409, json: { code: 'conflict' } });
    await get('error').waitFor(); await dialog.locator('#account-security-cancel:enabled').waitFor(); await get('cancel').click();
    cases.push({ action: 'logout_all', logout: false, reauthorize: false });
    await open(); const uncertain = await receive(); await uncertain.route.abort('connectionfailed');
    await get('error').waitFor(); await dialog.locator('#account-security-cancel:enabled').waitFor();
    for (const name of ['submit', 'logout', 'reauthorize']) assert(await get(name).isDisabled());
    await get('form').evaluate(el => el.requestSubmit()); await settle(); assert.equal(writes.length, 6);
    await get('cancel').click();
    await open(); const malformed = await receive();
    await malformed.route.fulfill({ status: 200, contentType: 'application/json', body: '{"id":' });
    await get('error').waitFor(); await dialog.locator('#account-security-cancel:enabled').waitFor();
    assert.match(await get('error').innerText(), /提交结果尚未确认/);
    for (const name of ['submit', 'logout', 'reauthorize']) assert(await get(name).isDisabled());
    await get('form').evaluate(el => el.requestSubmit()); await settle(); assert.equal(writes.length, 7);
    await get('cancel').click(); assert.deepEqual(blocked, []); assert.deepEqual(errors, []);
    fs.mkdirSync(out, { recursive: true });
    fs.writeFileSync(path.join(out, 'security-submit.json'), JSON.stringify({ ui_version: version, fixture: 'synthetic; all writes intercepted before network', cases, busy_guard: true, uncertain_submission_blocked: true, malformed_response_blocked: true, intercepted_writes: writes.length, forwarded_writes: 0 }, null, 2) + '\n');
    console.log('Four reset payloads, independent logout, busy, uncertain-submit and malformed-response guards passed; zero forwarded writes.');
  } finally { if (browser) await browser.close(); fixture.kill(); await exit; }
}
run().catch(error => { console.error(error); process.exitCode = 1; });
