// Local dev server. In Azure the same handler runs via src/functions/router.js.
const http = require('http');
const { createApp } = require('./src/app');
const { createStore } = require('./src/store');
const { createSecrets } = require('./src/secrets');

const PORT = Number(process.env.PORT || 3000);
const store = createStore();
const secrets = createSecrets();
const handle = createApp({ store, secrets });

http
  .createServer((req, res) => {
    const chunks = [];
    let size = 0;
    req.on('data', (c) => {
      size += c.length;
      if (size > 1e6) req.destroy();
      else chunks.push(c);
    });
    req.on('end', async () => {
      const out = await handle({
        method: req.method,
        url: `http://${req.headers.host || `localhost:${PORT}`}${req.url}`,
        headers: req.headers,
        body: Buffer.concat(chunks).toString('utf8'),
      });
      res.writeHead(out.status, out.headers);
      res.end(out.body);
    });
  })
  .listen(PORT, () => {
    console.log(`GitHub App Factory running at http://localhost:${PORT}`);
    console.log(`  Store: ${store.kind}  |  Vault: ${secrets.vaultProvider}  |  Outbox: ${secrets.outbox.kind}`);
  });
