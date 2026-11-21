// Serve the actual plugin and a separate parent on different loopback origins.
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const http = require('node:http');
const fs = require('node:fs/promises');
const path = require('node:path');
const { createInterface } = require('node:readline');
const { setTimeout: delay } = require('node:timers/promises');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function waitUntil(condition) {
  const end = Date.now() + 5000;
  while (!condition() && Date.now() < end) await delay(25);
  assert(condition(), 'Expected a visible geometry or an authenticated request');
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname,'login_ui_fixture.cjs')], { stdio: ['ignore','pipe','inherit'] });
  const stopped = once(fixture,'exit');
  let browser, host, release;
  try {
    const [line] = await once(createInterface({input:fixture.stdout}),'line');
    const info = JSON.parse(line), child = new URL(info.url);
    assert.equal(child.hostname,'127.0.0.1'); assert.notEqual(child.port,'8317');
    host = http.createServer((req,res) => {
      res.writeHead(200, {'Content-Type':'text/html; charset=utf-8','Cache-Control':'no-store'});
      res.end('<!doctype html><body style="margin:0"><iframe id="plugin" style="width:100%;height:80vh" src="' + child.origin + '/#login"></iframe></body>');
    });
    host.listen(0,'127.0.0.1'); await once(host,'listening');
    const parent = 'http://localhost:' + host.address().port;
    assert.notEqual(new URL(parent).port,'8317');
    browser = await chromium.launch({executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
    const page = await browser.newPage();
    page.setDefaultTimeout(6000);
    const signals = [], reads = [], unexpected = [], prefix = '/v0/management/plugins/model-quality';
    let hold;
    page.on('console', message => {
      if (message.text().startsWith('fixture-intersection ')) signals.push(JSON.parse(message.text().slice(21)));
    });
    await page.addInitScript(({origin,key,storage}) => {
      if (location.origin !== origin) return;
      localStorage.setItem(storage,key);
      document.addEventListener('DOMContentLoaded', () => {
        const observer = new IntersectionObserver(([entry]) => console.log('fixture-intersection ' + JSON.stringify({
          intersects:entry.isIntersecting, documentHidden:document.hidden, rootBoundsAvailable:!!entry.rootBounds
        })));
        observer.observe(document.documentElement);
      });
    }, {origin:child.origin,key:info.management_key,storage:'model-quality:management-key:' + prefix});
    await page.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      if (![child.origin,parent].includes(url.origin) || request.method() !== 'GET') {
        unexpected.push(request.method()); return route.abort();
      }
      if (url.origin === child.origin && url.pathname.startsWith(prefix + '/')) {
        assert.equal(request.headers().authorization,'Bearer ' + info.management_key);
        const resource = url.pathname.slice(prefix.length); reads.push(resource);
        if (hold && resource === '/login/jobs') {
          const gate = hold; hold = null; gate.started(request); await gate.release;
        }
      }
      return route.continue();
    });
    await page.goto(parent);
    const frame = page.frames().find(frame => frame.parentFrame() && frame.url().startsWith(child.origin));
    assert(frame);
    await frame.locator('#account-history-open:enabled').click();
    await frame.locator('[data-task-log-update]').filter({hasText:'更新于'}).waitFor();
    const parentAccess = await frame.evaluate(() => { try { return window.parent.document.title; } catch (error) { return error.name; } });
    assert.equal(parentAccess,'SecurityError');
    await waitUntil(() => signals.at(-1)?.intersects === true);
    const started = new Promise(resolve => { hold = { started:resolve, release:new Promise(done => { release = done; }) }; });
    await frame.locator('#login-refresh').click(); const request = await started;
    const style = type => page.locator('#plugin').evaluate((el,type) => {
      el.style.display = type === 'display' ? 'none' : '';
      el.style.opacity = type === 'opacity' ? '0' : '1';
      el.style.visibility = type === 'visibility' ? 'hidden' : 'visible';
      return new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done)));
    }, type);
    await style('display'); await waitUntil(() => signals.at(-1)?.intersects === false);
    const before = reads.length;
    release(); const response = await request.response();
    assert.equal(response.status(),200); await response.finished(); await delay(500);
    const hiddenReads = reads.slice(before); assert.deepEqual(hiddenReads,[]);
    await style(''); await waitUntil(() => signals.at(-1)?.intersects === true && reads.slice(before).includes('/login/logs'));
    const styleSignals = {};
    for (const property of ['opacity','visibility']) {
      const initial = signals.length;
      await style(property); await delay(200);
      styleSignals[property] = signals.slice(initial);
      assert.equal(signals.at(-1).intersects,true);
    }
    assert.deepEqual(unexpected,[]);
    const sha = async name => createHash('sha256').update(await fs.readFile(path.join(__dirname,'../plugin/ui',name))).digest('hex');
    const result = { chromium:browser.version(), differentHostsAndPorts:true, parentAccess, signals, hiddenReads, resumedLog:true,
      opacityZeroSignals:styleSignals.opacity, visibilityHiddenSignals:styleSignals.visibility, writes:0,
      source_sha256:{'login.js':await sha('login.js'),'app.js':await sha('app.js'),'index.html':await sha('index.html')} };
    if (process.env.UI_VISIBILITY_OUTPUT) await fs.writeFile(process.env.UI_VISIBILITY_OUTPUT,JSON.stringify(result,null,2)+'\n');
    console.log(JSON.stringify(result));
  } finally {
    release?.(); await browser?.close();
    if (host) { host.closeAllConnections(); await new Promise(done => host.close(done)); }
    fixture.kill(); await stopped;
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
