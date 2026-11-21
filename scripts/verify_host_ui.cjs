// Render the pinned CPA frontend around the real native plugin in an isolated fake host.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const crypto = require('node:crypto');
const os = require('node:os');
const { execFileSync } = require('node:child_process');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const root = path.resolve(__dirname, '..');
const output = path.resolve(process.env.UI_VERIFY_OUTPUT || path.join(root, 'artifacts/samepage-4.6.0/verification'));
const demoDirectory = path.resolve(process.env.MQ_DEMO_DIR || path.join(root, 'artifacts/samepage-4.6.0/host-ui-demo'));
const preview = process.env.HOST_UI_PREVIEW_SOURCE === '1';
const names = require('../plugin/ui/public_assets.json');
const source = Object.fromEntries(names.map(name => [name, fs.readFileSync(path.join(root, 'plugin/ui', name))]));
const hash = value => crypto.createHash('sha256').update(value).digest('hex');
const sourceHashes = Object.fromEntries(names.map(name => [name, hash(source[name])]));
const sourceVersion = JSON.parse(source['index.html'].toString().match(/<script type="application\/json" id="build-info">(.*?)<\/script>/)[1]).ui_version;
const baselineManifest = JSON.parse(fs.readFileSync(path.join(root, 'artifacts/samepage-4.5.0/baseline/baseline.json')));
const baselineCommit = baselineManifest.root_head;
const baselineNames = Object.keys(baselineManifest.sha256);
const baseline = Object.fromEntries(baselineNames.map(name => [name, fs.readFileSync(path.join(root, 'artifacts/samepage-4.5.0/baseline/frozen/plugin/ui', name))]));
assert.deepEqual(Object.fromEntries(baselineNames.map(name => [name, hash(baseline[name])])), baselineManifest.sha256);
const referenceRoot = path.join(root, 'workspaces/cpa-management-reference');
const frontend = path.join(referenceRoot, 'dist/index.html');
const referenceHead = 'd0126752201a35ff999bd2a1d4741215542258a8';
const referenceCheckoutVerified = fs.existsSync(path.join(referenceRoot, '.git'));
if (referenceCheckoutVerified) {
  assert.equal(execFileSync('git', ['-C', referenceRoot, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim(), referenceHead);
  assert.equal(execFileSync('git', ['-C', referenceRoot, 'status', '--short'], { encoding: 'utf8' }).trim(), '');
}
assert.equal(hash(fs.readFileSync(frontend)), '650ca63b43487bcf2ebda6e6d66b8ac8429e5c1715c5f00f50405e9addf0bf70');
assert(demoDirectory.startsWith(path.join(root, 'artifacts') + path.sep));
fs.mkdirSync(output, { recursive: true });
fs.mkdirSync(demoDirectory, { recursive: true });
const statePath = path.join(demoDirectory, 'state.json');
if (fs.existsSync(statePath)) assert(!['ready', 'starting'].includes(JSON.parse(fs.readFileSync(statePath)).status), 'Do not reuse an already running demo');
const env = { ...process.env, MQ_DEMO_DIR: demoDirectory };
const demo = command => JSON.parse(execFileSync('python3', [path.join(root, 'scripts/demo.py'), command], { cwd: root, env, encoding: 'utf8', timeout: 75000 }));
const mime = name => name.endsWith('.js') ? 'application/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html';
const report = {
  result: 'failed', started_at: new Date().toISOString(), scope: preview ? 'Preview: real host and pinned CPA shell; plugin source intercepted for development' : 'Real patched CPA host, native plugin, pinned CPA management frontend, loopback fake upstream and fake credentials',
  source_sha256: sourceHashes, baseline_commit: baselineCommit,
  baseline_source_sha256: Object.fromEntries(baselineNames.map(name => [name, hash(baseline[name])])),
  host_reference: { head: referenceHead, checkout_verified: referenceCheckoutVerified, html_sha256: hash(fs.readFileSync(frontend)), artifact: path.relative(root, frontend), provenance: 'Existing single-file production build in the clean pinned reference checkout; same SHA-256 as previously audited installed management static file.' },
  checks: [], screenshots: [], screenshot_sha256: {}, matrix: [], compact_geometry: [], history_geometry: [], connection_geometry: [],
};
const check = text => report.checks.push(text);
(async () => {
  let browser, state;
  try {
    state = demo('start');
    assert(!state.host_url.endsWith(':8317'));
    report.host_url = state.host_url;
    report.binary_sha256 = Object.fromEntries(['cli-proxy-api-quality', `model-quality.${process.platform === 'darwin' ? 'dylib' : 'so'}`].map(name => [`artifacts/${name}`, hash(fs.readFileSync(path.join(root, 'artifacts', name)))]));
    report.session_directory = path.relative(root, state.session_dir);
    const nativeHashes = {};
    for (const name of names) nativeHashes[name] = hash(Buffer.from(await (await fetch(`${state.host_url}/v0/resource/plugins/model-quality/${name}`)).arrayBuffer()));
    report.native_resource_sha256 = nativeHashes;
    if (!preview) assert.deepEqual(nativeHashes, sourceHashes, 'Native resources must match the current source');
    check(preview ? 'Development source override explicitly recorded' : 'All six actual native resource hashes equal current source');
    browser = await chromium.launch({ headless: true, executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    report.browser = await browser.version();
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', reducedMotion: 'reduce', colorScheme: 'light' });
    let variant = 'current';
    const routeRequest = async route => {
      const url = new URL(route.request().url());
      if (['/unknown-theme-parent', '/unsupported-theme-parent', '/cross-theme-parent'].includes(url.pathname) && ['127.0.0.1', 'localhost'].includes(url.hostname) && url.port === new URL(state.host_url).port) {
        const title = url.pathname === '/unknown-theme-parent' ? 'Unrecognized application' : 'CLI Proxy API Management Center';
        const theme = url.pathname === '/unsupported-theme-parent' ? 'future-theme' : 'dark';
        await route.fulfill({ contentType: 'text/html', body: `<!doctype html><html data-theme="${theme}"><head><title>${title}</title></head><body><iframe src="${state.page_url}" style="width:100%;height:800px"></iframe></body></html>` });
        return;
      }
      if (url.origin !== state.host_url) { await route.abort(); return; }
      if (url.pathname === '/management.html') { await route.fulfill({ contentType: 'text/html', body: fs.readFileSync(frontend) }); return; }
      const name = names.find(name => url.pathname === `/v0/resource/plugins/model-quality/${name}`);
      if (name && (variant === 'baseline' || preview)) {
        await route.fulfill({ contentType: mime(name), body: (variant === 'baseline' ? baseline : source)[name] }); return;
      }
      await route.continue();
    };
    await context.route('**/*', routeRequest);
    const page = await context.newPage();
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(`${state.host_url}/management.html#/login`);
    await page.locator('input[name="cpa-management-key"]').fill(state.admin_key);
    const remember = page.getByRole('checkbox');
    if (await remember.count()) await remember.first().uncheck();
    await page.getByRole('button', { name: '登录', exact: true }).click();
    await page.locator('.main-header').waitFor();
    check('Pinned CPA frontend authenticates against the isolated actual host using only its fake management key');
    const navigate = async hashPath => {
      const link = page.locator(`a[href="#${hashPath}"]`).first();
      if (await page.locator('.mobile-menu-btn').isVisible() && !(await page.locator('.sidebar').evaluate(node => node.classList.contains('open')))) await page.locator('.mobile-menu-btn').click();
      await link.click();
      if (hashPath.startsWith('/plugin-pages/')) {
        try { await page.locator('iframe').waitFor(); } catch (error) { fs.writeFileSync(path.join(output, 'host-failure.txt'), await page.locator('body').innerText()); await page.screenshot({path:path.join(output, 'host-failure.png')}); throw error; }
        const frame = await page.locator('iframe').first().elementHandle().then(node => node.contentFrame());
        assert(frame, 'Plugin resource iframe must load');
        await frame.locator('#run-form').waitFor();
        await frame.locator('#page-manual[data-view]').waitFor();
        await page.locator('.page-transition--animating').waitFor({ state: 'detached' });
        return frame;
      }
      await page.locator('iframe').waitFor({ state: 'detached' });
      return null;
    };
    const setTheme = async theme => {
      await page.locator('.theme-menu > button').click();
      await page.getByRole('menuitemradio', { name: { light: '羊毛纸', white: '纯白', dark: '暗色', auto: '跟随系统' }[theme], exact: true }).click();
      await page.waitForFunction(expected => document.documentElement.getAttribute('data-theme') === expected, theme === 'light' ? null : theme === 'auto' ? 'white' : theme);
    };
    const sidebar = async collapsed => {
      if (!(await page.locator('.sidebar-toggle-floating').isVisible())) return 'mobile-closed';
      const current = await page.locator('.app-shell').evaluate(node => node.classList.contains('sidebar-is-collapsed'));
      if (current !== collapsed) await page.locator('.sidebar-toggle-floating').click();
      await page.waitForFunction(expected => {
        const shell = document.querySelector('.app-shell');
        if (shell.classList.contains('sidebar-is-collapsed') !== expected) return false;
        const style = getComputedStyle(shell);
        const width = parseFloat(style.getPropertyValue(expected ? '--sidebar-collapsed-width' : '--sidebar-panel-width'));
        const content = document.querySelector('iframe') || document.querySelector('.main-content');
        return !content || Math.abs(content.getBoundingClientRect().width - (innerWidth - width)) <= 1;
      }, collapsed);
      await page.evaluate(async () => {
        const iframe = document.querySelector('iframe');
        let previous = '', stable = 0;
        for (let index = 0; index < 120 && stable < 12; index++) {
          await new Promise(resolve => requestAnimationFrame(resolve));
          const box = (iframe || document.querySelector('.main-content')).getBoundingClientRect();
          const current = [box.x, box.width].map(value => value.toFixed(2)).join(':');
          stable = current === previous ? stable + 1 : 0;
          previous = current;
        }
        if (stable < 12) throw new Error('Host sidebar geometry did not settle');
      });
      return collapsed ? 'collapsed' : 'expanded';
    };
    const capture = async (file, detail) => {
      const target = path.join(output, file);
      await page.screenshot({ path: target, animations: 'disabled' });
      report.screenshots.push({ file, ...detail });
      report.screenshot_sha256[file] = hash(fs.readFileSync(target));
    };
    const connectPlugin = async frame => {
      const saved = variant !== 'baseline' && await frame.evaluate(() => !!localStorage.getItem('model-quality:management-key:/v8/management/plugins/model-quality'));
      if (!saved) {
        await frame.locator('#connection-open').focus();
        await frame.locator('#connection-open').press('Enter');
        await frame.locator('#management-key').fill(state.admin_key);
        await frame.locator('#connection-form button[type="submit"]').click();
      }
      await frame.locator('body.connected').waitFor();
      const options = await frame.locator('#target option').evaluateAll(nodes => nodes.map(node => node.value).filter(Boolean));
      assert(options.length);
      await frame.locator('#target').selectOption(options[0]);
      if (variant === 'baseline') await frame.locator('#models').fill('mq-reasoner');
      else await frame.locator('#models').selectOption('mq-reasoner');
      await frame.locator('#question').selectOption('logic-candy');
    };
    const verifyEmptyHistory = async (target, connection) => {
      const initialViewport = page.viewportSize();
      await target.locator('[data-manual-view="history"]').click();
      await target.locator('#manual-history').waitFor({ state: 'visible' });
      assert.equal(await target.locator('#history-list [data-run]').count(), 0, `${connection}: the native history fixture is empty`);
      for (const theme of ['light', 'white', 'dark']) {
        await setTheme(theme);
        await target.locator(`html[data-theme="${theme}"]`).waitFor();
        for (const [width, height] of [[1640, 870], [1280, 720], [390, 844]]) {
          await page.setViewportSize({ width, height });
          await sidebar(false);
          await target.evaluate(() => scrollTo(0, 0));
          if (connection === 'disconnected') {
            const connectionGeometry = await target.evaluate(() => {
              const box = selector => document.querySelector(selector).getBoundingClientRect().toJSON();
              const button = document.getElementById('connection-open');
              return { nav: box('.primary-nav'), connection: box('#connection-open'), options: box('.option-row'), start: box('#run-submit'), prompt: box('#prompt'), inHeader: !!button.closest('.app-header'), mainBanner: !!document.querySelector('main #connect-banner'), frameWidth: innerWidth };
            });
            report.connection_geometry.push({ theme, viewport: { width, height }, ...connectionGeometry });
            assert(connectionGeometry.inHeader && !connectionGeometry.mainBanner, 'Disconnected management connection belongs in the header, without a duplicate main banner');
            if (connectionGeometry.frameWidth > 600) {
              assert(connectionGeometry.connection.x >= connectionGeometry.nav.right - 1, `${theme} ${width}: connection follows the task tabs`);
              assert(Math.max(connectionGeometry.connection.top, connectionGeometry.nav.top) < Math.min(connectionGeometry.connection.bottom, connectionGeometry.nav.bottom), `${theme} ${width}: connection and task tabs share a header row`);
            } else {
              assert(connectionGeometry.connection.top >= connectionGeometry.nav.bottom, 'Phone connection wraps below the three tabs');
              assert(await target.locator('.primary-nav').evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'All three phone tabs fit');
            }
            if (width >= 1280) {
              const actionGap = connectionGeometry.start.top - connectionGeometry.options.bottom;
              assert(actionGap >= 12 - 1 && actionGap <= 20 + 1, `${theme} ${width}: disconnected Start has a compact 12–20px gap below options: ${JSON.stringify(connectionGeometry)}`);
              assert(Math.abs(connectionGeometry.start.bottom - connectionGeometry.prompt.bottom) <= 1, 'Disconnected Start remains aligned with the prompt bottom');
            } else assert(connectionGeometry.connection.height >= 44, 'The embedded phone connection action keeps a 44px touch target');
          }
          const geometry = await target.evaluate(() => {
            const panel = document.getElementById('manual-history');
            const workspace = document.querySelector('.answer-workspace');
            const toolbar = document.querySelector('.answer-toolbar');
            panel.scrollTop = 10000;
            workspace.scrollTop = 10000;
            const box = node => node.getBoundingClientRect().toJSON();
            const pagination = panel.querySelector('.pagination');
            return { width: innerWidth, height: innerHeight, documentHeight: document.documentElement.scrollHeight, workspace: { ...box(workspace), scrollHeight: workspace.scrollHeight, clientHeight: workspace.clientHeight, scrollTop: workspace.scrollTop, overflow: getComputedStyle(workspace).overflowY }, panel: { ...box(panel), scrollHeight: panel.scrollHeight, clientHeight: panel.clientHeight, scrollTop: panel.scrollTop }, toolbar: box(toolbar), heading: box(panel.querySelector('.history-summary')), content: box(document.getElementById('history-list')), paginationVisible: !!pagination.getClientRects().length };
          });
          report.history_geometry.push({ connection, theme, viewport: { width, height }, ...geometry });
          assert(geometry.panel.scrollHeight <= geometry.panel.clientHeight + 1 && geometry.panel.scrollTop === 0, `${connection} ${theme} ${width}: empty history must not have internal scrolling: ${JSON.stringify(geometry)}`);
          assert(geometry.workspace.scrollHeight <= geometry.workspace.clientHeight + 1 && geometry.workspace.scrollTop === 0, `${connection} ${theme} ${width}: toolbar container must not scroll: ${JSON.stringify(geometry)}`);
          assert(geometry.heading.top >= geometry.toolbar.bottom - 1, `${connection} ${theme} ${width}: history heading is below the non-scrolling toolbar`);
          assert(!geometry.paginationVisible, `${connection} ${theme} ${width}: empty history does not expose unnecessary pagination`);
          assert(geometry.panel.bottom - geometry.content.bottom <= 32, `${connection} ${theme} ${width}: normal empty history should fit its content without large filler space`);
          if (width >= 1280) {
            assert(geometry.documentHeight <= geometry.height + 2 && geometry.content.bottom <= geometry.height + 1, `${connection} ${theme} ${width}: fitting empty history remains entirely in the desktop viewport`);
          } else {
            await target.locator('#history-list').scrollIntoViewIfNeeded();
            const content = await target.locator('#history-list').boundingBox();
            assert(content.y >= 0 && content.y + content.height <= height + 1, 'Phone empty history content remains reachable');
          }
          await capture(`empty-history-${connection}-${theme}-${width}x${height}.png`, { role: 'empty-history', connection, theme, viewport: { width, height }, data: 'actual native plugin with no manual runs' });
        }
      }
      await page.setViewportSize(initialViewport);
      await sidebar(false);
      await setTheme('light');
      await target.evaluate(() => scrollTo(0, 0));
      await target.locator('[data-manual-view="results"]').click();
      check(`${connection}: empty native history has no false scrolling, keeps its summary below the toolbar, hides unnecessary pagination and fits its content in all three themes at 1640×870, 1280×720 and 390×844`);
      if (connection === 'disconnected') check('Disconnected connection action stays beside the header tabs with no main banner; desktop Start has 12–20px option spacing and aligns with the prompt bottom');
    };
    let frame;
    for (const theme of ['light', 'white', 'dark']) {
      await setTheme(theme);
      await navigate('/plugins');
      await page.getByRole('heading', { name: '插件管理', exact: true }).waitFor();
      await page.locator('main').getByText('model-quality', { exact: true }).first().waitFor();
      await capture(`host-plugins-${theme}-1440x900.png`, { role: 'host-adjacent', theme, viewport: { width: 1440, height: 900 }, data: 'native fake-host plugin listing' });
    }
    await setTheme('light');
    frame = await navigate('/plugin-pages/model-quality/0');
    await frame.locator('html[data-theme-source="host"][data-theme="light"]').waitFor();
    assert.equal(await frame.locator('#theme-toggle').isVisible(), false);
    assert.equal(await frame.locator('body').evaluate(node => node.classList.contains('connected')), false);
    check('An authenticated CPA shell does not imply a connected plugin; host theme follows while plugin still requests its own connection');
    await verifyEmptyHistory(frame, 'disconnected');
    await connectPlugin(frame);
    await verifyEmptyHistory(frame, 'connected');
    const callsBefore = (await (await fetch(`${state.upstream_url}/stats`)).json()).calls.length;
    assert.equal(callsBefore, 0);
    await frame.locator('#samples').selectOption('2');
    await frame.locator('#run-submit').click();
    await frame.waitForFunction(() => document.querySelectorAll('.sample:not(.sample-pending)').length === 2 && document.getElementById('run-cancel').classList.contains('hidden'));
    check('Actual embedded native plugin completes one explicit two-answer candy run against the offline fake upstream');
    const manualView = async (target, view) => {
      await target.evaluate(() => window.scrollTo(0, 0));
      if (view === 'setup') {
        if (await target.locator('#reading-toggle').getAttribute('aria-pressed') === 'true') await target.locator('#reading-toggle').click();
        view = 'results';
      }
      await target.locator(`#page-manual .view-nav [data-manual-view="${view}"]`).click();
      await target.locator(`#page-manual[data-view="${view}"]`).waitFor();
    };
    await manualView(frame, 'setup');
    await frame.locator('#prompt').fill('Draft survives switching workspaces and refreshing completed results.');
    await manualView(frame, 'results');
    assert.equal(await frame.locator('#run-form').isVisible(), true, 'Current answers share the page with configuration');
    assert.equal(await frame.evaluate(() => document.activeElement.id), 'manual-results');
    await manualView(frame, 'history');
    assert.equal(await frame.locator('#manual-history').isVisible(), true);
    await manualView(frame, 'setup');
    assert.equal(await frame.locator('#prompt').inputValue(), 'Draft survives switching workspaces and refreshing completed results.');
    await frame.locator('#refresh').click();
    await frame.locator('#refresh:not([aria-busy])').waitFor();
    assert.equal(await frame.locator('#prompt').inputValue(), 'Draft survives switching workspaces and refreshing completed results.');
    await frame.locator('#reading-toggle').click();
    assert.equal(await frame.locator('#reading-toggle').getAttribute('aria-pressed'), 'true');
    assert.equal(await frame.locator('#run-form').isVisible(), false);
    assert.equal(await frame.evaluate(() => document.activeElement.id), 'reading-toggle');
    await frame.locator('#reading-toggle').click();
    assert.equal(await frame.locator('#reading-toggle').getAttribute('aria-pressed'), 'false');
    assert.equal(await frame.locator('#run-form').isVisible(), true);
    assert.equal(await frame.locator('#prompt').inputValue(), 'Draft survives switching workspaces and refreshing completed results.');
    check('Configuration stays visible above current answers and history; explicit expanded reading preserves button focus and restores drafts; refresh retains the editable draft separately from frozen execution');
    await frame.locator('#question').selectOption('logic-candy');
    await manualView(frame, 'results');
    assert.equal(await frame.evaluate(() => document.activeElement.id), 'manual-results');
    check('Answer navigation moves keyboard focus to results without discarding the form');
    await manualView(frame, 'setup');
    const sizes = [[1640, 870], [1440, 900], [1280, 800], [768, 900], [390, 844], [360, 800], [1280, 720]];
    for (const theme of ['light', 'white', 'dark']) {
      await setTheme(theme);
      await frame.locator(`html[data-theme="${theme}"]`).waitFor();
      for (const [width, height] of sizes) {
        await page.setViewportSize({ width, height });
        const states = width > 768 ? [false, true] : [false];
        for (const collapsed of states) {
          const shell = await sidebar(collapsed);
          await frame.evaluate(() => window.scrollTo(0, 0));
          const geometry = await frame.evaluate(() => ({ width: innerWidth, height: innerHeight, scrollWidth: document.documentElement.scrollWidth, header: document.querySelector('.app-header').getBoundingClientRect().toJSON(), main: document.querySelector('main').getBoundingClientRect().toJSON() }));
          const box = await page.locator('iframe').boundingBox();
          const toolbar = await page.locator('.floating-actions').boundingBox();
          const nav = await frame.locator('.primary-nav').boundingBox();
          assert(geometry.scrollWidth <= geometry.width + 1, `${theme} ${width} ${shell}: iframe horizontal overflow`);
          assert(box && toolbar && nav);
          if (width > 768) {
            const expectedWidth = await page.locator('.app-shell').evaluate((node, value) => innerWidth - parseFloat(getComputedStyle(node).getPropertyValue(value ? '--sidebar-collapsed-width' : '--sidebar-panel-width')), collapsed);
            assert(Math.abs(geometry.width - expectedWidth) <= 1, `${theme} ${width} ${shell}: iframe width must match settled sidebar geometry`);
          }

          assert(nav.y >= toolbar.y + toolbar.height + 4, `${theme} ${width} ${shell}: task navigation overlaps host floating tools`);
          report.matrix.push({ theme, viewport: { width, height }, sidebar: shell, iframe: { width: geometry.width, height: geometry.height }, toolbar_bottom: toolbar.y + toolbar.height, plugin_nav_top: nav.y, horizontal_overflow: geometry.scrollWidth - geometry.width });
          if (width <= 768) {
            const [credential, question, prompt, model, start] = await Promise.all(['target', 'question', 'prompt', 'models', 'run-submit'].map(id => frame.locator(`#${id}`).boundingBox()));
            if (width <= 600) {
              assert(Math.abs(credential.x - prompt.x) <= 1, 'Phone credential and prompt fields share the left edge');
              assert(question.x >= credential.x && (question.y >= credential.y + credential.height || question.x >= credential.x + credential.width), 'Phone question selection follows its credential in the responsive flow');
            } else {
              assert(Math.abs(credential.y - question.y) <= 1 && question.x > credential.x, 'Tablet credential selection stays immediately left of the question');
            }
            assert(prompt.y >= Math.max(credential.y + credential.height, question.y + question.height), 'Narrow screens place prompt editing below credential and question selection');
            assert(model.y >= prompt.y + prompt.height, 'Narrow screens place model options after the prompt');
            if (width <= 390) {
              assert(start.height >= 44 && Math.abs(start.width - prompt.width) <= 1, 'Phone starts with a full-width touch target');
              await frame.locator('#run-submit').scrollIntoViewIfNeeded();
              const reachableStart = await frame.locator('#run-submit').boundingBox();
              assert(reachableStart.y >= 0 && reachableStart.y + reachableStart.height <= height + 1, `${theme} ${width}×${height}: start action remains reachable in the natural phone flow`);
              await frame.evaluate(() => window.scrollTo(0, 0));
            }
          }
          if (width >= 1280) {
            const [credential, question, prompt, model, samples, start] = await Promise.all(['target', 'question', 'prompt', 'models', 'samples', 'run-submit'].map(id => frame.locator(`#${id}`).boundingBox()));
            assert(Math.abs(credential.y - question.y) <= 1 && question.x >= credential.x + credential.width, 'Desktop credential selection stays immediately left of the question');
            assert(Math.abs(prompt.x - credential.x) <= 1 && prompt.y >= question.y + question.height, 'Desktop prompt editing fills the left column below the selectors');
            assert(model.x >= prompt.x + prompt.width && start.x >= model.x && start.y >= samples.y + samples.height, 'Desktop model options and the Start action occupy the right column in order');
            assert(Math.abs(start.y + start.height - prompt.y - prompt.height) <= 1, 'Desktop Start bottom aligns with the prompt editor bottom');
            const fit = await frame.evaluate(() => {
              const exposedRect = node => {
                const rect = node.getBoundingClientRect();
                let top = Math.max(0, rect.top), bottom = Math.min(innerHeight, rect.bottom);
                let left = Math.max(0, rect.left), right = Math.min(innerWidth, rect.right);
                const clipping = [];
                for (let parent = node.parentElement; parent; parent = parent.parentElement) {
                  const style = getComputedStyle(parent), box = parent.getBoundingClientRect();
                  const clipX = /auto|scroll|hidden|clip/.test(style.overflowX);
                  const clipY = /auto|scroll|hidden|clip/.test(style.overflowY);
                  if (clipY) { top = Math.max(top, box.top + parent.clientTop); bottom = Math.min(bottom, box.top + parent.clientTop + parent.clientHeight); }
                  if (clipX) { left = Math.max(left, box.left + parent.clientLeft); right = Math.min(right, box.left + parent.clientLeft + parent.clientWidth); }
                  if (clipX || clipY) clipping.push(parent.id || parent.className || parent.tagName);
                }
                return { top: rect.top, bottom: rect.bottom, height: rect.height, width: rect.width, visible: !!node.getClientRects().length, visible_height: Math.max(0, bottom - top), visible_width: Math.max(0, right - left), clipping };
              };
              const answer = [...document.querySelectorAll('.answer-text')].find(node => node.getClientRects().length);
              return {
                width: innerWidth, height: innerHeight, document_height: document.documentElement.scrollHeight,
                page_scroll: scrollY,
                answer: answer ? { ...exposedRect(answer), text: answer.textContent } : null,
                controls: Object.fromEntries(['target', 'question', 'prompt', 'models', 'reasoning', 'samples', 'run-submit'].map(id => [id, exposedRect(document.getElementById(id))])),
              };
            });
            report.compact_geometry.push({ theme, viewport: { width, height }, sidebar: shell, ...fit });
            assert(fit.document_height <= fit.height + 2, `${theme} ${width}x${height} ${shell}: configuration requires document scrolling: ${JSON.stringify(fit)}`);
            for (const [id, rect] of Object.entries(fit.controls)) assert(rect.visible && rect.top >= 0 && rect.bottom <= fit.height + 1 && rect.visible_height >= rect.height - 1 && rect.visible_width >= rect.width - 1, `${theme} ${width}x${height} ${shell}: ${id} is outside the first viewport or clipped by an ancestor: ${JSON.stringify(fit)}`);
            assert(fit.answer && fit.answer.text.trim() && fit.answer.visible_height >= Math.max(40, fit.answer.height - 1) && fit.answer.visible_width >= fit.answer.width - 1, `${theme} ${width}x${height} ${shell}: an actual short answer must be readable together with configuration: ${JSON.stringify(fit)}`);
          }

          await capture(`after-embedded-${theme}-${width}x${height}-${shell}.png`, { role: 'after', theme, viewport: { width, height }, sidebar: shell, iframe: { width: geometry.width, height: geometry.height }, data: 'same native fake-host two-answer candy run' });
          await frame.locator('[data-page="operations"]').click();
          const overflow = await frame.evaluate(() => document.documentElement.scrollWidth - innerWidth);
          assert(overflow <= 1, `Operations overflow at ${width} ${shell}`);
          if (width === 1440 && !collapsed || width === 360) await capture(`after-operations-${theme}-${width}x${height}-${shell}.png`, { role: 'operations', theme, viewport: { width, height }, sidebar: shell, data: 'native fake-host empty attention and credential restrictions' });
          await frame.locator('[data-page="manual"]').click();
        }
      }
    }
    check('All three themes pass actual host iframe overflow and floating-toolbar clearance at 1640×870, 1440×900, 1280×800, 768×900, 390×844, 360×800 and user IAB viewport 1280×720; both desktop sidebar states');
    check('At 1640×870, 1440×900, 1280×800 and 1280×720, both desktop sidebar states show credential, question, prompt, model, reasoning, answer count, bottom-aligned Start and an actual short answer together in the first viewport without document scrolling');
    await page.setViewportSize({ width: 1280, height: 720 });
    await sidebar(false);
    await setTheme('light');
    const compactPrompt = await frame.locator('#prompt').inputValue();
    await frame.locator('#prompt').fill('Long custom draft '.repeat(800));
    const longPromptFit = await frame.evaluate(() => ({ height: innerHeight, document_height: document.documentElement.scrollHeight, prompt_scroll: document.getElementById('prompt').scrollHeight > document.getElementById('prompt').clientHeight, start_bottom: document.getElementById('run-submit').getBoundingClientRect().bottom }));
    assert(longPromptFit.prompt_scroll && longPromptFit.document_height <= longPromptFit.height + 2 && longPromptFit.start_bottom <= longPromptFit.height + 1, 'Long drafts stay within the prompt editor and retain the start action');
    await frame.locator('#question').selectOption('logic-candy');
    assert.equal(await frame.locator('#prompt').inputValue(), compactPrompt);
    const form = frame.locator('#run-form');
    assert.equal(await form.locator('#max-tokens, #temperature, .output-options').count(), 0);
    assert.doesNotMatch(await form.textContent(), /输出设置与说明|输出上限|temperature/i);
    const options = await frame.locator('.test-target').evaluate(node => ({
      fields: [...node.querySelectorAll('input, select')].map(field => field.id),
      overflow: getComputedStyle(node).overflowY,
      scrollHeight: node.scrollHeight,
      clientHeight: node.clientHeight,
    }));
    assert.deepEqual(options.fields, ['models', 'reasoning', 'samples']);
    assert.equal(options.overflow, 'visible');
    assert(options.scrollHeight <= options.clientHeight + 1, 'Options fit without a nested scroll area');
    await frame.locator('#target').focus();
    report.manual_focus_order = ['target'];
    for (const id of ['question', 'prompt', 'models', 'reasoning', 'samples', 'run-submit']) {
      await page.keyboard.press('Tab');
      const focused = await frame.evaluate(() => document.activeElement.id);
      assert.equal(focused, id);
      report.manual_focus_order.push(focused);
    }
    assert.equal(await frame.evaluate(() => scrollY), 0, 'Keyboard navigation keeps the desktop form in place');
    check('No output disclosure or overrides remain in the manual form; all three options fit without scrolling, and keyboard focus follows credential, question, prompt, model, reasoning, answer count and start');
    await manualView(frame, 'results');
    const answerFit = await frame.evaluate(() => ({ height: innerHeight, document_height: document.documentElement.scrollHeight, panel: document.getElementById('manual-results').getBoundingClientRect().toJSON() }));
    assert(answerFit.document_height <= answerFit.height + 2, 'The answer workspace scrolls locally on desktop');
    await capture('after-answers-light-1280x720.png', { role: 'answer-reading', theme: 'light', viewport: { width: 1280, height: 720 }, data: 'same native two-answer run; configuration, start action and answers visible together' });
    await frame.locator('#reading-toggle').click();
    assert.equal(await frame.locator('#manual-setup').isVisible(), false);
    await capture('after-expanded-reading-light-1280x720.png', { role: 'expanded-reading', theme: 'light', viewport: { width: 1280, height: 720 }, data: 'same native two-answer run, explicit user-selected expanded reading' });
    await frame.locator('#reading-toggle').click();
    assert.equal(await frame.locator('#manual-setup').isVisible(), true);
    await manualView(frame, 'history');
    assert(await frame.evaluate(() => document.documentElement.scrollHeight <= innerHeight + 2), 'History remains inside the desktop workspace');
    await manualView(frame, 'setup');
    check('Long drafts stay inside the prompt editor; same-page answers, explicit expanded reading and local history stay within the desktop viewport');

    await page.setViewportSize({ width: 1440, height: 900 });
    await sidebar(false);
    await setTheme('auto');
    await frame.locator('html[data-theme="white"][data-theme-source="host"]').waitFor();
    await page.emulateMedia({ colorScheme: 'dark' });
    await frame.locator('html[data-theme="dark"][data-theme-source="host"]').waitFor();
    await page.emulateMedia({ colorScheme: 'light' });
    await frame.locator('html[data-theme="white"][data-theme-source="host"]').waitFor();
    check('Actual CPA auto theme applies white in system light and dark in system dark; plugin follows both applied states');
    for (const theme of ['light', 'white', 'dark']) {
      for (const [width, height] of [[1440, 900], [1280, 720], [360, 800]]) {
        await page.setViewportSize({ width, height });
        await sidebar(false);
        await setTheme(theme);
        await navigate('/plugins');
        variant = 'baseline';
        const oldFrame = await navigate('/plugin-pages/model-quality/0');
        await connectPlugin(oldFrame);
        if (await oldFrame.locator('#theme-toggle').isVisible()) await oldFrame.locator('#theme-toggle').selectOption(theme);
        await oldFrame.locator('#samples').fill('2');
        await oldFrame.evaluate(() => window.scrollTo(0, 0));
        await oldFrame.locator('[data-manual-view="history"]').click();
        try {
          await oldFrame.locator('#page-manual[data-view="history"] #manual-history').waitFor({ state: 'visible', timeout: 5000 });
        } catch (error) {
          report.baseline_navigation_failure = { theme, width, height, errors: [...errors], ...(await oldFrame.evaluate(() => ({ view: document.getElementById('page-manual').dataset.view, build: document.getElementById('build-info').textContent, active: { tag: document.activeElement.tagName, id: document.activeElement.id }, text: document.body.innerText, sections: ['page-manual', 'manual-history', 'manual-setup', 'manual-results'].map(id => { const node = document.getElementById(id); return { id, classes: node.className, box: node.getBoundingClientRect().toJSON(), display: getComputedStyle(node).display }; }), workspace: document.querySelector('.answer-workspace').outerHTML, nav: document.querySelector('#page-manual .view-nav').outerHTML }))) };
          await capture('baseline-navigation-failure.png', { role: 'failure-diagnostic', theme, viewport: { width, height } });
          throw error;
        }
        await oldFrame.locator('#history-list [data-run]').first().click();
        await oldFrame.locator('#page-manual[data-view="results"]').waitFor();
        await oldFrame.locator('.sample:not(.sample-pending)').first().waitFor();
        await oldFrame.evaluate(() => window.scrollTo(0, 0));
        await capture(`before-embedded-${theme}-${width}x${height}-${width > 768 ? 'expanded' : 'mobile-closed'}.png`, { role: 'before', ui_version: baselineManifest.ui_version, theme, viewport: { width, height }, data: 'same native fake-host two-answer candy run; exact frozen baseline resources, original separate answer workspace' });
        await oldFrame.locator('#page-manual .view-nav [data-manual-view="setup"]').click();
        await oldFrame.evaluate(() => window.scrollTo(0, 0));
        await capture(`before-configuration-${theme}-${width}x${height}.png`, { role: 'before-configuration', ui_version: baselineManifest.ui_version, theme, viewport: { width, height }, data: 'same native fake-host two-answer candy run; baseline configuration hides the current answers' });
        await navigate('/plugins');
        variant = 'current';
        frame = await navigate('/plugin-pages/model-quality/0');
        await connectPlugin(frame);
        await frame.locator('#samples').selectOption('2');
        await manualView(frame, 'history');
        await frame.locator('#manual-history').waitFor({ state: 'visible' });
        await frame.locator('#history-list [data-run]').first().click();
        await frame.locator('#page-manual[data-view="results"]').waitFor();
        await frame.locator('.sample:not(.sample-pending)').first().waitFor();
        await manualView(frame, 'setup');
        await frame.evaluate(() => window.scrollTo(0, 0));
        await capture(`paired-after-embedded-${theme}-${width}x${height}.png`, { role: 'paired-after', ui_version: sourceVersion, theme, viewport: { width, height }, data: 'same native fake-host two-answer candy run and form configuration as paired baseline' });
      }
    }
    check(`Before screenshots use frozen baseline ${baselineManifest.ui_version} resources and the identical native task, theme, viewport and sidebar state used for after captures; both former separate workspaces are recorded`);
    await page.setViewportSize({ width: 1440, height: 900 });
    await sidebar(false);
    await setTheme('light');
    const zoomProfile = fs.mkdtempSync(path.join(os.tmpdir(), 'mq-host-browser-zoom-'));
    let zoomContext;
    try {
      zoomContext = await chromium.launchPersistentContext(zoomProfile, { headless: true, executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', viewport: null, locale: 'zh-CN', reducedMotion: 'reduce', args: ['--window-size=1440,900'] });
      const zoomPage = zoomContext.pages()[0];
      await zoomPage.goto('chrome://settings/appearance');
      await zoomPage.locator('#zoomLevel').selectOption('2');
      await zoomContext.route('**/*', routeRequest);
      await zoomPage.goto(`${state.host_url}/management.html#/login`);
      const zoomMetrics = await zoomPage.evaluate(() => ({ inner_width: innerWidth, inner_height: innerHeight, outer_width: outerWidth, device_pixel_ratio: devicePixelRatio, css_zoom: getComputedStyle(document.documentElement).zoom }));
      assert.equal(zoomMetrics.device_pixel_ratio, 2);
      assert.equal(zoomMetrics.inner_width * 2, zoomMetrics.outer_width);
      assert.equal(zoomMetrics.css_zoom, '1');
      report.browser_zoom = { percent: 200, method: 'Chrome settings appearance Page zoom in an isolated temporary persistent profile', ...zoomMetrics };
      await zoomPage.locator('input[name="cpa-management-key"]').fill(state.admin_key);
      await zoomPage.getByRole('button', { name: '登录', exact: true }).click();
      await zoomPage.locator('.main-header').waitFor();
      await zoomPage.locator('.mobile-menu-btn').click();
      await zoomPage.locator('a[href="#/plugin-pages/model-quality/0"]').click();
      await zoomPage.locator('iframe').waitFor();
      const zoomFrame = await zoomPage.locator('iframe').first().elementHandle().then(node => node.contentFrame());
      await zoomFrame.locator('#page-manual[data-view]').waitFor();
      await connectPlugin(zoomFrame);
      const zoomCDP = await zoomContext.newCDPSession(zoomPage);
      for (const theme of ['light', 'white', 'dark']) {
        await zoomPage.locator('.theme-menu > button').click();
        await zoomPage.getByRole('menuitemradio', { name: { light: '羊毛纸', white: '纯白', dark: '暗色' }[theme], exact: true }).click();
        await zoomFrame.locator(`html[data-theme="${theme}"]`).waitFor();
        await zoomFrame.evaluate(() => window.scrollTo(0, 0));
        const geometry = await zoomFrame.evaluate(() => ({ width: innerWidth, height: innerHeight, scrollWidth: document.documentElement.scrollWidth }));
        assert(geometry.scrollWidth <= geometry.width + 1);
        const toolbar = await zoomPage.locator('.floating-actions').boundingBox();
        const nav = await zoomFrame.locator('.primary-nav').boundingBox();
        assert(nav.y >= toolbar.y + toolbar.height + 4);
        const file = `after-embedded-${theme}-200percent-browser-zoom.png`;
        const shot = await zoomCDP.send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: false, fromSurface: true });
        const raster = Buffer.from(shot.data, 'base64');
        assert.equal(raster.readUInt32BE(16), zoomMetrics.outer_width, 'Zoom screenshot covers the full physical width');
        fs.writeFileSync(path.join(output, file), raster);
        report.screenshots.push({ file, role: 'browser-zoom', theme, zoom: 200, viewport: zoomMetrics, iframe: geometry });
        report.screenshot_sha256[file] = hash(fs.readFileSync(path.join(output, file)));
      }
      await zoomFrame.locator('#connection-top').click();
      const dialogFits = await zoomFrame.locator('#connection-dialog').evaluate(node => { const box = node.getBoundingClientRect(); return box.top >= 0 && box.bottom <= innerHeight + 1; });
      assert(dialogFits, 'Connection dialog stays within the actual browser-zoom viewport');
      await zoomFrame.locator('#connection-dialog').press('Escape');
      assert.equal(await zoomFrame.evaluate(() => document.activeElement.id), 'connection-top');
      check('Actual Chrome 200% browser zoom verified through outerWidth/innerWidth/devicePixelRatio with CSS zoom1: all themes retain iframe width, toolbar clearance, fitting connection dialog and Esc focus return');
    } finally {
      if (zoomContext) await zoomContext.close();
      fs.rmSync(zoomProfile, { recursive: true, force: true });
    }
    const fallbackPage = await context.newPage();
    for (const kind of ['unknown', 'unsupported']) {
      const parentOrigin = kind === 'cross' ? state.host_url.replace('127.0.0.1', 'localhost') : state.host_url;
      await fallbackPage.goto(`${parentOrigin}/${kind}-theme-parent`);
      const child = await fallbackPage.locator('iframe').elementHandle().then(node => node.contentFrame());
      await child.locator('html[data-theme-source="local"]').waitFor();
      assert(await child.locator('#theme-toggle').isVisible());
      await child.locator('#theme-toggle').selectOption('white');
      await child.locator('html[data-theme="white"]').waitFor();
      if (kind === 'unsupported') {
        await fallbackPage.evaluate(() => document.documentElement.setAttribute('data-theme', 'dark'));
        await child.locator('html[data-theme="dark"][data-theme-source="host"]').waitFor();
        assert.equal(await child.locator('#theme-toggle').isVisible(), false);
        await fallbackPage.evaluate(() => document.documentElement.setAttribute('data-theme', 'future-theme'));
        await child.locator('html[data-theme="white"][data-theme-source="local"]').waitFor();
      }
    }
    if (!preview) {
      const blocked = fallbackPage.waitForEvent('requestfailed', { predicate: request => request.url() === state.page_url && ['net::ERR_BLOCKED_BY_RESPONSE', 'net::ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS'].includes(request.failure()?.errorText) });
      await fallbackPage.goto(`${state.host_url.replace('127.0.0.1', 'localhost')}/cross-theme-parent`);
      const rejected = await blocked;
      report.cross_origin_rejection = rejected.failure().errorText;
      const policy = (await fetch(state.page_url)).headers.get('content-security-policy');
      assert(policy.includes("frame-ancestors 'self'"));
      report.native_frame_policy = policy;
      assert.equal(await fallbackPage.frameLocator('iframe').locator('html[data-theme-source]').count(), 0);
      check(`Cross-origin native embedding fails closed before plugin code runs (${report.cross_origin_rejection}); actual native response separately declares frame-ancestors self`);
    }
    await fallbackPage.close();
    check('Actual native resources safely fall back under unrecognized same-origin and unsupported applied themes; observer recovery preserves prior local choice');
    const callsAfter = (await (await fetch(`${state.upstream_url}/stats`)).json()).calls.length;
    assert.equal(callsAfter, 2, 'Theme/layout/history/refresh checks must not call models');
    check('Only the two explicitly submitted fake model requests occurred; theme, layout, navigation, refresh and history reads caused zero additional calls');
    assert.deepEqual(errors, [], 'No browser JavaScript errors');
    assert.deepEqual(Object.fromEntries(names.map(name => [name, hash(fs.readFileSync(path.join(root, 'plugin/ui', name)))])), sourceHashes, 'Sources remain stable during verification');
    report.unverified = ['Physical phone hardware and touch gestures', 'Screen-reader assistive technology'];
    report.result = preview ? 'preview-passed' : 'passed';
    report.finished_at = new Date().toISOString();
  } catch (error) {
    report.error = error.stack;
    throw error;
  } finally {
    if (browser) await browser.close();
    if (state) demo('stop');
    fs.writeFileSync(path.join(output, preview ? 'host-ui-preview.json' : 'host-ui-results.json'), JSON.stringify(report, null, 2) + '\n');
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
