// Browser contract checks against an in-memory CPA directory. No model endpoint is exposed.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const { spawn } = require('node:child_process');
const { once } = require('node:events');
const { createInterface } = require('node:readline');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function main() {
  const host = spawn(process.execPath, [path.join(__dirname,'login_ui_fixture.cjs')], {stdio:['ignore','pipe','inherit']});
  const exited = once(host,'exit');
  let browser;
  try {
    const [line] = await Promise.race([once(createInterface({input:host.stdout}),'line'),exited.then(() => { throw Error('Fixture exited'); })]);
    const info = JSON.parse(line), base = new URL(info.url), prefix = '/v0/management/plugins/model-quality';
    assert.equal(base.hostname,'127.0.0.1'); assert.notEqual(base.port,'8317'); assert.equal(info.pid,host.pid);
    browser = await chromium.launch({executablePath:process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',headless:true});
    const page = await browser.newPage({viewport:{width:1360,height:900},reducedMotion:'reduce'});
    page.setDefaultTimeout(5500);
    const account = {auth_id:'same-file',auth_index:'f-1',email:'same@example.test',provider:'codex',object_type:'oauth',runtime_status:'active',effective_available:true,models:['gpt-6-astra']};
    const route = (auth_id,auth_index,group,name) => ({auth_id,auth_index,provider:'codex',object_type:'provider',is_provider:true,provider_group_id:group,
      provider_name:name,provider_disable_unit:'group',runtime_status:'registered',effective_available:true,models:['gpt-6-astra']});
    const group='compat/first', a=route('north-a','idx-A',group,'北区'), b=route('north-b','idx-B',group,'北区');
    const c = {...route('west','idx-C','compat/other','西区'),runtime_status:'disabled',manual_disabled:true,effective_available:false};
    const unknown = {...account,auth_id:'uncertain',object_type:'unknown'};
    let directory = [account,a,b,c,unknown], catalogOK = true, partial = false, revision = 1, runPreviewExpected = false, resultsOffline = false;
    const rules = [], previews = [], writes = [], batches = [], unexpected = [], errors = [], layouts = [], assets = new Map();
    const names = ['index.html','style.css','app.js','view.js','client.js','login.js'];
    page.on('response', response => {
      const url = new URL(response.url()), name = path.basename(url.pathname) || 'index.html';
      if (url.origin === base.origin && names.includes(name)) assets.set(name,response.body().then(body => createHash('sha256').update(body).digest('hex')));
    });
    const countTypes = rows => ({oauth:rows.filter(t => t.object_type === 'oauth').length,provider:rows.filter(t => t.object_type === 'provider').length});
    const plans = draft => {
      const independentRecovery = t => draft.target_scope === 'all_enabled' && draft.action === 'disable_credential' && draft.auto_restore &&
        t.manual_disabled && t.quality_owner === draft.id && !!draft.id;
      const selected = directory.find(t => t.auth_id === draft.auth_id);
      const selectedGroup = draft.target_group || (selected && draft.authorized_groups?.includes(selected.provider_group_id) ? selected.provider_group_id : '');
      const inventory = directory.filter(t => draft.object_types?.includes(t.object_type) && (!partial || t.object_type === 'oauth') &&
        (draft.target_scope !== 'single' || draft.auth_id === t.auth_id || (selectedGroup && selectedGroup === t.provider_group_id)));
      const excluded = [], match = [];
      for (const t of inventory) {
        const detail = t.error_policy_block || (draft.target_scope === 'all_enabled' && t.manual_disabled && !independentRecovery(t)
          ? {code:'manual_disabled',reason:'对象已在 CPA 停用，未纳入「仅已启用」。'} : null);
        if (detail) excluded.push({auth_id:t.auth_id,email:t.email,provider_name:t.provider_name,...detail});
        else match.push(t);
      }
      const missing = match.filter(t => t.auth_id === 'west');
      const recovery = match.filter(independentRecovery).length;
      return {verified:!partial,reason:partial?'CPA 提供商配置目录未读到，AI 提供商范围待核实。':undefined,directory_counts:countTypes(inventory),excluded,candidates:countTypes(match),
        count:match.length-missing.length,recovery_count:recovery,skipped:missing.length,skip_details:missing.map(t => ({auth_id:t.auth_id,code:'unverified_route',reason:'停用路由的模型映射无法核实。'})),
        preflight_details: match.some(t => t.auth_id === a.auth_id) ? [{auth_id:a.auth_id,code:'missing_model',model:'fake-missing',reason:'CPA 目录已确认模型缺项。'}] : []};
    };
    page.on('pageerror',e => errors.push(e.message));
    await page.route('**/*',async request => {
      const r = request.request(), u = new URL(r.url());
      if (u.origin !== base.origin) { unexpected.push(u.origin); return request.abort(); }
      const api = u.pathname.slice(prefix.length);
      const list = rows => request.fulfill({json:{items:rows,total:rows.length,offset:0}});
      if (r.method() === 'GET') {
        if (resultsOffline && (api === '/runs' || api.startsWith('/runs/'))) return request.fulfill({status:503,json:{error:{message:'synthetic offline results'}}});
        if (api === '/targets') {
          if (!catalogOK) return request.fulfill({status:503,json:{error:{message:'synthetic offline directory'}}});
          const items = partial ? directory.filter(t=>t.object_type==='oauth') : directory;
          return request.fulfill({json:{items,total:items.length,offset:0,provider_directory_verified:!partial}});
        }
        if (api === '/rules') return list(rules);
        return request.continue();
      }
      const body = r.postDataJSON();
      if (api === '/rules/scope-preview' && r.method() === 'POST') {
        if (!runPreviewExpected) unexpected.push('Scope preview without an explicit run');
        runPreviewExpected=false;
        previews.push(body); return request.fulfill({json:plans(body)});
      }
      if (api === '/rules' && ['PUT','POST'].includes(r.method())) {
        writes.push(body);
        const target=directory.find(t=>t.auth_id===body.auth_id && t.provider===body.provider && t.auth_index===body.target_auth_index);
        if (body.expected_target_object_type && body.expected_target_object_type!==target?.object_type) {
          return request.fulfill({status:409,json:{error:{code:'conflict',message:'CPA 当前目录无法核实所选对象的来源、提供方或索引，请刷新后重新选择'}}});
        }
        const prior = rules.find(t => t.id === body.id), saved = {legacy_scope:prior?.legacy_scope,legacy_provider_observation:prior?.legacy_provider_observation,
          ...body,id:body.id || 'rule-'+revision,revision:revision++,enabled:true,timer_active:false};
        delete saved.expected_target_object_type;
        if (target) saved.target_object_type=target.object_type;
        saved.parameters={...prior?.parameters,...body.parameters};
        if(saved.parameters.reasoning_effort === 'default') saved.parameters.reasoning_effort='';
        if (prior) rules[rules.indexOf(prior)] = saved; else rules.push(saved);
        return request.fulfill({json:saved});
      }
      if (api === '/rules/run' && r.method() === 'POST') {
        batches.push(body); const rule = rules.find(t => t.id === body.id); assert.equal(body.revision,rule.revision);
        const {directory_counts,excluded,...receipt} = plans(rule);
        return request.fulfill({status:202,json:{...receipt,scope_batch_id:'synthetic-batch'}});
      }
      unexpected.push(api); return request.abort();
    });
    const el = selector => page.locator(selector), visible = (selector,text) => el(selector).filter({hasText:text}).first().waitFor();
    const chooseScope = async id => { await el(id).check(); assert(await el(id).isChecked()); };
    const directoryCount = async (oauth,provider) => {
      const expected = '候选对象 · OAuth 账户 '+oauth+' 个 · AI 提供商 '+provider+' 条路由。';
      await visible('#rule-scope-note',expected);
      assert.equal(await el('#rule-scope-note').innerText(),expected);
    };
    const save = async () => { await el('#rule-form button[type=submit]').click(); await el('#rule-dialog').waitFor({state:'hidden'}); };
    const out = process.env.UI_OUTPUT;
    if (out) await fs.mkdir(out,{recursive:true});
    await page.goto(info.url); await el('#login-connect').click(); await el('#management-key').fill(info.management_key);
    await el('#connection-form button[type=submit]').click(); await el('body.connected').waitFor(); await el('[data-page=operations]').click();
    assert.equal(await el('#ops-include-providers').count(),0);
    await el('#rule-new').click();
    assert(await el('#rule-object-oauth').isChecked()); assert(!await el('#rule-object-provider').isChecked());
    assert(await el('#rule-scope-all').isChecked()); assert(!await el('#rule-timer').isChecked()); assert(await el('[name=rule-action][value=observe_only]').isChecked());
    const shortcuts = ['gpt-6.2-sol','gpt-6-astra','gpt-6.1-sol'];
    assert.deepEqual(await el('#rule-model-selection code').allTextContents(),[shortcuts[0]]);
    assert.deepEqual(await el('#rule-model-shortcuts button:enabled code').allTextContents(),shortcuts.slice(1));
    assert.doesNotMatch(await el('#rule-dialog').innerText(),/模型默认/);
    await directoryCount(1,0);
    assert.equal(await el('#rule-dialog #rule-scope-skips,#rule-dialog #rule-scope-skip-list').count(),0);
    await el('#rule-name').fill('按规则筛选');
    await el('#rule-object-oauth').uncheck(); await el('#rule-form button[type=submit]').click();
    await visible('#rule-object-oauth-error','至少选择'); assert.equal(writes.length,0);
    await el('#rule-object-provider').check(); await chooseScope('#rule-scope-directory');
    await directoryCount(0,3);
    await chooseScope('#rule-scope-single');
    assert.equal(await el('#rule-target optgroup[label="OAuth 账户"]').count(),0);
    assert.equal(await el('#rule-target optgroup[label$="配置组"] option').count(),2);
    assert.equal(await el('#rule-target optgroup[label$="单条路由"] option').count(),3);
    await el('#rule-target').selectOption(a.auth_id);
    const grant = el('#rule-group-options input').first();
    assert(!await grant.isChecked()); await directoryCount(0,1);
    assert.doesNotMatch(await el('#rule-dialog').innerText(),/模型默认/);
    await save(); assert.deepEqual(writes[0].object_types,['provider']); assert.deepEqual(writes[0].authorized_groups,[]);
    assert.equal(writes[0].parameters.reasoning_effort,'high'); assert.deepEqual(writes[0].models,[shortcuts[0]]);
    assert.equal(writes[0].auth_id,a.auth_id); assert.equal(writes[0].target_auth_index,a.auth_index); assert.equal(writes[0].expected_target_object_type,'provider'); assert(!writes[0].target_group); assert.equal(batches.length,0);
    const first = rules[0].id;
    directory=directory.map(t=>t===a?{...t,auth_index:'replacement'}:t);
    await el('#refresh').click(); await el('[data-rule-edit="'+first+'"]').click();
    assert(await el('#rule-reasoning [value="high"]').isChecked());
    assert.match(await el('#rule-target option:checked').innerText(),/idx-A.*原绑定/);
    assert(await el('#rule-target option:checked').evaluate(option=>option.disabled),await el('#rule-target').innerHTML());
    await directoryCount(0,0);
    await el('#rule-target').selectOption(a.auth_id);
    assert.match(await el('#rule-target option:checked').innerText(),/replacement/);
    await directoryCount(0,1);
    await el('#rule-form .editor-footer [data-close]').click(); await el('#action-confirm').click();
    directory=directory.map(t=>t.auth_id===a.auth_id?a:t); await el('#refresh').click();
    await visible('.quality-rule-scope','AI 提供商 · 指定对象'); await el('[data-rule-edit="'+first+'"]').click();
    await el('#rule-target').selectOption({label:'北区 · 全部 2 条路由'});
    await directoryCount(0,2);
    assert(!await grant.isChecked());
    await grant.check(); await visible('#rule-group-options label','idx-A、idx-B');
    await el('#rule-object-oauth').check(); await chooseScope('#rule-scope-directory');
    await el('[name=rule-action][value=disable_credential]').check(); await el('#rule-auto-restore').check();
    await directoryCount(1,3);
    for (const [width,height,theme] of [[1360,900,'light'],[390,720,'dark'],[320,600,'white']]) {
      await page.setViewportSize({width,height}); await page.evaluate(t => document.documentElement.dataset.theme=t,theme);
      await el('.rule-form-scroll').evaluate(n => n.scrollTop=0);
      const layout = await el('#rule-dialog').evaluate(node => {
        const area=node.querySelector('.rule-form-scroll'), footer=node.querySelector('.editor-footer').getBoundingClientRect(), box=node.getBoundingClientRect();
        const scope = node.querySelector('#rule-scope-all').closest('fieldset'), objects = node.querySelector('#rule-object-oauth').closest('fieldset');
        return {overflow:area.scrollWidth>area.clientWidth+1 || node.scrollWidth>node.clientWidth+1,
          saveVisible:footer.bottom<=box.bottom+1 && footer.top>box.top && footer.bottom<=innerHeight,
          scopeAboveObjects:scope.getBoundingClientRect().bottom<=objects.getBoundingClientRect().top+1 && !!(scope.compareDocumentPosition(objects)&Node.DOCUMENT_POSITION_FOLLOWING),
          controls:[...node.querySelectorAll('.rule-membership label,.rule-group-option')].filter(n=>n.getClientRects().length).every(n=>n.getBoundingClientRect().right<=area.getBoundingClientRect().right+1)};
      });
      assert(!layout.overflow && layout.saveVisible && layout.controls && layout.scopeAboveObjects,JSON.stringify({width,...layout})); layouts.push({width,height,theme,...layout});
      if (out) await el('#rule-dialog').screenshot({path:path.join(out,'rule-objects-'+width+'.png')});
    }
    await save(); const both=writes[1]; assert.deepEqual(both.object_types,['oauth','provider']); assert.equal(both.target_scope,'all_credentials');
    assert.deepEqual(both.authorized_groups,[group]); assert.equal(both.auto_restore,true); assert.equal(batches.length,0);
    partial=true; await el('#refresh').click(); await el('[data-rule-edit="'+first+'"]').click();
    await visible('#rule-scope-note','OAuth 账户 1 个'); await visible('#rule-scope-note',/AI 提供商\s*待核实/);
    assert.doesNotMatch(await el('#rule-scope-note').innerText(),/AI 提供商 0 条|恢复复检|未执行/);
    await el('#rule-form .editor-footer [data-close]').click(); partial=false; await el('#refresh').click();
    assert.equal(previews.length,0); runPreviewExpected=true;
    await el('[data-rule-run="'+first+'"]').click(); await visible('#action-description','恢复复检 0 个'); await el('#action-confirm').click();
    await visible('#ops-dispatch-receipt','恢复复检 0 个'); await visible('#ops-dispatch-receipt','未执行 1 个');
    await visible('#ops-dispatch-receipt','模型映射无法核实'); assert.equal(batches.length,1);
    // The previous completed round now owns this synthetic wrong-answer suspension.
    directory=directory.map(t=>t.auth_id===account.auth_id?{...t,manual_disabled:true,runtime_status:'disabled',quality_owner:first}:t);
    await el('#refresh').click(); await el('[data-rule-edit="'+first+'"]').click(); await directoryCount(1,3);
    await chooseScope('#rule-scope-all'); await directoryCount(0,2);
    if (out) await el('#rule-dialog').screenshot({path:path.join(out,'rule-enabled-320.png')});
    await save(); assert.equal(writes[2].target_scope,'all_enabled');
    assert.equal(previews.length,1); runPreviewExpected=true;
    await el('[data-rule-run="'+first+'"]').click(); await visible('#action-description','恢复复检 1 个'); await el('#action-confirm').click();
    await visible('#ops-dispatch-receipt','检测 2 个 · 恢复复检 1 个 · 未执行 0 个'); assert.equal(batches.length,2);
    await el('[data-rule-edit="'+first+'"]').click(); await chooseScope('#rule-scope-single'); await el('#rule-target').selectOption({label:'北区 · 全部 2 条路由'});
    await save(); assert.equal(writes[3].target_group,group); assert.equal(writes[3].auth_id,undefined);
    const localGroup='native/multi';
    directory=directory.filter(t => t.auth_id!==b.auth_id).concat(['one','two'].map(k=>({...route('local-'+k,'local-'+k,localGroup,'单路由启停组'),provider_disable_unit:'route'})));
    await el('#refresh').click(); await el('[data-rule-edit="'+first+'"]').click(); await directoryCount(0,1);
    await visible('#rule-group-options label','影响 1 条路由');
    await el('#rule-target').selectOption({label:'单路由启停组 · 全部 2 条路由'}); await directoryCount(0,2);
    assert.equal(await el('#rule-group-options [data-group-grant="'+localGroup+'"]').count(),0);
    await el('#rule-form .editor-footer [data-close]').click(); await el('#action-confirm').click();
    const legacyModels = ['gpt-6-astra','custom-legacy-model'];
    const legacyParameters = {reasoning_effort:'',stream:false,max_tokens:768,temperature:0.25};
    const migrated={...rules[0],id:'legacy',name:'旧的仅观察规则',models:legacyModels,parameters:legacyParameters,legacy_scope:true,legacy_provider_observation:true,target_scope:'all_enabled',
      target_group:'',object_types:['oauth'],authorized_groups:[],timer_active:false}; rules.push(migrated);
    await el('#refresh').click(); await el('[data-rule-edit=legacy]').click(); await el('#rule-legacy-scope').waitFor();
    assert.deepEqual(await el('#rule-model-selection code').allTextContents(),legacyModels);
    assert.doesNotMatch(await el('#rule-dialog').innerText(),/模型默认/);
    assert(!await el('#rule-adopt-scope').isChecked()); await el('#rule-name').fill('仅编辑名称'); await save();
    assert(!writes[4].adopt_scope); assert.deepEqual(writes[4].authorized_groups,[]);
    assert.deepEqual(writes[4].models,legacyModels);
    assert.deepEqual(rules.find(t=>t.id==='legacy').parameters,legacyParameters);
    await el('[data-rule-edit=legacy]').click(); await el('#rule-adopt-scope').check(); await el('#rule-object-provider').check(); await save();
    assert(writes[5].adopt_scope); assert.deepEqual(writes[5].object_types,['oauth','provider']);
    catalogOK=false; await el('#refresh').click(); await el('#rule-new').click();
    await visible('#rule-scope-note','目录待核实');
    assert.doesNotMatch(await el('#rule-scope-note').innerText(),/账户 \d+|提供商 \d+|未执行|恢复复检/);
    await el('#rule-name').fill('目录未取得时保存'); await save();
    assert.equal(writes[6].target_scope,'all_enabled'); assert.deepEqual(writes[6].object_types,['oauth']); assert.equal(batches.length,2);
    catalogOK=true;
    const disabledAccount = (key,reason) => ({...account,auth_id:key,auth_index:'synthetic-'+key,email:key+'@example.test',
      runtime_status:'disabled',manual_disabled:true,effective_available:false,...(reason ? {error_policy_block:{code:'error_policy_disabled',reason}} : {})});
    const manuallyEnabled = {...disabledAccount('reenabled','原处置已确认 401 身份验证失败。'),runtime_status:'active',manual_disabled:false};
    directory = [manuallyEnabled,disabledAccount('manual'),disabledAccount('quota','原处置已确认 429 账户额度耗尽。')];
    await page.setViewportSize({width:1360,height:900});
    await page.evaluate(()=>document.documentElement.dataset.theme='light');
    await el('#refresh').click(); await el('#rule-new').click();
    const directoryChecks = [];
    for (const [id,count] of [['#rule-scope-all',1],['#rule-scope-directory',3]]) {
      await chooseScope(id);
      await directoryCount(count,0);
      assert.doesNotMatch(await el('#rule-dialog').innerText(),/未纳入本轮|查看范围明细|已确认 401|已确认 429/);
      directoryChecks.push({target_scope:await el(id).inputValue(),summary:await el('#rule-scope-note').innerText()});
      if (out) {
        for (const width of [1360,390]) {
          await page.setViewportSize({width,height:width===1360?900:720});
          await el('#rule-scope-note').scrollIntoViewIfNeeded();
          await el('#rule-dialog').screenshot({path:path.join(out,'directory-'+(id==='#rule-scope-directory'?'all':'enabled')+'-'+width+'.png')});
        }
      }
    }
    await el('#rule-form .editor-footer [data-close]').click(); await el('#action-confirm').click();
    resultsOffline=true;
    directory = [manuallyEnabled,...['native_cooldown','native_unavailable'].map(reason=>({...account,auth_id:reason,auth_index:'idx-'+reason,
      runtime_status:'error',manual_disabled:false,effective_available:false,reasons:[reason]}))];
    await el('#refresh').click(); await el('#rule-new').click();
    assert(await el('#rule-scope-all').isChecked()); await directoryCount(3,0);
    directoryChecks.push({target_scope:'all_enabled',directory_states:['active_with_error_policy_history','native_cooldown','native_unavailable'],
      results_offline:true,summary:await el('#rule-scope-note').innerText()});
    assert.equal(writes.length,7); assert.equal(batches.length,2); assert.equal(previews.length,2); assert.equal(runPreviewExpected,false);
    await el('#rule-form .editor-footer [data-close]').click();
    const replacement = {...account,auth_id:a.auth_id,auth_index:a.auth_index,email:'replacement@example.test',models:['replacement-only-model']};
    const bound = {...rules[0],id:'bound-route',name:'固定来源验证',target_scope:'single',target_group:'',auth_id:a.auth_id,
      provider:a.provider,target_auth_index:a.auth_index,target_object_type:'provider',object_types:['oauth','provider'],authorized_groups:[]};
    rules.push(bound);
    directory=[replacement,account,b]; resultsOffline=false;
    await el('#refresh').click();
    const boundCard=el('[data-key="rule:bound-route"] .quality-rule-scope');
    await boundCard.waitFor();
    const scopeLabel=await boundCard.innerText();
    assert.match(scopeLabel,/供应商路由.*north-a.*idx-A.*原绑定待核实/);
    assert.doesNotMatch(scopeLabel,/replacement@example/);
    await el('#rule-search').fill(replacement.email);
    assert.equal(await boundCard.count(),0,'A different source entered the saved-rule search index');
    await el('#rule-search').fill('');
    await el('[data-rule-edit="bound-route"]').click();
    const option=el('#rule-target option:checked');
    assert.match(await option.innerText(),/供应商路由.*north-a.*idx-A.*原绑定待核实/);
    assert(await option.evaluate(o=>o.disabled));
    assert.equal(await option.getAttribute('data-kind'),'provider');
    await directoryCount(0,0);
    assert.equal(await el('#rule-model-catalog option[value="replacement-only-model"]').count(),0);
    assert.equal(writes.length,7); assert.equal(batches.length,2);
    if (out) await el('#rule-dialog').screenshot({path:path.join(out,'bound-origin.png')});
    await el('#rule-target').selectOption(account.auth_id);
    assert.equal(await option.getAttribute('data-kind'),'oauth');
    assert(!(await option.evaluate(o=>o.disabled)));
    await directoryCount(1,0);
    await save();
    assert.equal(writes.length,8);
    assert.equal(writes.at(-1).auth_id,account.auth_id);
    assert.equal(writes.at(-1).target_auth_index,account.auth_index);
    assert.equal(writes.at(-1).expected_target_object_type,'oauth');
    assert.equal(batches.length,2); assert.equal(previews.length,2);
    await el('[data-rule-edit="bound-route"]').click();
    await el('#rule-name').fill('仅修改名称');
    await save();
    assert.equal(writes.length,9);
    assert.equal(writes.at(-1).expected_target_object_type,undefined);
    const savedCount=rules.length;
    await el('#rule-new').click();
    await el('#rule-name').fill('新规则来源冲突');
    await el('#rule-object-provider').check(); await chooseScope('#rule-scope-single');
    await el('#rule-target').selectOption(b.auth_id);
    directory=directory.map(t=>t===b?{...replacement,auth_id:b.auth_id,auth_index:b.auth_index}:t);
    await el('#rule-form button[type=submit]').click();
    await visible('#rule-dialog',/来源、提供方或索引/);
    assert.equal(writes.length,10); assert.equal(writes.at(-1).expected_target_object_type,'provider');
    assert.equal(rules.length,savedCount); assert.equal(batches.length,2); assert.equal(previews.length,2);
    assert.deepEqual(errors,[]); assert.deepEqual(unexpected,[]);
    const hashes = {};
    for (const [name,checksum] of assets) hashes[name]=await checksum;
    assert.deepEqual(Object.keys(hashes).sort(),[...names].sort());
    const version = JSON.parse(await el('#build-info').textContent()).ui_version;
    if (out) await fs.writeFile(path.join(out,'rule-objects.json'),JSON.stringify({ui_version:version,ui_source_sha256:hashes,previews:previews.length,saved:writes,dispatched:batches.length,
      model_calls:0,editor_scope_previews:0,dispatch_scope_previews:previews.length,layouts,directory_checks:directoryChecks,shortcut_order:shortcuts,legacy_models:legacyModels,legacy_parameters:legacyParameters,
      empty_selection_rejected:true,verified_group_authorization:true,unknown_directory_save:true,fixed_origin_replacement:{scope_label:scopeLabel,
        replacement_selected:false,replacement_model_offered:false,explicit_other_binding_saved:true,ordinary_rename_preserved:true,
        draft_changed_before_post_rejected:true}},null,2)+'\n');
    console.log('Local rule directory counts, zero editor scope previews, model shortcuts, legacy edits, dispatch previews, batch receipts and three responsive layouts passed.');
  } finally { await browser?.close(); host.kill(); await exited; }
}
main().catch(error => { console.error(error); process.exitCode=1; });
