// Exercise the real browser client with deterministic transport and clock substitutes.
// No browser, network listener, real credentials, or upstream calls are involved.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const vm = require('node:vm');

const sourcePath = path.resolve(__dirname, '../../plugin/ui/client.js');
const source = fs.readFileSync(sourcePath, 'utf8')
  .replace(/^export\s+(?=class |const )/gm, '')
  + '\nglobalThis.clientModule = { ManagementClient, DiscardedRequest, isDiscarded };\n';
const prefix = '/v0/management/plugins/model-quality';

function deferred() {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
function abortError() {
  const error = new Error('synthetic cancellation');
  error.name = 'AbortError';
  return error;
}
function response(value, status = 200) {
  return { status, ok: status >= 200 && status < 300, text: async () => JSON.stringify(value) };
}

class FakeClock {
  now = 0;
  sequence = 0;
  timers = new Map();
  setTimeout = (callback, delay = 0) => {
    const id = ++this.sequence;
    this.timers.set(id, { at: this.now + Number(delay), callback });
    return id;
  };
  clearTimeout = id => this.timers.delete(id);
  advance(milliseconds) {
    const end = this.now + milliseconds;
    for (;;) {
      const next = [...this.timers].filter(([, value]) => value.at <= end)
        .sort((a, b) => a[1].at - b[1].at || a[0] - b[0])[0];
      if (!next) break;
      this.now = next[1].at;
      this.timers.delete(next[0]);
      next[1].callback();
    }
    this.now = end;
  }
}

function harness({ respectAbort = true, onUnauthorized } = {}) {
  const clock = new FakeClock();
  const calls = [];
  const fetch = (url, options) => {
    const pending = deferred();
    const call = { url, options, ...pending };
    calls.push(call);
    if (respectAbort) {
      if (options.signal.aborted) pending.reject(abortError());
      else options.signal.addEventListener('abort', () => pending.reject(abortError()), { once: true });
    }
    return pending.promise;
  };
  const context = vm.createContext({
    URL, AbortController, Error, TypeError,
    fetch, setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout,
  });
  new vm.Script(source, { filename: sourcePath }).runInContext(context);
  const module = context.clientModule;
  const client = new module.ManagementClient(prefix, onUnauthorized);
  client.open('fixture-management-key');
  return { clock, calls, client, ...module };
}

test('a GET aborts at 15 seconds and a later read succeeds in the same session', async () => {
  const { client, clock, calls, isDiscarded } = harness();
  const request = client.requestHostLogin('/accounts');
  const failure = assert.rejects(request, error => error.code === 'read_timeout' && !isDiscarded(error));
  assert.equal(calls.length, 1);
  clock.advance(14999);
  assert.equal(calls[0].options.signal.aborted, false);
  clock.advance(1);
  await failure;
  assert.equal(calls[0].options.signal.aborted, true);
  assert.equal(calls.length, 1, 'a timed-out read is not automatically replayed by the client');
  assert.equal(clock.timers.size, 0);

  const recovered = client.requestHostLogin('/accounts');
  assert.equal(calls[1].options.headers.Authorization, 'Bearer fixture-management-key');
  calls[1].resolve(response({ items: [{ id: 'fixture-account' }] }));
  assert.equal((await recovered).items[0].id, 'fixture-account');
  assert.equal(clock.timers.size, 0);
});

test('the GET deadline also bounds response-body reading', async () => {
  const { client, clock, calls } = harness();
  const reading = client.requestHostLogin('/jobs?limit=200');
  const failure = assert.rejects(reading, error => error.code === 'read_timeout');
  const body = deferred();
  const enteredBody = deferred();
  calls[0].options.signal.addEventListener('abort', () => body.reject(abortError()), { once: true });
  calls[0].resolve({ status: 200, ok: true, text: () => { enteredBody.resolve(); return body.promise; } });
  await enteredBody.promise;
  clock.advance(15000);
  await failure;
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
});

test('clear discards pending reads and requires a new management session', async () => {
  const { client, calls, clock, isDiscarded } = harness();
  const epoch = client.epoch;
  const reading = client.requestHostLogin('/accounts/detail?id=fixture-account');
  const discarded = assert.rejects(reading, isDiscarded);
  client.clear();
  await discarded;
  assert.equal(calls[0].options.signal.aborted, true);
  assert.ok(client.epoch > epoch);
  assert.equal(clock.timers.size, 0);
  await assert.rejects(client.requestHostLogin('/accounts'), /请先连接管理接口/);
  assert.equal(calls.length, 1);
});

test('opening a new session rejects a late old response and cannot log out the new key', async () => {
  const invalidated = [];
  const { client, calls, clock, isDiscarded } = harness({ respectAbort: false, onUnauthorized: key => invalidated.push(key) });
  const oldRead = client.requestHostLogin('/accounts');
  const discarded = assert.rejects(oldRead, isDiscarded);
  client.open('replacement-fixture-key');
  assert.equal(calls[0].options.signal.aborted, true);
  const currentRead = client.requestHostLogin('/accounts');
  calls[1].resolve(response({ items: [] }));
  await currentRead;
  calls[0].resolve(response({ code: 'unauthorized' }, 401));
  await discarded;
  assert.deepEqual(invalidated, []);
  assert.equal(calls[1].options.headers.Authorization, 'Bearer replacement-fixture-key');
  assert.equal(clock.timers.size, 0);
});

test('a current-session 401 invokes logout once and discards the response', async () => {
  const invalidated = [];
  let client;
  const setup = harness({ onUnauthorized: key => { invalidated.push(key); client.clear(); } });
  client = setup.client;
  const reading = client.requestHostLogin('/accounts');
  const discarded = assert.rejects(reading, setup.isDiscarded);
  setup.calls[0].resolve(response({ code: 'unauthorized' }, 401));
  await discarded;
  assert.deepEqual(invalidated, ['fixture-management-key']);
  await assert.rejects(client.requestHostLogin('/accounts'), /请先连接管理接口/);
  assert.equal(setup.calls.length, 1);
  assert.equal(setup.clock.timers.size, 0);
});

test('account-scoped history is allowed while invalid routes never reach fetch', async () => {
  const { client, calls } = harness();
  const reading = client.requestHostLogin('/jobs?account_id=fixture-account&limit=200');
  assert.equal(calls[0].url, `${prefix}/login/jobs?account_id=fixture-account&limit=200`);
  calls[0].resolve(response({ items: [] }));
  await reading;
  for (const [route, method] of [
    ['jobs', 'GET'], ['https://outside.invalid/jobs', 'GET'], ['//outside.invalid/jobs', 'GET'],
    ['/accounts/../jobs', 'GET'], ['/jobs#fragment', 'GET'], ['/jobs?redirect=outside', 'GET'],
    ['/jobs?unknown=1', 'GET'], ['/accounts/start', 'GET'], ['/jobs', 'POST'], ['/accounts', 'PATCH'],
  ]) {
    await assert.rejects(client.requestHostLogin(route, method, {}, 'fixture-request'), /不支持的登录管理操作/, `${method} ${route}`);
  }
  await assert.rejects(client.requestHostLogin('/accounts/start', 'POST', { id: 'fixture-account' }), /缺少请求标识/);
  assert.equal(calls.length, 1);
});

test('writes have no client deadline, keep their idempotency key, and are sent only once', async () => {
  const { client, calls, clock } = harness();
  const body = { id: 'fixture-account', revision: 7 };
  let settled = false;
  const write = client.requestHostLogin('/accounts/start', 'POST', body, 'fixture-once');
  write.then(() => { settled = true; }, () => { settled = true; });
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
  clock.advance(120000);
  await Promise.resolve();
  assert.equal(settled, false);
  assert.equal(calls[0].options.signal.aborted, false);
  assert.equal(calls[0].options.headers['Idempotency-Key'], 'fixture-once');
  assert.equal(calls[0].options.method, 'POST');
  assert.deepEqual(JSON.parse(calls[0].options.body), body);
  calls[0].resolve(response({ id: 'fixture-job', status: 'queued' }));
  assert.equal((await write).status, 'queued');
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
});

test('a write transport failure is marked uncertain and never replayed', async () => {
  const { client, calls, clock } = harness();
  const write = client.requestHostLogin('/accounts/security', 'POST', { id: 'fixture-account', revision: 1, action: 'reset_totp' }, 'fixture-security-once');
  const failure = assert.rejects(write, error => {
    assert.equal(error.code, 'management_network_error');
    assert.doesNotMatch(error.message, /synthetic-secret|private-endpoint/);
    return true;
  });
  calls[0].reject(new TypeError('synthetic-secret at private-endpoint'));
  await failure;
  clock.advance(120000);
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
});

test('a GET transport error is distinct from an upstream login network_error', async () => {
  const { client, calls, clock } = harness();
  const read = client.requestHostLogin('/accounts');
  const failure = assert.rejects(read, error => error.code === 'management_network_error');
  calls[0].reject(new TypeError('synthetic network interruption'));
  await failure;
  const retry = client.requestHostLogin('/accounts');
  calls[1].resolve(response({ items: [] }));
  assert.equal((await retry).items.length, 0);
  assert.equal(calls.length, 2);
  assert.equal(clock.timers.size, 0);
});

test('a server refusal preserves its code and is not automatically retried', async () => {
  const { client, calls, clock } = harness();
  const write = client.requestHostLogin('/accounts/start', 'POST', { id: 'fixture-account', revision: 1 }, 'fixture-server-error');
  const failure = assert.rejects(write, error => error.status === 503 && error.code === 'storage_unavailable');
  calls[0].resolve(response({ error: 'storage temporarily unavailable', code: 'storage_unavailable' }, 503));
  await failure;
  clock.advance(120000);
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
});

test('an unreadable write response remains uncertain without replaying the request', async () => {
  const { client, calls, clock } = harness();
  const write = client.requestHostLogin('/accounts/security', 'POST', { id: 'fixture-account' }, 'fixture-truncated-once');
  const failure = assert.rejects(write, error => {
    assert.equal(error.code, 'management_response_invalid');
    assert.equal(error.status, 200);
    assert.equal(error.outcomeUnknown, true);
    assert.doesNotMatch(error.message, /synthetic-secret/);
    return true;
  });
  calls[0].resolve({ status: 200, ok: true, text: async () => '{"synthetic-secret":' });
  await failure;
  clock.advance(120000);
  assert.equal(calls.length, 1);
  assert.equal(clock.timers.size, 0);
});

test('a forbidden read retains its HTTP status with plain text or a non-object JSON body', async () => {
  const { client, calls } = harness();
  for (const body of ['Synthetic denial', 'null', '[]', 'false']) {
    const read = client.requestHostLogin('/accounts');
    const failure = assert.rejects(read, error => error.status === 403 && error.code !== 'management_network_error');
    calls.at(-1).resolve({ status: 403, ok: false, text: async () => body });
    await failure;
  }
});

test('a 401 invalidates the current key without waiting for an error body', async () => {
  const invalidated = [];
  const { client, calls, isDiscarded } = harness({ onUnauthorized: key => invalidated.push(key) });
  const read = client.requestHostLogin('/accounts');
  const failure = assert.rejects(read, isDiscarded);
  let bodyRead = false;
  calls[0].resolve({ status: 401, ok: false, text: async () => { bodyRead = true; return 'unneeded'; } });
  await failure;
  assert.deepEqual(invalidated, ['fixture-management-key']);
  assert.equal(bodyRead, false);
});

test('a denied HTTP response remains a denial if its body stream breaks', async () => {
  const { client, calls } = harness();
  const read = client.requestHostLogin('/accounts');
  const failure = assert.rejects(read, error => error.status === 403 && error.code === 'management_response_invalid');
  calls[0].resolve({ status: 403, ok: false, text: async () => { throw new TypeError('incomplete fixture body'); } });
  await failure;
});

test('simultaneous workspace and visibility reads share transport until a mutation changes the snapshot', async () => {
  const { client, calls } = harness();
  const first = client.request('/targets?limit=100');
  const second = client.request('/targets?limit=100');
  assert.equal(calls.length, 1, 'concurrent display reads should issue one GET');
  const write = client.request('/rules/timer', 'PUT', { id: 'fake', revision: 1, enabled: false });
  const during = client.request('/targets?limit=100');
  assert.equal(calls.length, 3, 'the configuration write starts a new read generation');
  calls[1].resolve(response({ ok: true }));
  await write;
  const after = client.request('/targets?limit=100');
  assert.equal(calls.length, 4, 'post-commit reads cannot reuse a pre-commit response');
  calls[0].resolve(response({ version: 1 }));
  calls[2].resolve(response({ version: 2 }));
  calls[3].resolve(response({ version: 3 }));
  assert.deepEqual(await Promise.all([first, second, during, after].map(read => read.then(item => item.version))), [1, 1, 2, 3]);
});

test('browser restriction, rate limit and backend credential failure keep independent sources', async () => {
  const invalidated = [];
  const { client, calls } = harness({ onUnauthorized: key => invalidated.push(key) });
  for (const [status, body, source] of [
    [403, { error: 'Forbidden' }, 'browser_access'],
    [422, { code: 'management_invalid', source: 'backend_management' }, 'backend_management'],
    [409, { code: 'revision_conflict' }, 'business'],
    [429, { error: 'Slow down' }, 'browser_access'],
  ]) {
    const request = client.request('/connection');
    calls.at(-1).resolve(response(body, status));
    await assert.rejects(request, error => error.source === source && error.status === status);
  }
  assert.deepEqual(invalidated, []);
});

test('a browser 429 with Retry-After suppresses further display requests during the hold', async () => {
  const { client, calls } = harness();
  const read = client.request('/connection');
  calls[0].resolve({ ...response({ error: 'Slow down' }, 429), headers: { get: () => '60' } });
  await assert.rejects(read, error => error.retryAfterMs === 60000);
  await assert.rejects(client.request('/rules'), error => error.status === 429);
  assert.equal(calls.length, 1);
});

test('CPA IP-ban duration survives as a browser read hold while a model 401 never logs out a session', async () => {
  const invalidated = [];
  const { client, calls } = harness({ onUnauthorized: key => invalidated.push(key) });
  const task = client.request('/runs/detail?id=synthetic');
  calls[0].resolve(response({ samples: [{ http_status: 401, credential_error: 'upstream' }] }));
  assert.equal((await task).samples[0].http_status, 401);
  const read = client.request('/connection');
  calls[1].resolve(response({ error: 'IP banned due to too many failed attempts. Try again in 16m14s' }, 403));
  await assert.rejects(read, error => error.retryAfterMs === 974000 && /暂时封禁/.test(error.message));
  await assert.rejects(client.requestHostLogin('/accounts'), error => error.status === 403);
  assert.equal(calls.length, 2);
  assert.deepEqual(invalidated, []);
});

test('a connection response can carry a backend delay or an unknown durable write', async () => {
  const { client, calls } = harness();
  const candidate = client.request('/connection', 'PUT', { revision: 8, mode: 'configure' });
  calls[0].resolve({ ...response({ error: { code: 'management_temporarily_banned', source: 'backend_management' } }, 503),
    headers: { get: () => '180' } });
  await assert.rejects(candidate, error => error.source === 'backend_management' && error.retryAfterMs === 180000);
  const retry = client.request('/connection');
  calls[1].resolve(response({ state: 'waiting' }));
  assert.equal((await retry).state, 'waiting');
  const unknown = client.request('/connection', 'DELETE', { revision: 8 });
  calls[2].resolve(response({ error: { code: 'connection_save_unknown', source: 'backend_management', outcome_unknown: true } }, 507));
  await assert.rejects(unknown, error => error.outcomeUnknown && error.source === 'backend_management');
  assert.equal(calls.length, 3);
});

test('an aborted mutation in the active page is an unknown receipt and cannot be replayed', async () => {
  const { client, calls, isDiscarded } = harness();
  const action = client.requestHostLogin('/accounts/security', 'POST', { id: 'fixture-account' }, 'fixture-receipt');
  calls[0].reject(abortError());
  await assert.rejects(action, error => !isDiscarded(error) && error.outcomeUnknown && error.source === 'browser_network');
  assert.equal(calls.length, 1);
});
