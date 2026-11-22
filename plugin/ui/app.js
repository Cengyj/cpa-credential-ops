import { CommandReceipts, commandFinished, commandSlot, commandStatus, creationSlot, notAccepted, receiptLine, unknownReceipt } from './commands.js';
import { patchView, hydrateInlinePreviews } from './markup.js';
import { BrowserAuthorization, WorkspaceAdmission, readDirectory } from './workspace.js';
import { ManagementClient, isDiscarded } from './client.js';
import { createLoginWorkbench } from './login.js';
import {
  active,
  availableModels,
  sampleWindow,
  copyRunConfig,
  credentialBlock,
  manualIssueLabel,
  reasonLabel,
  credentialActions,
  configuredUpstreamActions,
  handlesCredential,
  upstreamConditions,
  date,
  escapeHTML,
  holdsPause,
  items,
  ruleModelShortcuts,
  splitRuleModels,
  safePreview,
  previewSource,
  isPartialAnswer,
  targetID,
  providerTargetName,
  frozenRunIdentity,
  qualityIdentityLabel,
  providerRouteReady,
  ruleObjectTypes,
  resolveRuleBinding,
  ruleScopeDescription,
  ruleTargetType,
  renderRunEvidence,
  renderHistoryResult,
  renderQualityDetail,
  renderSampleEvidence,
  renderRuleRows,
  renderRuleActivityContext,
  renderOrphanedHandling,
  renderScopeDispatch,
  dynamicScopeLabel,
  renderQualityRuns,
  renderQualityEmpty,
  ruleState,
  ruleScheduleEnabled,
  renderReleasePreview,
  questionTitle,
  questionTestSelection,
} from './view.js';

const $ = (id) => document.getElementById(id);
let ruleGroupGrants = new Set();
let workspaceGeneration = 0;
const manualTargetDraft = { id: '', provider: '', authIndex: '', isProvider: false, label: '', models: new Map() };
let manualReasoning = '';
const state = {
  connected: false,
  backend: null,
  targetsFresh: false,
  providerDirectoryVerified: true,
  targets: [],
  questions: [],
  rules: [],
  orphanedHandling: [],
  actions: [],
  metrics: null,
  runs: [],
  run: null,
  readingRun: null,
  pendingManual: null,
  historyOffset: 0,
  historyTotal: null,
  historyOrigin: null,
  poll: null,
  pollFailures: 0,
  release: null,
  settings: {},
  scopeBatch: '', scopeRule: '', batchOffset: 0, activityTotal: 0, activityRows: null,
  receiptBatch: '',
  page: 'login',
  manualView: 'results',
  readingExpanded: false,
  releaseBlocked: true,
  releaseAction: null,
  compareRun: null,
  sampleOffset: 0,
  reads: { questions: 0, run: 0, history: 0, activity: 0, operations: 0, targets: 0, settings: 0, refresh: 0, preview: 0, editor: 0 },
  busy: new Set(),
  editor: null,
  resourceStatus: {},
};
const value = (id) => $(id).value.trim();
const number = (id) => Number($(id).value);
function sampleCount(text) {
  const count = Number(text);
  return /^\d+$/.test(text) && Number.isSafeInteger(count) && count > 0 ? count : null;
}
const sampleCountError = '请输入可精确表示的正整数（最多 9007199254740991）。';
function readSampleCount(id) {
  const count = sampleCount(value(id));
  if (count === null) fieldError(id, sampleCountError);
  return count;
}
const checked = (id) => $(id).checked;
const findTarget = (id) => state.targets.find((target) => targetID(target) === id);
const ruleHeld = (rule) => holdsPause(rule, state.targets);
const managementPrefix = '/v0/management/plugins/model-quality';
const browserAuth = new BrowserAuthorization(managementPrefix);
const workspaceStorage = `model-quality:workspace:${managementPrefix}`;
const manualReceiptStorage = `model-quality:manual-receipt:${managementPrefix}`;
const manualCommands = new CommandReceipts('model-quality:manual-commands:' + managementPrefix, newKey);
let manualReceiptKey = '';
function restoreManualReceipt() {
  if (!state.connected) return;
  try {
    const legacy = sessionStorage.getItem(manualReceiptStorage);
    if (legacy && manualCommands.remember('legacy', legacy)) sessionStorage.removeItem(manualReceiptStorage);
  } catch { /* The active tab can still retain an in-memory reference. */ }
  const refs = manualCommands.records();
  if (!refs.has(manualReceiptKey)) manualReceiptKey = refs.keys().next().value || '';
}

function clearManualReceipt() {
  if (manualReceiptKey) manualCommands.forget(manualCommands.records().get(manualReceiptKey), manualReceiptKey);
  manualReceiptKey = '';
}

const ruleCommands = new CommandReceipts('model-quality:rule-commands:' + managementPrefix, newKey);
const shortcutStorage = `model-quality:rule-model-shortcuts:${managementPrefix}`;
let selectedRuleModels = [], customShortcuts = [], shortcutsLoaded = false, shortcutStorageNote = '';
const client = new ManagementClient(managementPrefix, key => {
  if (pageAccess && syncStoredAccess()) return;
  disconnect('当前浏览器的管理鉴权已失效，请输入有效密钥。后台授权状态需在登录后核对。', true, { expectedKey: key });
});
const api = async (path, method = 'GET', body, options) => {
  try { return await (method === 'GET' && !options?.fresh && pageAccess?.admission?.take(state.page, path) || client.request(path, method, body, options)); }
  catch (error) {
    if (error.detail?.startsWith('blocked: provider ')) error.message = reasonLabel(error.detail);
    throw error;
  }
};
const loginWorkbench = createLoginWorkbench({
  request: (path, method = 'GET', body, idempotencyKey) => method === 'GET' && pageAccess?.admission?.take('login', '/login' + path) || client.requestHostLogin(path, method, body, idempotencyKey),
  connected: () => state.connected,
  visible: () => pageDisplayed(),
  onFreshness: (failed, loading) => { if (state.connected && state.page === 'login') renderFreshness(failed, loading); },
  onCommitted: () => { targetsReadAt = 0; state.targetsFresh = false; if (state.page !== 'login') return refresh(); },
});

let stopThemeFollowing = () => {};
let localTheme = ['light', 'white', 'dark'].includes($('theme-toggle').value) ? $('theme-toggle').value : 'light';
function initializeTheme() {
  stopThemeFollowing();
  stopThemeFollowing = () => {};
  const selector = $('theme-toggle');
  const apply = (theme, followsHost = false) => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.themeSource = followsHost ? 'host' : 'local';
    document.body.classList.toggle('host-themed', followsHost);
    selector.hidden = followsHost;
    selector.disabled = followsHost;
    selector.value = theme;
  };
  apply(localTheme);
  try {
    if (window.parent === window) return;
    const parentDocument = window.parent.document;
    // Recognize the pinned management frontend without accessing authentication or storage.
    if (parentDocument.title.trim() !== 'CLI Proxy API Management Center') return;
    const parentRoot = parentDocument.documentElement;
    const update = () => {
      try {
        const applied = parentRoot.getAttribute('data-theme');
        if (applied === null) apply('light', true);
        else if (applied === 'white' || applied === 'dark') apply(applied, true);
        else apply(localTheme);
      } catch {
        apply(localTheme);
      }
    };
    const observer = new MutationObserver(update);
    observer.observe(parentRoot, { attributes: true, attributeFilter: ['data-theme'] });
    stopThemeFollowing = () => observer.disconnect();
    update();
  } catch {
    // Cross-origin and unrecognized parents keep the independent page controls.
    apply(localTheme);
  }
}

let noticeTimer;
const feedbackScope = () => `${client.epoch}:${state.page}:${state.page === 'manual' ? state.manualView : ''}`;
function clearNotice() {
  clearTimeout(noticeTimer);
  delete $('notice').dataset.ruleDeleteReceipt;
  $('notice').classList.add('hidden');
  $('notice').textContent = '';
  if ($('notice').parentNode !== $('main')) $('main').prepend($('notice'));
}

function notice(message, error = false, before = null) {
  clearNotice();
  if (before) $(before).before($('notice'));
  $('notice').textContent = message;
  $('notice').classList.remove('hidden');
  $('notice').classList.toggle('error', error);
  const dialog = document.querySelector('dialog[open], .inline-editor:not(.hidden)');
  if (error && !before && dialog && dialog.id !== 'release-dialog') {
    let feedback = dialog.querySelector('.dialog-message');
    if (!feedback) {
      feedback = document.createElement('div');
      feedback.className = 'notice error dialog-message';
      feedback.setAttribute('role', 'alert');
      dialog.querySelector('.dialog-heading').after(feedback);
    }
    feedback.textContent = message;
    feedback.tabIndex = -1;
    if (!dialog.querySelector('[aria-invalid="true"]')) feedback.focus({ preventScroll: false });
  }
  if (!error) noticeTimer = setTimeout(clearNotice, 6000);
}

function localFeedback(button, message) {
  const original = button.textContent;
  button.style.minWidth = `${button.getBoundingClientRect().width}px`;
  button.textContent = message;
  $('local-feedback').textContent = message;
  setTimeout(() => {
    if (button.isConnected && button.textContent === message) { button.textContent = original; button.style.removeProperty('min-width'); }
  }, 2500);
}

let actionRequest = null;
function actionDialog({ title, description, confirm = '确认', input = false, danger = false, submit }) {
  if ($('action-dialog').open) return;
  $('action-title').textContent = title;
  $('action-description').textContent = description;
  $('action-name-field').hidden = !input;
  $('action-name').required = input;
  $('action-name').value = '';
  $('action-error').classList.add('hidden');
  $('action-name').removeAttribute('aria-invalid');
  $('action-confirm').textContent = confirm;
  $('action-confirm').className = danger ? 'danger' : 'primary';
  actionRequest = { submit, input, epoch: client.epoch };
  $('action-dialog').showModal();
  $(input ? 'action-name' : 'action-cancel').focus();
}

function editorSnapshot(id) {
  const fields = [...$(id).querySelectorAll('input,select,textarea')].filter(field => !field.hasAttribute('data-preference') && !field.hasAttribute('data-group-grant')).map((field) => [field.id, ['checkbox','radio'].includes(field.type) ? field.checked : field.value]);
  return JSON.stringify(id === 'rule-dialog' ? [fields, selectedRuleModels, [...ruleGroupGrants].sort()] : fields);
}
function editorDirty() { return state.editor && state.editor.snapshot !== editorSnapshot(state.editor.id); }
function updateEditorDirty() {
  if (!state.editor) return;
  const indicator = $(state.editor.id).querySelector('.editor-dirty'), dirty = editorDirty();
  if (indicator.dataset.changed === String(dirty)) return;
  indicator.dataset.changed = String(dirty);
  indicator.textContent = dirty ? '有未保存更改' : state.editor.id === 'rule-dialog' ? '' : '尚未更改';
}
function protectEditor(action) {
  if (state.editor?.saving) { notice('正在保存当前编辑，请等待结果。'); return; }
  if (!editorDirty()) { action(); return; }
  actionDialog({ title: '放弃未保存的更改？', description: '当前编辑尚未保存。放弃后将关闭这些更改，已保存的规则和预算不受影响。', confirm: '放弃更改', danger: true, submit: action });
}

const uncertainRuleSaveMessage = '保存结果未确认，草稿已保留。正在按原请求标识读取受理回执。';
async function saveEditor(action) {
  const editor = state.editor;
  if (!editor || editor.saving) return;
  if (editor.saveUncertain) throw new Error(uncertainRuleSaveMessage);
  editor.saving = true;
  const controls = [...$(editor.id).querySelectorAll('input,select,textarea,button')].map((node) => [node, node.disabled]);
  controls.forEach(([node]) => { node.disabled = true; });
  let failedField;
  try { await action(editor); }
  catch (error) { failedField = error.field; throw error; }
  finally {
    editor.saving = false;
    controls.forEach(([node, disabled]) => { node.disabled = disabled; });
    if (state.editor === editor) {
      renderRuleTargetSelectors();
      updateRuleFields();
      if (failedField) $(failedField).focus();
    }
    renderAdmission();
  }
}

function markFieldError(id, message, focus = true) {
  const field = $(id);
  const errorID = `${id}-error`;
  let feedback = $(errorID);
  if (!feedback) {
    feedback = document.createElement('span');
    feedback.id = errorID;
    feedback.className = 'field-error';
    feedback.setAttribute('role', 'alert');
    (field.closest('.rule-stepper, .rule-object-options') || field).after(feedback);
  }
  if (feedback.textContent !== message) feedback.textContent = message;
  field.setAttribute('aria-invalid', 'true');
  field.setAttribute('aria-describedby', [...new Set((field.getAttribute('aria-describedby') || '').split(' ').filter(Boolean).concat(errorID))].join(' '));
  if (focus) {
    field.closest('details')?.setAttribute('open', '');
    field.focus();
  }
  return feedback;
}

function fieldError(id, message) {
  markFieldError(id, message);
  const error = new Error(message);
  error.field = id;
  throw error;
}

async function safely(action, key = '', button = null) {
  if (key && state.busy.has(key)) return;
  if (key) state.busy.add(key);
  const scope = feedbackScope();
  if (key) document.querySelectorAll('.dialog-message').forEach((node) => node.remove());
  const originalContent = button ? [...button.childNodes] : [];
  if (key === 'submit-run') renderAdmission();
  const busyLabel = key.includes('save') || key === 'save-rule' ? '保存中…' : key.includes('refresh') ? '读取状态中…' : key === 'connect' ? '连接中…' : '';
  if (button) {
    button.style.minWidth = `${button.getBoundingClientRect().width}px`;
    button.disabled = true;
    button.setAttribute('aria-busy', 'true');
    if (busyLabel) button.textContent = busyLabel;
  }
  try {
    return await action();
  } catch (error) {
    if (!isDiscarded(error) && scope === feedbackScope() && !(key === 'save-rule' && error.field)) notice(error.message || '操作失败，请重试。', true);
  } finally {
    if (key) state.busy.delete(key);
    if (button) {
      button.removeAttribute('aria-busy');
      button.disabled = false;
      button.style.removeProperty('min-width');
      if (busyLabel && button.textContent === busyLabel) button.replaceChildren(...originalContent);
    }
    renderAdmission();
  }
}

function updateFreshness() {
  if (state.page === 'login') return;
  const resources = state.page === 'manual' ? ['targets','questions','history'] : ['targets','questions','actions','rules','activity','metrics','settings'];
  const statuses = resources.map(name => state.resourceStatus[name]);
  renderFreshness(statuses.includes('error'), statuses.includes('loading'));
}
function renderFreshness(failed, loading) {
  $('data-state').textContent = failed ? '部分数据更新失败，请刷新重试' : loading ? '正在读取页面数据…' : `更新于 ${new Date().toLocaleTimeString('zh-CN', { hour12: false })}`;
  $('data-state').classList.toggle('stale', failed);
}

const backendLabels = {
  unconfigured: '后台未配置', waiting: '后台等待 CPA 就绪', ready: '后台管理授权已验证',
  invalid: '后台管理授权需要更新', restricted: '后台访问受限', rate_limited: '后台请求受限流',
  error: '后台配置异常', closing: '后台正在关闭',
};

let pageAccess = null, connectionTimer = null, candidateClient = null;
let statusSequence = 0, statusNextRead = 0, statusError = '', statusFailures = 0;
let editorRevision = null, backendWritePending = false, pageSuspended = false;

function validBackend(data) {
  if (!data || !Number.isSafeInteger(data.revision) || data.revision < 0 ||
      !Object.hasOwn(backendLabels, data.state) || typeof data.configured !== 'boolean') {
    throw new Error('后台连接状态格式无效，请刷新核对插件版本。');
  }
  return data;
}

function accessStatusLabel(backend) {
  if (pageAccess?.readError && state.connected) return '后台状态更新失败';
  return backend ? backendLabels[backend.state] : '后台状态待读取';
}

function renderConnection() {
  const backend = state.backend;
  const label = backendWritePending ? '正在核实后台配置' : accessStatusLabel(backend);
  const browser = state.connected ? '当前浏览器已通过 CPA 鉴权。' : '当前浏览器尚未验证。';
  const source = backend?.source === 'deployment' ? '（部署 Secret）' : '';
  const problem = { management_invalid_response: 'CPA 管理接口返回的格式无效', connection_invalidation_save_failed: '失效密钥已停用，后台正在等待私有目录可写以保存失效标记' }[backend?.code] || backend?.code;
  const diagnostic = problem ? `（${problem}）` : '';
  const retryAt = Date.parse(backend?.next_attempt_at);
  const retryLabel = backend?.code === 'connection_invalidation_save_failed' ? '下次保存失效标记' : '后台下次核验时间';
  const retry = Number.isFinite(retryAt) ? `${retryLabel} ${new Date(retryAt).toLocaleTimeString('zh-CN', { hour12: false })}。` : '';
  $('connection-status').textContent = [browser, backend && label + source + diagnostic + '。', retry, pageAccess?.readError, statusError].filter(Boolean).join(' ');
  $('connection-state').textContent = pageAccess?.readError ? (state.connected ? label : '页面数据暂不可达') : !backendWritePending && backend?.state === 'ready' ? '已连接' : label;
  $('connection-state').parentElement.dataset.backend = pageAccess?.readError ? 'stale' : backend?.state || '';
  $('connection-submit').textContent = state.connected ? (backend?.configured ? '更新后台密钥' : '保存后台连接') : '连接';
  $('connection-clear').hidden = !state.connected || !backend || (backend.state === 'unconfigured' && !backend.can_migrate);
  $('connection-clear').disabled = backendWritePending || !Number.isSafeInteger(editorRevision);
  $('management-key').required = !state.connected;
  $('management-key').placeholder = state.connected ? '留空使用当前浏览器的密钥' : '输入管理密钥';
}

function showConnectionDialog() {
  editorRevision = state.backend?.revision ?? null;
  renderConnection();
  $('connection-dialog').showModal();
  if (pageAccess && !backendWritePending && (state.connected || !pageAccess.admitting)) void readConnection(pageAccess);
}

function scheduleConnectionRead() {
  clearTimeout(connectionTimer);
  if (!pageAccess || !pageDisplayed() || backendWritePending || pageAccess.reading || !state.connected && pageAccess.admitting) return;
  connectionTimer = setTimeout(() => readConnection(pageAccess), Math.max(0, statusNextRead - Date.now()));
}

function restoreManagementConnection() {
  const key = browserAuth.savedKey();
  if (key) return connectManagement(key, { restoring: true });
  document.body.removeAttribute('data-resuming');
  if (key === null) notice('浏览器未允许本地存储，本次页面需输入管理密钥。');
}

function establishBrowserAccess(access) {
  if (state.connected || pageAccess !== access) return;
  if (syncStoredAccess()) return;
  browserAuth.verified(access);
  state.connected = true;
  document.body.classList.add('connected');
  document.body.removeAttribute('data-resuming');
  $('data-state').textContent = '正在读取页面数据…';
  $('management-key').value = '';
  if (!access.restoring) $('connection-dialog').close();
  renderConnection();
  rememberWorkspace();
  renderAdmission();
  renderHistory();
  renderRules();
  renderActivityState();
  openCurrentWorkbench();
  if (state.page === 'manual') resumeManualReceipts();
  schedulePoll();
  if (!access.expectedStorage) notice('当前浏览器未能保存身份。重新打开页面时需要再次输入管理密钥。');
}

function admitWorkspace(access, fetchStatus = true) {
  if (pageAccess !== access || access.admitting) return access.admitting;
  const firstPage = { login: loginWorkbench.entryResource, manual: '/targets?limit=100&offset=0', operations: '/rules?limit=100&offset=0' }[state.page];
  const admission = access.admission = new WorkspaceAdmission(state.page, firstPage, path => client.request(path));
  access.admitting = (async () => {
    try {
      await admission.response;
      if (pageAccess !== access) return;
      establishBrowserAccess(access);
    } catch (error) {
      if (pageAccess !== access || isDiscarded(error)) return;
      // A failure in one resource cannot decide the browser's access to another.
      // A definitive host rejection has already ended this session in the client.
    }
    if (fetchStatus && pageAccess === access && !access.reading) await readConnection(access);
  })().finally(() => { access.admitting = null; if (pageAccess === access) scheduleConnectionRead(); });
  return access.admitting;
}

async function readConnection(access) {
  if (!access || access !== pageAccess || backendWritePending) return;
  if (access.reading) return access.reading;
  const sequence = ++statusSequence;
  access.reading = (async () => {
    try {
      const backend = validBackend(await api('/connection'));
      if (pageAccess !== access || sequence !== statusSequence) return;
      if (syncStoredAccess()) return;
      const first = !access.seenBackend;
      access.seenBackend = true;
      const previous = state.backend?.state;
      state.backend = backend;
      if (state.connected) {
        if (previous !== backend.state) renderAdmission();
        if (!first) recoverVisibleData(previous !== backend.state);
      }
      if (editorRevision === null && $('connection-dialog').open) editorRevision = backend.revision;
      statusFailures = 0;
      if (access.readError && !state.connected) clearNotice();
      access.readError = '';
      statusNextRead = Date.now() + (backend.state === 'waiting' ? 5000 : 30000);
      const { command: setup, retryAt } = browserAuth.setupPlan(access, backend, first);
      if (retryAt) statusNextRead = Math.min(statusNextRead, retryAt);
      establishBrowserAccess(access);
      if (setup) await changeBackend(access, 'PUT', backend.revision, setup.mode, '', setup);
    } catch (error) {
      if (pageAccess !== access || sequence !== statusSequence || isDiscarded(error)) return;
      access.readError = `${state.connected ? '后台授权状态更新失败。' : '页面数据暂不可达。'}${error.message}`;
      statusFailures++;
      const delay = Math.min(60000, 2000 * 2 ** Math.min(statusFailures - 1, 5));
      statusNextRead = Date.now() + Math.max(error.retryAfterMs || 0, error.status === 403 ? 30000 : delay);
      document.body.removeAttribute('data-resuming');
      if (!state.connected) {
        notice(access.readError + ' 将自动重试读取。', true);
        if (!access.admitting && ![401, 403, 429].includes(error.status)) void admitWorkspace(access, false);
      }
    } finally {
      if (pageAccess === access) {
        access.reading = null;
        renderConnection();
        scheduleConnectionRead();
      }
    }
  })();
  return access.reading;
}

function connectManagement(key, { restoring = false } = {}) {
  key = key.trim();
  if (!key) return;
  if (pageAccess?.key === key) return readConnection(pageAccess);
  clearTimeout(connectionTimer);
  client.open(key);
  pageAccess = browserAuth.open(key, restoring);
  statusFailures = 0;
  statusNextRead = 0;
  statusError = '';
  document.body.toggleAttribute('data-resuming', restoring);
  return restoring ? admitWorkspace(pageAccess) : readConnection(pageAccess);
}

async function changeBackend(access, method, revision, mode, replacement = '', setup = null) {
  if (pageAccess !== access || !Number.isSafeInteger(revision) || backendWritePending) return;
  if (syncStoredAccess()) return;
  const saved = browserAuth.savedKey();
  const switching = !!replacement && replacement !== access.key;
  const transport = switching ? new ManagementClient(managementPrefix, () => {
    throw Object.assign(new Error('输入的密钥未通过 CPA 鉴权。'), { status: 401 });
  }) : client;
  if (switching) { candidateClient = transport; transport.open(replacement); }
  backendWritePending = true;
  const sequence = ++statusSequence;
  const current = () => pageAccess === access && statusSequence === sequence;
  clearTimeout(connectionTimer);
  renderConnection();
  try {
    // An interrupted page may only reconcile an attempted write with a read.
    browserAuth.rememberIntent({ attempted: true });
    const body = method === 'PUT' ? { revision, mode } : { revision };
    const next = validBackend(await transport.request('/connection', method, body));
    if (!current()) return;
    if (browserAuth.savedKey() !== saved) return syncStoredAccess();
    if (switching) {
      const remembered = browserAuth.remember(access, replacement);
      client.open(replacement);
      if (!remembered) notice('已更新后台授权，但浏览器未能保存新身份。', true);
    }
    const backendChanged = next.state !== state.backend?.state;
    state.backend = next;
    if (switching && state.page === 'login') void loginWorkbench.refresh();
    else if (switching && pageDisplayed()) void safely(refresh);
    else if (backendChanged) recoverVisibleData(true);
    renderAdmission();
    if (statusError && $('notice').textContent === statusError) clearNotice();
    access.readError = statusError = '';
    editorRevision = next.revision;
    statusNextRead = Date.now() + (next.state === 'waiting' ? 5000 : 30000);
    $('management-key').value = '';
    browserAuth.notifyRevision(next.revision);
    if (mode === 'configure') $('connection-dialog').close();
    if (method === 'DELETE') notice('已清除后台管理配置。当前浏览器仍可查询账号、规则和任务。');
  } catch (error) {
    if (!current() || isDiscarded(error)) return;
    if (error.status === 409 || error.outcomeUnknown) editorRevision = null;
    const note = error.outcomeUnknown ? '后台配置写入结果待核对。页面将只读取状态。' :
      error.status === 409 ? '后台配置版本已变化，请核对最新状态后重新操作。' :
      error.status === 507 ? '本次配置保存失败。请检查插件数据卷是否可写以及剩余空间。' : '后台连接未更新。';
    const detail = { management_key_invalid: '提交的密钥未通过后台 CPA 鉴权。', management_restricted: 'CPA 限制回环访问。', management_temporarily_banned: 'CPA 暂时封禁了回环访问。', management_rate_limited: 'CPA 管理接口正在限流。', management_network: '后台暂时无法连接 CPA。', management_unavailable: 'CPA 管理接口暂未就绪。' }[error.code];
    statusError = note + (detail || (error.status === 401 ? error.message : ''));
    statusNextRead = browserAuth.retrySetup(setup, error);
    if (statusNextRead) statusError += ' 页面将在 CPA 就绪后继续首次配置。';
    notice(statusError, true);
  } finally {
    if (switching) { transport.clear(); if (candidateClient === transport) candidateClient = null; }
    if (current()) {
      backendWritePending = false;
      renderConnection();
      scheduleConnectionRead();
    }
  }
}

function syncStoredAccess() {
  return browserAuth.changed(pageAccess, saved => {
    disconnect(saved ? '' : '已退出此浏览器。后台任务继续按原许可执行。', false, { forgetKey: false });
    if (saved) void connectManagement(saved, { restoring: true });
  });
}

function disconnect(
  message = '已清除管理密钥与页面数据。后台任务继续按原许可执行，重新登录后可查询。',
  failed = false,
  { forgetKey = true, expectedKey = pageAccess?.key } = {},
) {
  clearTimeout(connectionTimer);
  pageAccess = null;
  candidateClient?.clear();
  candidateClient = null;
  backendWritePending = false;
  statusSequence++;
  state.backend = null;
  $('rule-command-note').hidden = true;
  $('rule-command-note').textContent = '';
  $('manual-command-note').hidden = true;
  $('manual-command-note').textContent = '';
  statusError = '';
  editorRevision = null;
  const forgotten = !forgetKey || browserAuth.forget(expectedKey);
  document.body.removeAttribute('data-resuming');
  if (forgetKey && window.self !== window.top) {
    try { sessionStorage.removeItem(workspaceStorage); } catch { /* Storage is optional. */ }
  }
  client.clear();
  state.connected = false;
  Object.assign(manualTargetDraft, { id: '', provider: '', authIndex: '', isProvider: false, label: '' });
  manualTargetDraft.models.clear();
  manualReasoning = '';
  loginWorkbench.reset();
  state.targetsFresh = false;
  state.providerDirectoryVerified = true;
  clearTimeout(state.poll);
  clearTimeout(operationsPoll);
  operationsReading = activityReading = false;
  Object.keys(state.reads).forEach((key) => state.reads[key]++);
  Object.assign(state, {
    targets: [],
    questions: [],
    rules: [],
    orphanedHandling: [],
    actions: [],
    metrics: null,
    runs: [],
    run: null,
    readingRun: null,
    pendingManual: null,
    pollFailures: 0,
    release: null,
    settings: {}, scopeBatch: '', scopeRule: '', batchOffset: 0, activityTotal: 0, activityRows: null,
    historyOffset: 0,
    historyTotal: null,
    historyOrigin: null,
    resourceStatus: {},
  });
  document.querySelectorAll('dialog[open]').forEach((dialog) => dialog.close());
  $('management-key').value = '';
  for (const id of [
    'models',
    'prompt',
    'rule-models',
    'rule-prompt',
    'rule-review-standard',
    'release-preview',
  ])
    $(id).value !== undefined ? ($(id).value = '') : $(id).replaceChildren();
  $('safe-preview').srcdoc = '';
  $('rule-form').reset();
  ruleGroupGrants.clear();
  selectedRuleModels = [];
  $('rule-model-selection').replaceChildren();
  $('ops-batch-bar').hidden = true;
  clearScopeReceipt();
  for (const id of ['action-title', 'action-description', 'action-error', 'local-feedback']) $(id).textContent = '';
  document.body.classList.remove('connected');
  $('connection-state').textContent = '未连接';
  state.releaseBlocked = true;
  state.releaseAction = null;
  state.compareRun = null;
  state.editor = null;
  actionRequest = null;
  $('action-name').value = '';
  $('run-feedback').classList.add('hidden');
  for (const id of ['compare-model-a', 'compare-model-b', 'sample-filter']) $(id).replaceChildren();
  $('run-progress').style.width = '0%';
  $('run-progress').parentElement.setAttribute('aria-valuenow', '0');
  $('evidence-content').replaceChildren();
  $('release-feedback').replaceChildren();
  $('release-feedback').classList.add('hidden');
  $('rule-search').value = '';
  $('rule-filter').value = 'all';
  setManualView('setup');
  $('data-state').textContent = failed ? '管理连接失效，请重新连接' : '管理接口未连接';
  $('data-state').classList.add('stale');
  renderRun();
  $('operations-history').replaceChildren();
  renderSelectors();
  renderHistory();
  renderRules();
  renderMetrics();
  renderActivityState();
  renderAdmission();
  renderConnection();
  if (!forgotten) notice('已断开当前连接，但浏览器未允许清除已保存的管理密钥。请清除此站点的本地存储。', true);
  else if (message) notice(message, failed);
  else clearNotice();
}

const workspaces = {
  login: ['账号管理', $('page-description').textContent],
  manual: ['智商测试', '选择凭证与模型，测试并比较回答。'],
  operations: ['质量运维', '管理检测规则，查看模型质量与运行记录。'],
};

function initialWorkspace() {
  const hash = location.hash.slice(1);
  if (hash || window.self === window.top) return hash;
  try { return sessionStorage.getItem(workspaceStorage); } catch { return ''; }
}

function rememberWorkspace() {
  if (!state.connected || window.self === window.top) return;
  try { sessionStorage.setItem(workspaceStorage, state.page); } catch { /* Navigation still works without storage. */ }
}

function openCurrentWorkbench() {
  if (state.page === 'login') {
    loginWorkbench.open({ focus: false });
  } else {
    loginWorkbench.close({ focus: false });
    if (!state.connected) return;
    if (!state.resourceStatus.questions || state.page === 'operations' && !state.resourceStatus.rules) void safely(refresh);
    else if (state.page === 'manual' && !state.resourceStatus.history) void safely(loadHistory);
  }
}

function setPage(page) {
  const previous = state.page;
  if (page !== state.page) { workspaceGeneration++; clearNotice(); }
  state.page = ['login', 'manual', 'operations'].includes(page) ? page : 'login';
  openCurrentWorkbench();
  $('page-manual').classList.toggle('hidden', state.page !== 'manual');
  $('page-operations').classList.toggle('hidden', state.page !== 'operations');
  $('page-login').classList.toggle('hidden', state.page !== 'login');
  const [name, description] = workspaces[state.page];
  $('page-crumb').textContent = name;
  $('page-description').textContent = description;
  document.querySelectorAll('[data-page]').forEach((button) => {
    button.classList.toggle('active', button.dataset.page === state.page);
    button.setAttribute('aria-current', button.dataset.page === state.page ? 'page' : 'false');
  });
  history.replaceState(null, '', `#${state.page}`);
  rememberWorkspace();
  scheduleWorkspaceLayout();
  if (previous !== state.page) {
    if (state.page === 'manual') resumeManualReceipts();
    if (state.connected) updateFreshness();
    if (pageAccess && !state.connected && pageAccess.admitting) void readConnection(pageAccess);
    syncOperationsVisibility();
  }
}

let workspaceLayoutFrame = 0;
function scheduleWorkspaceLayout() {
  cancelAnimationFrame(workspaceLayoutFrame);
  workspaceLayoutFrame = requestAnimationFrame(() => {
    if (!['operations', 'manual'].includes(state.page)) return;
    const manual = state.page === 'manual';
    const workspace = document.querySelector(manual ? '.manual-workbench' : '.quality-workspace');
    // Measure the document offset so scrolling never changes the panel height.
    const top = workspace.getBoundingClientRect().top + window.scrollY;
    const compactManual = manual && window.innerWidth < 960;
    const minimum = manual ? (compactManual ? 380 : 520) : 460;
    const bottomSpace = compactManual ? 16 : manual ? 32 : 24;
    workspace.style.setProperty(manual ? '--manual-panel-height' : '--quality-panel-height', `${Math.max(minimum, Math.floor(window.innerHeight - top - bottomSpace))}px`);
  });
}

function setManualPane(view) {
  $('page-manual').dataset.pane = view;
  for (const button of document.querySelectorAll('.manual-pane-switch button')) {
    button.setAttribute('aria-pressed', String(button.dataset.manualPane === view));
  }
}

function setManualView(view, focus = false) {
  const showConfiguration = view === 'setup';
  const nextView = view === 'history' ? 'history' : 'results';
  if (nextView !== state.manualView) clearNotice();
  state.manualView = nextView;
  if (showConfiguration) setReadingExpanded(false);
  for (const name of ['results', 'history']) {
    $(`manual-${name}`).classList.toggle('hidden', name !== state.manualView);
  }
  $('manual-result-footer').classList.toggle('hidden', state.manualView !== 'results');
  $('manual-history-footer').classList.toggle('hidden', state.manualView !== 'history');
  document.querySelector('.answer-workspace')?.classList.remove('hidden');
  setManualPane(view);
  document.querySelectorAll('.view-nav [data-manual-view]').forEach((button) => {
    const selected = button.dataset.manualView === state.manualView;
    button.classList.toggle('active', selected);
    button.setAttribute('aria-current', selected ? 'page' : 'false');
    button.setAttribute('aria-controls', `manual-${button.dataset.manualView}`);
  });
  $('page-manual').dataset.view = state.manualView;
  $('manual-title').textContent = '智商测试';
  scheduleWorkspaceLayout();
  if (focus) {
    const section = $(showConfiguration ? 'manual-setup' : `manual-${state.manualView}`);
    section.tabIndex = -1;
    section.focus({ preventScroll: true });
    section.scrollIntoView({ block: 'nearest', behavior: 'instant' });
  }
}

function setReadingExpanded(expanded) {
  state.readingExpanded = expanded;
  if (expanded && $('page-manual').dataset.pane === 'setup') setManualPane(state.manualView);
  $('manual-setup').classList.toggle('hidden', expanded);
  $('page-manual').dataset.readingExpanded = String(expanded);
  const toggle = $('reading-toggle');
  if (!toggle) return;
  toggle.querySelector('span').textContent = expanded ? '返回配置' : '展开阅读';
  toggle.setAttribute('aria-pressed', String(expanded));
  toggle.setAttribute('aria-controls', 'manual-setup');
  scheduleWorkspaceLayout();
}

function openEditor(id) {
  loginWorkbench.close({ focus: false });
  state.reads.editor++;
  state.editor = { id, snapshot: editorSnapshot(id), opener: document.activeElement };
  if (!$(id).querySelector('.editor-dirty')) {
    const indicator = document.createElement('span');
    indicator.className = 'editor-dirty';
    indicator.setAttribute('role', 'status');
    $(id).querySelector('.editor-footer, .button-row.end').prepend(indicator);
  }
  updateEditorDirty();
  $(id).showModal();
  $(id).querySelector('.rule-form-scroll').scrollTop = 0;
  $('rule-name').focus({ preventScroll: true });
  $(id).scrollTop = 0;
}

function closeSurface(id, saved = false) {
  const node = $(id);
  if (state.editor?.id === id) {
    if (!saved && state.editor.saving) return;
    if (!saved && editorDirty()) { protectEditor(() => closeSurface(id, true)); return; }
    const opener = state.editor.opener?.isConnected ? state.editor.opener : $('rule-new');
    state.editor = null;
    node.close();
    // Nested discard confirmation remains modal until its own submit completes.
    if (!$('action-dialog').open) opener.focus({ preventScroll: true });
    else $('action-dialog').addEventListener('close', () => opener.isConnected && opener.focus({ preventScroll: true }), { once: true });
    return;
  }
  if (node.tagName === 'DIALOG' && (id !== 'action-dialog' || !state.busy.has('dialog-action'))) node.close();
}
$('rule-dialog').addEventListener('cancel', event => { event.preventDefault(); closeSurface('rule-dialog'); });

function targetOptions(targets = state.targets, forRule = false) {
  const seen = new Set(), duplicates = new Set();
  for (const target of targets) {
    if (!target.email) continue;
    if (seen.has(target.email)) duplicates.add(target.email);
    seen.add(target.email);
  }
  return '<option value="">选择凭证</option>' + targets.map(target => {
    if (target.is_provider) {
      const ready = providerRouteReady(target);
      return `<option value="${escapeHTML(targetID(target))}" ${forRule && !ready ? 'disabled' : ''}>${escapeHTML(providerTargetName(target))}${ready ? '' : ' · 路由未确认'}</option>`;
    }
    const label = target.email || `${target.provider || 'unknown'} · ${target.auth_index || targetID(target)}`;
    const suffix = duplicates.has(target.email) ? ` · ${target.auth_index || targetID(target)}` : '';
    const disabled = target.manual_disabled ? ' · CPA 已停用' : '';
    return `<option value="${escapeHTML(targetID(target))}">${escapeHTML(label + suffix)}${target.quality_paused ? ' · 质量暂停' : ''}${disabled}</option>`;
  }).join('');
}

function credentialName(id) {
  const target = findTarget(id);
  return target?.is_provider ? providerTargetName(target) : target?.email || id;
}

function questionOptions() {
  return (
    state.questions
      .map(
        (question) =>
          `<option value="${escapeHTML(question.id)}">${escapeHTML(questionTitle(question))}</option>`,
      )
      .join('') +
    (state.questions.some((question) => question.id === 'custom')
      ? ''
      : '<option value="custom">自定义题目</option>')
  );
}

function renderSelectors() {
  renderTargetSelectors();
  renderQuestionSelectors();
}

function selectedObjectTypes() {
  return [...$('rule-form').querySelectorAll('[name="rule-object"]:checked')].map(input => input.value);
}

function ruleGroups(targets = state.targets) {
  const result = new Map();
  for (const target of targets) {
    if (ruleTargetType(target) !== 'provider' || !target.provider_group_id) continue;
    if (!result.has(target.provider_group_id)) result.set(target.provider_group_id, {
      name: target.provider_name || target.provider_group_id, targets: [], verified:true,
    });
    const group = result.get(target.provider_group_id);
    group.targets.push(target);
    if (target.reasons?.some(reason => ['identity_conflict','provider_route_unverified'].includes(reason))) group.verified = false;
  }
  return result;
}

const groupValue = id => `~group/${id}`;
const routeValue = id => id?.startsWith('~') ? '~' + id : id || '';
const selectedRuleGroup = () => $('rule-target').selectedOptions[0]?.dataset.group || '';
function ruleOptionBinding(option) {
  const { id, index, provider, kind } = option?.dataset || {};
  return { auth_id: id, provider, target_auth_index: index, target_object_type: kind };
}
function selectedRuleTarget() {
  const option = $('rule-target').selectedOptions[0];
  return option?.disabled ? undefined : resolveRuleBinding(ruleOptionBinding(option), state.targets).target;
}
function renderRuleTargetSelectors(rule) {
  if (state.editor?.saving) return;
  const kinds = selectedObjectTypes();
  const visible = state.targets.filter(target => kinds.includes(ruleTargetType(target)));
  const options = ['<option value="">选择对象</option>'];
  function optionGroup(label, entries) {
    if (entries.length) options.push(`<optgroup label="${escapeHTML(label)}">${entries.join('')}</optgroup>`);
  }
  function targetOption(target, label) {
    const id = targetID(target);
    const stateLabel = target.manual_disabled || target.runtime_status === 'disabled' ? ' · 已停用' : '';
    return `<option value="${escapeHTML(routeValue(id))}" data-id="${escapeHTML(id)}" data-kind="${ruleTargetType(target)}" data-index="${escapeHTML(target.auth_index)}" data-provider="${escapeHTML(target.provider)}">${escapeHTML(label + stateLabel)}</option>`;
  }
  const accounts = visible.filter(target => ruleTargetType(target) === 'oauth');
  const emails = new Set(), repeated = new Set();
  for (const target of accounts) { if (emails.has(target.email)) repeated.add(target.email); emails.add(target.email); }
  optionGroup('OAuth 账户', accounts.map(target => targetOption(target,
    target.email ? target.email + (repeated.has(target.email) ? ' · ' + (target.auth_index || targetID(target)) : '') : targetID(target))));
  const groups = ruleGroups(visible);
  optionGroup('AI 提供商 · 配置组', [...groups].map(([id, group]) =>
    `<option value="${escapeHTML(groupValue(id))}" data-group="${escapeHTML(id)}" ${group.verified ? '' : 'disabled'}>${escapeHTML(group.name)} · 全部 ${group.targets.length} 条路由${group.verified ? '' : ' · 身份待核实'}</option>`));
  optionGroup('AI 提供商 · 单条路由', visible.filter(target => ruleTargetType(target) === 'provider').map(target =>
    targetOption(target, [target.provider_name || target.provider, target.auth_index || targetID(target)].join(' · '))));

  const field = $('rule-target'), last = field.selectedOptions[0];
  const config = rule?.config || rule;
  const group = rule ? rule.target_group : last?.dataset.group;
  const binding = resolveRuleBinding(rule || ruleOptionBinding(last), visible);
  let previous = group ? groupValue(group) : routeValue(binding.id);
  if (previous && (group ? !groups.has(group) : !binding.target)) {
    // Preserve an existing binding when an inventory refresh loses it. An explicit type change clears incompatible choices.
    previous = '~saved/' + previous;
    const attributes = group ? `data-group="${escapeHTML(group)}"` : `data-id="${escapeHTML(binding.id)}" data-kind="${escapeHTML(binding.type)}" data-index="${escapeHTML(binding.index)}" data-provider="${escapeHTML(binding.provider)}"`;
    options.push(`<option value="${escapeHTML(previous)}" ${attributes} disabled>${escapeHTML(group ? group + ' · 原绑定待核实' : binding.pendingLabel)}</option>`);
  }
  const html = options.join('');
  patchView(field, html);
  field.value = previous || '';
  for (const [id, stored] of [['rule-judge-target',config?.judge?.auth_id],['rule-control-target',config?.control_auth_id]]) {
    const select = $(id), chosen = stored ?? select.value;
    const missing = chosen && !state.targets.some(t => targetID(t) === chosen);
    const allowed = state.targets.filter(t => !t.is_provider || kinds.includes('provider'));
    const omitted = chosen && !allowed.some(t => targetID(t) === chosen);
    patchView(select, targetOptions(allowed,true) + (omitted ? `<option value="${escapeHTML(chosen)}" disabled>${escapeHTML(chosen)} · ${missing ? '目录待核实' : '不在所选类型内'}</option>` : ''));
    select.value = chosen || '';
  }
}

function renderRuleGroupGrants() {
  const enabled = selectedObjectTypes().includes('provider');
  $('rule-group-access').hidden = !enabled;
  if (!enabled) return;
  const groups = ruleGroups();
  const rows = [];
  for (const id of new Set([...groups.keys(), ...ruleGroupGrants])) {
    const group = groups.get(id), selected = ruleGroupGrants.has(id);
    if (group && !group.targets.some(t => t.provider_disable_unit === 'group') && !selected) continue;
    const routes = group?.targets.map(t => t.auth_index || targetID(t));
    const verified = group?.verified && state.targetsFresh;
    const detail = routes?.length ? `影响 ${routes.length} 条路由 · ${routes.join('、')}${verified ? '' : ' · 目录待核实'}` : '原授权配置组当前未核实';
    rows.push(`<label class="rule-group-option" data-key="grant:${escapeHTML(id)}"><input type="checkbox" data-group-grant="${escapeHTML(id)}" ${!selected && !verified ? 'disabled' : ''} ${selected ? 'checked' : ''}><span>${escapeHTML(group?.name || id)}</span><small>${escapeHTML(detail)}</small></label>`);
  }
  $('rule-group-access').hidden = !rows.length;
  patchView($('rule-group-options'), rows.join(''));
  for (const input of $('rule-group-options').querySelectorAll('input')) input.checked = ruleGroupGrants.has(input.dataset.groupGrant);
}

const manualModelKey = () => manualTargetDraft.id;
const manualTargetMatches = target => !!target && target.provider === manualTargetDraft.provider &&
  !!target.is_provider === manualTargetDraft.isProvider && (target.auth_index || '') === manualTargetDraft.authIndex;
function selectedManualTarget() {
  const option = $('target').selectedOptions[0], selected = findTarget(option?.dataset.id);
  return !option?.disabled && manualTargetMatches(selected) ? selected : null;
}

function renderManualTargetSelectors(resetReasoning = false) {
  const labels = new Map(), counts = new Map();
  for (const target of state.targets) {
    const label = target.is_provider ? `${target.provider_name || target.provider} · ${target.auth_index || targetID(target)}` : target.email || targetID(target);
    counts.set(label, (counts.get(label) || 0) + 1);
    labels.set(targetID(target), label);
  }
  const options = [`<option value="">${state.targets.length ? '选择测试对象' : state.connected ? '暂无测试对象' : '连接后选择对象'}</option>`];
  for (const [provider, label] of [[false, 'OAuth 账户'], [true, 'AI 提供商 · 单条路由']]) {
    const targets = state.targets.filter(target => !!target.is_provider === provider);
    if (!targets.length) continue;
    options.push(`<optgroup label="${label}">${targets.map(target => {
      const id = targetID(target), name = labels.get(id);
      const identity = counts.get(name) > 1 ? `${name} · ${provider ? target.provider : target.auth_index || id}` : name;
      const status = target.manual_disabled || target.runtime_status === 'disabled' ? ' · 已停用' : provider && !providerRouteReady(target) ? ' · 路由不可用' : '';
      return `<option value="${escapeHTML(routeValue(id))}" data-id="${escapeHTML(id)}">${escapeHTML(identity + status)}</option>`;
    }).join('')}</optgroup>`);
  }
  const id = manualTargetDraft.id, missing = id && !manualTargetMatches(findTarget(id));
  const selected = missing ? `~saved/${routeValue(id)}` : routeValue(id);
  if (missing) options.push(`<option value="${escapeHTML(selected)}" data-id="${escapeHTML(id)}" disabled>${escapeHTML(manualTargetDraft.label || id)} · 原绑定待核实</option>`);
  const field = $('target');
  patchView(field, options.join(''));
  field.value = selected;
  field.disabled = !state.targets.length;
  renderTarget(resetReasoning);
}

function renderTargetSelectors() {
  renderManualTargetSelectors();
  renderRuleTargetSelectors();
  if (state.editor) updateRuleFields();
}

function renderQuestionSelectors() {
  const previousQuestion = value('question');
  const previousPrompt = $('prompt').value;
  $('question-read-note').hidden = state.resourceStatus.questions !== 'error';
  const curated = ['logic-candy', 'visual-pelican-html', 'custom'];
  const questions = curated.map((id) => state.questions.find((question) => question.id === id)).filter(Boolean);
  $('question').innerHTML = questions.length
    ? questions.map((question) => `<option value="${escapeHTML(question.id)}">${escapeHTML(questionTitle(question))}</option>`).join('')
    : '<option value="">连接后加载题目</option>';
  if (questions.some((question) => question.id === previousQuestion)) $('question').value = previousQuestion;
  if (!state.editor) {
    const ruleQuestion = value('rule-question');
    $('rule-question').innerHTML = questionOptions();
    if ([...$('rule-question').options].some((option) => option.value === ruleQuestion)) $('rule-question').value = ruleQuestion;
  }
  renderQuestion();
  if (previousQuestion && value('question') === previousQuestion) {
    $('prompt').value = previousPrompt;
    updateQuestionNote();
  }
}

const reasoningLabels = { none: '关闭思考', minimal: '最低', low: '低', medium: '中', high: '高', xhigh: '极高', max: '最高', auto: '自动' };
const requestedReasoningLevels = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'];
const unknownProviderReasoning = (target, model) => target?.is_provider && availableModels(target).includes(model) && !Object.hasOwn(target.reasoning || {}, model);
function availableReasoningLevels(target, model, requested = '') {
  const capability = availableModels(target).includes(model) ? target.reasoning?.[model] : null;
  if (unknownProviderReasoning(target, model)) return new Set(['low', 'medium', 'high', 'xhigh', 'max', ...(['none', 'minimal', 'auto'].includes(requested) ? [requested] : [])]);
  const levels = capability?.Levels ?? capability?.levels ?? [];
  const supported = new Set(Array.isArray(levels) ? levels.filter((level) => Object.hasOwn(reasoningLabels, level)) : []);
  if (!levels.length && Number(capability?.Max ?? capability?.max) > 0) requestedReasoningLevels.forEach(level => supported.add(level));
  else if (capability && !levels.length) requestedReasoningLevels.filter(level => level !== 'minimal').forEach(level => supported.add(level));
  if (capability?.ZeroAllowed ?? capability?.zero_allowed) supported.add('none');
  if (capability?.DynamicAllowed ?? capability?.dynamic_allowed) supported.add('auto');
  return supported;
}

function renderReasoning(resetInvalid = false) {
  const target = selectedManualTarget();
  const model = value('models');
  const validModel = availableModels(target).includes(model);
  const capability = validModel ? target.reasoning?.[model] : null;
  const previous = manualReasoning;
  const supported = availableReasoningLevels(target, model, previous);
  const unavailable = !!previous && !supported.has(previous) && !resetInvalid;
  manualReasoning = supported.has(previous) || unavailable ? previous : '';
  const choices = [['', '默认'], ...Object.entries(reasoningLabels).filter(([level]) => supported.has(level))];
  if (unavailable) choices.push([previous, `原设置 ${previous} 已不可用`]);
  const options = choices.map(([level, label]) => `<option value="${escapeHTML(level)}"${unavailable && level === previous ? ' disabled=""' : ''}>${escapeHTML(label)}</option>`).join('');
  if ($('reasoning').innerHTML !== options) $('reasoning').innerHTML = options;
  $('reasoning').value = manualReasoning;
  $('reasoning').disabled = !validModel || (!supported.size && !unavailable);
  $('reasoning').dataset.unavailable = String(unavailable);
  const note = !validModel ? ''
    : unavailable ? '原思考强度已不在支持范围，请重新选择。'
    : unknownProviderReasoning(target, model) ? '未提供可核实的思考档位；按请求发送，实际支持取决于 CPA 转换和上游。'
    : !capability ? Object.hasOwn(target.reasoning || {}, model) ? '此模型未提供思考参数，使用模型默认设置。' : '思考能力未知，使用模型默认设置。'
    : !supported.size ? '目录未提供可识别的思考档位，使用模型默认设置。'
    : !((capability.Levels ?? capability.levels)?.length) && Number(capability.Max ?? capability.max) > 0 ? '选择请求思考强度，CPA 将按模型支持的预算范围调整。'
    : !((capability.Levels ?? capability.levels)?.length) ? '未声明思考档位或预算限制；按请求发送，实际支持取决于 CPA 转换和上游。'
    : '仅显示此模型支持的思考强度。';
  $('reasoning-note').textContent = note;
  $('reasoning-note').hidden = !note || note === '仅显示此模型支持的思考强度。';
  $('reasoning-note').classList.toggle('error-note', unavailable);
}

function renderTarget(selectModel = false) {
  const target = selectedManualTarget(), id = manualTargetDraft.id;
  const provider = manualTargetDraft.isProvider;
  const models = availableModels(target);
  const previous = manualTargetDraft.models.get(manualModelKey());
  const unavailable = !!previous && !models.includes(previous);
  const selected = previous || models[0] || '';
  if (target && selected) manualTargetDraft.models.set(manualModelKey(), selected);
  const subject = provider ? '供应商' : '凭证';
  const placeholder = !target ? `先选择${subject}` : `此${subject}暂无已注册模型`;
  const options = (unavailable ? `<option value="${escapeHTML(previous)}" disabled>${escapeHTML(previous)}（已不可用）</option>` : '') + (models.length ? models.map((model) => `<option value="${escapeHTML(model)}">${escapeHTML(model)}</option>`).join('') : `<option value="">${placeholder}</option>`);
  if ($('models').innerHTML !== options) $('models').innerHTML = options;
  $('models').value = selected;
  $('models').disabled = !models.length || (provider && !providerRouteReady(target));
  $('models').title = selected;
  $('model-note').textContent = unavailable ? canRefreshManualState({ auth_id: id, models: [previous] })
    ? '当前缓存未提供此模型，开始测试时将刷新核对。' : '原模型已从目录移除，请重新选择模型。'
    : target && !models.length && !provider ? '未发现已注册模型，请在 CPA 检查配置后刷新数据。' : '';
  $('model-note').classList.toggle('error-note', !!target && (!models.length || unavailable));
  if (!target) {
    $('target').removeAttribute('title');
    $('target-summary').classList.remove('field-state');
    const missing = provider ? '原选供应商或密钥索引已离开当前目录，请重新选择。' : '原选凭证已离开当前目录，请重新选择。';
    const missingSelection = id;
    const empty = 'CPA 当前没有可选择的测试对象。';
    $('target-summary').textContent = resourceMessage('targets', `测试${subject}`) ||
      (missingSelection ? missing : state.targets.length ? '' : empty);
  } else {
    const reason = provider && !state.providerDirectoryVerified ? '供应商目录更新失败，显示上次已读数据。' : !state.targetsFresh ? '凭证目录读取失败，当前状态待核实。' : credentialBlock({ auth_id: targetID(target), models: selected ? [selected] : [] }, state.targets, { manual: true });
    $('target').title = target.is_provider ? providerTargetName(target) : target.email || targetID(target);
    $('target-summary').classList.toggle('field-state', !reason && !provider);
    patchView($('target-summary'), reason ? `<span class="error-note">${escapeHTML(reason)}</span>` : provider ? '通过 CPA 原生路由测试' : '凭证可用');
  }
  renderReasoning(selectModel);
  renderAdmission();
}

const questionTextEncoder = new TextEncoder();
function customQuestionTextError(field) {
  const standard = field.id === 'rule-review-standard';
  // Match Go's strings.TrimSpace emptiness check and count the original UTF-8 text.
  if (!standard && !/[^\p{White_Space}]/u.test(field.value)) return '请填写题目内容。';
  const limit = standard ? 4096 : 16384;
  const excess = questionTextEncoder.encode(field.value).length - limit;
  return excess > 0 ? `至少删减 ${excess.toLocaleString('zh-CN')} 字节（UTF-8 上限 ${limit.toLocaleString('zh-CN')}）` : '';
}

function updateQuestionTextValidity(field, custom) {
  const message = custom ? customQuestionTextError(field) : '';
  field.setCustomValidity(message);
  const marked = field.hasAttribute('aria-invalid');
  if (message && (field.value || marked)) {
    const error = markFieldError(field.id, message, false);
    if (!marked && document.activeElement === field) error.scrollIntoView({ block: 'nearest', container: 'nearest' });
  } else clearFieldError(field);
}

function readCustomQuestionText(id) {
  const field = $(id), message = customQuestionTextError(field);
  if (message) fieldError(id, message);
  return field.value;
}

function updateQuestionNote() {
  const question = state.questions.find((entry) => entry.id === value('question'));
  const selected = questionTestSelection(question, $('prompt').value);
  const changed = !!question && question.id !== 'custom' && selected.question_id === 'custom';
  $('question-version').textContent = changed ? '已修改' : '';
  const note = $('prompt-note');
  note.hidden = !changed;
  note.textContent = changed ? '按自定义题目测试，不沿用参考答案。' : '';
  updateQuestionTextValidity($('prompt'), selected.question_id === 'custom');
  renderAdmission();
}

function renderQuestion() {
  const question = state.questions.find((entry) => entry.id === value('question'));
  $('prompt').readOnly = false;
  $('prompt').value = question?.prompt || '';
  updateQuestionNote();
}

function manualRunConfig() {
  const target = selectedManualTarget();
  if (!target) {
    fieldError('target', '请选择当前目录中的测试对象。');
  }
  const model = value('models');
  const refreshable = canRefreshManualState({ auth_id: targetID(target), provider: target.provider, models: [model] });
  if (!model || (!availableModels(target).includes(model) && !refreshable)) fieldError('models', '请选择此凭证目录中的可用模型。');
  if ($('reasoning').dataset.unavailable === 'true' && !refreshable) fieldError('reasoning', '请重新选择可用的思考强度。');
  const samples = readSampleCount('samples');
  const question = state.questions.find((entry) => entry.id === value('question'));
  const selection = questionTestSelection(question, $('prompt').value);
  if (selection.question_id === 'custom') readCustomQuestionText('prompt');
  const config = {
    auth_id: targetID(target), provider: target.provider, models: [model], samples,
    expected_object_type: manualTargetDraft.isProvider ? 'provider' : 'oauth', expected_auth_index: manualTargetDraft.authIndex || undefined,
    ...selection,
    parameters: { stream: true },
    judge: { mode: 'manual' },
  };
  if (manualReasoning) config.parameters.reasoning_effort = manualReasoning;
  return config;
}

function runConfig(prefix = '') {
  if (!prefix) return manualRunConfig();
  const get = (name) => value(`${prefix}${name}`);
  const scope = draftRuleScope(), all = !!dynamicScopeLabel(scope.target_scope) || !!scope.target_group;
  if (!scope.object_types.length) fieldError('rule-object-oauth', '请至少选择一种检测对象。');
  if (!all && !scope.auth_id) fieldError('rule-target', '请选择要检测的对象。');
  if (scope.expected_target_object_type && (!$('rule-target').selectedOptions[0]?.dataset.index || $('rule-target').selectedOptions[0].disabled)) {
    fieldError('rule-target', '当前目录无法核实所选对象，请刷新后重新选择。');
  }
  const models = draftRuleModels();
  const problem = ruleModelsProblem(models);
  if (problem) fieldError(`${prefix}models`, problem);
  const samples = readSampleCount(`${prefix}samples`);
  const judge = { mode: get('judge-mode') };
  if (judge.mode === 'llm') {
    const selected = findTarget(get('judge-target'));
    if (!selected || targetID(selected) === scope.auth_id)
      fieldError(`${prefix}judge-target`, '判题需要独立于被测凭证的另一凭证。');
    if (!get('judge-model')) fieldError(`${prefix}judge-model`, '请输入判题模型。');
    Object.assign(judge, {
      auth_id: targetID(selected),
      provider: selected.provider,
      model: get('judge-model'),
    });
  }
  const config = {
    ...scope,
    models,
    question_id: get('question'),
    samples,
    parameters: {
      stream: state.rules.find(r => r.id === value('rule-id'))?.parameters?.stream ?? true,
    },
    judge,
  };
  if (!all && config.question_id === 'random-linear' && get('control-target')) {
    const control = findTarget(get('control-target'));
    if (!control || targetID(control) === scope.auth_id)
      fieldError(`${prefix}control-target`, '随机题通过对照必须选择另一个凭证。');
    config.control_auth_id = targetID(control);
    config.control_provider = control.provider;
  }
  if (!Number.isSafeInteger(models.length * samples * (config.control_auth_id ? 2 : 1))) {
    fieldError(`${prefix}samples`, '模型与对照的单对象样本总数超过可精确表示的整数范围。');
  }
  const effort = $('rule-reasoning').querySelector('input:checked')?.value;
  if (effort) config.parameters.reasoning_effort = effort;
  if (config.question_id === 'custom') {
    config.prompt = readCustomQuestionText(`${prefix}prompt`);
    config.review_standard = readCustomQuestionText(`${prefix}review-standard`);
  }
  return config;
}

function blockedConfig(config, options) {
  if (!state.connected) return '请先连接管理接口。';
  if (!state.targetsFresh) return '凭证状态尚未确认，请刷新后再执行。';
  return credentialBlock(config, state.targets, options);
}

function manualBindingProblem(config, target) {
  const provider = config.provider_name || config.provider_observation || config.expected_object_type === 'provider';
  const index = config.subject_auth_index || config.expected_auth_index;
  if (!provider && !config.expected_object_type && !index) return '';
  if (!target || (config.provider && target.provider !== config.provider) || (index && target.auth_index !== index) ||
    (provider && !target.is_provider) || (config.expected_object_type === 'oauth' && target.is_provider)) {
    return provider ? '原绑定的供应商或密钥索引已不可用，请重新选择测试目标。' : '原绑定的凭证或索引已不可用，请重新选择测试目标。';
  }
  return '';
}

function blockedManualConfig(config, verified = false) {
  const target = findTarget(config.auth_id);
  if (verified && target?.is_provider && !state.providerDirectoryVerified) return '供应商目录暂不可核实，未提交测试。';
  if (!verified && target?.is_provider && state.backend && state.backend.state !== 'ready') return (backendLabels[state.backend?.state] || '后台状态待读取') + '，暂不能核实供应商配置。';
  const binding = manualBindingProblem(config, target);
  if (binding) return binding;
  if ((config.provider_name || config.provider_observation || config.expected_object_type === 'provider') &&
    config.models?.some(model => !availableModels(target).includes(model))) return '此供应商已不再注册原模型，请重新选择测试模型。';
  const blocked = blockedConfig(config, { manual: true }) || resourceMessage('questions', '题库');
  if (blocked) return blocked;
  if (config.auth_id && config.models?.some(model => !availableModels(findTarget(config.auth_id)).includes(model))) return '当前目录没有所选模型，请重新选择测试模型。';
  if (config.parameters?.reasoning_effort && config.models?.some(model => !availableReasoningLevels(findTarget(config.auth_id), model, config.parameters.reasoning_effort).has(config.parameters.reasoning_effort))) return '所选思考强度已不在当前模型支持范围，请重新选择。';
  return '';
}

function canRefreshManualState(config) {
  const target = findTarget(config.auth_id);
  if (!state.connected || state.resourceStatus.questions !== 'ready' || !target ||
    target.quality_paused || target.retained_pause || target.handling_state_reason || target.reasons?.includes('identity_conflict') ||
    target.credential_handling && !['disabled', 'restored', 'acknowledged', 'externally_enabled'].includes(target.credential_handling)) return false;
  const needsRead = !state.targetsFresh || (target.is_provider && (!state.providerDirectoryVerified || state.backend?.state !== 'ready'));
  const disabled = target.manual_disabled || target.runtime_status === 'disabled';
  if (!disabled && (!needsRead || credentialBlock(config, state.targets, { manual: true }))) return false;
  if (manualBindingProblem(config, target) || config.provider && config.provider !== target.provider) return false;
  return config.models?.length > 0 && config.models.every(model => availableModels(target).includes(model) ||
    manualTargetDraft.models.get(targetID(target)) === model);
}

function renderAdmission() {
  const config = {
    auth_id: selectedManualTarget() ? manualTargetDraft.id : '',
    models: value('models') ? [value('models')] : [],
    judge: { mode: 'manual' },
  };
  const blocked = blockedManualConfig(config) || (config.auth_id && !availableModels(selectedManualTarget()).includes(value('models')) ? '请先选择可用的测试目标和模型。' : '') || ($('reasoning').dataset.unavailable === 'true' ? '请重新选择可用的思考强度。' : '');
  const refreshable = canRefreshManualState(config);
  const testing = state.connected && (!!state.pendingManual || state.busy.has('submit-run'));
  const phase = state.pendingManual?.status;
  const retrySubmission = state.connected && !testing && !!manualReceiptKey;
  const summary = $('manual-config-summary');
  const phaseText = { queued: '排队中', running: '测试中', cancelling: '停止中', checking_receipt: '正在核对上次提交' }[phase] || '提交中';
  const count = sampleCount(value('samples'));
  const summaryText = testing ? phaseText : retrySubmission ? '未查到上次任务' : count ? `${count} 份回答` : '请输入回答数量';
  if (summary.textContent !== summaryText) summary.textContent = summaryText;
  if (!state.run) $('run-empty-title').textContent = testing ? '等待测试结果' : '暂无回答';
  if (!testing) $('run-submit-label').textContent = retrySubmission ? '重新提交' : '开始测试';
  $('run-submit').setAttribute('aria-busy', String(testing));
  $('run-submit').disabled = testing || !count || !config.auth_id || (!!blocked && !refreshable) || !$('prompt').validity.valid;
  $('run-submit').title = state.connected ? refreshable ? '提交时读取 CPA 当前状态；仍停用时不会调用模型。' : blocked || $('prompt').validationMessage : '';
  $('release-confirm').disabled = state.releaseBlocked || state.busy.has('release') || !state.connected;
  const ruleSave = $('rule-form').querySelector('button[type="submit"]');
  ruleSave.disabled = !state.connected || !!state.editor?.saving || !!state.editor?.saveUncertain;
  ruleSave.title = state.editor?.saveUncertain ? '保存结果未确认，正在核对请求回执。' : '';
  for (const id of [
    'ops-all-rules',
    'run-export',
    'run-cancel',
    'refresh',
  ])
    $(id).disabled = !state.connected || $(id).getAttribute('aria-busy') === 'true';
  if (state.run) {
    const reason = blockedManualConfig(state.run.config || {});
    const refreshableRun = canRefreshManualState(state.run.config || {});
    $('run-rerun').disabled = (!!reason && !refreshableRun) || testing;
    $('run-rerun').title = refreshableRun ? '提交时读取 CPA 当前状态；仍停用时不会调用模型。' : reason;
  }
}

function newKey() {
  return globalThis.crypto.randomUUID?.() || Array.from(globalThis.crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('');
}

function manualSelection() {
  return { sequence: state.reads.run, scope: feedbackScope() };
}

function resumeManualReceipts() {
  if (!state.connected || state.pendingManual) return;
  restoreManualReceipt();
  if (manualReceiptKey) void checkManualReceipt(manualSelection(), false);
  const epoch = client.epoch, generation = workspaceGeneration;
  void (async () => {
    for (const [id, slot] of manualCommands.records()) {
      const current = () => state.connected && epoch === client.epoch && state.page === 'manual' && workspaceGeneration === generation && pageDisplayed();
      if (!current()) return;
      if (id === manualReceiptKey) continue;
      let label, finished = false;
      try {
        const run = await readManualReceipt(id);
        label = '已受理 · 任务 ' + run.id;
        finished = !active(run);
      } catch (error) {
        if (isDiscarded(error)) return;
        finished = error.status === 410 && error.code === 'gone';
        label = finished ? '历史记录已清理' : missingManualReceipt(error) ? '尚未查到受理回执' : '回执待读取';
      }
      if (!current()) return;
      const line = receiptLine($('manual-command-note'), id);
      line.textContent = `手动测试 · ${label} · ${id}`;
      line.dataset.finished = String(finished);
      if (finished) manualCommands.forget(slot, id);
    }
  })();
}

function missingManualReceipt(error) {
  return notAccepted(error) || error.status === 404 && error.code === 'not_found' && error.detail === 'not_found: submission receipt';
}
async function readManualReceipt(key) {
  const reply = await api(`/runs/detail?idempotency_key=${encodeURIComponent(key)}`);
  const run = reply?.run || reply;
  if (!run?.id || run.source !== 'manual_test' || run.config?.idempotency_key !== key) throw new Error('任务回执尚未确认。');
  return run;
}

function trackManualRun(run, selection, focus) {
  state.pollFailures = 0;
  state.pendingManual = active(run) ? { id: run.id, status: run.status } : null;
  if (!state.pendingManual) clearManualReceipt();
  if (selection.sequence === state.reads.run) {
    ++state.reads.run;
    state.historyOrigin = null;
    state.run = run;
    renderRun();
    if (focus && state.page === 'manual' && selection.scope === feedbackScope()) {
      clearNotice();
      setManualView('results', true);
      $('manual-results').scrollTop = 0;
    }
  }
  renderAdmission();
  schedulePoll(0);
  if (state.page === 'manual' && pageDisplayed()) void safely(loadHistory);
}

// Only an explicit submit may send a POST. A missing receipt can still be
// racing admission, so even that retry must reuse the original key.
async function checkManualReceipt(selection, focus) {
  const key = manualReceiptKey;
  const pending = { status: 'checking_receipt', selection, focus };
  state.pendingManual = pending;
  renderAdmission();
  try {
    const run = await readManualReceipt(key);
    if (state.pendingManual !== pending) return;
    trackManualRun(run, selection, focus);
  } catch (error) {
    if (state.pendingManual !== pending || isDiscarded(error)) return;
    const missing = missingManualReceipt(error);
    const removed = error.status === 410 && error.code === 'gone';
    if (missing || removed) {
      state.pendingManual = null;
      state.pollFailures = 0;
      if (removed) clearManualReceipt();
      renderAdmission();
      schedulePoll();
      return missing ? 'missing' : 'removed';
    }
    state.pollFailures = Math.min(state.pollFailures + 1, 4);
    schedulePoll();
  }
}

async function submitRun(config, binding = config) {
  if (state.pendingManual) return;
  const selection = manualSelection(), epoch = client.epoch;
  const current = () => state.connected && client.epoch === epoch && state.page === 'manual' && pageDisplayed();
  const slot = await commandSlot('manual', config);
  if (!current()) return;
  const previous = Array.from(manualCommands.records()).find(([id,known]) => known === slot || (known === 'legacy' && id === manualReceiptKey));
  let retry = !!previous;
  manualReceiptKey = previous?.[0] || '';
  if (retry && await checkManualReceipt(selection, true) !== 'missing') return;
  if (!current()) return;
  let targetRead = loadTargets({ fresh: true });
  for (;;) {
    try { await targetRead; }
    catch (error) { if (epoch !== client.epoch || targetRead === latestTargetsRead) throw error; }
    if (epoch !== client.epoch) return;
    if (targetRead === latestTargetsRead) break;
    targetRead = latestTargetsRead;
  }
  if (!current()) return;
  const blocked = blockedManualConfig(binding, true);
  if (blocked) throw new Error(blocked);
  if (!retry) {
    const claim = await manualCommands.claim(slot, current);
    manualReceiptKey = claim.id;
    retry = claim.existing;
    if (retry && await checkManualReceipt(selection, true) !== 'missing') return;
  }
  if (!current()) return;
  const key = manualReceiptKey;
  let id;
  try {
    const reply = await api('/runs', 'POST', { ...config, idempotency_key: key });
    id = reply.run_id || reply.id;
  } catch (error) {
    if (isDiscarded(error)) return;
    if (!error.outcomeUnknown && error.status < 500 && error.status !== 408 && !(retry && error.status === 409)) {
      if (!retry || error.status === 410) clearManualReceipt();
      if (error.code === 'blocked') error.message = manualIssueLabel(error.detail);
      throw error;
    }
    if (retry && error.code === 'blocked' && error.status === 409) {
      if (await checkManualReceipt(selection, true) === 'missing') throw new Error(manualIssueLabel(error.detail));
      return;
    }
  }
  if (!id) {
    await checkManualReceipt(selection, true);
    return;
  }
  trackManualRun({ id, source: 'manual_test', status: 'queued', config, created_at: new Date().toISOString() }, selection, true);
}

function updateRunProgress(run) {
  const id = run.id || run.run_id;
  if (id === state.pendingManual?.id) {
    state.pendingManual = active(run) ? { id, status: run.status } : null;
    if (!state.pendingManual) clearManualReceipt();
  }
  const row = state.runs.findIndex(item => (item.id || item.run_id) === id);
  if (row >= 0) {
    state.runs[row] = run;
    renderHistory();
  }
  renderAdmission();
}

async function loadRun(id, show = true, historyOrigin = null) {
  if (!id) throw new Error('接口未返回任务 ID。');
  const sequence = ++state.reads.run;
  state.readingRun = { id, sequence };
  try {
    const reply = await api(`/runs/detail?id=${encodeURIComponent(id)}`);
    if (sequence !== state.reads.run) return;
    state.run = reply.run || reply;
    if (show) state.historyOrigin = historyOrigin;
    updateRunProgress(state.run);
    state.pollFailures = 0;
    renderRun();
    $('run-feedback').classList.add('hidden');
    if (show) {
      if (state.page === 'manual') clearNotice();
      setManualView('results', state.page === 'manual');
      $('manual-results').scrollTop = 0;
    }
  } finally {
    if (state.readingRun?.sequence === sequence) state.readingRun = null;
    if (sequence === state.reads.run) schedulePoll();
  }
}

function schedulePoll(delay = Math.min(10000, 1200 * 2 ** state.pollFailures)) {
  clearTimeout(state.poll);
  state.poll = null;
  if (!state.connected || state.page !== 'manual' || !pageDisplayed()) return;
  const pending = state.pendingManual;
  if (pending?.status === 'checking_receipt') {
    state.poll = setTimeout(() => {
      if (state.pendingManual === pending) void checkManualReceipt(pending.selection, pending.focus);
    }, delay);
    return;
  }
  const selected = state.run?.id || state.run?.run_id;
  const ids = [...new Set([active(state.run) && selected, state.pendingManual?.id].filter(Boolean))];
  if (!ids.length) return;
  const epoch = client.epoch;
  const timer = state.poll = setTimeout(async () => {
    const current = () => epoch === client.epoch && timer === state.poll;
    if (!current()) return;
    const results = await Promise.allSettled(ids.map(id => api(`/runs/detail?id=${encodeURIComponent(id)}`)));
    if (!current()) return;
    let finished = false, retry = false;
    for (const [index, result] of results.entries()) {
      const id = ids[index];
      if (result.status === 'rejected') {
        const error = result.reason;
        if (isDiscarded(error)) continue;
        const missing = error.code === 'not_found' || error.status === 404;
        if (missing && id === state.pendingManual?.id) {
          state.pendingManual = null;
          clearManualReceipt();
        }
        if (missing && id === selected) {
          state.run = { ...state.run, status: 'unavailable' };
          renderRun();
        }
        if (id === selected) {
          $('run-feedback').textContent = missing ? '这条测试记录已不可用。' : `进度查询失败：${error.message}。会自动重试查询。`;
          $('run-feedback').classList.remove('hidden');
        }
        retry ||= !missing;
        finished ||= missing;
        continue;
      }
      const run = result.value.run || result.value;
      updateRunProgress(run);
      if (id === selected) {
        state.run = run;
        renderRun();
        $('run-feedback').classList.add('hidden');
      }
      finished ||= !active(run);
    }
    state.pollFailures = retry ? Math.min(state.pollFailures + 1, 4) : 0;
    renderAdmission();
    schedulePoll();
    if (finished && state.page === 'manual' && pageDisplayed()) await loadHistory();
  }, delay);
}

const detailDialog = $('quality-detail');
let qualityDetail = null;

function openQualityDetail(id) {
  if (!id) throw new Error('接口未返回任务 ID。');
  clearTimeout(qualityDetail?.timer);
  qualityDetail = { id, run: null, read: 0, timer: null };
  $('quality-detail-content').replaceChildren();
  detailDialog.querySelector('footer').hidden = true;
  detailDialog.querySelector('.dialog-message')?.remove();
  $('quality-detail-message').textContent = '正在读取检测记录…';
  $('quality-detail-retry').hidden = true;
  detailDialog.showModal();
  detailDialog.querySelector('.quality-detail-scroll').scrollTop = 0;
  return readQualityDetail(qualityDetail);
}

async function readQualityDetail(detail) {
  if (!detail || detail !== qualityDetail || !detailDialog.open) return;
  clearTimeout(detail.timer);
  detail.reading = true;
  const sequence = ++detail.read;
  const current = () => detailDialog.open && detail === qualityDetail && sequence === detail.read;
  try {
    const reply = await api(`/runs/detail?id=${encodeURIComponent(detail.id)}`);
    if (!current()) return;
    const run = reply.run || reply;
    patchView($('quality-detail-content'), renderQualityDetail(run, state.rules, { sampleOffset: detail.sampleOffset }));
    detail.run = run;
    $('quality-detail-message').textContent = '';
    $('quality-detail-retry').hidden = true;
    $('quality-detail-cancel').hidden = !active(run);
    detailDialog.querySelector('footer').hidden = false;
    if (operationsVisible() && qualityDetailActive(run)) detail.timer = setTimeout(() => readQualityDetail(detail), 1500);
    const row = state.activityRows?.findIndex(item => (item.id || item.run_id) === detail.id);
    if (row >= 0) {
      state.activityRows[row] = run;
      patchView($('operations-history'), renderQualityRuns(state.activityRows, state.rules));
    }
  } catch (error) {
    if (!current() || isDiscarded(error)) return;
    $('quality-detail-message').textContent = error.status === 404 ? '这条检测记录已不可用。' : `检测记录读取失败：${error.message}`;
    $('quality-detail-retry').hidden = false;
    if (error.status === 404) {
      detail.run = null;
      $('quality-detail-content').replaceChildren();
      detailDialog.querySelector('footer').hidden = true;
    }
  } finally {
    if (sequence === detail.read) detail.reading = false;
  }
}

function qualityDetailActive(run) {
  return run && (active(run) || ['disable_pending', 'restore_pending', 'delete_pending'].includes(run.action_status));
}

detailDialog.addEventListener('close', () => {
  if (detailDialog.open) return;
  clearTimeout(qualityDetail?.timer);
  qualityDetail = null;
  $('quality-detail-content').replaceChildren();
});
$('quality-detail-retry').addEventListener('click', () => readQualityDetail(qualityDetail));
$('quality-detail-export').addEventListener('click', () => exportRun(qualityDetail?.run));
$('quality-detail-cancel').addEventListener('click', event => {
  const detail = qualityDetail;
  if (!active(detail?.run)) return;
  safely(async () => {
    await api('/runs/cancel', 'POST', { id: detail.id });
    await readQualityDetail(detail);
  }, `cancel:${detail.id}`, event.currentTarget);
});

function renderRun() {
  const run = state.run;
  $('history-return').hidden = !run || state.historyOrigin?.id !== (run.id || run.run_id);
  if (!run) {
    for (const id of ['run-content', 'task-actions', 'run-feedback', 'run-rerun', 'run-export', 'run-cancel']) $(id).classList.add('hidden');
    for (const id of ['run-frozen-data', 'sample-list', 'run-meta', 'manual-result-summary']) $(id).replaceChildren();
    $('run-empty').classList.remove('hidden');
    state.compareRun = null;
    renderAdmission();
    return;
  }
  $('run-empty').classList.add('hidden');
  $('run-content').classList.remove('hidden');
  $('task-actions').classList.remove('hidden');
  const isActive = active(run);
  const repeatable = run.source === 'manual_test' && run.config?.models?.length === 1 && run.config?.judge?.mode === 'manual' && !run.config?.control_auth_id;
  $('run-rerun').classList.toggle('hidden', isActive || !repeatable);
  $('run-export').classList.remove('hidden');
  $('run-cancel').classList.toggle('hidden', !isActive);
  const id = run.id || run.run_id;
  const sameRun = state.compareRun === id;
  if (!sameRun) state.sampleOffset = 0;
  const rendered = renderRunEvidence(run, { sampleOffset: state.sampleOffset });
  patchView($('run-meta'), rendered.metaHTML);
  patchView($('run-frozen-data'), `${rendered.conditionsHTML}<div class="run-identifier">任务 ${escapeHTML(run.id || run.run_id)}</div>`);
  const progress = Math.min(100, Math.round(rendered.completed / Math.max(1, rendered.planned) * 100));
  $('run-progress').parentElement.hidden = !isActive || !rendered.planned;
  $('run-progress').style.width = `${progress}%`;
  $('run-progress').parentElement.setAttribute('aria-valuenow', String(progress));
  const unit = run.source === 'manual_test' ? '回答' : '样本';
  let summary = '';
  if (run.pruned_samples > 0) {
    summary = `留存 ${run.samples?.length || 0} 条记录 · 已裁剪 ${run.pruned_samples} 条`;
  } else if (rendered.planned && (isActive || rendered.completed !== rendered.planned)) {
    summary = `已结束 ${rendered.completed} / ${rendered.planned} 份${unit}`;
  } else if (run.samples?.length > 1) {
    summary = `共 ${run.samples.length} 条${unit}记录`;
  }
  const resultSummary = $('manual-result-summary');
  if (resultSummary.textContent !== summary) resultSummary.textContent = summary;
  if (sameRun) patchView($('sample-list'), rendered.samplesHTML);
  else $('sample-list').innerHTML = rendered.samplesHTML;
  hydrateInlinePreviews($('sample-list'), run);
  if (!sameRun) {
    const models = [...new Set([...(run.config?.models || []), ...(run.samples || []).map((sample) => sample.model || sample.request_model || '模型未知')])];
    const options = models.map((model, index) => `<option value="${escapeHTML(model)}">${index + 1} · ${escapeHTML(model.length > 48 ? `${model.slice(0,18)}…${model.slice(-24)}` : model)}</option>`).join('');
    $('compare-model-a').innerHTML = options;
    $('compare-model-b').innerHTML = '<option value="">不对比</option>' + options;
    $('compare-model-b').value = models[1] || '';
    document.querySelector('.model-selectors').classList.toggle('hidden', models.length < 2);
    document.querySelector('.comparison-toolbar').classList.toggle('hidden', models.length < 2 && ((run.source === 'manual_test' && run.config?.judge?.mode === 'manual') || (run.config?.samples || 1) < 2));
    state.compareRun = id;
    setRunDetails(false);
    $('run-feedback').classList.add('hidden');
  }
  const range = sampleWindow(run.config?.samples, state.sampleOffset);
  const filter = $('sample-filter'), previous = sameRun ? filter.value : 'all';
  const options = Array.from({ length: range.end - range.start }, (_, i) =>
    `<option value="${range.start + i}">样本 ${range.start + i + 1}</option>`).join('');
  patchView(filter, '<option value="all">全部样本</option>' + options);
  filter.value = previous !== 'all' && previous !== '' && Number(previous) >= range.start && Number(previous) < range.end ? previous : 'all';
  applyComparison();
  renderAdmission();
}

function setRunDetails(open) {
  $('run-frozen').setAttribute('aria-expanded', String(open));
  $('run-frozen-data').hidden = !open;
}

$('run-frozen').addEventListener('click', () => setRunDetails($('run-frozen-data').hidden));

function applyComparison() {
  const selected = [value('compare-model-a'), value('compare-model-b')].filter(Boolean);
  const filter = value('sample-filter');
  const grid = $('sample-list').querySelector('.comparison-grid');
  if (!grid) return;
  const order = [...selected, ...[...grid.children].map((group) => group.dataset.model).filter((model) => !selected.includes(model))];
  for (const [index, model] of order.entries()) {
    const group = [...grid.children].find((node) => node.dataset.model === model);
    if (group && grid.children[index] !== group) grid.insertBefore(group, grid.children[index]);
  }
  let visible = 0;
  for (const group of grid.children) {
    const shown = selected.includes(group.dataset.model);
    group.classList.toggle('hidden', !shown);
    if (shown) visible++;
    for (const sample of group.querySelectorAll('.sample')) sample.classList.toggle('hidden', filter !== 'all' && sample.dataset.sample !== filter);
  }
  grid.classList.toggle('single-model', visible < 2);
  const visibleGroup = [...grid.children].find((node) => !node.classList.contains('hidden'));
  grid.style.setProperty('--comparison-rows', String(1 + (visibleGroup?.querySelectorAll('.sample:not(.hidden)').length || 1)));
}

function resourceMessage(resource, label) {
  const status = state.resourceStatus[resource];
  if (status === 'ready') return '';
  if (status === 'loading') return `正在读取${label}…`;
  if (status === 'error') return `${label}读取失败，请刷新重试。`;
  return state.connected ? `尚未加载${label}，请刷新读取。` : `连接管理接口后读取${label}。`;
}

function renderRuleContext() {
  const panel = $('ops-rule-context');
  const rule = state.connected && !state.scopeBatch && state.rules.find(item => item.id === state.scopeRule);
  const html = rule ? renderRuleActivityContext(rule, credentialName, {
    dispatchOpen: panel.querySelector('details[data-skip-rule][open]')?.dataset.skipRule === rule.id,
    handlingOpen: panel.querySelector('details[data-rule-handling][open]')?.dataset.ruleHandling === rule.id,
    canAcknowledge: state.resourceStatus.rules === 'ready' && state.targetsFresh,
  }) : '';
  if (html || panel.hasChildNodes()) patchView(panel, html);
  panel.hidden = !html;
  const retained = $('ops-orphaned-handling');
  const orphaned = state.connected ? renderOrphanedHandling(state.orphanedHandling, credentialName, {
    expanded: !!retained.querySelector('details[open]'),
    canAcknowledge: state.resourceStatus.rules === 'ready' && state.targetsFresh,
    readError: state.resourceStatus.rules === 'error' ? '规则读取失败，保留上次待核对记录。请刷新重试。'
      : state.resourceStatus.targets === 'error' ? '凭证目录读取失败，请刷新后核对。' : '',
  }) : '';
  if (orphaned || retained.hasChildNodes()) patchView(retained, orphaned);
  retained.hidden = !orphaned;
}

function renderActivityState() {
  const message = resourceMessage('activity', '规则运行记录');
  if (message && !$('operations-history').querySelector('.history-row, .quality-run-table')) patchView($('operations-history'), renderQualityEmpty(message));
  for (const [id, resource, label] of [['rules-read-note', 'rules', '规则'], ['activity-read-note', 'activity', '运行记录']]) {
    $(id).hidden = state.resourceStatus[resource] !== 'error';
    $(id).textContent = `${label}读取失败，当前内容可能已过期。请刷新重试。`;
  }
  $('ops-scope-label').textContent = state.scopeRule ? state.rules.find(r => r.id === state.scopeRule)?.name || '已删除规则的运行记录' : '全部规则';
  $('ops-scope-label').hidden = !!state.scopeBatch && !state.scopeRule;
  $('ops-scope-clear').hidden = !state.scopeRule;
  $('ops-all-rules').setAttribute('aria-pressed', String(!state.scopeRule && !state.scopeBatch));
  renderRuleContext();
  $('ops-record-count').textContent = state.resourceStatus.activity === 'ready' ? `共 ${state.activityTotal} 条` : '';
  $('ops-history-clear').disabled = !state.connected || state.resourceStatus.activity !== 'ready' || (!state.activityTotal && !state.scopeRule && !state.scopeBatch);
  if (state.resourceStatus.activity !== 'ready') $('ops-batch-prev').disabled = $('ops-batch-next').disabled = true;
}

const historyDay = new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
const historyTime = new Intl.DateTimeFormat('zh-CN', { hour: '2-digit', minute: '2-digit', hour12: false });

function historyRows() {
  const rows = state.runs.map(run => {
    const stamp = new Date(run.created_at);
    const hasDate = run.created_at && !String(run.created_at).startsWith('0001-') && Number.isFinite(stamp.getTime());
    const id = escapeHTML(run.id || run.run_id);
    const config = run.config || {};
    const title = escapeHTML(questionTitle(config.question || state.questions.find(question => question.id === config.question_id), config.question_id));
    const identity = frozenRunIdentity(run);
    const supplier = identity.supplier;
    const credential = escapeHTML(supplier ? `供应商 · ${identity.label}` : identity.label);
    const models = escapeHTML(config.models?.join(' / ') || '模型未记录');
    const time = hasDate ? `<time class="history-clock" datetime="${stamp.toISOString()}" title="${date(stamp)}"><span class="history-hour">${historyTime.format(stamp)}</span><span class="history-date">${historyDay.format(stamp)}</span></time>` : '时间未记录';
    return `<tr class="history-row" data-history-id="${id}" data-key="run:${id}">
      <td class="history-time">${time}</td>
      <td class="history-subject"><button class="history-open" type="button" data-run="${id}" aria-label="查看「${title}」的完整回答" aria-describedby="history-meta-${id}">
        <span class="history-title" title="${title}">${title}</span>
        <span class="history-meta" id="history-meta-${id}" title="${credential} · ${models}"><span class="history-credential">${supplier ? '' : '<span class="sr-only">凭证 </span>'}${credential}</span><span aria-hidden="true">·</span><span class="history-models"><span class="sr-only">模型 </span>${models}</span></span>
      </button></td>
      <td class="history-result">${renderHistoryResult(run)}</td>
      <td class="history-actions"><button type="button" class="history-delete" data-delete-run="${id}" aria-label="删除测试记录：${title}" title="${active(run) ? '测试结束后可删除' : '删除记录'}" ${active(run) ? 'disabled' : ''}>
        <svg class="control-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18M8 6V4h8v2M5 6l1 14h12l1-14M10 10v6m4-6v6"/></svg></button></td>
    </tr>`;
  });
  return `<table class="history-table" aria-label="手动测试记录"><thead><tr><th scope="col">测试时间</th><th scope="col">题目与目标</th><th scope="col">答题结果</th><th scope="col"><span class="sr-only">删除记录</span></th></tr></thead><tbody>${rows.join('')}</tbody></table>`;
}

function renderHistory() {
  $('history-total').textContent = state.historyTotal == null ? '' : `共 ${state.historyTotal} 条记录`;
  $('history-count').textContent = state.historyTotal || '';
  $('history-range').textContent = state.runs.length ? `第 ${state.historyOffset + 1}–${state.historyOffset + state.runs.length} 条` : '';
  $('history-clear-all').disabled = !state.connected || state.resourceStatus.history !== 'ready' || !state.historyTotal;
  $('history-read-note').hidden = state.resourceStatus.history !== 'error' || !state.runs.length;
  patchView($('history-list'), state.runs.length
    ? historyRows()
    : `<div class="empty-line calm-empty"><strong>${escapeHTML(resourceMessage('history', '测试记录') || '还没有测试记录')}</strong>${state.resourceStatus.history === 'ready' ? '<span>开始一次智商测试后，记录会出现在这里。</span>' : ''}</div>`);
  $('history-page').textContent = `第 ${Math.floor(state.historyOffset / 10) + 1} 页`;
  $('history-prev').disabled = state.historyOffset === 0;
  $('history-next').disabled = state.historyOffset + 10 >= state.historyTotal;
  $('history-pagination').hidden = state.historyTotal <= 10 && state.historyOffset === 0;
}

async function loadHistory(offset = state.historyOffset) {
  const sequence = ++state.reads.history;
  if (!state.resourceStatus.history) state.resourceStatus.history = 'loading';
  if (!state.runs.length) renderHistory();
  let result;
  try {
    result = await api(`/runs?source=manual_test&offset=${offset}&limit=10&view=summary`);
  } catch (error) {
    if (sequence === state.reads.history && !isDiscarded(error)) {
      state.resourceStatus.history = 'error';
      renderHistory();
      updateFreshness();
      return false;
    }
    return;
  }
  if (sequence !== state.reads.history) return;
  const rows = items(result), total = result.total ?? rows.length;
  const lastOffset = Math.max(0, Math.floor((total - 1) / 10) * 10);
  if (offset > lastOffset) return loadHistory(lastOffset);
  state.resourceStatus.history = 'ready';
  if (offset !== state.historyOffset) $('manual-history').scrollTop = 0;
  state.historyOffset = offset;
  state.runs = rows;
  state.historyTotal = total;
  renderHistory();
  updateFreshness();
  return true;
}

async function cleanupRequest(path, body) {
  const confirmation = actionRequest;
  let receipt = null;
  try {
    const result = await api(path, 'DELETE', body);
    if (path === '/rules/run-history' || path === '/runs/history') {
      const archive = path === '/rules/run-history';
      const archived = archive ? (result?.archived ?? 0) : 0;
      const handling = archive ? (result?.kept_for_handling ?? 0) : 0;
      if ([result?.deleted, result?.kept, archived, handling].every(count => Number.isSafeInteger(count) && count >= 0) &&
          Number.isSafeInteger(result.deleted + archived) && handling <= result.kept) receipt = result;
    } else if (result?.ok === true) receipt = result;
  } catch (error) {
    if (isDiscarded(error) || (!error.outcomeUnknown && error.status >= 400 && error.status < 500 && error.status !== 408)) throw error;
  }
  // A second bulk DELETE could remove runs that completed after the original request.
  confirmation.cleanupSent = true;
  return receipt;
}

async function refreshCleanupLists(epoch) {
  try { await loadOperations(); }
  catch (error) { if (isDiscarded(error)) throw error; }
  return client.epoch === epoch;
}

function focusAfterCleanup(id) {
  $('action-dialog').addEventListener('close', () => {
    if ($(id).getClientRects().length) $(id).focus({ preventScroll: true });
  }, { once: true });
}

function forgetManualRun(id) {
  if (state.readingRun?.id === id && state.readingRun.sequence === state.reads.run) ++state.reads.run;
  if ((state.run?.id || state.run?.run_id) === id) { state.run = null; renderRun(); }
  if (state.pendingManual?.id === id) { state.pendingManual = null; clearManualReceipt(); }
  schedulePoll();
}

async function reconcileManualResult(receipt, previouslySelected) {
  // A detail response started before the clear may still contain a deleted run.
  if (state.readingRun) { ++state.reads.run; state.readingRun = null; }
  const id = state.run?.id || state.run?.run_id;
  if (!id || state.run.source !== 'manual_test') return true;
  if (receipt?.kept === 0 && id === previouslySelected) {
    forgetManualRun(id);
    return true;
  }
  try {
    await loadRun(id, false);
    return true;
  } catch (error) {
    if (isDiscarded(error)) throw error;
    if (error.status !== 404 || error.code !== 'not_found') return false;
    forgetManualRun(id);
    return true;
  }
}

function confirmClearManualHistory() {
  actionDialog({
    title: '清空所有测试记录？',
    description: '将永久删除所有分页中已结束的手动测试及其回答。进行中和处置所需的记录会保留。',
    confirm: '清空所有记录', danger: true,
    submit: async () => {
      const epoch = client.epoch;
      const selected = state.run?.id || state.run?.run_id;
      const receipt = await cleanupRequest('/runs/history');
      if (epoch !== client.epoch) return;
      if (receipt?.deleted) {
        state.runs = [];
        state.historyOffset = 0;
        state.historyTotal = receipt.kept;
        state.resourceStatus.history = 'loading';
      }
      const [listed, reconciled] = await Promise.all([loadHistory(0), reconcileManualResult(receipt, selected)]);
      if (epoch !== client.epoch) return;
      if (!receipt || receipt.deleted) void loadOperations().catch(() => { /* Each resource reports its own read error. */ });
      if (state.page !== 'manual' || state.manualView !== 'history') return;
      let message = receipt ? `已删除 ${receipt.deleted} 条测试记录。` : '清空回执未确认，删除条数未知。';
      if (receipt?.kept) message += `已保留 ${receipt.kept} 条进行中或处置所需的记录。`;
      if (listed === false || !reconciled) message += '部分数据读取失败，请刷新核对。';
      focusAfterCleanup('manual-history');
      notice(message, !receipt || listed === false || !reconciled, 'history-read-note');
      $('manual-history').scrollTop = 0;
    },
  });
}

function confirmHistoryDelete(id) {
  const run = state.runs.find(item => (item.id || item.run_id) === id);
  if (!run || active(run)) return;
  actionDialog({
    title: '删除测试记录',
    description: `删除「${questionTitle(run.config?.question, run.config?.question_id)}」（${date(run.created_at)}）及本次回答？任务 ${id}，对象 ${frozenRunIdentity(run).label}。`,
    confirm: '删除', danger: true,
    submit: async () => {
      const epoch = client.epoch;
      const receipt = await cleanupRequest('/runs', { id });
      let removed = !!receipt, checked = '';
      if (!receipt) {
        try {
          const result = await api(`/runs/detail?id=${encodeURIComponent(id)}`);
          if ((result?.run || result)?.id === id) checked = '当前仍可读取此记录。';
        } catch (error) {
          if (isDiscarded(error)) throw error;
          removed = error.status === 404 && error.code === 'not_found';
          if (removed) checked = '已按任务 ID 核对，当前查不到此记录。';
        }
      }
      if (removed) {
        forgetManualRun(id);
        const rows = state.runs.filter(item => (item.id || item.run_id) !== id);
        if (rows.length !== state.runs.length) state.historyTotal = Math.max(0, state.historyTotal - 1);
        state.runs = rows;
        renderHistory();
        renderAdmission();
      }
      const refreshed = await loadHistory();
      if (epoch !== client.epoch || state.page !== 'manual') return;
      focusAfterCleanup('manual-history');
      if (!receipt) notice(`删除回执未确认。${checked || '任务状态仍待读取。'}${refreshed === false ? '列表读取失败，请刷新。' : ''}`, true, 'manual-history');
      else if (refreshed === false) notice('记录已删除，列表刷新失败，请刷新重试。', true, 'manual-history');
    },
  });
}

function ruleCatalogNotice() {
  if (!state.questions.length && state.resourceStatus.questions !== 'ready') return resourceMessage('questions', '题库');
  return state.resourceStatus.targets === 'loading' ? '正在读取测试对象目录…' : '';
}

function renderRules() {
  const query = value('rule-search').toLocaleLowerCase(), filter = value('rule-filter');
  const filtered = state.rules.filter(rule => {
    const config = rule.config || rule;
    return (filter === 'all' || ruleState(rule, state.targets) === filter) && [rule.name, rule.id, config.auth_id, ...(config.models || []), ruleScopeDescription(rule, state.targets)].join(' ').toLocaleLowerCase().includes(query);
  });
  const message = !state.rules.length && resourceMessage('rules', '检测规则');
  const catalogNote = ruleCatalogNotice();
  const controls = catalogNote ? { ...state, connected: false } : state;
  patchView($('rules-list'), message ? renderQualityEmpty(message) : state.rules.length && !filtered.length ? renderQualityEmpty('没有匹配的规则', '调整搜索内容或状态筛选。', '<button class="text-button" data-rule-clear>清除筛选</button>') : renderRuleRows(filtered, state.targets, controls));
  const settingsPending = !['ready', 'error'].includes(state.resourceStatus.settings);
  for (const button of document.querySelectorAll('#rule-new, #rules-list [data-rule-new], #rules-list [data-rule-edit]')) {
    const waitingSettings = !button.hasAttribute('data-rule-edit') && settingsPending;
    button.disabled = !state.connected || !!catalogNote || waitingSettings;
    button.title = catalogNote || (waitingSettings ? '正在读取凭证处置能力' : '');
  }
  $('rule-count').textContent = state.resourceStatus.rules === 'ready' || state.rules.length ? String(state.rules.length) : '—';
  $('ops-all-count').textContent = $('rule-count').textContent;
  $('rule-summary').textContent = state.resourceStatus.rules === 'ready' || state.rules.length ? `显示 ${filtered.length} / ${state.rules.length} 条规则` : '尚未读取检测规则';
  $('rule-timed-count').textContent = state.resourceStatus.rules === 'ready' ? `定时 ${state.rules.filter(rule => ruleState(rule, state.targets) === 'timed').length}` : '';
}

function renderMetrics() {
  const metrics = state.metrics || {};
  $('metrics-read-note').hidden = state.resourceStatus.metrics !== 'error';
  $('metrics-read-note').textContent = state.metrics ? '检测统计读取失败，以下为上次读取的数值。请刷新重试。' : '检测统计读取失败，请刷新重试。';
  const percentage = (number) =>
    number == null ? '—' : `${(number <= 1 ? number * 100 : number).toFixed(1)}%`;
  const judged = metrics.judged ?? (metrics.correct != null && metrics.incorrect != null ? metrics.correct + metrics.incorrect : null);
  const prunedUnknown = metrics.unlocated_pruned_samples > 0;
  const stats = [
    [
      '质量通过率',
      percentage(metrics.quality_pass_rate ?? metrics.pass_rate),
      [`正确 ${metrics.correct ?? '—'}`, '/', `可判定 ${judged ?? '—'}`],
    ],
    [
      prunedUnknown ? '评测覆盖率下限' : '评测覆盖率',
      percentage(metrics.coverage ?? metrics.coverage_rate),
      [`已覆盖 ${metrics.covered ?? judged ?? '—'}`, '/', `${prunedUnknown ? '计划至多' : '计划'} ${metrics.planned ?? '—'}`],
      '', prunedUnknown ? `${metrics.unlocated_pruned_samples} 条历史裁剪记录缺少样本位置` : '',
    ],
    ['结论未知', metrics.unknown ?? '—', ['不计为答错', '·', `明确答错 ${metrics.incorrect ?? '—'}`]],
    [
      '已知用量',
      (metrics.known_tokens ?? metrics.total_tokens) == null
        ? '—'
        : Number(metrics.known_tokens ?? metrics.total_tokens).toLocaleString(),
      [`用量未知 ${metrics.unknown_usage ?? '—'}`, '·', `未采集事件 ${metrics.dropped_usage_events ?? metrics.dropped_events ?? '—'}`],
      'token',
    ],
  ];
  patchView($('metrics'), stats
    .map(
      ([name, total, [left, divider, right], unit, note]) =>
        `<article class="metric"><span class="metric-label">${escapeHTML(name)}</span>
          <div class="metric-amount"><strong class="metric-value"${total === '—' ? ' data-missing' : ''}>${escapeHTML(total)}</strong>${unit && total !== '—' ? `<span class="metric-unit">${escapeHTML(unit)}</span>` : ''}</div>
          <small><span>${escapeHTML(left)}</span> <span class="metric-divider" aria-hidden="true">${divider}</span> <span>${escapeHTML(right)}</span></small>${note ? `<small>${escapeHTML(note)}</small>` : ''}</article>`,
    )
    .join(''));
}

function allPages(path, key, options) {
  const generation = workspaceGeneration, epoch = client.epoch;
  return readDirectory(path, key, url => api(url, 'GET', undefined, options), options?.fresh,
    () => workspaceGeneration === generation && epoch === client.epoch && pageDisplayed());
}

async function loadActivity() {
  const sequence = ++state.reads.activity;
  activityReading = true;
  const query = new URLSearchParams({limit: '20', source: 'scheduled_probe', offset: String(state.batchOffset), view: 'summary'});
  if (state.scopeRule) query.set('rule_id', state.scopeRule);
  if (state.scopeBatch) query.set('scope_batch_id', state.scopeBatch);
  try {
    const page = await api(`/runs?${query}`);
    if (sequence !== state.reads.activity) return;
    const total = page.total ?? 0;
    const lastOffset = Math.max(0, Math.floor((total - 1) / 20) * 20);
    if (total && state.batchOffset > lastOffset) {
      state.batchOffset = lastOffset;
      return loadActivity();
    }
    const offset = total ? page.offset ?? state.batchOffset : 0;
    state.batchOffset = offset;
    state.activityTotal = total;
    state.activityRows = items(page);
    state.resourceStatus.activity = 'ready';
    $('ops-batch-summary').textContent = total ? `${state.scopeBatch ? '本批次 ' : ''}${offset + 1}–${Math.min(offset + 20, total)} / ${total}` : '0 条运行记录';
    $('ops-batch-prev').disabled = offset === 0;
    $('ops-batch-next').disabled = offset + 20 >= total;
    patchView($('operations-history'), renderQualityRuns(state.activityRows, state.rules));
  } catch (error) {
    if (sequence !== state.reads.activity || isDiscarded(error)) return;
    state.resourceStatus.activity = 'error';
    throw error;
  } finally {
    if (sequence === state.reads.activity) {
      activityReading = false;
      renderActivityState();
      updateFreshness();
    }
  }
}

function applyRuleSettings(settings, status) {
  state.settings = settings;
  state.resourceStatus.settings = status;
  renderRuleTargetSelectors();
  renderRules();
  if (state.editor) updateRuleFields();
  updateFreshness();
}

async function loadRuleSettings() {
  const sequence = ++state.reads.settings;
  try {
    const data = await api('/settings');
    if (sequence !== state.reads.settings) return;
    if (!data || typeof data !== 'object' || Array.isArray(data)) throw Error('管理接口返回了无效设置。');
    applyRuleSettings(data, 'ready');
  } catch (error) {
    if (sequence === state.reads.settings) applyRuleSettings(state.settings, 'error');
    throw error;
  }
}

let operationsPoll, operationsReading = false, activityReading = false;
let operationsFrameVisible = window.self === window.top;

function pageDisplayed() {
  return !pageSuspended && operationsFrameVisible && !document.hidden && navigator.onLine !== false;
}

function operationsVisible() {
  return state.connected && state.page === 'operations' && pageDisplayed();
}

function scheduleOperationsPoll(delay) {
  clearTimeout(operationsPoll);
  if (operationsReading || !operationsVisible()) return;
  delay ??= ruleCommands.records().size ? 1500 : state.activityRows?.some(qualityDetailActive) ? 5000 : 15000;
  operationsPoll = setTimeout(() => {
    if (operationsVisible()) void loadOperations(true).catch(() => {}); // Resource errors are displayed in their panels.
  }, delay);
}

let targetsReadAt = 0, operationsReadAt = 0, qualityRetryAt = 0;
function recoverVisibleData(backendChanged = false) {
  if (!state.connected || !pageDisplayed() || state.page === 'login') return;
  const failed = state.resourceStatus;
  if (failed.questions === 'error' && Date.now() >= qualityRetryAt) void loadQuestions().catch(() => {});
  if (state.page === 'operations' && (failed.actions === 'error' || failed.settings === 'error') && !operationsReading) {
    void loadOperations().catch(() => {});
  } else if (backendChanged || (state.page === 'manual' && failed.targets === 'error')) {
    void loadTargets().catch(() => {});
    if (backendChanged) void loadRuleSettings().catch(() => {});
  }
  if (state.page === 'manual' && state.manualView === 'history' && failed.history === 'error') void loadHistory();
}
function syncOperationsVisibility() {
  scheduleConnectionRead();
  schedulePoll();
  loginWorkbench.syncVisibility?.();
  if (!pageDisplayed() || !state.connected) {
    workspaceGeneration++;
    clearTimeout(operationsPoll);
    clearTimeout(qualityDetail?.timer);
    return;
  }
  const now = Date.now();
  scheduleOperationsPoll(Math.max(0, operationsReadAt + 5000 - now));
  recoverVisibleData();
  if (state.page === 'manual' && state.resourceStatus.questions === 'ready' &&
    (now - targetsReadAt >= 5000 || state.busy.has('submit-run'))) void loadTargets().catch(() => {});
  if (!qualityDetail) return;
  clearTimeout(qualityDetail.timer);
  if (operationsVisible() && !qualityDetail.reading && qualityDetailActive(qualityDetail.run)) void readQualityDetail(qualityDetail);
}

let latestTargetsRead;
function loadTargets(options) {
  const sequence = ++state.reads.targets, epoch = client.epoch;
  latestTargetsRead = (async () => {
    let data;
    try {
      const result = await allPages('/targets', 'auth_id', options);
      if (!result || typeof result !== 'object' || Array.isArray(result)) throw Error('凭证目录数据格式无效，请刷新重试。');
      data = result;
    } finally {
      if (epoch === client.epoch && sequence === state.reads.targets) {
        targetsReadAt = Date.now();
        state.targetsFresh = data !== undefined;
        state.resourceStatus.targets = data === undefined ? 'error' : 'ready';
        if (data) {
          const rows = items(data);
          const present = new Set(rows.map(targetID));
          const retained = data.providerDirectoryVerified ? [] : state.targets.filter(t => t.is_provider && !present.has(targetID(t)));
          state.targets = [...rows, ...retained];
          state.providerDirectoryVerified = data.providerDirectoryVerified;
        }
        renderTargetSelectors();
        renderRules();
        renderRuleContext();
        if (state.activityRows) patchView($('operations-history'), renderQualityRuns(state.activityRows, state.rules));
        if (state.runs.length) renderHistory();
        updateFreshness();
      }
    }
  })();
  return latestTargetsRead;
}

async function loadOperations(live = false) {
  const sequence = ++state.reads.operations;
  const epoch = client.epoch;
  const current = () => epoch === client.epoch && sequence === state.reads.operations;
  clearTimeout(operationsPoll);
  operationsReading = true;
  const resources = live ? ['rules', 'metrics', 'activity', 'targets'] : ['rules', 'metrics', 'activity', 'actions', 'targets', 'settings'];
  for (const resource of resources) if (!state.resourceStatus[resource]) state.resourceStatus[resource] = 'loading';
  if (!state.rules.length) renderRules();
  renderActivityState();
  // Apply each read as it settles so a slow statistic cannot delay a credential restriction.
  async function read(resource, request, render) {
    let data;
    try {
      const result = await request;
      if (!result || typeof result !== 'object' || Array.isArray(result)) throw Error('管理数据格式无效，请刷新重试。');
      data = result;
    }
    finally {
      if (current()) {
        state.resourceStatus[resource] = data === undefined ? 'error' : 'ready';
        render(data);
        updateFreshness();
      }
    }
  }
  const rulesRead = read('rules', allPages('/rules', 'id'), data => {
    if (data) {
      state.rules = items(data);
      state.orphanedHandling = data.orphanedHandling;
    }
    if (state.activityRows) patchView($('operations-history'), renderQualityRuns(state.activityRows, state.rules));
    renderRules();
    renderActivityState();
  });
  const requests = [
    ruleCommands.reconcile(readRuleCommand, showRuleCommand, ruleReceiptError, () => current() && operationsVisible()),
    rulesRead,
    read('metrics', api('/metrics?view=summary'), data => {
      if (data) state.metrics = data;
      renderMetrics();
    }),
    live && activityReading ? undefined : loadActivity(),
    loadTargets(),
  ];
  if (!live) requests.push(
    read('actions', allPages('/actions', 'action_id'), data => {
      if (data) state.actions = items(data);
    }),
    loadRuleSettings().catch(() => {}), // Optional settings have their own read status.
  );
  const results = await Promise.allSettled(requests);
  if (!current()) return;
  operationsReading = false;
  operationsReadAt = Date.now();
  // Reconciliation callers still require positive reads of rules, actions and credentials.
  const failed = results.find(result => result.status === 'rejected');
  scheduleOperationsPoll(failed ? 12000 : undefined);
  if (failed) throw failed.reason;
}

async function loadQuestions() {
  const sequence = ++state.reads.questions;
  if (!state.resourceStatus.questions) state.resourceStatus.questions = 'loading';
  try {
    const data = await api('/questions');
    if (sequence !== state.reads.questions) return;
    const list = Array.isArray(data) ? data : data?.items;
    if (!Array.isArray(list) || list.some(question => !question?.id)) throw Error('题库返回了无效的目录，请刷新重试。');
    state.questions = list;
    state.resourceStatus.questions = 'ready';
  } catch (error) {
    if (sequence === state.reads.questions) {
      state.resourceStatus.questions = 'error';
      qualityRetryAt = Date.now() + 3000;
    }
    throw error;
  } finally {
    if (sequence === state.reads.questions) {
      renderQuestionSelectors();
      renderRules();
      updateFreshness();
    }
  }
}

async function refresh() {
  const sequence = ++state.reads.refresh, runRead = state.reads.run;
  if (state.page === 'login') return;
  const requests = state.page === 'operations' ? [loadQuestions(), loadOperations()] : [loadQuestions(), loadTargets(), loadHistory()];
  await Promise.allSettled(requests);
  if (sequence !== state.reads.refresh) return;
  if (state.page === 'manual' && state.run && runRead === state.reads.run) await loadRun(state.run.id || state.run.run_id, false);
  const deletion = $('notice').dataset.ruleDeleteReceipt;
  if (deletion && state.page === 'operations' && state.resourceStatus.rules === 'ready' && state.resourceStatus.activity === 'ready') {
    notice(deletion, false, 'ops-all-rules');
  }
}

const idByteLength = id => new TextEncoder().encode(id).length;
const validShortcut = id => typeof id === 'string' && id.length > 0 && id.length <= 100 && idByteLength(id) <= 256 && !/[\s,，;；]/u.test(id);
const ruleModelLimit = () => state.settings.max_models || 8;
const draftRuleModels = () => [...new Set([...selectedRuleModels, ...splitRuleModels(value('rule-models'))])];
function ruleModelsProblem(models) {
  if (!models.length || models.length > ruleModelLimit()) return `请指定 1–${ruleModelLimit()} 个检测模型。`;
  return models.some(id => idByteLength(id) > 256) ? '单个模型 ID 不能超过 256 字节。' : '';
}

function loadRuleShortcuts() {
  if (shortcutsLoaded) return;
  shortcutsLoaded = true;
  try {
    const saved = JSON.parse(localStorage.getItem(shortcutStorage) || '[]');
    if (!Array.isArray(saved)) throw new Error('Invalid shortcut storage');
    customShortcuts = [...new Set(saved.filter(id => validShortcut(id) && !ruleModelShortcuts.includes(id)))];
  } catch { shortcutStorageNote = '无法读取浏览器快捷项，内置模型仍可使用。'; }
}
function saveRuleShortcuts(models) {
  customShortcuts = [...new Set(models.filter(id => !ruleModelShortcuts.includes(id)))];
  try {
    localStorage.setItem(shortcutStorage, JSON.stringify(customShortcuts));
    shortcutStorageNote = '';
  } catch { shortcutStorageNote = '浏览器不允许保存，快捷项仅在本页生效。'; }
  $('rule-shortcut-status').textContent = shortcutStorageNote;
  renderRuleModelControls();
}

const modelGlyph = name => `<svg class="control-icon" viewBox="0 0 24 24" aria-hidden="true"><path d="${{plus:'M12 5v14M5 12h14', check:'m20 6-11 11-5-5', x:'M18 6 6 18M6 6l12 12'}[name]}"/></svg>`;
function modelTokens(models, action, label) {
  return models.map(id => {
    const safe = escapeHTML(id);
    return `<li class="rule-model-token" data-key="id:${safe}"><code>${safe}</code><button type="button" data-${action}="${safe}" aria-label="${label} ${safe}" title="${label} ${safe}">${modelGlyph('x')}</button></li>`;
  }).join('');
}
function renderRuleModelControls() {
  const effective = draftRuleModels(), limit = ruleModelLimit();
  const pending = effective.length - selectedRuleModels.length;
  $('rule-model-count').textContent = `已选 ${selectedRuleModels.length}/${limit}${pending ? ` · 待添加 ${pending}` : ''}`;
  patchView($('rule-model-selection'), modelTokens(selectedRuleModels, 'remove-model', '移除模型'));
  patchView($('rule-model-shortcuts'), [...ruleModelShortcuts, ...customShortcuts].map(id => {
    const safe = escapeHTML(id), selected = effective.includes(id);
    return `<button class="rule-shortcut" data-key="quick:${safe}" type="button" data-add-model="${safe}" aria-pressed="${selected}" ${selected || effective.length >= limit ? 'disabled' : ''}>${modelGlyph(selected ? 'check' : 'plus')}<code>${safe}</code></button>`;
  }).join(''));
  patchView($('rule-custom-shortcuts'), modelTokens(customShortcuts, 'remove-shortcut', '移除快捷项'));
  $('rule-model-add').disabled = !value('rule-models');
  $('rule-shortcut-save').disabled = !value('rule-shortcut-draft');
}

function changeRuleModels(models, consumeDraft = false) {
  selectedRuleModels = models;
  if (consumeDraft) $('rule-models').value = '';
  $('rule-models').dispatchEvent(new Event('input', {bubbles: true}));
}
function addRuleModels(ids, consumeDraft = false) {
  const models = [...new Set([...selectedRuleModels, ...ids])];
  const problem = ruleModelsProblem([...new Set([...models, ...splitRuleModels(value('rule-models'))])]);
  if (problem) { markFieldError('rule-models', problem); return; }
  changeRuleModels(models, consumeDraft);
  $('rule-models').focus({ preventScroll: true });
}

const keepingLegacySchedule = () => !$('rule-legacy-schedule').hidden && !checked('rule-convert-cron');

// Validate before locking the form so the first invalid control can receive focus.
function validateRuleForm(form) {
  const messages = { 'rule-name': '请填写规则名称。', 'rule-target': '请选择要检测的对象。',
    'rule-samples': sampleCountError, 'rule-cron': '请填写五段 Cron 检测周期。',
    'rule-max-results': '保留检测结果数须为 1–200 的整数。' };
  for (const field of form.elements) {
    if (!field.willValidate || (field.validity.valid && (field.id !== 'rule-name' || field.value.trim()))) continue;
    markFieldError(field.id, messages[field.id] || field.validationMessage);
    return false;
  }
  return true;
}

function updateRuleSchedule() {
  const legacy = keepingLegacySchedule(), cron = $('rule-cron');
  $('rule-schedule-fields').hidden = legacy;
  cron.disabled = legacy;
  if (legacy) clearFieldError(cron);
  const rule = state.rules.find(r => r.id === value('rule-id'));
  $('rule-schedule-note').textContent = rule && ruleHeld(rule) ? '凭证暂停归属未解除，检测已停止。'
    : rule && checked('rule-timer') && $('rule-timer').dataset.initial === 'true' && rule.timer_active !== true ? '保留定时设置，当前尚未运行；保存不会重新启动。'
    : checked('rule-timer') ? '保存后从下个周期开始，仅运行本条规则。'
    : rule?.enabled === false ? '原规则已暂停，保存不会恢复检测。' : '关闭时仅手动执行。';
}

let previewRead;
async function updateRulePreview(question) {
  const preview = $('rule-preview'), toggle = $('rule-preview-toggle'), answer = $('rule-preview-answer'), editor = state.editor;
  toggle.hidden = !question || question.kind === 'custom';
  if (toggle.hidden) toggle.setAttribute('aria-expanded', 'false');
  preview.hidden = toggle.getAttribute('aria-expanded') !== 'true';
  const random = question?.kind === 'random';
  $('rule-preview-prompt').textContent = random ? '执行时生成本轮题面，同一范围批次使用同一道题。' : question?.prompt || '未提供题面';
  if (preview.hidden || !editor || question?.kind !== 'logic') {
    previewRead = null;
    answer.textContent = random ? '执行时由本地求解器计算' : '无固定参考答案';
    return;
  }
  if (previewRead?.question === question && previewRead.editor === editor) return;
  const read = previewRead = {question, editor};
  const current = () => previewRead === read && state.editor === editor && !preview.hidden && value('rule-question') === question.id;
  answer.textContent = '读取中…';
  try {
    const record = await api(`/rules/question-preview?id=${encodeURIComponent(question.id)}`);
    if (!current()) return;
    if (record.id !== question.id || record.version !== question.version || record.prompt !== question.prompt || !record.answer) throw Error();
    answer.textContent = record.answer;
  } catch {
    if (current()) answer.textContent = '未取得此版本的参考答案，收起后可重试。';
  }
}

function updateRuleQuestion(all, target) {
  const question = state.questions.find(q => q.id === value('rule-question'));
  const custom = value('rule-question') === 'custom', random = question?.kind === 'random';
  const original = state.rules.find(r => r.id === value('rule-id'));
  const saved = original?.config || original;
  const official = state.settings.capabilities?.mode === 'official_observation';
  const mode = $('rule-judge-mode');
  const llm = mode.querySelector('[value="llm"]'), deterministic = mode.querySelector('[value="deterministic"]');
  const hasOracle = question?.kind === 'logic' || random;
  // An unrelated edit must not rewrite a historical judge or control configuration.
  const sameQuestion = !!saved && saved.question_id === value('rule-question');
  const keepJudge = sameQuestion && saved.judge?.mode === mode.value;
  llm.disabled = all || (official && !(sameQuestion && saved.judge?.mode === 'llm'));
  deterministic.disabled = !!question && !hasOracle && !(keepJudge && mode.value === 'deterministic');
  if ((mode.value === 'llm' && llm.disabled) || (mode.value === 'deterministic' && deterministic.disabled)) mode.value = hasOracle ? 'deterministic' : 'manual';
  const unsupported = official && !all && (mode.value === 'llm' || (random && value('rule-control-target')));
  const note = unsupported ? '原规则依赖独立凭证；官方 CPA 无法核验独立身份，不能执行该配置。'
    : !hasOracle && question ? (mode.value === 'deterministic' ? '此题没有可自动判定的整数答案；原判题设置只会给出「无法判断」。' : '此题没有可自动判定的整数答案。')
    : !all && official && target ? '官方 CPA 无法核验独立凭证身份，不提供独立模型判题。' : '';
  $('rule-judge-note').textContent = note; $('rule-judge-note').hidden = !note;
  void updateRulePreview(question);
  $('rule-prompt-field').hidden = !custom; $('rule-standard-field').hidden = !custom;
  $('rule-control-fields').classList.toggle('hidden', all || !random || (official && !saved?.control_auth_id));
  $('rule-judge-fields').hidden = mode.value !== 'llm';
  for (const id of ['rule-prompt', 'rule-review-standard']) updateQuestionTextValidity($(id), custom);
  if ($('rule-control-fields').classList.contains('hidden')) clearFieldError($('rule-control-target'));
  if ($('rule-judge-fields').hidden) { clearFieldError($('rule-judge-target')); clearFieldError($('rule-judge-model')); }
}

function ruleHandling() { return $('rule-form').elements.namedItem('rule-action').value || 'observe_only'; }

$('rule-upstream-options').innerHTML = Object.entries(upstreamConditions).map(([key, label]) =>
  `<label class="setting-field">${label}<select id="rule-upstream-${key}" aria-describedby="rule-upstream-note">${Object.entries(credentialActions).map(([action, name]) => `<option value="${action}">${name}</option>`).join('')}</select></label>`
).join('');

function upstreamActions() {
  return Object.fromEntries(Object.keys(upstreamConditions).map(key => [key, value(`rule-upstream-${key}`)]));
}

function updateRuleHandling() {
  const action = ruleHandling(), pause = action === 'disable_credential';
  const errorActions = Object.values(upstreamActions());
  const managing = handlesCredential(action) || errorActions.some(handlesCredential);
  const supported = state.settings?.capabilities?.credential_management === true;
  for (const input of $('rule-form').querySelectorAll('[name="rule-action"]')) input.disabled = input.value !== 'observe_only' && !supported;
  for (const option of $('rule-upstream-options').querySelectorAll('option')) option.disabled = handlesCredential(option.value) && !supported;
  $('rule-restore-option').hidden = !pause;
  $('rule-auto-restore').disabled = !pause;
  $('rule-restore-detail').textContent = $('rule-scope-directory').checked
    ? '人工停用或本规则因答错停用的授权对象，本轮全部模型与样本通过且写前条件未变才恢复。他规则的答错归属不转移。'
    : '仅恢复本规则因答错停用的授权对象。本轮全部模型与样本通过且写前条件未变才恢复。';
  $('rule-scope-detail').textContent = $('rule-scope-directory').checked
    ? '全部对象可检测已停用对象；未获本规则恢复授权时仅记录。上游错误停用期间退出所有答题和恢复。'
    : $('rule-scope-all').checked ? '普通检测仅纳入已启用对象。本规则答错停用且开启自动恢复的对象另行复检。'
    : '按指定条目执行；检测资格不授予他规则的恢复归属，也不代替整组启停授权。';
  $('rule-handling-note').textContent = action === 'delete_credential' ? '移除仅适用于已获准处置的文件凭证。整轮可判定且有明确答错时删除。'
    : pause ? '整轮可判定且有明确答错时暂停对象。整组启停还须在本规则中授权。' : '答错时仅记录检测结果。';
  let note = '上游错误独立于答错处理，无标准答案的题型也可使用受支持的探测通道。模型不支持可由已核实的非空 CPA 目录缺项或上游明确的 400/404 确认。普通限流、超时和未知结果只记入运行记录。';
  if (errorActions.includes('disable_credential')) note += '错误确认停用后，该对象的全部模型退出后续运维检测和恢复复检，直到在 CPA 手动启用。';
  if (errorActions.includes('delete_credential')) note += '移除仅适用于允许删除的文件凭证。';
  if (!supported) note += state.resourceStatus.settings === 'error' ? '凭证处置能力读取失败，可刷新重试。' : '当前宿主未提供凭证处置。';
  else if (managing && value('rule-control-target') && !$('rule-control-fields').classList.contains('hidden')) note += '管理探测暂不支持独立对照凭证。';
  if (handlesCredential(action) && value('rule-judge-mode') !== 'deterministic') $('rule-handling-note').textContent += '答错自动处置需要标准答案判定；人工补充评审不自动处置。';
  $('rule-upstream-note').textContent = note;
}

function draftRuleScope() {
  const target_scope = $('rule-form').elements.namedItem('rule-scope').value;
  const object_types = selectedObjectTypes();
  const scope = { object_types, target_scope, authorized_groups: object_types.includes('provider') ? [...ruleGroupGrants].sort() : [] };
  if (!$('rule-legacy-scope').hidden && checked('rule-adopt-scope')) scope.adopt_scope = true;
  if (target_scope === 'single') {
    const option = $('rule-target').selectedOptions[0];
    if (option?.dataset.group) scope.target_group = option.dataset.group;
    else if (option?.dataset.id) {
      scope.auth_id = option.dataset.id;
      scope.provider = option.dataset.provider || '';
      if (option.dataset.index) scope.target_auth_index = option.dataset.index;
      const saved = state.rules.find(rule => rule.id === value('rule-id'));
      const prior = saved?.config || saved;
      const sameBinding = prior?.auth_id === scope.auth_id && prior.provider === scope.provider &&
        (!saved.target_auth_index || saved.target_auth_index === scope.target_auth_index) &&
        (!saved.target_object_type || saved.target_object_type === option.dataset.kind);
      if (!sameBinding && ['oauth', 'provider'].includes(option.dataset.kind)) scope.expected_target_object_type = option.dataset.kind;
    }
  }
  return scope;
}

const scopeNumber = value => Number.isSafeInteger(value) && value >= 0;
function scopePreviewText(data) {
  const pending = data?.reason || '目录待核实，执行时将重新读取。';
  if (!data || ![data.candidates?.oauth, data.candidates?.provider, data.count, data.skipped, data.recovery_count].every(scopeNumber)) return pending;
  if (!data.verified && !data.count && !data.skipped && !data.candidates.oauth && !data.candidates.provider) return pending;
  const candidates = `${data.verified ? '候选对象' : '已核实候选'} · OAuth 账户 ${data.candidates.oauth} 个 · AI 提供商 ${data.candidates.provider} 条路由。`;
  return candidates + `检测 ${data.count - data.recovery_count} 个 · 恢复复检 ${data.recovery_count} 个 · 未执行 ${data.skipped} 个。` + (data.verified ? data.reason || '' : pending);
}

function renderRuleCandidateCount(scope, kinds, sources) {
  const note = $('rule-scope-note');
  if (!state.targetsFresh) {
    if (note.textContent !== '候选对象 · 目录待核实。') note.textContent = '候选对象 · 目录待核实。';
    return;
  }
  const counts = {oauth: 0, provider: 0};
  if (scope !== 'single' || !$('rule-target').selectedOptions[0]?.disabled) {
    for (const source of sources) {
      if (scope === 'all_enabled' && (source.manual_disabled || source.runtime_status === 'disabled')) continue;
      counts[ruleTargetType(source)]++;
    }
  }
  const providers = kinds.includes('provider') && !state.providerDirectoryVerified ? '待核实' : `${counts.provider} 条路由`;
  const message = `候选对象 · OAuth 账户 ${counts.oauth} 个 · AI 提供商 ${providers}。`;
  if (note.textContent !== message) note.textContent = message;
}

function updateRuleFields() {
  if (state.editor?.saving) return;
  const scope = $('rule-form').elements.namedItem('rule-scope').value;
  const all = !!dynamicScopeLabel(scope);
  const keepScope = !$('rule-legacy-scope').hidden && !checked('rule-adopt-scope');
  for (const field of $('rule-form').querySelectorAll('.rule-membership fieldset')) field.disabled = keepScope;
  $('rule-target-field').hidden = all; $('rule-target').required = !all; $('rule-target').disabled = all || keepScope;
  if (all) clearFieldError($('rule-target'));
  const selected = draftRuleModels(), target = selectedRuleTarget(), group = !all && selectedRuleGroup();
  const kinds = selectedObjectTypes();
  const sources = state.targets.filter(t => kinds.includes(ruleTargetType(t)) &&
    (all || (group ? t.provider_group_id === group : t === target) || (target?.provider_group_id === t.provider_group_id && ruleGroupGrants.has(t.provider_group_id))));
  const models = state.targetsFresh ? [...new Set(sources.flatMap(availableModels))].sort() : [];
  updateRuleQuestion(all || !!group, target);
  renderRuleGroupGrants();
  if (keepScope) for (const input of $('rule-group-options').querySelectorAll('input')) input.disabled = true;
  renderRuleCandidateCount(scope, kinds, sources);
  const picker = $('rule-model-catalog');
  const options = `<option value="">${models.length ? '从凭证目录添加' : '凭证目录暂无模型'}</option>` + models.map(m => `<option value="${escapeHTML(m)}" ${selected.includes(m) ? 'disabled' : ''}>${escapeHTML(m)}</option>`).join('');
  patchView(picker, options);
  picker.disabled = !models.length || selected.length >= ruleModelLimit();
  picker.title = !models.length ? '凭证目录暂无模型' : picker.disabled ? '已达检测模型上限' : '从凭证目录添加模型';
  renderRuleModelControls();
  const samples = sampleCount(value('rule-samples'));
  for (const button of $('rule-form').querySelectorAll('[data-sample-step]')) {
    button.disabled = button.dataset.sampleStep === '1' ? samples === Number.MAX_SAFE_INTEGER : samples === 1;
  }
  const byCount = value('rule-retention-mode') === 'count';
  const countField = $('rule-max-results');
  countField.hidden = countField.disabled = !byCount;
  countField.required = byCount;
  const limit = number('rule-max-results'), retention = $('rule-retention-note');
  retention.textContent = byCount && samples && limit > 0 && limit <= 200 && limit < selected.length * samples
    ? `判定后按每个对象最近 ${limit} 份结果保留，受保护的处置证据另行保留。` : '';
  retention.hidden = !retention.textContent;
  updateRuleSchedule();
  updateRuleHandling();
}

function editRule(rule = {}) {
  if (ruleCatalogNotice()) return;
  const config = rule.config || rule;
  const kinds = ruleObjectTypes(rule, state.targets);
  for (const input of $('rule-form').querySelectorAll('[name="rule-object"]')) input.checked = kinds.includes(input.value);
  ruleGroupGrants = new Set(rule.authorized_groups || []);
  $('rule-adopt-scope').checked = false;
  $('rule-legacy-scope').hidden = !(rule.legacy_scope || rule.legacy_provider_observation);
  renderRuleTargetSelectors(rule);
  loadRuleShortcuts();
  selectedRuleModels = rule.id ? [...(config.models || [])] : [ruleModelShortcuts[0]];
  $('rule-models').value = $('rule-shortcut-draft').value = '';
  $('rule-shortcut-panel').hidden = true;
  $('rule-shortcut-toggle').setAttribute('aria-expanded', 'false');
  $('rule-shortcut-status').textContent = shortcutStorageNote;
  $('rule-form').querySelectorAll('[aria-invalid="true"]').forEach(clearFieldError);
  $('rule-form').querySelector('.dialog-message')?.remove();
  $('rule-preview-toggle').setAttribute('aria-expanded', 'false');
  $('rule-dialog-title').textContent = rule.id ? '编辑检测规则' : '新建检测规则';
  const legacy = !!rule.id && (!!rule.timezone || rule.interval_seconds > 0);
  $('rule-legacy-schedule').hidden = !legacy;
  $('rule-original-plan').textContent = !legacy ? '' : rule.cron ? `${rule.cron} · ${rule.timezone || '服务端时区'}` : `每 ${rule.interval_seconds} 秒`;
  $('rule-convert-cron').checked = false;
  const effort = rule.id ? config.parameters?.reasoning_effort || '' : 'high';
  const efforts = $('rule-form').elements.namedItem('rule-effort');
  const legacyEffort = !Array.from(efforts).some(radio => radio.value && radio.value === effort);
  $('rule-legacy-effort').hidden = !legacyEffort;
  $('rule-legacy-effort').querySelector('input').disabled = !legacyEffort;
  $('rule-legacy-effort').querySelector('span').textContent = effort === 'default' || !effort ? '未指定' : effort;
  for (const radio of efforts) radio.checked = radio.value === (legacyEffort ? '' : effort);
  const legacyRetention = !!rule.id && !rule.max_results && rule.retention_days > 0;
  $('rule-retention-mode').querySelector('[value="legacy"]').hidden = !legacyRetention;
  const values = {
    id: rule.id || '',
    revision: rule.revision || '',
    name: rule.name || '',
    question: config.question_id || 'logic-candy',
    samples: config.samples || 1,
    prompt: config.prompt || '',
    'review-standard': config.review_standard || '',
    'judge-mode': config.judge?.mode || 'deterministic',
    'judge-target': config.judge?.auth_id || '',
    'control-target': config.control_auth_id || '',
    'judge-model': config.judge?.model || '',
    cron: rule.cron || $('rule-cron').defaultValue,
    'retention-mode': legacyRetention ? 'legacy' : rule.retention_mode || 'count',
    'max-results': rule.max_results || $('rule-max-results').defaultValue,
  };
  for (const [name, current] of Object.entries(values)) $(`rule-${name}`).value = current;
  $('rule-form').elements.namedItem('rule-scope').value = rule.id ? rule.target_scope || 'single' : 'all_enabled';
  const held = ruleHeld(rule);
  $('rule-form').elements.namedItem('rule-action').value = ['disable_credential','delete_credential'].includes(rule.action) ? rule.action : 'observe_only';
  const defaultErrorAction = !rule.id && state.settings.capabilities?.credential_management === true
    ? 'disable_credential' : 'observe_only';
  for (const key of Object.keys(upstreamConditions)) {
    $(`rule-upstream-${key}`).value = rule.upstream_error_actions?.[key] ?? defaultErrorAction;
  }
  $('rule-auto-restore').checked = rule.auto_restore === true;
  const timer = $('rule-timer');
  timer.checked = ruleScheduleEnabled(rule) === true;
  timer.dataset.initial = String(timer.checked);
  timer.disabled = held || (!!rule.id && typeof ruleScheduleEnabled(rule) !== 'boolean');
  updateRuleFields();
  $('rule-hold-note').classList.toggle('hidden', !held);
  setPage('operations');
  openEditor('rule-dialog');
}

const readRuleCommand = id => api('/rules/commands?idempotency_key=' + encodeURIComponent(id));

function showRuleCommand(slot, id, command) {
  const kind = slot.startsWith('create:') ? 'create' : 'run';
  if (command?.idempotency_key !== id || command.kind !== kind || !Object.hasOwn(commandStatus, command.state)) throw Error('规则操作回执格式无效。');
  const node = receiptLine($('rule-command-note'), id);
  if (node.dataset.finished === 'true' && !commandFinished(command.state)) return true;
  const action = slot.startsWith('create:') ? '创建规则' : '立即执行';
  const code = command.error?.code || command.code || '';
  node.textContent = `${action} · ${commandStatus[command.state]}${code ? ' · ' + code : ''} · ${id}`;
  node.hidden = false;
  node.dataset.finished = String(commandFinished(command.state));
  if (command.state === 'completed') {
    if (slot.startsWith('create:') && state.editor?.commandID === id && state.editor.saveUncertain) closeSurface('rule-dialog', true);
    const rule = state.rules.find(item => item.id === command.rule_id);
    if (rule && command.result && scopeNumber(command.result.count)) showScopeReceipt(rule, command.result);
  }
  return commandFinished(command.state);
}

function ruleReceiptError(slot, id, error) {
  if (isDiscarded(error)) return;
  const node = receiptLine($('rule-command-note'), id);
  if (node.dataset.finished === 'true') return;
  if (notAccepted(error) && state.editor?.commandID === id) {
    state.editor.saveUncertain = false;
    notice('尚未查到本次保存的受理回执。可用原参数和请求标识再次提交。', true);
    renderAdmission();
  }
  const issue = notAccepted(error) ? '尚未查到受理回执' : '回执待读取';
  if (!node.textContent) node.textContent = `${slot.startsWith('create:') ? '创建规则' : '立即执行'} · ${issue} · ${id}`;
  else if (!node.textContent.includes(issue)) node.append(' · ' + issue);
  node.hidden = false;
}

async function submitRuleCommand(slot, send, editor) {
  const epoch = client.epoch;
  const { id, reply } = await ruleCommands.submit(slot, id => {
    if (editor) editor.commandID = id;
    return send(id);
  }, readRuleCommand, () => state.connected && client.epoch === epoch);
  if (!operationsVisible()) return { pending: true };
  if (Object.hasOwn(commandStatus, reply?.state)) {
    if (showRuleCommand(slot, id, reply)) ruleCommands.forget(slot, id);
    return reply.state === 'completed' ? (reply.result || { id: reply.rule_id, revision: reply.revision }) : { pending: true };
  }
  // Older synchronized responses use the rule or batch body directly.
  const valid = slot.startsWith('create:') ? reply?.id && Number.isSafeInteger(reply.revision) && reply.revision > 0 :
    (typeof reply?.run_id === 'string' && reply.run_id || scopeNumber(reply?.count) && scopeNumber(reply.skipped) && (!reply.count || reply.scope_batch_id));
  if (valid) ruleCommands.forget(slot, id);
  return reply;
}

async function saveRule(editor) {
  const config = runConfig('rule-');
  const body = {
    ...config,
    name: value('rule-name'),
    action: ruleHandling(),
    upstream_error_actions: upstreamActions(),
    auto_restore: ruleHandling() === 'disable_credential' && checked('rule-auto-restore'),
    activation: String(checked('rule-timer')) === $('rule-timer').dataset.initial ? 'keep' : checked('rule-timer') ? 'start' : 'stop',
    automatic_pause: false,
    fail_threshold: state.rules.find(r => r.id === value('rule-id'))?.fail_threshold || 3,
  };
  if (handlesCredential(body.action) || Object.values(body.upstream_error_actions).some(handlesCredential)) {
    if (!state.settings?.capabilities?.credential_management) throw Error('当前宿主未提供凭证处置，请刷新后重试。');
    if (handlesCredential(body.action) && config.judge.mode !== 'deterministic') fieldError('rule-judge-mode', '答错后停用或移除凭证需使用标准答案判定；上游错误处理不受此限制。');
    if (config.judge.mode === 'llm') fieldError('rule-judge-mode', '管理探测暂不支持独立模型判题，请使用人工评审或标准答案判定。');
    if (config.control_auth_id) fieldError('rule-control-target', '管理探测需清除对照凭证。');
  }
  const retentionMode = value('rule-retention-mode');
  if (retentionMode !== 'legacy') body.retention_mode = retentionMode;
  if (retentionMode === 'count') {
    const requested = number('rule-max-results');
    if (!Number.isInteger(requested) || requested < 1 || requested > 200) fieldError('rule-max-results', '保留检测结果数须为 1–200 的整数。');
    body.max_results = requested;
  }
  if (!keepingLegacySchedule()) {
    const cron = value('rule-cron').split(/\s+/);
    if (cron.length !== 5 || cron.some(field => /[@=]/.test(field))) fieldError('rule-cron', '请输入五段 Cron：分、时、日、月、星期，不含秒或时区。');
    body.cron = cron.join(' ');
  }
  if (value('rule-id')) Object.assign(body, { id: value('rule-id'), revision: number('rule-revision') });
  try {
    const saved = body.id ? await api('/rules', 'PUT', body) :
      await submitRuleCommand(await creationSlot(body), id => api('/rules', 'POST', { ...body, idempotency_key: id }), editor);
    if (saved?.pending) { editor.saveUncertain = true; return; }
    if (!saved || typeof saved.id !== 'string' || !saved.id || !Number.isSafeInteger(saved.revision) || saved.revision < 1 ||
        body.id && (saved.id !== body.id || saved.revision <= body.revision)) {
      const failure = new Error('保存回执缺少有效规则标识或版本，请核对规则列表。');
      failure.outcomeUnknown = true;
      throw failure;
    }
  }
  catch (error) {
    if (!body.id && !isDiscarded(error) && unknownReceipt(error)) {
      // A failed receipt does not prove creation failed. Retrying this POST
      // could create a second scheduled rule, so preserve the draft for review.
      editor.saveUncertain = true;
      notice(uncertainRuleSaveMessage, true);
      void loadOperations().catch(failure => {
        if (!isDiscarded(failure) && state.editor === editor) notice('保存结果未确认，草稿已保留，已禁止重复保存。规则列表读取失败，请关闭草稿后刷新核对。', true);
      });
      return;
    }
    if (error.code === 'invalid_cron' && !keepingLegacySchedule()) fieldError('rule-cron', '检测周期无效，请核对各段的取值、步长和日期。');
    throw error;
  }
  closeSurface('rule-dialog', true);
  if (state.page === 'operations') notice(`规则“${body.name}”已保存。${body.activation === 'start' ? '将从下个周期开始检测。' : ''}`);
  try { await loadOperations(); }
  catch (error) { if (!isDiscarded(error) && state.page === 'operations') notice('规则已保存，部分目录数据待核实。'); }
}

function releaseFeedback(message, error = false) {
  $('release-feedback').textContent = message;
  $('release-feedback').classList.remove('hidden');
  $('release-feedback').classList.toggle('error', error);
}

async function previewAction(index) {
  const action = state.actions[index];
  if (!action) return;
  const sequence = ++state.reads.preview;
  state.releaseBlocked = true;
  state.releaseAction = action;
  state.release = null;
  $('release-preview').textContent = '正在读取暂停归属与凭证限制…';
  $('release-feedback').classList.add('hidden');
  $('release-refresh').classList.add('hidden');
  $('release-confirm').textContent = '确认解除，保持检测停止';
  renderAdmission();
  $('release-dialog').showModal();
  const release = { rule_id: action.rule_id, action_id: action.action_id || action.id, action_revision: action.action_revision };
  try {
    const preview = await api('/actions/preview', 'POST', release);
    if (sequence !== state.reads.preview) return;
    state.release = release;
    $('release-preview').innerHTML = renderReleasePreview(preview, action);
    state.releaseBlocked = preview.can_release === false || preview.blocked === true || preview.conflict === true;
    renderAdmission();
    return true;
  } catch (error) {
    if (isDiscarded(error) || sequence !== state.reads.preview) return;
    $('release-preview').textContent = '未取得有效解除预览。';
    releaseFeedback(`预览未完成：${error.message}。请刷新并核对当前归属。`, true);
    $('release-refresh').classList.remove('hidden');
  }
  renderAdmission();
  return false;
}

async function reconcileRelease() {
  const sequence = state.reads.preview;
  const action = { ...state.releaseAction };
  const current = () => sequence === state.reads.preview && $('release-dialog').open;
  state.releaseBlocked = true;
  renderAdmission();
  try {
    await loadOperations();
  } catch (error) {
    if (!isDiscarded(error) && current()) releaseFeedback(`状态核对失败：${error.message}。解除结果仍待核对，请再次刷新。`, true);
    renderAdmission();
    return;
  }
  if (!current()) return;
  const id = action.action_id || action.id;
  const index = state.actions.findIndex((action) => (action.action_id || action.id) === id);
  if (index >= 0) {
    const updated = await previewAction(index);
    if (!updated) return;
    releaseFeedback('已重新读取当前动作版本。请核对更新后的预览，再决定是否解除。');
    notice('暂停预览已更新，请核对当前版本。');
  } else {
    const target = findTarget(action.auth_id);
    const reasons = target?.reasons || target?.unavailable_reasons || [];
    const remaining = [target?.manual_disabled ? '人工禁用仍生效' : '', target?.quality_paused ? '仍有质量暂停' : '', ...reasons].filter(Boolean);
    $('release-preview').innerHTML = renderReleasePreview({ action, targets: state.targets }, action);
    releaseFeedback(`宿主当前列表中已无此暂停归属。${remaining.length ? `剩余限制：${remaining.join('；')}。` : target?.effective_available === true ? '宿主当前报告凭证可用。' : '凭证是否可用仍需核对。'}相关检测规则仍停止，恢复检测须另行显式操作。`);
    notice('已核对宿主状态：原暂停归属已不在活动列表；相关检测规则仍停止，请查看剩余限制。');
    $('release-confirm').textContent = '此归属已不在活动列表';
    state.release = null;
    renderAdmission();
  }
}

function exportRun(run) {
  if (!run) return;
  const blob = new Blob([JSON.stringify(run, null, 2)], { type: 'application/json' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = `model-quality-${run.id || run.run_id || 'run'}.json`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

async function dispatchClick(event) {
  let button = event.target.closest('button,[data-run]');
  if (!button && window.getSelection()?.isCollapsed) {
    button = event.target.closest('tr[data-history-id], tr.quality-run-row')?.querySelector('[data-run]');
    if (button?.closest('.quality-run-row')) button.focus({ preventScroll: true });
  }
  if (!button) return;
  if (button.dataset.deleteRun) { confirmHistoryDelete(button.dataset.deleteRun); return; }
  if (button.dataset.close) {
    closeSurface(button.dataset.close);
    if (button.dataset.close === 'release-dialog') { state.reads.preview++; state.releaseBlocked = true; }
    if (button.dataset.close === 'preview-dialog') $('safe-preview').srcdoc = '';
    return;
  }
  const manualView = button.dataset.manualView || button.dataset.manualPane;
  if (manualView) {
    setManualView(manualView, !button.dataset.manualPane);
    if (manualView === 'history' && state.connected) await loadHistory();
    return;
  }
  if (button.id === 'reading-toggle') {
    setReadingExpanded(!state.readingExpanded);
    button.focus({ preventScroll: true });
    return;
  }
  if (button.hasAttribute('data-rule-new')) { protectEditor(() => editRule()); return; }
  if (button.hasAttribute('data-rule-clear')) { $('rule-search').value = ''; $('rule-filter').value = 'all'; renderRules(); $('rule-search').focus(); return; }
  if (button.dataset.ruleTimer) {
    const rule = state.rules.find(item => item.id === button.dataset.ruleTimer);
    if (!rule || button.disabled) return;
    await safely(async () => {
      renderRules();
      try {
        const updated = await api('/rules/timer', 'PUT', { id: rule.id, revision: rule.revision, enabled: !ruleScheduleEnabled(rule) });
        Object.assign(rule, updated);
      } catch (error) {
        if (isDiscarded(error)) throw error;
        await loadOperations().catch(() => {});
        if (error.status === 409) throw new Error('规则状态已变更，请核对列表后重试。');
        throw error;
      }
      await loadOperations();
    }, `rule-timer:${rule.id}`);
    renderRules();
    return;
  }
  if (button.dataset.ruleSelect) {
    state.scopeRule = state.scopeRule === button.dataset.ruleSelect ? '' : button.dataset.ruleSelect;
    const stacked = $('ops-activity').offsetTop > $('ops-rules').offsetTop;
    selectBatch('', 0, !!state.scopeRule && stacked); await loadActivity(); return;
  }
  if (button.dataset.page) {
    setPage(button.dataset.page);
    return;
  }
  if (button.dataset.run) {
    if (button.closest('#page-operations')) return openQualityDetail(button.dataset.run);
    const origin = { id: button.dataset.run, offset: state.historyOffset, top: $('manual-history').scrollTop };
    setPage('manual');
    await loadRun(button.dataset.run, true, origin);
    return;
  }
  if (button.id === 'history-return' && state.historyOrigin) {
    const { id, offset, top } = state.historyOrigin;
    setManualView('history');
    const section = $('manual-history');
    section.scrollTop = top;
    (section.querySelector(`[data-run="${CSS.escape(id)}"]`) || section).focus({ preventScroll: true });
    await loadHistory(offset);
    return;
  }
  if (button.dataset.expandSample != null) {
    const output = button.closest('.sample').querySelector('.answer-text');
    const expanded = output.classList.toggle('expanded');
    button.textContent = expanded ? '收起长回答' : button.dataset.partialOutput === 'true' ? '展开已接收内容' : '展开完整回答';
    button.setAttribute('aria-expanded', String(expanded));
    if (!expanded) button.scrollIntoView({ block: 'nearest' });
    return;
  }
  const detail = button.closest('#quality-detail') ? qualityDetail : null;
  const sourceRun = detail ? detail.run : state.run;
  if (button.hasAttribute('data-sample-offset') || button.hasAttribute('data-sample-jump')) {
    const field = button.closest('.sample-pages').querySelector('[data-sample-ordinal]');
    let offset = Number(button.dataset.sampleOffset);
    if (button.hasAttribute('data-sample-jump')) {
      const ordinal = sampleCount(field.value);
      field.setCustomValidity(ordinal && ordinal <= sourceRun.config.samples ? '' : `请输入 1 至 ${sourceRun.config.samples} 的样本序号。`);
      if (!field.reportValidity()) return;
      offset = ordinal - 1;
    }
    if (detail) {
      detail.sampleOffset = offset;
      patchView($('quality-detail-content'), renderQualityDetail(sourceRun, state.rules, { sampleOffset: offset }));
    } else {
      state.sampleOffset = offset;
      $('sample-filter').value = 'all';
      renderRun();
    }
    return;
  }
  const retainedSample = index => sourceRun.samples[Number(index) - (sourceRun.pruned_samples || 0)];
  if (button.dataset.copySample != null) {
    await navigator.clipboard.writeText(retainedSample(button.dataset.copySample).output);
    localFeedback(button, '已复制原文');
    return;
  }
  if (button.dataset.copyModel != null) {
    await navigator.clipboard.writeText(button.dataset.copyModel);
    localFeedback(button, '已复制 ID');
    return;
  }
  if (button.dataset.previewSample != null || button.dataset.evidenceSample != null) {
    const preview = button.dataset.previewSample != null;
    const sample = retainedSample(preview ? button.dataset.previewSample : button.dataset.evidenceSample);
    const prefix = preview ? 'preview' : 'evidence';
    const dialog = $(prefix + '-dialog'), context = $(prefix + '-context');
    const reading = !detail && sourceRun.source === 'manual_test';
    const partial = preview && isPartialAnswer(sample);
    const ordinal = Number.isInteger(sample.index) && sample.index >= 0 ? `回答 ${sample.index + 1}` : '回答';
    dialog.classList.toggle('answer-reader-dialog', reading);
    $(prefix + '-title').textContent = preview ? (partial ? '部分作品预览' : reading ? '作品预览' : '隔离预览') : (reading ? '执行详情' : '样本条件与证据');
    context.hidden = !reading && !partial;
    context.textContent = [...(reading ? [ordinal, sample.model || sample.request_model] : []), partial ? '当前为已接收的部分内容，作品可能不完整。' : ''].filter(Boolean).join(' · ');
    if (preview) {
      const documentHTML = safePreview(previewSource(sample.output || '')?.source || sample.output || '');
      dialog.showModal();
      $('safe-preview').srcdoc = documentHTML;
    } else {
      const evidenceID = sample.evidence?.auth_id;
      const pinned = evidenceID && evidenceID === sourceRun.config?.auth_id;
      const identity = pinned ? frozenRunIdentity(sourceRun) : { label: evidenceID, supplier: false };
      const label = detail && pinned ? qualityIdentityLabel(identity) : identity.label;
      $('evidence-content').innerHTML = renderSampleEvidence(sample, sourceRun.source === 'manual_test', label, identity.supplier);
      $('evidence-content').scrollTop = 0;
      dialog.showModal();
    }
    return;
  }
  if (button.dataset.reviewSample != null) {
    const id = sourceRun.id || sourceRun.run_id;
    const selection = state.reads.run;
    const current = () => detail ? detail === qualityDetail && detailDialog.open
      : selection === state.reads.run && (state.run?.id || state.run?.run_id) === id;
    const fields = button.closest('.sample-review');
    try {
      await api('/runs/review', 'POST', {
        id, sample_index: Number(button.dataset.reviewSample),
        verdict: fields.querySelector('select').value,
        note: fields.querySelector('input').value.trim(),
      });
    } catch (error) {
      if (current()) throw error;
      return;
    }
    if (!current()) return;
    if (detail) await readQualityDetail(detail);
    else await loadRun(id, false);
    if (button.isConnected) localFeedback(button, '评审已保存');
    return;
  }
  if (button.dataset.ruleEdit) {
    protectEditor(() => editRule(state.rules.find((rule) => rule.id === button.dataset.ruleEdit)));
    return;
  }
  if (button.dataset.ruleRun) {
    const rule = state.rules.find((entry) => entry.id === button.dataset.ruleRun);
    if (!rule) throw new Error('规则当前不可用');
    const config = rule.config || rule;
    if (!rule?.enabled || ruleHeld(rule))
      throw new Error('检测已停止。请先人工解除凭证暂停，再显式恢复检测规则。');
    const run = () => submitRuleCommand('run:' + rule.id + ':' + rule.revision, key => api('/rules/run', 'POST', { id: rule.id, revision: rule.revision, idempotency_key: key }));
    if (dynamicScopeLabel(rule.target_scope) || rule.target_group || rule.authorized_groups?.length) {
      const epoch = client.epoch;
      const draft = {id:rule.id,revision:rule.revision,object_types:rule.object_types,target_scope:rule.target_scope,
        auth_id:config.auth_id,provider:config.provider,target_auth_index:rule.target_auth_index,target_group:rule.target_group,
        authorized_groups:rule.authorized_groups,models:config.models,samples:config.samples,
        parameters:config.parameters?.reasoning_effort || !ruleObjectTypes(rule,state.targets).includes('provider') ? config.parameters : {...config.parameters,reasoning_effort:'default'},
        action:rule.action,auto_restore:rule.auto_restore,upstream_error_actions:rule.upstream_error_actions,
        question_id:config.question_id,judge:config.judge,control_auth_id:config.control_auth_id,control_provider:config.control_provider};
      let preview;
      try { preview = await api('/rules/scope-preview','POST',draft); }
      catch (error) { if (isDiscarded(error)) return; }
      if (client.epoch !== epoch || state.page !== 'operations') return;
      const directory = scopePreviewText(preview);
      const upstream = configuredUpstreamActions(rule.upstream_error_actions);
      const managed = handlesCredential(rule.action) || upstream.length > 0;
      const configured = [
        ...(handlesCredential(rule.action) ? [`整轮可判且有答错时${credentialActions[rule.action]}`] : []),
        ...upstream.map(([condition, action]) => `${condition}时${action}`),
      ];
      const samples = preview?.verified && scopeNumber(preview.count) && Number.isSafeInteger(config.samples) && config.samples > 0
        ? BigInt(preview.count) * BigInt((config.models || []).length) * BigInt(config.samples) : null;
      const summary = managed ? `本规则已配置${configured.join('，')}。` : samples == null ? '' : `最多计划 ${samples} 份目标回答。`;
      actionDialog({ title: '执行范围检测？', description: directory + summary + '开始时将按当前目录重新匹配。', confirm: '开始检测', submit: async () => {
        let receipt;
        try {
          receipt = await run();
          if (receipt?.pending) { scheduleOperationsPoll(1000); return; }
          if (!Number.isSafeInteger(receipt?.count) || receipt.count < 0 || !Number.isSafeInteger(receipt.skipped) || receipt.skipped < 0 || (receipt.count > 0 && !receipt.scope_batch_id)) throw Error('invalid receipt');
        } catch (error) {
          if (isDiscarded(error) || (error.status >= 400 && error.status < 500 && !error.outcomeUnknown)) throw error;
          // Do not leave a retry button when the server may already have queued a batch.
          state.scopeRule = rule.id;
          selectBatch('');
          await loadOperations().catch(() => {});
          if (epoch !== client.epoch || state.page !== 'operations') return;
          notice('提交结果未确认。请先核对规则运行记录，不要重复执行。', true);
          return;
        }
        if (receipt.scope_batch_id) selectBatch(receipt.scope_batch_id);
        showScopeReceipt(rule, receipt);
        try { await loadOperations(); }
        catch (error) { if (isDiscarded(error)) throw error; notice('已收到手动执行回执，部分列表读取失败，请刷新。', true, 'activity-read-note'); }
      } }); return;
    }
    const reply = await run();
    if (reply?.pending) { scheduleOperationsPoll(1000); return; }
    if (scopeNumber(reply.count)) showScopeReceipt(rule, reply);
    if (reply.run_id || reply.id) await Promise.all([openQualityDetail(reply.run_id || reply.id), loadActivity()]);
    else await loadOperations();
    return;
  }
  if (button.dataset.ruleBatch) {
    selectBatch(button.dataset.ruleBatch); await loadActivity(); return;
  }
  if (button.dataset.ruleDelete) {
    const rule = state.rules.find((entry) => entry.id === button.dataset.ruleDelete);
    if (!rule) return;
    actionDialog({ title: '删除检测规则', description: `将删除“${rule.name || rule.id}”及可清理的运行记录和日志。CPA 凭证状态不变；仍有效的上游错误停用来源和未确认处置继续保留。此操作无法撤销。`, confirm: '删除规则', danger: true, submit: async () => {
      const epoch = client.epoch;
      const receipt = await cleanupRequest('/rules', { id: rule.id, revision: rule.revision });
      const selected = state.scopeRule === rule.id || (state.scopeBatch && (state.scopeBatch === rule.last_scope_batch || state.activityRows?.some(row => (row.rule_id || row.config?.rule_id) === rule.id)));
      if (receipt) state.rules = state.rules.filter(entry => entry.id !== rule.id);
      if (receipt && selected) {
        state.scopeRule = '';
        selectBatch('', 0, false);
      }
      if (!await refreshCleanupLists(epoch)) return;
      const listed = state.resourceStatus.rules === 'ready' ? state.rules.some(entry => entry.id === rule.id) : null;
      if (!receipt && listed === false && selected) {
        state.scopeRule = '';
        selectBatch('', 0, false);
        try { await loadActivity(); } catch (error) { if (isDiscarded(error)) throw error; }
      }
      if (epoch !== client.epoch || state.page !== 'operations') return;
      if (receipt || listed === false) focusAfterCleanup('ops-all-rules');
      const current = state.resourceStatus.rules === 'ready' && state.resourceStatus.activity === 'ready';
      const message = receipt ? `规则“${rule.name || rule.id}”已删除。` : `删除规则的回执未确认。${listed === null ? '' : listed ? '当前规则仍在列表中。' : '当前列表中已无此规则。'}`;
      notice(message + (current ? '' : '列表读取失败，请刷新核对。'), !receipt || !current, 'ops-all-rules');
      if (receipt && !current) $('notice').dataset.ruleDeleteReceipt = message;
    } });
    return;
  }
  if (button.dataset.handlingAck) {
    const ruleID = button.dataset.handlingRule;
    const handling = ruleID ? state.rules.find(r => r.id === ruleID)?.handling : state.orphanedHandling;
    const record = handling?.find(h => h.action_id === button.dataset.handlingAck && h.auth_id === button.dataset.handlingAuth);
    if (!record) throw new Error('处置记录已变化，请刷新。');
    actionDialog({title: '核对未确认处置', description: '重新读取 CPA 当前状态，核对这次结果未确认的处置。只有确认已启用或已移除时才结束旧关联；不会修改 CPA 凭证或规则启停。普通已确认停用在 CPA 手动启用后会自动同步。', confirm: '核对当前状态', submit: async () => {
      const scope = feedbackScope();
      await api('/rules/handling/acknowledge', 'POST', {rule_id: ruleID || '', auth_id: record.auth_id, action_id: record.action_id});
      await loadOperations();
      if (scope === feedbackScope()) notice('处置结果已核对。');
    }});
    return;
  }
  if (button.dataset.actionPreview != null) await previewAction(Number(button.dataset.actionPreview));
}

document.addEventListener('click', (event) => {
  const button = event.target.closest(
    '[data-rule-run],[data-rule-batch],[data-rule-delete],[data-handling-ack],[data-action-preview],[data-review-sample]',
  );
  if (button?.disabled) return;
  safely(() => dispatchClick(event), button ? `action:${JSON.stringify(button.dataset)}` : '', button);
});
document.addEventListener('keydown', (event) => {
  if (['Enter', ' '].includes(event.key) && event.target.matches('[data-run]:not(button)')) { event.preventDefault(); event.target.click(); }
});
$('connection-top').addEventListener('click', showConnectionDialog);
$('connection-open').addEventListener('click', () => {
  showConnectionDialog();
  $('management-key').focus();
});
$('connection-dialog').addEventListener('close', () => {
  $('management-key').value = '';
  editorRevision = null;
});
$('connection-form').addEventListener('submit', event => {
  event.preventDefault();
  const key = value('management-key');
  const action = state.connected
    ? () => changeBackend(pageAccess, 'PUT', editorRevision, 'configure', key)
    : () => connectManagement(key);
  safely(action, 'connect', event.submitter);
});
$('connection-clear').addEventListener('click', () => {
  const revision = editorRevision, access = pageAccess;
  if (!access || !Number.isSafeInteger(revision)) return;
  actionDialog({ title: '清除后台管理配置？', description: '依赖管理接口的定时检测和处置将等待重新配置。浏览器访问和账号巡检继续按各自许可运行。', confirm: '清除后台配置', danger: true,
    submit: () => changeBackend(access, 'DELETE', revision) });
});
$('disconnect').addEventListener('click', () => disconnect());
$('refresh').addEventListener('click', () => safely(state.page === 'login' ? loginWorkbench.refresh : refresh, 'refresh', $('refresh')));
$('target').addEventListener('change', () => {
  const option = $('target').selectedOptions[0], target = findTarget(option?.dataset.id);
  Object.assign(manualTargetDraft, { id: target ? targetID(target) : '', provider: target?.provider || '', authIndex: target?.auth_index || '',
    isProvider: !!target?.is_provider, label: option?.textContent || '' });
  renderTarget(true);
});
$('models').addEventListener('change', () => {
  manualTargetDraft.models.set(manualModelKey(), value('models'));
  renderTarget(true);
});
$('reasoning').addEventListener('change', event => {
  manualReasoning = event.currentTarget.value;
  clearFieldError($('reasoning'));
  renderReasoning(true); renderAdmission();
});
$('samples').addEventListener('input', renderAdmission);
$('question').addEventListener('change', renderQuestion);
for (const input of $('rule-form').querySelectorAll('[name="rule-object"]')) input.addEventListener('change', () => {
  const kinds = selectedObjectTypes(), option = $('rule-target').selectedOptions[0];
  if (!kinds.includes('provider')) ruleGroupGrants.clear();
  if ((option?.dataset.group && !kinds.includes('provider')) || (option?.dataset.kind && !kinds.includes(option.dataset.kind))) $('rule-target').value = '';
  clearFieldError($('rule-object-oauth'));
  renderRuleTargetSelectors();
  updateRuleFields();
});
$('rule-group-options').addEventListener('change', event => {
  const input = event.target.closest('input[data-group-grant]');
  if (!input) return;
  if (input.checked) ruleGroupGrants.add(input.dataset.groupGrant);
  else ruleGroupGrants.delete(input.dataset.groupGrant);
  updateRuleFields();
  updateEditorDirty();
});
$('rule-adopt-scope').addEventListener('change', () => {
  const saved = state.rules.find(rule => rule.id === value('rule-id'));
  if (saved && !checked('rule-adopt-scope')) {
    const kinds = ruleObjectTypes(saved, state.targets);
    for (const input of $('rule-form').querySelectorAll('[name="rule-object"]')) input.checked = kinds.includes(input.value);
    $('rule-form').elements.namedItem('rule-scope').value = saved.target_scope || 'single';
    ruleGroupGrants = new Set(saved.authorized_groups || []);
    renderRuleTargetSelectors(saved);
  }
  updateRuleFields();
});
for (const input of $('rule-form').querySelectorAll('[name="rule-action"]')) input.addEventListener('change', updateRuleFields);
$('rule-upstream-options').addEventListener('change', updateRuleFields);
for (const id of ['rule-target','rule-question','rule-scope-all','rule-scope-directory','rule-scope-single','rule-auto-restore','rule-judge-mode','rule-judge-target','rule-judge-model','rule-models','rule-samples','rule-control-target','rule-max-results','rule-retention-mode']) $(id).addEventListener('input', updateRuleFields);
for (const id of ['rule-timer','rule-convert-cron','rule-cron']) $(id).addEventListener('input', updateRuleSchedule);
for (const button of $('rule-form').querySelectorAll('[data-sample-step]')) {
  button.addEventListener('click', () => {
    const field = $('rule-samples'), count = sampleCount(field.value);
    field.value = count === null ? 1 : Math.min(Number.MAX_SAFE_INTEGER, Math.max(1, count + Number(button.dataset.sampleStep)));
    field.dispatchEvent(new Event('input', { bubbles: true }));
    if (button.disabled) field.focus({ preventScroll: true });
  });
}
$('rule-preview-toggle').addEventListener('click', ({currentTarget: toggle}) => {
  toggle.setAttribute('aria-expanded', String(toggle.getAttribute('aria-expanded') !== 'true'));
  void updateRulePreview(state.questions.find(q => q.id === value('rule-question')));
});
$('rule-model-catalog').addEventListener('change', () => {
  const model = value('rule-model-catalog');
  if (!model) return;
  $('rule-model-catalog').value = '';
  addRuleModels([model]);
});
function commitModelInput() {
  const ids = splitRuleModels(value('rule-models'));
  if (ids.length) addRuleModels(ids, true);
}
$('rule-model-add').addEventListener('click', commitModelInput);
$('rule-models').addEventListener('keydown', event => {
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) { event.preventDefault(); commitModelInput(); }
});
$('rule-model-shortcuts').addEventListener('click', event => {
  const button = event.target.closest('[data-add-model]');
  if (button && !button.disabled) addRuleModels([button.dataset.addModel]);
});
$('rule-model-selection').addEventListener('click', event => {
  const button = event.target.closest('[data-remove-model]');
  if (!button) return;
  const index = selectedRuleModels.indexOf(button.dataset.removeModel);
  changeRuleModels(selectedRuleModels.filter(id => id !== button.dataset.removeModel));
  const buttons = $('rule-model-selection').querySelectorAll('button');
  (buttons[index] || buttons[index - 1] || $('rule-models')).focus({preventScroll:true});
});
$('rule-shortcut-toggle').addEventListener('click', () => {
  const panel = $('rule-shortcut-panel');
  panel.hidden = !panel.hidden;
  $('rule-shortcut-toggle').setAttribute('aria-expanded', String(!panel.hidden));
  if (!panel.hidden) $('rule-shortcut-draft').focus();
});
$('rule-shortcut-draft').addEventListener('input', () => { $('rule-shortcut-save').disabled = !value('rule-shortcut-draft'); });
$('rule-shortcut-save').addEventListener('click', () => {
  const ids = splitRuleModels(value('rule-shortcut-draft'));
  if (!ids.length || !ids.every(validShortcut)) {
    $('rule-shortcut-status').textContent = '请输入有效模型 ID，每项最多 100 个字符、256 字节。';
    $('rule-shortcut-draft').focus(); return;
  }
  $('rule-shortcut-draft').value = '';
  saveRuleShortcuts([...customShortcuts, ...ids]);
  if (!shortcutStorageNote) $('rule-shortcut-status').textContent = '快捷项已保存。';
  $('rule-shortcut-draft').focus();
});
$('rule-custom-shortcuts').addEventListener('click', event => {
  const button = event.target.closest('[data-remove-shortcut]');
  if (!button) return;
  saveRuleShortcuts(customShortcuts.filter(id => id !== button.dataset.removeShortcut));
  $('rule-shortcut-draft').focus();
});
$('run-form').addEventListener('submit', (event) => {
  event.preventDefault();
  safely(() => {
    const config = runConfig(), target = selectedManualTarget();
    const binding = target?.is_provider ? { ...config, provider_name: target.provider_name || target.provider, subject_auth_index: target.auth_index } : config;
    return submitRun(config, binding);
  }, 'submit-run');
});
$('run-cancel').addEventListener('click', () =>
  safely(async () => {
    const id = state.run.id || state.run.run_id;
    const scope = feedbackScope();
    await api('/runs/cancel', 'POST', { id });
    if (id === (state.run?.id || state.run?.run_id)) {
      if (scope === feedbackScope()) notice('已请求停止，后台正在核对执行进度。');
      await loadRun(id, false);
    }
    await loadHistory();
  }, 'cancel-run', $('run-cancel')),
);
$('run-rerun').addEventListener('click', () =>
  safely(
    async () => {
      const binding = state.run.config;
      await submitRun(copyRunConfig(binding), binding);
    },
    'submit-run',
  ),
);
$('run-export').addEventListener('click', () => exportRun(state.run));
$('history-clear-all').addEventListener('click', confirmClearManualHistory);
$('history-prev').addEventListener('click', () =>
  safely(() => loadHistory(Math.max(0, state.historyOffset - 10))),
);
$('history-next').addEventListener('click', () =>
  safely(() => loadHistory(state.historyOffset + 10)),
);
$('rule-new').addEventListener('click', () => protectEditor(() => editRule()));
$('rule-form').addEventListener('submit', (event) => {
  event.preventDefault();
  if (!validateRuleForm(event.currentTarget)) return;
  safely(() => saveEditor(saveRule), 'save-rule', event.submitter);
});
$('release-confirm').addEventListener('click', () =>
  safely(async () => {
    if (state.releaseBlocked || !state.release) return;
    const binding = { ...state.release };
    const sequence = state.reads.preview;
    const current = () => sequence === state.reads.preview && $('release-dialog').open;
    state.releaseBlocked = true;
    releaseFeedback('正在提交人工解除。请等待宿主确认…');
    try {
      await api('/actions/release', 'POST', { ...binding, pause_rule: true });
      if (!current()) { await loadOperations(); return; }
      releaseFeedback('宿主已确认释放此暂停归属。正在核对剩余限制…');
      $('release-confirm').textContent = '暂停归属已释放';
      await reconcileRelease();
    } catch (error) {
      if (isDiscarded(error) || !current()) return;
      const conflict = error.status === 409;
      releaseFeedback(conflict ? `动作版本或归属冲突：${error.message}。当前预览已失效，请刷新后重新核对。` : `解除结果尚未确认：${error.message}。宿主可能已执行解除；请刷新核对，不要重复提交。`, true);
      notice(conflict ? '解除发生版本冲突，请在预览中刷新核对。' : '解除结果尚未确认，请在预览中刷新核对。', true);
    } finally {
      if (current()) $('release-refresh').classList.remove('hidden');
      renderAdmission();
    }
  }, 'release', $('release-confirm')),
);
$('release-refresh').addEventListener('click', () => safely(reconcileRelease, 'release-refresh', $('release-refresh')));
function clearScopeReceipt() {
  state.receiptBatch = '';
  $('ops-dispatch-receipt').hidden = true;
  $('ops-dispatch-receipt').replaceChildren();
}

function showScopeReceipt(rule, receipt) {
  clearScopeReceipt();
  const panel = $('ops-dispatch-receipt');
  const title = `手动执行回执 · ${rule.name || rule.id}`;
  const dispatch = { matched: receipt.count, skipped: receipt.skipped, details: receipt.skip_details, skipReasons: receipt.skip_reasons, reason:receipt.reason,
    preflight_details:receipt.preflight_details, recovery_count: receipt.recovery_count, candidates:receipt.candidates };
  state.receiptBatch = receipt.scope_batch_id || '';
  patchView(panel, renderScopeDispatch(dispatch, title, rule.id, { receipt: true, expanded: true }));
  const details = panel.querySelector('details');
  if (details) details.open = true;
  panel.hidden = false;
  $('local-feedback').textContent = `${title}。检测 ${receipt.count - (receipt.recovery_count || 0)} 个，恢复复检 ${receipt.recovery_count || 0} 个，未执行 ${receipt.skipped} 个。`;
}

function selectBatch(batch, offset = 0, focus = true) {
  if (!batch || batch !== state.receiptBatch) clearScopeReceipt();
  state.scopeBatch = batch; state.batchOffset = offset;
  state.activityRows = null;
  if (batch) state.scopeRule = '';
  $('ops-batch-bar').hidden = !batch;
  $('ops-batch-summary').textContent = '正在读取记录…';
  $('ops-batch-prev').disabled = $('ops-batch-next').disabled = true;
  state.resourceStatus.activity = 'loading'; patchView($('operations-history'), ''); renderActivityState(); renderRules();
  if (focus && !$('action-dialog').open) {
    $('ops-activity').focus({ preventScroll: true });
    $('ops-activity').scrollIntoView({ block: 'nearest', behavior: 'instant' });
  }
}
for (const [id, shift] of [['ops-batch-prev',-20],['ops-batch-next',20],['ops-batch-clear',0]]) $(id).addEventListener('click', () => {
  selectBatch(shift ? state.scopeBatch : '', shift ? Math.max(0, state.batchOffset + shift) : 0, false);
  safely(loadActivity, 'batch-page');
});
$('ops-history-clear').addEventListener('click', () => actionDialog({
  title: '清空所有规则的运行记录？',
  description: (state.scopeBatch ? '当前显示单个批次。' : state.scopeRule ? `当前显示规则“${state.rules.find(rule => rule.id === state.scopeRule)?.name || state.scopeRule}”。` : '') +
    '将清空所有规则的已结束记录，包括已确认的错误暂停记录。已确认的暂停继续生效，处置依据另行留档。仍在执行、处置待核对或用于历史独立暂停的记录会继续显示。',
  confirm: '清空记录', danger: true,
  submit: async () => {
    const epoch = client.epoch;
    const result = await cleanupRequest('/rules/run-history');
    state.scopeRule = '';
    selectBatch('', 0, false);
    if (!await refreshCleanupLists(epoch) || state.page !== 'operations') return;
    focusAfterCleanup('ops-activity');
    const current = state.resourceStatus.activity === 'ready';
    let message = '清空回执未确认，清空条数未知。';
    if (result) {
      const archived = result.archived ?? 0;
      const handling = result.kept_for_handling ?? 0;
      const cleared = result.deleted + archived;
      const active = result.kept - handling;
      message = cleared ? `已清空 ${cleared} 条运行记录。` : '当前没有可清空的运行记录。';
      if (archived) message += `其中 ${archived} 条的处置依据已留档。`;
      if (active) message += `还有 ${active} 条任务仍在进行。`;
      if (handling) message += `保留 ${handling} 条待核对处置或历史独立暂停所需的记录。`;
    }
    notice(message + (current ? (result ? '' : '已重新读取全部规则记录。') : '记录读取失败，请刷新核对。'), !result || !current, 'activity-read-note');
  },
}));
for (const id of ['ops-scope-clear', 'ops-all-rules']) $(id).addEventListener('click', () => { state.scopeRule = ''; selectBatch('', 0, false); safely(loadActivity, 'activity-scope'); });
$('rule-search').addEventListener('input', renderRules);
$('ops-rule-context').addEventListener('toggle', event => {
  if (event.target.matches('details[data-skip-rule], details[data-rule-handling]')) renderRuleContext();
}, true);
$('ops-orphaned-handling').addEventListener('toggle', event => {
  if (event.target.matches('details[data-orphaned-handling]')) renderRuleContext();
}, true);
$('rule-filter').addEventListener('change', renderRules);
window.addEventListener('beforeunload', (event) => {
  if (editorDirty()) { event.preventDefault(); event.returnValue = ''; }
});
window.addEventListener('storage', event => {
  browserAuth.storageEvent(event.key, syncStoredAccess, () => {
    if (pageAccess && pageDisplayed()) void readConnection(pageAccess);
  });
});
window.addEventListener('pagehide', event => {
  pageSuspended = true;
  clearTimeout(connectionTimer);
  clearTimeout(state.poll);
  clearTimeout(operationsPoll);
  clearTimeout(qualityDetail?.timer);
  loginWorkbench.syncVisibility?.();
  if (!event.persisted) {
    pageAccess = null;
    client.clear();
    candidateClient?.clear();
    stopThemeFollowing();
  }
});
window.addEventListener('pageshow', event => {
  if (!event.persisted) return;
  pageSuspended = false;
  syncStoredAccess();
  syncOperationsVisibility();
});
document.body.classList.toggle('embedded', window.self !== window.top);
if (window.self !== window.top) {
  const visibility = new IntersectionObserver(([entry]) => {
    if (operationsFrameVisible !== entry.isIntersecting) {
      operationsFrameVisible = entry.isIntersecting;
      syncOperationsVisibility();
    }
  });
  visibility.observe(document.documentElement);
}
document.addEventListener('visibilitychange', syncOperationsVisibility);
window.addEventListener('online', syncOperationsVisibility);
window.addEventListener('offline', syncOperationsVisibility);
$('connection-build').textContent = `UI ${JSON.parse($('build-info').textContent).ui_version}`;
document.querySelector('.skip-link').addEventListener('click', event => {
  event.preventDefault();
  $('main').focus();
  $('main').scrollIntoView({ block: 'start' });
});
setPage(initialWorkspace());
setManualView('setup');
setReadingExpanded(false);
initializeTheme();
$('theme-toggle').addEventListener('change', () => {
  if (document.body.classList.contains('host-themed')) return;
  const selected = value('theme-toggle');
  if (!['light', 'white', 'dark'].includes(selected)) return;
  localTheme = selected;
  document.documentElement.dataset.theme = selected;
});
$('action-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const request = actionRequest;
  if (!request || request.cleanupSent || state.busy.has('dialog-action')) return;
  if (request.input && !value('action-name')) {
    $('action-error').textContent = '请输入预设名称。';
    $('action-error').classList.remove('hidden');
    $('action-name').setAttribute('aria-invalid', 'true');
    $('action-name').focus();
    return;
  }
  state.busy.add('dialog-action');
  const button = $('action-confirm');
  const label = button.textContent;
  button.style.minWidth = `${button.getBoundingClientRect().width}px`;
  button.textContent = request.input ? '保存中…' : '处理中…';
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  try {
    await request.submit(value('action-name'));
    if (actionRequest === request && request.epoch === client.epoch) $('action-dialog').close();
  } catch (error) {
    if (!isDiscarded(error) && actionRequest === request && request.epoch === client.epoch) {
      if (request.cleanupSent) {
        $('action-dialog').close();
        notice('清理请求已发送，读取结果失败。请刷新核对。', true);
        return;
      }
      $('action-error').textContent = error.message || '操作失败，请重试。';
      $('action-error').classList.remove('hidden');
      $('action-error').focus();
    }
  } finally {
    state.busy.delete('dialog-action');
    button.disabled = false;
    button.removeAttribute('aria-busy');
    button.style.removeProperty('min-width');
    button.textContent = label;
  }
});
$('action-dialog').addEventListener('cancel', (event) => { if (state.busy.has('dialog-action')) event.preventDefault(); });
$('action-dialog').addEventListener('close', () => { if (!$('action-dialog').open) { actionRequest = null; $('action-name').value = ''; } });
function clearFieldError(field) {
  if (field.id && field.hasAttribute('aria-invalid')) {
    field.removeAttribute('aria-invalid');
    const errorID = `${field.id}-error`;
    $(errorID)?.remove();
    field.setAttribute('aria-describedby', (field.getAttribute('aria-describedby') || '').split(' ').filter((id) => id !== errorID).join(' '));
  }
}
document.addEventListener('input', (event) => {
  const field = event.target;
  if (field.hasAttribute('data-sample-ordinal')) field.setCustomValidity('');
  if (field.id === 'samples' || field.id === 'rule-samples') {
    if (sampleCount(field.value) === null) markFieldError(field.id, sampleCountError, false);
    else clearFieldError(field);
  } else if (field.id === 'prompt') updateQuestionNote();
  else if (field.id === 'rule-prompt' || field.id === 'rule-review-standard') updateQuestionTextValidity(field, value('rule-question') === 'custom');
  else clearFieldError(field);
  if (event.target.closest('#rule-dialog')) updateEditorDirty();
});
document.addEventListener('keydown', event => {
  if (event.key === 'Enter' && event.target.matches?.('[data-sample-ordinal]')) {
    event.preventDefault();
    event.target.closest('.sample-pages').querySelector('[data-sample-jump]').click();
  }
});
document.addEventListener('change', (event) => { if (event.target.closest('#rule-dialog')) updateEditorDirty(); });
for (const id of ['compare-model-a', 'compare-model-b', 'sample-filter']) $(id).addEventListener('change', () => {
  if (value('compare-model-a') === value('compare-model-b')) $('compare-model-b').value = '';
  applyComparison();
});
$('preview-dialog').addEventListener('close', () => { $('safe-preview').srcdoc = ''; });
$('release-dialog').addEventListener('close', () => { state.reads.preview++; state.releaseBlocked = true; });
window.addEventListener('hashchange', () => setPage(location.hash.slice(1)));
window.addEventListener('resize', scheduleWorkspaceLayout);
const workspaceLayoutObserver = new ResizeObserver(scheduleWorkspaceLayout);
for (const selector of ['.app-header', '.primary-nav', '.quality-heading', '.quality-overview', '.manual-pane-switch', '#notice']) workspaceLayoutObserver.observe(document.querySelector(selector));
renderMetrics();
renderRules();
renderActivityState();
renderAdmission();

const phoneLayout = matchMedia('(max-width: 600px)');
const adaptTaskActions = () => { $('task-actions').open = !phoneLayout.matches; };
phoneLayout.addEventListener('change', adaptTaskActions);
adaptTaskActions();
restoreManagementConnection();
$('task-actions').addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && phoneLayout.matches) {
    $('task-actions').open = false;
    $('task-actions').querySelector('summary').focus();
  }
});
document.addEventListener('click', (event) => {
  if (phoneLayout.matches && !event.target.closest('#task-actions')) $('task-actions').open = false;
});
