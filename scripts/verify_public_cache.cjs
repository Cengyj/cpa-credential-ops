// Use a fresh browser profile and only the public assets served by the selected local CPA.
const assert = require('node:assert/strict');
const { createHash } = require('node:crypto');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');
const { gunzipSync } = require('node:zlib');
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || '/Users/ceng/.cache/codex-runtimes/codex-primary-runtime/dependencies/node/node_modules/playwright');

async function visit(context, indexURL, assets) {
  const page = await context.newPage();
  const session = await context.newCDPSession(page);
  await session.send('Network.enable');
  const requests = new Map(), validators = new Map(), networkStatus = new Map(), errors = [];
  let authenticatedRequests = 0;
  page.on('pageerror', error => errors.push(error.message));
  session.on('Network.requestWillBeSent', ({ requestId, request }) => {
    const url = new URL(request.url);
    if (url.origin === indexURL.origin) {
      assert.equal(request.method, 'GET');
      const name = url.pathname.slice(indexURL.pathname.lastIndexOf('/') + 1);
      if (url.pathname === new URL(name, indexURL).pathname && Object.hasOwn(assets, name)) requests.set(name, requestId);
    }
  });
  session.on('Network.requestWillBeSentExtraInfo', ({ requestId, headers }) => {
    const fields = Object.entries(headers);
    if (fields.some(([key]) => ['authorization', 'x-management-key', 'x-api-key'].includes(key.toLowerCase()))) authenticatedRequests++;
    validators.set(requestId, fields.find(([key]) => key.toLowerCase() === 'if-none-match')?.[1] || null);
  });
  // The browser exposes a revalidated response as 200 to the document, while ExtraInfo reports HTTP 304 on the wire.
  session.on('Network.responseReceivedExtraInfo', ({ requestId, statusCode }) => networkStatus.set(requestId, statusCode));
  try {
    await page.goto(indexURL.href);
    await page.waitForLoadState('networkidle');
    assert.deepEqual(errors, []);
    const version = await page.locator('#build-info').evaluate(node => JSON.parse(node.textContent).ui_version);
    assert.deepEqual([...requests.keys()].sort(), Object.keys(assets).sort());
    const records = Object.fromEntries([...requests].map(([name, id]) => [name, {
      networkStatus: networkStatus.get(id), validator: validators.get(id),
    }]));
    assert.equal(authenticatedRequests, 0);
    return { version, records, authenticatedRequests };
  } finally {
    await page.close();
  }
}

function get(url, headers) {
  return new Promise((resolve, reject) => {
    const req = http.get(url, { headers, timeout: 10000 }, res => {
      const chunks = [];
      res.on('data', chunk => chunks.push(chunk));
      res.on('end', () => resolve({ status: res.statusCode, headers: res.headers, body: Buffer.concat(chunks) }));
      res.on('error', reject);
    });
    req.on('timeout', () => req.destroy(new Error('Public resource read timed out')));
    req.on('error', reject);
  });
}

async function main() {
  const [origin, currentFile, priorFile, reportFile] = process.argv.slice(2);
  const indexURL = new URL('/v0/resource/plugins/model-quality/index.html', origin);
  assert.equal(indexURL.protocol, 'http:');
  assert.equal(indexURL.hostname, '127.0.0.1');
  assert.equal(indexURL.username + indexURL.password, '');
  const current = JSON.parse(await fs.readFile(currentFile)), prior = JSON.parse(await fs.readFile(priorFile));
  const assets = current.ui_source_sha256;
  const browser = await chromium.launch({ executablePath: process.env.CHROME_PATH || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' });
  try {
    const context = await browser.newContext();
    const first = await visit(context, indexURL, assets);
    const reopened = await visit(context, indexURL, assets);
    assert.equal(first.version, current.ui_version);
    assert.equal(reopened.version, current.ui_version);
    const previousDigests = {};
    for (const [name, digest] of Object.entries(assets)) {
      const tag = `W/"${digest}"`;
      assert.deepEqual(first.records[name], { networkStatus: 200, validator: null }, name);
      assert.deepEqual(reopened.records[name], { networkStatus: 304, validator: tag }, name);
      const previous = prior.ui_source_sha256[name], changed = previous !== digest;
      const response = await get(new URL(name, indexURL), { 'If-None-Match': `W/"${previous}"` });
      assert.equal(response.status, changed ? 200 : 304, name);
      assert.equal(response.headers.etag, tag);
      if (changed) assert.equal(createHash('sha256').update(response.body).digest('hex'), digest, name);
      else assert.equal(response.body.length, 0, name);
      previousDigests[name] = { changed, networkStatus: response.status, bytes: response.body.length };
    }
    assert(Object.values(previousDigests).some(item => item.changed), 'The comparison requires a changed resource');
    const plain = await get(indexURL, { 'Accept-Encoding': 'identity' });
    const compressed = await get(indexURL, { 'Accept-Encoding': 'gzip' });
    assert.equal(plain.status, 200);
    assert.equal(compressed.status, 200);
    assert.equal(compressed.headers['content-encoding'], 'gzip');
    assert.deepEqual(gunzipSync(compressed.body), plain.body);
    for (const reply of [plain, compressed]) {
      assert.equal(reply.headers.etag, `W/"${assets['index.html']}"`);
      assert.match(reply.headers.vary, /\bAccept-Encoding\b/i);
    }
    const report = { ui: current.ui_version, priorUI: prior.ui_version, first, reopened, previousDigests,
      compression: { identityBytes: plain.body.length, gzipBytes: compressed.body.length, validator: compressed.headers.etag },
      authenticatedRequests: first.authenticatedRequests + reopened.authenticatedRequests };
    await fs.mkdir(path.dirname(reportFile), { recursive: true });
    await fs.writeFile(reportFile, JSON.stringify(report, null, 2) + '\n');
    console.log('Verified 6 cold browser responses, 6 conditional reopens, changed assets, and gzip negotiation.');
  } finally {
    await browser.close();
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
