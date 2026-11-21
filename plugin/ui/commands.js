import { DiscardedRequest } from './client.js';

// The browser remembers opaque operation IDs so it can ask the service what
// happened. Business payloads, secrets and authoritative results stay out of storage.
export const commandStatus = {
  accepted: '已受理', running: '执行中', awaiting_verification: '等待持久化核对', completed: '已完成', failed: '执行失败',
  interrupted: '已中断', cancelling: '取消请求中', cancelled: '已取消', reconciliation_required: '结果待核对',
};
export const commandFinished = status => ['completed', 'failed', 'interrupted', 'cancelled', 'reconciliation_required'].includes(status);
export const notAccepted = error => error?.status === 404 && error.code === 'command_not_accepted';
export const unknownReceipt = error => error?.outcomeUnknown || !Number.isInteger(error?.status) || error.status === 408 || error.status >= 500;

// A read/write IndexedDB transaction also serializes reservations on HTTP
// origins where Web Locks is unavailable. No command or key is written to it.
async function reserveOnHTTP(reserve) {
  if (!globalThis.indexedDB) return reserve();
  const db = await new Promise(resolve => {
    let open;
    try { open = indexedDB.open('model-quality-command-reservations', 1); } catch { resolve(); return; }
    open.onupgradeneeded = () => open.result.createObjectStore('reservations');
    open.onsuccess = () => resolve(open.result);
    open.onerror = () => resolve();
  });
  if (!db) return reserve();
  try {
    return await new Promise((resolve, reject) => {
      const tx = db.transaction('reservations', 'readwrite');
      let result;
      tx.objectStore('reservations').get('lock').onsuccess = () => {
        try { result = reserve(); } catch (error) { tx.abort(); reject(error); }
      };
      tx.oncomplete = () => resolve(result);
      tx.onabort = () => reject(tx.error || Error('无法保留本次操作标识。'));
    });
  } finally { db.close(); }
}

export function receiptLine(container, id) {
  container.hidden = false;
  let line = Array.from(container.children).find(node => node.dataset.commandId === id);
  if (!line) {
    line = document.createElement('div');
    line.dataset.commandId = id;
    container.append(line);
    const finished = container.querySelectorAll('[data-finished="true"]');
    if (finished.length >= 20) finished[0].remove();
  }
  return line;
}

export class CommandReceipts {
  constructor(namespace, newID, { shared = () => window.localStorage, session = () => window.sessionStorage } = {}) {
    this.namespace = namespace;
    this.prefix = namespace + ':pending:';
    this.newID = newID;
    this.shared = shared;
    this.session = session;
    this.volatile = new Map();
    this.sending = new Set();
    this.migrated = false;
  }

  // Readers call this only after browser access is established. A private
  // browsing policy can deny either storage independently.
  records() {
    if (!this.migrated) this.migrateSession();
    const records = new Map(this.volatile);
    for (const store of [this.session, this.shared]) {
      try {
        const storage = store();
        for (let i = 0; i < storage.length; i++) {
          const key = storage.key(i);
          if (!key?.startsWith(this.prefix)) continue;
          const id = key.slice(this.prefix.length), slot = storage.getItem(key);
          if (slot && id && id.length <= 128) records.set(id, slot);
        }
      } catch { /* In-memory references remain available to this tab. */ }
    }
    return records;
  }

  migrateSession() {
    this.migrated = true;
    try {
      const session = this.session();
      const old = session.getItem(this.namespace);
      if (!old) return;
      let durable = true;
      for (const [slot, id] of JSON.parse(old)) {
        if (typeof slot === 'string' && typeof id === 'string' && id && id.length <= 128) {
          durable = this.remember(slot, id) && durable;
        }
      }
      if (durable) session.removeItem(this.namespace);
    } catch { /* A legacy or inaccessible session cannot start a command. */ }
  }

  remember(slot, id) {
    const name = this.prefix + id;
    for (const store of [this.shared, this.session]) {
      try {
        const storage = store();
        storage.setItem(name, slot);
        if (storage.getItem(name) === slot) return true;
      } catch { /* Fall back without recording the command payload. */ }
    }
    this.volatile.set(id, slot);
    return false;
  }

  forget(slot, id) {
    if (this.volatile.get(id) === slot) this.volatile.delete(id);
    for (const store of [this.shared, this.session]) {
      try {
        const storage = store(), name = this.prefix + id;
        if (storage.getItem(name) === slot) storage.removeItem(name);
      } catch { /* A later authorized read can still reconcile the same ID. */ }
    }
  }

  async claim(slot, current) {
    const reserve = () => {
      if (!current()) throw new DiscardedRequest();
      for (const [id, knownSlot] of this.records()) if (slot === knownSlot) return { id, existing: true };
      const id = this.newID();
      this.remember(slot, id);
      return { id, existing: false };
    };
    // Web Locks serialize the small local reservation on secure origins. Each
    // ID has its own storage entry even where the browser lacks this API.
    if (globalThis.navigator?.locks) return navigator.locks.request(this.prefix + slot, reserve);
    return reserveOnHTTP(reserve);
  }

  async submit(slot, send, check, current = () => true) {
    if (this.sending.has(slot)) throw Error('此操作正在提交。');
    this.sending.add(slot);
    try {
      const { id, existing } = await this.claim(slot, current);
      if (!current()) throw new DiscardedRequest();
      if (existing) {
        try { return { id, reply: await check(id) }; }
        catch (error) {
          if (!notAccepted(error)) throw error;
          // Only this explicit submit can repeat a POST. The backend binds the
          // original ID to its parameters and a durable write intent.
        }
      }
      if (!current()) throw new DiscardedRequest();
      try { return { id, reply: await send(id) }; }
      catch (error) {
        if (!existing && !unknownReceipt(error)) this.forget(slot, id);
        throw error;
      }
    } finally {
      this.sending.delete(slot);
    }
  }

  async reconcile(read, receive, failed, current) {
    if (!current()) return;
    for (const [id, slot] of this.records()) {
      if (!current()) return;
      if (this.sending.has(slot)) continue;
      try {
        const data = await read(id);
        if (!current()) continue;
        if (receive(slot, id, data)) this.forget(slot, id);
      } catch (error) {
        if (current()) {
          failed(slot, id, error);
        }
      }
    }
  }
}

// FIPS 180-4 SHA-256 for HTTP origins without SubtleCrypto. Only the digest
// of the explicit draft is stored. The server checks the actual parameters.
function sha256(bytes) {
  const constants = [
    0x428a2f98,0x71374491,0xb5c0fbcf,0xe9b5dba5,0x3956c25b,0x59f111f1,0x923f82a4,0xab1c5ed5,
    0xd807aa98,0x12835b01,0x243185be,0x550c7dc3,0x72be5d74,0x80deb1fe,0x9bdc06a7,0xc19bf174,
    0xe49b69c1,0xefbe4786,0x0fc19dc6,0x240ca1cc,0x2de92c6f,0x4a7484aa,0x5cb0a9dc,0x76f988da,
    0x983e5152,0xa831c66d,0xb00327c8,0xbf597fc7,0xc6e00bf3,0xd5a79147,0x06ca6351,0x14292967,
    0x27b70a85,0x2e1b2138,0x4d2c6dfc,0x53380d13,0x650a7354,0x766a0abb,0x81c2c92e,0x92722c85,
    0xa2bfe8a1,0xa81a664b,0xc24b8b70,0xc76c51a3,0xd192e819,0xd6990624,0xf40e3585,0x106aa070,
    0x19a4c116,0x1e376c08,0x2748774c,0x34b0bcb5,0x391c0cb3,0x4ed8aa4a,0x5b9cca4f,0x682e6ff3,
    0x748f82ee,0x78a5636f,0x84c87814,0x8cc70208,0x90befffa,0xa4506ceb,0xbef9a3f7,0xc67178f2,
  ];
  const hash = [0x6a09e667,0xbb67ae85,0x3c6ef372,0xa54ff53a,0x510e527f,0x9b05688c,0x1f83d9ab,0x5be0cd19];
  const padded = new Uint8Array(Math.ceil((bytes.length + 9) / 64) * 64);
  padded.set(bytes); padded[bytes.length] = 0x80;
  const view = new DataView(padded.buffer);
  view.setUint32(padded.length - 8, Math.floor(bytes.length / 0x20000000));
  view.setUint32(padded.length - 4, bytes.length * 8);
  const rotate = (word, bits) => (word >>> bits) | (word << (32 - bits));
  const words = new Uint32Array(64);
  for (let offset = 0; offset < padded.length; offset += 64) {
    for (let i = 0; i < 16; i++) words[i] = view.getUint32(offset + i * 4);
    for (let i = 16; i < 64; i++) {
      const x = words[i - 15], y = words[i - 2];
      words[i] = words[i - 16] + (rotate(x,7) ^ rotate(x,18) ^ (x >>> 3)) + words[i - 7] + (rotate(y,17) ^ rotate(y,19) ^ (y >>> 10));
    }
    let [a,b,c,d,e,f,g,h] = hash;
    for (let i = 0; i < 64; i++) {
      const t1 = h + (rotate(e,6) ^ rotate(e,11) ^ rotate(e,25)) + ((e & f) ^ (~e & g)) + constants[i] + words[i];
      const t2 = (rotate(a,2) ^ rotate(a,13) ^ rotate(a,22)) + ((a & b) ^ (a & c) ^ (b & c));
      h = g;
      g = f;
      f = e;
      e = (d + t1) >>> 0;
      d = c;
      c = b;
      b = a;
      a = (t1 + t2) >>> 0;
    }
    [a,b,c,d,e,f,g,h].forEach((word,i) => { hash[i] = (hash[i] + word) >>> 0; });
  }
  return hash.map(word => word.toString(16).padStart(8,'0')).join('');
}

export async function digestCommandBytes(bytes, subtle = globalThis.crypto?.subtle) {
  if (!subtle) return sha256(bytes);
  const digest = new Uint8Array(await subtle.digest('SHA-256', bytes));
  return Array.from(digest, b => b.toString(16).padStart(2, '0')).join('');
}

export async function commandSlot(kind, body) {
  const bytes = new TextEncoder().encode(JSON.stringify(body));
  return kind + ':' + await digestCommandBytes(bytes);
}

export const creationSlot = body => commandSlot('create', body);
