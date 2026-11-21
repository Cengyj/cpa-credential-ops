// Writes are fulfilled in memory. Neither CPA nor an upstream model receives requests.
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const fs = require('node:fs');
const path = require('node:path');
const checkRuleLayout = require('./rule_editor_layout.cjs');
const checkWorkspace = require('./quality_workspace_layout.cjs');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const child = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore','pipe','inherit'] });
  const exit = once(child,'exit'); let browser;
  try {
    const [line] = await Promise.race([once(createInterface({input:child.stdout}),'line'),exit.then(() => { throw Error('Missing fixture'); })]);
    const info = JSON.parse(line), origin = new URL(info.url).origin, prefix = '/v0/management/plugins/model-quality';
    assert.equal(info.pid,child.pid); assert.notEqual(new URL(origin).port,'8317');
    browser = await chromium.launch({executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
    const context = await browser.newContext({viewport:{width:1440,height:900},timezoneId:'America/Los_Angeles',reducedMotion:'reduce'});
    const directory = Array.from({length:123},(_,i) => ({auth_id:`fixture-${i}`,auth_index:`index-${i}`,provider:i%2?'codex':'offline',object_type:'oauth',models:[i===122?'new-only':'shared'],runtime_status:'active',effective_available:true}));
    const rules = [], children = [], writes = [], unexpected = [], errors = [], readOffsets = new Set();
    directory[122].email = 'scope@example.test';
    let uncertain = false, failActivity = false, previewReads = 0, dispatchPreviews = 0, detailGate, failDetail = false;
    const reads = [];
    await context.route('**/*', async route => {
      const req=route.request(), u=new URL(req.url()), p=u.pathname.slice(prefix.length);
      if(u.origin!==origin){unexpected.push('external');return route.abort();}
      const range = rows => {const offset=Number(u.searchParams.get('offset')||0),limit=Number(u.searchParams.get('limit')||50); return route.fulfill({json:{items:rows.slice(offset,offset+limit),total:rows.length,offset,limit}});};
      if(req.method()==='GET') {
        reads.push(p);
        if (p === '/runs/detail') {
          const run = children.find(child => child.id === u.searchParams.get('id'));
          if (detailGate && run?.id === 'run-0') await detailGate;
          if (failDetail) return route.fulfill({status:503,json:{error:{message:'synthetic detail failure'}}});
          if (run) return route.fulfill({json: run});
        }
        if(p==='/rules/question-preview') previewReads++;
        if(p==='/targets'){readOffsets.add(Number(u.searchParams.get('offset')));return range(directory);}
        if(p==='/rules') return range(rules);
        if(p==='/settings') return route.fulfill({json:{scheduling_enabled:false,retention_days:30,max_models:8,max_samples:32,capabilities:{mode:'official_observation',credential_management:true,management_session:true}}});
        if(p==='/runs' && u.searchParams.get('source')==='scheduled_probe') { if(failActivity) return route.fulfill({status:503,json:{error:{message:'synthetic read failure'}}}); return range(children.filter(r=>!u.searchParams.has('scope_batch_id')||u.searchParams.get('scope_batch_id')===r.config.scope_batch_id).filter(r=>!u.searchParams.has('rule_id')||u.searchParams.get('rule_id')===r.config.rule_id)); }
        return route.continue();
      }
      const b=req.postDataJSON();
      if(p==='/rules/scope-preview' && req.method()==='POST') {
        dispatchPreviews++;
        const oauth=b.target_scope==='single'?directory.filter(t=>t.auth_id===b.auth_id).length:directory.length;
        return route.fulfill({json:{verified:true,candidates:{oauth,provider:0},count:oauth,skipped:0,recovery_count:0,skip_details:[]}});
      }
      writes.push({p,method:req.method(),b});
      if(p==='/rules/run-history' && req.method()==='DELETE') {
        const deleted=children.length; children.length=0;
        return route.fulfill({json:{deleted,kept:0}});
      }
      if(p==='/rules/timer' && req.method()==='PUT') {
        const r=rules.find(row=>row.id===b.id);
        assert.deepEqual(Object.keys(b).sort(),['enabled','id','revision']);
        assert.equal(r.revision,b.revision);
        Object.assign(r,{timer_active:b.enabled,revision:r.revision+1});
        return route.fulfill({json:r});
      }
      if(p==='/rules' && req.method()==='POST') {const r={...b,id:`rule-${rules.length+1}`,revision:1,status:'detecting',enabled:true,timer_active:b.activation==='start'};rules.push(r);return route.fulfill({json:r});}
      if(p==='/rules' && req.method()==='PUT') {const old=rules.find(r=>r.id===b.id);assert.equal(old.revision,b.revision);if(b.cron==='65 * * * *')return route.fulfill({status:400,json:{error:{code:'invalid_cron'}}});if(b.cron){delete old.interval_seconds;delete old.timezone;}if(b.activation!=='keep')old.timer_active=b.activation==='start';Object.assign(old,b,{revision:b.revision+1});return route.fulfill({json:old});}
      if(p==='/rules/run' && req.method()==='POST') {
        if(uncertain) return route.abort('connectionfailed');
        assert.deepEqual(b,{id:'rule-1',revision:1});
        for(let i=0;i<23;i++) children.push({id:`run-${i}`,source:'scheduled_probe',status:'completed',created_at:'2026-11-18T00:00:00Z',config:{rule_id:b.id,auth_id:`result-${i}`,models:['shared'],parameters:{reasoning_effort:'low'},scope_batch_id:'test-batch'}});
        children[0].credential_email = 'snapshot@example.test';
        Object.assign(children[0].config, {samples:1, question:{id:'logic-candy',kind:'logic', answer:'21'}, judge:{mode:'deterministic'}});
        Object.assign(children[0], {action_status:'observe_only', samples:[{model:'shared', index:0, execution_status:'completed', quality_verdict:'correct', comparable:false, output:'21', reason:'exact answer', evidence:{path:'cpa_management_probe',reason:'management_probe_not_business_payload'}}]});
        children[1].config.auth_id = directory[122].auth_id;
        children[1].samples = [{model:'shared', index:0, execution_status:'completed', quality_verdict:'incorrect', output:'22'}];
        const correctSample = children[0].samples[0];
        children[2].config.samples = 2;
        children[2].samples = [correctSample, { ...correctSample }];
        children[3].config.samples = 1;
        children[3].samples = [{ ...correctSample, error: 'fixture request failed', quality_verdict: 'incorrect' }];
        rules[0].last_scope_batch='test-batch';return route.fulfill({status:202,json:{count:23,skipped:100,scope_batch_id:'test-batch'}});
      }
      unexpected.push(p);return route.abort();
    });
    const page=await context.newPage();page.setDefaultTimeout(7000);page.on('pageerror',e=>errors.push(e.message));
    const el=s=>page.locator(s), click=s=>el(s).click(), text=(s,t)=>el(s).filter({hasText:t}).first().waitFor();
    await page.goto(info.url);await click('#login-connect');await el('#management-key').fill(info.management_key);await click('#connection-form button[type=submit]');await el('body.connected').waitFor();
    await click('[data-page=operations]');
    const workspaceChecks = [{ state: 'empty', ...await checkWorkspace(el('.quality-workspace')) }];
    await click('#rule-new');
    assert.equal(await el('[data-ops-view],#page-operations [role=tab]').count(),0);
    assert(await el('#operations-main').isVisible());
    const ui_version=JSON.parse(await el('#build-info').textContent()).ui_version;
    const out=path.resolve(process.env.UI_OUTPUT||path.join(__dirname,`../artifacts/quality-scope-ui-${ui_version}`));fs.mkdirSync(out,{recursive:true});
    const focused = id => page.waitForFunction(id => document.activeElement?.id === id,id);
    const cleanDraft = async () => { assert.equal(await el('#rule-form .editor-dirty').getAttribute('data-changed'),'false'); assert.equal(await el('#rule-form .editor-dirty').textContent(),''); };
    const configOnly = async () => {
      assert.equal(await el('#rule-estimate,#rule-sample-summary,#rule-cron-unit,#rule-cron-hint').count(),0);
      assert.doesNotMatch(await el('#rule-form').innerText(), /预计|暂不可估算|次主测|次探测|探测次数|每凭证最多/);
      assert.equal(await el('#rule-form').getByLabel('凭证检测周期（五段 Cron，服务端时区）',{exact:true}).inputValue(),defaultCron);
    };
    assert(readOffsets.has(100));assert(await el('#rule-scope-all').isChecked());assert(await el('#rule-target-field').isHidden());
    assert.equal(await el('#rule-max-tokens,#settings-open,#settings-dialog').count(),0);
    const defaultCron='*/30 * * * *';
    const efforts=['low','medium','high','xhigh','max'];
    const pick = level => el(`.effort-scale input[value="${level}"]`);
    const radios = el('#rule-reasoning').getByRole('radio');
    assert.equal(await el('#rule-interval,#rule-timezone,#rule-temperature,#rule-retention').count(),0);
    assert.deepEqual(await radios.evaluateAll(nodes=>nodes.map(n=>n.value)),efforts);
    assert(await el('#rule-target').isDisabled());assert(!(await el('#rule-target').evaluate(e=>e.required)));
    assert.equal(await el('#rule-form [role=tab],#rule-form [role=tabpanel],#rule-schedule-toggle,[data-rule-pause]').count(),0);
    assert.equal(await el('#rule-form input[type=checkbox]:visible').count(),3);
    assert.equal(await el('#rule-form').getByLabel(/^规则名称/).count(),1);
    await checkRuleLayout(el('#rule-dialog'));
    assert(await el('#rule-cron').isVisible());assert.equal(await el('#rule-cron').inputValue(),defaultCron);
    assert.equal(await el('#rule-max-results').inputValue(),'100');assert(!(await el('#rule-timer').isChecked()));
    await cleanDraft();await configOnly();
    assert.equal(await el('.rule-mode').count(),0);
    const handling = value => el(`[name="rule-action"][value="${value}"]`);
    assert(await handling('observe_only').isChecked());assert(await el('#rule-restore-option').isHidden());
    for (const width of [1440,390,320]) {
      await page.setViewportSize({width,height:900});
      for (const theme of ['light','white','dark']) {
        await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
        await handling('disable_credential').check();await el('#rule-auto-restore').check();
        assert(await el('#rule-restore-option').isVisible());await checkRuleLayout(el('#rule-dialog'));
        await el('.rule-handling').screenshot({path:path.join(out,`handling-${width}-${theme}.png`)});
        const controls=await el('.handling-options label').evaluateAll(nodes=>nodes.map(n=>{const r=n.getBoundingClientRect();return {x:r.x,y:r.y,right:r.right,bottom:r.bottom,font:getComputedStyle(n.querySelector('span')).fontSize}}));
        assert(controls.every((r,i)=>r.font==='12px' && (i===0 || r.x>=controls[i-1].right-1 || r.y>=controls[i-1].bottom-1)));
        await handling('delete_credential').check();assert(await el('#rule-restore-option').isHidden());await handling('observe_only').check();
      }
    }
    await handling('disable_credential').check();await el('#rule-auto-restore').uncheck();await handling('observe_only').check();await page.setViewportSize({width:1440,height:900});await page.evaluate(()=>document.documentElement.dataset.theme='light');
    await cleanDraft();assert.equal(writes.length,0);
    const selected = () => el('#rule-model-selection code').allTextContents();
    const quick = id => el(`[data-add-model="${id}"]`);
    const defaults = ['gpt-6.2-sol','gpt-6-astra','gpt-6.1-sol'];
    assert.deepEqual(await selected(),[defaults[0]]);assert.deepEqual(await el('#rule-model-shortcuts code').allTextContents(),defaults);
    // Capture the initial composer and its dense/empty states without changing saved rules.
    for (const width of [1440,390,320]) {
      await page.setViewportSize({width,height:1000});
      for (const theme of ['light','white','dark']) {
        await page.evaluate(t=>document.documentElement.dataset.theme=t,theme);
        await checkRuleLayout(el('#rule-dialog'));
        await checkRuleLayout.captureModelCard(page,el('.rule-instrument'),path.join(out,`model-card-default-${width}-${theme}.png`));
      }
      await page.evaluate(()=>document.documentElement.dataset.theme='light');
      await el('#rule-models').fill('model-b model-c model-d model-e model-f model-g model-h');
      await el('#rule-model-add').click();
      assert.equal((await selected()).length,8);await checkRuleLayout(el('#rule-dialog'));
      assert(await quick(defaults[1]).isDisabled());
      await checkRuleLayout.captureModelCard(page,el('.rule-instrument'),path.join(out,`model-card-full-${width}.png`));
      await el('#rule-models').fill('over-limit');await el('#rule-model-add').click();
      await text('#rule-models-error','1–8');assert.equal((await selected()).length,8);await checkRuleLayout(el('#rule-dialog'));
      await checkRuleLayout.captureModelCard(page,el('.rule-instrument'),path.join(out,`model-card-limit-${width}.png`));
      await el('#rule-models').fill('');
      for(const id of await selected()) await click(`[data-remove-model="${id}"]`);
      await checkRuleLayout(el('#rule-dialog'));
      await checkRuleLayout.captureModelCard(page,el('.rule-instrument'),path.join(out,`model-card-empty-${width}.png`));
      await quick(defaults[0]).click();
      await click('#rule-shortcut-toggle');await el('#rule-shortcut-draft').fill('preview-model');
      await checkRuleLayout(el('#rule-dialog'));
      await checkRuleLayout.captureModelCard(page,el('.rule-instrument'),path.join(out,`model-card-shortcuts-${width}.png`));
      await el('#rule-shortcut-draft').fill('');await click('#rule-shortcut-toggle');await cleanDraft();
    }
    await page.setViewportSize({width:1440,height:900});
    assert(await pick('high').isChecked());
    for(const level of efforts) assert(await pick(level).locator('+ span').isVisible());
    await pick('high').focus();await page.keyboard.press('ArrowRight');assert(await pick('xhigh').isChecked());await text('.editor-dirty','有未保存更改');
    await page.keyboard.press('ArrowRight');assert(await pick('max').isChecked());
    await page.keyboard.press('ArrowLeft');assert(await pick('xhigh').isChecked());
    await page.keyboard.press('ArrowLeft');assert(await pick('high').isChecked());await cleanDraft();
    assert(await el('[data-sample-step="-1"]').isDisabled());await click('[data-sample-step="1"]');assert.equal(await el('#rule-samples').inputValue(),'2');await click('[data-sample-step="-1"]');await cleanDraft();
    assert.equal(previewReads,0);assert(await el('#rule-preview').isHidden());await el('#rule-preview-toggle').press('Space');await text('#rule-preview-answer','合成参考答案');assert.equal(previewReads,1);
    await el('#rule-question').selectOption('random-linear');await text('#rule-preview-answer','本地求解器');assert.equal(previewReads,1);
    await el('#rule-question').selectOption('custom');assert(await el('#rule-preview-toggle').isHidden());assert(await el('#rule-preview').isHidden());assert(await el('#rule-prompt-field').isVisible());assert.equal(await el('#rule-judge-mode').inputValue(),'manual');
    await el('#rule-name').fill('校验草稿');await click('#rule-form button[type=submit]');await focused('rule-prompt');assert.equal(writes.length,0);
    await el('#rule-prompt').fill('自定义草稿');await el('#rule-review-standard').fill('评审草稿');assert.equal(await el('#rule-prompt').inputValue(),'自定义草稿');assert.equal(await el('#rule-review-standard').inputValue(),'评审草稿');
    await el('#rule-prompt').fill('');await el('#rule-review-standard').fill('');await el('#rule-name').fill('');
    await el('#rule-question').selectOption('logic-candy');await el('#rule-judge-mode').selectOption('deterministic');await cleanDraft();
        await el('#rule-name').fill('指向字段的校验');await el('#rule-scope-single').check();await click('#rule-form button[type=submit]');await focused('rule-target');assert(await el('#rule-model-title').isVisible());assert.equal(writes.length,0);
    await el('#rule-scope-all').check();await el('#rule-name').fill('');
    assert(await quick(defaults[0]).isDisabled());await text('#rule-scope-note','OAuth 账户 123 个');
    await el('#rule-scope-all').focus();await page.keyboard.press('ArrowRight');assert(await el('#rule-scope-directory').isChecked());assert(await el('#rule-target-field').isHidden());await text('#rule-scope-note','OAuth 账户 123 个');await page.keyboard.press('ArrowRight');assert(await el('#rule-scope-single').isChecked());await el('#rule-scope-all').check();
    await click('#rule-shortcut-toggle');await el('#rule-shortcut-draft').fill('local-one，local-two\nlocal-one；gpt-6-astra');await click('#rule-shortcut-save');
    assert.deepEqual(await el('#rule-custom-shortcuts code').allTextContents(),['local-one','local-two']);await cleanDraft();
    await click('#rule-shortcut-toggle');await quick(defaults[1]).press('Enter');await quick(defaults[2]).click();await quick('local-two').click();
    assert.deepEqual(await selected(),[...defaults,'local-two']);
    await click('#rule-shortcut-toggle');await click('[data-remove-shortcut="local-two"]');assert.deepEqual(await selected(),[...defaults,'local-two']);assert.equal(await quick('local-two').count(),0);await click('#rule-shortcut-toggle');
    for(const model of [...defaults,'local-two']) await click(`[data-remove-model="${model}"]`);
    assert(await el('.deck-empty').isVisible()); await focused('rule-models');
    await el('#rule-model-catalog').selectOption('new-only');assert.doesNotMatch(await el('#rule-scope-note').innerText(),/暂无匹配/);
    await el('#rule-models').fill('shared\nnew-only，shared;shared');await el('#rule-models').press('Enter');assert.deepEqual(await selected(),['new-only','shared']);
    await el('#rule-name').fill('日常观察');await el('#rule-samples').fill('2');await pick('max').check();await configOnly();await text('#rule-model-count','已选 2');
    for (const select of await el('#rule-upstream-options select').all()) await select.selectOption('observe_only');
    await el('#rule-timer').check();await text('#rule-schedule-note','仅运行本条规则');await el('#rule-timer').uncheck();
    await el('#rule-cron').fill('');assert(await el('#rule-cron').evaluate(n=>n.validity.valueMissing));await click('#rule-form button[type=submit]');await focused('rule-cron');assert(await el('#rule-plan-title').isVisible());assert.equal(writes.length,0);
    await el('#rule-cron').fill('* * * *');await click('#rule-form button[type=submit]');await text('#rule-cron-error','五段 Cron');assert.equal(writes.length,0);await el('#rule-cron').fill(defaultCron);
    await el('#rule-max-results').fill('201');await click('#rule-form button[type=submit]');assert.equal(writes.length,0);await el('#rule-max-results').fill('100');
    // Long edits and intermediate iframe widths must not push controls off-screen.
    for (const width of [1140,810,670,633,519,320]) {
      await page.setViewportSize({width,height:800});
      await el('#rule-name').fill('周'.repeat(100));
      await el('#rule-models').fill('a'.repeat(246));await el('#rule-models').press('Enter');
      await checkRuleLayout(el('#rule-dialog'));
      assert(await el('#rule-dialog').evaluate(el=>el.scrollWidth<=el.clientWidth+1 && document.documentElement.scrollWidth<=innerWidth));
      await click('[data-remove-model="'+'a'.repeat(246)+'"]');await el('#rule-name').fill('日常观察');
    }
    const widths=[1440,390,320], layouts=[];for(const width of widths){
      await page.setViewportSize({width,height:width<=390?844:900});
      for(const theme of ['light','white','dark']) {
        await page.evaluate(theme=>document.documentElement.dataset.theme=theme,theme);
        assert(await el('#rule-dialog').evaluate(n=>n.scrollWidth<=n.clientWidth+1 && document.documentElement.scrollWidth<=innerWidth));
        layouts.push({width,theme,...await checkRuleLayout(el('#rule-dialog'))});
        await el('.rule-form-scroll').evaluate(n=>n.scrollTop=0);
        await el('#rule-dialog').screenshot({path:path.join(out,`rule-sheet-${width}-${theme}.png`)});
      }
      for (const level of efforts) { await pick(level).check();assert(await pick(level).isChecked());assert(await pick(level).locator('+ span').evaluate(n=>n.scrollWidth<=n.clientWidth+1)); }
    }
    await click('#rule-form button[type=submit]'); await el('[data-rule-run="rule-1"]').waitFor();assert.equal(writes.length,1);
    assert.equal(rules[0].target_scope,'all_enabled');assert(!rules[0].auth_id && !rules[0].provider);assert.equal(rules[0].parameters.max_tokens,undefined);assert.deepEqual(rules[0].models,['new-only','shared']);assert.equal(rules[0].parameters.reasoning_effort,'max');
    assert.equal(await el('#rules-list summary').count(),0);await click('[data-rule-edit="rule-1"]');assert(await pick('max').isChecked());assert(await el('#rule-legacy-effort').isHidden());await click('#rule-form .editor-footer [data-close]');
    assert.equal(rules[0].cron,defaultCron);assert.equal(rules[0].max_results,100);assert.equal(rules[0].timer_active,false);assert(!['interval_seconds','timezone','retention_days','auth_id','provider'].some(k=>k in writes[0].b));assert(!('temperature' in writes[0].b.parameters));
    await click('#rule-new');await el('#rule-scope-single').check();await el('#rule-target').selectOption('fixture-122');await text('#rule-scope-note','OAuth 账户 1 个');await click('[data-remove-model="gpt-6.2-sol"]');await el('#rule-models').fill('new-only');await el('#rule-name').fill('指定凭证');await click('#rule-form button[type=submit]');await el('[data-rule-run="rule-2"]').waitFor();assert.equal(writes.length,2);assert.equal(rules[1].target_scope,'single');assert.equal(rules[1].auth_id,'fixture-122');assert.deepEqual(rules[1].models,['new-only']);assert.equal(rules[1].parameters.reasoning_effort,'high');
    for(const on of [true,false]) {
      await click('[data-rule-edit="rule-2"]');assert.equal(await el('#rule-timer').isChecked(),!on);
      await el('#rule-timer').setChecked(on);await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
      assert.equal(writes.at(-1).b.activation,on?'start':'stop');assert.equal(rules[0].timer_active,false);
      await text('[data-key="rule:rule-2"] .rule-state',on?'定时开启':'仅手动');
    }
    for(const on of [true,false]) {
      const toggle=el('[data-rule-timer="rule-2"]');
      await toggle.focus();await page.keyboard.press('Space');
      await el(`[data-rule-timer="rule-2"][aria-checked="${on}"]:enabled`).waitFor();
      assert.equal(await toggle.innerText(),on?'定时开启':'仅手动');
      assert.equal(rules[0].timer_active,false);
      assert.equal(writes.at(-1).p,'/rules/timer');
      await click('[data-rule-edit="rule-2"]');assert.equal(await el('#rule-timer').isChecked(),on);
      await click('#rule-form .editor-footer [data-close]');
    }
    assert.equal(dispatchPreviews,0);
    await click('[data-rule-run="rule-1"]');await text('#action-description','最多计划 492 份目标回答'); await click('#action-cancel');assert(!writes.some(w=>w.p==='/rules/run'));assert.equal(dispatchPreviews,1);
    await click('[data-rule-run="rule-1"]');await click('#action-confirm');await text('#ops-batch-summary','1–20 / 23');assert.equal(await el('#operations-history [data-run]').count(),20);
    await text('#operations-history','请求强度 low');
    await text('#operations-history','snapshot@example.test');
    const oldSubject = el('[data-key="run:run-1"] .quality-identity-cell');
    assert.match(await oldSubject.innerText(), /fixture-122/);
    assert.doesNotMatch(await oldSubject.innerText(), /scope@example\.test/, 'A missing historical email must not be filled from the live directory');
    const runResult = index => el(`[data-key="run:run-${index}"] .quality-result`);
    assert.equal(await runResult(0).locator('.history-ratio').innerText(), '1 / 1');
    assert.equal(await runResult(1).locator('.history-ratio').count(), 0, 'Missing sample plan cannot establish a whole-round score');
    assert.equal(await runResult(2).locator('.history-ratio').count(), 0, 'Duplicate records cannot cover a missing slot');
    assert.match(await runResult(3).innerText(), /执行失败 1/);
    assert.doesNotMatch(await runResult(3).innerText(), /答错|0 \/ 1/);
    await text('[data-key="rule:rule-2"] .quality-rule-scope','scope@example.test');
    assert.equal(await el('#rule-target option[value="fixture-122"]').textContent(),'scope@example.test');
    await el('#rule-search').fill('scope@example.test');assert.equal(await el('.quality-rule').count(),1);await el('#rule-search').fill('');
    const beforePage = reads.length;
    await click('#ops-batch-next');await text('#ops-batch-summary','21–23 / 23');assert.equal(await el('#operations-history [data-run]').count(),3);assert(await el('#ops-batch-next').isDisabled());
    assert.deepEqual(reads.slice(beforePage), ['/runs']);
    await click('#operations-history [data-run="run-21"]');
    await text('#quality-detail-content','result-21');
    await page.keyboard.press('Escape');
    await text('#ops-batch-summary','本批次 21–23 / 23');
    assert(await el('#operations-history [data-run="run-21"]').evaluate(node => node === document.activeElement));
    await el('#ops-activity').screenshot({path:path.join(out,'scope-batch-390.png')});await click('#ops-batch-prev');await text('#ops-batch-summary','1–20 / 23');await click('#ops-batch-clear');await el('#ops-batch-bar').waitFor({state:'hidden'});
    await click('[data-rule-select="rule-1"]');await click('#ops-rule-context [data-rule-batch="test-batch"]');await text('#ops-batch-summary','1–20 / 23');
    await page.setViewportSize({width:1440,height:900});await el('#ops-activity').screenshot({path:path.join(out,'scope-batch-1440.png')});
    // Rule selection filters on the server before pagination; all history is reachable.
    await click('#ops-batch-clear');await text('#ops-batch-summary','1–20 / 23');await click('#ops-batch-next');await text('#ops-batch-summary','21–23 / 23');
    await click('[data-rule-select="rule-2"]');await text('#ops-scope-label','指定凭证');await text('#operations-history','暂无运行记录');
    workspaceChecks.push({ state: 'empty-records', ...await checkWorkspace(el('.quality-workspace')) });
    await click('[data-rule-select="rule-1"]');await text('#ops-scope-label','日常观察');await text('#ops-batch-summary','1–20 / 23');
    await click('#ops-all-rules');assert.equal(await el('[data-rule-select][aria-pressed=true]').count(),0);
    assert.equal(await el('#ops-all-rules').getAttribute('aria-pressed'),'true');
    await el('#rule-search').fill('new-only');assert.equal(await el('.quality-rule').count(),2);
    await el('#rule-search').fill('no-such-rule');await text('#rules-list','没有匹配的规则');await click('[data-rule-clear]');
    await el('#rule-filter').selectOption('timed');await text('#rules-list','没有匹配的规则');await el('#rule-filter').selectOption('manual');assert.equal(await el('.quality-rule').count(),2);await el('#rule-filter').selectOption('all');
    failActivity=true;await click('#refresh');await text('#activity-read-note','读取失败');assert.equal(await el('#operations-history [data-run]').count(),20);assert(await el('#ops-batch-next').isDisabled());
    failActivity=false;await click('#refresh');await el('#activity-read-note').waitFor({state:'hidden'});
    // Escape and nested discard preserve draft, focus, and the inert workbench.
    await click('#rule-new');await focused('rule-name');await page.keyboard.press('Escape');await el('#rule-dialog').waitFor({state:'hidden'});await focused('rule-new');
    await click('#rule-new');await el('#rule-name').fill('未保存的草稿');await page.keyboard.press('Escape');await el('#action-dialog').waitFor();await click('#action-cancel');assert(await el('#rule-dialog').isVisible());assert.equal(await el('#rule-name').inputValue(),'未保存的草稿');
    await page.keyboard.press('Escape');await click('#action-confirm');await el('#rule-dialog').waitFor({state:'hidden'});await focused('rule-new');
    for(const width of [1920,1440,1160,1064,390,320]) {
      await page.setViewportSize({width,height:width===1160?650:900});
      for (const theme of ['light','white','dark']) {
        await el('#theme-toggle').selectOption(theme);
        workspaceChecks.push({ state:'populated', theme, ...await checkWorkspace(el('.quality-workspace')) });
        await el('.quality-workspace').screenshot({path:path.join(out,`quality-workspace-${width}-${theme}.png`)});
      }
    }
    await page.setViewportSize({width:1440,height:900});
    const footerBefore = await el('#ops-activity > footer').boundingBox();
    await el('#operations-history').focus(); await page.keyboard.press('End');
    await el('#operations-history').evaluate(node => node.scrollTop = node.scrollHeight);
    assert(await el('#operations-history').evaluate(node => node.scrollTop > 0));
    assert.deepEqual(await el('#ops-activity > footer').boundingBox(),footerBefore);
    assert(await el('.quality-run-table th').first().evaluate(th => Math.abs(th.getBoundingClientRect().top - th.closest('.quality-run-list').getBoundingClientRect().top) < 2));
    await el('#operations-history').evaluate(node => node.scrollTop = 0);
    uncertain=true;await click('[data-rule-select="rule-2"]');await click('[data-rule-run="rule-1"]');await click('#action-confirm');await text('#notice','提交结果未确认');await el('#action-dialog').waitFor({state:'hidden'});assert(await el('#ops-activity').isVisible());assert.equal(await el('#ops-scope-label').textContent(),'日常观察');assert.equal(writes.filter(w=>w.p==='/rules/run').length,2);
    // Unrelated edits do not re-submit legacy overrides or replace legacy schedules.
    const originalModels = ['custom legacy id','模型,旧'];
    for(const effort of ['', 'auto','none','minimal']) {
      const old={...rules[1], id:'old-'+(effort||'default'),revision:3,models:originalModels,interval_seconds:750,timezone:'Asia/Kathmandu',retention_days:37,cron:effort?'0 9 * * *':'',parameters:{stream:false,max_tokens:256,temperature:0.7,...(effort?{reasoning_effort:effort}:{})}};delete old.max_results;
      delete old.target_scope;rules.push(old);await click('#refresh');await click(`[data-rule-edit="${old.id}"]`);
      assert(await el('#rule-scope-single').isChecked());assert.deepEqual(await selected(),originalModels);assert(await el('#rule-legacy-effort input').isChecked());assert.equal(await el('#rule-legacy-effort span').textContent(),effort||'未指定');
      await pick('low').check();await el('#rule-legacy-effort input').check();await cleanDraft();
      assert(await el('#rule-cron').isHidden());await text('#rule-original-plan',effort?'Asia/Kathmandu':'750');assert.equal(await el('#rule-retention-mode').inputValue(),'legacy');assert(await el('#rule-max-results').isHidden());
      await el('#rule-name').fill('旧规则改名');await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
      const submitted=writes.at(-1).b;assert.deepEqual(submitted.models,originalModels);assert.equal(submitted.parameters.reasoning_effort,undefined);assert.equal(submitted.parameters.stream,false);assert(!('max_tokens' in submitted.parameters || 'temperature' in submitted.parameters));assert(!['interval_seconds','timezone','cron','retention_days','max_results'].some(k=>k in submitted));
    }
    await click('[data-rule-edit="old-auto"]');await el('#rule-convert-cron').check();assert.equal(await el('#rule-cron').inputValue(),'0 9 * * *');await el('#rule-cron').fill('65 * * * *');await click('#rule-form button[type=submit]');await text('#rule-cron-error','无效');await focused('rule-cron');
    await el('#rule-cron').fill('15 10 * * *');await el('#rule-timer').check();await el('#rule-retention-mode').selectOption('count');await el('#rule-max-results').fill('1');await text('#rule-retention-note','至少保留 2 条');await pick('medium').check();
    await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});assert.equal(writes.at(-1).b.cron,'15 10 * * *');assert.equal(writes.at(-1).b.max_results,2);
    const changed=rules.find(r=>r.id==='old-auto');changed.next_run='2026-11-19T04:30:00Z';changed.next_run_local='2026-11-19T10:15:00+05:45';await click('#refresh');await text('#rules-list','10:15:00 · UTC+05:45');await click('[data-rule-edit="old-auto"]');assert.equal(await el('#rule-cron').inputValue(),'15 10 * * *');assert(await el('#rule-legacy-schedule').isHidden());await click('#rule-form .editor-footer [data-close]');
    const held={...rules[1],id:'held-rule',revision:1,status:'awaiting_release',enabled:false,question_id:'random-linear',judge:{mode:'llm',auth_id:'fixture-1',model:'shared'},control_auth_id:'fixture-2'};
    rules.push(held);await click('#refresh');await click('[data-rule-edit="held-rule"]');assert(await el('#rule-hold-note').isVisible());
    assert(await el('#rule-control-fields').isVisible());assert(await el('#rule-judge-fields').isVisible());assert.equal(await el('#rule-judge-target').inputValue(),'fixture-1');assert.equal(await el('#rule-control-target').inputValue(),'fixture-2');await text('#rule-judge-note','不能执行');
    assert(await el('#rule-timer').isDisabled());assert(!(await el('#rule-timer').isChecked()));await click('#rule-form .editor-footer [data-close]');
    directory.length=0;await page.reload();await el('body.connected').waitFor();await click('[data-page=operations]');await click('#rule-new');
    assert(await el('#rule-model-catalog').isDisabled());assert(await quick('local-one').isEnabled());assert.equal(await quick('local-two').count(),0);assert.deepEqual(await selected(),[defaults[0]]);
    const safeWrites=writes.length;await el('#rule-name').fill('空目录测试');await el('#rule-models').fill('a b c d e f g h');await click('#rule-form button[type=submit]');await text('#rule-models-error','1–8');assert.equal(writes.length,safeWrites);
    await el('#rule-models').fill('a b c d');await el('#rule-samples').fill('8');await click('#rule-form button[type=submit]');await text('#rule-samples-error','限额 32');assert.equal(writes.length,safeWrites);
    await el('#rule-models').fill('');await el('#rule-samples').fill('1');await quick(defaults[1]).click();await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});assert.deepEqual(writes.at(-1).b.models,defaults.slice(0,2));
    await click('#rule-new');await el('#rule-name').fill('答错后暂停');await handling('disable_credential').check();await el('#rule-auto-restore').check();
    await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
    assert.equal(writes.at(-1).b.action,'disable_credential');assert.equal(writes.at(-1).b.auto_restore,true);
    const handlingRule=rules.at(-1);await click(`[data-rule-edit="${handlingRule.id}"]`);
    assert(await handling('disable_credential').isChecked());assert(await el('#rule-auto-restore').isChecked());
    await el('#rule-scope-directory').check();await text('#rule-restore-detail','人工停用');
    assert.equal(await el('#rule-scope-note').innerText(),'候选对象 · OAuth 账户 0 个 · AI 提供商 0 条路由。');
    await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
    assert.equal(writes.at(-1).b.target_scope,'all_credentials');assert(!writes.at(-1).b.auth_id);assert(writes.at(-1).b.auto_restore);
    handlingRule.last_manual_skips=['operator-fixture'];await click('#refresh');
    await click(`[data-rule-select="${handlingRule.id}"]`);await text('#ops-rule-context summary','上轮跳过 1 个手动禁用凭证');
    assert.doesNotMatch(await el('#rules-list').textContent(),/上轮跳过|operator-fixture/);
    await click(`[data-rule-edit="${handlingRule.id}"]`);assert(await el('#rule-scope-directory').isChecked());assert(await el('#rule-auto-restore').isChecked());
    await handling('delete_credential').check();await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
    assert.equal(writes.at(-1).b.action,'delete_credential');assert.equal(writes.at(-1).b.auto_restore,false);
    await click(`[data-rule-edit="${handlingRule.id}"]`);
    await el('#rule-retention-mode').selectOption('manual');assert(await el('#rule-max-results').isHidden());
    await click('#rule-form button[type=submit]');await el('#rule-dialog').waitFor({state:'hidden'});
    assert.equal(writes.at(-1).b.retention_mode,'manual');assert(!('max_results' in writes.at(-1).b));
    await click(`[data-rule-edit="${handlingRule.id}"]`);assert.equal(await el('#rule-retention-mode').inputValue(),'manual');
    await click('#rule-form .editor-footer [data-close]');
    await click('#ops-all-rules');
    const openRow = id => click(`#operations-history [data-run="run-${id}"]`);
    const dialog = el('#quality-detail');
    let releaseDetail;
    detailGate = new Promise(resolve => { releaseDetail = resolve; });
    await openRow(0); await text('#quality-detail-message','正在读取');
    await page.keyboard.press('Escape'); await openRow(1);
    await text('.quality-detail-identity','fixture-122');
    assert.equal(await el('.quality-detail-facts dd').first().innerText(),'错误');
    assert.equal(await el('#quality-detail .sample-heading .tag').count(),0);
    assert.equal(await el('#quality-detail .answer-text').innerText(),'22');
    const late = page.waitForResponse(response => response.url().endsWith('/runs/detail?id=run-0'));
    releaseDetail(); await late; detailGate = null;
    assert.equal(await el('.quality-detail-identity > strong').innerText(),'fixture-122');
    await click('[data-close="quality-detail"]');
    failDetail = true;
    await openRow(0); await text('#quality-detail-message','读取失败');
    failDetail = false; await click('#quality-detail-retry');
    await text('.quality-detail-identity','snapshot@example.test'); await text('.quality-detail-facts','本轮未处置');
    assert.equal(await el('.quality-detail-facts dd').first().innerText(),'正确');
    await text('.quality-detail-question','21'); await text('#quality-detail .sample-heading','样本 1');
    assert.equal(await el('#quality-detail .sample-heading .tag').count(),0);
    assert.equal(await el('#quality-detail .answer-text').innerText(),'21');
    assert.doesNotMatch(await dialog.innerText(),/证据不足/);
    assert(await el('#page-operations').isVisible()); assert(await el('#page-manual').isHidden());
    const manualBefore = await el('#run-meta').textContent();
    await click('#quality-detail [data-evidence-sample="0"]');await text('#evidence-content','回答与标准答案一致');
    await click('#evidence-content summary:has-text("执行条件与诊断标识")');await text('#evidence-content','本次使用 CPA 管理接口检测指定凭证');
    await click('[data-close="evidence-dialog"]'); assert(await dialog.isVisible());
    for (const width of [1440,390,320]) {
      await page.setViewportSize({width,height:width === 1440 ? 900 : 670});
      assert(await dialog.evaluate(node => node.scrollWidth <= node.clientWidth && node.querySelector('.quality-detail-scroll').scrollWidth <= node.querySelector('.quality-detail-scroll').clientWidth));
      await dialog.screenshot({path:path.join(out,`quality-detail-${width}.png`)});
    }
    await page.setViewportSize({width:1440,height:900});
    await page.keyboard.press('Escape'); await dialog.waitFor({state:'hidden'});
    assert.equal(await el('#run-meta').textContent(),manualBefore);
    await el('#operations-history [data-run="run-18"]').scrollIntoViewIfNeeded();
    const listPosition = () => el('#operations-history').evaluate(node => node.scrollTop);
    const scroll = await listPosition(); assert(scroll > 0);
    await openRow(18); await text('#quality-detail-content','result-18'); await click('[data-close="quality-detail"]');
    assert.equal(await listPosition(),scroll);
    await click('#ops-history-clear');await text('#action-title','所有规则');await click('#action-cancel');
    assert(!writes.some(w=>w.p==='/rules/run-history'));
    await click('#ops-history-clear');await click('#action-confirm');await text('#ops-record-count','共 0 条');
    await el('#action-dialog').waitFor({state:'hidden'});assert(await el('#ops-history-clear').isDisabled());
    assert.equal(writes.filter(w=>w.p==='/rules/run-history').length,1);
    assert.deepEqual(errors,[]);assert.deepEqual(unexpected,[]);
    fs.writeFileSync(path.join(out,'quality-workspace.json'),JSON.stringify({ui_version,checks:workspaceChecks,sticky_table_header:true,fixed_pagination:true,all_rules_selection:true,real_writes:0},null,2)+'\n');
    fs.writeFileSync(path.join(out,'quality-scope.json'),JSON.stringify({ui_version,widths,reference_defaults:true,persistent_shortcuts:true,multi_model:true,empty_directory:true,legacy_round_trip:true,keyboard_scope:true,limit_validation:true,uncertain_no_retry:true,paged_batch:true,no_global_write:true,single_server_cron:true,explicit_conversion:true,zoned_list:true,rule_efforts:efforts,per_account_result_limit:true,effort_keyboard_and_roundtrip:true,requested_effort_in_history:true,intersection_estimate_at_execution:true,no_editor_estimate:true,exact_cron_label:true,layout_checks:layouts,lazy_question_reference:true,continuous_sheet:true,modal:true,rule_filter:true,server_history_filter:true,all_history_pagination:true,read_error_preserves_rows:true,escape_discard_focus:true,single_timer:true,inline_validation:true,layouts:layouts.length,fixture_writes:writes.length,real_writes:0},null,2)+'\n');
    assert.equal(dispatchPreviews,3);
    console.log('Quality rule editor: reference defaults, keyboard, custom shortcuts, batch input, multi-model payloads, original rules, empty catalog, budgets, scheduling and paged results passed.');
  } finally { if(browser) await browser.close();child.kill();await exit; }
}
main().catch(error=>{console.error(error);process.exitCode=1;});
