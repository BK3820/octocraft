// Secret delivery backends.
//   Vault:  HashiCorp Vault (VAULT_ADDR + VAULT_TOKEN) → Azure Key Vault (KEY_VAULT_URL) → local mock file.
//   Email:  ZIP + .eml written to an "outbox" (Azure Blob container when STORAGE_ACCOUNT_NAME is set, else local dir).
const fs = require('fs');
const path = require('path');
const { createZip } = require('./zip');

function credential() {
  const { DefaultAzureCredential } = require('@azure/identity');
  return new DefaultAzureCredential();
}

function credsPayload(creds) {
  return {
    app_id: String(creds.id),
    slug: creds.slug,
    client_id: creds.client_id,
    client_secret: creds.client_secret,
    webhook_secret: creds.webhook_secret || '',
    private_key: creds.pem,
  };
}

function hashicorpVault(env) {
  const mount = env.VAULT_KV_MOUNT || 'secret';
  const prefix = env.VAULT_PATH_PREFIX || 'github-apps';
  return {
    provider: 'hashicorp',
    async store(creds) {
      const secretPath = `${prefix}/${creds.slug}`;
      const res = await fetch(`${env.VAULT_ADDR}/v1/${mount}/data/${secretPath}`, {
        method: 'POST',
        headers: { 'X-Vault-Token': env.VAULT_TOKEN, 'Content-Type': 'application/json' },
        body: JSON.stringify({ data: credsPayload(creds) }),
      });
      if (!res.ok) throw new Error(`Vault write failed: ${res.status} ${await res.text()}`);
      const body = await res.json();
      return { location: `${env.VAULT_ADDR} → ${mount}/${secretPath}`, version: body.data?.version };
    },
  };
}

function azureKeyVault(env) {
  const { SecretClient } = require('@azure/keyvault-secrets');
  const client = new SecretClient(env.KEY_VAULT_URL, credential());
  return {
    provider: 'keyvault',
    async store(creds) {
      const base = `ghapp-${creds.slug}`.replace(/[^A-Za-z0-9-]/g, '-').slice(0, 100);
      const tags = { app_id: String(creds.id), client_id: creds.client_id, slug: creds.slug };
      const p = credsPayload(creds);
      const items = {
        'private-key': p.private_key,
        'client-secret': p.client_secret,
        ...(p.webhook_secret ? { 'webhook-secret': p.webhook_secret } : {}),
      };
      const names = [];
      for (const [suffix, value] of Object.entries(items)) {
        await client.setSecret(`${base}-${suffix}`, value, { tags, contentType: suffix === 'private-key' ? 'application/x-pem-file' : 'text/plain' });
        names.push(`${base}-${suffix}`);
      }
      return { location: `${env.KEY_VAULT_URL.replace(/\/$/, '')} → ${names.join(', ')}` };
    },
  };
}

function mockVault(dir) {
  return {
    provider: 'mock',
    async store(creds) {
      fs.mkdirSync(dir, { recursive: true });
      const file = path.join(dir, 'mock-vault.json');
      const vault = fs.existsSync(file) ? JSON.parse(fs.readFileSync(file, 'utf8')) : {};
      vault[`secret/github-apps/${creds.slug}`] = { ...credsPayload(creds), written_at: new Date().toISOString() };
      fs.writeFileSync(file, JSON.stringify(vault, null, 2));
      return { location: `mock-vault → secret/github-apps/${creds.slug}` };
    },
  };
}

function localOutbox(dir) {
  return {
    kind: 'local',
    async put(name, buf) {
      fs.mkdirSync(dir, { recursive: true });
      fs.writeFileSync(path.join(dir, name), buf);
    },
    async get(name) {
      const f = path.join(dir, name);
      return fs.existsSync(f) ? fs.readFileSync(f) : null;
    },
  };
}

function blobOutbox(account, containerName) {
  const { BlobServiceClient } = require('@azure/storage-blob');
  const container = new BlobServiceClient(`https://${account}.blob.core.windows.net`, credential()).getContainerClient(containerName);
  let ready;
  const ensure = () => (ready ||= container.createIfNotExists());
  return {
    kind: 'azure-blob',
    async put(name, buf) {
      await ensure();
      await container.getBlockBlobClient(name).uploadData(buf);
    },
    async get(name) {
      await ensure();
      try {
        return await container.getBlobClient(name).downloadToBuffer();
      } catch (e) {
        if (e.statusCode === 404) return null;
        throw e;
      }
    },
  };
}

function buildZip(creds) {
  const meta = {
    app_id: creds.id,
    slug: creds.slug,
    client_id: creds.client_id,
    client_secret: creds.client_secret,
    webhook_secret: creds.webhook_secret || null,
    html_url: creds.html_url,
  };
  return createZip([
    { name: `${creds.slug}/app-credentials.json`, content: JSON.stringify(meta, null, 2) },
    { name: `${creds.slug}/private-key.pem`, content: creds.pem },
    {
      name: `${creds.slug}/README.txt`,
      content: `GitHub App: ${creds.slug}\nApp ID: ${creds.id}\n\nStore these credentials in a secret manager and delete this archive.\n`,
    },
  ]);
}

function buildEml(slug, to, zipBuf, stamp) {
  const boundary = `----gaf${stamp}`;
  return [
    `To: ${to}`,
    'From: github-app-factory@localhost',
    `Subject: Credentials for GitHub App ${slug}`,
    'MIME-Version: 1.0',
    `Content-Type: multipart/mixed; boundary="${boundary}"`,
    '',
    `--${boundary}`,
    'Content-Type: text/plain; charset=utf-8',
    '',
    `Your GitHub App "${slug}" has been created. Credentials are attached.`,
    '',
    `--${boundary}`,
    `Content-Type: application/zip; name="${slug}.zip"`,
    'Content-Transfer-Encoding: base64',
    `Content-Disposition: attachment; filename="${slug}.zip"`,
    '',
    zipBuf.toString('base64').replace(/.{76}/g, '$&\r\n'),
    `--${boundary}--`,
    '',
  ].join('\r\n');
}

function createSecrets(env = process.env) {
  const dataDir = env.DATA_DIR || path.join(__dirname, '..', 'data');
  const vault =
    env.VAULT_ADDR && env.VAULT_TOKEN ? hashicorpVault(env) : env.KEY_VAULT_URL ? azureKeyVault(env) : mockVault(dataDir);
  const outbox = env.STORAGE_ACCOUNT_NAME
    ? blobOutbox(env.STORAGE_ACCOUNT_NAME, env.OUTBOX_CONTAINER || 'outbox')
    : localOutbox(path.join(dataDir, 'outbox'));

  return {
    vaultProvider: vault.provider,
    outbox,
    storeInVault: (creds) => vault.store(creds),
    // POC: no SMTP. The zip + MIME .eml are written to the outbox for download/relay.
    async emailZip(creds, to) {
      const zip = buildZip(creds);
      const stamp = Date.now();
      const zipName = `${creds.slug}-${stamp}.zip`;
      const emlName = `${creds.slug}-${stamp}.eml`;
      await outbox.put(zipName, zip);
      await outbox.put(emlName, Buffer.from(buildEml(creds.slug, to, zip, stamp)));
      return { to, zipName, emlName };
    },
  };
}

module.exports = { createSecrets, buildZip };
