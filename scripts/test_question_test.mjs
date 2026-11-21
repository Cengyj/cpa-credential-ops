import assert from 'node:assert/strict';
import { auditLabel, credentialBlock, frozenRunIdentity, previewLimit, previewSource, providerRouteReady, questionTestSelection, reasonLabel, renderHistoryResult, renderQualityDetail, renderQualityRuns, renderRuleActivityContext, renderRuleRows, renderRunEvidence, renderSampleEvidence, ruleScopeDescription, statusTag, verdictTag } from '../plugin/ui/view.js';

const question = { id: 'logic-candy', title: '糖果逻辑测试', kind: 'logic', answer: '21', prompt: 'Only this exact prompt owns the reference answer.', version: 'v1' };
assert.deepEqual(questionTestSelection(question, question.prompt), { question_id: 'logic-candy' });
for (const prompt of [`${question.prompt} `, question.prompt.replace('exact', 'changed'), 'A different question']) {
  assert.deepEqual(questionTestSelection(question, prompt), { question_id: 'custom', prompt });
}
assert.deepEqual(questionTestSelection(undefined, 'My question'), { question_id: 'custom', prompt: 'My question' });

const fixedRule = { target_scope: 'single', object_types: ['oauth','provider'], target_object_type: 'provider', target_auth_index: 'saved-index',
  auth_id: 'saved-id', provider: 'codex' };
const fixedRoute = { auth_id: 'saved-id', provider: 'codex', auth_index: 'saved-index', object_type: 'provider', is_provider: true, provider_name: '原供应商' };
const replacingFile = { ...fixedRoute, object_type: 'oauth', is_provider: false, email: 'replacement@example.test' };
assert.match(ruleScopeDescription(fixedRule, [fixedRoute]), /原供应商/);
for (const directory of [[replacingFile], [], [{ ...fixedRoute, auth_index: 'new-index', provider_name: '替换名称' }]]) {
  const label = ruleScopeDescription(fixedRule, directory);
  assert.match(label, /供应商路由.*saved-id.*saved-index.*原绑定待核实/);
  assert.doesNotMatch(label, /replacement@example|替换名称/);
}
const oldUnclassified = { ...fixedRule, target_object_type: '' };
assert.match(ruleScopeDescription(oldUnclassified, []), /对象.*saved-id.*原绑定待核实/);

const waitingTimer = { id: 'fixture-rule', name: 'fixture', enabled: true, scheduling_enabled: true, timer_active: true,
  timer_wait_reason: '部分对象等待管理目录。', next_run_local: '2026-11-22T08:00:00+08:00', pending_schedule: { due: '2026-11-21T00:00:00Z' } };
const waitingRows = renderRuleRows([waitingTimer], [], { connected: true, targetsFresh: true });
assert.match(waitingRows, /部分对象等待管理目录/);
assert.match(waitingRows, /aria-checked="true"/);
const waitingDetails = renderRuleActivityContext(waitingTimer, id => id, {});
assert.match(waitingDetails, /待补检周期.*2026-11-21 00:00:00/);
const loginHold = renderRuleRows([{ ...waitingTimer, status: 'credential_write_unknown', timer_active: false }], [], { connected: true, targetsFresh: true });
assert.match(loginHold, /凭证写入待核对/);
assert.match(loginHold, /data-rule-run="fixture-rule" disabled/);

const run = {
  source: 'manual_test', status: 'completed', action_status: 'not_applicable', created_at: '2026-11-20T13:00:00Z',
  config: { models: ['example-model'], samples: 1, question, judge: { mode: 'manual' } },
  samples: [{ model: 'example-model', index: 0, execution_status: 'completed', quality_verdict: 'unknown', reason: 'manual assessment required', output: '<svg onload="alert(1)"></svg>', usage_known: false }],
};
const simple = renderRunEvidence(run);
const recordedPolicy = { code: 'error_policy_disabled', reason: 'CPA 注册目录缺少所选模型', model: 'old-model' };
const external = { auth_id: 'fake-active', credential_handling: 'disabled', error_policy_block: recordedPolicy, runtime_status: 'active', effective_available: true, models: ['example-model'] };
const manual = { manual: true };
const config = { auth_id: external.auth_id, models: ['example-model'] };
assert.equal(credentialBlock(config, [external], manual), '');
assert.match(credentialBlock({ ...config, models: ['old-model'] }, [external], manual), /当前目录没有所选模型 old-model/);
assert.match(credentialBlock(config, [{ ...external, retained_pause: true }], manual), /质量暂停/);
assert.match(credentialBlock(config, [{ ...external, runtime_status: 'disabled' }], manual), /仍在 CPA 停用/);
assert.match(credentialBlock(config, [{ ...external, effective_available: false }], manual), /当前不可用/);
assert.equal(credentialBlock(config, [{ ...external, error_policy_block: null }], manual), '');
assert.equal(credentialBlock({ control_auth_id: external.auth_id }, [external], manual), '');
assert.equal(credentialBlock({ judge: { mode: 'llm', auth_id: external.auth_id } }, [external], manual), '');
assert.equal(credentialBlock(config, [external]), '');
const registered = { ...external, is_provider: true, provider: 'openai', auth_index: 'provider-route', runtime_status: 'registered' };
assert(providerRouteReady(registered));
assert.equal(credentialBlock(config, [registered], manual), '');
assert.equal(credentialBlock({ control_auth_id: registered.auth_id }, [registered]), '');
assert.equal(providerRouteReady(undefined), false);
for (const patch of [{ manual_disabled: true }, { runtime_status: 'disabled' }, { effective_available: false }, { retained_pause: true }]) {
  assert.equal(providerRouteReady({ ...registered, ...patch }), false);
  assert.notEqual(credentialBlock(config, [{ ...registered, ...patch }], manual), '');
}
assert.notEqual(credentialBlock(config, [{ ...registered, credential_handling: 'attention' }], manual), '');
assert.equal(credentialBlock(config, [{ ...external, handling_state_reason: 'CPA 管理连接不可用' }], manual), 'CPA 管理连接不可用');
const disposition = { action_id: 'fake-action', auth_id: 'fake-current', state: 'disabled', rule_id: 'answer-rule', answer_owner_rule_id: 'answer-rule',
  error_source: { rule_id: 'error-rule', condition: 'unauthorized', model: 'example-model' } };
const handlingView = record => renderRuleActivityContext({ id: 'answer-rule', handling: [record] }, id => id, { handlingOpen: true, canAcknowledge: true });
assert.match(handlingView(disposition), /错误来源规则 error-rule.*答错恢复归属 answer-rule/);
assert.match(handlingView(disposition), /触发模型 example-model/);
assert.match(handlingView(disposition), /退出答题和恢复复检/);
assert.doesNotMatch(handlingView(disposition), /data-handling-ack/);
assert.match(handlingView({ ...disposition, state: 'attention' }), /data-handling-ack/);
assert.match(handlingView({ ...disposition, state: 'externally_enabled' }), /本次处置关联已结束/);
assert.doesNotMatch(handlingView({ ...disposition, answer_owner_rule_id: '', rule_id: 'error-rule' }), /答错恢复归属/);
assert.match(handlingView({ ...disposition, error_source: {rule_id:'error-rule',condition:'model_unsupported',model:'missing-model',catalog_missing:true} }), /CPA 注册目录缺少 missing-model/);
assert.doesNotMatch(handlingView({ ...disposition, error_source: {rule_id:'<script>rule</script>',condition:'unauthorized',model:'<img src=x>'} }), /<script>|<img src=x>/);
const blockedHistory = { ...run, status: 'blocked', samples: [], error: 'blocked: credential already disabled by error policy' };
const blockedSummary = renderRunEvidence(blockedHistory).metaHTML;
assert.match(blockedSummary, /当前凭证状态尚未确认，无法开始智商测试。请刷新后重试。/);
assert.doesNotMatch(blockedSummary, /blocked|credential already disabled|错误策略|质量运维|处置/);
assert.equal(blockedHistory.error, 'blocked: credential already disabled by error policy');
for (const [answer_match, individual, overall] of [['correct', '答案正确', '正确'], ['incorrect', '答案错误', '错误'], ['unknown', '未判定', '未判定']]) {
  const one = renderRunEvidence({ ...run, samples: [{ ...run.samples[0], output: '29', answer_match }] });
  assert(one.samplesHTML.includes(`data-verdict="${answer_match}"`));
  assert(one.samplesHTML.includes('>' + individual + '<'));
  assert(one.conditionsHTML.includes('>' + overall + '<'));
  assert.doesNotMatch(one.metaHTML, />已完成<|>正确<|>错误<|>未判定</);
  assert.equal(one.samplesHTML.includes('<dt>标准答案</dt><dd>21</dd>'), answer_match === 'incorrect');
}
const wrongSample = { ...run.samples[0], output: '29', answer_match: 'incorrect' };
for (const sample of [{ ...wrongSample, review: 'unknown' }, { ...wrongSample, execution_status: 'failed' }]) {
  assert.doesNotMatch(renderRunEvidence({ ...run, samples: [sample] }).samplesHTML, /class="answer-reference"/);
}
const customConfig = { ...run.config, question: { ...question, kind: 'custom' } };
assert.doesNotMatch(renderRunEvidence({ ...run, config: customConfig, samples: [wrongSample] }).samplesHTML, /class="answer-reference"/);
assert.equal(simple.completed, 1);
assert.equal(simple.planned, 1);
assert.match(simple.metaHTML, /<h3[^>]*>糖果逻辑测试<\/h3>/);
assert.match(simple.metaHTML, />example-model</);
assert.match(simple.metaHTML, /<time datetime="2026-11-20T13:00:00Z">/);
assert.match(simple.samplesHTML, />模型回答</);
assert.doesNotMatch(simple.samplesHTML, /class="sample-number"/);
assert.doesNotMatch(renderRunEvidence({ ...run, created_at: undefined }).metaHTML, /<time/);
assert.doesNotMatch(simple.metaHTML, /无法判断|动作：/);
assert.doesNotMatch(simple.samplesHTML, /sample-state-label|需要人工评审|data-review-sample/);
assert.match(simple.samplesHTML, /&lt;svg onload=&quot;alert\(1\)&quot;&gt;/);
assert.doesNotMatch(simple.samplesHTML, /<svg onload/);
assert.doesNotMatch(simple.samplesHTML, /data-inline-preview|data-preview-sample/, 'An empty SVG tag is not a drawing');

const visualOutput = '<!doctype html><html><body><svg viewBox="0 0 10 10"><circle r="4"/></svg>';
const visualRun = { ...run, config: { ...run.config, question_id: 'visual-pelican-html', question: { id: 'visual-pelican-html', kind: 'visual' } },
  answer_preview: { model: 'example-model', index: 0, kind: 'html' } };
const visualIssues = [
  ['response limit exceeded', '响应数据超过采集上限，本次接收已停止。'],
  ['output limit exceeded', '回答正文超过输出上限，本次接收已停止。'],
  ['response answer limit exceeded', '回答正文超过输出上限，本次接收已停止。'],
  ['response frame limit exceeded', '单个响应帧超过采集上限，本次接收已停止。'],
  ['response transport limit exceeded', '流式响应累计数据超过传输上限，本次接收已停止。'],
  ['context deadline exceeded', '请求超时，本次接收已停止。'],
  ['host.model.stream_read: context deadline exceeded', '请求超时，本次接收已停止。'],
  ['read tcp 127.0.0.1:1234: i/o timeout', '请求超时，本次接收已停止。'],
];
for (const [error, explanation] of visualIssues) {
  const sample = { ...run.samples[0], execution_status: 'failed', error, output: visualOutput };
  const record = { ...visualRun, samples: [sample] }, original = JSON.stringify(record);
  const rendered = renderRunEvidence(record), evidence = renderSampleEvidence(sample, true);
  assert(rendered.samplesHTML.includes(explanation), error);
  assert(evidence.includes(explanation), error);
  assert.match(rendered.samplesHTML, /放大部分作品/);
  assert.match(rendered.samplesHTML, /data-inline-preview="0"/);
  assert.match(rendered.samplesHTML, /当前为已接收的部分内容，作品可能不完整/);
  assert.match(renderHistoryResult(record), /HTML 部分作品/);
  assert.doesNotMatch(renderHistoryResult(record), /history-score/);
  if (error.includes('limit exceeded')) assert.doesNotMatch(rendered.samplesHTML, /超时|token 上限/);
  assert.equal(JSON.stringify(record), original, 'Error explanations must preserve stored evidence');
}
for (const [reason, explanation, forbidden] of [
  ['response output token limit reached', '回答达到输出 token 上限，未能完整生成。', /超时/],
  ['missing or truncated completion', '回答未完整结束或已截断，不作为答题质量失败证据', /token 上限|超时/],
]) {
  const sample = { ...run.samples[0], execution_status: 'incomplete', reason, output: visualOutput };
  const rendered = renderRunEvidence({ ...visualRun, samples: [sample] });
  assert(rendered.samplesHTML.includes(explanation));
  assert.match(rendered.samplesHTML, /回答不完整/);
  assert.match(rendered.samplesHTML, /放大部分作品/);
  assert.match(rendered.samplesHTML, /data-inline-preview="0"/);
  assert.doesNotMatch(rendered.samplesHTML, forbidden);
}
for (const execution_status of ['empty', 'failed', 'running']) {
  const rendered = renderRunEvidence({ ...visualRun, samples: [{ ...run.samples[0], execution_status, output: '' }] });
  assert.doesNotMatch(rendered.samplesHTML, /data-preview-sample/);
  assert.match(rendered.samplesHTML, execution_status === 'running' ? /正在等待模型返回内容/ : /本次没有返回回答正文/);
}
const completedVisual = { ...visualRun, samples: [{ ...run.samples[0], output: visualOutput }] };
assert.match(renderRunEvidence(completedVisual).samplesHTML, /未判定/);
assert.doesNotMatch(renderRunEvidence(completedVisual).samplesHTML, /预览部分|当前为已接收/);
assert.match(renderHistoryResult(completedVisual), /HTML 作品/);
assert.equal(reasonLabel('Possible timeout reported by the model'), 'Possible timeout reported by the model');

// Drawing detection is independent from question type and keeps the source untouched.
for (const value of ['', 'A plain answer.', 'Explain <svg> and <html> tags.', '&lt;svg&gt;', '<svg></svg>', '```javascript\nconst markup = "<svg viewBox=\"0 0 10 10\"></svg>";\n```']) {
  assert.equal(previewSource(value), null, value);
}
const standaloneSVG = '<svg viewBox="0 0 10 10"><circle r="4"/></svg>';
for (const value of [standaloneSVG, '```svg\n' + standaloneSVG + '\n```', '<?xml version="1.0"?>\n' + standaloneSVG]) {
  assert.deepEqual(previewSource(value), { kind: 'svg', source: standaloneSVG });
}
assert.deepEqual(previewSource('<style>circle{fill:red}</style>' + standaloneSVG), { kind: 'html', source: '<style>circle{fill:red}</style>' + standaloneSVG });
assert.equal(previewSource('```html\n' + visualOutput)?.source, visualOutput, 'A truncated fenced HTML answer remains identifiable');
const completedMarkup = renderRunEvidence(completedVisual).samplesHTML;
assert.match(completedMarkup, /<iframe[^>]*data-inline-preview="0"[^>]*data-part="preview"[^>]*sandbox=""[^>]*referrerpolicy="no-referrer"/);
assert.doesNotMatch(completedMarkup, /<iframe[^>]*(?:inert|srcdoc|allow-scripts|allow-same-origin)/);
assert.match(completedMarkup, /<details[^>]*data-source-sample="0"[^>]*><summary>查看原文<\/summary>/);
assert.doesNotMatch(completedMarkup, /<details[^>]*data-source-sample="0"[^>]*\bopen/);
assert.match(completedMarkup, /放大作品/);
assert.doesNotMatch(completedMarkup, /data-expand-sample/);
const tooLarge = '<html><body>' + 'x'.repeat(previewLimit) + '</body></html>';
const tooLargeMarkup = renderRunEvidence({ ...completedVisual, samples: [{ ...completedVisual.samples[0], output: tooLarge }] }).samplesHTML;
assert.doesNotMatch(tooLargeMarkup, /data-inline-preview|data-preview-sample/);
assert.match(tooLargeMarkup, /1 MiB/);
assert(tooLargeMarkup.includes('&lt;html&gt;&lt;body&gt;' + 'x'.repeat(previewLimit)));
const mixedMarkup = renderRunEvidence({ ...completedVisual, config: { ...completedVisual.config, samples: 3 }, samples: [
  completedVisual.samples[0], { ...completedVisual.samples[0], index: 1, output: 'Plain second sample' },
  { ...completedVisual.samples[0], index: 2, output: standaloneSVG, execution_status: 'incomplete' },
] }).samplesHTML;
assert.deepEqual([...mixedMarkup.matchAll(/data-inline-preview="(\d+)"/g)].map(match => match[1]), ['0', '2']);
assert.deepEqual([...mixedMarkup.matchAll(/data-source-sample="(\d+)"/g)].map(match => match[1]), ['0', '2']);
assert.match(mixedMarkup, /Plain second sample/);

const scheduled = renderRunEvidence({ ...run, source: 'scheduled_probe' });
assert.match(scheduled.metaHTML, /无法判断/);
assert.match(scheduled.samplesHTML, /sample-state-label/);
assert.match(scheduled.samplesHTML, /data-review-sample/);
const partial = renderRunEvidence({...run, source:'scheduled_probe', action_status:'insufficient_evidence',
  config:{...run.config, samples:2}, samples:[{...run.samples[0], comparable:false, quality_verdict:'correct'}]});
assert.match(partial.metaHTML, /正确 1/);
assert.match(partial.metaHTML, /1 \/ 2 样本已结束/);
assert.match(partial.metaHTML, /整轮判定依据不足，未处理凭证/);
assert.doesNotMatch(partial.samplesHTML, /证据不足/);

// A provider flag identifies the run, not the rule's permission to act or restore.
const managedProviderRun = { ...run, id: 'provider-round', source: 'scheduled_probe', action_status: 'observe_only',
  config: { ...run.config, auth_id: 'fake-provider', provider: 'codex', provider_name: 'Synthetic provider', subject_auth_index: 'fake-route',
    provider_observation: true, managed_probe: { provider_mechanism: 'excluded_models' }, rule_id: 'provider-rule', rule_revision: 7,
    judge: { mode: 'deterministic' } },
  samples: [{ ...run.samples[0], quality_verdict: 'correct', answer_match: 'correct', output: '21', evidence: { path: 'provider_config_probe' } }],
};

// Stored verdicts survive export, but an unsuccessful execution has no answer verdict.
for (const [execution_status, error, outcome, statusLabel, upstream_condition] of [
  ['completed', 'upstream HTTP 502', 'failed', '执行失败'],
  ['completed', '', 'failed', '执行失败', 'unauthorized'],
  ['refused', '', 'refused', '模型拒答'],
  ['skipped', 'route unavailable before request', 'skipped', '未执行'],
]) {
  for (const quality_verdict of ['correct', 'incorrect']) {
    const sample = Object.freeze({ ...managedProviderRun.samples[0], execution_status, error, quality_verdict, upstream_condition });
    const record = { ...managedProviderRun, samples: [sample] };
    assert.match(renderQualityRuns([record], []), new RegExp('data-outcome="' + outcome + '">' + statusLabel + ' 1'));
    const detail = renderQualityDetail(record, []);
    assert.match(detail, new RegExp('class="quality-sample-status" data-outcome="' + outcome + '"'));
    assert(detail.includes(statusLabel + ' 1'));
    if (upstream_condition) assert.match(detail, /上游 HTTP 401/);
    const evidence = renderSampleEvidence(sample);
    assert(evidence.includes('执行</span><span class="tag '));
    assert(evidence.includes('>' + statusLabel + '</span>'));
    assert.match(evidence, /答案<\/span><span class="tag neutral">无法判断</);
    assert(evidence.includes('&quot;quality_verdict&quot;: &quot;' + quality_verdict + '&quot;'));
    const manualEvidence = renderSampleEvidence(sample, true);
    assert.doesNotMatch(manualEvidence, /按本次题目的标准答案核对/);
    const manualStatus = manualEvidence.split('</div>')[0];
    assert(manualStatus.includes('>' + statusLabel + '</span>'));
    assert.doesNotMatch(manualStatus, /已完成|答案正确|答案错误/);
  }
}
// A run-level issue invalidates the whole-round score, while a sound sample remains judged.
for (const issue of [{ error: 'round read failed' }, { block_reason: 'identity_conflict' },
  { cancel_requested: true }, { upstream_condition: 'unauthorized' }]) {
  const value = { ...managedProviderRun, ...issue };
  assert.doesNotMatch(renderQualityRuns([value], []), /class="history-score"/);
  assert.match(renderQualityRuns([value], []), /data-outcome="correct">答对 1/);
  assert.match(renderQualityDetail(value, []), /class="quality-sample-status" data-outcome="correct"/);
  const manual = { ...run, ...issue, samples: value.samples };
  assert.doesNotMatch(renderHistoryResult(manual), /class="history-score"/);
  assert.match(renderRunEvidence(manual).conditionsHTML, /留存回答判定/);
}

// Each round owns its frozen identity; a current directory or another round cannot rename it.
const identityCompleted = { ...managedProviderRun, id: 'identity-completed', config: { ...managedProviderRun.config,
  auth_id: 'provider-a', subject_auth_index: 'route-a', provider_name: 'Frozen provider', parameters: { reasoning_effort: 'high' } } };
const identitySkipped = { ...identityCompleted, id: 'identity-skipped', status: 'skipped', samples: [],
  skip_detail: { auth_id: 'provider-a', email: '供应商 · Frozen provider · route-a', code: 'identity_unverified',
    reason: 'Synthetic scope identity was not verified for this round.' } };
const liveIdentity = id => id ? `UNRELATED CURRENT DIRECTORY ${id}` : '';
const absentBinding = { ...identitySkipped, config: { ...identitySkipped.config, provider_observation: false, provider_name: '', auth_id: 'saved-route', subject_auth_index: 'saved-index' },
  skip_detail: { auth_id: 'saved-route', code: 'provider_directory_unavailable', reason: '上游配置目录本轮未读到。' } };
for (const [kind, code, wanted, idLabel] of [
  ['provider', 'provider_directory_unavailable', '供应商路由 · saved-index', '路由 ID'],
  ['provider_group', 'provider_directory_unavailable', '供应商配置组 · saved-route', '配置组 ID'],
  ['', 'group_missing', '供应商配置组 · saved-route', '配置组 ID'],
  ['', 'provider_directory_unavailable', '对象 · saved-route', '对象 ID'],
  ['oauth', 'credential_missing', 'saved-route', '凭证 ID'],
]) {
  const value = { ...absentBinding, skip_detail: { ...absentBinding.skip_detail, code, ...(kind ? {object_type: kind} : {}) } };
  for (const html of [renderQualityRuns([value], []), renderQualityDetail(value, [])]) {
    assert(html.includes(`<bdi>${wanted}</bdi>`), 'Missing binding identity: ' + wanted);
    assert(!html.includes('Frozen provider'));
  }
  assert(renderRunEvidence(value).conditionsHTML.includes(`<dt>${idLabel}</dt><dd>saved-route</dd>`));
}
const unrelatedGroupDetail = { ...identityCompleted, samples: [], skip_detail: { auth_id: 'different-group', code: 'group_missing', object_type: 'provider_group' } };
assert(renderQualityDetail(unrelatedGroupDetail, []).includes('<bdi>供应商路由 · route-a</bdi>'));
const providerCatalogEvidence = { ...identityCompleted, samples: [],
  preflight_detail: { code: 'missing_model', model: 'absent', auth_id: identityCompleted.config.auth_id, reason: 'CPA 已核实该凭证没有注册所选模型。' },
  config: { ...identityCompleted.config, catalog_missing_model: 'absent' } };
for (const markup of [renderQualityRuns([providerCatalogEvidence], []), renderQualityDetail(providerCatalogEvidence, [])]) {
  assert(markup.includes('CPA 供应商路由模型目录') && !markup.includes('CPA 凭证模型目录'));
  assert(!markup.split('<details class="quality-detail-conditions"')[0].includes('该凭证没有注册所选模型'));
}
assert.equal(providerCatalogEvidence.preflight_detail.reason, 'CPA 已核实该凭证没有注册所选模型。');
const historicalDependencies = { ...identityCompleted, config: { ...identityCompleted.config,
  control_auth_id: 'old-control-id', judge: { mode: 'llm', auth_id: 'old-judge-id' } } };
const historicalConditions = renderRunEvidence(historicalDependencies, liveIdentity).conditionsHTML;
assert.match(historicalConditions, /<dt>对照凭证 ID<\/dt><dd>old-control-id<\/dd>/);
assert.match(historicalConditions, /<dt>判题凭证 ID<\/dt><dd>old-judge-id<\/dd>/);
assert.doesNotMatch(historicalConditions, /UNRELATED CURRENT DIRECTORY/);
for (const change of [
  { status: 'queued', samples: [] }, { status: 'blocked', samples: [], block_reason: 'budget_exhausted' },
  { pruned_samples: 1, samples: [] }, { samples: [{ ...identityCompleted.samples[0], evidence: {} }] },
  { samples: [{ ...identityCompleted.samples[0], evidence: { path: 'toString' } }] },
]) {
  const record = { ...identityCompleted, ...change, config: { ...identityCompleted.config, provider_name: '' } };
  const row = renderQualityRuns([record], []), detail = renderQualityDetail(record, []);
  for (const surface of [row, detail]) assert.doesNotMatch(surface, /CPA 原生路由|配置定向探测|\[native code\]/);
  if (!record.samples.length && !record.pruned_samples) {
    assert(row.includes('计划强度 high'));
    assert(renderRunEvidence(record).conditionsHTML.includes('计划思考强度'));
  }
}
function assertFrozenIdentity(value, label, supplier = true) {
  const original = JSON.stringify(value);
  const list = renderQualityRuns([value], [], liveIdentity), detail = renderQualityDetail(value, [], liveIdentity);
  const evidence = renderRunEvidence(value, liveIdentity);
  const title = supplier ? `供应商路由 · ${value.config.subject_auth_index || value.config.auth_id}` : label;
  assert.equal(frozenRunIdentity(value).label, label);
  assert.equal(frozenRunIdentity(value).supplier, supplier);
  assert.equal(list.match(/class="quality-run-open"[^>]*><bdi>(.*?)<\/bdi>/)?.[1], title, 'List identity');
  assert.equal(detail.match(/class="quality-detail-identity"[^>]*><strong><bdi>(.*?)<\/bdi>/)?.[1], title, 'Detail identity');
  assert(evidence.conditionsHTML.includes(`<dt>${supplier ? '目标供应商' : '目标凭证'}</dt><dd>${title}</dd>`), 'Frozen condition identity');
  const header = detail.match(/class="quality-detail-identity"[\s\S]*?<\/header>/)?.[0];
  assert(header.includes(value.config.auth_id === title ? `<bdi>${title}</bdi>` : `<code>${value.config.auth_id}</code>`), 'Full ID is available for every outcome');
  if (value.status === 'skipped') {
    assert.match(header, /范围调度准入/, 'An unexecuted round identifies the admission check that actually happened');
    assert.doesNotMatch(header, /CPA 原生路由|配置定向探测/, 'A skipped round cannot claim a model execution path');
  }
  assert(list.includes(`${value.status === 'skipped' ? '计划强度' : '请求强度'} high`), 'Skipped and executed rounds retain the correct meaning of frozen effort');
  for (const markup of [list, detail, evidence.conditionsHTML]) assert.doesNotMatch(markup, /UNRELATED CURRENT DIRECTORY/);
  const manual = renderRunEvidence({ ...value, source: 'manual_test', config: { ...value.config, judge: { mode: 'manual' } } }, liveIdentity);
  assert(manual.conditionsHTML.includes(`<dt>${supplier ? '供应商' : '凭证'}</dt><dd>${label}</dd>`), 'Manual history uses the same frozen identity');
  assert.equal(JSON.stringify(value), original, 'Display must not rewrite retained evidence');
  return list;
}
const standardProviderIdentity = 'Frozen provider · route-a';
const completedIdentityList = assertFrozenIdentity(identityCompleted, standardProviderIdentity);
const skippedIdentityList = assertFrozenIdentity(identitySkipped, standardProviderIdentity);
assert.match(completedIdentityList, /class="history-score" data-tone="good"/); assert.match(completedIdentityList, /1 \/ 1/);
assert.doesNotMatch(completedIdentityList, /未发起模型请求/);
assert.match(skippedIdentityList, /未执行/); assert.match(skippedIdentityList, /未发起模型请求/);
assert(skippedIdentityList.includes(identitySkipped.skip_detail.reason));
assert.doesNotMatch(skippedIdentityList, /class="history-score"/);
for (const provider_observation of [true, false]) {
  assertFrozenIdentity({ ...identitySkipped, config: { ...identitySkipped.config, provider_name: '', provider_observation } }, standardProviderIdentity);
}
assertFrozenIdentity({ ...identitySkipped, skip_detail: { ...identitySkipped.skip_detail, email: '供应商 · Different recorded name · route-a' } }, standardProviderIdentity);
assertFrozenIdentity({ ...identitySkipped, credential_email: '供应商 · Older recorded name · route-a',
  config: { ...identitySkipped.config, provider_name: '' } }, 'Older recorded name · route-a');
assertFrozenIdentity({ ...identitySkipped, config: { ...identitySkipped.config, provider_name: '', subject_auth_index: '' } }, '供应商 · Frozen provider · route-a');
assertFrozenIdentity({ ...identitySkipped, config: { ...identitySkipped.config, provider_name: '' },
  skip_detail: { ...identitySkipped.skip_detail, email: '供应商 · Another route name · route-b' } }, '供应商 · Another route name · route-b');
assertFrozenIdentity({ ...identitySkipped, config: { ...identitySkipped.config, provider_name: '' },
  skip_detail: { ...identitySkipped.skip_detail, email: '' } }, '供应商（名称未记录） · route-a');
assertFrozenIdentity({ ...identitySkipped, config: { ...identitySkipped.config, provider_name: '' },
  skip_detail: { ...identitySkipped.skip_detail, auth_id: 'other-provider' } }, '供应商（名称未记录） · route-a');
assertFrozenIdentity({ ...identityCompleted, config: { ...identityCompleted.config, subject_auth_index: '' } }, 'Frozen provider · provider-a');
const secondRoute = { ...identityCompleted, id: 'identity-other-route', config: { ...identityCompleted.config, auth_id: 'provider-b', subject_auth_index: 'route-b' } };
assertFrozenIdentity(secondRoute, 'Frozen provider · route-b');
const together = renderQualityRuns([identityCompleted, identitySkipped, secondRoute], [], liveIdentity);
assert.equal((together.match(/class="quality-run-row"/g) || []).length, 3, 'Same-object rounds remain separate records');
for (const id of [identityCompleted.id, identitySkipped.id, secondRoute.id]) assert.equal((together.match(new RegExp(`data-run="${id}"`, 'g')) || []).length, 1);
assert.equal((together.match(/<bdi>供应商路由 · route-a<\/bdi>/g) || []).length, 2);
assert.equal((together.match(/<bdi>供应商路由 · route-b<\/bdi>/g) || []).length, 1);
assert.doesNotMatch(together, /class="quality-run-auth-id"/, 'Two rounds of one auth ID do not create an identity conflict');
const collidingRoute = { ...identityCompleted, id: 'identity-index-collision', config: { ...identityCompleted.config, auth_id: 'provider-c' } };
const ambiguousList = renderQualityRuns([identityCompleted, identitySkipped, collidingRoute], [], liveIdentity);
assert.equal((ambiguousList.match(/class="quality-run-row"/g) || []).length, 3);
for (const id of ['provider-a', 'provider-c']) assert(ambiguousList.includes(`class="quality-run-auth-id">路由 ID ${id}<`), 'Identical route indexes with different auth IDs must be visibly distinguishable');
for (const value of [identityCompleted, identitySkipped]) {
  const account = { ...value, credential_email: 'frozen@example.test', config: { ...value.config,
    auth_id: 'account.json', subject_auth_index: 'account-index', provider_name: '', provider_observation: false },
    ...(value.skip_detail ? { skip_detail: { ...value.skip_detail, auth_id: 'account.json', email: 'frozen@example.test' } } : {}) };
  assertFrozenIdentity(account, 'frozen@example.test', false);
  assertFrozenIdentity({ ...account, credential_email: '', skip_detail: account.skip_detail ? { ...account.skip_detail, email: '' } : undefined }, 'account.json', false);
}

const providerRules = [{ id: 'provider-rule', name: 'Provider rule', revision: 7, action: 'disable_credential', auto_restore: true }];
function assertRunHandling(value, label) {
  const list = renderQualityRuns([value], providerRules), detail = renderQualityDetail(value, providerRules, id => id);
  assert.match(list, /<th scope="col">对象与规则<\/th>/);
  assert.match(list, /<th scope="col">本轮处置<\/th>/);
  assert.match(list, /class="quality-handling-cell" data-label="本轮处置"/);
  assert(list.includes(`data-handling="${value.action_status || ''}">${label}</span>`), `List handling: ${value.status}/${value.action_status}`);
  assert(detail.includes(`<dt>本轮处置</dt><dd>${label}</dd>`), `Detail handling: ${value.status}/${value.action_status}`);
  for (const markup of [list, detail, renderRunEvidence(value).metaHTML]) {
    assert.doesNotMatch(markup, /供应商仅观察/);
  }
  if (value.status !== 'skipped') assert(renderRunEvidence(value).metaHTML.includes(`本轮处置 · ${label}`));
}
for (const provider_observation of [true, false]) {
  for (const action_status of ['observe_only', 'provider_observation', 'not_applicable']) {
    const value = { ...managedProviderRun, action_status, config: { ...managedProviderRun.config, provider_observation } };
    assertRunHandling(value, '本轮未处置');
    for (const status of ['queued', 'running', 'cancelling']) assertRunHandling({ ...value, status }, '检测中，尚未处置');
    for (const status of ['failed', 'cancelled', 'blocked']) assertRunHandling({ ...value, status }, '本轮未处置');
    assertRunHandling({ ...value, status: 'skipped', samples: [], skip_detail: { code: 'provider_unavailable', reason: 'Synthetic route unavailable' } }, '本轮未执行，未处置');
  }
}
// Answer counts and a passing retained sample cannot establish whole-round or group permissions.
for (const change of [
  { samples: [] },
  { samples: [{ ...managedProviderRun.samples[0], quality_verdict: 'incorrect' }] },
  { samples: [{ ...managedProviderRun.samples[0], quality_verdict: 'unknown', review: 'correct' }] },
  { config: { ...managedProviderRun.config, samples: 2 } },
  { samples: [managedProviderRun.samples[0], managedProviderRun.samples[0]] },
  { pruned_samples: 1 },
]) assertRunHandling({ ...managedProviderRun, ...change }, '本轮未处置');
const errorSkippedProvider = { ...managedProviderRun, status: 'skipped', samples: [],
  skip_detail: { code: 'error_policy_disabled', reason: 'Synthetic previous upstream error' } };
assertRunHandling(errorSkippedProvider, '保持暂停，本轮未改写');
assertRunHandling({ ...errorSkippedProvider, action_status: undefined }, '保持暂停，本轮未改写');
assertRunHandling({ ...managedProviderRun, action_status: 'credential_suspended', config: { ...run.config, auth_id: 'fake-account' } }, '已暂停凭证');
assertRunHandling({ ...managedProviderRun, action_status: 'credential_suspended', config: { ...managedProviderRun.config, provider_name: '' } }, '已暂停');
for (const [action_status, label] of Object.entries({
  credential_suspended: '已暂停', credential_removed: '已移除凭证', credential_restored: '整轮通过，已恢复',
  credential_kept_disabled: '保持停用', credential_action_attention: '凭证处置待核对',
  insufficient_evidence: '整轮判定依据不足，未处理凭证', catalog_action_unconfirmed: '目录或凭证资格未能再次确认，未执行处置',
  credential_changed: '凭证已变更，未处理', provider_config_conflict: '提供商配置已变化，本次写入已终止',
  disable_pending: '正在暂停凭证', delete_pending: '正在移除凭证', restore_pending: '正在恢复凭证',
  future_action_result: 'future_action_result', '': '未记录处理结果',
})) {
  assertRunHandling({ ...managedProviderRun, action_status }, label);
  assertRunHandling({ ...errorSkippedProvider, action_status }, action_status ? label : '保持暂停，本轮未改写');
}
for (const [credential_action, label] of Object.entries({ disable_credential: '停用结果待核对', delete_credential: '移除结果待核对', restore_credential: '恢复结果待核对', '': '处置结果待核对' })) {
  assertRunHandling({ ...managedProviderRun, action_status: 'credential_action_uncertain', credential_action }, label);
  assertRunHandling({ ...errorSkippedProvider, action_status: 'credential_action_uncertain', credential_action }, label);
}

// Duplicate and out-of-plan records stay readable without completing a missing position.
const duplicate = { ...run, config: { ...run.config, samples: 2 }, samples: [
  { ...run.samples[0], output: 'duplicate A', answer_match: 'correct' },
  { ...run.samples[0], output: 'duplicate B', answer_match: 'correct' },
] };
assert.match(renderRunEvidence(duplicate).conditionsHTML, /部分已判定/);
assert.doesNotMatch(renderHistoryResult(duplicate), /class="history-score"/, 'Repeated model/sample positions cannot complete the history score');
const endedSample = { ...run.samples[0], answer_match: 'correct' };
for (const status of ['running', 'cancelled', 'failed']) {
  assert.doesNotMatch(renderHistoryResult({ ...run, status, samples: [endedSample] }), /class="history-score"/, status);
}
const anomalous = renderRunEvidence({ ...duplicate, samples: [...duplicate.samples,
  { ...run.samples[0], model: 'other-model', output: 'unplanned model' },
  { ...run.samples[0], index: -1, output: 'negative index' },
  { ...run.samples[0], index: 4, output: 'out of range' },
  { ...run.samples[0], index: undefined, output: 'unspecified index' },
] });
assert.equal(anomalous.completed, 1);
assert.equal(anomalous.planned, 2);
for (const answer of ['duplicate A', 'duplicate B', 'unplanned model', 'negative index', 'out of range', 'unspecified index']) {
  assert(anomalous.samplesHTML.includes('>' + answer + '</pre>'));
}
assert.match(anomalous.samplesHTML, /回答缺失/);
const trimmed = { ...run, source: 'scheduled_probe', pruned_samples: 1,
  config: { ...run.config, samples: 3 }, samples: [{ ...run.samples[0], index: 1 }],
  pruned_sample_slots: [{ model: 'example-model', index: 2 }],
};
const knownSlots = renderRunEvidence(trimmed);
assert.match(knownSlots.samplesHTML, /样本 1<\/span><\/h4><span class="tag warn">样本缺失/);
assert.match(knownSlots.samplesHTML, /样本 3<\/span><\/h4><span class="tag neutral">历史已裁剪/);
const partiallyKnown = renderRunEvidence({ ...trimmed, pruned_samples: 2 });
assert.match(partiallyKnown.samplesHTML, /样本 1<\/span><\/h4><span class="tag neutral">无留存回答/);
assert.match(partiallyKnown.samplesHTML, /样本 3<\/span><\/h4><span class="tag neutral">历史已裁剪/);

const failed = renderRunEvidence({ ...run, status: 'failed', error: 'network unavailable', samples: [{ ...run.samples[0], execution_status: 'failed', output: '', error: 'upstream disconnected' }] });
assert.match(failed.metaHTML, /network unavailable/);
assert.match(failed.samplesHTML, /upstream disconnected/);
assert.match(failed.samplesHTML, /执行失败/);
assert.match(failed.metaHTML, />执行失败</);
assert.doesNotMatch(failed.metaHTML, /错误 1/);
const mixed = renderRunEvidence({ ...run, samples: [
  { ...run.samples[0], answer_match: 'correct' },
  { ...run.samples[0], index: 1, answer_match: 'incorrect' },
  { ...run.samples[0], index: 2, review: 'unknown', answer_match: 'correct' },
  { ...run.samples[0], index: 3, quality_verdict: 'incorrect', answer_match: 'correct' },
  { ...run.samples[0], index: 4, review: 'correct', quality_verdict: 'incorrect' },
] });
assert.match(mixed.conditionsHTML, />部分正确</);
assert.doesNotMatch(mixed.metaHTML, /部分正确/);
assert.doesNotMatch(mixed.metaHTML, /(正确|错误|未判定)\s*\d/);
assert.match(mixed.samplesHTML, />答案正确</);
assert.match(mixed.samplesHTML, />答案错误</);
assert.match(mixed.samplesHTML, />回答 2</);
for (const reason of ['cancelled', 'context canceled']) assert.equal(reasonLabel(reason), '测试已取消');
assert.equal(reasonLabel('unmapped upstream error'), 'unmapped upstream error');
assert.equal(auditLabel('credential_external_enable'), '已识别 CPA 人工启用');
assert.equal(auditLabel('credential_external_change'), '已识别 CPA 当前凭证变更');
const cancelledSample = { ...run.samples[0], execution_status: 'cancelled', error: 'context canceled', output: '' };
const cancelled = renderRunEvidence({ ...run, status: 'cancelled', samples: [cancelledSample] });
assert.match(cancelled.samplesHTML, /测试已取消/);
assert.equal(cancelledSample.error, 'context canceled');
for (const status of ['completed', 'complete', 'success', 'succeeded', 'released']) {
  assert.doesNotMatch(statusTag(status), /tag good/, 'Execution completion and owned release must not imply correct quality or full availability');
}
assert.match(verdictTag('correct'), /tag good/, 'Only an explicitly correct verdict uses the quality success color');
console.log('Question test prompt binding, manual result presentation, escaping and rule-view compatibility passed.');
