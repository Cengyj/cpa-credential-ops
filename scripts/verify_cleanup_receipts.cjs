// Drive the actual confirmation dialogs against isolated, in-memory cleanup writes.
const assert = require('node:assert/strict');
const { once } = require('node:events');
const { spawn } = require('node:child_process');
const { createInterface } = require('node:readline');
const fs = require('node:fs/promises');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

function gate() {
  let release;
  const promise = new Promise(resolve => { release = resolve; });
  return { promise, release };
}

async function main() {
  const fixture = spawn(process.execPath, [path.join(__dirname, 'login_ui_fixture.cjs')], { stdio: ['ignore', 'pipe', 'inherit'] });
  const exited = once(fixture, 'exit');
  let browser;
  try {
    const [line] = await once(createInterface({ input: fixture.stdout }), 'line');
    const { url, management_key } = JSON.parse(line), { origin, hostname, port } = new URL(url);
    assert.equal(hostname, '127.0.0.1'); assert.notEqual(port, '8317');
    browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
    const out = process.env.UI_OUTPUT, report = { source: {}, scenarios: {} };
    if (out) await fs.mkdir(out, { recursive: true });
    for (const file of ['app.js', 'index.html', 'client.js', 'view.js', 'style.css', 'login.js']) {
      report.source[file] = createHash('sha256').update(await fs.readFile(path.join(__dirname, '../plugin/ui', file))).digest('hex');
    }
    const observed = process.argv.includes('--observe');
    const prefix = '/v0/management/plugins/model-quality', a = 'rule-a', b = 'rule-b';
    async function scenario(name, interact, { width = 1440, height = 900, theme = 'light', bulkHistory = false, confirmedHandling = false, groupHandling = false, onlyHandledRuns = false } = {}) {
      const context = await browser.newContext({ viewport: { width, height }, reducedMotion: 'reduce' });
      try {
        const page = await context.newPage(); page.setDefaultTimeout(6000);
        const served = new Map();
        if (!report.served) page.on('response',response=>{
          const url = new URL(response.url()), file = path.basename(url.pathname) || 'index.html';
          if (url.origin===origin && Object.hasOwn(report.source,file)) served.set(file,response.body().then(bytes=>createHash('sha256').update(bytes).digest('hex')));
        });
        const rules = [a, b].map((id, index) => ({ id, name: ['周一观察', '周二观察'][index], revision: 3, target_scope: 'all_enabled', enabled: true, timer_active: false, action: 'observe_only', models: ['fixture-reasoner'], samples: 1, question_id: 'logic-candy' }));
        const run = (id, rule, status = 'completed') => ({ id, source: rule ? 'scheduled_probe' : 'manual_test', created_at: '2026-11-20T02:00:00Z', status, samples: [], config: { rule_id: rule, auth_id: 'fixture-quality-primary', models: ['fixture-reasoner'], samples: 1, question_id: 'logic-candy' } });
        const runs = new Map([run('manual', ''), run('other-manual', ''), run('first', a), run('second', b), run('in-progress', b, 'running'), run('protected', b)].map(item => [item.id, item]));
        const targets = [], archivedRuns = new Set();
        if (confirmedHandling) {
          const record = {auth_id:'fixture-quality-primary',action_id:'confirmed-401',state:'disabled',
            email:'rule-suspended@example.test',run_id:'first',upstream_condition:'unauthorized'};
          Object.assign(rules[0], {upstream_error_actions:{unauthorized:'disable_credential'},handling:[record]});
          Object.assign(runs.get('first'), {status:'failed',upstream_condition:'unauthorized',action_status:'credential_suspended',credential_email:record.email});
          runs.set('ordinary',run('ordinary',a));
          if (groupHandling) {
            rules.splice(1);
            for (const [id,r] of runs) if (r.config.rule_id===b) runs.delete(id);
            rules[0].handling=[{...record,email:'',auth_id:'route-one'},{...record,email:'',auth_id:'route-two'}];
            for (const {auth_id} of rules[0].handling) targets.push({auth_id,auth_index:'idx-'+auth_id,
              provider:'openai-compatibility',provider_name:'隔离提供商',provider_group_id:'fixture-group',provider_disable_unit:'group',
              object_type:'provider',is_provider:true,models:['fixture-reasoner'],runtime_status:'disabled',manual_disabled:true,effective_available:false});
          } else {
            targets.push({auth_id:record.auth_id,auth_index:'fixture-index',email:record.email,provider:'codex',object_type:'oauth',
              models:['fixture-reasoner'],runtime_status:'disabled',manual_disabled:true,effective_available:false});
          }
        }
        if (onlyHandledRuns) {
          const reference = runs.get('first');
          const firstHandling = rules[0].handling[0];
          for (const [id, item] of runs) if (item.source === 'scheduled_probe') runs.delete(id);
          Object.assign(rules[0], { last_scope_batch: 'finished-batch', last_run: 'handled-6', handling: [] });
          for (let i = 1; i <= 6; i++) {
            const id = `handled-${i}`, authID = `fixture-handled-${i}`, email = `${authID}@example.test`;
            rules[0].handling.push({ ...firstHandling, run_id: id, auth_id: authID, action_id: `confirmed-401-${i}`, email });
            runs.set(id, { ...reference, id, credential_email: email,
              config: { ...reference.config, auth_id: authID, scope_batch_id: 'finished-batch' } });
          }
        }
        const receiptStorage = 'model-quality:manual-receipt:' + prefix;
        if (bulkHistory) {
          for (let i = 1; i <= 11; i++) runs.set('archived-' + i, run('archived-' + i, ''));
          const running = run('active-manual', '', 'running');
          running.config.idempotency_key = 'synthetic-active-receipt';
          runs.set(running.id, running);
          await page.addInitScript(([storage, value]) => {
            if (window === window.top) sessionStorage.setItem(storage, value);
          }, [receiptStorage, running.config.idempotency_key]);
        }
        const writes = [], reads = [], errors = [], unexpectedWrites = [], control = {};
        page.on('pageerror', error => errors.push(error.message));
        await context.route('**/*', async route => {
          const req = route.request(), u = new URL(req.url());
          assert.equal(u.origin, origin);
          const p = u.pathname.slice(prefix.length);
          if (!u.pathname.startsWith(prefix + '/')) {
            if (confirmedHandling && !['GET','HEAD'].includes(req.method())) {
              unexpectedWrites.push({method:req.method(),path:u.pathname}); return route.abort();
            }
            return route.continue();
          }
          const slice = rows => {
            const offset = Number(u.searchParams.get('offset') || 0), limit = Number(u.searchParams.get('limit') || 20);
            return route.fulfill({ json: { items: rows.slice(offset, offset + limit), offset, limit, total: rows.length } });
          };
          if (req.method() === 'GET') {
            reads.push(p + u.search);
            if (p === '/metrics' && control.metricsFail) return route.fulfill({ status: 503, json: { error: 'offline metrics' } });
            if (p === '/runs') return slice([...runs.values()].filter(r => !archivedRuns.has(r.id) && r.source === u.searchParams.get('source') &&
              (!u.searchParams.get('rule_id') || u.searchParams.get('rule_id') === r.config.rule_id) &&
              (!u.searchParams.get('scope_batch_id') || u.searchParams.get('scope_batch_id') === r.config.scope_batch_id)));
            if (p === '/rules') {
              if (control.rulesReadFailed) return route.fulfill({status:503,json:{error:{message:'synthetic offline rule list'}}});
              return slice(rules);
            }
            if (p === '/targets' && confirmedHandling) return slice(targets.map(t => {
              const owned=rules.some(rule=>rule.handling?.some(entry=>entry.auth_id===t.auth_id));
              return owned?{...t,error_policy_block:{code:'error_policy_disabled',auth_id:t.auth_id,reason:'401'}}:t;
            }));
            if (p === '/runs/detail') {
              const key = u.searchParams.get('idempotency_key');
              const item = key ? [...runs.values()].find(r => r.config.idempotency_key === key) : runs.get(u.searchParams.get('id'));
              if (control.holdID && item?.id === control.holdID) {
                control.entered.release();
                await control.resume.promise;
              }
              return item && !archivedRuns.has(item.id) ? route.fulfill({ json: item }) : route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'missing run' } } });
            }
            return route.continue();
          }
          const body = req.postData() ? req.postDataJSON() : null;
          writes.push({ p, body });
          if (req.method()!=='DELETE') {
            unexpectedWrites.push({method:req.method(),path:u.pathname}); return route.abort();
          }
          let receipt = { ok: true };
          if (p === '/rules/run-history') {
            receipt = { deleted: 0, kept: 0 };
            const confirmedRuns = new Set(rules.flatMap(rule => (rule.handling || []).map(action => action.run_id)));
            for (const [id, r] of runs) {
              if (r.source !== 'scheduled_probe' || archivedRuns.has(id)) continue;
              if (r.status === 'running' || id === 'protected') {
                receipt.kept++;
                if (id === 'protected') receipt.kept_for_handling = 1;
              } else if (confirmedRuns.has(id)) {
                archivedRuns.add(id);
                receipt.archived = (receipt.archived || 0) + 1;
              } else {
                runs.delete(id);
                receipt.deleted++;
              }
            }
            for (const rule of rules) {
              if (!runs.has(rule.last_run) || archivedRuns.has(rule.last_run)) delete rule.last_run;
              if (![...runs.values()].some(r => r.config.scope_batch_id === rule.last_scope_batch && !archivedRuns.has(r.id))) delete rule.last_scope_batch;
            }
            if (control.afterRuleClear) runs.set('finished-later', run('finished-later', b));
          } else if (p === '/rules') {
            if (control.conflict) return route.fulfill({ status: 409, json: { error: { code: 'conflict', message: '规则仍有关联的运行记录' } } });
            const index = rules.findIndex(rule => rule.id === body.id && rule.revision === body.revision);
            if (index < 0) return route.fulfill({ status: 404, json: { error: { code: 'not_found', message: 'missing rule' } } });
            rules.splice(index,1);
            for (const [id,r] of runs) if (r.config.rule_id===body.id) runs.delete(id);
            if (control.failRuleReadAfterDelete) control.rulesReadFailed=true;
          } else if (p === '/runs/history') {
            assert.equal(body, null);
            if (control.rejectClear) return route.fulfill({ status: 409, json: { error: { code: 'conflict', message: '记录清理被拒绝' } } });
            const manual = [...runs.values()].filter(r => r.source === 'manual_test');
            const finished = manual.filter(r => r.status === 'completed');
            for (const record of finished) runs.delete(record.id);
            receipt = { deleted: finished.length, kept: manual.length - finished.length };
            if (control.afterClear) runs.set('new-terminal', run('new-terminal', ''));
          } else if (p === '/runs') runs.delete(body.id);
          else throw Error('Unexpected fixture write ' + p);
          if (control.failReceipt) { control.failReceipt = false; return route.abort('connectionfailed'); }
          if (control.badReceipt) return route.fulfill({ json: { deleted: 2, kept: 2, archived: 'invalid count' } });
          return route.fulfill({ json: receipt });
        });
        await page.goto(url); await page.selectOption('#theme-toggle', theme);
        report.ui_version=JSON.parse(await page.locator('#build-info').textContent()).ui_version;
        await page.click('#connection-open'); await page.fill('#management-key', management_key);
        await page.click('#connection-form button[type=submit]'); await page.waitForSelector('body.connected');
        await page.click('[data-page=operations]');
        await page.click('[data-rule-select=rule-a]');
        const confirm = async () => {
          await page.click('#action-confirm');
          await page.waitForFunction(() => !document.querySelector('#action-confirm').hasAttribute('aria-busy'));
        };
        const snapshot = () => page.evaluate(() => ({
          dialog: document.querySelector('#action-dialog').open,
          error: document.querySelector('#action-error:not(.hidden)')?.textContent || '',
          notice: document.querySelector('#notice:not(.hidden)')?.textContent || '',
          visibleNotice: document.querySelector('#notice').getBoundingClientRect().bottom > 0 && document.querySelector('#notice').getBoundingClientRect().top < innerHeight && document.querySelector('#notice').getClientRects().length > 0,
          title: document.querySelector('#action-title').textContent,
          scope: document.querySelector('#ops-scope-label').textContent,
          activity: [...document.querySelectorAll('#operations-history [data-run]')].map(n => n.dataset.run),
          history: [...document.querySelectorAll('#history-list [data-run]')].map(n => n.dataset.run),
          focus: document.activeElement?.id || '',
          selected: document.querySelector('#run-frozen-data .run-identifier')?.textContent || '',
          overflow: document.documentElement.scrollWidth > innerWidth,
        }));
        const screenshot = suffix => out && page.screenshot({ path: path.join(out, name + '-' + suffix + '.png'), animations: 'disabled' });
        const result = await interact({ page, control, confirm, snapshot, screenshot, reads, writes, receiptStorage });
        assert.deepEqual(errors, []); assert.deepEqual(unexpectedWrites,[]);
        if (!report.served) {
          report.served=Object.fromEntries(await Promise.all([...served].map(async ([file,hash])=>[file,await hash])));
          assert.deepEqual(report.served,report.source);
        }
        report.scenarios[name] = { ...result, writes, remaining: [...runs.keys()], ...(archivedRuns.size ? { archived: [...archivedRuns] } : {}) };
      } finally { await context.close(); }
    }
    await scenario('clear', async ({ page, control, confirm, snapshot, screenshot }) => {
      await page.click('#ops-history-clear');
      const description = await page.locator('#action-description').innerText();
      await screenshot('confirm');
      control.failReceipt = true;
      control.afterRuleClear = true;
      await confirm(); const lost = await snapshot();
      await screenshot('receipt');
      if (lost.dialog) await confirm();
      return { description, lost, end: await snapshot() };
    });
    await scenario('clear_only_confirmed_pauses', async ({ page, confirm, snapshot, screenshot, writes }) => {
      await page.locator('#operations-history [data-run=handled-6]').waitFor();
      assert.equal((await snapshot()).activity.length, 6);
      await page.click('[data-rule-batch=finished-batch]');
      await page.click('#ops-history-clear');
      const description = await page.locator('#action-description').innerText();
      assert.match(description, /所有规则.+已确认的暂停继续生效/);
      await confirm();
      const cleared = await snapshot();
      assert.deepEqual(cleared.activity, []);
      assert.match(cleared.notice, /已清空 6 条运行记录。.*6 条的处置依据已留档/);
      assert.doesNotMatch(cleared.notice, /进行|可清空/);
      assert.equal(cleared.scope, '全部规则');
      assert(await page.locator('#ops-history-clear').isDisabled());
      await screenshot('cleared');
      await page.click('[data-rule-select=rule-a]');
      await page.locator('#ops-rule-context [data-rule-handling] summary').first().click();
      await page.locator('#ops-rule-context .quality-handling-records > div').filter({ hasText: 'fixture-handled-6@example.test' }).waitFor();
      assert.equal(await page.locator('#ops-rule-context .quality-handling-records > div').count(), 6);
      assert.equal(await page.locator('#ops-rule-context [data-rule-batch], #ops-rule-context [data-run]').count(), 0);
      await page.locator('#ops-record-count').filter({ hasText: '共 0 条' }).waitFor();
      const [refresh] = await Promise.all([page.waitForResponse(response => response.url().includes('/runs?') && response.url().includes('source=scheduled_probe')), page.click('#refresh')]);
      await refresh.finished();
      await page.locator('#refresh:not([aria-busy])').waitFor();
      const refreshed = await snapshot();
      assert.deepEqual(refreshed.activity, []);
      assert.deepEqual(refreshed.history.sort(), ['manual', 'other-manual']);
      assert.deepEqual(writes, [{ p: '/rules/run-history', body: null }]);
      return { description, cleared, refreshed };
    }, { confirmedHandling: true, onlyHandledRuns: true });
    await scenario('clear_with_separate_retention_counts', async ({ page, confirm, snapshot, writes }) => {
      await page.click('#ops-history-clear');
      await confirm();
      const stopped = await snapshot();
      assert.deepEqual(stopped.activity.sort(), ['in-progress', 'protected']);
      assert.match(stopped.notice, /已清空 2 条运行记录。.*1 条任务仍在进行。.*保留 1 条待核对处置或历史独立暂停所需的记录。/);
      assert.doesNotMatch(stopped.notice, /处置依据已留档/);
      assert.deepEqual(writes, [{ p: '/rules/run-history', body: null }]);
      return { stopped };
    });
    await scenario('rule', async ({ page, control, confirm, snapshot, screenshot }) => {
      await page.click('[data-rule-delete=rule-a]'); control.metricsFail = true;
      await confirm(); const acknowledged = await snapshot();
      await screenshot('receipt');
      if (acknowledged.dialog) await confirm();
      return { acknowledged, end: await snapshot() };
    }, { width: 390, height: 844, theme: 'dark' });
    async function openOwnedDeletion(page) {
      const card = page.locator('[data-key="rule:rule-a"]');
      assert.equal(await card.locator('.quality-handling-records').count(),0);
      const handling = page.locator('#ops-rule-context [data-rule-handling]');
      if (!await handling.evaluate(node=>node.open)) await handling.locator('summary').first().click();
      await handling.locator('.quality-handling-records').getByText(/401 认证失败/).first().waitFor();
      await page.locator('#operations-history [data-run=ordinary]').first().waitFor();
      await page.locator('#operations-history [data-run=first]').first().waitFor();
      await card.locator('[data-rule-delete]').click();
      const description = await page.locator('#action-description').innerText();
      assert.match(description,/周一观察/); assert.match(description,/删除/);
      assert.doesNotMatch(description,/保留的凭证处置|处置记录已保留|核对人工|人工处理|待核对/);
      return description;
    }
    async function verifyDeletedAfterRefresh(page, expectedRules, expectedTargets) {
      const get = suffix => page.waitForResponse(response =>
        new URL(response.url()).pathname===prefix+suffix && response.request().method()==='GET');
      const [rulesResponse,targetsResponse] = await Promise.all([get('/rules'),get('/targets'),page.click('#refresh')]);
      assert.equal(rulesResponse.status(),200); assert.equal(targetsResponse.status(),200);
      const rules = (await rulesResponse.json()).items, targets = (await targetsResponse.json()).items;
      assert.deepEqual(rules.map(r=>r.id),expectedRules); assert(rules.every(r=>!r.handling?.some(h=>h.action_id==='confirmed-401')));
      assert.deepEqual(targets.map(t=>t.auth_id),expectedTargets);
      assert(targets.every(t=>t.manual_disabled && t.runtime_status==='disabled' && !t.error_policy_block));
      await page.locator('[data-rule-select=rule-a]').waitFor({state:'detached'});
      await page.locator('#operations-history [data-run=ordinary],#operations-history [data-run=first]').waitFor({state:'detached'});
      assert.equal(await page.locator('#retained-handling,#retained-handling-list,[data-handling-ack="confirmed-401"]').count(),0);
      assert.doesNotMatch(await page.locator('#page-operations').innerText(),/保留的凭证处置|处置记录已保留|核对人工处理/);
      return targets.map(t=>({auth_id:t.auth_id,runtime_status:t.runtime_status,plugin_error_policy_block:!!t.error_policy_block}));
    }
    const deleteA = [{p:'/rules',body:{id:a,revision:3}}];
    await scenario('delete_rule_with_handling', async ({page,confirm,snapshot,screenshot,writes}) => {
      const description = await openOwnedDeletion(page); await page.click('#action-cancel');
      assert.deepEqual(writes,[]); await openOwnedDeletion(page); await screenshot('confirm'); await confirm();
      const deleted = await snapshot();
      assert(!deleted.dialog); assert.match(deleted.notice,/已删除/); assert.equal(deleted.scope,'全部规则');
      const targets = await verifyDeletedAfterRefresh(page,[b],['fixture-quality-primary']);
      assert(await page.locator('[data-rule-select=rule-b]').isVisible());
      const end = await snapshot();
      assert.deepEqual(end.activity.sort(),['in-progress','protected','second']);
      assert.deepEqual(end.history.sort(),['manual','other-manual']);
      assert.deepEqual(writes,deleteA); await screenshot('refreshed');
      return {description,deleted,refreshed_targets:targets,end,cpa_writes:0};
    }, {confirmedHandling:true});
    await scenario('delete_rule_with_handling_read_failure', async ({page,control,confirm,snapshot,screenshot,writes}) => {
      await openOwnedDeletion(page); control.failRuleReadAfterDelete=true; await confirm();
      const failed = await snapshot();
      assert(!failed.dialog); assert.match(failed.notice,/已删除/); assert.match(failed.notice,/读取失败|刷新核对/);
      assert.doesNotMatch(failed.notice,/核对人工|处置.*保留/); await screenshot('read-failed');
      control.rulesReadFailed=false;
      const targets = await verifyDeletedAfterRefresh(page,[b],['fixture-quality-primary']);
      await page.waitForFunction(()=>!document.querySelector('#notice:not(.hidden)')?.textContent.includes('列表读取失败'));
      const end = await snapshot();
      assert.deepEqual(end.activity.sort(),['in-progress','protected','second']); assert.deepEqual(writes,deleteA);
      await page.locator('#rules-list').scrollIntoViewIfNeeded();
      await screenshot('refreshed');
      return {failed,refreshed_targets:targets,end,cpa_writes:0};
    }, {confirmedHandling:true,width:390,height:844,theme:'dark'});
    await scenario('delete_last_rule_with_group_handling', async ({page,confirm,screenshot,snapshot,writes}) => {
      await openOwnedDeletion(page); await confirm();
      const targets = await verifyDeletedAfterRefresh(page,[],['route-one','route-two']);
      const end = await snapshot();
      assert.equal(await page.locator('[data-rule-select]').count(),0); assert.deepEqual(end.activity,[]); assert.deepEqual(writes,deleteA);
      await page.locator('#rules-list').scrollIntoViewIfNeeded();
      await screenshot('refreshed');
      return {refreshed_targets:targets,end,cpa_writes:0};
    }, {confirmedHandling:true,groupHandling:true,width:390,height:844});
    await scenario('manual', async ({ page, control, confirm, snapshot, screenshot }) => {
      await page.click('[data-page=manual]'); await page.click('[data-manual-view=history]');
      await page.click('[data-run=manual]'); await page.waitForSelector('#run-frozen-data .run-identifier', { state: 'attached' });
      await page.click('[data-manual-view=history]'); await page.click('[data-delete-run=manual]');
      control.failReceipt = true;
      await confirm(); const lost = await snapshot();
      await screenshot('receipt');
      if (lost.dialog) await confirm();
      return { lost, end: await snapshot() };
    }, { width: 1440, height: 650, theme: 'white' });
    async function openManualHistory(page) {
      await page.click('[data-page=manual]');
      const pane = page.locator('[data-manual-pane=history]');
      if (await pane.isVisible()) await pane.click();
      else await page.click('[data-manual-view=history]');
      await page.locator('#history-clear-all:enabled').waitFor();
    }
    await scenario('manual_bulk_pages', async ({ page, confirm, writes, snapshot, screenshot, receiptStorage }) => {
      await openManualHistory(page);
      await page.click('[data-run=manual]');
      await page.locator('#run-frozen-data .run-identifier').filter({ hasText: '任务 manual' }).waitFor({ state: 'attached' });
      await openManualHistory(page);
      await page.click('#history-next');
      await page.locator('#history-page').filter({ hasText: '第 2 页' }).waitFor();
      const beforeCancel = (await snapshot()).history;
      await page.click('#history-clear-all');
      assert.match(await page.locator('#action-description').innerText(), /进行中|运行中|仍在运行/);
      await screenshot('confirm');
      await page.click('#action-cancel');
      assert.deepEqual(writes, []);
      assert.deepEqual((await snapshot()).history, beforeCancel);
      await page.click('#history-clear-all');
      await confirm();
      const end = await snapshot();
      assert.deepEqual(end.history, ['active-manual']);
      assert(!end.selected);
      assert(await page.locator('[data-delete-run=active-manual]').isDisabled());
      assert(await page.locator('#history-pagination').isHidden());
      assert.equal(await page.evaluate(storage => sessionStorage.getItem(storage), receiptStorage), 'synthetic-active-receipt');
      assert.equal(await page.locator('#run-submit').getAttribute('aria-busy'), 'true');
      assert.match(await page.locator('#manual-config-summary').textContent(), /测试中/);
      assert.match(end.notice, /\b13\b/);
      assert.match(end.notice, /\b1\b/);
      assert.deepEqual(writes, [{ p: '/runs/history', body: null }]);
      await screenshot('retained');
      return { end };
    }, { width: 320, height: 800, theme: 'dark', bulkHistory: true });
    await scenario('manual_bulk_refused_or_lost', async ({ page, control, confirm, snapshot, writes }) => {
      await openManualHistory(page);
      await page.click('#history-clear-all');
      control.rejectClear = true;
      await confirm();
      const rejected = await snapshot();
      assert(rejected.dialog && rejected.error && rejected.history.includes('manual'));
      await page.click('#action-cancel');
      control.rejectClear = false;
      control.failReceipt = true;
      control.afterClear = true;
      await page.click('#history-clear-all');
      await confirm();
      const end = await snapshot();
      assert.match(end.notice, /回执|未确认/);
      assert.deepEqual(end.history, ['new-terminal']);
      assert.equal(writes.length, 2);
      return { rejected, end };
    });
    await scenario('manual_bulk_empty', async ({ page, confirm, snapshot }) => {
      await openManualHistory(page);
      await page.click('#history-clear-all'); await confirm();
      assert(await page.locator('#history-clear-all').isDisabled());
      assert(await page.locator('#history-list .empty-line').isVisible());
      return { end: await snapshot() };
    });
    await scenario('invalid_clear_receipt', async ({ page, control, confirm, snapshot }) => {
      await page.click('#ops-history-clear'); control.badReceipt = true;
      await confirm(); return { end: await snapshot() };
    }, { width: 320, height: 800 });
    await scenario('rule_conflict', async ({ page, control, confirm, snapshot }) => {
      await page.click('[data-rule-delete=rule-a]'); control.conflict = true;
      await confirm(); const rejected = await snapshot();
      await page.click('#action-cancel'); return { rejected, end: await snapshot() };
    });
    await scenario('other_selection', async ({ page, control, confirm, snapshot }) => {
      await page.click('[data-page=manual]'); await page.click('[data-manual-view=history]');
      control.holdID = 'other-manual'; control.entered = gate(); control.resume = gate();
      const late = page.waitForResponse(response => response.url().endsWith('/runs/detail?id=other-manual'));
      try {
        await page.click('[data-run=other-manual]'); await control.entered.promise;
        await page.click('[data-delete-run=manual]'); await confirm();
      } finally { control.resume.release(); }
      await (await late).finished();
      await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
      return { end: await snapshot() };
    });
    if (out) await fs.writeFile(path.join(out, 'cleanup-receipts.json'), JSON.stringify(report, null, 2) + '\n');
    if (observed) return;
    const { clear, rule, manual, invalid_clear_receipt: invalid, rule_conflict: conflict } = report.scenarios;
    assert.match(clear.description, /所有规则/);
    assert.equal(clear.writes.length, 1); assert(!clear.lost.dialog && !clear.lost.overflow);
    assert.deepEqual(clear.lost.activity.sort(), ['finished-later', 'in-progress', 'protected']);
    assert.deepEqual(clear.remaining.sort(), ['finished-later', 'in-progress', 'manual', 'other-manual', 'protected']);
    assert.match(clear.lost.notice, /回执|未确认/);
    assert.equal(rule.writes.length, 1); assert(!rule.acknowledged.dialog && !rule.acknowledged.overflow);
    assert.match(rule.acknowledged.notice, /已删除/); assert.equal(rule.acknowledged.scope, '全部规则'); assert(rule.acknowledged.visibleNotice);
    assert.deepEqual(manual.writes, [{ p: '/runs', body: { id: 'manual' } }]);
    assert(!manual.lost.dialog && !manual.lost.selected && !manual.lost.overflow); assert.deepEqual(manual.lost.history, ['other-manual']);
    assert.match(manual.lost.notice, /回执|未确认|查无|查不到/);
    assert(!invalid.end.dialog && !invalid.end.notice.includes('undefined') && !invalid.end.overflow); assert.match(invalid.end.notice, /回执|未确认/);
    assert(conflict.rejected.dialog && /conflict/.test(conflict.rejected.error) && conflict.remaining.includes('first'));
    assert.equal(report.scenarios.other_selection.end.selected, '任务 other-manual');
    const scheduled = ['first', 'second', 'in-progress', 'protected'];
    assert.deepEqual(report.scenarios.manual_bulk_pages.remaining.sort(), [...scheduled, 'active-manual'].sort());
    assert.deepEqual(report.scenarios.manual_bulk_refused_or_lost.remaining.sort(), [...scheduled, 'new-terminal'].sort());
    assert.deepEqual(report.scenarios.manual_bulk_empty.remaining.sort(), [...scheduled].sort());
    console.log('Manual and rule history cleanup passed, including six archived pauses, separate retention counts, and uncertain receipts.');
  } finally { await browser?.close(); fixture.kill(); await exited; }
}
main().catch(error => { console.error(error); process.exitCode = 1; });
