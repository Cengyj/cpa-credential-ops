// Requests use a page-local session; the app manages explicit credential persistence.
export class DiscardedRequest extends Error {
  constructor() {
    super('The request belongs to an inactive view or session.');
    this.name = 'DiscardedRequest';
  }
}

export const isDiscarded = (error) => error instanceof DiscardedRequest || error?.name === 'AbortError';

function retryDelay(headers) {
  const value = headers?.get('Retry-After');
  if (!value) return 0;
  const delay = /^\d+$/.test(value) ? Number(value) * 1000 : Date.parse(value) - Date.now();
  return Number.isFinite(delay) && delay > 0 ? Math.min(2147483000, delay) : 0;
}

export class ManagementClient {
  #key = '';
  #epoch = 0;
  #pending = new Set();
  #reads = new Map();
  #readLimit = null;

  constructor(prefix, onUnauthorized) {
    this.prefix = prefix;
    this.onUnauthorized = onUnauthorized;
  }

  get epoch() {
    return this.#epoch;
  }

  open(key) {
    this.clear();
    this.#key = key.trim();
  }

  clear() {
    this.#key = '';
    this.#epoch++;
    for (const controller of this.#pending) controller.abort();
    this.#pending.clear();
    this.#reads.clear();
    this.#readLimit = null;
  }

  request(path, method = 'GET', body, { fresh = false } = {}) {
    return this.#dispatch(this.prefix, path, method, body, undefined, fresh);
  }

  async requestHostLogin(path, method = 'GET', body, idempotencyKey) {
    const allowed = {
      '/capabilities': ['GET'], '/targets': ['GET'],
      '/jobs': ['GET', 'DELETE'], '/commands': ['GET'], '/detail': ['GET'], '/logs': ['GET'], '/candidate': ['GET'],
      '/callback': ['POST'], '/cancel': ['POST'], '/commit': ['POST'], '/discard': ['POST'],
      '/vault/status': ['GET'],
      '/batches': ['GET'], '/batches/cancel': ['POST'], '/accounts': ['GET'], '/accounts/detail': ['GET'], '/accounts/import': ['POST'], '/accounts/entry': ['PUT', 'DELETE'],
      '/accounts/security': ['POST'], '/accounts/security/complete': ['POST'],
      '/conversion/preview': ['POST'], '/conversion/upload': ['POST'],
      '/accounts/start': ['POST'], '/accounts/inspect': ['POST'], '/accounts/settings': ['GET', 'PUT'],
    };
    const parsed = new URL(path, 'https://management.invalid');
    const queryKeys = new Set(['id', 'auth_id', 'account_id', 'cursor', 'limit']);
    if (typeof path !== 'string' || !path.startsWith('/') || path.startsWith('//') || path.split('?')[0] !== parsed.pathname || parsed.origin !== 'https://management.invalid' || parsed.hash ||
        !allowed[parsed.pathname]?.includes(method) || [...parsed.searchParams.keys()].some(key => !queryKeys.has(key))) {
      throw new Error('不支持的登录管理操作。');
    }
    if (method !== 'GET' && !idempotencyKey) throw new Error('登录操作缺少请求标识。');
    return this.#dispatch(`${this.prefix}/login`, parsed.pathname + parsed.search, method, body, idempotencyKey);
  }

  #dispatch(prefix, path, method, body, idempotencyKey, fresh = false) {
    const epoch = this.#epoch;
    if (method !== 'GET') {
      this.#reads.clear();
      return this.#send(prefix, path, method, body, idempotencyKey).finally(() => {
        if (epoch === this.#epoch) this.#reads.clear();
      });
    }
    if (this.#readLimit && Date.now() < this.#readLimit.until) return Promise.reject(this.#readLimit.error);
    const url = prefix + path;
    if (fresh) this.#reads.delete(url);
    else if (this.#reads.has(url)) return this.#reads.get(url);
    const read = this.#send(prefix, path, method, body, idempotencyKey).finally(() => {
      if (this.#reads.get(url) === read) this.#reads.delete(url);
    });
    // A pre-submission snapshot cannot satisfy a later, independent display refresh.
    if (!fresh) this.#reads.set(url, read);
    return read;
  }

  #admissionError(error, retryAfter) {
    if (retryAfter) error.retryAfterMs = retryAfter;
    if (error.source !== 'browser_access') return error;
    const ban = /^IP banned due to too many failed attempts\. Try again in (?:(\d+)h)?(?:(\d+)m)?(\d+)s$/i.exec(error.detail || '');
    if (ban) {
      const remaining = ((Number(ban[1] || 0) * 60 + Number(ban[2] || 0)) * 60 + Number(ban[3])) * 1000;
      error.retryAfterMs = Math.max(retryAfter, Math.min(2147483000, remaining));
      error.message = 'CPA 暂时封禁了当前浏览器的管理访问 (403)。';
    }
    if (error.status === 429 || error.retryAfterMs || ban) {
      const until = Date.now() + Math.max(1000, error.retryAfterMs || 15000);
      if (!this.#readLimit || until > this.#readLimit.until) this.#readLimit = { error, until };
    }
    return error;
  }

  async #send(prefix, path, method, body, idempotencyKey) {
    if (!this.#key) throw new Error('请先连接管理接口。');
    const epoch = this.#epoch;
    const controller = new AbortController();
    this.#pending.add(controller);
    let readTimedOut = false;
    let errorStatus = 0, retryAfter = 0;
    const readTimer = method === 'GET' ? setTimeout(() => { readTimedOut = true; controller.abort(); }, 15000) : null;
    const headers = { Authorization: `Bearer ${this.#key}`, Accept: 'application/json' };
    if (idempotencyKey) headers['Idempotency-Key'] = idempotencyKey;
    if (method === 'POST' && prefix === this.prefix + '/login' && ['/conversion/upload', '/accounts/inspect'].includes(path)) headers.Prefer = 'respond-async';
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    try {
      const response = await fetch(`${prefix}${path}`, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        cache: 'no-store',
        credentials: 'omit',
        redirect: 'error',
        signal: controller.signal,
      });
      if (epoch !== this.#epoch) throw new DiscardedRequest();
      if (!response.ok) { errorStatus = response.status; retryAfter = retryDelay(response.headers); }
      if (response.status === 401) {
        controller.abort();
        this.onUnauthorized?.(this.#key);
        throw new DiscardedRequest();
      }
      const text = response.status === 204 ? '' : await response.text();
      if (epoch !== this.#epoch) throw new DiscardedRequest();
      let data = {};
      try {
        data = text ? JSON.parse(text) : {};
      } catch {
        const invalid = new Error(`管理接口返回了无法解析的响应 (${response.status})，请核对任务状态。`);
        invalid.status = response.status;
        invalid.code = 'management_response_invalid';
        invalid.source = response.status === 403 || response.status === 429 ? 'browser_access' : 'browser_response';
        invalid.outcomeUnknown = method !== 'GET';
        throw this.#admissionError(invalid, retryAfter);
      }
      if (!response.ok) {
        const message = data?.error?.message || data?.error || data?.message || `接口返回 ${response.status}`;
        const code = data?.code || data?.error?.code;
        const detail = typeof message === 'string' ? message.replaceAll(this.#key, '[已隐藏]') : '操作失败';
        const source = data?.source || data?.error?.source || (!code && [403, 429].includes(response.status) ? 'browser_access' : 'business');
        const display = source === 'browser_access' ? (response.status === 403 ? '当前浏览器访问受限 (403)' : 'CPA 限制了页面读取频率 (429)') : detail;
        const error = new Error(`${display}${code ? ` (${code})` : ''}`);
        error.detail = detail;
        error.status = response.status;
        error.code = code;
        error.source = source;
        error.outcomeUnknown = data?.outcome_unknown === true || data?.error?.outcome_unknown === true;
        throw this.#admissionError(error, retryAfter);
      }
      return data;
    } catch (error) {
      if (epoch !== this.#epoch) throw new DiscardedRequest();
      if (errorStatus && (readTimedOut || error.name === 'AbortError' || error instanceof TypeError)) {
        const incomplete = new Error(`管理接口返回 HTTP ${errorStatus}，错误详情未能完整读取。`);
        incomplete.status = errorStatus;
        incomplete.code = 'management_response_invalid';
        incomplete.source = errorStatus === 403 || errorStatus === 429 ? 'browser_access' : 'browser_response';
        incomplete.outcomeUnknown = method !== 'GET';
        throw this.#admissionError(incomplete, retryAfter);
      }
      if (readTimedOut) {
        const timeout = new Error('管理数据读取超时，将重新读取。');
        timeout.code = 'read_timeout';
        timeout.source = 'browser_network';
        throw timeout;
      }
      if (error.name === 'AbortError' && method === 'GET') throw new DiscardedRequest();
      if (error.name === 'AbortError' || error instanceof TypeError) {
        const network = new Error(method === 'GET' ? '页面数据暂不可达，请检查浏览器网络。' : '页面与 CPA 的连接中断，操作结果待核对。');
        network.code = 'management_network_error';
        network.source = 'browser_network';
        network.outcomeUnknown = method !== 'GET';
        throw network;
      }
      throw error;
    } finally {
      clearTimeout(readTimer);
      this.#pending.delete(controller);
    }
  }
}
