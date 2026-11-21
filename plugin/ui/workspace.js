import { DiscardedRequest } from './client.js';

// Two consumers can use the same authenticated first page: browser admission and
// the workspace projection. Only keep it within the current navigation and key.
export class WorkspaceAdmission {
  constructor(page, path, read) {
    this.page = page;
    this.path = path;
    this.response = read(path);
  }

  take(page, path) {
    if (this.page !== page || this.path !== path) return;
    this.path = '';
    return this.response;
  }
}

export async function readDirectory(path, key, getPage, fresh, current = () => true) {
  // An expired snapshot may be reopened once from offset zero.
  for (let attempt = 0; ; attempt++) {
    if (attempt && !current()) throw new DiscardedRequest();
    try { return await readPages(path, key, getPage, fresh, current); }
    catch (error) {
      if (path !== '/targets' || error.status !== 409 || attempt > 0) throw error;
    }
  }
}

async function readPages(path, key, getPage, fresh, current) {
  const items = [], seen = new Set();
  let total, snapshot, providerDirectoryVerified = true, orphanedHandling = [];
  for (;;) {
    const query = new URLSearchParams({ limit: '100', offset: String(items.length) });
    if (snapshot) query.set('snapshot', snapshot);
    else if (fresh && path === '/targets') query.set('fresh', '1');
    const page = await getPage(path + '?' + query);
    if (!Array.isArray(page?.items) || !Number.isSafeInteger(page.total) || page.total < 0) throw Error('目录返回无效分页数据。');
    if (total === undefined) {
      total = page.total;
      snapshot = page.snapshot;
      if (path === '/rules') {
        if (page.orphaned_handling !== undefined && !Array.isArray(page.orphaned_handling)) throw Error('处置目录格式无效。');
        orphanedHandling = page.orphaned_handling || [];
      }
    } else if (total !== page.total || snapshot !== page.snapshot) throw Error('读取期间目录已变化。');
    providerDirectoryVerified &&= page.provider_directory_verified !== false;
    for (const row of page.items) {
      const id = row?.[key];
      if (!id || seen.has(id)) throw Error('目录包含空标识或重复的对象。');
      seen.add(id);
      items.push(row);
    }
    if (items.length === total) return { items, total, providerDirectoryVerified, orphanedHandling };
    if (!page.items.length || items.length > total) throw Error('目录读取不完整。');
    if (!current()) throw new DiscardedRequest();
  }
}

// Browser storage holds access material and the recovery intent for the single
// initial setup. It never supplies a backend revision or verifies CPA access.
export class BrowserAuthorization {
  constructor(prefix, storage = () => window.localStorage, clock = Date.now, random = Math.random) {
    this.keyName = 'model-quality:management-key:' + prefix;
    this.intentName = 'model-quality:connection-migration:' + prefix;
    this.revisionName = 'model-quality:connection-revision:' + prefix;
    this.storage = storage;
    this.clock = clock;
    this.random = random;
    this.localIntent = null;
  }

  savedKey() {
    try { return this.storage().getItem(this.keyName)?.trim() || ''; }
    catch { return null; }
  }

  open(key, restoring) {
    return { key, restoring, expectedStorage: this.savedKey() };
  }

  changed(access, switchKey) {
    const key = this.savedKey();
    if (key === null || key === (access?.expectedStorage || '')) return false;
    switchKey(key);
    return true;
  }

  verified(access) {
    if (!access.restoring) this.remember(access, access.key);
  }

  remember(access, key) {
    access.key = key;
    try {
      this.storage().setItem(this.keyName, key);
      access.expectedStorage = key;
      return true;
    } catch {
      access.expectedStorage = this.savedKey();
      return false;
    }
  }

  forget(expected) {
    try {
      const storage = this.storage();
      if (expected === undefined || storage.getItem(this.keyName) === expected) storage.removeItem(this.keyName);
      return true;
    } catch { return false; }
  }

  notifyRevision(revision) {
    try { this.storage().setItem(this.revisionName, String(revision)); }
    catch { /* A later official GET still returns the durable state. */ }
  }

  storageEvent(name, onIdentity, onRevision) {
    if (name === null || name === this.keyName) onIdentity();
    if (name === this.revisionName) onRevision();
  }

  setupPlan(access, backend, first) {
    let previous = this.localIntent;
    try {
      const record = this.storage().getItem(this.intentName);
      if (record) previous = JSON.parse(record);
    } catch { /* Storage may be disabled or contain an obsolete record. */ }
    if (previous?.retryAt && previous.revision === backend.revision && (backend.can_migrate || backend.state === 'invalid')) {
      return this.clock() < previous.retryAt ? { retryAt: previous.retryAt } : { command: previous };
    }
    if (first && !access.restoring && (backend.can_migrate || backend.state === 'invalid')) return { command: { revision: backend.revision, mode: 'configure' } };
    if (first && access.restoring && backend.can_migrate && !previous) return { command: { revision: backend.revision, mode: 'migrate' } };
    return {};
  }

  rememberIntent(intent) {
    this.localIntent = intent;
    try { this.storage().setItem(this.intentName, JSON.stringify(intent)); }
    catch { /* In-page reconciliation still has the original intent. */ }
  }

  retrySetup(setup, error) {
    const prewrite = setup && !error.outcomeUnknown && error.source === 'backend_management' &&
      ['management_network', 'management_unavailable', 'management_restricted', 'management_temporarily_banned', 'management_rate_limited'].includes(error.code);
    if (!prewrite) return 0;
    const attempts = Math.min((setup.attempts || 0) + 1, 6);
    const minimum = ['management_network', 'management_unavailable'].includes(error.code) ? 2000 : 30000;
    const delay = Math.min(120000, minimum * 2 ** (attempts - 1)) * (1 + this.random() * 0.25);
    const retryAt = this.clock() + Math.max(error.retryAfterMs || 0, delay);
    this.rememberIntent({ revision: setup.revision, mode: setup.mode, attempts, retryAt });
    return retryAt;
  }
}
