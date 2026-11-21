// Restart separate Chromium processes on a temporary profile containing synthetic data only.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const profile = await fs.mkdtemp(path.join(os.tmpdir(),'model-quality-fake-browser-'));
  const server = spawn(process.execPath,[path.join(__dirname,'login_ui_fixture.cjs')],{stdio:['ignore','pipe','inherit']});
  const exit = once(server,'exit');
  let context;
  try {
    const [line] = await once(createInterface({input:server.stdout}),'line');
    const info = JSON.parse(line), {origin,hostname,port} = new URL(info.url);
    assert.equal(hostname,'127.0.0.1'); assert.notEqual(port,'8317');
    const prefix = '/v0/management/plugins/model-quality';
    const writes = [], lookups = [], processes = [], errors = [];
    let completed = false;
    for (const first of [true,false]) {
      context = await chromium.launchPersistentContext(profile, {executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
      const cdp = await context.browser().newBrowserCDPSession();
      const {processInfo} = await cdp.send('SystemInfo.getProcessInfo');
      processes.push(processInfo.find(row => row.type === 'browser').id);
      await cdp.detach();
      await context.route('**/*',async route => {
        const req = route.request(), url = new URL(req.url());
        assert.equal(url.origin, origin);
        if (!url.pathname.startsWith(prefix + '/')) return route.continue();
        assert.equal(req.headers().authorization,'Bearer ' + info.management_key);
        if (req.method() === 'POST') {
          assert(first); assert.equal(url.pathname,prefix + '/login/accounts/inspect');
          assert.equal(writes.length,0);
          writes.push(req.headers()['idempotency-key']);
          return route.abort('connectionfailed');
        }
        assert.equal(req.method(),'GET');
        if (url.pathname === prefix + '/login/batches') {
          const id = url.searchParams.get('id'); assert.equal(id,writes[0]); lookups.push({first,id});
          return route.fulfill({json:{id,kind:'inspection',status:completed ? 'completed' : 'running',completed:Number(completed),total:1}});
        }
        return route.continue();
      });
      const page = context.pages()[0]; page.setDefaultTimeout(6500);
      page.on('pageerror',error => errors.push(error.message));
      if (first) {
        await page.goto(origin + '/management.html#/auth-files');
        await page.evaluate(([p,k]) => localStorage.setItem('model-quality:management-key:' + p,k),[prefix,info.management_key]);
      }
      await page.goto(origin + '/#login');
      if (first) {
        const lost = page.waitForEvent('requestfailed', r => r.url().endsWith('/login/accounts/inspect'));
        await page.click('#account-inspect'); await lost;
      } else {
        await page.locator('#account-batch-note').filter({hasText:'已完成'}).waitFor();
        assert.match(await page.locator('#account-batch-note').innerText(), new RegExp(writes[0]));
      }
      await context.close(); context = null;
      completed = true;
    }
    assert.notEqual(processes[0],processes[1]); assert.equal(writes.length,1);
    assert(lookups.some(row => !row.first)); assert.deepEqual(errors,[]);
    console.log(JSON.stringify({separateBrowserProcesses:2,sameSyntheticProfile:true,originalCommands:writes.length,originalIDReadsAfterRelaunch:lookups.filter(row => !row.first).length,automaticPOSTsAfterRelaunch:0,backendConfigurationWrites:0}));
  } finally {
    await context?.close(); server.kill(); await exit; await fs.rm(profile,{recursive:true,force:true});
  }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
