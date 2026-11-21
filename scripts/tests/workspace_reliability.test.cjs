const assert = require('node:assert/strict');
const test = require('node:test');
const ui = '../../plugin/ui/';

test('HTTP and HTTPS command digests agree with native SHA-256 for empty, multi-block, Unicode and large JSON inputs', async () => {
  const { digestCommandBytes } = await import(ui + 'commands.js');
  const { createHash, webcrypto } = require('node:crypto');
  for (const value of ['', 'abc', 'a'.repeat(55), 'b'.repeat(56), 'c'.repeat(129), '试题🍵',
    JSON.stringify({items: Array.from({length:12000}, (_,i) => '账号条目 ' + i)})]) {
    const bytes = new TextEncoder().encode(value), expected = createHash('sha256').update(bytes).digest('hex');
    assert.equal(await digestCommandBytes(bytes, null), expected);
    assert.equal(await digestCommandBytes(bytes, webcrypto.subtle), expected);
  }
});

test('leaving a workspace during a page read starts no follow-up page or expiry retry', async () => {
  const { readDirectory } = await import(ui + 'workspace.js');
  const { isDiscarded } = await import(ui + 'client.js');
  for (const expired of [false,true]) {
    let reads = 0;
    await assert.rejects(readDirectory('/targets','auth_id', async () => {
      reads++;
      if (expired) throw Object.assign(Error('expired'), {status:409});
      return {total:2,snapshot:'1',items:[{auth_id:'one'}]};
    }, false, () => false), isDiscarded);
    assert.equal(reads,1);
  }
});

test('workspace admission reuses only the matching first read once', async () => {
  const { WorkspaceAdmission } = await import(ui + 'workspace.js');
  let requests = 0;
  const entry = new WorkspaceAdmission('login', '/login/accounts', async () => ({ version: ++requests }));
  assert.equal(entry.take('operations', '/login/accounts'), undefined);
  assert.equal(await entry.take('login', '/metrics'), undefined);
  assert.equal((await entry.take('login', '/login/accounts')).version, 1);
  assert.equal(entry.take('login', '/login/accounts'), undefined);
  assert.equal(requests, 1);
});

test('a replacement snapshot discards all previously accumulated pages and preserves fresh intent', async () => {
  const { readDirectory } = await import(ui + 'workspace.js');
  const response = [
    { total: 2, snapshot: 'A', items: [{ auth_id: 'discard' }] },
    Object.assign(Error('expired'), { status: 409 }),
    { total: 2, snapshot: 'B', items: [{ auth_id: 'x' }], provider_directory_verified: false },
    { total: 2, snapshot: 'B', items: [{ auth_id: 'y' }] },
  ];
  const reads = [];
  const rows = await readDirectory('/targets', 'auth_id', async url => {
    reads.push(url);
    const page = response.shift();
    if (page instanceof Error) throw page;
    return page;
  }, true);
  assert.deepEqual(reads, ['/targets?limit=100&offset=0&fresh=1', '/targets?limit=100&offset=1&snapshot=A',
    '/targets?limit=100&offset=0&fresh=1', '/targets?limit=100&offset=1&snapshot=B']);
  assert.deepEqual(rows.items, [{ auth_id: 'x' }, { auth_id: 'y' }]);
  assert.equal(rows.providerDirectoryVerified, false);
});

test('repeated expiry is bounded and a changed or malformed page is never appended', async () => {
  const { readDirectory } = await import(ui + 'workspace.js');
  let reads = 0;
  await assert.rejects(readDirectory('/targets', 'auth_id', async () => {
    reads++; throw Object.assign(Error('expired'), { status: 409 });
  }), /expired/);
  assert.equal(reads, 2);
  for (const change of [{ snapshot: 'B' }, { total: 3 }, { items: [{ auth_id: 'a' }] }]) {
    const pages = [{ total: 2, snapshot: 'A', items: [{ auth_id: 'a' }] }, { total: 2, snapshot: 'A', items: [{ auth_id: 'b' }], ...change }];
    await assert.rejects(readDirectory('/targets', 'auth_id', async () => pages.shift()));
  }
});

test('command lookups cannot replay a write, a generic 404 cannot authorize re-submission', async () => {
  const { CommandReceipts, creationSlot } = await import(ui + 'commands.js');
  const tracker = new CommandReceipts('test', () => 'original');
  const sent = [];
  const send = id => { sent.push(id); throw Object.assign(Error('lost'), { outcomeUnknown: true }); };
  const generic = () => { throw Object.assign(Error('no route'), { status: 404, code: 'not_found' }); };
  const missing = () => { throw Object.assign(Error('not accepted'), { status: 404, code: 'command_not_accepted' }); };
  const slot = await creationSlot({ name: 'synthetic-rule' });
  assert(!slot.includes('synthetic-rule'));
  await assert.rejects(tracker.submit(slot, send, generic), /lost/);
  const errors = [];
  await tracker.reconcile(missing, assert.fail, (_slot, id, e) => errors.push([id, e.code]), () => true);
  await assert.rejects(tracker.submit(slot, send, generic), /no route/);
  assert.deepEqual(sent, ['original']);
  assert.deepEqual(errors, [['original', 'command_not_accepted']]);
  await tracker.submit(slot, id => { sent.push(id); return { state: 'accepted' }; }, missing);
  assert.deepEqual(sent, ['original', 'original']);
});

function isolatedStorage() {
  const values = new Map();
  return {
    get length() { return values.size; },
    key: index => [...values.keys()][index] ?? null,
    getItem: key => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: key => values.delete(key),
  };
}

test('browser identity observes cross-tab changes without erasing a newly rotated or explicitly cleared key', async () => {
  const { BrowserAuthorization } = await import(ui + 'workspace.js');
  const store = isolatedStorage();
  const first = new BrowserAuthorization('/fixture', () => store);
  const second = new BrowserAuthorization('/fixture', () => store);
  const access = first.open('first-fake', false);
  assert.equal(first.savedKey(), '');
  first.verified(access);
  const sibling = second.open(second.savedKey(), true);
  assert.equal(first.changed(access, assert.fail), false);
  assert(second.remember(sibling, 'second-fake'));
  const changed = [];
  assert(first.changed(access, value => changed.push(value)));
  assert(first.forget(access.key));
  assert.equal(first.savedKey(), 'second-fake');
  second.forget(sibling.key);
  assert(first.changed(access, value => changed.push(value)));
  assert.deepEqual(changed, ['second-fake', '']);
  const events = [];
  first.storageEvent(first.revisionName, assert.fail, () => events.push('read'));
  first.storageEvent(first.keyName, () => events.push('key'), assert.fail);
  first.storageEvent('unrelated', assert.fail, assert.fail);
  assert.deepEqual(events, ['read','key']);
});

test('setup planning reads only, delays only a confirmed pre-write failure and never replays an unknown result', async () => {
  const { BrowserAuthorization } = await import(ui + 'workspace.js');
  const store = isolatedStorage();
  let now = 100;
  const newSession = () => new BrowserAuthorization('/fixture', () => store, () => now, () => 0);
  const auth = newSession(), access = auth.open('fake', true);
  const pristine = { state: 'unconfigured', configured: false, can_migrate: true, revision: 5 };
  const setup = auth.setupPlan(access, pristine, true).command;
  assert.deepEqual(setup, { mode: 'migrate', revision: 5 });
  assert.equal(store.length, 0, 'Planning does not configure CPA or update browser identity');
  auth.rememberIntent({ attempted: true });
  assert.equal(auth.retrySetup(setup, { source:'backend_management', code:'management_network', outcomeUnknown:true }), 0);
  assert.deepEqual(newSession().setupPlan(access, pristine, true), {});
  const deadline = auth.retrySetup(setup, { source:'backend_management', code:'management_rate_limited', retryAfterMs:65000 });
  assert.equal(deadline, now + 65000);
  assert.deepEqual(newSession().setupPlan(access, pristine, true), { retryAt:deadline });
  now = deadline;
  assert.equal(newSession().setupPlan(access, pristine, false).command.mode, 'migrate');
  assert.deepEqual(newSession().setupPlan(access, { ...pristine, revision:6, can_migrate:false }, true), {});
});

test('independent tabs retain both pending commands and removing one cannot overwrite the other', async () => {
  const { CommandReceipts } = await import(ui + 'commands.js');
  const shared = isolatedStorage();
  const tab = id => {
    const session = isolatedStorage();
    return new CommandReceipts('fake-ops', () => id, { shared: () => shared, session: () => session });
  };
  const a = tab('one'), b = tab('two');
  await Promise.all([
    a.submit('inspect', id => ({ id }), assert.fail),
    b.submit('conversion:opaque:0', id => ({ id }), assert.fail),
  ]);
  assert.deepEqual([...tab('fresh').records()].sort(), [['one', 'inspect'], ['two', 'conversion:opaque:0']]);
  a.forget('inspect', 'one');
  assert.deepEqual([...b.records()], [['two', 'conversion:opaque:0']]);
  assert.deepEqual([...tab('reopened').records()], [['two', 'conversion:opaque:0']]);
});

test('legacy session-only command references migrate without sending or replacing other tabs’ records', async () => {
  const { CommandReceipts } = await import(ui + 'commands.js');
  const shared = isolatedStorage(), session = isolatedStorage();
  session.setItem('fake-ops', JSON.stringify([['inspect','legacy']]));
  shared.setItem('fake-ops:pending:other','run:test');
  const receipts = new CommandReceipts('fake-ops', assert.fail, { shared: () => shared, session: () => session });
  await receipts.reconcile(assert.fail, assert.fail, assert.fail, () => false);
  assert(session.getItem('fake-ops'), 'Unverified workspaces must not even migrate local operation references');
  assert.deepEqual([...receipts.records()].sort(), [['legacy','inspect'],['other','run:test']]);
  assert.equal(session.getItem('fake-ops'), null);
  assert.equal(shared.getItem('fake-ops:pending:legacy'), 'inspect');
});
