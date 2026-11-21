import { escapeHTML, previewLimit, previewSource } from './markup.js';
export { escapeHTML, previewLimit, previewSource, safePreview } from './markup.js';
export const items = (data) => (Array.isArray(data) ? data : data?.items || []);
export const targetID = (target) => target.auth_id || target.id;
export const providerTargetName = target => `供应商 · ${[target.provider_name || target.provider, target.auth_index || targetID(target)].filter(Boolean).join(' · ')}`;
function frozenProviderName(config, recordedLabel) {
  const index = config.subject_auth_index;
  let name = config.provider_name;
  // Older records kept a display label instead of the structured name. Only
  // normalize it when this record's frozen route index confirms its suffix.
  if (!name && index && typeof recordedLabel === 'string') {
    const prefix = '供应商 · ', suffix = ` · ${index}`;
    if (recordedLabel.startsWith(prefix) && recordedLabel.endsWith(suffix)) {
      name = recordedLabel.slice(prefix.length, -suffix.length).trim();
    }
  }
  return name || '';
}

export function frozenProviderLabel(config = {}, recordedLabel = '') {
  const name = frozenProviderName(config, recordedLabel);
  if (!name && !config.provider_observation) return '';
  if (!name && recordedLabel) return recordedLabel;
  return [name || '供应商（名称未记录）', config.subject_auth_index || config.auth_id].filter(Boolean).join(' · ');
}

const historySubjectTerms = {
  provider: ['供应商', '路由 ID'], provider_group: ['供应商配置组', '配置组 ID'],
  oauth: ['凭证', '凭证 ID'], unknown: ['对象', '对象 ID'],
};

export function frozenRunIdentity(run = {}) {
  const config = run.config || {};
  const detail = run.preflight_detail || run.skip_detail;
  const id = config.auth_id || detail?.auth_id || '';
  const matchingDetail = !detail?.auth_id || detail.auth_id === id ? detail : null;
  const recordedLabel = run.credential_email || matchingDetail?.email || '';
  const routeLabel = frozenProviderLabel(config, recordedLabel);
  const index = config.subject_auth_index || matchingDetail?.auth_index || '';
  let type = matchingDetail?.object_type;
  if (!Object.hasOwn(historySubjectTerms, type)) {
    // An older missing-row diagnostic has no verified subject from this occurrence.
    if (routeLabel) type = 'provider';
    else if (matchingDetail?.code === 'group_missing') type = 'provider_group';
    else if (!recordedLabel && ['provider_directory_unavailable', 'credential_missing', 'unverified_inventory_entry'].includes(matchingDetail?.code)) type = 'unknown';
    else type = 'oauth';
  }
  const [noun, idLabel] = historySubjectTerms[type];
  return { id, index, type, noun, idLabel, supplier: type === 'provider' || type === 'provider_group',
    name: type === 'provider' ? frozenProviderName(config, recordedLabel) : '',
    label: (type === 'provider' ? routeLabel : recordedLabel) || id || '对象未记录' };
}

export function qualityIdentityLabel(identity) {
  if (identity.type === 'provider') return `供应商路由 · ${identity.index || identity.id || '标识未记录'}`;
  if (identity.type === 'provider_group' || identity.type === 'unknown') return `${identity.noun} · ${identity.id || '标识未记录'}`;
  return identity.label;
}
export function availableModels(target) {
  return [...new Set((target?.models || []).map(entry => typeof entry === 'string' ? entry : entry.id || entry.model).filter(Boolean))];
}
export function targetRuntimeReady(target) {
  return target?.effective_available === true && !target.manual_disabled &&
    (target.is_provider ? ['registered', 'active'] : ['active']).includes(target.runtime_status);
}
export function providerRouteReady(target, models = []) {
  const catalog = availableModels(target);
  return !!(target && targetID(target) && target.auth_index && target.provider) && targetRuntimeReady(target) &&
    !target.quality_paused && !target.retained_pause && catalog.length > 0 && models.every(model => catalog.includes(model));
}

export function ruleTargetType(target) {
  if (target?.object_type) return target.object_type;
  if (!target || target.reasons?.includes('unverified_inventory_entry')) return 'unknown';
  if (target.is_provider) return 'provider';
  if (['oauth','auth_file'].includes(target.type) || target.email) return 'oauth';
  return 'unknown';
}

export function ruleObjectTypes(rule, targets) {
  if (Array.isArray(rule.object_types)) return rule.object_types;
  const config = rule.config || rule;
  const target = targets.find(t => targetID(t) === config.auth_id);
  return [rule.target_group || ruleTargetType(target) === 'provider' ? 'provider' : 'oauth'];
}

export function resolveRuleBinding(rule, targets) {
  const config = rule.config || rule;
  const id = config.auth_id, provider = config.provider, index = rule.target_auth_index;
  const type = rule.target_object_type || (!rule.legacy_scope && rule.object_types?.length === 1 ? rule.object_types[0] : '');
  const target = id && targets.find(t => targetID(t) === id && (!provider || t.provider === provider) &&
    (!index || t.auth_index === index) && (!type || ruleTargetType(t) === type));
  const object = type === 'provider' ? '供应商路由' : type === 'oauth' ? '凭证' : '对象';
  const pendingLabel = [object, id, index !== id && index, '原绑定待核实'].filter(Boolean).join(' · ');
  return { id, provider, index, type, target, pendingLabel };
}

export function ruleScopeDescription(rule, targets) {
  const types = ruleObjectTypes(rule, targets).map(type => ({oauth:'OAuth 账户',provider:'AI 提供商'})[type]).filter(Boolean).join('、');
  const group = rule.target_group && targets.find(t => ruleTargetType(t) === 'provider' && t.provider_group_id === rule.target_group);
  const { target, id, pendingLabel } = resolveRuleBinding(rule, targets);
  const selected = rule.target_group ? `${group?.provider_name || rule.target_group}（配置组）`
    : target ? target.is_provider ? providerTargetName(target) : target.email || id : id && pendingLabel;
  const scope = dynamicScopeLabel(rule.target_scope) || `指定对象 · ${selected || '目录待核实'}`;
  return [types, scope].filter(Boolean).join(' · ');
}
// These are upstream rule-entry presets, not a claim of live CPA availability.
export const ruleModelShortcuts = ['gpt-6.2-sol', 'gpt-6-astra', 'gpt-6.1-sol'];
export const splitRuleModels = (text) => [...new Set(text.split(/[\s,，;；]+/u).filter(Boolean))];
export const active = (run) => ['queued', 'running', 'cancelling'].includes(run?.status);
export const date = (time) => {
  if (!time || String(time).startsWith('0001-')) return '未知';
  const parsed = new Date(time);
  return Number.isNaN(parsed.getTime()) ? '未知' : parsed.toLocaleString('zh-CN', {
    year: 'numeric', month: '2-digit', day: '2-digit',
    hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
  });
};
export const duration = (ms) => ms == null || !Number.isFinite(Number(ms)) || Number(ms) < 0
  ? '未知' : Number(ms) < 1000 ? `${Number(ms).toLocaleString('zh-CN', { maximumFractionDigits: 2 })} ms` : `${(Number(ms) / 1000).toFixed(2)} s`;
const sourceLabels = { manual_test: '智商测试', scheduled_probe: '规则检测', production: '业务请求', judge: '独立判题', internal_other: '其他内部请求', unknown: '来源未知' };
export const sourceLabel = (source) => sourceLabels[source] || source || '来源未知';
const questionTitles = {
  'logic-candy': '糖果逻辑测试', 'logic-boxes': '约束盒子',
  'visual-pelican-html': '鹈鹕骑自行车（HTML）', 'visual-pelican': '鹈鹕骑自行车',
  'random-linear': '随机约束题', custom: '自定义题目',
};
export const questionTitle = (question, id = question?.id) =>
  question?.title || questionTitles[id] || id || '题目未记录';
const auditLabels = {
  provider_config_conflict: '提供商配置条件已变化，已终止写入',
  disable_pending: '正在暂停凭证', delete_pending: '正在移除凭证', restore_pending: '正在恢复凭证',
  credential_suspended: '已暂停凭证', credential_removed: '已移除凭证', credential_restored: '整轮通过，已恢复凭证',
  credential_action_uncertain: '处置结果待核对', credential_external_enable: '已识别 CPA 人工启用',
  credential_external_change: '已识别 CPA 当前凭证变更', credential_handling_acknowledged: '人工处理已核对，规则设置保持不变',

  scope_dispatched: '匹配动态检测范围',
  observation_completed: '观察完成', settings_saved: '设置已保存',
  rule_created: '规则已创建', rule_updated: '规则已更新', rule_saved: '规则已保存',
  rule_schedule_state: '规则调度已变更', rule_schedule_invalid: '检测周期无效，未执行', rule_deleted: '规则已删除',
  credential_login_prepare: '凭证登录前检查', legacy_ownership_retained: '历史暂停归属已保留',
  credential_login_result: '凭证更新协调结果',
  credential_binding_changed: '新条目需要核对规则绑定',
  pause_requested: '已请求质量暂停', pause_uncertain: '质量暂停结果待核对',
  credential_paused: '凭证已质量暂停', manual_release_requested: '已请求人工解除',
  manual_release: '已人工解除自身暂停', reconciled_pause: '暂停归属已核对',
};
export const auditLabel = (kind) => auditLabels[kind] || kind || '审计记录';
const reasonLabels = {
  provider_targets_excluded: '供应商检测已关闭，本次规则检测已停止。',
  'blocked: provider targets are excluded from quality operations': '质量运维已关闭供应商检测。',
  'blocked: provider target is unavailable': '供应商路由当前不可用，请核对 CPA 配置。',
  'blocked: provider model is not registered': '供应商未注册所选模型。',
  cancelled: '测试已取消',
  'context canceled': '测试已取消',
  queue_full: '队列容量不足，本轮未入队。',
  budget_exhausted: '调用预算已用尽，本轮未执行。',
  official_host_evidence_unavailable: '当前宿主未提供可验证的最终执行条件，本次结果仅供观察。',
  retained_pause_unverified: '历史暂停归属待核对',
  management_probe_not_business_payload: '本次使用 CPA 管理接口检测指定凭证。',
  configuration_pinned_probe: '按已核实的提供商配置定向探测。',
  'exact answer': '回答与标准答案一致',
  'integer differs from frozen reference': '回答与标准答案不一致',
  'manual assessment required': '需要人工评审',
  'answer is not exactly one integer': '回答不符合仅输出一个整数的格式，无法判定',
  'invalid integer': '整数格式无效，无法判定',
  'refusal is not quality evidence': '模型拒答，不作为答题质量失败证据',
  'missing or truncated completion': '回答未完整结束或已截断，不作为答题质量失败证据',
  'response limit exceeded': '响应数据超过采集上限，本次接收已停止。',
  'output limit exceeded': '回答正文超过输出上限，本次接收已停止。',
  'response answer limit exceeded': '回答正文超过输出上限，本次接收已停止。',
  'response frame limit exceeded': '单个响应帧超过采集上限，本次接收已停止。',
  'response transport limit exceeded': '流式响应累计数据超过传输上限，本次接收已停止。',
  'response output token limit reached': '回答达到输出 token 上限，未能完整生成。',
  'incomplete SSE frame': '流式响应在单帧接收完成前结束，回答可能不完整。',
  'premature stream EOF': '流式响应提前结束，未确认回答完整完成。',
  'judge budget exhausted': '独立判题预算不足，无法判定',
  'judge execution failed': '独立判题执行失败，无法判定',
  'invalid judge JSON': '独立判题未返回有效 JSON，无法判定',
  'judge must return an object': '独立判题未返回 JSON 对象，无法判定',
  'invalid key': '独立判题返回的字段名无效，无法判定',
  'duplicate or unexpected judge field': '独立判题返回重复或未约定字段，无法判定',
  'judge fields must be strings': '独立判题返回的字段类型无效，无法判定',
  'invalid judge object': '独立判题返回的对象不完整，无法判定',
  'extra judge output': '独立判题包含额外输出，无法判定',
  'invalid judge verdict': '独立判题结论格式无效，无法判定',
  'all selected models explicitly failed complete comparable rounds; manual release required': '所有选定模型在完整、可比较的连续轮次中明确答错，须人工解除暂停',
};
export function reasonLabel(reason) {
  if (reasonLabels[reason]) return reasonLabels[reason];
  if (typeof reason === 'string' && /(?:^|:\s*)(?:context deadline exceeded|i\/o timeout|net\/http: timeout awaiting response headers|Client\.Timeout exceeded while awaiting headers)(?:$|\s*\()/.test(reason)) {
    return '请求超时，本次接收已停止。';
  }
  return reason || '未提供说明';
}
export function manualIssueLabel(reason) {
  const message = reason?.replace(/^blocked:\s*/, '');
  if (message?.endsWith('，CPA 当前仍停用此凭证。')) return 'CPA 当前仍停用此凭证，无法开始智商测试。';
  if (message?.endsWith('，凭证已按错误策略停用。') || message === 'credential already disabled by error policy' ||
      message === '凭证存在未解除的处置记录，请在质量运维中核对。') {
    return '当前凭证状态尚未确认，无法开始智商测试。请刷新后重试。';
  }
  if (message === '凭证的处置记录在测试提交后发生了变化，请重新核对。') return '测试提交后凭证状态已变化，请刷新后重试。';
  if (['CPA 当前凭证与原错误处置记录的身份不一致，请核对账号。', 'CPA 当前账号标识与原错误处置记录不一致，请核对账号。',
    '无法确认刷新后的凭证仍属于原账号，请在质量运维中核对。'].includes(message)) {
    return '无法确认所选凭证的账号身份，请核对账号后重试。';
  }
  return reasonLabel(message);
}
const verdictLabels = { correct: '正确', incorrect: '错误', unknown: '无法判断' };
export const statusLabels = {
  unavailable: '记录不可用',
  queued: '排队中',
  running: '执行中',
  completed: '已完成',
  complete: '已完成',
  success: '完成',
  succeeded: '完成',
  failed: '执行失败',
  error: '执行失败',
  cancelled: '已取消',
  canceling: '正在取消',
  cancelling: '正在取消',
  interrupted: '已中断',
  detecting: '检测中',
  awaiting_release: '等待人工解除',
  credential_update_pending: '等待凭证更新',
  credential_update_failed: '等待重新授权成功',
  credential_write_unknown: '凭证写入待核对',
  credential_identity_conflict: '授权条目身份待核对',
  credential_paused: '凭证已暂停',
  blocked: '受阻',
  skipped: '未执行',
  pending: '等待中',
  observe_only: '仅检测',
  credential_suspended: '已暂停凭证',
  credential_kept_disabled: '保持停用',
  credential_removed: '已移除凭证',
  credential_restored: '整轮通过，已恢复',
  credential_action_uncertain: '处置结果待核对',
  credential_action_attention: '凭证处置待核对',
  insufficient_evidence: '整轮判定依据不足，未处理凭证',
  catalog_action_unconfirmed: '目录或凭证资格未能再次确认，未执行处置',
  credential_changed: '凭证已变更，未处理',
  provider_config_conflict: '提供商配置已变化，本次写入已终止',
  management_session_required: '后台管理授权待就绪',
  disable_pending: '正在暂停凭证',
  delete_pending: '正在移除凭证',
  restore_pending: '正在恢复凭证',
  paused: '凭证已暂停',
  released: '已释放',
  conflict: '归属冲突',
  none: '无动作',
  not_applicable: '不适用',
  refused: '模型拒答',
  incomplete: '回答不完整',
  empty: '空回答',
};
export const tag = (text, tone = 'neutral') => `<span class="tag ${tone}">${escapeHTML(text)}</span>`;
export const statusTag = (status) =>
  tag(
    statusLabels[status] || status || '未知',
    ['failed', 'error', 'conflict', 'blocked'].includes(status)
      ? 'bad'
      : ['queued', 'running'].includes(status)
        ? 'warn'
        : 'neutral',
  );
export const verdictTag = (verdict) =>
  tag(
    verdictLabels[verdict] || '无法判断',
    verdict === 'correct' ? 'good' : verdict === 'incorrect' ? 'bad' : 'neutral',
  );

// Editing any part of a built-in prompt detaches its reference answer.
export function questionTestSelection(question, prompt) {
  const text = String(prompt ?? '');
  if (question?.id && question.id !== 'custom' && text === question.prompt) return { question_id: question.id };
  return { question_id: 'custom', prompt: text };
}

export function copyRunConfig(source) {
  const config = {};
  // Retain only identity evidence available in this saved manual run.
  const kind = source.expected_object_type || (source.provider_observation || source.provider_name ? 'provider' : '');
  const index = source.subject_auth_index || source.expected_auth_index;
  if (kind) config.expected_object_type = kind;
  if (index) config.expected_auth_index = index;
  for (const key of [
    'auth_id',
    'provider',
    'models',
    'question_id',
    'prompt',
    'review_standard',
    'seed',
    'samples',
    'parameters',
    'judge',
    'control_auth_id',
    'control_provider',
  ]) {
    if (source[key] !== undefined) config[key] = source[key];
  }
  return structuredClone(config);
}

export function credentialBlock(config, targets, { manual = false } = {}) {
  const references = [
    ['目标', config.auth_id],
    ['对照', config.control_auth_id],
    ['判题', config.judge?.mode === 'llm' ? config.judge.auth_id : ''],
  ];
  for (const [role, id] of references) {
    if (!id) continue;
    const target = targets.find((entry) => targetID(entry) === id);
    if (!target) return `${role}凭证当前不可发现，请刷新并核对状态。`;
    if (target.quality_paused || target.retained_pause)
      return `${role}凭证已被质量暂停，禁止所有复测。仅管理员手动解除后才可再次检测。`;
    if (target.handling_state_reason) return target.handling_state_reason;
    const policy = target.error_policy_block?.code === 'error_policy_disabled';
    if (manual && target.credential_handling && !['disabled', 'restored', 'acknowledged', 'externally_enabled'].includes(target.credential_handling)) {
      return '当前凭证状态尚未确认，无法开始智商测试。请刷新后重试。';
    }
    if (target.manual_disabled || target.runtime_status === 'disabled') {
      if (policy) return `${role}${target.is_provider ? '供应商路由' : '凭证'}当前仍在 CPA 停用，停用期间退出答题和恢复复检。`;
      return `${role}${target.is_provider ? '供应商' : '凭证'}已在 CPA 停用。`;
    }
    if (target.is_provider && !providerRouteReady(target)) {
      const reasons = target.reasons || [];
      if (reasons.includes('provider_models_unavailable')) return `${role}供应商在 CPA 暂无已注册模型。`;
      if (reasons.includes('native_cooldown')) return `${role}供应商路由正在 CPA 冷却，请稍后重试。`;
      if (reasons.includes('native_unavailable')) return `${role}供应商路由当前被 CPA 标记为不可用，请核对运行状态。`;
      if (reasons.includes('identity_conflict')) return `${role}供应商的路由标识不一致，请刷新并核对密钥索引。`;
      return `${role}供应商的 CPA 原生路由尚未确认，请刷新后重试。`;
    }
    if (policy && role === '目标') {
      const models = availableModels(target);
      const missing = config.models?.find(model => !models.includes(model));
      if (missing) return `CPA 当前目录没有所选模型 ${missing}，请选择已注册的模型。`;
      if (!config.models?.length || !models.length) return '请从已注册的模型中选择本次测试模型。';
    }
    if (target.effective_available === false) return `${role}凭证当前不可用，请核对 CPA 运行状态。`;
  }
  return '';
}

export function holdsPause(rule, targets) {
  const config = rule.config || rule;
  const references = [
    config.auth_id,
    config.control_auth_id,
    config.judge?.mode === 'llm' ? config.judge.auth_id : '',
    rule.blocked_auth_id,
  ];
  return (
    rule.status === 'awaiting_release' ||
    !!rule.action_id || !!rule.blocked_auth_id ||
    references.some((id) => targets.find((target) => targetID(target) === id)?.quality_paused)
  );
}

// Evidence rendering keeps execution, verdict, and comparability independent.
const evidenceValue = (value, fallback = '未知') =>
  value === null || value === undefined || value === '' ? fallback : String(value);
const evidenceBoolean = (value, yes, no) => value === true ? yes : value === false ? no : '未知';
const evidenceFact = (label, value, className = '') =>
  `<div${className ? ` class="${escapeHTML(className)}"` : ''}><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(evidenceValue(value))}</dd></div>`;
const evidenceFacts = (pairs) => `<dl class="evidence-facts">${pairs.map(([label, value]) => evidenceFact(label, value)).join('')}</dl>`;
const evidenceJSON = (object) => `<details class="raw-evidence"><summary>原始 JSON</summary><pre>${escapeHTML(JSON.stringify(object, null, 2))}</pre></details>`;
const judgeName = (mode) => ({ deterministic: '标准答案判定', manual: '人工评审', llm: '独立模型判题' })[mode] || evidenceValue(mode);
const reviewName = (verdict) => ({ correct: '正确', incorrect: '错误', unknown: '无法判断' })[verdict] || '未评审';
const completedSample = (sample) => !!sample.execution_status && !['pending', 'queued', 'running', 'cancelling', 'canceling'].includes(sample.execution_status);
const sampleModel = sample => sample.model || sample.request_model || '模型未知';
export const isPartialAnswer = sample => !sample || sampleExecutionStatus(sample) !== 'completed';

function answerVerdict(sample) {
  if (sampleExecutionStatus(sample) !== 'completed') return '';
  return sample.review || (['correct', 'incorrect'].includes(sample.quality_verdict) ? sample.quality_verdict : sample.answer_match) || 'unknown';
}

function answerTag(sample) {
  const verdict = answerVerdict(sample);
  if (!verdict) return statusTag(sampleExecutionStatus(sample));
  if (verdict === 'correct') return tag('答案正确', 'good');
  if (verdict === 'incorrect') return tag('答案错误', 'warn');
  return tag('未判定');
}

function answerCounts(run) {
  const counts = new Map();
  for (const sample of run.samples || []) {
    if (!completedSample(sample)) continue;
    const key = answerVerdict(sample) || 'no_answer';
    counts.set(key, (counts.get(key) || 0) + 1);
  }
  return counts;
}

function renderHistoryScore(correct, incorrect) {
  const total = correct + incorrect;
  const description = `${total} 份回答中有 ${correct} 份答对`;
  const mark = incorrect ? 'M10 6.5v4m0 2.6v.1' : 'm6.5 10 2.3 2.3 4.7-4.8';
  return `<span class="history-score" data-tone="${incorrect ? 'bad' : 'good'}" title="${description}">
    <svg class="control-icon" viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7.5"/><path d="${mark}"/></svg>
    <span class="sr-only">${description}。</span><span class="history-ratio" aria-hidden="true">${correct} / ${total}</span></span>`;
}

function recordedSampleCoverage(run) {
  const models = run.config?.models || [];
  const perModel = run.config?.samples;
  const planned = Number.isInteger(perModel) && perModel > 0 ? new Set(models).size * perModel : 0;
  const slots = new Set();
  for (const sample of run.samples || []) {
    if (completedSample(sample) && models.includes(sampleModel(sample)) && Number.isInteger(sample.index) && sample.index >= 0 && sample.index < perModel) {
      slots.add(JSON.stringify([sampleModel(sample), sample.index]));
    }
  }
  return { planned, finished: slots.size };
}

function roundCompletedCleanly(run) {
  return run.status === 'completed' && !run.error && !run.block_reason && !run.cancel_requested && !run.upstream_condition;
}

function fullyJudgedRound(run, { planned, finished }, judged) {
  return roundCompletedCleanly(run) && !run.pruned_samples && planned > 0 && finished === planned &&
    run.samples.length === planned && judged === planned;
}

// Count recorded answers separately from attempts that never produced a judgeable answer.
export function renderHistoryResult(run) {
  const samples = run.samples || [];
  const coverage = recordedSampleCoverage(run);
  const counts = new Map();
  for (const sample of samples) {
    if (!completedSample(sample)) continue;
    const verdict = answerVerdict(sample);
    const result = verdict || sampleExecutionStatus(sample);
    counts.set(result, (counts.get(result) || 0) + 1);
  }
  const facts = [];
  const add = (text, tone = 'muted') => facts.push(`<span class="history-fact" data-tone="${tone}">${escapeHTML(text)}</span>`);
  if (active(run)) {
    const progress = coverage.planned ? ` · ${coverage.finished}/${coverage.planned} 份已结束` : '';
    add(statusLabels[run.status] + progress);
  } else if (['cancelled', 'interrupted', 'failed', 'blocked', 'skipped', 'unavailable'].includes(run.status) && !counts.has(run.status)) {
    add(statusLabels[run.status], 'warn');
  }
  const labels = {
    correct: ['答对', 'good'], incorrect: ['答错', 'bad'], unknown: ['未判定', 'muted'],
    refused: ['拒答', 'warn'], incomplete: ['回答不完整', 'warn'], empty: ['空回答', 'muted'],
    failed: ['执行失败', 'warn'], cancelled: ['已取消', 'muted'],
  };
  const correct = counts.get('correct') || 0, incorrect = counts.get('incorrect') || 0;
  if (fullyJudgedRound(run, coverage, correct + incorrect)) {
    facts.push(renderHistoryScore(correct, incorrect));
  } else {
    for (const [key, count] of counts) {
      const [label, tone] = Object.hasOwn(labels, key) ? labels[key] : [statusLabels[key] || '执行状态未知', 'muted'];
      add(`${label} ${count}`, tone);
    }
  }
  if (run.pruned_samples > 0) add(`已裁剪 ${run.pruned_samples}`);
  else if (!active(run) && !counts.size) add(run.answer_preview ? '有回答，判定未记录' : '未取得回答');
  else if (!active(run) && coverage.finished && coverage.planned > coverage.finished) add(`缺少 ${coverage.planned - coverage.finished} 份回答`);
  const outcome = `<span class="history-outcome" aria-label="答题结果">${facts.join('')}</span>`;
  const preview = run.answer_preview;
  if (!preview) return outcome;
  const visual = preview.kind === 'html' || preview.kind === 'svg';
  if (!visual && !preview.text) return outcome;
  const ordinal = Number.isInteger(preview.index) && preview.index >= 0 ? `回答 ${preview.index + 1}` : '回答摘录';
  const label = run.config?.models?.length > 1 && preview.model ? `${preview.model} · ${ordinal}` : ordinal;
  const previewSample = samples.find(sample => sampleModel(sample) === preview.model && sample.index === preview.index);
  const partial = previewSample && isPartialAnswer(previewSample);
  const excerpt = visual ? `${preview.kind.toUpperCase()} ${partial ? '部分作品' : '作品'}，打开记录可预览` : preview.text + (preview.truncated ? '…' : '');
  const prefix = coverage.planned === 1 && samples.length <= 1 && !run.pruned_samples && preview.index === 0 ? '' : `<span class="history-excerpt-label">${escapeHTML(label)} · </span>`;
  return `<span class="history-excerpt">${prefix}<bdi class="history-excerpt-text" dir="auto">${escapeHTML(excerpt)}</bdi></span>` + outcome;
}

export function renderManualVerdict(run) {
  if (run.source !== 'manual_test' || active(run)) return '';
  const counts = answerCounts(run);
  const correct = counts.get('correct') || 0, incorrect = counts.get('incorrect') || 0;
  if (correct && incorrect) return tag('部分正确', 'warn');
  if (correct || incorrect) {
    const models = run.config?.models || [];
    const count = run.config?.samples;
    const reviewed = run.samples.filter(s => ['correct', 'incorrect'].includes(answerVerdict(s)));
    const positions = new Set(reviewed.filter(s => models.includes(sampleModel(s)) && Number.isInteger(s.index) && s.index >= 0 && s.index < count)
      .map(s => JSON.stringify([sampleModel(s), s.index])));
    if (counts.size > 1 || !models.length || !count || reviewed.length !== positions.size || positions.size !== models.length * count) return tag('部分已判定');
    return correct ? tag('正确', 'good') : tag('错误', 'warn');
  }
  return counts.has('unknown') ? tag('未判定') : '';
}

export function formatSampleUsage(sample) {
  const usage = sample.usage;
  if (!usage || sample.usage_known === false) return '用量未知';
  const total = usage.total_tokens ??
    (usage.input_tokens != null && usage.output_tokens != null ? usage.input_tokens + usage.output_tokens : null);
  return total == null || !Number.isFinite(Number(total)) || Number(total) < 0 ? '用量未知' : `${Number(total).toLocaleString('zh-CN')} token`;
}

const unjudgedExecutionStates = new Set(['refused', 'incomplete', 'empty', 'cancelled', 'interrupted', 'pending', 'queued', 'running', 'cancelling', 'canceling', 'skipped', 'blocked', 'failed']);

function sampleExecutionStatus(sample) {
  const status = sample.execution_status;
  if (unjudgedExecutionStates.has(status)) return status;
  return sample.error || sample.upstream_condition || status === 'error' ? 'failed' : status;
}

function qualitySampleOutcome(sample) {
  const status = sampleExecutionStatus(sample);
  if (status === 'completed') {
    return ['correct', 'incorrect'].includes(sample.quality_verdict) ? sample.quality_verdict : 'unknown';
  }
  return unjudgedExecutionStates.has(status) ? status : 'unknown';
}

function qualityCounts(samples) {
  const outcomes = samples.map(qualitySampleOutcome);
  const correct = outcomes.filter(value => value === 'correct').length;
  const incorrect = outcomes.filter(value => value === 'incorrect').length;
  return { correct, incorrect, unknown: samples.length - correct - incorrect };
}

function credentialHandlingLabel(run) {
  const noAction = ['observe_only', 'provider_observation', 'not_applicable'].includes(run.action_status);
  if (run.status === 'skipped' && run.skip_detail?.code === 'error_policy_disabled' && (!run.action_status || noAction)) return '保持暂停，本轮未改写';
  if (noAction) {
    if (run.status === 'skipped') return '本轮未执行，未处置';
    return active(run) ? '检测中，尚未处置' : '本轮未处置';
  }
  if (run.action_status === 'credential_suspended' && (run.config?.provider_observation || run.config?.provider_name)) return '已暂停';
  if (run.action_status === 'credential_action_uncertain') {
    const intent = { disable_credential: '停用', delete_credential: '移除', restore_credential: '恢复' }[run.credential_action];
    if (intent) return `${intent}结果待核对`;
  }
  return statusLabels[run.action_status] || run.action_status || '未记录处理结果';
}

const probePaths = {provider_native_probe:'提供商 · CPA 原生路由',provider_config_probe:'提供商配置定向探测',cpa_management_probe:'CPA 管理接口定向探测'};
function recordedProbePath(run) {
  const paths = (run.samples || []).map(sample => sample.evidence?.path).filter(path => Object.hasOwn(probePaths, path));
  return [...new Set(paths)].map(path => probePaths[path]).join('、');
}

function hasRecordedSamples(run) {
  return run.pruned_samples > 0 || (run.samples || []).some(sample => completedSample(sample) && sample.execution_status !== 'skipped');
}

export const upstreamConditions = Object.freeze({
  unauthorized: '401 认证失败', payment_required: '402 付费要求',
  quota_exhausted: '429 配额耗尽', model_unsupported: '指定模型不支持',
});

export const credentialActions = Object.freeze({ observe_only: '不处理', disable_credential: '暂停凭证', delete_credential: '移除凭证' });
export function handlesCredential(action) {
  return action === 'disable_credential' || action === 'delete_credential';
}
export function configuredUpstreamActions(actions) {
  return Object.entries(upstreamConditions)
    .filter(([key]) => handlesCredential(actions?.[key]))
    .map(([key, label]) => [label, credentialActions[actions[key]]]);
}
const upstreamLabel = key => key === 'model_unsupported' ? '400/404 指定模型不支持' : Object.hasOwn(upstreamConditions, key) ? upstreamConditions[key] : key;

// The probe stores this exact marker for an upstream response. Management errors use a different format.
const upstreamHTTPStatus = /^upstream HTTP ([45]\d{2})$/;
function upstreamResponse(condition, error) {
  const status = upstreamHTTPStatus.exec(error || '')?.[1];
  if (condition === 'model_unsupported') {
    return status === '400' || status === '404' ? `上游 HTTP ${status} 指定模型不支持` : '上游已确认指定模型不支持';
  }
  if (Object.hasOwn(upstreamConditions, condition)) return `上游 HTTP ${upstreamConditions[condition]}`;
  if (status === '429') return '上游 HTTP 429（额度原因未确认）';
  return status ? `上游 HTTP ${status}` : '';
}

function runUpstreamResponses(run) {
  const samples = run.samples || [];
  const responses = samples.map(sample => upstreamResponse(sample.upstream_condition, sample.error));
  if (run.upstream_condition && !samples.some(sample => sample.upstream_condition === run.upstream_condition)) {
    responses.push(upstreamResponse(run.upstream_condition));
  }
  return [...new Set(responses.filter(Boolean))].join('、');
}

function admissionEvidence(run) {
  if (run.samples?.length || run.pruned_samples) return null;
  const preflight = run.preflight_detail, detail = preflight || run.skip_detail;
  const catalogModel = run.config?.catalog_missing_model;
  if (!detail && !catalogModel && run.status !== 'skipped') return null;
  const catalog = detail?.code === 'missing_model' ? !!(preflight || catalogModel) : !detail && !!catalogModel;
  const suspended = detail?.code === 'error_policy_disabled';
  const stopped = catalog && (run.block_reason || run.error ||
    (run.cancel_requested || run.status === 'cancelled' ? 'cancelled' : ''));
  const catalogResult = catalog && !stopped;
  const checked = catalogResult && ['completed', 'skipped'].includes(run.status);
  const identity = frozenRunIdentity(run);
  const object = identity.type === 'provider' ? '供应商路由' : identity.noun;
  let reason = detail?.reason;
  if (object !== '凭证') {
    if (reason === 'CPA 已核实该凭证没有注册所选模型。') reason = `CPA 已核实该${object}没有注册所选模型。`;
    if (reason === '当前目录没有这份规则绑定的凭证。') reason = `当前目录中找不到规则绑定的${object}。`;
  }
  return {
    id: detail?.auth_id || run.config?.auth_id, email: detail?.email || run.credential_email,
    model: detail?.model || catalogModel,
    status: checked ? tag('已检测') : statusTag(run.status),
    catalogResult,
    result: catalogResult ? 'CPA 目录未注册所选模型' : suspended ? '已按错误规则暂停，未重复检测' : '未发起模型请求',
    explanation: catalogResult ? 'CPA 目录未注册所选模型，本次未请求上游。' : suspended ? '本轮未重复答题。' : '未发起模型请求',
    source: catalog || detail?.code === 'missing_model' ? `CPA ${object}模型目录` : suspended ? '已确认的错误策略暂停' : preflight ? `当前${object}状态检查` : '范围调度准入',
    reasonLabel: stopped ? '未执行原因' : catalog || preflight ? '检查结果' : '跳过原因',
    reason: stopped ? reasonLabel(stopped) : reason || (catalog ? `${object}目录中缺少所选模型。` : '原因未记录'),
  };
}

function sampleStates(sample) {
  const status = sampleExecutionStatus(sample);
  const tone = status === 'failed' ? 'bad' :
    ['running', 'queued', 'incomplete', 'refused', 'empty'].includes(status) ? 'warn' : 'neutral';
  return `<span class="sample-state"><span class="sample-state-label">执行</span>${tag(statusLabels[status] || status || '未知', tone)}</span>
    <span class="sample-state"><span class="sample-state-label">答案</span>${verdictTag(qualitySampleOutcome(sample))}</span>`;
}

function renderFrozenConditions(run, credentialLabel) {
  const config = run.config || {};
  const identity = frozenRunIdentity(run);
  const question = config.question || {};
  const parameters = config.parameters || {};
  const judge = config.judge || {};
  const questionName = questionTitle(question, config.question_id);
  const promptText = question.prompt || config.prompt;
  if (run.source === 'manual_test' && judge.mode === 'manual') {
    const verdict = renderManualVerdict(run);
    const verdictLabel = run.pruned_samples > 0 || !roundCompletedCleanly(run) ? '留存回答判定' : '整体判定';
    const settings = [
      [identity.noun, credentialLabel], ['模型', config.models?.join(' / ')],
      ['题目', questionName], ['回答数量', config.samples],
      ...(['logic', 'random'].includes(question.kind) && question.answer != null && question.answer !== '' ? [['标准答案', question.answer]] : []),
      ['思考强度', parameters.reasoning_effort || '未指定'],
      ...(parameters.max_tokens == null ? [] : [['输出上限', `${parameters.max_tokens} token`]]),
      ...(parameters.temperature == null ? [] : [['Temperature', parameters.temperature]]),
      ['运行状态', statusLabels[run.status] || run.status],
    ];
    return `<dl class="answer-test-config">${settings.map(([label, value]) => evidenceFact(label, value)).join('')}
      ${verdict ? `<div class="answer-overall-verdict"><dt>${verdictLabel}</dt><dd>${verdict}</dd></div>` : ''}</dl>
      <details class="answer-test-prompt"><summary>查看本次题面</summary><pre class="condition-text">${escapeHTML(evidenceValue(promptText, '题面未记录'))}</pre></details>`;
  }
  return `<div class="condition-overview">${evidenceFacts([
    [`目标${identity.noun}`, credentialLabel], ['提供方', config.provider],
    ...(identity.type === 'provider' ? [['供应商名称', identity.name || '本轮未记录供应商名称']] : []),
    ['检测模型', config.models?.join(' / ')], ['每模型样本', config.samples],
    ['题目', questionName], ['判题方式', judgeName(judge.mode)],
    [hasRecordedSamples(run) ? '请求思考强度' : '计划思考强度', parameters.reasoning_effort || '未指定'],
    ...(parameters.max_tokens == null ? [] : [['输出上限', `${parameters.max_tokens} token`]]),
    ...(parameters.temperature == null ? [] : [['Temperature', parameters.temperature]]),
    ['流式请求', evidenceBoolean(parameters.stream, '启用', '关闭')],
  ])}</div>
  <details class="evidence-section"><summary>本次题目与参考答案</summary>
    <h4>题面</h4><pre class="condition-text">${escapeHTML(evidenceValue(promptText, '题面未记录'))}</pre>
    <h4>参考标准 / 参考答案</h4><pre class="condition-text">${escapeHTML(evidenceValue(config.review_standard || question.answer, '未提供；不能据此推断答案正确'))}</pre>
    <p class="field-note">参考答案用于判题，不发送给被测模型。</p>
  </details>
  <details class="evidence-section"><summary>检测身份与关联记录</summary>${evidenceFacts([
    [identity.idLabel, identity.id], ...(identity.type === 'provider_group' ? [] : [['路由索引', identity.index]]),
    [`${identity.noun}身份版本`, config.identity_version], ['冻结指纹', config.fingerprint],
    ['最终载荷配置版本', config.payload_config_version], ...(config.scope_batch_id ? [['范围批次', config.scope_batch_id]] : []), ...(config.batch_id ? [['对照批次', config.batch_id]] : []),
    ['对照凭证 ID', config.control_auth_id || '未配置'], ['对照身份', config.control_identity],
    ['判题凭证 ID', judge.auth_id || '未配置'], ['判题模型', judge.model || '未配置'],
    ['判题身份', config.judge_identity], ['随机种子', config.seed ?? question.seed],
    ['关联规则', config.rule_id || '无'], ['规则版本', config.rule_revision],
    ['判题模板', judge.template_version],
  ])}</details>
  ${evidenceJSON({ config, source: run.source, status: run.status, action_status: run.action_status, credential_action: run.credential_action, upstream_condition: run.upstream_condition, preflight_detail: run.preflight_detail, skip_detail: run.skip_detail, error: run.error || null })}`;
}

export function renderSampleEvidence(sample = {}, manual = false, credentialLabel = sample.evidence?.auth_id, supplier = false) {
  const evidence = sample.evidence || {};
  const copy = { ...sample };
  delete copy.output;
  const verdict = manual && answerVerdict(sample);
  const explanation = sample.error ? reasonLabel(sample.error) : verdict && sample.review ? '历史人工评审' : verdict && ['correct', 'incorrect'].includes(sample.answer_match) ? '按本次题目的标准答案核对' : reasonLabel(sample.reason);
  const response = upstreamResponse(sample.upstream_condition, sample.error);
  const findings = evidenceFacts([
    ['判题 / 执行说明', explanation],
    ...(response ? [['上游响应', response]] : []),
  ]);
  const route = evidenceFacts([
    ['选择的模型', sample.model || sample.request_model],
    ['请求模型', evidence.requested_model], ['宿主解析模型', evidence.resolved_model],
    ['上游响应标记', sample.response_model], [supplier ? '固定供应商' : '固定凭证', credentialLabel], ['执行路径', Object.hasOwn(probePaths, evidence.path) ? probePaths[evidence.path] : evidence.path],
  ]) + '<p class="field-note">上游返回的模型名称不构成身份认证。</p>';
  const metrics = [
    ['总耗时', duration(sample.total_ms)], ['首字时间（TTFT）', duration(sample.ttft_ms)],
    ['模型用量', formatSampleUsage(sample)],
  ];
  const review = [
    ['独立判题用量', formatSampleUsage({ usage: sample.judge_usage })],
    ['人工评审', reviewName(sample.review)], ['评审备注', sample.review_note || '无'],
  ];
  const diagnostics = `<details class="evidence-section"><summary>执行条件与诊断标识</summary>${evidenceFacts([
      ['执行条件说明', evidence.reason ? reasonLabel(evidence.reason) : null],
      ['网关执行条件可比较', evidenceBoolean(sample.comparable, '是', '否')],
      ['网关载荷已验证', evidenceBoolean(evidence.verified, '是', '否')],
      ['条件发生变化', evidenceBoolean(evidence.conditions_changed, '是', '否')],
      ['最终载荷配置版本', evidence.payload_config_version],
      ['最终载荷指纹', evidence.payload_fingerprint],
      ['原始条件说明', evidence.reason], ['原始判题 / 执行说明', sample.reason || sample.error],
    ])}</details>${evidenceJSON(copy)}`;
  if (!manual) return `<div class="evidence-verdict sample-statuses">${sampleStates(sample)}</div>${findings}
    <h3>模型与执行路径</h3>${route}<h3>耗时与用量</h3>${evidenceFacts([...metrics, ...review])}${diagnostics}`;
  const hasReview = sample.review || sample.review_note || sample.judge_usage;
  return `<section class="answer-evidence-result"><div class="evidence-verdict sample-statuses">${statusTag(sampleExecutionStatus(sample))}${verdict ? `<span data-verdict="${escapeHTML(verdict)}">${answerTag(sample)}</span>` : ''}</div>${findings}</section>
    <section class="answer-evidence-metrics" aria-label="耗时与用量">${evidenceFacts(metrics)}</section>
    <section class="answer-evidence-route"><h3>模型与执行路径</h3>${route}</section>
    ${hasReview ? `<details class="evidence-section"><summary>判题与评审</summary>${evidenceFacts(review)}</details>` : ''}${diagnostics}`;
}

function answerStatusIcon(tone) {
  const marks = { correct: 'm6.6 10 2.3 2.3 4.6-4.6', incorrect: 'm7.5 7.5 5 5m0-5-5 5', failed: 'M10 6.4v4.4m0 2.7v.1' };
  const mark = Object.hasOwn(marks, tone) ? marks[tone] : 'M7.2 10h5.6';
  return `<svg class="control-icon" viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7.4"/><path d="${mark}"/></svg>`;
}

function readingStatus(sample) {
  const verdict = answerVerdict(sample);
  const failed = sampleExecutionStatus(sample) === 'failed';
  const tone = verdict || (failed ? 'failed' : 'neutral');
  return `<div class="sample-statuses answer-status" data-verdict="${escapeHTML(tone)}">${answerStatusIcon(tone)}${answerTag(sample)}</div>`;
}

function qualitySampleStatus(sample) {
  const outcome = qualitySampleOutcome(sample);
  const label = outcome === 'unknown' ? '未判定' : verdictLabels[outcome] || statusLabels[outcome] || '执行状态未知';
  return `<span class="quality-sample-status" data-outcome="${escapeHTML(outcome)}">${answerStatusIcon(outcome)}${escapeHTML(label)}</span>`;
}

function renderAnswer(sample, recordIndex, ordinal, isActive, { control, simple, qualityDetail, reading, single, question, annotation = '' }) {
  const model = sampleModel(sample);
  const number = Number.isInteger(ordinal) && ordinal >= 0 ? ordinal + 1 : null;
  const label = single ? '模型回答' : `${control ? '对照 · ' : ''}${simple ? '回答' : '样本'}${number === null ? '（序号未知）' : ' ' + number}`;
  const output = sample.output || '';
  const preview = previewSource(output);
  const isHTML = !!preview;
  const inlinePreview = reading && isHTML && completedSample(sample) && output.length <= previewLimit;
  const previewTooLarge = isHTML && output.length > previewLimit;
  const partial = !!output.trim() && isPartialAnswer(sample);
  const isCode = isHTML || /^\s*(?:```|~~~|\/\/|#!|(?:const|let|var|function|import|class|def|package)\s|<\?xml)/m.test(output);
  const short = (reading || qualityDetail) && !isCode && output.length <= 24 && !!output.trim() &&
    !/[\r\n]/.test(output.trim()) && output.split(/\r\n|\r|\n/).length <= 2;
  const showReference = simple && short && sample.answer_match === 'incorrect' && !sample.review && answerVerdict(sample) === 'incorrect' &&
    ['logic', 'random'].includes(question?.kind) && typeof question.answer === 'string' && /^-?\d{1,20}$/.test(question.answer);
  const pending = ['pending', 'queued', 'running'].includes(sample.execution_status);
  const pendingText = reading || qualityDetail ? '正在等待模型返回内容。' : '正在等待回答，取得结果后会在这里更新。';
  const missingText = reading || qualityDetail ? '本次没有返回回答正文。' : '本次没有取得回答内容，请查看执行状态。';
  const review = sample.review || 'unknown';
  const idPrefix = qualityDetail ? 'quality-' : '';
  const statusHTML = qualityDetail ? qualitySampleStatus(sample) : simple ? readingStatus(sample) : `<div class="sample-statuses">${sampleStates(sample)}</div>`;
  const actions = control ? '' : `<div class="sample-actions" data-part="actions">
    ${!inlinePreview && (output.length > 1500 || output.split("\n").length > 12) ? `<button class="text-button" data-expand-sample="${recordIndex}" data-partial-output="${partial}" aria-expanded="false">${partial ? '展开已接收内容' : '展开完整回答'}</button>` : ''}
    <button class="text-button" data-copy-sample="${recordIndex}" ${!output ? 'disabled' : ''}>复制原文</button>
    ${isHTML && !previewTooLarge ? `<button class="text-button" data-preview-sample="${recordIndex}">${inlinePreview ? partial ? '放大部分作品' : '放大作品' : partial ? '预览部分 HTML / SVG' : '预览 HTML / SVG'}</button>` : ''}
    <button class="text-button" data-evidence-sample="${recordIndex}">${simple ? '执行详情' : '条件详情'}</button>
  </div>`;
  const reviewHTML = simple ? (sample.review ? `<details class="review-disclosure" data-part="review"><summary>历史人工评审</summary><p>${escapeHTML(reviewName(sample.review))}${sample.review_note ? ` · ${escapeHTML(sample.review_note)}` : ''}</p></details>` : '') : control ? `<details class="evidence-section" data-part="review"><summary>对照条件详情</summary>${renderSampleEvidence(sample)}</details>` :
    `<details class="review-disclosure" data-part="review"><summary>人工评审${sample.review ? ` · ${escapeHTML(reviewName(sample.review))}` : ''}</summary>
      <p class="field-note">评审单独留存，不触发自动暂停或解除。</p>
      <div class="sample-review"><label>结论<select aria-label="${escapeHTML(model + ' ' + label + ' ' + annotation)} 人工评审" id="${idPrefix}review-${recordIndex}">
        <option value="unknown" ${review === 'unknown' ? 'selected' : ''}>无法判断</option>
        <option value="correct" ${review === 'correct' ? 'selected' : ''}>正确</option>
        <option value="incorrect" ${review === 'incorrect' ? 'selected' : ''}>错误</option></select></label>
        <label>评审备注<input id="${idPrefix}review-note-${recordIndex}" maxlength="1000" placeholder="记录判断依据" aria-label="评审备注" value="${escapeHTML(sample.review_note || '')}"></label>
        <button class="subtle small" data-review-sample="${recordIndex}" ${isActive ? 'disabled' : ''}>保存评审</button>
      </div>${isActive ? '<p class="field-note">任务结束后可保存人工评审。</p>' : ''}
    </details>`;
  const reason = sample.error || (!simple || sample.reason !== 'manual assessment required' ? sample.reason : '');
  const response = upstreamResponse(sample.upstream_condition, sample.error);
  const reasonText = [response, reason && !(response && upstreamHTTPStatus.test(reason)) ? reasonLabel(reason) : '',
    partial ? `当前为已接收的部分内容，${isHTML ? '作品' : '回答'}可能不完整。` : ''].filter(Boolean).join(' · ');
  const sampleNumber = reading || number === null ? '' : `<span class="sample-number" aria-hidden="true">${String(number).padStart(2, '0')}</span>`;
  const answer = `<pre class="answer-text ${isCode ? 'answer-code' : 'answer-prose'}${(reading || qualityDetail) && !output ? ' answer-empty' : ''}${qualityDetail && short ? ' quality-short-answer' : ''}" data-part="output" tabindex="0" aria-label="${escapeHTML([model, label, annotation].filter(Boolean).join(' '))}${reading ? '' : ' 回答'}">${escapeHTML(output || (pending ? pendingText : missingText))}</pre>`;
  const artwork = inlinePreview ? `<iframe class="answer-preview" data-inline-preview="${recordIndex}" data-part="preview" title="${escapeHTML(`${model} ${label} · ${partial ? '部分作品' : '作品'}预览，可滚动查看`)}" sandbox="" referrerpolicy="no-referrer" tabindex="0"></iframe>
    <details class="answer-source" data-source-sample="${recordIndex}" data-part="source"><summary>查看原文</summary>${answer}</details>` : answer;
  const previewNote = previewTooLarge ? '<p class="field-note" data-part="preview-note">作品超过 1 MiB 预览上限，可查看或复制原文。</p>' : '';
  const referenceHTML = showReference ? `<dl class="answer-reference" data-part="reference"><dt>标准答案</dt><dd>${escapeHTML(question.answer)}</dd></dl>` : '';
  const usage = formatSampleUsage(sample);
  const measurements = [['总耗时', duration(sample.total_ms)], ['首字时间', duration(sample.ttft_ms)], ['用量', (reading || qualityDetail) && usage === '用量未知' ? '未知' : usage]];
  const visible = qualityDetail ? measurements.filter(([, value]) => value !== '未知') : measurements;
  const metrics = reading
    ? `<dl class="sample-meta" data-part="meta">${visible.map(([key, value]) => evidenceFact(key, value)).join('')}</dl>`
    : visible.length ? `<div class="sample-meta" data-part="meta">${visible.map(([key, value]) => `<span>${key} <strong>${escapeHTML(value)}</strong></span>`).join('')}</div>` : '';
  return `<article class="sample${reading ? ' answer-sample' : ''}${control ? ' control-sample' : ''}" data-key="${control ? 'control' : 'record'}:${recordIndex}" data-model="${escapeHTML(model)}" data-sample="${number === null ? '' : ordinal}">
    <div class="sample-heading" data-part="heading"><span class="sample-model-label" title="${escapeHTML(model)}">${escapeHTML(model)}</span><h4 class="sample-title">${sampleNumber}<span>${label}</span></h4>${annotation ? tag(annotation, 'warn') : ''}${statusHTML}</div>
    ${reading ? `<div class="answer-body${short ? ' short-answer' : ''}" data-part="body">${artwork}${previewNote}${referenceHTML}</div>` : answer + previewNote}
    <p class="sample-reason${reasonText ? '' : ' hidden'}" data-part="reason">${escapeHTML(reasonText)}</p>
    <div class="sample-footer" data-part="footer">${metrics}
    ${actions}</div>${reviewHTML}</article>`;
}

function renderAbsentSample(model, ordinal, isActive, pruned, unlocated, { simple, reading, single }) {
  let status = simple ? '回答缺失' : '样本缺失';
  let reason = simple ? '未取得这份回答，请查看执行状态。' : '本轮未取得该样本，不能作为完整失败证据。';
  if (pruned) {
    status = '历史已裁剪';
    reason = '这份回答已按规则的保留上限移除。';
  } else if (unlocated) {
    status = '无留存回答';
    reason = '旧记录未保存全部裁剪位置，无法确认此处是缺答还是已裁剪。';
  } else if (isActive) {
    status = '等待结果';
    reason = '后台按计划执行，结果取得后在此更新。';
  }
  if (reading) {
    const label = single ? '模型回答' : `${simple ? '回答' : '样本'} ${ordinal + 1}`;
    return `<article class="sample answer-sample sample-pending" data-key="slot:${ordinal}" data-model="${escapeHTML(model)}" data-sample="${ordinal}"><div class="sample-heading" data-part="heading"><span class="sample-model-label" title="${escapeHTML(model)}">${escapeHTML(model)}</span><h4 class="sample-title">${label}</h4><span class="answer-slot-state">${status}</span></div><p class="empty-line" data-part="pending">${reason}</p></article>`;
  }
  return `<article class="sample sample-pending" data-key="slot:${ordinal}" data-model="${escapeHTML(model)}" data-sample="${ordinal}"><div class="sample-heading" data-part="heading"><span class="sample-model-label" title="${escapeHTML(model)}">${escapeHTML(model)}</span><h4 class="sample-title"><span class="sample-number" aria-hidden="true">${String(ordinal + 1).padStart(2, '0')}</span><span>${simple ? '回答' : '样本'} ${ordinal + 1}</span></h4>${tag(status, isActive || pruned || unlocated ? 'neutral' : 'warn')}</div><p class="empty-line" data-part="pending">${reason}</p></article>`;
}

export function renderRunEvidence(run = {}, { qualityDetail = false } = {}) {
  const config = run.config || {};
  const skip = admissionEvidence(run);
  const identity = frozenRunIdentity(run);
  const supplier = identity.supplier;
  const credentialLabel = qualityDetail || run.source !== 'manual_test' ? qualityIdentityLabel(identity) : identity.label;
  const samples = run.samples || [];
  const manual = run.source === 'manual_test';
  const simple = manual && config.judge?.mode === 'manual';
  const isActive = active(run);
  const plannedModels = new Set(config.models?.length ? config.models : samples.map(sampleModel));
  const models = [...new Set([...plannedModels, ...samples.map(sampleModel)])];
  const count = Number.isInteger(config.samples) && config.samples > 0 ? config.samples : 1;
  const planned = plannedModels.size * count;
  const inPlan = sample => plannedModels.has(sampleModel(sample)) && Number.isInteger(sample.index) && sample.index >= 0 && sample.index < count;
  const completed = new Set(samples.filter(s => inPlan(s) && completedSample(s)).map(s => JSON.stringify([sampleModel(s), s.index]))).size;
  const pruned = run.pruned_samples || 0;
  const prunedSlots = run.pruned_sample_slots || [];
  const unlocated = pruned > prunedSlots.length;
  const { correct, incorrect, unknown } = qualityCounts(samples);
  const verdictSummary = [correct && `正确 ${correct}`, incorrect && `答错 ${incorrect}`, unknown && `无法判断 ${unknown}`].filter(Boolean).join(' · ');
  const reference = config.judge?.mode === 'deterministic' && config.question?.answer;
  const issue = run.error || run.block_reason || (manual && skip?.reason);
  const issueHTML = issue ? `<p class="notice${run.error || run.block_reason ? ' error' : ''}">${escapeHTML(manual ? manualIssueLabel(issue) : issue)}</p>` : '';
  const timestamp = date(run.created_at);
  const questionHeader = manual
    ? `<h3 class="answer-question">${escapeHTML(questionTitle(config.question, config.question_id))}</h3>
      <div class="answer-run-byline"><span class="answer-run-model"><svg class="control-icon" viewBox="0 0 20 20" aria-hidden="true"><path d="m10 2.5 6.5 3.7v7.6L10 17.5l-6.5-3.7V6.2L10 2.5ZM3.8 6.3l6.2 3.6 6.2-3.6M10 10v7"/></svg><span class="sr-only">模型 </span><bdi class="run-model">${escapeHTML(models.join(' / ') || '模型未记录')}</bdi></span>
        ${supplier ? `<span class="answer-run-supplier">供应商 <bdi>${escapeHTML(credentialLabel)}</bdi></span>` : ''}
        <span class="answer-run-time"><svg class="control-icon" viewBox="0 0 20 20" aria-hidden="true"><circle cx="10" cy="10" r="7.2"/><path d="M10 5.6v4.7l3 1.8"/></svg><span class="sr-only">测试时间 </span>${timestamp === '未知' ? '时间未记录' : `<time datetime="${escapeHTML(run.created_at)}">${escapeHTML(timestamp)}</time>`}</span>
        ${run.status !== 'completed' ? statusTag(run.status) : ''}</div>` : '';
  const operationSummary = simple || skip ? '' : `<div class="run-state-line">${manual ? '' : statusTag(run.status)}<strong>${run.pruned_samples ? `保留 ${samples.length} 份结果` : `${completed} / ${planned} 样本已结束`}</strong><span>${verdictSummary}</span></div>
    <div class="run-context-line"><span>${identity.noun} <strong>${escapeHTML(evidenceValue(credentialLabel))}</strong></span>${manual ? '' : `<span>${escapeHTML(questionTitle(config.question, config.question_id))}</span>`}${reference ? `<span>标准答案 <strong>${escapeHTML(reference)}</strong></span>` : ''}<span>${hasRecordedSamples(run) ? '请求强度' : '计划强度'} ${escapeHTML(config.parameters?.reasoning_effort || '未指定')}</span>${manual ? '' : `<span>本轮处置 · ${escapeHTML(credentialHandlingLabel(run))}</span>`}</div>`;
  const metaHTML = questionHeader + operationSummary + issueHTML;
  if (skip) return {
    metaHTML: manual ? metaHTML : skip.status,
    conditionsHTML: renderFrozenConditions(run, credentialLabel),
    samplesHTML: '', completed: 0, planned: 0,
  };
  const retained = samples.map((sample, i) => ({ sample, recordIndex: pruned + i }));
  const reading = run.source === 'manual_test' && !qualityDetail;
  const single = reading && simple && planned === 1 && samples.length + pruned <= 1 && samples.every(inPlan);
  const sampleOptions = { simple, qualityDetail, reading, single, question: config.question };
  const groups = models.map(model => {
    const entries = retained.filter(({ sample }) => sampleModel(sample) === model);
    const plannedCount = plannedModels.has(model) ? count : 0;
    const slots = Array.from({ length: plannedCount }, (_, ordinal) => entries.filter(({ sample }) => sample.index === ordinal));
    const content = slots.map((records, ordinal) => {
      const removed = prunedSlots.some(s => s.model === model && s.index === ordinal);
      if (!records.length) return renderAbsentSample(model, ordinal, isActive, removed, unlocated, sampleOptions);
      return records.map(({ sample, recordIndex }) => renderAnswer(sample, recordIndex, ordinal, isActive, {
        ...sampleOptions, annotation: records.length > 1 || removed ? `序号重复 · 原记录 ${recordIndex + 1}` : '',
      })).join('');
    }).join('');
    const extras = entries.filter(({ sample }) => !inPlan(sample)).map(({ sample, recordIndex }) => renderAnswer(sample, recordIndex, sample.index, isActive, {
      ...sampleOptions, annotation: `计划外记录 · 原记录 ${recordIndex + 1}`,
    })).join('');
    const occupied = slots.filter(records => records.length).length;
    const summary = plannedCount ? `${occupied} / ${plannedCount} 份结果${occupied === entries.length ? '' : ` · 保留 ${entries.length} 条记录`}` : `计划外模型 · 保留 ${entries.length} 条记录`;
    return `<section class="model-result" data-key="${escapeHTML(`model:${model}`)}" data-model="${escapeHTML(model)}"><div class="model-heading" data-part="model-heading"><div class="model-identity"><h3 title="${escapeHTML(model)}">${escapeHTML(model)}</h3><span>${summary}</span></div><button type="button" class="text-button model-copy" data-copy-model="${escapeHTML(model)}" aria-label="复制模型 ID ${escapeHTML(model)}">复制 ID</button></div>${content}${extras}</section>`;
  }).join('');
  const controlHTML = run.controls?.length ? `<details class="control-results" data-key="controls"><summary>同批对照 · ${run.controls.length} 份结果</summary><p class="field-note">只有身份不同、同题、同模型映射、同有效条件的通过样本才构成有效对照。</p><div class="comparison-grid">${run.controls.map((sample, index) => `<section class="model-result" data-key="${escapeHTML(`control-model:${index}:${sample.model || '模型未知'}`)}"><div class="model-heading" data-part="model-heading"><h3>${escapeHTML(sample.model || '模型未知')}</h3><span>对照凭证</span></div>${renderAnswer(sample, index, sample.index ?? index, isActive, { control: true, qualityDetail })}</section>`).join('')}</div></details>` : '';
  return {
    metaHTML,
    conditionsHTML: renderFrozenConditions(run, credentialLabel),
    samplesHTML: `<div class="comparison-grid${simple ? ` question-test-results${models.length === 1 ? ' single-question' : ''}` : ''}" data-key="models">${groups || '<p class="empty-line">本轮没有记录模型或样本。</p>'}</div>${controlHTML}`,
    completed,
    planned,
  };
}

export function renderQualityDetail(run, rules) {
  const config = run.config || {};
  const ruleID = run.rule_id || config.rule_id;
  const ruleName = rules.find(rule => rule.id === ruleID)?.name || ruleID || '规则检测';
  const { conditionsHTML, samplesHTML, completed, planned } = renderRunEvidence(run, { qualityDetail: true });
  const skip = admissionEvidence(run);
  const identity = frozenRunIdentity(run);
  const label = qualityIdentityLabel(identity);
  const path = skip?.source || recordedProbePath(run);
  const stamp = date(run.created_at);
  const header = `<header class="quality-detail-identity" data-key="identity"><strong><bdi>${escapeHTML(label)}</bdi></strong>
    ${identity.type === 'provider' ? `<div class="quality-detail-name">${escapeHTML(identity.name || '本轮未记录供应商名称')}</div>` : ''}
    ${identity.id && identity.id !== label ? `<code>${escapeHTML(identity.id)}</code>` : ''}
    <div class="quality-detail-byline"><span><span class="sr-only">检测规则 </span>${escapeHTML(ruleName)}</span>
      ${config.recovery_check ? '<span>恢复复检</span>' : ''}${path ? `<span>${escapeHTML(path)}</span>` : ''}
      ${stamp === '未知' ? '<span>时间未记录</span>' : `<time datetime="${escapeHTML(run.created_at)}">${escapeHTML(stamp)}</time>`}</div></header>`;
  const handling = evidenceFact('本轮处置', credentialHandlingLabel(run));
  const note = (title, value) => evidenceFact(title, value, 'quality-detail-note');
  const issue = run.error || run.block_reason || run.reason;
  const conflict = run.action_status === 'provider_config_conflict' ? note('配置写入', '快照或路由集合变化时会停止写入。若提交时 ETag 已过期，CPA 返回 HTTP 412，本次操作结束。') : '';
  const issueHTML = conflict + (issue && reasonLabel(issue) !== skip?.reason ? note('运行说明', reasonLabel(issue)) : '');
  const conditions = `<details class="quality-detail-conditions" data-key="config"><summary>检测配置与原始记录</summary>
    <div class="run-identifier">任务 ID <code>${escapeHTML(run.id || run.run_id || '未记录')}</code></div>${conditionsHTML}</details>`;
  if (skip) {
    return header + `<dl class="quality-detail-facts" data-key="checks">
      <div><dt>运行状态</dt><dd>${skip.status}<small>${escapeHTML(skip.explanation)}</small></dd></div>
      ${evidenceFact('判断依据', skip.source)}${handling}
      ${note(skip.reasonLabel, skip.reason)}${skip.model ? note('涉及模型', skip.model) : ''}
      ${Object.hasOwn(credentialActions, run.credential_action) ? note('处置动作', credentialActions[run.credential_action]) : ''}${issueHTML}
    </dl>${conditions}`;
  }

  const samples = run.samples || [];
  const answered = samples.filter(sample => sampleExecutionStatus(sample) === 'completed');
  const executionCounts = new Map();
  for (const sample of samples) {
    if (!completedSample(sample) || sampleExecutionStatus(sample) === 'completed') continue;
    const state = statusLabels[qualitySampleOutcome(sample)] || '执行状态未知';
    executionCounts.set(state, (executionCounts.get(state) || 0) + 1);
  }
  const executionIssues = [...executionCounts].map(([state, count]) => `${state} ${count}`).join(' · ');
  const { correct, incorrect, unknown } = qualityCounts(answered);
  const single = planned === 1 && samples.length === 1 && !run.pruned_samples;
  const verdict = [[correct, '正确', 'correct'], [incorrect, '错误', 'incorrect'], [unknown, '未判定', 'neutral']]
    .filter(([count]) => count).map(([count, name, tone]) => `<span data-outcome="${tone}">${name}${single ? '' : ' ' + count}</span>`).join('');
  const progress = run.pruned_samples ? `保留 ${samples.length} 份 · 已裁剪 ${run.pruned_samples} 份` : planned ? `${completed} / ${planned} 份样本已结束` : '';
  const response = runUpstreamResponses(run);
  const problem = (response ? note('上游响应', response) : '') + issueHTML;
  const deterministic = config.judge?.mode === 'deterministic';
  const reference = deterministic ? config.question?.answer : config.review_standard || config.question?.answer;
  let referenceHTML = '';
  if (reference != null && reference !== '') {
    const title = deterministic ? '标准答案' : '参考标准';
    referenceHTML = String(reference).length > 80 || /[\r\n]/.test(reference)
      ? `<details class="quality-detail-reference" data-key="reference"><summary>查看${title}</summary><pre class="condition-text">${escapeHTML(reference)}</pre></details>`
      : `<dl>${evidenceFact(title, reference)}</dl>`;
  }
  return header + `<dl class="quality-detail-facts" data-key="summary">
      <div><dt>${run.pruned_samples ? '留存答案判定' : '答案判定'}</dt><dd>${verdict || (active(run) ? '等待判定' : '暂无判定')}</dd></div>
      <div><dt>运行状态</dt><dd>${statusTag(run.status)}${progress ? `<small>${escapeHTML(progress)}</small>` : ''}${executionIssues ? `<small>${escapeHTML(executionIssues)}</small>` : ''}</dd></div>
      ${handling}${problem}</dl>
    <section class="quality-detail-answers" aria-label="模型回答" data-key="answers">
      <div class="quality-detail-question"><h3>${escapeHTML(questionTitle(config.question, config.question_id))}</h3>${referenceHTML}</div>
      ${samplesHTML}</section>${conditions}`;
}
// Pure operation views keep restriction ownership separate from rule state.
const restrictionLabels = {
  provider_route_unverified: '供应商路由尚未核实',
  provider_models_unavailable: '供应商模型目录不可用',
  unverified_inventory_entry: '目录记录来源尚未核实',
  manual_disabled: '人工禁用',
  operator_disabled: '人工禁用',
  quality_pause: '质量暂停',
  quality_paused: '质量暂停',
  native_cooldown: '原生冷却',
  cooldown: '原生冷却',
  unavailable: '当前不可用',
  native_unavailable: '宿主标记暂不可用',
  runtime_status_unavailable: '运行状态尚未确认',
  retained_pause_unverified: '历史暂停归属待核对',
};
const restrictionText = (reason) => restrictionLabels[reason] || String(reason ?? '未知限制');
const officialQualityUnavailable = (target) => target?.unsupported_reason === 'official_host_quality_pause_unavailable';
const operationFact = (label, value) =>
  `<div><dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value == null || value === '' ? '未知' : value)}</dd></div>`;
const restrictionList = (reasons, fallback) =>
  Array.isArray(reasons) && reasons.length
    ? `<ul class="restriction-list">${reasons.map((reason) => `<li>${escapeHTML(restrictionText(reason))}</li>`).join('')}</ul>`
    : `<p class="field-note">${escapeHTML(fallback)}</p>`;

function ruleTime(stamp = '') {
  // Keep the offset of this occurrence, including DST, independent of the browser zone.
  const match = /^(\d{4}-\d\d-\d\d)T(\d\d:\d\d:\d\d)(?:\.\d+)?(Z|[+-]\d\d:\d\d)$/.exec(stamp);
  if (!match || stamp.startsWith('0001-')) return '时间待确认';
  return `${match[1]} ${match[2]} · UTC${match[3] === 'Z' ? '' : match[3]}`;
}

export function renderQualityEmpty(title, description = '', action = '') {
  return `<div class="quality-empty"><svg class="control-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M5 3h14v18H5zM8 7h8M8 11h8M8 15h4"/></svg><strong>${escapeHTML(title)}</strong>${description ? `<span>${escapeHTML(description)}</span>` : ''}${action}</div>`;
}

export function ruleState(rule, targets) {
  if (holdsPause(rule, targets) || rule.enabled !== true || typeof rule.timer_active !== 'boolean') return 'stopped';
  return rule.timer_active ? 'timed' : 'manual';
}

export const ruleScheduleEnabled = rule => typeof rule.scheduling_enabled === 'boolean' ? rule.scheduling_enabled : rule.timer_active;

export function dynamicScopeLabel(scope) {
  return scope === 'all_credentials' ? '全部' : scope === 'all_enabled' ? '仅已启用' : '';
}

function handlingRecord(record, name, canAcknowledge, ruleID = '') {
  const source = record.error_source;
  const upstream = source?.condition || record.upstream_condition;
  const catalogModel = source?.catalog_missing ? source.model : record.catalog_missing_model;
  const resolved = record.state === 'disabled' || record.state === 'deleted';
  const evidence = catalogModel ? `CPA 注册目录缺少 ${catalogModel}` : upstream && upstreamLabel(upstream);
  const ended = ['restored', 'acknowledged', 'externally_enabled'].includes(record.state);
  const owner = record.answer_owner_rule_id || (!evidence && record.state === 'disabled' ? record.rule_id : '');
  const status = record.state === 'disabled' ? (evidence ? '已按错误规则停用，退出答题和恢复复检' : '已因答错停用，恢复归属保留')
    : record.state === 'deleted' ? '已移除凭证' : ended ? '本次处置关联已结束' : '处置待核对';
  const summary = [record.email || name(record.auth_id), evidence, source?.rule_id && `错误来源规则 ${source.rule_id}`,
    source?.model && `触发模型 ${source.model}`, source?.created_at && `错误记录于 ${date(source.created_at)}`,
    owner && `${ended ? '原答错恢复归属' : '答错恢复归属'} ${owner}`, status].filter(Boolean).map(escapeHTML).join(' · ');
  const check = `<button type="button" class="text-button" data-handling-ack="${escapeHTML(record.action_id)}" data-handling-auth="${escapeHTML(record.auth_id)}" data-handling-rule="${escapeHTML(ruleID)}" ${canAcknowledge ? '' : 'disabled title="请刷新核对规则与目录"'}>核对人工处理</button>`;
  const followup = record.state === 'disabled' ? '。在 CPA 手动启用后，正常状态读取会自动结束旧关联。' : record.state === 'deleted' ? '。不会自动重建凭证。' : '';
  return `<div data-key="handling:${escapeHTML(JSON.stringify([record.action_id, record.auth_id]))}"><span>${summary}${followup}</span>${resolved || ended ? '' : check}</div>`;
}

export function renderOrphanedHandling(records, name, { expanded, canAcknowledge, readError }) {
  if (!records.length) return '';
  const note = readError ? `<p class="field-note" role="status">${escapeHTML(readError)}</p>` : '';
  return `<div class="rule-activity-tools" data-key="orphaned-handling"><details data-orphaned-handling><summary>已删除规则的待核对处置（${records.length}）</summary>
    ${note}<div class="quality-handling-records">${expanded ? records.map(record => handlingRecord(record, name, canAcknowledge)).join('') : ''}</div></details></div>`;
}

export function renderScopeSkipDetails(dispatch) {
  const details = dispatch.details || dispatch.skip_details;
  const renderList = (rows, label) => `<ul class="scope-skip-list" tabindex="0" aria-label="${label}">${rows.map(row => {
    const subject = frozenRunIdentity({ skip_detail: row });
    const identity = subject.type === 'oauth' ? row.email || row.provider_name || row.auth_id || '对象标识未记录' : qualityIdentityLabel(subject);
    const id = row.auth_id && identity !== row.auth_id ? `<code>${escapeHTML(row.auth_id)}</code>` : '';
    const link = row.run_id ? `<button type="button" class="text-button small" data-run="${escapeHTML(row.run_id)}" aria-label="查看 ${escapeHTML(row.auth_id || identity)} 的详情">详情</button>` : '';
    return `<li><div><strong>${escapeHTML(identity)}</strong>${id}${link}</div><p>${row.model ? `<code>${escapeHTML(row.model)}</code> · ` : ''}${escapeHTML(row.reason || row.code || '原因未记录')}</p></li>`;
  }).join('')}</ul>`;
  const records = rows => Array.isArray(rows) ? rows.filter(row => row && typeof row === 'object' && !Array.isArray(row)) : [];
  const rows = records(details), checks = records(dispatch.preflight_details);
  if (!rows.length && !checks.length) {
    const disabled = dispatch.skipReasons?.manual_disabled;
    const aggregate = Number.isSafeInteger(disabled) && disabled > 0 ? `其中人工禁用 ${disabled} 个。` : '';
    return `<p class="field-note">${aggregate}本轮未记录逐个对象的明细。</p>`;
  }
  const note = rows.length < dispatch.skipped ? `<p class="field-note">已记录其中 ${rows.length} 个对象的明细。</p>` : '';
  return note + (rows.length ? renderList(rows, '未执行的对象和原因') : '') +
    (checks.length ? '<p class="field-note">执行前检查</p>' + renderList(checks, '执行前检查结果') : '');
}

export function renderScopeDispatch(dispatch, title, ruleID, { receipt = false, expanded = false } = {}) {
  const count = value => Number.isSafeInteger(value) && value >= 0 ? value : '未知';
  const at = dispatch.at ? ` <time datetime="${escapeHTML(dispatch.at)}">${escapeHTML(date(dispatch.at))}</time>` : '';
  const recovery = count(dispatch.recovery_count);
  const matched = count(dispatch.matched ?? dispatch.count);
  const normal = typeof matched === 'number' && typeof recovery === 'number' ? matched - recovery : matched;
  const counts = dispatch.candidates;
  const candidates = counts && typeof count(counts.oauth) === 'number' && typeof count(counts.provider) === 'number' ? `OAuth 账户 ${counts.oauth} · AI 提供商 ${counts.provider} · ` : '';
  const totals = dispatch.directory_unavailable ? '未执行 · 匹配数量待核实' :
    `${candidates}检测 ${normal} 个${typeof recovery === 'number' ? ' · 恢复复检 ' + recovery + ' 个' : ''} · 未执行 ${count(dispatch.skipped)} 个`;
  const countsHTML = `<span class="dispatch-counts">${totals}</span>${dispatch.reason ? `<span class="dispatch-counts">${escapeHTML(dispatch.reason)}</span>` : ''}`;
  const hasDetails = dispatch.skipped > 0 || dispatch.details?.length || dispatch.skip_details?.length || dispatch.preflight_details?.length;
  const header = `<span class="dispatch-heading">${escapeHTML(title)}${receipt ? at : ''}</span>${receipt ? countsHTML : ''}`;
  if (receipt && !hasDetails) return `<div class="scope-dispatch">${header}</div>`;
  const facts = receipt ? '' : `<p class="dispatch-facts">${at}${countsHTML}</p>`;
  const body = expanded ? facts + (hasDetails ? renderScopeSkipDetails(dispatch) : '') : '';
  return `<details class="scope-dispatch" data-part="dispatch-skips" data-skip-rule="${escapeHTML(ruleID)}"><summary>${header}</summary><div class="scope-skip-body">${body}</div></details>`;
}

export function renderRuleActivityContext(rule, name, { dispatchOpen, handlingOpen, canAcknowledge }) {
  const recent = rule.last_scope_batch
    ? `<button type="button" class="text-button small" data-rule-batch="${escapeHTML(rule.last_scope_batch)}">最近批次</button>`
    : rule.last_run ? `<button type="button" class="text-button small" data-run="${escapeHTML(rule.last_run)}">最近结果</button>` : '';
  let dispatch = '';
  if (rule.last_scope_dispatch) {
    dispatch = renderScopeDispatch(rule.last_scope_dispatch, '最近调度', rule.id, { expanded: dispatchOpen });
  } else if (rule.target_scope === 'all_credentials' && rule.last_manual_skips?.length) {
    const skipped = rule.last_manual_skips;
    const body = dispatchOpen ? renderScopeSkipDetails({ skipped: skipped.length, details:
      skipped.map(id => ({ auth_id: id, email: name(id), reason: '人工禁用' })) }) : '';
    dispatch = `<details class="scope-dispatch" data-part="legacy-skips" data-skip-rule="${escapeHTML(rule.id)}"><summary>上轮跳过 ${skipped.length} 个手动禁用凭证</summary><div class="scope-skip-body">${body}</div></details>`;
  }
  const handling = rule.handling?.length ? `<details data-part="handling" data-rule-handling="${escapeHTML(rule.id)}"><summary>处置记录（${rule.handling.length}）</summary>
    <div class="quality-handling-records">${handlingOpen ? rule.handling.map(record => handlingRecord(record, name, canAcknowledge, rule.id)).join('') : ''}</div></details>` : '';
  const due = rule.pending_schedule?.due;
  const catchup = due ? `<p class="field-note">待补检周期：${escapeHTML(ruleTime(due))}</p>` : '';
  return recent || dispatch || handling || catchup ? `<div class="rule-activity-tools" data-key="context:${escapeHTML(rule.id)}">${catchup}${recent}${dispatch}${handling}</div>` : '';
}

export function renderRuleRows(rules, targets, { connected, targetsFresh, settings = {}, scopeRule = '', busy, resourceStatus }) {
  if (!rules.length) return renderQualityEmpty('还没有检测规则', '创建一条规则，按需手动执行或开启定时检测。', '<button class="subtle" data-rule-new>创建第一条规则</button>');
  const officialHost = settings.capabilities?.mode === 'official_observation' || targets.some(officialQualityUnavailable);
  const rulesCurrent = resourceStatus?.rules !== 'error';
  const ready = connected && targetsFresh && rulesCurrent;
  return rules.map((rule) => {
    const config = rule.config || rule, held = holdsPause(rule, targets);
    const enabled = rule.enabled === true && !held, timed = rule.timer_active, scheduled = ruleScheduleEnabled(rule);
    const known = typeof rule.enabled === 'boolean' && typeof timed === 'boolean';
    const unsupported = rule.status === 'unsupported' || (officialHost && (rule.action === 'pause_credential' || rule.automatic_pause));
    const on = enabled && timed === true;
    const loginHold = ['credential_update_pending', 'credential_update_failed', 'credential_write_unknown', 'credential_identity_conflict'].includes(rule.status);
    const saving = busy?.has(`rule-timer:${rule.id}`);
    const label = held ? officialHost ? '历史归属待核对' : '等待人工解除' : unsupported ? '不支持' : !known ? '状态未确认' : scheduled && !on ? '定时未运行' : on ? '定时开启' : enabled ? '仅手动' : '已停止';
    const canSwitch = officialHost && known && ready && !held && !unsupported;
    const hint = saving ? '正在保存' : canSwitch ? scheduled ? (on ? '暂停定时检测' : '定时设置已保留但尚未运行；点击关闭已保存定时。') : '启用定时检测' : label;
    const blockedBy = held ? '目标或依赖凭证暂停，检测已停止' : !rulesCurrent ? '规则状态未确认，请刷新' : loginHold ? statusLabels[rule.status] : !ready ? '目录状态未确认，请刷新' : rule.timer_wait_reason || '';
    const id = escapeHTML(rule.id), selected = scopeRule === rule.id;
    const models = config.models || [];
    const scope = escapeHTML(ruleScopeDescription(rule, targets));
    const action = {
      observe_only: '仅记录', disable_credential: rule.auto_restore ? '暂停对象 · 整轮通过后恢复' : '暂停对象',
      delete_credential: '移除凭证', pause_credential: '独立质量暂停',
    }[rule.action] || '设置未确认';
    const upstream = configuredUpstreamActions(rule.upstream_error_actions);
    const title = escapeHTML(rule.name || rule.id);
    return `<article class="quality-rule${selected ? ' selected' : ''}" data-key="rule:${id}">
      <header class="quality-rule-head">
        <button type="button" class="quality-rule-select" data-rule-select="${id}" aria-pressed="${selected}" aria-controls="ops-activity" title="查看「${title}」的运行记录"><strong>${title}</strong></button>
        <button type="button" class="rule-state rule-timer-toggle" role="switch" aria-label="${title}的定时检测" aria-checked="${scheduled === true}" aria-busy="${!!saving}" data-rule-timer="${id}" title="${hint}" ${!canSwitch || saving ? 'disabled' : ''}><span class="rule-timer-track" aria-hidden="true"></span>${label}</button>
      </header>
      <dl class="quality-rule-config">
        <div class="quality-rule-scope"><dt>范围</dt><dd title="${scope}">${scope}</dd></div>
        <div><dt>模型</dt><dd class="quality-rule-models" title="${escapeHTML(models.join('、'))}">${models.length ? `<code>${escapeHTML(models[0])}</code>` : '未指定'}${models.length > 1 ? `<small>等 ${models.length} 个</small>` : ''}</dd></div>
        ${operationFact('答错时', action)}
        ${upstream.length ? `<div><dt>上游错误</dt><dd title="${escapeHTML(upstream.map(([condition, handling]) => condition + ' · ' + handling).join('，'))}">${upstream.length} 项处置策略</dd></div>` : ''}
        ${on ? operationFact(rule.timer_wait_reason && !rule.pending_schedule ? '待补检周期' : '下次执行', ruleTime(rule.next_run_local || rule.next_run || rule.next_run_at)) : ''}
      </dl>
      ${blockedBy ? `<p class="quality-rule-warning">${escapeHTML(blockedBy)}</p>` : ''}
      <footer class="quality-rule-actions">
        <button type="button" class="text-button" data-rule-edit="${id}">编辑</button>
        <button type="button" class="text-button" data-rule-delete="${id}" ${held || !ready ? 'disabled' : ''}>删除</button>
        <button type="button" data-rule-run="${id}" ${!ready || !enabled || unsupported || loginHold ? 'disabled' : ''}><svg class="control-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M6 4.75a1 1 0 0 1 1.5-.87l12.5 7.25a1 1 0 0 1 0 1.74L7.5 20.12A1 1 0 0 1 6 19.25Z"/></svg>立即执行</button>
      </footer>
    </article>`;
  }).join('');
}

function qualityRunResult(run) {
  const outcomes = new Map();
  for (const sample of run.samples || []) {
    const outcome = qualitySampleOutcome(sample);
    outcomes.set(outcome, (outcomes.get(outcome) || 0) + 1);
  }
  const correct = outcomes.get('correct') || 0, incorrect = outcomes.get('incorrect') || 0;
  if (fullyJudgedRound(run, recordedSampleCoverage(run), correct + incorrect)) return renderHistoryScore(correct, incorrect);
  const labels = { correct: '答对', incorrect: '答错', unknown: '未判定' };
  const facts = [];
  for (const [outcome, count] of outcomes) {
    facts.push(`<span class="run-outcome" data-outcome="${outcome}">${labels[outcome] || statusLabels[outcome]} ${count}</span>`);
  }
  if (run.pruned_samples > 0) facts.push(`<span class="run-outcome">已裁剪 ${escapeHTML(run.pruned_samples)}</span>`);
  if (run.status !== 'completed' && !outcomes.has(run.status)) {
    facts.unshift(statusTag(run.status));
  }
  return facts.join('') || '暂无判定';
}

function qualityRunRow(run, ruleName, clock, stamp, duplicateIndexes, duplicateLabels) {
  const config = run.config || {};
  const skip = admissionEvidence(run);
  const subject = frozenRunIdentity(run);
  const identity = qualityIdentityLabel(subject);
  const models = config.models || [];
  const model = models.length > 1 ? `${models[0]} 等 ${models.length} 个模型` : models[0] || '模型未记录';
  const effort = config.parameters?.reasoning_effort;
  const issue = skip?.reason || run.error || run.block_reason || run.reason;
  const reason = [skip?.model, skip?.catalogResult ? '未请求上游' : issue && reasonLabel(issue)].filter(Boolean).join(' · ');
  const response = !skip && runUpstreamResponses(run);
  const source = [config.recovery_check && '恢复复检',skip?.source || recordedProbePath(run)].filter(Boolean).join(' · ');
  const handling = credentialHandlingLabel(run);
  const showID = subject.type === 'provider' ? duplicateIndexes.has(subject.index)
    : subject.type === 'oauth' && (skip || duplicateLabels.has(identity)) && subject.id && subject.id !== identity;
  const idLabel = subject.idLabel;
  return `<tr class="quality-run-row" data-key="run:${escapeHTML(run.id || run.run_id)}">
    <td class="quality-identity-cell">
      <button type="button" class="quality-run-open" data-run="${escapeHTML(run.id || run.run_id)}" aria-haspopup="dialog"${subject.id ? ` title="${idLabel} ${escapeHTML(subject.id)}"` : ''} aria-label="查看 ${escapeHTML(identity)} 的运行详情${subject.id ? ` · ${idLabel} ${escapeHTML(subject.id)}` : ''} · ${escapeHTML(ruleName)} · ${escapeHTML(stamp)}"><bdi>${escapeHTML(identity)}</bdi><span aria-hidden="true">›</span></button>
      ${subject.supplier && subject.name ? `<div class="quality-run-name">${escapeHTML(subject.name)}</div>` : ''}
      ${showID ? `<small class="quality-run-auth-id">${subject.supplier ? idLabel + ' ' : ''}${escapeHTML(subject.id || '未记录')}</small>` : ''}
      <div class="quality-run-context"><span>${escapeHTML(ruleName)}</span><span title="${escapeHTML(models.join(' / '))}">${escapeHTML(model)}</span>${effort ? `<span>${hasRecordedSamples(run) ? '请求强度' : '计划强度'} ${escapeHTML(effort)}</span>` : ''}</div>
    </td>
    <td class="quality-result-cell" data-label="检测结果"><div class="quality-result">${skip ? `${skip.status}<span>${escapeHTML(skip.result)}</span>` : qualityRunResult(run)}${response ? `<span>${escapeHTML(response)}</span>` : ''}</div>
      ${reason ? `<small class="quality-run-reason" title="${escapeHTML(reason)}">${escapeHTML(reason)}</small>` : ''}</td>
    <td class="quality-handling-cell" data-label="本轮处置"><span data-handling="${escapeHTML(run.action_status)}">${escapeHTML(handling)}</span>${source ? `<small>${escapeHTML(source)}</small>` : ''}</td>
    <td class="quality-time-cell">${clock ? `<time datetime="${escapeHTML(run.created_at)}" title="${escapeHTML(stamp)}">${escapeHTML(clock)}</time>` : '未知'}</td>
  </tr>`;
}

export function renderQualityRuns(runs, rules) {
  if (!runs.length) return renderQualityEmpty('暂无运行记录', '手动或定时执行规则后，在这里查看检测结果。');
  const ruleNames = new Map(rules.map(rule => [rule.id, rule.name]));
  const routeIDs = new Map(), labelIDs = new Map();
  for (const run of runs) {
    const identity = frozenRunIdentity(run);
    if (!identity.supplier) {
      if (!labelIDs.has(identity.label)) labelIDs.set(identity.label, new Set());
      labelIDs.get(identity.label).add(identity.id);
    }
    if (!identity.index) continue;
    if (!routeIDs.has(identity.index)) routeIDs.set(identity.index, new Set());
    routeIDs.get(identity.index).add(identity.id);
  }
  const duplicateIndexes = new Set([...routeIDs].filter(([, ids]) => ids.size > 1).map(([index]) => index));
  const duplicateLabels = new Set([...labelIDs].filter(([, ids]) => ids.size > 1).map(([label]) => label));
  const days = new Map();
  for (const run of runs) {
    const stamp = date(run.created_at), [day, clock] = stamp.split(' ');
    const ruleID = run.rule_id || run.config?.rule_id;
    if (!days.has(day)) days.set(day, []);
    days.get(day).push(qualityRunRow(run, ruleNames.get(ruleID) || ruleID || '规则检测', clock, stamp, duplicateIndexes, duplicateLabels));
  }
  const groups = Array.from(days, ([day, rows]) => `<tbody data-key="day:${escapeHTML(day)}"><tr class="quality-run-day"><th scope="rowgroup" colspan="4">${day === '未知' ? '日期未记录' : escapeHTML(day)}</th></tr>${rows.join('')}</tbody>`).join('');
  return `<table class="quality-run-table" aria-label="规则检测运行记录"><thead><tr><th scope="col">对象与规则</th><th scope="col">检测结果</th><th scope="col">本轮处置</th><th scope="col">时间</th></tr></thead>${groups}</table>`;
}

export function renderTargetRows(targets) {
  if (!targets.length) return '<tr><td colspan="3" class="empty-line"><div class="calm-empty"><strong>没有可展示的凭证</strong><span>请连接并刷新，或先在 CPA 添加凭证。列表缺项不表示原有暂停已解除。</span></div></td></tr>';
  return targets.map((target) => {
    const reasons = target.reasons ?? target.unavailable_reasons;
    const officialHost = officialQualityUnavailable(target);
    const flags = [
      target.manual_disabled === true ? tag(target.credential_handling === 'disabled' ? '规则暂停' : '人工禁用', 'bad') : target.manual_disabled === false ? '' : tag('人工禁用状态未知'),
      target.quality_paused === true ? tag('质量暂停 · 整个凭证', 'warn') : target.quality_paused === false ? '' : tag('质量暂停状态未知'),
    ].filter(Boolean);
    const clearStates = [
      target.manual_disabled === false ? '未人工禁用' : '',
      target.quality_paused === false ? '无质量暂停' : '',
    ].filter(Boolean);
    const otherReasons = Array.isArray(reasons) ? reasons.filter((reason) =>
      !(target.manual_disabled === true && ['manual_disabled', 'operator_disabled'].includes(reason)) &&
      !(target.quality_paused === true && ['quality_pause', 'quality_paused'].includes(reason))) : null;
    const identity = targetID(target);
    return `<tr data-key="target:${escapeHTML(targetID(target))}">
      <td data-label="凭证"><div class="table-identity"><strong>${escapeHTML(target.is_provider ? providerTargetName(target) : target.email || target.auth_index || identity || '未知')}</strong>${target.auth_index && target.auth_index !== identity || !identity ? `<small class="table-id">${escapeHTML(identity || '标识未知')}</small>` : ''}<span class="target-provider">${escapeHTML(target.provider || '未知')}<span> · ${escapeHTML(({config_api_key:'配置密钥',auth_file:'认证文件',oauth:'OAuth'})[target.type] || target.type || '类型未知')}</span></span></div></td>
      <td data-label="可用状态">${tag(target.effective_available === true ? '可用' : target.effective_available === false ? '不可用' : '状态未知', target.effective_available === true ? 'good' : 'neutral')}</td>
      <td data-label="限制说明"><div class="target-restrictions">${flags.length ? `<div class="button-row target-restriction-flags">${flags.join('')}</div>` : ''}${clearStates.length ? `<small>${escapeHTML(clearStates.join(' · '))}</small>` : ''}${officialHost && target.quality_paused === true ? '<small>历史暂停归属仍保留</small>' : ''}${otherReasons === null ? '<p class="field-note">其他限制原因未知</p>' : otherReasons.length ? restrictionList(otherReasons, '') : ''}${!officialHost && target.unsupported_reason ? `<small>${escapeHTML(target.unsupported_reason)}</small>` : ''}</div></td>
    </tr>`;
  }).join('');
}

export function renderActionItems(actions, targets) {
  if (!actions.length) return '<div class="empty-line calm-empty"><strong>没有保留的质量暂停归属</strong><span>当前仅检测与观察。凭证是否可用，请查看凭证状态。</span></div>';
  const officialHost = targets.some(officialQualityUnavailable);
  return actions.map((action, index) => {
    const target = targets.find((entry) => targetID(entry) === action.auth_id);
    const owned = action.owner_plugin === 'model-quality';
    const retained = action.status === 'retained_unverified';
    // Target capability only admits a preview; the host still validates release authority.
    const capabilityConfirmed = !officialHost && target?.can_pause === true;
    const canPreview = capabilityConfirmed && owned && !!(action.action_id || action.id) && action.action_revision != null;
    const capabilityNote = officialHost ? '官方接口不支持创建或解除质量暂停。此历史归属记录继续保留。'
      : !target ? '凭证当前不可发现，解除能力尚未确认。此暂停归属记录继续保留。'
      : !capabilityConfirmed ? '宿主未确认独立质量暂停能力，暂不可解除。此暂停归属记录继续保留。' : '';
    return `<article class="action-item" data-key="action:${escapeHTML(action.action_id || action.id || action.auth_id)}">
      <div class="action-summary"><div class="button-row">${tag(retained ? '历史归属待核对' : '质量暂停', 'warn')}<strong>${escapeHTML(target?.email || target?.auth_index || action.auth_id || '凭证未知')}</strong></div><p>${escapeHTML(action.reason ? reasonLabel(action.reason) : '暂停原因未提供')}</p><small>${retained ? '归属记录已保留，当前宿主未验证暂停状态' : '影响整个凭证所有模型的新业务执行'} · ${escapeHTML(date(action.created_at || action.time))}</small><dl class="evidence-facts">${operationFact('暂停归属', action.owner_plugin)}${operationFact('检测规则', action.rule_id)}${operationFact('动作版本', action.action_revision)}</dl></div>
      <div class="action-decision">${action.evidence_run_id ? `<button class="text-button" data-run="${escapeHTML(action.evidence_run_id)}">查看失败证据</button>` : ''}${capabilityNote ? `<p>${escapeHTML(capabilityNote)}</p>` : ''}<p>${target?.manual_disabled ? '同时存在人工禁用；解除质量暂停后仍然生效。' : target ? '解除后相关检测规则仍停止，须另行恢复。' : '凭证当前不可发现；解除前需核对身份与剩余限制。'}</p><button class="danger small" data-action-preview="${index}" ${canPreview ? '' : 'disabled'}>${capabilityConfirmed ? '预览并解除自身暂停' : '解除质量暂停不可用'}</button>${!owned ? '<small>仅可解除本插件持有的暂停</small>' : ''}</div>
    </article>`;
  }).join('');
}

export function renderReleasePreview(preview, originalAction = {}) {
  const action = preview.action || originalAction;
  const targets = Array.isArray(preview.targets) ? preview.targets : preview.targets?.items || [];
  const target = targets.find((entry) => targetID(entry) === action.auth_id);
  const hasRemaining = Array.isArray(preview.remaining_reasons);
  const currentReasons = target?.reasons ?? target?.unavailable_reasons;
  const knownRemaining = hasRemaining ? [...preview.remaining_reasons] : (Array.isArray(currentReasons) ? currentReasons.filter((reason) => !['quality_pause', 'quality_paused'].includes(reason)) : []);
  if (target?.manual_disabled && !knownRemaining.includes('manual_disabled')) knownRemaining.push('manual_disabled');
  const conflict = preview.conflict === true || preview.blocked === true || preview.can_release === false;
  return `${conflict ? '<p class="notice error">当前暂停无法解除。状态或版本已变化，请关闭并刷新后重新预览。</p>' : ''}
    <section class="release-scope"><h3>${escapeHTML(target?.email || target?.auth_index || action.auth_id || '影响凭证未知')}</h3><p>仅释放下面这一条归属匹配的质量暂停，作用范围为整个凭证的所有模型。</p><dl class="evidence-facts">${operationFact('凭证标识', action.auth_id)}${operationFact('暂停归属', action.owner_plugin)}${operationFact('关联规则', action.rule_id)}${operationFact('动作标识', action.action_id || action.id)}${operationFact('动作版本', action.action_revision)}${operationFact('凭证身份版本', action.identity_version)}</dl></section>
    <section class="release-scope"><h3>解除后仍需处理</h3>${restrictionList(knownRemaining, hasRemaining ? '预览未报告其他限制；最终可用状态以解除后的宿主查询为准。' : '剩余限制尚不能完整确定，解除后须重新读取宿主状态。')}<p class="field-note">人工禁用、原生冷却与其他归属的暂停继续生效。当前质量暂停标记不能证明只有一个暂停归属。</p></section>
    <details><summary>原始解除预览 JSON</summary><pre>${escapeHTML(JSON.stringify(preview, null, 2))}</pre></details>`;
}
