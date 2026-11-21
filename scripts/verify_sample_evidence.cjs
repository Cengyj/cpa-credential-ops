// Exercise retained answers and reviews through the same dialogs used by administrators.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const firstAnswer = '同序号的第一条回答：甲有 9 颗糖。';
const secondAnswer = '同序号的第二条回答：甲有 7 颗糖。';
const otherAnswer = '另一个模型的回答：甲有 9 颗糖。';
const firstDraft = '只属于第一条回答的草稿';
const secondDraft = '只属于第二条回答的草稿';
const oldRunID = 'fixture-run-observation';
const manualID = 'fixture-run-complete';
const nextID = 'fixture-run-visual';

function run(id, manual = false) {
  const sample = (model, output, response_model) => ({
    model, index: 1, output, response_model, execution_status: 'completed',
    quality_verdict: output === secondAnswer ? 'incorrect' : 'correct',
    evidence: { requested_model: model, auth_id: 'fixture-quality-primary' },
  });
  return {
    id, source: manual ? 'manual_test' : 'scheduled_probe', status: 'completed',
    action_status: manual ? 'not_applicable' : 'insufficient_evidence',
    credential_email: 'evidence@example.test', created_at: '2026-11-20T03:45:00Z',
    config: {
      models: ['model-a', 'model-b'], samples: 2, auth_id: 'fixture-quality-primary',
      rule_id: manual ? '' : 'fixture-rule-observe', judge: { mode: 'deterministic' },
      question: { id: 'custom', title: '糖果数量', prompt: '甲有多少颗糖？', answer: '9' },
    },
    ...(manual ? {} : { pruned_samples: 1 }),
    samples: [
      sample('model-a', firstAnswer, 'first-response'),
      sample('model-a', secondAnswer, 'second-response'),
      sample('model-b', otherAnswer, 'other-response'),
    ],
  };
}

function gate() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { resolve, promise };
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const closed = once(fixture, 'exit');
  const output = process.env.UI_OUTPUT;
  const baseline = process.env.BASELINE_UI;
  if (output) await fs.mkdir(output, { recursive: true });
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line);
    const { origin, port } = new URL(url);
    assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, permissions: ['clipboard-read', 'clipboard-write'], reducedMotion: 'reduce' });
    const page = await context.newPage();
    page.setDefaultTimeout(5000);
    const quality = run(oldRunID), manual = run(manualID, true);
    const knownSlots = run('fixture-run-observation-error');
    knownSlots.pruned_sample_slots = [{ model: 'model-b', index: 0 }];
    const reviews = [], mutations = [], errors = [];
    const result = { quality: {}, manual: {}, reviews, sizes: [] };
    let heldReview, heldDetail;
    page.on('pageerror', error => errors.push(error.message));
    await context.route('**/*', async route => {
      const req = route.request(), location = new URL(req.url());
      assert.equal(location.origin, origin);
      const pathname = location.pathname;
      if (req.method() !== 'GET') mutations.push(pathname);
      const name = pathname.split('/').pop() || 'index.html';
      if (baseline && require('../plugin/ui/public_assets.json').includes(name)) {
        return route.fulfill({ path: path.join(baseline, name) });
      }
      if (pathname.endsWith('/runs/review') && req.method() === 'POST') {
        const body = req.postDataJSON();
        const record = body.id === quality.id ? quality : manual;
        const entry = record.samples[body.sample_index - (record.pruned_samples || 0)];
        assert(entry);
        reviews.push({ ...body, output: entry.output });
        entry.review = body.verdict;
        entry.review_note = body.note;
        if (heldReview) {
          heldReview.entered.resolve();
          await heldReview.wait.promise;
        }
        return route.fulfill({ json: {} });
      }
      assert.equal(req.method(), 'GET', 'only synthetic review writes are allowed');
      if (pathname.endsWith('/runs/detail')) {
        const id = location.searchParams.get('id');
        if (heldDetail?.id === id) {
          heldDetail.entered.resolve();
          await heldDetail.wait.promise;
        }
        const record = [quality, manual, knownSlots].find(run => run.id === id);
        if (record) return route.fulfill({ json: record });
      }
      return route.continue();
    });
    await page.goto(url);
    await page.click('#login-connect');
    await page.fill('#management-key', management_key);
    await page.click('#connection-form button[type=submit]');
    await page.waitForSelector('body.connected');
    await page.click('[data-page=operations]');
    await page.click('#operations-history [data-run=' + oldRunID + ']');
    const answers = page.locator('#quality-detail .sample:has(.answer-text)');
    const sample = output => answers.filter({ has: page.locator('.answer-text', { hasText: output }) });
    await sample(firstAnswer).waitFor();
    result.quality.rendered = await answers.locator('.answer-text').allTextContents();
    result.quality.unavailable = await page.locator('#quality-detail .sample-pending').allInnerTexts();
    result.quality.keys = await answers.evaluateAll(nodes => nodes.map(node => node.dataset.key));
    const download = async selector => {
      const pending = page.waitForEvent('download');
      await page.click(selector);
      const file = await pending;
      return JSON.parse(await fs.readFile(await file.path(), 'utf8'));
    };
    const exported = await download('#quality-detail-export');
    assert.equal(exported.config.question.prompt, quality.config.question.prompt);
    assert.deepEqual(exported.samples.map(s => s.output), quality.samples.map(s => s.output));
    result.quality.exported = exported.samples.map(s => s.output);
    for (const [width, height] of [[1440, 900], [1280, 640], [390, 844], [320, 700]]) {
      await page.setViewportSize({ width, height });
      await page.locator('#quality-detail .model-heading').first().evaluate(node => node.scrollIntoView({ block: 'start' }));
      const dimensions = await page.locator('#quality-detail').evaluate(d => {
        const scroll = d.querySelector('.quality-detail-scroll');
        const root = document.documentElement;
        return { dialogWidth: d.offsetWidth, scrollWidth: scroll.scrollWidth, clientWidth: scroll.clientWidth, bodyOverflow: Math.max(0, root.scrollWidth - root.clientWidth) };
      });
      assert.equal(dimensions.bodyOverflow, 0);
      assert.equal(dimensions.scrollWidth, dimensions.clientWidth);
      result.sizes.push({ width, height, ...dimensions });
      if (output) {
        await page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))));
        await page.screenshot({ path: path.join(output, 'quality-' + width + '.png'), animations: 'disabled' });
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 });
    result.quality.copied = [];
    for (const item of await answers.all()) {
      const body = await item.locator('.answer-text').textContent();
      await item.locator('[data-copy-sample]').click();
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      assert.equal(copied, body);
      result.quality.copied.push(copied);
      await item.locator('[data-evidence-sample]').click();
      const stored = quality.samples.find(s => s.output === body);
      assert((await page.locator('#evidence-content').innerText()).includes(stored.response_model));
      await page.click('[data-close=evidence-dialog]');
    }
    await sample(firstAnswer).locator('.review-disclosure summary').click();
    await sample(firstAnswer).locator('input').fill(firstDraft);
    if (await sample(secondAnswer).count()) {
      await sample(secondAnswer).locator('.review-disclosure summary').click();
      await sample(secondAnswer).locator('input').fill(secondDraft);
    }
    // A save elsewhere in the same dialog triggers a detail read after concurrent count pruning.
    const other = sample(otherAnswer);
    await other.locator('.review-disclosure summary').click();
    await other.locator('select').selectOption('correct');
    await other.locator('input').fill('核对另一模型');
    quality.samples.shift();
    quality.pruned_samples++;
    await other.locator('[data-review-sample]').click();
    await page.waitForFunction(expected => Array.from(document.querySelectorAll('#quality-detail .answer-text')).every(n => n.textContent !== expected), firstAnswer);
    const second = sample(secondAnswer);
    result.quality.draftAfterTrim = await second.locator('input').inputValue();
    result.quality.openAfterTrim = await second.locator('.review-disclosure').getAttribute('open') !== null;
    if (output) {
      await second.locator('.answer-text').scrollIntoViewIfNeeded();
      await page.screenshot({ path: path.join(output, 'review-after-prune.png'), animations: 'disabled' });
    }
    if (!result.quality.openAfterTrim) await second.locator('.review-disclosure summary').click();
    await second.locator('select').selectOption('incorrect');
    await second.locator('[data-review-sample]').click();
    await page.waitForFunction(() => document.querySelectorAll('#quality-detail .review-disclosure summary')[0].textContent.includes('错误'));
    const afterReview = await download('#quality-detail-export');
    assert.equal(afterReview.samples[0].review_note, result.quality.draftAfterTrim);
    assert.equal(afterReview.samples[0].output, secondAnswer);
    await page.click('[data-close=quality-detail]');
    await page.click('#operations-history [data-run=' + knownSlots.id + ']');
    await page.locator('#quality-detail .quality-detail-identity').waitFor();
    result.quality.knownPositions = await page.locator('#quality-detail .sample-pending').evaluateAll(nodes => nodes.map(node => ({
      model: node.dataset.model, index: Number(node.dataset.sample), status: node.querySelector('.tag').textContent,
    })));
    if (output) {
      await page.locator('#quality-detail .model-heading').first().evaluate(node => node.scrollIntoView({ block: 'start' }));
      await page.screenshot({ path: path.join(output, 'known-pruning-1440.png'), animations: 'disabled' });
    }
    await page.click('[data-close=quality-detail]');
    await page.click('[data-page=manual]');
    await page.click('[data-manual-view=history]');
    await page.click('#history-list [data-run=' + manualID + ']');
    const manualAnswers = page.locator('#sample-list .sample:has(.answer-text)');
    await manualAnswers.first().waitFor();
    result.manual.rendered = await manualAnswers.locator('.answer-text').allTextContents();
    result.manual.unavailable = await page.locator('#sample-list .sample-pending').allInnerTexts();
    result.manual.copied = [];
    for (const item of await manualAnswers.all()) {
      const body = await item.locator('.answer-text').textContent();
      await item.locator('[data-copy-sample]').click();
      const copied = await page.evaluate(() => navigator.clipboard.readText());
      assert.equal(copied, body);
      result.manual.copied.push(copied);
    }
    const beforeReview = await download('#run-export');
    assert.deepEqual(beforeReview.samples.map(s => s.output), manual.samples.map(s => s.output));
    if (output) await page.screenshot({ path: path.join(output, 'manual-1440.png'), animations: 'disabled' });
    // A late acknowledgement for the old historical review must leave the newer selection intact.
    const target = manualAnswers.last();
    await target.locator('.review-disclosure summary').click();
    await target.locator('input').fill('旧记录备注');
    heldReview = { entered: gate(), wait: gate() };
    await target.locator('[data-review-sample]').click();
    await heldReview.entered.promise;
    await page.click('[data-manual-view=history]');
    heldDetail = { id: nextID, entered: gate(), wait: gate() };
    await page.click('#history-list [data-run=' + nextID + ']');
    await heldDetail.entered.promise;
    const ack = page.waitForResponse(response => response.url().endsWith('/runs/review'));
    heldReview.wait.resolve();
    await (await ack).finished();
    const settle = () => page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))));
    await settle();
    const later = page.waitForResponse(response => response.url().endsWith('/runs/detail?id=' + nextID));
    heldDetail.wait.resolve();
    await (await later).finished();
    await settle();
    result.manual.selectedAfterAck = await page.locator('#run-frozen-data .run-identifier').textContent();
    result.credentialWrites = mutations.filter(path => !path.endsWith('/runs/review')).length;
    assert.equal(result.credentialWrites, 0);
    assert.deepEqual(errors, []);
    if (output) await fs.writeFile(path.join(output, 'sample-evidence.json'), JSON.stringify(result, null, 2) + '\n');
    if (!baseline) {
      assert.deepEqual(result.quality.rendered, result.quality.exported);
      assert.deepEqual(result.manual.rendered, manual.samples.map(s => s.output));
      assert.deepEqual(result.quality.knownPositions, [
        { model: 'model-a', index: 0, status: '样本缺失' },
        { model: 'model-b', index: 0, status: '历史已裁剪' },
      ]);
      assert.equal(result.quality.draftAfterTrim, secondDraft);
      assert.equal(result.quality.openAfterTrim, true);
      assert.equal(result.manual.selectedAfterAck, '任务 ' + nextID);
      assert.deepEqual(reviews.map(({ id, sample_index, output }) => [id, sample_index, output]), [
        [oldRunID, 3, otherAnswer], [oldRunID, 2, secondAnswer], [manualID, 2, otherAnswer],
      ]);
    }
    console.log(JSON.stringify(result));
  } finally {
    await browser?.close();
    fixture.kill();
    await closed;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
