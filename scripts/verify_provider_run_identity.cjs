// Compare frozen identities across real UI surfaces using authored, local-only records.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const os = require('node:os');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const assetNames = ['index.html', 'app.js', 'view.js', 'style.css', 'client.js', 'login.js'];
const sourceHashes = async () => Object.fromEntries(await Promise.all(assetNames.map(async name => [name,
  createHash('sha256').update(await fs.readFile(path.join(__dirname, '../plugin/ui', name))).digest('hex')])));

async function fixedBindingFixtures() {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'quality-history-test-'));
  const file = path.join(dir, 'runs.json');
  try {
    execFileSync('go', ['test', '-count=1', '-run', '^TestFixedMissingSubjectHistory$', './quality'], {
      cwd: path.join(__dirname, '../plugin'), env: { ...process.env, QUALITY_HISTORY_TEST_FIXTURE: file },
    });
    return JSON.parse(await fs.readFile(file, 'utf8'));
  } finally {
    await fs.rm(dir, { recursive: true, force: true });
  }
}

async function main() {
  const hashes = await sourceHashes();
  const fixedBindings = await fixedBindingFixtures();
  const summaries = new Map(fixedBindings.map(test => [test.detail.id, test.summary]));
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line), { origin, port, hostname } = new URL(url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, timezoneId: 'Asia/Shanghai', reducedMotion: 'reduce' });
    page.setDefaultTimeout(7000);
    const out = process.env.UI_OUTPUT;
    if (out) await fs.mkdir(out, { recursive: true });
    const served = {};
    for (const name of assetNames) {
      const response = await page.request.get(origin + '/v0/resource/plugins/model-quality/' + name);
      assert.equal(response.status(), 200);
      served[name] = createHash('sha256').update(await response.body()).digest('hex');
    }
    assert.deepEqual(served, hashes, 'The browser fixture must serve the exact product assets under test');
    const shot = name => out && page.screenshot({ path: path.join(out, name + '.png'), animations: 'disabled' });
    const prefix = '/v0/management/plugins/model-quality';
    const rule = { id: 'identity-rule', name: '冻结身份回归', revision: 3, enabled: true, timer_active: false,
      target_scope: 'all_credentials', object_types: ['provider', 'oauth'], action: 'observe_only',
      models: ['fixture-reasoner'], samples: 1, question_id: 'logic-candy', judge: { mode: 'deterministic' } };
    const config = { auth_id: 'provider-a', subject_auth_index: 'route-a', provider: 'openai', provider_name: 'Frozen provider',
      provider_observation: true, rule_id: rule.id, rule_revision: rule.revision, scope_batch_id: 'identity-batch',
      models: rule.models, samples: 1, question_id: rule.question_id,
      question: { id: rule.question_id, title: '冻结题目', kind: 'logic', prompt: 'Synthetic retained question.', answer: '21' },
      judge: rule.judge, parameters: { reasoning_effort: 'high', stream: true } };
    const sample = { model: 'fixture-reasoner', index: 0, execution_status: 'completed', quality_verdict: 'correct',
      answer_match: 'correct', output: '21', evidence: { auth_id: config.auth_id, path: 'provider_config_probe' } };
    const completed = { id: 'provider-completed', source: 'scheduled_probe', status: 'completed', created_at: '2026-11-21T02:30:00Z',
      completed_at: '2026-11-21T02:30:03Z', action_status: 'observe_only', config, samples: [sample] };
    const responseError = { ...completed, id: 'provider-response-error', samples: [
      { ...sample, error: 'upstream HTTP 502', output: '' },
    ] };
    const sampleCondition = { ...completed, id: 'provider-sample-condition', samples: [
      { ...sample, upstream_condition: 'unauthorized' },
    ] };
    const roundError = { ...completed, id: 'provider-round-error', error: 'Synthetic round error after the valid answer.' };
    const reason = '本轮供应商目录未核实，未发起模型请求。';
    const skipped = { ...completed, id: 'provider-skipped-named', status: 'skipped', samples: [],
      skip_detail: { auth_id: config.auth_id, email: '供应商 · Frozen provider · route-a', code: 'identity_unverified', reason } };
    const rows = [
      completed,
      skipped,
      { ...skipped, id: 'provider-skipped-legacy', config: { ...config, provider_name: '' } },
      { ...skipped, id: 'provider-skipped-unnamed', config: { ...config, provider_name: '' }, skip_detail: { ...skipped.skip_detail, email: '' } },
      { ...completed, id: 'provider-other-route', config: { ...config, auth_id: 'provider-b', subject_auth_index: 'route-b' },
        samples: [{ ...sample, evidence: { ...sample.evidence, auth_id: 'provider-b' } }] },
      ...[completed, skipped].map((row, index) => ({ ...row, id: index ? 'account-skipped' : 'account-completed', credential_email: 'frozen@example.test',
        config: { ...config, auth_id: 'account.json', subject_auth_index: 'account-index', provider_name: '', provider_observation: false,
          control_auth_id: 'provider-a', judge: { mode: 'llm', auth_id: 'provider-b', model: 'fixture-judge' } },
        ...(index ? { skip_detail: { ...skipped.skip_detail, auth_id: 'account.json', email: 'frozen@example.test' } }
          : { samples: [{ ...sample, evidence: { ...sample.evidence, auth_id: 'account.json', path: 'official-sdk' } }] }) })),
      responseError, sampleCondition, roundError,
    ];
    const manual = { ...completed, id: 'manual-frozen-provider', source: 'manual_test', action_status: 'not_applicable',
      config: { ...config, judge: { mode: 'manual' } } };
    const retained = JSON.stringify(rows), retainedManual = JSON.stringify(manual);
    const target = (auth_id, auth_index, extra = {}) => ({ auth_id, auth_index, provider: 'openai', is_provider: true,
      provider_name: 'CURRENT DIRECTORY NAME', type: 'config_api_key', object_type: 'provider',
      models: rule.models, runtime_status: 'active', effective_available: true, manual_disabled: false,
      quality_paused: false, retained_pause: false, reasoning: { 'fixture-reasoner': { levels: ['high'] } }, ...extra });
    let targets = [target('provider-a', 'route-a'), target('provider-b', 'route-b'),
      target('account.json', 'account-index', { is_provider: false, object_type: 'oauth', type: 'oauth', provider_name: '', email: 'current@example.test' })];
    const errors = [], writes = [], reads = [], exported = [], observations = [];
    let targetReads = 0;
    page.on('pageerror', error => errors.push(error.message));
    await page.route('**/*', async route => {
      const request = route.request(), u = new URL(request.url());
      assert.equal(u.origin, origin, 'The fixture must not contact any external service');
      const api = u.pathname.slice(prefix.length);
      if (request.method() === 'GET') {
        if (api === '/targets') { targetReads++; return route.fulfill({ json: { items: targets, total: targets.length, offset: 0 } }); }
        if (api === '/rules') return route.fulfill({ json: { items: [rule], total: 1, offset: 0 } });
        if (api === '/runs') {
          const available = u.searchParams.get('source') === 'scheduled_probe' ? rows : [manual];
          const offset = Number(u.searchParams.get('offset') || 0), limit = Number(u.searchParams.get('limit') || 20);
          const items = available.slice(offset, offset + limit).map(row => summaries.get(row.id) || ({ ...row,
            samples: row.samples.map(({ model, index, execution_status, quality_verdict, answer_match, evidence, error, upstream_condition }) => ({
              model, index, execution_status, quality_verdict, answer_match,
              ...(error ? { error } : {}),
              ...(upstream_condition ? { upstream_condition } : {}),
              ...(['provider_config_probe', 'provider_native_probe', 'cpa_management_probe'].includes(evidence?.path)
                ? { evidence: { path: evidence.path } } : {}),
            })),
            ...(row.source === 'manual_test' ? { answer_preview: { model: sample.model, index: 0, kind: 'text', text: '21' } } : {}) }));
          return route.fulfill({ json: { items, total: available.length, offset, limit } });
        }
        if (api === '/runs/detail') {
          const row = [...rows, manual].find(row => row.id === u.searchParams.get('id'));
          assert(row, 'Unexpected history detail request'); reads.push(row.id); return route.fulfill({ json: row });
        }
        return route.continue();
      }
      writes.push({ method: request.method(), api });
      throw new Error('Read-only history regression attempted a mutation: ' + request.method() + ' ' + api);
    });
    const el = selector => page.locator(selector), content = el('#quality-detail-content');
    const rowElement = id => el('#operations-history .quality-run-row').filter({ has: el('[data-run="' + id + '"]') });
    const expectedTitle = row => row.config.provider_observation
      ? '供应商路由 · ' + row.config.subject_auth_index : 'frozen@example.test';
    const download = async () => {
      const event = page.waitForEvent('download'); await el('#quality-detail-export').click();
      const file = await event, record = JSON.parse(await fs.readFile(await file.path(), 'utf8')); exported.push(record); return record;
    };
    const checkList = async phase => {
      assert.equal(await el('#operations-history .quality-run-row').count(), rows.length, 'One visible row remains per retained run');
      const rendered = [];
      for (const row of rows) {
        const element = rowElement(row.id), title = await element.locator('.quality-run-open bdi').innerText();
        const identity = await element.locator('.quality-identity-cell').innerText();
        const result = await element.locator('.quality-result-cell').innerText();
        assert.equal(title, expectedTitle(row), row.id + ' title');
        assert(identity.includes((row.status === 'skipped' ? '计划强度' : '请求强度') + ' high'), row.id + ' frozen effort meaning');
        assert.doesNotMatch(identity, /CURRENT DIRECTORY NAME|current@example\.test/);
        if (row.id === 'provider-skipped-unnamed') assert.doesNotMatch(identity, /Frozen provider/, 'A neighboring round cannot supply this round\'s missing name');
        if (row === responseError || row === sampleCondition) {
          assert.match(result, /执行失败 1/); assert.doesNotMatch(result, /答对|答错|未发起模型请求/);
          assert.equal(await element.locator('.history-score').count(), 0);
        } else if (row === roundError) {
          assert.match(result, /答对 1/); assert(result.includes(roundError.error));
          assert.equal(await element.locator('.history-score').count(), 0);
        } else if (row.status === 'completed') {
          assert.match(result, /1 \/ 1/); assert.doesNotMatch(result, /未执行|未发起模型请求/);
          assert.equal(await element.locator('.history-score[data-tone=good]').count(), 1);
        } else {
          assert.match(result, /未执行/); assert.match(result, /未发起模型请求/); assert(result.includes(reason));
          assert.equal(await element.locator('.history-score').count(), 0);
        }
        const handling = await element.locator('.quality-handling-cell').innerText();
        if (row.config.provider_observation && row.status === 'completed') assert.match(handling, /提供商配置定向探测/);
        rendered.push({ id: row.id, title, identity, result, handling });
      }
      for (const row of rows.slice(1, 4)) assert.equal(rendered.find(item => item.id === row.id).title, rendered[0].title, 'Same auth ID and index have the same operation title');
      assert.notEqual(rendered[0].title, rendered[4].title, 'A different frozen route stays distinct');
      observations.push({ phase, rows: rendered });
    };
    await page.goto(url); await el('#connection-open').click(); await el('#management-key').fill(management_key);
    await el('#connection-form button[type=submit]').click(); await el('body.connected').waitFor();
    await el('[data-page=operations]').click(); await el('#operations-history [data-run=provider-completed]').waitFor();
    await checkList('current-directory-renamed'); await shot('provider-identity-list');
    for (const row of rows) {
      await rowElement(row.id).locator('.quality-run-open').click(); await content.locator('.quality-detail-identity').waitFor();
      assert.equal(await content.locator('.quality-detail-identity > strong > bdi').innerText(), expectedTitle(row));
      assert.equal(await content.locator('.quality-detail-identity > code').innerText(), row.config.auth_id);
      const facts = await content.locator('.quality-detail-facts').innerText();
      if (row === responseError || row === sampleCondition) {
        const expectedResponse = row === responseError ? /上游 HTTP 502/ : /上游 HTTP 401/;
        assert.match(facts, /暂无判定/); assert.match(facts, /执行失败 1/); assert.match(facts, expectedResponse);
        assert.match(await content.locator('.quality-sample-status').innerText(), /执行失败/);
        await content.locator('[data-evidence-sample="0"]').click();
        const evidenceVerdict = await el('#evidence-content .evidence-verdict').innerText();
        assert.match(evidenceVerdict, /执行失败/); assert.match(evidenceVerdict, /无法判断/);
        assert.doesNotMatch(evidenceVerdict, /已完成|正确|错误/);
        assert.match(await el('#evidence-content').innerText(), expectedResponse);
        await el('[data-close=evidence-dialog]').click();
      } else if (row === roundError) {
        assert(facts.includes(roundError.error)); assert.match(facts, /正确/); assert.doesNotMatch(facts, /执行失败 1/);
        assert.equal(await content.locator('.quality-sample-status').innerText(), '正确');
        await content.locator('[data-evidence-sample="0"]').click();
        const sampleVerdict = await el('#evidence-content .evidence-verdict').innerText();
        assert.match(sampleVerdict, /正确/); assert.doesNotMatch(sampleVerdict, /执行失败|无法判断/);
        await el('[data-close=evidence-dialog]').click();
      } else if (row.status === 'completed') {
        assert.match(facts, /已完成/); assert.match(facts, /正确/); assert.doesNotMatch(facts, /未发起模型请求/);
        assert.equal(await content.locator('.sample').count(), 1);
        if (row.config.provider_observation) assert.match(await content.locator('.quality-detail-byline').innerText(), /提供商配置定向探测/);
      } else {
        assert.match(facts, /未执行/); assert(facts.includes(reason)); assert.equal(await content.locator('.sample').count(), 0);
        const header = await content.locator('.quality-detail-identity').innerText();
        assert.match(header, /范围调度准入/, 'The detail header identifies this round\'s actual admission check');
        assert.doesNotMatch(header, /CPA 原生路由|配置定向探测/, 'An unexecuted round must not claim a model execution path');
      }
      await content.locator('.quality-detail-conditions > summary').click();
      const conditions = content.locator('.condition-overview');
      const frozen = await conditions.innerText();
      assert.match(frozen, row.status === 'skipped' ? /计划思考强度[\s\S]*high/ : /请求思考强度[\s\S]*high/);
      assert.doesNotMatch(frozen, /CURRENT DIRECTORY NAME|current@example\.test/);
      if (row.config.control_auth_id) {
        const identities = content.getByText('检测身份与关联记录', { exact: true });
        await identities.click();
        const dependencies = await identities.locator('..').innerText();
        assert.match(dependencies, /对照凭证 ID\s+provider-a/); assert.match(dependencies, /判题凭证 ID\s+provider-b/);
        assert.doesNotMatch(dependencies, /CURRENT DIRECTORY NAME|current@example\.test/);
      }
      if (row.id === 'provider-skipped-unnamed') assert.doesNotMatch(frozen, /Frozen provider/);
      assert.deepEqual(await download(), row, 'Export preserves this exact round, including its missing fields');
      if (['provider-completed', 'provider-skipped-legacy', 'provider-skipped-unnamed'].includes(row.id)) await shot(row.id + '-detail');
      observations.push({ phase: 'detail', id: row.id, title: expectedTitle(row), facts, frozen });
      await page.keyboard.press('Escape');
    }
    targets = [];
    const previousReads = targetReads;
    await el('#refresh').click(); await el('#refresh:not([aria-busy=true])').waitFor();
    assert(targetReads > previousReads); await checkList('current-directory-empty');
    await page.setViewportSize({ width: 390, height: 844 }); await page.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
    await checkList('mobile'); await shot('provider-identity-mobile');
    await rowElement('provider-skipped-unnamed').locator('.quality-run-open').click();
    assert.equal(await content.locator('.quality-detail-identity > strong > bdi').innerText(), expectedTitle(rows[3]));
    assert(await el('#quality-detail').evaluate(node => node.scrollWidth <= node.clientWidth &&
      node.querySelector('.quality-detail-scroll').scrollWidth <= node.querySelector('.quality-detail-scroll').clientWidth));
    await shot('provider-identity-mobile-detail'); await page.keyboard.press('Escape');
    await page.setViewportSize({ width: 1440, height: 1000 });
    const collision = { ...completed, id: 'provider-index-collision', config: { ...config, auth_id: 'provider-c', provider_name: 'Other frozen provider' } };
    const otherAccount = { ...rows[5], id: 'account-email-collision', config: { ...rows[5].config, auth_id: 'other-account.json' } };
    rows.push(collision, otherAccount);
    await el('#refresh').click(); await el('#refresh:not([aria-busy=true])').waitFor();
    await checkList('ambiguous-index-and-email');
    for (const [id, authID] of [[completed.id, 'provider-a'], [collision.id, 'provider-c'], [rows[5].id, 'account.json'], [otherAccount.id, 'other-account.json']]) {
      const visibleID = rowElement(id).locator('.quality-run-auth-id');
      assert(await visibleID.isVisible(), id + ' distinct auth ID must remain visible');
      assert.equal(await visibleID.innerText(), id.startsWith('provider-') ? '路由 ID ' + authID : authID);
    }
    assert.equal(await rowElement('provider-other-route').locator('.quality-run-auth-id').count(), 0);
    await shot('ambiguous-identities'); rows.splice(-2);
    const originalRows = [...rows];
    const oldAmbiguous = { ...skipped, id: 'legacy-absent-untyped', config: { ...config, provider_name: '', provider_observation: false },
      skip_detail: { auth_id: config.auth_id, code: 'provider_directory_unavailable', reason: '合成旧目录读取失败。' } };
    const oldGroup = { ...oldAmbiguous, id: 'legacy-absent-group', skip_detail: { ...oldAmbiguous.skip_detail, code: 'group_missing' } };
    const older = [
      { scenario: 'legacy-unknown', detail: oldAmbiguous },
      { scenario: 'legacy-group', detail: oldGroup },
    ];
    const cases = [...fixedBindings, ...older];
    rows.splice(0, rows.length, ...cases.map(test => test.detail));
    await el('#refresh').click(); await el('#refresh:not([aria-busy=true])').waitFor();
    await rowElement(cases[0].detail.id).waitFor();
    assert.equal(await el('#operations-history .quality-run-row').count(), cases.length);
    for (const { scenario, detail } of cases) {
      let title, idLabel;
      if (scenario.startsWith('provider_group') || scenario === 'legacy-group') {
        title = '供应商配置组 · ' + detail.config.auth_id; idLabel = '配置组 ID';
      } else if (scenario.startsWith('provider')) {
        title = '供应商路由 · ' + detail.config.subject_auth_index; idLabel = '路由 ID';
      } else if (scenario === 'legacy-unknown') {
        title = '对象 · ' + detail.config.auth_id; idLabel = '对象 ID';
      } else {
        title = detail.config.auth_id; idLabel = '凭证 ID';
      }
      const row = rowElement(detail.id);
      assert.equal(await row.locator('.quality-run-open bdi').innerText(), title, scenario);
      if (scenario === 'provider-catalog') {
        assert.match(await row.innerText(), /CPA 供应商路由模型目录/);
        assert.match(await row.innerText(), /未请求上游/);
      }
      await row.locator('.quality-run-open').click();
      assert.equal(await content.locator('.quality-detail-identity > strong > bdi').innerText(), title);
      const facts = await content.locator('.quality-detail-facts').innerText();
      assert.doesNotMatch(facts, /CPA 凭证模型目录/);
      if (scenario === 'provider-catalog') assert.match(facts, /CPA 已核实该供应商路由没有注册所选模型/);
      else assert.match(facts, /未发起模型请求/);
      await content.locator('.quality-detail-conditions > summary').click();
      const identitySummary = content.getByText('检测身份与关联记录', { exact: true });
      await identitySummary.click();
      const identityFields = identitySummary.locator('..');
      assert.equal(await identityFields.getByText(idLabel, { exact: true }).locator('..').locator('dd').innerText(), detail.config.auth_id);
      assert.deepEqual(await download(), detail);
      observations.push({ phase: 'missing-binding', scenario, title, facts, id: detail.id });
      await page.keyboard.press('Escape');
    }
    await shot('fixed-missing-bindings');
    rows.splice(0, rows.length, ...originalRows);
    await el('#refresh').click(); await el('#refresh:not([aria-busy=true])').waitFor();
    await el('[data-page=manual]').click();
    await el('[data-manual-view=history]:visible,[data-manual-pane=history]:visible').first().click();
    const history = el('[data-history-id="' + manual.id + '"]');
    await history.waitFor();
    assert.equal(await history.locator('.history-credential').innerText(), '供应商 · Frozen provider · route-a');
    await history.locator('[data-run]').click();
    await el('#sample-list [data-evidence-sample="0"]').click(); await el('#evidence-dialog[open]').waitFor();
    const sampleEvidence = await el('#evidence-content').innerText();
    assert.match(sampleEvidence, /Frozen provider · route-a/); assert.doesNotMatch(sampleEvidence, /CURRENT DIRECTORY NAME/);
    await shot('manual-frozen-evidence'); await page.keyboard.press('Escape');
    assert.deepEqual(errors, []); assert.deepEqual(writes, []);
    assert.equal(JSON.stringify(rows), retained); assert.equal(JSON.stringify(manual), retainedManual);
    assert.deepEqual(await sourceHashes(), hashes, 'Product assets changed during this verification');
    if (out) {
      await fs.writeFile(path.join(out, 'fixture-served-sha256.json'), JSON.stringify(served, null, 2) + '\n');
      await fs.writeFile(path.join(out, 'provider-run-identity.json'), JSON.stringify({ hashes, observations, reads,
        exported, sampleEvidence, targetReads, pageErrors: errors, modelCalls: 0, credentialWrites: 0 }, null, 2) + '\n');
    }
    console.log('Verified frozen supplier route titles across executed and skipped rounds, independent outcomes, ordinary credentials, directory changes, original exports and sample evidence.');
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
