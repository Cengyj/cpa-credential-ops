// Exercise account management against a loopback fixture without real credentials.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const http = require('node:http');
const crypto = require('node:crypto');
const { chromium } = require('/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');
const root = path.resolve(__dirname, '..');
const output = path.resolve(process.env.ACCOUNT_UI_OUTPUT || path.join(root, 'artifacts/account-refined-8.8.0/account-ui'));
const sourceHashes = () => Object.fromEntries(require('../plugin/ui/public_assets.json').map(name=>[name,crypto.createHash('sha256').update(fs.readFileSync(path.join(root,'plugin/ui',name))).digest('hex')]));
const source = sourceHashes();
fs.mkdirSync(output, { recursive: true });
const accounts = [], jobs = [], calls = [], errors = [], checks = [];
const credentials = new Map();
const logRecords = new Map();
const logFailures = new Set();
const securityRecoveries = new Map();
let holdSecurityWrite = false, releaseSecurityWrite, securityWriteStarted;
let securityWriteUnknown = false;
let nextLogRead = null;
let nextAccountRead = null;
let nextJobRead = null;
const heldReads = new Set();
let initialized = true, storageMode = 'plaintext', storageAvailable = true, importFailure = false, sequence = 0, unauthorized = false;
let holdAccountWrite = false, releaseAccountWrite, accountWriteStarted;
let holdImportWrite = false, releaseImportWrite, importWriteStarted;
let holdStartWrite = false, releaseStartWrite, startWriteStarted;
let accountDetailFailure = false;
let settings = { revision: 1, inspection_enabled: false, auto_relogin_enabled: false, interval_minutes: 15, login_proxy_url: '' };
const accountView = account => ({ ...account, effective_proxy_url: account.effective_proxy_url ?? (account.proxy_url || settings.login_proxy_url || 'direct'), proxy_source: account.proxy_source || (account.proxy_url ? 'account' : settings.login_proxy_url ? 'login_default' : 'direct') });
const target = { auth_id:'offline-codex', email:'fixture@example.test', account_id:'acct_fixture_complete', user_id:'user_fixture_complete', masked_email:'f***@example.test', identity_version:'offline-identity', can_update:true, manual_disabled:false, quality_paused:false };
const json = (res, data, status = 200) => { res.writeHead(status, {'Content-Type':'application/json','Cache-Control':'no-store'}); res.end(JSON.stringify(data)); };
const server = http.createServer(async (req,res) => {
  const url = new URL(req.url,'http://localhost');
  if(!url.pathname.startsWith('/v0/management/')) {
    const name = url.pathname === '/' ? 'index.html' : url.pathname.slice(1);
    if(!require('../plugin/ui/public_assets.json').includes(name)) { res.writeHead(404);res.end();return; }
    res.writeHead(200,{'Content-Type':name.endsWith('.js')?'text/javascript':name.endsWith('.css')?'text/css':'text/html'});res.end(fs.readFileSync(path.join(root,'plugin/ui',name)));return;
  }
  let raw=''; for await (const chunk of req) raw += chunk;
  const body = raw ? JSON.parse(raw) : {};
  calls.push({path:url.pathname,query:Object.fromEntries(url.searchParams),method:req.method,body});
  if(unauthorized || req.headers.authorization !== 'Bearer offline-key') { unauthorized=false;json(res,{code:'unauthorized'},401);return; }
  if(req.method!=='GET') assert(req.headers['idempotency-key']);
  if(url.pathname.startsWith('/v0/management/plugins/') && !url.pathname.startsWith('/v0/management/plugins/model-quality/login/')) {
    if(url.pathname.endsWith('/questions')) json(res,{items:[{id:'custom',title:'自定义题目',kind:'custom'}]});
    else json(res,{items:[],total:0});
    return;
  }
  const suffix=url.pathname.replace('/v0/management/plugins/model-quality/login','');
  const account=accounts.find(a=>a.id===body.id);
  const job=jobs.find(j=>j.id===(body.job_id||url.searchParams.get('id')?.replace(':candidate','')));
  if(suffix==='/capabilities') return json(res,{available:true,automatic_commit:true,commit_mode:'account_auto_commit',security_actions:{reset_totp:true,logout_all:true},methods:{browser:{available:true},password_totp:{available:true}},vault:{available:storageAvailable,initialized,storage_mode:storageMode}});
  if(suffix==='/targets') return json(res,{items:[target]});
  if(suffix==='/vault/status') return json(res,{available:storageAvailable,initialized,storage_mode:storageMode});
  if(suffix==='/accounts') return json(res,{items:accounts.map(accountView)});
  if(suffix==='/accounts/detail') {
    const selected=accounts.find(a=>a.id===url.searchParams.get('id'));
    if(!selected) return json(res,{code:'not_found'},404);
    if(accountDetailFailure) { accountDetailFailure=false;return json(res,{code:'storage_unavailable',message:'DO_NOT_RENDER_ACCOUNT_SECRET'},503); }
    const response=JSON.parse(JSON.stringify({account:accountView(selected),credentials:credentials.get(selected.id),security_recovery:securityRecoveries.get(selected.id)}));
    const held=nextAccountRead;nextAccountRead=null;
    if(held) { held.received();await held.gate; }
    return json(res,response);
  }
  if(suffix==='/accounts/settings') {
    if(req.method==='PUT') { if(body.revision!==settings.revision) return json(res,{code:'conflict'},409);settings={...body,revision:settings.revision+1}; }
    return json(res,settings);
  }
  if(suffix==='/accounts/import') {
    assert(initialized && storageAvailable && storageMode==='plaintext');
    if(holdImportWrite) { holdImportWrite=false;await new Promise(resolve=>{releaseImportWrite=resolve;importWriteStarted();}); }
    if(importFailure) {importFailure=false;return json(res,{code:'storage_unavailable',message:'DO_NOT_RENDER_STORAGE_SECRET'},503);}
    for(const entry of body.entries) { const next={id:`account-${++sequence}`,revision:1,email:entry.email,masked_email:`${entry.email[0]}***@example.test`,label:'',proxy_url:body.proxy_url||'',auth_id:'',identity_version:'',health_status:'not_linked',health_reason:'login_required',auto_relogin:false,attention_required:false,last_checked_at:'',next_check_at:'',last_job_id:'',last_job_status:'',consecutive_failures:0};accounts.push(next);credentials.set(next.id,{...entry}); }
    return json(res,{items:accounts});
  }
  if(suffix==='/accounts/entry') {
    if(req.method==='PUT'&&holdAccountWrite) { holdAccountWrite=false;await new Promise(resolve=>{releaseAccountWrite=resolve;accountWriteStarted();}); }
    if(!account||body.revision!==account.revision) return json(res,{code:'conflict'},409);
    if(req.method==='DELETE') { accounts.splice(accounts.indexOf(account),1);credentials.delete(account.id);return json(res,{deleted:true}); }
    else { Object.assign(account,{label:body.label,auto_relogin:body.auto_relogin,proxy_url:body.proxy_url??account.proxy_url,revision:account.revision+1});if(body.credentials)credentials.set(account.id,{...body.credentials}); }
    return json(res,account||{});
  }
  if(suffix==='/accounts/inspect') { for(const a of accounts.filter(a=>!body.id||a.id===body.id)) { a.last_checked_at='2026-11-15T20:00:00Z';a.revision++;if(a.auth_id){a.health_status='needs_reauth';a.health_reason='requires_sign_in';} } return json(res,{items:accounts}); }
  if(suffix==='/accounts/security') {
    if(holdSecurityWrite) { holdSecurityWrite=false;await new Promise(resolve=>{releaseSecurityWrite=resolve;securityWriteStarted();}); }
    if(securityWriteUnknown) { securityWriteUnknown=false;return json(res,{code:'idempotency_unknown'},503); }
    if(!account||body.revision!==account.revision) return json(res,{code:'conflict'},409);
    assert(['reset_totp','logout_all'].includes(body.action));assert.equal(typeof body.logout_all_devices,'boolean');assert.equal(typeof body.reauthorize,'boolean');
    const next={id:`job-${++sequence}`,revision:1,account_id:account.id,automatic:false,method:'password_totp',intent:account.auth_id?'reauthorize':'import',status:'running',phase:'security_login',action:body.action,security_status:'running',reauthorize:body.reauthorize,auto_commit:body.reauthorize,enable_credential:body.reauthorize,credential_enabled:false,email:account.email,created_at:'2026-11-17T01:00:00Z'};
    jobs.push(next);Object.assign(account,{last_job_id:next.id,last_job_status:next.status,last_job_action:next.action,last_job_security_status:next.security_status,revision:account.revision+1});return json(res,next);
  }
  if(suffix==='/accounts/security/complete') {
    const recovery=securityRecoveries.get(account?.id);assert(recovery?.can_complete);assert.equal(body.job_id,recovery.job_id);
    credentials.get(account.id).totp_secret=recovery.new_totp_secret;securityRecoveries.delete(account.id);
    Object.assign(account,{security_pending:false,last_job_status:'succeeded',last_job_totp_reset:true,revision:account.revision+1});
    const securityJob=jobs.find(j=>j.id===body.job_id);Object.assign(securityJob,{status:'succeeded',security_status:'complete',totp_reset:true,reauthorize:false});return json(res,account);
  }
  if(suffix==='/accounts/start') {
    if(holdStartWrite) { holdStartWrite=false;await new Promise(resolve=>{releaseStartWrite=resolve;startWriteStarted();}); }
    if(!account||body.revision!==account.revision) return json(res,{code:'conflict'},409);
    const next={id:`job-${++sequence}`,revision:1,account_id:account.id,automatic:false,method:'password_totp',intent:(body.target_auth_id||account.auth_id)?'reauthorize':'import',status:'running',phase:'totp',auto_commit:true,enable_credential:true,credential_enabled:false,email:account.email,masked_email:account.masked_email,candidate_id:`job-${sequence}:candidate`,created_at:'2026-11-15T20:01:00Z'};
    jobs.push(next);Object.assign(account,{last_job_id:next.id,last_job_status:next.status,last_job_auto_commit:true,last_job_credential_enabled:false,revision:account.revision+1});return json(res,next);
  }
  if(suffix==='/logs') {
    const id=url.searchParams.get('id');
    if(!job) return json(res,{code:'not_found'},404);
    const response=JSON.parse(JSON.stringify(logRecords.get(id)||{job_id:id,entries:[],legacy:true}));
    const held=nextLogRead?.id===id?nextLogRead:null;
    if(held){nextLogRead=null;held.received();await held.gate;}
    if(logFailures.delete(id))return json(res,{code:'storage_unavailable',message:'DO_NOT_RENDER_LOG_SECRET'},503);
    return json(res,response);
  }
  if(suffix==='/jobs') return json(res,{items:[...jobs].reverse()});
  if(suffix==='/detail') {
    const response=JSON.parse(JSON.stringify(job||{}));const held=nextJobRead;nextJobRead=null;
    if(held){held.received();await held.gate;}return json(res,response);
  }
  if(suffix==='/candidate') return json(res,{id:job.candidate_id,revision:job.revision,job_id:job.id,email:job.email,account_id:'acct_fixture_complete',user_id:'user_fixture_complete',masked_email:job.masked_email,account_id_masked:'acct_***fixture',target_auth_id:job.intent==='reauthorize'?target.auth_id:'',intent:job.intent,restrictions:['new_credential_disabled','rules_stopped']});
  if(suffix==='/commit') { job.status='committed';job.revision++;const a=accounts.find(a=>a.id===job.account_id);Object.assign(a,{auth_id:target.auth_id,identity_version:target.identity_version,last_job_status:'committed',revision:a.revision+1});return json(res,job); }
  json(res,{code:'not_found'},404);
});
(async()=>{
 await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
 const browser=await chromium.launch({headless:true,executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'});
 try {
  const page=await browser.newPage({viewport:{width:1280,height:800},reducedMotion:'reduce'});
  page.on('pageerror',e=>errors.push(e.message));
  await page.route('**/*',route=>new URL(route.request().url()).hostname==='127.0.0.1'?route.continue():route.abort());
  const settle=async()=>page.evaluate(async()=>{await document.fonts.ready;await new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));});
  const screenshots=[];
  const accountDensityGeometry=[];
  const capture=async(name,reset=true)=>{if(reset)await page.evaluate(()=>{scrollTo(0,0);document.querySelectorAll('.login-scroll,.account-table-wrap,.login-job-detail,.account-editor-body,.account-settings-layout,.account-import-body').forEach(n=>{n.scrollTop=0;n.scrollLeft=0;});document.querySelectorAll('textarea').forEach(n=>{n.scrollLeft=0;n.scrollTop=0;});});await settle();assert.equal(await page.evaluate(()=>document.documentElement.scrollWidth>innerWidth),false,`No page overflow: ${name}`);const modalOpen=await page.evaluate(()=>!!document.querySelector('#account-import-dialog[open],#account-editor-dialog[open],#account-security-dialog[open]'));await page.screenshot({path:path.join(output,name+'.png'),fullPage:!modalOpen});screenshots.push(name+'.png');};
  const assertViewportAction=async(selector)=>{
    const geometry=await page.locator(selector).evaluate(node=>{
      const box=node.getBoundingClientRect();
      const visible={left:0,top:0,right:innerWidth,bottom:innerHeight};
      for(let parent=node.parentElement;parent;parent=parent.parentElement){
        const style=getComputedStyle(parent),clip=parent.getBoundingClientRect();
        if(/auto|scroll|hidden|clip/.test(style.overflowX)){visible.left=Math.max(visible.left,clip.left+parent.clientLeft);visible.right=Math.min(visible.right,clip.left+parent.clientLeft+parent.clientWidth);}
        if(/auto|scroll|hidden|clip/.test(style.overflowY)){visible.top=Math.max(visible.top,clip.top+parent.clientTop);visible.bottom=Math.min(visible.bottom,clip.top+parent.clientTop+parent.clientHeight);}
      }
      return {left:box.left,top:box.top,right:box.right,bottom:box.bottom,width:box.width,height:box.height,visible};
    });
    assert(geometry.width>0&&geometry.height>0&&geometry.left>=geometry.visible.left-1&&geometry.top>=geometry.visible.top-1&&geometry.right<=geometry.visible.right+1&&geometry.bottom<=geometry.visible.bottom+1,`Element is fully visible without ancestor clipping: ${selector} ${JSON.stringify(geometry)}`);
  };
  const assertSelectedJobVisible=async(id,context)=>{
    await settle();
    const selected=page.locator('#login-jobs .login-job.selected');
    assert.equal(await selected.count(),1,`One selected task: ${context}`);
    assert.equal(await selected.getAttribute('data-login-job'),id,`Selected task matches detail: ${context}`);
    assert.equal(await selected.getAttribute('aria-pressed'),'true');
    const geometry=await selected.evaluate(row=>{const list=row.parentElement,box=list.getBoundingClientRect(),item=row.getBoundingClientRect();const top=box.top+list.clientTop;return {top:item.top,bottom:item.bottom,visibleTop:top,visibleBottom:top+list.clientHeight};});
    assert(geometry.top>=geometry.visibleTop-1&&geometry.bottom<=geometry.visibleBottom+1,`Selected task is fully visible inside its list: ${context} ${JSON.stringify(geometry)}`);
  };
  const accountCompletion=id=>new Promise(resolve=>{
    const matches=request=>{const url=new URL(request.url());return url.pathname.endsWith('/accounts/detail')&&url.searchParams.get('id')===id;};
    const cleanup=()=>{page.off('response',response);page.off('requestfailed',failure);};
    const response=value=>{if(matches(value.request())){cleanup();value.finished().then(()=>resolve('response'));}};
    const failure=value=>{if(matches(value)){cleanup();resolve('cancelled');}};
    page.on('response',response);page.on('requestfailed',failure);
  });
  const holdDetail=()=>{let release,received;const done=new Promise(resolve=>received=resolve);const gate=new Promise(resolve=>release=resolve);heldReads.add(release);nextAccountRead={gate,received};return {received:done,release:()=>{release();heldReads.delete(release);}};};
  const holdLogs=id=>{let release,received;const done=new Promise(resolve=>received=resolve);const gate=new Promise(resolve=>release=resolve);heldReads.add(release);nextLogRead={id,gate,received};return {received:done,release:()=>{release();heldReads.delete(release);}};};
  const refresh=async()=>{const response=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/model-quality/login/accounts'));await page.locator('#login-refresh').evaluate(n=>n.click());await (await response).finished();await settle();};
  await page.goto(`http://127.0.0.1:${server.address().port}/#login`);
  await page.locator('#login-connect').click();await page.locator('#management-key').fill('offline-key');await page.locator('#connection-form button[type=submit]').click();
  await page.locator('#account-import-open:enabled').waitFor();
  assert(await page.locator('#login-view-accounts').isVisible());assert.equal(await page.locator('#login-form, #login-view-temporary, [data-login-view=temporary]').count(),0);
  assert.equal(await page.locator('[data-login-view]').count(),3);assert(!await page.locator('#login-view-settings').isVisible());
  assert.equal(await page.locator('.account-overview button').count(),4);
  assert.equal(await page.locator('tbody#account-list').count(),1,'Accounts retain table semantics');
  assert.deepEqual(await page.locator('.account-table thead th').allTextContents(),['账号','CPA 状态','当前操作','操作时间','操作类型','操作']);
  assert.equal(await page.locator('#account-list .account-expand').count(),0,'Account metadata is visible without a disclosure control');
  assert.equal(await page.locator('[data-account-filter]').count(),3);
  assert.equal(await page.locator('#login-view-accounts').getAttribute('data-panel'),'list');assert.equal(await page.locator('#account-editor-dialog').evaluate(n=>n.open),false);
  assert(calls.every(c=>c.method==='GET'));checks.push('Accounts are the default view; opening and connecting make no writes');
  await page.locator('#account-import-open').click();
  assert(await page.locator('.account-main').isVisible());assert(await page.locator('#account-import-form').isVisible());
  assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open&&n.matches(':modal')),true);
  assert.equal(await page.locator('#login-view-accounts').getAttribute('data-panel'),'list');
  assert.equal(await page.locator('#account-import-text').evaluate(n=>document.activeElement===n),true);
  await page.locator('#account-import-text').fill('discard@example.test----discard-password----JBSWY3DPEHPK3PXP');
  assert.match(await page.locator('#account-import-count').innerText(),/1/);
  await page.keyboard.press('Escape');await page.locator('#account-import-dialog').waitFor({state:'hidden'});
  assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert.equal(await page.locator('#account-import-open').evaluate(n=>document.activeElement===n),true);
  await page.locator('#account-import-empty').click();
  await page.locator('#account-import-text').fill('discard@example.test----discard-password----JBSWY3DPEHPK3PXP');
  await page.locator('#account-import-cancel').click();
  assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert.equal(await page.locator('#account-import-empty').evaluate(n=>document.activeElement===n),true);
  await page.locator('#account-import-open').click();
  // Chrome may move focus to browser chrome (reported as body), but never to the inert page.
  for(let i=0;i<7;i++) { await page.keyboard.press('Tab');assert(await page.locator('#account-import-dialog').evaluate(n=>n.contains(document.activeElement)||document.activeElement===document.body),'Native modal excludes background controls from forward focus'); }
  for(let i=0;i<7;i++) { await page.keyboard.press('Shift+Tab');assert(await page.locator('#account-import-dialog').evaluate(n=>n.contains(document.activeElement)||document.activeElement===document.body),'Native modal excludes background controls from reverse focus'); }
  const viewBeforeBlockedClick=await page.locator('[data-login-view].active').getAttribute('data-login-view');
  const navBox=await page.locator('[data-login-view=tasks]').boundingBox();
  await page.mouse.click(navBox.x+navBox.width/2,navBox.y+navBox.height/2);
  assert.equal(await page.locator('[data-login-view].active').getAttribute('data-login-view'),viewBeforeBlockedClick);
  checks.push('Import is a native modal over the directory; keyboard focus stays inside, background navigation is inert, Escape and Cancel clear inputs and restore the exact opener');
  await page.locator('#account-import-submit:enabled').waitFor();
  assert.equal(await page.locator('#account-vault-init, #login-vault-init').count(),0);
  assert((await page.locator('#account-vault-note').innerText()).includes('明文保存在本机'));
  assert(calls.every(c=>c.method==='GET'));checks.push('Plaintext account storage is ready immediately; no initialization controls or requests');
  initialized=false;storageMode=undefined;
  await page.locator('#account-import-cancel').click();
  await page.locator('[data-page=operations]').click();await page.locator('[data-page=login]').click();await page.locator('#account-import-open:enabled').waitFor();
  await page.locator('#account-import-open').click();assert(await page.locator('#account-import-submit').isDisabled());
  assert((await page.locator('#account-vault-note').innerText()).includes('升级宿主'));
  initialized=true;storageMode='plaintext';storageAvailable=false;
  await page.locator('#account-import-cancel').click();
  await page.locator('[data-page=operations]').click();await page.locator('[data-page=login]').click();await page.locator('#account-import-open:enabled').waitFor();
  await page.locator('#account-import-open').click();assert(await page.locator('#account-import-submit').isDisabled());
  assert((await page.locator('#account-vault-note').innerText()).includes('暂不可用'));
  storageAvailable=true;
  await page.locator('#account-import-cancel').click();
  await page.locator('[data-page=operations]').click();await page.locator('[data-page=login]').click();await page.locator('#account-import-open:enabled').waitFor();
  await page.locator('#account-import-open').click();await page.locator('#account-import-submit:enabled').waitFor();
  checks.push('Older or unavailable host storage blocks saving with an upgrade or storage explanation');
  const parsed = await page.evaluate(async()=>{const {parseAccountImport}=await import('./login.js'); const result=parseAccountImport('fixture@example.test----  pass----word  ----JBSWY3DPEHPK3PXP\r\n\n');let duplicate=false;try{parseAccountImport('a@example.test----p----s\nA@example.test----p----s');}catch{duplicate=true;}return {result,duplicate};});
  assert.equal(parsed.result[0].password,'  pass----word  ');assert(parsed.duplicate);checks.push('Bulk parser preserves password spaces and separators, rejects duplicate accounts');
  await page.locator('#account-import-text').fill('invalid row');await page.locator('#account-import-submit').click();
  assert(!calls.some(c=>c.path.endsWith('/accounts/import')));
  assert(await page.locator('#account-import-error').isVisible());
  assert.equal(await page.locator('#account-import-text').inputValue(),'invalid row');
  assert.equal(await page.locator('#account-import-error').evaluate(n=>document.activeElement===n),true);
  await capture('import-validation-light-1280');
  checks.push('Invalid import stays in the modal with visible focused validation and preserves the draft for correction');
  importFailure=true;
  await page.locator('#account-import-text').fill('storage-failure@example.test----password----JBSWY3DPEHPK3PXP');await page.locator('#account-import-submit').click();
  await page.locator('#account-import-error').filter({hasText:'存储暂不可用'}).waitFor();
  assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open),true);
  assert.equal(accounts.length,0);assert.equal(jobs.length,0);assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert(!(await page.locator('body').innerText()).includes('DO_NOT_RENDER_STORAGE_SECRET'));
  checks.push('Storage failure clears submitted plaintext and reports no success or login');
  await page.locator('#account-import-text').fill('fixture@example.test----  pass----word  ----JBSWY3DPEHPK3PXP\nsecond@example.test----password2----JBSWY3DPEHPK3PXP');
  await page.locator('#account-import-dialog .login-proxy-options summary').click();
  await page.locator('#account-import-proxy').fill('http://fixture-user:fixture-pass@127.0.0.1:7890');
  assert.match(await page.locator('#account-import-count').innerText(),/2/);
  const importWriteReached=new Promise(resolve=>{importWriteStarted=resolve;});
  const importWritesBefore=calls.filter(c=>c.path.endsWith('/accounts/import')).length;
  holdImportWrite=true;await page.locator('#account-import-submit').click();await importWriteReached;
  assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert(await page.locator('#account-import-text').isDisabled());assert(await page.locator('#account-import-submit').isDisabled());
  assert(await page.locator('#account-import-close').isDisabled());assert(await page.locator('#account-import-cancel').isDisabled());
  await page.keyboard.press('Escape');assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open),true);
  await page.locator('#account-import-form').evaluate(n=>n.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));
  await settle();assert.equal(calls.filter(c=>c.path.endsWith('/accounts/import')).length,importWritesBefore+1);
  releaseImportWrite();releaseImportWrite=null;await page.locator('#account-list .account-row').nth(1).waitFor();
  await page.locator('#account-import-dialog').waitFor({state:'hidden'});
  checks.push('Pending import clears submitted values, blocks closing and duplicate submission, then closes only after successful completion');
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/import')).at(-1).body.entries[0].password,'  pass----word  ');
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/import')).at(-1).body.proxy_url,'http://fixture-user:fixture-pass@127.0.0.1:7890');
  assert.equal(await page.locator('#account-import-proxy').inputValue(),'');
  assert(accounts.every(account=>account.proxy_url==='http://fixture-user:fixture-pass@127.0.0.1:7890'));
  assert.equal(await page.locator('#account-list [data-label=登录出口]').count(),0);
  checks.push('Batch import stores its explicit proxy on accounts without a network display column; submitted input clears and account detail exposes the saved setting');
  assert.equal(await page.locator('#account-import-text').inputValue(),'');assert.equal(jobs.length,0);checks.push('Explicit plaintext import creates records and clears plaintext without logging in');
  const row=()=>page.locator('#account-list .account-row').filter({hasText:'fixture@example.test'});
  const editorReady=async()=>page.locator('#account-editor-save:enabled').waitFor();
  assert((await row().innerText()).includes('fixture@example.test'));assert(!(await row().innerText()).includes('f***'));
  checks.push('Full account identity is visible; legacy masked fields are never used');
  await row().locator('[data-account-action=edit]').click();await editorReady();assert(await page.locator('#account-auto-relogin').isDisabled());
  assert(await page.locator('.account-main').isVisible());
  assert.equal(await page.locator('#account-editor-dialog').evaluate(n=>n.open&&n.matches(':modal')),true);
  assert.equal(await page.locator('#account-editor-close').evaluate(n=>document.activeElement===n),true);
  assert.equal(await page.locator('#account-email').inputValue(),'fixture@example.test');
  assert.equal(await page.locator('#account-password').inputValue(),'  pass----word  ');
  assert.equal(await page.locator('#account-totp').inputValue(),'JBSWY3DPEHPK3PXP');
  assert.equal(await page.locator('#account-proxy-url').inputValue(),'http://fixture-user:fixture-pass@127.0.0.1:7890');
  assert.equal(await page.locator('#account-password').getAttribute('type'),'text');
  assert.equal(await page.locator('#account-password').evaluate(n=>n.readOnly),true);
  assert.equal(jobs.length,0);assert.deepEqual(await page.evaluate(()=>({local:Object.fromEntries(Object.entries(localStorage)),session:Object.fromEntries(Object.entries(sessionStorage))})),{local:{'model-quality:management-key:/v0/management/plugins/model-quality':'offline-key'},session:{}},'Account passwords and TOTP are never persisted in browser storage; only the management key is saved');
  await page.locator('#account-delete-open').click();await page.locator('#account-delete-no').click();
  assert.equal(await page.locator('#account-password').inputValue(),'  pass----word  ');assert.equal(await page.locator('#account-totp').inputValue(),'JBSWY3DPEHPK3PXP');
  await page.locator('#account-editor-close').click();assert.equal(await page.locator('#account-password').inputValue(),'');
  assert.equal(await row().locator('[data-account-action=edit]').evaluate(n=>document.activeElement===n),true);
  await row().locator('[data-account-action=edit]').click();await editorReady();
  for(let i=0;i<16;i++){await page.keyboard.press('Tab');assert(await page.locator('#account-editor-dialog').evaluate(n=>n.contains(document.activeElement)||document.activeElement===document.body),'Editor modal excludes directory controls from tab order');}
  await page.keyboard.press('Escape');await page.locator('#account-editor-dialog').waitFor({state:'hidden'});
  assert.equal(await page.locator('#account-editor [data-login-secret]').evaluateAll(nodes=>nodes.some(n=>n.value)),false);
  assert.equal(await row().locator('[data-account-action=edit]').evaluate(n=>document.activeElement===n),true);
  checks.push('Explicit account detail shows complete email/password/TOTP without login or browser persistence; close clears them');
  checks.push('Account detail is an in-place native modal; initial focus, keyboard isolation, Escape clearing and exact row focus return work');
  accountDetailFailure=true;await row().locator('[data-account-action=edit]').click();
  await page.locator('#account-editor-error').filter({hasText:'存储暂不可用'}).waitFor();
  assert(await page.locator('#account-editor-retry').isVisible());
  assert.equal(await page.locator('#account-password').inputValue(),'');assert.equal(await page.locator('#account-totp').inputValue(),'');
  assert(!(await page.locator('#account-editor-dialog').innerText()).includes('DO_NOT_RENDER_ACCOUNT_SECRET'));
  await capture('editor-load-error-light-1280');
  await page.locator('#account-editor-retry').click();await editorReady();
  assert.equal(await page.locator('#account-password').inputValue(),'  pass----word  ');
  assert(!await page.locator('#account-editor-error').isVisible());await page.locator('#account-editor-cancel').click();
  checks.push('Detail read failure is local to the modal, clears old material and retries without navigation or exposing server text');
  const firstHeld=holdDetail();
  const firstResponse=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/accounts/detail')&&new URL(r.url()).searchParams.get('id')===accounts[0].id);
  await row().locator('[data-account-action=edit]').click();await firstHeld.received;
  await page.locator('[data-login-view=tasks]').evaluate(n=>n.click());firstHeld.release();await (await firstResponse).finished();await settle();
  assert.equal(await page.locator('#account-password').inputValue(),'');assert(!await page.locator('#account-editor').isVisible());
  await page.locator('[data-login-view=accounts]').click();
  const secondHeld=holdDetail();
  const secondResponse=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/accounts/detail')&&new URL(r.url()).searchParams.get('id')===accounts[0].id);
  await row().locator('[data-account-action=edit]').click();await secondHeld.received;
  await page.locator('#account-editor-cancel').click();assert(await page.locator('.account-main').isVisible());
  await page.locator('#account-list .account-row').filter({hasText:'second@example.test'}).locator('[data-account-action=edit]').click();await editorReady();
  assert.equal(await page.locator('#account-email').inputValue(),'second@example.test');assert.equal(await page.locator('#account-password').inputValue(),'password2');
  secondHeld.release();await (await secondResponse).finished();await settle();
  assert.equal(await page.locator('#account-email').inputValue(),'second@example.test');assert.equal(await page.locator('#account-password').inputValue(),'password2');
  await page.locator('#account-editor-close').click();
  checks.push('Delayed secret detail cannot repopulate a closed view or overwrite another selected account');
  const firstAccountBeforeOptionalStart={...accounts[0]};
  await row().locator('[data-account-action=edit]').click();await editorReady();
  assert(await page.locator('#account-login-options').isVisible());
  await page.locator('#account-login-options summary').click();await page.locator('#account-login-target').selectOption(target.auth_id);
  await page.locator('#account-login-start').click();await page.locator('#account-editor-dialog').waitFor({state:'hidden'});
  await row().locator('.account-row-actions [data-account-action=task]').waitFor();
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/start')).at(-1).body.target_auth_id,target.auth_id);
  assert.equal(jobs[0].intent,'reauthorize');assert.equal(accounts[0].auth_id,'');assert(await page.locator('#login-view-accounts').isVisible());
  assert(!calls.some(c=>c.path.endsWith('/commit')));
  accounts[0]={...firstAccountBeforeOptionalStart};jobs.splice(0);await refresh();
  checks.push('Optional existing-credential target stays available in the detail modal and submits only on explicit login without binding or automatic navigation');
  await page.locator('#account-search').fill('fixture@example.test');
  const startQuery=await page.locator('#account-search').inputValue();
  const startCount=calls.filter(c=>c.path.endsWith('/accounts/start')).length;
  const startWriteReached=new Promise(resolve=>{startWriteStarted=resolve;});holdStartWrite=true;
  await row().locator('[data-account-action=start]').click();await startWriteReached;
  assert(await page.locator('#login-view-accounts').isVisible());assert(!await page.locator('#login-view-tasks').isVisible());
  assert.equal(await page.locator('dialog[open]').count(),0);
  assert(await row().locator('[data-account-action=start]').isDisabled());
  await row().locator('[data-account-action=start]').evaluate(n=>n.dispatchEvent(new MouseEvent('click',{bubbles:true})));
  await settle();assert.equal(calls.filter(c=>c.path.endsWith('/accounts/start')).length,startCount+1);
  releaseStartWrite();releaseStartWrite=null;await row().locator('.account-row-actions [data-account-action=task]').waitFor();
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/start')).at(-1).body.target_auth_id,undefined);
  assert.equal(await page.locator('#account-search').inputValue(),startQuery);assert(await page.locator('#login-view-accounts').isVisible());
  assert(await row().locator('[data-account-action=start]').isDisabled());assert.equal(accounts[0].auth_id,'');
  await capture('login-started-in-list-light-1280');
  assert.equal(await page.locator('#login-notice').count(),0);
  const loginJob=jobs[0];
  Object.assign(loginJob,{status:'committing',phase:'saving',revision:loginJob.revision+1});
  Object.assign(accounts[0],{last_job_status:'committing',revision:accounts[0].revision+1});
  await refresh();await row().locator('.account-job-status').filter({hasText:'保存并启用中'}).waitFor();
  assert(await row().locator('[data-account-action=start]').isDisabled());assert(await page.locator('#login-view-accounts').isVisible());
  assert.equal(await page.locator('dialog[open]').count(),0);
  Object.assign(loginJob,{status:'committed',phase:'',credential_enabled:true,target_auth_id:target.auth_id,revision:loginJob.revision+1});
  Object.assign(accounts[0],{auth_id:target.auth_id,identity_version:target.identity_version,last_job_status:'committed',last_job_credential_enabled:true,last_login_at:'2026-11-15T20:01:02Z',health_status:'unknown',health_reason:'awaiting_inspection',revision:accounts[0].revision+1});
  await refresh();await row().locator('.account-job-status').filter({hasText:'已保存并启用'}).waitFor();
  assert(await page.locator('#login-view-accounts').isVisible());assert.equal(await page.locator('dialog[open]').count(),0);
  assert.equal(await page.locator('#account-search').inputValue(),startQuery);assert.equal(await page.locator('[data-login-action=commit]').count(),0);
  assert(!calls.some(c=>c.path.endsWith('/commit')),'Successful account login never sends a second commit request');
  assert.equal(await row().locator('.account-status .tag').evaluate(node=>node.classList.contains('good')),false,'Enabled credential does not imply verified CPA health');
  await capture('login-completed-in-list-light-1280');
  await page.setViewportSize({width:390,height:844});await capture('login-completed-in-list-light-390');await page.setViewportSize({width:1280,height:800});
  await row().locator('.account-row-actions [data-account-action=task]').click();await page.locator('#login-job-detail h3').filter({hasText:'已保存并启用'}).waitFor();
  assert.match(await page.locator('#login-job-detail').innerText(),/无需再次确认/);assert.equal(await page.locator('[data-login-action=commit]').count(),0);
  await page.locator('[data-login-view=accounts]').click();await page.locator('#account-search').fill('');
  checks.push('One row action completes 2FA, background saving, CPA binding and enablement in place, with no confirmation, second write, modal or automatic navigation; observed CPA health stays distinct');
  const boundBeforeStart={...accounts[0]};
  const boundStartResponse=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/accounts/start'));
  await row().locator('[data-account-action=start]').click();await (await boundStartResponse).finished();await refresh();
  const boundStart=calls.filter(c=>c.path.endsWith('/accounts/start')).at(-1);
  assert.equal(boundStart.body.id,accounts[0].id);assert.equal(boundStart.body.target_auth_id,undefined);assert.equal(jobs.at(-1).intent,'reauthorize');
  assert(await page.locator('#login-view-accounts').isVisible());assert.equal(await page.locator('dialog[open]').count(),0);
  jobs.splice(1);accounts[0]={...boundBeforeStart};await refresh();
  checks.push('Bound-account relogin directly uses its existing backend binding and remains in the directory');
  const beforeSecurityAccount={...accounts[0]},beforeSecurityJobs=[...jobs],beforeSecurityCredentials={...credentials.get(accounts[0].id)};
  const securityCalls=()=>calls.filter(c=>c.path.endsWith('/accounts/security'));
  const openSecurity=async action=>{await row().locator('[data-account-action=more]').click();await page.locator(action==='reset_totp'?'#account-reset-totp':'#account-logout-all').click();await page.locator('#account-security-dialog').waitFor();};
  await row().locator('[data-account-action=more]').click();assert(await page.locator('#account-reset-totp').isVisible());
  assert(await page.locator('#account-logout-all').isVisible());await capture('security-menu-light-1280');await page.keyboard.press('Escape');
  assert.equal(await row().locator('[data-account-action=more]').evaluate(n=>document.activeElement===n),true);
  const securityCallsBefore=securityCalls().length;
  await openSecurity('reset_totp');assert.equal(securityCalls().length,securityCallsBefore);
  assert(!await page.locator('#account-security-logout').isChecked());assert(await page.locator('#account-security-reauthorize').isChecked());
  assert.equal(await page.locator('#account-security-identity').innerText(),accounts[0].email);
  assert.match(await page.locator('#account-security-description').innerText(),/旧验证器/);
  for(const theme of ['light','white','dark'])for(const viewport of [{width:1280,height:720},{width:390,height:844}]){
    await page.locator('#theme-toggle').evaluate((node,theme)=>{node.value=theme;node.dispatchEvent(new Event('change',{bubbles:true}));},theme);await page.setViewportSize(viewport);await capture(`security-reset-${theme}-${viewport.width}`);await assertViewportAction('#account-security-submit');
  }
  await page.setViewportSize({width:1280,height:800});await page.locator('#theme-toggle').evaluate(node=>{node.value='light';node.dispatchEvent(new Event('change',{bubbles:true}));});
  await page.locator('#account-security-cancel').click();assert.equal(securityCalls().length,securityCallsBefore);
  assert.equal(await row().locator('[data-account-action=more]').evaluate(n=>document.activeElement===n),true);
  await openSecurity('reset_totp');await page.locator('#account-security-logout').check();
  const securityReached=new Promise(resolve=>{securityWriteStarted=resolve;});holdSecurityWrite=true;
  await page.locator('#account-security-submit').click();await Promise.race([securityReached,new Promise((_,reject)=>setTimeout(()=>reject(new Error('Security request was not sent: '+JSON.stringify(errors))),10000))]);
  assert(await page.locator('#account-security-submit').isDisabled());assert(await page.locator('#account-security-close').isDisabled());
  await page.keyboard.press('Escape');assert(await page.locator('#account-security-dialog').evaluate(n=>n.open));
  await page.locator('#account-security-form').evaluate(n=>n.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));await settle();assert.equal(securityCalls().length,securityCallsBefore+1);
  releaseSecurityWrite();releaseSecurityWrite=null;await page.locator('#account-security-dialog').waitFor({state:'hidden'});await refresh();
  assert.deepEqual(securityCalls().at(-1).body,{id:beforeSecurityAccount.id,revision:beforeSecurityAccount.revision,action:'reset_totp',logout_all_devices:true,reauthorize:true});
  assert(await page.locator('#login-view-accounts').isVisible());assert.match(await row().locator('.account-operation-type').innerText(),/重置 2FA/);
  assert.match(await row().locator('.account-activity').innerText(),/登录账号安全会话/);assert(await row().locator('[data-account-action=start]').isDisabled());
  await capture('security-running-light-1280');
  const resetJob=jobs.at(-1);Object.assign(resetJob,{status:'failed',phase:'',error_code:'exchange_failed',totp_reset:true,devices_logged_out:true,security_status:'complete'});
  Object.assign(accounts[0],{last_job_status:'failed',last_job_totp_reset:true,last_job_devices_logged_out:true,revision:accounts[0].revision+1});credentials.get(accounts[0].id).totp_secret='KRSXG5DSNFXGOIDB';await refresh();
  assert.match(await row().locator('.account-activity').innerText(),/后续步骤未完成/);assert.match(await row().locator('.account-activity').innerText(),/新 2FA 已保存/);
  await row().locator('[data-account-action=edit]').click();await editorReady();assert.equal(await page.locator('#account-totp').inputValue(),'KRSXG5DSNFXGOIDB');await page.locator('#account-editor-close').click();
  await row().locator('.account-row-actions [data-account-action=task]').click();await page.locator('#login-job-detail').filter({hasText:'旧密钥不会恢复'}).waitFor();await page.locator('[data-login-view=accounts]').click();
  checks.push('Security actions use one focused confirmation, explicit reset/logout/reauthorization choices, duplicate-submit protection and inline progress; a later OAuth failure preserves and shows the saved new TOTP');
  await openSecurity('logout_all');assert(!await page.locator('#account-security-logout-option').isVisible());await page.locator('#account-security-reauthorize').uncheck();
  await capture('security-logout-light-1280');await page.locator('#account-security-submit').click();await page.locator('#account-security-dialog').waitFor({state:'hidden'});await refresh();
  assert.equal(securityCalls().at(-1).body.action,'logout_all');assert.equal(securityCalls().at(-1).body.logout_all_devices,false);assert.equal(securityCalls().at(-1).body.reauthorize,false);
  const logoutJob=jobs.at(-1);Object.assign(logoutJob,{status:'succeeded',phase:'',security_status:'complete',devices_logged_out:true});Object.assign(accounts[0],{last_job_status:'succeeded',last_job_devices_logged_out:true,revision:accounts[0].revision+1});await refresh();
  assert.match(await row().locator('.account-job-status').innerText(),/安全操作已完成/);assert(await row().locator('[data-account-action=start]').isEnabled());
  checks.push('Logout can run independently with CPA reauthorization explicitly disabled and reports completion without a credential upload');
  await openSecurity('reset_totp');securityWriteUnknown=true;await page.locator('#account-security-submit').click();await page.locator('#account-security-error').waitFor();
  assert(await page.locator('#account-security-submit').isDisabled());await page.locator('#account-security-cancel').click();
  Object.assign(resetJob,{status:'security_uncertain',security_status:'uncertain',totp_reset:false,devices_logged_out:false,error_code:'security_uncertain'});
  Object.assign(accounts[0],{security_pending:true,last_job_id:resetJob.id,last_job_status:'security_uncertain',last_job_action:'reset_totp',last_job_totp_reset:false,last_job_devices_logged_out:false,revision:accounts[0].revision+1});
  securityRecoveries.set(accounts[0].id,{job_id:resetJob.id,new_totp_secret:'MFRGGZDFMZTWQ2LK',activation_confirmed:false,can_complete:false,phase:'totp_activate'});await refresh();
  assert(await row().locator('[data-account-action=start]').isDisabled());await row().locator('[data-account-action=more]').click();
  assert(await row().locator('[data-account-action=reset_totp]').isDisabled());assert(await page.locator('#account-logout-all').isDisabled());assert(await row().locator('[data-account-action=delete]').isDisabled());await page.keyboard.press('Escape');
  await row().locator('[data-account-action=edit]').click();await page.locator('#account-security-recovery').waitFor();await page.waitForFunction(()=>document.querySelector('#account-security-recovery-secret').value==='MFRGGZDFMZTWQ2LK');
  assert(await page.locator('#account-editor-save').isDisabled());assert(await page.locator('#account-replace-secrets').isDisabled());assert(await page.locator('#account-delete-open').isDisabled());
  assert(!await page.locator('#account-security-complete').isVisible());assert(await page.locator('#account-security-recovery-secret').isEnabled());assert(await page.locator('#account-security-recovery-secret').evaluate(n=>n.readOnly));assert.match(await page.locator('#account-security-recovery-note').innerText(),/不代表它已生效/);
  await capture('security-unconfirmed-light-1280');await page.locator('#account-editor-close').click();assert.equal(await page.locator('#account-security-recovery-secret').inputValue(),'');
  securityRecoveries.set(accounts[0].id,{job_id:resetJob.id,new_totp_secret:'MFRGGZDFMZTWQ2LK',activation_confirmed:true,can_complete:true,phase:'totp_activate'});
  await row().locator('[data-account-action=edit]').click();await page.locator('#account-security-complete:enabled').waitFor();assert.match(await page.locator('#account-security-recovery-note').innerText(),/不会退出设备、重新授权或上传 CPA/);
  const completeCallBoundary=calls.length;await page.locator('#account-security-complete').click();await page.locator('#account-editor-dialog').waitFor({state:'hidden'});await refresh();
  assert.deepEqual(calls.slice(completeCallBoundary).filter(c=>c.method!=='GET').map(c=>c.path.split('/login')[1]),['/accounts/security/complete']);
  assert.equal(credentials.get(accounts[0].id).totp_secret,'MFRGGZDFMZTWQ2LK');assert(!accounts[0].security_pending);
  checks.push('Unknown security submissions cannot be resubmitted; unresolved accounts block mutations and show unconfirmed secrets only in authenticated details; proven activation enables local-only completion');
  accounts[0]={...beforeSecurityAccount};jobs.splice(0,jobs.length,...beforeSecurityJobs);credentials.set(accounts[0].id,beforeSecurityCredentials);securityRecoveries.clear();await refresh();
  await row().locator('[data-account-action=edit]').click();await editorReady();await page.locator('#account-label').fill('备用账号');await page.locator('#account-auto-relogin').check();await page.locator('#account-editor-save').click();
  await page.locator('#account-list').filter({hasText:'备用账号'}).waitFor();assert(accounts[0].auto_relogin);assert(!settings.auto_relogin_enabled);checks.push('Account automation permission remains separate from global inspection and reauthorization');
  await row().locator('[data-account-action=edit]').click();await editorReady();await page.locator('#account-replace-secrets').check();
  assert.equal(await page.locator('#account-email').inputValue(),'fixture@example.test');await page.locator('#account-password').fill('replacement-password');await page.locator('#account-totp').fill('JBSWY3DPEHPK3PXP');
  const accountWriteReached=new Promise(resolve=>{accountWriteStarted=resolve;});
  holdAccountWrite=true;await page.locator('#account-editor-save').click();await accountWriteReached;
  assert(releaseAccountWrite);assert(await page.locator('#account-password').isDisabled());assert.equal(await page.locator('#account-password').inputValue(),'');
  assert(await page.locator('#account-replace-secrets').isDisabled());
  assert(await page.locator('#account-editor-close').isDisabled());assert(await page.locator('#account-editor-cancel').isDisabled());
  await page.keyboard.press('Escape');assert.equal(await page.locator('#account-editor-dialog').evaluate(n=>n.open),true);
  const pendingAccountWrites=calls.filter(c=>c.path.endsWith('/accounts/entry')&&c.method==='PUT').length;
  await page.locator('#account-editor').evaluate(n=>n.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true})));await settle();
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/entry')&&c.method==='PUT').length,pendingAccountWrites);
  releaseAccountWrite();releaseAccountWrite=null;await page.locator('#account-editor').waitFor({state:'hidden'});
  assert.equal(await page.locator('#account-editor [data-login-secret]').evaluateAll(nodes=>nodes.some(n=>n.value)),false);
  checks.push('Pending account save locks secret fields and clears submitted values before and after completion');
  await row().locator('[data-account-action=edit]').click();await editorReady();
  await page.locator('#account-proxy-url').fill('direct');await page.locator('#account-editor-save').click();await page.locator('#account-editor-dialog').waitFor({state:'hidden'});
  assert.equal(accounts[0].proxy_url,'direct');
  await row().locator('[data-account-action=edit]').click();await editorReady();
  assert.equal(await page.locator('#account-proxy-url').inputValue(),'direct');
  await page.locator('#account-proxy-url').fill('');await page.locator('#account-editor-save').click();await page.locator('#account-editor-dialog').waitFor({state:'hidden'});
  assert.equal(accounts[0].proxy_url,'');
  checks.push('Account proxy can explicitly select direct connection or clear its override, persists only on save, and clears from the closed dialog');
  for(const status of ['commit_unknown','saved_not_adopted']) {
    accounts[0].last_job_status=status;accounts[0].revision++;
    await row().locator('[data-account-action=start]').filter({hasText:'等待核对'}).waitFor();assert(await row().locator('[data-account-action=start]').isDisabled());
  }
  accounts[0].last_job_status='committed';accounts[0].revision++;await row().locator('[data-account-action=start]:enabled').waitFor();
  checks.push('Unresolved credential commits block new account login until reconciliation');
  await page.locator('[data-login-view=settings]').click();assert(await page.locator('#login-view-settings').isVisible());
  if(!await page.locator('#account-settings').evaluate(n=>n.open))await page.locator('#account-settings summary').click();await page.locator('#account-inspection-enabled').check();await page.locator('#account-auto-enabled').check();await page.locator('#account-interval').fill('25');
  const beforeProxySave=jobs.length;await page.locator('#account-default-proxy').fill('socks5h://127.0.0.1:1080');assert.equal(settings.login_proxy_url,'');
  assert.equal(settings.interval_minutes,15);await page.locator('#account-settings-save').click();await page.locator('#account-schedule-summary').filter({hasText:'25'}).waitFor();
  assert(settings.inspection_enabled&&settings.auto_relogin_enabled);assert.equal(settings.interval_minutes,25);checks.push('Interval and independent switches persist only on explicit save');
  assert.equal(settings.login_proxy_url,'socks5h://127.0.0.1:1080');assert.equal(jobs.length,beforeProxySave);
  await page.locator('[data-login-view=accounts]').click();assert.equal(await page.locator('.account-network').count(),0);
  await page.locator('#account-settings-open').click();assert.equal(await page.locator('#account-default-proxy').evaluate(n=>document.activeElement===n),true);assert.equal(await page.locator('#account-default-proxy').inputValue(),'socks5h://127.0.0.1:1080');
  checks.push('Global login proxy saves explicitly without login, remains available in the settings view, and the directory shortcut opens its setting');
  const draftRevision=settings.revision;
  await page.locator('#account-interval').fill('35');
  const writesBeforeSettingsNavigation=calls.filter(c=>c.method!=='GET').length;
  for(const view of ['accounts','tasks','settings'])await page.locator(`[data-login-view=${view}]`).click();
  assert.equal(await page.locator('#account-interval').inputValue(),'35');assert.equal(settings.interval_minutes,25);
  assert.equal(calls.filter(c=>c.method!=='GET').length,writesBeforeSettingsNavigation);
  checks.push('Inspection is a separate view; switching views preserves its unsaved draft and never submits or starts login');
  settings={...settings,revision:settings.revision+1,interval_minutes:40,auto_relogin_enabled:false};
  await page.locator('#account-schedule-summary').filter({hasText:'40'}).waitFor();
  assert.equal(await page.locator('#account-interval').inputValue(),'35');
  await page.locator('#account-settings-save').click();
  await page.locator('#account-settings-note').filter({hasText:'已载入最新值'}).waitFor();
  assert.equal(calls.filter(c=>c.path.endsWith('/accounts/settings')&&c.method==='PUT').at(-1).body.revision,draftRevision);
  assert.equal(settings.interval_minutes,40);assert.equal(await page.locator('#account-interval').inputValue(),'40');assert(!await page.locator('#account-auto-enabled').isChecked());
  await page.locator('#account-auto-enabled').check();await page.locator('#account-interval').fill('25');await page.locator('#account-settings-save').click();await page.locator('#account-schedule-summary').filter({hasText:'25'}).waitFor();
  assert(settings.auto_relogin_enabled);checks.push('Polling preserves a settings draft revision; conflicting save cannot overwrite another operator and reloads current values');
  await page.locator('[data-login-view=accounts]').click();
  await page.locator('#account-inspect').click();await page.locator('#account-list').filter({hasText:'需要重新授权'}).waitFor();assert.equal(jobs.length,1);checks.push('Inspect-now reads CPA state without starting a login');
  await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('temporary@example.test----must-clear----JBSWY3DPEHPK3PXP');
  await page.locator('[data-page=manual]').evaluate(n=>n.click());assert.equal(await page.locator('#account-import-text').inputValue(),'');assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open),false);await page.locator('[data-page=login]').click();await page.locator('#account-import-open:enabled').waitFor();
  checks.push('Programmatic feature navigation forcibly closes the modal and clears bulk secrets; reopening restores account records');
  const originalAccounts=accounts.map(a=>({...a}));
  const originalJobs=jobs.map(job=>({...job}));
  const currentLogJob={...jobs[0],id:'logs-current',account_id:accounts[0].id,status:'failed',phase:'totp',error_code:'interaction_required',error_stage:'totp',error_reason:'email_verification',created_at:'2026-11-16T04:00:00Z',expires_at:'2099-01-01T00:00:00Z'};
  const historyLogJob={...currentLogJob,id:'logs-history',created_at:'2026-11-15T04:00:00Z',status:'cancelled',error_code:'',error_stage:'',error_reason:''};
  const otherLogJob={...currentLogJob,id:'logs-other-account',account_id:accounts[1].id,email:accounts[0].email,created_at:'2026-11-16T03:00:00Z',status:'interrupted',error_code:'interrupted',error_stage:'',error_reason:''};
  const currentLogEntries=Array.from({length:80},(_,i)=>({id:i+1,time:new Date(Date.UTC(2026,10,16,4,0,i)).toISOString(),level:i===79?'error':'info',status:i===79?'failed':'running',phase:i===79?'totp':'authorization',...(i===79?{error_code:'interaction_required',error_stage:'totp',error_reason:'email_verification'}:{})}));
  currentLogEntries[0].upstream_payload={access_token:'DO_NOT_RENDER_ENTRY_SECRET'};
  logRecords.set(currentLogJob.id,{job_id:currentLogJob.id,entries:currentLogEntries,legacy:false});
  logRecords.set(historyLogJob.id,{job_id:historyLogJob.id,entries:[{id:1,time:'2026-11-15T04:00:00Z',level:'warn',status:'cancelled',phase:'password'}],legacy:false});
  logRecords.set(otherLogJob.id,{job_id:otherLogJob.id,entries:[{id:1,time:'2026-11-16T03:00:00Z',level:'warn',status:'interrupted',phase:'review'}],legacy:false});
  jobs.splice(0,jobs.length,historyLogJob,otherLogJob,currentLogJob);
  Object.assign(accounts[0],{last_job_id:currentLogJob.id,last_job_status:currentLogJob.status,last_job_error_code:currentLogJob.error_code,last_job_error_stage:currentLogJob.error_stage,last_job_error_reason:currentLogJob.error_reason});
  Object.assign(accounts[1],{last_job_id:otherLogJob.id,last_job_status:otherLogJob.status});
  await refresh();
  const logsControl=action=>page.locator(`[data-account-log-action="${action}"]`);
  const logEntries=()=>page.locator('.account-log-panel .account-log-entry');
  const logWritesBefore=calls.filter(call=>call.method!=='GET').length;
  const firstAccountLogRow=()=>page.locator('#account-list .account-row').filter({hasText:'fixture@example.test'});
  const secondAccountLogRow=()=>page.locator('#account-list .account-row').filter({hasText:'second@example.test'});
  assert.equal(await page.locator('.account-table th').filter({hasText:'到期时间'}).count(),0,'Unsupported account expiry is removed');
  assert(!(await firstAccountLogRow().innerText()).includes('2099'),'Task expiry is never presented as account metadata');
  await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===80);
  assert.equal(await page.locator('#account-list .account-log-row').count(),1);
  assert.equal(await firstAccountLogRow().evaluate(row=>row.nextElementSibling?.classList.contains('account-log-row')),true,'Logs expand immediately under their account');
  assert.equal(await page.locator('.account-log-row > td').getAttribute('colspan'),'6');
  assert.equal(await page.locator('dialog[open]').count(),0);assert(await page.locator('#login-view-accounts').isVisible());
  assert.equal(await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').getAttribute('aria-expanded'),'true');
  assert.match(await page.locator('.account-log-panel').innerText(),/操作日志/);
  assert.match(await page.locator('.account-log-panel').innerText(),/时间[\s\S]*类别[\s\S]*执行内容/);
  assert.equal(calls.filter(call=>call.method!=='GET').length,logWritesBefore);
  checks.push('Logs open inline immediately below the selected account, span all six columns, remain read-only and keep the operator in the account directory');
  await logsControl('errors').click();assert.equal(await logEntries().count(),1);
  assert.match(await page.locator('.account-log-panel').innerText(),/验证 2FA[\s\S]*需要邮箱验证码/);
  await logEntries().first().locator('summary').click();
  assert.match(await logEntries().first().locator('pre').innerText(),/email_verification/);
  await logsControl('all').click();assert.equal(await logEntries().count(),80);
  await page.evaluate(()=>{Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async text=>{window.__accountCopiedLogs=text;}}});});
  await logsControl('copy').click();
  await page.waitForFunction(()=>typeof window.__accountCopiedLogs==='string'&&window.__accountCopiedLogs.length>0);
  const copiedLogs=await page.evaluate(()=>window.__accountCopiedLogs);
  const copiedEntries=copiedLogs.trim().split('\n').map(line=>JSON.parse(line));
  assert.equal(copiedEntries.length,80);assert.equal(copiedEntries.at(-1).error_reason,'email_verification');assert(copiedEntries.every(entry=>entry.job_id===currentLogJob.id));
  assert(!copiedLogs.includes('replacement-password')&&!copiedLogs.includes('JBSWY3DPEHPK3PXP')&&!copiedLogs.includes('DO_NOT_RENDER_ENTRY_SECRET'));
  await page.evaluate(()=>{navigator.clipboard.writeText=async()=>{throw new Error('clipboard unavailable');};});
  await logsControl('copy').click();await page.locator('.account-log-read-error').filter({hasText:'复制失败'}).waitFor();
  assert.equal(await logEntries().count(),80,'Clipboard failure preserves visible logs');
  checks.push('The log panel filters abnormal events, expands real stored events, copies only documented visible records, and handles clipboard denial without losing content or exposing extra upstream fields');
  await logsControl('history').click();
  await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===81);
  const historyCalls=calls.filter(call=>call.path.endsWith('/logs'));
  assert(historyCalls.some(call=>call.query.id===historyLogJob.id));
  assert(!historyCalls.some(call=>call.query.id===otherLogJob.id),'An identical email does not authorize reading a different account history');
  assert.match(await page.locator('.account-log-panel').innerText(),/历史/);
  await logsControl('current').click();await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===80);
  checks.push('History groups only jobs explicitly owned by the selected account; a same-email task belonging to another account is excluded, and the current operation can be restored');
  await logEntries().first().locator('summary').click();
  assert.equal(await logEntries().first().locator('details').evaluate(node=>node.open),true);
  await page.locator('#account-log-viewport').evaluate(viewport=>{viewport.scrollTop=70;viewport.dispatchEvent(new Event('scroll'));});
  const logScrollBefore=await page.locator('#account-log-viewport').evaluate(viewport=>viewport.scrollTop);
  assert(logScrollBefore>0);
  await logsControl('all').focus();
  accounts[0].label='Polling preserves an open account log';accounts[0].revision++;
  await refresh();await settle();
  assert.equal(await page.locator('#account-log-viewport').evaluate(viewport=>viewport.scrollTop),logScrollBefore);
  assert.equal(await logEntries().first().locator('details').evaluate(node=>node.open),true);
  assert.equal(await logsControl('all').evaluate(button=>document.activeElement===button),true);
  assert.equal(await logsControl('follow').getAttribute('aria-pressed'),'false');
  checks.push('Account polling preserves the expanded log, raw record disclosure, keyboard focus and manually chosen local scroll position; manual scrolling pauses follow-latest');
  const summaryFocusKey=await logEntries().first().locator('details').getAttribute('data-log-key');
  await logEntries().first().locator('summary').focus();
  currentLogJob.status='interaction_required';currentLogJob.revision++;accounts[0].last_job_status=currentLogJob.status;
  await refresh();await settle();
  assert.equal(await page.evaluate(()=>document.activeElement?.closest('details')?.dataset.logKey),summaryFocusKey,'A task transition preserves raw-record summary focus even when the log viewport is rebuilt');
  checks.push('Task transitions preserve keyboard focus on the same expanded raw log record across rebuilt log content');
  logFailures.add(currentLogJob.id);
  await logsControl('refresh').click();
  await page.locator('.account-log-read-error').filter({hasText:'重试'}).waitFor({state:'visible'});
  assert.equal(await logEntries().count(),80,'A log refresh failure retains previously loaded events');
  assert(!(await page.locator('.account-log-panel').innerText()).includes('DO_NOT_RENDER_LOG_SECRET'));
  await capture('logs-refresh-error-light-1280',false);
  await logsControl('refresh').click();
  await page.locator('.account-log-read-error').waitFor({state:'hidden'});
  checks.push('A failed log read preserves already loaded records and offers retry without rendering raw server messages');
  const delayedLogRead=holdLogs(currentLogJob.id);
  const delayedLogResponse=page.waitForResponse(response=>new URL(response.url()).pathname.endsWith('/model-quality/login/logs')&&new URL(response.url()).searchParams.get('id')===currentLogJob.id);
  await logsControl('refresh').click();await delayedLogRead.received;
  await secondAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===1);
  assert.equal(await page.locator('#account-list .account-log-row').count(),1);
  assert.equal(await secondAccountLogRow().evaluate(row=>row.nextElementSibling?.classList.contains('account-log-row')),true);
  delayedLogRead.release();await (await delayedLogResponse).finished();await settle();
  assert.equal(await logEntries().count(),1);assert.match(await page.locator('.account-log-panel').innerText(),/已中断/);
  await secondAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();assert.equal(await page.locator('.account-log-row').count(),0);
  checks.push('Only one account log opens at a time; a delayed response cannot overwrite another account, and the same Logs action closes it');
  const legacyLogJob={...currentLogJob,id:'logs-legacy',error_stage:'',error_reason:''};
  jobs.push(legacyLogJob);Object.assign(accounts[0],{last_job_id:legacyLogJob.id,last_job_error_stage:'',last_job_error_reason:''});
  await refresh();await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  await page.waitForFunction(()=>document.querySelector('.account-log-panel')?.textContent.match(/旧记录|旧任务|历史任务|步骤日志/));
  assert.equal(await logEntries().count(),0,'Legacy tasks never acquire invented successful login steps');
  await capture('logs-legacy-light-1280');
  await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  Object.assign(accounts[0],{last_job_id:currentLogJob.id,last_job_error_stage:'totp',last_job_error_reason:'email_verification'});
  await refresh();await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===80);
  for(const theme of ['light','white','dark'])for(const viewport of [{width:1554,height:605},{width:390,height:844}]) {
    await page.locator('#theme-toggle').selectOption(theme);await page.setViewportSize(viewport);await settle();
    assert(await page.locator('#account-log-viewport').evaluate(viewport=>viewport.scrollHeight>viewport.clientHeight&&viewport.clientHeight<=271),'Log events use a bounded local scroll area');
    await capture(`logs-${theme}-${viewport.width}`,false);
  }
  await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  assert.equal(calls.filter(call=>call.method!=='GET').length,logWritesBefore);
  checks.push('Legacy jobs explain unavailable step history without invented events; inline logs fit three themes at desktop and phone widths with a bounded local scrollbar');
  const suppliedLogID='7d020b6f-69fe-4b63-9cd0-0d1ff48a5d05';
  const suppliedLogEntries=[
    {id:1,time:'2026-11-16T11:51:13.126737Z',level:'info',status:'queued',phase:'queued'},
    {id:2,time:'2026-11-16T11:51:13.140813Z',level:'info',status:'running',phase:'authorization'},
    {id:3,time:'2026-11-16T11:51:15.757957Z',level:'warn',status:'interaction_required',phase:'',error_code:'interaction_required',error_stage:'authorization',error_reason:'unsupported_request'},
  ];
  const suppliedJob={...currentLogJob,id:suppliedLogID,status:'interaction_required',phase:'',error_code:'interaction_required',error_stage:'authorization',error_reason:'unsupported_request',created_at:suppliedLogEntries[0].time};
  jobs.push(suppliedJob);logRecords.set(suppliedLogID,{job_id:suppliedLogID,entries:suppliedLogEntries,legacy:false});
  Object.assign(accounts[0],{last_job_id:suppliedLogID,last_job_status:suppliedJob.status,last_job_error_code:suppliedJob.error_code,last_job_error_stage:suppliedJob.error_stage,last_job_error_reason:suppliedJob.error_reason});
  await refresh();await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  await page.waitForFunction(()=>document.querySelectorAll('.account-log-panel .account-log-entry').length===3);
  const suppliedResult=page.locator('.account-log-result');
  await suppliedResult.waitFor({state:'visible'});
  const suppliedExplanation=await suppliedResult.innerText();
  assert.match(suppliedExplanation,/2\.63\s*秒/,'Elapsed time comes from the first and last real event timestamps');
  assert.match(suppliedExplanation,/授权/);assert.match(suppliedExplanation,/尚未适配|暂不支持|未适配/);assert.match(suppliedExplanation,/运行时|运行环境|自动登录/);assert(!suppliedExplanation.includes('临时登录'),'Recovery advice must not direct users to the removed page');
  assert(!/(账号已异常|账号被封禁|密码错误|2FA错误|验证码错误)/.test(suppliedExplanation),'A blocked runtime request does not prove account, password or 2FA failure');
  assert.match(await logEntries().first().locator('summary').innerText(),/排队|等待/);
  assert.match(await logEntries().nth(1).locator('summary').innerText(),/登录中|进行|授权/);
  assert(!(await logEntries().nth(1).locator('summary').innerText()).includes('完成登录授权'),'A running authorization phase does not claim completed authorization');
  assert.equal(await logEntries().last().evaluate(node=>node.classList.contains('is-warning')),true);
  for(const theme of ['light','white','dark'])for(const viewport of [{width:1554,height:805},{width:390,height:844}]) {
    await page.locator('#theme-toggle').selectOption(theme);await page.setViewportSize(viewport);await capture(`logs-supplied-request-${theme}-${viewport.width}`);
  }
  await logEntries().last().locator('summary').click();
  const suppliedRaw=JSON.parse(await logEntries().last().locator('pre').innerText());
  assert.deepEqual(suppliedRaw,{...suppliedLogEntries[2],job_id:suppliedLogID});
  assert.equal(calls.filter(call=>call.method!=='GET').length,logWritesBefore);
  await firstAccountLogRow().locator('.account-row-actions [data-account-action=logs]').click();
  checks.push('The exact supplied three-event log explains the unsupported authorization request as a runtime compatibility stop, keeps the original safe event fields, avoids account/password/2FA failure claims and opens read-only across desktop and phone themes');
  accounts.splice(0,accounts.length,...originalAccounts.map(account=>({...account})));jobs.splice(0,jobs.length,...originalJobs);
  await page.locator('#theme-toggle').selectOption('light');await page.setViewportSize({width:1280,height:800});await refresh();
  const diagnosticAccount={...originalAccounts[0],last_job_id:'diagnostic-fixture',last_job_status:'interaction_required',last_job_error_code:'interaction_required',last_job_error_stage:'totp',last_job_error_reason:'email_verification'};
  accounts.splice(0,accounts.length,diagnosticAccount);await refresh();
  assert.match(await page.locator('#account-list .account-activity').innerText(),/登录未完成/);
  assert.match(await page.locator('#account-list').innerText(),/验证 2FA[\s\S]*需要邮箱验证码/);
  await capture('account-diagnostic-light-1280');
  Object.assign(diagnosticAccount,{last_job_error_stage:'',last_job_error_reason:''});await refresh();
  assert.match(await page.locator('#account-list').innerText(),/未记录具体原因/);
  assert(!(await page.locator('#account-list').innerText()).includes('请使用官方浏览器授权'));
  await capture('account-historical-failure-light-1280');
  Object.assign(diagnosticAccount,{last_job_status:'failed',last_job_error_code:'runtime_failed',last_job_error_stage:'authorization',last_job_error_reason:'proxy_connection_failed'});await refresh();
  assert.match(await page.locator('#account-list').innerText(),/打开授权页[\s\S]*代理连接失败/);
  checks.push('Account columns show authorization outcomes, full diagnostic stage and cause directly; old records explicitly state missing evidence');
  for(const [proxy,error,copy] of [['socks5://fixture-user:fixture-pass@127.0.0.1:1080','proxy_auth_unsupported','SOCKS 代理暂不支持用户名和密码'],['https://proxy.example.test/unexpected-path','invalid_proxy','代理地址无效']]) {
    Object.assign(diagnosticAccount,{effective_proxy_url:proxy,proxy_source:'credential',proxy_error:error});await refresh();
    assert.equal(await page.locator('#account-list .account-network, #account-list .account-proxy-value').count(),0);
    assert((await page.locator('#account-list .account-activity').innerText()).includes(copy));
    assert(await page.locator('#account-list [data-account-action=start]').isDisabled());
    if(error==='proxy_auth_unsupported')await capture('account-inherited-proxy-error-light-1280');
  }
  checks.push('Unsupported inherited CPA proxies show a fixed error in current operation and block login while the removed network column remains absent');
  const userStateAccount={...originalAccounts.find(a=>!a.auth_id),auth_id:'',label:'',health_status:'not_linked',health_reason:'unbound',last_job_id:'user-layout-fixture',last_job_status:'interaction_required',last_job_error_code:'interaction_required',last_job_error_stage:'',last_job_error_reason:'',proxy_url:'direct',effective_proxy_url:'direct',proxy_source:'direct',proxy_error:'',attention_required:true,auto_relogin:false,last_checked_at:'',next_check_at:''};
  assert(userStateAccount.id,'An offline unbound account is available for the user screenshot comparison');
  accounts.splice(0,accounts.length,userStateAccount);await refresh();
  await page.locator('#theme-toggle').selectOption('white');await page.setViewportSize({width:1554,height:605});await settle();
  assert.match(await page.locator('#account-list .account-activity').innerText(),/登录未完成/);
  assert.match(await page.locator('#account-list .account-status').innerText(),/尚未绑定/);
  assert.equal(await page.locator('#account-list [data-label=登录出口]').count(),0);
  assert.match(await page.locator('#account-list').innerText(),/未记录具体原因/);
  assert.equal(await page.locator('#account-list .account-expand').count(),0);
  await capture('user-state-white-1554');
  checks.push('The user screenshot state retains six columns for an offline unbound account and unfinished login, with no login-egress column; its full diagnostic is immediately visible');
  const sparseAccount={...originalAccounts.find(a=>!a.auth_id),label:'',health_status:'not_linked',health_reason:'login_required',auto_relogin:false,attention_required:false,last_job_id:'',last_job_status:'',last_login_at:'',last_checked_at:'',next_check_at:''};
  assert(sparseAccount.id,'A synthetic unbound account is available for the sparse directory regression');
  accounts.splice(0,accounts.length,sparseAccount);
  await page.locator('[data-login-view=tasks]').click();await page.locator('[data-login-view=accounts]').click();await refresh();
  const accountViewports=[{width:1554,height:605},{width:1440,height:900},{width:1280,height:720},{width:390,height:844}];
  for(const [theme,viewport] of ['light','white','dark'].flatMap(theme=>accountViewports.map(viewport=>[theme,viewport]))) {
    await page.locator('#theme-toggle').selectOption(theme);await page.setViewportSize(viewport);await settle();
    assert.equal(await page.locator('#account-list .account-row').count(),1);
    const geometry=await page.evaluate(()=>{
      const panel=document.querySelector('.account-main').getBoundingClientRect(),wrap=document.querySelector('#account-table-wrap').getBoundingClientRect(),table=document.querySelector('.account-table').getBoundingClientRect(),row=document.querySelector('#account-list .account-row').getBoundingClientRect(),footer=document.querySelector('.account-pagination');
      const actions=[...document.querySelectorAll('#account-list .account-row-actions button')].map(button=>{const box=button.getBoundingClientRect();return {top:box.top,left:box.left,right:box.right,width:box.width,height:box.height};});
      const email=document.querySelector('#account-list .account-email'),emailStyle=getComputedStyle(email);
      return {panelHeight:panel.height,wrapHeight:wrap.height,tableHeight:table.height,rowHeight:row.height,rowBottom:row.bottom,panelBottom:panel.bottom,paginationHidden:footer.hidden&&footer.getBoundingClientRect().height===0,actions,email:{width:email.clientWidth,contentWidth:email.scrollWidth,ellipsis:emailStyle.textOverflow==='ellipsis'}};
    });
    accountDensityGeometry.push({state:'single-unbound',theme,viewport,...geometry});
    assert(geometry.wrapHeight<=geometry.tableHeight+3,`A single account does not stretch its table into empty space: ${JSON.stringify(geometry)}`);
    assert(geometry.paginationHidden,`An unfiltered single-page directory omits pagination: ${JSON.stringify(geometry)}`);
    assert(geometry.panelBottom-geometry.rowBottom<=32,`Single-account directory leaves only ordinary inset spacing after its row: ${JSON.stringify(geometry)}`);
    assert.equal(await page.locator('#account-list .account-row td').count(),6,'Account metadata remains visible in six separate columns');
    assert.equal(await page.locator('#account-list .account-row [data-label="到期时间"]').count(),0,'Account expiry remains absent at all viewport sizes');
    for(const selector of ['.account-email','.account-status','.account-activity','.account-row-actions'])assert.equal(await page.locator('#account-list .account-row').locator(selector).count(),1,`Account row preserves its ${selector} content and actions`);
    assert.equal(await page.locator('#account-list .account-email').innerText(),sparseAccount.email);
    assert(!geometry.email.ellipsis&&geometry.email.contentWidth<=geometry.email.width+1,`The full account email fits without truncation: ${JSON.stringify(geometry.email)}`);
    assert.equal(geometry.actions.length,6);
    if(viewport.width>=1051){
      await page.locator('#account-list .account-row-actions').scrollIntoViewIfNeeded();
      await assertViewportAction('#account-list [data-account-action=start]');await assertViewportAction('#account-list [data-account-action=edit]');
      assert(Math.abs(geometry.actions[0].top-geometry.actions[1].top)<=2&&Math.abs(geometry.actions[1].top-geometry.actions[2].top)<=2&&geometry.actions[3].top>geometry.actions[0].top,`Desktop actions use the reference two-row layout: ${JSON.stringify(geometry.actions)}`);
      assert(geometry.actions.every(action=>action.width<160)&&geometry.actions[1].left-geometry.actions[0].right<=16,`Desktop actions remain compact and closely grouped: ${JSON.stringify(geometry.actions)}`);
      assert(geometry.rowHeight<=132,`An ordinary account row stays compact: ${JSON.stringify(geometry)}`);
    }else {
      assert(geometry.actions.every(action=>action.height>=44),`Phone row actions retain usable touch targets: ${JSON.stringify(geometry.actions)}`);
      const toolbarTargets=await page.locator('.account-toolbar > .button-row button').evaluateAll(buttons=>buttons.map(button=>{const box=button.getBoundingClientRect();return {id:button.id,width:box.width,height:box.height};}));
      assert.equal(toolbarTargets.length,3);
      assert(toolbarTargets.every(button=>button.width>=44&&button.height>=44),`Phone proxy, inspection and import controls retain 44px touch targets: ${JSON.stringify(toolbarTargets)}`);
    }
    await capture(`single-account-${theme}-${viewport.width}`);
  }
  checks.push('A six-column account table fits one account without excess blank space in three themes at 1554×605, 1440×900, 1280×720 and 390×844; complete emails, compact two-row desktop actions and phone touch targets stay usable');
  accounts.splice(0,accounts.length,...originalAccounts);
  await page.locator('#theme-toggle').selectOption('light');await page.setViewportSize({width:1280,height:800});await refresh();
  for(let i=0;i<25;i++) {
    const n=i+1,email=`operations.${String(n).padStart(2,'0')}@example.test`;
    const a={...originalAccounts[0],id:`sample-${n}`,revision:1,email,label:n===1?'生产主账号':'',masked_email:'o***@example.test',auth_id:`codex-production-${String(n).padStart(2,'0')}.json`,health_status:n%4===0?'needs_reauth':n%5===0?'disabled':'unknown',health_reason:n%4===0?'requires_sign_in':n%5===0?'manual_disabled':'no_auth_failure_observed',attention_required:n%4===0,auto_relogin:n%2===0,last_job_id:'',last_job_status:n%4===0?'failed':'committed',last_login_at:'2026-11-16T01:25:00Z',last_checked_at:'2026-11-16T02:10:00Z',next_check_at:'2026-11-16T02:25:00Z'};
    accounts.push(a);credentials.set(a.id,{email,password:`fixture-password-${n}`,totp_secret:'JBSWY3DPEHPK3PXP'});
  }
  await refresh();await page.waitForFunction(()=>document.querySelectorAll('#account-list .account-row').length===20);
  const unknownHealth=page.locator('#account-list .account-row').filter({hasText:'operations.03@example.test'}).locator('.account-status .tag');
  assert.equal(await unknownHealth.innerText(),'状态未知');
  assert.equal(await unknownHealth.evaluate(node=>node.classList.contains('good')),false,'A historical committed task must not color unknown CPA health as successful');
  checks.push('Unknown CPA health remains neutral even when the latest historical authorization was committed');
  await page.locator('#account-page-next').click();assert.equal(await page.locator('#account-list .account-row').count(),7);assert(await page.locator('#account-page-next').isDisabled());
  await page.locator('#account-page-prev').click();assert.equal(await page.locator('#account-list .account-row').count(),20);
  await page.locator('#account-search').fill('operations.25@example.test');assert.equal(await page.locator('#account-list .account-row').count(),1);
  await page.locator('#account-search').fill('生产主账号');assert.equal(await page.locator('#account-list .account-row').count(),1);
  await page.locator('#account-search').fill('codex-production-24.json');assert.equal(await page.locator('#account-list .account-row').count(),1);
  await page.locator('[data-account-filter=linked]').click();
  const writesBeforeDirectoryNavigation=calls.filter(c=>c.method!=='GET').length;
  await page.locator('#account-list [data-account-action=edit]').click();await editorReady();
  assert(await page.locator('.account-main').isVisible());assert(await page.locator('#account-editor').isVisible());assert.equal(await page.locator('#account-editor-dialog').evaluate(n=>n.matches(':modal')),true);
  await page.locator('#account-editor-cancel').click();
  assert(await page.locator('.account-main').isVisible());assert(!await page.locator('#account-editor').isVisible());
  assert.equal(await page.locator('#account-password').inputValue(),'');
  assert.equal(await page.locator('#account-search').inputValue(),'codex-production-24.json');
  assert.equal(await page.locator('[data-account-filter=linked]').getAttribute('aria-pressed'),'true');
  await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('unsaved@example.test----unsaved-password----JBSWY3DPEHPK3PXP');
  await page.locator('#account-import-cancel').click();
  assert(await page.locator('.account-main').isVisible());assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert.equal(await page.locator('#account-search').inputValue(),'codex-production-24.json');assert.equal(await page.locator('#account-list .account-row').count(),1);
  assert.equal(calls.filter(c=>c.method!=='GET').length,writesBeforeDirectoryNavigation);
  checks.push('Editor close and import Cancel preserve the account query and filter, clear secret drafts, and make no writes');
  await page.locator('[data-account-filter=all]').click();
  await page.locator('#account-search').fill('no-such-account');assert(await page.locator('#account-no-results').isVisible());
  await page.locator('#account-clear-filter').click();assert.equal(await page.locator('#account-search').inputValue(),'');assert.equal(await page.locator('#account-list .account-row').count(),20);
  await page.locator('[data-account-filter=attention]').click();assert((await page.locator('#account-list .account-row').count())>0);assert((await page.locator('#account-list').innerText()).includes('需要重新授权'));assert(!(await page.locator('#account-list').innerText()).includes('second@example.test'));
  await page.locator('[data-account-filter=linked]').click();assert(!(await page.locator('#account-list').innerText()).includes('second@example.test'));
  await page.locator('[data-account-filter=all]').click();
  checks.push('Full email, label and CPA identifier search; attention/linked filters; bounded 20-row pagination and empty-search reset');
  assert.equal((await page.locator('#account-filter-all-count').innerText()).trim(),'27');
  assert.equal((await page.locator('#account-filter-linked-count').innerText()).trim(),'26');
  const attentionCount=Number((await page.locator('#account-filter-attention-count').innerText()).trim());
  await page.locator('[data-account-filter=attention]').click();assert.equal(await page.locator('#account-list .account-row').count(),attentionCount);
  await page.locator('[data-account-filter=all]').click();
  await page.setViewportSize({width:1280,height:720});
  await settle();
  const denseGeometry=await page.evaluate(()=>{const wrap=document.querySelector('#account-table-wrap'),table=document.querySelector('.account-table'),footer=document.querySelector('.account-pagination').getBoundingClientRect();return {wrapHeight:wrap.clientHeight,contentHeight:table.getBoundingClientRect().height,footerBottom:footer.bottom,viewportHeight:innerHeight};});
  accountDensityGeometry.push({state:'twenty-visible-accounts',theme:'light',viewport:{width:1280,height:720},...denseGeometry});
  assert(denseGeometry.contentHeight>denseGeometry.wrapHeight&&denseGeometry.footerBottom<=denseGeometry.viewportHeight,`Many accounts retain bounded local scrolling with visible pagination: ${JSON.stringify(denseGeometry)}`);
  for(const theme of ['light','white','dark']) {
    await page.locator('#theme-toggle').selectOption(theme);await page.setViewportSize({width:1554,height:605});await settle();
    const geometry=await page.evaluate(()=>{const wrap=document.querySelector('#account-table-wrap'),table=document.querySelector('.account-table'),footer=document.querySelector('.account-pagination').getBoundingClientRect(),rows=document.querySelectorAll('#account-list .account-row');return {wrapHeight:wrap.clientHeight,wrapBottom:wrap.getBoundingClientRect().bottom,contentHeight:table.getBoundingClientRect().height,firstRowBottom:rows[0].getBoundingClientRect().bottom,secondActionsBottom:rows[1].querySelector('.account-row-actions').getBoundingClientRect().bottom,footerBottom:footer.bottom,viewportHeight:innerHeight};});
    accountDensityGeometry.push({state:'twenty-visible-accounts',theme,viewport:{width:1554,height:605},...geometry});
    assert(geometry.wrapHeight>=80&&geometry.contentHeight>geometry.wrapHeight&&geometry.footerBottom<=geometry.viewportHeight,`The reference viewport retains a useful local list with reachable pagination: ${JSON.stringify(geometry)}`);
    assert(geometry.firstRowBottom<=geometry.wrapBottom&&geometry.secondActionsBottom<=geometry.wrapBottom,`A short desktop shows the complete first account and reachable second-row actions: ${JSON.stringify(geometry)}`);
    await capture(`accounts-paginated-${theme}-1554`);
  }
  await page.locator('#theme-toggle').selectOption('light');await page.setViewportSize({width:1280,height:720});await settle();
  checks.push('Dense account lists remain locally scrollable and keep pagination inside the desktop viewport');
  const previousListScroll=await page.locator('#account-table-wrap').evaluate(n=>{n.scrollTop=220;return n.scrollTop;});
  assert(previousListScroll>0);
  await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('one@example.test----password----JBSWY3DPEHPK3PXP\n\nsecond@example.test----password----JBSWY3DPEHPK3PXP\n');
  assert.match(await page.locator('#account-import-count').innerText(),/2/);
  await page.locator('#account-import-close').click();
  assert.equal(await page.locator('#account-table-wrap').evaluate(n=>n.scrollTop),previousListScroll);
  assert.equal(await page.locator('#account-import-text').inputValue(),'');
  assert(!await page.locator('#account-import-error').isVisible());
  await page.locator('#account-import-open').click();
  assert.match(await page.locator('#account-import-count').innerText(),/0/);
  await page.locator('[data-login-view=tasks]').evaluate(n=>n.click());
  assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open),false);
  await page.locator('[data-login-view=accounts]').click();
  checks.push('Filters show real counts; import counts nonempty lines, preserves directory scrolling, resets on reopen, and closes on programmatic local navigation');
  const denseAccounts=accounts.slice(0,8).map(a=>({...a}));
  denseAccounts[2].email='very.long.account.name.for.workspace@example.test';credentials.get(denseAccounts[2].id).email=denseAccounts[2].email;
  const baseJob={...jobs[0]};
  for(let i=0;i<5;i++)jobs.push({...baseJob,id:`history-${i}`,email:denseAccounts[i+2].email,account_id:denseAccounts[i+2].id,status:['failed','running','committed','interaction_required','committed'][i],phase:'',error_code:i===0?'credentials_rejected':i===3?'interaction_required':'',automatic:i%2===0,created_at:`2026-11-16T02:0${i}:00Z`});
  for(let i=0;i<5;i++){const j=jobs.find(j=>j.id===`history-${i}`);denseAccounts[i+2].last_job_id=j.id;denseAccounts[i+2].last_job_status=j.status;}
  accounts.splice(0,accounts.length,...denseAccounts);
  await page.setViewportSize({width:1280,height:720});await refresh();
  assert.equal((await page.locator('#account-active-count').innerText()).trim(),'1','Only the one running task is counted as an active login');
  const writesBeforeOverviewNavigation=calls.filter(c=>c.method!=='GET').length;
  await page.locator('#account-active-jobs-open').click();assert(await page.locator('#login-view-tasks').isVisible());
  await page.locator('[data-login-view=accounts]').click();
  assert.equal(calls.filter(c=>c.method!=='GET').length,writesBeforeOverviewNavigation);
  checks.push('The four summary controls expose all/attention/linked counts and current login activity; the activity entry opens Tasks without making writes');
  await row().locator('.account-row-actions [data-account-action=task]').click();await page.locator('#login-job-detail h3').filter({hasText:'已保存'}).waitFor();
  await assertSelectedJobVisible(jobs[0].id,'opening an older task from its account');
  assert(await page.locator('#login-jobs').evaluate(list=>list.scrollHeight>list.clientHeight&&list.scrollTop>0),'Older account task requires the task list to scroll');
  await page.locator('#login-jobs').evaluate(list=>list.scrollTop=0);
  const changingJob=jobs.find(job=>job.id==='history-1');changingJob.status='committed';changingJob.revision++;
  await page.locator('[data-login-job=history-1] .tag').filter({hasText:'已保存'}).waitFor();await settle();
  assert.equal(await page.locator('#login-jobs').evaluate(list=>list.scrollTop),0,'Polling preserves the position after the operator scrolls away from the selected task');
  changingJob.status='running';changingJob.revision++;await refresh();await settle();
  assert.equal(await page.locator('#login-jobs').evaluate(list=>list.scrollTop),0,'Explicit refresh preserves the manually chosen task-list position');
  await page.locator('[data-login-view=accounts]').click();await page.locator('[data-login-view=tasks]').click();
  await assertSelectedJobVisible(jobs[0].id,'reopening the task view');
  checks.push('Opening an older task from its account or reopening Tasks reveals the selected row; polling and refresh preserve manual list scrolling');
  let releaseJobRead,jobReadReached;
  const jobReadReceived=new Promise(resolve=>jobReadReached=resolve);
  nextJobRead={gate:new Promise(resolve=>releaseJobRead=resolve),received:jobReadReached};heldReads.add(releaseJobRead);
  const heldJobResponse=page.waitForResponse(r=>new URL(r.url()).pathname.endsWith('/model-quality/login/detail')&&new URL(r.url()).searchParams.get('id')==='history-0');
  await page.locator('[data-login-job=history-0]').click();await jobReadReceived;
  assert.equal(await page.locator('#login-job-detail [data-login-action]').count(),0);assert(!(await page.locator('#login-job-detail').innerText()).includes('fixture@example.test'));
  await page.locator('[data-login-job=history-3]').click();await page.locator('#login-job-detail h3').filter({hasText:'登录未完成'}).waitFor();
  releaseJobRead();heldReads.delete(releaseJobRead);await (await heldJobResponse).finished();await settle();
  assert((await page.locator('#login-job-detail').innerText()).includes(jobs.find(j=>j.id==='history-3').email));assert((await page.locator('#login-job-detail').innerText()).includes('登录未完成'));
  checks.push('Task selection clears old actions immediately; delayed detail cannot overwrite a newer selected task');
  for(const theme of ['light','white','dark']) {
    await page.locator('#theme-toggle').selectOption(theme);
    for(const viewport of [{width:1280,height:720},{width:390,height:844}]) {
      await page.setViewportSize(viewport);
      accounts.splice(0,accounts.length,...denseAccounts);await page.locator('[data-login-view=accounts]').click();await refresh();
      await capture(`accounts-${theme}-${viewport.width}`);
      await page.locator('[data-login-view=settings]').click();await page.locator('#account-inspection-enabled').waitFor();assert(await page.locator('#account-settings').evaluate(n=>n.open));await capture(`settings-${theme}-${viewport.width}`);if(viewport.width===1280)await assertViewportAction('#account-settings-save');
      await page.locator('[data-login-view=accounts]').click();
      await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('new.account@example.test----example-password----JBSWY3DPEHPK3PXP');
      await capture(`import-${theme}-${viewport.width}`);if(viewport.width===1280)await assertViewportAction('#account-import-submit');await page.locator('#account-import-close').click();
      await row().locator('[data-account-action=edit]').click();await editorReady();await capture(`editor-${theme}-${viewport.width}`);if(viewport.width===1280){for(const selector of ['#account-email','#account-password','#account-totp','#account-editor-save'])await assertViewportAction(selector);}await page.locator('#account-editor-close').click();
      await page.locator('[data-login-view=tasks]').click();await page.locator('[data-login-job=history-0]').click();await page.locator('#login-job-detail').filter({hasText:'登录失败'}).waitFor();await assertSelectedJobVisible('history-0',`task ${theme} ${viewport.width}`);await capture(`tasks-${theme}-${viewport.width}`);
      jobs[0].status='awaiting_commit';jobs[0].revision++;
      await page.locator('#login-refresh').click();await page.locator(`[data-login-job="${jobs[0].id}"]`).click();await page.locator('[data-login-action=commit]').waitFor();await assertSelectedJobVisible(jobs[0].id,`candidate ${theme} ${viewport.width}`);await capture(`candidate-${theme}-${viewport.width}`);await assertSelectedJobVisible(jobs[0].id,`captured candidate ${theme} ${viewport.width}`);if(viewport.width===1280)await assertViewportAction('[data-login-action=commit]');
      jobs[0].status='committed';jobs[0].revision++;
      accounts.splice(0,accounts.length);await page.locator('[data-login-view=accounts]').click();await refresh();await page.locator('#account-empty').waitFor();await capture(`empty-${theme}-${viewport.width}`);
    }
    for(const viewport of [{width:1554,height:605},{width:1440,height:900}]) {
      await page.setViewportSize(viewport);accounts.splice(0,accounts.length,...denseAccounts);await refresh();
      await capture(`accounts-${theme}-${viewport.width}`);
      await assertViewportAction('#account-import-open');await page.locator('#account-list .account-row:first-child [data-account-action=start]').scrollIntoViewIfNeeded();await assertViewportAction('#account-list .account-row:first-child [data-account-action=start]');
      accounts.splice(0,accounts.length);await refresh();await page.locator('#account-empty').waitFor();
      await capture(`empty-${theme}-${viewport.width}`);await assertViewportAction('#account-import-empty');
    }
  }
  accounts.splice(0,accounts.length,...denseAccounts);await refresh();await page.locator('#theme-toggle').selectOption('light');
  for(const viewport of [{width:1440,height:900},{width:360,height:780},{width:768,height:1024}]) {await page.setViewportSize(viewport);if(viewport.width!==1440)await capture(`accounts-light-${viewport.width}`);await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('new.account@example.test----example-password----JBSWY3DPEHPK3PXP');await capture(`import-light-${viewport.width}`);await assertViewportAction('#account-import-submit');await page.locator('#account-import-cancel').click();await row().locator('[data-account-action=edit]').click();await editorReady();await capture(`editor-light-${viewport.width}`);await assertViewportAction('#account-editor-save');await page.locator('#account-editor-cancel').click();}
  checks.push('Desktop login/save/confirm actions and complete account fields stay visible; all three views and import/editor/candidate/empty states fit three themes and desktop/phone sizes, including the 1554×605 reference viewport and 360/768/1440px widths');
  accounts.splice(0,accounts.length,...originalAccounts);jobs.splice(1);await refresh();
  await page.setViewportSize({width:1280,height:800});
  const deleteBoundary=calls.length;
  await page.locator('#account-list .account-row').filter({hasText:'second@example.test'}).locator('[data-account-action=delete]').click();
  await page.locator('#account-delete-confirm').waitFor();assert(await page.locator('#account-editor-dialog').isHidden());assert.equal(accounts.length,2);
  await page.locator('#account-delete-yes').click();await page.waitForFunction(()=>document.querySelectorAll('#account-list .account-row').length===1);
  assert(!calls.slice(deleteBoundary).some(c=>c.path.endsWith('/accounts/detail')));
  assert.equal(accounts[0].auth_id,target.auth_id);checks.push('Deleting a saved account requires confirmation and leaves the CPA credential intact');
  await page.locator('#account-import-open').click();await page.locator('#account-import-text').fill('clear-on-401@example.test----unsaved-password----JBSWY3DPEHPK3PXP');
  assert.notEqual(await page.locator('#account-import-text').inputValue(),'');
  unauthorized=true;await page.locator('#refresh').evaluate(n=>n.click());await page.locator('#connection-open').waitFor();assert.equal(await page.locator('[data-login-secret]').evaluateAll(nodes=>nodes.some(n=>n.value)),false);
  assert.equal(await page.locator('#account-import-dialog').evaluate(n=>n.open),false);
  assert.equal(await page.locator('#account-list .account-row').count(),0);
  await page.locator('#login-connect').click();await page.locator('#management-key').fill('offline-key');await page.locator('#connection-form button[type=submit]').click();await page.locator('#account-import-open:enabled').waitFor();
  const unauthorizedHeld=holdDetail();
  const unauthorizedResponse=accountCompletion(accounts[0].id);
  await row().locator('[data-account-action=edit]').click();await unauthorizedHeld.received;
  unauthorized=true;await page.locator('#login-refresh').evaluate(n=>n.click());await page.locator('#connection-open').waitFor();
  unauthorizedHeld.release();await unauthorizedResponse;await settle();
  assert.equal(await page.locator('[data-login-secret]').evaluateAll(nodes=>nodes.some(n=>n.value)),false);assert.equal(await page.locator('#account-list .account-row').count(),0);
  checks.push('A delayed or cancelled detail response after 401 cannot restore saved plaintext or account identity');
  assert.equal(await page.evaluate(()=>localStorage.length+sessionStorage.length),0);assert.deepEqual(errors,[]);
  assert(!calls.some(c=>/\/runs|quality-pauses\/release/.test(c.path)&&c.method!=='GET'));checks.push('401 clears account data/secrets; no browser storage or model/pause-release writes');
  assert(!calls.some(c=>c.path.endsWith('/vault/initialize')));
  assert.deepEqual(sourceHashes(),source,'UI source must stay unchanged while verification and captures run');
  fs.writeFileSync(path.join(output,'report.json'),JSON.stringify({result:'passed',fixture_scope:'Loopback-only synthetic accounts; no real credentials or remote model calls',checks,screenshots,account_density_geometry:accountDensityGeometry,source_sha256:source},null,2)+'\n');console.log(`Account UI: ${checks.length} checks passed with offline fixtures.`);
 } finally {for(const release of heldReads)release();if(releaseAccountWrite)releaseAccountWrite();if(releaseImportWrite)releaseImportWrite();if(releaseStartWrite)releaseStartWrite();if(releaseSecurityWrite)releaseSecurityWrite();await browser.close();await new Promise(resolve=>server.close(resolve));}
})().catch(e=>{console.error(e.stack);process.exitCode=1;});
