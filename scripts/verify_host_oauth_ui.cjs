// Invoked by TestHostOAuthBrowser with the public files from the tagged Go build.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');

const origin = 'https://cpa.example.test';
const resource = '/v0/resource/plugins/model-quality/';
const management = '/v0/management/plugins/model-quality/';
const panel = origin + '/management.html#/plugin-pages/model-quality/0';
const labels = [['添加 OAuth 账号', '/oauth'], ['管理已有凭证', '/auth-files']];
const host = `<!doctype html><meta charset="utf-8"><title>CLI Proxy API Management Center</title>
<style>body{margin:0}iframe{border:0;width:100%;height:100dvh;display:block}</style>
<body><script>
function render() {
  if (location.hash.startsWith('#/plugin-pages/')) {
    const frame = document.createElement('iframe');
    frame.src = '${resource}index.html';
    document.body.replaceChildren(frame);
  } else {
    document.body.textContent = location.hash;
  }
}
addEventListener('hashchange', render);
render();
</script></body>`;
const target = {auth_id: 'fixture-credential', provider: 'openai', models: ['fixture-model'], effective_available: true};
const responses = {
  connection: { state: 'ready', revision: 1, configured: true, can_migrate: false },
  targets: { items: [target], total: 1 }, questions: { items: [] },
  rules: { items: [] }, actions: { items: [] }, runs: { items: [], total: 0 },
  settings: {}, metrics: {},
};

(async () => {
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH });
  try {
    const page = await browser.newPage();
    const session = [];
    const violations = [];
    page.on('pageerror', error => violations.push(error.message));
    let mode = 'host_oauth';
    await page.route('**/*', async route => {
      const req = route.request();
      const url = new URL(req.url());
      if (url.origin === origin && req.method() === 'GET') {
        if (url.pathname === '/management.html') return route.fulfill({ contentType: 'text/html', body: host });
        if (url.pathname.startsWith(resource)) {
          const name = url.pathname.slice(resource.length);
          if (['index.html', 'login.js', 'app.js', 'view.js', 'client.js', 'style.css'].includes(name)) {
            const type = name.endsWith('.js') ? 'text/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html';
            return route.fulfill({ contentType: type, path: path.join(process.argv[2], name) });
          }
        }
        if (url.pathname.startsWith(management) && req.headers().authorization === 'Bearer fixture-key') {
          const endpoint = url.pathname.slice(management.length);
          session.push(endpoint);
          if (endpoint === 'login/capabilities') return route.fulfill({ json: { login_mode: mode } });
          if (Object.hasOwn(responses, endpoint)) return route.fulfill({ json: responses[endpoint] });
        }
      }
      violations.push(req.method() + ' ' + req.url());
      return route.abort();
    });
    await page.goto(panel);
    const frame = page.frameLocator('iframe');
    for (const [label, destination] of labels) {
      await frame.getByRole('link', { name: label }).click();
      await page.waitForURL(origin + '/management.html#' + destination);
      await page.locator('iframe').waitFor({ state: 'detached' });
      assert.equal(await page.locator('body').innerText(), '#' + destination);
      await page.goBack();
    }
    assert.equal(session.length, 0);

    const capabilitiesRead = () => page.waitForResponse(origin + management + 'login/capabilities');
    await frame.getByRole('button', { name: '未连接', exact: true }).click();
    await frame.locator('#management-key').fill('fixture-key');
    await Promise.all([capabilitiesRead(), frame.locator('#connection-form button[type="submit"]').click()]);
    await frame.locator('body.connected').waitFor();
    assert.equal(await frame.locator('#login-workbench').count(), 0);
    assert.deepEqual(session.filter(p => p.startsWith('login/')), ['login/capabilities']);
    await frame.locator('[data-page="manual"]').click();
    await frame.locator('#target').selectOption('fixture-credential');
    await frame.locator('#models').selectOption('fixture-model');
    await frame.locator('[data-page="operations"]').click();
    await frame.locator('#page-operations').waitFor();
    await Promise.all([capabilitiesRead(), frame.locator('[data-page="login"]').click()]);
    mode = 'fixture-version-mismatch';
    await Promise.all([capabilitiesRead(), frame.locator('#refresh').click()]);
    await frame.locator('#host-oauth-status').waitFor();
    mode = 'host_oauth';
    await Promise.all([capabilitiesRead(), frame.locator('#refresh').click()]);
    await frame.locator('#host-oauth-status').waitFor({ state: 'hidden' });
    for (const [width, height] of [[1366, 768], [375, 780]]) {
      await page.setViewportSize({ width, height });
      for (const [label] of labels) {
        const box = await frame.getByRole('link', {name: label}).boundingBox();
        assert(box && box.width > 0 && box.height >= 44 && box.x >= 0 && box.x + box.width <= width);
      }
      if (process.env.UI_VERIFY_OUTPUT) {
        fs.mkdirSync(process.env.UI_VERIFY_OUTPUT, { recursive: true });
        await page.screenshot({ path: path.join(process.env.UI_VERIFY_OUTPUT, `host-oauth-${width}.png`) });
      }
    }
    await page.goto(origin + resource + 'index.html');
    await page.getByRole('link', { name: labels[1][0] }).click();
    await page.waitForURL(origin + '/management.html#/auth-files');
    assert.deepEqual(violations, []);
    assert(session.includes('rules') && session.includes('targets') && session.includes('questions'));
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
