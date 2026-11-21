// Only synthetic receipts are admitted. No account runner or remote endpoint is used.
const assert = require('node:assert/strict');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const { createInterface } = require('node:readline');
const path = require('node:path');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore','pipe','inherit'] });
  const exit = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({input:fixture.stdout}), 'line');
    const info = JSON.parse(line), {origin,hostname,port} = new URL(info.url);
    assert.equal(hostname,'127.0.0.1'); assert.notEqual(port,'8317');
    browser = await chromium.launch({executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
    const prefix = '/v0/management/plugins/model-quality', report = {};
    for (const [name, account, kind, status] of [
      ['first_login','fixture-new','login','accepted'],
      ['manual_reauthorization','fixture-enabled','login','accepted'],
      ['reset_confirmed_seed','fixture-enabled','reset_totp','accepted'],
      ['logout_legacy_unknown','fixture-enabled','logout_all','reconciliation_required'],
      ['logout_expired_receipt','fixture-enabled','logout_all','expired'],
      ['login_missing_then_explicit_submit','fixture-new','login','missing'],
    ]) {
      const context = await browser.newContext(), writes = [], reads = [], errors = [];
      try {
        let page;
        async function newPage() {
          page = await context.newPage(); page.setDefaultTimeout(6000);
          page.on('pageerror', e => errors.push(e.message));
        }
        await newPage();
        await page.goto(origin + '/management.html#/auth-files');
        await page.evaluate(([p,k]) => localStorage.setItem('model-quality:management-key:' + p,k), [prefix,info.management_key]);
        const path = kind === 'login' ? '/accounts/start' : '/accounts/security';
        const receipt = id => {
          const job = {id:'synthetic-job-only',account_id:account,revision:2,method:'password_totp',status:'running',
            ...(kind === 'login' ? {} : {action:kind,totp_reset:kind === 'reset_totp'})};
          return {id, ...(status === 'accepted' ? {path,job_id:job.id,job} : {}),status,
            ...(status === 'reconciliation_required' ? {outcome_unknown:true,code:'idempotency_unknown'} : {})};
        };
        await context.route('**/*',async route => {
          const request = route.request(), url = new URL(request.url());
          assert.equal(url.origin,origin);
          if (!url.pathname.startsWith(prefix + '/')) return route.continue();
          assert.equal(request.headers().authorization,'Bearer ' + info.management_key);
          if (url.pathname === prefix + '/login/commands' && request.method() === 'GET') {
            const id = url.searchParams.get('id'); assert(writes.some(w => w.id === id)); reads.push(id);
            if (status === 'missing') return route.fulfill({status:404,json:{code:'command_not_accepted'}});
            return route.fulfill({json:receipt(id)});
          }
          if (request.method() === 'GET') return route.continue();
          assert.equal(request.method(),'POST'); assert.equal(url.pathname,prefix + '/login' + path);
          const id = request.headers()['idempotency-key'], body = request.postDataJSON();
          assert(id && body.id === account); writes.push({id,body});
          if (writes.length === 1) return route.abort('connectionfailed');
          return route.fulfill({json:{id:'synthetic-second',account_id:account,status:'running',method:'password_totp',revision:2}});
        });
        async function execute(alternative = false) {
          const button = action => page.locator('[data-account-action="' + action + '"][data-account-id="' + account + '"]');
          if (kind === 'login') {
            await button('start').click();
            await page.locator('#account-login-upload').setChecked(alternative);
            return page.click('#account-login-submit');
          }
          if (kind === 'reset_totp') await button('reset_totp').click();
          else { await button('more').click(); await page.click('#account-logout-all'); }
          return page.click('#account-security-submit');
        }
        await page.goto(origin + '/#login');
        const lost = page.waitForEvent('requestfailed', r => r.url().endsWith('/login' + path));
        await execute(); await lost;
        await page.close(); await newPage(); await page.goto(origin + '/#login');
        const note = page.locator('#account-command-note');
        const messages = {accepted:'已受理',missing:'尚未查到受理回执',reconciliation_required:'结果待核对',expired:'历史结果已过期'};
        await note.filter({hasText:messages[status]}).waitFor();
        const original = writes[0].id;
        assert.equal(writes.length,1); assert(reads.includes(original)); assert.match(await note.innerText(),new RegExp(original));
        if (kind === 'reset_totp') assert.match(await note.innerText(),/新 2FA 已保存/);
        if (status === 'accepted') assert.match(await note.innerText(),/synthetic-job-only/);
        if (status === 'reconciliation_required' || status === 'expired') {
          const boundary = reads.length;
          await execute();
          await page.locator('#account-security-error').waitFor();
          assert(reads.slice(boundary).includes(original)); assert.equal(writes.length,1);
        }
        if (status === 'missing' || name === 'first_login') {
          const next = page.waitForResponse(r => r.request().method() === 'POST' && r.url().endsWith('/login' + path));
          await execute(name === 'first_login'); await next;
          assert.equal(writes.length,2);
          if (name === 'first_login') { assert.notEqual(writes[1].id,original); assert.notDeepEqual(writes[0].body,writes[1].body); }
          else { assert.equal(writes[1].id,original); assert.deepEqual(writes[0].body,writes[1].body); }
        }
        const stored = await page.evaluate(() => JSON.stringify(Object.entries(localStorage).filter(([k]) => k.includes('account-commands:'))));
        assert(!stored.includes(account) && !stored.includes('password') && !stored.includes('synthetic-job'));
        assert.deepEqual(errors,[]);
        report[name] = { closedLastTab:true, originalIDReads:reads.filter(id => id === original).length, automaticWrites:0, explicitWrites:writes.length, uniqueIDs:new Set(writes.map(w => w.id)).size };
      } finally { await context.close(); }
    }
    console.log(JSON.stringify(report));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(e => { console.error(e); process.exitCode = 1; });
