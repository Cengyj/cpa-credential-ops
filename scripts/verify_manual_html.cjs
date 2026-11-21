// Drive real manual-answer interactions against authored, isolated model responses.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const sourceHashes = async () => Object.fromEntries(await Promise.all(['index.html', 'app.js', 'view.js', 'style.css', 'client.js', 'login.js'].map(async name => [name, createHash('sha256').update(await fs.readFile(path.join(__dirname, '../plugin/ui', name))).digest('hex')])));

const partialHTML = '<!doctype html><html><body><svg viewBox="0 0 240 160"><circle cx="120" cy="80" r="50" fill="#22604a"/></svg><!--' + 'Synthetic partial HTML. '.repeat(90);
const completeHTML = partialHTML + '--></body></html>';
const svgAnswer = '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 160"><rect id="svg-only-art" x="30" y="20" width="180" height="120" fill="#22604a"/></svg>';
const wideHTML = `<!doctype html><html><head><style>
html,body{margin:0;height:100%;overflow:hidden}body{font-family:system-ui}
.artwork-grid{position:relative;display:grid;grid-template-columns:1000px 1200px;width:2200px;min-height:2600px;background:linear-gradient(#e2f3eb,#a1cfc0)}
.artwork-frame{width:1000px;height:620px;border-radius:24px;overflow:hidden;background:#fff}
#content-end{position:absolute;right:24px;bottom:24px;padding:12px;background:#164d3b;color:white}
@keyframes fixture-pulse{from{opacity:.7}to{opacity:1}}.pulse{animation:fixture-pulse 2s linear infinite}
</style></head><body><main class="artwork-grid"><section class="artwork-frame"><svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 1000 620">
<defs><g id="local-piece"><rect width="160" height="120" rx="20" fill="#22604a"/><circle cx="80" cy="60" r="34" fill="#e8c35b"/></g></defs>
<use class="local-use pulse" href="#local-piece" x="70" y="100"/><use class="local-use" xlink:href="#local-piece" x="300" y="100"/>
<use class="local-use" href="#local-piece" x="530" y="100"/><use class="local-use" href="#local-piece" x="760" y="100"><animateTransform attributeName="transform" type="translate" values="0 0;0 8;0 0" dur="2s" repeatCount="indefinite"/></use>
</svg></section><aside>Wide synthetic canvas</aside><strong id="content-end">BOTTOM RIGHT END</strong></main></body></html>`;

function historySummary(run) {
  const first = run.samples.find(sample => sample.output);
  return { ...run, samples: run.samples.map(({ output, ...sample }) => sample), ...(first ? { answer_preview: { model: first.model, index: first.index,
    kind: /<(?:!doctype|html|svg)/i.test(first.output) ? 'html' : 'text', text: first.output.slice(0, 70) } } : {}) };
}
async function frameFor(locator) {
  await locator.waitFor({ state: 'visible' });
  await locator.evaluate(node => { if (!node.getAttribute('srcdoc')) throw new Error('Preview was not hydrated'); });
  const frame = await (await locator.elementHandle()).contentFrame();
  assert(frame); await frame.locator('body').waitFor(); return frame;
}
const geometry = frame => frame.evaluate(() => {
  const node = document.scrollingElement;
  return { left: node.scrollLeft, top: node.scrollTop, width: node.scrollWidth, height: node.scrollHeight, viewportWidth: node.clientWidth, viewportHeight: node.clientHeight };
});
async function wheelFrame(page, locator, frame, dx, dy) {
  await locator.scrollIntoViewIfNeeded(); const box = await locator.boundingBox(), viewport = page.viewportSize();
  assert(box && box.width > 0 && box.height > 0);
  await page.mouse.move(Math.max(5, Math.min(viewport.width - 5, box.x + box.width / 2)), Math.max(5, Math.min(viewport.height - 5, box.y + box.height / 2)));
  await page.mouse.wheel(dx, dy); await page.waitForTimeout(160); return geometry(frame);
}
async function wheelToEnd(page, locator, frame) {
  const before = await geometry(frame); let after = before;
  assert(before.width > before.viewportWidth && before.height > before.viewportHeight, 'Fixture must overflow in both directions');
  for (let n = 0; n < 5; n++) {
    after = await wheelFrame(page, locator, frame, 12000, 12000);
    if (after.left >= after.width - after.viewportWidth - 2 && after.top >= after.height - after.viewportHeight - 2) break;
  }
  assert(after.left > before.left && after.top > before.top, 'Actual wheel input must scroll both axes');
  assert(after.left >= after.width - after.viewportWidth - 2 && after.top >= after.height - after.viewportHeight - 2, 'Wheel must reach both ends');
  const marker = await frame.locator('#content-end').evaluate(node => { const r = node.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: innerWidth, height: innerHeight }; });
  assert(marker.left >= 0 && marker.top >= 0 && marker.right <= marker.width + 1 && marker.bottom <= marker.height + 1, 'The final content marker must actually be visible');
  return { before, after, marker };
}

async function main() {
  const initialSourceHashes = await sourceHashes();
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] }), exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const info = JSON.parse(line), { origin, hostname, port } = new URL(info.url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, reducedMotion: 'reduce' }), page = await context.newPage();
    page.setDefaultTimeout(6500);
    const out = process.env.UI_OUTPUT; if (out) await fs.mkdir(out, { recursive: true });
    const capture = name => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const prefix = '/v0/management/plugins/model-quality', posts = [], errors = [], results = [], previewRequests = [], previewAttempts = [], previewFailures = [], externalRequests = [], popups = [], runs = new Map();
    let run, detailReads = 0;
    const refresh = async () => { await page.click('#refresh'); await page.waitForSelector('#refresh:not([aria-busy=true])'); };
    const showHistory = () => page.locator('[data-manual-view=history]:visible,[data-manual-pane=history]:visible').first().click();
    const publish = samples => { run = { ...run, status: 'completed', samples }; runs.set(run.id, run); };
    const sample = (output, extra = {}) => ({ model: 'fixture-visual', index: 0, execution_status: 'completed', quality_verdict: 'unknown', reason: 'manual assessment required', output, ...extra });
    const card = index => page.locator('#sample-list .sample').filter({ has: page.locator(`[data-copy-sample="${index}"]`) });
    const inline = index => card(index).locator('[data-inline-preview]');
    await page.addInitScript(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async text => { window.__fixtureClipboard = text; } } }));
    page.on('pageerror', error => errors.push(error.message)); page.on('popup', popup => { popups.push(popup.url()); popup.close(); });
    context.on('request', request => { if (/^https?:/.test(request.url()) && request.frame().parentFrame()) previewAttempts.push(request.url()); });
    context.on('requestfailed', request => { if (request.frame().parentFrame()) previewFailures.push({ url: request.url(), error: request.failure()?.errorText }); });
    await context.route('**/*', async route => {
      const req = route.request(), url = new URL(req.url());
      if (url.origin !== origin) { externalRequests.push(req.url()); return route.abort(); }
      if (req.frame().parentFrame()) { previewRequests.push(req.url()); return route.abort(); }
      const api = url.pathname.slice(prefix.length);
      if (api === '/runs' && req.method() === 'POST') {
        const config = req.postDataJSON(); posts.push(config);
        run = { id: 'synthetic-html-run', source: 'manual_test', status: 'running', action_status: 'not_applicable', created_at: '2026-11-21T08:00:00Z',
          config: { ...config, parameters: { ...config.parameters, max_tokens: 2048 }, question: { id: config.question_id, title: '鹈鹕骑自行车（HTML）', kind: 'visual' } }, samples: [] };
        runs.set(run.id, run); return route.fulfill({ status: 202, json: { run_id: run.id } });
      }
      assert.equal(req.method(), 'GET', 'Only the synthetic manual run may be submitted');
      if (api === '/runs/detail') { detailReads++; const record = runs.get(url.searchParams.get('id')) || run; if (record) return route.fulfill({ json: { ...record, answer_preview: historySummary(record).answer_preview } }); }
      if (api === '/runs' && url.searchParams.get('source') === 'manual_test') return route.fulfill({ json: { items: [...runs.values()].map(historySummary), total: runs.size, offset: 0 } });
      return route.continue();
    });
    await page.goto(info.url); await page.click('#connection-open'); await page.fill('#management-key', info.management_key);
    await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
    await page.click('[data-page=manual]'); await page.selectOption('#target', 'fixture-quality-primary');
    await page.selectOption('#models', 'fixture-visual'); await page.selectOption('#question', 'visual-pelican-html');
    await page.selectOption('#samples', '1'); await page.click('#run-submit');
    await page.locator('#run-meta').getByText('执行中', { exact: true }).waitFor();
    assert.equal(posts.length, 1); assert.equal(posts[0].question_id, 'visual-pelican-html');
    assert.deepEqual(posts[0].parameters, { stream: true }); assert.deepEqual(posts[0].judge, { mode: 'manual' }); assert(!Object.hasOwn(posts[0], 'prompt'));
    assert.match(await page.locator('#sample-list').innerText(), /等待结果/); assert.equal(await page.locator('#sample-list iframe').count(), 0); assert(await page.locator('#run-cancel').isVisible());

    // Completion reveals the drawing without opening a modal.
    publish([sample(wideHTML)]); await refresh(); let frame = await frameFor(inline(0)); await frame.locator('.local-use').first().waitFor();
    assert(await card(0).locator('[data-source-sample]').isVisible()); assert.equal(await card(0).locator('[data-source-sample]').getAttribute('open'), null);
    assert(await card(0).locator('.answer-text').isHidden()); assert.equal(await card(0).locator('.answer-text').textContent(), wideHTML);
    const localUses = await frame.locator('.local-use').evaluateAll(nodes => nodes.map(node => { const b = node.getBBox(); return { href: node.getAttribute('href') || node.getAttribute('xlink:href'), width: b.width, height: b.height }; }));
    assert.equal(localUses.length, 4); assert(localUses.every(use => use.href === '#local-piece' && use.width > 0 && use.height > 0), 'All local SVG defs references must paint');
    assert.equal(await frame.locator('svg').getAttribute('viewBox'), '0 0 1000 620'); assert.equal(await frame.locator('animateTransform').count(), 1);
    assert.equal(await frame.locator('.pulse').evaluate(node => getComputedStyle(node).animationName), 'fixture-pulse');
    assert.equal(await frame.locator('.artwork-frame').evaluate(node => getComputedStyle(node).overflow), 'hidden');
    const desktopBounds = await inline(0).boundingBox(); assert(Math.abs(desktopBounds.width / desktopBounds.height - 4 / 3) < .04, 'The normal desktop artwork viewport should preserve its 4:3 canvas');
    await capture('inline-complete-desktop'); const desktopScroll = await wheelToEnd(page, inline(0), frame); await capture('inline-desktop-bottom-right');
    results.push({ id: 'immediate-inline-local-svg', localUses, scroll: desktopScroll });
    await card(0).locator('[data-source-sample] > summary').click(); assert(await card(0).locator('.answer-text').isVisible());
    await card(0).locator('[data-copy-sample]').click(); assert.equal(await page.evaluate(() => window.__fixtureClipboard), wideHTML);
    await card(0).locator('[data-source-sample] > summary').click();
    await card(0).locator('[data-preview-sample]').click(); await page.locator('#preview-dialog[open]').waitFor();
    let enlarged = await frameFor(page.locator('#safe-preview')); const enlargedScroll = await wheelToEnd(page, page.locator('#safe-preview'), enlarged); await capture('enlarged-bottom-right');
    await page.locator('[data-close=preview-dialog]').click(); await page.locator('#preview-dialog').waitFor({ state: 'hidden' });
    assert.equal(await page.locator('#safe-preview').getAttribute('srcdoc'), ''); assert.equal((await geometry(frame)).top, desktopScroll.after.top);
    results.push({ id: 'enlarged-scroll-and-close', scroll: enlargedScroll });

    // Refresh and automatic polling retain the frame document, scroll and open source.
    await wheelFrame(page, inline(0), frame, -12000, -12000); await wheelFrame(page, inline(0), frame, 360, 440);
    await card(0).locator('[data-source-sample] > summary').click(); await frame.evaluate(() => { window.__fixtureDocumentMarker = 'retained-document'; });
    await inline(0).evaluate(node => { window.__fixtureInlineElement = node; }); const retainedScroll = await geometry(frame);
    const assertRetained = async () => { assert(await inline(0).evaluate(node => node === window.__fixtureInlineElement)); assert.equal(await frame.evaluate(() => window.__fixtureDocumentMarker), 'retained-document'); const current = await geometry(frame); assert.deepEqual({ left: current.left, top: current.top }, { left: retainedScroll.left, top: retainedScroll.top }); assert.equal(await card(0).locator('[data-source-sample]').getAttribute('open'), ''); };
    await refresh(); await assertRetained();
    run = { ...run, status: 'running', config: { ...run.config, samples: 2 } }; runs.set(run.id, run); await refresh();
    const beforePoll = detailReads, polled = await page.waitForResponse(response => new URL(response.url()).pathname === prefix + '/runs/detail'); await polled.finished(); await page.waitForTimeout(100);
    assert(detailReads > beforePoll); await assertRetained();
    run = { ...run, status: 'completed', config: { ...run.config, samples: 1 } }; runs.set(run.id, run); await refresh();
    results.push({ id: 'refresh-and-poll-retain-document-scroll-and-source' });
    publish([sample(svgAnswer)]); await refresh(); frame = await frameFor(inline(0)); await frame.locator('#svg-only-art').waitFor();
    assert.equal(await frame.evaluate(() => window.__fixtureDocumentMarker), undefined); assert.equal(await frame.locator('.artwork-grid').count(), 0); assert.equal(await card(0).locator('.answer-text').textContent(), svgAnswer);
    const fenced = '```html\n' + completeHTML + '\n```'; publish([sample(fenced)]); await refresh(); frame = await frameFor(inline(0)); await frame.locator('svg circle').waitFor(); assert.equal(await card(0).locator('.answer-text').textContent(), fenced);

    const cases = [
      { id: 'legacy-limit', error: 'response limit exceeded', message: '响应数据超过采集上限，本次接收已停止。' },
      { id: 'transport-limit', error: 'response transport limit exceeded', message: '流式响应累计数据超过传输上限，本次接收已停止。' },
      { id: 'frame-limit', error: 'response frame limit exceeded', message: '单个响应帧超过采集上限，本次接收已停止。' },
      { id: 'answer-limit', error: 'response answer limit exceeded', message: '回答正文超过输出上限，本次接收已停止。' },
      { id: 'token-limit', execution_status: 'incomplete', reason: 'response output token limit reached', message: '回答达到输出 token 上限，未能完整生成。' },
      { id: 'unknown-truncation', execution_status: 'incomplete', reason: 'missing or truncated completion', message: '回答未完整结束或已截断，不作为答题质量失败证据' },
      { id: 'timeout', error: 'host.model.stream_read: context deadline exceeded', message: '请求超时，本次接收已停止。' },
      { id: 'cancelled', execution_status: 'cancelled', error: 'context canceled', message: '测试已取消' },
    ];
    for (const scenario of cases) {
      const current = sample(partialHTML, { execution_status: scenario.execution_status || 'failed', error: scenario.error, reason: scenario.reason }); publish([current]);
      if (scenario.id === 'cancelled') { run.status = 'cancelled'; runs.set(run.id, run); } await refresh();
      const reason = card(0).locator('.sample-reason'); await reason.filter({ hasText: scenario.message }).waitFor();
      if (scenario.id !== 'timeout') assert.doesNotMatch(await reason.innerText(), /超时/); if (scenario.id !== 'token-limit') assert.doesNotMatch(await reason.innerText(), /token 上限/);
      assert.match(await card(0).innerText(), /部分|不完整/); frame = await frameFor(inline(0)); await frame.locator('svg circle').waitFor();
      assert.equal(await card(0).locator('.answer-text').textContent(), partialHTML); assert.match(await page.locator('#history-list .history-excerpt').first().textContent(), /HTML 部分作品/);
      await card(0).locator('[data-preview-sample]').click(); await page.locator('#preview-dialog[open]').waitFor(); assert.equal(await page.locator('#preview-title').innerText(), '部分作品预览');
      assert.match(await page.locator('#preview-context').innerText(), /当前为已接收的部分内容，作品可能不完整/); await page.keyboard.press('Escape');
      await card(0).locator('[data-evidence-sample]').click(); await page.locator('#evidence-content').getByText(scenario.message, { exact: true }).waitFor(); await page.keyboard.press('Escape');
      if (scenario.id === 'legacy-limit') { await capture('partial-inline-answer'); const downloading = page.waitForEvent('download'); await page.click('#run-export'); const downloaded = await downloading, original = JSON.parse(await fs.readFile(await downloaded.path(), 'utf8')); assert.equal(original.samples[0].error, current.error); assert.equal(original.samples[0].output, current.output); }
      assert(await page.locator('#run-cancel').isHidden()); results.push({ id: scenario.id, execution_status: current.execution_status, partial: true, explanation: scenario.message });
    }
    for (const output of ['', '这是一份纯文本回答。', 'Explain <svg> and <html> tags as literal text.', '&lt;svg&gt;escaped source&lt;/svg&gt;']) {
      publish([sample(output, { execution_status: output ? 'completed' : 'empty' })]); await refresh(); assert.equal(await card(0).locator('[data-inline-preview],[data-preview-sample]').count(), 0);
      assert(await card(0).locator('.answer-text').isVisible()); assert.equal(await card(0).locator('.answer-text').textContent(), output || '本次没有返回回答正文。');
    }
    results.push({ id: 'plain-text-and-empty-stay-readable-without-preview' });

    run.config.samples = 3; publish([sample(completeHTML), sample('Plain second sample', { index: 1 }), sample(svgAnswer, { index: 2, execution_status: 'incomplete', reason: 'missing or truncated completion' })]); await refresh();
    assert.equal(await page.locator('#sample-list [data-inline-preview]').count(), 2); await (await frameFor(inline(0))).locator('circle').waitFor(); await (await frameFor(inline(2))).locator('#svg-only-art').waitFor();
    assert.equal(await card(1).locator('[data-inline-preview]').count(), 0); assert.equal(await card(1).locator('.answer-text').textContent(), 'Plain second sample');
    const originalID = run.id, historical = { ...run, id: 'synthetic-other-history', config: { ...run.config, samples: 1 }, samples: [sample('<svg viewBox="0 0 200 100"><text id="other-history-art" x="10" y="40">OTHER HISTORY</text></svg>')] };
    runs.set(historical.id, historical); await refresh(); await inline(0).evaluate(node => { window.__fixtureOldHistoryElement = node; }); await showHistory(); await page.locator(`[data-run="${historical.id}"]`).click();
    frame = await frameFor(inline(0)); await frame.locator('#other-history-art').waitFor(); assert(!(await inline(0).evaluate(node => node === window.__fixtureOldHistoryElement))); assert.equal(await page.locator('#sample-list [data-inline-preview]').count(), 1);
    await showHistory(); await page.locator(`[data-run="${originalID}"]`).click(); await (await frameFor(inline(2))).locator('#svg-only-art').waitFor(); results.push({ id: 'per-sample-and-history-artwork-identity' });
    await page.reload(); await page.waitForSelector('body.connected'); await page.click('[data-page=manual]'); await showHistory();
    await page.locator(`[data-run="${originalID}"]`).click(); await (await frameFor(inline(0))).locator('circle').waitFor(); await (await frameFor(inline(2))).locator('#svg-only-art').waitFor();
    assert.equal(await card(0).locator('.answer-text').textContent(), completeHTML); assert.equal(await card(1).locator('.answer-text').textContent(), 'Plain second sample');
    assert.equal(posts.length, 1); results.push({ id: 'page-reload-restores-history-without-new-model-request' });

    run.config.samples = 1; publish([sample(wideHTML)]); await refresh(); await page.setViewportSize({ width: 390, height: 844 }); frame = await frameFor(inline(0));
    const mobileScroll = await wheelToEnd(page, inline(0), frame); assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)); await capture('inline-mobile-bottom-right');
    await card(0).locator('[data-preview-sample]').click(); await page.locator('#preview-dialog[open]').waitFor(); enlarged = await frameFor(page.locator('#safe-preview'));
    const mobileEnlargedScroll = await wheelToEnd(page, page.locator('#safe-preview'), enlarged), bounds = await page.locator('#preview-dialog').boundingBox();
    assert(bounds.x >= 0 && bounds.y >= 0 && bounds.x + bounds.width <= 391 && bounds.y + bounds.height <= 845); await capture('enlarged-mobile-bottom-right');
    await page.keyboard.press('Escape'); await page.locator('#preview-dialog').waitFor({ state: 'hidden' }); results.push({ id: 'mobile-inline-and-enlarged-scroll', inline: mobileScroll, enlarged: mobileEnlargedScroll }); await page.setViewportSize({ width: 1440, height: 900 });

    const attack = `<style>@import url("${origin}/preview-import-leak");.paint{background:url("${origin}/preview-css-leak")}circle{opacity:.7}</style>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 240 160"><defs><circle id="safe-dot" cx="120" cy="80" r="50"/></defs><use id="safe-use" href="#safe-dot"/>
<use id="external-use" href="https://example.invalid/external.svg#shape"/><use id="bad-use" href="javascript:alert(1)"/><circle class="paint" cx="120" cy="80" r="10"><animate attributeName="opacity" values=".5;1;.5" dur="2s" repeatCount="indefinite"/></circle></svg>
<script>parent.document.body.dataset.compromised='yes';fetch('${origin}/preview-fetch-leak');window.open('https://example.invalid/popup')</script><img src="${origin}/preview-image-leak" onerror="parent.document.body.dataset.compromised='yes'">
<iframe src="${origin}/preview-frame-leak"></iframe><meta http-equiv="refresh" content="0;url=https://example.invalid/nav"><a href="#safe-dot">Local navigation<set attributeName="href" to="https://example.invalid/animated"/></a>
<a href="https://example.invalid/link">External link</a><form action="${origin}/preview-form-leak"><input value="secret"></form><button onclick="parent.document.body.dataset.compromised='yes'">Standalone button</button><input value="Standalone input"><template><a href="https://example.invalid/template-nav">Hidden link</a><img src="https://example.invalid/template-image"></template><div><template shadowrootmode="open"><a href="https://example.invalid/shadow-nav">Shadow link</a><img src="https://example.invalid/shadow-image"></template></div><noscript><a href="https://example.invalid/noscript-nav">No-script link</a><img src="https://example.invalid/noscript-image"></noscript>`;
    publish([sample(attack)]); await refresh();
    for (const modal of [false, true]) {
      if (modal) { await card(0).locator('[data-preview-sample]').click(); await page.locator('#preview-dialog[open]').waitFor(); }
      const locator = modal ? page.locator('#safe-preview') : inline(0), attackFrame = await frameFor(locator); await attackFrame.locator('#safe-use').waitFor();
      assert.equal(await locator.getAttribute('sandbox'), ''); assert.equal(await locator.getAttribute('inert'), null); assert.equal(await locator.getAttribute('referrerpolicy'), 'no-referrer');
      const csp = await attackFrame.locator('meta[http-equiv="Content-Security-Policy"]').getAttribute('content'); for (const rule of ["default-src 'none'", "script-src 'none'", "connect-src 'none'", "form-action 'none'", "base-uri 'none'"]) assert(csp.includes(rule));
      assert.equal(await attackFrame.locator('script,form,iframe,frame,object,embed,template,noscript,a,area,map,meta[http-equiv=refresh],set').count(), 0); assert.equal(await attackFrame.locator('[src],[srcset],[onclick],[onerror]').count(), 0);
      const refs = await attackFrame.locator('[href],[xlink\\:href]').evaluateAll(nodes => nodes.map(node => ({ tag: node.localName, ref: node.getAttribute('href') || node.getAttribute('xlink:href') }))); assert.deepEqual(refs, [{ tag: 'use', ref: '#safe-dot' }]);
      assert(await attackFrame.locator('#safe-use').evaluate(node => node.getBBox().width > 0 && node.getBBox().height > 0)); assert.equal(await attackFrame.locator('animate').count(), 1);
      assert.equal(await attackFrame.locator('button,input').evaluateAll(nodes => nodes.every(node => node.disabled)), true); assert.equal(await page.locator('body').getAttribute('data-compromised'), null); if (modal) await page.keyboard.press('Escape');
    }
    await page.waitForTimeout(150); assert.deepEqual(previewRequests, [], JSON.stringify({ previewAttempts, previewFailures, previewRequests }));
    assert(previewAttempts.every(url => previewFailures.some(failure => failure.url === url && /CSP|csp|blocked/i.test(failure.error || ''))), JSON.stringify({ previewAttempts, previewFailures })); assert.deepEqual(externalRequests, []); assert.deepEqual(popups, []);
    results.push({ id: 'sandbox-local-svg-only-css-smil-preserved', previewRequests, blockedAttempts: previewFailures, externalRequests, popups });
    const oversized = '<html><body>' + 'x'.repeat(1024 * 1024) + '</body></html>'; publish([sample(oversized)]); await refresh();
    assert.equal(await card(0).locator('[data-inline-preview]').count(), 0); assert.match(await card(0).innerText(), /1 MiB|预览.*上限/); assert.equal(await card(0).locator('.answer-text').textContent(), oversized); results.push({ id: 'oversized-preview-falls-back-with-exact-source' });
    assert.equal(posts.length, 1); assert.deepEqual(errors, []); assert.deepEqual(await sourceHashes(), initialSourceHashes, 'UI assets changed during verification');
    if (out) await fs.writeFile(path.join(out, 'manual-html.json'), JSON.stringify({ sourceHashes: initialSourceHashes, request: posts[0], results, detailReads, modelCalls: 0, credentialWrites: 0 }, null, 2) + '\n');
    console.log('Manual inline HTML/SVG, real wheel scrolling, mobile/modal, source, retention, partial states and network isolation passed.');
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
