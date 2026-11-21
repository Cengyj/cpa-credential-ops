import { escapeHTML, date } from './view.js';
import { isDiscarded } from './client.js';

const labels = {
  queued: '排队中', running: '登录中', waiting_browser: '等待浏览器授权', exchanging: '兑换授权',
  validating: '核验身份', awaiting_commit: '等待确认导入', committing: '保存中', committed: '已保存',
  saved_not_adopted: '已保存，尚未采用', commit_unknown: '提交结果待核对', interaction_required: '登录未完成',
  failed: '登录失败', conflict: '身份或状态冲突', cancelled: '已取消', cancelling: '正在取消',
  discarded: '已放弃保存', expired: '已过期', interrupted: '已中断', succeeded: '操作完成', security_uncertain: '安全操作待核对',
};
const activeStates = new Set(['queued', 'running', 'waiting_browser', 'exchanging', 'validating', 'committing', 'cancelling']);
const cancellableStates = new Set(['queued', 'running', 'waiting_browser', 'exchanging', 'validating']);
const reasons = {
  invalid_request: '请求格式不正确，请核对输入后重新操作。',
  cpa_plan_excluded: '套餐命中 CPA 上传排除设置，授权已保留，未写入 CPA。调整设置后可在本次会话中手动确认上传。',
  invalid_proxy: '代理地址无效。请填写 HTTP、HTTPS、SOCKS5、SOCKS5H 地址，或 direct 直连；不要包含路径、查询参数或片段。',
  proxy_auth_unsupported: '登录服务无法完成代理认证，请核对代理用户名、密码和插件版本。',
  invalid_credentials: '请填写邮箱、密码和 2FA 长期密钥。',
  credentials_rejected: '密码被拒绝，请核对后手动创建新的登录任务。',
  unsupported_login_target: '此凭证来源不支持重新授权。',
  unsupported_login_store: '当前 CPA 不支持保存此类凭证。',
  quality_authority_unavailable: 'CPA 状态尚未就绪，暂时无法导入凭证。',
  quality_reconciliation_pending: '凭证提交已确认，规则等待本地回执核对。',
  retained_quality_pause: '此凭证保留历史独立质量暂停，当前授权写入受保护。',
  credential_handling_pending: '此凭证仍有待核对的处置写入，请先核对原处置结果。',
  quality_credential_restricted: 'CPA 当前条目仍受质量处置限制，请查看凭证状态和处置记录。',
  home_controlled: '当前 Home 管理模式不支持此操作。',
  runtime_invalid: '登录服务校验失败，请检查插件安装。',
  runtime_failed: '登录请求未完成，请查看最后一个请求的结果。',
  runtime_timeout: '登录等待超时，未自动重试。',
  runtime_protocol_error: '登录服务返回的结果无效，未保存授权。',
  rate_limited: '此账号已达到手动登录频率限制，请稍后再试。',
  identity_conflict: '本次登录身份与目标凭证不一致，未更新凭证。',
  login_commit_pending: '此凭证有尚未确认的保存结果，请先核对 CPA 中的凭证状态。',
  invalid_callback: 'OAuth 回调校验未通过，未交换或保存凭证。请查看任务中的具体原因。',
  already_consumed: '此授权回调已经处理，请刷新任务核对结果。',
  exchange_failed: '官方授权兑换未完成，未自动重试。',
  storage_unavailable: '无法保存登录记录，请检查 CPA 状态。',
  storage_capacity: '登录记录已满，请联系管理员整理记录后重试。',
  vault_conflict: '保存资料正在使用或版本已变化，请等待任务完成后刷新。',
  vault_corrupt: '登录资料无法读取，请检查账号存储和备份。',
  vault_identity_changed: '保存资料对应的凭证身份已变化，请更新资料后再使用。',
  idempotency_conflict: '请求标识已用于不同操作，请刷新任务核对。',
  idempotency_unknown: '上次请求结果尚未确认，请刷新任务核对，避免重复登录。',
  interrupted: 'CPA 重启后任务已中断，未自动重试。',
  runtime_unavailable: '登录服务尚未就绪，请检查插件安装。',
  runtime_hash_mismatch: '登录服务校验失败，请检查插件安装。',
  runtime_unsupported: '当前平台不支持此登录服务。',
  unsupported_method: '当前 CPA 不支持此登录方式。',
  unsupported_credential_source: '此凭证来源不支持重新授权。',
  unsupported_source: '此凭证来源不支持重新授权。',
  unsupported_provider: '此凭证不是受支持的 Codex OAuth 凭证。',
  credential_source: '此凭证来源不支持重新授权。',
  home_unsupported: '当前 Home 管理模式不支持此操作。',
  plugin_unavailable: '插件暂不可用，请重新连接后核对。',
  capability_unavailable: '登录服务暂不可用，请检查插件安装和配置。',
  password_rejected: '密码被拒绝，请核对后手动创建新的登录任务。',
  totp_rejected: '二次验证被拒绝，请核对 2FA 长期密钥和本机时间。',
  invalid_totp: 'TOTP 密钥格式或参数不受支持。请使用 Base32 或标准 SHA1 / 六位 / 30 秒的 TOTP URI。',
  malformed: '输入格式不正确，请核对后重试。',
  interaction_required: '自动登录未完成，请根据任务记录核对停止步骤。',
  identity_changed: '登录期间凭证身份已变化，请重新读取并核对目标。',
  policy_changed: '登录期间凭证设置已变化，未覆盖修改。请核对 CPA 状态后重新登录。',
  credential_enable_conflict: '授权文件已保存，但 CPA 尚未确认启用。请核对凭证状态，勿重复登录或导入。',
  duplicate_identity: '该身份已有凭证，请选择已有凭证重新授权。',
  identity_mismatch: '本次登录身份与目标凭证不一致，未更新凭证。',
  identity_unverifiable: '授权未提供足够的身份信息，无法安全导入。',
  workspace_mismatch: '授权工作区与目标凭证不一致。',
  commit_in_progress: '凭证正在提交，请刷新任务核对结果。',
  revision_conflict: '任务状态已更新，请刷新后重新核对。',
  conflict: '状态已变化，请刷新任务并重新预览。',
  queue_full: '登录队列已满，请稍后手动重试。',
  login_budget: '此账号已达到手动登录频率限制，请稍后再试。',
  already_running: '此账号已有登录任务，请查看现有任务。',
  vault_locked: '本机登录资料不可用，请检查存储文件和访问权限。',
  vault_unavailable: '登录资料暂不可用，请检查 CPA 存储。',
  vault_not_initialized: '当前 CPA 不支持保存登录资料，请更新 CPA。',
  vault_save_failed: '授权已提交，但登录资料保存失败。不要重复登录，请核对资料库。',
  store_unavailable: '无法保存凭证，请检查 CPA 存储。',
  callback_invalid: '回调地址不符合本次授权会话，请复制完整回调地址。',
  state_mismatch: '回调与本次授权会话不一致。',
  expired: '会话已过期，请创建新的登录任务。',
  cancelled: '登录任务已取消。',
  network_error: '认证网络请求失败，未自动重试。',
  management_network_error: '无法连接 CPA 管理接口，请检查连接后刷新状态。',
  management_offline: '浏览器已离线。恢复连接并刷新状态后再操作。',
  authorization_failed: '官方授权未完成，请核对账号状态后手动重试。',
  timeout: '登录等待超时，未自动重试。',
  account_exists: '此账号已保存，请在账号列表中更新资料。',
  duplicate_account: '导入内容含重复账号，或账号已经存在，请核对后再保存。',
  account_limit: '已达到账号数量上限，请先整理不再使用的账号。',
  account_busy: '此账号正在登录或提交，请等待任务结束后再修改。',
  account_unbound: '此账号尚未绑定 CPA 凭证，请先点击 2FA 登录。',
  account_not_linked: '此账号尚未绑定 CPA 凭证，请先点击 2FA 登录。',
  cpa_credential_missing: '此账号绑定的 CPA 凭证已找不到。请核对 CPA 凭证列表。',
  cpa_credential_unavailable: '无法读取 CPA 凭证，已停止上传。',
  not_found: '该记录已不存在，请刷新列表后核对。',
  automation_disabled: '自动授权已关闭，本次自动任务不再继续。',
  security_failed: '安全操作未完成，请根据停止步骤核对原因。',
  security_journal_invalid: '本地安全操作记录无法核实。请保留现有记录和备份，勿重复修改账号。',
  security_unavailable: '当前插件不支持账号安全操作，请更新插件。',
  security_storage_failed: '安全操作记录未能可靠保存，已停止后续步骤。',
  security_outcome_unknown: '安全操作可能已生效，但结果尚未确认；不会自动重试。',
  security_rejected: '官方接口拒绝了本次安全操作。',
  security_state_unconfirmed: '无法确认账号当前的 2FA 状态，已停止修改。',
  security_pending: '此账号有未确认的安全操作。请先查看任务和账号资料中的核对信息，勿重复重置或登录。',
  security_uncertain: '安全操作可能已生效，但结果尚未确认。请查看任务与账号资料中的核对信息，勿重复操作。',
  security_action_unsupported: '当前插件不支持此安全操作，请更新后重试。',
  security_identity_mismatch: '安全会话身份与当前账号不一致，已停止操作。',
  security_identity_unverified: '无法核实安全会话对应的账号，未继续修改。',
  security_checkpoint_failed: '安全操作的本地记录未能可靠保存，已停止后续步骤。',
  totp_save_failed: '新 2FA 尚未可靠保存。请查看账号资料中的待核对密钥与任务结果，勿重复重置。',
  totp_factor_ambiguous: '无法确认要重置的 2FA 验证方式，未执行重置。',
  logout_failed: '退出所有设备未完成，请查看任务记录。',

};
const planLabels = { free: 'Free', go: 'Go', plus: 'Plus', pro: 'Pro', prolite: 'Pro Lite', business_premium: 'Business Premium', team: 'Team', enterprise: 'Enterprise', edu: 'Edu', unknown: '未知套餐' };
const planType = value => Object.hasOwn(planLabels, value) ? value : 'unknown';
const planLabel = value => planLabels[planType(value)];
const idOf = job => String(job?.id || job?.job_id || '');
const isSecurityJob = job => ['reset_totp', 'logout_all'].includes(job?.action);
const operationLabel = job => job?.action === 'reset_totp' ? '重置 2FA' : job?.action === 'logout_all' ? '退出所有设备' : job?.intent === 'reauthorize' ? '重新授权' : job?.intent === 'import' ? '首次授权' : '登录授权';
const securityResult = job => [job?.totp_reset ? '新 2FA 已保存' : '', job?.devices_logged_out ? '已退出所有设备' : ''].filter(Boolean).join(' · ');
const jobLabel = job => job?.error_code === 'cpa_plan_excluded' && job.status === 'awaiting_commit' ? '套餐已排除'
  : job?.status === 'awaiting_commit' && job.account_id ? '待上传'
  : job?.status === 'committed' && job.credential_enabled ? '已保存并启用'
  : job?.status === 'committing' && job.enable_credential ? '保存并启用中'
  : job?.status === 'succeeded' && isSecurityJob(job) ? '安全操作已完成'
  : job?.status === 'running' && isSecurityJob(job) ? '安全操作中'
  : job?.status === 'failed' && isSecurityJob(job) ? securityResult(job) ? '后续步骤未完成' : '安全操作未完成'
  : labels[job?.status] || '待核对';
const callbackReasons = {
  callback_url_invalid: 'OAuth 回调地址与本次授权不一致或格式无效',
  callback_parameters_invalid: 'OAuth 回调参数格式无效或关键参数重复',
  callback_state_mismatch: 'OAuth 回调的 state 与本次授权不匹配',
  callback_code_invalid: 'OAuth 回调缺少有效授权码',
  callback_authorization_denied: '授权服务拒绝了本次 OAuth 授权',
};
const networkReasons = {
  proxy_auth_rejected: '代理认证被拒绝（407）', proxy_loopback_unreachable: '容器无法连接回环代理',
  proxy_tls_failed: '代理 TLS 连接失败', proxy_dns_failed: '代理地址无法解析', network_dns_failed: '认证服务域名无法解析',
  proxy_connection_refused: '代理拒绝连接', network_connection_refused: '认证服务拒绝连接',
  proxy_connection_timeout: '连接代理超时', network_timeout: '认证网络请求超时', network_tls_failed: '认证服务 TLS 连接失败',
};
// getRandomValues is also available on remotely hosted HTTP management panels.
const key = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
const messageFor = code => reasons[code] || '登录操作未完成，请刷新任务核对状态。';
const safeAuthorizationURL = value => {
  try {
    const url = new URL(value);
    return url.protocol === 'https:' && url.hostname === 'auth.openai.com' && !url.port && !url.username && !url.password && url.pathname === '/oauth/authorize' ? url.href : '';
  } catch { return ''; }
};

function readWorkflowPreference(workflow) {
  try {
    const saved = JSON.parse(localStorage.getItem(`model-quality.workflow.${workflow}.v1`));
    return {
      logout: saved?.logout === true,
      reauthorize: saved?.reauthorize === true,
      upload: saved?.upload === true && (workflow === 'login' || saved?.reauthorize === true),
    };
  } catch {
    return { logout: false, reauthorize: false, upload: false };
  }
}
function saveWorkflowPreference(workflow, choices) {
  try {
    localStorage.setItem(`model-quality.workflow.${workflow}.v1`, JSON.stringify(choices));
  } catch { /* The current dialog remains usable if browser storage is blocked. */ }
}

// Authentication inputs remain in this workbench only until its request is sent.
export function createLoginWorkbench({ request, connected, visible = () => !document.hidden && navigator.onLine !== false, onCommitted }) {
  const $ = id => document.getElementById(id);
  const state = { open: false, epoch: 0, capabilities: null, targets: [], jobs: [], selected: '', job: null, candidate: null, vault: null, timer: null, busy: new Set(), urls: new Map(), detailSignature: '', storageRead: 0, detailRead: 0, jobsRead: 0, view: 'accounts', accounts: [], accountSettings: null, accountRead: 0, accountsAvailable: false, accountSignature: '', accountEditing: null, accountStarting: '', accountDetailFailed: false, settingsDirty: false, settingsDraftRevision: null, accountDetailRead: 0, accountDetailLoading: false, accountQuery: '', accountFilter: 'all', accountPage: 0, jobSignature: '', revealJob: false, accountsLoaded: false, jobsAvailable: false, bootstrapReady: false, readErrors: new Map(), nextReadAt: 0, readFailures: 0 };
  const get = path => request(path);
  let snapshotReads = new Map();
  function snapshotRead(path) {
    if (snapshotReads.has(path)) return snapshotReads.get(path);
    const pending = get(path).finally(() => {
      if (snapshotReads.get(path) === pending) snapshotReads.delete(path);
    });
    snapshotReads.set(path, pending);
    return pending;
  }
  function invalidateSnapshot() {
    state.nextReadAt = 0;
    snapshotReads.clear();
  }
  const write = async (path, body, method = 'POST') => {
    if (navigator.onLine === false) {
      const error = new Error('The management connection is offline.');
      error.code = 'management_offline';
      throw error;
    }
    const session = snapshotReads;
    invalidateSnapshot();
    try { return await request(path, method, body, key()); }
    finally { if (snapshotReads === session) invalidateSnapshot(); }
  };
  const current = epoch => state.open && state.epoch === epoch && connected();
  const accountLoginAvailable = () => state.capabilities?.account_login_available ?? (state.capabilities?.methods?.password_totp?.available === true && state.capabilities?.available !== false);
  const localStorageReady = vault => vault.available !== false && vault.initialized === true && vault.storage_mode === 'plaintext';
  const storageMessage = vault => vault.available === false ? messageFor(vault.reason || 'vault_unavailable') : '当前 CPA 不支持保存账号资料，请更新后重试。';
  let loading = false;
  let refreshCycle = null;
  let accountLogCycle = null;
  const accountLogs = { account: '', scope: 'current', selected: '', query: '', filter: 'all', follow: true, cache: new Map(), read: 0, loading: false, error: '', updated: '', signature: '', row: null, jobs: [], hasMore: false };
  const taskLogs = { cache: new Map(), query: '', filter: 'all', follow: false, loading: false, error: '', updated: '', read: 0, signature: '', cycle: null };
  const taskList = { query: '', filter: 'all', hasMore: false };
  let historyDeleteTarget = null;
  let accountLoginTarget = null;
  let importOpener = null;
  let surfaceOpener = null;
  let editorOriginID = '';
  let editorCredentials = null;
  let deleteTarget = null;
  let menuAccountID = '';
  let securityAccount = null;
  let securityAction = '';
  let securitySubmissionUncertain = false;
  let securityRecoveryJob = '';
  let securityRecoveryCompletable = false;
  const convertEl = name => $('account-convert-' + name);
  const convertLimit = 2 * 1024 * 1024;
  const conversion = { items: [], source: '', fileName: '', busy: '', sequence: 0 };
  const retryableConversion = new Set(['cpa_plan_excluded', 'security_pending', 'quality_authority_unavailable', 'plugin_unavailable', 'cancelled']);
  const conversionMessages = {
    conversion_invalid_json: 'JSON 无效、字段重复或内容过大，请检查导出文件。', conversion_unsupported_format: '未识别到 sub2api 账号或凭证，请使用包含 credentials 的导出 JSON。',
    conversion_unsupported_version: '不支持此导出版本。', conversion_account_limit: '每次支持 1–100 个账号。', conversion_unsupported_provider: '仅支持 OpenAI / Codex。', conversion_unsupported_type: '仅支持 OAuth 凭证，不支持 API Key 或 Cookie。',
    conversion_missing_access_token: '缺少 access_token。', conversion_missing_account_id: '缺少 ChatGPT 账号 ID，token 中也未找到。', conversion_invalid_token: 'Token 格式无效，无法解析账号信息。', conversion_invalid_expiry: '凭证有效期格式无效。', conversion_expired_without_refresh: 'Access token 已过期且没有 refresh_token。',
    invalid_credentials: '凭证字段格式不正确。', duplicate_identity: '此账号已有凭证，未覆盖或重复上传。', identity_conflict: '凭证与 token 中的账号信息不一致。', identity_unverifiable: 'Token 缺少稳定的账号或用户身份，无法安全上传。',
    cpa_plan_excluded: '此套餐被 CPA 上传策略排除。', security_pending: '此账号有进行中或待核对的操作，请先处理。', commit_unknown: '上传结果待核对，请在 CPA 凭证列表确认，勿重复上传。', saved_not_adopted: '文件可能已保存，运行状态待核对，勿重复上传。',
    conversion_duplicate_source: '文件内已有相同身份的凭证。', conversion_already_present: 'CPA 已有相同凭证。', conversion_previously_uploaded: '已核对上次上传的凭证。', conversion_previous_upload_changed: '上次上传后的凭证已变化或移除，请在 CPA 核对。',
    missing_email: '未提供邮箱', missing_expiry: '有效期未知', no_refresh_token: '无 refresh_token，到期后无法自动刷新', access_token_expired: 'Access token 已过期，由 CPA 按需刷新', kept_disabled: '保留禁用状态',
  };
  const conversionMessage = code => conversionMessages[code] || reasons[code] || '请在 CPA 凭证列表核对该条记录。';
  function conversionFeedback(message = '', tone = '') {
    const el = convertEl('feedback');
    el.textContent = message;
    el.dataset.tone = tone;
    el.hidden = !message;
  }
  function canSelectConversion(item) {
    return item.status === 'ready' || (item.status === 'failed' && retryableConversion.has(item.code));
  }
  function updateConversionControls() {
    const { busy, items, fileName } = conversion;
    const uploading = busy === 'upload';
    const selected = items.filter(item => item.selected && canSelectConversion(item)).length;
    for (const id of ['source', 'file', 'file-open', 'edit', 'close', 'cancel']) convertEl(id).disabled = uploading;
    convertEl('preview').disabled = !!busy || !connected() || !convertEl('source').value.trim();
    convertEl('upload').disabled = !!busy || !connected() || !selected;
    convertEl('preview').textContent = busy === 'preview' ? '正在转换…' : '预览转换结果';
    convertEl('upload').textContent = uploading ? '正在上传…' : selected ? `上传 ${selected} 个凭证` : '上传到 CPA';
    convertEl('count').textContent = `已选 ${selected} 个`;
    convertEl('count').hidden = !items.length;
    convertEl('file-name').textContent = fileName;
    convertEl('file-name').hidden = !fileName;
    convertEl('dialog').setAttribute('aria-busy', String(!!busy));
    for (const el of convertEl('list').querySelectorAll('[data-convert-select]')) {
      el.disabled = !!busy || !canSelectConversion(items[Number(el.dataset.convertSelect)]);
    }
    for (const el of convertEl('list').querySelectorAll('[data-convert-download]')) el.disabled = !!busy;
  }
  function renderConversion() {
    const { items } = conversion;
    convertEl('input').hidden = !!items.length;
    convertEl('results').hidden = !items.length;
    convertEl('upload').hidden = !items.length;
    const counts = new Map();
    for (const item of items) counts.set(item.status, (counts.get(item.status) || 0) + 1);
    convertEl('results-title').textContent = items.some(item => !['ready', 'invalid'].includes(item.status)) ? '上传结果' : '凭证预览';
    const labels = { ready: '可上传', uploaded: '已上传', existing: '已存在', invalid: '无法转换', failed: '未上传', uncertain: '待核对' };
    convertEl('summary').textContent = [`共 ${items.length} 条`, ...Object.entries(labels).filter(([status]) => counts.has(status)).map(([status, label]) => `${counts.get(status)} 条${label}`)].join(' · ');
    convertEl('list').innerHTML = items.map((item, index) => {
      const name = item.email || item.name || `凭证 ${item.index + 1}`;
      const meta = [item.name !== name && item.name, item.plan_type && item.plan_type !== 'unknown' && planLabel(item.plan_type)].filter(Boolean).join(' · ');
      let status = labels[item.status];
      if (item.status === 'uploaded') status = item.enabled ? '已上传并启用' : '已上传，保持禁用';
      const issue = item.code || item.error;
      const notes = [];
      if (issue === 'conversion_duplicate_source' && Number.isInteger(item.duplicate_of)) notes.push(`与第 ${item.duplicate_of + 1} 条凭证重复。`);
      else if (issue) notes.push(conversionMessage(issue));
      if (item.status === 'failed' && canSelectConversion(item)) notes.push('处理后可重新勾选上传。');
      for (const warning of item.warnings || []) {
        if (warning !== 'kept_disabled' || item.status !== 'uploaded') notes.push(conversionMessage(warning));
      }
      return `<div class="conversion-item" data-state="${escapeHTML(item.status)}"><input type="checkbox" data-convert-select="${index}" aria-label="选择${escapeHTML(name)}" ${item.selected ? 'checked' : ''}><div class="conversion-item-details"><strong dir="auto">${escapeHTML(name)}</strong>${meta ? `<small>${escapeHTML(meta)}</small>` : ''}<div class="conversion-item-status">${escapeHTML(status)}</div>${notes.length ? `<small>${escapeHTML(notes.join(' '))}</small>` : ''}</div>${item.credential ? `<button type="button" class="text-button small" data-convert-download="${index}" title="${escapeHTML(item.filename || '下载 CPA JSON')}" aria-label="下载 ${escapeHTML(name)} 的 CPA JSON">下载 JSON</button>` : ''}</div>`;
    }).join('');
    updateConversionControls();
  }
  function resetConversion() {
    conversion.sequence++;
    Object.assign(conversion, { items: [], source: '', fileName: '', busy: '' });
    convertEl('source').value = '';
    convertEl('file').value = '';
    convertEl('paste').open = false;
    delete convertEl('dropzone').dataset.dragging;
    conversionFeedback();
    renderConversion();
  }
  function closeConversion(restoreFocus = false) {
    resetConversion();
    if (convertEl('dialog').open) convertEl('dialog').close();
    if (restoreFocus && !convertEl('open').disabled) convertEl('open').focus({ preventScroll: true });
  }
  convertEl('open').addEventListener('click', () => {
    if (accountBusy()) return;
    clearSecrets(); closeAccountEditors();
    convertEl('dialog').showModal();
    convertEl('file-open').focus({ preventScroll: true });
  });
  for (const id of ['close', 'cancel']) convertEl(id).addEventListener('click', () => { if (conversion.busy !== 'upload') closeConversion(true); });
  convertEl('dialog').addEventListener('cancel', event => { event.preventDefault(); if (conversion.busy !== 'upload') closeConversion(true); });
  convertEl('source').addEventListener('input', () => {
    conversion.sequence++; conversion.busy = ''; conversion.fileName = ''; conversionFeedback(); updateConversionControls();
  });
  convertEl('edit').addEventListener('click', () => {
    if (conversion.busy) return;
    const pasted = conversion.fileName ? '' : conversion.source;
    resetConversion();
    if (pasted) {
      convertEl('paste').open = true;
      convertEl('source').value = pasted;
      updateConversionControls(); convertEl('source').focus();
    } else convertEl('file-open').focus();
  });
  function conversionCurrent(sequence, epoch) {
    return sequence === conversion.sequence && current(epoch) && convertEl('dialog').open;
  }
  async function previewConversion(source, sequence, epoch) {
    if (!source.trim()) { conversionFeedback('没有可读取的 JSON 内容。', 'error'); return; }
    if (new TextEncoder().encode(source).length > convertLimit) { conversionFeedback('内容超过 2 MiB，请分批导入。', 'error'); return; }
    conversion.busy = 'preview'; conversionFeedback('正在预览凭证…'); updateConversionControls();
    try {
      const result = await write('/conversion/preview', { source });
      if (!conversionCurrent(sequence, epoch)) return;
      const rows = result?.items;
      if (!Array.isArray(rows) || !rows.length || rows.length > 100 || rows.some((item, i) => item?.index !== i || !['ready', 'invalid'].includes(item.status))) {
        conversionFeedback('CPA 返回的预览不完整，请重新预览。', 'error'); return;
      }
      conversion.source = source;
      conversion.items = rows.map(item => ({ ...item, selected: item.status === 'ready' }));
      convertEl('source').value = '';
      conversionFeedback(); renderConversion();
      convertEl('dialog').querySelector('.conversion-scroll').scrollTop = 0;
      convertEl('results-title').focus({ preventScroll: true });
    } catch (error) {
      if (conversionCurrent(sequence, epoch) && !isDiscarded(error)) {
        conversionFeedback(conversionMessages[error.code] || reasons[error.code] || '未能取得 CPA 的转换预览，请检查面板连接后重试。', 'error');
      }
    } finally {
      if (sequence === conversion.sequence) { conversion.busy = ''; updateConversionControls(); }
    }
  }
  convertEl('preview').addEventListener('click', () => {
    if (conversion.busy || !connected()) return;
    void previewConversion(convertEl('source').value.trim(), ++conversion.sequence, state.epoch);
  });
  async function importConversionFile(files) {
    if (conversion.busy === 'upload' || !connected() || !convertEl('dialog').open || conversion.items.length) return;
    resetConversion();
    if (files.length !== 1) { conversionFeedback('请每次选择一个 JSON 文件，同一文件可包含多个凭证。', 'error'); return; }
    const [file] = files;
    conversion.fileName = file.name;
    updateConversionControls();
    if (file.size > convertLimit) { conversionFeedback('文件超过 2 MiB，请分批导入。', 'error'); return; }
    const sequence = conversion.sequence, epoch = state.epoch;
    conversion.busy = 'read'; conversionFeedback('正在读取文件…'); updateConversionControls();
    try {
      const source = await file.text();
      if (!conversionCurrent(sequence, epoch)) return;
      await previewConversion(source, sequence, epoch);
    } catch {
      if (conversionCurrent(sequence, epoch)) conversionFeedback('读取失败，请重新选择 JSON 文件。', 'error');
    } finally {
      if (sequence === conversion.sequence) { conversion.busy = ''; updateConversionControls(); }
    }
  }
  convertEl('file-open').addEventListener('click', () => convertEl('file').click());
  convertEl('file').addEventListener('change', event => {
    const files = Array.from(event.target.files);
    if (files.length) void importConversionFile(files);
  });
  const fileDrag = event => Array.from(event.dataTransfer?.types || []).includes('Files');
  const dropzone = convertEl('dropzone');
  dropzone.addEventListener('dragover', event => {
    if (!fileDrag(event)) return;
    event.preventDefault(); event.dataTransfer.dropEffect = 'copy'; dropzone.dataset.dragging = 'true';
  });
  dropzone.addEventListener('dragleave', event => {
    if (!dropzone.contains(event.relatedTarget)) delete dropzone.dataset.dragging;
  });
  dropzone.addEventListener('drop', event => {
    delete dropzone.dataset.dragging;
    if (!fileDrag(event)) return;
    event.preventDefault(); void importConversionFile(Array.from(event.dataTransfer.files));
  });
  for (const type of ['dragover', 'drop']) document.addEventListener(type, event => {
    if (convertEl('dialog').open && fileDrag(event)) event.preventDefault();
  });
  convertEl('list').addEventListener('change', event => {
    if (conversion.busy || event.target.dataset.convertSelect === undefined) return;
    const item = conversion.items[Number(event.target.dataset.convertSelect)];
    if (item && canSelectConversion(item)) item.selected = event.target.checked;
    updateConversionControls();
  });
  convertEl('list').addEventListener('click', event => {
    const button = event.target.closest('[data-convert-download]');
    if (!button || conversion.busy) return;
    const item = conversion.items[Number(button.dataset.convertDownload)];
    if (!item?.credential) return;
    const url = URL.createObjectURL(new Blob([JSON.stringify(item.credential, null, 2) + '\n'], { type: 'application/json' }));
    const link = document.createElement('a'); link.href = url; link.download = item.filename; link.click(); setTimeout(() => URL.revokeObjectURL(url), 1000);
  });
  convertEl('upload').addEventListener('click', async () => {
    const selected = conversion.items.filter(item => item.selected && canSelectConversion(item));
    if (conversion.busy || !selected.length || !connected()) return;
    const sequence = ++conversion.sequence, epoch = state.epoch;
    conversion.busy = 'upload'; conversionFeedback(); updateConversionControls();
    const unknown = { status: 'uncertain', code: 'commit_unknown', selected: false };
    try {
      const result = await write('/conversion/upload', { source: conversion.source, indexes: selected.map(item => item.index) });
      if (!conversionCurrent(sequence, epoch)) return;
      for (const item of selected) {
        const matches = Array.isArray(result?.items) ? result.items.filter(row => row?.index === item.index) : [];
        const saved = matches[0];
        if (matches.length !== 1 || !['uploaded', 'existing', 'failed', 'uncertain'].includes(saved.status) || (saved.status === 'uploaded' && typeof saved.enabled !== 'boolean')) Object.assign(item, unknown);
        else Object.assign(item, { status: saved.status, code: saved.code || (saved.status === 'uncertain' ? 'commit_unknown' : ''), enabled: saved.enabled, selected: false });
      }
      renderConversion();
      if (selected.some(item => item.status === 'uncertain')) conversionFeedback('部分上传结果尚未确认，请在 CPA 凭证列表核对。', 'error');
    } catch (error) {
      if (!conversionCurrent(sequence, epoch) || isDiscarded(error)) return;
      if (error.code !== 'management_offline' && (error.outcomeUnknown || !error.status || error.status >= 500 || ['idempotency_unknown', 'storage_unavailable'].includes(error.code))) {
        for (const item of selected) Object.assign(item, unknown);
        renderConversion(); conversionFeedback('上传结果尚未确认，请在 CPA 凭证列表核对，勿重复上传。', 'error');
      } else conversionFeedback(conversionMessage(error.code), 'error');
      return;
    } finally {
      if (sequence === conversion.sequence) { conversion.busy = ''; updateConversionControls(); }
    }
    if (selected.some(item => ['uploaded', 'uncertain'].includes(item.status))) {
      try { await reload(); if (current(epoch)) await onCommitted?.(); }
      catch { /* Refresh errors do not change confirmed per-row receipts. */ }
    }
  });

  function importFeedback(message = '') {
    $('account-import-error').textContent = message;
    $('account-import-error').hidden = !message;
  }
  function updateImportCount() {
    const count = $('account-import-text').value.split(/\r?\n/).filter(line => line.trim()).length;
    $('account-import-count').textContent = `${count} / 100 个账号`;
    const vault = state.vault || state.capabilities?.vault || {};
    $('account-import-submit').disabled = accountBusy() || !localStorageReady(vault) || count === 0 || count > 100;
    $('account-import-text').setAttribute('aria-invalid', String(count > 100));
    $('account-import-submit').textContent = state.busy.has('account') && $('account-import-dialog').open ? '正在导入…' : count ? `导入 ${count} 个账号` : '导入账号';
  }
  function closeAccountImport(restoreFocus = false) {
    const opener = importOpener;
    importOpener = null;
    $('account-import-text').value = '';
    $('account-import-proxy').value = '';
    $('account-import-dialog').querySelector('.login-proxy-options').open = false;
    importFeedback();
    if ($('account-import-dialog').open) $('account-import-dialog').close();
    updateImportCount();
    if (restoreFocus && opener?.isConnected && !opener.disabled) opener.focus({ preventScroll: true });
  }

  function feedback(message = '', error = false) {
    if ($('account-security-dialog').open && error) {
      securityFeedback(message);
      $('account-security-error').focus({ preventScroll: true });
      return;
    }
    if ($('account-import-dialog').open && error) {
      importFeedback(message);
      $('account-import-error').focus({ preventScroll: true });
      return;
    }
    if ($('account-editor-dialog').open && error) {
      editorFeedback(message);
      $('account-editor-error').focus({ preventScroll: true });
      return;
    }
    const target = state.view === 'tasks' ? 'login-history-feedback' : state.view === 'settings' ? 'login-settings-feedback' : '';
    $('account-action-announcement').textContent = !target && !error ? message : '';
    $('account-action-error').textContent = !target && error ? message : '';
    $('account-action-error').hidden = !!target || !error || !message;
    for (const id of ['login-history-feedback', 'login-settings-feedback']) {
      $(id).textContent = id === target ? message : '';
      $(id).classList.toggle('hidden', id !== target || !message);
      $(id).classList.toggle('error', id === target && error);
    }
  }
  function fail(error) {
    if (isDiscarded(error)) return;
    if (securitySubmissionUncertain && $('account-security-dialog').open) {
      feedback('提交结果尚未确认。请关闭窗口并查看账号任务，勿再次提交。', true);
      return;
    }
    const message = error instanceof TypeError || error.outcomeUnknown ? '未收到可确认的提交结果。请刷新账号和任务，勿重复提交。'
      : error.status === 404 && !error.code ? '登录接口暂不可用，请检查 CPA 中的插件注册状态。'
      : messageFor(error.code);
    feedback(message, true);
  }
  function clearSecrets() {
    editorCredentials = null;
    $('login-workbench').querySelectorAll('[data-login-secret], #login-callback').forEach(input => { input.value = ''; });
    syncEditorCredentials();
  }
  function editorCredentialsChanged() {
    return !!editorCredentials && ($('account-password').value !== editorCredentials.password || $('account-totp').value.trim() !== editorCredentials.totp_secret);
  }
  function syncEditorCredentials() {
    const changed = editorCredentialsChanged();
    for (const id of ['account-password', 'account-totp']) $(id).required = changed;
    const seed = $('account-totp').value.trim();
    $('account-totp').setCustomValidity(changed && seed && !validImportTOTP(seed) ? '请填写有效的 2FA 长期密钥，不是六位验证码。' : '');
  }
  function closeAccountSurface(restoreFocus = false, force = false) {
    if (!force && state.view === 'settings' && state.busy.has('account')) return false;
    if (!force && state.view === 'tasks' && state.busy.has('history-delete')) return false;
    closeHistoryDelete();
    const previous = state.view, opener = surfaceOpener;
    surfaceOpener = null;
    state.view = 'accounts';
    if (previous === 'tasks') {
      clearSelectedDetail();
      state.readErrors.delete('任务详情');
    }
    for (const id of ['login-history-dialog', 'login-settings-dialog']) if ($(id).open) $(id).close();
    if (restoreFocus && state.open && connected()) {
      const fallback = $(previous === 'tasks' ? 'account-history-open' : 'account-settings-open');
      (opener?.isConnected && opener.getClientRects().length && !opener.disabled ? opener : fallback).focus({ preventScroll: true });
    }
    return true;
  }
  function setView(view, opener = document.activeElement) {
    if (!['accounts', 'tasks', 'settings'].includes(view)) return;
    if (view === 'accounts') { closeAccountSurface(); return; }
    if (!state.open || !connected() || state.view === view || !closeAccountSurface()) return;
    clearSecrets(); feedback(); closeAccountEditors();
    state.view = view;
    surfaceOpener = opener;
    $(view === 'tasks' ? 'login-history-dialog' : 'login-settings-dialog').showModal();
    if (view === 'settings' && !$('account-settings-note').textContent) $('account-settings-note').textContent = state.settingsDirty ? '有未保存更改，关闭后保留草稿' : '修改后点击保存生效';
    $(view === 'tasks' ? 'login-task-search' : 'account-default-proxy').focus({ preventScroll: true });
    if (view === 'tasks') {
      state.revealJob = true;
      revealSelectedJob();
      void loadSelected().catch(() => { /* The detail view reports its own read error. */ });
    }
    renderReadStatus();
    updateControls();
  }
  function editorFeedback(message = '') {
    $('account-editor-error').textContent = message;
    $('account-editor-error').hidden = !message;
  }
  function focusAccount(id, action = 'more') {
    const button = [...$('account-list').querySelectorAll('[data-account-action]')].find(node => node.dataset.accountId === id && node.dataset.accountAction === action);
    (button && !button.disabled ? button : $('account-import-open')).focus({ preventScroll: true });
  }
  function closeAccountEditor(restoreFocus = false) {
    const id = state.accountEditing?.id || editorOriginID;
    const active = !!id || $('account-editor-dialog').open;
    editorOriginID = '';
    editorCredentials = null;
    state.accountDetailRead++; state.accountDetailLoading = false; state.accountDetailFailed = false;
    state.accountEditing = null; securityRecoveryJob = ''; securityRecoveryCompletable = false;
    $('account-editor').querySelectorAll('[data-login-secret]').forEach(input => { input.value = ''; });
    syncEditorCredentials();
    $('account-editor-identity').textContent = '';
    $('account-editor-note').textContent = '';
    $('account-effective-proxy').textContent = '';
    $('account-copy-feedback').textContent = '';
    $('account-label').value = '';
    $('account-editor-retry').hidden = true;
    $('account-login-options').open = false;
    $('account-login-target').value = '';
    editorFeedback();
    $('account-security-recovery').hidden = true;
    $('account-security-recovery-secret-label').hidden = true;
    $('account-security-recovery-note').textContent = '';
    if ($('account-editor-dialog').open) $('account-editor-dialog').close();
    if (active) renderAccounts();
    if (restoreFocus && id && state.open && connected() && state.view === 'accounts') focusAccount(id);
  }
  function closeAccountEditors() {
    closeAccountDelete();
    closeAccountLogin();
    closeConversion();
    closeAccountImport(); closeAccountEditor(); closeAccountSecurity(); closeAccountMenu();
  }
  function securityScrollBox() {
    return $('account-security-dialog').querySelector(securityAction === 'reset_totp' ? '.account-security-scroll' : '.account-security-body');
  }
  function securityFeedback(message = '') {
    const error = $('account-security-error');
    const changed = error.textContent !== message;
    error.textContent = message;
    error.hidden = !message;
    if (message && changed && $('account-security-dialog').open) {
      const body = securityScrollBox();
      const bounds = body.getBoundingClientRect(), alert = error.getBoundingClientRect();
      if (alert.bottom > bounds.bottom - 12) body.scrollTop += alert.bottom - bounds.bottom + 12;
      else if (alert.top < bounds.top + 12) body.scrollTop += alert.top - bounds.top - 12;
    }
  }
  function securityAvailable(action) {
    return state.capabilities?.security_actions?.[action] === true;
  }
  function securityProxyIssue(account) {
    return !!account?.proxy_error && account.proxy_source !== 'credential';
  }
  function closeAccountMenu(restoreFocus = false) {
    const id = menuAccountID;
    menuAccountID = '';
    if ($('account-actions-menu').matches(':popover-open')) $('account-actions-menu').hidePopover();
    $('account-list').querySelectorAll('[data-account-action="more"]').forEach(button => button.setAttribute('aria-expanded', 'false'));
    if (restoreFocus && id && current(state.epoch) && state.view === 'accounts') focusAccount(id, 'more');
  }
  function positionAccountMenu(button) {
    const menu = $('account-actions-menu');
    if (!menu.matches(':popover-open')) return;
    const bounds = button?.getBoundingClientRect();
    const viewport = document.documentElement.clientWidth;
    if (!bounds?.height || bounds.bottom <= 0 || bounds.top >= innerHeight || bounds.right <= 0 || bounds.left >= viewport) { closeAccountMenu(); return; }
    menu.style.left = `${Math.max(12, Math.min(bounds.right - menu.offsetWidth, viewport - menu.offsetWidth - 12))}px`;
    menu.style.top = `${Math.max(12, bounds.bottom + menu.offsetHeight + 8 <= innerHeight ? bounds.bottom + 4 : bounds.top - menu.offsetHeight - 4)}px`;
  }
  let positioningMenu = false;
  function scheduleMenuPosition() {
    if (!menuAccountID || positioningMenu) return;
    positioningMenu = true;
    requestAnimationFrame(() => {
      positioningMenu = false;
      if (menuAccountID) positionAccountMenu([...$('account-list').querySelectorAll('[data-account-action="more"]')].find(node => node.dataset.accountId === menuAccountID));
    });
  }
  function showAccountMenu(account, button) {
    if (!connected() || loading) return;
    if (menuAccountID === account.id && $('account-actions-menu').matches(':popover-open')) { closeAccountMenu(true); return; }
    closeAccountMenu();
    menuAccountID = account.id;
    updateSecurityControls();
    const menu = $('account-actions-menu');
    menu.showPopover();
    positionAccountMenu(button);
    button.setAttribute('aria-expanded', 'true');
    (menu.querySelector('button:not(:disabled)') || menu).focus({ preventScroll: true });
  }
  function updateSecurityControls() {
    const menuAccount = state.accounts.find(account => account.id === menuAccountID);
    const unavailable = accountBusy() || !menuAccount || accountLoginPending(menuAccount);
    $('account-logout-all').disabled = unavailable || !securityAvailable('logout_all') || !!menuAccount?.credentials_error || securityProxyIssue(menuAccount);
    $('account-actions-menu').querySelectorAll('[data-account-menu-action]').forEach(button => {
      const action = button.dataset.accountMenuAction;
      const readOnly = action === 'logs' || action === 'task';
      button.disabled = !menuAccount || (readOnly ? !connected() || loading : accountBusy()) || action === 'task' && !menuAccount?.last_job_id;
    });
    $('account-logout-all').title = securityAvailable('logout_all') ? '' : '当前插件不支持退出所有设备';
    const latest = state.accounts.find(account => account.id === securityAccount?.id);
    const busy = state.busy.has('account');
    const blocked = accountBusy() || !latest || accountLoginPending(latest) || latest.revision !== securityAccount?.revision || !securityAvailable(securityAction) || securitySubmissionUncertain;
    $('account-security-submit').disabled = blocked;
    $('account-security-submit').textContent = busy && $('account-security-dialog').open ? '正在提交…' : securityAction === 'logout_all' ? '确认退出所有设备' : '确认重置 2FA';
    $('account-security-form').setAttribute('aria-busy', String(busy));
    $('account-security-logout').disabled = blocked || !securityAvailable('logout_all');
    $('account-security-reauthorize').disabled = blocked;
    renderSecurityPlan(blocked);
    for (const id of ['account-security-close', 'account-security-cancel']) $(id).disabled = busy;
    $('account-security-complete').disabled = accountBusy() || state.accountDetailLoading || !securityRecoveryCompletable || !securityRecoveryJob;
    if ($('account-security-dialog').open && !busy && latest && latest.revision !== securityAccount?.revision && !securitySubmissionUncertain) securityFeedback('账号状态已变化。请关闭后重新打开，核对最新状态再操作。');
  }
  function renderSecurityPlan(blocked = $('account-security-reauthorize').disabled) {
    if (!securityAction) return;
    const reset = securityAction === 'reset_totp';
    const logout = reset && $('account-security-logout').checked;
    const reauthorize = $('account-security-reauthorize').checked;
    const uploadInput = $('account-security-upload');
    if (!reauthorize) uploadInput.checked = false;
    uploadInput.disabled = blocked || !reauthorize;
    const upload = uploadInput.checked;
    $('account-security-upload-help').textContent = reauthorize ? '上传并启用新凭证。' : '开启重登授权后可选。';
    $('account-security-plan-hint').textContent = reset ? `已选 ${1 + Number(logout) + Number(reauthorize) + Number(upload)} 项` : '可选';
    const signature = `${securityAction}:${logout}:${reauthorize}:${upload}`;
    const list = $('account-security-steps');
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    $('account-security-logout-option').dataset.selected = String(logout);
    $('account-security-reauthorize-option').dataset.selected = String(reauthorize);
    $('account-security-upload-option').dataset.selected = String(upload);
    if (reset) {
      const steps = ['重置 2FA'];
      if (logout) steps.push('退出设备');
      if (reauthorize) steps.push('重登授权');
      if (upload) steps.push('上传 CPA');
      const summary = $('account-security-summary');
      summary.replaceChildren();
      const spokenPlan = document.createElement('span');
      spokenPlan.className = 'sr-only reset-execution-speech';
      spokenPlan.textContent = steps.length === 1 ? '仅重置 2FA' : steps.join(' → ');
      summary.append(spokenPlan);
      for (const [index, text] of steps.entries()) {
        const step = document.createElement('span');
        step.className = 'reset-execution-step';
        step.setAttribute('aria-hidden', 'true');
        step.dataset.order = index + 1;
        const title = document.createElement('strong');
        title.textContent = steps.length === 1 ? '仅重置 2FA' : text;
        const detail = document.createElement('small');
        detail.textContent = ({ '重置 2FA': '保存新密钥', '退出设备': '提交退出请求', '重登授权': '生成新授权', '上传 CPA': '上传并启用' })[text];
        step.append(title, detail);
        summary.append(step);
      }
    } else {
      $('account-security-step-count').textContent = `${1 + Number(reauthorize) + Number(upload)} 项操作`;
      $('account-security-flow-reauthorize').dataset.selected = String(reauthorize);
      $('account-security-flow-upload').dataset.selected = String(upload);
    }
  }
  function closeAccountSecurity(restoreFocus = false) {
    const id = securityAccount?.id;
    const originAction = securityAction === 'reset_totp' ? 'reset_totp' : 'more';
    securityAccount = null; securityAction = ''; securitySubmissionUncertain = false;
    $('account-security-identity').textContent = '';
    $('account-security-identity').removeAttribute('title');
    $('account-security-logout').checked = false;
    $('account-security-reauthorize').checked = false;
    $('account-security-upload').checked = false;
    securityFeedback();
    if ($('account-security-dialog').open) $('account-security-dialog').close();
    if (restoreFocus && id && current(state.epoch) && state.view === 'accounts') focusAccount(id, originAction);
  }
  function showAccountSecurity(account, action) {
    if (accountBusy() || accountLoginPending(account) || !securityAvailable(action) || account.credentials_error || securityProxyIssue(account)) return;
    closeAccountMenu(); clearSecrets(); closeAccountEditors(); feedback();
    securityAccount = { ...account }; securityAction = action;
    const reset = action === 'reset_totp';
    const choices = readWorkflowPreference(action);
    $('account-security-logout').checked = reset && securityAvailable('logout_all') && choices.logout;
    $('account-security-reauthorize').checked = choices.reauthorize;
    $('account-security-upload').checked = choices.upload;
    $('account-security-title').textContent = reset ? '重置 2FA' : '退出所有设备';
    $('account-security-identity').textContent = reset ? '' : account.email;
    if (reset) $('account-security-identity').removeAttribute('title');
    else $('account-security-identity').title = account.email || '';
    $('account-security-dialog').dataset.action = action;
    $('account-security-dialog').setAttribute('aria-describedby', reset ? 'account-security-description' : 'account-security-identity account-security-logout-description');
    $('account-security-plan-title').textContent = '后续操作';
    for (const id of ['account-security-logout', 'account-security-reauthorize', 'account-security-upload']) {
      if (reset) $(id).setAttribute('role', 'switch');
      else $(id).removeAttribute('role');
    }
    $('account-security-notes').open = false;
    $('account-security-logout-option').hidden = !reset;
    renderSecurityPlan();
    $('account-security-dialog').showModal();
    updateSecurityControls();
    securityScrollBox().scrollTop = 0;
    $('account-security-cancel').focus({ preventScroll: true });
  }
  function renderSecurityRecovery(detail) {
    const recovery = detail.security_recovery;
    const pending = !!detail.account?.security_pending;
    securityRecoveryJob = recovery?.job_id || '';
    securityRecoveryCompletable = !!recovery?.can_complete;
    $('account-security-recovery').hidden = !pending && !recovery;
    $('account-security-recovery-secret-label').hidden = !recovery?.new_totp_secret;
    $('account-security-recovery-secret').value = recovery?.new_totp_secret || '';
    $('account-security-complete').hidden = !securityRecoveryCompletable;
    $('account-security-recovery-note').textContent = securityRecoveryCompletable
      ? '已确认新 2FA 激活，但本地保存尚未完成。点击下方按钮仅完成本机资料保存，不会退出设备、重新授权或上传 CPA。'
      : recovery?.activation_confirmed ? '已确认新 2FA 激活，后续结果仍需核对。当前账号已暂停登录和修改；请结合任务日志处理。'
      : '远端操作结果尚未确认，待核对密钥已隔离保存，不作为有效凭据显示。请结合任务日志核对账号，勿再次重置。';
    if (recovery) for (const name of Object.keys(recovery)) recovery[name] = '';
  }
  function accountLoginPending(account) {
    return !!account.security_pending || activeStates.has(account.last_job_status) || ['awaiting_commit', 'commit_unknown', 'saved_not_adopted', 'security_uncertain'].includes(account.last_job_status);
  }
  function manualLoginBlocked(account) {
    if (account.last_job_status === 'awaiting_commit') return !!account.security_pending || !account.last_job_id;
    return accountLoginPending(account);
  }
  function applyAccountJob(account, job) {
    Object.assign(account, { last_job_id: idOf(job), last_job_status: job.status || 'queued', last_job_created_at: job.created_at, last_job_automatic: !!job.automatic, last_job_phase: job.phase, last_job_intent: job.intent, last_job_error_code: job.error_code, last_job_error_stage: job.error_stage, last_job_error_reason: job.error_reason, last_job_auto_commit: !!job.auto_commit, last_job_credential_enabled: !!job.credential_enabled, last_job_action: job.action, last_job_security_status: job.security_status, last_job_totp_reset: !!job.totp_reset, last_job_devices_logged_out: !!job.devices_logged_out });
  }
  function accountBusy() { return !state.open || loading || !connected() || navigator.onLine === false || !state.accountsAvailable || !state.jobsAvailable || !state.bootstrapReady || state.busy.has('account'); }
  function deleteUnavailable(account) {
    return !account || accountLoginPending(account) || !localStorageReady(state.vault || state.capabilities?.vault || {});
  }
  function updateDeleteControls() {
    const target = deleteTarget;
    if (!target) return;
    const latest = state.accounts.find(item => item.id === target.id);
    const changed = !latest || latest.revision !== target.revision;
    let problem = target.error;
    if (!problem && changed) problem = '账号资料已变化，请关闭后重新核对。';
    if (!problem && accountLoginPending(latest)) problem = '此账号有进行中或待核对的操作，暂时不能删除。';
    if (!problem && (navigator.onLine === false || !state.accountsAvailable || !state.jobsAvailable)) problem = '无法核对账号状态，暂时不能删除。';
    if (!problem && deleteUnavailable(latest)) problem = '账号存储不可用，暂时不能删除。';
    const error = $('account-delete-error');
    if (error.textContent !== problem) error.textContent = problem;
    error.hidden = !problem || target.submitting;
    $('account-delete-confirm').setAttribute('aria-busy', String(target.submitting));
    $('account-delete-no').disabled = target.submitting;
    $('account-delete-no').textContent = problem ? '关闭' : '取消';
    $('account-delete-yes').disabled = target.submitting || !!problem || accountBusy();
    $('account-delete-yes').textContent = target.submitting ? '正在删除…' : '确认删除';
  }
  function showAccountDelete(account, fromEditor = false) {
    if (deleteTarget || accountBusy() || deleteUnavailable(account)) return;
    closeAccountMenu();
    deleteTarget = { id: account.id, revision: account.revision, fromEditor, submitting: false, error: '' };
    $('account-delete-identity').textContent = account.email || account.masked_email || account.label || account.id;
    updateDeleteControls();
    $('account-delete-confirm').showModal();
    $('account-delete-no').focus({ preventScroll: true });
  }
  function closeAccountDelete(restoreFocus = false) {
    const target = deleteTarget;
    if (!target) return;
    deleteTarget = null;
    $('account-delete-confirm').close();
    $('account-delete-identity').textContent = '';
    $('account-delete-error').textContent = '';
    if (!restoreFocus || !state.open || !connected() || state.view !== 'accounts') return;
    if (target.fromEditor && $('account-editor-dialog').open) {
      if (state.accountsAvailable && !state.accounts.some(item => item.id === target.id)) {
        closeAccountEditor(); $('account-search').focus({ preventScroll: true }); return;
      }
      ($('account-delete-open').disabled ? $('account-editor-close') : $('account-delete-open')).focus({ preventScroll: true });
    } else focusAccount(target.id, 'delete');
  }
  function updateAccountControls() {
    const unavailable = !state.open || !connected();
    $('login-workbench').toggleAttribute('data-initializing', unavailable || loading || !state.accountsLoaded);
    $('account-history-open').disabled = unavailable;
    $('account-settings-open').disabled = unavailable;
    $('account-search').disabled = $('account-status-filter').disabled = unavailable || !state.accountsLoaded;
    $('account-convert-open').disabled = accountBusy() || !state.capabilities?.credential_conversion;
    const busy = accountBusy();
    for (const id of ['account-inspect', 'account-import-open', 'account-import-empty', 'account-settings-save', 'account-default-proxy', 'account-inspection-enabled', 'account-auto-enabled', 'account-interval', 'account-editor-save', 'account-login-start']) $(id).disabled = busy;
    $('account-upload-plans').disabled = busy;
    $('account-settings-save').disabled = busy || !state.settingsDirty;
    $('account-settings-save').textContent = state.view === 'settings' && state.busy.has('account') ? '正在保存…' : '保存设置';
    $('account-settings-draft').hidden = !state.settingsDirty;
    $('login-settings-dialog').querySelectorAll('[data-account-surface-close]').forEach(button => { button.disabled = state.busy.has('account'); });
    $('account-inspect').disabled = busy || !state.accounts.length;
    $('account-auto-enabled').disabled = busy || !$('account-inspection-enabled').checked;
    $('account-inspect').setAttribute('aria-busy', String(state.busy.has('account')));
    const vault = state.vault || state.capabilities?.vault || {};
    const storageReady = localStorageReady(vault);
    $('account-import-submit').disabled = busy || !storageReady;
    $('account-import-text').disabled = busy;
    $('account-import-proxy').disabled = busy;
    for (const id of ['account-import-close', 'account-import-cancel']) $(id).disabled = state.busy.has('account');
    $('account-import-form').setAttribute('aria-busy', String(state.busy.has('account')));
    updateImportCount();
    const editing = state.accounts.find(account => account.id === state.accountEditing?.id) || state.accountEditing;
    const editorBusy = busy || state.accountDetailLoading || state.accountDetailFailed || !storageReady || !!editing?.security_pending;
    $('account-editor').setAttribute('aria-busy', String(state.accountDetailLoading || state.busy.has('account')));
    $('account-editor').querySelectorAll('input').forEach(input => { input.disabled = editorBusy; });
    $('account-editor-save').disabled = editorBusy;
    $('account-security-recovery-secret').disabled = busy || state.accountDetailLoading || state.accountDetailFailed;
    for (const id of ['account-editor-close', 'account-editor-cancel']) $(id).disabled = state.busy.has('account');
    $('account-editor-retry').disabled = busy || state.accountDetailLoading;
    $('account-login-target').disabled = editorBusy;
    $('account-login-start').disabled = editorBusy || !editing || manualLoginBlocked(editing) || !accountLoginAvailable() || !!editing.credentials_error || securityProxyIssue(editing);
    $('account-delete-open').disabled = editorBusy || !!editing && accountLoginPending(editing);
    updateDeleteControls();
    $('account-email').readOnly = true;
    syncEditorCredentials();
    $('account-auto-relogin').disabled = editorBusy || !state.accountEditing?.auth_id;
    $('account-vault-note').textContent = storageReady ? '账号、密码和 2FA 密钥以明文保存在本机。' : storageMessage(vault);
    $('account-copy-material').disabled = editorBusy || !state.accountEditing || !editorCredentials || !!state.accountEditing.security_pending;
    $('account-list').querySelectorAll('.account-row button').forEach(button => {
      const action = button.dataset.accountAction;
      const readOnly = ['logs', 'task', 'more'].includes(action);
      let unavailable = button.dataset.unavailable === 'true';
      if (action === 'reset_totp' || action === 'delete') {
        const account = state.accounts.find(item => item.id === button.dataset.accountId);
        unavailable = action === 'delete' ? deleteUnavailable(account) : !account || accountLoginPending(account);
        if (action === 'reset_totp') {
          unavailable ||= !securityAvailable('reset_totp') || !!account?.credentials_error || securityProxyIssue(account);
          button.title = securityAvailable('reset_totp') ? '重置 2FA，核对后执行' : '当前插件不支持重置 2FA';
        }
      }
      button.disabled = (readOnly ? !connected() || loading : busy) || unavailable;
    });
    const pages = Math.max(1, Math.ceil(filteredAccounts().length / 20));
    updateSecurityControls();
    $('account-page-prev').disabled = unavailable || state.accountPage === 0;
    $('account-page-next').disabled = unavailable || state.accountPage >= pages - 1;
  }
  function renderAccountSettings() {
    const settings = state.accountSettings;
    $('account-schedule-summary').textContent = !settings ? '尚未加载' : settings.inspection_enabled ? `每 ${settings.interval_minutes} 分钟 · ${settings.auto_relogin_enabled ? '已允许自动登录' : '只巡检'}` : '定时巡检未开启';
    if (!state.settingsDirty && settings) {
      $('account-inspection-enabled').checked = !!settings.inspection_enabled;
      $('account-auto-enabled').checked = !!settings.auto_relogin_enabled;
      $('account-interval').value = settings.interval_minutes || 15;
      $('account-default-proxy').value = settings.login_proxy_url || '';
      document.querySelectorAll('[name="cpa-upload-plan"]').forEach(input => { input.checked = (settings.cpa_upload_excluded_plans || []).includes(input.value); });
    }
  }
  function filteredAccounts() {
    const query = state.accountQuery.trim().toLocaleLowerCase();
    return state.accounts.filter(account => {
      if (query && ![account.email, account.label, account.auth_id, planLabel(account.plan_type)].some(value => String(value || '').toLocaleLowerCase().includes(query))) return false;
      if (state.accountFilter === 'active' && !activeStates.has(account.last_job_status)) return false;
      if (state.accountFilter === 'linked' && !account.auth_id) return false;
      if (state.accountFilter === 'attention' && !needsAttention(account)) return false;
      return true;
    });
  }
  function renderAccounts() {
    $('login-workbench').toggleAttribute('data-accounts-loaded', state.accountsLoaded);
    const count = state.accounts.length;
    const filtered = filteredAccounts();
    const pages = Math.max(1, Math.ceil(filtered.length / 20));
    state.accountPage = Math.min(state.accountPage, pages - 1);
    const shown = filtered.slice(state.accountPage * 20, (state.accountPage + 1) * 20);
    const signature = JSON.stringify([shown, state.jobs, state.bootstrapReady, accountLoginAvailable(), state.accountSettings?.login_proxy_url, state.accountEditing?.id, state.accountsAvailable, state.accountStarting, accountLogs.account]);
    const totals = { all:count, attention:state.accounts.filter(needsAttention).length, linked:state.accounts.filter(account => account.auth_id).length, active:state.accounts.filter(account => activeStates.has(account.last_job_status)).length };
    const filterLabels = { all:'全部状态', attention:'待处理', linked:'已绑定', active:'进行中' };
    for (const option of $('account-status-filter').options) option.textContent = `${filterLabels[option.value]} · ${state.accountsLoaded ? totals[option.value] : '—'}`;
    $('account-status-filter').value = state.accountFilter;
    let message = '';
    if (state.readErrors.has('账号列表')) message = (state.accountsLoaded ? '显示上次读取的账号，正在等待重新读取。' : '账号列表读取失败，正在等待重新读取。') + state.readErrors.get('账号列表');
    else if (state.readErrors.has('巡检设置')) message = '巡检设置暂未刷新，部分操作暂不可用。';
    $('account-list-summary').textContent = message;
    $('account-list-summary').hidden = !message;
    $('account-table-wrap').classList.toggle('hidden', !filtered.length);
    $('account-empty').classList.toggle('hidden', count > 0);
    $('account-no-results').hidden = !state.accountsLoaded || !count || filtered.length > 0;
    $('account-filter-count').textContent = state.accountQuery || state.accountFilter !== 'all' ? `${filtered.length} 个匹配` : '';
    $('account-page-label').textContent = filtered.length ? `${state.accountPage * 20 + 1}–${Math.min((state.accountPage + 1) * 20, filtered.length)} / ${filtered.length}` : '0 个账号';
    document.querySelector('.account-pagination').hidden = !count || pages <= 1 && !state.accountQuery && state.accountFilter === 'all';
    $('account-page-prev').hidden = pages <= 1;
    $('account-page-next').hidden = pages <= 1;
    if (signature !== state.accountSignature) {
      const activeElement = document.activeElement;
      const logFocused = accountLogs.row?.contains(activeElement) ? activeElement : null;
      const logFocusedKey = logFocused?.closest('details')?.dataset.logKey;
      const logTop = accountLogs.row?.querySelector('#account-log-viewport')?.scrollTop || 0;
      const focused = activeElement?.dataset;
      state.accountSignature = signature;
      accountLogs.row?.remove();
      $('account-list').innerHTML = shown.map(account => {
        const id = escapeHTML(account.id);
        const job = { ...state.jobs.find(item => idOf(item) === account.last_job_id && item.account_id === account.id), id: account.last_job_id, account_id: account.id, created_at: account.last_job_created_at, automatic: account.last_job_automatic, phase: account.last_job_phase, intent: account.last_job_intent, status: account.last_job_status, error_code: account.last_job_error_code, error_stage: account.last_job_error_stage, error_reason: account.last_job_error_reason, auto_commit: account.last_job_auto_commit, enable_credential: account.last_job_auto_commit, credential_enabled: account.last_job_credential_enabled, action: account.last_job_action, security_status: account.last_job_security_status, totp_reset: account.last_job_totp_reset, devices_logged_out: account.last_job_devices_logged_out };
        const failed = ['failed', 'interaction_required', 'conflict', 'interrupted', 'expired'].includes(account.last_job_status);
        const healthDetail = account.security_pending ? '安全操作待核对' : account.health_reason && !['no_auth_failure_observed', 'unbound', 'login_required'].includes(account.health_reason) ? healthReason(account.health_reason) : '';
        const last = account.last_job_status ? jobLabel(job) : '暂无记录';
        const unresolved = ['commit_unknown', 'saved_not_adopted', 'security_uncertain'].includes(account.last_job_status);
        const submitting = state.accountStarting === account.id;
        const auto = account.auto_relogin ? (account.attention_required ? '自动登录已暂停' : '允许自动登录') : '';
        const awaitingInspection = account.health_reason === 'awaiting_inspection' && ['unknown', 'not_checked'].includes(account.health_status);
        const unbound = !account.auth_id && ['not_linked', 'unbound', 'unknown', 'not_checked'].includes(account.health_status);
        const healthText = unbound ? '尚未绑定' : awaitingInspection ? '待巡检' : healthLabel(account.health_status);
        const healthTone = ['needs_reauth', 'requires_sign_in', 'auth_failed', 'missing', 'identity_changed'].includes(account.health_status) ? 'bad' : ['paused', 'quality_paused', 'disabled', 'manual_disabled', 'cooldown', 'native_cooldown'].includes(account.health_status) ? 'warn' : ['healthy', 'available', 'ok', 'no_auth_failure'].includes(account.health_status) ? 'good' : '';
        const jobTone = (account.last_job_status === 'committed' && job.credential_enabled || account.last_job_status === 'succeeded') ? 'good' : account.last_job_status === 'interaction_required' ? 'warn' : failed ? 'bad' : unresolved || account.last_job_status === 'awaiting_commit' ? 'warn' : '';
        const loginBusy = submitting || activeStates.has(account.last_job_status);
        const loginProgress = isSecurityJob(job) ? last : ({ queued: '排队中', running: '登录中', waiting_browser: '待授权', exchanging: '授权中', validating: '核验中', committing: '保存中', cancelling: '取消中' })[account.last_job_status] || last;
        const loginBlocked = manualLoginBlocked(account);
        const uploadJob = account.pending_upload_job_id || (account.last_job_status === 'awaiting_commit' ? account.last_job_id : '');
        const loginLabel = submitting ? '提交中' : (account.security_pending && !activeStates.has(account.last_job_status)) || unresolved ? '待核对' : loginBlocked ? loginProgress : '2FA 登录';
        const loginTitle = loginBusy && job.phase && !submitting ? `${loginLabel} · ${phaseLabel(job.phase)}` : loginLabel;
        const plan = planType(account.plan_type);
        const identityNote = account.credentials_error ? '资料不可读取' : account.label || auto;
        const identityDetails = [account.label, auto, account.last_login_at ? `上次授权 ${date(account.last_login_at)}` : '', account.credentials_error ? '登录资料暂不可读取' : ''].filter(Boolean).join(' · ');
        const activitySummary = operationLabel(job);
        const activityTitle = account.last_job_id ? [activitySummary, last, job.created_at ? date(job.created_at) : ''].filter(Boolean).join('｜') : last;
        return `<tr class="account-row${state.accountEditing?.id === account.id || accountLogs.account === account.id ? ' selected' : ''}" data-account-row="${id}" data-active="${activeStates.has(account.last_job_status)}">
          <td data-label="账号"><div class="account-identity">
            <span class="account-avatar" aria-hidden="true">${escapeHTML((account.email || '?').slice(0, 1).toUpperCase())}</span><div class="account-identity-copy"><strong class="account-email" title="${escapeHTML(account.email || '')}">${escapeHTML(account.email || '邮箱暂不可读取')}</strong>
            <div class="account-identity-meta"><span class="account-plan" data-plan="${plan}" title="${plan === 'unknown' ? '暂无可识别的套餐授权信息；未知不等于 Free' : `授权套餐：${escapeHTML(planLabel(plan))}，来自授权快照，非实时订阅查询`}"><span class="account-plan-label">套餐</span><span class="account-plan-value">${escapeHTML(plan === 'unknown' ? '未知' : planLabel(plan))}</span></span>${identityNote ? `<span class="account-identity-note${account.credentials_error ? ' error-note' : ''}" title="${escapeHTML(identityDetails)}">${escapeHTML(identityNote)}</span>` : ''}</div>
          </div></div></td>
          <td data-label="CPA 状态"><div class="account-status" title="${escapeHTML([healthText, account.auth_id, healthDetail, account.last_checked_at ? `最近巡检 ${date(account.last_checked_at)}` : '', account.next_check_at ? `下次巡检 ${date(account.next_check_at)}` : ''].filter(Boolean).join('｜'))}">
            <span class="account-health-tag ${healthTone}"><span class="account-state-dot" aria-hidden="true"></span><span class="account-status-text">${escapeHTML(healthText)}</span></span>
            <small class="account-status-summary">${escapeHTML(healthDetail || (account.last_checked_at ? `巡检 ${compactDate(account.last_checked_at)}` : account.auth_id || '登录后自动上传 CPA'))}</small>
          </div></td>
          <td data-label="最近操作"><div class="account-activity" title="${escapeHTML(activityTitle)}">
            <div class="account-activity-heading">${account.last_job_status === 'awaiting_commit' ? `<button type="button" class="account-job-status account-status-link ${jobTone}" data-account-action="task" data-account-id="${id}" aria-label="${escapeHTML(last)}，核对授权"><span class="account-state-dot" aria-hidden="true"></span><span class="account-status-text">${escapeHTML(last)}</span></button>` : `<span class="account-job-status ${jobTone}" data-active="${activeStates.has(account.last_job_status)}"><span class="account-state-dot" aria-hidden="true"></span><span class="account-status-text">${escapeHTML(last)}</span></span>`}${job.created_at ? `<time class="account-operation-time" title="${escapeHTML(date(job.created_at))}">${escapeHTML(compactDate(job.created_at)).replace(/^(.*?)(\d{2}:\d{2})$/, '<span class="account-operation-date">$1</span>$2')}</time>` : ''}</div>
            ${account.last_job_id ? `<button type="button" class="account-operation-type" data-account-action="logs" data-account-id="${id}" aria-label="${escapeHTML(activityTitle)}，查看日志" aria-expanded="${accountLogs.account === account.id}">${escapeHTML(activitySummary)}</button>` : ''}
          </div></td>
          <td data-label="操作"><div class="account-row-actions">
            <div class="account-primary-actions">
              <button type="button" class="account-action account-start-action" data-account-action="start" data-account-id="${id}" title="${escapeHTML(loginTitle)}" aria-busy="${loginBusy}" data-unavailable="${loginBlocked || !accountLoginAvailable() || !!account.credentials_error || securityProxyIssue(account)}" ${loginBlocked ? 'disabled' : ''}>${accountIcon('login')}<span class="account-action-label">${escapeHTML(loginLabel)}</span></button>
              <button type="button" class="account-action account-reset-action" data-account-action="reset_totp" data-account-id="${id}" title="重置 2FA，核对后执行" disabled>${accountIcon('reset')}<span class="account-action-label">重置 2FA</span></button>
              <button type="button" class="account-action account-delete-action" data-account-action="delete" data-account-id="${id}" aria-label="删除账号 ${escapeHTML(account.email || account.id)}" aria-haspopup="dialog" aria-controls="account-delete-confirm" title="删除账号资料，确认后执行；CPA 凭证仍保留" disabled>${accountIcon('delete')}<span class="account-action-label">删除</span></button>
            </div>
            <div class="account-secondary-actions">
              <button type="button" class="account-action" data-account-action="upload" data-account-id="${id}" data-unavailable="${!uploadJob || !!account.security_pending || job.error_code === 'cpa_plan_excluded'}" title="${job.error_code === 'cpa_plan_excluded' ? '此套餐已排除上传' : uploadJob ? '核对并上传 CPA 凭证' : '生成新授权后可上传'}" disabled>${accountIcon('upload')}<span class="account-action-label">上传</span></button>
              <button type="button" class="account-action account-logs-action" data-account-action="logs" data-account-id="${id}" aria-label="查看日志" title="${accountLogs.account === account.id ? '收起日志' : '查看日志'}" aria-expanded="${accountLogs.account === account.id}">${accountIcon('logs')}<span class="account-action-label">日志</span></button>
              <button type="button" class="account-action account-more-action" data-account-action="more" data-account-id="${id}" title="更多操作" aria-controls="account-actions-menu" aria-expanded="${menuAccountID === account.id}" aria-label="${escapeHTML(account.email || '账号')}的更多操作"><span class="account-action-label">更多</span>${accountIcon('down')}</button>
            </div>
          </div></td>
        </tr>`;
      }).join('');
      mountAccountLogs();
      if (accountLogs.row) accountLogs.row.querySelector('#account-log-viewport').scrollTop = logTop;
      if (logFocused?.isConnected) logFocused.focus({ preventScroll: true });
      else if (logFocusedKey && accountLogs.row) [...accountLogs.row.querySelectorAll('details')].find(detail => detail.dataset.logKey === logFocusedKey)?.querySelector('summary').focus({ preventScroll: true });
      if (focused?.accountId && focused?.accountAction) [...$('account-list').querySelectorAll('[data-account-action]')].find(button => button.dataset.accountId === focused.accountId && button.dataset.accountAction === focused.accountAction && button.className === activeElement.className)?.focus({ preventScroll: true });
    }
    updateAccountControls();
  }
  function accountLogJobs() {
    const account = state.accounts.find(item => item.id === accountLogs.account);
    if (!account) return [];
    const indexed = new Map();
    for (const job of [...state.jobs, ...accountLogs.jobs]) {
      if (job.account_id === account.id && (!indexed.has(idOf(job)) || (job.revision || 0) >= (indexed.get(idOf(job)).revision || 0))) indexed.set(idOf(job), job);
    }
    const jobs = [...indexed.values()];
    jobs.sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')));
    return jobs;
  }
  function jobsForAccount() {
    const jobs = accountLogJobs();
    const account = state.accounts.find(item => item.id === accountLogs.account);
    const selected = accountLogs.scope === 'history' ? accountLogs.selected || idOf(jobs[0]) : account?.last_job_id;
    return jobs.filter(job => idOf(job) === selected).slice(0, 1);
  }
  function resetAccountLogs() {
    accountLogs.read++;
    accountLogCycle = null;
    accountLogs.row?.remove();
    Object.assign(accountLogs, { account: '', scope: 'current', selected: '', query: '', filter: 'all', follow: true, loading: false, error: '', updated: '', signature: '', row: null, jobs: [], hasMore: false });
    accountLogs.cache.clear();
  }
  function toggleAccountLogs(account) {
    const close = accountLogs.account === account.id;
    resetAccountLogs();
    if (!close) accountLogs.account = account.id;
    renderAccounts();
    if (!close) loadAccountLogs();
  }
  function mountAccountLogs() {
    const anchor = [...$('account-list').querySelectorAll('.account-row')].find(row => row.dataset.accountRow === accountLogs.account);
    if (!anchor) return;
    if (!accountLogs.row) {
      const row = document.createElement('tr');
      row.className = 'account-log-row';
      row.innerHTML = `<td colspan="4"><section class="account-log-panel" aria-label="账号操作日志">
        <div class="account-log-header"><div class="account-log-heading">${accountIcon('logs')}<strong>操作日志</strong><span data-log-count></span></div>
          <div class="account-log-tools"><div class="account-log-filters" aria-label="日志范围"><button type="button" data-account-log-action="current">最近一次</button><button type="button" data-account-log-action="history">历史任务</button></div>
          <button type="button" class="account-log-tool" data-account-log-action="refresh" aria-label="刷新操作日志" title="刷新">${accountIcon('refresh')}</button>
          <button type="button" class="account-log-text-tool" data-account-log-action="copy">${accountIcon('copy')}<span>复制摘要</span></button>
          <button type="button" class="account-log-text-tool" data-account-log-action="export">${accountIcon('download')}<span>导出 JSON</span></button></div>
        </div>
        <label class="account-log-history" hidden><span>选择任务</span><select data-log-job aria-label="选择历史任务"></select><small data-log-history-note></small></label>
        <div class="account-log-read-error" role="status" hidden></div><div class="account-log-overview"></div>
        <div class="account-log-result" aria-label="排查建议" hidden></div>
        <div class="account-log-toolbar"><div class="account-log-filters" aria-label="日志筛选"><button type="button" data-account-log-action="all">全部</button><button type="button" data-account-log-action="errors">异常 <span data-log-errors>0</span></button></div><label class="account-log-search">${accountIcon('search')}<input type="search" data-log-search aria-label="搜索操作日志" placeholder="搜索阶段、原因或代码" autocomplete="off"></label><span data-log-matches></span></div>
        <div class="account-log-column-head"><span>时间 / 间隔</span><span>级别</span><span>执行阶段与结果</span></div>
        <div id="account-log-viewport" class="account-log-viewport" tabindex="0" role="region" aria-label="操作日志内容"></div>
        <div class="account-log-footer"><span data-log-update role="status"></span><button type="button" data-account-log-action="follow" aria-pressed="true">跟随最新</button></div>
      </section></td>`;
      row.querySelector('[data-log-search]').addEventListener('input', event => {
        accountLogs.query = event.target.value; accountLogs.follow = false; renderAccountLogs();
      });
      row.querySelector('[data-log-job]').addEventListener('change', event => {
        accountLogs.selected = event.target.value; accountLogs.follow = false;
        renderAccountLogs(); loadAccountLogs();
      });
      accountLogs.row = row;
      row.addEventListener('toggle', event => {
        if (event.target.matches('details[open]') && accountLogs.follow) { accountLogs.follow = false; renderAccountLogs(); }
      }, true);
      row.querySelector('#account-log-viewport').addEventListener('scroll', event => {
        const viewport = event.currentTarget;
        if (viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop > 12 && accountLogs.follow) {
          accountLogs.follow = false; renderAccountLogs();
        }
      });
    }
    anchor.after(accountLogs.row);
    renderAccountLogs();
  }
  function visibleLogEntries() {
    return jobsForAccount().flatMap(job => filterLogEntries(accountLogs.cache.get(idOf(job))?.entries || [], job, accountLogs.filter, accountLogs.query).map(entry => ({ ...entry, job_id: idOf(job) })));
  }
  function accountLogReport() {
    return buildLogReport(jobsForAccount(), accountLogs.cache, {
      ui_version: JSON.parse($('build-info').textContent).ui_version,
      runtime_version: state.capabilities?.runtime?.runtime_version || 'unknown',
      scope: accountLogs.scope, history_limited: accountLogs.hasMore,
      read_incomplete: !!accountLogs.error || accountLogs.loading,
    });
  }
  function renderAccountLogs() {
    const row = accountLogs.row;
    if (!row) return;
    const jobs = jobsForAccount();
    const job = jobs[0];
    const entries = visibleLogEntries();
    const saved = job && accountLogs.cache.get(idOf(job));
    const all = saved?.entries || [];
    row.querySelector('[data-log-count]').textContent = `${all.length} 条事件`;
    row.querySelector('[data-log-errors]').textContent = String(all.filter(logEntryIsIssue).length);
    for (const action of ['current', 'history', 'all', 'errors', 'follow']) row.querySelector(`[data-account-log-action="${action}"]`).setAttribute('aria-pressed', String([accountLogs.scope, accountLogs.filter, accountLogs.follow && 'follow'].includes(action)));
    row.querySelector('[data-account-log-action="refresh"]').disabled = accountLogs.loading;
    for (const action of ['copy', 'export']) row.querySelector(`[data-account-log-action="${action}"]`).disabled = !job;
    const history = row.querySelector('.account-log-history');
    history.hidden = accountLogs.scope !== 'history';
    const options = accountLogJobs().map(item => `<option value="${escapeHTML(idOf(item))}">${escapeHTML(`${date(item.created_at)} · ${operationLabel(item)} · ${jobLabel(item)} · ${idOf(item).slice(0, 8)}`)}</option>`).join('');
    const select = row.querySelector('[data-log-job]');
    if (select.dataset.options !== options) { select.innerHTML = options; select.dataset.options = options; }
    if (job) select.value = idOf(job);
    row.querySelector('[data-log-history-note]').textContent = accountLogs.hasMore ? '仅列出最近 200 次任务' : `${accountLogJobs().length} 次任务`;
    const error = row.querySelector('.account-log-read-error');
    error.hidden = !accountLogs.error; error.textContent = accountLogs.error;
    const retention = logRetention(saved);
    row.querySelector('[data-log-update]').textContent = [accountLogs.loading ? '正在读取…' : accountLogs.updated ? `更新于 ${logTime(accountLogs.updated)}` : '尚未读取', '间隔为相邻事件记录时间差'].join(' · ');
    row.querySelector('[data-log-matches]').textContent = accountLogs.query || accountLogs.filter === 'errors' ? `${entries.length} / ${all.length} 条` : '';
    const overview = row.querySelector('.account-log-overview');
    const timing = logTiming(all);
    const lastPhase = [...all].reverse().find(entry => entry.error_stage || entry.phase);
    const overviewHTML = job ? `<div class="account-log-summary"><strong>${escapeHTML(operationLabel(job))}</strong><span class="account-log-outcome" data-status="${escapeHTML(job.status)}">${escapeHTML(jobLabel(job))}</span><span>${escapeHTML(job.method === 'browser' ? '浏览器授权' : '密码与 2FA')}</span><span>${job.automatic ? '自动触发' : '手动触发'}</span>${timing ? `<span>记录跨度 <b>${escapeHTML(timing)}</b></span>` : ''}</div><div class="account-log-reference"><span>任务 <code>${escapeHTML(idOf(job))}</code></span><time>${escapeHTML(date(job.created_at))}</time></div>${lastPhase ? `<div class="account-log-reference"><span>最后记录阶段：${escapeHTML(phaseLabel(lastPhase.error_stage || lastPhase.phase))}</span>${securityResult(job) ? `<span>当前已确认：${escapeHTML(securityResult(job))}</span>` : ''}</div>` : ''}${retention ? `<p class="account-log-retention">${escapeHTML(retention)}</p>` : ''}` : '';
    if (overview.innerHTML !== overviewHTML) overview.innerHTML = overviewHTML;
    const result = row.querySelector('.account-log-result');
    result.hidden = !job || (!job.error_code && !['failed', 'conflict', 'interaction_required', 'security_uncertain', 'commit_unknown', 'saved_not_adopted', 'interrupted', 'expired'].includes(job.status));
    if (!result.hidden) {
      const codes = [['错误', job.error_code], ['阶段', job.error_stage], ['原因', job.error_reason]].filter(([,value]) => value);
      result.classList.toggle('is-error', ['failed', 'conflict', 'security_uncertain', 'commit_unknown', 'saved_not_adopted'].includes(job.status));
      const resultHTML = `<strong>${escapeHTML(jobDiagnostic(job) || jobLabel(job))}</strong><p>${escapeHTML(diagnosticAdvice(job))}</p>${codes.length ? `<div class="account-log-codes">${codes.map(([label,value])=>`<span>${label} <code>${escapeHTML(value)}</code></span>`).join('')}</div>` : ''}`;
      if (result.innerHTML !== resultHTML) result.innerHTML = resultHTML;
    }
    const signature = JSON.stringify([jobs, entries, saved?.legacy, saved?.truncated, accountLogs.filter, accountLogs.query, accountLogs.scope, accountLogs.loading && !all.length]);
    if (signature === accountLogs.signature) return;
    accountLogs.signature = signature;
    const viewport = row.querySelector('#account-log-viewport');
    const top = viewport.scrollTop;
    const opened = new Set([...viewport.querySelectorAll('details[open]')].map(detail => detail.dataset.logKey));
    const focused = document.activeElement?.closest('details')?.dataset.logKey;
    viewport.innerHTML = renderLogEntries(entries, all, job, opened) || `<p class="account-log-empty">${accountLogs.loading ? '正在读取操作日志…' : saved?.legacy ? '历史任务没有完整事件记录，可查看上方任务结果。' : accountLogs.query ? '没有匹配的日志，请调整搜索词。' : accountLogs.filter === 'errors' ? '本次任务暂无异常日志' : job ? '此任务暂无操作日志' : '此账号暂无任务记录'}</p>`;
    if (focused) [...viewport.querySelectorAll('details')].find(detail => detail.dataset.logKey === focused)?.querySelector('summary').focus({ preventScroll: true });
    viewport.scrollTop = accountLogs.follow ? viewport.scrollHeight : top;
  }
  function loadAccountLogs(force = false) {
    if (!accountLogs.account || !state.open || !connected() || navigator.onLine === false || document.hidden) return Promise.resolve();
    if (accountLogCycle?.epoch === state.epoch && accountLogCycle.account === accountLogs.account && accountLogCycle.read === accountLogs.read) {
      accountLogCycle.pending = true;
      accountLogCycle.force ||= force;
      return accountLogCycle.promise;
    }
    const cycle = { epoch: state.epoch, account: accountLogs.account, read: accountLogs.read, pending: false, force, promise: null };
    const valid = () => current(cycle.epoch) && cycle.account === accountLogs.account && cycle.read === accountLogs.read;
    accountLogCycle = cycle;
    accountLogs.loading = true;
    cycle.promise = (async () => {
      try {
        do {
          const forceRead = cycle.force;
          cycle.pending = false; cycle.force = false;
          await readAccountLogSnapshot(cycle, forceRead);
        } while (cycle.pending && valid() && navigator.onLine !== false && !document.hidden);
      } catch (error) {
        if (valid() && !isDiscarded(error)) accountLogs.error = '日志读取未完成，已保留已加载内容。请点击刷新重试。';
      } finally {
        if (accountLogCycle === cycle) {
          accountLogCycle = null;
          accountLogs.loading = false;
          if (valid()) renderAccountLogs();
        }
      }
    })();
    return cycle.promise;
  }
  async function readAccountLogSnapshot(cycle, force) {
    const accountID = cycle.account;
    const scope = accountLogs.scope;
    const valid = () => current(cycle.epoch) && accountID === accountLogs.account && cycle.read === accountLogs.read && scope === accountLogs.scope;
    accountLogs.error = ''; renderAccountLogs();
    try {
      const history = await get(`/jobs?account_id=${encodeURIComponent(accountID)}&limit=200`);
      if (!valid()) return;
      if (!Array.isArray(history.items) || history.items.some(job => !idOf(job) || job.account_id !== accountID)) throw new Error('invalid_job_response');
      const currentID = scope === 'history' ? accountLogs.selected : state.accounts.find(account => account.id === accountID)?.last_job_id;
      const retainedJob = accountLogJobs().find(job => idOf(job) === currentID);
      accountLogs.jobs = [...history.items, ...(retainedJob && !history.items.some(job => idOf(job) === currentID) ? [retainedJob] : [])];
      accountLogs.hasMore = !!history.has_more;
      if (currentID && !history.items.some(job => idOf(job) === currentID)) {
        let detail;
        try { detail = await get(`/detail?id=${encodeURIComponent(currentID)}`); }
        catch (error) {
          if (valid() && error.status === 404) {
            accountLogs.error = '所选任务已不存在，已保留此前加载的内容。';
            return;
          }
          throw error;
        }
        if (!valid()) return;
        if ((scope === 'history' ? accountLogs.selected : state.accounts.find(account => account.id === accountID)?.last_job_id) !== currentID) { cycle.pending = true; return; }
        const job = detail.job || detail;
        if (idOf(job) !== currentID || job.account_id !== accountID || typeof job.status !== 'string') throw new Error('invalid_job_response');
        accountLogs.jobs = [...accountLogs.jobs.filter(item => idOf(item) !== currentID), job];
      }
      const jobs = jobsForAccount();
      const pending = jobs.filter(job => force || accountLogs.cache.get(idOf(job))?.revision !== job.revision || !accountLogs.cache.has(idOf(job)) || activeStates.has(job.status));
      for (let index = 0; index < pending.length; index += 4) {
        const responses = await Promise.allSettled(pending.slice(index, index + 4).map(async job => {
          return { job, response: await readLogSnapshot(job, valid) };
        }));
        if (!valid()) return;
        for (const response of responses) {
          if (response.status === 'rejected') { accountLogs.error = '日志读取未完成，已保留已加载内容。请点击刷新重试。'; continue; }
          const { job, response: data } = response.value;
          if (data) accountLogs.cache.set(idOf(job), data);
        }
        renderAccountLogs();
        if (responses.every(response => response.status === 'rejected') || navigator.onLine === false || document.hidden) break;
      }
      if (valid() && !accountLogs.error) accountLogs.updated = new Date().toISOString();
    } catch (error) {
      if (valid() && !isDiscarded(error)) accountLogs.error = '日志读取未完成，已保留已加载内容。请点击刷新重试。';
    } finally {
      if (valid()) renderAccountLogs();
    }
  }
  $('account-list').addEventListener('click', async event => {
    const button = event.target.closest('[data-account-log-action]');
    if (!button || button.disabled) return;
    const action = button.dataset.accountLogAction;
    if (action === 'current' || action === 'history') { accountLogs.scope = action; renderAccountLogs(); await loadAccountLogs(); }
    else if (action === 'all' || action === 'errors') { accountLogs.filter = action; renderAccountLogs(); }
    else if (action === 'refresh') await loadAccountLogs(true);
    else if (action === 'follow') {
      accountLogs.follow = !accountLogs.follow; renderAccountLogs();
      if (accountLogs.follow) { const viewport = $('account-log-viewport'); viewport.scrollTop = viewport.scrollHeight; }
    } else if (action === 'copy' || action === 'export') {
      const accountID = accountLogs.account;
      const report = accountLogReport();
      try {
        if (action === 'copy') await navigator.clipboard.writeText(logReportText(report));
        else {
          const url = URL.createObjectURL(new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' }));
          const anchor = document.createElement('a'); anchor.href = url; anchor.download = `login-diagnostics-${report.jobs[0]?.id || 'account'}.json`; anchor.click(); URL.revokeObjectURL(url);
        }
        if (accountID === accountLogs.account) accountLogs.row.querySelector('[data-log-update]').textContent = action === 'copy' ? '已复制所选任务摘要与全部已加载事件' : '已导出所选任务与全部已加载事件';
      } catch {
        if (accountID === accountLogs.account) { accountLogs.error = action === 'copy' ? '复制失败，请使用“导出 JSON”保存排查信息。' : '导出失败，请重试。'; renderAccountLogs(); }
      }
    }
  });

  function setReadError(name, error) {
    if (isDiscarded(error)) return;
    if (error) state.readErrors.set(name, error.status === 403 ? 'CPA 拒绝了页面访问（403）。' : error.status === 429 ? 'CPA 正在限流（429）。' : '');
    else state.readErrors.delete(name);
    renderReadStatus();
    updateControls();
  }
  function renderReadStatus() {
    const failed = [...state.readErrors.keys()];
    const reasons = [...new Set(state.readErrors.values())].join('');
    const message = failed.length ? `${failed.join('、')}暂未刷新，正在等待重新读取。${reasons}` : '';
    for (const id of ['login-history-read-note', 'login-settings-read-note']) {
      $(id).hidden = !failed.length;
      $(id).textContent = message;
    }
  }
  async function readAccountList() {
    const data = await snapshotRead('/accounts');
    if (!Array.isArray(data?.items) || !data.items.every(account => account && typeof account.id === 'string' && account.id && Number.isSafeInteger(account.revision))) {
      throw new Error('管理接口返回了无效的账号列表。');
    }
    return data.items;
  }
  async function loadAccounts(initialAccounts) {
    const epoch = state.epoch;
    const sequence = ++state.accountRead;
    const isCurrent = () => current(epoch) && sequence === state.accountRead;
    async function read(response, label, apply) {
      try {
        const data = await response;
        if (!isCurrent()) return false;
        apply(data);
        setReadError(label, null);
        return true;
      } catch (error) {
        if (isCurrent() && !isDiscarded(error)) {
          state.accountsAvailable = false;
          setReadError(label, error);
        }
        return false;
      } finally {
        if (isCurrent()) renderAccounts();
      }
    }
    const [validAccounts, validSettings] = await Promise.all([
      read(initialAccounts ?? readAccountList(), '账号列表', accounts => {
        state.accounts = accounts;
        state.accountsLoaded = true;
      }),
      read(snapshotRead('/accounts/settings'), '巡检设置', data => {
        if (!Number.isSafeInteger(data?.revision) || typeof data.inspection_enabled !== 'boolean' || typeof data.auto_relogin_enabled !== 'boolean' || !Number.isFinite(data.interval_minutes)) throw new Error('invalid_settings');
        state.accountSettings = data;
        renderAccountSettings();
      }),
    ]);
    if (!isCurrent()) return;
    state.accountsAvailable = validAccounts && validSettings;
    updateAccountControls();
  }
  async function accountAction(action) {
    if (accountBusy()) return;
    const epoch = state.epoch;
    state.busy.add('account'); updateAccountControls(); feedback(); editorFeedback();
    try { await action(epoch); }
    catch (error) { if (current(epoch)) { fail(error); try { await loadAccounts(); } catch {} } }
    finally { state.busy.delete('account'); if (current(epoch)) updateAccountControls(); }
  }
  function showAccountImport(event) {
    if (accountBusy()) return;
    clearSecrets(); closeAccountEditors(); feedback();
    importOpener = event?.currentTarget || $('account-import-open');
    updateAccountControls();
    $('account-import-dialog').showModal();
    $('account-import-text').focus({ preventScroll: true });
  }
  async function showAccountEditor(account) {
    closeAccountMenu();
    if (accountBusy()) return;
    clearSecrets(); closeAccountEditors(); feedback();
    editorOriginID = account.id;
    const epoch = state.epoch;
    const sequence = ++state.accountDetailRead;
    state.accountEditing = { ...account };
    state.accountDetailLoading = true; state.accountDetailFailed = false;
    editorFeedback();
    $('account-editor-retry').hidden = true;
    $('account-editor-identity').textContent = account.email || '';
    $('account-login-options').hidden = !!account.auth_id;
    $('account-login-target').innerHTML = '<option value="">创建新的 CPA 凭证</option>' + state.targets.filter(target => target.can_update !== false).map(target => `<option value="${escapeHTML(target.auth_id)}">更新 ${escapeHTML(target.email ? `${target.email} · ${target.auth_id}` : target.auth_id)}</option>`).join('');
    $('account-editor-title').textContent = '账号资料';
    $('account-label').value = account.label || '';
    $('account-proxy-url').value = account.proxy_url || '';
    $('account-email').value = account.email || '';
    renderEffectiveProxy(account);
    $('account-auto-relogin').checked = !!account.auto_relogin;
    $('account-secret-editor').hidden = false;
    $('account-secret-editor').querySelectorAll('input').forEach(input => { input.required = false; });
    $('account-security-recovery').hidden = true;
    $('account-security-recovery-secret').value = '';
    $('account-editor-note').textContent = '正在读取登录资料…';
    renderAccounts();
    $('account-editor-dialog').showModal();
    $('account-editor-close').focus({ preventScroll: true });
    try {
      const detail = await get(`/accounts/detail?id=${encodeURIComponent(account.id)}`);
      if (!current(epoch) || sequence !== state.accountDetailRead || state.view !== 'accounts' || state.accountEditing?.id !== account.id) {
        if (detail.security_recovery) for (const name of Object.keys(detail.security_recovery)) detail.security_recovery[name] = '';
        if (detail.credentials) for (const name of Object.keys(detail.credentials)) detail.credentials[name] = '';
        return;
      }
      state.accountEditing = { ...detail.account };
      renderEffectiveProxy(detail.account);
      $('account-label').value = detail.account.label || '';
      $('account-proxy-url').value = detail.account.proxy_url || '';
      $('account-auto-relogin').checked = !!detail.account.auto_relogin;
      $('account-email').value = detail.credentials.email || '';
      $('account-password').value = detail.credentials.password || '';
      $('account-totp').value = detail.credentials.totp_secret || '';
      editorCredentials = { password: $('account-password').value, totp_secret: $('account-totp').value.trim() };
      for (const name of Object.keys(detail.credentials)) detail.credentials[name] = '';
      renderSecurityRecovery(detail);
      $('account-editor-note').textContent = detail.account.auth_id ? detail.account.auth_id : '尚未绑定 CPA 凭证。首次登录成功后可开启自动登录。';
    } catch (error) {
      if (current(epoch) && sequence === state.accountDetailRead) {
        $('account-email').value = account.email || '';
        state.accountDetailFailed = true;
        $('account-editor-note').textContent = '';
        editorFeedback(`${error instanceof TypeError ? '登录资料读取失败。' : messageFor(error.code)} 可点击重试重新读取。`);
        $('account-editor-retry').hidden = false;
      }
    } finally {
      if (current(epoch) && sequence === state.accountDetailRead) { state.accountDetailLoading = false; updateAccountControls(); }
    }
  }
  function renderEffectiveProxy(account) {
    const source = { account: '账号指定', login_default: '默认登录代理', credential: 'CPA 凭证代理', direct: '直接连接' }[account.proxy_source] || '来源未提供';
    $('account-effective-proxy').textContent = `${source} · ${account.effective_proxy_url || '出口尚未读取'}`;
    $('account-effective-proxy').classList.toggle('error-note', !!account.proxy_error);
  }
  function closeAccountLogin(restoreFocus = false) {
    const target = accountLoginTarget;
    accountLoginTarget = null;
    if ($('account-login-dialog').open) $('account-login-dialog').close();
    if (!restoreFocus || !target) return;
    if ($('account-editor-dialog').open) $('account-login-start').focus({ preventScroll: true });
    else focusAccount(target.id, 'start');
  }
  function openAccountLogin(account, targetID = '') {
    if (accountBusy() || manualLoginBlocked(account) || !accountLoginAvailable() || account.credentials_error || securityProxyIssue(account)) return;
    accountLoginTarget = { id: account.id, targetID, revision: account.revision };
    $('account-login-identity').textContent = account.email || account.masked_email || '';
    $('account-login-upload').checked = readWorkflowPreference('login').upload;
    $('account-login-dialog').showModal();
  }
  async function startAccount(account, targetID, upload) {
    if (accountBusy() || manualLoginBlocked(account) || !accountLoginAvailable() || account.credentials_error || securityProxyIssue(account)) return;
    const epoch = state.epoch;
    const fromEditor = $('account-editor-dialog').open;
    state.accountStarting = account.id;
    renderAccounts();
    await accountAction(async requestEpoch => {
      const result = await write('/accounts/start', { id: account.id, revision: account.revision, upload_to_cpa: upload, ...(targetID ? { target_auth_id: targetID } : {}) });
      if (!current(requestEpoch)) return;
      const job = result.job || result;
      const saved = state.accounts.find(item => item.id === account.id);
      if (saved) applyAccountJob(saved, job);
      state.jobs = [job, ...state.jobs.filter(item => idOf(item) !== idOf(job))];
      if (state.view === 'accounts') selectJob(idOf(job));
      if (fromEditor) closeAccountEditor();
      renderAccounts();
      await reload();
    });
    if (current(epoch) && state.accountStarting === account.id) {
      state.accountStarting = '';
      renderAccounts();
      if (fromEditor && !$('account-editor-dialog').open && state.view === 'accounts') focusAccount(account.id);
    }
  }
  function taskActionBlocked() { return !state.open || !connected() || navigator.onLine === false || loading || !state.jobsAvailable || state.readErrors.has('任务详情') || state.busy.has('action') || state.busy.has('history-delete'); }
  function updateControls() {
    const unavailable = !state.open || !connected() || loading;
    $('login-refresh').disabled = unavailable;
    $('login-export').disabled = unavailable || !state.job || taskLogs.loading;
    updateHistoryDelete();
    $('login-job-detail').querySelectorAll('[data-login-action]').forEach(button => { button.disabled = taskActionBlocked() || button.hasAttribute('data-plan-blocked'); });
    updateAccountControls();
  }
  function updateHistoryDelete() {
    const button = $('login-history-delete-open');
    button.disabled = !state.job?.can_delete || taskActionBlocked();
    button.title = state.job && !state.job.can_delete ? '可删除已结束且无需核对的记录' : '删除所选历史记录';
  }
  async function loadStorageStatus() {
    const sequence = ++state.storageRead;
    const epoch = state.epoch;
    state.vault = state.capabilities?.vault || null;
    updateControls();
    try {
      const result = await get('/vault/status');
      if (!current(epoch) || sequence !== state.storageRead) return;
      state.vault = result;
      updateControls();
    } catch (error) { if (current(epoch) && !isDiscarded(error)) updateControls(); }
  }
  function renderJobs() {
    const signature = JSON.stringify([state.jobs, state.selected, taskList.query, taskList.filter, taskList.hasMore]);
    if (state.jobSignature === signature) { revealSelectedJob(); return; }
    state.jobSignature = signature;
    $('login-job-count').textContent = state.jobs.length ? String(state.jobs.length) : '';
    const jobs = filterTaskJobs(state.jobs, taskList.filter, taskList.query);
    $('login-task-total').textContent = `${jobs.length}${jobs.length !== state.jobs.length ? ` / ${state.jobs.length}` : ''}${taskList.hasMore ? ' · 最近 200 条' : ' 条'}`;
    $('login-task-filter').value = taskList.filter;
    $('login-history-summary').textContent = `${taskList.hasMore ? '最近' : '共'} ${state.jobs.length} 条记录`;
    const focused = document.activeElement?.dataset.loginJob;
    $('login-jobs').innerHTML = jobs.length ? jobs.map(job => {
      const id = idOf(job);
      const status = jobLabel(job);
      return `<button type="button" class="login-job${state.selected === id ? ' selected' : ''}" data-login-job="${escapeHTML(id)}" data-active="${activeStates.has(job.status)}" aria-pressed="${state.selected === id}">
        <span class="login-job-top"><span class="login-job-icon">${accountIcon(job.action === 'reset_totp' ? 'reset' : job.action === 'logout_all' ? 'logout' : 'login')}</span><span class="login-job-operation">${escapeHTML(operationLabel(job))}</span><time title="${escapeHTML(date(job.created_at))}">${escapeHTML(compactDate(job.created_at))}</time></span>
        <span class="login-job-identity"><strong>${escapeHTML(job.email || (job.method === 'browser' ? '浏览器授权' : '账号登录'))}</strong></span>
        <span class="login-job-footer"><span class="tag ${taskTone(job)}">${escapeHTML(status)}</span>${activeStates.has(job.status) && job.phase ? `<small>${escapeHTML(phaseLabel(job.phase))}</small>` : job.automatic ? '<small>自动触发</small>' : ''}</span>
      </button>`;
    }).join('') : `<div class="login-jobs-empty empty-state">${accountIcon('task')}<h3>${state.jobs.length ? '没有匹配的记录' : '暂无历史记录'}</h3><p>${state.jobs.length ? '调整关键词或筛选条件。' : '登录后，在这里查看进度与结果。'}</p></div>`;
    if (focused) [...$('login-jobs').querySelectorAll('[data-login-job]')].find(button => button.dataset.loginJob === focused)?.focus({ preventScroll: true });
    $('login-export').disabled = !state.job || taskLogs.loading;
    revealSelectedJob();
  }
  function revealSelectedJob() {
    if (!state.revealJob || state.view !== 'tasks') return;
    const list = $('login-jobs');
    const selected = list.querySelector('.login-job.selected');
    if (!selected || selected.dataset.loginJob !== state.selected || !list.clientHeight) return;
    const bounds = list.getBoundingClientRect();
    const row = selected.getBoundingClientRect();
    const top = bounds.top + list.clientTop;
    const bottom = top + list.clientHeight;
    if (getComputedStyle(list).display === 'flex') {
      if (row.left < bounds.left) list.scrollLeft += row.left - bounds.left;
      else if (row.right > bounds.right) list.scrollLeft += row.right - bounds.right;
    } else if (row.top < top || row.height > list.clientHeight) list.scrollTop += row.top - top;
    else if (row.bottom > bottom) list.scrollTop += row.bottom - bottom;
    state.revealJob = false;
  }
  function renderDetail(force = false) {
    const job = state.job;
    updateHistoryDelete();
    if (!job) return;
    const candidate = state.candidate;
    const signature = JSON.stringify([idOf(job), job.revision, job.email, job.status, job.phase, job.error_code, job.error_stage, job.error_reason, job.runtime_code, job.vault_error, job.auto_commit, job.enable_credential, job.credential_enabled, job.action, job.security_status, job.totp_reset, job.devices_logged_out, job.reauthorize, candidate, state.urls.get(idOf(job))]);
    if (!force && state.detailSignature === signature) return;
    state.detailSignature = signature;
    const id = idOf(job);
    const url = safeAuthorizationURL(job.authorization_url || state.urls.get(id));
    const awaiting = job.status === 'awaiting_commit';
    const restrictions = Array.isArray(candidate?.restrictions) ? candidate.restrictions : [];
    const tone = taskTone(job);
    const heading = `<header class="login-detail-header" data-active="${activeStates.has(job.status)}"><div class="history-result-line"><span class="login-detail-eyebrow">${accountIcon(job.action === 'reset_totp' ? 'reset' : job.action === 'logout_all' ? 'logout' : 'login')}${escapeHTML(operationLabel(job))}${job.automatic ? ' · 自动触发' : ''}</span><span class="tag ${tone}" role="status">${escapeHTML(jobLabel(job))}</span></div><h3 class="login-detail-identity">${escapeHTML(job.email || '等待身份核验')}</h3><div class="login-detail-meta"><span>${job.method === 'browser' ? '浏览器授权' : '密码与 2FA'}</span><time>${escapeHTML(date(job.created_at))}</time><span data-task-span></span></div></header>`;
    let body = '';
    if (job.error_code) body += `<div class="login-diagnostic"><p class="field-error">${escapeHTML(jobDiagnostic(job) || (job.automatic ? automaticError(job.error_code) : messageFor(job.error_code)))}</p><p class="field-note">${escapeHTML(diagnosticAdvice(job))}</p>${job.error_stage ? `<p class="field-note">停止步骤：${escapeHTML(diagnosticStage(job.error_stage))}</p>` : ''}</div>`;
    if (isSecurityJob(job)) body += securityProgress(job);
    if (securityResult(job) && !['committed', 'succeeded'].includes(job.status)) body += `<p class="account-security-result">${escapeHTML(securityResult(job))}。${job.reauthorize ? '后续授权结果请结合当前任务状态核对。' : '本次未选择重新授权 CPA。'}</p>`;
    if (job.status === 'security_uncertain' && !job.error_code) body += '<div class="notice error">安全操作结果尚未确认，请在账号资料中核对保留的信息，勿重复重置或退出。</div>';
    if (job.vault_error) body += '<p class="field-error">授权提交已完成，但登录资料未保存。请核对凭证状态后单独保存资料，无需重复登录。</p>';
    if (job.status === 'waiting_browser' && job.method === 'browser') body += `<div class="login-browser-action">${url ? `<a class="login-auth-link" href="${escapeHTML(url)}" target="_blank" rel="noopener noreferrer">打开 OpenAI 官方授权页面<svg class="nav-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M14 4h6v6M20 4 10 14M10 4H4v16h16v-6"/></svg></a>` : '<p class="field-note">授权链接暂不可用，请刷新此任务。</p>'}<p class="field-note">完成官方登录后，复制地址栏中的完整回调地址。即使回调页面无法打开，也可在这里提交。</p><label for="login-callback">回调地址<input id="login-callback" type="text" autocomplete="off" maxlength="8192" placeholder="http://localhost:1455/auth/callback?…"></label><button type="button" class="subtle small" data-login-action="callback">提交回调并核验</button></div>`;
    if (awaiting && candidate) body += `<div class="login-candidate"><h4>核对待上传凭证</h4><dl class="evidence-facts"><div><dt>账号</dt><dd>${escapeHTML(candidate.email || '未提供')}</dd></div><div><dt>账号／工作区</dt><dd>${escapeHTML(candidate.account_id || '未提供')}</dd></div><div><dt>授权套餐</dt><dd>${escapeHTML(planLabel(candidate.plan_type))}</dd></div><div><dt>用户 ID</dt><dd>${escapeHTML(candidate.user_id || '未提供')}</dd></div><div><dt>提交动作</dt><dd>${job.enable_credential ? '保存并启用 CPA 凭证' : candidate.intent === 'reauthorize' || job.intent === 'reauthorize' ? '更新已有凭证的授权' : '创建默认人工禁用的新凭证'}</dd></div><div><dt>目标凭证</dt><dd>${escapeHTML(candidate.target_auth_id || '导入时创建')}</dd></div><div><dt>授权有效至</dt><dd>${escapeHTML(date(job.expires_at))}</dd></div></dl>${restrictions.length ? `<ul class="restriction-list">${restrictions.map(value => `<li>${escapeHTML(reasonLabel(value))}</li>`).join('')}</ul>` : ''}<div class="button-row login-candidate-actions"><button type="button" class="primary" data-login-action="commit" ${restrictions.includes('cpa_plan_excluded') ? 'data-plan-blocked' : ''}>确认上传 CPA</button><button type="button" class="subtle" data-login-action="discard">放弃授权</button></div></div>`;
    else if (awaiting) body += '<p class="field-note">正在读取待保存的账号信息…</p>';
    if (job.status === 'committed') body += '<p class="login-task-result-note">授权已保存到 CPA，相关检测规则保持停止。</p>';
    if (['saved_not_adopted', 'commit_unknown'].includes(job.status)) body += '<div class="notice error">保存结果尚未确认。请刷新任务并核对 CPA 凭证，勿重新登录或重复导入。</div>';
    if (job.status === 'interaction_required' && !job.error_code) body += `<p class="field-note">${escapeHTML(jobDiagnostic(job))}</p>`;
    if (job.status === 'committing') body += `<p class="field-note">${job.enable_credential ? '正在保存并启用 CPA 凭证。' : '正在保存 CPA 凭证。'}此阶段不能取消。</p>`;
    if (activeStates.has(job.status) && job.phase) body += `<div class="login-current-phase field-note" role="status"><span class="login-phase-indicator" aria-hidden="true"></span><span><small>当前步骤</small><strong>${escapeHTML(phaseLabel(job.phase))}</strong></span></div>`;
    if (cancellableStates.has(job.status)) body += '<button type="button" class="subtle small" data-login-action="cancel">取消登录任务</button>';
    const logPanel = $('login-job-detail').querySelector('.login-task-logs');
    if (logPanel) {
      $('login-job-detail').querySelector('.login-detail-header').outerHTML = heading;
      $('login-job-detail').querySelector('.login-detail-body').innerHTML = body;
    } else {
      $('login-job-detail').innerHTML = `${heading}<div class="login-detail-body">${body}</div>`;
      mountTaskLogs();
    }
    renderTaskLogs();
    $('login-job-detail').querySelectorAll('[data-login-action]').forEach(button => { button.disabled = taskActionBlocked() || button.hasAttribute('data-plan-blocked'); });
  }
  function clearSelectedDetail() {
    state.job = null; state.candidate = null; state.detailSignature = ''; state.detailRead++;
    taskLogs.signature = '';
    $('login-job-detail').innerHTML = state.selected ? '<p class="empty-line" role="status">正在读取任务…</p>' : '<p class="empty-line">请选择一条记录。</p>';
    updateHistoryDelete();
  }
  function selectJob(id) {
    state.selected = id; state.revealJob = true;
    taskLogs.read++; taskLogs.query = ''; taskLogs.filter = 'all'; taskLogs.follow = false; taskLogs.error = ''; taskLogs.updated = ''; taskLogs.loading = false; taskLogs.cycle = null;
    clearSelectedDetail();
  }
  function openHistoryDelete() {
    if ($('login-history-delete-open').disabled) return;
    const job = state.job;
    historyDeleteTarget = { id: idOf(job), revision: job.revision };
    $('login-history-delete-summary').textContent = [job.email, operationLabel(job), compactDate(job.created_at)].filter(Boolean).join(' · ');
    $('login-history-delete-error').hidden = true;
    $('login-history-delete-error').textContent = '';
    $('login-history-delete-yes').disabled = false;
    $('login-history-delete-yes').textContent = '确认删除';
    $('login-history-delete-no').disabled = false;
    $('login-history-delete').showModal();
  }
  function closeHistoryDelete(restoreFocus = false) {
    historyDeleteTarget = null;
    if ($('login-history-delete').open) $('login-history-delete').close();
    if (restoreFocus && state.view === 'tasks') {
      const button = $('login-history-delete-open');
      (button.disabled ? $('login-task-search') : button).focus({ preventScroll: true });
    }
  }
  function removeHistoryJob(id) {
    state.jobs = state.jobs.filter(job => idOf(job) !== id);
    state.urls.delete(id);
    taskLogs.cache.delete(id);
    accountLogs.cache.delete(id);
    accountLogs.jobs = accountLogs.jobs.filter(job => idOf(job) !== id);
    if (state.selected === id) {
      const next = filterTaskJobs(state.jobs, taskList.filter, taskList.query)[0];
      selectJob(next ? idOf(next) : '');
    }
    renderJobs();
  }
  async function deleteHistoryJob() {
    const target = historyDeleteTarget;
    if (!target || state.busy.has('history-delete')) return;
    const epoch = state.epoch;
    const confirm = $('login-history-delete-yes');
    const cancel = $('login-history-delete-no');
    const errorLine = $('login-history-delete-error');
    state.busy.add('history-delete');
    confirm.disabled = cancel.disabled = true;
    confirm.textContent = '删除中…';
    errorLine.hidden = true;
    feedback();
    updateHistoryDelete();
    try {
      await write('/jobs', { job_id: target.id, revision: target.revision }, 'DELETE');
      if (!current(epoch)) return;
      state.jobsRead++;
      state.accountRead++;
      removeHistoryJob(target.id);
      closeHistoryDelete();
      $('login-task-search').focus({ preventScroll: true });
      await reload();
      if (current(epoch) && state.view === 'tasks') feedback('已删除记录');
    } catch (error) {
      if (!current(epoch)) return;
      if (error.status === 404 || error.outcomeUnknown || error instanceof TypeError) {
        await reload();
        if (!current(epoch)) return;
      }
      if (error.status === 404 || state.selected !== target.id) {
        closeHistoryDelete(true);
        feedback('记录已移除，历史列表已更新。');
        return;
      }
      errorLine.textContent = error.outcomeUnknown || error instanceof TypeError ? '删除结果未确认，请关闭窗口后刷新历史记录。'
        : error.status === 409 ? '记录状态已变化，请关闭窗口并刷新后重试。'
        : messageFor(error.code);
      errorLine.hidden = false;
    } finally {
      state.busy.delete('history-delete');
      if (current(epoch)) {
        cancel.disabled = false;
        confirm.textContent = '确认删除';
        updateHistoryDelete();
        if ($('login-history-delete').open) cancel.focus();
      }
    }
  }
  function mountTaskLogs() {
    $('login-job-detail').insertAdjacentHTML('beforeend', `<section class="login-task-logs" aria-label="任务执行日志">
      <div class="login-task-log-heading"><div><strong>执行日志</strong><span data-task-log-count></span></div><div class="button-row"><button type="button" class="account-log-text-tool" data-task-log-action="copy">${accountIcon('copy')}复制诊断</button><button type="button" class="account-log-text-tool" data-task-log-action="export">${accountIcon('download')}导出 JSON</button></div></div>
      <div class="login-task-log-toolbar"><div class="account-log-filters" aria-label="执行日志筛选"><button type="button" data-task-log-action="all" aria-pressed="true">全部</button><button type="button" data-task-log-action="errors" aria-pressed="false">异常 <span data-task-log-errors>0</span></button></div><label class="account-log-search">${accountIcon('search')}<input type="search" data-task-log-search aria-label="搜索任务日志" placeholder="搜索阶段、请求或错误码" autocomplete="off"></label><span data-task-log-matches></span></div>
      <div class="account-log-read-error" data-task-log-error role="status" hidden></div><p class="account-log-retention" data-task-log-retention hidden></p>
      <div class="account-log-column-head"><span>时间 / 间隔</span><span>级别</span><span>执行阶段与结果</span></div>
      <div class="login-task-log-viewport" tabindex="0" role="region" aria-label="任务日志事件"></div>
      <div class="account-log-footer"><span data-task-log-update role="status"></span><button type="button" data-task-log-action="follow" aria-pressed="false">跟随最新</button></div>
      <details class="login-task-diagnostics"><summary>任务诊断信息</summary><dl class="login-diagnostic-facts" data-task-diagnostic-facts></dl><p class="field-note">报告包含全部已读取事件，不受筛选影响；不含密码、2FA 密钥或令牌。</p></details>
    </section>`);
    $('login-job-detail').querySelector('[data-task-log-search]').value = taskLogs.query;
    $('login-job-detail').querySelector('.login-task-log-viewport').addEventListener('scroll', event => {
      const viewport = event.currentTarget;
      if (taskLogs.follow && viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop > 30) {
        taskLogs.follow = false;
        $('login-job-detail').querySelector('[data-task-log-action="follow"]')?.setAttribute('aria-pressed', 'false');
      }
    });
  }
  function renderTaskLogs() {
    const panel = $('login-job-detail').querySelector('.login-task-logs');
    const job = state.job;
    if (!panel || !job) return;
    const saved = taskLogs.cache.get(idOf(job));
    const all = saved?.entries || [];
    const entries = filterLogEntries(all, job, taskLogs.filter, taskLogs.query);
    panel.querySelector('[data-task-log-count]').textContent = `${all.length} 条事件`;
    panel.querySelector('[data-task-log-errors]').textContent = all.filter(logEntryIsIssue).length;
    panel.querySelector('[data-task-log-matches]').textContent = taskLogs.query || taskLogs.filter === 'errors' ? `${entries.length} / ${all.length}` : '';
    for (const action of ['all', 'errors', 'follow']) panel.querySelector(`[data-task-log-action="${action}"]`).setAttribute('aria-pressed', String(action === 'follow' ? taskLogs.follow : taskLogs.filter === action));
    for (const action of ['copy', 'export']) panel.querySelector(`[data-task-log-action="${action}"]`).disabled = taskLogs.loading;
    $('login-export').disabled = taskLogs.loading;
    const error = panel.querySelector('[data-task-log-error]'); error.hidden = !taskLogs.error; error.textContent = taskLogs.error;
    const retention = panel.querySelector('[data-task-log-retention]'); retention.textContent = logRetention(saved); retention.hidden = !retention.textContent;
    const duration = $('login-job-detail').querySelector('[data-task-span]'); duration.textContent = logTiming(all) ? `记录跨度 ${logTiming(all)}` : '';
    panel.querySelector('[data-task-log-update]').textContent = taskLogs.loading ? '正在读取日志…' : taskLogs.updated ? `更新于 ${logTime(taskLogs.updated)} · 展开事件查看详细字段` : '展开事件查看详细字段';
    const facts = [['任务编号', idOf(job)], ['任务版本', job.revision], ['错误代码', job.error_code], ['运行时原因', job.runtime_code], ['HTTP 状态', job.http_status], ['停止阶段', job.error_stage], ['原因代码', job.error_reason], ['日志范围', all.length ? `#${all[0].id} – #${all.at(-1).id}` : '尚无事件']].filter(([,value]) => value !== undefined && value !== '');
    panel.querySelector('[data-task-diagnostic-facts]').innerHTML = facts.map(([label,value]) => `<div><dt>${label}</dt><dd><code>${escapeHTML(String(value))}</code></dd></div>`).join('');
    const signature = JSON.stringify([idOf(job), entries, taskLogs.query, taskLogs.filter, taskLogs.loading && !all.length, saved?.legacy]);
    if (signature === taskLogs.signature) return;
    taskLogs.signature = signature;
    const viewport = panel.querySelector('.login-task-log-viewport');
    const top = viewport.scrollTop;
    const opened = new Set([...viewport.querySelectorAll('details[open]')].map(item => item.dataset.logKey));
    const focused = document.activeElement?.closest('details')?.dataset.logKey;
    viewport.innerHTML = renderLogEntries(entries, all, job, opened) || `<p class="account-log-empty">${taskLogs.loading ? '正在读取日志…' : taskLogs.query ? '没有匹配的事件，请调整关键词。' : taskLogs.filter === 'errors' ? '本次任务暂无异常事件' : saved?.legacy ? '历史任务未记录完整事件，请结合任务结果核对。' : '此任务暂无事件记录'}</p>`;
    if (focused) [...viewport.querySelectorAll('details')].find(item => item.dataset.logKey === focused)?.querySelector('summary').focus({ preventScroll: true });
    viewport.scrollTop = taskLogs.follow ? viewport.scrollHeight : top;
  }
  async function readLogSnapshot(job, valid = () => true) {
    const entries = new Map();
    let cursor = 0, data;
    for (let page = 0; page < 20; page++) {
      data = await get(`/logs?id=${encodeURIComponent(idOf(job))}${cursor ? `&cursor=${cursor}` : ''}`);
      if (!valid()) return null;
      if (data.job_id !== idOf(job) || !Array.isArray(data.entries)) throw new Error('invalid_log_response');
      for (const raw of data.entries) {
        const entry = projectLogEntry(raw);
        if (!Number.isSafeInteger(entry.id) || entry.id < 1) throw new Error('invalid_log_event');
        entries.set(entry.id, entry);
      }
      if (!data.has_more) {
        const ordered = [...entries.values()].sort((a,b) => a.id - b.id).slice(-200);
        return { entries: ordered, legacy: !!data.legacy, truncated: !!data.truncated || (ordered[0]?.id || 0) > 1, first_id: Number(data.first_id || ordered[0]?.id || 0), last_id: Number(data.last_id || ordered.at(-1)?.id || 0), retained_count: Number(data.retained_count ?? ordered.length), revision: job.revision };
      }
      if (!Number.isSafeInteger(data.next_cursor) || data.next_cursor <= cursor) throw new Error('invalid_log_cursor');
      cursor = data.next_cursor;
    }
    throw new Error('log_read_incomplete');
  }
  function loadTaskLogs(force = false) {
    if (state.view !== 'tasks' || !state.job || !current(state.epoch)) return Promise.resolve();
    const job = state.job, id = idOf(job), epoch = state.epoch, read = taskLogs.read;
    if (taskLogs.cycle?.id === id && taskLogs.cycle.epoch === epoch && taskLogs.cycle.read === read) return taskLogs.cycle.promise;
    if (!force && !taskLogs.error && taskLogs.cache.get(id)?.revision === job.revision && !activeStates.has(job.status)) { renderTaskLogs(); return Promise.resolve(); }
    const cycle = { id, epoch, read };
    const valid = () => current(epoch) && state.selected === id && taskLogs.read === read;
    taskLogs.cycle = cycle; taskLogs.loading = true; taskLogs.error = ''; renderTaskLogs();
    cycle.promise = (async () => {
      try {
        const saved = await readLogSnapshot(job, valid);
        if (!valid() || !saved) return;
        taskLogs.cache.set(id, saved); taskLogs.updated = new Date().toISOString();
      } catch (error) {
        if (valid() && !isDiscarded(error)) taskLogs.error = '日志未完整读取，已保留此前内容。点击“刷新任务”重试；导出报告会标记读取不完整。';
      } finally {
        if (taskLogs.cycle === cycle) taskLogs.cycle = null;
        if (valid()) { taskLogs.loading = false; renderTaskLogs(); }
      }
    })();
    return cycle.promise;
  }
  async function exportTaskLog(action = 'export') {
    if (!state.job || taskLogs.loading) return;
    const id = state.selected, epoch = state.epoch;
    await loadTaskLogs(true);
    if (!current(epoch) || state.selected !== id || !state.job) return;
    const report = buildLogReport([state.job], taskLogs.cache, {
      ui_version: JSON.parse($('build-info').textContent).ui_version,
      runtime_version: state.capabilities?.runtime?.runtime_version || 'unknown',
      scope: 'current', read_incomplete: !!taskLogs.error || !taskLogs.cache.has(id),
    });
    try {
      if (action === 'copy') await navigator.clipboard.writeText(logReportText(report));
      else downloadLogReport(report);
      if (current(epoch) && state.selected === id) $('login-job-detail').querySelector('[data-task-log-update]').textContent = action === 'copy' ? '已复制诊断与全部已读取事件' : '已导出当前任务与全部已读取事件';
    } catch {
      if (current(epoch) && state.selected === id) { taskLogs.error = action === 'copy' ? '复制失败，请使用“导出 JSON”。' : '导出失败，请重试。'; renderTaskLogs(); }
    }
  }
  async function loadSelected() {
    if (state.view !== 'tasks' || !state.selected) return;
    const epoch = state.epoch;
    const sequence = ++state.detailRead;
    const selected = state.selected;
    try {
      const result = await get(`/detail?id=${encodeURIComponent(selected)}`);
      if (!current(epoch) || state.selected !== selected || sequence !== state.detailRead) return;
      const job = result.job || result;
      if (idOf(job) !== selected || typeof job.status !== 'string') throw new Error('invalid_job_response');
      state.job = job;
      if (job.authorization_url) state.urls.set(selected, safeAuthorizationURL(job.authorization_url));
      if (job.status === 'awaiting_commit') {
        const candidate = await get(`/candidate?id=${encodeURIComponent(job.candidate_id || selected)}`);
        if (!current(epoch) || state.selected !== selected || sequence !== state.detailRead) return;
        state.candidate = candidate.candidate || candidate;
      } else state.candidate = null;
      setReadError('任务详情', null);
      renderDetail();
      void loadTaskLogs();
    } catch (error) {
      if (!current(epoch) || state.selected !== selected || sequence !== state.detailRead) return;
      if (error.status === 404 && error.code === 'not_found' && state.jobsAvailable) {
        removeHistoryJob(selected);
        setReadError('任务详情', null);
        if (state.selected) await loadSelected();
        return;
      }
      setReadError('任务详情', error);
      throw error;
    }
  }
  async function refreshBootstrap() {
    const epoch = state.epoch;
    try {
      const [capabilities, targets, vault] = await Promise.all([snapshotRead('/capabilities'), snapshotRead('/targets'), snapshotRead('/vault/status')]);
      if (!current(epoch)) return;
      if (typeof capabilities?.available !== 'boolean' || !capabilities.methods || !Array.isArray(targets?.items) || typeof vault?.available !== 'boolean') throw new Error('invalid_capabilities');
      state.capabilities = capabilities; state.targets = targets.items; state.vault = vault;
      state.bootstrapReady = true; setReadError('登录服务', null);
    } catch (error) {
      if (!current(epoch) || isDiscarded(error)) return;
      state.bootstrapReady = false; setReadError('登录服务', error);
    }
  }
  async function refreshSnapshot(initialAccounts) {
    const epoch = state.epoch;
    const sequence = ++state.jobsRead;
    const detailReadAtStart = state.detailRead;
    const previous = new Map(state.jobs.map(job => [idOf(job), job.status]));
    state.readErrors.delete('网络连接'); state.readErrors.delete('登录状态');
    const reads = [loadAccounts(initialAccounts), (async () => {
      let selectedFirstJob = false;
      try {
        const result = await snapshotRead('/jobs?limit=200');
        if (!current(epoch) || sequence !== state.jobsRead) return;
        if (!Array.isArray(result.items) || result.items.some(job => !idOf(job) || typeof job.status !== 'string')) throw new Error('invalid_jobs');
        state.jobs = result.items; taskList.hasMore = !!result.has_more; state.jobsAvailable = true; setReadError('任务列表', null);
        if (!state.selected) {
          const next = filterTaskJobs(state.jobs, taskList.filter, taskList.query)[0];
          if (next) {
            selectJob(idOf(next));
            selectedFirstJob = true;
          }
        }
        renderJobs();
      } catch (error) {
        if (!current(epoch) || sequence !== state.jobsRead || isDiscarded(error)) return;
        state.jobsAvailable = false; setReadError('任务列表', error);
      }
      if (state.view === 'tasks') {
        // Opening history during this poll starts a detail read. Refresh again if the list is newer.
        const latest = state.jobs.find(job => idOf(job) === state.selected);
        if (selectedFirstJob || state.detailRead === detailReadAtStart || latest?.revision > state.job?.revision) await loadSelected();
      }
    })()];
    if (!state.bootstrapReady) reads.push(refreshBootstrap());
    await Promise.allSettled(reads);
    if (!current(epoch) || sequence !== state.jobsRead) return;
    void loadAccountLogs();
    if (!current(epoch) || sequence !== state.jobsRead) return;
    renderAccounts();
    const completed = state.jobs.filter(job => job.auto_commit && job.status === 'committed' && previous.has(idOf(job)) && previous.get(idOf(job)) !== 'committed');
    const securityCompleted = state.jobs.filter(job => isSecurityJob(job) && job.status === 'succeeded' && previous.has(idOf(job)) && previous.get(idOf(job)) !== 'succeeded');
    if (securityCompleted.length) feedback(`${securityCompleted.length === 1 ? securityCompleted[0].email || '账号' : `${securityCompleted.length} 个账号`}安全操作完成。${securityCompleted.length === 1 ? securityResult(securityCompleted[0]) : ''}`);
    if (completed.length) {
      const enabled = completed.every(job => job.credential_enabled);
      feedback(`${completed.length === 1 ? completed[0].email || '账号' : `${completed.length} 个账号`}${completed.every(isSecurityJob) ? '安全操作与重新授权完成' : '登录完成'}，${enabled ? '凭证已保存到 CPA 并启用' : '凭证已保存到 CPA'}。`);
      try { await onCommitted?.(); } catch { /* Keep the completed result if other workspaces are unavailable. */ }
      if (!current(epoch) || sequence !== state.jobsRead) return;
    }
    state.readFailures = state.readErrors.size ? Math.min(3, state.readFailures + 1) : 0;
    state.nextReadAt = state.readErrors.size ? 0 : performance.now() + pollDelay();
  }
  function reload(initialAccounts) {
    if (!state.open || !connected()) return Promise.resolve();
    if (refreshCycle?.epoch === state.epoch) { refreshCycle.pending = true; return refreshCycle.promise; }
    const cycle = { epoch: state.epoch, pending: false, promise: null };
    refreshCycle = cycle;
    clearTimeout(state.timer);
    cycle.promise = (async () => {
      try {
        do { cycle.pending = false; await refreshSnapshot(initialAccounts); initialAccounts = undefined; }
        while (cycle.pending && current(cycle.epoch));
      } catch (error) {
        if (current(cycle.epoch)) setReadError('登录状态', error);
      } finally {
        if (refreshCycle === cycle) refreshCycle = null;
        if (current(cycle.epoch)) { updateControls(); renderReadStatus(); schedule(); }
      }
    })();
    renderReadStatus();
    return cycle.promise;
  }
  function pollDelay() {
    return state.readFailures ? Math.min(30000, 3000 * 2 ** (state.readFailures - 1)) : state.jobs.some(job => activeStates.has(job.status)) ? 2000 : 6000;
  }
  function schedule(delay = pollDelay()) {
    clearTimeout(state.timer);
    if (!state.open || !connected() || !visible()) return;
    state.timer = setTimeout(() => { reload(); }, delay);
  }
  function resumeReads(initialAccounts) {
    if (!state.open || !connected() || !visible()) return;
    if (refreshCycle?.epoch === state.epoch) return refreshCycle.promise;
    const remaining = state.nextReadAt - performance.now();
    if (!initialAccounts && remaining > 0 && state.accountsAvailable && state.jobsAvailable && state.bootstrapReady && !state.readErrors.size) {
      updateControls();
      schedule(remaining);
      return;
    }
    state.bootstrapReady = false;
    state.jobsAvailable = false;
    state.accountsAvailable = false;
    updateControls();
    return reload(initialAccounts);
  }
  async function open({ focus = true, initialAccounts } = {}) {
    if (state.open || !connected()) return;
    state.open = true;
    const epoch = ++state.epoch;
    loading = !state.accountsLoaded;
    clearTimeout(state.timer);
    if (focus) $('login-title').focus({ preventScroll: true });
    feedback();
    try { await resumeReads(initialAccounts); }
    finally { if (current(epoch)) { loading = false; updateControls(); renderReadStatus(); } }
  }
  function close({ reset = false, focus = true } = {}) {
    if (!state.open && !reset) return;
    if (state.view !== 'accounts' || state.busy.size || conversion.busy || refreshCycle?.epoch === state.epoch) state.nextReadAt = 0;
    state.open = false;
    loading = false;
    state.epoch++;
    clearTimeout(state.timer);
    clearSecrets();
    state.urls.clear();
    state.accountStarting = '';
    resetAccountLogs();
    taskLogs.read++; taskLogs.cache.clear(); taskLogs.cycle = null; taskLogs.loading = false; taskLogs.signature = ''; taskLogs.error = '';
    state.candidate = null;
    state.job = null;
    state.detailSignature = '';
    state.jobSignature = ''; state.revealJob = false;
    closeAccountEditors();
    closeAccountSurface(false, true);
    $('login-job-detail').innerHTML = '<p class="empty-line">选择任务，查看进度和结果。</p>';
    if (reset) {
      snapshotReads.clear();
      snapshotReads = new Map();
      state.capabilities = null; state.targets = []; state.jobs = []; state.selected = ''; state.vault = null;
      state.accounts = []; state.accountsLoaded = false; state.readErrors.clear(); state.nextReadAt = 0; state.readFailures = 0; state.bootstrapReady = false; state.jobsAvailable = false; state.accountSettings = null; state.accountsAvailable = false; state.accountSignature = ''; state.settingsDirty = false; state.settingsDraftRevision = null; taskList.query = ''; taskList.filter = 'all'; $('login-task-search').value = ''; state.accountQuery = ''; state.accountFilter = 'all'; state.accountPage = 0; $('account-search').value = '';
      $('account-list').replaceChildren(); $('account-settings-form').reset(); $('account-settings-note').textContent = ''; renderAccountSettings(); renderAccounts();
      renderJobs(); feedback(); renderReadStatus();
    }
    if (focus) document.querySelector('[data-page="login"]').focus({ preventScroll: true });
  }
  async function action(name, button) {
    if (taskActionBlocked() || !state.job) return;
    const epoch = state.epoch;
    const job = state.job;
    let body = { job_id: idOf(job), revision: job.revision };
    if (name === 'callback') {
      const callback = $('login-callback')?.value.trim();
      if (!callback) { feedback('请粘贴完整回调地址。', true); return; }
      body = { job_id: idOf(job), callback_url: callback };
      $('login-callback').value = '';
    }
    if (name === 'commit') {
      if (!state.candidate) return;
      body = { job_id: idOf(job), candidate_id: state.candidate.id, revision: state.candidate.revision };
    }
    if (!['callback','commit','cancel','discard'].includes(name)) return;
    state.busy.add('action'); button.disabled = true; feedback();
    try {
      const response = write(`/${name}`, body);
      if (body.callback_url) body.callback_url = '';
      await response;
      if (!current(epoch)) return;
      state.detailSignature = '';
      await reload();
      if (name === 'commit') await onCommitted?.();
    } catch (error) { if (current(epoch)) { fail(error); try { await reload(); } catch {} } }
    finally { if (body.callback_url) body.callback_url = ''; state.busy.delete('action'); if (current(epoch)) renderDetail(true); }
  }
  $('account-actions-menu').addEventListener('toggle', event => {
    if (event.newState === 'closed' && !$('account-actions-menu').matches(':popover-open')) closeAccountMenu();
  });
  $('account-actions-menu').addEventListener('keydown', event => {
    const buttons = [...$('account-actions-menu').querySelectorAll('button:not(:disabled)')];
    if (event.key === 'Escape') { event.preventDefault(); closeAccountMenu(true); }
    if (['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key) && buttons.length) {
      event.preventDefault();
      const index = buttons.indexOf(document.activeElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : (index + (event.key === 'ArrowDown' ? 1 : -1) + buttons.length) % buttons.length;
      buttons[next].focus();
    }
  });
  addEventListener('resize', scheduleMenuPosition);
  document.addEventListener('scroll', event => {
    if (!$('account-actions-menu').contains(event.target)) scheduleMenuPosition();
  }, { capture: true, passive: true });
  $('account-logout-all').addEventListener('click', () => {
    const account = state.accounts.find(item => item.id === menuAccountID);
    if (account && !$('account-logout-all').disabled) showAccountSecurity(account, 'logout_all');
  });
  for (const id of ['account-security-close', 'account-security-cancel']) $(id).addEventListener('click', () => {
    if (!state.busy.has('account')) closeAccountSecurity(true);
  });
  $('account-security-dialog').addEventListener('cancel', event => {
    event.preventDefault();
    if (!state.busy.has('account')) closeAccountSecurity(true);
  });
  $('account-security-dialog').addEventListener('close', () => {
    if (!$('account-security-dialog').open) closeAccountSecurity(true);
  });
  $('account-security-form').addEventListener('submit', event => {
    event.preventDefault();
    if (accountBusy() || !securityAccount || !$('account-security-dialog').open || $('account-security-submit').disabled) return;
    const account = securityAccount;
    const body = { id: account.id, revision: account.revision, action: securityAction, logout_all_devices: securityAction === 'reset_totp' && $('account-security-logout').checked, reauthorize: $('account-security-reauthorize').checked, upload_to_cpa: $('account-security-upload').checked };
    securityFeedback();
    accountAction(async epoch => {
      let result;
      try { result = await write('/accounts/security', body); }
      catch (error) {
        if (current(epoch) && (error instanceof TypeError || error.outcomeUnknown || ['management_network_error', 'network_error', 'idempotency_unknown', 'security_pending'].includes(error.code))) {
          securitySubmissionUncertain = true;
          securityFeedback('提交结果尚未确认。请关闭窗口并查看账号任务，勿再次提交。');
        }
        throw error;
      }
      if (!current(epoch)) return;
      const job = result.job || result;
      state.jobs = [job, ...state.jobs.filter(item => idOf(item) !== idOf(job))];
      const saved = state.accounts.find(item => item.id === account.id);
      if (saved) applyAccountJob(saved, job);
      closeAccountSecurity(true);
      renderAccounts();
      feedback(`已开始${operationLabel(job)}，进度与结果会在账号行更新。`);
      await reload();
    });
  });
  $('account-security-complete').addEventListener('click', () => {
    const account = state.accountEditing;
    if (!account || accountBusy() || !securityRecoveryCompletable || !securityRecoveryJob) return;
    const body = { id: account.id, revision: account.revision, job_id: securityRecoveryJob };
    accountAction(async epoch => {
      await write('/accounts/security/complete', body);
      if (!current(epoch)) return;
      closeAccountEditor(true); await reload();
      if (current(epoch)) feedback('新 2FA 已完成本地保存。本次未执行退出设备、重新授权或上传 CPA。');
    });
  });
  $('account-history-open').addEventListener('click', () => setView('tasks'));
  $('account-settings-open').addEventListener('click', () => setView('settings'));
  document.querySelectorAll('[data-account-surface-close]').forEach(button => button.addEventListener('click', () => closeAccountSurface(true)));
  for (const [view, id] of [['tasks','login-history-dialog'], ['settings','login-settings-dialog']]) {
    $(id).addEventListener('keydown', event => {
      if (event.key !== 'Escape' || event.isComposing) return;
      event.preventDefault(); event.stopPropagation(); closeAccountSurface(true);
    }, true);
    $(id).addEventListener('cancel', event => { event.preventDefault(); closeAccountSurface(true); });
    $(id).addEventListener('close', () => { if (!$(id).open && state.view === view) closeAccountSurface(false, true); });
  }
  function syncVisibility() {
    if (visible()) return resumeReads();
    clearTimeout(state.timer);
    if (state.open && connected() && navigator.onLine === false) setReadError('网络连接', new Error('offline'));
  }
  for (const id of ['account-security-logout', 'account-security-reauthorize', 'account-security-upload']) $(id).addEventListener('change', () => {
    if (!securityAction) return;
    renderSecurityPlan();
    saveWorkflowPreference(securityAction, {
      logout: securityAction === 'reset_totp' && $('account-security-logout').checked,
      reauthorize: $('account-security-reauthorize').checked,
      upload: $('account-security-upload').checked,
    });
  });
  $('account-copy-material').addEventListener('click', async () => {
    if ($('account-copy-material').disabled) return;
    const id = state.accountEditing?.id, epoch = state.epoch;
    try {
      await navigator.clipboard.writeText(`${$('account-email').value}----${$('account-password').value}----${$('account-totp').value}`);
      if (current(epoch) && state.accountEditing?.id === id) $('account-copy-feedback').textContent = '已复制邮箱、密码与 2FA，请妥善保管。';
    } catch { if (current(epoch) && state.accountEditing?.id === id) $('account-copy-feedback').textContent = '无法访问剪贴板，请手动复制。'; }
  });
  for (const id of ['account-import-open', 'account-import-empty']) $(id).addEventListener('click', showAccountImport);
  for (const id of ['account-import-close', 'account-import-cancel']) $(id).addEventListener('click', () => {
    if (!state.busy.has('account')) closeAccountImport(true);
  });
  $('account-import-dialog').addEventListener('cancel', event => {
    event.preventDefault();
    if (!state.busy.has('account')) closeAccountImport(true);
  });
  $('account-import-dialog').addEventListener('close', () => {
    if (!$('account-import-dialog').open) closeAccountImport(true);
  });
  $('account-import-text').addEventListener('input', () => { importFeedback(); updateImportCount(); });
  for (const id of ['account-editor-close', 'account-editor-cancel']) $(id).addEventListener('click', () => {
    if (!state.busy.has('account')) closeAccountEditor(true);
  });
  $('account-editor-dialog').addEventListener('cancel', event => {
    event.preventDefault();
    if (!state.busy.has('account')) closeAccountEditor(true);
  });
  $('account-editor-dialog').addEventListener('close', () => {
    if (!$('account-editor-dialog').open) closeAccountEditor(true);
  });
  $('account-editor-retry').addEventListener('click', () => {
    const account = state.accounts.find(item => item.id === state.accountEditing?.id) || state.accountEditing;
    if (account) showAccountEditor(account);
  });
  $('account-search').addEventListener('input', event => { state.accountQuery = event.target.value; state.accountPage = 0; renderAccounts(); });
  $('account-status-filter').addEventListener('change', () => { state.accountFilter = $('account-status-filter').value; state.accountPage = 0; renderAccounts(); });
  $('account-clear-filter').addEventListener('click', () => { taskList.query = ''; taskList.filter = 'all'; $('login-task-search').value = ''; state.accountQuery = ''; state.accountFilter = 'all'; state.accountPage = 0; $('account-search').value = ''; renderAccounts(); $('account-search').focus(); });
  $('account-page-prev').addEventListener('click', () => { state.accountPage = Math.max(0, state.accountPage - 1); renderAccounts(); });
  $('account-page-next').addEventListener('click', () => { state.accountPage++; renderAccounts(); });
  $('account-import-form').addEventListener('submit', event => {
    event.preventDefault();
    if (accountBusy() || !$('account-import-dialog').open || $('account-import-submit').disabled) return;
    let entries;
    try { entries = parseAccountImport($('account-import-text').value, state.accounts.map(account => account.email)); }
    catch (error) { importFeedback(error.message); $('account-import-error').focus(); return; }
    importFeedback();
    const opener = importOpener;
    accountAction(async epoch => {
      const proxy = $('account-import-proxy').value.trim();
      const response = write('/accounts/import', { entries, ...(proxy ? { proxy_url: proxy } : {}) });
      $('account-import-text').value = '';
      $('account-import-proxy').value = '';
      updateImportCount();
      entries.forEach(entry => { entry.email = ''; entry.password = ''; entry.totp_secret = ''; });
      await response;
      if (!current(epoch)) return;
      closeAccountEditors();
      await loadAccounts();
      if (current(epoch)) feedback('账号已导入，可在列表中点击“2FA 登录”。');
    }).finally(() => {
      entries.forEach(entry => { entry.email = ''; entry.password = ''; entry.totp_secret = ''; });
      if (state.open && connected() && state.view === 'accounts' && !$('account-import-dialog').open && opener?.isConnected && !opener.disabled) opener.focus({ preventScroll: true });
    });
  });
  $('account-inspect').addEventListener('click', () => accountAction(async epoch => {
    await write('/accounts/inspect', {});
    if (!current(epoch)) return;
    await loadAccounts();
    if (current(epoch)) feedback('巡检完成，已更新 CPA 账号状态。');
  }));
  $('account-settings-form').addEventListener('input', event => {
    if (!state.settingsDirty) state.settingsDraftRevision = state.accountSettings?.revision ?? null;
    state.settingsDirty = true;
    if (event.target.id === 'account-inspection-enabled' && !event.target.checked) $('account-auto-enabled').checked = false;
    updateAccountControls();
    $('account-settings-note').textContent = '有未保存更改，关闭后保留草稿';
  });
  $('account-settings-form').addEventListener('submit', event => {
    event.preventDefault();
    if (!$('account-settings-form').reportValidity() || !state.accountSettings) return;
    const body = { revision: state.settingsDraftRevision ?? state.accountSettings.revision, inspection_enabled: $('account-inspection-enabled').checked, auto_relogin_enabled: $('account-auto-enabled').checked, interval_minutes: Number($('account-interval').value), login_proxy_url: $('account-default-proxy').value.trim(), cpa_upload_excluded_plans: [...document.querySelectorAll('[name="cpa-upload-plan"]:checked')].map(input => input.value) };
    accountAction(async epoch => {
      try { await write('/accounts/settings', body, 'PUT'); }
      catch (error) {
        if (!current(epoch)) return;
        if (!['conflict', 'revision_conflict'].includes(error.code)) throw error;
        state.settingsDirty = false; state.settingsDraftRevision = null;
        await loadAccounts();
        if (current(epoch)) $('account-settings-note').textContent = state.accountsAvailable ? '设置已由其他操作更新，已载入最新值，请核对后再修改。' : '设置已由其他操作更新，等待重新读取后再修改。';
        return;
      }
      if (!current(epoch)) return;
      state.settingsDirty = false; state.settingsDraftRevision = null;
      await loadAccounts();
      if (current(epoch)) $('account-settings-note').textContent = '设置已保存，上传前将按最新排除套餐检查。';
    });
  });
  for (const id of ['account-password', 'account-totp']) $(id).addEventListener('input', () => {
    syncEditorCredentials();
    $('account-copy-feedback').textContent = '';
  });
  $('account-editor').addEventListener('submit', event => {
    event.preventDefault();
    syncEditorCredentials();
    if (accountBusy() || state.accountEditing?.security_pending || state.accountDetailLoading || state.accountDetailFailed || !editorCredentials || !$('account-editor-dialog').open || !$('account-editor').reportValidity() || !state.accountEditing) return;
    const account = state.accountEditing;
    const editorEpoch = state.epoch;
    const body = { id: account.id, revision: account.revision, label: $('account-label').value.trim(), auto_relogin: $('account-auto-relogin').checked, proxy_url: $('account-proxy-url').value.trim() };
    if (editorCredentialsChanged()) body.credentials = { email: account.email, password: $('account-password').value, totp_secret: $('account-totp').value.trim() };
    accountAction(async epoch => {
      const response = write('/accounts/entry', body, 'PUT');
      clearSecrets();
      if (body.credentials) Object.keys(body.credentials).forEach(name => { body.credentials[name] = ''; });
      try { await response; } catch (error) {
        if (current(epoch) && state.accountEditing?.id === account.id) {
          state.accountDetailFailed = true;
          $('account-editor-retry').hidden = false;
          $('account-email').value = account.email || '';
          $('account-editor-note').textContent = '保存尚未确认。请先重新读取资料，核对最新状态后再修改；此次保存不会自动重试。';
        }
        throw error;
      }
      if (!current(epoch)) return;
      clearSecrets(); closeAccountEditors(); await loadAccounts();
      if (current(epoch)) feedback('账号设置已保存。');
    }).finally(() => {
      if (body.credentials) Object.keys(body.credentials).forEach(name => { body.credentials[name] = ''; });
      if (current(editorEpoch) && state.view === 'accounts' && !$('account-editor-dialog').open) focusAccount(account.id);
    });
  });
  $('account-delete-open').addEventListener('click', () => showAccountDelete(state.accountEditing, true));
  $('account-delete-no').addEventListener('click', () => { if (!deleteTarget?.submitting) closeAccountDelete(true); });
  $('account-delete-confirm').addEventListener('cancel', event => {
    event.preventDefault();
    if (!deleteTarget?.submitting) closeAccountDelete(true);
  });
  $('account-delete-yes').addEventListener('click', async () => {
    const target = deleteTarget;
    updateDeleteControls();
    if (!target || $('account-delete-yes').disabled) return;
    const epoch = state.epoch;
    let deleted = false;
    await accountAction(async () => {
      target.submitting = true; updateDeleteControls();
      try {
        const result = await write('/accounts/entry', { id: target.id, revision: target.revision }, 'DELETE');
        if (result?.deleted !== true) throw new Error('missing_delete_receipt');
      } catch (error) {
        if (!current(epoch) || deleteTarget !== target) return;
        target.error = error.status === 409 || error.status === 404 ? '账号已变化或已移除，请关闭后核对列表。'
          : error.status >= 400 && error.status < 500 ? '删除请求未被接受，请关闭后核对账号状态。'
          : '未能确认删除结果，请关闭后核对账号列表。';
        target.submitting = false; updateDeleteControls();
        await loadAccounts();
        return;
      }
      if (!current(epoch) || deleteTarget !== target) return;
      deleted = true;
      closeAccountDelete();
      if (target.fromEditor) closeAccountEditor();
      if (accountLogs.account === target.id) resetAccountLogs();
      state.accounts = state.accounts.filter(item => item.id !== target.id);
      renderAccounts();
      await loadAccounts();
      if (current(epoch)) feedback('账号资料已删除。CPA 凭证和历史任务已保留。');
    });
    if (deleted && current(epoch) && state.view === 'accounts' && !document.querySelector('dialog[open]') && document.activeElement === document.body) $('account-search').focus({ preventScroll: true });
  });
  async function dispatchAccountAction(account, action, button) {
    if (action === 'more') { showAccountMenu(account, button); return; }
    if (action === 'logs') { toggleAccountLogs(account); return; }
    if (action === 'reset_totp') { showAccountSecurity(account, 'reset_totp'); return; }
    if (action === 'delete') { showAccountDelete(account); return; }
    if (action === 'edit') { await showAccountEditor(account); return; }
    if (action === 'inspect') {
      await accountAction(async epoch => {
        await write('/accounts/inspect', { id: account.id });
        if (!current(epoch)) return;
        await loadAccounts();
        if (current(epoch)) feedback('已更新此账号的 CPA 状态。');
      });
      return;
    }
    if (action === 'task' || action === 'upload') {
      taskList.query = ''; taskList.filter = 'all'; $('login-task-search').value = '';
      selectJob(action === 'upload' && account.pending_upload_job_id ? account.pending_upload_job_id : account.last_job_id);
      const opener = [...$('account-list').querySelectorAll('[data-account-action="more"]')].find(node => node.dataset.accountId === account.id);
      setView('tasks', action === 'upload' ? button : opener); renderJobs();
      return;
    }
    if (action === 'start') openAccountLogin(account);
  }
  $('account-list').addEventListener('click', event => {
    const button = event.target.closest('[data-account-action]');
    if (!button || button.disabled) return;
    const account = state.accounts.find(item => item.id === button.dataset.accountId);
    if (account) dispatchAccountAction(account, button.dataset.accountAction, button);
  });
  $('account-actions-menu').addEventListener('click', event => {
    const button = event.target.closest('[data-account-menu-action]');
    if (!button || button.disabled) return;
    const account = state.accounts.find(item => item.id === menuAccountID);
    if (!account) return;
    const action = button.dataset.accountMenuAction;
    closeAccountMenu(action === 'logs' || action === 'inspect');
    dispatchAccountAction(account, action, button);
  });
  $('account-login-start').addEventListener('click', () => {
    const account = state.accounts.find(item => item.id === state.accountEditing?.id) || state.accountEditing;
    if (account && $('account-editor-dialog').open && !state.accountDetailLoading && !state.accountDetailFailed) openAccountLogin(account, $('account-login-target').value);
  });
  for (const id of ['account-login-close', 'account-login-cancel']) $(id).addEventListener('click', () => closeAccountLogin(true));
  $('account-login-dialog').addEventListener('cancel', event => { event.preventDefault(); closeAccountLogin(true); });
  $('account-login-upload').addEventListener('change', event => {
    if (accountLoginTarget) saveWorkflowPreference('login', { upload: event.target.checked });
  });
  $('account-login-form').addEventListener('submit', event => {
    event.preventDefault();
    const target = accountLoginTarget;
    const account = state.accounts.find(item => item.id === target?.id);
    if (!account || accountBusy()) return;
    const upload = $('account-login-upload').checked;
    closeAccountLogin();
    if (account.revision !== target.revision || manualLoginBlocked(account)) {
      feedback('账号状态已变化，请重新打开登录窗口确认。');
      return;
    }
    void startAccount(account, target.targetID, upload);
  });
  $('login-jobs').addEventListener('click', async event => {
    const button = event.target.closest('[data-login-job]');
    if (!button) return;
    const epoch = state.epoch;
    clearSecrets(); selectJob(button.dataset.loginJob);
    renderJobs(); try { await loadSelected(); } catch (error) { if (current(epoch)) fail(error); }
  });
  $('login-job-detail').addEventListener('click', event => { const button = event.target.closest('[data-login-action]'); if (button && !button.disabled) action(button.dataset.loginAction, button); });
  $('login-refresh').addEventListener('click', async () => { const epoch = state.epoch; try { feedback(); state.bootstrapReady = false; await reload(); await loadTaskLogs(true); } catch (error) { if (current(epoch)) fail(error); } });
  $('login-export').addEventListener('click', () => void exportTaskLog());
  $('login-history-delete-open').addEventListener('click', openHistoryDelete);
  $('login-history-delete-yes').addEventListener('click', () => void deleteHistoryJob());
  $('login-history-delete-no').addEventListener('click', () => closeHistoryDelete(true));
  $('login-history-delete').addEventListener('cancel', event => {
    event.preventDefault();
    if (!state.busy.has('history-delete')) closeHistoryDelete(true);
  });
  $('login-task-search').addEventListener('input', event => { taskList.query = event.target.value; renderJobs(); });
  $('login-task-filter').addEventListener('change', () => { taskList.filter = $('login-task-filter').value; renderJobs(); });
  $('login-job-detail').addEventListener('input', event => {
    if (event.target.matches('[data-task-log-search]')) { taskLogs.query = event.target.value; renderTaskLogs(); }
  });
  $('login-job-detail').addEventListener('click', event => {
    const button = event.target.closest('[data-task-log-action]');
    if (!button || button.disabled) return;
    const action = button.dataset.taskLogAction;
    if (['all', 'errors'].includes(action)) { taskLogs.filter = action; renderTaskLogs(); }
    else if (action === 'follow') {
      taskLogs.follow = !taskLogs.follow; renderTaskLogs();
      const viewport = $('login-job-detail').querySelector('.login-task-log-viewport');
      if (taskLogs.follow) viewport.scrollTop = viewport.scrollHeight;
    } else if (['copy', 'export'].includes(action)) void exportTaskLog(action);
  });
  return { open, close, syncVisibility, refresh: () => { state.bootstrapReady = false; return reload(); }, reset: () => close({ reset: true, focus: false }) };
}

function phaseLabel(phase) {
  return ({ security_login: '登录账号安全会话', security_check: '核对账号与 2FA 状态', totp_disable: '移除旧 2FA', totp_enroll: '登记新 2FA', totp_activate: '激活新 2FA', totp_commit: '提交并核对新密钥', totp_saved: '新 2FA 已保存', logout_all: '退出所有设备', reauthorization: '重新授权 CPA', security_complete: '安全操作完成', queued: '等待执行', starting: '准备登录', runtime_start: '准备登录', browser_start: '准备登录', session: '连接登录服务', authorization: '正在进行登录授权', email: '填写账号邮箱', password: '验证账号密码', totp: '验证 2FA', mfa: '验证 2FA', oauth: '获取 Codex 授权', callback: '接收授权回调', browser_verification: '处理网站验证', consent: '确认授权', workspace: '核对账号工作区', authorizing: '获取官方授权', token_exchange: '兑换并核验授权', exchanging: '兑换授权', validating: '核验账号身份', review: '等待确认保存', saving: '保存凭证' })[phase] || '正在处理登录任务';
}
function diagnosticStage(stage) {
  if (stage === 'saving') return '上传 CPA';
  if (['security_login', 'security_check', 'totp_disable', 'totp_enroll', 'totp_activate', 'totp_commit', 'totp_saved', 'logout_all', 'reauthorization', 'security_complete'].includes(stage)) return phaseLabel(stage);
  return ({ runtime_start: '准备登录', browser_start: '准备登录', session: '连接登录服务', authorization: '打开授权页', email: '填写邮箱', password: '验证密码', totp: '验证 2FA', oauth: '获取 Codex 授权', browser_verification: '处理网站验证', consent: '确认授权', workspace: '选择工作区', callback: '接收回调' })[stage] || '登录流程';
}
function jobDiagnostic(job) {
  if (job.runtime_code === 'service_error' && !job.error_reason) {
    const step = failedReauthorization(job) ? '重新授权' : diagnosticStage(job.error_stage);
    return `${step}时，认证服务${job.http_status >= 500 && job.http_status <= 599 ? '返回 ' + httpStatusLabel(job.http_status) : '返回了服务端错误'}。`;
  }
  const reason = callbackReasons[job.error_reason] || networkReasons[job.error_reason] || ({ human_verification: '需要完成人机验证', browser_verification_timeout: '网站自动检查超时', browser_verification_required: '网站验证尚未完成', email_verification: '需要邮箱验证码', phone_verification: '需要手机验证', unsupported_request: '当前插件暂不支持此登录步骤', unsupported_navigation: '遇到暂不支持的页面跳转', submit_unavailable: '未找到继续按钮', totp_method_unconfirmed: '无法确认当前验证码方式', workspace_selection: '需要选择工作区', account_selection: '需要选择账号', unexpected_dialog: '出现额外对话框', unexpected_download: '出现非预期下载', unsupported_page: '未识别当前登录页面', upstream_rejected: '官方页面拒绝验证', proxy_connection_failed: '代理连接失败', session_unconfirmed: '未确认网页登录会话', authentication_not_advanced: '验证响应未确认流程前进', totp_factor_missing: '未找到可用的 2FA 验证方式', sentinel_unavailable: '官方登录安全组件未就绪' })[job.error_reason];
  if (reason) return `${job.error_stage ? `${diagnosticStage(job.error_stage)}：` : ''}${reason}`;
  if (job.status === 'interaction_required' || job.error_code === 'interaction_required') return '旧记录未保存停止步骤，请查看任务。';
  if (job.error_code) return messageFor(job.error_code);
  return '';
}
function diagnosticAdvice(job) {
  if (['commit_unknown', 'saved_not_adopted'].includes(job.status)) return '凭证保存结果尚未确认。请核对 CPA 凭证与任务状态，勿重复登录或导入。';
  if (job.status === 'security_uncertain') return '请查看账号资料保留的安全操作信息与新密钥。未确认的修改不会自动重试，旧 2FA 可能已失效。';
  if (failedReauthorization(job)) {
    const effects = [job.totp_reset && '新 2FA 已保存', job.devices_logged_out && '退出设备请求已获接受'].filter(Boolean);
    return `${effects.join('，')}。请稍后单独发起登录授权${job.auto_commit ? '，授权成功后再上传 CPA' : ''}。`;
  }
  if (isSecurityJob(job) && job.totp_reset) return '新 2FA 已保存，旧密钥不会恢复。请仅处理失败的后续步骤，不要为了重新授权再次重置 2FA。';
  if (isSecurityJob(job) && job.devices_logged_out) return '服务器已接受退出所有设备请求；会话失效可能有延迟。请仅处理失败的后续授权。';
  if (job.error_code === 'cpa_plan_excluded') return '在“全局设置”中调整排除套餐后，可在会话有效期内确认上传；关闭排除不会自动补传，已有凭证不会被删除。';
  if (job.error_reason === 'proxy_loopback_unreachable') return 'Docker 中的 127.0.0.1 / localhost 指容器本身。若使用 Mac 宿主机代理，可将地址设为 host.docker.internal，并核对代理允许容器连接。修改后再手动登录。';
  if (job.error_reason === 'proxy_auth_rejected') return '核对代理用户名和密码。407 是代理认证响应，不能据此判定 OpenAI 密码或 2FA 错误。';
  if (job.error_reason === 'proxy_dns_failed') return '代理服务器地址解析失败，请核对代理主机名及容器 DNS。';
  if (job.error_reason === 'network_dns_failed') return '认证服务域名解析失败。若需要由代理解析域名，请使用 SOCKS5H；SOCKS5 使用本地 DNS。';
  if (['proxy_tls_failed', 'network_tls_failed'].includes(job.error_reason)) return '核对代理协议、证书信任和网络连接。TLS 连接未建立，未据此判断账号资料错误。';
  if (job.error_reason === 'proxy_connection_refused') return '代理端口拒绝连接，请核对代理是否运行、监听地址和端口；容器访问宿主机时需使用可达地址。';
  if (job.error_reason === 'network_connection_refused') return '认证服务连接被拒绝，请核对网络出口与代理规则后再手动登录。';
  if (['proxy_connection_timeout', 'network_timeout'].includes(job.error_reason)) return '认证网络等待超时，请检查代理连通性与网络出口。核对日志中的停止步骤后，再决定是否重新登录。';
  if (job.error_reason === 'browser_verification_timeout') return '官方页面的自动检查未在等待时间内完成，尚未确认需要人工验证。请保留任务日志并检查代理连接，本次任务不会自动重试。';
  if (job.error_reason === 'browser_verification_required') return '官方接口返回了网站验证页面，当前认证步骤未完成。此响应未确认密码或 2FA 错误，也未确认需要人工验证。';
  if (job.error_reason === 'session_unconfirmed') return '尚未确认网页会话登录成功，未继续获取 Codex 授权。请核对官方账号要求和代理连接后再手动重试。';
  if (job.error_reason === 'authentication_not_advanced') return '已收到验证响应，但没有确认下一步认证状态，未判定验证成功。请保留任务的停止步骤和错误原因以便排查。';
  if (job.error_reason === 'totp_factor_missing') return '未找到此账号可用的 2FA 验证方式。请核对账号设置；本次未提交验证码。';
  if (job.error_reason === 'sentinel_unavailable') return '官方登录安全组件未能正常加载或返回所需信息。请检查代理能否访问官方认证资源，再手动重试。';
  if (job.error_reason === 'unsupported_request') return '当前插件暂不支持此登录步骤，请保留排障信息并更新插件。这不代表密码或 2FA 错误。';
  if (job.error_reason === 'proxy_connection_failed') return '检查代理地址、端口和认证资料后，再手动登录。可在“全局设置”中设置默认代理。';
  if (['human_verification', 'email_verification', 'phone_verification', 'workspace_selection', 'account_selection'].includes(job.error_reason)) return '此步骤需要人工处理，当前自动登录无法继续。请核对官方账号要求后再决定是否重新登录，本次任务不会自动重试。';
  if (['unsupported_navigation', 'submit_unavailable', 'totp_method_unconfirmed', 'unsupported_page'].includes(job.error_reason)) return '当前登录页面与自动流程不匹配。请保留排障信息，更新插件后再试。';
  if (job.status === 'interaction_required' && !job.error_reason) return '此记录缺少具体原因，无法判断是否需要额外验证。请先核对账号资料和代理。';
  if (['invalid_proxy', 'proxy_auth_unsupported'].includes(job.error_code)) return '在账号资料或“全局设置”中修改代理后再手动登录。';
  return '本次任务已停止。核对账号资料、网络及 CPA 状态后，再决定是否手动重试。';
}
function failedReauthorization(job) {
  return isSecurityJob(job) && job.reauthorize && job.status === 'failed' && job.security_status === 'complete' &&
    (job.totp_reset || job.devices_logged_out) && job.runtime_code === 'service_error';
}
function httpStatusLabel(status) {
  const description = ({ 502: '网关错误', 503: '服务暂不可用', 504: '网关超时' })[status];
  return description ? `HTTP ${status}（${description}）` : `HTTP ${status}`;
}
function securityProgress(job) {
  const rows = [];
  const unfinished = activeStates.has(job.status) ? '等待结果' : job.status === 'security_uncertain' ? '待核对' : '未确认完成';
  if (job.action === 'reset_totp') rows.push(['重置并保存新 2FA', job.totp_reset ? '已完成' : unfinished, !!job.totp_reset]);
  if (job.action === 'logout_all' || job.logout_all_devices) rows.push(['退出所有设备', job.devices_logged_out ? '已完成' : unfinished, !!job.devices_logged_out]);
  const authorized = ['awaiting_commit', 'committing', 'committed', 'saved_not_adopted', 'commit_unknown'].includes(job.status);
  const authFailed = failedReauthorization(job);
  if (job.reauthorize) rows.push(['重登授权', authorized ? '已完成' : authFailed ? '失败' : unfinished, authorized]);
  if (job.reauthorize && (job.auto_commit || authorized)) rows.push(['上传 CPA', job.credential_enabled ? '已完成' : job.status === 'awaiting_commit' ? '待上传' : authFailed ? '未开始' : unfinished, !!job.credential_enabled]);
  const current = rows.findIndex(([, ,done]) => !done);
  return `<ol class="login-security-progress" aria-label="安全操作进度">${rows.map(([label, status, done], index) => `<li data-complete="${done}" data-state="${done ? 'complete' : index !== current ? 'pending' : activeStates.has(job.status) ? 'active' : 'failed'}"><span class="login-security-step-label">${escapeHTML(label)}</span><strong class="${done ? 'is-complete' : ''}">${!authFailed && !done && index > current ? '未确认完成' : status}</strong></li>`).join('')}</ol>`;
}

function reasonLabel(reason) {
  return ({ cpa_plan_excluded: '套餐命中上传排除设置，暂不上传 CPA', not_inference_verified: '此授权尚未经过模型测试', quality_paused: '质量暂停继续生效', operator_disabled: '人工禁用继续生效', manual_disabled: '人工禁用继续生效', native_cooldown: 'CPA 冷却继续生效', rules_stopped: '相关检测规则保持停止', credential_write_isolation: '保存期间相关检测等待，确认同一条目后按当前规则许可继续', new_credential_disabled: '新凭证默认人工禁用' })[reason] || '其他凭证限制继续生效';
}

export function validImportTOTP(value) {
  let seed = value;
  if (/^otpauth:/i.test(value)) {
    const authority = value.match(/^otpauth:\/\/([^/?#]*)/i)?.[1];
    const rawPath = value.split('?')[0];
    if (/[\u0000-\u0020\u007f]/.test(value) || !authority || authority.includes('@') || /%(?![0-9a-f]{2})/i.test(rawPath)) return false;
    let uri;
    try { uri = new URL(value); } catch { return false; }
    if (uri.protocol !== 'otpauth:' || uri.host !== 'totp' || uri.hash || uri.username || uri.password) return false;
    const values = {};
    for (const part of uri.search.slice(1).split('&')) {
      const split = part.indexOf('=');
      let name;
      try { name = decodeURIComponent((split < 0 ? part : part.slice(0, split)).replace(/\+/g, ' ')); } catch { continue; }
      if (!['secret', 'algorithm', 'digits', 'period'].includes(name)) continue;
      if (Object.hasOwn(values, name)) return false;
      try { values[name] = decodeURIComponent((split < 0 ? '' : part.slice(split + 1)).replace(/\+/g, ' ')); } catch { return false; }
    }
    if (values.algorithm && values.algorithm.toUpperCase() !== 'SHA1' || values.digits && values.digits !== '6' || values.period && values.period !== '30') return false;
    seed = values.secret || '';
  }
  seed = seed.toUpperCase().replace(/[\s-]/g, '').replace(/=+$/, '');
  if (seed.length < 16 || seed.length > 128 || ![0, 2, 4, 5, 7].includes(seed.length % 8) || !/^[A-Z2-7]+$/.test(seed)) return false;
  return ('ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'.indexOf(seed.at(-1)) & ((1 << (seed.length * 5 % 8)) - 1)) === 0;
}
export function parseAccountImport(text, existingEmails = []) {
  const lines = text.split(/\r?\n/).map((line, index) => ({ line, number: index + 1 })).filter(item => item.line.trim());
  if (!lines.length || lines.length > 100) throw new Error('请填写 1–100 行账号资料。');
  const seen = new Set();
  const existing = new Set(existingEmails.map(email => String(email || '').trim().toLowerCase()));
  return lines.map(({ line, number }) => {
    const first = line.indexOf('----');
    const last = line.lastIndexOf('----');
    if (first < 1 || first === last) throw new Error(`第 ${number} 行格式不完整，请使用“邮箱----密码----2FA”。`);
    const email = line.slice(0, first).trim().toLowerCase();
    const password = line.slice(first + 4, last);
    const totp_secret = line.slice(last + 4).trim();
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email) || email.length > 254 || !password || password.length > 4096) throw new Error(`第 ${number} 行的邮箱或密码无效，请核对后重试。`);
    if (!totp_secret || totp_secret.length > 1024 || !validImportTOTP(totp_secret)) throw new Error(`第 ${number} 行的 2FA 密钥无效。请填写验证器长期密钥或标准 TOTP URI，不是六位验证码。`);
    if (seen.has(email)) throw new Error(`第 ${number} 行与前面的账号重复，请移除重复项。`);
    if (existing.has(email)) throw new Error(`第 ${number} 行的账号已存在，请在账号资料中更新密码与 2FA。`);
    seen.add(email);
    return { email, password, totp_secret };
  });
}

function healthLabel(status) {
  return ({ not_linked: '尚未绑定', unbound: '尚未绑定', not_checked: '尚未巡检', unknown: '状态未知', healthy: '未记录认证异常', available: '未记录认证异常', ok: '未记录认证异常', needs_reauth: '需要重新授权', requires_sign_in: '需要重新登录', auth_failed: '鉴权失效', missing: 'CPA 凭证缺失', identity_changed: '凭证身份已变化', disabled: '人工禁用', manual_disabled: '人工禁用', paused: '质量暂停', quality_paused: '质量暂停', cooldown: '冷却中', native_cooldown: '冷却中', unsupported: '不支持巡检', unavailable: '暂不可用', refresh_pending: '等待 CPA 刷新', blocked: '暂不可自动处理', pending: '待核对', no_auth_failure: '未记录认证异常' })[status] || '待核对';
}
// Only credential and reconciliation reasons belong in CPA status. Older records
// may contain login errors; their diagnostics remain in recent operations/logs.
function healthReason(reason) {
  return ({ login_required: '2FA 登录成功后自动绑定', awaiting_inspection: '等待首次巡检', no_auth_failure_observed: '未记录认证异常', credential_deleted: '不会自动重建凭证', unbound: '2FA 登录成功后自动绑定', not_found: '不会自动重建凭证', missing: '不会自动重建凭证', requires_sign_in: 'CPA 标记需要重新登录', unauthorized: 'CPA 记录鉴权失败', invalid_grant: '授权已失效', token_expired: '等待 CPA 刷新', native_refresh_pending: '等待 CPA 刷新完成', native_unavailable: 'CPA 暂未标记为可用；不会据此自动重登', credential_login_pending: '等待上次授权提交完成', refresh_outcome_unknown: '刷新结果未确认；需人工重新授权', siwc_sign_in_required: 'SIWC 授权需人工重新登录', quality_identity_unavailable: '凭证身份尚未确认', login_source_conflict: '凭证来源存在冲突，需人工核对', login_source_unavailable: '凭证来源暂不可读取', login_target_unavailable: 'CPA 凭证状态暂不可读取', quality_paused: '跳过自动重新授权', manual_disabled: '跳过自动重新授权', operator_disabled: '跳过自动重新授权', native_cooldown: '等待冷却结束', cooldown: '等待冷却结束', identity_changed: '需人工核对绑定身份', unsupported_login_target: '当前来源不支持更新', auth_failure: 'CPA 记录鉴权失败', no_auth_failure: '未发现需要重登的记录', login_commit_pending: '等待上次提交完成', pending_commit: '等待上次提交完成', identity_conflict: '凭证身份冲突，需人工核对', security_uncertain: '安全操作待核对', security_pending: '安全操作待核对', plugin_unavailable: 'CPA 状态暂不可读取', storage_unavailable: '巡检数据暂不可读取', policy_changed: '凭证状态已变化，需重新巡检' })[reason] || '';
}
function automaticError(code) {
  if (['runtime_timeout', 'runtime_failed', 'exchange_failed', 'rate_limited', 'network_error', 'timeout'].includes(code)) return '本次自动登录未完成。后续重试受间隔和次数限制，请查看账号状态。';
  return messageFor(code);
}

function needsAttention(account) {
  return !!account.attention_required || !!account.security_pending || ['needs_reauth', 'requires_sign_in', 'auth_failed', 'missing', 'identity_changed'].includes(account.health_status) || ['commit_unknown', 'saved_not_adopted', 'security_uncertain'].includes(account.last_job_status);
}
function compactDate(value) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? '时间未知' : new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }).format(parsed);
}

function taskTone(job) {
  if (['committed', 'succeeded'].includes(job.status)) return 'good';
  if (['failed', 'conflict', 'commit_unknown', 'saved_not_adopted'].includes(job.status)) return 'bad';
  if (job.error_code || ['security_uncertain', 'interaction_required', 'interrupted', 'expired'].includes(job.status)) return 'warn';
  return activeStates.has(job.status) ? 'active' : '';
}
function filterTaskJobs(jobs, filter = 'all', query = '') {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return jobs.filter(job => {
    if (filter === 'active' && !activeStates.has(job.status)) return false;
    if (filter === 'issues' && !['bad', 'warn'].includes(taskTone(job))) return false;
    const text = [idOf(job), job.email, operationLabel(job), jobLabel(job), job.phase, job.error_code, job.error_stage, job.error_reason, job.runtime_code, jobDiagnostic(job)].join(' ').toLowerCase();
    return terms.every(term => text.includes(term));
  });
}
const requestLabels = {
  auth_providers: '读取登录方式', auth_csrf: '获取会话校验', auth_signin: '建立登录会话', auth_session: '核对登录会话', auth_callback: '完成会话回调', oauth_authorize: '请求 OAuth 授权', authorize_continue: '继续账号授权', password_verify: '提交密码验证', totp_challenge: '获取 2FA 验证方式', totp_verify: '提交 2FA 验证', session_select: '选择登录会话', workspace_select: '选择工作区', organization_select: '选择组织', consent_accept: '确认授权许可', mfa_info: '读取 2FA 状态', totp_disable: '移除旧 2FA', totp_enroll: '登记新 2FA', totp_activate: '激活新 2FA', logout_all: '退出所有设备', navigation: '加载认证页面',
};
function projectLogEntry(entry) {
  const strings = ['time', 'level', 'status', 'phase', 'error_code', 'error_stage', 'error_reason', 'event', 'runtime_code', 'request_kind', 'method', 'security_status'];
  const numbers = ['id', 'revision', 'http_status', 'duration_ms'];
  const booleans = ['failed', 'totp_reset', 'devices_logged_out', 'credential_enabled'];
  const result = {};
  for (const key of strings) if (typeof entry[key] === 'string') result[key] = entry[key].slice(0, 256);
  for (const key of numbers) if (Number.isSafeInteger(entry[key]) && entry[key] >= 0) result[key] = entry[key];
  for (const key of booleans) if (typeof entry[key] === 'boolean') result[key] = entry[key];
  return result;
}
function renderLogEntries(entries, all, job, opened = new Set()) {
  return entries.map(entry => {
    const entryKey = `${idOf(job)}:${entry.id}`;
    const summary = logEntrySummary(entry, job);
    const index = all.findIndex(item => item.id === entry.id);
    const interval = index > 0 ? logInterval(all[index - 1].time, entry.time) : '';
    const code = entry.error_reason || entry.runtime_code || entry.error_code;
    const level = logEntryIsIssue(entry) && entry.level === 'info' ? 'warn' : entry.level;
    const facts = [['事件', `#${entry.id}`], ['记录时间', entry.time], ['类型', entry.event], ['任务版本', entry.revision], ['状态', entry.status], ['阶段', entry.error_stage || entry.phase], ['错误代码', entry.error_code], ['运行时错误', entry.runtime_code], ['原因代码', entry.error_reason], ['请求', entry.request_kind], ['方法', entry.method], ['HTTP 状态', entry.http_status], ['请求耗时', typeof entry.duration_ms === 'number' ? `${entry.duration_ms} ms` : undefined], ['安全操作状态', entry.security_status], ['2FA 已保存', typeof entry.totp_reset === 'boolean' ? String(entry.totp_reset) : undefined], ['退出请求已接受', typeof entry.devices_logged_out === 'boolean' ? String(entry.devices_logged_out) : undefined], ['凭证已启用', typeof entry.credential_enabled === 'boolean' ? String(entry.credential_enabled) : undefined]].filter(([,value]) => value !== undefined && value !== '');
    const requestMeta = entry.event === 'request' ? `<span class="account-log-request-meta">${entry.method ? `<code>${escapeHTML(entry.method)}</code>` : ''}${entry.http_status ? `<code>HTTP ${entry.http_status}</code>` : ''}${typeof entry.duration_ms === 'number' ? `<span>${entry.duration_ms} ms</span>` : ''}</span>` : '';
    return `<div class="account-log-entry ${level === 'error' ? 'is-error' : level === 'warn' ? 'is-warning' : ''}"><div class="account-log-clock"><time title="${escapeHTML(date(entry.time))}">${escapeHTML(logTime(entry.time))}</time><small title="与上一条已记录事件的间隔">${interval ? `+${escapeHTML(interval)}` : `#${entry.id}`}</small></div><span class="account-log-kind">${level === 'error' ? '错误' : level === 'warn' ? '提醒' : entry.event === 'request' ? '请求' : ['committed', 'succeeded'].includes(entry.status) ? '完成' : '进度'}</span><details data-log-key="${escapeHTML(entryKey)}"${opened.has(entryKey) ? ' open' : ''}><summary>${accountIcon('chevron')}<span class="account-log-message"><strong class="account-log-title">${escapeHTML(summary.title)}</strong>${summary.detail ? `<span class="account-log-detail">${escapeHTML(summary.detail)}</span>` : ''}${requestMeta}${code ? `<code class="account-log-inline-code">${escapeHTML(code)}</code>` : ''}</span></summary><div class="account-log-event-detail"><dl>${facts.map(([label,value]) => `<div><dt>${label}</dt><dd><code>${escapeHTML(String(value))}</code></dd></div>`).join('')}</dl><pre>${escapeHTML(JSON.stringify(projectLogEntry(entry), null, 2))}</pre></div></details></div>`;
  }).join('');
}
function downloadLogReport(report) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(report, null, 2)], { type: 'application/json' }));
  const anchor = document.createElement('a'); anchor.href = url; anchor.download = `login-diagnostics-${report.jobs[0]?.id || 'task'}.json`; anchor.click(); URL.revokeObjectURL(url);
}
function accountIcon(name) {
  if (name === 'upload') return '<svg class="login-control-icon" viewBox="0 0 20 20" aria-hidden="true"><path d="M10 13V3m-4 4 4-4 4 4M3 13v4h14v-4"/></svg>';
  const paths = { check: 'm4 10 4 4 8-8', alert: 'M10 6v5M10 14h.01M10 2a8 8 0 1 1 0 16 8 8 0 0 1 0-16', logout: 'M9 3H4v14h5M11 6l4 4-4 4M7 10h10', edit: 'm12.5 3.5 4 4M4 12l9-9 4 4-9 9-5 1zM11 17h6', down: 'm5 7 5 5 5-5', reset: 'M4 7a6.5 6.5 0 1 1-.4 5M4 3v4h4', more: 'M3 10h.01M10 10h.01M17 10h.01', login: 'M11 3h5v14h-5M3 10h10M9 6l4 4-4 4', user: 'M13 6a3 3 0 1 1-6 0 3 3 0 0 1 6 0M4 17v-2c0-5 12-5 12 0v2', delete: 'M3 5h14M7 5V3h6v2M5 5l1 12h8l1-12M8 8v6M12 8v6', task: 'M6 3h8v3H6zM6 5H4v13h12V5h-2M7 10h6M7 14h6', logs: 'M4 3h12v14H4zM7 7h6M7 10h6M7 13h4', refresh: 'M16 7a6.5 6.5 0 1 0 .4 5M16 3v4h-4', copy: 'M7 7h10v10H7zM13 7V3H3v10h4', download: 'M10 2v11m-4-4 4 4 4-4M3 13v4h14v-4', search: 'M13 13l4 4M14 8a6 6 0 1 1-12 0 6 6 0 0 1 12 0', chevron: 'm7 5 5 5-5 5' };
  return `<svg class="login-control-icon" viewBox="0 0 20 20" aria-hidden="true"><path d="${paths[name] || paths.logs}"/></svg>`;
}
function logTime(value) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? '时间未知' : parsed.toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', fractionalSecondDigits: 3 });
}
function logInterval(start, end) {
  const milliseconds = Date.parse(end) - Date.parse(start);
  if (!Number.isFinite(milliseconds) || milliseconds < 0) return '';
  if (milliseconds < 1000) return `${milliseconds} ms`;
  if (milliseconds < 60000) return `${(milliseconds / 1000).toFixed(2)} s`;
  return `${Math.floor(milliseconds / 60000)}m ${(milliseconds % 60000 / 1000).toFixed(1)}s`;
}
function logTiming(entries) {
  return entries.length > 1 ? logInterval(entries[0].time, entries.at(-1).time) : '';
}
function logRetention(saved) {
  if (!saved) return '';
  return [saved.legacy ? '早期任务缺少完整步骤记录。' : '', saved.truncated ? `日志开头已超出保留范围，当前从事件 #${saved.entries?.[0]?.id || '未知'} 开始；最多保留 200 条。` : '', saved.entries?.some(entry => entry.event === 'request_log_limit') ? '请求明细已达到 128 条上限；后续仍记录阶段变化与最终结果。' : ''].filter(Boolean).join(' ');
}
function logEntrySummary(entry, job) {
  const phase = entry.error_stage ? diagnosticStage(entry.error_stage) : entry.phase ? phaseLabel(entry.phase) : '';
  const reason = entry.error_reason ? logReasonLabel(entry.error_reason) : entry.error_code ? messageFor(entry.error_code) : '';
  if (entry.event === 'request_log_limit') return { title: '请求明细已达记录上限', detail: '后续继续记录阶段变化与最终结果' };
  if (entry.event === 'request') return { title: requestLabels[entry.request_kind] || '认证请求', detail: [entry.http_status >= 400 ? httpStatusLabel(entry.http_status) : '', reason || (entry.failed ? '请求传输失败' : ''), phase].filter(Boolean).join(' · ') };
  if (entry.error_code === 'runtime_failed' && entry.runtime_code === 'service_error' && !entry.error_reason) {
    const status = entry.http_status || (job && entry.revision === job.revision && entry.error_stage === job.error_stage && entry.runtime_code === job.runtime_code ? job.http_status : 0);
    return { title: status ? httpStatusLabel(status) : '认证服务返回了服务端错误', detail: phase };
  }
  // Event titles use only facts recorded at that moment, never the job's final security flags.
  const title = reason || (['running', 'exchanging', 'validating'].includes(entry.status) && phase ? phase : entry.status === 'succeeded' && isSecurityJob(job) ? '安全操作完成' : labels[entry.status] || '状态已更新');
  const detail = reason ? phase : phase && phase !== title ? phase : '';
  return { title, detail };
}
function logEntryIsIssue(entry) { return ['warn', 'error'].includes(entry.level) || !!entry.error_code || !!entry.failed || entry.http_status >= 400; }
function filterLogEntries(entries, job, filter = 'all', query = '') {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  return entries.filter(entry => {
    if (filter === 'errors' && !logEntryIsIssue(entry)) return false;
    const summary = logEntrySummary(entry, job);
    const text = [summary.title, summary.detail, entry.id, entry.time, entry.status, entry.phase, entry.error_code, entry.error_stage, entry.error_reason, entry.runtime_code, entry.request_kind, entry.method, entry.http_status, entry.duration_ms].join(' ').toLowerCase();
    return terms.every(term => text.includes(term));
  });
}
function buildLogReport(jobs, cache, context = {}) {
  // Reports use an explicit field list; never serialize a Job or runtime response wholesale.
  const fields = ['id', 'revision', 'method', 'intent', 'action', 'automatic', 'status', 'phase', 'error_code', 'error_stage', 'error_reason', 'runtime_code', 'http_status', 'created_at', 'updated_at', 'auto_commit', 'enable_credential', 'credential_enabled', 'security_status', 'totp_reset', 'devices_logged_out', 'reauthorize', 'logout_all_devices'];
  return {
    schema_version: 2, generated_at: new Date().toISOString(),
    time_zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
    ui_version: context.ui_version, runtime_version: context.runtime_version,
    scope: context.scope || 'current', history_limited: !!context.history_limited,
    read_incomplete: !!context.read_incomplete || jobs.some(job => !cache.has(idOf(job)) || cache.get(idOf(job)).revision !== job.revision),
    export_scope: 'Selected task; all loaded events, independent of display search and severity filters.',
    jobs: jobs.map(job => {
      const stored = cache.get(idOf(job));
      const booleans = new Set(['automatic', 'auto_commit', 'enable_credential', 'credential_enabled', 'totp_reset', 'devices_logged_out', 'reauthorize', 'logout_all_devices']);
      const facts = Object.fromEntries(fields.filter(field => ['revision', 'http_status'].includes(field) ? Number.isSafeInteger(job[field]) : booleans.has(field) ? typeof job[field] === 'boolean' : typeof job[field] === 'string').map(field => [field, job[field]]));
      const events = (stored?.entries || []).map(projectLogEntry);
      return { ...facts, id: idOf(job), operation: operationLabel(facts), result: jobLabel(facts),
        diagnosis: jobDiagnostic(facts), advice: facts.error_code || ['security_uncertain','commit_unknown','saved_not_adopted'].includes(facts.status) ? diagnosticAdvice(facts) : '',
        log_loaded: !!stored, legacy: !!stored?.legacy, truncated: !!stored?.truncated,
        log_revision: stored?.revision ?? null, log_matches_job_revision: !!stored && stored.revision === job.revision,
        request_details_limited: events.some(entry => entry.event === 'request_log_limit'),
        retained_count: stored?.retained_count ?? events.length,
        first_event_id: events[0]?.id ?? null, last_event_id: events.at(-1)?.id ?? null,
        recorded_span: logTiming(events) || null, events };
    }),
  };
}
function logReportText(report) {
  const lines = [`Credential Ops 操作排查 · UI ${report.ui_version || 'unknown'} · 运行时 ${report.runtime_version || 'unknown'}`, `导出时间：${report.generated_at} · 时区 ${report.time_zone}`, '范围：所选任务的全部已加载事件（不受搜索和异常筛选影响）'];
  if (report.read_incomplete) lines.push('读取尚未完成，以下仅为已加载内容。');
  for (const job of report.jobs) {
    lines.push('', `${job.operation} · ${job.result}`, `任务：${job.id}`, `开始：${job.created_at || '未知'}`, `记录跨度：${job.recorded_span || '未提供'}`);
    if (job.error_code) lines.push(`错误：${job.error_code} · 阶段：${job.error_stage || '未记录'} · 原因：${job.error_reason || (job.http_status ? httpStatusLabel(job.http_status) : '未记录')}`);
    if (job.runtime_code) lines.push(`运行时错误：${job.runtime_code}`);
    if (job.http_status) lines.push(`HTTP 状态：${job.http_status}`);
    if (job.diagnosis) lines.push(`诊断：${job.diagnosis}`);
    if (job.advice) lines.push(`建议：${job.advice}`);
    if (!job.log_loaded || job.legacy || job.truncated) lines.push(`完整性：${!job.log_loaded ? '日志尚未读取' : [job.legacy ? '早期记录不完整' : '', job.truncated ? '开头已截断' : ''].filter(Boolean).join('；')}`);
    if (job.request_details_limited) lines.push('请求明细已达到记录上限；后续仅记录阶段变化与最终结果。');
    if (job.log_loaded && !job.log_matches_job_revision) lines.push('任务状态已更新，事件记录与当前任务版本尚未同步；请刷新后重新导出。');
    lines.push('事件（原始 UTC 时间）：');
    for (const entry of job.events) {
      const summary = logEntrySummary(entry, job);
      lines.push(`${entry.time} #${entry.id} [${entry.level}] ${summary.title}${summary.detail ? ' · ' + summary.detail : ''}`, JSON.stringify(entry));
    }
  }
  return lines.join('\n');
}
function logReasonLabel(reason) {
  return callbackReasons[reason] || networkReasons[reason] || ({
    human_verification: '需要完成人机验证', browser_verification_timeout: '网站自动检查超时', browser_verification_required: '网站验证尚未完成', email_verification: '需要邮箱验证码', phone_verification: '需要手机验证',
    unsupported_request: '当前登录服务暂不支持此授权步骤', unsupported_navigation: '暂不支持当前页面跳转', submit_unavailable: '未找到继续按钮',
    totp_method_unconfirmed: '无法确认验证码方式', workspace_selection: '需要选择工作区', account_selection: '需要选择账号',
    unexpected_dialog: '出现额外对话框', unexpected_download: '出现非预期下载', unsupported_page: '未识别当前登录页面',
    upstream_rejected: '官方页面拒绝验证', proxy_connection_failed: '代理连接失败',
    session_unconfirmed: '未确认网页登录会话', authentication_not_advanced: '验证响应未确认流程前进', response_invalid: '认证响应格式无效', response_too_large: '认证响应超过大小限制', response_read_failed: '认证响应读取失败',
    totp_factor_missing: '未找到可用的 2FA 验证方式', sentinel_unavailable: '官方登录安全组件未就绪',
  })[reason] || reason;
}
