// Transport-agnostic request handler shared by the local server (server.js)
// and the Azure Functions entry point (src/functions/router.js).
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');

const PUBLIC_DIR = path.join(__dirname, '..', 'public');
const SLUG_RE = /^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const OUTBOX_NAME_RE = /^[A-Za-z0-9][\w.-]*\.(zip|eml)$/;

function safeEqual(a, b) {
  const x = Buffer.from(String(a));
  const y = Buffer.from(String(b));
  return x.length === y.length && crypto.timingSafeEqual(x, y);
}

function validateRequest(body) {
  const errors = [];
  const m = body.manifest || {};
  if (!m.name || m.name.length > 34) errors.push('manifest.name is required (max 34 chars)');
  if (!m.url) errors.push('manifest.url (homepage) is required');
  if (!['org', 'user'].includes(body.owner_type)) errors.push("owner_type must be 'org' or 'user'");
  if (body.owner_type === 'org' && !SLUG_RE.test(body.owner || '')) errors.push('owner (org login) is invalid');
  if (!['vault', 'email'].includes(body.secret_storage)) errors.push("secret_storage must be 'vault' or 'email'");
  if (body.secret_storage === 'email' && !EMAIL_RE.test(body.notify_email || ''))
    errors.push('notify_email is required for email delivery');
  return errors;
}

function fakeCreds(row, githubWeb) {
  const { privateKey } = crypto.generateKeyPairSync('rsa', {
    modulusLength: 2048,
    privateKeyEncoding: { type: 'pkcs1', format: 'pem' },
    publicKeyEncoding: { type: 'spki', format: 'pem' },
  });
  const slug = row.manifest.name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
  return {
    simulated: true,
    id: 100000 + crypto.randomInt(900000),
    slug,
    client_id: 'Iv1.' + crypto.randomBytes(8).toString('hex'),
    client_secret: crypto.randomBytes(20).toString('hex'),
    webhook_secret: row.manifest.hook_attributes ? crypto.randomBytes(16).toString('hex') : null,
    pem: privateKey,
    html_url: `${githubWeb}/apps/${slug}`,
  };
}

function createApp({ store, secrets, env = process.env, fetchImpl = (...a) => fetch(...a) }) {
  const INGEST_TOKEN = env.INGEST_TOKEN || 'dev-ingest-token';
  const GITHUB_WEB = (env.GITHUB_WEB_URL || 'https://github.com').replace(/\/$/, '');
  const GITHUB_API = (env.GITHUB_API_URL || 'https://api.github.com').replace(/\/$/, '');
  const GITHUB_TOKEN = env.GITHUB_TOKEN;
  const ALLOW_SIMULATE = env.ALLOW_SIMULATE !== 'false';

  const json = (status, body, headers = {}) => ({
    status,
    headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', ...headers },
    body: JSON.stringify(body),
  });
  const now = () => new Date().toISOString();
  const update = (id, patch) => store.update(id, { ...patch, updated_at: now() });

  function baseUrl(req) {
    if (env.BASE_URL) return env.BASE_URL.replace(/\/$/, '');
    const u = new URL(req.url);
    const proto = req.headers['x-forwarded-proto'] || u.protocol.replace(':', '');
    const host = req.headers['x-forwarded-host'] || req.headers.host || u.host;
    return `${proto}://${host}`;
  }

  // App Service Authentication (Easy Auth) injects the signed-in principal.
  const approver = (req) => req.headers['x-ms-client-principal-name'] || null;

  function githubNewAppUrl(row, state) {
    const base =
      row.owner_type === 'org'
        ? `${GITHUB_WEB}/organizations/${encodeURIComponent(row.owner)}/settings/apps/new`
        : `${GITHUB_WEB}/settings/apps/new`;
    return `${base}?state=${encodeURIComponent(state)}`;
  }

  async function commentOnIssue(row, text) {
    const src = row.source || {};
    if (!GITHUB_TOKEN || src.type !== 'issue' || !src.repo || !src.issue_number) return;
    try {
      await fetchImpl(`${GITHUB_API}/repos/${src.repo}/issues/${src.issue_number}/comments`, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${GITHUB_TOKEN}`,
          Accept: 'application/vnd.github+json',
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ body: text }),
      });
    } catch (e) {
      console.warn('Issue comment failed:', e.message);
    }
  }

  async function finalize(row, creds, who) {
    const delivery =
      row.secret_storage === 'vault'
        ? { method: 'vault', provider: secrets.vaultProvider, ...(await secrets.storeInVault(creds)) }
        : { method: 'email', ...(await secrets.emailZip(creds, row.notify_email)) };
    const updated = await update(row.id, {
      status: 'created',
      state_nonce: null,
      approved_by: who || row.approved_by || null,
      app: { id: creds.id, slug: creds.slug, client_id: creds.client_id, html_url: creds.html_url },
      delivery,
      simulated: !!creds.simulated,
    });
    await commentOnIssue(
      updated,
      `✅ GitHub App **${creds.slug}** (ID ${creds.id}) was created${creds.simulated ? ' (simulated)' : ''}.\n` +
        `Credentials delivered via **${delivery.method}**${delivery.method === 'email' ? ` to ${row.notify_email}` : ''}.`
    );
    return updated;
  }

  function parseBody(raw) {
    if (!raw) return {};
    try {
      return JSON.parse(raw);
    } catch {
      const e = new Error('Invalid JSON');
      e.status = 400;
      throw e;
    }
  }

  return async function handle(req) {
    const url = new URL(req.url);
    const p = url.pathname.replace(/\/+$/, '') || '/';
    const method = req.method.toUpperCase();
    try {
      if (method === 'GET' && (p === '/' || p === '/index.html')) {
        return {
          status: 200,
          headers: { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' },
          body: fs.readFileSync(path.join(PUBLIC_DIR, 'index.html'), 'utf8'),
        };
      }

      if (method === 'GET' && p === '/api/health') return json(200, { ok: true, store: store.kind });

      if (method === 'GET' && p === '/api/config') {
        return json(200, {
          vault_provider: secrets.vaultProvider,
          outbox: secrets.outbox.kind,
          store: store.kind,
          github_web: GITHUB_WEB,
          issue_comments: !!GITHUB_TOKEN,
          allow_simulate: ALLOW_SIMULATE,
          user: approver(req),
        });
      }

      // Machine-to-machine ingest (IssueOps Action today, ServiceNow later). Excluded from Easy Auth.
      if (method === 'POST' && p === '/api/ingest') {
        if (!safeEqual(req.headers.authorization || '', `Bearer ${INGEST_TOKEN}`)) return json(401, { error: 'Unauthorized' });
        const body = parseBody(req.body);
        const errors = validateRequest(body);
        if (errors.length) return json(400, { errors });

        const dupe = (await store.list()).find(
          (r) => r.status !== 'rejected' && r.manifest.name.toLowerCase() === body.manifest.name.toLowerCase()
        );
        if (dupe) return json(409, { error: `Request for '${body.manifest.name}' already exists`, id: dupe.id });

        const row = {
          id: crypto.randomUUID(),
          status: 'pending',
          owner_type: body.owner_type,
          owner: body.owner_type === 'org' ? body.owner : null,
          secret_storage: body.secret_storage,
          notify_email: body.notify_email || null,
          requester: body.requester || null,
          source: body.source || { type: 'api' },
          manifest: body.manifest,
          created_at: now(),
          updated_at: now(),
        };
        await store.insert(row);
        console.log(`Queued request ${row.id} for app '${row.manifest.name}'`);
        return json(201, { id: row.id, status: row.status, review_url: `${baseUrl(req)}/#${row.id}` });
      }

      if (method === 'GET' && p === '/api/requests') {
        const rows = (await store.list()).map(({ state_nonce, ...r }) => r).reverse();
        return json(200, rows);
      }

      const m = p.match(/^\/api\/requests\/([\w-]+)\/(start|simulate|reject|reset)$/);
      if (method === 'POST' && m) {
        const [, id, action] = m;
        const row = await store.get(id);
        if (!row) return json(404, { error: 'Request not found' });
        const body = parseBody(req.body);
        const who = approver(req);

        if (action === 'reject')
          return json(200, await update(id, { status: 'rejected', reject_reason: body.reason || null, approved_by: who }));
        if (action === 'reset') return json(200, await update(id, { status: 'pending', state_nonce: null }));
        if (row.status === 'created' || row.status === 'rejected') return json(409, { error: `Request already ${row.status}` });
        if (action === 'simulate' && !ALLOW_SIMULATE) return json(403, { error: 'Simulation disabled' });

        // Approver can override the delivery method at click time.
        const storage = body.secret_storage || row.secret_storage;
        const email = body.notify_email || row.notify_email;
        if (!['vault', 'email'].includes(storage)) return json(400, { error: 'Invalid secret_storage' });
        if (storage === 'email' && !EMAIL_RE.test(email || '')) return json(400, { error: 'Valid email required' });
        Object.assign(row, await update(id, { secret_storage: storage, notify_email: email || null }));

        if (action === 'simulate') return json(200, await finalize(row, fakeCreds(row, GITHUB_WEB), who));

        const nonce = crypto.randomBytes(16).toString('hex');
        await update(id, { status: 'awaiting_github', state_nonce: nonce, approved_by: who });
        const manifest = { ...row.manifest, redirect_url: `${baseUrl(req)}/callback` };
        return json(200, { action_url: githubNewAppUrl(row, `${id}.${nonce}`), manifest });
      }

      // GitHub App Manifest flow redirect target.
      if (method === 'GET' && p === '/callback') {
        const code = url.searchParams.get('code');
        const [id, nonce] = String(url.searchParams.get('state') || '').split('.');
        const row = id ? await store.get(id) : null;
        if (!code || !row || !row.state_nonce || !safeEqual(row.state_nonce, nonce || ''))
          return json(400, { error: 'Invalid or expired state' });

        const r = await fetchImpl(`${GITHUB_API}/app-manifests/${encodeURIComponent(code)}/conversions`, {
          method: 'POST',
          headers: { Accept: 'application/vnd.github+json' },
        });
        if (!r.ok) {
          const msg = await r.text();
          await update(id, { status: 'failed', error: msg, state_nonce: null });
          return json(502, { error: 'Manifest conversion failed', detail: msg });
        }
        await finalize(row, await r.json(), approver(req));
        return { status: 302, headers: { Location: `/#done-${id}` }, body: '' };
      }

      const ob = p.match(/^\/outbox\/([^/]+)$/);
      if (method === 'GET' && ob) {
        if (!OUTBOX_NAME_RE.test(ob[1])) return json(400, { error: 'Invalid name' });
        const buf = await secrets.outbox.get(ob[1]);
        if (!buf) return json(404, { error: 'Not found' });
        return {
          status: 200,
          headers: {
            'Content-Type': ob[1].endsWith('.zip') ? 'application/zip' : 'message/rfc822',
            'Content-Disposition': `attachment; filename="${ob[1]}"`,
            'Cache-Control': 'no-store',
          },
          body: buf,
        };
      }

      return json(404, { error: 'Not found' });
    } catch (e) {
      if (e.status) return json(e.status, { error: e.message });
      console.error(e);
      return json(500, { error: 'Internal error' });
    }
  };
}

module.exports = { createApp, validateRequest };
