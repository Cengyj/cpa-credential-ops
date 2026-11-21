import { escapeHTML } from './markup.js';
import { isDiscarded } from './client.js';

export function createConversion({ connected, current, epochNow, write, submitBatch, newID, planLabel, reasons, canOpen, beforeOpen, afterUpload }) {
  const $ = id => document.getElementById(id);
  const convertEl = name => $('account-convert-' + name);
  const convertLimit = 2 * 1024 * 1024;
  const conversion = { items: [], source: '', sourceRef: '', retrySlot: '', fileName: '', busy: '', sequence: 0 };
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
    const selected = items.filter(item => conversion.retrySlot ? item.batchSlot === conversion.retrySlot : item.selected && canSelectConversion(item)).length;
    for (const id of ['source', 'file', 'file-open', 'edit', 'close', 'cancel']) convertEl(id).disabled = uploading;
    convertEl('preview').disabled = !!busy || !connected() || !convertEl('source').value.trim();
    convertEl('upload').disabled = !!busy || !connected() || !selected;
    convertEl('preview').textContent = busy === 'preview' ? '正在转换…' : '预览转换结果';
    convertEl('upload').textContent = uploading ? '正在上传…' : conversion.retrySlot ? '用原请求标识重提' : selected ? `上传 ${selected} 个凭证` : '上传到 CPA';
    convertEl('count').textContent = `已选 ${selected} 个`;
    convertEl('count').hidden = !items.length;
    convertEl('file-name').textContent = fileName;
    convertEl('file-name').hidden = !fileName;
    convertEl('dialog').setAttribute('aria-busy', String(!!busy));
    for (const el of convertEl('list').querySelectorAll('[data-convert-select]')) {
      el.disabled = !!busy || !!conversion.retrySlot || !canSelectConversion(items[Number(el.dataset.convertSelect)]);
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
    const labels = { pending: '已受理', ready: '可上传', uploaded: '已上传', existing: '已存在', invalid: '无法转换', failed: '未上传', uncertain: '待核对' };
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
    Object.assign(conversion, { items: [], source: '', sourceRef: '', retrySlot: '', fileName: '', busy: '' });
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
    if (!canOpen()) return;
    beforeOpen();
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
      conversion.sourceRef = result.source_ref || newID();
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
    void previewConversion(convertEl('source').value.trim(), ++conversion.sequence, epochNow());
  });
  async function importConversionFile(files) {
    if (conversion.busy === 'upload' || !connected() || !convertEl('dialog').open || conversion.items.length) return;
    resetConversion();
    if (files.length !== 1) { conversionFeedback('请每次选择一个 JSON 文件，同一文件可包含多个凭证。', 'error'); return; }
    const [file] = files;
    conversion.fileName = file.name;
    updateConversionControls();
    if (file.size > convertLimit) { conversionFeedback('文件超过 2 MiB，请分批导入。', 'error'); return; }
    const sequence = conversion.sequence, epoch = epochNow();
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
  function applyResults(selected, result) {
    const unknown = { status: 'uncertain', code: 'commit_unknown', selected: false };
    for (const item of selected) {
      const matches = Array.isArray(result?.items) ? result.items.filter(row => row?.index === item.index) : [];
      const saved = matches[0];
      if (matches.length !== 1 || !['uploaded', 'existing', 'failed', 'uncertain'].includes(saved.status) || (saved.status === 'uploaded' && typeof saved.enabled !== 'boolean')) Object.assign(item, unknown);
      else Object.assign(item, { status: saved.status, code: saved.code || (saved.status === 'uncertain' ? 'commit_unknown' : ''), enabled: saved.enabled, selected: false });
    }
    renderConversion();
    if (selected.some(item => item.status === 'uncertain')) conversionFeedback('部分上传结果尚未确认，请在 CPA 凭证列表核对。', 'error');
  }
  convertEl('upload').addEventListener('click', async () => {
    const selected = conversion.items.filter(item => conversion.retrySlot ? item.batchSlot === conversion.retrySlot : item.selected && canSelectConversion(item));
    if (conversion.busy || !selected.length || !connected()) return;
    const sequence = ++conversion.sequence, epoch = epochNow();
    conversion.busy = 'upload'; conversionFeedback(); updateConversionControls();
    const unknown = { status: 'uncertain', code: 'commit_unknown', selected: false };
    try {
      const indexes = selected.map(item => item.index);
      const slot = 'conversion:' + conversion.sourceRef + ':' + indexes.join(',');
      conversion.retrySlot = '';
      for (const item of selected) item.batchSlot = slot;
      const result = await submitBatch(slot, '/conversion/upload', { source: conversion.source, indexes });
      if (!conversionCurrent(sequence, epoch)) return;
      if (result.status && !['completed','failed','interrupted','cancelled','reconciliation_required'].includes(result.status)) {
        for (const item of selected) Object.assign(item, { status: 'pending', selected: false });
        conversionFeedback('批次已受理，进度与结果会自动更新。');
        renderConversion();
        return;
      }
      applyResults(selected, result);
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
      try { await afterUpload(epoch); }
      catch { /* Refresh errors do not change confirmed per-row receipts. */ }
    }
  });

  return {
    close: closeConversion,
    get busy() { return conversion.busy; },
    missing(slot) {
      if (!convertEl('dialog').open || !conversion.items.some(item => item.batchSlot === slot && ['uncertain', 'pending'].includes(item.status))) return;
      conversion.retrySlot = slot;
      conversionFeedback('尚未查到此次上传的受理回执。可用原选项和请求标识再次提交。');
      updateConversionControls();
    },
    receive(slot, batch) {
      if (conversion.retrySlot === slot) conversion.retrySlot = '';
      const pending = conversion.items.filter(item => item.batchSlot === slot && ['uncertain', 'pending'].includes(item.status));
      if (pending.length && convertEl('dialog').open) applyResults(pending, batch);
    },
  };
}
