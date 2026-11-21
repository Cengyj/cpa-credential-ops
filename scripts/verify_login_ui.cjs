// Current account history dialog. The historical three-tab UI and its original
// explicit workflow assertions remain available in verify_login_ui_legacy.cjs.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

function gate() {
  let release;
  const promise = new Promise(resolve => { release = resolve; });
  return {promise,release};
}

async function main() {
  const ui = path.resolve(__dirname,'../plugin/ui');
  const names = require('../plugin/ui/public_assets.json');
  const sourceHashes = async () => Object.fromEntries(await Promise.all(names.map(async name =>
    [name,createHash('sha256').update(await fs.readFile(path.join(ui,name))).digest('hex')])));
  const source = await sourceHashes();
  const fixture = spawn(process.execPath,[path.join(__dirname,'login_ui_fixture.cjs')],{stdio:['ignore','pipe','inherit']});
  const exit = once(fixture,'exit');
  let browser;
  try {
    const [line] = await once(createInterface({input:fixture.stdout}),'line');
    const info = JSON.parse(line), {origin,hostname,port} = new URL(info.url);
    assert.equal(hostname,'127.0.0.1'); assert.notEqual(port,'8317');
    browser = await chromium.launch({executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
    const page = await browser.newPage({viewport:{width:1280,height:800},reducedMotion:'reduce'});
    page.setDefaultTimeout(6000);
    const prefix = '/v0/management/plugins/model-quality', email = 'visible-identity@example.test';
    const validURL = 'https://auth.openai.com/oauth/authorize?state=offline-state&code_challenge=offline-challenge';
    const callback = 'http://localhost:1455/auth/callback?code=CANARY_CALLBACK&state=offline-state';
    const base = {account_id:'isolated-private-account',target_auth_id:'isolated-private-target',method:'password_totp',intent:'reauthorize',
      email,masked_email:'v***@example.test',created_at:'2026-11-16T01:00:00Z',expires_at:'2099-11-16T01:15:00Z',revision:1,plan_type:'pro'};
    const done = {...base,id:'isolated-terminal-job',method:'password_totp',action:'reset_totp',status:'failed',phase:'',reauthorize:true,
      totp_reset:true,devices_logged_out:true,security_status:'complete',error_code:'exchange_failed',error_stage:'oauth',
      runtime_code:'service_error',http_status:503,can_delete:true,raw:{access_token:'CANARY_TOKEN'},password:'CANARY_PASSWORD',totp_secret:'CANARY_TOTP'};
    const active = {...base,id:'isolated-browser-job',method:'browser',status:'waiting_browser',phase:'authorization',authorization_url:validURL};
    const candidate = {...base,id:'isolated-candidate-job',status:'awaiting_commit',candidate_id:'isolated-candidate'};
    const unknown = {...base,id:'isolated-unknown-job',status:'saved_not_adopted',error_code:'credential_enable_conflict',auto_commit:true,enable_credential:true};
    const jobs = [done,active,candidate,unknown], writes = [], errors = [], results = {};
    let held, optionalCandidate, detailFailure = false, forced401 = false;
    page.on('pageerror',e => errors.push(e.message));
    await page.route('**/*',async route => {
      const req = route.request(), url = new URL(req.url());
      assert.equal(url.origin,origin);
      if (!url.pathname.startsWith(prefix + '/')) return route.continue();
      if (req.method() !== 'GET') { writes.push(req.method() + ' ' + url.pathname); return route.fulfill({status:405,json:{code:'test_read_only'}}); }
      assert.equal(req.headers().authorization,'Bearer ' + info.management_key);
      if (forced401) { forced401 = false; return route.fulfill({status:401,json:{code:'unauthorized'}}); }
      const resource = url.pathname.slice(prefix.length);
      if (resource === '/login/jobs') return route.fulfill({json:{items:jobs.map(({authorization_url, ...job}) => job),has_more:false}});
      const id = url.searchParams.get('id'), job = jobs.find(j => j.id === id);
      if (resource === '/login/detail' && job) {
        const value = {...job}, pause = held?.id === id ? held : null;
        if (pause) { held = null; pause.entered.release(); await pause.wait.promise; }
        if (detailFailure && id === active.id) return route.fulfill({status:503,json:{code:'storage_unavailable'}});
        return route.fulfill({json:value});
      }
      if (resource === '/login/candidate') {
        const owner = jobs.find(job => job.candidate_id === id); assert(owner);
        if (owner === active && optionalCandidate) { optionalCandidate.entered.release(); await optionalCandidate.wait.promise; }
        return route.fulfill({json:{id,job_id:owner.id,revision:owner.revision,email,account_id:'isolated-oauth-account',user_id:'isolated-oauth-user',
          restrictions:['quality_paused','manual_disabled','rules_stopped'],intent:'reauthorize',target_auth_id:base.target_auth_id}});
      }
      if (resource === '/login/logs' && job) {
        const entry = {id:1,time:base.created_at,status:job.status,phase:job.phase,error_stage:job.error_stage,error_code:job.error_code,
          runtime_code:job.runtime_code,http_status:job.http_status,raw:{access_token:'CANARY_TOKEN'},authorization_url:validURL,callback_url:callback};
        return route.fulfill({json:{job_id:id,revision:job.revision,first_id:1,last_id:1,retained_count:1,next_cursor:1,has_more:false,entries:[entry]}});
      }
      return route.continue();
    });
    const openHistory = async () => {
      if (!await page.locator('#login-history-dialog').evaluate(el => el.open)) await page.click('#account-history-open');
    };
    const detail = page.locator('#login-job-detail'), link = page.locator('.login-auth-link');
    const select = async job => {
      await openHistory();
      await page.locator('[data-login-job="' + job.id + '"]').click();
      await detail.locator('.login-detail-identity').filter({hasText:email}).waitFor();
    };
    const refresh = async (id, code = 200) => {
      const response = page.waitForResponse(r => new URL(r.url()).pathname === prefix + '/login/detail' && new URL(r.url()).searchParams.get('id') === id && r.status() === code);
      await page.click('#login-refresh'); await (await response).finished();
      await page.evaluate(() => new Promise(done => requestAnimationFrame(() => requestAnimationFrame(done))));
    };
    await page.goto(origin + '/management.html#/auth-files');
    await page.evaluate(([p,k]) => localStorage.setItem('model-quality:management-key:' + p,k),[prefix,info.management_key]);
    await page.goto(origin + '/#login');
    assert.equal(await page.locator('#login-view-temporary,#login-form,#login-start').count(),0);
    await page.locator('#account-import-open:enabled').waitFor(); await select(done);
    await detail.locator('.tag').filter({hasText:'后续步骤未完成'}).waitFor();
    assert.match(await detail.innerText(),/新 2FA 已保存/); assert.match(await detail.innerText(),/已退出所有设备/);
    assert.equal(await detail.locator('.login-auth-link,[data-login-action=commit],[data-login-action=cancel]').count(),0);
    assert(await page.locator('#login-history-delete-open').isEnabled());
    await page.locator('#login-task-search').fill(email); assert.equal(await page.locator('[data-login-job]').count(),jobs.length); await page.locator('#login-task-search').fill('');
    const download = page.waitForEvent('download');
    await page.click('#login-export');
    const reportText = await fs.readFile(await (await download).path(),'utf8'), report = JSON.parse(reportText);
    assert.equal(report.read_incomplete,false);
    assert.equal(report.jobs[0].id,done.id); assert.equal(report.jobs[0].http_status,503); assert.equal(report.jobs[0].totp_reset,true);
    assert.equal(report.jobs[0].events.length,1); assert.equal(report.jobs[0].events[0].http_status,503);
    for (const secret of [email,base.masked_email,base.target_auth_id,base.account_id,'CANARY_TOKEN','CANARY_PASSWORD','CANARY_TOTP',
      'CANARY_CALLBACK',info.management_key,'auth.openai.com','offline-state','offline-challenge','authorization_url','access_token']) {
      assert(!reportText.includes(secret), 'Export contains a private field or value');
    }
    assert.equal(await detail.locator('.login-detail-identity').innerText(),email);
    results.terminal = {fullEmailOnScreen:true,filteredByEmail:true,canDelete:true,confirmedSecurityEffects:2};
    results.export = {jobID:done.id,httpStatus:503,eventCount:1,excludedEmailTokensAndURLs:true};
    await select(candidate); await detail.getByRole('button',{name:'确认上传 CPA'}).waitFor();
    for (const marker of [email,'isolated-oauth-account','isolated-oauth-user','质量暂停','人工禁用']) assert((await detail.innerText()).includes(marker),marker);
    await select(unknown); await detail.locator('.notice').filter({hasText:'保存结果尚未确认'}).waitFor();
    assert.equal(await detail.locator('[data-login-action=commit]').count(),0);
    assert(await page.locator('#login-history-delete-open').isDisabled());
    results.protectedStates = {awaitingCandidate:true,uncertainProtected:true};
    await select(active); await link.waitFor(); assert.equal(await link.getAttribute('href'),validURL);
    const input = page.locator('#login-callback'); await input.fill(callback);
    await refresh(active.id); assert.equal(await input.inputValue(),callback);
    const pause = held = {id:active.id,entered:gate(),wait:gate()};
    const pending = refresh(active.id,503);
    await pause.entered.promise;
    assert.equal(await link.getAttribute('href'),validURL);
    detailFailure = true; pause.wait.release(); await pending;
    await page.locator('#login-history-read-note').filter({hasText:'任务详情'}).waitFor();
    assert.equal(await link.getAttribute('href'),validURL); assert.equal(await input.inputValue(),callback);
    const staleExport = page.waitForEvent('download');
    await page.click('#login-export');
    const staleReport = JSON.parse(await fs.readFile(await (await staleExport).path(),'utf8'));
    assert.equal(staleReport.jobs[0].id,active.id);
    assert(staleReport.jobs[0].events.length > 0);
    assert.equal(staleReport.read_incomplete,true,'Readable events cannot certify a job summary whose refresh failed');
    results.export.staleSummaryMarkedIncomplete = true;
    detailFailure = false;
    await refresh(active.id);
    const recoveredExport = page.waitForEvent('download');
    await page.click('#login-export');
    assert.equal(JSON.parse(await fs.readFile(await (await recoveredExport).path(),'utf8')).read_incomplete,false);
    results.export.currentSummaryRecovered = true;
    for (const replacement of [null,'https://auth.openai.com@evil.example/oauth/authorize']) {
      if (replacement === null) delete active.authorization_url;
      else active.authorization_url = replacement;
      active.revision++;
      await refresh(active.id);
      assert.equal(await link.count(),0,'A successful detail read withdrawing or rejecting a URL removes the previously shown link');
      assert.equal(await input.inputValue(),callback,'The same active job retains its draft after navigation availability changes');
      assert(!(await detail.innerHTML()).includes('offline-challenge'));
      active.authorization_url = validURL; active.revision++;
      await refresh(active.id); await link.waitFor();
      assert.equal(await link.getAttribute('href'),validURL); assert.equal(await input.inputValue(),callback);
    }
    results.browserNavigation = {validLink:true,pendingKeptPrevious:true,failedReadKeptPrevious:true,successfulWithdrawalRemoved:true,invalidURLRemoved:true,sameJobDraftPreserved:true};
    active.status = 'awaiting_commit'; active.candidate_id = 'isolated-browser-candidate'; delete active.authorization_url; active.revision++;
    optionalCandidate = {entered:gate(),wait:gate()};
    await refresh(active.id); await optionalCandidate.entered.promise;
    assert.equal(await link.count(),0); assert.equal(await input.count(),0);
    assert.equal(await detail.locator('[data-login-action=commit]').count(),0);
    optionalCandidate.wait.release(); await detail.locator('[data-login-action=commit]').waitFor();
    results.browserNavigation.transitionWithPendingCandidateRevoked = true;
    active.status = 'waiting_browser'; active.authorization_url = validURL; delete active.candidate_id; active.revision++;
    await refresh(active.id); await link.waitFor(); assert.equal(await input.inputValue(),'');
    await input.fill(callback);
    await page.keyboard.press('Escape'); await page.click('[data-page=manual]'); await page.click('[data-page=login]');
    await select(active); await link.waitFor(); assert.equal(await input.inputValue(),'');
    const late = held = {id:done.id,entered:gate(),wait:gate()};
    await page.locator('[data-login-job="' + done.id + '"]').click(); await late.entered.promise;
    await select(active); await link.waitFor();
    const delivered = page.waitForResponse(r => r.url().endsWith('/login/detail?id=' + done.id));
    late.wait.release(); await (await delivered).finished();
    assert.equal(await link.getAttribute('href'),validURL);
    results.navigation = {discardedDraftOnLeaving:true,oldDetailDidNotReplaceActiveJob:true};
    await input.fill(callback); forced401 = true;
    await page.click('#login-refresh'); await page.waitForSelector('body:not(.connected)');
    assert.equal(await page.evaluate(() => localStorage.getItem('model-quality:management-key:/v0/management/plugins/model-quality')),null);
    assert(!await page.locator('[data-login-secret], #login-callback').evaluateAll(nodes => nodes.some(node => node.value)));
    assert.equal(await link.count(),0);
    assert.deepEqual(writes,[]); assert.deepEqual(errors,[]);
    results.access = {expiredIdentityCleared:true,secretDraftCleared:true,businessPOSTs:0,backendConfigurationWrites:0};
    assert.deepEqual(await sourceHashes(),source);
    if (process.env.LOGIN_UI_OUTPUT) {
      await fs.mkdir(process.env.LOGIN_UI_OUTPUT,{recursive:true});
      const report = {status:'passed',fixture_scope:'Authenticated loopback-only seeded tasks, read-only browser',checks:Object.keys(results),source_sha256:source,...results};
      await fs.writeFile(path.join(process.env.LOGIN_UI_OUTPUT,'report.json'),JSON.stringify(report,null,2)+'\n');
    }
    console.log(JSON.stringify(results));
  } finally { await browser?.close(); fixture.kill(); await exit; }
}
main().catch(e => {console.error(e);process.exitCode = 1;});
