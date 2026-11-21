// Drive durable command recovery through the embedded UI against a loopback-only fixture.
const assert = require('node:assert/strict');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const { createInterface } = require('node:readline');
const path = require('node:path');
const fs = require('node:fs/promises');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore','pipe','inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const info = JSON.parse(line);
    const { origin, hostname, port } = new URL(info.url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const prefix = '/v0/management/plugins/model-quality', metadata = {};
    const fixtures = [
      { name: 'inspection_lost_response', route: '/login/accounts/inspect', kind: 'inspection', workspace: 'login', receipt: '/login/batches' },
      { name: 'inspection_not_accepted', missing: true, route: '/login/accounts/inspect', kind: 'inspection', workspace: 'login', receipt: '/login/batches' },
      { name: 'upload_lost_response', route: '/login/conversion/upload', kind: 'conversion', workspace: 'login', receipt: '/login/batches' },
      { name: 'rule_run_lost_response', route: '/rules/run', kind: 'run', workspace: 'operations', receipt: '/rules/commands' },
      { name: 'rule_creation_lost_response', route: '/rules', kind: 'create', workspace: 'operations', receipt: '/rules/commands' },
      { name: 'rule_creation_not_accepted', missing: true, route: '/rules', kind: 'create', workspace: 'operations', receipt: '/rules/commands' },
    ];
    for (const scenario of fixtures) {
      const { name, route: action, kind, workspace, receipt, missing } = scenario;
      const context = await browser.newContext();
      try {
        let page = await context.newPage(); page.setDefaultTimeout(7000);
        const writes = [], reads = [], errors = [];
        const isAccount = workspace === 'login';
        const source = '{"opaque":"synthetic-conversion-only"}';
        let id, accepted = 0, admitted = false, savedParams = null, queried = 0, terminal = false;
        const record = () => isAccount ?
          { id, kind, status: terminal ? 'completed' : 'running', total: 1, completed: Number(terminal), items: kind === 'conversion' && terminal ? [{ index: 0, status: 'uploaded', enabled: false }] : [] } :
          { idempotency_key: id, kind, rule_id: kind === 'create' ? 'isolated-new-rule' : 'fixture-rule-observe', revision: 2,
            state: terminal ? 'completed' : 'accepted', ...(kind === 'run' && terminal ? { result: { count: 1, skipped: 0, scope_batch_id: 'isolated-batch', run_ids: ['isolated-only'] } } : {}) };
        page.on('pageerror', e => errors.push(e.message));
        await page.goto(origin + '/management.html#/auth-files');
        await page.evaluate(([prefix, key]) => localStorage.setItem('model-quality:management-key:' + prefix, key), [prefix, info.management_key]);
        await context.route('**/*', async handler => {
          const req = handler.request(), url = new URL(req.url()), resource = url.pathname.slice(prefix.length);
          assert.equal(url.origin, origin);
          if (!url.pathname.startsWith(prefix + '/')) return handler.continue();
          assert.equal(req.headers().authorization, 'Bearer ' + info.management_key);
          if (resource === receipt && req.method() === 'GET') {
            const requested = url.searchParams.get(isAccount ? 'id' : 'idempotency_key');
            assert.equal(requested, id); reads.push(requested); queried++;
            return admitted ? handler.fulfill({ json: record() }) : handler.fulfill({ status: 404, json: { code: 'command_not_accepted' } });
          }
          if (resource === '/rules/scope-preview') return handler.continue();
          if (resource === '/login/conversion/preview' && req.method() === 'POST') {
            assert.equal(req.postDataJSON().source, source);
            return handler.fulfill({ json: { source_ref: 'synthetic-opaque-source', items: [{ index: 0, status: 'ready', name: 'Synthetic upload' }] } });
          }
          if (req.method() === 'GET') return handler.continue();
          assert.equal(resource, action); assert.equal(req.method(), 'POST');
          const body = req.postDataJSON(), key = isAccount ? req.headers()['idempotency-key'] : body.idempotency_key;
          assert(key && (!id || key === id));
          if (isAccount) assert.equal(req.headers().prefer, 'respond-async');
          if (savedParams) assert.deepEqual(body, savedParams);
          savedParams = body; id = key; writes.push(key);
          if (!missing || writes.length > 1) {
            assert(!admitted); admitted = true; accepted++;
          }
          if (writes.length === 1) return handler.abort('connectionfailed');
          return handler.fulfill({ status: 202, json: kind === 'create' ? { id: record().rule_id, revision: 2 } : record() });
        });
        const execute = async () => {
          if (kind === 'inspection') return page.click('#account-inspect');
          if (kind === 'run') return page.click('[data-rule-run="fixture-rule-observe"]');
          if (kind === 'create') {
            await page.click('#rule-new'); await page.fill('#rule-name', 'isolated rule recovery');
            return page.click('#rule-form button[type=submit]');
          }
          await page.click('#account-convert-open');
          await page.click('#account-convert-paste summary');
          await page.fill('#account-convert-source', source);
          await page.click('#account-convert-preview');
          await page.click('#account-convert-upload');
        };
        await page.goto(origin + '/#' + workspace);
        const lost = page.waitForEvent('requestfailed', r => new URL(r.url()).pathname === prefix + action);
        await execute(); await lost;
        // sessionStorage belongs to the closed tab. Only the remembered browser
        // identity and opaque, persistent operation references survive here.
        await page.close(); page = await context.newPage(); page.setDefaultTimeout(7000);
        page.on('pageerror', e => errors.push(e.message));
        const first = page.waitForRequest(r => r.url().startsWith(origin + prefix + '/'));
        await page.goto(origin + '/#' + workspace);
        assert.equal(new URL((await first).url()).pathname, prefix + (isAccount ? '/login/accounts' : '/rules'));
        const note = page.locator(isAccount ? '#account-batch-note' : '#rule-command-note');
        await note.filter({ hasText: missing ? '尚未查到受理回执' : /执行中|已受理/ }).waitFor();
        assert.equal(writes.length, 1, 'A fresh tab with empty sessionStorage only reads the original receipt');
        assert(queried > 0 && reads.every(value => value === id));
        const persisted = await page.evaluate(() => JSON.stringify([Object.entries(sessionStorage), Object.entries(localStorage)]));
        assert(!persisted.includes(source) && !persisted.includes('isolated rule recovery'));
        if (missing) {
          const resubmitted = page.waitForResponse(r => r.request().method() === 'POST' && new URL(r.url()).pathname === prefix + action);
          await execute(); await resubmitted;
          assert.equal(writes.length, 2);
          assert.equal(new Set(writes).size, 1);
        }
        if (kind !== 'create' || !missing) {
          terminal = true; await page.click('#refresh'); await note.filter({ hasText: '已完成' }).waitFor();
        }
        assert.equal(accepted, 1); assert.deepEqual(errors, []);
        metadata[name] = { submissions: writes.length, logicalCommands: accepted, receiptReads: queried, lastPageClosed: true, automaticReplays: 0 };
      } finally { await context.close(); }
    }
    const pair = await browser.newContext();
    try {
      const first = await pair.newPage(), second = await pair.newPage();
      for (const page of [first, second]) page.setDefaultTimeout(7000);
      await first.goto(origin + '/management.html#/auth-files');
      await first.evaluate(([p,k]) => localStorage.setItem('model-quality:management-key:' + p, k), [prefix, info.management_key]);
      const pending = new Map(), recovered = new Set(), cancellationWrites = [];
      let releaseCancelRead;
      let delayedID, startDelayed, releaseDelayed;
      const delayedStarted = new Promise(resolve => { startDelayed = resolve; });
      let reopened;
      await pair.route('**/*', async handler => {
        const req = handler.request(), url = new URL(req.url()), route = url.pathname.slice(prefix.length);
        assert.equal(url.origin, origin);
        if (!url.pathname.startsWith(prefix + '/')) return handler.continue();
        assert.equal(req.headers().authorization, 'Bearer ' + info.management_key);
        if (route === '/login/batches' && req.method() === 'GET') {
          const id = url.searchParams.get('id');
          assert(pending.has(id));
          if (reopened && req.frame().page() === reopened) recovered.add(id);
          if (id === delayedID && !cancellationWrites.includes(id) && !releaseDelayed) {
            const old = {...pending.get(id)};
            startDelayed();
            await new Promise(resolve => { releaseDelayed = resolve; });
            return handler.fulfill({json:old});
          }
          if (cancellationWrites.includes(id) && pending.get(id).status === 'cancelling') {
            await new Promise(resolve => { releaseCancelRead = resolve; });
          }
          return handler.fulfill({ json: pending.get(id) });
        }
        if (req.method() === 'GET') return handler.continue();
        if (route === '/login/batches/cancel') {
          const id = req.postDataJSON().id;
          assert.equal(req.headers()['idempotency-key'], id + '-cancel');
          assert(!cancellationWrites.includes(id));
          cancellationWrites.push(id);
          Object.assign(pending.get(id), {status:'cancelling',cancel_requested:true});
          return handler.fulfill({json:{status:'cancelling',cancel_requested:true}});
        }
        if (route === '/login/conversion/preview') return handler.fulfill({ json: {
          source_ref:'synthetic-file-pair', items:[{index:0,name:'Fake',status:'ready'}],
        } });
        assert(['/login/accounts/inspect','/login/conversion/upload'].includes(route));
        assert.equal(req.headers().prefer, 'respond-async');
        const id = req.headers()['idempotency-key'];
        assert(id && !pending.has(id));
        const receipt = {id, kind:route.endsWith('/inspect') ? 'inspection':'conversion',status:'running',total:1,completed:0};
        pending.set(id, receipt);
        return handler.fulfill({ status:202, json:receipt });
      });
      await Promise.all([first.goto(origin + '/#login'),second.goto(origin + '/#login')]);
      await second.click('#account-convert-open'); await second.click('#account-convert-paste summary');
      await second.fill('#account-convert-source', '{"private":"DRAFT_ONLY_FIXTURE"}');
      await second.click('#account-convert-preview');
      await Promise.all([first.click('#account-inspect'), second.click('#account-convert-upload')]);
      await first.locator('#account-batch-note').filter({hasText:'执行中'}).waitFor();
      await second.locator('#account-convert-feedback').filter({hasText:'批次已受理'}).waitFor();
      assert.equal(pending.size,2);
      await Promise.all([first.close(),second.close()]);
      reopened = await pair.newPage();
      const both = [...pending.keys()].map(id => reopened.waitForResponse(r => r.url() === origin + prefix + '/login/batches?id=' + id));
      await reopened.goto(origin + '/#login'); await Promise.all(both);
      assert.deepEqual([...recovered].sort(), [...pending.keys()].sort());
      const batchNote = reopened.locator('#account-batch-note');
      for (const id of pending.keys()) await batchNote.filter({hasText:id}).waitFor();
      const uploadID = [...pending.keys()].find(id => pending.get(id).kind === 'conversion');
      const uploadLine = batchNote.locator('[data-command-id="' + uploadID + '"]');
      delayedID = uploadID;
      await reopened.click('#refresh'); await delayedStarted;
      const finalRead = reopened.waitForRequest(r => r.url().endsWith('/login/batches?id=' + uploadID));
      await uploadLine.getByRole('button', {name:'取消此批次'}).click();
      await uploadLine.filter({hasText:'取消请求中'}).waitFor();
      await finalRead;
      assert(releaseCancelRead);
      Object.assign(pending.get(uploadID), {status:'cancelled',total:2,completed:1,items:[{index:0,status:'uploaded',enabled:false}]});
      releaseCancelRead();
      await uploadLine.filter({hasText:'已取消'}).waitFor();
      assert.match(await uploadLine.innerText(), /1\/2/);
      assert.match(await uploadLine.innerText(), /已上传 1/);
      const staleRead = reopened.waitForResponse(r => r.url().endsWith('/login/batches?id=' + uploadID));
      releaseDelayed(); await (await staleRead).finished();
      await reopened.waitForSelector('#refresh:not([aria-busy=true])');
      assert.match(await uploadLine.innerText(),/已取消/);
      assert.equal(await uploadLine.getByRole('button').count(),0);
      assert.deepEqual(cancellationWrites,[uploadID]);
      const saved = await reopened.evaluate(() => JSON.stringify(Object.entries(localStorage)));
      assert(!saved.includes('DRAFT_ONLY_FIXTURE'));
      assert.equal(pending.size,2);
      metadata.independent_tabs = { tabsClosed:2, recoveredIDs:recovered.size, visibleReceipts:2, acceptedCommands:pending.size, automaticReplays:0 };
      metadata.cancel_ack = { cancellationWrites:1, immediateCancelling:true, completedUploadsVisible:1, lateRunningDidNotReopen:true };
    } finally { await pair.close(); }
    const http = await browser.newContext();
    try {
      const testOrigin = 'http://commands.test';
      await http.route('**/*', async route => {
        const url = new URL(route.request().url());
        assert.equal(url.origin,testOrigin);
        if (url.pathname === '/') return route.fulfill({contentType:'text/html',body:'<!doctype html><title>Isolated HTTP origin</title>'});
        assert(['/commands.js','/client.js'].includes(url.pathname));
        return route.fulfill({contentType:'text/javascript',body:await fs.readFile(path.join(__dirname,'../plugin/ui',url.pathname.slice(1)))});
      });
      const tabs = await Promise.all([http.newPage(),http.newPage()]);
      await Promise.all(tabs.map(async (page,n) => {
        await page.goto(testOrigin);
        await page.evaluate(async n => {
          if (isSecureContext || crypto.subtle || navigator.locks) throw Error('This fixture requires an insecure origin.');
          const { CommandReceipts, creationSlot } = await import('./commands.js');
          let sequence = 0;
          const data = new CommandReceipts('isolated-http-commands', () => 'tab-' + n + '-' + (++sequence));
          window.claim = async draft => data.claim(await creationSlot({name:draft}), () => true);
        }, n);
      }));
      const same = await Promise.all(tabs.map(page => page.evaluate(() => window.claim('SAME_SYNTHETIC_DRAFT'))));
      assert.equal(new Set(same.map(receipt => receipt.id)).size,1);
      assert.equal(same.filter(receipt => receipt.existing).length,1);
      const different = await tabs[1].evaluate(() => window.claim('DIFFERENT_SYNTHETIC_DRAFT'));
      assert(!different.existing); assert.notEqual(different.id,same[0].id);
      assert(!await tabs[0].evaluate(() => JSON.stringify(Object.entries(localStorage)).includes('SYNTHETIC_DRAFT')));
      metadata.http_drafts = { secureContext:false, sameDraftIDs:1, differentDraftIDs:2, tabs:2 };
    } finally { await http.close(); }
    console.log(JSON.stringify(metadata));
  } finally {
    await browser?.close(); fixture.kill(); await exit;
  }
}
main().catch(e => { console.error(e); process.exitCode = 1; });
