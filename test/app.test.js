const { test } = require('node:test');
const assert = require('node:assert');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');
const { createApp } = require('../src/app');
const { localStore } = require('../src/store');
const { createSecrets } = require('../src/secrets');
const { buildRequest } = require('../scripts/issue-to-request');

function setup(extraEnv = {}) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'gaf-'));
  const env = { DATA_DIR: dir, INGEST_TOKEN: 'tkn', ...extraEnv };
  const handle = createApp({ store: localStore(dir), secrets: createSecrets(env), env, fetchImpl: async () => ({ ok: true }) });
  const call = (method, p, body, headers = {}) =>
    handle({ method, url: `http://localhost${p}`, headers, body: body ? JSON.stringify(body) : '' }).then((r) => ({
      ...r,
      json: typeof r.body === 'string' && r.body.startsWith('{') || r.body?.[0] === '[' ? JSON.parse(r.body) : null,
    }));
  return { dir, call };
}

const ISSUE_BODY = `### App name

demo-bot

### Description

Demo

### Owner type

org

### Organization

my-org

### Homepage URL

https://example.com

### Webhook URL

_No response_

### Permissions

contents: read
issues: write
bogus line

### Webhook events

- [X] push
- [ ] issues

### Secret delivery

email

### Delivery email

a@example.com`;

const issuePayload = () =>
  buildRequest({ body: ISSUE_BODY, number: 7, html_url: 'https://github.com/o/r/issues/7', user: { login: 'u' } }, 'o/r');

test('issue form body is parsed into a manifest request', () => {
  const r = issuePayload();
  assert.equal(r.manifest.name, 'demo-bot');
  assert.deepEqual(r.manifest.default_permissions, { contents: 'read', issues: 'write' });
  assert.deepEqual(r.manifest.default_events, []); // no webhook → no events
  assert.equal(r.manifest.hook_attributes, undefined);
  assert.equal(r.secret_storage, 'email');
  assert.equal(r.source.issue_number, 7);
});

test('ingest requires the bearer token', async () => {
  const { call } = setup();
  assert.equal((await call('POST', '/api/ingest', issuePayload())).status, 401);
  assert.equal((await call('POST', '/api/ingest', issuePayload(), { authorization: 'Bearer nope' })).status, 401);
});

test('ingest → list → simulate (email) produces a valid zip in the outbox', async () => {
  const { dir, call } = setup();
  const auth = { authorization: 'Bearer tkn' };
  const created = await call('POST', '/api/ingest', issuePayload(), auth);
  assert.equal(created.status, 201);
  assert.equal((await call('POST', '/api/ingest', issuePayload(), auth)).status, 409);

  const list = await call('GET', '/api/requests');
  assert.equal(list.json.length, 1);
  assert.equal(list.json[0].status, 'pending');

  const sim = await call('POST', `/api/requests/${created.json.id}/simulate`, {});
  assert.equal(sim.status, 200);
  assert.equal(sim.json.status, 'created');
  assert.equal(sim.json.delivery.method, 'email');

  const zip = path.join(dir, 'outbox', sim.json.delivery.zipName);
  const listing = execFileSync('unzip', ['-l', zip]).toString();
  assert.match(listing, /demo-bot\/private-key\.pem/);
  const dl = await call('GET', `/outbox/${sim.json.delivery.zipName}`);
  assert.equal(dl.status, 200);
  assert.ok(Buffer.isBuffer(dl.body));
  assert.equal((await call('GET', '/outbox/..%2Frequests.json')).status, 400);

  // No secrets persisted in the request store.
  assert.doesNotMatch(fs.readFileSync(path.join(dir, 'requests.json'), 'utf8'), /PRIVATE KEY|client_secret/);
});

test('start builds a manifest-flow URL and callback rejects bad state', async () => {
  const { call } = setup({ BASE_URL: 'https://factory.example.com' });
  const body = { ...issuePayload(), secret_storage: 'vault', notify_email: undefined };
  const { json } = await call('POST', '/api/ingest', body, { authorization: 'Bearer tkn' });
  const start = await call('POST', `/api/requests/${json.id}/start`, {});
  assert.equal(start.status, 200);
  assert.match(start.json.action_url, /^https:\/\/github\.com\/organizations\/my-org\/settings\/apps\/new\?state=/);
  assert.equal(start.json.manifest.redirect_url, 'https://factory.example.com/callback');
  assert.equal((await call('GET', `/callback?code=x&state=${json.id}.wrong`)).status, 400);
});

test('UI and health are served', async () => {
  const { call } = setup();
  const ui = await call('GET', '/');
  assert.equal(ui.status, 200);
  assert.match(ui.body, /GitHub App Factory/);
  assert.equal((await call('GET', '/api/health')).status, 200);
});
