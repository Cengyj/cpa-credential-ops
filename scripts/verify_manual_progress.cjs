// Drive real UI events against an isolated fixture and synthetic run responses.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const excerpt = '甲有 9 颗糖，乙有 7 颗糖，丙有 5 颗糖。';
const answers = [excerpt + '\n\n' + '逐项验证数量和差值。'.repeat(24) + '仅在详情中出现的推理结尾。', '另一份回答给出了丙有 4 颗糖。'];
const scoredExamples = [
  { id: 'test-1', correct: 1, total: 2, preview: excerpt },
  { id: 'fixture-run-correct', correct: 1, total: 1, preview: '甲 9 颗、乙 7 颗、丙 5 颗。相邻各差 2 颗，总数为 21。' },
  { id: 'fixture-run-incorrect', correct: 0, total: 1, preview: '5 × 7 = 30，所以共有 30 支铅笔。' },
];

// A history read only returns sample metadata and one short preview. Outputs belong to the detail response.
function historySummary(run) {
  const { id, source, created_at, status, error, config, credential_email, pruned_samples } = run;
  const samples = run.samples.map(({ model, index, execution_status, quality_verdict, answer_match, review, error }) =>
    ({ model, index, execution_status, quality_verdict, answer_match, review, error }));
  const row = { id, source, created_at, status, error, credential_email, pruned_samples, samples,
    config: { models: config.models, samples: config.samples, auth_id: config.auth_id, question_id: config.question_id, question: { id: config.question?.id, title: config.question?.title } } };
  if (run.samples.length) row.answer_preview = { model: samples[0].model, index: samples[0].index, text: excerpt, truncated: true };
  return row;
}

async function test() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { fixture: name, pid, url, management_key } = JSON.parse(line);
    const { origin, hostname, port } = new URL(url);
    assert.equal(name, 'synthetic-plugin-ui'); assert.equal(pid, fixture.pid);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
    let posts = 0, reads = 0, run, acknowledge;
    const deleted = new Set();
    const detailReads = [];
    let holdHistory;
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), u = new URL(request.url());
      assert.equal(u.origin, origin);
      if (u.pathname.endsWith('/targets')) {
        const response = await (await route.fetch()).json();
        for (const target of response.items) {
          if (target.auth_id === 'fixture-quality-primary') target.email = 'current@example.test';
        }
        return route.fulfill({ json: response });
      }
      if (u.pathname.endsWith('/runs/detail')) detailReads.push(u.searchParams.get('id'));
      if (u.pathname.endsWith('/runs') && request.method() === 'DELETE') {
        deleted.add(request.postDataJSON().id);
        return route.fulfill({ json: { ok: true } });
      }
      if (u.pathname.endsWith('/runs') && request.method() === 'POST') {
        posts++;
        run = { id: 'test-' + posts, credential_email: 'saved@example.test', config: request.postDataJSON(), created_at: new Date().toISOString(), source: 'manual_test', status: 'queued', samples: [] };
        if (posts === 1) await new Promise(resolve => { acknowledge = resolve; });
        return route.fulfill({ status: 202, json: { run_id: run.id } });
      }
      if (u.pathname.endsWith('/runs/cancel') && request.method() === 'POST') {
        run.status = 'cancelling';
        return route.fulfill({ json: {} });
      }
      assert.equal(request.method(), 'GET');
      if (run && u.pathname.endsWith('/runs/detail') && u.searchParams.get('id') === run.id) {
        if (++reads === 1) return route.fulfill({ status: 503, json: { error: 'temporary offline failure' } });
        return route.fulfill({ json: { ...run, answer_preview: historySummary(run).answer_preview } });
      }
      if (u.pathname.endsWith('/runs') && u.searchParams.get('source') === 'manual_test') {
        assert.equal(u.searchParams.get('view'), 'summary');
        if (holdHistory) await holdHistory;
        const offset = Number(u.searchParams.get('offset'));
        const limit = Number(u.searchParams.get('limit'));
        u.searchParams.set('offset', '0');
        u.searchParams.set('limit', '100');
        const fullPage = await (await route.fetch({ url: u.href })).json();
        const rows = (run ? [historySummary(run), ...fullPage.items] : fullPage.items).filter(item => !deleted.has(item.id));
        assert(rows.every(row => row.samples.every(sample => !Object.hasOwn(sample, 'output'))));
        return route.fulfill({ json: { items: rows.slice(offset, offset + limit), total: rows.length, offset, limit } });
      }
      return route.continue();
    });
    await page.goto(origin);
    await page.click('#login-connect');
    await page.fill('#management-key', management_key);
    await page.click('#connection-form button[type=submit]');
    await page.waitForSelector('#account-import-open:enabled');
    await page.click('[data-page=manual]');
    await page.selectOption('#target', 'fixture-quality-primary');
    assert.equal(await page.locator('#target option:checked').textContent(), 'current@example.test');
    await page.selectOption('#models', 'fixture-reasoner');
    await page.locator('#reasoning').selectOption('max');
    await page.selectOption('#question', 'logic-candy');
    await page.fill('#samples', '2');
    const button = page.locator('#run-submit');
    const label = button.locator('#run-submit-label');
    const spinner = button.locator('#run-submit-spinner');
    const play = button.locator('#run-submit-play');
    const summary = page.locator('#manual-config-summary');
    const idleSize = await button.boundingBox();
    const checkBusy = async phase => {
      await summary.filter({ hasText: phase }).waitFor();
      assert.equal(await label.innerText(), '开始测试');
      assert.equal(await button.getAttribute('aria-busy'), 'true');
      assert(await button.isDisabled() && await spinner.isVisible() && await play.isHidden());
      const size = await button.boundingBox();
      assert.equal(size.width, idleSize.width);
      assert.equal(size.height, idleSize.height);
    };
    assert(await spinner.isHidden() && await play.isVisible());
    const submitted = page.waitForRequest(req => req.method() === 'POST' && req.url().endsWith('/runs'));
    await button.click();
    const body = (await submitted).postDataJSON();
    assert.equal(body.parameters.reasoning_effort, 'max');
    assert.equal(body.auth_id, 'fixture-quality-primary');
    await checkBusy('提交中');
    assert.notEqual(await spinner.evaluate(node => getComputedStyle(node).animationName), 'none');
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await spinner.evaluate(node => getComputedStyle(node).animationName), 'none');
    assert(await spinner.isVisible());
    await page.emulateMedia({ reducedMotion: 'no-preference' });
    await page.locator('#run-form').evaluate(node => node.requestSubmit());
    acknowledge();
    await checkBusy('排队中');
    await page.locator('#run-feedback').filter({ hasText: '自动重试查询' }).waitFor();
    run.status = 'running';
    await checkBusy('测试中');
    await page.click('[data-manual-view=history]');
    const activeRow = page.locator('tr[data-history-id=test-1]');
    assert(!/答错|答对/.test(await activeRow.innerText()));
    await page.click('[data-run=fixture-run-complete]');
    await page.waitForFunction(() => document.querySelector('#run-frozen-data').textContent.includes('fixture-run-complete'));
    const frozenIdentity = await page.locator('#run-frozen-data .answer-test-config').innerText();
    assert(frozenIdentity.includes('fixture-quality-primary'), 'A legacy run without a saved email retains its original credential ID');
    assert(!frozenIdentity.includes('current@example.test'), 'A present-day catalog label must not rename a saved run');
    assert(await button.isDisabled());
    assert(await page.locator('#run-rerun').isDisabled());
    await page.locator('#run-form').evaluate(node => node.requestSubmit());
    run.samples = answers.map((output, index) => ({
      model: 'fixture-reasoner', index, output, execution_status: 'completed', quality_verdict: 'unknown',
      answer_match: 'incorrect', ...(index === 0 ? { review: 'correct' } : {}),
    }));
    run.status = 'completed';
    await page.waitForSelector('#run-submit:enabled');
    assert.equal(posts, 1);
    assert((await page.locator('#run-frozen-data').textContent()).includes('fixture-run-complete'));
    await page.click('[data-manual-view=history]');
    const row = page.locator('tr[data-history-id=test-1]');
    const openDetail = row.locator('button[data-run=test-1]');
    await row.waitFor();
    assert(await openDetail.locator('.history-title').isVisible(), 'The question itself should open the full answer');
    assert.equal(await row.locator('button').count(), 2, 'Each record should have one detail action and one delete action');
    const description = await openDetail.evaluate(button => document.getElementById(button.getAttribute('aria-describedby'))?.textContent || '');
    assert.match(description, /saved@example\.test/);
    assert.match(description, /fixture-reasoner/);
    assert.match(await row.innerText(), /saved@example\.test/);
    assert.match(await row.innerText(), /fixture-reasoner/);
    assert(await row.locator('time').isVisible());
    assert(await page.locator('#task-indicator').isHidden());
    const outcome = row.locator('.history-outcome');
    await outcome.locator('.history-ratio').waitFor();
    for (const { id, correct, total, preview } of scoredExamples) {
      const record = page.locator(`tr[data-history-id="${id}"]`);
      const score = record.locator('.history-score');
      const incorrect = total - correct;
      assert.equal(await score.getAttribute('data-tone'), incorrect ? 'bad' : 'good');
      assert.equal((await score.locator('.history-ratio').innerText()).replace(/\s+/g, ''), `${correct}/${total}`);
      assert((await score.ariaSnapshot()).includes(`${total} 份回答中有 ${correct} 份答对`));
      const visibleScore = await record.locator('.history-outcome').evaluate(outcome => {
        const copy = outcome.cloneNode(true);
        copy.querySelectorAll('.sr-only').forEach(text => text.remove());
        return copy.textContent.replace(/\s+/g, '');
      });
      assert.equal(visibleScore, `${correct}/${total}`, 'The ratio is the only visible score text');
      assert.equal(await score.locator('svg[aria-hidden=true]').count(), 1);
      assert((await record.locator('.history-excerpt').innerText()).includes(preview));
    }
    const preview = row.locator('.history-excerpt');
    assert((await preview.innerText()).includes(excerpt));
    assert(!(await row.innerText()).includes('仅在详情中出现'));
    assert.equal(await page.locator('#history-list').getByText(/^(错误|正常|已完成)$/).count(), 0);
    const failed = page.locator('tr[data-history-id=fixture-run-error] .history-outcome');
    assert.equal(await page.locator('tr[data-history-id=fixture-run-error] .history-excerpt').count(), 0);
    assert.match(await failed.innerText(), /异常|失败|未取得|未返回|未获得/);
    assert.doesNotMatch(await failed.innerText(), /答错/);
    assert.equal((await failed.innerText()).match(/执行失败/g)?.length, 1, 'The run and its single failed sample should have one readable result');
    const unknown = page.locator('tr[data-history-id=fixture-run-complete] .history-outcome');
    assert.match(await unknown.innerText(), /未判定|待判定|待评审|尚未判定/);
    const html = page.locator('tr[data-history-id=fixture-run-visual] .history-excerpt');
    assert.match(await html.innerText(), /HTML/i);
    assert.equal(await html.locator('html, iframe, script').count(), 0);
    const pruned = page.locator('tr[data-history-id=fixture-run-history-1]');
    assert.match(await pruned.innerText(), /裁剪|已清理|未保留/);
    assert.match(await pruned.locator('.history-outcome').innerText(), /答对\s*1/);
    assert.doesNotMatch(await pruned.innerText(), /答错/);
    const partial = page.locator('tr[data-history-id=fixture-run-history-2] .history-outcome');
    assert.match(await partial.innerText(), /答对\s*1/);
    assert.match(await partial.innerText(), /未判定\s*1/);
    for (const record of [failed, unknown, pruned, partial]) {
      assert.equal(await record.locator('.history-score').count(), 0, 'A score requires a verdict for every planned answer');
    }
    const scoreOnly = page.locator('tr[data-history-id=fixture-run-history-3]');
    assert.equal((await scoreOnly.locator('.history-ratio').innerText()).replace(/\s+/g, ''), '1/1');
    assert.equal(await scoreOnly.locator('.history-excerpt').count(), 0, 'A retained verdict needs no placeholder excerpt');
    assert(detailReads.every(id => !id || ['test-1', 'fixture-run-complete'].includes(id)), 'History lists must not fetch details for unselected rows');
    const remove = page.locator('[data-delete-run=test-1]');
    const clear = page.locator('#history-clear-all');
    for (const [width, mode, theme] of [
      [1440, 'desktop', 'light'], [1440, 'desktop-dark', 'dark'], [1440, 'expanded', 'light'], [1064, 'compact-desktop', 'dark'],
      [390, 'mobile-light', 'light'], [390, 'mobile-dark', 'dark'], [320, 'mobile-compact-light', 'light'], [320, 'mobile', 'dark'],
    ]) {
      const mobile = width <= 390;
      await page.setViewportSize({ width, height: mobile ? 844 : 900 });
      if (mode === 'expanded') await page.click('#reading-toggle');
      assert.equal(await page.locator('#reading-toggle').getAttribute('aria-pressed'), String(mode === 'expanded'));
      if (mobile) await page.click('[data-manual-pane=history]');
      await page.selectOption('#theme-toggle', theme);
      const [rowBox, openBox, deleteBox, clearBox] = await Promise.all([row.boundingBox(), openDetail.boundingBox(), remove.boundingBox(), clear.boundingBox()]);
      assert(rowBox && openBox && deleteBox && clearBox);
      assert(await outcome.isVisible() && await preview.isVisible(), `Result and excerpt should remain visible at ${width}px`);
      const ordered = await page.locator('#history-list .history-excerpt').evaluateAll(excerpts => excerpts.every(excerpt => {
        const verdict = excerpt.parentElement.querySelector('.history-outcome');
        return verdict && excerpt.getBoundingClientRect().bottom <= verdict.getBoundingClientRect().top + 1;
      }));
      assert(ordered, `Each answer excerpt should appear above its verdict at ${width}px`);
      const fonts = await row.evaluate(node => ['.history-title', '.history-hour'].map(selector => {
        const style = getComputedStyle(node.querySelector(selector));
        return [style.fontWeight, style.fontSize];
      }));
      assert.deepEqual(fonts[0], fonts[1], 'The question and time use the same typography');
      assert.equal(fonts[0][0], '400', 'Question and time use regular weight in both themes');
      const separateActions = openBox.x + openBox.width <= deleteBox.x + 1 || openBox.y + openBox.height <= deleteBox.y + 1;
      assert(separateActions, `Detail and delete actions must not overlap at ${width}px`);
      for (const content of [outcome, preview]) {
        const bounds = await content.boundingBox();
        assert(bounds && bounds.width > 0 && bounds.x >= rowBox.x - 1 && bounds.x + bounds.width <= rowBox.x + rowBox.width + 1, `Result text must fit the record at ${width}px`);
      }
      if (mobile) assert(deleteBox.width >= 40 && deleteBox.height >= 40, 'The mobile delete action needs a usable touch target');
      assert(deleteBox.x + deleteBox.width <= width && clearBox.x + clearBox.width <= width);
      assert(await page.locator('#manual-history').evaluate(node => node.scrollWidth <= node.clientWidth + 1));
      const heading = page.locator('.history-table thead th').first();
      const narrowList = await page.locator('.answer-workspace').evaluate(node => node.clientWidth <= 639);
      if (narrowList) {
        const header = page.locator('.history-table thead');
        assert((await header.boundingBox())?.height <= 2 || await header.isHidden(), 'Narrow lists should not consume a desktop column heading row');
        assert(!await row.getByText('答题结果', { exact: true }).isVisible(), 'Narrow rows should not repeat the result heading');
        const nextTop = await row.evaluate(node => node.nextElementSibling?.getBoundingClientRect().top);
        assert(Math.abs(nextTop - (rowBox.y + rowBox.height)) <= 1, 'Narrow records should form a continuous list');
      } else {
        const before = await heading.boundingBox();
        assert(before && before.height > 15);
        const scrolled = await page.locator('#manual-history').evaluate(node => { node.scrollTop = 100; return node.scrollTop; });
        if (scrolled > 40) {
          const after = await heading.boundingBox();
          assert(after && Math.abs(before.y - after.y) < 24, 'Column headings must stay in view while the record list scrolls');
        }
        await page.locator('#manual-history').evaluate(node => { node.scrollTop = 0; });
      }
      if (process.env.UI_OUTPUT) {
        await page.mouse.move(0, 0);
        await page.locator('.answer-workspace').screenshot({ path: path.join(process.env.UI_OUTPUT, `manual-history-${mode}.png`), animations: 'disabled' });
      }
      await openDetail.focus();
      await page.keyboard.press('Tab');
      assert(await remove.evaluate(button => document.activeElement === button));
      if (mode === 'expanded') await page.click('#reading-toggle');
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    await html.click();
    const previewButton = page.locator('#sample-list [data-preview-sample]');
    await previewButton.click();
    await page.locator('#preview-dialog[open]').waitFor();
    assert.equal(await page.locator('#safe-preview').getAttribute('sandbox'), '');
    const drawing = page.frameLocator('#safe-preview').locator('svg');
    for (const width of [1440, 320]) {
      await page.setViewportSize({ width, height: 850 });
      await drawing.waitFor();
      const bounds = await drawing.boundingBox();
      assert(bounds.width > 120 && bounds.height > 60, `The saved SVG needs a visible drawing surface at ${width}px`);
    }
    await page.keyboard.press('Escape');
    assert(await page.locator('#preview-dialog').isHidden());
    assert(await previewButton.evaluate(button => document.activeElement === button));
    const reader = page.locator('#manual-results');
    const toHistory = page.locator('[data-manual-pane=history]');
    await toHistory.click();
    await page.click('[data-run=fixture-run-complete]');
    await page.locator('#sample-list .answer-text').filter({ hasText: '未知数' }).waitFor();
    const previousOffset = await reader.evaluate(node => { node.scrollTop = node.scrollHeight; return node.scrollTop; });
    assert(previousOffset > 100, 'The mobile fixture needs a scrollable first record');
    await toHistory.click();
    await page.click('[data-manual-pane=results]');
    assert.equal(await reader.evaluate(node => node.scrollTop), previousOffset, 'Switching tabs should keep the position within the same answer');
    await toHistory.click();
    await page.click('[data-run=test-1]');
    await page.locator('#sample-list .answer-text').filter({ hasText: answers[0] }).waitFor();
    assert.equal(await reader.evaluate(node => node.scrollTop), 0, 'Opening another saved answer should start at its question');
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.click('#history-return');
    for (const { id, preview } of scoredExamples.filter(example => example.total === 1)) {
      await page.click(`button[data-run="${id}"]`);
      const answer = page.locator('#sample-list .answer-text');
      await answer.filter({ hasText: preview }).waitFor();
      assert.equal(await answer.textContent(), preview);
      const card = await page.locator('.answer-workspace').boundingBox();
      const setup = await page.locator('#manual-setup').boundingBox();
      assert(Math.abs(card.y - setup.y) <= 1 && Math.abs(card.height - setup.height) <= 1, 'Both desktop panels align for a short answer');
      const textBox = await answer.boundingBox();
      const copy = await page.locator('#sample-list [data-copy-sample]').boundingBox();
      assert(copy.y - textBox.y - textBox.height < 100, 'Copy should stay beside the short response');
      assert(await page.getByRole('progressbar', { name: '回答完成进度' }).isHidden());
      await page.click('#history-return');
    }
    await preview.click();
    const correct = page.locator('#sample-list .sample-statuses[data-verdict="correct"]');
    const incorrect = page.locator('#sample-list .sample-statuses[data-verdict="incorrect"]');
    await correct.getByText('答案正确', { exact: true }).waitFor();
    await incorrect.getByText('答案错误', { exact: true }).waitFor();
    assert(await correct.locator('svg[aria-hidden=true]').isVisible());
    assert(await incorrect.locator('svg[aria-hidden=true]').isVisible());
    for (const theme of ['light', 'dark']) {
      await page.selectOption('#theme-toggle', theme);
      const rgb = node => getComputedStyle(node).color.match(/\d+/g).map(Number);
      const [good, bad] = await Promise.all([correct.getByText('答案正确').evaluate(rgb), incorrect.getByText('答案错误').evaluate(rgb)]);
      assert(good[1] > good[0] + 60, `Correct answers use a distinct green in ${theme}`);
      assert(bad[0] > bad[1] + 100 && bad[1] > bad[2], `Incorrect answers use a warm red in ${theme}`);
    }
    assert(await page.locator('#run-meta .answer-question').isVisible());
    assert(await page.locator('#run-meta time[datetime]').isVisible());
    assert.equal(await page.locator('#run-meta').getByText(/^(已完成|正确|错误|部分正确)$/).count(), 0);
    assert(await page.getByRole('progressbar', { name: '回答完成进度' }).isHidden());
    assert.deepEqual(await page.locator('#sample-list .answer-text').allTextContents(), answers);
    let releaseHistory;
    holdHistory = new Promise(done => { releaseHistory = done; });
    try {
      await page.click('#history-return');
      await preview.filter({ hasText: excerpt }).waitFor();
      assert.match(await outcome.locator('.history-ratio').innerText(), /1\s*\/\s*2/);
      assert(await openDetail.evaluate(button => document.activeElement === button), 'Returning to the list should restore keyboard focus to the record details');
    } finally {
      holdHistory = null;
      releaseHistory();
    }
    await openDetail.focus();
    await page.keyboard.press('Enter');
    await page.locator('#run-content').waitFor();
    const details = page.locator('#run-frozen');
    for (const width of [1440, 320]) {
      await page.setViewportSize({ width, height: 900 });
      if (width === 320) await page.click('[data-manual-pane=results]');
      await details.focus();
      const before = await details.boundingBox();
      const meta = await page.locator('#run-meta').boundingBox();
      await page.keyboard.press('Enter');
      await page.locator('#run-frozen-data').waitFor();
      assert((await page.locator('#run-frozen-data').textContent()).includes('saved@example.test'));
      assert.deepEqual(await details.boundingBox(), before);
      assert.deepEqual(await page.locator('#run-meta').boundingBox(), meta);
      const panel = await page.locator('#run-frozen-data').boundingBox();
      assert(panel.y >= meta.y + meta.height - 1 && panel.width > meta.width);
      await page.keyboard.press('Space');
      assert(await page.locator('#run-frozen-data').isHidden());
      assert(await details.evaluate(el => document.activeElement === el));
      assert.deepEqual(await details.boundingBox(), before);
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    holdHistory = new Promise(done => { releaseHistory = done; });
    try {
      await page.click('#run-rerun');
      await checkBusy('排队中');
      await page.click('#run-cancel');
      await checkBusy('停止中');
      run.status = 'cancelled';
      await page.waitForSelector('#run-submit:enabled', { timeout: 6000 });
    } finally {
      holdHistory = null;
      releaseHistory();
    }
    assert.equal(await label.innerText(), '开始测试');
    assert(await spinner.isHidden() && await play.isVisible());
    assert.equal(posts, 2);
    await page.click('[data-manual-view=history]');
    const detailBefore = detailReads.length;
    const trash = page.locator('[data-delete-run=test-2]');
    await trash.focus();
    await page.keyboard.press('Enter');
    await page.locator('#action-dialog[open]').waitFor();
    await page.click('#action-cancel');
    assert.equal(deleted.size, 0);
    await trash.click();
    await page.click('#action-confirm');
    await trash.waitFor({ state: 'detached' });
    await page.locator('#action-dialog').waitFor({ state: 'hidden' });
    assert(deleted.has('test-2'));
    assert.equal(detailReads.length, detailBefore);
    assert(await page.locator('#manual-history').isVisible());
    await page.click('[data-manual-view=results]');
    assert(await page.locator('#run-empty').isVisible());
    await page.click('[data-manual-view=history]');
    assert.equal(await trash.count(), 0);
    await page.click('#history-next');
    await page.locator('#history-page').filter({ hasText: '第 2 页' }).waitFor();
    const lastPageIds = await page.locator('#history-list [data-delete-run]').evaluateAll(nodes => nodes.map(node => node.dataset.deleteRun));
    const lastPageRow = page.locator(`tr[data-history-id="${lastPageIds[0]}"]`);
    const lastPageExcerpt = await lastPageRow.locator('.history-excerpt').innerText();
    await lastPageRow.locator('button[data-run]').click(); await page.locator('#sample-list .answer-text').first().waitFor();
    await page.click('#history-return');
    assert.equal(await page.locator('#history-page').textContent(), '第 2 页');
    assert.equal(await lastPageRow.locator('.history-excerpt').innerText(), lastPageExcerpt);
    for (const id of lastPageIds) {
      await page.click(`[data-delete-run="${id}"]`);
      await page.click('#action-confirm');
      await page.locator('#action-dialog').waitFor({ state: 'hidden' });
      assert(deleted.has(id));
    }
    assert.equal(await page.locator('#history-page').textContent(), '第 1 页');
    // The second call was never admitted. The first sample and the original reason survive both views.
    let verifiedBudgets = 0;
    for (const [error, message] of [
      ['budget_exhausted', '当时的调用预算不足，记录未区分具体上限。'],
      ['token_budget_exhausted', '当日 token 预算不足，未发起这次模型调用。'],
    ]) {
      run = { ...run, id: 'budget-' + ++verifiedBudgets, status: 'failed', error, config: { ...run.config, samples: 2 },
        samples: [{ model: run.config.models[0], index: 0, output: answers[0], execution_status: 'completed', quality_verdict: 'unknown', answer_match: 'correct' }] };
      await page.click('#refresh');
      const budgetRow = page.locator('tr[data-history-id="' + run.id + '"]');
      await budgetRow.getByText(message, { exact: true }).waitFor();
      assert.match(await budgetRow.innerText(), /答对 1/);
      assert.equal(await budgetRow.locator('.history-score').count(), 0);
      await budgetRow.locator('[data-run]').click();
      await page.locator('#run-meta').getByText(message, { exact: true }).waitFor();
      assert.equal(await page.locator('#sample-list .answer-text').first().textContent(), answers[0]);
      await page.click('#history-return');
    }
    assert.equal(posts, 2, 'Viewing a budget refusal must not resubmit either model call');
    await page.click('[data-manual-view=results]');
    const quantity = page.locator('#samples');
    for (const invalid of ['0','-1','1.5','9007199254740992','9007199254740990.1']) {
      await quantity.fill(invalid);
      assert(await button.isDisabled());
      assert.match(await page.locator('#samples-error').textContent(), /正整数/);
    }
    for (const count of [257, Number.MAX_SAFE_INTEGER]) {
      const before = posts;
      await quantity.fill(String(count));
      assert.equal(await quantity.getAttribute('aria-invalid'), null);
      const submit = page.waitForRequest(r => r.method() === 'POST' && r.url().endsWith('/runs'));
      await button.click();
      assert.equal((await submit).postDataJSON().samples, count);
      await checkBusy('排队中');
      run.samples = [{ model: run.config.models[0], index: count - 1, output: 'last synthetic answer', execution_status: 'completed', quality_verdict: 'unknown' }];
      run.status = 'completed';
      await page.locator('#run-submit:enabled').waitFor();
      const pages = page.locator('#sample-list .sample-pages');
      await pages.getByText(`样本 1–16 / ${count}`, {exact:true}).waitFor();
      assert.equal(await page.locator('#sample-list .sample').count(), 16);
      assert.equal(await page.locator('#sample-filter option').count(), 17);
      await pages.getByRole('button', {name:'下一页'}).click();
      await pages.getByText(`样本 17–32 / ${count}`, {exact:true}).waitFor();
      await pages.locator('[data-sample-ordinal]').fill(String(count));
      await pages.getByRole('button', {name:'跳转'}).click();
      await page.locator('#sample-list .answer-text').filter({hasText:'last synthetic answer'}).waitFor();
      assert(await pages.getByRole('button', {name:'下一页'}).isDisabled());
      assert.equal(posts, before + 1);
    }
    for (const width of [390, 320]) {
      await page.setViewportSize({width, height: 800});
      await page.click('[data-manual-pane=setup]');
      assert.equal(await quantity.inputValue(), String(Number.MAX_SAFE_INTEGER));
      assert.equal(await quantity.getAttribute('inputmode'), 'numeric');
      assert(await page.locator('#manual-setup').evaluate(n => n.scrollWidth <= n.clientWidth + 1));
      await page.click('[data-manual-pane=results]');
      assert(await page.locator('#sample-list .sample-pages').evaluate(n => n.scrollWidth <= n.clientWidth + 1));
      assert(await page.locator('#sample-list .answer-text').filter({hasText:'last synthetic answer'}).isVisible());
    }
    assert.deepEqual(errors, []);
    console.log('Manual history outcomes, bounded excerpts, full answer details, same-page return, responsive and keyboard controls, deletion, and complete run lifecycle passed.');
  } finally {
    await browser?.close();
    fixture.kill();
  }
}
test().catch(error => { console.error(error); process.exitCode = 1; });
