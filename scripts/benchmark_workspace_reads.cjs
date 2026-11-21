// Measure the first visible workspace against the pinned source baseline, using
// new browser contexts and memory-only, loopback fixtures for both builds.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { execFileSync, spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { performance } = require('node:perf_hooks');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

const baseline = 'd557de6ef52d977a114602cafa4711c60729e565';
const root = path.join(__dirname, '..');
const legacy = ['index.html','style.css','client.js','app.js','view.js','login.js'];
const median = values => values.slice().sort((a, b) => a - b)[Math.floor(values.length / 2)];
async function benchmark() {
  const temp = await fs.mkdtemp(path.join(os.tmpdir(), 'model-quality-ui-read-benchmark-'));
  let browser;
  try {
    for (const file of ['scripts/login_ui_fixture.cjs', ...legacy.map(name => 'plugin/ui/' + name)]) {
      await fs.mkdir(path.dirname(path.join(temp,file)), { recursive: true });
      await fs.writeFile(path.join(temp,file), execFileSync('git', ['show', baseline + ':' + file], { cwd: root, maxBuffer: 4 * 1024 * 1024 }));
    }
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const report = { baseline, browser: browser.version(), samples: 5, results: {} };
    for (const [name,directory] of [['before',temp], ['after',root]]) {
      const fixture = spawn(process.execPath, [path.join(directory,'scripts/login_ui_fixture.cjs')], { stdio: ['ignore','pipe','inherit'] });
      const stopped = once(fixture,'exit');
      try {
        const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
        const info = JSON.parse(line);
        const { origin, hostname, port } = new URL(info.url);
        assert.equal(hostname,'127.0.0.1'); assert.notEqual(port,'8317');
        report.results[name] = {};
        for (const workspace of ['login','manual','operations']) {
          const timings = [], readCounts = [], endpoints = new Set();
          for (let n=0; n<report.samples; n++) {
            const context = await browser.newContext();
            try {
              const page = await context.newPage();
              const prefix = '/v0/management/plugins/model-quality';
              let count = 0;
              await context.addInitScript(([prefix,key]) => {
                if (window === window.top) localStorage.setItem('model-quality:management-key:' + prefix, key);
              }, [prefix, info.management_key]);
              await page.route('**/*', async route => {
                const req = route.request(), u = new URL(req.url());
                assert.equal(u.origin, origin); assert.equal(req.method(), 'GET');
                if (u.pathname.startsWith(prefix + '/')) {
                  assert.equal(req.headers().authorization, 'Bearer ' + info.management_key);
                  endpoints.add(u.pathname.slice(prefix.length) + (u.searchParams.get('source') ? '?source=' + u.searchParams.get('source') : ''));
                  count++;
                }
                await route.continue();
              });
              const start = performance.now();
              await page.goto(origin + '/#' + workspace);
              const ready = workspace === 'login' ? '#account-import-open:enabled' :
                workspace === 'manual' ? '#target option[value="fixture-quality-primary"]' : '[data-rule-select="fixture-rule-observe"]';
              await page.locator(ready).waitFor({state:'attached'});
              timings.push(Math.round(performance.now() - start));
              await page.waitForLoadState('networkidle');
              readCounts.push(count);
            } finally { await context.close(); }
          }
          report.results[name][workspace] = { medianReadyMs: median(timings), readyMs: timings, reads: readCounts, resources: [...endpoints].sort() };
        }
      } finally { fixture.kill(); await stopped; }
    }
    console.log(JSON.stringify(report,null,2));
  } finally {
    await browser?.close(); await fs.rm(temp,{recursive:true,force:true});
  }
}
benchmark().catch(e => { console.error(e); process.exitCode=1; });
