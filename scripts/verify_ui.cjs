// Exercise the embedded UI against a local fake management service.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const { chromium } = require(
  process.env.PLAYWRIGHT_MODULE ||
    '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright',
);
const root = path.resolve(__dirname, '..');
const output = process.env.UI_VERIFY_OUTPUT || path.join(root, 'artifacts/verification');
const uiSource = process.env.UI_SOURCE_DIR || path.join(root, 'plugin/ui');
const sourceHashes = () => Object.fromEntries(require('../plugin/ui/public_assets.json').map((name) => [name, require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(uiSource, name))).digest('hex')]));
const initialSourceHashes = sourceHashes();
fs.mkdirSync(output, { recursive: true });
const calls = [];
const robustnessChecks = [];
const managementStorageKey = 'model-quality:management-key:/v8/management/plugins/model-quality';
const browserStorage = (page) => page.evaluate(() => ({ local: Object.fromEntries(Object.entries(localStorage)), session: Object.fromEntries(Object.entries(sessionStorage)) }));
const deferred = () => {
  let resolve;
  const promise = new Promise((done) => {
    resolve = done;
  });
  return { promise, resolve };
};
const staticCSP = fs
  .readFileSync(path.join(root, 'plugin/cmd/model-quality/main.go'), 'utf8')
  .match(/"Content-Security-Policy": \{"([^"]+)"\}/)[1];
const models = ['mock-reasoner', 'mock-visual'];
const targets = [
  {
    auth_id: 'mock-credential-a',
    auth_index: 'mock-a',
    provider: 'openai',
    type: 'config_api_key',
    models,
    reasoning: {
      'mock-reasoner': { Min: 0, Max: 0, ZeroAllowed: true, DynamicAllowed: false, Levels: ['none', 'low', 'medium', 'high', 'max'] },
      'mock-visual': null,
    },
    identity_version: 'identity-a',
    manual_disabled: false,
    quality_paused: false,
    effective_available: true,
    can_pause: true,
    can_retest: false,
    reasons: [],
  },
  {
    auth_id: 'mock-credential-b',
    auth_index: 'mock-b',
    provider: 'openai',
    type: 'config_api_key',
    models,
    identity_version: 'identity-b',
    manual_disabled: false,
    quality_paused: false,
    effective_available: true,
    can_pause: true,
    can_retest: false,
    reasons: [],
  },
];
const questions = [
  {id:'custom',title:'自定义题目',kind:'custom',prompt:'',version:'custom'},
  {id:'logic-candy', title:'糖果逻辑题', kind:'logic', prompt:'有三个人，每人拿到不同数量的糖果。根据条件推导糖果总数，只输出整数。', version:'offline-v1'},
  {id:'visual-pelican-html', title:'鹈鹕自行车 HTML', kind:'html', prompt:'创建独立 HTML，用内联 SVG 绘制骑自行车的鹈鹕。', version:'offline-v1'},
  {
    id: 'random-linear',
    title: '可复现随机约束题',
    kind: 'random',
    prompt: '提交时生成随机题面',
    version: '1',
  },
  {
    id: 'logic',
    title: '有界组合推理',
    kind: 'logic',
    prompt: '在 1 到 9 中，所有偶数的和是多少？只输出整数。',
    version: '1',
  },
  {
    id: 'visual',
    title: '轨道与节奏',
    kind: 'html',
    prompt: '创建独立 HTML，用 SVG 绘制行星轨道，不使用外部资源。',
    version: '1',
  },
];
let rules = [];
let presets = [];
let actions = [];
let runs = [];
let sequence = 0;
let settings = {
  data_dir: 'mock-data',
  workers: 2,
  queue: 32,
  max_models: 8,
  max_samples: 32,
  max_output_tokens: 4096,
  max_response_bytes: 262144,
  daily_calls: 200,
  daily_tokens: 1000000,
  retention_days: 30,
  max_storage_bytes: 134217728,
  scheduling_enabled: false,
  automatic_pause: false,
};
const created = '2026-11-13T13:00:00Z';
const list = (array, url) => {
  const offset = Number(url.searchParams.get('offset') || 0),
    limit = Number(url.searchParams.get('limit') || 50);
  return { items: array.slice(offset, offset + limit), total: array.length, offset, limit };
};
const config = {
  auth_id: 'mock-credential-a',
  provider: 'openai',
  models: ['mock-reasoner'],
  question_id: 'logic-candy',
  samples: 1,
  parameters: { max_tokens: 2048, temperature: 0, stream: true },
  judge: { mode: 'deterministic' },
};
const pausedRuleFixture = () => ({...config,id:'rule-1',name:'核心模型巡检',status:'awaiting_release',enabled:false,action:'pause_credential',automatic_pause:true,scheduling_enabled:false,fail_count:3,fail_threshold:3,action_id:'pause-1',blocked_auth_id:targets[0].auth_id,revision:1});


const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (!url.pathname.startsWith('/v8/management/')) {
    const asset = url.pathname.split('/').pop() || 'index.html';
    if (!require('../plugin/ui/public_assets.json').includes(asset)) {
      res.writeHead(404).end();
      return;
    }
    res.setHeader('Content-Security-Policy', staticCSP);
    res.setHeader(
      'Content-Type',
      asset.endsWith('.js') ? 'text/javascript' : asset.endsWith('.css') ? 'text/css' : 'text/html',
    );
    res.end(fs.readFileSync(path.join(uiSource, asset)));
    return;
  }
  let body = '';
  for await (const chunk of req) body += chunk;
  const parsed = body ? JSON.parse(body) : {};
  calls.push({
    method: req.method,
    path: url.pathname,
    query: url.search,
    body: parsed,
    authorization: req.headers.authorization,
  });
  if (!['Bearer offline-test-key', 'Bearer offline-test-key-next'].includes(req.headers.authorization)) {
    res.writeHead(401, { 'Content-Type': 'application/json' }).end('{"error":"unauthorized"}');
    return;
  }
  const apiPath = url.pathname.replace('/v8/management/plugins/model-quality', '');
  let result = {},
    status = 200;
  switch (`${req.method} ${apiPath}`) {
    case 'GET /targets':
      result = list(targets, url);
      break;
    case 'GET /questions':
      result = list(questions, url);
      break;
    case 'GET /presets':
      result = list(presets, url);
      break;
    case 'POST /presets':
      presets.push({ ...parsed, id: 'preset-1', created_at: created });
      result = presets.at(-1);
      break;
    case 'DELETE /presets':
      presets = presets.filter((preset) => preset.id !== parsed.id);
      result = { ok: true };
      break;
    case 'GET /history':
      result = list(
        [
          {
            id: 'audit-1',
            kind: 'rule_saved',
            time: created,
            rule_id: 'rule-1',
            detail: '规则配置已保存，未启动模型调用',
          },
        ],
        url,
      );
      break;
    case 'GET /rules':
      result = list(rules, url);
      break;
    case 'GET /actions':
      result = list(actions, url);
      break;
    case 'GET /runs':
      result = list(runs, url);
      break;
    case 'GET /runs/detail':
      result = runs.find((run) => run.id === url.searchParams.get('id'));
      break;
    case 'GET /metrics':
      result = {
        correct: 6,
        incorrect: 1,
        unknown: 1,
        quality_pass_rate: 6 / 7,
        coverage: 7 / 8,
        known_tokens: 1893,
        unknown_usage: 1,
        dropped_usage_events: 0,
      };
      break;
    case 'GET /settings':
      result = settings;
      break;
    case 'PUT /settings':
      settings = parsed;
      result = settings;
      break;
    case 'POST /runs': {
      if (
        [parsed.auth_id, parsed.control_auth_id, parsed.judge?.auth_id].some(
          (id) => targets.find((target) => target.auth_id === id)?.quality_paused,
        )
      ) {
        status = 409;
        result = { error: { code: 'blocked', message: 'credential quality pause requires manual release' } };
        break;
      }
      const id = `run-${++sequence}`;
      const sample = {
        model: parsed.models[0],
        index: 0,
        execution_status: 'completed',
        quality_verdict: 'unknown',
        output: '21',
        total_ms: 630,
        ttft_ms: 145,
        usage: { input_tokens: 41, output_tokens: 2, total_tokens: 43 },
        comparable: true,
        response_model: 'mock-reasoner',
        reason: 'manual assessment required',
        evidence: {
          verified: true, requested_model: parsed.models[0], resolved_model: 'mock-upstream-model',
          auth_id: parsed.auth_id, payload_fingerprint: 'offline-payload-fingerprint',
          conditions_changed: false, path: 'openai/chat/completions', payload_config_version: 'offline-v1',
        },
      };
      runs.unshift({
        id,
        status: 'completed',
        source: 'manual_test',
        created_at: created,
        completed_at: created,
        config: { ...parsed, identity_version: 'identity-a', fingerprint: 'frozen', question: parsed.question_id === 'custom' ? {id:'custom',prompt:parsed.prompt,version:'custom'} : questions.find(q => q.id === parsed.question_id) },
        samples: Array.from({length: parsed.samples || 1}, (_, index) => ({...sample, index})),
        action_status: 'not_applicable',
      });
      result = { run_id: id };
      status = 202;
      break;
    }
    case 'POST /runs/cancel':
      runs.find((run) => run.id === parsed.id).status = 'cancelled';
      result = { ok: true };
      break;
    case 'POST /runs/review':
      result = { ok: true };
      break;
    case 'POST /rules':
      rules.push({
        ...parsed,
        id: 'rule-1',
        revision: 1,
        fail_count: 0,
        status: parsed.enabled ? 'detecting' : 'paused',
      });
      result = rules.at(-1);
      break;
    case 'PUT /rules':
      rules = rules.map((rule) =>
        rule.id === parsed.id ? { ...parsed, revision: parsed.revision + 1 } : rule,
      );
      result = rules[0];
      break;
    case 'DELETE /rules':
      rules = rules.filter((rule) => rule.id !== parsed.id);
      result = { ok: true };
      break;
    case 'POST /rules/pause': {
      const rule = rules.find((entry) => entry.id === parsed.id);
      rule.enabled = !parsed.paused;
      rule.status = rule.enabled ? 'detecting' : 'paused';
      rule.revision++;
      result = { ok: true };
      break;
    }
    case 'POST /actions/preview':
      result = {
        can_release: true,
        action: actions[0],
        remaining_reasons: ['manual_disabled'],
        scope: 'all_models',
      };
      break;
    case 'POST /actions/release':
      assert.equal(parsed.pause_rule, true);
      actions = [];
      targets[0].quality_paused = false;
      targets[0].reasons = targets[0].reasons.filter((reason) => reason !== 'quality_pause');
      rules[0].enabled = false;
      rules[0].status = 'paused';
      rules[0].action_id = '';
      rules[0].blocked_auth_id = '';
      result = { ok: true };
      break;
    default:
      status = 404;
      result = { error: `unsupported mock ${req.method} ${apiPath}` };
  }
  res.writeHead(status, { 'Content-Type': 'application/json' }).end(JSON.stringify(result));
});

(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const browser = await chromium.launch({headless:true, executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
  const page = await browser.newPage({
    viewport: { width: 1440, height: 900 },
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
  });
  const producedScreenshots = new Set();
  const takeScreenshot = async options => { await page.screenshot(options); producedScreenshots.add(path.basename(options.path)); };
  const errors = [];
  const viewportEvidence = [];
  const captureFixtures = {};
  const dialogGeometry = [];
  const historyGeometry = [];
  const headerGeometry = [];
  const captureSurfaceThemes = async (surface, targetSelector, dialog = false) => {
    const previousViewport = page.viewportSize();
    const previousTheme = await page.locator('html').getAttribute('data-theme');
    const target = page.locator(targetSelector);
    const previousScroll = await target.evaluate(node => node.scrollTop);
    for (const theme of ['light', 'white', 'dark']) {
      await page.evaluate(value => { document.documentElement.dataset.theme = value; }, theme);
      for (const [width, height] of [[1280, 800], [390, 844]]) {
        await page.setViewportSize({ width, height });
        await page.evaluate(async () => {
          scrollTo(0, 0);
          await document.fonts.ready;
          await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        });
        await target.evaluate(node => { node.scrollTop = 0; });
        assert(await target.isVisible(), `${surface} is visible for ${theme} ${width}px capture`);
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), `${surface} fits ${theme} ${width}px without page overflow`);
        if (dialog) {
          const box = await target.boundingBox();
          assert(box.x >= 0 && box.x + box.width <= width + 1 && box.y >= 0 && box.y + box.height <= height + 1, `${surface} dialog fits the ${width}×${height} viewport`);
        }
        await takeScreenshot({ path: path.join(output, `surface-${surface}-${theme}-${width}.png`), animations: 'disabled', fullPage: !dialog });
      }
    }
    await page.setViewportSize(previousViewport);
    await page.evaluate(value => { document.documentElement.dataset.theme = value || 'light'; }, previousTheme);
    await target.evaluate((node, top) => { node.scrollTop = top; }, previousScroll);
  };
  const verifyDialogPadding = async (id) => {
    await captureSurfaceThemes(id, `#${id}`, true);
    const previousViewport = page.viewportSize();
    const previousScroll = await page.locator(`#${id}`).evaluate((node) => node.scrollTop);
    for (const width of [360, 390]) {
      await page.setViewportSize({ width, height: 844 });
      const geometry = await page.locator(`#${id}`).evaluate(async (node) => {
        await document.fonts.ready;
        await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        node.scrollTop = node.scrollHeight;
        const rect = node.getBoundingClientRect();
        const last = node.lastElementChild.getBoundingClientRect();
        return { paddingBottom: parseFloat(getComputedStyle(node).paddingBottom), bottomInset: rect.bottom - last.bottom, dialogBottom: rect.bottom, lastContentBottom: last.bottom };
      });
      assert(geometry.paddingBottom >= 16, `${id} retains visible inner spacing at ${width}px`);
      assert(geometry.bottomInset >= 15.9, `${id} content remains inset from its bottom edge at ${width}px: ${JSON.stringify(geometry)}`);
      dialogGeometry.push({ id, width, height: 844, ...geometry });
      await takeScreenshot({ path: path.join(output, `dialog-padding-${id}-${width}.png`), animations: 'disabled' });
    }
    await page.setViewportSize(previousViewport);
    await page.locator(`#${id}`).evaluate((node, top) => { node.scrollTop = top; }, previousScroll);
  };
  const pairedCapture = async (name, width = 1440, height = 900, theme = 'light') => {
    const previousViewport = page.viewportSize();
    const previousTheme = await page.locator('html').getAttribute('data-theme');
    await page.setViewportSize({ width, height });
    await page.evaluate(async ({value,name}) => {
      document.documentElement.dataset.theme = value;
      scrollTo(0, 0);
      if(name.startsWith('evidence')) document.getElementById('manual-results').scrollIntoView();
      await document.fonts.ready;
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    }, {value:theme,name});
    await takeScreenshot({ path: path.join(output, `${name.startsWith('extra-') ? '' : 'paired-'}${name}.png`), animations: 'disabled' });
    const fixture = name.startsWith('operations-paused') ? {targets,actions,rules} : name.startsWith('setup') ? {targets,questions,form: await page.locator('#run-form').evaluate(form=>Object.fromEntries([...form.querySelectorAll('input,textarea,select')].map(node=>[node.id,node.value])))} : runs[0];
    captureFixtures[`paired-${name}.png`] = require('node:crypto').createHash('sha256').update(JSON.stringify(fixture,(key,value)=>key==='idempotency_key'?undefined:value)).digest('hex');
    viewportEvidence.push({ name, width, height, theme, ...(await page.evaluate(() => { const answer = [...document.querySelectorAll('.answer-text')].find((node) => node.getClientRects().length); return { pageOverflow: document.documentElement.scrollWidth > innerWidth, firstAnswerTop: answer?.getBoundingClientRect().top ?? null }; })) });
    await page.setViewportSize(previousViewport);
    await page.evaluate((value) => { document.documentElement.dataset.theme = value || 'light'; }, previousTheme);
  };
  const setup = async () => {
    const legacySetup = page.locator('#page-manual .view-nav [data-manual-view=setup]');
    if (await legacySetup.count()) { await legacySetup.click(); return; }
    if (await page.locator('#reading-toggle').getAttribute('aria-pressed') === 'true') await page.locator('#reading-toggle').click();
    await page.locator('#page-manual .view-nav [data-manual-view=results]').click();
  };
  const selectModel = async (model) => {
    const field = page.locator('#models');
    if (process.env.BASELINE_CAPTURE && await field.evaluate(node => node.tagName) !== 'SELECT') await field.fill(model);
    else await field.selectOption(model);
  };
  const selectSamples = async (count) => {
    const field = page.locator('#samples');
    if (process.env.BASELINE_CAPTURE && await field.evaluate(node => node.tagName) !== 'SELECT') await field.fill(count);
    else await field.selectOption(count);
  };
  const verifyModelDiscovery = async () => {
    const originalTargets = structuredClone(targets);
    const writesBefore = calls.filter(call => call.method !== 'GET').length;
    const refresh = async () => {
      await page.locator('#refresh').click();
      await page.locator('#refresh:not([aria-busy])').waitFor();
    };
    const modelChoices = () => page.locator('#models option').evaluateAll(nodes => nodes.filter(node => node.value && !node.disabled).map(node => node.value));
    try {
      assert.equal(await page.locator('#models').evaluate(node => node.tagName), 'SELECT', 'Models must be selectable without typing an ID');
      assert.deepEqual(await modelChoices(), models);
      assert.equal(await page.locator('#models').inputValue(), 'mock-reasoner', 'Selecting a credential chooses its first registered model');
      assert.deepEqual(await page.locator('#reasoning option').evaluateAll(nodes => nodes.filter(node => !node.disabled).map(node => node.value)), ['', 'none', 'low', 'medium', 'high', 'max']);
      await page.locator('#reasoning').selectOption('max');
      assert.equal(await page.locator('#reasoning').inputValue(), 'max', 'The highest registered reasoning level remains selectable');
      await page.locator('#reasoning').selectOption('high');
      targets[0].reasoning['mock-reasoner'].Levels = ['none', 'low'];
      await refresh();
      assert.equal(await page.locator('#reasoning').inputValue(), 'high', 'Refreshing changed capability does not silently change draft parameters');
      assert.deepEqual(await page.locator('#reasoning').evaluate(node => ({ value: node.value, disabled: node.selectedOptions[0]?.disabled, unavailable: node.dataset.unavailable })), { value: 'high', disabled: true, unavailable: 'true' }, 'The previous reasoning selection remains explicitly unavailable after registry changes');
      assert(await page.locator('#run-submit').isDisabled(), 'A stale reasoning choice blocks submission until explicit selection');
      await page.locator('#reasoning').selectOption('low');
      assert.equal(await page.locator('#run-submit').isDisabled(), false);
      targets[0].reasoning = structuredClone(originalTargets[0].reasoning);
      await refresh();
      assert.equal(await page.locator('#reasoning').inputValue(), 'low', 'A still-supported reasoning choice survives refresh');
      await page.locator('#reasoning').selectOption('high');
      await page.locator('#models').click();
      await page.keyboard.press('Escape');
      await page.locator('#models').focus();
      await page.keyboard.press('m');
      assert.equal(await page.locator('#models').inputValue(), 'mock-visual', 'Native model choices support pointer opening and keyboard selection');
      assert.equal(await page.locator('#reasoning').inputValue(), '', 'Changing to an unknown-capability model clears incompatible reasoning');
      assert.equal(await page.locator('#reasoning option:not([value=""])').count(), 0);
      assert.deepEqual(await page.locator('#reasoning option').evaluateAll(nodes => nodes.map(node => node.value)), ['']);
      await refresh();
      assert.equal(await page.locator('#models').inputValue(), 'mock-visual', 'Refresh preserves a still-registered model');

      targets[1].models = ['mock-special'];
      targets[1].reasoning = { 'mock-special': null };
      await refresh();
      await page.locator('#target').selectOption(targets[1].auth_id);
      assert.deepEqual(await modelChoices(), ['mock-special']);
      assert.equal(await page.locator('#models').inputValue(), 'mock-special', 'Credential changes cannot carry a model from a different registry');
      assert.equal(await page.locator('#reasoning option:not([value=""])').count(), 0, 'A known model without reasoning controls stays on the default');
      await page.locator('#target').selectOption(targets[0].auth_id);
      assert.equal(await page.locator('#models').inputValue(), 'mock-reasoner');

      targets[0].models = ['mock-visual'];
      await refresh();
      assert.equal(await page.locator('#models').inputValue(), 'mock-reasoner', 'A disappearing model must not silently retarget the draft on refresh');
      assert(await page.locator('#models option:checked').isDisabled(), 'A stale model remains visible as unavailable');
      assert(await page.locator('#run-submit').isDisabled());
      assert.match(await page.locator('#model-note').textContent(), /不可用|不再|重新|失效|未注册/);
      await selectModel('mock-visual');
      assert.equal(await page.locator('#run-submit').isDisabled(), false, 'Explicitly selecting a registered model resolves the stale draft');
      for (const absent of [false, true]) {
        if (absent) delete targets[0].models;
        else targets[0].models = [];
        await refresh();
        assert.deepEqual(await modelChoices(), [], 'Empty or absent registry results never invent selectable models');
        assert(await page.locator('#run-submit').isDisabled());
        assert((await page.locator('#model-note').textContent()).trim(), 'Missing model choices have an actionable explanation');
      }
      assert.equal(calls.filter(call => call.method !== 'GET').length, writesBefore, 'Selecting credentials, models, reasoning and refreshing never submits a model job');
      robustnessChecks.push('credential-scoped native model selection works by pointer and keyboard, follows capability data, preserves valid drafts and blocks stale/empty models without executing a job');
    } finally {
      targets.splice(0, targets.length, ...originalTargets);
      await refresh();
      await page.locator('#target').selectOption('mock-credential-a');
      await selectModel('mock-reasoner');
      await page.locator('#reasoning').selectOption('');
    }
  };
  const history = () => page.locator('#page-manual .view-nav [data-manual-view=history]').click();
  const operations = async (view = 'attention') => {
    await page.locator('[data-page=operations]').click();
    await page.locator(`[data-ops-view=${view}]`).click();
  };
  const capture = async (name) => {
    await page.evaluate(async () => {
      scrollTo(0, 0);
      await document.fonts.ready;
      await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    });
    await takeScreenshot({ animations: 'disabled', path: path.join(output, name), fullPage: true });
  };
  const verifyDisconnectedHeader = async () => {
    const previousViewport = page.viewportSize();
    assert.equal(await page.locator('#connect-banner').count(), 0, 'The main workspace no longer contains a connection banner');
    assert.equal(await page.locator('.app-header #connection-open').count(), 1, 'The disconnected connection action belongs to the header');
    for (const [width, height] of [[1640, 870], [1280, 720], [390, 844]]) {
      await page.setViewportSize({ width, height });
      const geometry = await page.evaluate(async () => {
        await document.fonts.ready;
        await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        const box = selector => document.querySelector(selector).getBoundingClientRect().toJSON();
        return { width: innerWidth, height: innerHeight, header: box('.app-header'), tabs: box('.primary-nav'), connection: box('#connection-open'), options: box('.option-row'), start: box('#run-submit'), prompt: box('#prompt'), pageOverflow: document.documentElement.scrollWidth > innerWidth };
      });
      headerGeometry.push(geometry);
      assert(!geometry.pageOverflow, `Disconnected header fits ${width}px without page overflow`);
      assert(geometry.connection.width > 0 && geometry.connection.height > 0, 'The relocated connection action remains visible');
      if (width >= 1280) {
        const gap = geometry.start.top - geometry.options.bottom;
        assert(gap >= 12 - 0.1 && gap <= 20 + 0.1, `Disconnected Start stays 12–20px below its options: ${JSON.stringify(geometry)}`);
        assert(Math.abs(geometry.start.bottom - geometry.prompt.bottom) <= 1, 'Compact disconnected Start remains aligned with the prompt bottom');
        assert(geometry.connection.left >= geometry.tabs.right && Math.abs((geometry.connection.top + geometry.connection.bottom) - (geometry.tabs.top + geometry.tabs.bottom)) <= 2, `Desktop connection action sits on the same row to the right of task tabs: ${JSON.stringify(geometry)}`);
      } else {
        assert(geometry.connection.height >= 44, `The phone connection action retains a usable touch target: ${JSON.stringify(geometry)}`);
        assert(geometry.connection.top >= geometry.tabs.bottom, `Phone connection action wraps below the three visible tabs: ${JSON.stringify(geometry)}`);
        assert(await page.locator('.primary-nav').evaluate(node => node.scrollWidth <= node.clientWidth + 1), 'All three phone tabs fit without horizontal scrolling');
      }
      await capture(`disconnected-header-${width}x${height}.png`);
    }
    await page.setViewportSize(previousViewport);
    robustnessChecks.push('connection stays beside desktop tabs and wraps below all three phone tabs; desktop Start keeps a 12–20px gap and aligns with the prompt bottom');
  };
  const historySnapshot = () => page.evaluate(() => {
    const panel = document.getElementById('manual-history');
    const workspace = document.querySelector('.answer-workspace');
    const toolbar = document.querySelector('.answer-toolbar');
    const heading = panel.querySelector('.history-summary');
    const pagination = panel.querySelector('.pagination');
    const emptyLine = panel.querySelector('#history-list .empty-line');
    const rect = node => node.getBoundingClientRect().toJSON();
    return {
      viewport: { width: innerWidth, height: innerHeight },
      panel: { ...rect(panel), clientHeight: panel.clientHeight, scrollHeight: panel.scrollHeight, scrollTop: panel.scrollTop },
      workspace: { ...rect(workspace), clientHeight: workspace.clientHeight, scrollHeight: workspace.scrollHeight, scrollTop: workspace.scrollTop },
      toolbar: rect(toolbar), heading: rect(heading), emptyLine: rect(emptyLine), paginationHidden: !pagination.getClientRects().length,
      rows: panel.querySelectorAll('[data-run]').length,
    };
  });
  const assertEmptyHistory = (geometry, label) => {
    assert.equal(geometry.rows, 0, `${label}: fixture must contain no history rows`);
    assert(geometry.panel.scrollHeight <= geometry.panel.clientHeight + 1, `${label}: empty history must not scroll when its content fits: ${JSON.stringify(geometry)}`);
    assert(geometry.workspace.scrollHeight <= geometry.workspace.clientHeight + 1, `${label}: the toolbar container must not scroll: ${JSON.stringify(geometry)}`);
    assert.equal(geometry.panel.scrollTop, 0, `${label}: empty history must not inherit another region's scroll`);
    assert.equal(geometry.workspace.scrollTop, 0, `${label}: toolbar container stays at its origin`);
    assert(geometry.heading.top >= geometry.toolbar.bottom - 1, `${label}: history heading must start below the toolbar: ${JSON.stringify(geometry)}`);
    assert(geometry.paginationHidden, `${label}: empty history does not expose unnecessary pagination`);
    assert(geometry.panel.bottom - geometry.emptyLine.bottom <= 32, `${label}: empty history must shrink to its content rather than leave a large blank tail`);
  };
  const verifyEmptyHistoryMatrix = async (connection) => {
    const previousViewport = page.viewportSize();
    const previousTheme = await page.locator('html').getAttribute('data-theme');
    const writesBefore = calls.filter(call => call.method !== 'GET').length;
    await history();
    await page.locator('#history-list .empty-line').waitFor();
    for (const theme of ['light', 'white', 'dark']) {
      await page.locator('#theme-toggle').selectOption(theme);
      for (const [width, height] of [[1640, 870], [1280, 720], [390, 844]]) {
        await page.setViewportSize({ width, height });
        await page.evaluate(async () => {
          scrollTo(0, 0);
          await document.fonts.ready;
          await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
        });
        const geometry = await historySnapshot();
        assertEmptyHistory(geometry, `${connection} ${theme} ${width}×${height}`);
        await page.locator('#history-list .empty-line').scrollIntoViewIfNeeded();
        const emptyLine = await page.locator('#history-list .empty-line').boundingBox();
        assert(emptyLine.y >= 0 && emptyLine.y + emptyLine.height <= height + 1, `${connection} ${theme} ${width}: empty-history content is reachable`);
        historyGeometry.push({ connection, theme, ...geometry });
        await capture(`empty-history-${connection}-${theme}-${width}x${height}.png`);
      }
    }
    await page.setViewportSize(previousViewport);
    await page.locator('#theme-toggle').selectOption(previousTheme || 'light');
    await setup();
    assert.equal(calls.filter(call => call.method !== 'GET').length, writesBefore, 'Empty-history layout checks never submit a model job');
    robustnessChecks.push(`${connection} empty history fits without an internal scrollbar, keeps its summary below the toolbar, shrinks to content and hides unused pagination at 1640×870, 1280×720 and 390×844 in all three themes`);
  };
  const verifySingleAnswerWidth = async (original) => {
    const previousViewport = page.viewportSize();
    const fixture = { ...structuredClone(original), id: 'single-prose-width-run' };
    fixture.config.samples = 1;
    fixture.samples = fixture.samples.slice(0, 1);
    runs.unshift(fixture);
    try {
      await page.setViewportSize({ width: 1640, height: 870 });
      await page.locator('#refresh').click();
      await page.locator('#refresh:not([aria-busy])').waitFor();
      await history();
      await page.locator('[data-run="single-prose-width-run"]').click();
      await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('single-prose-width-run'));
      const measure = await page.locator('.single-question .answer-prose').evaluate(node => {
        const style = getComputedStyle(node), canvas = document.createElement('canvas');
        const context = canvas.getContext('2d');
        context.font = style.font;
        return { width: node.getBoundingClientRect().width, character: context.measureText('0').width, padding: parseFloat(style.paddingLeft) + parseFloat(style.paddingRight), border: parseFloat(style.borderLeftWidth) + parseFloat(style.borderRightWidth), maxWidth: style.maxWidth };
      });
      assert(measure.width <= measure.character * 80 + measure.padding + measure.border + 1, `Single prose answers stay within an 80-character reading measure: ${JSON.stringify(measure)}`);
      historyGeometry.push({ state: 'single-answer-reading-width', ...measure });
      await capture('single-prose-reading-width-1640x870.png');
      await history();
      await page.locator(`[data-run="${original.id}"]`).click();
      await page.waitForFunction(id => document.getElementById('run-frozen-data').textContent.includes(id), original.id);
    } finally {
      runs = runs.filter(run => run.id !== fixture.id);
      await page.setViewportSize(previousViewport);
    }
    robustnessChecks.push('single manual prose answers stay within an 80-character reading measure at 1640×870');
  };
  const verifyHistoryScrollIsolation = async () => {
    const previousViewport = page.viewportSize();
    const emptyHistoryRoute = '**/runs?source=manual_test&*';
    await page.setViewportSize({ width: 1280, height: 720 });
    await setup();
    const resultScroll = await page.locator('#manual-results').evaluate(node => {
      node.scrollTop = Math.min(120, node.scrollHeight - node.clientHeight);
      return { top: node.scrollTop, maximum: node.scrollHeight - node.clientHeight };
    });
    assert(resultScroll.top > 0, 'Long comparison results must exercise the results region scrollbar');
    const toolbarBefore = await page.locator('.answer-toolbar').boundingBox();
    await page.route(emptyHistoryRoute, route => route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], total: 0, offset: 0, limit: 10 }) }));
    try {
      const emptyResponse = page.waitForResponse(response => response.url().includes('/runs?source=manual_test&'));
      await history();
      await emptyResponse;
      await page.waitForFunction(() => document.getElementById('history-total').textContent === '共 0 条记录' && !document.querySelector('#history-list [data-run]'));
      const geometry = await historySnapshot();
      assertEmptyHistory(geometry, 'history after scrolled results');
      assert(Math.abs(geometry.toolbar.top - toolbarBefore.y) <= 1, 'Switching from scrolled results cannot move the toolbar');
      const readingRight = await page.locator('#reading-toggle').evaluate(node => node.getBoundingClientRect().right);
      assert(Math.abs(readingRight - geometry.toolbar.right) <= 1, 'History keeps the reading action aligned to the toolbar right edge after result actions are hidden');
      await capture('empty-history-after-scrolled-results.png');
      await page.locator('#page-manual .view-nav [data-manual-view=results]').click();
      const restored = await page.locator('#manual-results').evaluate(node => ({ top: node.scrollTop, maximum: node.scrollHeight - node.clientHeight }));
      assert.equal(restored.top, Math.min(resultScroll.top, restored.maximum), 'Returning to answers preserves the results region scroll independently');
      assert.equal(await page.locator('.answer-workspace').evaluate(node => node.scrollTop), 0, 'The shared toolbar container never inherits answer scrolling');
      historyGeometry.push({ connection: 'connected', state: 'after-scrolled-results', resultScroll, restored, ...geometry });
    } finally {
      await page.unroute(emptyHistoryRoute);
      await page.locator('#manual-results').evaluate(node => { node.scrollTop = 0; });
      await page.setViewportSize(previousViewport);
    }
    robustnessChecks.push('long results scroll independently; switching to empty history keeps the toolbar and heading separated and returning restores only the results scroll');
  };
  const stateMatrix = async (prefix) => {
    const size=page.viewportSize();
    const oldTheme=await page.locator('html').getAttribute('data-theme');
    for (const theme of ['light','white','dark']) {
      await page.locator('#theme-toggle').selectOption(theme);
      for (const [width,height] of [[1440,900],[1280,800],[768,1024],[390,844],[360,800]]) {
        await page.setViewportSize({width,height});
        await capture(`${prefix}-${theme}-${width}.png`);
        assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${prefix} ${theme} ${width} page overflow`);
        if (width >= 1280) {
          const fit = await page.evaluate(() => ({ height: innerHeight, document_height: document.documentElement.scrollHeight, save: document.querySelector('#rule-form button[type="submit"]')?.getBoundingClientRect().toJSON() }));
          assert(fit.document_height <= fit.height + 2, `${prefix} ${theme} ${width}: desktop content should scroll within its workspace: ${JSON.stringify(fit)}`);
          if (prefix === 'rule-editor') assert(fit.save.top >= 0 && fit.save.bottom <= fit.height + 1, 'Rule save remains visible while its long fields scroll locally');
        }

      }
    }
    await page.setViewportSize(size);
    await page.locator('#theme-toggle').selectOption(oldTheme || 'light');
  };
  const verifyManagementPersistence = async () => {
    const context = await browser.newContext({ viewport: { width: 1280, height: 800 }, locale: 'zh-CN' });
    const requests = [];
    context.on('request', request => {
      if (new URL(request.url()).pathname.startsWith('/v8/management/')) requests.push(request);
    });
    context.on('page', testPage => testPage.on('pageerror', error => errors.push(error.message)));
    const fixtureURL = `${base}/v0/resource/plugins/model-quality/index.html`;
    const waitConnected = testPage => testPage.locator('body.connected').waitFor();
    const assertSaved = async (testPage, key = 'offline-test-key') => {
      assert.deepEqual(await browserStorage(testPage), { local: key ? { [managementStorageKey]: key } : {}, session: {} });
      assert.equal(await testPage.locator('#management-key').inputValue(), '', 'The saved key is not repopulated into the form');
    };
    const connect = async (testPage, key = 'offline-test-key') => {
      await testPage.locator(await testPage.locator('#connection-top').isVisible() ? '#connection-top' : '#connection-open').click();
      await testPage.locator('#management-key').fill(key);
      await testPage.locator('#connection-form button[type=submit]').click();
    };
    const disconnect = async testPage => {
      await testPage.locator('#connection-top').click();
      await testPage.locator('#disconnect').click();
      await testPage.locator('body:not(.connected)').waitFor();
      await assertSaved(testPage, null);
    };
    const testPage = await context.newPage();
    try {
      await testPage.goto(fixtureURL);
      await connect(testPage, '   offline-test-key   ');
      await waitConnected(testPage);
      await assertSaved(testPage);
      const beforeReload = requests.length;
      await testPage.reload();
      await waitConnected(testPage);
      assert(requests.length > beforeReload, 'Refresh authenticates through management reads');
      await assertSaved(testPage);
      assert.equal(await testPage.locator('#connection-dialog').evaluate(dialog => dialog.open), false);
      const reopened = await context.newPage();
      await reopened.goto(fixtureURL);
      await waitConnected(reopened);
      await assertSaved(reopened);
      await reopened.close();
      robustnessChecks.push('successful authentication saves only the trimmed plugin management key; reload and a newly opened page reconnect without reopening the dialog or filling its input');

      const beforeRestore = requests.length;
      await testPage.evaluate(() => {
        dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
        dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
      });
      await waitConnected(testPage);
      await testPage.waitForLoadState('networkidle');
      assert(requests.length > beforeRestore, 'A simulated persisted pageshow revalidates the restored session');
      await assertSaved(testPage);
      robustnessChecks.push('persisted page lifecycle handlers reconnect using saved authentication and clear the old request session');

      const otherTab = await context.newPage();
      await otherTab.goto(fixtureURL);
      await waitConnected(otherTab);
      const restoringGate = deferred(), restoringRequested = deferred();
      const heldRestore = async route => {
        restoringRequested.resolve();
        await restoringGate.promise;
        await route.continue().catch(() => {});
      };
      await testPage.route('**/targets?limit=100', heldRestore);
      try {
        await testPage.reload();
        await restoringRequested.promise;
        await disconnect(otherTab);
        restoringGate.resolve();
        await waitConnected(testPage);
        await assertSaved(testPage, null);
        await otherTab.reload();
        await otherTab.waitForLoadState('networkidle');
        assert.equal(await otherTab.locator('body.connected').count(), 0, 'A late restore in another tab cannot undo an explicit Disconnect');
      } finally {
        restoringGate.resolve();
        await testPage.unroute('**/targets?limit=100', heldRestore);
        await otherTab.close();
      }
      await disconnect(testPage);
      await connect(testPage);
      await waitConnected(testPage);
      robustnessChecks.push('a delayed automatic restore cannot resave a key after another tab explicitly disconnects');

      const newerTab = await context.newPage();
      await newerTab.goto(fixtureURL);
      await waitConnected(newerTab);
      await connect(newerTab, 'offline-test-key-next');
      await waitConnected(newerTab);
      await assertSaved(newerTab, 'offline-test-key-next');
      await testPage.route('**/metrics', route => route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"expired old session"}' }));
      try {
        await testPage.locator('#refresh').click();
        await testPage.locator('#notice').filter({ hasText: '鉴权已失效' }).waitFor();
        assert.equal(await testPage.locator('body.connected').count(), 0);
        await assertSaved(testPage, 'offline-test-key-next');
        await newerTab.reload();
        await waitConnected(newerTab);
        await assertSaved(newerTab, 'offline-test-key-next');
      } finally {
        await testPage.unroute('**/metrics');
        await newerTab.close();
      }
      await connect(testPage);
      await waitConnected(testPage);
      robustnessChecks.push('401 from an older tab clears that session without deleting a different key successfully saved by a newer tab');

      for (const failure of ['server', 'network']) {
        const routePattern = '**/targets?limit=100';
        await testPage.route(routePattern, route => failure === 'server'
          ? route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"temporarily unavailable"}' })
          : route.abort('failed'));
        await testPage.reload();
        await testPage.locator('#notice').filter({ hasText: /连接未完成|连接失败|自动连接/ }).waitFor();
        assert.equal(await testPage.locator('body.connected').count(), 0);
        await assertSaved(testPage);
        await testPage.unroute(routePattern);
        await testPage.reload();
        await waitConnected(testPage);
        await assertSaved(testPage);
      }
      robustnessChecks.push('HTTP 503 and network failures retain the saved key; the next refresh reconnects after the service recovers');

      await disconnect(testPage);
      const afterDisconnect = requests.length;
      await testPage.reload();
      await testPage.waitForLoadState('networkidle');
      assert.equal(requests.length, afterDisconnect, 'Refresh after Disconnect makes no authenticated requests');
      await assertSaved(testPage, null);
      robustnessChecks.push('explicit Disconnect clears saved authentication and a subsequent refresh stays disconnected without management reads');

      await testPage.evaluate(key => localStorage.setItem(key, 'expired-offline-key'), managementStorageKey);
      await testPage.reload();
      await testPage.locator('#notice').filter({ hasText: '鉴权已失效' }).waitFor();
      await assertSaved(testPage, null);
      assert.equal(await testPage.locator('body.connected').count(), 0);
      const afterUnauthorized = requests.length;
      await testPage.reload();
      await testPage.waitForLoadState('networkidle');
      assert.equal(requests.length, afterUnauthorized, 'An expired saved key is not retried on the next refresh');
      robustnessChecks.push('401 while restoring removes the saved key and does not retry it on later page loads');

      const gate = deferred(), requested = deferred();
      const delayedRoute = async route => {
        requested.resolve();
        await gate.promise;
        await route.continue().catch(() => {});
      };
      await testPage.route('**/targets?limit=100', delayedRoute);
      try {
        await connect(testPage);
        await requested.promise;
        await assertSaved(testPage, null);
        await testPage.locator('#connection-open').click();
        await testPage.locator('#disconnect').click();
        gate.resolve();
        await testPage.waitForLoadState('networkidle');
        await assertSaved(testPage, null);
        assert.equal(await testPage.locator('body.connected').count(), 0, 'A cancelled pending connection cannot restore the connected state');
      } finally {
        gate.resolve();
        await testPage.unroute('**/targets?limit=100', delayedRoute);
      }
      robustnessChecks.push('pending authentication is never persisted; Disconnect aborts a delayed connection and late reads cannot resave its key or reconnect');

      for (const method of ['getItem', 'setItem']) {
        await testPage.evaluate(method => {
          const original = Storage.prototype[method];
          window.restoreStorageMethod = () => { Storage.prototype[method] = original; };
          Storage.prototype[method] = function () { throw new DOMException('Storage is blocked by browser policy', 'SecurityError'); };
        }, method);
        // A persisted pageshow exercises startup reads without resetting the injected storage failure.
        if (method === 'getItem') await testPage.evaluate(() => {
          dispatchEvent(new PageTransitionEvent('pagehide', { persisted: true }));
          dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }));
        });
        if (method === 'getItem') assert.match(await testPage.locator('#notice').textContent(), /存储/, 'Blocked storage reads explain how to connect manually');
        await connect(testPage);
        await waitConnected(testPage);
        if (method === 'setItem') assert.match(await testPage.locator('#notice').textContent(), /保存|存储|记住|刷新/, 'Blocked storage writes explain that refresh persistence is unavailable');
        await testPage.evaluate(() => { window.restoreStorageMethod(); delete window.restoreStorageMethod; });
        await disconnect(testPage);
      }
      robustnessChecks.push('blocked browser storage reads and writes do not prevent manual connection and explain that refresh persistence is unavailable');
      assert(requests.every(request => request.method() === 'GET'), 'Restoring, reconnecting and page lifecycle checks never start model/login jobs or mutate management state');
      assert(!requests.some(request => request.url().includes('offline-test-key') || request.url().includes('expired-offline-key')), 'Management keys never appear in request URLs');
      robustnessChecks.push('automatic connection restoration performs management GETs only and never starts model evaluation or OpenAI login');
    } finally {
      await context.close();
    }
  };
  page.on('pageerror', (error) => errors.push(error.message));
  try {
    await page.goto(`${base}/v0/resource/plugins/model-quality/index.html`);
    await page.locator('#run-form').waitFor();
    assert.equal(await page.locator('[data-page]').count(), 3);
    assert.equal(await page.locator('#sidebar').count(), 0);
    await page.keyboard.press('Tab');
    assert(await page.evaluate(() => document.activeElement.classList.contains('skip-link')));
    await page.keyboard.press('Enter');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'main');
    assert.equal(calls.length, 0);
    if (!process.env.BASELINE_CAPTURE) {
      assert.equal(await page.locator('#prompt-reset').count(), 0, 'The removed reset action is not exposed');
      assert.equal(await page.locator('#run-guidance').isVisible(), false, 'Disconnected guidance under Start stays hidden');
      assert.equal(await page.locator('#run-plan').isVisible(), false);
      assert.equal(await page.locator('#run-admission').isVisible(), false);
      robustnessChecks.push('removed reset action and disconnected Start guidance stay absent from the visible manual form');
      await verifyDisconnectedHeader();
    }
    await capture('disconnected-page.png');
    if (!process.env.BASELINE_CAPTURE) await verifyEmptyHistoryMatrix('disconnected');
    await page.locator('#connection-open').click();
    if (!process.env.BASELINE_CAPTURE) await verifyDialogPadding('connection-dialog');
    await page.locator('#management-key').fill('offline-test-key');
    await page.locator('#connection-form button[type=submit]').click();
    await page.waitForFunction(() => document.getElementById('connection-state').textContent === '已连接');
    assert(calls.every(call => call.method === 'GET' && call.authorization === 'Bearer offline-test-key'));
    assert(!calls.some(call => call.query.includes('offline-test-key')));
    assert.deepEqual(await browserStorage(page), { local: { [managementStorageKey]: 'offline-test-key' }, session: {} });
    assert.equal(await page.locator('#management-key').inputValue(), '');
    if (!process.env.BASELINE_CAPTURE) await verifyManagementPersistence();
    if (!process.env.BASELINE_CAPTURE) await verifyEmptyHistoryMatrix('connected');
    await page.locator('#target').selectOption('mock-credential-a');
    await page.locator('#question').selectOption('logic-candy');
    if (!process.env.BASELINE_CAPTURE) await verifyModelDiscovery();
    await selectModel('mock-reasoner');
    await selectSamples('2');
    await pairedCapture('setup-desktop');
    await pairedCapture('setup-mobile', 390, 844);
    await pairedCapture('setup-dark-desktop', 1440, 900, 'dark');
    await pairedCapture('setup-white-desktop', 1440, 900, 'white');
    if (!process.env.BASELINE_CAPTURE) {
      const previousViewport = page.viewportSize();
      await page.setViewportSize({ width: 1064, height: 720 });
      const originalTarget = structuredClone(targets[0]);
      const longID = 'offline-credential-' + 'long-identifier-'.repeat(15);
      Object.assign(targets[0], { auth_id: longID, auth_index: longID, manual_disabled: true, effective_available: false, reasons: ['manual_disabled'] });
      await page.locator('#refresh').click();
      await page.locator('#refresh:not([aria-busy])').waitFor();
      await page.locator('#target').selectOption(longID);
      const fit = await page.evaluate(() => ({ height: innerHeight, document_height: document.documentElement.scrollHeight, start: document.getElementById('run-submit').getBoundingClientRect().toJSON(), detail_text: document.getElementById('target-summary').textContent }));
      assert(fit.document_height <= fit.height + 2 && fit.start.top >= 0 && fit.start.bottom <= fit.height + 1, `Long credential names keep the disabled Start action visible: ${JSON.stringify(fit)}`);
      assert((await page.locator('#target').getAttribute('title')).includes(longID), 'The full credential ID remains inspectable in its title');
      assert.match(fit.detail_text, /人工禁用/);
      assert(await page.locator('#run-submit').isDisabled());
      await capture('long-credential-expanded-desktop.png');
      robustnessChecks.push('long credential names retain their full ID in a title and show active restrictions while the disabled start action stays within the desktop viewport');
      Object.keys(targets[0]).forEach(key => delete targets[0][key]);
      Object.assign(targets[0], originalTarget);
      await page.locator('#refresh').click();
      await page.locator('#refresh:not([aria-busy])').waitFor();
      await page.locator('#target').selectOption(originalTarget.auth_id);
      await page.setViewportSize(previousViewport);
    }
    const builtin = await page.locator('#prompt').inputValue();
    await page.locator('#run-submit').click();
    await page.waitForFunction(() => document.querySelectorAll('.sample').length === 2);
    const baselineExclusive = process.env.BASELINE_CAPTURE && await page.locator('[data-manual-view=setup]').count() > 0;
    assert.equal(await page.locator('#manual-setup').isVisible(), !baselineExclusive, 'Configuration and answers remain visible together after submission; the frozen baseline retains its original behavior');
    assert.equal(await page.locator('.sample pre').first().textContent(), '21');
    assert.equal(runs[0].config.models.length, 1);
    assert.equal(runs[0].config.samples, 2);
    assert.equal(runs[0].config.judge.mode, 'manual');
    assert.equal(actions.length, 0);
    if (!process.env.BASELINE_CAPTURE) await verifySingleAnswerWidth(runs[0]);
    if (!process.env.BASELINE_CAPTURE) {
      const previousViewport = page.viewportSize();
      const writesBeforeResize = calls.filter(call => call.method !== 'GET').length;
      await page.setViewportSize({ width: 700, height: 900 });
      assert.equal(await page.locator('#task-actions').evaluate(node => node.open), true, 'Tablet task actions remain expanded above the phone breakpoint');
      for (const id of ['run-rerun', 'run-export']) {
        assert(await page.locator(`#${id}`).isVisible(), `${id} remains visible at 700px`);
        await page.locator(`#${id}`).focus();
        assert.equal(await page.evaluate(() => document.activeElement.id), id, `${id} remains keyboard reachable at 700px`);
      }
      assert.equal(calls.filter(call => call.method !== 'GET').length, writesBeforeResize, 'Responsive action controls do not submit jobs on resize');
      await page.setViewportSize(previousViewport);
      robustnessChecks.push('700px tablet task actions keep rerun and export visible and keyboard reachable without a hidden collapsed disclosure');
    }
    await pairedCapture('evidence-desktop');
    await pairedCapture('evidence-mobile', 390, 844);
    if (process.env.BASELINE_CAPTURE) {
      rules = [pausedRuleFixture()];
      actions = [{auth_id:targets[0].auth_id,owner_plugin:'model-quality',rule_id:'rule-1',rule_revision:1,action_id:'pause-1',action_revision:1,identity_version:'identity-a',reason:'所有选定模型连续 3 轮明确失败',created_at:created}];
      targets[0].quality_paused=true; targets[0].manual_disabled=true; targets[0].effective_available=false; targets[0].reasons=['manual_disabled','quality_pause'];
      await operations(); await page.locator('#refresh').click(); await page.locator('[data-action-preview]').waitFor();
      await pairedCapture('operations-paused-desktop'); await pairedCapture('operations-paused-mobile',390,844);
      const screenshots=[...producedScreenshots].filter(name=>name.startsWith('paired-')).sort();
      fs.writeFileSync(path.join(output,'ui-results.json'),JSON.stringify({result:'passed',scope:'same fake fixtures and exact frozen source; before/after capture only',source_sha256:initialSourceHashes,checks:['baseline task/answer/pause fixtures rendered'],capture_fixture_sha256:captureFixtures,screenshots,screenshot_sha256:Object.fromEntries(screenshots.map(name=>[name,require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(output,name))).digest('hex')]))},null,2)+'\n');
      return;
    }
    assert.equal(await page.locator('[data-manual-view=setup]').count(), 0, 'Configuration is not a separate workspace tab');
    await page.locator('#reading-toggle').click();
    assert.equal(await page.locator('#reading-toggle').getAttribute('aria-pressed'), 'true');
    assert.equal(await page.locator('#manual-setup').isVisible(), false, 'Only explicit expanded reading hides configuration');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'reading-toggle');
    await page.locator('#reading-toggle').click();
    assert.equal(await page.locator('#reading-toggle').getAttribute('aria-pressed'), 'false');
    assert.equal(await page.locator('#manual-setup').isVisible(), true);
    await history();
    assert.equal(await page.locator('#manual-setup').isVisible(), true, 'History navigation also preserves the visible configuration');
    await setup();
    assert.equal(await page.locator('#prompt').inputValue(), builtin, 'Expanded reading and local history preserve the executed draft');
    robustnessChecks.push('configuration and current answers share one page; only explicit expanded reading hides configuration, preserves focus and restores the unchanged draft');
    const contrastEvidence = [];
    const luminance = hex => {
      const rgb=hex.replace('#','').match(/../g).map(v=>parseInt(v,16)/255).map(v=>v<=.04045?v/12.92:((v+.055)/1.055)**2.4);
      return rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722;
    };
    for (const theme of ['light','white','dark']) {
      await page.locator('#theme-toggle').selectOption(theme);
      const colors=await page.evaluate(()=>{
        const style=getComputedStyle(document.documentElement);
        return Object.fromEntries(['page','card','input','floating','hover','ink','muted','good','good-bg','warn','warn-bg','bad','bad-bg','accent','accent-hover','on-accent','control-border','focus','danger','on-danger'].map(name=>[name,style.getPropertyValue('--'+name).trim()]));
      });
      const pairs=[];
      for(const ink of ['ink','muted']) for(const bg of ['page','card','input','floating','hover']) pairs.push([ink,bg,4.5]);
      for(const status of ['good','warn','bad']) pairs.push([status,status+'-bg',4.5]);
      pairs.push(['on-accent','accent',4.5],['on-accent','accent-hover',4.5],['on-danger','danger',4.5]);
      for(const bg of ['page','card','input','floating']) pairs.push(['control-border',bg,3],['focus',bg,3]);
      const checks=pairs.map(([foreground,background,threshold])=>{
        const a=luminance(colors[foreground]),b=luminance(colors[background]);
        const ratio=(Math.max(a,b)+.05)/(Math.min(a,b)+.05);
        assert(ratio>=threshold,`${theme} ${foreground}/${background}: ${ratio.toFixed(2)} < ${threshold}`);
        return {foreground,background,ratio,threshold,result:'passed'};
      });
      contrastEvidence.push({theme,colors,checks});
      for(const [width,height] of [[1440,900],[1280,800],[768,1024],[390,844],[360,800]]) {
        await page.setViewportSize({width,height});
        await capture(`matrix-${theme}-${width}.png`);
        assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),`${theme} ${width} page overflow`);
        if(width<768){
          const small=await page.locator('input:visible,select:visible,textarea:visible').evaluateAll(nodes=>nodes.filter(n=>n.type!=='checkbox'&&parseFloat(getComputedStyle(n).fontSize)<16).map(n=>n.id));
          assert.deepEqual(small,[],'Phone text fields remain at least 16px');
          assert((await page.locator('#run-submit').boundingBox()).height>=44,'Primary phone action is at least 44px');
        }
      }
    }
    fs.writeFileSync(path.join(output,'contrast-results.json'),JSON.stringify({result:'passed',source_sha256:initialSourceHashes,browser:await browser.version(),scope:'Computed semantic role pairs in three themes; no certification claim',themes:contrastEvidence},null,2)+'\n');
    await page.setViewportSize({width:1440,height:900});
    await page.locator('#theme-toggle').selectOption('light');
    robustnessChecks.push('static shell, themes and connection cause no writes; locally saved management key; one model with two manual answers has no quality action');
    await page.locator('#prompt').fill(builtin+'\n新增的自定义条件');
    assert.match(await page.locator('#prompt-note').textContent(),/自定义/);
    await page.locator('#run-submit').click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('run-2'));
    assert.equal(runs[0].config.question_id,'custom');
    assert.equal(runs[0].config.question.prompt,builtin+'\n新增的自定义条件');
    assert(!runs[0].config.question.answer);
    await page.locator('[data-evidence-sample]').first().click();
    await verifyDialogPadding('evidence-dialog');
    await page.keyboard.press('Escape');
    assert.equal(await page.locator('#evidence-dialog').isVisible(),false);
    assert.equal(await page.locator('[data-evidence-sample]').first().evaluate(n=>n===document.activeElement),true);
    const frozenConfig=structuredClone(runs[0].config);
    await page.locator('#run-rerun').click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('run-3'));
    assert.equal(runs[0].config.question.prompt,frozenConfig.question.prompt);
    assert.deepEqual(runs[0].config.parameters,frozenConfig.parameters);
    const downloadEvent=page.waitForEvent('download');
    await page.locator('#run-export').click();
    const download=await downloadEvent;
    const exported=fs.readFileSync(await download.path(),'utf8');
    assert(!exported.includes('offline-test-key'));
    assert.equal(JSON.parse(exported).config.question.prompt,frozenConfig.question.prompt);
    robustnessChecks.push('eligible rerun preserves frozen question and parameters; exported JSON preserves results and omits the saved management key');
    await setup();
    await page.locator('#question').selectOption('custom');
    await page.locator('#prompt').fill('自定义题草稿，在刷新后仍须保留。');
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#prompt').inputValue(),'自定义题草稿，在刷新后仍须保留。');
    robustnessChecks.push('edited built-ins become custom without inherited answers; refresh preserves custom prompt drafts');
    const pending={id:'cancel-run',status:'running',source:'manual_test',created_at:created,config,samples:[],action_status:'not_applicable'};
    runs.unshift(pending);
    await page.locator('#refresh').click(); await history();
    await page.locator('[data-run="cancel-run"]').first().click();
    await page.locator('#run-cancel').click();
    await page.waitForFunction(() => document.getElementById('run-meta').textContent.includes('已取消'));
    await operations('rules');
    await page.locator('#rule-new').click();
    await page.locator('#rule-name').fill('核心模型巡检');
    await page.locator('#rule-target').selectOption('mock-credential-a');
    await page.locator('#rule-models').fill('mock-reasoner\nmock-visual');
    await page.locator('#rule-question').selectOption('logic');
    await capture('rule-editor-page.png');
    await stateMatrix('rule-editor');
    const invalidModels = Array.from({ length: 9 }, (_, index) => `offline-model-${index + 1}`).join('\n');
    const writesBeforeInvalidRule = calls.filter((call) => call.method !== 'GET').length;
    await page.locator('#rule-models').fill(invalidModels);
    await page.locator('#rule-form button[type=submit]').click();
    await page.waitForFunction(() => {
      const field = document.getElementById('rule-models');
      return field.getAttribute('aria-invalid') === 'true' && !field.disabled;
    });
    assert.equal(await page.locator('#rule-models').inputValue(), invalidModels, 'Client validation preserves the invalid rule draft');
    assert.equal(await page.locator('#rule-name').inputValue(), '核心模型巡检');
    assert.equal(await page.evaluate(() => document.activeElement.id), 'rule-models', 'Client validation focuses the invalid field after the save lock is released');
    assert.match(await page.locator('#rule-models-error').textContent(), /1–8/);
    assert.equal(calls.filter((call) => call.method !== 'GET').length, writesBeforeInvalidRule, 'Invalid rule validation makes no API writes or model calls');
    await page.locator('#rule-models').fill('mock-reasoner\nmock-visual');
    assert.equal(await page.locator('#rule-models').getAttribute('aria-invalid'), null);
    robustnessChecks.push('client-side rule validation preserves the draft, focuses its invalid field after unlocking, and makes no API writes');
    const callCountBeforeSave = calls.filter(
      (call) => call.method === 'POST' && call.path.endsWith('/runs'),
    ).length;
    const ruleSaveEntered = deferred();
    const ruleSaveGate = deferred();
    await page.route('**/rules', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      ruleSaveEntered.resolve();
      await ruleSaveGate.promise;
      await route.continue();
    });
    await page.locator('#rule-form button[type=submit]').click();
    await ruleSaveEntered.promise;
    assert.equal(await page.locator('#rule-name').isEnabled(), false, 'Saving locks the submitted draft against edits');
    assert.equal(await page.locator('[data-close=rule-dialog]').first().isEnabled(), false, 'Saving locks editor close until its result is known');
    await page.locator('#settings-open').evaluate(node => node.click());
    assert.equal(await page.locator('#settings-dialog').isVisible(), false, 'A queued settings trigger cannot replace the saving editor');
    await capture('rule-saving-page.png');
    ruleSaveGate.resolve();
    await page.locator('#rules-list strong').filter({ hasText: '核心模型巡检' }).waitFor();
    await page.unroute('**/rules');
    robustnessChecks.push('an in-flight rule save locks draft fields and close controls and cannot be replaced by a different editor');
    await capture('rules-page.png');
    await captureSurfaceThemes('rules', '#ops-rules');
    assert.equal(
      calls.filter((call) => call.method === 'POST' && call.path.endsWith('/runs')).length,
      callCountBeforeSave,
      'Saving rule must not call a model',
    );
    assert.equal(rules[0].action, 'observe_only');
    assert.equal(rules[0].automatic_pause, false);
    assert(!('automatic_recovery' in rules[0]));
    assert(!('pass_threshold' in rules[0]));
    assert(!('retest_seconds' in rules[0]));
    assert.equal(await page.locator('#rule-auto-recovery,#rule-pass,#rule-retest').count(), 0);
    assert.equal(rules[0].scheduling_enabled, false);
    rules.push({ ...rules[0], id: 'delete-rule', name: '待删除的离线规则' });
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    const deletesBefore = calls.filter((call) => call.method === 'DELETE' && call.path.endsWith('/rules')).length;
    await page.locator('[data-rule-delete="delete-rule"]').click();
    assert.match(await page.locator('#action-dialog').textContent(), /待删除的离线规则/);
    await verifyDialogPadding('action-dialog');
    await page.locator('#action-cancel').click();
    assert.equal(calls.filter((call) => call.method === 'DELETE' && call.path.endsWith('/rules')).length, deletesBefore);
    await page.locator('[data-rule-delete="delete-rule"]').click();
    await page.locator('#action-confirm').click();
    await page.locator('[data-rule-delete="delete-rule"]').waitFor({ state: 'detached' });
    assert.equal(rules.length, 1);
    assert.equal(calls.filter((call) => call.method === 'DELETE' && call.path.endsWith('/rules')).length, deletesBefore + 1);
    robustnessChecks.push('rule deletion names the object, supports cancellation and submits exactly one explicit delete');
    const rulePauseEntered = deferred();
    const rulePauseGate = deferred();
    const rulePausesBefore = calls.filter((call) => call.method === 'POST' && call.path.endsWith('/rules/pause')).length;
    await page.route('**/rules/pause', async (route) => {
      rulePauseEntered.resolve();
      await rulePauseGate.promise;
      await route.continue();
    });
    await page.locator('[data-rule-pause]').click();
    await rulePauseEntered.promise;
    await page.locator('[data-page=manual]').click();
    await setup();
    rulePauseGate.resolve();
    await page.waitForFunction(() => document.getElementById('rules-list').textContent.includes('恢复检测规则'));
    assert.equal(await page.locator('#notice').isVisible(), false, 'A delayed rule pause cannot report success in the manual workspace');
    assert.equal(calls.filter((call) => call.method === 'POST' && call.path.endsWith('/rules/pause')).length, rulePausesBefore + 1);
    await page.unroute('**/rules/pause');
    await operations('rules');
    await page.locator('[data-rule-pause]').filter({ hasText: '恢复检测规则' }).waitFor();
    assert.equal(rules[0].enabled, false);
    robustnessChecks.push('a delayed rule pause completes exactly once without leaking feedback into the manual workspace');

    const settingsReadEntered = deferred();
    const settingsReadGate = deferred();
    await page.route('**/settings', async (route) => {
      if (route.request().method() !== 'GET') return route.continue();
      settingsReadEntered.resolve();
      await settingsReadGate.promise;
      await route.continue();
    });
    await page.locator('#settings-open').click();
    await settingsReadEntered.promise;
    await page.locator('#rule-new').click();
    await page.locator('#rule-name').fill('保留后打开的草稿');
    settingsReadGate.resolve();
    await page.waitForFunction(() => !document.getElementById('settings-open').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#rule-dialog').isVisible(), true);
    assert.equal(await page.locator('#settings-dialog').isVisible(), false);
    assert.equal(await page.locator('#rule-name').inputValue(), '保留后打开的草稿');
    await page.unroute('**/settings');
    await page.locator('[data-close=rule-dialog]').first().click();
    await page.locator('#action-confirm').click();
    await page.locator('#action-dialog').waitFor({ state: 'hidden' });
    robustnessChecks.push('a delayed budget read cannot replace a rule editor opened after the read began');

    await page.locator('#settings-open').click();
    await page.locator('#settings-dialog').waitFor({ state: 'visible' });
    await captureSurfaceThemes('settings', '#settings-dialog');
    await page.locator('#setting-daily_calls').fill('120');
    await page.route('**/settings', async (route) => {
      if (route.request().method() === 'PUT')
        return route.fulfill({
          status: 409,
          contentType: 'application/json',
          body: '{"error":{"code":"conflict","message":"configuration revision conflict"}}',
        });
      return route.continue();
    });
    await page.locator('#settings-form button[type=submit]').click();
    await page.locator('#settings-dialog .dialog-message').waitFor({ state: 'visible' });
    assert((await page.locator('#settings-dialog .dialog-message').textContent()).includes('conflict'));
    assert.equal(await page.locator('#settings-dialog').isVisible(), true);
    assert.equal(await page.locator('#setting-daily_calls').inputValue(), '120');
    await page.unroute('**/settings');
    await page.locator('[data-close=settings-dialog]').first().click();
    await page.locator('#action-dialog').waitFor({ state: 'visible' });
    assert.match(await page.locator('#action-dialog').textContent(), /放弃|未保存/);
    await page.locator('#action-cancel').click();
    assert.equal(await page.locator('#settings-dialog').isVisible(), true);
    assert.equal(await page.locator('#setting-daily_calls').inputValue(), '120');
    robustnessChecks.push('unsaved budget edits require explicit discard and cancellation preserves the draft');
    robustnessChecks.push('failed mutation keeps form values and exposes its error inside the inline editor');
    await page.locator('#settings-form button[type=submit]').click();
    await page.waitForFunction(() =>
      document.getElementById('notice').textContent.includes('全局开关与预算已保存'),
    );
    assert.equal(settings.daily_calls, 120);
    assert.equal(settings.data_dir, 'mock-data');
    assert.equal(settings.workers, 2);
    assert(!('automatic_recovery' in settings));
    assert.equal(await page.locator('#settings-auto-recovery').count(), 0);

    actions = [
      {
        auth_id: 'mock-credential-a',
        owner_plugin: 'model-quality',
        rule_id: 'rule-1',
        rule_revision: 1,
        action_id: 'pause-1',
        action_revision: 1,
        identity_version: 'identity-a',
        reason: '所有选定模型连续 3 轮明确失败',
        created_at: created,
      },
    ];
    rules[0] = pausedRuleFixture();
    targets[0].quality_paused = true;
    targets[0].manual_disabled = true;
    targets[0].effective_available = false;
    targets[0].reasons = ['manual_disabled', 'quality_pause'];
    await operations();
    await page.locator('#refresh').click();
    await page.locator('[data-action-preview]').waitFor();
    assert.equal(await page.locator('[data-rule-run]').isDisabled(), true);
    assert.equal(await page.locator('[data-rule-pause]').isDisabled(), true);
    assert.equal(await page.locator('#run-submit').isDisabled(), true);
    assert.equal(await page.locator('#run-rerun').isDisabled(), true);
    const heldCallCount = calls.filter(
      (call) => call.method === 'POST' && call.path.endsWith('/runs'),
    ).length;
    await page.locator('[data-page=manual]').click();
    await setup();
    assert.equal(await page.locator('#run-submit').isDisabled(), true);
    assert.equal(calls.filter(call => call.method === 'POST' && call.path.endsWith('/runs')).length, heldCallCount);
    await operations();
    await page.evaluate(() => scrollTo(0, 0));
    await takeScreenshot({
      animations: 'disabled',
      path: path.join(output, 'operations-page.png'),
      fullPage: true,
    });
    await pairedCapture('operations-paused-desktop');
    await pairedCapture('operations-paused-mobile', 390, 844);
    await stateMatrix('operations-paused');
    await page.locator('[data-action-preview]').click();
    await page.locator('#release-dialog').waitFor({ state: 'visible' });
    await page.waitForFunction(() => !document.getElementById('release-confirm').disabled);
    await verifyDialogPadding('release-dialog');
    assert.equal(
      await page.locator('#release-pause-rule').count(),
      0,
      'Manual release cannot keep detection enabled',
    );
    const originalAction = { ...actions[0] };
    await page.route('**/actions/release', (route) => route.fulfill({
      status: 409,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'conflict', message: 'action missing or revision changed' } }),
    }));
    await page.locator('#release-confirm').click();
    await page.waitForFunction(() => document.getElementById('release-dialog').textContent.includes('conflict'));
    assert.equal(await page.locator('#release-dialog').isVisible(), true);
    assert.equal(await page.locator('#release-confirm').isDisabled(), true, 'Stale release requires a fresh preview');
    assert.equal(targets[0].quality_paused, true);
    await capture('release-conflict-page.png');
    await page.unroute('**/actions/release');
    actions[0].action_revision = 2;
    await page.route('**/actions/preview', (route) => route.fulfill({
      status: 503, contentType: 'application/json', body: '{"error":"preview storage unavailable"}',
    }));
    await page.locator('#release-refresh').click();
    await page.waitForFunction(() => document.getElementById('release-feedback').textContent.includes('预览未完成'));
    assert.equal(await page.locator('#release-confirm').isDisabled(), true);
    assert(!(await page.locator('#release-feedback').textContent()).includes('已重新读取'));
    await page.unroute('**/actions/preview');
    const previewGate = deferred();
    const previewRequested = deferred();
    await page.route('**/actions/preview', async (route) => {
      previewRequested.resolve();
      await previewGate.promise;
      await route.continue();
    });
    await page.locator('#release-refresh').click();
    await previewRequested.promise;
    await page.locator('[data-close=release-dialog]').first().click();
    previewGate.resolve();
    await page.waitForFunction(() => !document.getElementById('release-refresh').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#release-dialog').isVisible(), false, 'A late preview cannot reopen a closed release dialog');
    await page.unroute('**/actions/preview');
    await page.locator('[data-action-preview]').click();
    await page.waitForFunction(() => !document.getElementById('release-confirm').disabled);
    robustnessChecks.push('failed preview cannot claim refreshed evidence and a delayed preview cannot reopen a closed dialog');
    await page.locator('#release-confirm').click();
    await page.waitForFunction(() => document.getElementById('release-feedback').textContent.includes('已无此暂停归属'));
    robustnessChecks.push('409 release conflict disables stale confirmation and rebinds a refreshed action revision');
    assert.equal(targets[0].manual_disabled, true);
    assert.equal(rules[0].enabled, false);
    assert.equal(rules[0].status, 'paused');
    const releaseCall = calls.find((call) => call.path.endsWith('/actions/release'));
    assert.deepEqual(Object.keys(releaseCall.body).sort(), [
      'action_id',
      'action_revision',
      'pause_rule',
      'rule_id',
    ]);
    assert.equal(releaseCall.body.action_revision, 2);
    await capture('released-still-restricted-page.png');
    await page.locator('[data-close=release-dialog]').first().click();
    actions = [{ ...originalAction, action_id: 'pause-uncertain', action_revision: 3 }];
    targets[0].quality_paused = true;
    targets[0].reasons = ['manual_disabled', 'quality_pause'];
    rules[0].status = 'awaiting_release';
    rules[0].action_id = 'pause-uncertain';
    await page.locator('#refresh').click();
    await page.locator('[data-action-preview]').click();
    let uncertainPosts = 0;
    await page.route('**/actions/release', (route) => {
      uncertainPosts++;
      actions = [];
      targets[0].quality_paused = false;
      targets[0].reasons = ['manual_disabled'];
      rules[0].status = 'paused';
      rules[0].action_id = '';
      rules[0].blocked_auth_id = '';
      rules[0].fail_count = 0;
      return route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'storage_unavailable', message: 'owned pause released; remaining restrictions await reconciliation' } }),
      });
    });
    await page.locator('#release-confirm').click();
    await page.waitForFunction(() => /未确认|不确定|待核对|待确认|待对账/.test(document.getElementById('release-dialog').textContent));
    assert.equal(await page.locator('#release-confirm').isDisabled(), true);
    assert.equal(await page.locator('#release-dialog').isVisible(), true);
    assert.match(await page.locator('#release-dialog').textContent(), /未确认|不确定|待核对|待确认|待对账/);
    await capture('release-uncertain-page.png');
    await page.route('**/targets?limit=100', (route) => route.fulfill({
      status: 503, contentType: 'application/json', body: '{"error":"remaining restrictions unavailable"}',
    }));
    await page.locator('#release-refresh').click();
    await page.waitForFunction(() => document.getElementById('release-feedback').textContent.includes('状态核对失败'));
    assert.equal(await page.locator('#release-confirm').isDisabled(), true);
    assert.match(await page.locator('#release-feedback').textContent(), /仍待核对/);
    await page.unroute('**/targets?limit=100');
    await page.locator('#release-refresh').click();
    await page.waitForFunction(() => document.getElementById('release-feedback').textContent.includes('已无此暂停归属'));
    await page.waitForFunction(() => document.querySelectorAll('[data-action-preview]').length === 0);
    assert.equal(uncertainPosts, 1, 'Reconciliation only reads; it must not retry the release');
    assert.equal(targets[0].manual_disabled, true);
    assert.equal(rules[0].enabled, false);
    assert.equal(await page.locator('#notice').evaluate((node) => node.classList.contains('error') && !node.classList.contains('hidden')), false, 'Reconciled release clears the stale global error');
    if (await page.locator('#release-dialog').isVisible()) await page.locator('[data-close=release-dialog]').first().click();
    await page.unroute('**/actions/release');
    robustnessChecks.push('uncertain 503 release is reconciled through reads without repeating mutation or clearing other restrictions');

    const attack = `<style>circle{opacity:.7}</style><svg viewBox="0 0 240 160"><circle cx="120" cy="80" r="50" fill="#22604a"/></svg><script>parent.document.body.dataset.compromised='yes';fetch('https://example.invalid/leak')</script><img src="https://example.invalid/image"><meta http-equiv="refresh" content="0;url=https://example.invalid/nav"><a href="https://example.invalid/link">Link<set attributeName="href" to="https://example.invalid/animated"/></a><form action="https://example.invalid/form"></form>`;
    runs.unshift({
      id: 'visual-run',
      status: 'completed',
      source: 'manual_test',
      created_at: created,
      config: { ...config, models:['mock-visual'], question_id: 'visual-pelican-html', judge:{mode:'manual'} },
      samples: [
        {
          model: 'mock-visual',
          index: 0,
          execution_status: 'completed',
          quality_verdict: 'unknown',
          output: attack,
          total_ms: 430,
          ttft_ms: null,
          usage: null,
          comparable: false,
        },
      ],
      action_status: 'not_applicable',
    });
    const external = [];
    await page.route('https://example.invalid/**', (route) => {
      external.push(route.request().url());
      route.abort();
    });
    await page.locator('#refresh').click();
    await page.locator('[data-page=manual]').click();
    await history();
    await page.locator('[data-run=visual-run]').click();
    await capture('insufficient-evidence-page.png');
    assert.equal(
      await page.locator('.sample pre').textContent(),
      attack,
      'Raw HTML must be displayed as text',
    );
    await page.locator('[data-preview-sample]').click();
    const frame = page.frameLocator('#safe-preview');
    await frame.locator('svg circle').waitFor();
    assert.equal(
      await frame.locator('svg circle').evaluate((element) => getComputedStyle(element).opacity),
      '0.7',
      'Parent CSP must permit sandbox inline styles',
    );
    assert.equal(await page.locator('#safe-preview').getAttribute('sandbox'), '');
    assert.equal(await frame.locator('script,form,iframe,meta[http-equiv=refresh]').count(), 0);
    assert.equal(await frame.locator('[src],[href],a,area,map,set').count(), 0);
    assert.equal(await page.locator('#safe-preview').getAttribute('inert'), null);
    assert.equal(await page.locator('body').getAttribute('data-compromised'), null);
    assert.deepEqual(external, []);
    await verifyDialogPadding('preview-dialog');
    await page.evaluate(() => scrollTo(0, 0));
    await takeScreenshot({
      animations: 'disabled',
      path: path.join(output, 'sandbox-preview.png'),
      fullPage: true,
    });
    await page.locator('[data-close=preview-dialog]').click();
    const longModel = 'offline-research-model-with-a-very-long-deployment-and-reasoning-variant-2026-11-14';
    const comparisonSamples = [longModel, 'mock-reasoner'].flatMap((model, modelIndex) =>
      [0, 1].map((index) => ({
        model, index, execution_status: 'completed',
        quality_verdict: modelIndex === 0 && index === 1 ? 'unknown' : modelIndex === 1 ? 'incorrect' : 'correct',
        comparable: !(modelIndex === 0 && index === 1),
        output: index ? `// Offline synthetic long output\nconst evidence = "${'condition_'.repeat(30)}";\n${'Only complete and comparable evidence can support a decision.\n'.repeat(14)}` : '20',
        total_ms: 650, ttft_ms: null, usage: null,
        reason: modelIndex === 0 && index === 1 ? 'truncated output; evidence incomplete' : 'offline comparison sample',
        evidence: { verified: modelIndex !== 0 || index !== 1, requested_model: model, resolved_model: 'offline-mapped-model', conditions_changed: false },
      })),
    );
    runs.unshift({
      id: 'comparison-run', status: 'completed', source: 'scheduled_probe', created_at: created,
      config: { ...config, models: [longModel, 'mock-reasoner'], samples: 2 },
      samples: comparisonSamples, action_status: 'not_applicable',
    });
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    await history();
    await page.locator('#history-list [data-run=comparison-run]').click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('comparison-run'));
    assert.equal(await page.locator('.model-result:visible').count(), 2);
    assert.equal(await page.locator('.sample:visible').count(), 4);
    await verifyHistoryScrollIsolation();
    await page.locator('#sample-filter').selectOption('1');
    assert.equal(await page.locator('.sample:visible').count(), 2);
    assert.equal(await page.locator('.sample[data-sample="1"]:visible').count(), 2);
    assert.equal(await page.locator('[data-review-sample="3"]').count(), 1, 'Review uses original sample array index after filtering');
    const expand = page.locator('[data-expand-sample]').first();
    await expand.click();
    assert.equal(await expand.getAttribute('aria-expanded'), 'true');
    assert.equal(await page.locator('.answer-text.expanded').evaluate((node) => node.scrollHeight <= node.clientHeight + 1), true);
    await expand.click();
    assert.equal(await expand.getAttribute('aria-expanded'), 'false');
    await capture('comparison-page.png');
    await pairedCapture('extra-comparison-desktop-light');
    await pairedCapture('extra-comparison-desktop-dark', 1440, 900, 'dark');
    await pairedCapture('extra-comparison-desktop-white', 1440, 900, 'white');
    await pairedCapture('extra-comparison-mobile-light', 390, 844);
    await page.locator('#theme-toggle').selectOption('dark');
    await capture('comparison-dark-page.png');
    await page.locator('#theme-toggle').selectOption('light');
    await page.locator('#compare-model-b').selectOption('');
    assert.equal(await page.locator('.sample:visible').count(), 1);
    await page.locator('#compare-model-a').selectOption('mock-reasoner');
    await page.locator('#compare-model-b').selectOption(longModel);
    assert.equal(await page.locator('.model-result:visible').count(), 2);
    assert.equal(await page.locator('.model-result:visible').first().getAttribute('data-model'), 'mock-reasoner', 'Primary selected model leads the reading order');
    await page.setViewportSize({ width: 390, height: 844 });
    const taskMenu = page.locator('#task-actions');
    await page.waitForFunction(() => !document.getElementById('task-actions').open);
    assert.equal(await taskMenu.getAttribute('open'), null, 'Phone task actions start collapsed');
    await taskMenu.locator('summary').click();
    assert.equal(await page.locator('#run-export').isVisible(), true);
    await page.locator('#run-export').focus();
    await page.keyboard.press('Escape');
    assert.equal(await taskMenu.getAttribute('open'), null);
    assert.equal(await taskMenu.locator('summary').evaluate((node) => document.activeElement === node), true, 'Escape returns focus to the action disclosure');
    await taskMenu.locator('summary').click();
    await page.locator('#page-manual .view-nav').click({ position: { x: 2, y: 2 } });
    assert.equal(await taskMenu.getAttribute('open'), null, 'Clicking outside closes phone task actions');
    robustnessChecks.push('phone task actions open explicitly, close on Escape or outside click and return keyboard focus to their disclosure');
    await capture('mobile-comparison-page.png');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'Long models and long code cannot overflow the page');
    await page.setViewportSize({ width: 1440, height: 1080 });
    robustnessChecks.push('model comparison and sample filtering preserve original review indices and contain long output on mobile');
    for (const width of [360, 390, 768, 1024, 1280, 1440]) {
      await page.setViewportSize({ width, height: width < 768 ? 844 : 900 });
      assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, `Comparison must fit ${width}px`);
      if (width < 768) {
        const smallInputs = await page.locator('input:visible,select:visible,textarea:visible').evaluateAll((nodes) => nodes.filter((node) => !['checkbox', 'radio'].includes(node.type) && parseFloat(getComputedStyle(node).fontSize) < 16).map((node) => node.id));
        assert.deepEqual(smallInputs, [], 'Phone fields use at least 16px text');
      }
    }
    assert.equal(await page.evaluate(() => CSS.supports('grid-template-rows', 'subgrid')), true, 'Verified Chromium supports comparison subgrid');
    await page.setViewportSize({ width: 720, height: 450 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, '200% zoom equivalent viewport reflows without horizontal page overflow');
    await page.setViewportSize({ width: 1440, height: 1080 });
    robustnessChecks.push('comparison reflows at 360/390/768/1024/1280/1440px and a 720×450 CSS viewport representing 200% zoom; Chromium subgrid verified');
    await operations('activity');
    assert.equal(await page.locator('#operations-history .empty-line').count(), 0, 'Audit history must not display an empty operational-history state');
    await captureSurfaceThemes('activity', '#ops-activity');
    robustnessChecks.push('audit-only operational history does not show an empty state');
    await operations();
    await page.locator('#theme-toggle').selectOption('dark');
    assert.equal(await page.locator('html').getAttribute('data-theme'), 'dark');
    await takeScreenshot({
      animations: 'disabled',
      path: path.join(output, 'operations-dark-page.png'),
      fullPage: true,
    });
    await page.evaluate(() => {
      location.hash = '#manual';
    });
    await page.locator('#page-manual').waitFor({ state: 'visible' });
    robustnessChecks.push('CPA warm light, dark and white themes with task navigation and hash navigation');
    await setup();

    await page.locator('#target').selectOption('mock-credential-b');
    await selectModel('mock-reasoner');
    await page.locator('#question').selectOption('custom');
    await page.locator('#prompt').fill('A custom prompt that must survive a refresh');
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#prompt').inputValue(), 'A custom prompt that must survive a refresh');
    robustnessChecks.push('refresh preserves unsaved custom question');
    await page.locator('#question').selectOption('logic-candy');

    await page.route('**/metrics', (route) =>
      route.fulfill({
        status: 503,
        contentType: 'application/json',
        body: JSON.stringify({
          error: { code: 'storage_unavailable', message: 'metrics storage unavailable' },
        }),
      }),
    );
    await page.locator('#refresh').click();
    await page.waitForFunction(() =>
      document.getElementById('notice').textContent.includes('storage_unavailable'),
    );
    assert.equal(await page.locator('#run-submit').isDisabled(), true);
    assert((await page.locator('#data-state').textContent()).includes('失败'));
    await capture('management-error-page.png');
    await page.unroute('**/metrics');
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    assert.equal(await page.locator('#run-submit').isDisabled(), false);
    robustnessChecks.push('management storage failure is visible and execution waits for refreshed state');

    const submitGate = deferred();
    const submitted = deferred();
    await page.route('**/model-quality/runs', async (route) => {
      if (route.request().method() !== 'POST') return route.continue();
      submitted.resolve();
      await submitGate.promise;
      await route.continue();
    });
    await page.evaluate(value => { window.__expectedRun=value; }, sequence+1);
    const beforeDouble = calls.filter((call) => call.method === 'POST' && call.path.endsWith('/runs')).length;
    await page.locator('#run-submit').click();
    await submitted.promise;
    await page.locator('#run-form').evaluate((form) => {
      form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }));
    });
    assert.equal(await page.locator('#run-submit').isDisabled(), true);
    submitGate.resolve();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('run-' + String(window.__expectedRun)));
    await page.unroute('**/model-quality/runs');
    assert.equal(
      calls.filter((call) => call.method === 'POST' && call.path.endsWith('/runs')).length,
      beforeDouble + 1,
    );
    robustnessChecks.push('duplicate submissions are coalesced while request is pending');

    runs.unshift({ ...pending, id: 'stale-run', status: 'completed' });
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    const staleGate = deferred();
    const staleRequested = deferred();
    const staleFinished = deferred();
    await page.route('**/runs/detail?id=stale-run', async (route) => {
      staleRequested.resolve();
      await staleGate.promise;
      await route.continue();
      staleFinished.resolve();
    });
    await history();
    await page.locator('[data-run=stale-run]').first().click();
    await staleRequested.promise;
    await history();
    await page.locator('[data-run=visual-run]').first().click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('visual-run'));
    staleGate.resolve();
    await staleFinished.promise;
    await page.unroute('**/runs/detail?id=stale-run');
    await page.evaluate(
      () => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))),
    );
    assert((await page.locator('#run-frozen-data').textContent()).includes('visual-run'));
    robustnessChecks.push('late task details cannot replace the newly selected task');

    {
      await page.clock.install();
      const reading = { id: 'reading-run', status: 'running', source: 'scheduled_probe', created_at: created, config: { ...config, models: [longModel, 'mock-reasoner'], samples: 2 }, samples: structuredClone(comparisonSamples), action_status: 'not_applicable' };
      reading.samples[1].reason = '';
      runs.unshift(reading);
      let readingPolls = 0;
      await page.route('**/runs/detail?id=reading-run', async (route) => { readingPolls++; await route.continue(); });
      await page.locator('#refresh').click();
      await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
      await history();
      await page.locator('[data-run=reading-run]').first().click();
      await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('reading-run'));
      await page.locator('#sample-filter').selectOption('1');
      await page.locator('.sample:visible .review-disclosure summary').first().click();
      await page.locator('#review-note-1').fill('未保存的轮询评审草稿');
      await page.locator('[data-expand-sample="1"]').click();
      await page.locator('.sample:visible .answer-text').last().evaluate((node) => { node.scrollTop = 120; });
      await page.locator('#review-note-1').focus();
      await page.locator('#review-note-1').evaluate((node) => node.setSelectionRange(2, 6));
      const probeState = () => page.evaluate(() => ({ active: document.activeElement.id, note: document.getElementById('review-note-1').value, selection: [document.getElementById('review-note-1').selectionStart, document.getElementById('review-note-1').selectionEnd], reviewOpen: document.getElementById('review-note-1').closest('details').open, expanded: document.querySelector('[data-expand-sample="1"]').getAttribute('aria-expanded'), scroll: [...document.querySelectorAll('.sample .answer-text')].at(-1).scrollTop, filter: document.getElementById('sample-filter').value }));
      const before = await probeState();
      reading.samples[0].output = 'Unrelated sample completed during polling';
      reading.samples[1].reason = 'exact answer';
      await page.clock.runFor(1200);
      await page.waitForFunction(() => [...document.querySelectorAll('.sample pre')].some((node) => node.textContent === 'Unrelated sample completed during polling'));
      const after = await probeState();
      fs.writeFileSync(path.join(output, 'polling-probe.json'), JSON.stringify({ before, after, readingPolls }, null, 2) + '\n');
      assert.deepEqual(after, before, 'Polling preserves focused review selection, draft, disclosure, expanded answer, internal scroll and filter');
      assert.equal(await page.locator('.sample:visible .sample-reason').first().isVisible(), true);
      assert.equal(await page.locator('.sample:visible .sample-reason').first().textContent(), '答案与冻结参考答案一致');
      robustnessChecks.push('a previously empty sample reason becomes visible when polling supplies a known explanation');
      robustnessChecks.push('controlled polling preserves focused review selection, unsaved note, disclosure, expanded output, internal scroll and comparison filter');
      await page.locator('.sample:visible .answer-text').first().evaluate((node) => {
        const range = document.createRange(); range.setStart(node.firstChild, 4); range.setEnd(node.firstChild, 24);
        const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
      });
      const selectedOutput = await page.evaluate(() => getSelection().toString());
      assert(selectedOutput.length > 0);
      reading.status = 'completed';
      await page.clock.runFor(1200);
      await page.waitForFunction(() => document.getElementById('run-meta').textContent.includes('已完成'));
      assert.equal(await page.evaluate(() => getSelection().toString()), selectedOutput, 'Finishing a run preserves output text selection');
      robustnessChecks.push('output text selection survives a background task finishing');
      await page.unroute('**/runs/detail?id=reading-run');
      await history();
      await page.locator('[data-run=visual-run]').first().click();
      await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('visual-run'));
      assert.equal(await page.locator('#sample-filter').inputValue(), 'all');
      assert.equal(await page.locator('.answer-text.expanded').count(), 0);
      assert.equal(await page.locator('#review-note-0').count(), 0);
      robustnessChecks.push('per-task review and comparison state resets when selecting another run');
      await page.clock.resume();
    }

    await page.clock.install();
    const polling = { ...pending, id: 'polling-run', status: 'running' };
    runs.unshift(polling);
    let pollReads = 0;
    await page.route('**/runs/detail?id=polling-run', async (route) => {
      pollReads++;
      if (pollReads === 2)
        return route.fulfill({
          status: 503,
          contentType: 'application/json',
          body: '{"error":"temporary read failure"}',
        });
      return route.continue();
    });
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    await history();
    await page.locator('[data-run=polling-run]').first().click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('polling-run'));
    const postsBeforePoll = calls.filter(
      (call) => call.method === 'POST' && call.path.endsWith('/runs'),
    ).length;
    await page.clock.runFor(1200);
    await page.waitForFunction(() => document.getElementById('run-feedback').textContent.includes('自动重试查询'));
    polling.status = 'completed';
    await page.clock.runFor(2400);
    await page.waitForFunction(() => document.getElementById('run-progress').parentElement.hidden &&
      document.getElementById('run-cancel').classList.contains('hidden'));
    assert.equal(pollReads, 3);
    assert.equal(await page.locator('#run-feedback').isVisible(), false, 'Recovered progress clears the stale query error');
    assert.equal(
      calls.filter((call) => call.method === 'POST' && call.path.endsWith('/runs')).length,
      postsBeforePoll,
    );
    await page.unroute('**/runs/detail?id=polling-run');
    await page.clock.resume();
    robustnessChecks.push(
      'poll failures retry with backoff using a controlled clock and never resubmit runs',
    );

    const stressModels = Array.from({ length: 8 }, (_, index) => `offline-same-prefix-deployment-model-${'long-name-'.repeat(10)}variant-${index + 1}`);
    const stressOutput = ('Readable offline evidence with a bounded response.\n').repeat(6000).slice(0, 262144);
    const stressRun = { id: 'stress-run', status: 'completed', source: 'scheduled_probe', created_at: created, config: { ...config, models: stressModels, samples: 4 }, samples: stressModels.flatMap((model) => Array.from({ length: 4 }, (_, index) => ({ model, index, output: stressOutput, execution_status: 'completed', quality_verdict: 'unknown', comparable: false, total_ms: 0, usage: null }))), action_status: 'not_applicable' };
    runs.unshift(stressRun);
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    await history();
    const stressStarted = performance.now();
    await page.locator('[data-run=stress-run]').first().click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('stress-run'));
    assert.equal(await page.locator('.sample').count(), 32);
    assert.equal(await page.locator('.model-result:visible').count(), 2);
    assert.equal(await page.locator('.sample:visible').count(), 8);
    await page.locator('#compare-model-b').selectOption(stressModels[7]);
    await page.locator('#sample-filter').selectOption('3');
    assert.equal(await page.locator('.sample:visible').count(), 2);
    assert.equal(await page.locator('.sample:visible .answer-text').first().textContent(), stressOutput);
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write']);
    const copied = page.locator('.sample:visible [data-copy-sample]').first();
    await copied.click();
    await page.waitForFunction(() => [...document.querySelectorAll('[data-copy-sample]')].some((node) => node.textContent.includes('已复制')));
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), stressOutput);
    assert.equal(await page.locator('#notice').isVisible(), false, 'Copy feedback stays by its action');
    await page.locator('.model-result:visible [data-copy-model]').last().click();
    assert.equal(await page.evaluate(() => navigator.clipboard.readText()), stressModels[7]);
    const stressEvidence = { models: 8, samples: 32, bytes_per_output: Buffer.byteLength(stressOutput), total_output_bytes: Buffer.byteLength(stressOutput) * 32, load_and_interact_ms: Math.round(performance.now() - stressStarted), output_preserved: true };
    fs.writeFileSync(path.join(output, 'stress-results.json'), JSON.stringify(stressEvidence, null, 2) + '\n');
    robustnessChecks.push('maximum 8 models and 32 samples with 256KiB outputs remain filterable, readable and losslessly copyable; full model IDs remain distinct');
    await page.setViewportSize({ width: 360, height: 800 });
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'Maximum content fits 360px');
    await capture('stress-mobile-page.png');
    const mobileSample = page.locator('.sample:visible').first();
    await mobileSample.locator('[data-expand-sample]').click();
    await mobileSample.locator('.answer-text').evaluate((node) => scrollTo(0, node.getBoundingClientRect().top + scrollY + 400));
    const mobileContext = await mobileSample.locator('.sample-heading').evaluate((node) => ({ top: node.getBoundingClientRect().top, model: node.querySelector('.sample-model-label')?.textContent, position: getComputedStyle(node).position }));
    assert.equal(mobileContext.position, 'sticky');
    assert(Math.abs(mobileContext.top) <= 1, 'Reading a long expanded answer keeps sample context at the top');
    assert.equal(mobileContext.model, stressModels[0]);
    await takeScreenshot({ path: path.join(output, 'mobile-reading-context.png'), animations: 'disabled' });
    robustnessChecks.push('mobile long-answer scrolling retains the model and sample identity in the single local sticky heading');
    await mobileSample.locator('[data-expand-sample]').click();
    const eightSamples = { ...stressRun, id: 'eight-samples-run', config: { ...stressRun.config, models: [stressModels[0]], samples: 8 }, samples: Array.from({ length: 8 }, (_, index) => ({ ...stressRun.samples[0], index })) };
    runs.unshift(eightSamples);
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    await history();
    await page.locator('[data-run=eight-samples-run]').first().click();
    await page.waitForFunction(() => document.getElementById('run-frozen-data').textContent.includes('eight-samples-run'));
    assert.equal(await page.locator('.sample:visible').count(), 8);
    await page.locator('#sample-filter').selectOption('7');
    assert.equal(await page.locator('.sample:visible').count(), 1);
    assert.equal(await page.locator('.sample:visible').getAttribute('data-sample'), '7');
    robustnessChecks.push('the maximum eight samples per model render and filter to sample eight without index drift');
    runs.push(...Array.from({ length: 25 }, (_, index) => ({ ...pending, id: `page-run-${index}`, status: 'completed' })));
    await page.locator('#refresh').click();
    await page.waitForFunction(() => !document.getElementById('refresh').hasAttribute('aria-busy'));
    await history();
    const firstHistory = await page.locator('#history-list').textContent();
    await page.locator('#history-next').click();
    await page.waitForFunction((previous) => document.getElementById('history-list').textContent !== previous, firstHistory);
    assert.equal(await page.locator('#history-list [data-run]').count(), 10);
    await page.locator('#history-prev').click();
    await page.waitForFunction((previous) => document.getElementById('history-list').textContent === previous, firstHistory);
    robustnessChecks.push('history pagination advances and returns without mixing task records');
    await page.locator('[data-run=polling-run]').first().click();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.evaluate(() => scrollTo(0, 0));
    await takeScreenshot({
      animations: 'disabled',
      path: path.join(output, 'mobile-page.png'),
      fullPage: true,
    });
    assert.equal(
      await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth),
      true,
      'Mobile page must not overflow horizontally',
    );
    await operations();
    assert.equal(await page.locator('#page-operations').isVisible(), true);
    await page.locator('[data-page=manual]').click();
    await setup();
    await capture('mobile-setup-page.png');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    await page.setViewportSize({ width: 820, height: 1180 });
    await capture('tablet-page.png');
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
    robustnessChecks.push('mobile and tablet task navigation have no horizontal page overflow');

    await page.setViewportSize({ width: 1440, height: 1080 });
    const disconnectGate = deferred();
    const disconnectRequested = deferred();
    await page.route('**/targets?limit=100', async (route) => {
      disconnectRequested.resolve();
      await disconnectGate.promise;
      await route.continue().catch(() => {});
    });
    await page.locator('#refresh').click();
    await disconnectRequested.promise;
    await page.locator('#connection-top').click();
    await page.locator('#disconnect').click();
    disconnectGate.resolve();
    await page.unroute('**/targets?limit=100');
    await page.waitForFunction(() => document.getElementById('connection-state').textContent === '未连接');
    assert(await page.locator('.app-header #connection-open').isVisible(), 'Disconnect restores the header connection action');
    assert.equal(await page.locator('#target option').count(), 1);
    assert.equal(await page.locator('#target').getAttribute('title') || '', '', 'Disconnect clears credential identity from the field title');
    assert.equal(await page.locator('#compare-model-a option,#compare-model-b option,#sample-filter option').count(), 0);
    assert.equal(await page.locator('.sample').count(), 0);
    assert.equal(await page.locator('#run-submit').isDisabled(), true);
    assert(!(await page.locator('body').textContent()).includes('mock-credential-a'));
    robustnessChecks.push(
      'disconnect aborts in-flight reads and removes credentials, history, outputs and dialogs',
    );

    await page.locator('#connection-open').click();
    await page.locator('#management-key').fill('offline-test-key');
    await page.locator('#connection-form button[type=submit]').click();
    await page.waitForFunction(() => document.getElementById('connection-state').textContent === '已连接');
    await page.route('**/metrics', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: '{"error":"expired"}' }),
    );
    await page.locator('#refresh').click();
    await page.waitForFunction(() => document.getElementById('connection-state').textContent === '未连接');
    assert.equal(await page.locator('#target option').count(), 1);
    assert((await page.locator('#notice').textContent()).includes('鉴权已失效'));
    await page.unroute('**/metrics');
    robustnessChecks.push('401 invalidates the whole management session without stale data');
    assert.equal(await page.evaluate(() => localStorage.length + sessionStorage.length), 0);
    assert.deepEqual(errors, [], 'No browser JavaScript errors');
    assert.deepEqual(sourceHashes(), initialSourceHashes, 'UI source must remain unchanged while browser validation is running');
    fs.writeFileSync(path.join(output, 'dialog-layout-results.json'), JSON.stringify({ result: 'passed', source_sha256: initialSourceHashes, browser: await browser.version(), scope: 'Real computed dialog bottom padding and scrolled content inset at 360/390px; safe-area inset is zero in desktop emulation', checks: dialogGeometry }, null, 2) + '\n');
    robustnessChecks.push('product dialogs retain usable computed padding and visible content inset at 360px and 390px');
    const report = {
      result: 'passed',
      verified_at: new Date().toISOString(),
      source_sha256: initialSourceHashes,
      viewport_evidence: viewportEvidence,
      history_geometry: historyGeometry,
      header_geometry: headerGeometry,
      capture_fixture_sha256: captureFixtures,
      browser: await browser.version(),
      platform: process.platform,
      architecture: process.arch,
      checks: [
        'static shell no API/model calls',
        'authenticated read-only connection',
        'locally saved management key',
        'manual fixed-auth request',
        'stream option',
        'running cancellation',
        'rule save no execution',
        'server audit history',
        'separate rule pause',
        'settings preserve immutable fields',
        'held target disables manual and rerun execution',
        'held target disables rule run and resume',
        'removed automatic recovery UI and payload fields',
        'manual release always keeps dependent rules paused',
        'action preview and CAS release',
        'operator disable retained',
        'HTML text escaping',
        'sandbox script and navigation suppression',
        'host CSP permits sandbox CSS',
        'responsive mobile layout',
        'no browser errors',
        ...robustnessChecks,
      ],
      api_calls: calls.length,
      screenshots: [...producedScreenshots].sort(),
      screenshot_sha256: Object.fromEntries([...producedScreenshots].sort().map((name) => [name, require('node:crypto').createHash('sha256').update(fs.readFileSync(path.join(output, name))).digest('hex')])),
    };
    fs.writeFileSync(path.join(output, 'ui-results.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  } catch (error) {
    await takeScreenshot({ path: path.join(output, 'failure.png'), fullPage: true }).catch(() => {});
    fs.writeFileSync(path.join(output, 'failure.json'), JSON.stringify({ error: error.stack, browserErrors: errors, lastCalls: calls.slice(-6), text: await page.locator('body').innerText() }, null, 2));
    throw error;
  } finally {
    await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
